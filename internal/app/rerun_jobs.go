package app

import "github.com/stanimirivanov/perfeng-control-plane/internal/reconcile"

func composeRerunJobs(enabled bool, database Database, review reconcile.JobResolver) (reconcile.JobResolver, error) {
	if !enabled {
		return nil, nil
	}
	store, ok := database.(reconcile.RerunJobStore)
	if !ok {
		return nil, ErrInvalidConfiguration
	}
	approved, ok := review.(reconcile.RerunJobReview)
	if !ok {
		return nil, ErrInvalidConfiguration
	}
	guarded, err := reconcile.NewRerunJobResolver(store, approved)
	if err != nil {
		return nil, ErrInvalidConfiguration
	}
	return guarded, nil
}
