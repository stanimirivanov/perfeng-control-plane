package registry

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/reconcile"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func TestReportPolicyRegistryResolvesK6NormalizationPlan(t *testing.T) {
	entry, current, _ := reportPolicyFixture(t)
	current.State = run.StateAnalyzing
	input, _ := normalizedManifestFixture(entry, current)
	registry, err := NewReportPolicyRegistry([]ReportPolicyEntry{entry})
	if err != nil {
		t.Fatal(err)
	}

	plan, err := registry.ResolveAnalysisPlan(context.Background(), "alice", current, input)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Source != input.Sources[0] || plan.Spec != entry.Normalization ||
		!plan.Producer.Equal(entry.NormalizerProducer) {
		t.Fatalf("plan = %+v", plan)
	}

	plan.Producer.Image = "changed"
	again, err := registry.ResolveAnalysisPlan(context.Background(), "alice", current, input)
	if err != nil || !again.Producer.Equal(entry.NormalizerProducer) {
		t.Fatal("resolved plan shares registry producer state")
	}
}

func TestReportPolicyRegistryResolvesNativePlaywrightNormalizationPlan(t *testing.T) {
	entry, current := v2RawPolicyFixture(t, "source-checkout")
	current.State = run.StateAnalyzing
	input, _ := normalizedManifestFixture(entry, current)
	input.Manifest.Format = "raw-result/v2"
	registry, err := NewReportPolicyRegistry([]ReportPolicyEntry{entry})
	if err != nil {
		t.Fatal(err)
	}

	plan, err := registry.ResolveAnalysisPlan(context.Background(), "alice", current, input)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Source.Format != reconcile.PlaywrightMeasurementsV2Format || plan.Spec.Layout != "" ||
		plan.Producer.Image != entry.NormalizerProducer.Image {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestReportPolicyRegistryResolvesKubeBurnerNormalizationPlan(t *testing.T) {
	entry, current := v2RawPolicyFixture(t, "oci-image")
	entry.RawProducer.Name = "kube-burner"
	entry.RawProducer.Version = "2.8.5"
	entry.Normalization = reconcile.NormalizationSpec{
		SourceFormat: reconcile.KubeBurnerPodLatencyFormat,
	}
	current.State = run.StateAnalyzing
	input, _ := normalizedManifestFixture(entry, current)
	input.Manifest.Format = "raw-result/v2"
	registry, err := NewReportPolicyRegistry([]ReportPolicyEntry{entry})
	if err != nil {
		t.Fatal(err)
	}

	plan, err := registry.ResolveAnalysisPlan(context.Background(), "alice", current, input)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Source != input.Sources[0] || plan.Spec != entry.Normalization ||
		plan.Spec.SourceFormat != reconcile.KubeBurnerPodLatencyFormat || plan.Spec.Layout != "" {
		t.Fatalf("plan = %+v", plan)
	}

	for _, test := range []struct {
		name   string
		mutate func(*reconcile.AnalysisInput)
	}{
		{name: "missing", mutate: func(input *reconcile.AnalysisInput) {
			input.Sources[0].Format = "kube-burner-job-summary-json"
		}},
		{name: "ambiguous", mutate: func(input *reconcile.AnalysisInput) {
			duplicate := input.Sources[0]
			duplicate.ID = "99999999-9999-4999-8999-999999999999"
			duplicate.URI += ".duplicate"
			input.Sources = append(input.Sources, duplicate)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := input
			candidate.Sources = append([]run.Artifact(nil), input.Sources...)
			test.mutate(&candidate)
			if _, err := registry.ResolveAnalysisPlan(
				context.Background(), "alice", current, candidate,
			); !errors.Is(err, run.ErrForbidden) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestReportPolicyRegistryRejectsUnapprovedNormalizationEvidence(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*run.Run, *reconcile.AnalysisInput)
	}{
		{name: "request", mutate: func(current *run.Run, _ *reconcile.AnalysisInput) {
			current.Request.Policy.Version = "2.0.0"
		}},
		{name: "candidate", mutate: func(current *run.Run, _ *reconcile.AnalysisInput) {
			current.Request.Candidate.Image = "ghcr.io/example/search@sha256:" + strings.Repeat("7", 64)
		}},
		{name: "missing format", mutate: func(_ *run.Run, input *reconcile.AnalysisInput) {
			input.Sources[0].Format = "browser-observations-json"
		}},
		{name: "ambiguous format", mutate: func(_ *run.Run, input *reconcile.AnalysisInput) {
			duplicate := input.Sources[0]
			duplicate.ID = "99999999-9999-4999-8999-999999999999"
			duplicate.URI += ".duplicate"
			input.Sources = append(input.Sources, duplicate)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			entry, current, _ := reportPolicyFixture(t)
			current.State = run.StateAnalyzing
			input, _ := normalizedManifestFixture(entry, current)
			test.mutate(&current, &input)
			registry, err := NewReportPolicyRegistry([]ReportPolicyEntry{entry})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := registry.ResolveAnalysisPlan(
				context.Background(), "alice", current, input,
			); !errors.Is(err, run.ErrForbidden) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestReportPolicyRegistryValidatesNormalizationPlanContext(t *testing.T) {
	entry, current, _ := reportPolicyFixture(t)
	current.State = run.StateAnalyzing
	input, _ := normalizedManifestFixture(entry, current)
	registry, err := NewReportPolicyRegistry([]ReportPolicyEntry{entry})
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name      string
		principal string
		state     run.State
		input     reconcile.AnalysisInput
	}{
		{name: "principal", state: run.StateAnalyzing, input: input},
		{name: "state", principal: "alice", state: run.StateRunning, input: input},
		{name: "input", principal: "alice", state: run.StateAnalyzing},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := current
			candidate.State = test.state
			if _, err := registry.ResolveAnalysisPlan(
				context.Background(), test.principal, candidate, test.input,
			); !errors.Is(err, run.ErrValidation) {
				t.Fatalf("error = %v", err)
			}
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := registry.ResolveAnalysisPlan(ctx, "alice", current, input); !errors.Is(
		err, context.Canceled,
	) {
		t.Fatalf("cancellation error = %v", err)
	}
	var nilRegistry *ReportPolicyRegistry
	if _, err := nilRegistry.ResolveAnalysisPlan(
		context.Background(), "alice", current, input,
	); !errors.Is(err, run.ErrValidation) {
		t.Fatalf("nil registry error = %v", err)
	}
}
