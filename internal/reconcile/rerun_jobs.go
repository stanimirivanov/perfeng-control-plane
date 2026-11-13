package reconcile

import (
	"context"
	"errors"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/runrerun"
	batchv1 "k8s.io/api/batch/v1"
)

// RerunJobStore owns the accepted association and immutable reusable template.
type RerunJobStore interface {
	LookupRerunExecution(context.Context, string, run.Run) (runrerun.Acceptance, bool, error)
	GetRerunTemplate(context.Context, string, run.Run) (*batchv1.Job, error)
	// RetainClaimedRerunTemplate checks current lease/revision, original actor
	// grants and exact approved baseline snapshots in the seal transaction.
	// PROVISIONING may only replay an identical existing validation seal.
	RetainClaimedRerunTemplate(context.Context, run.Claim, *batchv1.Job) (*batchv1.Job, error)
}

// RerunJobReview supplies ordinary resolution and exact current registry review.
// It does not establish current baseline eligibility or lease authority.
type RerunJobReview interface {
	JobResolver
	ResolveRerunTemplate(context.Context, runrerun.Acceptance, run.Run) (*batchv1.Job, error)
}

// RerunJobResolver seals reviewed validation and recovers cleanup from its seal.
// Provisioning requires claimed replay of the exact reviewed validation seal.
type RerunJobResolver struct {
	store  RerunJobStore
	review RerunJobReview
}

// NewRerunJobResolver requires accepted storage and independent registry review.
func NewRerunJobResolver(store RerunJobStore, review RerunJobReview) (*RerunJobResolver, error) {
	if store == nil || review == nil {
		return nil, run.ErrValidation
	}
	return &RerunJobResolver{store: store, review: review}, nil
}

// ResolveJob preserves ordinary selection; known reruns never fall back to it.
func (resolver *RerunJobResolver) ResolveJob(ctx context.Context, principal string, current run.Run) (*batchv1.Job, error) {
	return resolver.resolveJob(ctx, principal, current, nil)
}

// ResolveClaimJob carries lease authority into validation capture or provisioning
// replay. Provisioning cannot backfill a missing seal; SQL replay rechecks current
// actor/baseline eligibility, and the caller must renew before Kubernetes I/O.
func (resolver *RerunJobResolver) ResolveClaimJob(ctx context.Context, claim run.Claim) (*batchv1.Job, error) {
	if !claim.Lease.Valid() || claim.Lease.RunID != claim.Run.ID {
		return nil, run.ErrValidation
	}
	return resolver.resolveJob(ctx, claim.Lease.Principal, claim.Run, &claim)
}
func (resolver *RerunJobResolver) resolveJob(ctx context.Context, principal string, current run.Run, claim *run.Claim) (*batchv1.Job, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	value, known, err := resolver.store.LookupRerunExecution(ctx, principal, current.Clone())
	if err != nil {
		if errors.Is(err, run.ErrNotFound) {
			err = run.ErrUnavailable
		}
		return nil, err
	}
	if !known {
		return resolver.review.ResolveJob(ctx, principal, current.Clone())
	}
	if value.Validate() != nil || value.Child.ID != current.ID || value.ChildPrincipal != principal || value.Child.Request != current.Request {
		return nil, run.ErrUnavailable
	}
	switch current.State {
	case run.StateValidating, run.StateProvisioning:
		if claim == nil {
			if current.State == run.StateProvisioning {
				return nil, run.ErrUnavailable
			}
			return nil, run.ErrValidation
		}
		template, err := resolver.review.ResolveRerunTemplate(ctx, value, current.Clone())
		if err != nil {
			return nil, err
		}
		return resolver.store.RetainClaimedRerunTemplate(ctx, *claim, template)
	case run.StateCancelling:
		template, err := resolver.store.GetRerunTemplate(ctx, principal, current.Clone())
		if errors.Is(err, run.ErrNotFound) {
			err = run.ErrUnavailable
		}
		return template, err
	default:
		return nil, run.ErrValidation
	}
}
