package app

import (
	"errors"
	"strings"
	"testing"
)

func TestLoadDatabaseConfigReturnsSecretWithoutParsingIt(t *testing.T) {
	databaseURL := "postgres://service:secret@postgres/perfeng?sslmode=require"

	config, err := loadDatabaseConfig(mapLookup(map[string]string{
		databaseURLVariable: databaseURL,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if config.URL != databaseURL {
		t.Fatal("database URL changed during configuration loading")
	}
}

func TestLoadDatabaseConfigRejectsMissingOrMalformedSecret(t *testing.T) {
	for name, values := range map[string]map[string]string{
		"missing": {},
		"empty":   {databaseURLVariable: ""},
		"padded":  {databaseURLVariable: " postgres://postgres/perfeng"},
		"control": {databaseURLVariable: "postgres://postgres/perfeng\nsecret"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := loadDatabaseConfig(mapLookup(values))
			databaseURL := values[databaseURLVariable]
			if !errors.Is(err, ErrInvalidConfiguration) ||
				(databaseURL != "" && strings.Contains(err.Error(), databaseURL)) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestLoadDatabaseConfigRejectsNilEnvironment(t *testing.T) {
	if _, err := loadDatabaseConfig(nil); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("error = %v", err)
	}
}
