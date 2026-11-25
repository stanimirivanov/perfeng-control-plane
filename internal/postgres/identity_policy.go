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
	enrolled, delegated, err := r.IdentityAdmission(ctx, actor, caller, kind)
	return enrolled && delegated, err
}

// IdentityAdmission distinguishes absent subject enrollment from missing client
// delegation at the same database snapshot. Neither observation grants Run access.
func (r *Repository) IdentityAdmission(ctx context.Context, actor, caller serviceaccess.Identity, kind string) (bool, bool, error) {
	if !actor.Valid() || !caller.Valid() || (kind != "HUMAN" && kind != "CI") {
		return false, false, run.ErrValidation
	}
	if caller.Issuer != actor.Issuer {
		return false, false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return false, false, err
	}
	var enrolled, delegated bool
	err := r.db.QueryRowContext(ctx, `SELECT EXISTS (
        SELECT 1 FROM perfeng_control.identity_enrollments
        WHERE issuer=$1 AND actor_id=$2 AND actor_kind=$3 AND enabled), EXISTS (
        SELECT 1 FROM perfeng_control.identity_enrollments e
        JOIN perfeng_control.identity_delegations d USING (issuer,actor_id)
        WHERE e.issuer=$1 AND e.actor_id=$2 AND e.actor_kind=$3
            AND d.caller_id=$4 AND e.enabled AND d.enabled)`, actor.Issuer, actor.ID, kind, caller.ID).Scan(&enrolled, &delegated)
	if err != nil {
		return false, false, storageError(err)
	}
	return enrolled, delegated, nil
}
