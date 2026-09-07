package objectstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

const rawManifestName = "raw-result.json"

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

// RawManifestResolver resolves the stable raw-result publication object into a
// complete immutable artifact reference. It does not read or parse object bytes.
type RawManifestResolver struct {
	inspector MetadataInspector
	bucket    string
	format    string
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
	if resolver == nil || principal == "" || !contract.ValidID(current.ID) {
		return run.Artifact{}, run.ErrValidation
	}

	key := fmt.Sprintf("runs/%s/%s", current.ID, rawManifestName)
	metadata, err := resolver.inspector.Inspect(ctx, resolver.bucket, key)
	if err != nil {
		return run.Artifact{}, err
	}
	if !validRawManifestMetadata(metadata, resolver.format) {
		return run.Artifact{}, run.ErrValidation
	}

	artifact := run.Artifact{
		ID: metadata.Values["artifact-id"], RunID: current.ID, Kind: "raw",
		URI:    fmt.Sprintf("s3://%s/%s", resolver.bucket, key),
		SHA256: metadata.Values["sha256"], SizeBytes: metadata.SizeBytes,
		MediaType: metadata.MediaType, Format: metadata.Values["format"],
	}
	if artifact.Validate() != nil {
		return run.Artifact{}, run.ErrValidation
	}

	return artifact, nil
}

func validRawManifestMetadata(metadata Metadata, expectedFormat string) bool {
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
