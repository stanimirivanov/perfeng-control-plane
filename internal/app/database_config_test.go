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

func TestReportCaptureConfigurationIsExplicitAndDefaultOff(t *testing.T) {
	values := map[string]string{databaseURLVariable: "postgres://example.invalid/db"}
	config, err := loadDatabaseConfig(mapLookup(values))
	if err != nil || config.CaptureReportInputs {
		t.Fatal("capture default changed", err)
	}
	values["PERFENG_REPORT_COMPARISON_CAPTURE_ENABLED"] = "true"
	config, err = loadDatabaseConfig(mapLookup(values))
	if err != nil || !config.CaptureReportInputs {
		t.Fatal("explicit capture missing", err)
	}
	values["PERFENG_REPORT_COMPARISON_CAPTURE_ENABLED"] = "maybe"
	if _, err := loadDatabaseConfig(mapLookup(values)); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatal("invalid capture toggle accepted", err)
	}
	if plans, err := composeReportInputs(false, nil, nil); err != nil || plans != nil {
		t.Fatal("disabled capture requires capabilities", err)
	}
	if _, err := composeReportInputs(true, nil, nil); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatal("enabled capture accepted missing store", err)
	}
}
