package run

import (
	"strings"
	"testing"
)

func TestArtifactValidation(t *testing.T) {
	valid := Artifact{ID: "1cfa0000-0000-4000-8000-000000000001", RunID: "perf-20260903-120000-12345678",
		Kind: "raw", URI: "s3://perfeng-artifacts/runs/example/summary.json", SHA256: strings.Repeat("a", 64),
		SizeBytes: 42, MediaType: "application/json", Format: "k6-summary/v1"}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Artifact){
		"ID":          func(a *Artifact) { a.ID = "not-a-uuid" },
		"run":         func(a *Artifact) { a.RunID = "" },
		"kind":        func(a *Artifact) { a.Kind = "verdict" },
		"hash":        func(a *Artifact) { a.SHA256 = "abcd" },
		"size":        func(a *Artifact) { a.SizeBytes = -1 },
		"media":       func(a *Artifact) { a.MediaType = "json" },
		"format":      func(a *Artifact) { a.Format = "" },
		"credentials": func(a *Artifact) { a.URI = "https://user:secret@example.com/result.json" },
		"presigned":   func(a *Artifact) { a.URI = "https://example.com/result?token=secret" },
		"local":       func(a *Artifact) { a.URI = "file:///tmp/result.json" },
		"empty-host":  func(a *Artifact) { a.URI = "https:///result.json" },
		"fragment":    func(a *Artifact) { a.URI += "#part" },
	} {
		t.Run(name, func(t *testing.T) {
			a := valid
			mutate(&a)
			if a.Validate() == nil {
				t.Fatal("invalid artifact accepted")
			}
		})
	}
}
