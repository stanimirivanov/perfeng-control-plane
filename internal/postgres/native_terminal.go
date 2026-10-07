package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/nativeworker"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

type storedNativeTerminal struct {
	workerID          string
	protocolVersion   int
	request           nativeworker.TerminalRequest
	resultingRevision int64
	acceptedAt        time.Time
}

type registeredNativeArtifact struct {
	artifactID   string
	slot         string
	kind         string
	format       string
	mediaType    string
	registeredAt time.Time
}

// ReportNativeBaselineTerminal accepts a v2 terminal outcome under v2 lease
// authority while retaining the Run's single durable terminal report.
func (r *Repository) ReportNativeBaselineTerminal(
	ctx context.Context,
	workerID string,
	request nativeworker.TerminalRequest,
) (nativeworker.TerminalAcknowledgement, error) {
	return r.reportNativeTerminal(ctx, workerID, request, nativeLeaseProtocolV2)
}

func (r *Repository) reportNativeTerminal(
	ctx context.Context,
	workerID string,
	request nativeworker.TerminalRequest,
	protocolVersion int,
) (nativeworker.TerminalAcknowledgement, error) {
	if !contract.ValidNativeWorkerIdentifier(workerID) || !request.Valid() {
		return nativeworker.TerminalAcknowledgement{}, run.ErrValidation
	}

	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()

	tx, err := r.begin(ctx)
	if err != nil {
		return nativeworker.TerminalAcknowledgement{}, err
	}
	defer tx.Rollback()

	scope, err := r.lockNativeRenewalScopeVersion(ctx, tx, workerID, request.LeaseID, protocolVersion)
	if err != nil {
		return nativeworker.TerminalAcknowledgement{}, err
	}
	if !matchingNativeLeaseAuthority(
		scope.stored,
		workerID,
		request.LeaseID,
		request.AttemptID,
		request.Capability,
	) {
		return nativeworker.TerminalAcknowledgement{}, nativeworker.ErrLeaseGone
	}

	stored, found, err := lockedNativeTerminal(ctx, tx, scope.runID)
	if err != nil {
		return nativeworker.TerminalAcknowledgement{}, err
	}
	if found {
		if stored.workerID != workerID || stored.protocolVersion != protocolVersion ||
			!sameNativeTerminal(stored.request, request) {
			return nativeworker.TerminalAcknowledgement{}, nativeworker.ErrTerminalConflict
		}

		return nativeworker.TerminalAcknowledgement{
			AttemptID: request.AttemptID, RunRevision: stored.resultingRevision,
			Disposition: nativeworker.TerminalAlreadyAccepted,
		}, nil
	}
	if scope.stored.releasedAt.Valid || !scope.now.Before(scope.stored.expiresAt) ||
		contract.Terminal(string(scope.current.State)) {
		return nativeworker.TerminalAcknowledgement{}, nativeworker.ErrLeaseGone
	}
	if scope.current.Revision != request.ExpectedRunRevision {
		return nativeworker.TerminalAcknowledgement{}, run.ErrRevision
	}

	if request.Kind == nativeworker.TerminalCompleted {
		if err = validateNativeTerminalEvidence(ctx, tx, scope, request, protocolVersion); err != nil {
			return nativeworker.TerminalAcknowledgement{}, err
		}
	}
	next, err := applyNativeTerminal(scope.current, request, scope.now)
	if err != nil {
		return nativeworker.TerminalAcknowledgement{}, err
	}
	if next.Revision != scope.current.Revision {
		if err = updateNativeRun(ctx, tx, next); err != nil {
			return nativeworker.TerminalAcknowledgement{}, err
		}
	}
	if err = insertNativeTerminal(
		ctx, tx, scope.runID, workerID, request, next.Revision, scope.now, protocolVersion,
	); err != nil {
		return nativeworker.TerminalAcknowledgement{}, err
	}
	if _, err = tx.ExecContext(ctx, `
		UPDATE perfeng_control.native_execution_leases
		SET expires_at=$1
		WHERE run_id=$2
	`, scope.now, scope.runID); err != nil {
		return nativeworker.TerminalAcknowledgement{}, storageError(err)
	}
	if err = tx.Commit(); err != nil {
		return nativeworker.TerminalAcknowledgement{}, storageError(err)
	}

	return nativeworker.TerminalAcknowledgement{
		AttemptID: request.AttemptID, RunRevision: next.Revision,
		Disposition: nativeworker.TerminalAccepted,
	}, nil
}

func lockedNativeTerminal(
	ctx context.Context,
	tx *sql.Tx,
	runID string,
) (storedNativeTerminal, bool, error) {
	var stored storedNativeTerminal
	var manifestID sql.NullString
	var failureCode sql.NullString
	var failureMessage sql.NullString
	var failureRetryable sql.NullBool
	err := tx.QueryRowContext(ctx, `
		SELECT lease_id::text,attempt_id::text,worker_id,protocol_version,expected_run_revision,kind,
			manifest_artifact_id::text,failure_code,failure_message,failure_retryable,
			resulting_run_revision,accepted_at
		FROM perfeng_control.native_terminal_reports
		WHERE run_id=$1
		FOR UPDATE
	`, runID).Scan(
		&stored.request.LeaseID,
		&stored.request.AttemptID,
		&stored.workerID,
		&stored.protocolVersion,
		&stored.request.ExpectedRunRevision,
		&stored.request.Kind,
		&manifestID,
		&failureCode,
		&failureMessage,
		&failureRetryable,
		&stored.resultingRevision,
		&stored.acceptedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return storedNativeTerminal{}, false, nil
	}
	if err != nil {
		return storedNativeTerminal{}, false, storageError(err)
	}
	stored.request.ManifestArtifactID = manifestID.String
	if failureCode.Valid && failureMessage.Valid && failureRetryable.Valid {
		stored.request.Failure = &nativeworker.TerminalFailure{
			Code:    nativeworker.TerminalFailureCode(failureCode.String),
			Message: failureMessage.String, Retryable: failureRetryable.Bool,
		}
	}
	stored.acceptedAt = stored.acceptedAt.UTC()
	if !contract.ValidNativeWorkerIdentifier(stored.workerID) ||
		stored.protocolVersion != nativeLeaseProtocolV2 ||
		!stored.requestWithoutCapabilityValid() ||
		stored.resultingRevision < 1 || stored.acceptedAt.IsZero() {
		return storedNativeTerminal{}, false, errors.New("invalid stored native terminal report")
	}

	return stored, true, nil
}

func (stored storedNativeTerminal) requestWithoutCapabilityValid() bool {
	stored.request.Capability = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	return stored.request.Valid()
}

func sameNativeTerminal(
	stored nativeworker.TerminalRequest,
	request nativeworker.TerminalRequest,
) bool {
	if stored.LeaseID != request.LeaseID || stored.AttemptID != request.AttemptID ||
		stored.ExpectedRunRevision != request.ExpectedRunRevision || stored.Kind != request.Kind ||
		stored.ManifestArtifactID != request.ManifestArtifactID ||
		(stored.Failure == nil) != (request.Failure == nil) {
		return false
	}

	return stored.Failure == nil || *stored.Failure == *request.Failure
}

func validateNativeTerminalEvidence(
	ctx context.Context,
	tx *sql.Tx,
	scope nativeRenewalScope,
	request nativeworker.TerminalRequest,
	protocolVersion int,
) error {
	planned, err := nativeTerminalArtifactPlan(scope.stored.plan, protocolVersion)
	if err != nil {
		return err
	}
	artifacts, err := registeredNativeArtifacts(ctx, tx, scope.runID, request.AttemptID)
	if err != nil {
		return err
	}
	bySlot := make(map[string]registeredNativeArtifact, len(artifacts))
	var manifest registeredNativeArtifact
	for _, artifact := range artifacts {
		bySlot[artifact.slot] = artifact
		if artifact.artifactID == request.ManifestArtifactID {
			manifest = artifact
		}
	}
	if manifest.artifactID == "" || manifest.kind != "manifest" {
		return nativeworker.ErrTerminalEvidence
	}
	for _, slot := range planned {
		artifact, found := bySlot[slot.Slot]
		if slot.Required && !found {
			return nativeworker.ErrTerminalEvidence
		}
		if found && (artifact.kind != slot.Kind || artifact.format != slot.Format ||
			artifact.mediaType != slot.MediaType || artifact.registeredAt.After(manifest.registeredAt)) {
			return nativeworker.ErrTerminalEvidence
		}
	}

	return nil
}

func nativeTerminalArtifactPlan(storedPlan []byte, protocolVersion int) ([]nativeworker.ArtifactSlot, error) {
	if protocolVersion != nativeLeaseProtocolV2 {
		return nil, run.ErrValidation
	}
	var plan nativeworker.BaselinePlan
	if err := json.Unmarshal(storedPlan, &plan); err != nil || plan.Validate() != nil {
		return nil, errors.New("invalid stored native baseline plan")
	}
	return plan.ArtifactPlan[:], nil
}

func registeredNativeArtifacts(
	ctx context.Context,
	tx *sql.Tx,
	runID string,
	attemptID string,
) (artifacts []registeredNativeArtifact, err error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT a.artifact_id::text,g.slot,a.kind,a.format,g.media_type,a.registered_at
		FROM perfeng_control.native_artifacts AS a
		JOIN perfeng_control.native_upload_grants AS g ON g.grant_id=a.grant_id
		WHERE g.run_id=$1 AND g.attempt_id=$2
		ORDER BY a.registered_at,a.artifact_id
	`, runID, attemptID)
	if err != nil {
		return nil, storageError(err)
	}
	defer func() {
		if closeErr := rows.Close(); err == nil && closeErr != nil {
			artifacts = nil
			err = storageError(closeErr)
		}
	}()

	artifacts = make([]registeredNativeArtifact, 0)
	for rows.Next() {
		var artifact registeredNativeArtifact
		if err = rows.Scan(
			&artifact.artifactID,
			&artifact.slot,
			&artifact.kind,
			&artifact.format,
			&artifact.mediaType,
			&artifact.registeredAt,
		); err != nil {
			return nil, storageError(err)
		}
		artifact.registeredAt = artifact.registeredAt.UTC()
		artifacts = append(artifacts, artifact)
	}
	if err = rows.Err(); err != nil {
		return nil, storageError(err)
	}

	return artifacts, nil
}

func applyNativeTerminal(
	current run.Run,
	request nativeworker.TerminalRequest,
	now time.Time,
) (run.Run, error) {
	if request.Kind == nativeworker.TerminalCompleted {
		return nativeworker.AdvancePhase(
			current,
			request.ExpectedRunRevision,
			nativeworker.PhaseFinalizing,
			now,
		)
	}
	if current.Revision != request.ExpectedRunRevision {
		return run.Run{}, run.ErrRevision
	}
	if current.State == run.StateCancelling || contract.Terminal(string(current.State)) {
		return run.Run{}, run.ErrConflict
	}
	if request.Failure.Code == nativeworker.TerminalFailureTool {
		collecting, err := nativeworker.AdvancePhase(
			current,
			request.ExpectedRunRevision,
			nativeworker.PhaseFinalizing,
			now,
		)
		if err != nil {
			return run.Run{}, err
		}

		return collecting.Transition(collecting.Revision, run.Change{
			State: run.StateTestFailure,
			Failure: &run.Failure{
				Code: run.FailureCodeToolError, Message: "Native browser execution failed",
			},
		}, now)
	}
	if current.State == run.StateCreated {
		var err error
		current, err = current.Transition(
			current.Revision,
			run.Change{State: run.StateValidating},
			now,
		)
		if err != nil {
			return run.Run{}, err
		}
	}

	return current.Transition(current.Revision, run.Change{
		State: run.StateInfrastructureFailure,
		Failure: &run.Failure{
			Code: run.FailureCodeInfrastructureError, Message: nativeTerminalFailureMessage(request.Failure.Code),
		},
	}, now)
}

func nativeTerminalFailureMessage(code nativeworker.TerminalFailureCode) string {
	switch code {
	case nativeworker.TerminalFailurePreparation:
		return "Native worker preparation failed"
	case nativeworker.TerminalFailureCapture:
		return "Native browser evidence capture failed"
	case nativeworker.TerminalFailureUpload:
		return "Native browser evidence upload failed"
	case nativeworker.TerminalFailureHostUnqualified:
		return "Native worker became unqualified"
	default:
		return "Native browser execution failed"
	}
}

func insertNativeTerminal(
	ctx context.Context,
	tx *sql.Tx,
	runID string,
	workerID string,
	request nativeworker.TerminalRequest,
	resultingRevision int64,
	acceptedAt time.Time,
	protocolVersion int,
) error {
	var manifestID any
	var failureCode any
	var failureMessage any
	var failureRetryable any
	if request.Kind == nativeworker.TerminalCompleted {
		manifestID = request.ManifestArtifactID
	} else {
		failureCode = request.Failure.Code
		failureMessage = request.Failure.Message
		failureRetryable = request.Failure.Retryable
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO perfeng_control.native_terminal_reports(
			run_id,lease_id,attempt_id,worker_id,protocol_version,expected_run_revision,kind,
			manifest_artifact_id,failure_code,failure_message,failure_retryable,
			resulting_run_revision,accepted_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
	`, runID, request.LeaseID, request.AttemptID, workerID, protocolVersion,
		request.ExpectedRunRevision, request.Kind, manifestID, failureCode,
		failureMessage, failureRetryable, resultingRevision, acceptedAt)

	return storageError(err)
}
