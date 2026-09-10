package app

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"testing"
)

func TestNewApplicationRuntimeComposesCompleteInertService(t *testing.T) {
	config := validApplicationConfig(t)
	resources := validRuntimeResourcesForTest(t, config)
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))

	runtime, err := NewApplicationRuntime(config, resources, logger)
	if err != nil {
		t.Fatal(err)
	}
	server, ok := runtime.server.(*http.Server)
	if !ok || server.Addr != config.HTTPServer.Address ||
		runtime.worker == nil || runtime.shutdownTimeout != config.HTTPServer.ShutdownTimeout {
		t.Fatal("application runtime is incomplete")
	}
	if output.Len() != 0 {
		t.Fatal("runtime construction emitted a log record")
	}
}

func TestNewApplicationRuntimeRejectsIncompleteResources(t *testing.T) {
	for name, invalidate := range map[string]func(*StartupResources){
		"authentication": func(resources *StartupResources) {
			resources.Authentication = nil
		},
		"database": func(resources *StartupResources) {
			resources.Database = nil
		},
		"registry": func(resources *StartupResources) {
			resources.Registry = nil
		},
		"artifact storage": func(resources *StartupResources) {
			resources.ArtifactStorage = nil
		},
		"Kubernetes namespace": func(resources *StartupResources) {
			resources.Kubernetes.Namespace = "different"
		},
		"Kubernetes Jobs": func(resources *StartupResources) {
			resources.Kubernetes.Jobs = nil
		},
		"Kubernetes Pods": func(resources *StartupResources) {
			resources.Kubernetes.Pods = nil
		},
		"executor config": func(resources *StartupResources) {
			resources.ExecutorConfig = KubernetesExecutorConfig{}
		},
	} {
		t.Run(name, func(t *testing.T) {
			config := validApplicationConfig(t)
			resources := validRuntimeResourcesForTest(t, config)
			invalidate(resources)

			runtime, err := NewApplicationRuntime(config, resources, testLogger())
			if runtime != nil || !errors.Is(err, ErrInvalidConfiguration) {
				t.Fatalf("runtime = %v, error = %v", runtime, err)
			}
		})
	}
}

func TestNewApplicationRuntimeRejectsInvalidInputs(t *testing.T) {
	config := validApplicationConfig(t)
	resources := validRuntimeResourcesForTest(t, config)
	for name, build := range map[string]func() (*Runtime, error){
		"configuration": func() (*Runtime, error) {
			return NewApplicationRuntime(ApplicationConfig{}, resources, testLogger())
		},
		"resources": func() (*Runtime, error) {
			return NewApplicationRuntime(config, nil, testLogger())
		},
		"logger": func() (*Runtime, error) {
			return NewApplicationRuntime(config, resources, nil)
		},
	} {
		t.Run(name, func(t *testing.T) {
			runtime, err := build()
			if runtime != nil || !errors.Is(err, ErrInvalidConfiguration) {
				t.Fatalf("runtime = %v, error = %v", runtime, err)
			}
		})
	}
}

func validRuntimeResourcesForTest(
	t *testing.T,
	config ApplicationConfig,
) *StartupResources {
	t.Helper()
	resources, err := newStartupResources(
		t.Context(),
		config,
		validStartupResourceBuilders(t, nil, &startupDatabaseStub{}),
	)
	if err != nil {
		t.Fatal(err)
	}

	return resources
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
}
