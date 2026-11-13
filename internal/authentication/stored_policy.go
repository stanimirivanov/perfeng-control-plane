package authentication

import (
	"context"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/serviceaccess"
)

// IdentityPolicyStore reads exact current subject enrollment/kind and caller
// delegation together. False means absent or disabled; errors never authorize.
// Implementations must be concurrent-safe, uncached and cancellation-aware.
type IdentityPolicyStore interface {
	IdentityAdmitted(context.Context, serviceaccess.Identity, serviceaccess.Identity, string) (bool, error)
}

// StoredIdentityPolicy translates current store observations to safe admission
// errors. It grants no service rights and retains no authority between requests.
type StoredIdentityPolicy struct{ store IdentityPolicyStore }

// NewStoredIdentityPolicy binds a trusted policy store without I/O.
func NewStoredIdentityPolicy(store IdentityPolicyStore) (*StoredIdentityPolicy, error) {
	if store == nil {
		return nil, run.ErrValidation
	}
	return &StoredIdentityPolicy{store: store}, nil
}

// AuthorizeIdentity denies absent/disabled policy and normalizes backend failures.
// Successful reads are observations, not authorization fences for later mutations.
func (p *StoredIdentityPolicy) AuthorizeIdentity(ctx context.Context, identity VerifiedIdentity) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	allowed, err := p.store.IdentityAdmitted(ctx, identity.Actor, identity.Caller, identity.ActorKind)
	if err != nil {
		return identityError(err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !allowed {
		return ErrUnauthenticated
	}
	return nil
}
