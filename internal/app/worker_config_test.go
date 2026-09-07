package app

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/worker"
)

func TestLoadWorkerConfigReturnsExplicitPolicy(t *testing.T) {
	config, err := loadWorkerConfig(mapLookup(validWorkerEnvironment()))
	if err != nil {
		t.Fatal(err)
	}
	want := worker.Config{
		WorkerID:     "control-plane-0",
		Capacity:     4,
		LeaseTTL:     30 * time.Second,
		RenewEvery:   10 * time.Second,
		PollEvery:    time.Second,
		FailureDelay: 5 * time.Second,
	}
	if !reflect.DeepEqual(config, want) {
		t.Fatalf("config = %#v", config)
	}
}

func TestLoadWorkerConfigRejectsInvalidPolicy(t *testing.T) {
	tests := map[string]func(map[string]string){
		"invalid identity": func(values map[string]string) {
			values[workerIDVariable] = "invalid worker"
		},
		"zero capacity": func(values map[string]string) {
			values[workerCapacityVariable] = "0"
		},
		"excessive capacity": func(values map[string]string) {
			values[workerCapacityVariable] = "101"
		},
		"short lease": func(values map[string]string) {
			values[workerLeaseTTLVariable] = "4s"
		},
		"fractional lease": func(values map[string]string) {
			values[workerLeaseTTLVariable] = "5.5s"
		},
		"late renewal": func(values map[string]string) {
			values[workerRenewEveryVariable] = "16s"
		},
		"zero poll": func(values map[string]string) {
			values[workerPollEveryVariable] = "0s"
		},
		"fractional failure delay": func(values map[string]string) {
			values[workerFailureDelayVariable] = "1.5s"
		},
		"long failure delay": func(values map[string]string) {
			values[workerFailureDelayVariable] = "6m"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			values := validWorkerEnvironment()
			mutate(values)

			if _, err := loadWorkerConfig(mapLookup(values)); !errors.Is(err, ErrInvalidConfiguration) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestLoadWorkerConfigAllowsImmediateFailureRediscovery(t *testing.T) {
	values := validWorkerEnvironment()
	values[workerFailureDelayVariable] = "0s"

	config, err := loadWorkerConfig(mapLookup(values))
	if err != nil || config.FailureDelay != 0 {
		t.Fatalf("config = %#v, error = %v", config, err)
	}
}

func TestLoadWorkerConfigRejectsNilEnvironment(t *testing.T) {
	if _, err := loadWorkerConfig(nil); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("error = %v", err)
	}
}

func validWorkerEnvironment() map[string]string {
	return map[string]string{
		workerIDVariable:           "control-plane-0",
		workerCapacityVariable:     "4",
		workerLeaseTTLVariable:     "30s",
		workerRenewEveryVariable:   "10s",
		workerPollEveryVariable:    "1s",
		workerFailureDelayVariable: "5s",
	}
}
