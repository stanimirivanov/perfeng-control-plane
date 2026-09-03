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

type normalizeFunc func(context.Context, string, run.Run, AnalysisInput) (run.Artifact, error)

func (normalize normalizeFunc) Normalize(
	ctx context.Context,
	principal string,
	current run.Run,
	input AnalysisInput,
) (run.Artifact, error) {
	return normalize(ctx, principal, current, input)
}

type analysisStore struct {
	*advancingStore
	artifacts   []run.Artifact
	registered  []run.Artifact
	listErr     error
	registerErr error
	listCalls   int
}

func (store *analysisStore) ListArtifacts(
	ctx context.Context,
	principal string,
	runID string,
) ([]run.Artifact, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store.listCalls++
	if principal == "" || runID == "" {
		return nil, run.ErrValidation
	}

	return append([]run.Artifact(nil), store.artifacts...), store.listErr
}

func (store *analysisStore) RegisterArtifact(
	ctx context.Context,
	principal string,
	artifact run.Artifact,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if principal == "" {
		return run.ErrValidation
	}
	if store.registerErr != nil {
		return store.registerErr
	}
	store.registered = append(store.registered, artifact)
	store.artifacts = append(store.artifacts, artifact)

	return nil
}

func normalizedArtifact(runID string) run.Artifact {
	return run.Artifact{
		ID: "10000000-0000-4000-8000-000000000004", RunID: runID, Kind: "normalized",
		URI:    "s3://perfeng-artifacts/runs/example/normalized-result.json",
		SHA256: strings.Repeat("d", 64), SizeBytes: 168,
		MediaType: "application/json", Format: "normalized-result/v1",
	}
}

func analysisArtifacts(runID string) []run.Artifact {
	set := rawArtifactSet(runID)

	return append(append([]run.Artifact{}, set.Artifacts...), set.Manifest)
}

func analysisFixture(t *testing.T) (*AnalysisReconciler, *analysisStore, run.Claim, *int) {
	t.Helper()

	claim := boundClaim(run.StateAnalyzing)
	store := &analysisStore{
		advancingStore: &advancingStore{},
		artifacts:      analysisArtifacts(claim.Run.ID),
	}
	calls := 0
	executor := normalizeFunc(func(
		ctx context.Context,
		principal string,
		current run.Run,
		input AnalysisInput,
	) (run.Artifact, error) {
		calls++
		if principal != claim.Lease.Principal || current.ID != claim.Run.ID ||
			current.Request != claim.Run.Request {
			t.Fatal("executor did not receive the owned immutable Run")
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("analysis has no attempt deadline")
		}
		set := rawArtifactSet(claim.Run.ID)
		if input.Manifest != set.Manifest || !reflect.DeepEqual(input.Sources, set.Artifacts) {
			t.Fatalf("analysis input = %+v", input)
		}

		return normalizedArtifact(current.ID), nil
	})
	reconciler, err := NewAnalysisReconciler(store, executor, DefaultAnalysisConfig())
	if err != nil {
		t.Fatal(err)
	}

	return reconciler, store, claim, &calls
}

func TestAnalysisRegistersNormalizedResultThenAdvances(t *testing.T) {
	reconciler, store, claim, calls := analysisFixture(t)
	result, err := reconciler.Reconcile(context.Background(), claim)
	if err != nil || result.RetryAfter != 0 {
		t.Fatalf("Reconcile() = %+v, %v", result, err)
	}
	want := normalizedArtifact(claim.Run.ID)
	if *calls != 1 || store.listCalls != 1 ||
		!reflect.DeepEqual(store.registered, []run.Artifact{want}) ||
		store.advancingStore.calls != 1 || store.change.State != run.StateReporting ||
		store.change.Failure != nil || store.lease != claim.Lease ||
		store.revision != claim.Run.Revision {
		t.Fatalf("unexpected analysis effects: calls=%d, store=%+v", *calls, store)
	}
}

func TestAnalysisRecoversRegisteredOutputWithoutDuplicateExecution(t *testing.T) {
	reconciler, store, claim, calls := analysisFixture(t)
	output := normalizedArtifact(claim.Run.ID)
	store.artifacts = append(store.artifacts, output)

	if _, err := reconciler.Reconcile(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	if *calls != 0 || len(store.registered) != 0 || store.advancingStore.calls != 1 ||
		store.change.State != run.StateReporting {
		t.Fatalf("recovery effects: calls=%d, store=%+v", *calls, store)
	}
}

func TestAnalysisWaitsQuietlyForPendingNormalization(t *testing.T) {
	reconciler, store, claim, _ := analysisFixture(t)
	reconciler.executor = normalizeFunc(func(context.Context, string, run.Run, AnalysisInput) (run.Artifact, error) {
		return run.Artifact{}, errors.Join(ErrAnalysisPending, errors.New("job still running"))
	})

	result, err := reconciler.Reconcile(context.Background(), claim)
	if err != nil || result.RetryAfter != DefaultAnalysisConfig().RetryAfter ||
		len(store.registered) != 0 || store.advancingStore.calls != 0 {
		t.Fatalf("pending = %+v, %v; store=%+v", result, err, store)
	}
}

func TestAnalysisPersistsSafeFailure(t *testing.T) {
	reconciler, store, claim, _ := analysisFixture(t)
	reconciler.executor = normalizeFunc(func(context.Context, string, run.Run, AnalysisInput) (run.Artifact, error) {
		return run.Artifact{}, errors.Join(ErrAnalysisFailed, errors.New("secret process output"))
	})

	if _, err := reconciler.Reconcile(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	failure := store.change.Failure
	if len(store.registered) != 0 || store.advancingStore.calls != 1 ||
		store.change.State != run.StateInfrastructureFailure || failure == nil ||
		failure.Code != run.FailureCodeAnalysisError || failure.Message != "normalization failed" ||
		strings.Contains(failure.Message, "secret") {
		t.Fatalf("unsafe analysis failure: %+v", store.change)
	}
}

func TestAnalysisPreservesOperationalAndAmbiguousErrors(t *testing.T) {
	for _, want := range []error{run.ErrUnavailable, context.Canceled, run.ErrArtifactConflict} {
		t.Run(want.Error(), func(t *testing.T) {
			reconciler, store, claim, _ := analysisFixture(t)
			reconciler.executor = normalizeFunc(func(context.Context, string, run.Run, AnalysisInput) (run.Artifact, error) {
				return run.Artifact{}, want
			})
			if _, err := reconciler.Reconcile(context.Background(), claim); !errors.Is(err, want) {
				t.Fatalf("error = %v, want %v", err, want)
			}
			if len(store.registered) != 0 || store.advancingStore.calls != 0 {
				t.Fatal("operational error changed durable state")
			}
		})
	}

	reconciler, store, claim, _ := analysisFixture(t)
	reconciler.executor = normalizeFunc(func(context.Context, string, run.Run, AnalysisInput) (run.Artifact, error) {
		return run.Artifact{}, errors.Join(ErrAnalysisPending, ErrAnalysisFailed)
	})
	if _, err := reconciler.Reconcile(context.Background(), claim); !errors.Is(err, run.ErrValidation) {
		t.Fatalf("ambiguous error = %v", err)
	}
	if len(store.registered) != 0 || store.advancingStore.calls != 0 {
		t.Fatal("ambiguous classification changed durable state")
	}

	for _, classified := range []error{ErrAnalysisPending, ErrAnalysisFailed} {
		reconciler, store, claim, _ = analysisFixture(t)
		reconciler.executor = normalizeFunc(func(context.Context, string, run.Run, AnalysisInput) (run.Artifact, error) {
			return run.Artifact{}, errors.Join(classified, context.Canceled)
		})
		if _, err := reconciler.Reconcile(context.Background(), claim); !errors.Is(err, context.Canceled) {
			t.Fatalf("wrapped cancellation = %v", err)
		}
		if len(store.registered) != 0 || store.advancingStore.calls != 0 {
			t.Fatal("wrapped cancellation changed durable state")
		}
	}
}

func TestAnalysisPreservesListingAndRegistrationErrors(t *testing.T) {
	reconciler, store, claim, calls := analysisFixture(t)
	store.listErr = run.ErrUnavailable
	if _, err := reconciler.Reconcile(context.Background(), claim); !errors.Is(err, run.ErrUnavailable) {
		t.Fatalf("list error = %v", err)
	}
	if *calls != 0 || len(store.registered) != 0 || store.advancingStore.calls != 0 {
		t.Fatal("listing error changed durable state")
	}

	reconciler, store, claim, calls = analysisFixture(t)
	store.registerErr = run.ErrArtifactConflict
	if _, err := reconciler.Reconcile(context.Background(), claim); !errors.Is(err, run.ErrArtifactConflict) {
		t.Fatalf("registration error = %v", err)
	}
	if *calls != 1 || len(store.registered) != 0 || store.advancingStore.calls != 0 {
		t.Fatal("registration error advanced lifecycle")
	}
}

func TestAnalysisRejectsInvalidPersistedEvidence(t *testing.T) {
	claim := boundClaim(run.StateAnalyzing)
	valid := analysisArtifacts(claim.Run.ID)
	secondManifest := rawArtifactSet(claim.Run.ID).Manifest
	secondManifest.ID = "20000000-0000-4000-8000-000000000003"
	secondManifest.URI = "s3://perfeng-artifacts/runs/example/other-raw-result.json"
	tests := map[string][]run.Artifact{
		"empty":              nil,
		"missing manifest":   valid[:2],
		"missing sources":    valid[2:],
		"duplicate manifest": append(append([]run.Artifact{}, valid...), secondManifest),
		"wrong run":          append([]run.Artifact{}, valid...),
		"unsupported output": append(append([]run.Artifact{}, valid...), normalizedArtifact(claim.Run.ID)),
	}
	tests["wrong run"][0].RunID = "perf-20260903-120000-deadbeef"
	unsupported := tests["unsupported output"]
	unsupported[len(unsupported)-1].Format = "other-result/v1"

	for name, artifacts := range tests {
		t.Run(name, func(t *testing.T) {
			reconciler, store, current, calls := analysisFixture(t)
			store.artifacts = artifacts
			if _, err := reconciler.Reconcile(context.Background(), current); !errors.Is(err, run.ErrValidation) {
				t.Fatalf("error = %v", err)
			}
			if *calls != 0 || len(store.registered) != 0 || store.advancingStore.calls != 0 {
				t.Fatal("invalid evidence changed durable state")
			}
		})
	}
}

func TestAnalysisRejectsInvalidExecutorOutput(t *testing.T) {
	claim := boundClaim(run.StateAnalyzing)
	valid := normalizedArtifact(claim.Run.ID)
	tests := map[string]run.Artifact{
		"empty":          {},
		"wrong run":      valid,
		"raw":            valid,
		"wrong media":    valid,
		"wrong format":   valid,
		"manifest ID":    valid,
		"source URI":     valid,
		"invalid digest": valid,
	}
	wrongRun := tests["wrong run"]
	wrongRun.RunID = "perf-20260903-120000-deadbeef"
	tests["wrong run"] = wrongRun
	raw := valid
	raw.Kind = "raw"
	tests["raw"] = raw
	wrongMedia := valid
	wrongMedia.MediaType = "application/x-ndjson"
	tests["wrong media"] = wrongMedia
	wrongFormat := valid
	wrongFormat.Format = "other-result/v1"
	tests["wrong format"] = wrongFormat
	set := rawArtifactSet(claim.Run.ID)
	manifestID := valid
	manifestID.ID = set.Manifest.ID
	tests["manifest ID"] = manifestID
	sourceURI := valid
	sourceURI.URI = set.Artifacts[0].URI
	tests["source URI"] = sourceURI
	invalidDigest := valid
	invalidDigest.SHA256 = "bad"
	tests["invalid digest"] = invalidDigest

	for name, output := range tests {
		t.Run(name, func(t *testing.T) {
			reconciler, store, current, _ := analysisFixture(t)
			reconciler.executor = normalizeFunc(func(context.Context, string, run.Run, AnalysisInput) (run.Artifact, error) {
				return output, nil
			})
			if _, err := reconciler.Reconcile(context.Background(), current); !errors.Is(err, run.ErrValidation) {
				t.Fatalf("error = %v", err)
			}
			if len(store.registered) != 0 || store.advancingStore.calls != 0 {
				t.Fatal("invalid output changed durable state")
			}
		})
	}
}

func TestAnalysisRecoversAfterUncertainAdvance(t *testing.T) {
	reconciler, store, claim, calls := analysisFixture(t)
	store.advancingStore.err = run.ErrUnavailable
	if _, err := reconciler.Reconcile(context.Background(), claim); !errors.Is(err, run.ErrUnavailable) {
		t.Fatalf("first error = %v", err)
	}
	if *calls != 1 || len(store.registered) != 1 || store.advancingStore.calls != 1 {
		t.Fatalf("first attempt: calls=%d, store=%+v", *calls, store)
	}

	store.advancingStore.err = nil
	if _, err := reconciler.Reconcile(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	if *calls != 1 || len(store.registered) != 1 || store.advancingStore.calls != 2 ||
		store.change.State != run.StateReporting {
		t.Fatalf("recovery: calls=%d, store=%+v", *calls, store)
	}
}

func TestAnalysisHonorsDeadlineAndValidatesInputs(t *testing.T) {
	reconciler, store, claim, _ := analysisFixture(t)
	reconciler.config.AttemptTimeout = 5 * time.Millisecond
	reconciler.executor = normalizeFunc(func(ctx context.Context, _ string, _ run.Run, _ AnalysisInput) (run.Artifact, error) {
		<-ctx.Done()

		return run.Artifact{}, ctx.Err()
	})
	if _, err := reconciler.Reconcile(context.Background(), claim); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline error = %v", err)
	}
	if len(store.registered) != 0 || store.advancingStore.calls != 0 {
		t.Fatal("timed-out analysis changed durable state")
	}

	executor := reconciler.executor
	for _, config := range []AnalysisConfig{
		{},
		{AttemptTimeout: -time.Second, RetryAfter: time.Second},
		{AttemptTimeout: 5*time.Minute + time.Nanosecond, RetryAfter: time.Second},
		{AttemptTimeout: time.Second, RetryAfter: time.Millisecond},
		{AttemptTimeout: time.Second, RetryAfter: 5*time.Minute + time.Second},
	} {
		if _, err := NewAnalysisReconciler(store, executor, config); !errors.Is(err, run.ErrValidation) {
			t.Fatalf("config %+v error = %v", config, err)
		}
	}
	if _, err := NewAnalysisReconciler(nil, executor, DefaultAnalysisConfig()); !errors.Is(err, run.ErrValidation) {
		t.Fatal(err)
	}
	if _, err := NewAnalysisReconciler(store, nil, DefaultAnalysisConfig()); !errors.Is(err, run.ErrValidation) {
		t.Fatal(err)
	}

	reconciler, store, claim, _ = analysisFixture(t)
	claim.Lease.Token = "invalid"
	if _, err := reconciler.Reconcile(context.Background(), claim); !errors.Is(err, run.ErrValidation) {
		t.Fatal(err)
	}
	claim = boundClaim(run.StateReporting)
	if _, err := reconciler.Reconcile(context.Background(), claim); !errors.Is(err, ErrStateNotHandled) {
		t.Fatal(err)
	}
	if store.listCalls != 0 || len(store.registered) != 0 || store.advancingStore.calls != 0 {
		t.Fatal("invalid input accessed mutable dependencies")
	}
}
