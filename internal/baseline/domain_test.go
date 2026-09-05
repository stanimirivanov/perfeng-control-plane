package baseline

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func TestNewCreatesIsolatedCandidate(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 4, 13, 5, 0, 0, time.UTC)
	input := validCreate()
	record, err := New(input, now)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if record.State != StateCandidate || record.Revision != 1 ||
		record.Qualification.Status != QualificationPending || len(record.Lifecycle) != 1 {
		t.Fatalf("New() = %#v", record)
	}
	if record.Validate() != nil {
		t.Fatal("New() returned an invalid record")
	}

	*input.Dataset.Seed = 99
	if *record.Dataset.Seed == 99 {
		t.Fatal("New() aliases the input dataset seed")
	}
}

func TestBaselineLifecycle(t *testing.T) {
	t.Parallel()

	record := mustNew(t)
	sampleCount := int64(5)
	maximumCV := 0.04
	qualified, err := record.Transition(record.Revision, Change{
		State: StateQualified,
		Qualification: &Qualification{
			Status: QualificationPassed, Reasons: []string{},
			SampleCount: &sampleCount, MaximumCV: &maximumCV,
		},
		Actor: "reviewer", Reason: "Evidence met the qualification policy.",
	}, record.CreatedAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("qualify error = %v", err)
	}

	approved, err := qualified.Transition(qualified.Revision, Change{
		State: StateApproved, Actor: "approver", Reason: "Approved for comparison.",
	}, qualified.CreatedAt.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("approve error = %v", err)
	}
	retired, err := approved.Transition(approved.Revision, Change{
		State: StateRetired, Actor: "approver", Reason: "Superseded by a newer baseline.",
	}, approved.CreatedAt.Add(3*time.Minute))
	if err != nil {
		t.Fatalf("retire error = %v", err)
	}
	if retired.State != StateRetired || retired.Revision != 4 || len(retired.Lifecycle) != 4 ||
		retired.Qualification.Status != QualificationPassed || retired.Validate() != nil {
		t.Fatalf("retired record = %#v", retired)
	}
}

func TestCandidateCanRetireWithFailedQualification(t *testing.T) {
	t.Parallel()

	record := mustNew(t)
	sampleCount := int64(3)
	maximumCV := 0.31
	retired, err := record.Transition(record.Revision, Change{
		State: StateRetired,
		Qualification: &Qualification{
			Status: QualificationFailed, Reasons: []string{"Variance exceeded policy."},
			SampleCount: &sampleCount, MaximumCV: &maximumCV,
		},
		Actor: "reviewer", Reason: "Rejected during qualification.",
	}, record.CreatedAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("Transition() error = %v", err)
	}
	if retired.Qualification.Status != QualificationFailed || retired.State != StateRetired {
		t.Fatalf("Transition() = %#v", retired)
	}
}

func TestTransitionRejectsInvalidRequests(t *testing.T) {
	t.Parallel()

	record := mustNew(t)
	tests := []struct {
		name     string
		revision int64
		change   Change
		now      time.Time
		want     error
	}{
		{
			name: "stale revision", revision: 0,
			change: Change{State: StateRetired, Actor: "reviewer", Reason: "Rejected."},
			now:    record.CreatedAt, want: run.ErrRevision,
		},
		{
			name: "skipped qualification", revision: record.Revision,
			change: Change{State: StateApproved, Actor: "reviewer", Reason: "Approved."},
			now:    record.CreatedAt, want: ErrTransition,
		},
		{
			name: "missing evidence", revision: record.Revision,
			change: Change{State: StateQualified, Actor: "reviewer", Reason: "Qualified."},
			now:    record.CreatedAt, want: run.ErrValidation,
		},
		{
			name: "backward time", revision: record.Revision,
			change: Change{State: StateRetired, Actor: "reviewer", Reason: "Rejected."},
			now:    record.CreatedAt.Add(-time.Second), want: run.ErrValidation,
		},
		{
			name: "unsupported precision", revision: record.Revision,
			change: Change{State: StateRetired, Actor: "reviewer", Reason: "Rejected."},
			now:    record.CreatedAt.Add(time.Nanosecond), want: run.ErrValidation,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := record.Transition(test.revision, test.change, test.now); !errors.Is(err, test.want) {
				t.Fatalf("Transition() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestNewRejectsInvalidIdentityAndTime(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 4, 13, 5, 0, 0, time.UTC)
	tests := []struct {
		name   string
		mutate func(*Create)
		at     time.Time
	}{
		{name: "baseline id", mutate: func(input *Create) { input.ID = "Wrong" }, at: now},
		{name: "source binding", mutate: func(input *Create) { input.Artifact.RunID = "perf-20260904-130000-deadbeef" }, at: now},
		{name: "artifact kind", mutate: func(input *Create) { input.Artifact.Kind = "raw" }, at: now},
		{name: "mutable image", mutate: func(input *Create) { input.Software.Image = "ghcr.io/example/app:latest" }, at: now},
		{name: "environment fingerprint", mutate: func(input *Create) { input.Environment.Fingerprint = "short" }, at: now},
		{name: "dataset seed", mutate: func(input *Create) { value := int64(-1); input.Dataset.Seed = &value }, at: now},
		{name: "actor", mutate: func(input *Create) { input.Actor = " " }, at: now},
		{name: "timestamp precision", mutate: func(*Create) {}, at: now.Add(time.Nanosecond)},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			input := validCreate()
			test.mutate(&input)
			if _, err := New(input, test.at); !errors.Is(err, run.ErrValidation) {
				t.Fatalf("New() error = %v", err)
			}
		})
	}
}

func TestQualificationValidation(t *testing.T) {
	t.Parallel()

	sampleCount := int64(5)
	maximumCV := 0.04
	tests := []Qualification{
		{Status: QualificationPending, Reasons: []string{}, SampleCount: &sampleCount},
		{Status: QualificationPassed, Reasons: []string{}, SampleCount: &sampleCount},
		{Status: QualificationFailed, Reasons: []string{}},
		{Status: QualificationFailed, Reasons: []string{"same", "same"}},
		{Status: "UNKNOWN", Reasons: []string{}, SampleCount: &sampleCount, MaximumCV: &maximumCV},
	}
	for _, qualification := range tests {
		if !errors.Is(qualification.Validate(), run.ErrValidation) {
			t.Fatalf("Validate() accepted %#v", qualification)
		}
	}
}

func TestCloneDoesNotShareMutableEvidence(t *testing.T) {
	t.Parallel()

	record := mustNew(t)
	clone := record.Clone()
	*clone.Dataset.Seed = 41
	clone.Qualification.Reasons = append(clone.Qualification.Reasons, "changed")
	clone.Lifecycle[0].Actor = "changed"

	if *record.Dataset.Seed == 41 || len(record.Qualification.Reasons) != 0 ||
		record.Lifecycle[0].Actor == "changed" {
		t.Fatal("Clone() shares mutable data with its source")
	}
}

func TestMatchesRequiresApprovalAndExactCompatibility(t *testing.T) {
	t.Parallel()

	record := mustNew(t)
	selection := selectionFor(record)
	if record.MatchesSelection(selection) {
		t.Fatal("candidate matched an approved-baseline selection")
	}

	sampleCount := int64(5)
	maximumCV := 0.04
	qualified, err := record.Transition(record.Revision, Change{
		State: StateQualified,
		Qualification: &Qualification{
			Status: QualificationPassed, Reasons: []string{},
			SampleCount: &sampleCount, MaximumCV: &maximumCV,
		},
		Actor: "reviewer", Reason: "Evidence met the qualification policy.",
	}, record.CreatedAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	approved, err := qualified.Transition(qualified.Revision, Change{
		State: StateApproved, Actor: "approver", Reason: "Approved for comparison.",
	}, qualified.CreatedAt.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !approved.MatchesSelection(selection) {
		t.Fatal("approved record did not match its exact compatibility dimensions")
	}

	tests := []struct {
		name   string
		mutate func(*Selection)
	}{
		{name: "baseline id", mutate: func(value *Selection) { value.ID = "other-baseline" }},
		{name: "version", mutate: func(value *Selection) { value.Version = "2.0.1" }},
		{name: "test", mutate: func(value *Selection) { value.TestID = "other-test" }},
		{name: "workload", mutate: func(value *Selection) { value.Workload.Version = "1.0.1" }},
		{name: "environment", mutate: func(value *Selection) { value.Environment.Fingerprint = strings.Repeat("a", 64) }},
		{name: "dataset", mutate: func(value *Selection) { seed := int64(8); value.Dataset.Seed = &seed }},
		{name: "invalid", mutate: func(value *Selection) { value.Version = "latest" }},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			changed := selection
			test.mutate(&changed)
			if approved.MatchesSelection(changed) {
				t.Fatal("approved record matched changed selection")
			}
		})
	}
}

func mustNew(t *testing.T) Record {
	t.Helper()
	record, err := New(validCreate(), time.Date(2026, 9, 4, 13, 5, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	return record
}

func validCreate() Create {
	seed := int64(7)

	return Create{
		ID: "approved-search-browser", Version: "2.0.0", TestID: "search-browser",
		SourceRunID: "perf-20260904-130000-a1b2c3d6",
		Artifact: run.Artifact{
			ID:    "77777777-7777-4777-8777-777777777777",
			RunID: "perf-20260904-130000-a1b2c3d6", Kind: "normalized",
			URI:       "s3://perfeng-example/runs/perf-20260904-130000-a1b2c3d6/normalized/k6.json",
			SHA256:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			SizeBytes: 1510, MediaType: "application/json", Format: "normalized-result/v1",
		},
		Software: Software{
			GitSHA:  "1111111111111111111111111111111111111111",
			Image:   "ghcr.io/example/app@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			Version: "2026.9.4",
		},
		Workload: rawresult.Identity{
			ID: "search-browser-smoke", Version: "1.0.0",
			SHA256: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		},
		Environment: Environment{
			Identity: rawresult.Identity{
				ID: "local-kind", Version: "1.0.0",
				SHA256: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
			},
			Fingerprint: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
		},
		Dataset: Dataset{
			Kind: "versioned", ID: "catalogue-v2", Version: "2.0.0",
			SHA256: "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
			Seed:   &seed,
		},
		Actor: "perfeng-control-plane", Reason: "Created from selected normalized evidence.",
	}
}

func selectionFor(record Record) Selection {
	return Selection{
		ID: record.ID, Version: record.Version, TestID: record.TestID,
		Workload: record.Workload, Environment: record.Environment, Dataset: cloneDataset(record.Dataset),
	}
}
