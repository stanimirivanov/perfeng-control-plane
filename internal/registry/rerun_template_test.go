package registry

import (
	"context"
	"strings"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/reconcile"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/runcomparison"
	"github.com/stanimirivanov/perfeng-control-plane/internal/runrerun"
	"github.com/stanimirivanov/perfeng-control-plane/internal/serviceaccess"
)

func TestRegistryReviewsExactFrozenRerunContext(t *testing.T) {
	entry, current, _ := reportPolicyFixture(t)
	current.State = run.StateValidating
	value := runrerun.Acceptance{Source: serviceaccess.Binding{OwnerPrincipal: "alice"}, Child: current, Inputs: runcomparison.Inputs{Request: current.Request, Workload: entry.Workload, PolicyBytes: entry.PolicyBytes, PolicyMode: "inform", Producer: entry.ReportProducer, ContractsVersion: entry.ContractsVersion}}
	registry, err := NewReportPolicyRegistry([]ReportPolicyEntry{entry})
	if err != nil {
		t.Fatal(err)
	}
	job, err := registry.ResolveRerunTemplate(context.Background(), value, current)
	if err != nil || job == nil {
		t.Fatal("exact frozen review denied", err)
	}
	job.Spec.Template.Spec.Containers[0].Args = []string{"caller mutation"}
	if again, err := registry.ResolveRerunTemplate(context.Background(), value, current); err != nil || again.Spec.Template.Spec.Containers[0].Args[0] == "caller mutation" {
		t.Fatal("registry template aliased", err)
	}
	for _, mode := range []string{"owner", "request", "workload", "policy", "producer", "version"} {
		changed := value
		changed.Inputs = value.Inputs.Clone()
		child := current
		switch mode {
		case "owner":
			changed.Source.OwnerPrincipal = "foreign"
		case "request":
			child.Request.Candidate.GitSHA = "ffffffffffffffffffffffffffffffffffffffff"
		case "workload":
			changed.Inputs.Workload.ID = "different"
		case "policy":
			changed.Inputs.PolicyBytes = []byte(`{}`)
		case "producer":
			changed.Inputs.Producer.Version = "9.9.9"
		case "version":
			changed.Inputs.ContractsVersion = "0.1.0"
		}
		if job, err := registry.ResolveRerunTemplate(context.Background(), changed, child); err == nil || job != nil {
			t.Fatal("unfrozen context reviewed", mode, err)
		}
	}
}

func TestRegistryRefusesWithdrawnOrUnsupportedRerunTemplates(t *testing.T) {
	entry, current, _ := reportPolicyFixture(t)
	current.State = run.StateValidating
	value := runrerun.Acceptance{Source: serviceaccess.Binding{OwnerPrincipal: "alice"}, Child: current, Inputs: runcomparison.Inputs{Request: current.Request, Workload: entry.Workload, PolicyBytes: entry.PolicyBytes, PolicyMode: "inform", Producer: entry.ReportProducer, ContractsVersion: entry.ContractsVersion}}
	withdrawn := entry
	withdrawn.CandidateImages = []string{"ghcr.io/example/other@sha256:" + strings.Repeat("f", 64)}
	reviewed, err := NewReportPolicyRegistry([]ReportPolicyEntry{withdrawn})
	if err != nil {
		t.Fatal(err)
	}
	if job, err := reviewed.ResolveRerunTemplate(context.Background(), value, current); err == nil || job != nil {
		t.Fatal("withdrawn candidate authorized", err)
	}
	for _, mode := range []string{"native", "benchmark"} {
		reviewed, err = NewReportPolicyRegistry([]ReportPolicyEntry{entry})
		if err != nil {
			t.Fatal(err)
		}
		// Guard the boundary even if a future producer supplies a new execution kind.
		for key, configured := range reviewed.entries {
			if mode == "native" {
				configured.executionTarget = run.ExecutionTargetNativeLinux
			} else {
				configured.benchmark = &reconcile.BenchmarkPlan{}
			}
			reviewed.entries[key] = configured
		}
		if job, err := reviewed.ResolveRerunTemplate(context.Background(), value, current); err == nil || job != nil {
			t.Fatal("unsupported path reviewed", mode, err)
		}
	}
}
