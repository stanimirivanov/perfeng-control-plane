package reconcile

import (
	"context"
	"errors"

	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/runrerun"
	batchv1 "k8s.io/api/batch/v1"
)

// RerunRawStore reads validated private child associations and sealed execution
// evidence. Absence is not authorization to reselect a known child's producer.
type RerunRawStore interface {
	LookupRerunExecution(context.Context, string, run.Run) (runrerun.Acceptance, bool, error)
	GetRerunTemplate(context.Context, string, run.Run) (*batchv1.Job, error)
}

// RerunRawReview approves ordinary evidence or verifies a known child's frozen
// workload/schema and sealed execution against current source-owner review.
type RerunRawReview interface {
	RawManifestApprover
	ApproveRerunRawManifest(context.Context, runrerun.Acceptance, run.Run, *batchv1.Job, rawresult.Manifest) error
}

// RerunRawApprover prevents synthetic child principals from reselecting trust.
// This is provenance approval, not lease or current service-grant authorization.
type RerunRawApprover struct {
	store  RerunRawStore
	review RerunRawReview
}

// NewRerunRawApprover requires private evidence readers and independent review;
// construction performs no lookup, registry approval or external I/O.
func NewRerunRawApprover(store RerunRawStore, review RerunRawReview) (*RerunRawApprover, error) {
	if store == nil || review == nil {
		return nil, run.ErrValidation
	}
	return &RerunRawApprover{store: store, review: review}, nil
}

// ApproveRawManifest delegates only proven ordinary Runs. Known children require
// exact acceptance and a seal; missing/corrupt evidence never permits fallback.
func (approver *RerunRawApprover) ApproveRawManifest(ctx context.Context, principal string, current run.Run, manifest rawresult.Manifest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	value, known, err := approver.store.LookupRerunExecution(ctx, principal, current.Clone())
	if err != nil {
		return rerunRawEvidenceError(err)
	}
	if !known {
		return approver.review.ApproveRawManifest(ctx, principal, current.Clone(), cloneRawManifest(manifest))
	}
	if value.Validate() != nil || value.ChildPrincipal != principal || value.Child.ID != current.ID || value.Child.Request != current.Request {
		return run.ErrUnavailable
	}
	if current.State != run.StateCollecting {
		return run.ErrValidation
	}
	sealed, err := approver.store.GetRerunTemplate(ctx, principal, current.Clone())
	if err != nil {
		return rerunRawEvidenceError(err)
	}
	if sealed == nil {
		return run.ErrUnavailable
	}
	return approver.review.ApproveRerunRawManifest(ctx, value, current.Clone(), sealed.DeepCopy(), cloneRawManifest(manifest))
}

func rerunRawEvidenceError(err error) error {
	if errors.Is(err, run.ErrNotFound) {
		return run.ErrUnavailable
	}
	return err
}
