package nativeworker

import (
	"context"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

// ErrTerminalEvidence means a completion report does not identify the complete
// registered evidence required by its reviewed artifact plan.
var ErrTerminalEvidence = errors.New("native terminal evidence is incomplete or inconsistent")

// ErrTerminalConflict means an accepted attempt already has a different
// durable terminal outcome.
var ErrTerminalConflict = errors.New("native terminal outcome conflicts with the accepted report")

// TerminalKind identifies the outcome reported by one native attempt.
type TerminalKind string

const (
	// TerminalCompleted hands complete registered evidence to collection.
	TerminalCompleted TerminalKind = "COMPLETED"
	// TerminalFailed records a classified execution failure without claiming
	// complete evidence.
	TerminalFailed TerminalKind = "FAILED"
)

// Valid reports whether kind belongs to Native Worker API v1 or v2.
func (kind TerminalKind) Valid() bool {
	return kind == TerminalCompleted || kind == TerminalFailed
}

// TerminalFailureCode is a bounded worker-side failure classification.
type TerminalFailureCode string

const (
	// TerminalFailurePreparation identifies setup failure after an attempt began.
	TerminalFailurePreparation TerminalFailureCode = "PREPARATION_FAILED"
	// TerminalFailureTool identifies an unsuccessful reviewed test process.
	TerminalFailureTool TerminalFailureCode = "TOOL_FAILED"
	// TerminalFailureCapture identifies failure to produce required evidence.
	TerminalFailureCapture TerminalFailureCode = "CAPTURE_FAILED"
	// TerminalFailureUpload identifies failure to publish required evidence.
	TerminalFailureUpload TerminalFailureCode = "UPLOAD_FAILED"
	// TerminalFailureHostUnqualified identifies loss of approved host properties.
	TerminalFailureHostUnqualified TerminalFailureCode = "HOST_BECAME_UNQUALIFIED"
)

// Valid reports whether code belongs to Native Worker API v1 or v2.
func (code TerminalFailureCode) Valid() bool {
	switch code {
	case TerminalFailurePreparation, TerminalFailureTool, TerminalFailureCapture,
		TerminalFailureUpload, TerminalFailureHostUnqualified:
		return true
	default:
		return false
	}
}

// TerminalFailure contains a bounded, human-safe worker explanation and a
// retryability hint. The hint does not override control-plane lifecycle policy.
type TerminalFailure struct {
	Code      TerminalFailureCode `json:"code"`
	Message   string              `json:"message"`
	Retryable bool                `json:"retryable"`
}

// Valid checks the complete Native Worker API failure shape.
func (failure TerminalFailure) Valid() bool {
	return failure.Code.Valid() && utf8.ValidString(failure.Message) &&
		utf8.RuneCountInString(failure.Message) >= 1 &&
		utf8.RuneCountInString(failure.Message) <= 1024
}

// TerminalRequest contains the complete authority and immutable outcome for
// one native attempt. Capability and failure message must not enter diagnostics.
type TerminalRequest struct {
	Kind                TerminalKind
	LeaseID             string
	AttemptID           string
	ExpectedRunRevision int64
	ManifestArtifactID  string
	Failure             *TerminalFailure
	Capability          string
}

// Valid checks terminal-report syntax without establishing current authority
// or registered-evidence completeness.
func (request TerminalRequest) Valid() bool {
	if !request.Kind.Valid() || !ValidLeaseIdentifier(request.LeaseID) ||
		!ValidLeaseIdentifier(request.AttemptID) || request.ExpectedRunRevision < 1 ||
		!ValidLeaseCapability(request.Capability) {
		return false
	}
	if request.Kind == TerminalCompleted {
		return ValidLeaseIdentifier(request.ManifestArtifactID) && request.Failure == nil
	}

	return request.ManifestArtifactID == "" && request.Failure != nil && request.Failure.Valid()
}

// String identifies a terminal report without exposing its capability or
// failure message.
func (request TerminalRequest) String() string {
	return fmt.Sprintf(
		"native terminal report %s attempt %s kind %s",
		request.LeaseID,
		request.AttemptID,
		request.Kind,
	)
}

// GoString identifies a terminal report in diagnostic formatting without
// exposing its capability or failure message.
func (request TerminalRequest) GoString() string {
	return request.String()
}

// TerminalDisposition distinguishes the first accepted report from an exact
// retry whose original response may have been lost.
type TerminalDisposition string

const (
	// TerminalAccepted means this request committed the terminal report.
	TerminalAccepted TerminalDisposition = "ACCEPTED"
	// TerminalAlreadyAccepted means the identical report was already committed.
	TerminalAlreadyAccepted TerminalDisposition = "ALREADY_ACCEPTED"
)

// TerminalAcknowledgement is the stable response for one accepted attempt.
type TerminalAcknowledgement struct {
	AttemptID   string              `json:"attemptId"`
	RunRevision int64               `json:"runRevision"`
	Disposition TerminalDisposition `json:"disposition"`
}

// Valid checks the complete Native Worker API acknowledgement.
func (acknowledgement TerminalAcknowledgement) Valid() bool {
	return ValidLeaseIdentifier(acknowledgement.AttemptID) &&
		acknowledgement.RunRevision >= 1 &&
		(acknowledgement.Disposition == TerminalAccepted ||
			acknowledgement.Disposition == TerminalAlreadyAccepted)
}

// TerminalStore is the privileged persistence boundary for native attempt
// completion. Implementations must serialize with renewal, release,
// cancellation and lease replacement and persist the report with its Run
// transition and lease expiry in one transaction.
type TerminalStore interface {
	// ReportNativeTerminal accepts one current lease-fenced outcome. Identical
	// retries return TerminalAlreadyAccepted with the original resulting Run
	// revision; a different report for the attempt returns ErrTerminalConflict.
	ReportNativeTerminal(
		context.Context,
		string,
		TerminalRequest,
	) (TerminalAcknowledgement, error)
}

// TerminalReporter validates terminal authority before invoking atomic
// persistence.
type TerminalReporter struct {
	store TerminalStore
}

// NewTerminalReporter constructs a native terminal-report use case.
func NewTerminalReporter(store TerminalStore) (*TerminalReporter, error) {
	if store == nil {
		return nil, run.ErrValidation
	}

	return &TerminalReporter{store: store}, nil
}

// Report validates caller-controlled terminal data before delegating the
// authority, evidence and lifecycle decision to storage.
func (reporter *TerminalReporter) Report(
	ctx context.Context,
	workerID string,
	request TerminalRequest,
) (TerminalAcknowledgement, error) {
	if reporter == nil || reporter.store == nil ||
		!contract.ValidNativeWorkerIdentifier(workerID) || !request.Valid() {
		return TerminalAcknowledgement{}, run.ErrValidation
	}

	return reporter.store.ReportNativeTerminal(ctx, workerID, request)
}
