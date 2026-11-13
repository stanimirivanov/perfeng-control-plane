package postgres

import (
	"context"
	"encoding/json"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/runrerun"
	"github.com/stanimirivanov/perfeng-control-plane/internal/serviceaccess"
)

// GetRerunExecution reads immutable execution evidence by exact native child
// owner/ID/request. Only trusted workers with a server-owned claim may call it:
// these selectors are not credentials, lease proof or current admission grants.
// Absence never permits inferring a selection from a registry or source Run.
func (r *Repository) GetRerunExecution(ctx context.Context, principal string, current run.Run) (runrerun.Acceptance, error) {
	if principal == "" || !contract.ValidID(current.ID) || current.Request.Validate() != nil {
		return runrerun.Acceptance{}, run.ErrValidation
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	tx, err := r.begin(ctx)
	if err != nil {
		return runrerun.Acceptance{}, err
	}
	defer tx.Rollback()
	request, err := json.Marshal(current.Request)
	if err != nil {
		return runrerun.Acceptance{}, run.ErrValidation
	}
	var actor serviceaccess.Identity
	var key, kind string
	var source serviceaccess.Binding
	err = tx.QueryRowContext(ctx, `SELECT i.actor_issuer,i.actor_id,i.idempotency_key,c.creator_kind,
 s.service_issuer,s.service_id,s.creator_issuer,s.creator_id,s.creator_kind,s.owner_principal,s.review_ref
 FROM perfeng_control.rerun_intents i
 JOIN perfeng_control.runs r ON r.principal=i.child_owner AND r.run_id=i.child_run_id
 JOIN perfeng_control.run_services c ON c.run_id=i.child_run_id AND c.owner_principal=i.child_owner
 JOIN perfeng_control.run_services s ON s.run_id=i.source_run_id AND s.owner_principal=i.source_owner
 WHERE i.child_owner=$1 AND i.child_run_id=$2 AND r.snapshot->'request'=$3::jsonb
 AND c.service_issuer=s.service_issuer AND c.service_id=s.service_id
 AND c.creator_issuer=i.actor_issuer AND c.creator_id=i.actor_id`, principal, current.ID, request).Scan(
		&actor.Issuer, &actor.ID, &key, &kind, &source.Service.Issuer, &source.Service.ID, &source.Creator.Issuer, &source.Creator.ID, &source.CreatorKind, &source.OwnerPrincipal, &source.ReviewReference)
	if err != nil {
		return runrerun.Acceptance{}, storageError(err)
	}
	value, err := rerunRecord(ctx, tx, actor, key)
	if err != nil {
		return runrerun.Acceptance{}, err
	}
	if value.Child.ID != current.ID || value.ChildPrincipal != principal || value.Child.Request != current.Request || value.Source != source || value.Intent.ActorKind != kind {
		return runrerun.Acceptance{}, run.ErrUnavailable
	}
	return value, nil
}

// LookupRerunExecution observes the durable child association independently of
// requested metadata. A known child's failed lookup must never authorize ordinary
// registry fallback. Atomic creation and immutable intent rows keep the two reads
// stable without network calls or inferring identity from a principal prefix.
func (r *Repository) LookupRerunExecution(ctx context.Context, principal string, current run.Run) (runrerun.Acceptance, bool, error) {
	if principal == "" || !contract.ValidID(current.ID) || current.Request.Validate() != nil {
		return runrerun.Acceptance{}, false, run.ErrValidation
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	var known bool
	err := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM perfeng_control.rerun_intents WHERE child_owner=$1 AND child_run_id=$2)`, principal, current.ID).Scan(&known)
	if err != nil {
		return runrerun.Acceptance{}, false, storageError(err)
	}
	if !known {
		return runrerun.Acceptance{}, false, nil
	}
	value, err := r.GetRerunExecution(ctx, principal, current)
	return value, true, err
}
