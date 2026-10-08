package nativeworker

import (
	"errors"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
)

// ErrAttemptNotFound conceals whether an attempt is absent or belongs to
// another authenticated worker.
var ErrAttemptNotFound = errors.New("native attempt not found")

// InspectionState describes a read-only observation, not lease authority.
type InspectionState string

const (
	// InspectionActive identifies a current, unexpired and unreleased lease.
	InspectionActive InspectionState = "ACTIVE"
	// InspectionInactive identifies a retained lease without current authority.
	InspectionInactive InspectionState = "INACTIVE"
	// InspectionTerminal identifies a durably accepted terminal report.
	InspectionTerminal InspectionState = "TERMINAL"
)

// InspectionRequest identifies one known Run and native attempt.
type InspectionRequest struct {
	RunID     string
	AttemptID string
}

// Valid checks the exact identifiers without granting authority over them.
func (request InspectionRequest) Valid() bool {
	return contract.ValidID(request.RunID) && ValidLeaseIdentifier(request.AttemptID)
}

// Inspection is one worker-scoped server snapshot. State-specific fields are
// deliberately absent from other states, and none is a bearer capability.
type Inspection struct {
	RunID        string          `json:"runId"`
	AttemptID    string          `json:"attemptId"`
	State        InspectionState `json:"state"`
	ServerTime   time.Time       `json:"serverTime"`
	LeaseID      string          `json:"leaseId,omitempty"`
	ExpiresAt    *time.Time      `json:"expiresAt,omitempty"`
	RunRevision  int64           `json:"runRevision,omitempty"`
	TerminalKind TerminalKind    `json:"terminalKind,omitempty"`
	AcceptedAt   *time.Time      `json:"acceptedAt,omitempty"`
}

// Valid checks the closed state-specific response shape and timestamp order.
func (inspection Inspection) Valid() bool {
	if !contract.ValidID(inspection.RunID) || !ValidLeaseIdentifier(inspection.AttemptID) ||
		inspection.ServerTime.IsZero() {
		return false
	}
	switch inspection.State {
	case InspectionActive:
		return ValidLeaseIdentifier(inspection.LeaseID) && inspection.ExpiresAt != nil &&
			inspection.ExpiresAt.After(inspection.ServerTime) && inspection.RunRevision >= 1 &&
			inspection.TerminalKind == "" && inspection.AcceptedAt == nil
	case InspectionInactive:
		return inspection.LeaseID == "" && inspection.ExpiresAt == nil &&
			inspection.RunRevision == 0 && inspection.TerminalKind == "" &&
			inspection.AcceptedAt == nil
	case InspectionTerminal:
		return inspection.LeaseID == "" && inspection.ExpiresAt == nil &&
			inspection.RunRevision >= 1 && inspection.TerminalKind.Valid() &&
			inspection.AcceptedAt != nil && !inspection.AcceptedAt.IsZero() &&
			!inspection.AcceptedAt.After(inspection.ServerTime)
	default:
		return false
	}
}
