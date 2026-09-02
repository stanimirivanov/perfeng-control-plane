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
	ErrValidation  = errors.New("validation failed")
	ErrNotFound    = errors.New("run not found")
	ErrConflict    = errors.New("idempotency conflict")
	ErrTerminal    = errors.New("run is terminal")
	ErrTransition  = errors.New("invalid transition")
	ErrRevision    = errors.New("stale revision")
	ErrUnavailable = errors.New("temporarily unavailable")
	ErrForbidden   = errors.New("forbidden")
)

type Reference struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
}
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
type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
type Run struct {
	ID           string     `json:"id"`
	State        string     `json:"state"`
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

func ValidKey(key string) bool { return keyPattern.MatchString(key) }

// Change is worker-only. A caller must supply the observed revision, a safe
// failure message (never raw logs), and an unambiguous observed process exit.
type Change struct {
	State        string
	Failure      *Failure
	ToolExitCode *int
}

func (r Run) Transition(expected int64, change Change, now time.Time) (Run, error) {
	if r.Revision != expected {
		return Run{}, ErrRevision
	}
	if r.Revision >= 9007199254740991 || !contract.CanTransition(r.State, change.State) {
		return Run{}, ErrTransition
	}
	if change.ToolExitCode != nil && (*change.ToolExitCode < 0 || *change.ToolExitCode > 255) {
		return Run{}, ErrValidation
	}
	validFailure := false
	if f := change.Failure; f != nil {
		if !utf8.ValidString(f.Message) || utf8.RuneCountInString(f.Message) < 1 || utf8.RuneCountInString(f.Message) > 1000 {
			return Run{}, ErrValidation
		}
		switch change.State {
		case "INVALID":
			validFailure = f.Code == "VALIDATION_FAILED"
		case "TEST_FAILURE":
			validFailure = f.Code == "TOOL_ERROR"
		case "INFRASTRUCTURE_FAILURE":
			validFailure = f.Code == "INFRASTRUCTURE_ERROR" || f.Code == "PIPELINE_TIMEOUT" || f.Code == "ANALYSIS_ERROR"
		}
		if !validFailure {
			return Run{}, ErrValidation
		}
	}
	needsFailure := change.State == "INVALID" || change.State == "TEST_FAILURE" || change.State == "INFRASTRUCTURE_FAILURE"
	if needsFailure && !validFailure {
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
	if contract.Terminal(next.State) {
		finished := next.UpdatedAt
		next.FinishedAt = &finished
	}
	return next.Clone(), nil
}

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
	Accept(ctx context.Context, principal, key string, request Request) (Accepted, error)
	Get(ctx context.Context, principal, id string) (Run, error)
	Cancel(ctx context.Context, principal, id string) (Run, error)
	Advance(ctx context.Context, principal, id string, expectedRevision int64, change Change) (Run, error)
}
