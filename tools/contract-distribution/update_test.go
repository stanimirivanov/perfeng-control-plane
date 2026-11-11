package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteCandidatePreservesBytesAndRefusesExistingOrUnsafeDestination(t *testing.T) {
	target := filepath.Join(t.TempDir(), "candidate")
	files := map[string][]byte{"lock.json": []byte("reviewed ledger"), "api/native/openapi.json": []byte("native bytes")}
	if err := writeCandidate(target, files); err != nil {
		t.Fatal(err)
	}
	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(target, name))
		if err != nil || string(got) != string(want) {
			t.Fatalf("candidate %s = %q, %v", name, got, err)
		}
	}
	if err := writeCandidate(target, files); err == nil {
		t.Fatal("existing candidate was overwritten")
	}
	invalid := filepath.Join(t.TempDir(), "invalid")
	if err := writeCandidate(invalid, map[string][]byte{"../escape.json": []byte("escape")}); err == nil {
		t.Fatal("unsafe candidate path was accepted")
	}
	if _, err := os.Stat(invalid); !os.IsNotExist(err) {
		t.Fatalf("invalid candidate was created: %v", err)
	}
}

func TestCompareRetainedRefusesMissingChangedAndExtraFiles(t *testing.T) {
	expected := map[string][]byte{"lock.json": []byte("ledger"), "schema.json": []byte("schema")}
	for _, change := range []string{"none", "missing", "changed", "extra", "symlink"} {
		t.Run(change, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "snapshot")
			if err := writeCandidate(root, expected); err != nil {
				t.Fatal(err)
			}
			var err error
			switch change {
			case "missing":
				err = os.Remove(filepath.Join(root, "schema.json"))
			case "changed":
				err = os.WriteFile(filepath.Join(root, "schema.json"), []byte("different"), 0o600)
			case "extra":
				err = os.WriteFile(filepath.Join(root, "extra.json"), []byte("extra"), 0o600)
			case "symlink":
				if err = os.Remove(filepath.Join(root, "schema.json")); err == nil {
					err = os.Symlink("lock.json", filepath.Join(root, "schema.json"))
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := compareRetained(root, expected); (err == nil) != (change == "none") {
				t.Fatalf("retained comparison = %v", err)
			}
		})
	}
}
