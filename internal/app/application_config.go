package app

import (
	"os"

	"github.com/stanimirivanov/perfeng-control-plane/internal/worker"
)

// ApplicationConfig contains the complete environment-sourced startup policy.
// It contains no opened clients, parsed credentials or loaded registry/template content.
type ApplicationConfig struct {
	Authentication              AuthenticationConfig
	Database                    DatabaseConfig
	HTTPServer                  HTTPServerConfig
	Worker                      worker.Config
	Kubernetes                  KubernetesConfig
	ArtifactStorage             ArtifactStorageConfig
	ArtifactVerification        ArtifactVerificationConfig
	Registry                    RegistryConfig
	KubernetesExecutorTemplates KubernetesExecutorTemplateConfig
	Reconciliation              ReconciliationConfig
}

// Validate checks every startup policy and their worker/reconciliation relationship.
func (config ApplicationConfig) Validate() error {
	if config.Authentication.Validate() != nil ||
		config.Database.Validate() != nil ||
		config.HTTPServer.Validate() != nil ||
		config.Worker.Validate() != nil ||
		config.Kubernetes.Validate() != nil ||
		config.ArtifactStorage.Validate() != nil ||
		config.ArtifactVerification.Validate() != nil ||
		config.Registry.Validate() != nil ||
		config.KubernetesExecutorTemplates.Validate() != nil ||
		config.Reconciliation.Validate(config.Worker) != nil {
		return ErrInvalidConfiguration
	}

	return nil
}

// LoadApplicationConfig reads and validates the complete environment-sourced
// startup policy without opening files, database connections or network clients.
func LoadApplicationConfig() (ApplicationConfig, error) {
	return loadApplicationConfig(os.LookupEnv)
}

func loadApplicationConfig(lookup lookupEnvironment) (ApplicationConfig, error) {
	if lookup == nil {
		return ApplicationConfig{}, configurationError("environment")
	}

	authentication, err := loadAuthenticationConfig(lookup)
	if err != nil {
		return ApplicationConfig{}, err
	}
	database, err := loadDatabaseConfig(lookup)
	if err != nil {
		return ApplicationConfig{}, err
	}
	httpServer, err := loadHTTPServerConfig(lookup)
	if err != nil {
		return ApplicationConfig{}, err
	}
	workerConfig, err := loadWorkerConfig(lookup)
	if err != nil {
		return ApplicationConfig{}, err
	}
	kubernetes, err := loadKubernetesConfig(lookup)
	if err != nil {
		return ApplicationConfig{}, err
	}
	artifactStorage, err := loadArtifactStorageConfig(lookup)
	if err != nil {
		return ApplicationConfig{}, err
	}
	artifactVerification, err := loadArtifactVerificationConfig(lookup)
	if err != nil {
		return ApplicationConfig{}, err
	}
	registry, err := loadRegistryConfig(lookup)
	if err != nil {
		return ApplicationConfig{}, err
	}
	executorTemplates, err := loadKubernetesExecutorTemplateConfig(lookup)
	if err != nil {
		return ApplicationConfig{}, err
	}
	reconciliation, err := loadReconciliationConfig(lookup, workerConfig)
	if err != nil {
		return ApplicationConfig{}, err
	}

	config := ApplicationConfig{
		Authentication:              authentication,
		Database:                    database,
		HTTPServer:                  httpServer,
		Worker:                      workerConfig,
		Kubernetes:                  kubernetes,
		ArtifactStorage:             artifactStorage,
		ArtifactVerification:        artifactVerification,
		Registry:                    registry,
		KubernetesExecutorTemplates: executorTemplates,
		Reconciliation:              reconciliation,
	}
	if config.Validate() != nil {
		return ApplicationConfig{}, configurationError("application policy")
	}

	return config, nil
}
