package app

import (
	"os"

	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
)

const contractsVersionVariable = "PERFENG_CONTRACTS_VERSION"

// RegistryContractsVersion opts into exact per-entry contracts versions rather
// than requiring a uniform version across the complete runtime registry.
const RegistryContractsVersion = "registry"

// ArtifactVerificationConfig selects registry-scoped evidence verification or
// preserves a uniform contracts-version constraint for an existing deployment.
type ArtifactVerificationConfig struct {
	ContractsVersion string
}

// Validate accepts the explicit registry mode or one exact contracts version.
func (config ArtifactVerificationConfig) Validate() error {
	if config.ContractsVersion != RegistryContractsVersion &&
		!rawresult.ValidContractsVersion(config.ContractsVersion) {
		return ErrInvalidConfiguration
	}

	return nil
}

// LoadArtifactVerificationConfig reads the evidence contracts selection policy.
func LoadArtifactVerificationConfig() (ArtifactVerificationConfig, error) {
	return loadArtifactVerificationConfig(os.LookupEnv)
}

func loadArtifactVerificationConfig(lookup lookupEnvironment) (ArtifactVerificationConfig, error) {
	if lookup == nil {
		return ArtifactVerificationConfig{}, configurationError("environment")
	}

	contractsVersion, err := requiredValue(lookup, contractsVersionVariable)
	if err != nil {
		return ArtifactVerificationConfig{}, err
	}
	config := ArtifactVerificationConfig{ContractsVersion: contractsVersion}
	if config.Validate() != nil {
		return ArtifactVerificationConfig{}, configurationError(contractsVersionVariable)
	}

	return config, nil
}
