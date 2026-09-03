package objectstore

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

type getObjectAPIFunc func(
	context.Context,
	*s3.GetObjectInput,
	...func(*s3.Options),
) (*s3.GetObjectOutput, error)

func (get getObjectAPIFunc) GetObject(
	ctx context.Context,
	input *s3.GetObjectInput,
	options ...func(*s3.Options),
) (*s3.GetObjectOutput, error) {
	return get(ctx, input, options...)
}

func TestS3GetterAdaptsGetObjectResponse(t *testing.T) {
	body := io.NopCloser(strings.NewReader("payload"))
	client := getObjectAPIFunc(func(
		ctx context.Context,
		input *s3.GetObjectInput,
		options ...func(*s3.Options),
	) (*s3.GetObjectOutput, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if aws.ToString(input.Bucket) != "perfeng-artifacts" ||
			aws.ToString(input.Key) != "runs/example/result.json" || len(options) != 0 {
			t.Fatalf("input = %+v; options=%d", input, len(options))
		}

		return &s3.GetObjectOutput{
			Body: body, ContentLength: aws.Int64(7), ContentType: aws.String("application/json"),
		}, nil
	})
	getter, err := NewS3Getter(client)
	if err != nil {
		t.Fatal(err)
	}

	object, err := getter.GetObject(
		context.Background(),
		"perfeng-artifacts",
		"runs/example/result.json",
	)
	if err != nil || object.Body != body || object.SizeBytes != 7 || object.MediaType != "application/json" {
		t.Fatalf("GetObject() = %+v, %v", object, err)
	}
	if err := object.Body.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestS3GetterClassifiesMissingAndTransientErrors(t *testing.T) {
	for name, test := range map[string]struct {
		err  error
		want error
	}{
		"no such key": {&types.NoSuchKey{}, ErrObjectNotFound},
		"not found":   {&types.NotFound{}, ErrObjectNotFound},
		"server":      {&smithy.GenericAPIError{Code: "BackendFailure", Fault: smithy.FaultServer}, run.ErrUnavailable},
		"slow down":   {&smithy.GenericAPIError{Code: "SlowDown", Fault: smithy.FaultClient}, run.ErrUnavailable},
		"timeout":     {timeoutError{}, run.ErrUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			getter, err := NewS3Getter(getObjectAPIFunc(func(
				context.Context,
				*s3.GetObjectInput,
				...func(*s3.Options),
			) (*s3.GetObjectOutput, error) {
				return nil, test.err
			}))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := getter.GetObject(context.Background(), "perfeng-artifacts", "runs/example/result.json"); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestS3GetterPreservesContextCancellation(t *testing.T) {
	clientCalled := false
	getter, err := NewS3Getter(getObjectAPIFunc(func(
		context.Context,
		*s3.GetObjectInput,
		...func(*s3.Options),
	) (*s3.GetObjectOutput, error) {
		clientCalled = true
		return nil, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := getter.GetObject(ctx, "perfeng-artifacts", "runs/example/result.json"); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	if clientCalled {
		t.Fatal("cancelled request reached S3")
	}

	getter, err = NewS3Getter(getObjectAPIFunc(func(
		context.Context,
		*s3.GetObjectInput,
		...func(*s3.Options),
	) (*s3.GetObjectOutput, error) {
		return nil, errors.Join(context.DeadlineExceeded, errors.New("secret request URL"))
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := getter.GetObject(context.Background(), "perfeng-artifacts", "runs/example/result.json"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline identity lost: %v", err)
	}
}

func TestS3GetterRedactsBackendDetails(t *testing.T) {
	for name, backendError := range map[string]error{
		"API":      &smithy.GenericAPIError{Code: "AccessDenied", Message: "credential=secret", Fault: smithy.FaultClient},
		"bad code": &smithy.GenericAPIError{Code: "secret value", Message: "credential=secret", Fault: smithy.FaultClient},
		"unknown":  errors.New("https://access:secret@example.invalid/signed"),
	} {
		t.Run(name, func(t *testing.T) {
			getter, err := NewS3Getter(getObjectAPIFunc(func(
				context.Context,
				*s3.GetObjectInput,
				...func(*s3.Options),
			) (*s3.GetObjectOutput, error) {
				return nil, backendError
			}))
			if err != nil {
				t.Fatal(err)
			}
			_, err = getter.GetObject(context.Background(), "perfeng-artifacts", "runs/example/result.json")
			if err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "example.invalid") {
				t.Fatalf("unsafe error = %v", err)
			}
		})
	}
}

func TestS3GetterRejectsInvalidInputAndResponse(t *testing.T) {
	calls := 0
	getter, err := NewS3Getter(getObjectAPIFunc(func(
		context.Context,
		*s3.GetObjectInput,
		...func(*s3.Options),
	) (*s3.GetObjectOutput, error) {
		calls++
		return nil, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []struct{ bucket, key string }{
		{"", "runs/example/result.json"},
		{"Invalid_Bucket", "runs/example/result.json"},
		{"perfeng-artifacts", ""},
		{"perfeng-artifacts", "/runs/example/result.json"},
	} {
		if _, err := getter.GetObject(context.Background(), input.bucket, input.key); !errors.Is(err, run.ErrValidation) {
			t.Fatalf("input %+v error = %v", input, err)
		}
	}
	if calls != 0 {
		t.Fatal("invalid input reached S3")
	}

	if _, err := getter.GetObject(context.Background(), "perfeng-artifacts", "runs/example/result.json"); !errors.Is(err, run.ErrUnavailable) {
		t.Fatalf("nil response error = %v", err)
	}
	for name, output := range map[string]*s3.GetObjectOutput{
		"nil body":         {},
		"nil length":       {Body: io.NopCloser(strings.NewReader("value")), ContentType: aws.String("text/plain")},
		"nil content type": {Body: io.NopCloser(strings.NewReader("value")), ContentLength: aws.Int64(5)},
	} {
		t.Run(name, func(t *testing.T) {
			getter, err := NewS3Getter(getObjectAPIFunc(func(
				context.Context,
				*s3.GetObjectInput,
				...func(*s3.Options),
			) (*s3.GetObjectOutput, error) {
				return output, nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := getter.GetObject(context.Background(), "perfeng-artifacts", "runs/example/result.json"); !errors.Is(err, run.ErrUnavailable) {
				t.Fatalf("response error = %v", err)
			}
		})
	}
	if _, err := NewS3Getter(nil); !errors.Is(err, run.ErrValidation) {
		t.Fatalf("nil client error = %v", err)
	}
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "secret timeout detail" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

var _ net.Error = timeoutError{}

func TestS3GetterResponseBodyWorksWithReader(t *testing.T) {
	content := []byte("verified")
	reference := artifactFor(content)
	client := getObjectAPIFunc(func(
		context.Context,
		*s3.GetObjectInput,
		...func(*s3.Options),
	) (*s3.GetObjectOutput, error) {
		return &s3.GetObjectOutput{
			Body:          io.NopCloser(bytes.NewReader(content)),
			ContentLength: aws.Int64(int64(len(content))),
			ContentType:   aws.String(reference.MediaType),
		}, nil
	})
	getter, err := NewS3Getter(client)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := NewReader(getter, "perfeng-artifacts", 1024)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := reader.Read(context.Background(), reference)
	if err != nil || !bytes.Equal(actual, content) {
		t.Fatalf("Read() = %q, %v", actual, err)
	}
}
