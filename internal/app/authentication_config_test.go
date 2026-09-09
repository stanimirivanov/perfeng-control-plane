package app

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAuthenticationConfigReturnsCanonicalAbsolutePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")

	config, err := loadAuthenticationConfig(mapLookup(map[string]string{
		authenticationFileVariable: path,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if config.CredentialFile != path {
		t.Fatalf("CredentialFile = %q, want %q", config.CredentialFile, path)
	}
}

func TestLoadAuthenticationConfigRejectsInvalidEnvironment(t *testing.T) {
	absolute := filepath.Join(t.TempDir(), "credentials.json")
	for name, value := range map[string]string{
		"missing":  "",
		"relative": "credentials.json",
		"padded":   " " + absolute,
		"non-canonical": filepath.Dir(absolute) + string(filepath.Separator) + "nested" +
			string(filepath.Separator) + ".." + string(filepath.Separator) + filepath.Base(absolute),
	} {
		t.Run(name, func(t *testing.T) {
			values := map[string]string{authenticationFileVariable: value}
			if name == "missing" {
				delete(values, authenticationFileVariable)
			}

			_, err := loadAuthenticationConfig(mapLookup(values))
			if !errors.Is(err, ErrInvalidConfiguration) ||
				(value != "" && strings.Contains(err.Error(), value)) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestAuthenticationConfigRejectsNilEnvironment(t *testing.T) {
	if _, err := loadAuthenticationConfig(nil); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("error = %v", err)
	}
}
