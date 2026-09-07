package app

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/reconcile"
	"github.com/stanimirivanov/perfeng-control-plane/internal/worker"
)

func TestLoadReconciliationConfigReturnsExplicitPolicy(t *testing.T) {
	workerConfig := validWorkerConfig()

	config, err := loadReconciliationConfig(
		mapLookup(validReconciliationEnvironment()),
		workerConfig,
	)
	if err != nil {
		t.Fatal(err)
	}
	want := ReconciliationConfig{
		ValidationAttemptTimeout: 8 * time.Second,
		Provisioning: reconcile.ProvisioningConfig{
			LeaseTTL:       30 * time.Second,
			AttemptTimeout: 10 * time.Second,
		},
		BoundExecution: reconcile.BoundExecutionConfig{
			RetryAfter:            5 * time.Second,
			CancellationPollEvery: time.Second,
		},
		Collection: reconcile.CollectionConfig{
			AttemptTimeout: 12 * time.Second,
			RetryAfter:     6 * time.Second,
		},
		Analysis: reconcile.AnalysisConfig{
			AttemptTimeout: 14 * time.Second,
			RetryAfter:     7 * time.Second,
		},
		Reporting: reconcile.ReportingConfig{
			AttemptTimeout: 16 * time.Second,
			RetryAfter:     8 * time.Second,
		},
	}
	if !reflect.DeepEqual(config, want) {
		t.Fatalf("config = %#v", config)
	}
}

func TestLoadReconciliationConfigRejectsInvalidPolicy(t *testing.T) {
	tests := map[string]func(map[string]string){
		"validation timeout": func(values map[string]string) {
			values[validationAttemptTimeoutVariable] = "6m"
		},
		"provisioning timeout": func(values map[string]string) {
			values[provisioningAttemptTimeoutVariable] = "16s"
		},
		"execution retry": func(values map[string]string) {
			values[executionRetryAfterVariable] = "1.5s"
		},
		"cancellation poll": func(values map[string]string) {
			values[cancellationPollEveryVariable] = "6m"
		},
		"collection timeout": func(values map[string]string) {
			values[collectionAttemptTimeoutVariable] = "6m"
		},
		"collection retry": func(values map[string]string) {
			values[collectionRetryAfterVariable] = "0s"
		},
		"analysis timeout": func(values map[string]string) {
			values[analysisAttemptTimeoutVariable] = "6m"
		},
		"analysis retry": func(values map[string]string) {
			values[analysisRetryAfterVariable] = "301s"
		},
		"reporting timeout": func(values map[string]string) {
			values[reportingAttemptTimeoutVariable] = "6m"
		},
		"reporting retry": func(values map[string]string) {
			values[reportingRetryAfterVariable] = "500ms"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			values := validReconciliationEnvironment()
			mutate(values)

			_, err := loadReconciliationConfig(mapLookup(values), validWorkerConfig())
			if !errors.Is(err, ErrInvalidConfiguration) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestReconciliationConfigRejectsInvalidWorkerAndLeaseMismatch(t *testing.T) {
	workerConfig := validWorkerConfig()
	config, err := loadReconciliationConfig(
		mapLookup(validReconciliationEnvironment()),
		workerConfig,
	)
	if err != nil {
		t.Fatal(err)
	}

	invalidWorker := workerConfig
	invalidWorker.Capacity = 0
	if err := config.Validate(invalidWorker); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("invalid worker error = %v", err)
	}

	config.Provisioning.LeaseTTL = time.Minute
	if err := config.Validate(workerConfig); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("lease mismatch error = %v", err)
	}
}

func TestLoadReconciliationConfigRejectsNilEnvironmentAndInvalidWorker(t *testing.T) {
	if _, err := loadReconciliationConfig(nil, validWorkerConfig()); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("nil environment error = %v", err)
	}

	if _, err := loadReconciliationConfig(
		mapLookup(validReconciliationEnvironment()),
		worker.Config{},
	); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("invalid worker error = %v", err)
	}
}

func validWorkerConfig() worker.Config {
	return worker.Config{
		WorkerID:     "control-plane-0",
		Capacity:     4,
		LeaseTTL:     30 * time.Second,
		RenewEvery:   10 * time.Second,
		PollEvery:    time.Second,
		FailureDelay: 5 * time.Second,
	}
}

func validReconciliationEnvironment() map[string]string {
	return map[string]string{
		validationAttemptTimeoutVariable:   "8s",
		provisioningAttemptTimeoutVariable: "10s",
		executionRetryAfterVariable:        "5s",
		cancellationPollEveryVariable:      "1s",
		collectionAttemptTimeoutVariable:   "12s",
		collectionRetryAfterVariable:       "6s",
		analysisAttemptTimeoutVariable:     "14s",
		analysisRetryAfterVariable:         "7s",
		reportingAttemptTimeoutVariable:    "16s",
		reportingRetryAfterVariable:        "8s",
	}
}
