package postgres

import (
	"context"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/serviceaccess"
)

// GetServiceRunBinding observes an immutable binding only with a current run:read
// grant for the independently verified actor. Absence, unbound and revoked all
// return ErrNotFound, with no legacy-owner fallback. This single-statement read
// does not authorize a later mutation: that transaction must recheck current grants.
func (r *Repository) GetServiceRunBinding(ctx context.Context, actor serviceaccess.Identity, runID string) (serviceaccess.Binding, error) {
	if !actor.Valid() || runID == "" {
		return serviceaccess.Binding{}, run.ErrValidation
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return serviceaccess.Binding{}, err
	}
	var binding serviceaccess.Binding
	err := r.db.QueryRowContext(ctx, `
        SELECT b.service_issuer, b.service_id, b.creator_issuer, b.creator_id,
            b.creator_kind, b.owner_principal, b.review_ref
        FROM perfeng_control.run_services b
        JOIN perfeng_control.service_grants g USING (service_issuer, service_id)
        WHERE b.run_id=$1 AND g.actor_issuer=$2 AND g.actor_id=$3
            AND g.permission='run:read' AND g.enabled`, runID, actor.Issuer, actor.ID).Scan(
		&binding.Service.Issuer, &binding.Service.ID, &binding.Creator.Issuer, &binding.Creator.ID,
		&binding.CreatorKind, &binding.OwnerPrincipal, &binding.ReviewReference)
	if err != nil {
		return serviceaccess.Binding{}, storageError(err)
	}

	return binding, nil
}

// HasServicePermission reads the current exact service/actor/right without caching
// or inherited rights. False is normal absence/revocation; errors never mean allow.
// Callers must supply verified identities and must not use this observation as a
// transferable capability or as an authorization fence for a separate transaction.
func (r *Repository) HasServicePermission(ctx context.Context, service, actor serviceaccess.Identity, permission serviceaccess.Permission) (bool, error) {
	if !service.Valid() || !actor.Valid() || !permission.Valid() {
		return false, run.ErrValidation
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	var allowed bool
	err := r.db.QueryRowContext(ctx, `SELECT EXISTS (
        SELECT 1 FROM perfeng_control.service_grants
        WHERE service_issuer=$1 AND service_id=$2 AND actor_issuer=$3 AND actor_id=$4
            AND permission=$5 AND enabled)`, service.Issuer, service.ID, actor.Issuer, actor.ID, permission).Scan(&allowed)
	if err != nil {
		return false, storageError(err)
	}

	return allowed, nil
}
