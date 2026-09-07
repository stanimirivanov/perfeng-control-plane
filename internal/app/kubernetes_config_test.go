package app

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestLoadKubernetesConfigReturnsExplicitPolicy(t *testing.T) {
	config, err := loadKubernetesConfig(mapLookup(validKubernetesEnvironment()))
	if err != nil {
		t.Fatal(err)
	}
	want := KubernetesConfig{
		Namespace:      "perf-runs",
		RequestTimeout: 10 * time.Second,
		QPS:            20.5,
		Burst:          30,
	}
	if !reflect.DeepEqual(config, want) {
		t.Fatalf("config = %#v", config)
	}
}

func TestLoadKubernetesConfigRejectsInvalidPolicy(t *testing.T) {
	tests := map[string]func(map[string]string){
		"namespace": func(values map[string]string) {
			values[kubernetesNamespaceVariable] = "Invalid_Namespace"
		},
		"timeout": func(values map[string]string) {
			values[kubernetesRequestTimeoutVariable] = "0s"
		},
		"low QPS": func(values map[string]string) {
			values[kubernetesQPSVariable] = "0.09"
		},
		"high QPS": func(values map[string]string) {
			values[kubernetesQPSVariable] = "100.1"
		},
		"NaN QPS": func(values map[string]string) {
			values[kubernetesQPSVariable] = "NaN"
		},
		"infinite QPS": func(values map[string]string) {
			values[kubernetesQPSVariable] = "+Inf"
		},
		"zero burst": func(values map[string]string) {
			values[kubernetesBurstVariable] = "0"
		},
		"high burst": func(values map[string]string) {
			values[kubernetesBurstVariable] = "1001"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			values := validKubernetesEnvironment()
			mutate(values)

			if _, err := loadKubernetesConfig(mapLookup(values)); !errors.Is(err, ErrInvalidConfiguration) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestLoadKubernetesConfigRejectsNilEnvironment(t *testing.T) {
	if _, err := loadKubernetesConfig(nil); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("error = %v", err)
	}
}

func validKubernetesEnvironment() map[string]string {
	return map[string]string{
		kubernetesNamespaceVariable:      "perf-runs",
		kubernetesRequestTimeoutVariable: "10s",
		kubernetesQPSVariable:            "20.5",
		kubernetesBurstVariable:          "30",
	}
}
