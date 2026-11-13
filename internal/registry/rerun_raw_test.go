package registry

import (
	"context"
	"strings"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/runcomparison"
	"github.com/stanimirivanov/perfeng-control-plane/internal/runrerun"
	"github.com/stanimirivanov/perfeng-control-plane/internal/serviceaccess"
)

func TestRegistryRerunRawApprovalBindsSourcePinsAndSealedImage(t *testing.T) {
	for _, mode := range []string{"approved", "owner", "workload", "schema", "image", "withdrawn", "v2", "v2-image", "native", "missing-seal", "state"} {
		t.Run(mode, func(t *testing.T) {
			entry, current, _ := reportPolicyFixture(t)
			if strings.HasPrefix(mode, "v2") {
				entry, current = v2RawPolicyFixture(t, "oci-image")
			}
			if mode == "native" {
				entry, current = v2RawPolicyFixture(t, "source-checkout")
			}
			current.State = run.StateCollecting
			value := runrerun.Acceptance{Source: serviceaccess.Binding{OwnerPrincipal: "alice"}, Child: current, Inputs: runcomparison.Inputs{Request: current.Request, ContractsVersion: entry.ContractsVersion, Workload: entry.Workload}}
			manifest := rawManifestFixture(entry, current)
			sealed := entry.ExecutionTemplate.DeepCopy()
			switch mode {
			case "owner":
				value.Source.OwnerPrincipal = "foreign"
			case "workload":
				value.Inputs.Workload.ID = "other-workload"
			case "schema":
				value.Inputs.ContractsVersion = "9.9.9"
			case "image", "v2-image":
				sealed.Spec.Template.Spec.Containers[0].Image = "ghcr.io/example/other@sha256:" + strings.Repeat("f", 64)
			case "missing-seal":
				sealed = nil
			case "state":
				current.State = run.StateAnalyzing
			case "withdrawn":
				entry.RawProducer.Version = "9.9.9"
			}
			reviewed, err := NewReportPolicyRegistry([]ReportPolicyEntry{entry})
			if err != nil {
				t.Fatal(err)
			}
			err = reviewed.ApproveRerunRawManifest(context.Background(), value, current, sealed, manifest)
			if ((mode == "approved" || mode == "v2") && err != nil) || (mode != "approved" && mode != "v2" && err == nil) {
				t.Fatal("rerun raw approval did not bind retained trust", err)
			}
		})
	}
}
