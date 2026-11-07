package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func sourceFixture(t *testing.T) (string, string) {
	t.Helper()
	source, snapshot := t.TempDir(), t.TempDir()
	path := filepath.Join(source, "api/run-management/v1/openapi.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"openapi":"3.1.1"}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "--quiet"}, {"add", "."}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--quiet", "-m", "source"}} {
		if output, err := exec.Command("git", append([]string{"-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "-C", source}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git fixture: %v %s", err, output)
		}
	}
	commit, err := exec.Command("git", "-C", source, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	manifest := map[string]any{"commit": string(bytes.TrimSpace(commit)), "sha256": map[string]string{"openapi.json": hex.EncodeToString(digest[:])}}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(snapshot, "lock.json"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(snapshot, "openapi.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return source, snapshot
}

func TestAttestationBindsImmutableSourceAndRefusesCoordinatedRewrite(t *testing.T) {
	source, snapshot := sourceFixture(t)
	var output bytes.Buffer
	if err := attest(source, snapshot, &output); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(output.Bytes(), []byte(`"sourcePath":"api/run-management/v1/openapi.json"`)) {
		t.Fatal(output.String())
	}
	data := []byte(`{"openapi":"3.1.1","injected":true}`)
	if err := os.WriteFile(filepath.Join(snapshot, "openapi.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	lock, err := os.ReadFile(filepath.Join(snapshot, "lock.json"))
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err = json.Unmarshal(lock, &value); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	value["sha256"].(map[string]any)["openapi.json"] = hex.EncodeToString(digest[:])
	lock, err = json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(snapshot, "lock.json"), lock, 0o600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err = attest(source, snapshot, &output); err == nil {
		t.Fatal("source mismatch admitted")
	}
	if output.Len() != 0 {
		t.Fatal("source mismatch emitted receipt")
	}
}

func TestSourceMappingKeepsFamilyOverridesAndRenames(t *testing.T) {
	lock := ledger{Commit: "primary", NativeWorker: family{Commit: "native"}}
	for _, test := range []struct{ name, commit, source string }{
		{"openapi.json", "primary", "api/run-management/v1/openapi.json"},
		{"transitions.json", "primary", "api/run-management/v1/transitions.json"},
		{"examples/create.json", "primary", "api/run-management/v1/examples/create.json"},
		{"examples/policy/browser.json", "primary", "examples/policy/browser.json"},
		{"api/native-worker/v2/openapi.json", "native", "api/native-worker/v2/openapi.json"},
		{"schemas/performance-analysis/v1/schema.json", "analysis", "schemas/performance-analysis/v1/schema.json"},
	} {
		commit, path, err := fileSource(test.name, lock, map[string]string{"schemas/performance-analysis/v1/schema.json": "analysis"})
		if err != nil || commit != test.commit || path != test.source {
			t.Fatalf("incorrect source mapping for %s: %s %s %v", test.name, commit, path, err)
		}
	}
	for _, name := range []string{"../outside.json", "api/performance-integration/v1/openapi.json", "schemas/unselected/file.json", "https://example.com/schema.json"} {
		if _, _, err := fileSource(name, lock, nil); err == nil {
			t.Fatal("undeclared source mapping accepted", name)
		}
	}
}

func TestAttestationRefusesInvalidSourceClaims(t *testing.T) {
	for _, test := range []struct{ name, commit, path string }{
		{"abbreviated commit", "abcd1234", "openapi.json"},
		{"missing object", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "openapi.json"},
		{"undeclared path mapping", "", "unknown.json"},
	} {
		t.Run(test.name, func(t *testing.T) {
			source, snapshot := sourceFixture(t)
			encoded, err := os.ReadFile(filepath.Join(snapshot, "lock.json"))
			if err != nil {
				t.Fatal(err)
			}
			var lock map[string]any
			if err = json.Unmarshal(encoded, &lock); err != nil {
				t.Fatal(err)
			}
			if test.commit != "" {
				lock["commit"] = test.commit
			}
			if test.path != "openapi.json" {
				if err = os.Rename(filepath.Join(snapshot, "openapi.json"), filepath.Join(snapshot, test.path)); err != nil {
					t.Fatal(err)
				}
				checksums := lock["sha256"].(map[string]any)
				checksums[test.path] = checksums["openapi.json"]
				delete(checksums, "openapi.json")
			}
			encoded, err = json.Marshal(lock)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(snapshot, "lock.json"), encoded, 0o600); err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			if err = attest(source, snapshot, &output); err == nil {
				t.Fatal("invalid source claim admitted")
			}
			if output.Len() != 0 {
				t.Fatal("invalid source claim emitted receipt")
			}
		})
	}
}

func TestAttestationIgnoresInheritedGitDirectoryOverride(t *testing.T) {
	source, snapshot := sourceFixture(t)
	t.Setenv("GIT_DIR", t.TempDir())
	var output bytes.Buffer
	if err := attest(source, snapshot, &output); err != nil {
		t.Fatal(err)
	}
}

func TestAttestationRejectsAmbiguousLedgerJSON(t *testing.T) {
	source, snapshot := sourceFixture(t)
	if err := os.WriteFile(filepath.Join(snapshot, "lock.json"), []byte(`{"commit":"first","commit":"second","sha256":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := attest(source, snapshot, &output); err == nil {
		t.Fatal("duplicate source identity admitted")
	}
	if output.Len() != 0 {
		t.Fatal("ambiguous identity emitted receipt")
	}
}
