package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/analysisresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/objectstore"
	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

const reportContractsVersion = "0.6.0"

type resolveReportManifestFunc func(
	context.Context, string, run.Run, ReportingInput,
) (run.Artifact, error)

func (resolve resolveReportManifestFunc) ResolveReportManifest(
	ctx context.Context,
	principal string,
	current run.Run,
	input ReportingInput,
) (run.Artifact, error) {
	return resolve(ctx, principal, current, input)
}

type approveReportManifestFunc func(
	context.Context, string, run.Run, ReportingInput, analysisresult.Manifest,
) error

func (approve approveReportManifestFunc) ApproveReportManifest(
	ctx context.Context,
	principal string,
	current run.Run,
	input ReportingInput,
	manifest analysisresult.Manifest,
) error {
	return approve(ctx, principal, current, input, manifest)
}

func TestVerifiedReportCollectorReturnsApprovedVerifiedReference(t *testing.T) {
	t.Parallel()

	current, input, reference, manifest := reportCollectorFixture()
	var read run.Artifact
	resolver := resolveReportManifestFunc(func(
		ctx context.Context,
		principal string,
		resolvedRun run.Run,
		resolvedInput ReportingInput,
	) (run.Artifact, error) {
		if ctx.Err() != nil || principal != "principal-a" ||
			resolvedRun != current || resolvedInput != input {
			t.Fatal("resolver received changed report context")
		}

		return reference, nil
	})
	reader := readArtifactFunc(func(ctx context.Context, artifact run.Artifact) ([]byte, error) {
		if ctx.Err() != nil {
			t.Fatal(ctx.Err())
		}
		read = artifact

		return encodeReportManifest(t, manifest), nil
	})
	approver := approveReportManifestFunc(func(
		ctx context.Context,
		principal string,
		approvedRun run.Run,
		approvedInput ReportingInput,
		approved analysisresult.Manifest,
	) error {
		if ctx.Err() != nil || principal != "principal-a" || approvedRun != current ||
			approvedInput != input || !reflect.DeepEqual(approved, manifest) {
			t.Fatal("approver received changed report context")
		}

		return nil
	})

	collector := newVerifiedReportCollector(t, resolver, reader, approver)
	actual, err := collector.CollectReportArtifact(
		context.Background(),
		"principal-a",
		current,
		input,
	)
	if err != nil {
		t.Fatal(err)
	}
	if actual != reference || read != reference {
		t.Fatalf("artifact = %#v, read = %#v", actual, read)
	}
}

func TestVerifiedReportCollectorClassifiesEvidenceFailures(t *testing.T) {
	t.Parallel()

	current, input, reference, manifest := reportCollectorFixture()
	tests := []struct {
		name       string
		resolveErr error
		readErr    error
		content    []byte
		want       error
	}{
		{name: "publication pending", resolveErr: ErrReportPending, want: ErrReportPending},
		{name: "output not visible", readErr: objectstore.ErrObjectNotFound, want: ErrReportPending},
		{name: "output bytes changed", readErr: objectstore.ErrObjectMismatch, want: ErrReportFailed},
		{name: "output read unavailable", readErr: run.ErrUnavailable, want: run.ErrUnavailable},
		{
			name: "operational error takes precedence",
			resolveErr: errors.Join(
				run.ErrUnavailable,
				ErrReportFailed,
			),
			want: run.ErrUnavailable,
		},
		{name: "invalid output JSON", content: []byte(`{}`), want: ErrReportFailed},
		{
			name: "ambiguous evidence",
			readErr: errors.Join(
				objectstore.ErrObjectNotFound,
				objectstore.ErrObjectMismatch,
			),
			want: run.ErrValidation,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			content := test.content
			if content == nil {
				content = encodeReportManifest(t, manifest)
			}
			collector := newVerifiedReportCollector(
				t,
				resolveReportManifestFunc(func(
					context.Context, string, run.Run, ReportingInput,
				) (run.Artifact, error) {
					return reference, test.resolveErr
				}),
				readArtifactFunc(func(context.Context, run.Artifact) ([]byte, error) {
					return content, test.readErr
				}),
				acceptingReportApprover(),
			)
			if _, err := collector.CollectReportArtifact(
				context.Background(), "principal-a", current, input,
			); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestVerifiedReportCollectorRejectsInvalidReferenceBeforeRead(t *testing.T) {
	t.Parallel()

	current, input, valid, _ := reportCollectorFixture()
	tests := map[string]run.Artifact{
		"wrong run": withArtifact(valid, func(a *run.Artifact) {
			a.RunID = "perf-20260903-120000-deadbeef"
		}),
		"raw kind": withArtifact(valid, func(a *run.Artifact) { a.Kind = "raw" }),
		"media type": withArtifact(valid, func(a *run.Artifact) {
			a.MediaType = "text/plain"
		}),
		"format": withArtifact(valid, func(a *run.Artifact) {
			a.Format = "normalized-result/v1"
		}),
		"candidate ID": withArtifact(valid, func(a *run.Artifact) {
			a.ID = input.Candidate.ID
		}),
		"candidate URI": withArtifact(valid, func(a *run.Artifact) {
			a.URI = input.Candidate.URI
		}),
	}

	for name, reference := range tests {
		reference := reference
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			readCalled := false
			collector := newVerifiedReportCollector(
				t,
				fixedReportResolver(reference),
				readArtifactFunc(func(context.Context, run.Artifact) ([]byte, error) {
					readCalled = true
					return nil, nil
				}),
				acceptingReportApprover(),
			)
			if _, err := collector.CollectReportArtifact(
				context.Background(), "principal-a", current, input,
			); !errors.Is(err, ErrReportFailed) {
				t.Fatalf("error = %v", err)
			}
			if readCalled {
				t.Fatal("invalid report reference was read")
			}
		})
	}
}

func TestVerifiedReportCollectorBindsExactCandidate(t *testing.T) {
	t.Parallel()

	current, input, reference, manifest := reportCollectorFixture()
	manifest.CandidateArtifact.ID = "55555555-5555-4555-8555-555555555555"
	manifest.CandidateArtifact.URI = "s3://perfeng-artifacts/runs/" + current.ID +
		"/normalized/other.json"
	collector := newVerifiedReportCollector(
		t,
		fixedReportResolver(reference),
		readArtifactFunc(func(context.Context, run.Artifact) ([]byte, error) {
			return encodeReportManifest(t, manifest), nil
		}),
		acceptingReportApprover(),
	)
	if _, err := collector.CollectReportArtifact(
		context.Background(), "principal-a", current, input,
	); !errors.Is(err, ErrReportFailed) {
		t.Fatalf("candidate mismatch error = %v", err)
	}
}

func TestVerifiedReportCollectorClassifiesApprovalFailures(t *testing.T) {
	t.Parallel()

	current, input, reference, manifest := reportCollectorFixture()
	tests := []struct {
		name string
		err  error
		want error
	}{
		{name: "unapproved policy", err: run.ErrForbidden, want: ErrReportFailed},
		{name: "invalid verdict", err: run.ErrValidation, want: ErrReportFailed},
		{name: "approval unavailable", err: run.ErrUnavailable, want: run.ErrUnavailable},
		{
			name: "operational approval takes precedence",
			err:  errors.Join(run.ErrUnavailable, run.ErrForbidden),
			want: run.ErrUnavailable,
		},
		{name: "canceled", err: context.Canceled, want: context.Canceled},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			collector := newVerifiedReportCollector(
				t,
				fixedReportResolver(reference),
				readArtifactFunc(func(context.Context, run.Artifact) ([]byte, error) {
					return encodeReportManifest(t, manifest), nil
				}),
				approveReportManifestFunc(func(
					context.Context,
					string,
					run.Run,
					ReportingInput,
					analysisresult.Manifest,
				) error {
					return test.err
				}),
			)
			if _, err := collector.CollectReportArtifact(
				context.Background(), "principal-a", current, input,
			); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestVerifiedReportCollectorValidatesDependenciesAndContext(t *testing.T) {
	t.Parallel()

	current, input, reference, _ := reportCollectorFixture()
	resolver := fixedReportResolver(reference)
	reader := readArtifactFunc(func(context.Context, run.Artifact) ([]byte, error) {
		return nil, nil
	})
	approver := acceptingReportApprover()
	for name, build := range map[string]func() error{
		"resolver": func() error {
			_, err := NewVerifiedReportCollector(nil, reader, approver, reportContractsVersion)
			return err
		},
		"reader": func() error {
			_, err := NewVerifiedReportCollector(resolver, nil, approver, reportContractsVersion)
			return err
		},
		"approver": func() error {
			_, err := NewVerifiedReportCollector(resolver, reader, nil, reportContractsVersion)
			return err
		},
		"version": func() error {
			_, err := NewVerifiedReportCollector(resolver, reader, approver, "latest")
			return err
		},
	} {
		if err := build(); !errors.Is(err, run.ErrValidation) {
			t.Fatalf("%s error = %v", name, err)
		}
	}

	collector := newVerifiedReportCollector(t, resolver, reader, approver)
	for _, test := range []struct {
		name      string
		principal string
		current   run.Run
		input     ReportingInput
	}{
		{name: "empty principal", current: current, input: input},
		{name: "invalid run", principal: "principal-a", current: run.Run{ID: "invalid"}, input: input},
		{name: "invalid input", principal: "principal-a", current: current},
	} {
		if _, err := collector.CollectReportArtifact(
			context.Background(), test.principal, test.current, test.input,
		); !errors.Is(err, run.ErrValidation) {
			t.Fatalf("%s error = %v", test.name, err)
		}
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := collector.CollectReportArtifact(
		canceled, "principal-a", current, input,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled error = %v", err)
	}
}

func reportCollectorFixture() (
	run.Run,
	ReportingInput,
	run.Artifact,
	analysisresult.Manifest,
) {
	current := boundClaim(run.StateReporting).Run
	input := ReportingInput{Candidate: normalizedArtifact(current.ID)}
	reference := reportArtifact(current.ID)
	manifest := analysisresult.Manifest{
		SchemaVersion: 1, Kind: "AnalysisResult", ContractsVersion: reportContractsVersion,
		RunID: current.ID, TestID: "checkout-api", CreatedAt: "2026-09-04T12:01:00Z",
		Producer: rawresult.Producer{
			Name: "perfeng-analysis", Version: "1.0.0",
			Image: "ghcr.io/stanimirivanov/perfeng-analysis@sha256:" + strings.Repeat("b", 64),
		},
		Policy: analysisresult.Policy{
			ID: "checkout-policy", Version: "1.0.0",
			SHA256: strings.Repeat("c", 64), Mode: "inform",
		},
		CandidateArtifact:  input.Candidate,
		ReferenceArtifacts: []run.Artifact{},
		Evaluations: []analysisresult.Evaluation{{
			RuleID: "checkout-latency",
			Metric: analysisresult.Metric{
				Name: "api.http.duration", Statistic: "p95", Unit: "ms",
			},
			Quality: analysisresult.Quality{
				Status: "PASS", Reasons: []string{}, Samples: reportInt64Pointer(20),
			},
			SLO: analysisresult.SLO{
				Status: "PASS", Reasons: []string{}, Value: reportFloatPointer(180),
			},
			Regression: analysisresult.Regression{
				Status: "INCONCLUSIVE", Reasons: []string{"No approved baseline is available."},
				CandidateValue: reportFloatPointer(180),
			},
		}},
	}

	return current, input, reference, manifest
}

func encodeReportManifest(t testing.TB, manifest analysisresult.Manifest) []byte {
	t.Helper()
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}

	return data
}

func newVerifiedReportCollector(
	t testing.TB,
	resolver ReportManifestResolver,
	reader ArtifactByteReader,
	approver ReportManifestApprover,
) *VerifiedReportCollector {
	t.Helper()
	collector, err := NewVerifiedReportCollector(
		resolver,
		reader,
		approver,
		reportContractsVersion,
	)
	if err != nil {
		t.Fatal(err)
	}

	return collector
}

func fixedReportResolver(reference run.Artifact) ReportManifestResolver {
	return resolveReportManifestFunc(func(
		context.Context, string, run.Run, ReportingInput,
	) (run.Artifact, error) {
		return reference, nil
	})
}

func acceptingReportApprover() ReportManifestApprover {
	return approveReportManifestFunc(func(
		context.Context,
		string,
		run.Run,
		ReportingInput,
		analysisresult.Manifest,
	) error {
		return nil
	})
}

func reportInt64Pointer(value int64) *int64     { return &value }
func reportFloatPointer(value float64) *float64 { return &value }
