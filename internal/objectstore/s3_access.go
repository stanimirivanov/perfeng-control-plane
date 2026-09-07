package objectstore

import (
	"context"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

// S3API contains the object operations required for artifact discovery and
// verified reads. A configured AWS SDK S3 client satisfies this interface.
type S3API interface {
	S3GetObjectAPI
	S3HeadObjectAPI
}

// S3ArtifactAccessConfig fixes the storage and contract policy shared by raw
// discovery, normalized discovery and verified object reads.
type S3ArtifactAccessConfig struct {
	Bucket            string
	RawManifestFormat string
	MaximumReadBytes  int64
}

// Validate checks the complete artifact storage policy without constructing a client.
func (config S3ArtifactAccessConfig) Validate() error {
	if !validBucket(config.Bucket) ||
		!validRawManifestFormat(config.RawManifestFormat) ||
		config.MaximumReadBytes < 1 || config.MaximumReadBytes > maximumReadSize {
		return run.ErrValidation
	}

	return nil
}

// S3ArtifactAccess composes S3 metadata discovery and verified byte reads
// behind the interfaces consumed by reconciliation collectors.
type S3ArtifactAccess struct {
	rawManifest        *RawManifestResolver
	normalizedManifest *NormalizedManifestResolver
	reader             *Reader
}

// NewS3ArtifactAccess constructs all artifact access adapters from one client
// and one policy. Client credentials, endpoint and transport remain the
// responsibility of the executable composition root.
func NewS3ArtifactAccess(
	client S3API,
	config S3ArtifactAccessConfig,
) (*S3ArtifactAccess, error) {
	if client == nil || config.Validate() != nil {
		return nil, run.ErrValidation
	}

	getter, err := NewS3Getter(client)
	if err != nil {
		return nil, err
	}
	inspector, err := NewS3MetadataInspector(client)
	if err != nil {
		return nil, err
	}
	rawManifest, err := NewRawManifestResolver(
		inspector,
		config.Bucket,
		config.RawManifestFormat,
	)
	if err != nil {
		return nil, err
	}
	normalizedManifest, err := NewNormalizedManifestResolver(inspector, config.Bucket)
	if err != nil {
		return nil, err
	}
	reader, err := NewReader(getter, config.Bucket, config.MaximumReadBytes)
	if err != nil {
		return nil, err
	}

	return &S3ArtifactAccess{
		rawManifest:        rawManifest,
		normalizedManifest: normalizedManifest,
		reader:             reader,
	}, nil
}

// ResolveRawManifest returns the stable raw-result publication reference.
func (access *S3ArtifactAccess) ResolveRawManifest(
	ctx context.Context,
	principal string,
	current run.Run,
) (run.Artifact, error) {
	if err := ctx.Err(); err != nil {
		return run.Artifact{}, err
	}
	if access == nil || access.rawManifest == nil {
		return run.Artifact{}, run.ErrValidation
	}

	return access.rawManifest.ResolveRawManifest(ctx, principal, current)
}

// ResolveNormalizedManifest returns the stable normalized-result publication reference.
func (access *S3ArtifactAccess) ResolveNormalizedManifest(
	ctx context.Context,
	principal string,
	current run.Run,
) (run.Artifact, error) {
	if err := ctx.Err(); err != nil {
		return run.Artifact{}, err
	}
	if access == nil || access.normalizedManifest == nil {
		return run.Artifact{}, run.ErrValidation
	}

	return access.normalizedManifest.ResolveNormalizedManifest(ctx, principal, current)
}

// Read verifies and returns the bytes of one immutable artifact reference.
func (access *S3ArtifactAccess) Read(
	ctx context.Context,
	artifact run.Artifact,
) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if access == nil || access.reader == nil {
		return nil, run.ErrValidation
	}

	return access.reader.Read(ctx, artifact)
}
