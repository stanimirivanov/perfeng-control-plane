package reconcile

import (
	"context"
	"errors"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/runrerun"
	batchv1 "k8s.io/api/batch/v1"
)

type rerunRawFixture struct {
	*rerunJobsFixture
	rawCalls, ordinaryRawCalls int
}

func (s *rerunRawFixture) ApproveRawManifest(context.Context, string, run.Run, rawresult.Manifest) error {
	s.ordinaryRawCalls++
	return nil
}
func (s *rerunRawFixture) ApproveRerunRawManifest(_ context.Context, value runrerun.Acceptance, current run.Run, sealed *batchv1.Job, _ rawresult.Manifest) error {
	s.rawCalls++
	if value.Source.OwnerPrincipal != "ci-owner" || value.Child.ID != current.ID || sealed == nil {
		return run.ErrForbidden
	}
	return s.reviewErr
}

func TestRerunRawApprovalUsesKnownSealedSelectionWithoutFallback(t *testing.T) {
	for _, mode := range []string{"approved", "ordinary", "missing", "missing-seal", "foreign", "request", "withdrawn", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			frozen, f := frozenRerunReport(t)
			f.current.State = run.StateCollecting
			store := &rerunRawFixture{rerunJobsFixture: &rerunJobsFixture{rerunReportFixture: frozen, sealed: provisioningTemplate()}}
			principal := "child-owner"
			var expected error
			ordinary, reviews := 0, 0
			switch mode {
			case "approved":
				reviews = 1
			case "ordinary":
				store.known = false
				ordinary = 1
			case "missing":
				store.lookupErr = run.ErrNotFound
				expected = run.ErrUnavailable
			case "missing-seal":
				store.sealed = nil
				expected = run.ErrUnavailable
			case "foreign":
				principal = "foreign"
				expected = run.ErrUnavailable
			case "request":
				f.current.Request.Candidate.GitSHA = "ffffffffffffffffffffffffffffffffffffffff"
				expected = run.ErrUnavailable
			case "withdrawn":
				store.reviewErr = run.ErrForbidden
				expected = run.ErrForbidden
				reviews = 1
			case "cancelled":
				expected = context.Canceled
			}
			guard, err := NewRerunRawApprover(store, store)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancelled" {
				cancel()
			}
			err = guard.ApproveRawManifest(ctx, principal, f.current, collectorManifest(f.current.ID))
			if !errors.Is(err, expected) || store.ordinaryRawCalls != ordinary || store.rawCalls != reviews {
				t.Fatal("raw trust bypassed retained child selection", err, store.rawCalls, store.ordinaryRawCalls)
			}
		})
	}
	if _, err := NewRerunRawApprover(nil, nil); !errors.Is(err, run.ErrValidation) {
		t.Fatal("missing capabilities admitted", err)
	}
}
