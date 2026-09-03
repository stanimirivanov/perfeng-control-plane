package reconcile

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/stanimirivanov/perfeng-control-plane/internal/kubernetes"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/worker"
)

type resolveJobFunc func(context.Context, string, run.Run) (*batchv1.Job, error)

func (resolve resolveJobFunc) ResolveJob(ctx context.Context, principal string, current run.Run) (*batchv1.Job, error) {
	return resolve(ctx, principal, current)
}

type provisioningStageFunc func(context.Context, run.Claim) (worker.Result, error)

func (stage provisioningStageFunc) Reconcile(ctx context.Context, claim run.Claim) (worker.Result, error) {
	return stage(ctx, claim)
}

type provisioningStore struct {
	claim      run.Claim
	execution  kubernetes.Execution
	found      bool
	getErr     error
	renewErr   error
	bindErr    error
	commitBind bool
	events     []string
	boundLease run.Lease
	renewedTTL time.Duration
}

func (store *provisioningStore) GetExecution(ctx context.Context, lease run.Lease) (kubernetes.Execution, bool, error) {
	store.events = append(store.events, "lookup")
	if err := ctx.Err(); err != nil {
		return kubernetes.Execution{}, false, err
	}

	return store.execution, store.found, store.getErr
}

func (store *provisioningStore) RenewClaim(ctx context.Context, lease run.Lease, ttl time.Duration) (run.Claim, error) {
	store.events = append(store.events, "renew")
	store.renewedTTL = ttl
	if err := ctx.Err(); err != nil {
		return run.Claim{}, err
	}

	return store.claim, store.renewErr
}

func (store *provisioningStore) BindExecution(ctx context.Context, lease run.Lease, execution kubernetes.Execution) error {
	store.events = append(store.events, "bind")
	if err := ctx.Err(); err != nil {
		return err
	}
	store.boundLease = lease
	if store.bindErr == nil || store.commitBind {
		store.execution, store.found = execution, true
	}

	return store.bindErr
}

type provisioningJobs struct {
	store           *provisioningStore
	job             *batchv1.Job
	creates         int
	failAfterCreate bool
	afterCreate     func()
}

func (jobs *provisioningJobs) Create(ctx context.Context, job *batchv1.Job, _ metav1.CreateOptions) (*batchv1.Job, error) {
	jobs.store.events = append(jobs.store.events, "create")
	jobs.creates++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if jobs.job != nil {
		return nil, apierrors.NewAlreadyExists(schema.GroupResource{Resource: "jobs"}, job.Name)
	}
	jobs.job = job.DeepCopy()
	jobs.job.UID = "job-uid"
	if jobs.afterCreate != nil {
		jobs.afterCreate()
	}
	if jobs.failAfterCreate {
		return nil, apierrors.NewServiceUnavailable("ambiguous create")
	}

	return jobs.job.DeepCopy(), nil
}

func (jobs *provisioningJobs) Get(ctx context.Context, name string, _ metav1.GetOptions) (*batchv1.Job, error) {
	jobs.store.events = append(jobs.store.events, "get-job")
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if jobs.job == nil {
		return nil, apierrors.NewNotFound(schema.GroupResource{Resource: "jobs"}, name)
	}

	return jobs.job.DeepCopy(), nil
}

func (jobs *provisioningJobs) Delete(context.Context, string, metav1.DeleteOptions) error {
	return errors.New("provisioning must not delete Jobs")
}

func provisioningTemplate() *batchv1.Job {
	zero, one, deadline, automount := int32(0), int32(1), int64(900), false

	return &batchv1.Job{Spec: batchv1.JobSpec{
		BackoffLimit: &zero, Completions: &one, Parallelism: &one, ActiveDeadlineSeconds: &deadline,
		Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever, AutomountServiceAccountToken: &automount,
			Containers: []corev1.Container{{
				Name: "runner", Image: "ghcr.io/example/runner@sha256:" + strings.Repeat("a", 64),
			}},
		}},
	}}
}

func provisioningFixture(t *testing.T) (*ProvisioningReconciler, *provisioningStore, *provisioningJobs) {
	t.Helper()

	store := &provisioningStore{claim: boundClaim(run.StateProvisioning)}
	jobs := &provisioningJobs{store: store}
	dispatcher, err := kubernetes.NewDispatcher(jobs, "perf-runs")
	if err != nil {
		t.Fatal(err)
	}
	resolver := resolveJobFunc(func(ctx context.Context, principal string, current run.Run) (*batchv1.Job, error) {
		store.events = append(store.events, "resolve")
		if principal != store.claim.Lease.Principal || current.ID != store.claim.Run.ID ||
			current.Request != store.claim.Run.Request {
			t.Fatal("resolver did not receive the authenticated principal and accepted Run")
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("resolution has no attempt deadline")
		}

		return provisioningTemplate(), nil
	})
	bound := provisioningStageFunc(func(context.Context, run.Claim) (worker.Result, error) {
		store.events = append(store.events, "bound")

		return worker.Result{RetryAfter: 7 * time.Second}, nil
	})
	reconciler, err := NewProvisioningReconciler(store, resolver, dispatcher, bound, DefaultProvisioningConfig())
	if err != nil {
		t.Fatal(err)
	}

	return reconciler, store, jobs
}

func TestProvisioningDispatchesBindsThenRoutesExistingExecution(t *testing.T) {
	reconciler, store, jobs := provisioningFixture(t)
	claim := store.claim
	result, err := reconciler.Reconcile(context.Background(), claim)
	if err != nil || result.RetryAfter != 0 || !store.found || store.execution.UID != jobs.job.UID {
		t.Fatalf("dispatch result = %+v, %v; binding = %+v", result, err, store.execution)
	}
	want := []string{"lookup", "resolve", "renew", "create", "bind"}
	if !reflect.DeepEqual(store.events, want) || store.boundLease != claim.Lease ||
		store.renewedTTL != reconciler.config.LeaseTTL || store.claim.Run != claim.Run {
		t.Fatalf("unexpected dispatch order, ownership or lifecycle mutation: %v", store.events)
	}

	store.events = nil
	jobs.job = nil
	result, err = reconciler.Reconcile(context.Background(), claim)
	if err != nil || result.RetryAfter != 7*time.Second || jobs.creates != 1 ||
		!reflect.DeepEqual(store.events, []string{"lookup", "bound"}) {
		t.Fatalf("existing binding was not routed without recreation: %+v, %v, %v", result, err, store.events)
	}
}

func TestProvisioningRecoversAmbiguousCreate(t *testing.T) {
	reconciler, store, jobs := provisioningFixture(t)
	jobs.failAfterCreate = true
	if _, err := reconciler.Reconcile(context.Background(), store.claim); !errors.Is(err, run.ErrUnavailable) {
		t.Fatalf("ambiguous create error = %v", err)
	}
	if jobs.job == nil || store.found {
		t.Fatal("ambiguous create incorrectly bound or discarded the Job")
	}
	uid := jobs.job.UID
	store.events = nil
	if _, err := reconciler.Reconcile(context.Background(), store.claim); err != nil {
		t.Fatal(err)
	}
	want := []string{"lookup", "resolve", "renew", "create", "get-job", "bind"}
	if !store.found || store.execution.UID != uid || !reflect.DeepEqual(store.events, want) {
		t.Fatalf("retry did not adopt the original Job: %+v, %v", store.execution, store.events)
	}
}

func TestProvisioningRecoversAmbiguousBinding(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "not committed", true: "committed"}[committed], func(t *testing.T) {
			reconciler, store, jobs := provisioningFixture(t)
			store.bindErr, store.commitBind = run.ErrUnavailable, committed
			if _, err := reconciler.Reconcile(context.Background(), store.claim); !errors.Is(err, run.ErrUnavailable) {
				t.Fatalf("ambiguous bind error = %v", err)
			}
			uid := jobs.job.UID
			store.bindErr = nil
			if _, err := reconciler.Reconcile(context.Background(), store.claim); err != nil {
				t.Fatal(err)
			}
			if !store.found || store.execution.UID != uid {
				t.Fatal("binding retry replaced the accepted execution")
			}
			if committed && jobs.creates != 1 {
				t.Fatal("persisted identity did not prevent repeated dispatch")
			}
		})
	}
}

func TestProvisioningRechecksCancellationAndRevisionAfterResolution(t *testing.T) {
	for _, state := range []run.State{run.StateCancelling, run.StateProvisioning} {
		t.Run(string(state), func(t *testing.T) {
			reconciler, store, jobs := provisioningFixture(t)
			claim := store.claim
			reconciler.resolver = resolveJobFunc(func(context.Context, string, run.Run) (*batchv1.Job, error) {
				store.claim.Run.State = state
				store.claim.Run.Revision++

				return provisioningTemplate(), nil
			})
			result, err := reconciler.Reconcile(context.Background(), claim)
			if err != nil || result.RetryAfter != 0 || jobs.creates != 0 || store.found {
				t.Fatalf("stale claim dispatched: %+v, %v, creates=%d", result, err, jobs.creates)
			}
		})
	}
}

func TestProvisioningBindsIdentityWhenCancellationRacesSuccessfulCreate(t *testing.T) {
	reconciler, store, jobs := provisioningFixture(t)
	claim := store.claim
	jobs.afterCreate = func() {
		store.claim.Run.State = run.StateCancelling
		store.claim.Run.Revision++
	}
	if _, err := reconciler.Reconcile(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	if !store.found || store.claim.Run.State != run.StateCancelling || store.boundLease != claim.Lease {
		t.Fatal("cancellation lost the accepted identity or was overwritten")
	}
}

func TestProvisioningPreservesErrorsWithoutDispatchingAfterFailedPreflight(t *testing.T) {
	for _, boundary := range []string{"lookup", "resolve", "renew", "nil-template", "invalid-template"} {
		t.Run(boundary, func(t *testing.T) {
			reconciler, store, jobs := provisioningFixture(t)
			want := run.ErrUnavailable
			switch boundary {
			case "lookup":
				store.getErr = want
			case "resolve":
				reconciler.resolver = resolveJobFunc(func(context.Context, string, run.Run) (*batchv1.Job, error) {
					return nil, want
				})
			case "renew":
				want, store.renewErr = run.ErrLeaseLost, run.ErrLeaseLost
			case "nil-template", "invalid-template":
				want = run.ErrValidation
				reconciler.resolver = resolveJobFunc(func(context.Context, string, run.Run) (*batchv1.Job, error) {
					if boundary == "nil-template" {
						return nil, nil
					}

					return &batchv1.Job{}, nil
				})
			}
			if _, err := reconciler.Reconcile(context.Background(), store.claim); !errors.Is(err, want) {
				t.Fatalf("error = %v, want %v", err, want)
			}
			if jobs.creates != 0 || store.found {
				t.Fatal("failed preflight created or bound an execution")
			}
		})
	}
}

func TestProvisioningDeadlineStopsResolution(t *testing.T) {
	reconciler, store, jobs := provisioningFixture(t)
	reconciler.config.AttemptTimeout = 5 * time.Millisecond
	reconciler.resolver = resolveJobFunc(func(ctx context.Context, _ string, _ run.Run) (*batchv1.Job, error) {
		<-ctx.Done()

		return nil, ctx.Err()
	})
	if _, err := reconciler.Reconcile(context.Background(), store.claim); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline error = %v", err)
	}
	if jobs.creates != 0 || store.found {
		t.Fatal("timed-out resolution dispatched an execution")
	}
}

func TestProvisioningDoesNotReplaceConflictingJob(t *testing.T) {
	reconciler, store, jobs := provisioningFixture(t)
	store.bindErr = run.ErrUnavailable
	if _, err := reconciler.Reconcile(context.Background(), store.claim); !errors.Is(err, run.ErrUnavailable) {
		t.Fatal(err)
	}
	uid := jobs.job.UID
	store.bindErr = nil
	reconciler.resolver = resolveJobFunc(func(context.Context, string, run.Run) (*batchv1.Job, error) {
		template := provisioningTemplate()
		template.Spec.Template.Spec.Containers[0].Args = []string{"changed"}

		return template, nil
	})
	if _, err := reconciler.Reconcile(context.Background(), store.claim); !errors.Is(err, kubernetes.ErrJobConflict) {
		t.Fatalf("conflicting Job error = %v", err)
	}
	if store.found || jobs.job.UID != uid || len(jobs.job.Spec.Template.Spec.Containers[0].Args) != 0 {
		t.Fatal("conflicting Job was bound or changed")
	}
}

func TestProvisioningPreservesLostOwnershipAndCancellationAfterCreate(t *testing.T) {
	for _, failure := range []error{run.ErrLeaseLost, kubernetes.ErrExecutionConflict, context.Canceled} {
		t.Run(failure.Error(), func(t *testing.T) {
			reconciler, store, jobs := provisioningFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if errors.Is(failure, context.Canceled) {
				jobs.afterCreate = cancel
			} else {
				store.bindErr = failure
			}
			if _, err := reconciler.Reconcile(ctx, store.claim); !errors.Is(err, failure) {
				t.Fatalf("bind error = %v, want %v", err, failure)
			}
			if store.found || jobs.job == nil {
				t.Fatal("failed binding fabricated persistence or deleted the accepted Job")
			}
		})
	}
}

func TestProvisioningRefusesNonProvisioningAndInvalidClaims(t *testing.T) {
	for _, state := range []run.State{run.StateCreated, run.StateValidating, run.StateRunning, run.StateCancelling, run.StateAborted} {
		reconciler, store, _ := provisioningFixture(t)
		claim := store.claim
		claim.Run.State = state
		if _, err := reconciler.Reconcile(context.Background(), claim); !errors.Is(err, ErrStateNotHandled) {
			t.Fatalf("state %s error = %v", state, err)
		}
		if len(store.events) != 0 {
			t.Fatalf("state %s accessed dependencies", state)
		}
	}
	reconciler, store, _ := provisioningFixture(t)
	claim := store.claim
	claim.Lease.Token = "invalid"
	if _, err := reconciler.Reconcile(context.Background(), claim); !errors.Is(err, run.ErrValidation) {
		t.Fatalf("invalid lease error = %v", err)
	}
}

func TestProvisioningValidatesConfigurationAndDependencies(t *testing.T) {
	reconciler, _, _ := provisioningFixture(t)
	for _, config := range []ProvisioningConfig{
		{},
		{LeaseTTL: time.Second, AttemptTimeout: time.Millisecond},
		{LeaseTTL: 30 * time.Second},
		{LeaseTTL: 30 * time.Second, AttemptTimeout: 16 * time.Second},
	} {
		if _, err := NewProvisioningReconciler(reconciler.store, reconciler.resolver, reconciler.dispatcher, reconciler.bound, config); !errors.Is(err, run.ErrValidation) {
			t.Fatalf("config %+v error = %v", config, err)
		}
	}
	for _, missing := range []string{"store", "resolver", "dispatcher", "bound"} {
		t.Run(missing, func(t *testing.T) {
			candidate := *reconciler
			switch missing {
			case "store":
				candidate.store = nil
			case "resolver":
				candidate.resolver = nil
			case "dispatcher":
				candidate.dispatcher = nil
			case "bound":
				candidate.bound = nil
			}
			if _, err := NewProvisioningReconciler(candidate.store, candidate.resolver, candidate.dispatcher, candidate.bound, candidate.config); !errors.Is(err, run.ErrValidation) {
				t.Fatalf("missing %s error = %v", missing, err)
			}
		})
	}
}
