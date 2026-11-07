package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
)

func TestReceiptBindsLoadedBytes(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"openapi.json", "api/native-worker/v2/openapi.json"} {
		data, err := contract.Files.ReadFile("snapshot/" + name)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, name)
		if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var output bytes.Buffer
	if err := admit(root, &output); err != nil {
		t.Fatal(err)
	}
	var receipt struct {
		Status string            `json:"status"`
		Scope  string            `json:"scope"`
		SHA256 map[string]string `json:"sha256"`
	}
	if err := json.Unmarshal(output.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Status != "loader-compatible" || receipt.Scope != "run-native-retained-fixtures" || len(receipt.SHA256) != 2 {
		t.Fatal("incorrect admission scope", output.String())
	}
	for name, checksum := range receipt.SHA256 {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(data)
		if checksum != hex.EncodeToString(digest[:]) {
			t.Fatal("receipt does not bind input", name)
		}
	}
}

func TestCandidateRootRefusesEscapingSymlink(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "external.json")
	if err := os.WriteFile(outside, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "openapi.json")); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := admit(root, &output); err == nil {
		t.Fatal("candidate escaped declared root")
	}
	if output.Len() != 0 {
		t.Fatal("refused candidate emitted a receipt")
	}
}
