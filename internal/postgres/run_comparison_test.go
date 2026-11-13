package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/policy"
	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/runcomparison"
)

func comparisonInputsFixture(t *testing.T, r *Repository, key string) (run.Run, runcomparison.Inputs) {
	t.Helper()
	source, artifact := completedBaselineSource(t, r, "alice", key+"-baseline")
	creation := baselineInput(source, artifact)
	creation.TestID = source.Request.TestSuite
	record, err := r.CreateBaseline(testContext, "alice", creation)
	if err != nil {
		t.Fatal(err)
	}
	count, cv := int64(30), 0.05
	record, err = r.TransitionBaseline(testContext, "alice", record.ID, record.Version, 1, baseline.Change{State: baseline.StateQualified, Actor: "reviewer", Reason: "qualified", Qualification: &baseline.Qualification{Status: baseline.QualificationPassed, Reasons: []string{}, SampleCount: &count, MaximumCV: &cv}})
	if err != nil {
		t.Fatal(err)
	}
	record, err = r.TransitionBaseline(testContext, "alice", record.ID, record.Version, 2, baseline.Change{State: baseline.StateApproved, Actor: "approver", Reason: "approved"})
	if err != nil {
		t.Fatal(err)
	}
	document := []byte(`{"apiVersion":"performance.perfeng.io/v1","kind":"PerformancePolicy","metadata":{"name":"checkout-policy","version":"1.0.0","owner":"tests"},"spec":{"mode":"inform","missingData":"inconclusive","rules":[{"id":"latency","metric":{"name":"api.http.duration","statistic":"p95","unit":"ms"},"slo":{"max":200},"regression":{"direction":"lower-is-better","practicalDifference":{"kind":"relative","value":0.1},"reference":{"baselineId":"approved-search-api","version":"2.0.0"}}}]}}`)
	request := testRequest(t)
	hash := sha256.Sum256(document)
	request.Policy.SHA256 = hex.EncodeToString(hash[:])
	accepted, err := r.Accept(testContext, "alice", key, request, run.ExecutionTargetKubernetes)
	if err != nil {
		t.Fatal(err)
	}
	current := accepted.Run
	for _, state := range []run.State{run.StateValidating, run.StateProvisioning, run.StateRunning, run.StateCollecting, run.StateAnalyzing, run.StateReporting} {
		current, err = r.Advance(testContext, "alice", current.ID, current.Revision, run.Change{State: state})
		if err != nil {
			t.Fatal(err)
		}
	}
	candidate := artifactFor(current.ID)
	candidate.Kind = "normalized"
	candidate.Format = "normalized-result/v1"
	candidate.ID = "2cfa0000-0000-4000-8000-000000000001"
	if err := r.RegisterArtifact(testContext, "alice", candidate); err != nil {
		t.Fatal(err)
	}
	inputs := runcomparison.Inputs{ContractsVersion: "0.21.0", Request: request, Candidate: candidate, Workload: record.Workload, PolicyBytes: document, PolicyMode: "inform", Producer: rawresult.Producer{Name: "perfeng-analysis", Version: "1.0.0", Image: "ghcr.io/example/analysis@sha256:" + strings.Repeat("a", 64)}, Baselines: []policy.BaselineResolution{{ID: record.ID, Version: record.Version, Artifact: &record.Artifact}}, BaselineSnapshots: []baseline.Record{record}}
	return current, inputs
}

func TestReportCaptureMigrationDoesNotEnrollHistory(t *testing.T) {
	dsn := testDatabase(t)
	owner := openTest(t, dsn)
	migrateBeforeVerification(t, owner, "0026_")
	legacy := accepted(t, owner, "alice", "legacy-capture-01").Run
	if err := owner.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.db.Exec(`UPDATE perfeng_control.runs SET report_capture_enrolled=true WHERE run_id=$1`, legacy.ID); err == nil {
		t.Fatal("historical enrollment changed")
	}
	captured, err := OpenWithReportCapture(testContext, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeTest(t, captured) })
	replay := accepted(t, captured, "alice", "legacy-capture-01").Run
	var enrolled bool
	if err := owner.db.QueryRow(`SELECT report_capture_enrolled FROM perfeng_control.runs WHERE run_id=$1`, replay.ID).Scan(&enrolled); err != nil || enrolled {
		t.Fatal("idempotency replay backfilled legacy Run", err)
	}
	if _, err := captured.GetReportInputs(testContext, "alice", legacy.ID); !errors.Is(err, run.ErrNotFound) {
		t.Fatal("history reconstructed", err)
	}
}

func TestReportInputsDurableConcurrentReplayAndRetirement(t *testing.T) {
	dsn := testDatabase(t)
	owner := openTest(t, dsn)
	if err := owner.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	capture, err := OpenWithReportCapture(testContext, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeTest(t, capture) })
	current, inputs := comparisonInputsFixture(t, capture, "comparison-request-01")
	// Terminate a real writer immediately after commit, without pool/defer cleanup.
	wire, err := json.Marshal(struct {
		Current run.Run
		Inputs  runcomparison.Inputs
	}{current, inputs})
	if err != nil {
		t.Fatal(err)
	}
	childContext, cancel := context.WithTimeout(testContext, 30*time.Second)
	defer cancel()
	child := exec.CommandContext(childContext, os.Args[0], "-test.run=^TestReportInputsCrashChild$")
	child.Env = append(os.Environ(), "PERFENG_REPORT_INPUTS_CHILD="+dsn)
	child.Stdin = bytes.NewReader(wire)
	output, err := child.CombinedOutput()
	var exited *exec.ExitError
	if !errors.As(err, &exited) || exited.ExitCode() != 17 {
		t.Fatalf("capture writer did not terminate after commit: %v %s", err, output)
	}

	var wait sync.WaitGroup
	failures := make(chan error, 6)
	for range 6 {
		wait.Go(func() {
			got, err := capture.RetainReportInputs(testContext, "alice", current, inputs)
			if err == nil && !reflect.DeepEqual(got, inputs) {
				err = run.ErrConflict
			}
			failures <- err
		})
	}
	wait.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := capture.GetReportInputs(testContext, "other", current.ID); !errors.Is(err, run.ErrNotFound) {
		t.Fatal("cross-principal inputs visible", err)
	}
	record := inputs.BaselineSnapshots[0]
	if _, err := capture.TransitionBaseline(testContext, "alice", record.ID, record.Version, record.Revision, baseline.Change{State: baseline.StateRetired, Actor: "approver", Reason: "retired later"}); err != nil {
		t.Fatal(err)
	}
	reopened := openTest(t, dsn)
	got, err := reopened.GetReportInputs(testContext, "alice", current.ID)
	if err != nil || !reflect.DeepEqual(got, inputs) {
		t.Fatal("retirement/reopen changed report inputs", err)
	}
	if _, err := reopened.RetainReportInputs(testContext, "alice", current, inputs); err != nil {
		t.Fatal("replay reviewed current approval", err)
	}
	changed := inputs.Clone()
	changed.Producer.Version = "2.0.0"
	if _, err := reopened.RetainReportInputs(testContext, "alice", current, changed); !errors.Is(err, run.ErrConflict) {
		t.Fatal("accepted inputs replaced", err)
	}
	got.PolicyBytes[0] = 'x'
	got.BaselineSnapshots[0].ID = "changed"
	again, err := reopened.GetReportInputs(testContext, "alice", current.ID)
	if err != nil || !reflect.DeepEqual(again, inputs) {
		t.Fatal("returned selection aliased", err)
	}
	for _, statement := range []string{`UPDATE perfeng_control.report_comparison_inputs SET sha256=sha256`, `DELETE FROM perfeng_control.report_comparison_inputs`, `TRUNCATE perfeng_control.report_comparison_inputs`} {
		if _, err := owner.db.Exec(statement); err == nil {
			t.Fatal("immutable selection changed")
		}
	}
}

func TestReportCaptureRejectsRevokedApprovalBeforeFirstWrite(t *testing.T) {
	dsn := testDatabase(t)
	r := openTest(t, dsn)
	if err := r.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	capture, err := OpenWithReportCapture(testContext, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeTest(t, capture) })
	current, inputs := comparisonInputsFixture(t, capture, "comparison-request-02")
	record := inputs.BaselineSnapshots[0]
	if _, err := r.TransitionBaseline(testContext, "alice", record.ID, record.Version, record.Revision, baseline.Change{State: baseline.StateRetired, Actor: "reviewer", Reason: "revoked before capture"}); err != nil {
		t.Fatal(err)
	}
	if _, err := capture.RetainReportInputs(testContext, "alice", current, inputs); err == nil {
		t.Fatal("stale approval captured")
	}
	if _, err := capture.GetReportInputs(testContext, "alice", current.ID); !errors.Is(err, run.ErrNotFound) {
		t.Fatal("failed capture was not atomic", err)
	}
	raw, err := json.Marshal(comparisonInputsDocument{SchemaVersion: 1, Inputs: inputs})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if _, err := r.db.Exec(`INSERT INTO perfeng_control.report_comparison_inputs VALUES($1,$2,$3,$4,$5)`, "alice", current.ID, inputs.Candidate.ID, raw, hex.EncodeToString(sum[:])); err == nil {
		t.Fatal("direct SQL bypassed approval check")
	}
}

func TestIntegrationAdmissionUsesReportCaptureEnrollment(t *testing.T) {
	dsn := testDatabase(t)
	r := openTest(t, dsn)
	if err := r.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	capture, err := OpenWithReportCapture(testContext, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeTest(t, capture) })
	_, publication := publicationEventFixture(t, capture, "capture-admission", true)
	var enrolled bool
	if err := r.db.QueryRow(`SELECT report_capture_enrolled FROM perfeng_control.runs WHERE run_id=$1`, publication.Data.Native.RunID).Scan(&enrolled); err != nil || !enrolled {
		t.Fatal("integration admission omitted enrollment", err)
	}
	var count int
	if err := r.db.QueryRow(`SELECT count(*) FROM perfeng_control.report_comparison_inputs`).Scan(&count); err != nil || count != 0 {
		t.Fatal("existing publication path fabricated report capture", err)
	}
}

func TestReportInputsCrashChild(t *testing.T) {
	dsn := os.Getenv("PERFENG_REPORT_INPUTS_CHILD")
	if dsn == "" {
		t.Skip("invoked only by the disposable report capture parent")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil || len(config.Fallbacks) != 0 || !(config.Host == "127.0.0.1" || config.Host == "::1" || config.Host == "localhost") || !regexp.MustCompile(`^perfeng_test_[a-f0-9]{16}$`).MatchString(config.Database) {
		t.Fatal("child requires the parent's disposable loopback database")
	}
	var wire struct {
		Current run.Run
		Inputs  runcomparison.Inputs
	}
	if err := json.NewDecoder(io.LimitReader(os.Stdin, 2<<20)).Decode(&wire); err != nil {
		t.Fatal("invalid child input")
	}
	repository, err := Open(testContext, dsn)
	if err != nil {
		t.Fatal("child cannot open disposable database")
	}
	if _, err := repository.RetainReportInputs(testContext, "alice", wire.Current, wire.Inputs); err != nil {
		t.Fatal("child capture failed", err)
	}
	os.Exit(17)
}

func TestReportCaptureCannotBackfillRegisteredOutput(t *testing.T) {
	dsn := testDatabase(t)
	r := openTest(t, dsn)
	if err := r.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	capture, err := OpenWithReportCapture(testContext, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeTest(t, capture) })
	current, inputs := comparisonInputsFixture(t, capture, "comparison-request-03")
	report := inputs.Candidate
	report.ID = "3cfa0000-0000-4000-8000-000000000001"
	report.Format = "analysis-result/v1"
	report.URI = "s3://evidence/report.json"
	if err := r.RegisterArtifact(testContext, "alice", report); err != nil {
		t.Fatal(err)
	}
	if _, err := capture.RetainReportInputs(testContext, "alice", current, inputs); !errors.Is(err, run.ErrConflict) {
		t.Fatal("registered report was backfilled", err)
	}
	payload, err := json.Marshal(comparisonInputsDocument{SchemaVersion: 1, Inputs: inputs})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	if _, err := r.db.Exec(`INSERT INTO perfeng_control.report_comparison_inputs VALUES($1,$2,$3,$4,$5)`, "alice", current.ID, inputs.Candidate.ID, payload, hex.EncodeToString(sum[:])); err == nil {
		t.Fatal("direct SQL backfilled a registered report")
	}
}
