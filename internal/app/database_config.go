package app

import "os"

const databaseURLVariable = "PERFENG_DATABASE_URL"

// DatabaseConfig contains the secret PostgreSQL connection string. Callers
// must never log or include this value in errors.
type DatabaseConfig struct {
	URL string
}

// Validate checks the database setting without parsing or exposing its components.
func (config DatabaseConfig) Validate() error {
	if !validRequiredValue(config.URL) {
		return ErrInvalidConfiguration
	}

	return nil
}

// LoadDatabaseConfig reads the PostgreSQL connection setting without parsing
// it into components that might later be exposed by an error.
func LoadDatabaseConfig() (DatabaseConfig, error) {
	return loadDatabaseConfig(os.LookupEnv)
}

func loadDatabaseConfig(lookup lookupEnvironment) (DatabaseConfig, error) {
	if lookup == nil {
		return DatabaseConfig{}, configurationError("environment")
	}

	databaseURL, err := requiredValue(lookup, databaseURLVariable)
	if err != nil {
		return DatabaseConfig{}, err
	}
	config := DatabaseConfig{URL: databaseURL}
	if config.Validate() != nil {
		return DatabaseConfig{}, configurationError(databaseURLVariable)
	}

	return config, nil
}
