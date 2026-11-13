package runcomparison

import (
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

func TestInputsRequireExactRequestPolicyAndCandidate(t *testing.T) {
	raw, err := contract.Files.ReadFile("snapshot/examples/create.json")
	if err != nil {
		t.Fatal(err)
	}
	var request run.Request
	if err := json.Unmarshal(raw, &request); err != nil {
		t.Fatal(err)
	}
	document := []byte(`{"apiVersion":"performance.perfeng.io/v1","kind":"PerformancePolicy","metadata":{"name":"checkout-policy","version":"1.0.0","owner":"tests"},"spec":{"mode":"inform","missingData":"inconclusive","rules":[{"id":"latency","metric":{"name":"api.http.duration","statistic":"p95","unit":"ms"},"slo":{"max":200}}]}}`)
	sum := sha256.Sum256(document)
	request.Policy.SHA256 = hex.EncodeToString(sum[:])
	current := run.Run{ID: "perf-20261113-080000-abcdef12", Request: request}
	inputs := Inputs{ContractsVersion: "0.29.0", Request: request, PolicyBytes: document, PolicyMode: "inform",
		Candidate: run.Artifact{ID: "2cfa0000-0000-4000-8000-000000000001", RunID: current.ID, Kind: "normalized", Format: "normalized-result/v1", MediaType: "application/json", URI: "s3://evidence/normalized.json", SHA256: strings.Repeat("a", 64), SizeBytes: 1},
		Workload:  rawresult.Identity{ID: "checkout-smoke", Version: "1.0.0", SHA256: strings.Repeat("b", 64)},
		Producer:  rawresult.Producer{Name: "perfeng-analysis", Version: "1.0.0", Image: "ghcr.io/example/analysis@sha256:" + strings.Repeat("c", 64)}}
	if err := inputs.Validate(current); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Inputs){
		"schema pin":           func(v *Inputs) { v.ContractsVersion = "latest" },
		"policy bytes":         func(v *Inputs) { v.PolicyBytes = append(v.PolicyBytes, ' ') },
		"request":              func(v *Inputs) { v.Request.Profile = "stress" },
		"candidate Run":        func(v *Inputs) { v.Candidate.RunID = "perf-20261113-080000-ffffffff" },
		"unexpected reference": func(v *Inputs) { v.Baselines = []policy.BaselineResolution{{ID: "unselected", Version: "1.0.0"}} },
	} {
		t.Run(name, func(t *testing.T) {
			changed := inputs.Clone()
			change(&changed)
			if err := changed.Validate(current); !errors.Is(err, run.ErrValidation) {
				t.Fatal("changed inputs accepted", err)
			}
		})
	}
	cloned := inputs.Clone()
	cloned.PolicyBytes[0] = 'x'
	if string(inputs.PolicyBytes) != string(document) {
		t.Fatal("clone aliases retained policy")
	}
}
