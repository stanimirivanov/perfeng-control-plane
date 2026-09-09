package app

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/registry"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func TestNewRuntimeRegistryLoadsRootAndCrossChecksContracts(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "marker"), []byte("reviewed"), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded := &registry.ReportPolicyRegistry{}
	load := func(source fs.FS, manifest string) (*registry.ReportPolicyRegistry, error) {
		marker, err := fs.ReadFile(source, "marker")
		if err != nil || string(marker) != "reviewed" || manifest != "registry.json" {
			return nil, errors.New("unexpected registry source")
		}

		return loaded, nil
	}
	validate := func(candidate *registry.ReportPolicyRegistry, version string) error {
		if candidate != loaded || version != "0.12.0" {
			return run.ErrValidation
		}

		return nil
	}

	actual, err := newRuntimeRegistry(
		RegistryConfig{RootDirectory: root, Manifest: "registry.json"},
		ArtifactVerificationConfig{ContractsVersion: "0.12.0"},
		load,
		validate,
	)
	if err != nil || actual != loaded {
		t.Fatalf("NewRuntimeRegistry() = %#v, %v", actual, err)
	}
}

func TestNewRuntimeRegistryRejectsInvalidInputs(t *testing.T) {
	validConfig := RegistryConfig{RootDirectory: t.TempDir(), Manifest: "registry.json"}
	validVerification := ArtifactVerificationConfig{ContractsVersion: "0.12.0"}
	loaded := &registry.ReportPolicyRegistry{}
	load := func(fs.FS, string) (*registry.ReportPolicyRegistry, error) { return loaded, nil }
	validate := func(*registry.ReportPolicyRegistry, string) error { return nil }

	for name, build := range map[string]func() (*registry.ReportPolicyRegistry, error){
		"config": func() (*registry.ReportPolicyRegistry, error) {
			return newRuntimeRegistry(RegistryConfig{}, validVerification, load, validate)
		},
		"verification": func() (*registry.ReportPolicyRegistry, error) {
			return newRuntimeRegistry(validConfig, ArtifactVerificationConfig{}, load, validate)
		},
		"loader": func() (*registry.ReportPolicyRegistry, error) {
			return newRuntimeRegistry(validConfig, validVerification, nil, validate)
		},
		"validator": func() (*registry.ReportPolicyRegistry, error) {
			return newRuntimeRegistry(validConfig, validVerification, load, nil)
		},
		"load failure": func() (*registry.ReportPolicyRegistry, error) {
			return newRuntimeRegistry(
				validConfig,
				validVerification,
				func(fs.FS, string) (*registry.ReportPolicyRegistry, error) {
					return nil, errors.New("secret registry path and content")
				},
				validate,
			)
		},
		"nil registry": func() (*registry.ReportPolicyRegistry, error) {
			return newRuntimeRegistry(
				validConfig,
				validVerification,
				func(fs.FS, string) (*registry.ReportPolicyRegistry, error) { return nil, nil },
				validate,
			)
		},
		"contracts mismatch": func() (*registry.ReportPolicyRegistry, error) {
			return newRuntimeRegistry(
				validConfig,
				validVerification,
				load,
				func(*registry.ReportPolicyRegistry, string) error { return run.ErrValidation },
			)
		},
	} {
		t.Run(name, func(t *testing.T) {
			actual, err := build()
			if actual != nil || !errors.Is(err, ErrInvalidConfiguration) ||
				strings.Contains(err.Error(), "secret") {
				t.Fatalf("registry = %#v, error = %v", actual, err)
			}
		})
	}
}
