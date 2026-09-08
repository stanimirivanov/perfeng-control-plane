package main

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/registry"
)

func TestRunValidatesRelativeManifestWithinRoot(t *testing.T) {
	root := t.TempDir()
	marker := []byte("reviewed")
	if err := os.WriteFile(filepath.Join(root, "marker"), marker, 0o600); err != nil {
		t.Fatal(err)
	}
	called := false
	load := func(source fs.FS, manifest string) (*registry.ReportPolicyRegistry, error) {
		called = true
		data, err := fs.ReadFile(source, "marker")
		if err != nil || string(data) != string(marker) || manifest != "config/registry.json" {
			t.Fatal("loader received changed filesystem or manifest")
		}

		return &registry.ReportPolicyRegistry{}, nil
	}
	var stdout, stderr bytes.Buffer
	code := run(
		[]string{"-root", root, "-manifest", "config/registry.json"},
		&stdout,
		&stderr,
		load,
	)
	if code != 0 || !called || stdout.String() != "Registry bundle is valid.\n" || stderr.Len() != 0 {
		t.Fatalf("code=%d called=%v stdout=%q stderr=%q", code, called, stdout.String(), stderr.String())
	}
}

func TestRunRejectsInvalidArgumentsWithoutLoading(t *testing.T) {
	load := func(fs.FS, string) (*registry.ReportPolicyRegistry, error) {
		t.Fatal("invalid arguments reached loader")
		return nil, nil
	}
	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "missing root"},
		{name: "missing manifest", args: []string{"-root", ".", "-manifest", ""}},
		{name: "positional argument", args: []string{"-root", ".", "extra"}},
		{name: "unknown flag", args: []string{"-unknown"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(test.args, &stdout, &stderr, load); code != 2 {
				t.Fatalf("code = %d", code)
			}
			if stdout.Len() != 0 || stderr.Len() == 0 {
				t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
		})
	}
}

func TestRunReportsValidationFailureWithoutConfigurationValues(t *testing.T) {
	root := t.TempDir()
	load := func(fs.FS, string) (*registry.ReportPolicyRegistry, error) {
		return nil, errors.New("sensitive parser detail")
	}
	var stdout, stderr bytes.Buffer
	code := run(
		[]string{"-root", root, "-manifest", "secret-name.json"},
		&stdout,
		&stderr,
		load,
	)
	if code != 1 || stdout.Len() != 0 ||
		stderr.String() != "Registry bundle validation failed.\n" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	for _, secret := range []string{root, "secret-name.json", "sensitive parser detail"} {
		if strings.Contains(stderr.String(), secret) {
			t.Fatalf("stderr contains configuration value %q", secret)
		}
	}
}

func TestRunHelpDoesNotLoad(t *testing.T) {
	load := func(fs.FS, string) (*registry.ReportPolicyRegistry, error) {
		t.Fatal("help reached loader")
		return nil, nil
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-help"}, &stdout, &stderr, load); code != 0 {
		t.Fatalf("code = %d", code)
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "Usage of validate-registry") {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}
