package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
)

func TestPinnedSnapshot(t *testing.T) {
	b, err := Files.ReadFile("snapshot/lock.json")
	if err != nil {
		t.Fatal(err)
	}
	var lock struct {
		Commit string
		SHA256 map[string]string
	}
	if err := json.Unmarshal(b, &lock); err != nil {
		t.Fatal(err)
	}
	if lock.Commit != "220140137a2e70367f3d6aa3bde8aede4d49c8b7" || len(lock.SHA256) != 3 {
		t.Fatal("unexpected contract provenance")
	}
	for name, expected := range lock.SHA256 {
		b, err := Files.ReadFile("snapshot/" + name)
		if err != nil {
			t.Fatal(err)
		}
		actual := sha256.Sum256(b)
		if hex.EncodeToString(actual[:]) != expected {
			t.Fatalf("snapshot checksum mismatch: %s", name)
		}
	}
}

func TestCreateFixtureAndStrictSchema(t *testing.T) {
	b, _ := Files.ReadFile("snapshot/examples/create.json")
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCreate(v); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(map[string]any){
		"unknown":        func(v map[string]any) { v["command"] = "sh" },
		"case":           func(v map[string]any) { v["TestSuite"] = v["testSuite"]; delete(v, "testSuite") },
		"missing":        func(v map[string]any) { delete(v, "policy") },
		"null":           func(v map[string]any) { v["candidate"] = nil },
		"profile":        func(v map[string]any) { v["profile"] = "nightly" },
		"floating":       func(v map[string]any) { v["candidate"].(map[string]any)["image"] = "ghcr.io/example/api:latest" },
		"version":        func(v map[string]any) { v["catalogue"].(map[string]any)["version"] = "1.0.0-rc1" },
		"hash":           func(v map[string]any) { v["policy"].(map[string]any)["sha256"] = "abcd" },
		"nested-unknown": func(v map[string]any) { v["environment"].(map[string]any)["url"] = "http://localhost" },
	} {
		t.Run(name, func(t *testing.T) {
			var copy map[string]any
			_ = json.Unmarshal(b, &copy)
			mutate(copy)
			if ValidateCreate(copy) == nil {
				t.Fatal("accepted invalid request")
			}
		})
	}
}
