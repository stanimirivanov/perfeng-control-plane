package postgres

import (
	"context"
	"strings"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/integration"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

var _ integration.RunAdmissionReader = (*Repository)(nil)

// GetIntegrationForRun resolves a scoped native association using the existing
// unique principal/Run key, then restores the same owned evidence as request reads.
// The second read rechecks visibility in case a legacy association changed.
func (r *Repository) GetIntegrationForRun(ctx context.Context, scope integration.RunAdmissionScope) (integration.Record, error) {
	if err := ctx.Err(); err != nil {
		return integration.Record{}, err
	}
	for _, value := range []string{scope.Provider, scope.Principal.Issuer, scope.Principal.ID, scope.RunID} {
		if strings.TrimSpace(value) == "" || strings.ContainsRune(value, 0) {
			return integration.Record{}, run.ErrValidation
		}
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	var identity contract.CatalogIdentity
	err := r.db.QueryRowContext(ctx, `SELECT issuer,request_id FROM perfeng_control.integration_requests
		WHERE principal=$1 AND run_id=$2 AND disposition->>'source'=$3
		AND context->'principal'->>'issuer'=$4`, scope.Principal.ID, scope.RunID, scope.Provider, scope.Principal.Issuer).
		Scan(&identity.Issuer, &identity.ID)
	if err != nil {
		return integration.Record{}, storageError(err)
	}
	record, err := readIntegration(ctx, r.db, scope.Principal.ID, identity)
	if err != nil {
		return integration.Record{}, err
	}
	if !runAdmissionMatches(record, scope) {
		return integration.Record{}, run.ErrNotFound
	}

	return record, nil
}

func runAdmissionMatches(record integration.Record, scope integration.RunAdmissionScope) bool {
	return record.Run != nil && record.Run.ID == scope.RunID &&
		record.Disposition.Source == scope.Provider && record.Disposition.Data.Context.Principal == scope.Principal
}
