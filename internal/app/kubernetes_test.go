package app

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

func TestNewKubernetesClientsAppliesPolicyWithoutChangingSource(t *testing.T) {
	config := validKubernetesConfig()
	source := &rest.Config{Host: "https://kubernetes.default.svc", QPS: 99, Burst: 99}
	factoryCalls := 0
	factory := newKubernetesClientset(func(actual *rest.Config) (clientset.Interface, error) {
		factoryCalls++
		if actual == source || actual.Host != source.Host || actual.Timeout != config.RequestTimeout ||
			actual.QPS != config.QPS || actual.Burst != config.Burst ||
			actual.UserAgent != "perfeng-control-plane" {
			t.Fatalf("REST config = %#v", actual)
		}

		return fake.NewSimpleClientset(), nil
	})

	clients, err := newKubernetesClients(
		context.Background(), config, func() (*rest.Config, error) { return source, nil }, factory,
	)
	if err != nil {
		t.Fatal(err)
	}
	if clients.Namespace != config.Namespace || clients.Jobs == nil || clients.Pods == nil ||
		factoryCalls != 1 {
		t.Fatalf("clients = %#v, factory calls = %d", clients, factoryCalls)
	}
	if source.Timeout != 0 || source.QPS != 99 || source.Burst != 99 || source.UserAgent != "" {
		t.Fatalf("source config changed: %#v", source)
	}
}

func TestNewKubernetesClientsRejectsInvalidDependencies(t *testing.T) {
	valid := validKubernetesConfig()
	invalid := valid
	invalid.Namespace = "Invalid"
	nanQPS := valid
	nanQPS.QPS = float32(math.NaN())
	tests := map[string]struct {
		config       KubernetesConfig
		load         loadKubernetesRESTConfig
		newClientset newKubernetesClientset
		want         error
	}{
		"configuration": {
			config:       invalid,
			load:         acceptingRESTConfig,
			newClientset: acceptingClientset,
			want:         ErrInvalidConfiguration,
		},
		"NaN QPS configuration": {
			config:       nanQPS,
			load:         acceptingRESTConfig,
			newClientset: acceptingClientset,
			want:         ErrInvalidConfiguration,
		},
		"loader": {
			config:       valid,
			newClientset: acceptingClientset,
			want:         ErrInvalidConfiguration,
		},
		"client factory": {
			config: valid,
			load:   acceptingRESTConfig,
			want:   ErrInvalidConfiguration,
		},
		"nil REST config": {
			config: valid,
			load: func() (*rest.Config, error) {
				return nil, nil
			},
			newClientset: acceptingClientset,
			want:         ErrDependencyInitialization,
		},
		"nil clientset": {
			config: valid,
			load:   acceptingRESTConfig,
			newClientset: func(*rest.Config) (clientset.Interface, error) {
				return nil, nil
			},
			want: ErrDependencyInitialization,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := newKubernetesClients(
				context.Background(), test.config, test.load, test.newClientset,
			)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestNewKubernetesClientsPreservesCancellation(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	loadCalled := false
	load := func() (*rest.Config, error) {
		loadCalled = true

		return &rest.Config{}, nil
	}
	if _, err := newKubernetesClients(
		canceled, validKubernetesConfig(), load, acceptingClientset,
	); !errors.Is(err, context.Canceled) || loadCalled {
		t.Fatalf("error = %v, loader called = %t", err, loadCalled)
	}

	ctx, cancelDuringLoad := context.WithCancel(context.Background())
	load = func() (*rest.Config, error) {
		cancelDuringLoad()

		return &rest.Config{}, nil
	}
	if _, err := newKubernetesClients(
		ctx, validKubernetesConfig(), load, acceptingClientset,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}

func TestNewKubernetesClientsRedactsProviderErrors(t *testing.T) {
	secret := "/var/run/secrets/kubernetes.io/serviceaccount/token"
	for name, test := range map[string]struct {
		load         loadKubernetesRESTConfig
		newClientset newKubernetesClientset
	}{
		"loader": {
			load: func() (*rest.Config, error) {
				return nil, errors.New("could not read " + secret)
			},
			newClientset: acceptingClientset,
		},
		"client factory": {
			load: acceptingRESTConfig,
			newClientset: func(*rest.Config) (clientset.Interface, error) {
				return nil, errors.New("invalid bearer token from " + secret)
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := newKubernetesClients(
				context.Background(), validKubernetesConfig(), test.load, test.newClientset,
			)
			if !errors.Is(err, ErrDependencyInitialization) || strings.Contains(err.Error(), secret) ||
				strings.Contains(err.Error(), "bearer token") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func validKubernetesConfig() KubernetesConfig {
	return KubernetesConfig{
		Namespace:      "perf-runs",
		RequestTimeout: 10 * time.Second,
		QPS:            20,
		Burst:          30,
	}
}

func acceptingRESTConfig() (*rest.Config, error) {
	return &rest.Config{Host: "https://kubernetes.default.svc"}, nil
}

func acceptingClientset(*rest.Config) (clientset.Interface, error) {
	return fake.NewSimpleClientset(), nil
}
