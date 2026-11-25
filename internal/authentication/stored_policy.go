package authentication

import (
	"context"
	"fmt"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/serviceaccess"
)

// ErrDelegationDenied identifies an enrolled actor whose verified client lacks
// current delegation. Generic identity boundaries treat it as unauthenticated.
var ErrDelegationDenied = fmt.Errorf("caller delegation denied: %w", ErrUnauthenticated)

// IdentityPolicyStore reads exact current subject enrollment/kind and caller
// delegation together. False means absent or disabled; errors never authorize.
// Implementations must be concurrent-safe, uncached and cancellation-aware.
type IdentityPolicyStore interface {
	// IdentityAdmission reports whether the actor is enrolled with the given
	// kind and whether its caller currently holds delegation.
	IdentityAdmission(context.Context, serviceaccess.Identity, serviceaccess.Identity, string) (enrolled, delegated bool, err error)
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
	enrolled, delegated, err := p.store.IdentityAdmission(ctx, identity.Actor, identity.Caller, identity.ActorKind)
	if err != nil {
		return identityError(err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !enrolled {
		return ErrUnauthenticated
	}
	if !delegated {
		return ErrDelegationDenied
	}
	return nil
}
