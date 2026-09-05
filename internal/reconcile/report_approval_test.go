package reconcile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/analysisresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

type reportTrustResolverFunc func(
	context.Context, string, run.Run, ReportingInput,
) (ReportTrust, error)

func (resolve reportTrustResolverFunc) ResolveReportTrust(
	ctx context.Context,
	principal string,
	current run.Run,
	input ReportingInput,
) (ReportTrust, error) {
	return resolve(ctx, principal, current, input)
}

type reportVerdictApproverFunc func(context.Context, []byte, analysisresult.Manifest) error

func (approve reportVerdictApproverFunc) ApproveReportVerdicts(
	ctx context.Context,
	policy []byte,
	manifest analysisresult.Manifest,
) error {
	return approve(ctx, policy, manifest)
}

type baselineApprovalStore struct {
	resolve func(context.Context, string, baseline.Selection) (baseline.Record, bool, error)
}

func (store *baselineApprovalStore) CreateBaseline(
	context.Context,
	string,
	baseline.Create,
) (baseline.Record, error) {
	return baseline.Record{}, errors.New("unexpected baseline creation")
}

func (store *baselineApprovalStore) GetBaseline(
	context.Context,
	string,
	string,
	string,
) (baseline.Record, error) {
	return baseline.Record{}, errors.New("unexpected baseline read")
}

func (store *baselineApprovalStore) ResolveApprovedBaseline(
	ctx context.Context,
	principal string,
	selection baseline.Selection,
) (baseline.Record, bool, error) {
	return store.resolve(ctx, principal, selection)
}

func (store *baselineApprovalStore) TransitionBaseline(
	context.Context,
	string,
	string,
	string,
	int64,
	baseline.Change,
) (baseline.Record, error) {
	return baseline.Record{}, errors.New("unexpected baseline transition")
}

type reportApprovalFixture struct {
	current   run.Run
	input     ReportingInput
	manifest  analysisresult.Manifest
	trust     ReportTrust
	selection baseline.Selection
	record    baseline.Record
}

func newReportApprovalFixture(t *testing.T) reportApprovalFixture {
	t.Helper()
	current, input, _, manifest := reportCollectorFixture()
	current.Request.TestSuite = manifest.TestID
	policy := []byte(`{"kind":"approved-policy"}`)
	digest := sha256.Sum256(policy)
	current.Request.Policy = run.Reference{
		ID: "checkout-policy", Version: "1.0.0", SHA256: hex.EncodeToString(digest[:]),
	}
	manifest.Policy = analysisresult.Policy{
		ID: current.Request.Policy.ID, Version: current.Request.Policy.Version,
		SHA256: current.Request.Policy.SHA256, Mode: "inform",
	}

	reference := normalizedArtifact("perf-20260902-120000-87654321")
	reference.ID = "20000000-0000-4000-8000-000000000005"
	reference.URI = "s3://perfeng-artifacts/runs/perf-20260902-120000-87654321/normalized-result.json"
	reference.SHA256 = strings.Repeat("e", 64)
	manifest.ReferenceArtifacts = []run.Artifact{reference}

	createdAt := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	record, err := baseline.New(baseline.Create{
		ID: "checkout-known-good", Version: "1.0.0", TestID: manifest.TestID,
		SourceRunID: reference.RunID, Artifact: reference,
		Software: baseline.Software{
			GitSHA: strings.Repeat("1", 40),
			Image:  "ghcr.io/example/checkout@sha256:" + strings.Repeat("a", 64),
		},
		Workload: rawresult.Identity{
			ID: "checkout-smoke", Version: "1.0.0", SHA256: strings.Repeat("b", 64),
		},
		Environment: baseline.Environment{
			Identity: rawresult.Identity{
				ID: "local-kind", Version: "1.0.0", SHA256: strings.Repeat("c", 64),
			},
			Fingerprint: strings.Repeat("d", 64),
		},
		Dataset: baseline.Dataset{Kind: "none"},
		Actor:   "reviewer", Reason: "Candidate created from completed evidence.",
	}, createdAt)
	if err != nil {
		t.Fatal(err)
	}
	samples, maximumCV := int64(30), 0.05
	record, err = record.Transition(1, baseline.Change{
		State: baseline.StateQualified,
		Qualification: &baseline.Qualification{
			Status: baseline.QualificationPassed, Reasons: []string{},
			SampleCount: &samples, MaximumCV: &maximumCV,
		},
		Actor: "reviewer", Reason: "Evidence passed qualification.",
	}, createdAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	record, err = record.Transition(2, baseline.Change{
		State: baseline.StateApproved, Actor: "approver", Reason: "Approved reference.",
	}, createdAt.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	selection := baseline.Selection{
		ID: record.ID, Version: record.Version, TestID: record.TestID,
		Workload: record.Workload, Environment: record.Environment, Dataset: record.Dataset,
	}
	trust := ReportTrust{
		PolicyBytes: policy, PolicyMode: manifest.Policy.Mode,
		Producer: manifest.Producer, Baselines: []baseline.Selection{selection},
	}

	return reportApprovalFixture{
		current: current, input: input, manifest: manifest,
		trust: trust, selection: selection, record: record,
	}
}

func TestTrustedReportApproverAuthorizesExactApprovedReferences(t *testing.T) {
	fixture := newReportApprovalFixture(t)
	resolved := 0
	store := &baselineApprovalStore{resolve: func(
		ctx context.Context,
		principal string,
		selection baseline.Selection,
	) (baseline.Record, bool, error) {
		resolved++
		if ctx.Err() != nil || principal != "principal-a" ||
			!reflect.DeepEqual(selection, fixture.selection) {
			t.Fatal("baseline resolver received changed trust context")
		}

		return fixture.record, true, nil
	}}
	verdictChecked := false
	approver, err := NewTrustedReportApprover(
		store,
		reportTrustResolverFunc(func(
			ctx context.Context,
			principal string,
			current run.Run,
			input ReportingInput,
		) (ReportTrust, error) {
			if ctx.Err() != nil || principal != "principal-a" ||
				!reflect.DeepEqual(current, fixture.current) || input != fixture.input {
				t.Fatal("trust resolver received changed report context")
			}

			return fixture.trust, nil
		}),
		reportVerdictApproverFunc(func(
			ctx context.Context,
			policy []byte,
			manifest analysisresult.Manifest,
		) error {
			verdictChecked = true
			if ctx.Err() != nil || string(policy) != string(fixture.trust.PolicyBytes) ||
				!reflect.DeepEqual(manifest, fixture.manifest) {
				t.Fatal("verdict approver received changed approved evidence")
			}

			return nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := approver.ApproveReportManifest(
		context.Background(), "principal-a", fixture.current, fixture.input, fixture.manifest,
	); err != nil {
		t.Fatal(err)
	}
	if resolved != 1 || !verdictChecked {
		t.Fatal("approval boundaries were not completed")
	}
}

func TestVerifiedReportCollectorUsesTrustedReportApproval(t *testing.T) {
	fixture := newReportApprovalFixture(t)
	output := reportArtifact(fixture.current.ID)
	approver := newTrustedReportApprover(
		t,
		fixture.trust,
		func(context.Context, string, baseline.Selection) (baseline.Record, bool, error) {
			return fixture.record, true, nil
		},
	)
	collector, err := NewVerifiedReportCollector(
		fixedReportResolver(output),
		readArtifactFunc(func(context.Context, run.Artifact) ([]byte, error) {
			return encodeReportManifest(t, fixture.manifest), nil
		}),
		approver,
		reportContractsVersion,
	)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := collector.CollectReportArtifact(
		context.Background(), "principal-a", fixture.current, fixture.input,
	)
	if err != nil || actual != output {
		t.Fatalf("CollectReportArtifact() = %#v, %v", actual, err)
	}
}

func TestTrustedReportApproverAllowsExplicitMissingBaseline(t *testing.T) {
	fixture := newReportApprovalFixture(t)
	fixture.manifest.ReferenceArtifacts = []run.Artifact{}
	approver := newTrustedReportApprover(
		t,
		fixture.trust,
		func(context.Context, string, baseline.Selection) (baseline.Record, bool, error) {
			return baseline.Record{}, false, nil
		},
	)
	if err := approver.ApproveReportManifest(
		context.Background(), "principal-a", fixture.current, fixture.input, fixture.manifest,
	); err != nil {
		t.Fatal(err)
	}
}

func TestTrustedReportApproverRejectsUntrustedClaims(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*reportApprovalFixture)
	}{
		{
			name: "changed policy bytes",
			mutate: func(fixture *reportApprovalFixture) {
				fixture.trust.PolicyBytes = []byte(`{"kind":"different"}`)
			},
		},
		{
			name: "unapproved producer",
			mutate: func(fixture *reportApprovalFixture) {
				fixture.trust.Producer.Image = "ghcr.io/example/report@sha256:" + strings.Repeat("f", 64)
			},
		},
		{
			name: "unexpected reference",
			mutate: func(fixture *reportApprovalFixture) {
				fixture.manifest.ReferenceArtifacts = []run.Artifact{}
			},
		},
		{
			name: "invalid selection",
			mutate: func(fixture *reportApprovalFixture) {
				fixture.trust.Baselines[0].Environment.Fingerprint = "changed"
			},
		},
		{
			name: "duplicate selection",
			mutate: func(fixture *reportApprovalFixture) {
				fixture.trust.Baselines = append(fixture.trust.Baselines, fixture.selection)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newReportApprovalFixture(t)
			test.mutate(&fixture)
			approver := newTrustedReportApprover(
				t,
				fixture.trust,
				func(context.Context, string, baseline.Selection) (baseline.Record, bool, error) {
					return fixture.record, true, nil
				},
			)
			if err := approver.ApproveReportManifest(
				context.Background(), "principal-a", fixture.current, fixture.input, fixture.manifest,
			); !errors.Is(err, run.ErrValidation) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestTrustedReportApproverPreservesBoundaryErrors(t *testing.T) {
	fixture := newReportApprovalFixture(t)
	for _, boundary := range []string{"trust", "baseline", "verdict"} {
		t.Run(boundary, func(t *testing.T) {
			want := run.ErrUnavailable
			trust := fixture.trust
			resolveErr, baselineErr, verdictErr := error(nil), error(nil), error(nil)
			switch boundary {
			case "trust":
				resolveErr = want
			case "baseline":
				baselineErr = want
			case "verdict":
				verdictErr = want
			}
			store := &baselineApprovalStore{resolve: func(
				context.Context, string, baseline.Selection,
			) (baseline.Record, bool, error) {
				return fixture.record, true, baselineErr
			}}
			approver, err := NewTrustedReportApprover(
				store,
				reportTrustResolverFunc(func(
					context.Context, string, run.Run, ReportingInput,
				) (ReportTrust, error) {
					return trust, resolveErr
				}),
				reportVerdictApproverFunc(func(
					context.Context, []byte, analysisresult.Manifest,
				) error {
					return verdictErr
				}),
			)
			if err != nil {
				t.Fatal(err)
			}
			if err := approver.ApproveReportManifest(
				context.Background(), "principal-a", fixture.current, fixture.input, fixture.manifest,
			); !errors.Is(err, want) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestTrustedReportApproverValidatesDependenciesAndContext(t *testing.T) {
	fixture := newReportApprovalFixture(t)
	valid := newTrustedReportApprover(
		t,
		fixture.trust,
		func(context.Context, string, baseline.Selection) (baseline.Record, bool, error) {
			return fixture.record, true, nil
		},
	)
	for name, dependencies := range map[string][]any{
		"baselines": {nil, valid.resolver, valid.verdicts},
		"resolver":  {valid.baselines, nil, valid.verdicts},
		"verdicts":  {valid.baselines, valid.resolver, nil},
	} {
		t.Run(name, func(t *testing.T) {
			store, _ := dependencies[0].(baseline.Repository)
			resolver, _ := dependencies[1].(ReportTrustResolver)
			verdicts, _ := dependencies[2].(ReportVerdictApprover)
			if _, err := NewTrustedReportApprover(store, resolver, verdicts); !errors.Is(err, run.ErrValidation) {
				t.Fatalf("error = %v", err)
			}
		})
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := valid.ApproveReportManifest(
		canceled, "principal-a", fixture.current, fixture.input, fixture.manifest,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled error = %v", err)
	}
	if err := valid.ApproveReportManifest(
		context.Background(), "", fixture.current, fixture.input, fixture.manifest,
	); !errors.Is(err, run.ErrValidation) {
		t.Fatalf("invalid context error = %v", err)
	}
}

func newTrustedReportApprover(
	t *testing.T,
	trust ReportTrust,
	resolve func(context.Context, string, baseline.Selection) (baseline.Record, bool, error),
) *TrustedReportApprover {
	t.Helper()
	approver, err := NewTrustedReportApprover(
		&baselineApprovalStore{resolve: resolve},
		reportTrustResolverFunc(func(
			context.Context, string, run.Run, ReportingInput,
		) (ReportTrust, error) {
			return trust, nil
		}),
		reportVerdictApproverFunc(func(
			context.Context, []byte, analysisresult.Manifest,
		) error {
			return nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}

	return approver
}
