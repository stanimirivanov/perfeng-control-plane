package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/normalizedresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/objectstore"
	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

const normalizedContractsVersion = "0.5.0"

type resolveNormalizedManifestFunc func(
	context.Context, string, run.Run, AnalysisInput,
) (run.Artifact, error)

func (resolve resolveNormalizedManifestFunc) ResolveNormalizedManifest(
	ctx context.Context, principal string, current run.Run, input AnalysisInput,
) (run.Artifact, error) {
	return resolve(ctx, principal, current, input)
}

type approveNormalizedManifestFunc func(
	context.Context, string, run.Run, AnalysisInput, normalizedresult.Manifest,
) error

func (approve approveNormalizedManifestFunc) ApproveNormalizedManifest(
	ctx context.Context,
	principal string,
	current run.Run,
	input AnalysisInput,
	manifest normalizedresult.Manifest,
) error {
	return approve(ctx, principal, current, input, manifest)
}

func TestVerifiedNormalizedCollectorReturnsApprovedVerifiedReference(t *testing.T) {
	t.Parallel()

	current, input, reference, manifest := normalizedCollectorFixture()
	var read run.Artifact
	resolver := resolveNormalizedManifestFunc(func(
		ctx context.Context, principal string, resolvedRun run.Run, resolvedInput AnalysisInput,
	) (run.Artifact, error) {
		if ctx.Err() != nil || principal != "worker-a" ||
			!reflect.DeepEqual(resolvedRun, current) || !reflect.DeepEqual(resolvedInput, input) {
			t.Fatal("resolver received changed analysis context")
		}
		resolvedInput.Sources[0].URI = "s3://mutated/elsewhere"

		return reference, nil
	})
	reader := readArtifactFunc(func(ctx context.Context, artifact run.Artifact) ([]byte, error) {
		if ctx.Err() != nil {
			t.Fatal(ctx.Err())
		}
		read = artifact

		return encodeNormalizedManifest(t, manifest), nil
	})
	approver := approveNormalizedManifestFunc(func(
		ctx context.Context,
		principal string,
		approvedRun run.Run,
		approvedInput AnalysisInput,
		approved normalizedresult.Manifest,
	) error {
		if ctx.Err() != nil || principal != "worker-a" ||
			!reflect.DeepEqual(approvedRun, current) || !reflect.DeepEqual(approvedInput, input) ||
			!reflect.DeepEqual(approved, manifest) {
			t.Fatal("approver received changed normalized context")
		}

		return nil
	})

	collector := newVerifiedNormalizedCollector(t, resolver, reader, approver)
	actual, err := collector.CollectNormalizedArtifact(
		context.Background(), "worker-a", current, input,
	)
	if err != nil {
		t.Fatal(err)
	}
	if actual != reference || read != reference {
		t.Fatalf("artifact = %#v, read = %#v", actual, read)
	}
}

func TestVerifiedNormalizedCollectorClassifiesEvidenceFailures(t *testing.T) {
	t.Parallel()

	current, input, reference, manifest := normalizedCollectorFixture()
	tests := []struct {
		name       string
		resolveErr error
		readErr    error
		content    []byte
		want       error
	}{
		{name: "publication pending", resolveErr: ErrAnalysisPending, want: ErrAnalysisPending},
		{name: "output not visible", readErr: objectstore.ErrObjectNotFound, want: ErrAnalysisPending},
		{name: "output bytes changed", readErr: objectstore.ErrObjectMismatch, want: ErrAnalysisFailed},
		{name: "output read unavailable", readErr: run.ErrUnavailable, want: run.ErrUnavailable},
		{name: "invalid output JSON", content: []byte(`{}`), want: ErrAnalysisFailed},
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
				content = encodeNormalizedManifest(t, manifest)
			}
			collector := newVerifiedNormalizedCollector(
				t,
				resolveNormalizedManifestFunc(func(
					context.Context, string, run.Run, AnalysisInput,
				) (run.Artifact, error) {
					return reference, test.resolveErr
				}),
				readArtifactFunc(func(context.Context, run.Artifact) ([]byte, error) {
					return content, test.readErr
				}),
				acceptingNormalizedApprover(),
			)
			if _, err := collector.CollectNormalizedArtifact(
				context.Background(), "worker-a", current, input,
			); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestVerifiedNormalizedCollectorRejectsInvalidReferenceBeforeRead(t *testing.T) {
	t.Parallel()

	current, input, valid, _ := normalizedCollectorFixture()
	tests := map[string]run.Artifact{
		"wrong run":  withArtifact(valid, func(a *run.Artifact) { a.RunID = "perf-20260903-120000-deadbeef" }),
		"raw kind":   withArtifact(valid, func(a *run.Artifact) { a.Kind = "raw" }),
		"media type": withArtifact(valid, func(a *run.Artifact) { a.MediaType = "text/plain" }),
		"format":     withArtifact(valid, func(a *run.Artifact) { a.Format = "result/v2" }),
		"collision":  withArtifact(valid, func(a *run.Artifact) { a.ID = input.Manifest.ID }),
	}

	for name, reference := range tests {
		reference := reference
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			readCalled := false
			collector := newVerifiedNormalizedCollector(
				t,
				fixedNormalizedResolver(reference),
				readArtifactFunc(func(context.Context, run.Artifact) ([]byte, error) {
					readCalled = true
					return nil, nil
				}),
				acceptingNormalizedApprover(),
			)
			if _, err := collector.CollectNormalizedArtifact(
				context.Background(), "worker-a", current, input,
			); !errors.Is(err, ErrAnalysisFailed) {
				t.Fatalf("error = %v", err)
			}
			if readCalled {
				t.Fatal("invalid output reference was read")
			}
		})
	}
}

func TestVerifiedNormalizedCollectorBindsExactSourceSet(t *testing.T) {
	t.Parallel()

	current, input, reference, manifest := normalizedCollectorFixture()
	tests := map[string]func(*normalizedresult.Manifest){
		"missing source": func(m *normalizedresult.Manifest) { m.SourceArtifacts = m.SourceArtifacts[1:] },
		"changed source": func(m *normalizedresult.Manifest) {
			m.SourceArtifacts[0].SHA256 = strings.Repeat("f", 64)
		},
		"reordered sources": func(m *normalizedresult.Manifest) {
			m.SourceArtifacts[0], m.SourceArtifacts[1] = m.SourceArtifacts[1], m.SourceArtifacts[0]
		},
	}

	for name, mutate := range tests {
		mutate := mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			changed := manifest
			changed.SourceArtifacts = append([]run.Artifact(nil), manifest.SourceArtifacts...)
			mutate(&changed)
			collector := newVerifiedNormalizedCollector(
				t,
				fixedNormalizedResolver(reference),
				readArtifactFunc(func(context.Context, run.Artifact) ([]byte, error) {
					return encodeNormalizedManifest(t, changed), nil
				}),
				acceptingNormalizedApprover(),
			)
			_, err := collector.CollectNormalizedArtifact(
				context.Background(), "worker-a", current, input,
			)
			if name == "reordered sources" && err != nil {
				t.Fatal(err)
			}
			if name != "reordered sources" && !errors.Is(err, ErrAnalysisFailed) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestVerifiedNormalizedCollectorClassifiesApprovalFailures(t *testing.T) {
	t.Parallel()

	current, input, reference, manifest := normalizedCollectorFixture()
	tests := []struct {
		name string
		err  error
		want error
	}{
		{name: "unapproved provenance", err: run.ErrForbidden, want: ErrAnalysisFailed},
		{name: "invalid approval", err: run.ErrValidation, want: ErrAnalysisFailed},
		{name: "approval unavailable", err: run.ErrUnavailable, want: run.ErrUnavailable},
		{name: "canceled", err: context.Canceled, want: context.Canceled},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			collector := newVerifiedNormalizedCollector(
				t,
				fixedNormalizedResolver(reference),
				readArtifactFunc(func(context.Context, run.Artifact) ([]byte, error) {
					return encodeNormalizedManifest(t, manifest), nil
				}),
				approveNormalizedManifestFunc(func(
					context.Context, string, run.Run, AnalysisInput, normalizedresult.Manifest,
				) error {
					return test.err
				}),
			)
			if _, err := collector.CollectNormalizedArtifact(
				context.Background(), "worker-a", current, input,
			); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestVerifiedNormalizedCollectorValidatesDependenciesAndContext(t *testing.T) {
	t.Parallel()

	current, input, reference, _ := normalizedCollectorFixture()
	resolver := fixedNormalizedResolver(reference)
	reader := readArtifactFunc(func(context.Context, run.Artifact) ([]byte, error) { return nil, nil })
	approver := acceptingNormalizedApprover()
	for name, build := range map[string]func() error{
		"resolver": func() error {
			_, err := NewVerifiedNormalizedCollector(nil, reader, approver, normalizedContractsVersion)
			return err
		},
		"reader": func() error {
			_, err := NewVerifiedNormalizedCollector(resolver, nil, approver, normalizedContractsVersion)
			return err
		},
		"approver": func() error {
			_, err := NewVerifiedNormalizedCollector(resolver, reader, nil, normalizedContractsVersion)
			return err
		},
		"version": func() error {
			_, err := NewVerifiedNormalizedCollector(resolver, reader, approver, "latest")
			return err
		},
	} {
		if err := build(); !errors.Is(err, run.ErrValidation) {
			t.Fatalf("%s error = %v", name, err)
		}
	}

	collector := newVerifiedNormalizedCollector(t, resolver, reader, approver)
	for _, test := range []struct {
		name      string
		principal string
		current   run.Run
		input     AnalysisInput
	}{
		{name: "empty principal", current: current, input: input},
		{name: "invalid run", principal: "worker-a", current: run.Run{ID: "invalid"}, input: input},
		{name: "invalid input", principal: "worker-a", current: current, input: AnalysisInput{}},
	} {
		if _, err := collector.CollectNormalizedArtifact(
			context.Background(), test.principal, test.current, test.input,
		); !errors.Is(err, run.ErrValidation) {
			t.Fatalf("%s error = %v", test.name, err)
		}
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := collector.CollectNormalizedArtifact(
		canceled, "worker-a", current, input,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled error = %v", err)
	}
}

func normalizedCollectorFixture() (
	run.Run, AnalysisInput, run.Artifact, normalizedresult.Manifest,
) {
	current := boundClaim(run.StateAnalyzing).Run
	sources := rawArtifacts(current.ID)
	input := AnalysisInput{Manifest: rawArtifactSet(current.ID).Manifest, Sources: sources}
	reference := normalizedArtifact(current.ID)
	manifest := normalizedresult.Manifest{
		SchemaVersion:    1,
		Kind:             "NormalizedResult",
		ContractsVersion: normalizedContractsVersion,
		RunID:            current.ID,
		TestID:           "checkout-api",
		Workload: rawresult.Identity{
			ID: "checkout-smoke", Version: "1.0.0", SHA256: strings.Repeat("a", 64),
		},
		Producer: rawresult.Producer{
			Name: "perfeng-analysis", Version: "1.0.0",
			Image: "ghcr.io/stanimirivanov/perfeng-analysis@sha256:" + strings.Repeat("b", 64),
		},
		MeasurementWindow: rawresult.Window{
			Start: "2026-09-03T12:00:00Z", End: "2026-09-03T12:01:00Z",
		},
		CreatedAt:       "2026-09-03T12:01:01Z",
		SourceArtifacts: append([]run.Artifact(nil), sources...),
		Results: []normalizedresult.Result{{
			SchemaVersion: 2,
			RunID:         current.ID,
			Metric: normalizedresult.Metric{
				Name: "http.request.duration", Direction: "lower-is-better",
			},
			Distribution: normalizedresult.Distribution{},
		}},
	}

	return current, input, reference, manifest
}

func encodeNormalizedManifest(t testing.TB, manifest normalizedresult.Manifest) []byte {
	t.Helper()
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}

	return data
}

func newVerifiedNormalizedCollector(
	t testing.TB,
	resolver NormalizedManifestResolver,
	reader ArtifactByteReader,
	approver NormalizedManifestApprover,
) *VerifiedNormalizedCollector {
	t.Helper()
	collector, err := NewVerifiedNormalizedCollector(
		resolver, reader, approver, normalizedContractsVersion,
	)
	if err != nil {
		t.Fatal(err)
	}

	return collector
}

func fixedNormalizedResolver(reference run.Artifact) NormalizedManifestResolver {
	return resolveNormalizedManifestFunc(func(
		context.Context, string, run.Run, AnalysisInput,
	) (run.Artifact, error) {
		return reference, nil
	})
}

func acceptingNormalizedApprover() NormalizedManifestApprover {
	return approveNormalizedManifestFunc(func(
		context.Context, string, run.Run, AnalysisInput, normalizedresult.Manifest,
	) error {
		return nil
	})
}
