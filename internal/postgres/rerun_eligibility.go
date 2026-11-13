package postgres

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"

	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/runrerun"
)

// lockRerunValidationEligibility follows the owned-child/lease lock. Current
// grants and sorted baseline approvals remain locked until seal commit/rollback.
// This point-in-time validation must not be reused as a later dispatch permit.
func lockRerunValidationEligibility(ctx context.Context, tx *sql.Tx, value runrerun.Acceptance) error {
	binding, err := rerunGrant(ctx, tx, value.Intent.Actor, value.Intent.SourceRunID, true)
	if errors.Is(err, run.ErrNotFound) {
		return run.ErrForbidden
	}
	if err != nil {
		return err
	}
	if binding != value.Source {
		return run.ErrUnavailable
	}
	records := slices.Clone(value.Inputs.BaselineSnapshots)
	slices.SortFunc(records, func(a, b baseline.Record) int {
		if order := cmp.Compare(a.ID, b.ID); order != 0 {
			return order
		}
		return cmp.Compare(a.Version, b.Version)
	})
	for _, record := range records {
		frozen, err := json.Marshal(record)
		if err != nil {
			return run.ErrUnavailable
		}
		var approved bool
		err = tx.QueryRowContext(ctx, `SELECT snapshot=$3::jsonb AND snapshot->>'state'='APPROVED' FROM perfeng_control.baselines WHERE principal=$1 AND baseline_id=$2 AND version=$4 FOR SHARE`, binding.OwnerPrincipal, record.ID, frozen, record.Version).Scan(&approved)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && !approved) {
			return run.ErrForbidden
		}
		if err != nil {
			return storageError(err)
		}
	}
	return nil
}
