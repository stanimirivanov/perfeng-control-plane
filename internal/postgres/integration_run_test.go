package postgres

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/integration"
	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func TestRunAdmissionOfflineScopeAndRecheck(t *testing.T) {
	scope := integration.RunAdmissionScope{Provider: "https://provider.example", Principal: contract.CatalogIdentity{Issuer: "https://principal.example", ID: "alice"}, RunID: "native-run"}
	for field := range 4 {
		for _, invalid := range []string{"", " ", "nul\x00value"} {
			changed := scope
			fields := []*string{&changed.Provider, &changed.Principal.Issuer, &changed.Principal.ID, &changed.RunID}
			*fields[field] = invalid
			got, err := (&Repository{}).GetIntegrationForRun(testContext, changed)
			if !errors.Is(err, run.ErrValidation) || !reflect.DeepEqual(got, integration.Record{}) {
				t.Fatal("invalid scope reached storage", err)
			}
		}
	}
	ctx, cancel := context.WithCancel(testContext)
	cancel()
	if got, err := (&Repository{}).GetIntegrationForRun(ctx, scope); !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, integration.Record{}) {
		t.Fatal("cancelled lookup reached storage", err)
	}
	record := integration.Record{Run: &run.Run{ID: scope.RunID}}
	record.Disposition.Source = scope.Provider
	record.Disposition.Data.Context.Principal = scope.Principal
	if !runAdmissionMatches(record, scope) {
		t.Fatal("matching retained scope rejected")
	}
	for field := range 4 {
		changed := scope
		fields := []*string{&changed.Provider, &changed.Principal.Issuer, &changed.Principal.ID, &changed.RunID}
		*fields[field] += "-changed"
		if runAdmissionMatches(record, changed) {
			t.Fatal("changed legacy mapping escaped visibility recheck", field)
		}
	}
	record.Run = nil
	if runAdmissionMatches(record, scope) {
		t.Fatal("missing Run accepted")
	}
}

func TestRunAdmissionScopedHistoryOwnershipAndReopen(t *testing.T) {
	dsn := testDatabase(t)
	r := openTest(t, dsn)
	if err := r.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	evidence, request := verificationFixture(t)
	message := integrationRequest(t, r, "run-admission")
	message.Data.Context.Baseline = nil
	review := func(context.Context, string, contract.IntegrationContext, func(context.Context, string, string, string) (baseline.Record, error)) (integration.AdmissionResolution, error) {
		return integration.AdmissionResolution{Request: request, Target: run.ExecutionTargetKubernetes, Verification: &evidence,
			Workload: rawresult.Identity{ID: "retained-workload", Version: "1.0.0", SHA256: strings.Repeat("b", 64)}}, nil
	}
	admitted, err := r.AdmitIntegrationResolved(testContext, "alice", "https://provider.example", message, review)
	if err != nil {
		t.Fatal(err)
	}
	scope := integration.RunAdmissionScope{Provider: admitted.Disposition.Source, Principal: message.Data.Context.Principal, RunID: admitted.Run.ID}
	got, err := r.GetIntegrationForRun(testContext, scope)
	if err != nil || !reflect.DeepEqual(got, admitted) {
		t.Fatal("retained admission differs", err)
	}
	got.Verification.PolicyBytes[0] = '!'
	got.Native.Workload.ID = "mutated"
	got.Disposition.Data.Context.Profile.Revision = "mutated"
	reopened := openTest(t, dsn)
	got, err = reopened.GetIntegrationForRun(testContext, scope)
	if err != nil || !reflect.DeepEqual(got, admitted) {
		t.Fatal("owned history lost on reopen", err)
	}
	for field := range 4 {
		changed := scope
		fields := []*string{&changed.Provider, &changed.Principal.Issuer, &changed.Principal.ID, &changed.RunID}
		*fields[field] += "-unknown"
		assertRunAdmissionAbsent(t, r, changed)
	}
	native := accepted(t, r, "alice", "native-only-run-admission")
	nativeScope := scope
	nativeScope.RunID = native.Run.ID
	assertRunAdmissionAbsent(t, r, nativeScope)
	cancellation := message
	cancellation.ID += "-cancel"
	cancellation.Type = "dev.perfeng.integration.cancel-requested.v1"
	cancellation.Data.Reason = "user"
	if _, err := r.CancelIntegration(testContext, "alice", cancellation); err != nil {
		t.Fatal(err)
	}
	got, err = r.GetIntegrationForRun(testContext, scope)
	if err != nil || got.Run == nil || got.Run.State != run.StateAborted || !reflect.DeepEqual(got.Disposition, admitted.Disposition) ||
		!reflect.DeepEqual(got.Native, admitted.Native) || !reflect.DeepEqual(got.Verification, admitted.Verification) {
		t.Fatal("lifecycle progress changed historical evidence", err)
	}
}

func assertRunAdmissionAbsent(t *testing.T, r *Repository, scope integration.RunAdmissionScope) {
	t.Helper()
	got, err := r.GetIntegrationForRun(testContext, scope)
	if !errors.Is(err, run.ErrNotFound) || !reflect.DeepEqual(got, integration.Record{}) {
		t.Fatal("unknown/unassociated/invisible Run exposed", err)
	}
}

func TestRunAdmissionPreservesLegacyAndCustomAbsence(t *testing.T) {
	r := openTest(t, testDatabase(t))
	if err := r.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	for _, resolved := range []bool{false, true} {
		message := integrationRequest(t, r, "run-absence-"+strings.ToLower(t.Name()))
		if resolved {
			message.ID += "-resolved"
			message.Data.Context.Request.ID += "-resolved"
			message.Data.Context.Attempt.ID += "-resolved"
		}
		message.Data.Context.Baseline = nil
		request := testRequest(t)
		var admitted integration.Record
		var err error
		if resolved {
			admitted, err = r.AdmitIntegrationResolved(testContext, "alice", "https://provider.example", message,
				func(context.Context, string, contract.IntegrationContext, func(context.Context, string, string, string) (baseline.Record, error)) (integration.AdmissionResolution, error) {
					return integration.AdmissionResolution{Request: request, Target: run.ExecutionTargetKubernetes,
						Workload: rawresult.Identity{ID: "retained-workload", Version: "1.0.0", SHA256: strings.Repeat("b", 64)}}, nil
				})
		} else {
			admitted, err = r.AdmitIntegration(testContext, "alice", "https://provider.example", message,
				func(context.Context, string, contract.IntegrationContext, func(context.Context, string, string, string) (baseline.Record, error)) (run.Request, run.ExecutionTarget, error) {
					return request, run.ExecutionTargetKubernetes, nil
				})
		}
		if err != nil {
			t.Fatal(err)
		}
		got, err := r.GetIntegrationForRun(testContext, integration.RunAdmissionScope{Provider: admitted.Disposition.Source, Principal: message.Data.Context.Principal, RunID: admitted.Run.ID})
		if err != nil || got.Verification != nil || (got.Native != nil) != resolved || !reflect.DeepEqual(got, admitted) {
			t.Fatal("historical absence reconstructed", err)
		}
	}
}
