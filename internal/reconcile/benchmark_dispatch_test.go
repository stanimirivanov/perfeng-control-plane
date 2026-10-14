package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/benchmark"
	"github.com/stanimirivanov/perfeng-control-plane/internal/kubernetes"
	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/worker"
)

type benchmarkStore struct {
	*provisioningStore
	reservation run.BenchmarkReservation
	held        bool
	reserveErr  error
	sealErr     error
}

func (store *benchmarkStore) GetBenchmarkReservation(context.Context, run.Lease) (run.BenchmarkReservation, bool, error) {
	store.events = append(store.events, "reservation")
	return store.reservation.Clone(), store.held, nil
}

func (store *benchmarkStore) ReserveBenchmark(_ context.Context, lease run.Lease, revision int64, intent run.BenchmarkIntent, admission []byte) (run.BenchmarkReservation, error) {
	store.events = append(store.events, "reserve")
	if store.reserveErr != nil {
		return run.BenchmarkReservation{}, store.reserveErr
	}
	if err := store.provisioningFence(revision); err != nil {
		return run.BenchmarkReservation{}, err
	}
	if !store.held {
		store.reservation = run.BenchmarkReservation{RunID: lease.RunID, Intent: intent, Admission: slices.Clone(admission)}
		store.held = true
	}
	return store.reservation.Clone(), nil
}

func (store *benchmarkStore) BindBenchmarkNamespace(_ context.Context, _ run.Lease, revision int64, hash, uid string) (run.BenchmarkReservation, error) {
	store.events = append(store.events, "bind-namespace")
	if err := store.provisioningFence(revision); err != nil {
		return run.BenchmarkReservation{}, err
	}
	if store.reservation.Intent.PlanSHA256 != hash || (store.reservation.NamespaceUID != "" && store.reservation.NamespaceUID != uid) {
		return run.BenchmarkReservation{}, run.ErrBenchmarkConflict
	}
	store.reservation.NamespaceUID = uid
	return store.reservation.Clone(), nil
}

func (store *benchmarkStore) SealBenchmarkRequest(_ context.Context, _ run.Lease, revision int64, _ string, request []byte) (run.BenchmarkReservation, error) {
	store.events = append(store.events, "seal")
	if err := store.provisioningFence(revision); err != nil {
		return run.BenchmarkReservation{}, err
	}
	if store.sealErr != nil {
		return run.BenchmarkReservation{}, store.sealErr
	}
	store.reservation.Request = slices.Clone(request)
	return store.reservation.Clone(), nil
}

func (store *benchmarkStore) provisioningFence(revision int64) error {
	if store.renewErr != nil {
		return store.renewErr
	}
	if store.claim.Run.Revision != revision {
		return run.ErrRevision
	}
	if store.claim.Run.State != run.StateProvisioning {
		return run.ErrTransition
	}
	if store.reservation.Cleanup != nil {
		return run.ErrBenchmarkConflict
	}
	return nil
}

func (store *benchmarkStore) BeginBenchmarkCleanup(_ context.Context, _ run.Lease, revision int64, hash string, next run.Change) (run.BenchmarkReservation, error) {
	store.events = append(store.events, "begin-cleanup")
	if store.renewErr != nil {
		return run.BenchmarkReservation{}, store.renewErr
	}
	if store.claim.Run.Revision != revision {
		return run.BenchmarkReservation{}, run.ErrRevision
	}
	if !store.held || store.reservation.Intent.PlanSHA256 != hash {
		return run.BenchmarkReservation{}, run.ErrBenchmarkConflict
	}
	if store.reservation.Cleanup == nil {
		store.reservation.Cleanup = &run.BenchmarkCleanup{Next: next}
	}
	return store.reservation.Clone(), nil
}
func (store *benchmarkStore) BindBenchmarkCleanupNamespace(_ context.Context, _ run.Lease, _ int64, _, uid string) (run.BenchmarkReservation, error) {
	if store.reservation.NamespaceUID != "" && store.reservation.NamespaceUID != uid {
		return run.BenchmarkReservation{}, run.ErrBenchmarkConflict
	}
	store.reservation.NamespaceUID = uid
	return store.reservation.Clone(), nil
}
func (store *benchmarkStore) RecordBenchmarkCleanupFence(context.Context, run.Lease, int64, string) (run.BenchmarkReservation, error) {
	store.events = append(store.events, "record-fence")
	return store.reservation.Clone(), nil
}
func (store *benchmarkStore) FinishBenchmarkCleanup(_ context.Context, _ run.Lease, revision int64, _ string, receipt []byte) (run.Run, error) {
	store.events = append(store.events, "finish-cleanup")
	if store.claim.Run.Revision != revision {
		return run.Run{}, run.ErrRevision
	}
	if store.reservation.Cleanup == nil || !store.reservation.Cleanup.Ready {
		return run.Run{}, run.ErrBenchmarkConflict
	}
	if store.claim.Run.State == run.StateCancelling {
		store.reservation.Cleanup.Next = run.Change{State: run.StateAborted}
	}
	store.advanced = store.reservation.Cleanup.Next
	store.reservation.Cleanup.ReleasedAt = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	store.reservation.Cleanup.Receipt = slices.Clone(receipt)
	return store.claim.Run, nil
}

type benchmarkResolver struct {
	plan     BenchmarkPlan
	err      error
	selected bool
}

func (*benchmarkResolver) HasBenchmarks() bool { return true }
func (resolver *benchmarkResolver) ResolveBenchmark(context.Context, string, run.Run) (BenchmarkPlan, bool, error) {
	return resolver.plan.Clone(), resolver.selected, resolver.err
}

type benchmarkTarget struct {
	store          *benchmarkStore
	err            error
	afterNamespace func()
	namespaceErr   error
	resourcesErr   error
	cleanupErr     error
	clean          bool
}

func (target *benchmarkTarget) InspectCleanupNamespace(context.Context, run.BenchmarkReservation) (string, bool, error) {
	target.store.events = append(target.store.events, "inspect-cleanup")
	return "00000000-0000-4000-8000-000000000002", true, target.namespaceErr
}
func (target *benchmarkTarget) CloseBenchmarkTarget(ctx context.Context, _ run.BenchmarkReservation, fence kubernetes.BenchmarkFence) error {
	if err := fence(ctx); err != nil {
		return err
	}
	target.store.events = append(target.store.events, "close-quota")
	return target.cleanupErr
}
func (target *benchmarkTarget) CleanupBenchmark(ctx context.Context, _ benchmark.Spec, _ run.BenchmarkReservation, _ *kubernetes.Execution, fence kubernetes.BenchmarkFence) (bool, []byte, error) {
	if err := fence(ctx); err != nil {
		return false, nil, err
	}
	target.store.events = append(target.store.events, "cleanup")
	return target.clean, []byte(`{"verified":true}`), target.cleanupErr
}

func (target *benchmarkTarget) Qualify(context.Context, benchmark.Spec) ([]byte, error) {
	target.store.events = append(target.store.events, "qualify")
	return []byte(`{"status":"ACCEPTED"}`), target.err
}
func (target *benchmarkTarget) EnsureNamespace(ctx context.Context, _ run.BenchmarkReservation, fence kubernetes.BenchmarkFence) (string, error) {
	if err := fence(ctx); err != nil {
		return "", err
	}
	target.store.events = append(target.store.events, "namespace")
	if target.afterNamespace != nil {
		target.afterNamespace()
	}
	return "00000000-0000-4000-8000-000000000002", target.namespaceErr
}
func (target *benchmarkTarget) CheckNamespace(context.Context, run.BenchmarkReservation) error {
	target.store.events = append(target.store.events, "check-namespace")
	return nil
}
func (target *benchmarkTarget) EnsureResources(ctx context.Context, _ benchmark.Spec, _ run.BenchmarkReservation, fence kubernetes.BenchmarkFence) error {
	if err := fence(ctx); err != nil {
		return err
	}
	target.store.events = append(target.store.events, "resources")
	return target.resourcesErr
}

type benchmarkExecutionController struct{ store *benchmarkStore }

func (*benchmarkExecutionController) ObserveJob(context.Context, kubernetes.Execution) (kubernetes.Observation, error) {
	return kubernetes.Observation{Phase: kubernetes.JobRunning}, nil
}
func (controller *benchmarkExecutionController) RequestJobStop(context.Context, kubernetes.Execution) error {
	controller.store.events = append(controller.store.events, "stop")
	return nil
}

func benchmarkPlanFixture(t *testing.T) BenchmarkPlan {
	t.Helper()
	descriptor := []byte(`{"cluster":"perfeng-local","fingerprint":"` + strings.Repeat("d", 64) + `","kubernetes":{"version":"v1.35.0","nodeCount":3,"nodePools":[{"name":"control-plane","cpuArchitecture":"amd64"},{"name":"performance-generator","cpuArchitecture":"amd64"},{"name":"sut","cpuArchitecture":"amd64"}]},"application":{"configurationHash":"` + benchmark.ProfileSHA256 + `"}}`)
	spec := benchmark.Spec{
		ClusterID: "perfeng-local", ClusterUID: "00000000-0000-4000-8000-000000000001", EnvironmentBytes: descriptor,
		Environment:    baseline.Environment{Identity: rawresult.Identity{ID: "local-kind-integration", Version: "1.0.0", SHA256: run.BenchmarkDigest(descriptor)}, Fingerprint: strings.Repeat("d", 64)},
		Workload:       rawresult.Identity{ID: "pod-startup-contained", Version: "1.0.0", SHA256: benchmark.WorkloadSHA256},
		TemplateSHA256: benchmark.TemplateSHA256, RunnerImage: "ghcr.io/stanimirivanov/perfeng-kube-burner@sha256:" + strings.Repeat("a", 64), Bucket: "perfeng-artifacts",
	}
	plan := BenchmarkPlan{Selection: spec, Template: kubernetes.BenchmarkJobTemplate(spec.RunnerImage, "registry", "storage", "http://seaweedfs-s3.perf-platform.svc.cluster.local:8333")}
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
	return plan
}

func newBenchmarkReconcilerFixture(t *testing.T) (*BenchmarkReconciler, *benchmarkStore, *benchmarkTarget, *provisioningJobs, *benchmarkResolver) {
	t.Helper()
	store := &benchmarkStore{provisioningStore: &provisioningStore{claim: boundClaim(run.StateProvisioning)}}
	target := &benchmarkTarget{store: store}
	jobs := &provisioningJobs{store: store.provisioningStore}
	dispatcher, err := kubernetes.NewDispatcher(jobs, "perf-generators")
	if err != nil {
		t.Fatal(err)
	}
	resolver := &benchmarkResolver{plan: benchmarkPlanFixture(t), selected: true}
	bound := provisioningStageFunc(func(context.Context, run.Claim) (worker.Result, error) {
		store.events = append(store.events, "bound")
		return worker.Result{RetryAfter: 7 * time.Second}, nil
	})
	fallback := provisioningStageFunc(func(context.Context, run.Claim) (worker.Result, error) {
		store.events = append(store.events, "fallback")
		return worker.Result{}, nil
	})
	reconciler, err := NewBenchmarkReconciler(store, resolver, target, dispatcher, &benchmarkExecutionController{store: store}, bound, fallback, DefaultProvisioningConfig())
	if err != nil {
		t.Fatal(err)
	}
	return reconciler, store, target, jobs, resolver
}

func TestBenchmarkDispatchReservesAndSealsBeforeExternalAuthority(t *testing.T) {
	reconciler, store, _, jobs, _ := newBenchmarkReconcilerFixture(t)
	if _, err := reconciler.Reconcile(context.Background(), store.claim); err != nil {
		t.Fatal(err)
	}
	last := -1
	for _, event := range []string{"qualify", "reserve", "namespace", "bind-namespace", "seal", "resources", "create", "bind"} {
		index := slices.Index(store.events, event)
		if index <= last {
			t.Fatalf("unsafe dispatch ordering for %s: %v", event, store.events)
		}
		last = index
	}
	if !store.held || !store.found || len(store.reservation.Request) == 0 || store.reservation.NamespaceUID == "" {
		t.Fatal("dispatch has no durable input/namespace identity")
	}
	jobs.job = nil
	store.events = nil
	if result, err := reconciler.Reconcile(context.Background(), store.claim); err != nil || result.RetryAfter != 7*time.Second || jobs.creates != 1 || !slices.Contains(store.events, "bound") {
		t.Fatalf("bound execution recreated: %v, %v", store.events, err)
	}
}

func TestBenchmarkRejectedAdmissionAndContentionHaveNoTargetWrites(t *testing.T) {
	for _, failure := range []error{benchmark.ErrNotQualified, run.ErrTargetReserved, run.ErrLeaseLost} {
		t.Run(failure.Error(), func(t *testing.T) {
			reconciler, store, target, jobs, _ := newBenchmarkReconcilerFixture(t)
			if errors.Is(failure, benchmark.ErrNotQualified) {
				target.err = failure
			} else {
				store.reserveErr = failure
			}
			if _, err := reconciler.Reconcile(context.Background(), store.claim); !errors.Is(err, failure) {
				t.Fatalf("error=%v", err)
			}
			if jobs.creates != 0 || slices.Contains(store.events, "namespace") || slices.Contains(store.events, "resources") {
				t.Fatalf("unadmitted writes: %v", store.events)
			}
		})
	}
}

func TestBenchmarkAmbiguousCreateRecoversWithoutFreshWrites(t *testing.T) {
	reconciler, store, target, jobs, resolver := newBenchmarkReconcilerFixture(t)
	jobs.failAfterCreate = true
	if _, err := reconciler.Reconcile(context.Background(), store.claim); err == nil || store.found || jobs.job == nil {
		t.Fatal("ambiguous create fixture failed")
	}
	sealed := slices.Clone(store.reservation.Request)
	resolver.err = run.ErrForbidden
	target.err = benchmark.ErrNotQualified
	store.events = nil
	if _, err := reconciler.Reconcile(context.Background(), store.claim); err != nil || !store.found || jobs.creates != 1 {
		t.Fatalf("ambiguous create recovery failed: %v", err)
	}
	if slices.Contains(store.events, "namespace") || slices.Contains(store.events, "resources") || slices.Contains(store.events, "qualify") || string(sealed) != string(store.reservation.Request) {
		t.Fatalf("recovery rematerialized inputs: %v", store.events)
	}
}

func TestBenchmarkCancellationWinsPartialMaterializationAndRetainsHold(t *testing.T) {
	reconciler, store, target, jobs, _ := newBenchmarkReconcilerFixture(t)
	target.afterNamespace = func() { store.claim.Run.State = run.StateCancelling; store.claim.Run.Revision++ }
	claim := store.claim
	if _, err := reconciler.Reconcile(context.Background(), claim); !errors.Is(err, run.ErrRevision) {
		t.Fatalf("cancelled materialization accepted: %v", err)
	}
	if !store.held || jobs.creates != 0 || len(store.reservation.Request) != 0 {
		t.Fatal("cancelled materialization escaped fence")
	}
	store.events = nil
	if _, err := reconciler.Reconcile(context.Background(), store.claim); err != nil {
		t.Fatal(err)
	}
	if !store.held || store.claim.Run.State != run.StateCancelling || slices.Contains(store.events, "namespace") || slices.Contains(store.events, "advance") {
		t.Fatalf("cancellation released/created/completed prematurely: %v", store.events)
	}
}

func TestBenchmarkCancellationRecoversAmbiguousJobAndStopsOnlyExecutor(t *testing.T) {
	reconciler, store, _, jobs, _ := newBenchmarkReconcilerFixture(t)
	jobs.failAfterCreate = true
	if _, err := reconciler.Reconcile(context.Background(), store.claim); err == nil {
		t.Fatal("ambiguous create expected")
	}
	store.claim.Run.State = run.StateCancelling
	store.claim.Run.Revision++
	store.events = nil
	if _, err := reconciler.Reconcile(context.Background(), store.claim); err != nil {
		t.Fatal(err)
	}
	if !store.held || !store.found || !slices.Contains(store.events, "stop") || slices.Contains(store.events, "advance") || slices.Contains(store.events, "create") {
		t.Fatalf("unsafe cancellation: %v", store.events)
	}
}

func TestBenchmarkSealingFailureAndLeaseLossNeverCreateExecutor(t *testing.T) {
	for _, failure := range []string{"seal", "lease"} {
		t.Run(failure, func(t *testing.T) {
			reconciler, store, target, jobs, _ := newBenchmarkReconcilerFixture(t)
			if failure == "seal" {
				store.sealErr = run.ErrUnavailable
			} else {
				target.afterNamespace = func() { store.renewErr = run.ErrLeaseLost }
			}
			if _, err := reconciler.Reconcile(context.Background(), store.claim); err == nil {
				t.Fatal("expected fence failure")
			}
			if jobs.creates != 0 || slices.Contains(store.events, "resources") {
				t.Fatalf("executor/authority created after failed seal: %v", store.events)
			}
		})
	}
}

func TestBenchmarkIntentRoundTripPreservesExactDescriptorBytes(t *testing.T) {
	plan := benchmarkPlanFixture(t)
	intent, err := plan.Intent()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := RestoreBenchmarkPlan(intent)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored.Selection.EnvironmentBytes) != string(plan.Selection.EnvironmentBytes) {
		t.Fatal("intent compacted exact descriptor bytes")
	}
	var encoded map[string]json.RawMessage
	if err := json.Unmarshal(intent.Plan, &encoded); err != nil {
		t.Fatal(err)
	}
	changed := intent
	changed.Plan = []byte(strings.Replace(string(intent.Plan), `"version":1,`, `"version":2,`, 1))
	changed.PlanSHA256 = run.BenchmarkDigest(changed.Plan)
	if _, err := RestoreBenchmarkPlan(changed); !errors.Is(err, run.ErrValidation) {
		t.Fatalf("unknown stored plan version accepted: %v", err)
	}
	intent.Plan[0] = 'x'
	if _, err := RestoreBenchmarkPlan(intent); !errors.Is(err, run.ErrValidation) {
		t.Fatalf("corrupt intent accepted: %v", err)
	}
}
