package app

import (
	"errors"
	"testing"
)

func TestLoadArtifactVerificationConfigReturnsPinnedVersion(t *testing.T) {
	config, err := loadArtifactVerificationConfig(mapLookup(map[string]string{
		contractsVersionVariable: "0.12.0",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if config.ContractsVersion != "0.12.0" {
		t.Fatalf("config = %#v", config)
	}
}

func TestLoadArtifactVerificationConfigRejectsInvalidEnvironment(t *testing.T) {
	for name, lookup := range map[string]lookupEnvironment{
		"nil":     nil,
		"missing": mapLookup(map[string]string{}),
		"latest": mapLookup(map[string]string{
			contractsVersionVariable: "latest",
		}),
		"padded": mapLookup(map[string]string{
			contractsVersionVariable: " 0.12.0",
		}),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := loadArtifactVerificationConfig(lookup); !errors.Is(err, ErrInvalidConfiguration) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestArtifactVerificationConfigValidatesProgrammaticValue(t *testing.T) {
	valid := ArtifactVerificationConfig{ContractsVersion: "0.12.0"}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (ArtifactVerificationConfig{}).Validate(); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("error = %v", err)
	}
}
