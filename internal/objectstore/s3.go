package objectstore

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

// S3GetObjectAPI is the single AWS SDK operation required by S3Getter.
// *s3.Client satisfies this interface.
type S3GetObjectAPI interface {
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
}

// S3Getter adapts the AWS SDK for Go v2 to the storage-neutral Getter contract.
type S3Getter struct {
	client S3GetObjectAPI
}

var _ Getter = (*S3Getter)(nil)
var _ S3GetObjectAPI = (*s3.Client)(nil)

// NewS3Getter requires a configured AWS SDK client. Endpoint, credentials,
// region, retry policy and path-style addressing belong to client composition.
func NewS3Getter(client S3GetObjectAPI) (*S3Getter, error) {
	if client == nil {
		return nil, run.ErrValidation
	}

	return &S3Getter{client: client}, nil
}

// GetObject retrieves one object and exposes only the fields required for
// independent verification. Backend messages and request details are redacted.
func (getter *S3Getter) GetObject(
	ctx context.Context,
	bucket string,
	key string,
) (Object, error) {
	if !validBucket(bucket) || key == "" || strings.HasPrefix(key, "/") {
		return Object{}, run.ErrValidation
	}
	if err := ctx.Err(); err != nil {
		return Object{}, err
	}

	output, err := getter.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return Object{}, classifyS3Error(ctx, err)
	}
	if output == nil || output.Body == nil {
		return Object{}, run.ErrUnavailable
	}
	if output.ContentLength == nil || output.ContentType == nil {
		return Object{}, errors.Join(run.ErrUnavailable, output.Body.Close())
	}

	return Object{
		Body:      output.Body,
		SizeBytes: aws.ToInt64(output.ContentLength),
		MediaType: aws.ToString(output.ContentType),
	}, nil
}

func classifyS3Error(ctx context.Context, err error) error {
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}

	var noSuchKey *types.NoSuchKey
	var notFound *types.NotFound
	if errors.As(err, &noSuchKey) || errors.As(err, &notFound) {
		return ErrObjectNotFound
	}

	var networkError net.Error
	if errors.As(err, &networkError) {
		return run.ErrUnavailable
	}

	var apiError smithy.APIError
	if errors.As(err, &apiError) {
		code := safeS3ErrorCode(apiError.ErrorCode())
		if apiError.ErrorFault() == smithy.FaultServer || transientS3Code(code) {
			return run.ErrUnavailable
		}

		return &s3Error{code: code}
	}

	return &s3Error{}
}

func transientS3Code(code string) bool {
	switch code {
	case "InternalError", "RequestTimeout", "ServiceUnavailable", "SlowDown", "Throttling":
		return true
	default:
		return false
	}
}

func safeS3ErrorCode(code string) string {
	if len(code) == 0 || len(code) > 64 {
		return ""
	}
	for _, character := range code {
		if (character < 'a' || character > 'z') &&
			(character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') &&
			character != '.' && character != '_' && character != '-' {
			return ""
		}
	}

	return code
}

type s3Error struct{ code string }

func (err *s3Error) Error() string {
	if err.code == "" {
		return "S3 GetObject failed"
	}

	return fmt.Sprintf("S3 GetObject failed (%s)", err.code)
}
