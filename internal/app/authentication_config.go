package app

import (
	"os"
	"path/filepath"
)

const authenticationFileVariable = "PERFENG_AUTHENTICATION_FILE"

// AuthenticationConfig identifies the mounted static credential document.
type AuthenticationConfig struct {
	CredentialFile string
}

// Validate rejects missing, relative and non-canonical credential paths.
func (config AuthenticationConfig) Validate() error {
	if !validRequiredValue(config.CredentialFile) ||
		!filepath.IsAbs(config.CredentialFile) ||
		filepath.Clean(config.CredentialFile) != config.CredentialFile ||
		filepath.Dir(config.CredentialFile) == config.CredentialFile {
		return configurationError(authenticationFileVariable)
	}

	return nil
}

// LoadAuthenticationConfig reads the mounted credential-document path.
func LoadAuthenticationConfig() (AuthenticationConfig, error) {
	return loadAuthenticationConfig(os.LookupEnv)
}

func loadAuthenticationConfig(lookup lookupEnvironment) (AuthenticationConfig, error) {
	if lookup == nil {
		return AuthenticationConfig{}, configurationError(authenticationFileVariable)
	}
	path, err := requiredValue(lookup, authenticationFileVariable)
	if err != nil {
		return AuthenticationConfig{}, err
	}
	config := AuthenticationConfig{CredentialFile: path}
	if err := config.Validate(); err != nil {
		return AuthenticationConfig{}, err
	}

	return config, nil
}
