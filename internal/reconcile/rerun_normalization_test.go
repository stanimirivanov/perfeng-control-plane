package reconcile

import (
	"context"
	"errors"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/normalizedresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/runrerun"
)

type rerunNormalizationFixture struct {
	*rerunReportFixture
	artifacts         map[string]run.Artifact
	reviewErr         error
	reviews, ordinary int
}

func (s *rerunNormalizationFixture) GetArtifact(_ context.Context, _ string, _ string, id string) (run.Artifact, error) {
	value, ok := s.artifacts[id]
	if !ok {
		return run.Artifact{}, run.ErrNotFound
	}
	return value, nil
}
func (s *rerunNormalizationFixture) ResolveAnalysisPlan(context.Context, string, run.Run, AnalysisInput) (AnalysisPlan, error) {
	s.ordinary++
	return AnalysisPlan{}, nil
}
func (s *rerunNormalizationFixture) ApproveNormalizedManifest(context.Context, string, run.Run, AnalysisInput, normalizedresult.Manifest) error {
	s.ordinary++
	return nil
}
func (s *rerunNormalizationFixture) ResolveRerunAnalysisPlan(_ context.Context, value runrerun.Acceptance, _ run.Run, input AnalysisInput) (AnalysisPlan, error) {
	s.reviews++
	if value.Source.OwnerPrincipal != "ci-owner" {
		return AnalysisPlan{}, run.ErrForbidden
	}
	return analysisPlan(input.Sources[0], NormalizationSpec{SourceFormat: input.Sources[0].Format, Layout: K6TypedLayout}), s.reviewErr
}
func (s *rerunNormalizationFixture) ApproveRerunNormalizedManifest(_ context.Context, value runrerun.Acceptance, _ run.Run, _ AnalysisInput, _ normalizedresult.Manifest) error {
	s.reviews++
	if value.Source.OwnerPrincipal != "ci-owner" {
		return run.ErrForbidden
	}
	return s.reviewErr
}

func TestRerunNormalizationRequiresExactRegisteredEvidenceWithoutFallback(t *testing.T) {
	for _, mode := range []string{"approved", "ordinary", "missing-association", "foreign", "request", "missing-manifest", "missing-source", "changed-reference", "withdrawn", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			frozen, f := frozenRerunReport(t)
			f.current.State = run.StateAnalyzing
			raw := rawArtifactSet(f.current.ID)
			input := AnalysisInput{Manifest: raw.Manifest, Sources: raw.Artifacts}
			store := &rerunNormalizationFixture{rerunReportFixture: frozen, artifacts: map[string]run.Artifact{}}
			store.artifacts[input.Manifest.ID] = input.Manifest
			for _, ref := range input.Sources {
				store.artifacts[ref.ID] = ref
			}
			principal := "child-owner"
			var expected error
			reviews, ordinary := 0, 0
			switch mode {
			case "approved":
				reviews = 2
			case "ordinary":
				store.known = false
				ordinary = 2
			case "missing-association":
				store.lookupErr = run.ErrNotFound
				expected = run.ErrUnavailable
			case "foreign":
				principal = "foreign"
				expected = run.ErrUnavailable
			case "request":
				f.current.Request.Candidate.GitSHA = "ffffffffffffffffffffffffffffffffffffffff"
				expected = run.ErrUnavailable
			case "missing-manifest":
				delete(store.artifacts, input.Manifest.ID)
				expected = run.ErrUnavailable
			case "missing-source":
				delete(store.artifacts, input.Sources[0].ID)
				expected = run.ErrUnavailable
			case "changed-reference":
				input.Sources[0].SizeBytes++
				expected = run.ErrConflict
			case "withdrawn":
				store.reviewErr = run.ErrForbidden
				expected = run.ErrForbidden
				reviews = 2
			case "cancelled":
				expected = context.Canceled
			}
			guard, err := NewRerunNormalizationResolver(store, store)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancelled" {
				cancel()
			}
			plan, planErr := guard.ResolveAnalysisPlan(ctx, principal, f.current, input)
			approvalErr := guard.ApproveNormalizedManifest(ctx, principal, f.current, input, normalizedresult.Manifest{})
			if !errors.Is(planErr, expected) || !errors.Is(approvalErr, expected) || store.reviews != reviews || store.ordinary != ordinary {
				t.Fatal("normalization bypassed retained evidence", planErr, approvalErr, store.reviews, store.ordinary)
			}
			if mode == "approved" && plan.Source != input.Sources[0] {
				t.Fatal("plan changed exact source")
			}
		})
	}
}
