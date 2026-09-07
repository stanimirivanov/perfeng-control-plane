package objectstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

type s3ArtifactAPIFunc struct {
	get  func(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	head func(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
}

func (client s3ArtifactAPIFunc) GetObject(
	ctx context.Context,
	input *s3.GetObjectInput,
	options ...func(*s3.Options),
) (*s3.GetObjectOutput, error) {
	return client.get(ctx, input, options...)
}

func (client s3ArtifactAPIFunc) HeadObject(
	ctx context.Context,
	input *s3.HeadObjectInput,
	options ...func(*s3.Options),
) (*s3.HeadObjectOutput, error) {
	return client.head(ctx, input, options...)
}

func TestS3ArtifactAccessComposesDiscoveryAndVerifiedReads(t *testing.T) {
	content := []byte(`{"schemaVersion":1}`)
	metadataByKey := map[string]map[string]string{
		"runs/" + manifestRunID + "/raw-result.json": {
			"artifact-id": "11111111-1111-4111-8111-111111111111",
			"sha256":      strings.Repeat("a", 64),
			"format":      "raw-result/v2",
		},
		"runs/" + manifestRunID + "/normalized-result.json": {
			"artifact-id": "22222222-2222-4222-8222-222222222222",
			"sha256":      strings.Repeat("b", 64),
			"format":      normalizedFormat,
		},
		"runs/" + manifestRunID + "/analysis-result.json": {
			"artifact-id": "44444444-4444-4444-8444-444444444444",
			"sha256":      strings.Repeat("d", 64),
			"format":      reportFormat,
		},
	}
	client := s3ArtifactAPIFunc{
		head: func(
			ctx context.Context,
			input *s3.HeadObjectInput,
			_ ...func(*s3.Options),
		) (*s3.HeadObjectOutput, error) {
			if ctx.Err() != nil || aws.ToString(input.Bucket) != "perfeng-artifacts" {
				t.Fatalf("HeadObject() bucket = %q", aws.ToString(input.Bucket))
			}
			metadata, exists := metadataByKey[aws.ToString(input.Key)]
			if !exists {
				t.Fatalf("HeadObject() key = %q", aws.ToString(input.Key))
			}

			return &s3.HeadObjectOutput{
				ContentLength: aws.Int64(512),
				ContentType:   aws.String("application/json"),
				Metadata:      metadata,
			}, nil
		},
		get: func(
			ctx context.Context,
			input *s3.GetObjectInput,
			_ ...func(*s3.Options),
		) (*s3.GetObjectOutput, error) {
			if ctx.Err() != nil || aws.ToString(input.Bucket) != "perfeng-artifacts" ||
				aws.ToString(input.Key) != "runs/"+manifestRunID+"/payload.json" {
				t.Fatalf("GetObject() = %q, %q", aws.ToString(input.Bucket), aws.ToString(input.Key))
			}

			return &s3.GetObjectOutput{
				Body:          io.NopCloser(bytes.NewReader(content)),
				ContentLength: aws.Int64(int64(len(content))),
				ContentType:   aws.String("application/json"),
			}, nil
		},
	}
	access, err := NewS3ArtifactAccess(client, S3ArtifactAccessConfig{
		Bucket:            "perfeng-artifacts",
		RawManifestFormat: "raw-result/v2",
		MaximumReadBytes:  1024,
	})
	if err != nil {
		t.Fatal(err)
	}

	rawArtifact, err := access.ResolveRawManifest(
		context.Background(), "worker-a", run.Run{ID: manifestRunID},
	)
	if err != nil || rawArtifact.Format != "raw-result/v2" || rawArtifact.Kind != "raw" {
		t.Fatalf("ResolveRawManifest() = %#v, %v", rawArtifact, err)
	}
	normalizedArtifact, err := access.ResolveNormalizedManifest(
		context.Background(), "worker-a", run.Run{ID: manifestRunID},
	)
	if err != nil || normalizedArtifact.Format != normalizedFormat ||
		normalizedArtifact.Kind != "normalized" {
		t.Fatalf("ResolveNormalizedManifest() = %#v, %v", normalizedArtifact, err)
	}
	reportArtifact, err := access.ResolveReportManifest(
		context.Background(), "worker-a", run.Run{ID: manifestRunID},
	)
	if err != nil || reportArtifact.Format != reportFormat || reportArtifact.Kind != "normalized" {
		t.Fatalf("ResolveReportManifest() = %#v, %v", reportArtifact, err)
	}
	reference := run.Artifact{
		ID: "33333333-3333-4333-8333-333333333333", RunID: manifestRunID, Kind: "raw",
		URI:       "s3://perfeng-artifacts/runs/" + manifestRunID + "/payload.json",
		SHA256:    fmt.Sprintf("%x", sha256.Sum256(content)),
		SizeBytes: int64(len(content)), MediaType: "application/json", Format: "k6-summary-json",
	}
	actual, err := access.Read(context.Background(), reference)
	if err != nil || !bytes.Equal(actual, content) {
		t.Fatalf("Read() = %q, %v", actual, err)
	}
}

func TestNewS3ArtifactAccessRejectsInvalidPolicy(t *testing.T) {
	client := s3ArtifactAPIFunc{
		get: func(
			context.Context, *s3.GetObjectInput, ...func(*s3.Options),
		) (*s3.GetObjectOutput, error) {
			return nil, errors.New("not called")
		},
		head: func(
			context.Context, *s3.HeadObjectInput, ...func(*s3.Options),
		) (*s3.HeadObjectOutput, error) {
			return nil, errors.New("not called")
		},
	}
	valid := S3ArtifactAccessConfig{
		Bucket:            "perfeng-artifacts",
		RawManifestFormat: "raw-result/v2",
		MaximumReadBytes:  1024,
	}
	tests := map[string]struct {
		client S3API
		config S3ArtifactAccessConfig
	}{
		"client": {config: valid},
		"bucket": {
			client: client,
			config: S3ArtifactAccessConfig{
				RawManifestFormat: "raw-result/v2",
				MaximumReadBytes:  1024,
			},
		},
		"raw format": {
			client: client,
			config: S3ArtifactAccessConfig{
				Bucket:            "perfeng-artifacts",
				RawManifestFormat: "raw-result/v3",
				MaximumReadBytes:  1024,
			},
		},
		"read limit": {
			client: client,
			config: S3ArtifactAccessConfig{
				Bucket:            "perfeng-artifacts",
				RawManifestFormat: "raw-result/v2",
			},
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := NewS3ArtifactAccess(test.client, test.config); !errors.Is(err, run.ErrValidation) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestNilS3ArtifactAccessRejectsOperations(t *testing.T) {
	var access *S3ArtifactAccess
	if _, err := access.ResolveRawManifest(
		context.Background(), "worker-a", run.Run{ID: manifestRunID},
	); !errors.Is(err, run.ErrValidation) {
		t.Fatalf("ResolveRawManifest() error = %v", err)
	}
	if _, err := access.ResolveNormalizedManifest(
		context.Background(), "worker-a", run.Run{ID: manifestRunID},
	); !errors.Is(err, run.ErrValidation) {
		t.Fatalf("ResolveNormalizedManifest() error = %v", err)
	}
	if _, err := access.ResolveReportManifest(
		context.Background(), "worker-a", run.Run{ID: manifestRunID},
	); !errors.Is(err, run.ErrValidation) {
		t.Fatalf("ResolveReportManifest() error = %v", err)
	}
	if _, err := access.Read(context.Background(), run.Artifact{}); !errors.Is(err, run.ErrValidation) {
		t.Fatalf("Read() error = %v", err)
	}
}

func TestS3ArtifactAccessPreservesCancellation(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	var access *S3ArtifactAccess
	if _, err := access.ResolveRawManifest(
		canceled, "worker-a", run.Run{},
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("ResolveRawManifest() error = %v", err)
	}
	if _, err := access.ResolveNormalizedManifest(
		canceled, "worker-a", run.Run{},
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("ResolveNormalizedManifest() error = %v", err)
	}
	if _, err := access.ResolveReportManifest(
		canceled, "worker-a", run.Run{},
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("ResolveReportManifest() error = %v", err)
	}
	if _, err := access.Read(canceled, run.Artifact{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Read() error = %v", err)
	}
}
