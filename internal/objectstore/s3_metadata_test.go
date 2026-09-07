package objectstore

import (
	"context"
	"errors"
	"net"
	"reflect"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

type headObjectAPIFunc func(
	context.Context,
	*s3.HeadObjectInput,
	...func(*s3.Options),
) (*s3.HeadObjectOutput, error)

func (head headObjectAPIFunc) HeadObject(
	ctx context.Context,
	input *s3.HeadObjectInput,
	options ...func(*s3.Options),
) (*s3.HeadObjectOutput, error) {
	return head(ctx, input, options...)
}

func TestS3MetadataInspectorAdaptsHeadObjectResponse(t *testing.T) {
	values := map[string]string{"artifact-id": "one", "sha256": "two", "format": "raw-result/v2"}
	client := headObjectAPIFunc(func(
		ctx context.Context,
		input *s3.HeadObjectInput,
		options ...func(*s3.Options),
	) (*s3.HeadObjectOutput, error) {
		if ctx.Err() != nil || aws.ToString(input.Bucket) != "perfeng-artifacts" ||
			aws.ToString(input.Key) != "runs/example/raw-result.json" || len(options) != 0 {
			t.Fatalf("input = %+v; options = %d", input, len(options))
		}

		return &s3.HeadObjectOutput{
			ContentLength: aws.Int64(512), ContentType: aws.String("application/json"),
			Metadata: values,
		}, nil
	})
	inspector, err := NewS3MetadataInspector(client)
	if err != nil {
		t.Fatal(err)
	}

	metadata, err := inspector.Inspect(
		context.Background(), "perfeng-artifacts", "runs/example/raw-result.json",
	)
	if err != nil || metadata.SizeBytes != 512 || metadata.MediaType != "application/json" ||
		!reflect.DeepEqual(metadata.Values, values) {
		t.Fatalf("Inspect() = %#v, %v", metadata, err)
	}
	metadata.Values["format"] = "changed"
	if values["format"] != "raw-result/v2" {
		t.Fatal("Inspect() returned storage-owned metadata")
	}
}

func TestS3MetadataInspectorClassifiesErrors(t *testing.T) {
	for name, test := range map[string]struct {
		err  error
		want error
	}{
		"missing": {&types.NotFound{}, ErrObjectNotFound},
		"timeout": {metadataTimeoutError{}, run.ErrUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			inspector, err := NewS3MetadataInspector(headObjectAPIFunc(func(
				context.Context, *s3.HeadObjectInput, ...func(*s3.Options),
			) (*s3.HeadObjectOutput, error) {
				return nil, test.err
			}))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := inspector.Inspect(
				context.Background(), "perfeng-artifacts", "runs/example/raw-result.json",
			); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestS3MetadataInspectorValidatesInputAndResponse(t *testing.T) {
	calls := 0
	client := headObjectAPIFunc(func(
		context.Context, *s3.HeadObjectInput, ...func(*s3.Options),
	) (*s3.HeadObjectOutput, error) {
		calls++
		return nil, nil
	})
	inspector, err := NewS3MetadataInspector(client)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []struct{ bucket, key string }{
		{"", "runs/example/raw-result.json"},
		{"Invalid", "runs/example/raw-result.json"},
		{"perfeng-artifacts", ""},
		{"perfeng-artifacts", "/runs/example/raw-result.json"},
	} {
		if _, err := inspector.Inspect(
			context.Background(), input.bucket, input.key,
		); !errors.Is(err, run.ErrValidation) {
			t.Fatalf("input %+v error = %v", input, err)
		}
	}
	if calls != 0 {
		t.Fatal("invalid input reached S3")
	}

	for name, output := range map[string]*s3.HeadObjectOutput{
		"nil response": nil,
		"nil length":   {ContentType: aws.String("application/json")},
		"nil type":     {ContentLength: aws.Int64(1)},
		"negative size": {
			ContentLength: aws.Int64(-1), ContentType: aws.String("application/json"),
		},
		"empty type": {ContentLength: aws.Int64(1), ContentType: aws.String("")},
	} {
		t.Run(name, func(t *testing.T) {
			inspector, err := NewS3MetadataInspector(headObjectAPIFunc(func(
				context.Context, *s3.HeadObjectInput, ...func(*s3.Options),
			) (*s3.HeadObjectOutput, error) {
				return output, nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := inspector.Inspect(
				context.Background(), "perfeng-artifacts", "runs/example/raw-result.json",
			); !errors.Is(err, run.ErrUnavailable) {
				t.Fatalf("error = %v", err)
			}
		})
	}

	if _, err := NewS3MetadataInspector(nil); !errors.Is(err, run.ErrValidation) {
		t.Fatalf("nil client error = %v", err)
	}
}

func TestS3MetadataInspectorPreservesCancellation(t *testing.T) {
	called := false
	inspector, err := NewS3MetadataInspector(headObjectAPIFunc(func(
		context.Context, *s3.HeadObjectInput, ...func(*s3.Options),
	) (*s3.HeadObjectOutput, error) {
		called = true
		return nil, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := inspector.Inspect(
		ctx, "perfeng-artifacts", "runs/example/raw-result.json",
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	if called {
		t.Fatal("cancelled inspection reached S3")
	}
}

type metadataTimeoutError struct{}

func (metadataTimeoutError) Error() string   { return "secret timeout detail" }
func (metadataTimeoutError) Timeout() bool   { return true }
func (metadataTimeoutError) Temporary() bool { return true }

var _ net.Error = metadataTimeoutError{}

func TestS3MetadataInspectorRedactsBackendDetails(t *testing.T) {
	inspector, err := NewS3MetadataInspector(headObjectAPIFunc(func(
		context.Context, *s3.HeadObjectInput, ...func(*s3.Options),
	) (*s3.HeadObjectOutput, error) {
		return nil, errors.New("https://access:secret@example.invalid/signed")
	}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = inspector.Inspect(
		context.Background(), "perfeng-artifacts", "runs/example/raw-result.json",
	)
	if err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "example.invalid") {
		t.Fatalf("unsafe error = %v", err)
	}
}
