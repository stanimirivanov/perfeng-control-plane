package app

import (
	"os"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/reconcile"
	"github.com/stanimirivanov/perfeng-control-plane/internal/worker"
)

const (
	validationAttemptTimeoutVariable   = "PERFENG_RECONCILIATION_VALIDATION_ATTEMPT_TIMEOUT"
	provisioningAttemptTimeoutVariable = "PERFENG_RECONCILIATION_PROVISIONING_ATTEMPT_TIMEOUT"
	executionRetryAfterVariable        = "PERFENG_RECONCILIATION_EXECUTION_RETRY_AFTER"
	cancellationPollEveryVariable      = "PERFENG_RECONCILIATION_CANCELLATION_POLL_EVERY"
	collectionAttemptTimeoutVariable   = "PERFENG_RECONCILIATION_COLLECTION_ATTEMPT_TIMEOUT"
	collectionRetryAfterVariable       = "PERFENG_RECONCILIATION_COLLECTION_RETRY_AFTER"
	analysisAttemptTimeoutVariable     = "PERFENG_RECONCILIATION_ANALYSIS_ATTEMPT_TIMEOUT"
	analysisRetryAfterVariable         = "PERFENG_RECONCILIATION_ANALYSIS_RETRY_AFTER"
	reportingAttemptTimeoutVariable    = "PERFENG_RECONCILIATION_REPORTING_ATTEMPT_TIMEOUT"
	reportingRetryAfterVariable        = "PERFENG_RECONCILIATION_REPORTING_RETRY_AFTER"
)

// ReconciliationConfig contains the bounded policy for every lifecycle stage.
type ReconciliationConfig struct {
	ValidationAttemptTimeout time.Duration
	Provisioning             reconcile.ProvisioningConfig
	BoundExecution           reconcile.BoundExecutionConfig
	Collection               reconcile.CollectionConfig
	Analysis                 reconcile.AnalysisConfig
	Reporting                reconcile.ReportingConfig
}

// Validate checks every stage and binds provisioning to the worker lease.
func (config ReconciliationConfig) Validate(workerConfig worker.Config) error {
	if workerConfig.Validate() != nil ||
		!reconcile.ValidAttemptTimeout(config.ValidationAttemptTimeout) ||
		config.Provisioning.Validate() != nil ||
		config.Provisioning.LeaseTTL != workerConfig.LeaseTTL ||
		config.BoundExecution.Validate() != nil ||
		config.Collection.Validate() != nil ||
		config.Analysis.Validate() != nil ||
		config.Reporting.Validate() != nil {
		return ErrInvalidConfiguration
	}

	return nil
}

// LoadReconciliationConfig reads the complete lifecycle-stage timing policy.
func LoadReconciliationConfig(workerConfig worker.Config) (ReconciliationConfig, error) {
	return loadReconciliationConfig(os.LookupEnv, workerConfig)
}

func loadReconciliationConfig(
	lookup lookupEnvironment,
	workerConfig worker.Config,
) (ReconciliationConfig, error) {
	if lookup == nil || workerConfig.Validate() != nil {
		return ReconciliationConfig{}, configurationError("reconciliation policy")
	}

	validationAttemptTimeout, err := requiredDuration(lookup, validationAttemptTimeoutVariable)
	if err != nil {
		return ReconciliationConfig{}, err
	}
	provisioningAttemptTimeout, err := requiredDuration(lookup, provisioningAttemptTimeoutVariable)
	if err != nil {
		return ReconciliationConfig{}, err
	}
	executionRetryAfter, err := requiredDuration(lookup, executionRetryAfterVariable)
	if err != nil {
		return ReconciliationConfig{}, err
	}
	cancellationPollEvery, err := requiredDuration(lookup, cancellationPollEveryVariable)
	if err != nil {
		return ReconciliationConfig{}, err
	}
	collectionAttemptTimeout, err := requiredDuration(lookup, collectionAttemptTimeoutVariable)
	if err != nil {
		return ReconciliationConfig{}, err
	}
	collectionRetryAfter, err := requiredDuration(lookup, collectionRetryAfterVariable)
	if err != nil {
		return ReconciliationConfig{}, err
	}
	analysisAttemptTimeout, err := requiredDuration(lookup, analysisAttemptTimeoutVariable)
	if err != nil {
		return ReconciliationConfig{}, err
	}
	analysisRetryAfter, err := requiredDuration(lookup, analysisRetryAfterVariable)
	if err != nil {
		return ReconciliationConfig{}, err
	}
	reportingAttemptTimeout, err := requiredDuration(lookup, reportingAttemptTimeoutVariable)
	if err != nil {
		return ReconciliationConfig{}, err
	}
	reportingRetryAfter, err := requiredDuration(lookup, reportingRetryAfterVariable)
	if err != nil {
		return ReconciliationConfig{}, err
	}

	config := ReconciliationConfig{
		ValidationAttemptTimeout: validationAttemptTimeout,
		Provisioning: reconcile.ProvisioningConfig{
			LeaseTTL:       workerConfig.LeaseTTL,
			AttemptTimeout: provisioningAttemptTimeout,
		},
		BoundExecution: reconcile.BoundExecutionConfig{
			RetryAfter:            executionRetryAfter,
			CancellationPollEvery: cancellationPollEvery,
		},
		Collection: reconcile.CollectionConfig{
			AttemptTimeout: collectionAttemptTimeout,
			RetryAfter:     collectionRetryAfter,
		},
		Analysis: reconcile.AnalysisConfig{
			AttemptTimeout: analysisAttemptTimeout,
			RetryAfter:     analysisRetryAfter,
		},
		Reporting: reconcile.ReportingConfig{
			AttemptTimeout: reportingAttemptTimeout,
			RetryAfter:     reportingRetryAfter,
		},
	}
	if config.Validate(workerConfig) != nil {
		return ReconciliationConfig{}, configurationError("reconciliation policy")
	}

	return config, nil
}
