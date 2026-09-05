// Package run owns lifecycle rules, not test execution or analysis verdicts.
package run

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
)

var (
	// ErrValidation identifies caller input that cannot satisfy a domain contract.
	ErrValidation = errors.New("validation failed")
	// ErrNotFound includes resources that are absent or invisible to the principal.
	ErrNotFound = errors.New("run not found")
	// ErrConflict identifies an idempotency or immutable-identity conflict.
	ErrConflict = errors.New("idempotency conflict")
	// ErrTerminal identifies an operation that cannot mutate a terminal Run.
	ErrTerminal = errors.New("run is terminal")
	// ErrTransition identifies a lifecycle edge absent from the pinned contract.
	ErrTransition = errors.New("invalid transition")
	// ErrRevision identifies a mutation based on an obsolete Run revision.
	ErrRevision = errors.New("stale revision")
	// ErrUnavailable classifies a transient or outcome-uncertain dependency failure.
	ErrUnavailable = errors.New("temporarily unavailable")
	// ErrForbidden identifies an authenticated principal that lacks authorization.
	ErrForbidden = errors.New("forbidden")
)

// State identifies a persisted lifecycle state from the run-management contract.
type State string

const (
	// StateCreated has been durably accepted but not yet validated.
	StateCreated State = "CREATED"
	// StateValidating resolves and approves the immutable execution inputs.
	StateValidating State = "VALIDATING"
	// StateProvisioning creates or recovers the external execution identity.
	StateProvisioning State = "PROVISIONING"
	// StateWarmingUp represents an execution preparing its measurement window.
	StateWarmingUp State = "WARMING_UP"
	// StateRunning represents an active measurement window.
	StateRunning State = "RUNNING"
	// StateCollecting verifies and registers raw evidence.
	StateCollecting State = "COLLECTING"
	// StateAnalyzing normalizes collected evidence.
	StateAnalyzing State = "ANALYZING"
	// StateReporting produces and verifies the final analysis report.
	StateReporting State = "REPORTING"
	// StateCancelling waits for the exact external execution to stop.
	StateCancelling State = "CANCELLING"
	// StateCompleted is a successful terminal pipeline outcome.
	StateCompleted State = "COMPLETED"
	// StateInvalid is terminal because approved execution inputs were invalid.
	StateInvalid State = "INVALID"
	// StateAborted is terminal after requested cancellation completes.
	StateAborted State = "ABORTED"
	// StateInfrastructureFailure is terminal after a platform failure.
	StateInfrastructureFailure State = "INFRASTRUCTURE_FAILURE"
	// StateTestFailure is terminal after the test tool fails without usable evidence.
	StateTestFailure State = "TEST_FAILURE"
)

// FailureCode identifies the safe failure category persisted with a failed Run.
type FailureCode string

const (
	// FailureCodeValidationFailed belongs only to StateInvalid.
	FailureCodeValidationFailed FailureCode = "VALIDATION_FAILED"
	// FailureCodeInfrastructureError identifies an infrastructure dependency failure.
	FailureCodeInfrastructureError FailureCode = "INFRASTRUCTURE_ERROR"
	// FailureCodePipelineTimeout identifies an exhausted orchestration deadline.
	FailureCodePipelineTimeout FailureCode = "PIPELINE_TIMEOUT"
	// FailureCodeToolError identifies a test-process failure without usable evidence.
	FailureCodeToolError FailureCode = "TOOL_ERROR"
	// FailureCodeAnalysisError identifies a normalization or reporting failure.
	FailureCodeAnalysisError FailureCode = "ANALYSIS_ERROR"
)

const maxRunRevision int64 = 9007199254740991

func (state State) acceptsFailure(code FailureCode) bool {
	switch state {
	case StateInvalid:
		return code == FailureCodeValidationFailed
	case StateTestFailure:
		return code == FailureCodeToolError
	case StateInfrastructureFailure:
		return code == FailureCodeInfrastructureError ||
			code == FailureCodePipelineTimeout ||
			code == FailureCodeAnalysisError
	default:
		return false
	}
}

func (state State) requiresFailure() bool {
	return state == StateInvalid ||
		state == StateTestFailure ||
		state == StateInfrastructureFailure
}

// Reference pins one approved resource version and its exact published bytes.
type Reference struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
}

// Candidate identifies the immutable software revision and image under test.
type Candidate struct {
	GitSHA string `json:"gitSha"`
	Image  string `json:"image"`
}

// Request contains only comparable values; equality is parsed-object equality.
type Request struct {
	TestSuite   string    `json:"testSuite"`
	Catalogue   Reference `json:"catalogue"`
	Profile     string    `json:"profile"`
	Candidate   Candidate `json:"candidate"`
	Environment Reference `json:"environment"`
	Policy      Reference `json:"policy"`
}

// Failure is a safe terminal classification; Message must never contain raw logs.
type Failure struct {
	Code    FailureCode `json:"code"`
	Message string      `json:"message"`
}

// Run is the current revisioned snapshot of one accepted execution request.
type Run struct {
	ID           string     `json:"id"`
	State        State      `json:"state"`
	Revision     int64      `json:"revision"`
	Request      Request    `json:"request"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
	FinishedAt   *time.Time `json:"finishedAt,omitempty"`
	ToolExitCode *int       `json:"toolExitCode,omitempty"`
	Failure      *Failure   `json:"failure,omitempty"`
}

// Clone prevents callers from mutating stored snapshots through pointers.
func (r Run) Clone() Run {
	if r.FinishedAt != nil {
		v := *r.FinishedAt
		r.FinishedAt = &v
	}
	if r.ToolExitCode != nil {
		v := *r.ToolExitCode
		r.ToolExitCode = &v
	}
	if r.Failure != nil {
		v := *r.Failure
		r.Failure = &v
	}

	return r
}

// Validate checks Request against the embedded, pinned CreateRun contract.
func (r Request) Validate() error {
	b, err := json.Marshal(r)
	if err != nil {
		return ErrValidation
	}
	var object any
	if json.Unmarshal(b, &object) != nil || contract.ValidateCreate(object) != nil {
		return ErrValidation
	}

	return nil
}

var keyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{15,127}$`)

// ValidKey reports whether key can identify an idempotent create operation.
func ValidKey(key string) bool { return keyPattern.MatchString(key) }

// Change is worker-only. A caller must supply the observed revision, a safe
// failure message (never raw logs), and an unambiguous observed process exit.
type Change struct {
	State        State
	Failure      *Failure
	ToolExitCode *int
}

// Transition applies one pinned lifecycle edge when expected matches Revision.
// It never mutates the receiver and clamps timestamps against backward clock movement.
func (r Run) Transition(expected int64, change Change, now time.Time) (Run, error) {
	if r.Revision != expected {
		return Run{}, ErrRevision
	}
	if r.Revision >= maxRunRevision ||
		!contract.CanTransition(string(r.State), string(change.State)) {
		return Run{}, ErrTransition
	}
	if change.ToolExitCode != nil && (*change.ToolExitCode < 0 || *change.ToolExitCode > 255) {
		return Run{}, ErrValidation
	}
	if f := change.Failure; f != nil {
		if !utf8.ValidString(f.Message) || utf8.RuneCountInString(f.Message) < 1 || utf8.RuneCountInString(f.Message) > 1000 {
			return Run{}, ErrValidation
		}
		if !change.State.acceptsFailure(f.Code) {
			return Run{}, ErrValidation
		}
	}
	if change.State.requiresFailure() && change.Failure == nil {
		return Run{}, ErrValidation
	}
	next := r.Clone()
	next.State = change.State
	next.Revision++
	next.UpdatedAt = now.UTC()
	// Wall-clock adjustments must not make revision timestamps move backwards.
	if next.UpdatedAt.Before(r.UpdatedAt) {
		next.UpdatedAt = r.UpdatedAt
	}
	next.Failure = change.Failure
	if change.ToolExitCode != nil {
		next.ToolExitCode = change.ToolExitCode
	}
	if contract.Terminal(string(next.State)) {
		finished := next.UpdatedAt
		next.FinishedAt = &finished
	}

	return next.Clone(), nil
}

// Accepted contains the immutable create response and idempotency-binding expiry.
type Accepted struct {
	Run       Run
	ExpiresAt time.Time
}

// Repository methods are atomic. Accept binds principal + key to an immutable
// original snapshot for >=24h. Replays never reset the current Run. A durable
// adapter must commit the run and binding in one transaction before returning.
// Cancel and Advance serialize against each other; Advance uses revision CAS.
// All returned values must be isolated copies. NotFound includes invisible runs.
type Repository interface {
	// Accept atomically creates a Run and binds principal and key for at least 24
	// hours. A live replay with the same Request returns the original Accepted
	// value; a different Request returns ErrConflict.
	Accept(ctx context.Context, principal, key string, request Request) (Accepted, error)

	// Get returns the current snapshot visible to principal. ErrNotFound includes
	// absent and cross-principal Runs.
	Get(ctx context.Context, principal, id string) (Run, error)

	// Cancel moves a nonterminal Run toward CANCELLING. Repeated cancellation of
	// CANCELLING or ABORTED is a no-op; other terminal states return ErrTerminal.
	Cancel(ctx context.Context, principal, id string) (Run, error)

	// Advance serializes a worker-only lifecycle mutation and applies Change
	// against expectedRevision, returning the same errors as Run.Transition.
	Advance(ctx context.Context, principal, id string, expectedRevision int64, change Change) (Run, error)
}
