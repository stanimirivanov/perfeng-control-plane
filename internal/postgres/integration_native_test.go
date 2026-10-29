package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/integration"
	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func TestIntegrationNativeBindingOfflineValidation(t *testing.T) {
	request := testRequest(t)
	record := integration.Record{Run: &run.Run{ID: "perf-20261029-120000-aabbccdd", Request: request}}
	resolved := integration.AdmissionResolution{Request: request, Target: run.ExecutionTargetKubernetes,
		Workload: rawresult.Identity{ID: "explicit-workload", Version: "1.0.0", SHA256: strings.Repeat("b", 64)}}
	binding, err := freezeIntegrationNative(record, resolved)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(binding)
	if err != nil {
		t.Fatal(err)
	}
	got, err := restoreIntegrationNative(raw, record)
	if err != nil || !reflect.DeepEqual(got, &binding) || got.Baseline != nil {
		t.Fatal("SLO-only evidence lost", err)
	}
	for _, bad := range [][]byte{[]byte(`{}`), []byte(`null`), append(raw, raw...), []byte(strings.Replace(string(raw), binding.RunID, "perf-20261029-120000-ffffffff", 1))} {
		if got, err := restoreIntegrationNative(bad, record); !errors.Is(err, run.ErrUnavailable) || got != nil {
			t.Fatal("malformed retained evidence accepted", err)
		}
	}
	resolved.Workload.SHA256 = "invalid"
	if got, err := freezeIntegrationNative(record, resolved); !errors.Is(err, run.ErrUnavailable) || !reflect.DeepEqual(got, contract.AnalysisNativeBinding{}) {
		t.Fatal("invalid review exposed native binding", err)
	}
}

func TestIntegrationNativeAtomicReplayScopedReadAndGuards(t *testing.T) {
	dsn := testDatabase(t)
	r := openTest(t, dsn)
	if err := r.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	source, artifact := completedBaselineSource(t, r, "alice", "native-binding-baseline")
	input := baselineInput(source, artifact)
	input.TestID = source.Request.TestSuite
	approved, err := r.CreateBaseline(testContext, "alice", input)
	if err != nil {
		t.Fatal(err)
	}
	samples, cv := int64(8), 0.03
	approved, err = r.TransitionBaseline(testContext, "alice", approved.ID, approved.Version, approved.Revision, baseline.Change{State: baseline.StateQualified, Actor: "reviewer", Reason: "qualified", Qualification: &baseline.Qualification{Status: baseline.QualificationPassed, SampleCount: &samples, MaximumCV: &cv}})
	if err != nil {
		t.Fatal(err)
	}
	approved, err = r.TransitionBaseline(testContext, "alice", approved.ID, approved.Version, approved.Revision, baseline.Change{State: baseline.StateApproved, Actor: "reviewer", Reason: "approved"})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(approved)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(append(encoded, '\n'))
	message := integrationRequest(t, r, "native-atomic")
	message.Data.Context.Baseline = &contract.CatalogReference{CatalogIdentity: contract.CatalogIdentity{Issuer: "https://provider.example/identity", ID: approved.ID}, Revision: approved.Version + ":3", SHA256: hex.EncodeToString(digest[:])}
	var calls atomic.Int32
	review := func(ctx context.Context, principal string, _ contract.IntegrationContext, read func(context.Context, string, string, string) (baseline.Record, error)) (integration.AdmissionResolution, error) {
		calls.Add(1)
		locked, err := read(ctx, principal, approved.ID, approved.Version)
		return integration.AdmissionResolution{Request: source.Request, Target: run.ExecutionTargetKubernetes, Workload: approved.Workload, Baseline: &locked}, err
	}
	results := make(chan integration.Record, 3)
	var wait sync.WaitGroup
	for range 3 {
		wait.Go(func() {
			got, err := r.AdmitIntegrationResolved(testContext, "alice", "https://provider.example/identity", message, review)
			if err != nil {
				t.Error(err)
				return
			}
			results <- got
		})
	}
	wait.Wait()
	close(results)
	var original integration.Record
	for got := range results {
		if got.Native == nil || got.Native.Baseline == nil || got.Native.Workload.ID != approved.Workload.ID {
			t.Fatal("accepted admission lost native evidence")
		}
		if original.Native != nil && !reflect.DeepEqual(got.Native, original.Native) {
			t.Fatal("replay changed evidence")
		}
		original = got
	}
	if calls.Load() != 1 || original.Native == nil {
		t.Fatal("review repeated or no acceptance")
	}
	if _, err := r.GetIntegration(testContext, "bob", message.Data.Context.Request); !errors.Is(err, run.ErrNotFound) {
		t.Fatal("cross-scope read", err)
	}
	for _, mutation := range []string{
		`UPDATE perfeng_control.integration_requests SET native_binding=NULL`,
		`DELETE FROM perfeng_control.integration_requests`,
		`TRUNCATE perfeng_control.integration_requests CASCADE`,
	} {
		_, err := r.db.Exec(mutation)
		var state interface{ SQLState() string }
		if !errors.As(err, &state) || state.SQLState() != "55000" {
			t.Fatal("binding mutation allowed", err)
		}
	}
	if _, err := r.TransitionBaseline(testContext, "alice", approved.ID, approved.Version, approved.Revision, baseline.Change{State: baseline.StateRetired, Actor: "reviewer", Reason: "retired"}); err != nil {
		t.Fatal(err)
	}
	reopened := openTest(t, dsn)
	never := func(context.Context, string, contract.IntegrationContext, func(context.Context, string, string, string) (baseline.Record, error)) (integration.AdmissionResolution, error) {
		t.Error("historical replay reviewed current authority")
		return integration.AdmissionResolution{}, run.ErrUnavailable
	}
	got, err := reopened.AdmitIntegrationResolved(testContext, "alice", "https://provider.example/identity", message, never)
	if err != nil || !reflect.DeepEqual(got.Native, original.Native) {
		t.Fatal("reopen/retirement lost historical binding", err)
	}
	legacy := integrationRequest(t, r, "unbound-legacy")
	old := func(context.Context, string, contract.IntegrationContext, func(context.Context, string, string, string) (baseline.Record, error)) (run.Request, run.ExecutionTarget, error) {
		return source.Request, run.ExecutionTargetKubernetes, nil
	}
	if _, err := r.AdmitIntegration(testContext, "alice", "https://provider.example/identity", legacy, old); err != nil {
		t.Fatal(err)
	}
	if got, err := r.AdmitIntegrationResolved(testContext, "alice", "https://provider.example/identity", legacy, never); err != nil || got.Native != nil {
		t.Fatal("legacy replay inferred binding", err)
	}
	_, err = r.db.Exec(`UPDATE perfeng_control.integration_requests SET native_binding=(SELECT native_binding FROM perfeng_control.integration_requests WHERE request_id=$1) WHERE request_id=$2`, message.Data.Context.Request.ID, legacy.Data.Context.Request.ID)
	var blocked interface{ SQLState() string }
	if !errors.As(err, &blocked) || blocked.SQLState() != "55000" {
		t.Fatal("legacy augmentation allowed", err)
	}
	slo := integrationRequest(t, r, "slo-native")
	slo.Data.Context.Baseline = nil
	noBaseline := func(context.Context, string, contract.IntegrationContext, func(context.Context, string, string, string) (baseline.Record, error)) (integration.AdmissionResolution, error) {
		return integration.AdmissionResolution{Request: source.Request, Target: run.ExecutionTargetKubernetes, Workload: approved.Workload}, nil
	}
	if got, err := r.AdmitIntegrationResolved(testContext, "alice", "https://provider.example/identity", slo, noBaseline); err != nil || got.Native == nil || got.Native.Baseline != nil {
		t.Fatal("explicit null baseline was confused with legacy absence", err)
	}
	denied := integrationRequest(t, r, "denied-native")
	deny := func(context.Context, string, contract.IntegrationContext, func(context.Context, string, string, string) (baseline.Record, error)) (integration.AdmissionResolution, error) {
		return integration.AdmissionResolution{}, run.ErrForbidden
	}
	if got, err := r.AdmitIntegrationResolved(testContext, "alice", "https://provider.example/identity", denied, deny); err != nil || got.Native != nil || got.Run != nil || got.Disposition.Data.Error == nil {
		t.Fatal("rejection acquired native evidence", err)
	}
	for _, mode := range []string{"missing-lock", "forged-snapshot", "invalid-workload"} {
		bad := integrationRequest(t, r, mode)
		bad.Data.Context.Baseline = message.Data.Context.Baseline
		var before, after int
		if err := r.db.QueryRow(`SELECT count(*) FROM perfeng_control.runs`).Scan(&before); err != nil {
			t.Fatal(err)
		}
		invalid := func(ctx context.Context, principal string, _ contract.IntegrationContext, read func(context.Context, string, string, string) (baseline.Record, error)) (integration.AdmissionResolution, error) {
			value := approved.Clone()
			if mode != "missing-lock" {
				var err error
				value, err = read(ctx, principal, approved.ID, approved.Version)
				if err != nil {
					return integration.AdmissionResolution{}, err
				}
			}
			if mode == "forged-snapshot" {
				value.Artifact.SHA256 = strings.Repeat("f", 64)
			}
			workload := value.Workload
			if mode == "invalid-workload" {
				workload.SHA256 = "invalid"
			}

			return integration.AdmissionResolution{Request: source.Request, Target: run.ExecutionTargetKubernetes, Workload: workload, Baseline: &value}, nil
		}
		if got, err := r.AdmitIntegrationResolved(testContext, "alice", "https://provider.example/identity", bad, invalid); !errors.Is(err, run.ErrUnavailable) || got.Native != nil {
			t.Fatal("forged authority retained", err)
		}
		if err := r.db.QueryRow(`SELECT count(*) FROM perfeng_control.runs`).Scan(&after); err != nil || before != after {
			t.Fatal("rollback leaked Run", err)
		}
		if _, err := r.GetIntegration(testContext, "alice", bad.Data.Context.Request); !errors.Is(err, run.ErrNotFound) {
			t.Fatal("rollback leaked disposition", err)
		}
	}
}
