package objectstore

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

const manifestRunID = "perf-20260902-130000-a1b2c3d5"

type inspectMetadataFunc func(context.Context, string, string) (Metadata, error)

func (inspect inspectMetadataFunc) Inspect(
	ctx context.Context,
	bucket string,
	key string,
) (Metadata, error) {
	return inspect(ctx, bucket, key)
}

func TestRawManifestResolverReturnsPublishedReference(t *testing.T) {
	for _, format := range []string{"raw-result/v1", "raw-result/v2"} {
		t.Run(format, func(t *testing.T) {
			wantMetadata := manifestMetadata(format)
			calls := 0
			inspector := inspectMetadataFunc(func(
				ctx context.Context,
				bucket string,
				key string,
			) (Metadata, error) {
				calls++
				if ctx.Err() != nil || bucket != "perfeng-artifacts" ||
					key != "runs/"+manifestRunID+"/raw-result.json" {
					t.Fatalf("Inspect() = %q, %q", bucket, key)
				}

				return wantMetadata, nil
			})
			resolver, err := NewRawManifestResolver(inspector, "perfeng-artifacts", format)
			if err != nil {
				t.Fatal(err)
			}

			artifact, err := resolver.ResolveRawManifest(
				context.Background(), "worker-a", run.Run{ID: manifestRunID},
			)
			if err != nil {
				t.Fatal(err)
			}
			want := run.Artifact{
				ID: wantMetadata.Values["artifact-id"], RunID: manifestRunID, Kind: "raw",
				URI:    "s3://perfeng-artifacts/runs/" + manifestRunID + "/raw-result.json",
				SHA256: wantMetadata.Values["sha256"], SizeBytes: wantMetadata.SizeBytes,
				MediaType: "application/json", Format: format,
			}
			if calls != 1 || !reflect.DeepEqual(artifact, want) {
				t.Fatalf("ResolveRawManifest() = %#v; calls = %d", artifact, calls)
			}
		})
	}
}

func TestRawManifestResolverRejectsInvalidPublishedMetadata(t *testing.T) {
	tests := map[string]func(*Metadata){
		"negative size": func(metadata *Metadata) { metadata.SizeBytes = -1 },
		"media type":    func(metadata *Metadata) { metadata.MediaType = "application/octet-stream" },
		"missing ID":    func(metadata *Metadata) { delete(metadata.Values, "artifact-id") },
		"invalid ID":    func(metadata *Metadata) { metadata.Values["artifact-id"] = "invalid" },
		"missing hash":  func(metadata *Metadata) { delete(metadata.Values, "sha256") },
		"invalid hash":  func(metadata *Metadata) { metadata.Values["sha256"] = "invalid" },
		"other format":  func(metadata *Metadata) { metadata.Values["format"] = "raw-result/v1" },
		"extra metadata": func(metadata *Metadata) {
			metadata.Values["unreviewed"] = "value"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			metadata := manifestMetadata("raw-result/v2")
			mutate(&metadata)
			resolver := newRawManifestResolver(t, inspectMetadataFunc(func(
				context.Context, string, string,
			) (Metadata, error) {
				return metadata, nil
			}))
			if _, err := resolver.ResolveRawManifest(
				context.Background(), "worker-a", run.Run{ID: manifestRunID},
			); !errors.Is(err, run.ErrValidation) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestRawManifestResolverPreservesInspectionErrors(t *testing.T) {
	want := errors.Join(ErrObjectNotFound, errors.New("publication pending"))
	resolver := newRawManifestResolver(t, inspectMetadataFunc(func(
		context.Context, string, string,
	) (Metadata, error) {
		return Metadata{}, want
	}))
	if _, err := resolver.ResolveRawManifest(
		context.Background(), "worker-a", run.Run{ID: manifestRunID},
	); !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("error = %v", err)
	}
}

func TestRawManifestResolverValidatesConfigurationAndContext(t *testing.T) {
	inspector := inspectMetadataFunc(func(context.Context, string, string) (Metadata, error) {
		return manifestMetadata("raw-result/v2"), nil
	})
	for name, input := range map[string]struct {
		inspector MetadataInspector
		bucket    string
		format    string
	}{
		"inspector": {bucket: "perfeng-artifacts", format: "raw-result/v2"},
		"bucket":    {inspector: inspector, bucket: "Invalid", format: "raw-result/v2"},
		"format":    {inspector: inspector, bucket: "perfeng-artifacts", format: "raw-result/v3"},
	} {
		if _, err := NewRawManifestResolver(
			input.inspector, input.bucket, input.format,
		); !errors.Is(err, run.ErrValidation) {
			t.Fatalf("%s error = %v", name, err)
		}
	}

	resolver := newRawManifestResolver(t, inspector)
	for name, input := range map[string]struct {
		principal string
		runID     string
	}{
		"principal": {runID: manifestRunID},
		"run ID":    {principal: "worker-a", runID: "invalid"},
	} {
		if _, err := resolver.ResolveRawManifest(
			context.Background(), input.principal, run.Run{ID: input.runID},
		); !errors.Is(err, run.ErrValidation) {
			t.Fatalf("%s error = %v", name, err)
		}
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := resolver.ResolveRawManifest(
		canceled, "worker-a", run.Run{ID: manifestRunID},
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}

	var nilResolver *RawManifestResolver
	if _, err := nilResolver.ResolveRawManifest(
		context.Background(), "worker-a", run.Run{ID: manifestRunID},
	); !errors.Is(err, run.ErrValidation) {
		t.Fatalf("nil resolver error = %v", err)
	}
}

func TestNormalizedManifestResolverReturnsPublishedReference(t *testing.T) {
	wantMetadata := manifestMetadata(normalizedFormat)
	calls := 0
	inspector := inspectMetadataFunc(func(
		ctx context.Context,
		bucket string,
		key string,
	) (Metadata, error) {
		calls++
		if ctx.Err() != nil || bucket != "perfeng-artifacts" ||
			key != "runs/"+manifestRunID+"/normalized-result.json" {
			t.Fatalf("Inspect() = %q, %q", bucket, key)
		}

		return wantMetadata, nil
	})
	resolver, err := NewNormalizedManifestResolver(inspector, "perfeng-artifacts")
	if err != nil {
		t.Fatal(err)
	}

	artifact, err := resolver.ResolveNormalizedManifest(
		context.Background(), "worker-a", run.Run{ID: manifestRunID},
	)
	if err != nil {
		t.Fatal(err)
	}
	want := run.Artifact{
		ID: wantMetadata.Values["artifact-id"], RunID: manifestRunID, Kind: "normalized",
		URI:    "s3://perfeng-artifacts/runs/" + manifestRunID + "/normalized-result.json",
		SHA256: wantMetadata.Values["sha256"], SizeBytes: wantMetadata.SizeBytes,
		MediaType: "application/json", Format: normalizedFormat,
	}
	if calls != 1 || !reflect.DeepEqual(artifact, want) {
		t.Fatalf("ResolveNormalizedManifest() = %#v; calls = %d", artifact, calls)
	}
}

func TestNormalizedManifestResolverRejectsInvalidPublishedMetadata(t *testing.T) {
	tests := map[string]func(*Metadata){
		"negative size": func(metadata *Metadata) { metadata.SizeBytes = -1 },
		"media type":    func(metadata *Metadata) { metadata.MediaType = "application/octet-stream" },
		"missing ID":    func(metadata *Metadata) { delete(metadata.Values, "artifact-id") },
		"invalid ID":    func(metadata *Metadata) { metadata.Values["artifact-id"] = "invalid" },
		"missing hash":  func(metadata *Metadata) { delete(metadata.Values, "sha256") },
		"invalid hash":  func(metadata *Metadata) { metadata.Values["sha256"] = "invalid" },
		"other format":  func(metadata *Metadata) { metadata.Values["format"] = "normalized-result/v2" },
		"extra metadata": func(metadata *Metadata) {
			metadata.Values["unreviewed"] = "value"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			metadata := manifestMetadata(normalizedFormat)
			mutate(&metadata)
			resolver := newNormalizedManifestResolver(t, inspectMetadataFunc(func(
				context.Context, string, string,
			) (Metadata, error) {
				return metadata, nil
			}))
			if _, err := resolver.ResolveNormalizedManifest(
				context.Background(), "worker-a", run.Run{ID: manifestRunID},
			); !errors.Is(err, run.ErrValidation) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestNormalizedManifestResolverPreservesInspectionErrors(t *testing.T) {
	want := errors.Join(ErrObjectNotFound, errors.New("publication pending"))
	resolver := newNormalizedManifestResolver(t, inspectMetadataFunc(func(
		context.Context, string, string,
	) (Metadata, error) {
		return Metadata{}, want
	}))
	if _, err := resolver.ResolveNormalizedManifest(
		context.Background(), "worker-a", run.Run{ID: manifestRunID},
	); !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("error = %v", err)
	}
}

func TestNormalizedManifestResolverValidatesConfigurationAndContext(t *testing.T) {
	inspector := inspectMetadataFunc(func(context.Context, string, string) (Metadata, error) {
		return manifestMetadata(normalizedFormat), nil
	})
	for name, input := range map[string]struct {
		inspector MetadataInspector
		bucket    string
	}{
		"inspector": {bucket: "perfeng-artifacts"},
		"bucket":    {inspector: inspector, bucket: "Invalid"},
	} {
		if _, err := NewNormalizedManifestResolver(
			input.inspector, input.bucket,
		); !errors.Is(err, run.ErrValidation) {
			t.Fatalf("%s error = %v", name, err)
		}
	}

	resolver := newNormalizedManifestResolver(t, inspector)
	for name, input := range map[string]struct {
		principal string
		runID     string
	}{
		"principal": {runID: manifestRunID},
		"run ID":    {principal: "worker-a", runID: "invalid"},
	} {
		if _, err := resolver.ResolveNormalizedManifest(
			context.Background(), input.principal, run.Run{ID: input.runID},
		); !errors.Is(err, run.ErrValidation) {
			t.Fatalf("%s error = %v", name, err)
		}
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := resolver.ResolveNormalizedManifest(
		canceled, "worker-a", run.Run{ID: manifestRunID},
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}

	var nilResolver *NormalizedManifestResolver
	if _, err := nilResolver.ResolveNormalizedManifest(
		context.Background(), "worker-a", run.Run{ID: manifestRunID},
	); !errors.Is(err, run.ErrValidation) {
		t.Fatalf("nil resolver error = %v", err)
	}
}

func TestReportManifestResolverReturnsPublishedReference(t *testing.T) {
	wantMetadata := manifestMetadata(reportFormat)
	calls := 0
	inspector := inspectMetadataFunc(func(
		ctx context.Context,
		bucket string,
		key string,
	) (Metadata, error) {
		calls++
		if ctx.Err() != nil || bucket != "perfeng-artifacts" ||
			key != "runs/"+manifestRunID+"/analysis-result.json" {
			t.Fatalf("Inspect() = %q, %q", bucket, key)
		}

		return wantMetadata, nil
	})
	resolver, err := NewReportManifestResolver(inspector, "perfeng-artifacts")
	if err != nil {
		t.Fatal(err)
	}

	artifact, err := resolver.ResolveReportManifest(
		context.Background(), "worker-a", run.Run{ID: manifestRunID},
	)
	if err != nil {
		t.Fatal(err)
	}
	want := run.Artifact{
		ID:        wantMetadata.Values["artifact-id"],
		RunID:     manifestRunID,
		Kind:      "normalized",
		URI:       "s3://perfeng-artifacts/runs/" + manifestRunID + "/analysis-result.json",
		SHA256:    wantMetadata.Values["sha256"],
		SizeBytes: wantMetadata.SizeBytes,
		MediaType: "application/json",
		Format:    reportFormat,
	}
	if calls != 1 || artifact != want {
		t.Fatalf("ResolveReportManifest() = %#v; calls = %d", artifact, calls)
	}
}

func TestReportManifestResolverRejectsInvalidPublishedMetadata(t *testing.T) {
	tests := map[string]func(*Metadata){
		"negative size": func(metadata *Metadata) { metadata.SizeBytes = -1 },
		"media type":    func(metadata *Metadata) { metadata.MediaType = "application/octet-stream" },
		"missing ID":    func(metadata *Metadata) { delete(metadata.Values, "artifact-id") },
		"invalid ID":    func(metadata *Metadata) { metadata.Values["artifact-id"] = "invalid" },
		"missing hash":  func(metadata *Metadata) { delete(metadata.Values, "sha256") },
		"invalid hash":  func(metadata *Metadata) { metadata.Values["sha256"] = "invalid" },
		"other format":  func(metadata *Metadata) { metadata.Values["format"] = normalizedFormat },
		"extra metadata": func(metadata *Metadata) {
			metadata.Values["unreviewed"] = "value"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			metadata := manifestMetadata(reportFormat)
			mutate(&metadata)
			resolver := newReportManifestResolver(t, inspectMetadataFunc(func(
				context.Context, string, string,
			) (Metadata, error) {
				return metadata, nil
			}))
			if _, err := resolver.ResolveReportManifest(
				context.Background(), "worker-a", run.Run{ID: manifestRunID},
			); !errors.Is(err, run.ErrValidation) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestReportManifestResolverPreservesInspectionErrors(t *testing.T) {
	want := errors.Join(ErrObjectNotFound, errors.New("publication pending"))
	resolver := newReportManifestResolver(t, inspectMetadataFunc(func(
		context.Context, string, string,
	) (Metadata, error) {
		return Metadata{}, want
	}))
	if _, err := resolver.ResolveReportManifest(
		context.Background(), "worker-a", run.Run{ID: manifestRunID},
	); !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("error = %v", err)
	}
}

func TestReportManifestResolverValidatesConfigurationAndContext(t *testing.T) {
	inspector := inspectMetadataFunc(func(context.Context, string, string) (Metadata, error) {
		return manifestMetadata(reportFormat), nil
	})
	for name, input := range map[string]struct {
		inspector MetadataInspector
		bucket    string
	}{
		"inspector": {bucket: "perfeng-artifacts"},
		"bucket":    {inspector: inspector, bucket: "Invalid"},
	} {
		if _, err := NewReportManifestResolver(
			input.inspector, input.bucket,
		); !errors.Is(err, run.ErrValidation) {
			t.Fatalf("%s error = %v", name, err)
		}
	}

	resolver := newReportManifestResolver(t, inspector)
	for name, input := range map[string]struct {
		principal string
		runID     string
	}{
		"principal": {runID: manifestRunID},
		"run ID":    {principal: "worker-a", runID: "invalid"},
	} {
		if _, err := resolver.ResolveReportManifest(
			context.Background(), input.principal, run.Run{ID: input.runID},
		); !errors.Is(err, run.ErrValidation) {
			t.Fatalf("%s error = %v", name, err)
		}
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := resolver.ResolveReportManifest(
		canceled, "worker-a", run.Run{ID: manifestRunID},
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}

	var nilResolver *ReportManifestResolver
	if _, err := nilResolver.ResolveReportManifest(
		context.Background(), "worker-a", run.Run{ID: manifestRunID},
	); !errors.Is(err, run.ErrValidation) {
		t.Fatalf("nil resolver error = %v", err)
	}
}

func manifestMetadata(format string) Metadata {
	return Metadata{
		SizeBytes: 512, MediaType: "application/json",
		Values: map[string]string{
			"artifact-id": "11111111-1111-4111-8111-111111111111",
			"sha256":      strings.Repeat("a", 64), "format": format,
		},
	}
}

func newRawManifestResolver(t testing.TB, inspector MetadataInspector) *RawManifestResolver {
	t.Helper()
	resolver, err := NewRawManifestResolver(inspector, "perfeng-artifacts", "raw-result/v2")
	if err != nil {
		t.Fatal(err)
	}

	return resolver
}

func newNormalizedManifestResolver(
	t testing.TB,
	inspector MetadataInspector,
) *NormalizedManifestResolver {
	t.Helper()
	resolver, err := NewNormalizedManifestResolver(inspector, "perfeng-artifacts")
	if err != nil {
		t.Fatal(err)
	}

	return resolver
}

func newReportManifestResolver(
	t testing.TB,
	inspector MetadataInspector,
) *ReportManifestResolver {
	t.Helper()
	resolver, err := NewReportManifestResolver(inspector, "perfeng-artifacts")
	if err != nil {
		t.Fatal(err)
	}

	return resolver
}
