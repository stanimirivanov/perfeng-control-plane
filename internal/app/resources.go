package app

import (
	"context"
	"errors"
	"sync"

	"github.com/stanimirivanov/perfeng-control-plane/internal/authentication"
	"github.com/stanimirivanov/perfeng-control-plane/internal/nativeworker"
	"github.com/stanimirivanov/perfeng-control-plane/internal/objectstore"
	"github.com/stanimirivanov/perfeng-control-plane/internal/registry"
)

// ErrResourceShutdown classifies failure to release an owned startup resource
// without exposing provider details.
var ErrResourceShutdown = errors.New("runtime resource shutdown failed")

// StartupResources contains the external and file-backed dependencies needed
// for inert service composition. It owns Database and must not be copied.
type StartupResources struct {
	Authentication     *authentication.Static
	Database           Database
	Registry           *registry.ReportPolicyRegistry
	ArtifactStorage    *objectstore.S3ArtifactAccess
	NativeUploadSigner nativeworker.UploadSigner
	Kubernetes         KubernetesClients
	ExecutorConfig     KubernetesExecutorConfig

	closeOnce sync.Once
	closeErr  error
}

type startupResourceBuilders struct {
	authentication func(AuthenticationConfig) (*authentication.Static, error)
	registry       func(
		RegistryConfig,
		ArtifactVerificationConfig,
	) (*registry.ReportPolicyRegistry, error)
	executorConfig func(
		KubernetesExecutorTemplateConfig,
	) (KubernetesExecutorConfig, error)
	artifactStorage func(context.Context, ArtifactStorageConfig) (*ArtifactStorageResources, error)
	kubernetes      func(context.Context, KubernetesConfig) (KubernetesClients, error)
	database        func(context.Context, DatabaseConfig) (Database, error)
}

// NewStartupResources loads bounded mounted content, constructs inert clients
// and opens PostgreSQL last so no later startup failure can leak the connection.
func NewStartupResources(ctx context.Context, config ApplicationConfig) (*StartupResources, error) {
	return newStartupResources(ctx, config, startupResourceBuilders{
		authentication:  NewAuthentication,
		registry:        NewRuntimeRegistry,
		executorConfig:  LoadKubernetesExecutorConfig,
		artifactStorage: NewArtifactStorage,
		kubernetes:      NewKubernetesClients,
		database:        NewDatabase,
	})
}

func newStartupResources(
	ctx context.Context,
	config ApplicationConfig,
	builders startupResourceBuilders,
) (*StartupResources, error) {
	if ctx == nil || config.Validate() != nil || !builders.valid() {
		return nil, ErrInvalidConfiguration
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	authenticator, err := builders.authentication(config.Authentication)
	if err != nil {
		return nil, ErrInvalidConfiguration
	}
	if authenticator == nil {
		return nil, ErrDependencyInitialization
	}
	loadedRegistry, err := builders.registry(config.Registry, config.ArtifactVerification)
	if err != nil {
		return nil, ErrInvalidConfiguration
	}
	if loadedRegistry == nil {
		return nil, ErrDependencyInitialization
	}
	executorConfig, err := builders.executorConfig(config.KubernetesExecutorTemplates)
	if err != nil {
		return nil, ErrInvalidConfiguration
	}
	if executorConfig.Validate() != nil {
		return nil, ErrDependencyInitialization
	}
	artifactStorage, err := builders.artifactStorage(ctx, config.ArtifactStorage)
	if err != nil {
		return nil, classifyDependencyInitialization(ctx, err)
	}
	if artifactStorage == nil || artifactStorage.Access == nil || artifactStorage.UploadSigner == nil {
		return nil, ErrDependencyInitialization
	}
	kubernetes, err := builders.kubernetes(ctx, config.Kubernetes)
	if err != nil {
		return nil, classifyDependencyInitialization(ctx, err)
	}
	if kubernetes.Namespace != config.Kubernetes.Namespace ||
		kubernetes.Jobs == nil || kubernetes.Pods == nil {
		return nil, ErrDependencyInitialization
	}
	database, err := builders.database(ctx, config.Database)
	if err != nil {
		return nil, classifyDependencyInitialization(ctx, err)
	}
	if database == nil {
		return nil, ErrDependencyInitialization
	}

	return &StartupResources{
		Authentication:     authenticator,
		Database:           database,
		Registry:           loadedRegistry,
		ArtifactStorage:    artifactStorage.Access,
		NativeUploadSigner: artifactStorage.UploadSigner,
		Kubernetes:         kubernetes,
		ExecutorConfig:     executorConfig,
	}, nil
}

func (builders startupResourceBuilders) valid() bool {
	return builders.authentication != nil && builders.registry != nil &&
		builders.executorConfig != nil && builders.artifactStorage != nil &&
		builders.kubernetes != nil && builders.database != nil
}

// Close releases the owned database exactly once. Concurrent calls receive the
// same value-free result; clients without close semantics require no action.
func (resources *StartupResources) Close() error {
	if resources == nil {
		return nil
	}
	resources.closeOnce.Do(func() {
		if resources.Database != nil && resources.Database.Close() != nil {
			resources.closeErr = ErrResourceShutdown
		}
	})

	return resources.closeErr
}
