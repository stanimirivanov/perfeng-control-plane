package registry

import (
	"context"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/runcomparison"
	"github.com/stanimirivanov/perfeng-control-plane/internal/runrerun"
	"github.com/stanimirivanov/perfeng-control-plane/internal/serviceaccess"
)

func TestRegistryRerunNormalizationBindsFrozenContextAndCurrentProducer(t *testing.T) {
	for _, mode := range []string{"approved", "owner", "workload", "schema", "producer", "source", "withdrawn"} {
		t.Run(mode, func(t *testing.T) {
			entry, current, _ := reportPolicyFixture(t)
			current.State = run.StateAnalyzing
			input, manifest := normalizedManifestFixture(entry, current)
			value := runrerun.Acceptance{Source: serviceaccess.Binding{OwnerPrincipal: "alice"}, Child: current, Inputs: runcomparison.Inputs{Request: current.Request, Workload: entry.Workload, ContractsVersion: entry.ContractsVersion}}
			switch mode {
			case "owner":
				value.Source.OwnerPrincipal = "foreign"
			case "workload":
				value.Inputs.Workload.ID = "other-workload"
			case "schema":
				value.Inputs.ContractsVersion = "9.9.9"
			case "producer":
				manifest.Producer.Version = "9.9.9"
			case "source":
				manifest.SourceArtifacts[0].SizeBytes++
			case "withdrawn":
				entry.Principals = []string{"other-owner"}
			}
			reviewed, err := NewReportPolicyRegistry([]ReportPolicyEntry{entry})
			if err != nil {
				t.Fatal(err)
			}
			plan, planErr := reviewed.ResolveRerunAnalysisPlan(context.Background(), value, current, input)
			approvalErr := reviewed.ApproveRerunNormalizedManifest(context.Background(), value, current, input, manifest)
			if mode == "approved" {
				if planErr != nil || approvalErr != nil || plan.Source != input.Sources[0] || !plan.Producer.Equal(entry.NormalizerProducer) {
					t.Fatal("exact normalization refused", planErr, approvalErr)
				}
			} else {
				if approvalErr == nil {
					t.Fatal("untrusted output approved")
				}
				if mode != "producer" && mode != "source" && planErr == nil {
					t.Fatal("untrusted context planned")
				}
			}
		})
	}
}
