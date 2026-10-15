package registry

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func TestResolveContractsVersionUsesExactAuthorizedEntry(t *testing.T) {
	checkout, checkoutRun, _ := reportPolicyFixture(t)
	checkout.ContractsVersion = "0.13.0"
	contained, benchmarkRun := benchmarkPolicyFixture(t)
	loaded, err := NewReportPolicyRegistry([]ReportPolicyEntry{checkout, contained})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		current run.Run
		version string
	}{{checkoutRun, "0.13.0"}, {benchmarkRun, "0.22.0"}} {
		for _, state := range []run.State{run.StateCollecting, run.StateAnalyzing, run.StateReporting} {
			current := test.current
			current.State = state
			t.Run(test.version+"/"+string(state), func(t *testing.T) {
				t.Parallel()
				version, err := loaded.ResolveContractsVersion(context.Background(), "alice", current)
				if err != nil || version != test.version {
					t.Fatalf("version = %q, error = %v", version, err)
				}
			})
		}
	}
}

func TestResolveContractsVersionRejectsUnmatchedOrInvalidContext(t *testing.T) {
	entry, current, _ := reportPolicyFixture(t)
	loaded, err := NewReportPolicyRegistry([]ReportPolicyEntry{entry})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, principal string
		mutate          func(*run.Run)
		want            error
	}{
		{name: "missing principal", want: run.ErrValidation},
		{name: "other principal", principal: "other", want: run.ErrForbidden},
		{name: "wrong state", principal: "alice", mutate: func(r *run.Run) { r.State = run.StateRunning }, want: run.ErrValidation},
		{name: "invalid request", principal: "alice", mutate: func(r *run.Run) { r.Request.Candidate.Image = "latest" }, want: run.ErrValidation},
		{name: "test", principal: "alice", mutate: func(r *run.Run) { r.Request.TestSuite = "other-test" }, want: run.ErrForbidden},
		{name: "catalogue", principal: "alice", mutate: func(r *run.Run) { r.Request.Catalogue.Version = "9.0.0" }, want: run.ErrForbidden},
		{name: "profile", principal: "alice", mutate: func(r *run.Run) { r.Request.Profile = "regression" }, want: run.ErrForbidden},
		{name: "environment", principal: "alice", mutate: func(r *run.Run) { r.Request.Environment.SHA256 = strings.Repeat("f", 64) }, want: run.ErrForbidden},
		{name: "policy", principal: "alice", mutate: func(r *run.Run) { r.Request.Policy.Version = "9.0.0" }, want: run.ErrForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := current
			if test.mutate != nil {
				test.mutate(&changed)
			}
			if version, err := loaded.ResolveContractsVersion(context.Background(), test.principal, changed); !errors.Is(err, test.want) || version != "" {
				t.Fatalf("version = %q, error = %v", version, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := loaded.ResolveContractsVersion(ctx, "alice", current); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
	var absent *ReportPolicyRegistry
	if _, err := absent.ResolveContractsVersion(context.Background(), "alice", current); !errors.Is(err, run.ErrValidation) {
		t.Fatalf("nil registry = %v", err)
	}
	current.Request.Candidate.Image = "ghcr.io/example/retired@sha256:" + strings.Repeat("9", 64)
	if version, err := loaded.ResolveContractsVersion(context.Background(), "alice", current); err != nil || version != entry.ContractsVersion {
		t.Fatalf("accepted Run stranded by admission allowlist: %q, %v", version, err)
	}
}
