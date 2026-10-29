package integration

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/policy"
	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func analysisVerificationFixture(t *testing.T) (AnalysisVerification, run.Request) {
	t.Helper()
	raw, err := contract.Files.ReadFile("snapshot/examples/create.json")
	if err != nil {
		t.Fatal(err)
	}
	var request run.Request
	if err := json.Unmarshal(raw, &request); err != nil {
		t.Fatal(err)
	}
	policyBytes, err := contract.Files.ReadFile("snapshot/examples/policy/browser.json")
	if err != nil {
		t.Fatal(err)
	}
	document, err := policy.Parse(policyBytes)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(policyBytes)
	request.Policy = run.Reference{ID: document.Metadata.Name, Version: document.Metadata.Version, SHA256: hex.EncodeToString(digest[:])}

	return AnalysisVerification{PolicyBytes: policyBytes, ContractsVersion: "0.25.0",
		Producer: rawresult.Producer{Name: "perfeng-analysis", Version: "1.0.0", Image: "ghcr.io/example/analysis@sha256:" + strings.Repeat("f", 64)}}, request
}

func TestAnalysisVerificationValidatesExactReviewedInputs(t *testing.T) {
	evidence, request := analysisVerificationFixture(t)
	if err := evidence.Validate(request); err != nil {
		t.Fatal(err)
	}
	for name, alter := range map[string]func(*AnalysisVerification, *run.Request){
		"policy name":    func(_ *AnalysisVerification, r *run.Request) { r.Policy.ID += "-other" },
		"policy version": func(_ *AnalysisVerification, r *run.Request) { r.Policy.Version = "99.0.0" },
		"policy digest":  func(_ *AnalysisVerification, r *run.Request) { r.Policy.SHA256 = strings.Repeat("0", 64) },
		"exact bytes":    func(e *AnalysisVerification, _ *run.Request) { e.PolicyBytes = append(e.PolicyBytes, '\n') },
		"invalid policy": func(e *AnalysisVerification, r *run.Request) {
			e.PolicyBytes = []byte(`{}`)
			digest := sha256.Sum256(e.PolicyBytes)
			r.Policy.SHA256 = hex.EncodeToString(digest[:])
		},
		"oversized policy": func(e *AnalysisVerification, _ *run.Request) { e.PolicyBytes = bytes.Repeat([]byte(" "), (1<<20)+1) },
		"producer image":   func(e *AnalysisVerification, _ *run.Request) { e.Producer.Image = "latest" },
		"producer version": func(e *AnalysisVerification, _ *run.Request) { e.Producer.Version = "latest" },
		"unsupported producer": func(e *AnalysisVerification, _ *run.Request) {
			e.Producer.Artifact = &rawresult.ExecutionArtifact{}
		},
		"registry sentinel": func(e *AnalysisVerification, _ *run.Request) { e.ContractsVersion = "registry" },
		"missing version":   func(e *AnalysisVerification, _ *run.Request) { e.ContractsVersion = "" },
		"invalid request":   func(_ *AnalysisVerification, r *run.Request) { r.Candidate.Image = "latest" },
	} {
		t.Run(name, func(t *testing.T) {
			changed, candidate := evidence.Clone(), request
			alter(&changed, &candidate)
			if err := changed.Validate(candidate); !errors.Is(err, run.ErrValidation) || err.Error() != run.ErrValidation.Error() {
				t.Fatal("invalid input accepted or diagnostics exposed", err)
			}
		})
	}
	// Different whitespace is valid policy but requires its own exact byte pin.
	evidence.PolicyBytes = append(evidence.PolicyBytes, '\n')
	digest := sha256.Sum256(evidence.PolicyBytes)
	request.Policy.SHA256 = hex.EncodeToString(digest[:])
	if err := evidence.Validate(request); err != nil {
		t.Fatal("matching exact policy bytes rejected", err)
	}
}

func TestAnalysisVerificationCloneOwnsBytesAndNestedProducer(t *testing.T) {
	original, _ := analysisVerificationFixture(t)
	copy := original.Clone()
	if !bytes.Equal(copy.PolicyBytes, original.PolicyBytes) || !copy.Producer.Equal(original.Producer) {
		t.Fatal("clone changed reviewed evidence")
	}
	copy.PolicyBytes[0] = '!'
	if bytes.Equal(copy.PolicyBytes, original.PolicyBytes) {
		t.Fatal("clone aliases exact policy bytes")
	}
	// Clone preserves ownership even for unsupported producer variants; Validate
	// separately rejects them for analysis publication.
	original.Producer.Artifact = &rawresult.ExecutionArtifact{DependencyLock: &rawresult.DependencyLock{SHA256: strings.Repeat("a", 64)}}
	copy = original.Clone()
	copy.Producer.Artifact.DependencyLock.SHA256 = strings.Repeat("b", 64)
	if original.Producer.Equal(copy.Producer) {
		t.Fatal("clone aliases nested producer provenance")
	}
}
