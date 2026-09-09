package app

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRegistryConfigReturnsMountedBundleLocation(t *testing.T) {
	root := t.TempDir()
	values := map[string]string{
		registryRootVariable: root, registryManifestVariable: "config/registry.json",
	}

	config, err := loadRegistryConfig(mapLookup(values))
	if err != nil {
		t.Fatal(err)
	}
	if config.RootDirectory != root || config.Manifest != "config/registry.json" {
		t.Fatalf("config = %#v", config)
	}
}

func TestLoadRegistryConfigRejectsInvalidEnvironment(t *testing.T) {
	root := t.TempDir()
	nonCanonicalRoot := filepath.Dir(root) + string(filepath.Separator) + "nested" +
		string(filepath.Separator) + ".." + string(filepath.Separator) + filepath.Base(root)
	for name, mutate := range map[string]func(map[string]string){
		"missing root":  func(values map[string]string) { delete(values, registryRootVariable) },
		"relative root": func(values map[string]string) { values[registryRootVariable] = "bundle" },
		"padded root":   func(values map[string]string) { values[registryRootVariable] = " " + root },
		"non-canonical root": func(values map[string]string) {
			values[registryRootVariable] = nonCanonicalRoot
		},
		"missing manifest": func(values map[string]string) { delete(values, registryManifestVariable) },
		"dot manifest":     func(values map[string]string) { values[registryManifestVariable] = "." },
		"parent manifest": func(values map[string]string) {
			values[registryManifestVariable] = "../registry.json"
		},
		"absolute manifest": func(values map[string]string) {
			values[registryManifestVariable] = "/registry.json"
		},
		"backslash manifest": func(values map[string]string) {
			values[registryManifestVariable] = `config\\registry.json`
		},
	} {
		t.Run(name, func(t *testing.T) {
			values := map[string]string{
				registryRootVariable: root, registryManifestVariable: "registry.json",
			}
			mutate(values)
			_, err := loadRegistryConfig(mapLookup(values))
			if !errors.Is(err, ErrInvalidConfiguration) {
				t.Fatalf("error = %v", err)
			}
			for _, value := range values {
				if value != "" && strings.Contains(err.Error(), value) {
					t.Fatalf("error discloses configured value: %v", err)
				}
			}
		})
	}
}

func TestRegistryConfigRejectsNilEnvironment(t *testing.T) {
	if _, err := loadRegistryConfig(nil); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("error = %v", err)
	}
}
