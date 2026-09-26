package objectstore

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsv4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/stanimirivanov/perfeng-control-plane/internal/nativeworker"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

type putObjectPresignAPIFunc func(
	context.Context,
	*s3.PutObjectInput,
	...func(*s3.PresignOptions),
) (*awsv4.PresignedHTTPRequest, error)

func (presign putObjectPresignAPIFunc) PresignPutObject(
	ctx context.Context,
	input *s3.PutObjectInput,
	options ...func(*s3.PresignOptions),
) (*awsv4.PresignedHTTPRequest, error) {
	return presign(ctx, input, options...)
}

func validPresignReservation() nativeworker.UploadReservation {
	issuedAt := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)

	return nativeworker.UploadReservation{
		GrantID:      "44444444-4444-4444-8444-444444444444",
		LeaseID:      "11111111-1111-4111-8111-111111111111",
		AttemptID:    "22222222-2222-4222-8222-222222222222",
		RunID:        "perf-20260926-120000-a1b2c3d4",
		Slot:         "measurements",
		ObjectKey:    "runs/perf-20260926-120000-a1b2c3d4/attempts/22222222-2222-4222-8222-222222222222/measurements",
		Size:         4096,
		MaximumBytes: 1 << 20,
		SHA256:       strings.Repeat("f", 64),
		MediaType:    "application/json",
		IssuedAt:     issuedAt,
		ExpiresAt:    issuedAt.Add(2 * time.Minute),
	}
}

func fakePresignedPut(
	reservation nativeworker.UploadReservation,
	signedAt time.Time,
	expires int,
) *awsv4.PresignedHTTPRequest {
	query := url.Values{
		"X-Amz-Algorithm":     {"AWS4-HMAC-SHA256"},
		"X-Amz-Credential":    {"EXAMPLE/20260926/us-east-1/s3/aws4_request"},
		"X-Amz-Date":          {signedAt.UTC().Format("20060102T150405Z")},
		"X-Amz-Expires":       {"109"},
		"X-Amz-Signature":     {strings.Repeat("0", 64)},
		"X-Amz-SignedHeaders": {"content-type;host;x-amz-checksum-sha256"},
		"x-id":                {"PutObject"},
	}
	query.Set("X-Amz-Expires", strconv.Itoa(expires))

	return &awsv4.PresignedHTTPRequest{
		URL: "https://objects.perfeng.invalid/perfeng-artifacts/" + reservation.ObjectKey +
			"?" + query.Encode(),
		Method: http.MethodPut,
		SignedHeader: http.Header{
			"Content-Type":          {reservation.MediaType},
			"Host":                  {"objects.perfeng.invalid"},
			"X-Amz-Checksum-Sha256": {"//////////////////////////////////////////8="},
		},
	}
}

func TestS3UploadPresignerBindsExactReservation(t *testing.T) {
	reservation := validPresignReservation()
	now := reservation.IssuedAt.Add(10 * time.Second)
	client := putObjectPresignAPIFunc(func(
		ctx context.Context,
		input *s3.PutObjectInput,
		options ...func(*s3.PresignOptions),
	) (*awsv4.PresignedHTTPRequest, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		configured := s3.PresignOptions{}
		for _, apply := range options {
			apply(&configured)
		}
		if aws.ToString(input.Bucket) != "perfeng-artifacts" ||
			aws.ToString(input.Key) != reservation.ObjectKey || input.Body != nil ||
			input.ContentLength != nil || aws.ToString(input.ContentType) != reservation.MediaType ||
			aws.ToString(input.ChecksumSHA256) != "//////////////////////////////////////////8=" ||
			configured.Expires != 109*time.Second {
			t.Fatalf("input = %#v, expiry = %s", input, configured.Expires)
		}

		return fakePresignedPut(reservation, now, 109), nil
	})
	presigner, err := newS3UploadPresigner(
		client,
		"perfeng-artifacts",
		func() time.Time { return now },
	)
	if err != nil {
		t.Fatal(err)
	}
	authorization, err := presigner.PresignNativeUpload(context.Background(), reservation)
	if err != nil || !authorization.Valid(reservation) ||
		authorization.ExpiresAt != now.Add(109*time.Second) ||
		authorization.Headers.ContentType != reservation.MediaType ||
		authorization.Headers.ChecksumSHA256 != "//////////////////////////////////////////8=" {
		t.Fatalf("authorization = %#v, error = %v", authorization, err)
	}
}

func TestS3UploadPresignerAcceptsVirtualHostedTarget(t *testing.T) {
	reservation := validPresignReservation()
	output := fakePresignedPut(reservation, reservation.IssuedAt, 109)
	output.URL = strings.Replace(
		output.URL,
		"objects.perfeng.invalid/perfeng-artifacts/",
		"perfeng-artifacts.objects.perfeng.invalid/",
		1,
	)
	output.SignedHeader.Set("Host", "perfeng-artifacts.objects.perfeng.invalid")
	presigner, err := newS3UploadPresigner(
		putObjectPresignAPIFunc(func(
			context.Context,
			*s3.PutObjectInput,
			...func(*s3.PresignOptions),
		) (*awsv4.PresignedHTTPRequest, error) {
			return output, nil
		}),
		"perfeng-artifacts",
		func() time.Time { return reservation.IssuedAt },
	)
	if err != nil {
		t.Fatal(err)
	}
	if authorization, err := presigner.PresignNativeUpload(
		context.Background(), reservation,
	); err != nil || !authorization.Valid(reservation) {
		t.Fatalf("authorization = %#v, error = %v", authorization, err)
	}
}

func TestS3UploadPresignerUsesAWSClientWithoutNetworkIO(t *testing.T) {
	now := time.Now().UTC()
	reservation := validPresignReservation()
	reservation.IssuedAt = now.Add(-time.Second)
	reservation.ExpiresAt = now.Add(2 * time.Minute)
	client := s3.NewFromConfig(aws.Config{
		Region: "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider(
			"EXAMPLE-NOT-A-REAL-ACCESS-KEY",
			"example-not-a-real-secret-key",
			"",
		),
	}, func(options *s3.Options) {
		options.BaseEndpoint = aws.String("https://objects.perfeng.invalid")
		options.UsePathStyle = true
	})
	presigner, err := NewS3UploadPresigner(
		s3.NewPresignClient(client),
		"perfeng-artifacts",
	)
	if err != nil {
		t.Fatal(err)
	}
	authorization, err := presigner.PresignNativeUpload(context.Background(), reservation)
	if err != nil || !authorization.Valid(reservation) {
		t.Fatalf("authorization = %#v, error = %v", authorization, err)
	}
	parsed, err := url.Parse(authorization.URL)
	if err != nil || parsed.Host != "objects.perfeng.invalid" ||
		parsed.Path != "/perfeng-artifacts/"+reservation.ObjectKey ||
		parsed.Query().Get("X-Amz-SignedHeaders") != "content-type;host;x-amz-checksum-sha256" {
		t.Fatalf("presigned URL shape is invalid: %v", err)
	}
}

func TestS3UploadPresignerRejectsInvalidExpiredAndCanceledRequests(t *testing.T) {
	reservation := validPresignReservation()
	calls := 0
	client := putObjectPresignAPIFunc(func(
		context.Context,
		*s3.PutObjectInput,
		...func(*s3.PresignOptions),
	) (*awsv4.PresignedHTTPRequest, error) {
		calls++

		return fakePresignedPut(reservation, reservation.IssuedAt, 60), nil
	})
	presigner, err := newS3UploadPresigner(
		client,
		"perfeng-artifacts",
		func() time.Time { return reservation.IssuedAt },
	)
	if err != nil {
		t.Fatal(err)
	}
	invalid := reservation
	invalid.ObjectKey = "worker-selected"
	if _, err := presigner.PresignNativeUpload(
		context.Background(), invalid,
	); !errors.Is(err, run.ErrValidation) {
		t.Fatalf("invalid reservation error = %v", err)
	}

	presigner.now = func() time.Time { return reservation.ExpiresAt }
	if _, err := presigner.PresignNativeUpload(
		context.Background(), reservation,
	); !errors.Is(err, nativeworker.ErrLeaseGone) {
		t.Fatalf("expired reservation error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	presigner.now = func() time.Time { return reservation.IssuedAt }
	if _, err := presigner.PresignNativeUpload(ctx, reservation); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled request error = %v", err)
	}
	if calls != 0 {
		t.Fatal("rejected request reached the AWS presigner")
	}
}

func TestS3UploadPresignerRedactsErrorsAndRejectsUnexpectedOutput(t *testing.T) {
	reservation := validPresignReservation()
	secret := "https://access:secret@objects.invalid/signed?signature=secret"
	presigner, err := newS3UploadPresigner(
		putObjectPresignAPIFunc(func(
			context.Context,
			*s3.PutObjectInput,
			...func(*s3.PresignOptions),
		) (*awsv4.PresignedHTTPRequest, error) {
			return nil, errors.New(secret)
		}),
		"perfeng-artifacts",
		func() time.Time { return reservation.IssuedAt },
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := presigner.PresignNativeUpload(
		context.Background(), reservation,
	); !errors.Is(err, run.ErrUnavailable) || strings.Contains(err.Error(), secret) {
		t.Fatalf("unsafe signing error = %v", err)
	}

	for name, mutate := range map[string]func(*awsv4.PresignedHTTPRequest){
		"method": func(output *awsv4.PresignedHTTPRequest) { output.Method = http.MethodPost },
		"HTTP": func(output *awsv4.PresignedHTTPRequest) {
			output.URL = strings.Replace(output.URL, "https://", "http://", 1)
		},
		"path": func(output *awsv4.PresignedHTTPRequest) {
			output.URL = strings.Replace(output.URL, reservation.ObjectKey, "runs/another", 1)
		},
		"prefixed path": func(output *awsv4.PresignedHTTPRequest) {
			output.URL = strings.Replace(
				output.URL,
				"/perfeng-artifacts/",
				"/untrusted/perfeng-artifacts/",
				1,
			)
		},
		"header": func(output *awsv4.PresignedHTTPRequest) { output.SignedHeader.Set("X-Extra", "value") },
		"expiry": func(output *awsv4.PresignedHTTPRequest) {
			output.URL = strings.Replace(output.URL, "X-Amz-Expires=109", "X-Amz-Expires=301", 1)
		},
		"operation": func(output *awsv4.PresignedHTTPRequest) {
			output.URL = strings.Replace(output.URL, "x-id=PutObject", "x-id=GetObject", 1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			output := fakePresignedPut(reservation, reservation.IssuedAt, 109)
			mutate(output)
			presigner.client = putObjectPresignAPIFunc(func(
				context.Context,
				*s3.PutObjectInput,
				...func(*s3.PresignOptions),
			) (*awsv4.PresignedHTTPRequest, error) {
				return output, nil
			})
			if _, err := presigner.PresignNativeUpload(
				context.Background(), reservation,
			); !errors.Is(err, run.ErrUnavailable) {
				t.Fatalf("unexpected output error = %v", err)
			}
		})
	}
}

func TestNewS3UploadPresignerRejectsInvalidDependencies(t *testing.T) {
	client := putObjectPresignAPIFunc(func(
		context.Context,
		*s3.PutObjectInput,
		...func(*s3.PresignOptions),
	) (*awsv4.PresignedHTTPRequest, error) {
		return nil, nil
	})
	for name, test := range map[string]struct {
		client S3PutObjectPresignAPI
		bucket string
		now    func() time.Time
	}{
		"client": {bucket: "perfeng-artifacts", now: time.Now},
		"bucket": {client: client, bucket: "Invalid_Bucket", now: time.Now},
		"clock":  {client: client, bucket: "perfeng-artifacts"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := newS3UploadPresigner(
				test.client, test.bucket, test.now,
			); !errors.Is(err, run.ErrValidation) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}
