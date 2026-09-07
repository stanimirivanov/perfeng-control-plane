package app

import (
	"os"

	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
)

const contractsVersionVariable = "PERFENG_CONTRACTS_VERSION"

// ArtifactVerificationConfig pins evidence parsing to one contracts bundle.
type ArtifactVerificationConfig struct {
	ContractsVersion string
}

// Validate checks the expected producer contracts version.
func (config ArtifactVerificationConfig) Validate() error {
	if !rawresult.ValidContractsVersion(config.ContractsVersion) {
		return ErrInvalidConfiguration
	}

	return nil
}

// LoadArtifactVerificationConfig reads the expected producer contracts version.
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
