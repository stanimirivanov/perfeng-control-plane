package app

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadApplicationConfigReturnsCompleteStartupPolicy(t *testing.T) {
	values := validApplicationEnvironment(t)

	config, err := loadApplicationConfig(mapLookup(values))
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	if config.Database.URL != values[databaseURLVariable] ||
		config.Authentication.CredentialFile != values[authenticationFileVariable] ||
		config.Registry.RootDirectory != values[registryRootVariable] ||
		config.KubernetesExecutorTemplates.AnalysisJobFile != values[analysisJobFileVariable] {
		t.Fatal("application config did not preserve all typed settings")
	}
	if config.Reconciliation.Provisioning.LeaseTTL != config.Worker.LeaseTTL {
		t.Fatalf("reconciliation lease = %s, worker lease = %s",
			config.Reconciliation.Provisioning.LeaseTTL, config.Worker.LeaseTTL)
	}
}

func TestLoadApplicationConfigUsesEveryConfigurationBoundary(t *testing.T) {
	for name, variable := range map[string]string{
		"authentication":        authenticationFileVariable,
		"database":              databaseURLVariable,
		"HTTP server":           httpAddressVariable,
		"worker":                workerIDVariable,
		"Kubernetes":            kubernetesNamespaceVariable,
		"artifact storage":      s3RegionVariable,
		"artifact verification": contractsVersionVariable,
		"registry":              registryRootVariable,
		"executor templates":    analysisJobFileVariable,
		"reconciliation":        validationAttemptTimeoutVariable,
	} {
		t.Run(name, func(t *testing.T) {
			values := validApplicationEnvironment(t)
			delete(values, variable)

			_, err := loadApplicationConfig(mapLookup(values))
			var configuration *ConfigurationError
			if !errors.As(err, &configuration) || configuration.Name != variable {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestLoadApplicationConfigDoesNotExposeSecretValues(t *testing.T) {
	values := validApplicationEnvironment(t)
	secret := "postgres://service:secret@example.invalid/database\ninvalid"
	values[databaseURLVariable] = secret

	_, err := loadApplicationConfig(mapLookup(values))
	if !errors.Is(err, ErrInvalidConfiguration) || strings.Contains(err.Error(), secret) ||
		strings.Contains(err.Error(), "service:secret") {
		t.Fatalf("error = %v", err)
	}
}

func TestApplicationConfigRejectsCrossPolicyMismatch(t *testing.T) {
	config, err := loadApplicationConfig(mapLookup(validApplicationEnvironment(t)))
	if err != nil {
		t.Fatal(err)
	}
	config.Reconciliation.Provisioning.LeaseTTL++

	if err := config.Validate(); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadApplicationConfigRejectsNilEnvironment(t *testing.T) {
	if _, err := loadApplicationConfig(nil); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("error = %v", err)
	}
}

func validApplicationEnvironment(t *testing.T) map[string]string {
	t.Helper()
	values := make(map[string]string)
	for _, source := range []map[string]string{
		validHTTPServerEnvironment(),
		validWorkerEnvironment(),
		validKubernetesEnvironment(),
		validArtifactStorageEnvironment(),
		validReconciliationEnvironment(),
	} {
		for name, value := range source {
			values[name] = value
		}
	}
	root := t.TempDir()
	values[authenticationFileVariable] = filepath.Join(root, "credentials.json")
	values[databaseURLVariable] = "postgres://service:secret@postgres/perfeng?sslmode=require"
	values[contractsVersionVariable] = "0.12.0"
	values[registryRootVariable] = root
	values[registryManifestVariable] = "registry.json"
	values[analysisJobFileVariable] = filepath.Join(root, "analysis.yaml")
	values[reportJobFileVariable] = filepath.Join(root, "report.yaml")

	return values
}
