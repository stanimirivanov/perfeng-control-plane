package app

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	registryRootVariable     = "PERFENG_REGISTRY_ROOT"
	registryManifestVariable = "PERFENG_REGISTRY_MANIFEST"
)

// RegistryConfig identifies one mounted reviewed runtime-registry bundle.
type RegistryConfig struct {
	RootDirectory string
	Manifest      string
}

// Validate requires a canonical absolute bundle root and a portable relative
// manifest path contained by that filesystem root.
func (config RegistryConfig) Validate() error {
	if !validRequiredValue(config.RootDirectory) ||
		!filepath.IsAbs(config.RootDirectory) ||
		filepath.Clean(config.RootDirectory) != config.RootDirectory ||
		filepath.Dir(config.RootDirectory) == config.RootDirectory {
		return configurationError(registryRootVariable)
	}
	if !validRequiredValue(config.Manifest) || config.Manifest == "." ||
		!fs.ValidPath(config.Manifest) || strings.ContainsRune(config.Manifest, '\\') {
		return configurationError(registryManifestVariable)
	}

	return nil
}

// LoadRegistryConfig reads the mounted registry root and relative manifest path.
func LoadRegistryConfig() (RegistryConfig, error) {
	return loadRegistryConfig(os.LookupEnv)
}

func loadRegistryConfig(lookup lookupEnvironment) (RegistryConfig, error) {
	if lookup == nil {
		return RegistryConfig{}, configurationError("environment")
	}
	root, err := requiredValue(lookup, registryRootVariable)
	if err != nil {
		return RegistryConfig{}, err
	}
	manifest, err := requiredValue(lookup, registryManifestVariable)
	if err != nil {
		return RegistryConfig{}, err
	}
	config := RegistryConfig{RootDirectory: root, Manifest: manifest}
	if err := config.Validate(); err != nil {
		return RegistryConfig{}, err
	}

	return config, nil
}
