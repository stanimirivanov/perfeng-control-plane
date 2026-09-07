package objectstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

const (
	rawManifestName        = "raw-result.json"
	normalizedManifestName = "normalized-result.json"
	reportManifestName     = "analysis-result.json"
	normalizedFormat       = "normalized-result/v1"
	reportFormat           = "analysis-result/v1"
)

// Metadata describes one object without transferring its body. Values contains
// only user-defined metadata returned by the storage service.
type Metadata struct {
	SizeBytes int64
	MediaType string
	Values    map[string]string
}

// MetadataInspector reads object metadata from an approved storage adapter.
type MetadataInspector interface {
	// Inspect returns independently owned metadata for one bucket and key.
	Inspect(context.Context, string, string) (Metadata, error)
}

type manifestPublication struct {
	name   string
	kind   string
	format string
}

// RawManifestResolver resolves the stable raw-result publication object into a
// complete immutable artifact reference. It does not read or parse object bytes.
type RawManifestResolver struct {
	inspector MetadataInspector
	bucket    string
	format    string
}

// NormalizedManifestResolver resolves the stable normalized-result publication
// object into a complete immutable artifact reference. It does not read or
// parse object bytes.
type NormalizedManifestResolver struct {
	inspector MetadataInspector
	bucket    string
}

// ReportManifestResolver resolves the stable report publication object into a
// complete immutable artifact reference. It does not read or parse object bytes.
type ReportManifestResolver struct {
	inspector MetadataInspector
	bucket    string
}

// NewRawManifestResolver pins discovery to one bucket and supported manifest
// generation. Callers must construct a new resolver to change either value.
func NewRawManifestResolver(
	inspector MetadataInspector,
	bucket string,
	format string,
) (*RawManifestResolver, error) {
	if inspector == nil || !validBucket(bucket) || !validRawManifestFormat(format) {
		return nil, run.ErrValidation
	}

	return &RawManifestResolver{inspector: inspector, bucket: bucket, format: format}, nil
}

// NewNormalizedManifestResolver pins discovery to one approved bucket.
func NewNormalizedManifestResolver(
	inspector MetadataInspector,
	bucket string,
) (*NormalizedManifestResolver, error) {
	if inspector == nil || !validBucket(bucket) {
		return nil, run.ErrValidation
	}

	return &NormalizedManifestResolver{inspector: inspector, bucket: bucket}, nil
}

// NewReportManifestResolver pins report discovery to one approved bucket.
func NewReportManifestResolver(
	inspector MetadataInspector,
	bucket string,
) (*ReportManifestResolver, error) {
	if inspector == nil || !validBucket(bucket) {
		return nil, run.ErrValidation
	}

	return &ReportManifestResolver{inspector: inspector, bucket: bucket}, nil
}

// ResolveRawManifest inspects only the deterministic Run-scoped publication
// key and requires the complete metadata contract emitted by the publisher.
func (resolver *RawManifestResolver) ResolveRawManifest(
	ctx context.Context,
	principal string,
	current run.Run,
) (run.Artifact, error) {
	if err := ctx.Err(); err != nil {
		return run.Artifact{}, err
	}
	if resolver == nil {
		return run.Artifact{}, run.ErrValidation
	}

	return resolvePublishedManifest(
		ctx,
		resolver.inspector,
		resolver.bucket,
		manifestPublication{name: rawManifestName, kind: "raw", format: resolver.format},
		principal,
		current,
	)
}

// ResolveNormalizedManifest inspects only the deterministic Run-scoped
// publication key and requires the complete metadata contract emitted by the
// normalizer.
func (resolver *NormalizedManifestResolver) ResolveNormalizedManifest(
	ctx context.Context,
	principal string,
	current run.Run,
) (run.Artifact, error) {
	if err := ctx.Err(); err != nil {
		return run.Artifact{}, err
	}
	if resolver == nil {
		return run.Artifact{}, run.ErrValidation
	}

	return resolvePublishedManifest(
		ctx,
		resolver.inspector,
		resolver.bucket,
		manifestPublication{
			name:   normalizedManifestName,
			kind:   "normalized",
			format: normalizedFormat,
		},
		principal,
		current,
	)
}

// ResolveReportManifest inspects only the deterministic Run-scoped report
// publication key and requires the complete metadata contract.
func (resolver *ReportManifestResolver) ResolveReportManifest(
	ctx context.Context,
	principal string,
	current run.Run,
) (run.Artifact, error) {
	if err := ctx.Err(); err != nil {
		return run.Artifact{}, err
	}
	if resolver == nil {
		return run.Artifact{}, run.ErrValidation
	}

	return resolvePublishedManifest(
		ctx,
		resolver.inspector,
		resolver.bucket,
		manifestPublication{
			name:   reportManifestName,
			kind:   "normalized",
			format: reportFormat,
		},
		principal,
		current,
	)
}

func resolvePublishedManifest(
	ctx context.Context,
	inspector MetadataInspector,
	bucket string,
	publication manifestPublication,
	principal string,
	current run.Run,
) (run.Artifact, error) {
	if principal == "" || !contract.ValidID(current.ID) {
		return run.Artifact{}, run.ErrValidation
	}

	key := fmt.Sprintf("runs/%s/%s", current.ID, publication.name)
	metadata, err := inspector.Inspect(ctx, bucket, key)
	if err != nil {
		return run.Artifact{}, err
	}
	if !validManifestMetadata(metadata, publication.format) {
		return run.Artifact{}, run.ErrValidation
	}

	artifact := run.Artifact{
		ID:        metadata.Values["artifact-id"],
		RunID:     current.ID,
		Kind:      publication.kind,
		URI:       fmt.Sprintf("s3://%s/%s", bucket, key),
		SHA256:    metadata.Values["sha256"],
		SizeBytes: metadata.SizeBytes,
		MediaType: metadata.MediaType,
		Format:    metadata.Values["format"],
	}
	if artifact.Validate() != nil {
		return run.Artifact{}, run.ErrValidation
	}

	return artifact, nil
}

func validManifestMetadata(metadata Metadata, expectedFormat string) bool {
	if metadata.MediaType != "application/json" || len(metadata.Values) != 3 ||
		metadata.Values["format"] != expectedFormat {
		return false
	}
	for _, field := range []string{"artifact-id", "sha256", "format"} {
		if strings.TrimSpace(metadata.Values[field]) == "" {
			return false
		}
	}

	return true
}

func validRawManifestFormat(format string) bool {
	return format == "raw-result/v1" || format == "raw-result/v2"
}
