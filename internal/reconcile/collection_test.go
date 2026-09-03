package reconcile

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

type collectRawFunc func(context.Context, string, run.Run) (RawArtifactSet, error)

func (collect collectRawFunc) CollectRawArtifacts(
	ctx context.Context,
	principal string,
	current run.Run,
) (RawArtifactSet, error) {
	return collect(ctx, principal, current)
}

type collectionStore struct {
	*advancingStore
	registered []run.Artifact
	principals []string
	failCall   int
	failErr    error
	calls      int
}

func (store *collectionStore) RegisterArtifact(
	ctx context.Context,
	principal string,
	artifact run.Artifact,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	store.calls++
	store.principals = append(store.principals, principal)
	if store.calls == store.failCall {
		return store.failErr
	}
	store.registered = append(store.registered, artifact)

	return nil
}

func rawArtifacts(runID string) []run.Artifact {
	return []run.Artifact{
		{
			ID: "10000000-0000-4000-8000-000000000001", RunID: runID, Kind: "raw",
			URI:    "s3://perfeng-artifacts/runs/example/summary.json",
			SHA256: strings.Repeat("a", 64), SizeBytes: 42,
			MediaType: "application/json", Format: "k6-summary-json",
		},
		{
			ID: "10000000-0000-4000-8000-000000000002", RunID: runID, Kind: "raw",
			URI:    "s3://perfeng-artifacts/runs/example/points.jsonl",
			SHA256: strings.Repeat("b", 64), SizeBytes: 84,
			MediaType: "application/x-ndjson", Format: "k6-json-points",
		},
	}
}

func rawArtifactSet(runID string) RawArtifactSet {
	return RawArtifactSet{
		Manifest: run.Artifact{
			ID: "10000000-0000-4000-8000-000000000003", RunID: runID, Kind: "raw",
			URI:    "s3://perfeng-artifacts/runs/example/raw-result.json",
			SHA256: strings.Repeat("c", 64), SizeBytes: 126,
			MediaType: "application/json", Format: "raw-result/v1",
		},
		Artifacts: rawArtifacts(runID),
	}
}

func collectionFixture(t *testing.T) (*CollectionReconciler, *collectionStore, run.Claim) {
	t.Helper()

	claim := boundClaim(run.StateCollecting)
	store := &collectionStore{advancingStore: &advancingStore{}}
	collector := collectRawFunc(func(ctx context.Context, principal string, current run.Run) (RawArtifactSet, error) {
		if principal != claim.Lease.Principal || current.ID != claim.Run.ID ||
			current.Request != claim.Run.Request {
			t.Fatal("collector did not receive the owned immutable Run")
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("collection has no attempt deadline")
		}

		return rawArtifactSet(current.ID), nil
	})
	reconciler, err := NewCollectionReconciler(store, collector, DefaultCollectionConfig())
	if err != nil {
		t.Fatal(err)
	}

	return reconciler, store, claim
}

func TestCollectionRegistersCompleteSetThenAdvances(t *testing.T) {
	reconciler, store, claim := collectionFixture(t)
	result, err := reconciler.Reconcile(context.Background(), claim)
	if err != nil || result.RetryAfter != 0 {
		t.Fatalf("Reconcile() = %+v, %v", result, err)
	}
	wantSet := rawArtifactSet(claim.Run.ID)
	want := append(append([]run.Artifact{}, wantSet.Artifacts...), wantSet.Manifest)
	if !reflect.DeepEqual(store.registered, want) ||
		!reflect.DeepEqual(store.principals, []string{
			claim.Lease.Principal, claim.Lease.Principal, claim.Lease.Principal,
		}) ||
		store.advancingStore.calls != 1 || store.change.State != run.StateAnalyzing ||
		store.change.Failure != nil || store.lease != claim.Lease ||
		store.revision != claim.Run.Revision {
		t.Fatalf("unexpected collection effects: %+v", store)
	}
}

func TestCollectionWaitsQuietlyForStorageVisibility(t *testing.T) {
	reconciler, store, claim := collectionFixture(t)
	reconciler.collector = collectRawFunc(func(context.Context, string, run.Run) (RawArtifactSet, error) {
		return RawArtifactSet{}, errors.Join(ErrArtifactsNotReady, errors.New("eventual consistency"))
	})

	result, err := reconciler.Reconcile(context.Background(), claim)
	if err != nil || result.RetryAfter != DefaultCollectionConfig().RetryAfter ||
		store.calls != 0 || store.advancingStore.calls != 0 {
		t.Fatalf("not ready = %+v, %v; store=%+v", result, err, store)
	}
}

func TestCollectionClassifiesInvalidExecutionOutput(t *testing.T) {
	reconciler, store, claim := collectionFixture(t)
	reconciler.collector = collectRawFunc(func(context.Context, string, run.Run) (RawArtifactSet, error) {
		return RawArtifactSet{}, errors.Join(ErrInvalidArtifacts, errors.New("secret raw parser detail"))
	})

	if _, err := reconciler.Reconcile(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	failure := store.change.Failure
	if store.calls != 0 || store.advancingStore.calls != 1 ||
		store.change.State != run.StateTestFailure || failure == nil ||
		failure.Code != run.FailureCodeToolError ||
		failure.Message != "execution did not produce valid raw artifacts" ||
		strings.Contains(failure.Message, "secret") {
		t.Fatalf("unsafe invalid-artifact result: %+v", store.change)
	}
}

func TestCollectionPreservesOperationalErrors(t *testing.T) {
	for _, want := range []error{run.ErrUnavailable, context.Canceled, run.ErrArtifactConflict} {
		t.Run(want.Error(), func(t *testing.T) {
			reconciler, store, claim := collectionFixture(t)
			reconciler.collector = collectRawFunc(func(context.Context, string, run.Run) (RawArtifactSet, error) {
				return RawArtifactSet{}, want
			})
			if _, err := reconciler.Reconcile(context.Background(), claim); !errors.Is(err, want) {
				t.Fatalf("error = %v, want %v", err, want)
			}
			if store.calls != 0 || store.advancingStore.calls != 0 {
				t.Fatal("operational error changed durable state")
			}
		})
	}
}

func TestCollectionRejectsAmbiguousCollectorClassification(t *testing.T) {
	reconciler, store, claim := collectionFixture(t)
	reconciler.collector = collectRawFunc(func(context.Context, string, run.Run) (RawArtifactSet, error) {
		return RawArtifactSet{}, errors.Join(ErrArtifactsNotReady, ErrInvalidArtifacts)
	})

	if _, err := reconciler.Reconcile(context.Background(), claim); !errors.Is(err, run.ErrValidation) {
		t.Fatalf("error = %v", err)
	}
	if store.calls != 0 || store.advancingStore.calls != 0 {
		t.Fatal("ambiguous classification changed durable state")
	}
}

func TestCollectionRejectsInvalidCollectorOutput(t *testing.T) {
	claim := boundClaim(run.StateCollecting)
	valid := rawArtifactSet(claim.Run.ID)
	tests := map[string]RawArtifactSet{
		"empty":            {Manifest: valid.Manifest},
		"wrong run":        valid,
		"normalized":       valid,
		"invalid":          valid,
		"invalid manifest": valid,
		"duplicate ID":     {Manifest: valid.Manifest, Artifacts: []run.Artifact{valid.Artifacts[0], valid.Artifacts[0]}},
		"duplicate URI":    valid,
		"manifest collision": {
			Manifest: valid.Manifest, Artifacts: []run.Artifact{valid.Manifest},
		},
	}
	wrongRun := valid
	wrongRun.Artifacts = append([]run.Artifact(nil), valid.Artifacts...)
	wrongRun.Artifacts[0].RunID = "perf-20260903-120000-deadbeef"
	tests["wrong run"] = wrongRun
	normalized := valid
	normalized.Artifacts = append([]run.Artifact(nil), valid.Artifacts...)
	normalized.Artifacts[0].Kind = "normalized"
	tests["normalized"] = normalized
	invalid := valid
	invalid.Artifacts = append([]run.Artifact(nil), valid.Artifacts...)
	invalid.Artifacts[0].SHA256 = "bad"
	tests["invalid"] = invalid
	invalidManifest := valid
	invalidManifest.Manifest.Format = "k6-summary-json"
	tests["invalid manifest"] = invalidManifest
	duplicateURI := valid
	duplicateURI.Artifacts = append([]run.Artifact(nil), valid.Artifacts...)
	duplicateURI.Artifacts[1].URI = valid.Artifacts[0].URI
	tests["duplicate URI"] = duplicateURI

	for name, artifacts := range tests {
		t.Run(name, func(t *testing.T) {
			reconciler, store, current := collectionFixture(t)
			reconciler.collector = collectRawFunc(func(context.Context, string, run.Run) (RawArtifactSet, error) {
				return artifacts, nil
			})
			if _, err := reconciler.Reconcile(context.Background(), current); !errors.Is(err, run.ErrValidation) {
				t.Fatalf("error = %v", err)
			}
			if store.calls != 0 || store.advancingStore.calls != 0 {
				t.Fatal("invalid collector output changed durable state")
			}
		})
	}
}

func TestCollectionRetriesIdempotentRegistrationAfterPartialFailure(t *testing.T) {
	reconciler, store, claim := collectionFixture(t)
	store.failCall, store.failErr = 2, run.ErrUnavailable

	if _, err := reconciler.Reconcile(context.Background(), claim); !errors.Is(err, run.ErrUnavailable) {
		t.Fatalf("first error = %v", err)
	}
	if len(store.registered) != 1 || store.advancingStore.calls != 0 {
		t.Fatalf("first attempt = %+v", store)
	}
	store.failCall = 0
	if _, err := reconciler.Reconcile(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	set := rawArtifactSet(claim.Run.ID)
	if store.calls != 5 || store.advancingStore.calls != 1 ||
		!reflect.DeepEqual(store.registered, []run.Artifact{
			set.Artifacts[0], set.Artifacts[0], set.Artifacts[1], set.Manifest,
		}) {
		t.Fatalf("retry effects = %+v", store)
	}
}

func TestCollectionHonorsDeadlineAndValidatesInputs(t *testing.T) {
	reconciler, store, claim := collectionFixture(t)
	reconciler.config.AttemptTimeout = 5 * time.Millisecond
	reconciler.collector = collectRawFunc(func(ctx context.Context, _ string, _ run.Run) (RawArtifactSet, error) {
		<-ctx.Done()

		return RawArtifactSet{}, ctx.Err()
	})
	if _, err := reconciler.Reconcile(context.Background(), claim); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline error = %v", err)
	}
	if store.calls != 0 || store.advancingStore.calls != 0 {
		t.Fatal("timed-out collection changed durable state")
	}

	collector := reconciler.collector
	for _, config := range []CollectionConfig{
		{},
		{AttemptTimeout: -time.Second, RetryAfter: time.Second},
		{AttemptTimeout: 5*time.Minute + time.Nanosecond, RetryAfter: time.Second},
		{AttemptTimeout: time.Second, RetryAfter: time.Millisecond},
		{AttemptTimeout: time.Second, RetryAfter: 5*time.Minute + time.Second},
	} {
		if _, err := NewCollectionReconciler(store, collector, config); !errors.Is(err, run.ErrValidation) {
			t.Fatalf("config %+v error = %v", config, err)
		}
	}
	if _, err := NewCollectionReconciler(nil, collector, DefaultCollectionConfig()); !errors.Is(err, run.ErrValidation) {
		t.Fatal(err)
	}
	if _, err := NewCollectionReconciler(store, nil, DefaultCollectionConfig()); !errors.Is(err, run.ErrValidation) {
		t.Fatal(err)
	}

	reconciler, store, claim = collectionFixture(t)
	claim.Lease.Token = "invalid"
	if _, err := reconciler.Reconcile(context.Background(), claim); !errors.Is(err, run.ErrValidation) {
		t.Fatal(err)
	}
	claim = boundClaim(run.StateAnalyzing)
	if _, err := reconciler.Reconcile(context.Background(), claim); !errors.Is(err, ErrStateNotHandled) {
		t.Fatal(err)
	}
	if store.calls != 0 || store.advancingStore.calls != 0 {
		t.Fatal("invalid input accessed mutable dependencies")
	}
}
