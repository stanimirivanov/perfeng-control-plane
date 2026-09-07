package app

import (
	"os"

	"github.com/stanimirivanov/perfeng-control-plane/internal/worker"
)

const (
	workerIDVariable           = "PERFENG_WORKER_ID"
	workerCapacityVariable     = "PERFENG_WORKER_CAPACITY"
	workerLeaseTTLVariable     = "PERFENG_WORKER_LEASE_TTL"
	workerRenewEveryVariable   = "PERFENG_WORKER_RENEW_EVERY"
	workerPollEveryVariable    = "PERFENG_WORKER_POLL_EVERY"
	workerFailureDelayVariable = "PERFENG_WORKER_FAILURE_DELAY"
)

// LoadWorkerConfig reads the complete worker identity, capacity and lease policy.
func LoadWorkerConfig() (worker.Config, error) {
	return loadWorkerConfig(os.LookupEnv)
}

func loadWorkerConfig(lookup lookupEnvironment) (worker.Config, error) {
	if lookup == nil {
		return worker.Config{}, configurationError("environment")
	}

	workerID, err := requiredValue(lookup, workerIDVariable)
	if err != nil {
		return worker.Config{}, err
	}
	capacity, err := requiredBoundedInteger(lookup, workerCapacityVariable, 1, 100)
	if err != nil {
		return worker.Config{}, err
	}
	leaseTTL, err := requiredDuration(lookup, workerLeaseTTLVariable)
	if err != nil {
		return worker.Config{}, err
	}
	renewEvery, err := requiredDuration(lookup, workerRenewEveryVariable)
	if err != nil {
		return worker.Config{}, err
	}
	pollEvery, err := requiredDuration(lookup, workerPollEveryVariable)
	if err != nil {
		return worker.Config{}, err
	}
	failureDelay, err := requiredNonNegativeDuration(lookup, workerFailureDelayVariable)
	if err != nil {
		return worker.Config{}, err
	}

	config := worker.Config{
		WorkerID:     workerID,
		Capacity:     capacity,
		LeaseTTL:     leaseTTL,
		RenewEvery:   renewEvery,
		PollEvery:    pollEvery,
		FailureDelay: failureDelay,
	}
	if config.Validate() != nil {
		return worker.Config{}, configurationError("worker policy")
	}

	return config, nil
}
