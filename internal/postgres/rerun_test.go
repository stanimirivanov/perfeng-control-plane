package postgres

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"reflect"
	"regexp"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/runrerun"
	"github.com/stanimirivanov/perfeng-control-plane/internal/serviceaccess"
)

func rerunFixture(t *testing.T) (*Repository, string, runrerun.Intent) {
	t.Helper()
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
	current, inputs := comparisonInputsFixture(t, capture, "rerun-source-0001")
	if _, err = r.RetainReportInputs(testContext, "alice", current, inputs); err != nil {
		t.Fatal(err)
	}
	report := inputs.Candidate
	report.ID = "3cfa0000-0000-4000-8000-000000000001"
	report.Format = "analysis-result/v1"
	report.URI = "s3://evidence/report.json"
	if err = r.RegisterArtifact(testContext, "alice", report); err != nil {
		t.Fatal(err)
	}
	bindReviewedRun(t, r, current.ID)
	if _, err = r.db.Exec(`INSERT INTO perfeng_control.service_administrators VALUES ('urn:services','search',session_user)`); err != nil {
		t.Fatal(err)
	}
	for _, actor := range []string{"maya", "lina"} {
		for _, permission := range []string{"run:read", "run:rerun"} {
			if err = serviceGrant(t, r.db, "search", actor, permission, true); err != nil {
				t.Fatal(err)
			}
		}
	}
	return r, dsn, runrerun.Intent{SourceRunID: current.ID, Key: "rerun-intent-0001", ReportID: report.ID, ReportSHA256: report.SHA256, Reason: "Investigate CI regression", Actor: serviceaccess.Identity{Issuer: "urn:people", ID: "maya"}, ActorKind: "HUMAN", Caller: serviceaccess.Identity{Issuer: "urn:clients", ID: "console"}}
}

func TestRerunAtomicReplayConcurrencyAndReopen(t *testing.T) {
	r, dsn, intent := rerunFixture(t)
	const clients = 8
	var wg sync.WaitGroup
	out := make(chan runrerun.Acceptance, clients)
	failures := make(chan error, clients)
	for range clients {
		wg.Go(func() {
			value, err := r.AcceptRerun(testContext, intent)
			if err != nil {
				failures <- err
				return
			}
			out <- value
		})
	}
	wg.Wait()
	close(out)
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	var first runrerun.Acceptance
	for value := range out {
		if first.ID == "" {
			first = value
		}
		if !reflect.DeepEqual(first, value) {
			t.Fatal("duplicate changed accepted evidence")
		}
	}
	if first.Child.ID == intent.SourceRunID || first.Intent.Actor != intent.Actor || first.Source.CreatorKind != "CI" || first.Child.Request != first.Inputs.Request {
		t.Fatal("lineage/attribution lost")
	}
	reopened := openTest(t, dsn)
	got, err := reopened.GetRerun(testContext, intent.Actor, intent.SourceRunID, intent.Key)
	if err != nil || !reflect.DeepEqual(got, first) {
		t.Fatal("reopen changed receipt", err)
	}
	changed := intent
	changed.Reason = "Different intent"
	if _, err = r.AcceptRerun(testContext, changed); !errors.Is(err, run.ErrConflict) {
		t.Fatal("changed body replayed", err)
	}
	changed = intent
	changed.Caller.ID = "renewed-console"
	got, err = r.AcceptRerun(testContext, changed)
	if err != nil || !reflect.DeepEqual(got, first) {
		t.Fatal("caller rotation changed actor intent", err)
	}
	anotherSource := accepted(t, r, "alice", "rerun-other-source1").Run
	bindReviewedRun(t, r, anotherSource.ID)
	changed = intent
	changed.SourceRunID = anotherSource.ID
	if _, err = r.AcceptRerun(testContext, changed); !errors.Is(err, run.ErrConflict) {
		t.Fatal("same key rebound to another source", err)
	}
	distinct := intent
	distinct.Key = "rerun-distinct-key02"
	distinctResult, err := r.AcceptRerun(testContext, distinct)
	if err != nil || distinctResult.Child.ID == first.Child.ID {
		t.Fatal("distinct intent keys collided", err)
	}
	changed = intent
	changed.Actor.ID = "lina"
	other, err := r.AcceptRerun(testContext, changed)
	if err != nil || other.Child.ID == first.Child.ID {
		t.Fatal("actors collided", err)
	}
	if err = serviceGrant(t, r.db, "search", "maya", "run:rerun", false); err != nil {
		t.Fatal(err)
	}
	if _, err = r.AcceptRerun(testContext, intent); !errors.Is(err, run.ErrNotFound) {
		t.Fatal("revoked mutation accepted", err)
	}
	if _, err = r.GetRerun(testContext, intent.Actor, intent.SourceRunID, intent.Key); err != nil {
		t.Fatal("read recovery needs rerun right", err)
	}
	if err = serviceGrant(t, r.db, "search", "maya", "run:read", false); err != nil {
		t.Fatal(err)
	}
	if _, err = r.GetRerun(testContext, intent.Actor, intent.SourceRunID, intent.Key); !errors.Is(err, run.ErrNotFound) {
		t.Fatal("revoked read disclosed receipt", err)
	}
}

func TestRerunReceiptFailureRollsBackChildAndOwnership(t *testing.T) {
	r, _, intent := rerunFixture(t)
	var before int
	if err := r.db.QueryRow(`SELECT count(*) FROM perfeng_control.runs`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.Exec(`ALTER TABLE perfeng_control.rerun_intents ADD CONSTRAINT injected_failure CHECK (false) NOT VALID`); err != nil {
		t.Fatal(err)
	}
	if partial, err := r.AcceptRerun(testContext, intent); err == nil || partial.ID != "" {
		t.Fatal("injected receipt failure exposed partial acceptance", err)
	}
	var after, receipts int
	if err := r.db.QueryRow(`SELECT count(*) FROM perfeng_control.runs`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if err := r.db.QueryRow(`SELECT count(*) FROM perfeng_control.rerun_intents`).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if after != before || receipts != 0 {
		t.Fatal("orphan child or receipt committed")
	}
	if _, err := r.db.Exec(`ALTER TABLE perfeng_control.rerun_intents DROP CONSTRAINT injected_failure`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.AcceptRerun(testContext, intent); err != nil {
		t.Fatal("rolled back key stayed reserved", err)
	}
}

func TestRerunImmutableScopeAndSourcePin(t *testing.T) {
	r, _, intent := rerunFixture(t)
	for _, change := range []func(*runrerun.Intent){func(v *runrerun.Intent) {
		v.ReportSHA256 = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	}, func(v *runrerun.Intent) { v.Actor.ID = "outsider" }} {
		bad := intent
		change(&bad)
		if _, err := r.AcceptRerun(testContext, bad); err == nil {
			t.Fatal("unbound intent accepted")
		}
	}
	accepted, err := r.AcceptRerun(testContext, intent)
	if err != nil {
		t.Fatal(err)
	}
	foreign := intent.Actor
	foreign.ID = "lina"
	if _, err = r.GetRerun(testContext, foreign, intent.SourceRunID, intent.Key); !errors.Is(err, run.ErrNotFound) {
		t.Fatal("foreign actor learned receipt", err)
	}
	for _, statement := range []string{`UPDATE perfeng_control.rerun_intents SET expires_at=clock_timestamp()`, `DELETE FROM perfeng_control.rerun_intents`, `TRUNCATE perfeng_control.rerun_intents`} {
		if _, err = r.db.Exec(statement); err == nil {
			t.Fatal("immutable acceptance changed", statement)
		}
	}
	var binding serviceaccess.Binding
	binding, err = r.GetServiceRunBinding(testContext, intent.Actor, accepted.Child.ID)
	if err != nil || binding.Creator != intent.Actor || binding.Service != accepted.Source.Service {
		t.Fatal("child service lineage missing", err)
	}
}

func TestRerunCrashChild(t *testing.T) {
	dsn := os.Getenv("PERFENG_RERUN_CHILD")
	if dsn == "" {
		t.Skip("invoked only by disposable rerun parent")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil || len(config.Fallbacks) != 0 || config.Host != "127.0.0.1" || !regexp.MustCompile(`^perfeng_test_[a-f0-9]{16}$`).MatchString(config.Database) {
		t.Fatal("unsafe child database")
	}
	var intent runrerun.Intent
	if err = json.NewDecoder(io.LimitReader(os.Stdin, 65536)).Decode(&intent); err != nil {
		t.Fatal("invalid child input")
	}
	r, err := Open(testContext, dsn)
	if err != nil {
		t.Fatal("cannot open child database")
	}
	if _, err = r.AcceptRerun(testContext, intent); err != nil {
		t.Fatal(err)
	}
	os.Exit(17)
}

func TestRerunRecoversAfterProcessExitWithoutResponse(t *testing.T) {
	r, dsn, intent := rerunFixture(t)
	wire, err := json.Marshal(intent)
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestRerunCrashChild$")
	child.Env = append(os.Environ(), "PERFENG_RERUN_CHILD="+dsn)
	child.Stdin = bytes.NewReader(wire)
	output, err := child.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 17 {
		t.Fatal("child did not terminate after acceptance", string(output), err)
	}
	got, err := r.GetRerun(testContext, intent.Actor, intent.SourceRunID, intent.Key)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := r.AcceptRerun(testContext, intent)
	if err != nil || !reflect.DeepEqual(got, replayed) {
		t.Fatal("uncertain acceptance duplicated", err)
	}
}

func TestRerunRetirementBlocksNewIntentButPreservesReplay(t *testing.T) {
	r, _, intent := rerunFixture(t)
	first, err := r.AcceptRerun(testContext, intent)
	if err != nil {
		t.Fatal(err)
	}
	record := first.Inputs.BaselineSnapshots[0]
	if _, err = r.TransitionBaseline(testContext, "alice", record.ID, record.Version, record.Revision, baseline.Change{State: baseline.StateRetired, Actor: "approver", Reason: "withdraw approval"}); err != nil {
		t.Fatal(err)
	}
	replay, err := r.AcceptRerun(testContext, intent)
	if err != nil || !reflect.DeepEqual(first, replay) {
		t.Fatal("retirement rewrote accepted intent", err)
	}
	intent.Key = "rerun-new-intent-02"
	if _, err = r.AcceptRerun(testContext, intent); !errors.Is(err, run.ErrValidation) {
		t.Fatal("retired baseline admitted new intent", err)
	}
}

func TestRerunUpgradeDoesNotInferHistory(t *testing.T) {
	r := openTest(t, testDatabase(t))
	migrateBeforeVerification(t, r, "0027_")
	original := accepted(t, r, "legacy-owner", "rerun-upgrade-old1").Run
	if err := r.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	if err := r.Migrate(testContext); err != nil {
		t.Fatal("migration replay", err)
	}
	var count int
	if err := r.db.QueryRow(`SELECT count(*) FROM perfeng_control.rerun_intents`).Scan(&count); err != nil || count != 0 {
		t.Fatal("history was inferred", err)
	}
	retained, err := r.Get(testContext, "legacy-owner", original.ID)
	if err != nil || !reflect.DeepEqual(retained, original) {
		t.Fatal("upgrade changed legacy acceptance", err)
	}
}

func TestRerunRejectsIneligibleReportMetadata(t *testing.T) {
	for _, mode := range []string{"raw", "empty", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			r, _, intent := rerunFixture(t)
			report, err := r.GetArtifact(testContext, "alice", intent.SourceRunID, intent.ReportID)
			if err != nil {
				t.Fatal(err)
			}
			report.ID = "4cfa0000-0000-4000-8000-000000000001"
			switch mode {
			case "raw":
				report.Kind = "raw"
			case "empty":
				report.SizeBytes = 0
			case "oversized":
				report.SizeBytes = 1048577
			}
			if err = r.RegisterArtifact(testContext, "alice", report); err != nil {
				t.Fatal(err)
			}
			intent.ReportID = report.ID
			if _, err = r.AcceptRerun(testContext, intent); !errors.Is(err, run.ErrValidation) {
				t.Fatal("ineligible report admitted", err)
			}
		})
	}
}

func TestRerunRejectsMalformedRetainedAcceptance(t *testing.T) {
	r, _, intent := rerunFixture(t)
	child := accepted(t, r, "alice", "rerun-malformed-child").Run
	bindReviewedRun(t, r, child.ID)
	payload := []byte(`{"schemaVersion":1,"acceptance":{"Intent":{"SourceRunID":"` + intent.SourceRunID + `"}}}`)
	_, err := r.db.Exec(`INSERT INTO perfeng_control.rerun_intents(actor_issuer,actor_id,idempotency_key,source_owner,source_run_id,report_id,child_owner,child_run_id,receipt_id,accepted_at,expires_at,receipt,sha256) VALUES('urn:people','maya',$1,'alice',$2,$3,'alice',$4,'5cfa0000-0000-4000-8000-000000000001',clock_timestamp(),clock_timestamp()+interval '24 hours',$5,encode(sha256($5),'hex'))`, intent.Key, intent.SourceRunID, intent.ReportID, child.ID, payload)
	if err != nil {
		t.Fatal(err)
	}
	if value, err := r.GetRerun(testContext, intent.Actor, intent.SourceRunID, intent.Key); !errors.Is(err, run.ErrUnavailable) || value.ID != "" {
		t.Fatal("malformed record masqueraded as acceptance", err)
	}
}
