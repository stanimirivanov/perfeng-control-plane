package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/objectstore"
	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

const rawContractsVersion = "0.4.0"

type resolveRawManifestFunc func(context.Context, string, run.Run) (run.Artifact, error)

func (resolve resolveRawManifestFunc) ResolveRawManifest(
	ctx context.Context,
	principal string,
	current run.Run,
) (run.Artifact, error) {
	return resolve(ctx, principal, current)
}

type readArtifactFunc func(context.Context, run.Artifact) ([]byte, error)

func (read readArtifactFunc) Read(ctx context.Context, artifact run.Artifact) ([]byte, error) {
	return read(ctx, artifact)
}

type approveRawManifestFunc func(context.Context, string, run.Run, rawresult.Manifest) error

func (approve approveRawManifestFunc) ApproveRawManifest(
	ctx context.Context,
	principal string,
	current run.Run,
	manifest rawresult.Manifest,
) error {
	return approve(ctx, principal, current, manifest)
}

func TestVerifiedRawCollectorReturnsOnlyApprovedVerifiedEvidence(t *testing.T) {
	t.Parallel()

	current := boundClaim(run.StateCollecting).Run
	manifest := collectorManifest(current.ID)
	manifestBytes := encodeRawManifest(t, manifest)
	manifestReference := collectorManifestReference(current.ID)
	var reads []run.Artifact

	resolver := resolveRawManifestFunc(func(
		ctx context.Context,
		principal string,
		resolvedRun run.Run,
	) (run.Artifact, error) {
		if ctx.Err() != nil || principal != "worker-a" || !reflect.DeepEqual(resolvedRun, current) {
			t.Fatal("resolver received changed collection context")
		}

		return manifestReference, nil
	})
	reader := readArtifactFunc(func(ctx context.Context, artifact run.Artifact) ([]byte, error) {
		if ctx.Err() != nil {
			t.Fatal(ctx.Err())
		}
		reads = append(reads, artifact)
		if artifact == manifestReference {
			return manifestBytes, nil
		}

		return []byte("verified"), nil
	})
	approver := approveRawManifestFunc(func(
		ctx context.Context,
		principal string,
		approvedRun run.Run,
		approved rawresult.Manifest,
	) error {
		if ctx.Err() != nil || principal != "worker-a" || !reflect.DeepEqual(approvedRun, current) ||
			!reflect.DeepEqual(approved, manifest) {
			t.Fatal("approver received changed manifest context")
		}
		approved.Artifacts[0].URI = "s3://mutated/elsewhere"

		return nil
	})

	collector := newVerifiedRawCollector(t, resolver, reader, approver)
	collected, err := collector.CollectRawArtifacts(context.Background(), "worker-a", current)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(collected, RawArtifactSet{
		Manifest: manifestReference, Artifacts: manifest.Artifacts,
	}) {
		t.Fatalf("collected = %#v", collected)
	}
	wantReads := append([]run.Artifact{manifestReference}, manifest.Artifacts...)
	if !reflect.DeepEqual(reads, wantReads) {
		t.Fatalf("reads = %#v, want %#v", reads, wantReads)
	}
}

func TestVerifiedRawCollectorClassifiesManifestFailures(t *testing.T) {
	t.Parallel()

	current := boundClaim(run.StateCollecting).Run
	validReference := collectorManifestReference(current.ID)
	tests := []struct {
		name       string
		resolveErr error
		reference  run.Artifact
		readErr    error
		content    []byte
		want       error
	}{
		{name: "publication pending", resolveErr: ErrArtifactsNotReady, want: ErrArtifactsNotReady},
		{name: "manifest not visible", reference: validReference, readErr: objectstore.ErrObjectNotFound, want: ErrArtifactsNotReady},
		{name: "manifest bytes changed", reference: validReference, readErr: objectstore.ErrObjectMismatch, want: ErrInvalidArtifacts},
		{name: "manifest read unavailable", reference: validReference, readErr: run.ErrUnavailable, want: run.ErrUnavailable},
		{name: "invalid manifest JSON", reference: validReference, content: []byte(`{}`), want: ErrInvalidArtifacts},
		{name: "ambiguous evidence error", reference: validReference, readErr: errors.Join(objectstore.ErrObjectNotFound, objectstore.ErrObjectMismatch), want: run.ErrValidation},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			resolver := resolveRawManifestFunc(func(context.Context, string, run.Run) (run.Artifact, error) {
				return test.reference, test.resolveErr
			})
			reader := readArtifactFunc(func(context.Context, run.Artifact) ([]byte, error) {
				return test.content, test.readErr
			})
			collector := newVerifiedRawCollector(t, resolver, reader, acceptingRawApprover())
			if _, err := collector.CollectRawArtifacts(context.Background(), "worker-a", current); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestVerifiedRawCollectorRejectsInvalidManifestReferenceBeforeRead(t *testing.T) {
	t.Parallel()

	current := boundClaim(run.StateCollecting).Run
	valid := collectorManifestReference(current.ID)
	tests := map[string]run.Artifact{
		"wrong run":    withArtifact(valid, func(a *run.Artifact) { a.RunID = "perf-20260903-120000-deadbeef" }),
		"normalized":   withArtifact(valid, func(a *run.Artifact) { a.Kind = "normalized" }),
		"media type":   withArtifact(valid, func(a *run.Artifact) { a.MediaType = "text/plain" }),
		"format":       withArtifact(valid, func(a *run.Artifact) { a.Format = "local-capture/v1" }),
		"invalid hash": withArtifact(valid, func(a *run.Artifact) { a.SHA256 = "invalid" }),
	}

	for name, reference := range tests {
		reference := reference
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			readerCalled := false
			collector := newVerifiedRawCollector(t,
				resolveRawManifestFunc(func(context.Context, string, run.Run) (run.Artifact, error) {
					return reference, nil
				}),
				readArtifactFunc(func(context.Context, run.Artifact) ([]byte, error) {
					readerCalled = true
					return nil, nil
				}),
				acceptingRawApprover(),
			)
			if _, err := collector.CollectRawArtifacts(context.Background(), "worker-a", current); !errors.Is(err, ErrInvalidArtifacts) {
				t.Fatalf("error = %v", err)
			}
			if readerCalled {
				t.Fatal("invalid manifest reference was read")
			}
		})
	}
}

func TestVerifiedRawCollectorRejectsManifestSourceCollision(t *testing.T) {
	t.Parallel()

	current := boundClaim(run.StateCollecting).Run
	manifestReference := collectorManifestReference(current.ID)
	manifest := collectorManifest(current.ID)
	manifest.Artifacts[0].ID = manifestReference.ID
	readCount := 0
	reader := readArtifactFunc(func(context.Context, run.Artifact) ([]byte, error) {
		readCount++
		return encodeRawManifest(t, manifest), nil
	})
	collector := newVerifiedRawCollector(
		t, fixedRawResolver(manifestReference), reader, acceptingRawApprover(),
	)
	if _, err := collector.CollectRawArtifacts(
		context.Background(), "worker-a", current,
	); !errors.Is(err, ErrInvalidArtifacts) {
		t.Fatalf("error = %v", err)
	}
	if readCount != 1 {
		t.Fatalf("reads = %d, want manifest only", readCount)
	}
}

func TestVerifiedRawCollectorClassifiesApprovalAndSourceFailures(t *testing.T) {
	t.Parallel()

	current := boundClaim(run.StateCollecting).Run
	manifest := collectorManifest(current.ID)
	manifestReference := collectorManifestReference(current.ID)
	manifestBytes := encodeRawManifest(t, manifest)
	tests := []struct {
		name       string
		approveErr error
		sourceErr  error
		want       error
	}{
		{name: "unapproved provenance", approveErr: run.ErrForbidden, want: ErrInvalidArtifacts},
		{name: "invalid approval", approveErr: run.ErrValidation, want: ErrInvalidArtifacts},
		{name: "approval unavailable", approveErr: run.ErrUnavailable, want: run.ErrUnavailable},
		{name: "source not visible", sourceErr: objectstore.ErrObjectNotFound, want: ErrArtifactsNotReady},
		{name: "source bytes changed", sourceErr: objectstore.ErrObjectMismatch, want: ErrInvalidArtifacts},
		{name: "source read unavailable", sourceErr: run.ErrUnavailable, want: run.ErrUnavailable},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			reader := readArtifactFunc(func(_ context.Context, artifact run.Artifact) ([]byte, error) {
				if artifact == manifestReference {
					return manifestBytes, nil
				}
				return nil, test.sourceErr
			})
			approver := approveRawManifestFunc(func(context.Context, string, run.Run, rawresult.Manifest) error {
				return test.approveErr
			})
			collector := newVerifiedRawCollector(t, fixedRawResolver(manifestReference), reader, approver)
			if _, err := collector.CollectRawArtifacts(context.Background(), "worker-a", current); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestVerifiedRawCollectorValidatesDependenciesAndContext(t *testing.T) {
	t.Parallel()

	current := boundClaim(run.StateCollecting).Run
	resolver := fixedRawResolver(collectorManifestReference(current.ID))
	reader := readArtifactFunc(func(context.Context, run.Artifact) ([]byte, error) { return nil, nil })
	approver := acceptingRawApprover()
	for name, build := range map[string]func() error{
		"resolver": func() error {
			_, err := NewVerifiedRawCollector(nil, reader, approver, rawContractsVersion)
			return err
		},
		"reader": func() error {
			_, err := NewVerifiedRawCollector(resolver, nil, approver, rawContractsVersion)
			return err
		},
		"approver": func() error {
			_, err := NewVerifiedRawCollector(resolver, reader, nil, rawContractsVersion)
			return err
		},
		"version": func() error { _, err := NewVerifiedRawCollector(resolver, reader, approver, "01.0.0"); return err },
	} {
		if err := build(); !errors.Is(err, run.ErrValidation) {
			t.Fatalf("%s error = %v", name, err)
		}
	}

	collector := newVerifiedRawCollector(t, resolver, reader, approver)
	for _, test := range []struct {
		name      string
		principal string
		current   run.Run
	}{
		{name: "empty principal", current: current},
		{name: "invalid run", principal: "worker-a", current: run.Run{ID: "invalid"}},
	} {
		if _, err := collector.CollectRawArtifacts(
			context.Background(), test.principal, test.current,
		); !errors.Is(err, run.ErrValidation) {
			t.Fatalf("%s error = %v", test.name, err)
		}
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := collector.CollectRawArtifacts(canceled, "worker-a", current); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled error = %v", err)
	}
}

func collectorManifest(runID string) rawresult.Manifest {
	return rawresult.Manifest{
		SchemaVersion:    1,
		Kind:             "RawResult",
		ContractsVersion: rawContractsVersion,
		RunID:            runID,
		TestID:           "checkout-api",
		Workload: rawresult.Identity{
			ID: "checkout-smoke", Version: "1.0.0", SHA256: strings.Repeat("a", 64),
		},
		Producer: rawresult.Producer{
			Name: "k6", Version: "2.2.0",
			Image: "ghcr.io/stanimirivanov/perfeng-k6@sha256:" + strings.Repeat("b", 64),
		},
		MeasurementWindow: rawresult.Window{
			Start: "2026-09-03T12:00:00Z", End: "2026-09-03T12:01:00Z",
		},
		CreatedAt: "2026-09-03T12:01:01Z",
		Artifacts: collectorSourceArtifacts(runID),
	}
}

func collectorManifestReference(runID string) run.Artifact {
	artifact := rawArtifactSet(runID).Manifest
	artifact.URI = "s3://perfeng-artifacts/runs/" + runID + "/raw-result.json"
	return artifact
}

func collectorSourceArtifacts(runID string) []run.Artifact {
	artifacts := rawArtifacts(runID)
	artifacts[0].URI = "s3://perfeng-artifacts/runs/" + runID + "/summary.json"
	artifacts[1].URI = "s3://perfeng-artifacts/runs/" + runID + "/points.jsonl"
	return artifacts
}

func encodeRawManifest(t testing.TB, manifest rawresult.Manifest) []byte {
	t.Helper()
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}

	return data
}

func newVerifiedRawCollector(
	t testing.TB,
	resolver RawManifestResolver,
	reader ArtifactByteReader,
	approver RawManifestApprover,
) *VerifiedRawCollector {
	t.Helper()
	collector, err := NewVerifiedRawCollector(resolver, reader, approver, rawContractsVersion)
	if err != nil {
		t.Fatal(err)
	}

	return collector
}

func fixedRawResolver(reference run.Artifact) RawManifestResolver {
	return resolveRawManifestFunc(func(context.Context, string, run.Run) (run.Artifact, error) {
		return reference, nil
	})
}

func acceptingRawApprover() RawManifestApprover {
	return approveRawManifestFunc(func(context.Context, string, run.Run, rawresult.Manifest) error {
		return nil
	})
}

func withArtifact(artifact run.Artifact, change func(*run.Artifact)) run.Artifact {
	change(&artifact)
	return artifact
}
