package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/reconcile"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func reportPolicyFixture(t *testing.T) (ReportPolicyEntry, run.Run, reconcile.ReportingInput) {
	t.Helper()
	policyBytes, err := contract.Files.ReadFile("snapshot/examples/policy/browser.json")
	if err != nil {
		t.Fatal(err)
	}
	policyBytes = []byte(strings.Replace(
		string(policyBytes), `"name": "search-browser"`, `"name": "search-policy"`, 1,
	))
	digest := sha256.Sum256(policyBytes)
	catalogue := run.Reference{
		ID: "application-tests", Version: "1.0.0", SHA256: strings.Repeat("a", 64),
	}
	environment := baseline.Environment{
		Identity: rawresult.Identity{
			ID: "local-kind", Version: "1.0.0", SHA256: strings.Repeat("d", 64),
		},
		Fingerprint: strings.Repeat("e", 64),
	}
	seed := int64(42)
	entry := ReportPolicyEntry{
		PolicyBytes: policyBytes, TestID: "search-browser",
		Catalogue: catalogue, Profile: "smoke",
		Producer: rawresult.Producer{
			Name: "perfeng-analysis", Version: "1.0.0",
			Image: "ghcr.io/example/perfeng-analysis@sha256:" + strings.Repeat("f", 64),
		},
		Workload: rawresult.Identity{
			ID: "browser-smoke", Version: "1.0.0", SHA256: strings.Repeat("b", 64),
		},
		Environment: environment,
		Dataset: baseline.Dataset{
			Kind: "versioned", ID: "search-data", Version: "1.0.0",
			SHA256: strings.Repeat("c", 64), Seed: &seed,
		},
		Principals: []string{"alice"},
	}
	current := run.Run{
		ID: "perf-20260905-120000-12345678", State: run.StateReporting, Revision: 7,
		Request: run.Request{
			TestSuite: "search-browser", Catalogue: catalogue, Profile: "smoke",
			Candidate: run.Candidate{
				GitSHA: strings.Repeat("1", 40),
				Image:  "ghcr.io/example/search@sha256:" + strings.Repeat("2", 64),
			},
			Environment: run.Reference{
				ID: environment.ID, Version: environment.Version, SHA256: environment.SHA256,
			},
			Policy: run.Reference{
				ID: "search-policy", Version: "1.0.0",
				SHA256: hex.EncodeToString(digest[:]),
			},
		},
	}
	input := reconcile.ReportingInput{Candidate: run.Artifact{
		ID: "11111111-1111-4111-8111-111111111111", RunID: current.ID,
		Kind: "normalized", URI: "s3://perfeng/runs/" + current.ID + "/normalized.json",
		SHA256: strings.Repeat("3", 64), SizeBytes: 100,
		MediaType: "application/json", Format: "normalized-result/v1",
	}}
	if current.Request.Validate() != nil || input.Candidate.Validate() != nil {
		t.Fatal("invalid registry test fixture")
	}

	return entry, current, input
}

func TestReportPolicyRegistryResolvesExactTrust(t *testing.T) {
	entry, current, input := reportPolicyFixture(t)
	registry, err := NewReportPolicyRegistry([]ReportPolicyEntry{entry})
	if err != nil {
		t.Fatal(err)
	}

	trust, err := registry.ResolveReportTrust(context.Background(), "alice", current, input)
	if err != nil {
		t.Fatal(err)
	}
	if string(trust.PolicyBytes) != string(entry.PolicyBytes) || trust.PolicyMode != "inform" ||
		trust.Producer != entry.Producer || len(trust.Baselines) != 1 {
		t.Fatalf("trust = %#v", trust)
	}
	selection := trust.Baselines[0]
	if selection.ID != "approved-search-browser" || selection.Version != "1.0.0" ||
		selection.TestID != current.Request.TestSuite || selection.Workload != entry.Workload ||
		selection.Environment != entry.Environment || selection.Validate() != nil ||
		selection.Dataset.Seed == nil || *selection.Dataset.Seed != 42 {
		t.Fatalf("selection = %#v", selection)
	}
}

func TestReportPolicyRegistryOwnsEntryAndResultData(t *testing.T) {
	entry, current, input := reportPolicyFixture(t)
	registry, err := NewReportPolicyRegistry([]ReportPolicyEntry{entry})
	if err != nil {
		t.Fatal(err)
	}
	entry.PolicyBytes[0] = 'x'
	*entry.Dataset.Seed = 7

	first, err := registry.ResolveReportTrust(context.Background(), "alice", current, input)
	if err != nil {
		t.Fatal(err)
	}
	first.PolicyBytes[0] = 'y'
	*first.Baselines[0].Dataset.Seed = 8
	second, err := registry.ResolveReportTrust(context.Background(), "alice", current, input)
	if err != nil {
		t.Fatal(err)
	}
	if second.PolicyBytes[0] != '{' || *second.Baselines[0].Dataset.Seed != 42 {
		t.Fatal("registry data was mutated through caller-owned storage")
	}
}

func TestReportPolicyRegistryRejectsUnapprovedContext(t *testing.T) {
	for _, test := range []struct {
		name      string
		principal string
		mutate    func(*run.Run)
	}{
		{name: "principal", principal: "bob"},
		{name: "test", principal: "alice", mutate: func(current *run.Run) {
			current.Request.TestSuite = "other-test"
		}},
		{name: "catalogue", principal: "alice", mutate: func(current *run.Run) {
			current.Request.Catalogue.SHA256 = strings.Repeat("4", 64)
		}},
		{name: "profile", principal: "alice", mutate: func(current *run.Run) {
			current.Request.Profile = "regression"
		}},
		{name: "environment", principal: "alice", mutate: func(current *run.Run) {
			current.Request.Environment.SHA256 = strings.Repeat("5", 64)
		}},
		{name: "policy", principal: "alice", mutate: func(current *run.Run) {
			current.Request.Policy.Version = "2.0.0"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			entry, current, input := reportPolicyFixture(t)
			registry, err := NewReportPolicyRegistry([]ReportPolicyEntry{entry})
			if err != nil {
				t.Fatal(err)
			}
			if test.mutate != nil {
				test.mutate(&current)
			}
			if _, err := registry.ResolveReportTrust(
				context.Background(), test.principal, current, input,
			); !errors.Is(err, run.ErrForbidden) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestReportPolicyRegistryValidatesEntries(t *testing.T) {
	if _, err := NewReportPolicyRegistry(nil); !errors.Is(err, run.ErrValidation) {
		t.Fatalf("empty error = %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*ReportPolicyEntry)
	}{
		{name: "policy", mutate: func(entry *ReportPolicyEntry) { entry.PolicyBytes = []byte(`{}`) }},
		{name: "test", mutate: func(entry *ReportPolicyEntry) { entry.TestID = "Invalid" }},
		{name: "catalogue", mutate: func(entry *ReportPolicyEntry) {
			entry.Catalogue.SHA256 = "invalid"
		}},
		{name: "profile", mutate: func(entry *ReportPolicyEntry) { entry.Profile = "Invalid" }},
		{name: "producer", mutate: func(entry *ReportPolicyEntry) { entry.Producer.Image = "latest" }},
		{name: "workload", mutate: func(entry *ReportPolicyEntry) { entry.Workload.SHA256 = "invalid" }},
		{name: "environment", mutate: func(entry *ReportPolicyEntry) {
			entry.Environment.Fingerprint = "invalid"
		}},
		{name: "dataset", mutate: func(entry *ReportPolicyEntry) { *entry.Dataset.Seed = -1 }},
		{name: "principals", mutate: func(entry *ReportPolicyEntry) { entry.Principals = nil }},
		{name: "blank principal", mutate: func(entry *ReportPolicyEntry) {
			entry.Principals = []string{" "}
		}},
		{name: "duplicate principal", mutate: func(entry *ReportPolicyEntry) {
			entry.Principals = []string{"alice", "alice"}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			entry, _, _ := reportPolicyFixture(t)
			test.mutate(&entry)
			if _, err := NewReportPolicyRegistry(
				[]ReportPolicyEntry{entry},
			); !errors.Is(err, run.ErrValidation) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	entry, _, _ := reportPolicyFixture(t)
	if _, err := NewReportPolicyRegistry(
		[]ReportPolicyEntry{entry, entry},
	); !errors.Is(err, run.ErrValidation) {
		t.Fatalf("duplicate entry error = %v", err)
	}
}

func TestReportPolicyRegistryValidatesResolutionContext(t *testing.T) {
	entry, current, input := reportPolicyFixture(t)
	registry, err := NewReportPolicyRegistry([]ReportPolicyEntry{entry})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		principal string
		state     run.State
		input     reconcile.ReportingInput
	}{
		{name: "principal", state: run.StateReporting, input: input},
		{name: "state", principal: "alice", state: run.StateRunning, input: input},
		{name: "candidate", principal: "alice", state: run.StateReporting},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := current
			candidate.State = test.state
			if _, err := registry.ResolveReportTrust(
				context.Background(), test.principal, candidate, test.input,
			); !errors.Is(err, run.ErrValidation) {
				t.Fatalf("error = %v", err)
			}
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := registry.ResolveReportTrust(
		ctx, "alice", current, input,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
	var nilRegistry *ReportPolicyRegistry
	if _, err := nilRegistry.ResolveReportTrust(
		context.Background(), "alice", current, input,
	); !errors.Is(err, run.ErrValidation) {
		t.Fatalf("nil registry error = %v", err)
	}
}
