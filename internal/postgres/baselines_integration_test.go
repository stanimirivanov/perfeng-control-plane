package postgres

import (
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func TestBaselinePersistence(t *testing.T) {
	dsn := testDatabase(t)
	first := openTest(t, dsn)
	if err := first.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	second := openTest(t, dsn)

	source, artifact := completedBaselineSource(t, first, "alice", "request-key-baseline")
	input := baselineInput(source, artifact)
	created, err := first.CreateBaseline(testContext, "alice", input)
	if err != nil {
		t.Fatal(err)
	}
	if created.State != baseline.StateCandidate || created.Revision != 1 || created.Validate() != nil {
		t.Fatal("invalid stored baseline candidate")
	}
	got, err := second.GetBaseline(testContext, "alice", created.ID, created.Version)
	if err != nil || !reflect.DeepEqual(got, created) {
		t.Fatal("baseline not durable across connections", err)
	}
	if _, err := second.GetBaseline(testContext, "bob", created.ID, created.Version); !errors.Is(err, run.ErrNotFound) {
		t.Fatal("cross-principal baseline read", err)
	}
	if _, err := second.CreateBaseline(testContext, "alice", input); !errors.Is(err, run.ErrConflict) {
		t.Fatal("duplicate baseline version", err)
	}

	notCompleted := accepted(t, first, "alice", "request-key-unfinished")
	unverified := artifact
	unverified.ID = "77777777-7777-4777-8777-777777777778"
	unverified.RunID = notCompleted.Run.ID
	unverified.URI = "s3://perfeng-artifacts/runs/" + notCompleted.Run.ID + "/normalized/k6.json"
	if err := first.RegisterArtifact(testContext, "alice", unverified); err != nil {
		t.Fatal(err)
	}
	unfinishedInput := baselineInput(notCompleted.Run, unverified)
	unfinishedInput.Version = "2.0.1"
	if _, err := first.CreateBaseline(testContext, "alice", unfinishedInput); !errors.Is(err, run.ErrValidation) {
		t.Fatal("baseline accepted an unfinished source Run", err)
	}
	if _, err := first.CreateBaseline(testContext, "bob", input); !errors.Is(err, run.ErrNotFound) {
		t.Fatal("baseline accepted cross-principal evidence", err)
	}
	mismatched := input
	mismatched.Version = "2.0.2"
	mismatched.Artifact.SHA256 = strings.Repeat("d", 64)
	if _, err := first.CreateBaseline(testContext, "alice", mismatched); !errors.Is(err, run.ErrValidation) {
		t.Fatal("baseline accepted an unregistered artifact claim", err)
	}

	sampleCount := int64(8)
	maximumCV := 0.03
	qualified, err := first.TransitionBaseline(
		testContext, "alice", created.ID, created.Version, created.Revision,
		baseline.Change{
			State: baseline.StateQualified,
			Qualification: &baseline.Qualification{
				Status: baseline.QualificationPassed, Reasons: []string{},
				SampleCount: &sampleCount, MaximumCV: &maximumCV,
			},
			Actor: "reviewer", Reason: "Evidence met the reviewed policy.",
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	approveBaselineConcurrently(t, first, second, qualified)
	approved, err := first.GetBaseline(testContext, "alice", created.ID, created.Version)
	if err != nil || approved.State != baseline.StateApproved || approved.Revision != 3 ||
		len(approved.Lifecycle) != 3 {
		t.Fatal("approved baseline snapshot is inconsistent", err)
	}

	if _, err := first.db.Exec(`
		UPDATE perfeng_control.baselines SET revision=4
		WHERE principal='alice' AND baseline_id=$1 AND version=$2`,
		created.ID, created.Version); err == nil {
		t.Fatal("database accepted relational/snapshot revision drift")
	}
}

func TestApprovedBaselineResolution(t *testing.T) {
	dsn := testDatabase(t)
	first := openTest(t, dsn)
	if err := first.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	second := openTest(t, dsn)

	source, artifact := completedBaselineSource(t, first, "alice", "request-key-resolution")
	created, err := first.CreateBaseline(testContext, "alice", baselineInput(source, artifact))
	if err != nil {
		t.Fatal(err)
	}
	selection := baselineSelection(created)
	if _, found, err := second.ResolveApprovedBaseline(testContext, "alice", selection); err != nil || found {
		t.Fatal("candidate resolved as approved", err)
	}

	sampleCount := int64(8)
	maximumCV := 0.03
	qualified, err := first.TransitionBaseline(
		testContext, "alice", created.ID, created.Version, created.Revision,
		baseline.Change{
			State: baseline.StateQualified,
			Qualification: &baseline.Qualification{
				Status: baseline.QualificationPassed, Reasons: []string{},
				SampleCount: &sampleCount, MaximumCV: &maximumCV,
			},
			Actor: "reviewer", Reason: "Evidence met the reviewed policy.",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	approved, err := first.TransitionBaseline(
		testContext, "alice", qualified.ID, qualified.Version, qualified.Revision,
		baseline.Change{
			State: baseline.StateApproved, Actor: "approver", Reason: "Approved as an anchor.",
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	resolved, found, err := second.ResolveApprovedBaseline(testContext, "alice", selection)
	if err != nil || !found || !reflect.DeepEqual(resolved, approved) {
		t.Fatal("approved baseline did not resolve", err)
	}
	if _, found, err = second.ResolveApprovedBaseline(testContext, "bob", selection); err != nil || found {
		t.Fatal("cross-principal baseline resolved", err)
	}
	mismatched := selection
	mismatched.Environment.Fingerprint = strings.Repeat("d", 64)
	if _, found, err = second.ResolveApprovedBaseline(testContext, "alice", mismatched); err != nil || found {
		t.Fatal("incompatible baseline resolved", err)
	}
	missing := selection
	missing.Version = "2.0.1"
	if _, found, err = second.ResolveApprovedBaseline(testContext, "alice", missing); err != nil || found {
		t.Fatal("missing baseline resolved", err)
	}
	invalid := selection
	invalid.Version = "latest"
	if _, _, err = second.ResolveApprovedBaseline(testContext, "alice", invalid); !errors.Is(err, run.ErrValidation) {
		t.Fatal("invalid selection was not rejected", err)
	}

	retired, err := first.TransitionBaseline(
		testContext, "alice", approved.ID, approved.Version, approved.Revision,
		baseline.Change{
			State: baseline.StateRetired, Actor: "approver", Reason: "Anchor retired.",
		},
	)
	if err != nil || retired.State != baseline.StateRetired {
		t.Fatal("could not retire approved baseline", err)
	}
	if _, found, err = second.ResolveApprovedBaseline(testContext, "alice", selection); err != nil || found {
		t.Fatal("retired baseline resolved", err)
	}
}

func approveBaselineConcurrently(
	t *testing.T,
	first, second *Repository,
	qualified baseline.Record,
) {
	t.Helper()
	change := baseline.Change{
		State: baseline.StateApproved, Actor: "approver", Reason: "Approved as an anchor.",
	}
	errorsSeen := make(chan error, 2)
	var wait sync.WaitGroup
	for _, repository := range []*Repository{first, second} {
		repository := repository
		wait.Go(func() {
			_, err := repository.TransitionBaseline(
				testContext, "alice", qualified.ID, qualified.Version, qualified.Revision, change,
			)
			errorsSeen <- err
		})
	}
	wait.Wait()
	close(errorsSeen)
	var approvals, stale int
	for err := range errorsSeen {
		switch {
		case err == nil:
			approvals++
		case errors.Is(err, run.ErrRevision):
			stale++
		default:
			t.Fatal("unexpected concurrent approval result", err)
		}
	}
	if approvals != 1 || stale != 1 {
		t.Fatal("concurrent approval was not serialized", approvals, stale)
	}
}

func completedBaselineSource(
	t *testing.T,
	repository *Repository,
	principal, key string,
) (run.Run, run.Artifact) {
	t.Helper()
	accepted := accepted(t, repository, principal, key)
	current := accepted.Run
	for _, state := range []run.State{
		run.StateValidating,
		run.StateProvisioning,
		run.StateRunning,
		run.StateCollecting,
		run.StateAnalyzing,
		run.StateReporting,
		run.StateCompleted,
	} {
		var err error
		current, err = repository.Advance(
			testContext, principal, current.ID, current.Revision, run.Change{State: state},
		)
		if err != nil {
			t.Fatal(err)
		}
	}
	artifact := run.Artifact{
		ID: "77777777-7777-4777-8777-777777777777", RunID: current.ID,
		Kind: "normalized", URI: "s3://perfeng-artifacts/runs/" + current.ID + "/normalized/k6.json",
		SHA256: strings.Repeat("a", 64), SizeBytes: 1510,
		MediaType: "application/json", Format: "normalized-result/v1",
	}
	if err := repository.RegisterArtifact(testContext, principal, artifact); err != nil {
		t.Fatal(err)
	}

	return current, artifact
}

func baselineInput(source run.Run, artifact run.Artifact) baseline.Create {
	return baseline.Create{
		ID: "approved-search-api", Version: "2.0.0", TestID: "search-api",
		SourceRunID: source.ID, Artifact: artifact,
		Software: baseline.Software{
			GitSHA: source.Request.Candidate.GitSHA,
			Image:  source.Request.Candidate.Image,
		},
		Workload: rawresult.Identity{
			ID: "search-api-steady", Version: "1.0.0", SHA256: strings.Repeat("b", 64),
		},
		Environment: baseline.Environment{
			Identity: rawresult.Identity{
				ID: source.Request.Environment.ID, Version: source.Request.Environment.Version,
				SHA256: source.Request.Environment.SHA256,
			},
			Fingerprint: strings.Repeat("c", 64),
		},
		Dataset: baseline.Dataset{Kind: "none"},
		Actor:   "performance-team", Reason: "Selected completed normalized evidence.",
	}
}

func baselineSelection(record baseline.Record) baseline.Selection {
	return baseline.Selection{
		ID: record.ID, Version: record.Version, TestID: record.TestID,
		Workload: record.Workload, Environment: record.Environment, Dataset: record.Dataset,
	}
}
