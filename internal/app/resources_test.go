package app

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	kubernetesfake "k8s.io/client-go/kubernetes/fake"

	"github.com/stanimirivanov/perfeng-control-plane/internal/authentication"
	"github.com/stanimirivanov/perfeng-control-plane/internal/objectstore"
	"github.com/stanimirivanov/perfeng-control-plane/internal/registry"
)

type startupDatabaseStub struct {
	Database
	closeCalls int
	closeErr   error
}

func (database *startupDatabaseStub) Close() error {
	database.closeCalls++

	return database.closeErr
}

func TestNewStartupResourcesAcquiresDatabaseLastAndClosesItOnce(t *testing.T) {
	config := validApplicationConfig(t)
	database := &startupDatabaseStub{}
	order := make([]string, 0, 6)
	builders := validStartupResourceBuilders(t, &order, database)

	resources, err := newStartupResources(context.Background(), config, builders)
	if err != nil {
		t.Fatal(err)
	}
	if resources.Database != database || resources.Authentication == nil ||
		resources.Registry == nil || resources.ArtifactStorage == nil ||
		resources.Kubernetes.Jobs == nil || resources.Kubernetes.Pods == nil ||
		resources.ExecutorConfig.Validate() != nil {
		t.Fatal("startup resources are incomplete")
	}
	want := []string{
		"authentication", "registry", "executor config",
		"artifact storage", "Kubernetes", "database",
	}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("acquisition order = %v, want %v", order, want)
	}
	if err := resources.Close(); err != nil {
		t.Fatal(err)
	}
	if err := resources.Close(); err != nil || database.closeCalls != 1 {
		t.Fatalf("second close error = %v, database close calls = %d", err, database.closeCalls)
	}
}

func TestNewStartupResourcesStopsBeforeDatabaseOnEarlierFailure(t *testing.T) {
	config := validApplicationConfig(t)
	database := &startupDatabaseStub{}
	order := make([]string, 0, 3)
	builders := validStartupResourceBuilders(t, &order, database)
	builders.executorConfig = func(KubernetesExecutorTemplateConfig) (KubernetesExecutorConfig, error) {
		order = append(order, "executor config")

		return KubernetesExecutorConfig{}, errors.New("secret mounted content")
	}

	resources, err := newStartupResources(context.Background(), config, builders)
	if resources != nil || !errors.Is(err, ErrInvalidConfiguration) ||
		strings.Contains(err.Error(), "secret") {
		t.Fatalf("resources = %v, error = %v", resources, err)
	}
	want := []string{"authentication", "registry", "executor config"}
	if !reflect.DeepEqual(order, want) || database.closeCalls != 0 {
		t.Fatalf("acquisition order = %v, database close calls = %d", order, database.closeCalls)
	}
}

func TestNewStartupResourcesClassifiesDependencyFailures(t *testing.T) {
	config := validApplicationConfig(t)
	database := &startupDatabaseStub{}
	builders := validStartupResourceBuilders(t, nil, database)
	builders.artifactStorage = func(context.Context, ArtifactStorageConfig) (*objectstore.S3ArtifactAccess, error) {
		return nil, errors.New("provider response contains secret")
	}

	resources, err := newStartupResources(context.Background(), config, builders)
	if resources != nil || !errors.Is(err, ErrDependencyInitialization) ||
		strings.Contains(err.Error(), "secret") || database.closeCalls != 0 {
		t.Fatalf("resources = %v, error = %v, database close calls = %d",
			resources, err, database.closeCalls)
	}
}

func TestNewStartupResourcesPreservesCancellation(t *testing.T) {
	config := validApplicationConfig(t)
	called := false
	builders := validStartupResourceBuilders(t, nil, &startupDatabaseStub{})
	builders.authentication = func(AuthenticationConfig) (*authentication.Static, error) {
		called = true

		return &authentication.Static{}, nil
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	resources, err := newStartupResources(canceled, config, builders)
	if resources != nil || !errors.Is(err, context.Canceled) || called {
		t.Fatalf("resources = %v, error = %v, builder called = %t", resources, err, called)
	}
}

func TestStartupResourcesCloseRedactsAndMemoizesFailure(t *testing.T) {
	database := &startupDatabaseStub{closeErr: errors.New("secret database shutdown detail")}
	resources := &StartupResources{Database: database}

	const callers = 8
	results := make(chan error, callers)
	var wait sync.WaitGroup
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			results <- resources.Close()
		}()
	}
	wait.Wait()
	close(results)

	for err := range results {
		if !errors.Is(err, ErrResourceShutdown) || strings.Contains(err.Error(), "secret") {
			t.Fatalf("close error = %v", err)
		}
	}
	if database.closeCalls != 1 {
		t.Fatalf("database close calls = %d", database.closeCalls)
	}
	var empty *StartupResources
	if err := empty.Close(); err != nil {
		t.Fatalf("nil resources close error = %v", err)
	}
}

func TestNewStartupResourcesRejectsInvalidComposition(t *testing.T) {
	valid := validApplicationConfig(t)
	builders := validStartupResourceBuilders(t, nil, &startupDatabaseStub{})
	if resources, err := newStartupResources(nil, valid, builders); resources != nil ||
		!errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("nil context resources = %v, error = %v", resources, err)
	}
	if resources, err := newStartupResources(
		context.Background(), ApplicationConfig{}, builders,
	); resources != nil || !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("invalid config resources = %v, error = %v", resources, err)
	}
	if resources, err := newStartupResources(
		context.Background(), valid, startupResourceBuilders{},
	); resources != nil || !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("invalid builders resources = %v, error = %v", resources, err)
	}
}

func validApplicationConfig(t *testing.T) ApplicationConfig {
	t.Helper()
	config, err := loadApplicationConfig(mapLookup(validApplicationEnvironment(t)))
	if err != nil {
		t.Fatal(err)
	}

	return config
}

func validStartupResourceBuilders(
	t *testing.T,
	order *[]string,
	database Database,
) startupResourceBuilders {
	t.Helper()
	executorConfig, err := LoadKubernetesExecutorConfig(writeExecutorJobs(t))
	if err != nil {
		t.Fatal(err)
	}
	record := func(name string) {
		if order != nil {
			*order = append(*order, name)
		}
	}

	return startupResourceBuilders{
		authentication: func(AuthenticationConfig) (*authentication.Static, error) {
			record("authentication")

			return &authentication.Static{}, nil
		},
		registry: func(RegistryConfig, ArtifactVerificationConfig) (*registry.ReportPolicyRegistry, error) {
			record("registry")

			return &registry.ReportPolicyRegistry{}, nil
		},
		executorConfig: func(KubernetesExecutorTemplateConfig) (KubernetesExecutorConfig, error) {
			record("executor config")

			return executorConfig, nil
		},
		artifactStorage: func(context.Context, ArtifactStorageConfig) (*objectstore.S3ArtifactAccess, error) {
			record("artifact storage")

			return &objectstore.S3ArtifactAccess{}, nil
		},
		kubernetes: func(_ context.Context, config KubernetesConfig) (KubernetesClients, error) {
			record("Kubernetes")
			clients := kubernetesfake.NewSimpleClientset()

			return KubernetesClients{
				Namespace: config.Namespace,
				Jobs:      clients.BatchV1().Jobs(config.Namespace),
				Pods:      clients.CoreV1().Pods(config.Namespace),
			}, nil
		},
		database: func(context.Context, DatabaseConfig) (Database, error) {
			record("database")

			return database, nil
		},
	}
}
