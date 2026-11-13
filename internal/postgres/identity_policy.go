package postgres

import (
	"context"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/serviceaccess"
)

// IdentityAdmitted observes exact subject enrollment and caller delegation in
// one current statement. It never caches, infers enrollment from a token, or
// grants service rights. Same-issuer callers implement ADR 0046's initial realm
// boundary. A committed revocation precedes the next statement's snapshot;
// mutations must separately fence their own current authorization.
func (r *Repository) IdentityAdmitted(ctx context.Context, actor, caller serviceaccess.Identity, kind string) (bool, error) {
	if !actor.Valid() || !caller.Valid() || (kind != "HUMAN" && kind != "CI") {
		return false, run.ErrValidation
	}
	if caller.Issuer != actor.Issuer {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	var allowed bool
	err := r.db.QueryRowContext(ctx, `SELECT EXISTS (
        SELECT 1 FROM perfeng_control.identity_enrollments e
        JOIN perfeng_control.identity_delegations d USING (issuer,actor_id)
        WHERE e.issuer=$1 AND e.actor_id=$2 AND e.actor_kind=$3
            AND d.caller_id=$4 AND e.enabled AND d.enabled)`, actor.Issuer, actor.ID, kind, caller.ID).Scan(&allowed)
	if err != nil {
		return false, storageError(err)
	}
	return allowed, nil
}
