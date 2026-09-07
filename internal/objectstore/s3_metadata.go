package objectstore

import (
	"context"
	"errors"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

// S3HeadObjectAPI is the single AWS SDK operation required by S3MetadataInspector.
// *s3.Client satisfies this interface.
type S3HeadObjectAPI interface {
	// HeadObject returns object metadata without transferring its body.
	HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
}

// S3MetadataInspector adapts AWS HeadObject to the storage-neutral metadata contract.
type S3MetadataInspector struct {
	client S3HeadObjectAPI
}

var _ MetadataInspector = (*S3MetadataInspector)(nil)
var _ S3HeadObjectAPI = (*s3.Client)(nil)

// NewS3MetadataInspector requires a configured AWS SDK client. Endpoint,
// credentials, region and retry policy belong to client composition.
func NewS3MetadataInspector(client S3HeadObjectAPI) (*S3MetadataInspector, error) {
	if client == nil {
		return nil, run.ErrValidation
	}

	return &S3MetadataInspector{client: client}, nil
}

// Inspect returns only the fields required to construct and verify an artifact reference.
func (inspector *S3MetadataInspector) Inspect(
	ctx context.Context,
	bucket string,
	key string,
) (Metadata, error) {
	if !validBucket(bucket) || key == "" || strings.HasPrefix(key, "/") {
		return Metadata{}, run.ErrValidation
	}
	if err := ctx.Err(); err != nil {
		return Metadata{}, err
	}

	output, err := inspector.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(key),
	})
	if err != nil {
		return Metadata{}, classifyS3Error(ctx, err)
	}
	if output == nil || output.ContentLength == nil || output.ContentType == nil {
		return Metadata{}, run.ErrUnavailable
	}

	values := make(map[string]string, len(output.Metadata))
	for key, value := range output.Metadata {
		values[key] = value
	}
	metadata := Metadata{
		SizeBytes: aws.ToInt64(output.ContentLength),
		MediaType: aws.ToString(output.ContentType), Values: values,
	}
	if metadata.SizeBytes < 0 || metadata.MediaType == "" {
		return Metadata{}, errors.Join(run.ErrUnavailable, ErrObjectMismatch)
	}

	return metadata, nil
}
