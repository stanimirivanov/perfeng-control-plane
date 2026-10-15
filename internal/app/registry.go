package app

import (
	"io/fs"
	"os"

	"github.com/stanimirivanov/perfeng-control-plane/internal/registry"
)

type loadReportPolicyRegistry func(fs.FS, string) (*registry.ReportPolicyRegistry, error)
type validateRegistryContracts func(*registry.ReportPolicyRegistry, string) error

// NewRuntimeRegistry loads one reviewed bundle. Registry mode preserves each
// entry's exact contracts version; an explicit version requires uniform entries.
func NewRuntimeRegistry(
	config RegistryConfig,
	verification ArtifactVerificationConfig,
) (*registry.ReportPolicyRegistry, error) {
	return newRuntimeRegistry(
		config,
		verification,
		registry.LoadReportPolicyRegistry,
		func(loaded *registry.ReportPolicyRegistry, version string) error {
			return loaded.ValidateContractsVersion(version)
		},
	)
}

func newRuntimeRegistry(
	config RegistryConfig,
	verification ArtifactVerificationConfig,
	load loadReportPolicyRegistry,
	validateContracts validateRegistryContracts,
) (*registry.ReportPolicyRegistry, error) {
	if config.Validate() != nil || verification.Validate() != nil ||
		load == nil || validateContracts == nil {
		return nil, ErrInvalidConfiguration
	}
	loaded, err := load(os.DirFS(config.RootDirectory), config.Manifest)
	if err != nil || loaded == nil {
		return nil, ErrInvalidConfiguration
	}
	if verification.ContractsVersion != RegistryContractsVersion {
		if err := validateContracts(loaded, verification.ContractsVersion); err != nil {
			return nil, ErrInvalidConfiguration
		}
	}

	return loaded, nil
}
