package postgres

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
)

func rerunTemplate() *batchv1.Job {
	backoff, one, deadline, automount := int32(0), int32(1), int64(120), false
	return &batchv1.Job{Spec: batchv1.JobSpec{BackoffLimit: &backoff, Completions: &one, Parallelism: &one, ActiveDeadlineSeconds: &deadline, Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever, AutomountServiceAccountToken: &automount, Containers: []corev1.Container{{Name: "runner", Image: "ghcr.io/example/k6@sha256:" + strings.Repeat("a", 64), Args: []string{"run", "scenario.js"}}}}}}}
}
func TestRerunTemplateSealsAcrossConcurrentCaptureAndReopen(t *testing.T) {
	r, dsn, intent := rerunFixture(t)
	accepted, err := r.AcceptRerun(testContext, intent)
	if err != nil {
		t.Fatal(err)
	}
	current, err := r.Advance(testContext, accepted.ChildPrincipal, accepted.Child.ID, 1, run.Change{State: run.StateValidating})
	if err != nil {
		t.Fatal(err)
	}
	original := rerunTemplate()
	const clients = 6
	var wait sync.WaitGroup
	fail := make(chan error, clients)
	for range clients {
		wait.Go(func() {
			value, err := r.RetainRerunTemplate(testContext, accepted.ChildPrincipal, current, original)
			if err == nil && !reflect.DeepEqual(value, original) {
				err = errors.New("capture changed template")
			}
			fail <- err
		})
	}
	wait.Wait()
	close(fail)
	for err := range fail {
		if err != nil {
			t.Fatal(err)
		}
	}
	original.Spec.Template.Spec.Containers[0].Args[1] = "caller-mutated.js"
	reopened := openTest(t, dsn)
	stored, err := reopened.GetRerunTemplate(testContext, accepted.ChildPrincipal, current)
	if err != nil || !reflect.DeepEqual(stored, rerunTemplate()) {
		t.Fatal("reopen/caller mutation changed seal", err)
	}
	if _, err = r.RetainRerunTemplate(testContext, accepted.ChildPrincipal, current, original); !errors.Is(err, run.ErrConflict) {
		t.Fatal("changed template replaced seal", err)
	}
	current, err = r.Advance(testContext, accepted.ChildPrincipal, current.ID, current.Revision, run.Change{State: run.StateProvisioning})
	if err != nil {
		t.Fatal(err)
	}
	replay, err := r.RetainRerunTemplate(testContext, accepted.ChildPrincipal, current, rerunTemplate())
	if err != nil || !reflect.DeepEqual(replay, stored) {
		t.Fatal("replay depends on mutable state", err)
	}
	wire, _ := json.Marshal(stored)
	again, _ := json.Marshal(replay)
	if !bytes.Equal(wire, again) {
		t.Fatal("retry bytes changed")
	}
	for _, sql := range []string{`UPDATE perfeng_control.rerun_templates SET sha256=repeat('a',64)`, `DELETE FROM perfeng_control.rerun_templates`, `TRUNCATE perfeng_control.rerun_templates`} {
		if _, err = r.db.Exec(sql); err == nil {
			t.Fatal("sealed evidence mutable", sql)
		}
	}
}
func TestRerunTemplateRejectsLateCaptureAndForeignScope(t *testing.T) {
	r, _, intent := rerunFixture(t)
	accepted, err := r.AcceptRerun(testContext, intent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.RetainRerunTemplate(testContext, accepted.ChildPrincipal, accepted.Child, rerunTemplate()); !errors.Is(err, run.ErrConflict) {
		t.Fatal("capture before validation", err)
	}
	current, err := r.Advance(testContext, accepted.ChildPrincipal, accepted.Child.ID, 1, run.Change{State: run.StateValidating})
	if err != nil {
		t.Fatal(err)
	}
	if value, err := r.GetRerunTemplate(testContext, "foreign", current); !errors.Is(err, run.ErrNotFound) || value != nil {
		t.Fatal("foreign scope disclosed template", err)
	}
	current, err = r.Advance(testContext, accepted.ChildPrincipal, current.ID, current.Revision, run.Change{State: run.StateProvisioning})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.RetainRerunTemplate(testContext, accepted.ChildPrincipal, current, rerunTemplate()); !errors.Is(err, run.ErrConflict) {
		t.Fatal("late capture fabricated history", err)
	}
}
func TestRerunTemplateRejectsInvalidAndOversizedJobs(t *testing.T) {
	r, _, intent := rerunFixture(t)
	accepted, err := r.AcceptRerun(testContext, intent)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"named", "unpinned", "oversized"} {
		job := rerunTemplate()
		switch mode {
		case "named":
			job.Name = "already-created"
		case "unpinned":
			job.Spec.Template.Spec.Containers[0].Image = "untrusted:latest"
		case "oversized":
			job.Spec.Template.Spec.Containers[0].Args = []string{strings.Repeat("x", 1048577)}
		}
		if value, err := r.RetainRerunTemplate(testContext, accepted.ChildPrincipal, accepted.Child, job); !errors.Is(err, run.ErrValidation) || value != nil {
			t.Fatal("invalid job admitted", mode, err)
		}
	}
}
func TestRerunTemplateUpgradeDoesNotBackfill(t *testing.T) {
	r := openTest(t, testDatabase(t))
	migrateBeforeVerification(t, r, "0028_")
	legacy := accepted(t, r, "alice", "template-upgrade01").Run
	if err := r.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := r.db.QueryRow(`SELECT count(*) FROM perfeng_control.rerun_templates`).Scan(&count); err != nil || count != 0 {
		t.Fatal("backfill inferred", err)
	}
	if value, err := r.GetRerunTemplate(testContext, "alice", legacy); !errors.Is(err, run.ErrNotFound) || value != nil {
		t.Fatal("ordinary Run acquired template", err)
	}
}

func TestRerunTemplateCannotBackfillBoundExecution(t *testing.T) {
	r, _, intent := rerunFixture(t)
	accepted, err := r.AcceptRerun(testContext, intent)
	if err != nil {
		t.Fatal(err)
	}
	current, err := r.Advance(testContext, accepted.ChildPrincipal, accepted.Child.ID, 1, run.Change{State: run.StateValidating})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.db.Exec(`INSERT INTO perfeng_control.kubernetes_executions VALUES($1,'perf-runs',$1,'job-uid',repeat('a',64))`, current.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = r.RetainRerunTemplate(testContext, accepted.ChildPrincipal, current, rerunTemplate()); !errors.Is(err, run.ErrConflict) {
		t.Fatal("bound execution backfilled", err)
	}
	payload, err := json.Marshal(rerunTemplateDocument{SchemaVersion: 1, Template: rerunTemplate()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.db.Exec(`INSERT INTO perfeng_control.rerun_templates VALUES($1,$2,$3,encode(sha256($3),'hex'))`, accepted.ChildPrincipal, current.ID, payload); err == nil {
		t.Fatal("SQL guard allowed bound execution backfill")
	}
}

func TestRerunTemplateCodecRejectsCorruptionAndUnsupportedVersion(t *testing.T) {
	valid, err := json.Marshal(rerunTemplateDocument{SchemaVersion: 1, Template: rerunTemplate()})
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(valid)
	if _, err = decodeRerunTemplate(valid, strings.Repeat("f", 64)); !errors.Is(err, run.ErrUnavailable) {
		t.Fatal("digest tampering accepted", err)
	}
	for _, raw := range [][]byte{bytes.Replace(valid, []byte(`"schemaVersion":1`), []byte(`"schemaVersion":2`), 1), []byte(`{"schemaVersion":1,"schemaVersion":1,"template":null}`)} {
		bad := sha256.Sum256(raw)
		if value, err := decodeRerunTemplate(raw, hex.EncodeToString(bad[:])); !errors.Is(err, run.ErrUnavailable) || value != nil {
			t.Fatal("malformed sealed document trusted", err)
		}
	}
	if value, err := decodeRerunTemplate(valid, hex.EncodeToString(hash[:])); err != nil || !reflect.DeepEqual(value, rerunTemplate()) {
		t.Fatal("valid document changed", err)
	}
}
