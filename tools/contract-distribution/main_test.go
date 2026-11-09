package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestCompareRequiresExactAuthenticatedMember(t *testing.T) {
	for _, test := range []struct {
		name string
		data []byte
		want bool
	}{
		{"exact", []byte("reviewed"), true},
		{"substituted", []byte("different"), false},
		{"missing", nil, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := t.TempDir()
			if err := os.WriteFile(filepath.Join(snapshot, "openapi.json"), []byte("reviewed"), 0o600); err != nil {
				t.Fatal(err)
			}
			verified := map[string]string{}
			if test.data != nil {
				verified["api/run-management/v1/openapi.json"] = digest(test.data)
			}
			members := map[string]member{
				"api/run-management/v1/openapi.json": {SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte("reviewed"))), Destination: "openapi.json"},
			}
			if err := compare(snapshot, verified, members); (err == nil) != test.want {
				t.Fatalf("comparison = %v, want success %t", err, test.want)
			}
		})
	}
}

func TestSelectCoversSnapshotWithoutBorrowingAnotherFamily(t *testing.T) {
	pins := map[string]publisher{"nativeWorker": {Revision: "new"}, "runPolicy": {Revision: "old"}}
	snapshot := ledger{Commit: "old", NativeWorker: family{Commit: "new"}, SHA256: map[string]string{
		"openapi.json":                      "run",
		"api/native-worker/v2/openapi.json": "native",
	}}
	selections, err := selectFamilies(snapshot, pins)
	if err != nil {
		t.Fatal(err)
	}
	if got := selections["runPolicy"]["api/run-management/v1/openapi.json"].SHA256; got != "run" {
		t.Fatalf("Run selection = %q", got)
	}
	if got := selections["nativeWorker"]["api/native-worker/v2/openapi.json"].SHA256; got != "native" {
		t.Fatalf("Native selection = %q", got)
	}
	pins["nativeWorker"] = publisher{Revision: "old"}
	if _, err := selectFamilies(snapshot, pins); err == nil {
		t.Fatal("substituted family revision passed")
	}
}

func TestManifestMustNameExpectedReleaseAndSource(t *testing.T) {
	data := []byte(`{"releaseId":"reviewed","source":{"revision":"actual"}}`)
	for _, test := range []struct {
		name, release, revision string
		want                    bool
	}{
		{"expected", "reviewed", "actual", true},
		{"wrong source", "reviewed", "other", false},
		{"wrong release", "other", "actual", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := verifyManifest(data, test.release, publisher{Revision: test.revision, ManifestSHA256: digest(data)}); (got == nil) != test.want {
				t.Fatalf("manifest validation = %v, want success %t", got, test.want)
			}
		})
	}
	if err := verifyManifest(data, "reviewed", publisher{Revision: "actual", ManifestSHA256: "different"}); err == nil {
		t.Fatal("unreviewed manifest digest passed")
	}
}
