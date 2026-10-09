package app

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/stanimirivanov/perfeng-control-plane/internal/nativeworker"
	"github.com/stanimirivanov/perfeng-control-plane/internal/objectstore"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func TestNewArtifactStorageAppliesReviewedClientPolicy(t *testing.T) {
	config := validArtifactStorageConfig()
	loadCalls := 0
	clientCalls := 0
	load := loadAWSConfig(func(
		ctx context.Context,
		options ...func(*awsconfig.LoadOptions) error,
	) (aws.Config, error) {
		loadCalls++
		if ctx.Err() != nil {
			t.Fatal(ctx.Err())
		}
		loaded := awsconfig.LoadOptions{}
		for _, apply := range options {
			if err := apply(&loaded); err != nil {
				t.Fatal(err)
			}
		}
		if loaded.Region != config.Client.Region ||
			loaded.RetryMode != aws.RetryModeStandard ||
			loaded.RetryMaxAttempts != config.Client.MaxAttempts {
			t.Fatalf("AWS load options = %#v", loaded)
		}

		return aws.Config{Region: loaded.Region}, nil
	})
	newClient := newS3Client(func(
		awsConfig aws.Config,
		options ...func(*s3.Options),
	) *s3.Client {
		clientCalls++
		configured := s3.Options{}
		for _, apply := range options {
			apply(&configured)
		}
		if awsConfig.Region != config.Client.Region ||
			aws.ToString(configured.BaseEndpoint) != config.Client.Endpoint ||
			configured.UsePathStyle != config.Client.UsePathStyle {
			t.Fatalf("S3 client config = %#v, %#v", awsConfig, configured)
		}

		return s3.NewFromConfig(awsConfig, options...)
	})

	resources, err := newArtifactStorage(context.Background(), config, load, newClient)
	if err != nil {
		t.Fatal(err)
	}
	if resources == nil || resources.Access == nil || resources.UploadSigner == nil ||
		loadCalls != 1 || clientCalls != 1 {
		t.Fatalf("resources = %v, load calls = %d, client calls = %d", resources, loadCalls, clientCalls)
	}
}

func TestNewArtifactStorageLeavesDefaultEndpointUnset(t *testing.T) {
	config := validArtifactStorageConfig()
	config.Client.Endpoint = ""
	config.Client.AllowHTTP = false
	config.Client.UsePathStyle = false

	newClient := newS3Client(func(
		awsConfig aws.Config,
		options ...func(*s3.Options),
	) *s3.Client {
		configured := s3.Options{}
		for _, apply := range options {
			apply(&configured)
		}
		if configured.BaseEndpoint != nil || configured.UsePathStyle {
			t.Fatalf("S3 client config = %#v", configured)
		}

		return s3.NewFromConfig(awsConfig, options...)
	})

	resources, err := newArtifactStorage(
		context.Background(),
		config,
		acceptAWSConfig,
		newClient,
	)
	if err != nil || resources == nil || resources.Access == nil || resources.UploadSigner == nil {
		t.Fatalf("NewArtifactStorage() = %v, %v", resources, err)
	}
}

func TestNewArtifactStorageSignsForWorkerEndpointWithoutChangingInternalAccess(t *testing.T) {
	config := validArtifactStorageConfig()
	config.Client.NativeUploadEndpoint = "https://objects.perfeng.invalid"
	var endpoints []string
	newClient := newS3Client(func(
		awsConfig aws.Config,
		options ...func(*s3.Options),
	) *s3.Client {
		configured := s3.Options{}
		for _, apply := range options {
			apply(&configured)
		}
		if !configured.UsePathStyle {
			t.Fatal("S3 path-style selection was lost")
		}
		endpoints = append(endpoints, aws.ToString(configured.BaseEndpoint))
		return s3.NewFromConfig(awsConfig, options...)
	})
	load := loadAWSConfig(func(
		_ context.Context,
		_ ...func(*awsconfig.LoadOptions) error,
	) (aws.Config, error) {
		return aws.Config{
			Region: config.Client.Region,
			Credentials: credentials.NewStaticCredentialsProvider(
				"EXAMPLE-NOT-A-REAL-ACCESS-KEY", "example-not-a-real-secret-key", "",
			),
		}, nil
	})

	resources, err := newArtifactStorage(context.Background(), config, load, newClient)
	if err != nil {
		t.Fatal(err)
	}
	if len(endpoints) != 2 || endpoints[0] != config.Client.Endpoint ||
		endpoints[1] != config.Client.NativeUploadEndpoint || resources.Access == nil {
		t.Fatalf("S3 client endpoints = %v", endpoints)
	}
	reservation := validNativeUploadReservation(time.Now().UTC())
	authorization, err := resources.UploadSigner.PresignNativeUpload(context.Background(), reservation)
	if err != nil {
		t.Fatalf("worker upload presigning failed: %v", err)
	}
	parsed, err := url.Parse(authorization.URL)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "objects.perfeng.invalid" ||
		parsed.Path != "/perfeng-artifacts/"+reservation.ObjectKey ||
		!authorization.Valid(reservation) {
		t.Fatal("worker upload authorization did not bind the reviewed HTTPS endpoint and key")
	}
}

func TestNewArtifactStorageKeepsInternalHTTPPresigningFailClosed(t *testing.T) {
	resources, err := newArtifactStorage(
		context.Background(), validArtifactStorageConfig(),
		func(context.Context, ...func(*awsconfig.LoadOptions) error) (aws.Config, error) {
			return aws.Config{
				Region: "us-east-1",
				Credentials: credentials.NewStaticCredentialsProvider(
					"EXAMPLE-NOT-A-REAL-ACCESS-KEY", "example-not-a-real-secret-key", "",
				),
			}, nil
		},
		acceptS3Client,
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = resources.UploadSigner.PresignNativeUpload(
		context.Background(), validNativeUploadReservation(time.Now().UTC()),
	)
	if !errors.Is(err, run.ErrUnavailable) {
		t.Fatalf("HTTP-derived native upload authorization error = %v", err)
	}
}

func TestNewArtifactStorageRejectsUnavailableWorkerEndpointClient(t *testing.T) {
	config := validArtifactStorageConfig()
	config.Client.NativeUploadEndpoint = "https://objects.perfeng.invalid"
	calls := 0
	newClient := newS3Client(func(awsConfig aws.Config, options ...func(*s3.Options)) *s3.Client {
		calls++
		if calls == 2 {
			return nil
		}
		return s3.NewFromConfig(awsConfig, options...)
	})
	_, err := newArtifactStorage(context.Background(), config, acceptAWSConfig, newClient)
	if !errors.Is(err, ErrDependencyInitialization) || calls != 2 {
		t.Fatalf("error = %v, client calls = %d", err, calls)
	}
}

func TestNewArtifactStorageRejectsDependenciesAndInvalidPolicy(t *testing.T) {
	valid := validArtifactStorageConfig()
	invalid := valid
	invalid.Client.MaxAttempts = 0

	tests := map[string]struct {
		config    ArtifactStorageConfig
		load      loadAWSConfig
		newClient newS3Client
		want      error
	}{
		"configuration": {
			config: invalid, load: acceptAWSConfig, newClient: acceptS3Client,
			want: ErrInvalidConfiguration,
		},
		"loader": {
			config: valid, newClient: acceptS3Client, want: ErrInvalidConfiguration,
		},
		"client factory": {
			config: valid, load: acceptAWSConfig, want: ErrInvalidConfiguration,
		},
		"nil client": {
			config: valid,
			load:   acceptAWSConfig,
			newClient: func(aws.Config, ...func(*s3.Options)) *s3.Client {
				return nil
			},
			want: ErrDependencyInitialization,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := newArtifactStorage(
				context.Background(), test.config, test.load, test.newClient,
			)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestNewArtifactStoragePreservesCancellation(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	loadCalled := false
	load := loadAWSConfig(func(
		context.Context,
		...func(*awsconfig.LoadOptions) error,
	) (aws.Config, error) {
		loadCalled = true

		return aws.Config{}, nil
	})

	_, err := newArtifactStorage(canceled, validArtifactStorageConfig(), load, acceptS3Client)
	if !errors.Is(err, context.Canceled) || loadCalled {
		t.Fatalf("error = %v, loader called = %t", err, loadCalled)
	}

	for name, test := range map[string]struct {
		loadErr error
		want    error
	}{
		"canceled": {
			loadErr: errors.Join(context.Canceled, errors.New("provider detail")),
			want:    context.Canceled,
		},
		"deadline": {
			loadErr: errors.Join(context.DeadlineExceeded, errors.New("provider detail")),
			want:    context.DeadlineExceeded,
		},
	} {
		t.Run(name, func(t *testing.T) {
			load := loadAWSConfig(func(
				context.Context,
				...func(*awsconfig.LoadOptions) error,
			) (aws.Config, error) {
				return aws.Config{}, test.loadErr
			})
			_, err := newArtifactStorage(
				context.Background(), validArtifactStorageConfig(), load, acceptS3Client,
			)
			if !errors.Is(err, test.want) || strings.Contains(err.Error(), "provider detail") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestNewArtifactStorageRedactsAWSConfigurationErrors(t *testing.T) {
	load := loadAWSConfig(func(
		context.Context,
		...func(*awsconfig.LoadOptions) error,
	) (aws.Config, error) {
		return aws.Config{}, errors.New("credential file C:/secrets/tenant-access-key")
	})

	_, err := newArtifactStorage(
		context.Background(), validArtifactStorageConfig(), load, acceptS3Client,
	)
	if !errors.Is(err, ErrDependencyInitialization) ||
		strings.Contains(err.Error(), "tenant-access-key") || strings.Contains(err.Error(), "C:/secrets") {
		t.Fatalf("error = %v", err)
	}
}

func validArtifactStorageConfig() ArtifactStorageConfig {
	return ArtifactStorageConfig{
		Client: S3ClientConfig{
			Region:       "us-east-1",
			Endpoint:     "http://seaweedfs:9000",
			UsePathStyle: true,
			AllowHTTP:    true,
			MaxAttempts:  3,
		},
		Access: objectstore.S3ArtifactAccessConfig{
			Bucket:            "perfeng-artifacts",
			RawManifestFormat: "raw-result/v2",
			MaximumReadBytes:  1024,
		},
	}
}

func validNativeUploadReservation(now time.Time) nativeworker.UploadReservation {
	return nativeworker.UploadReservation{
		GrantID:   "44444444-4444-4444-8444-444444444444",
		LeaseID:   "11111111-1111-4111-8111-111111111111",
		AttemptID: "22222222-2222-4222-8222-222222222222",
		RunID:     "perf-20260926-120000-a1b2c3d4",
		Slot:      "measurements",
		ObjectKey: "runs/perf-20260926-120000-a1b2c3d4/attempts/22222222-2222-4222-8222-222222222222/measurements",
		Size:      4096, MaximumBytes: 1 << 20, SHA256: strings.Repeat("f", 64),
		MediaType: "application/json", IssuedAt: now.Add(-time.Second),
		ExpiresAt: now.Add(2 * time.Minute),
	}
}

func acceptAWSConfig(
	_ context.Context,
	options ...func(*awsconfig.LoadOptions) error,
) (aws.Config, error) {
	loaded := awsconfig.LoadOptions{}
	for _, apply := range options {
		if err := apply(&loaded); err != nil {
			return aws.Config{}, err
		}
	}

	return aws.Config{Region: loaded.Region}, nil
}

func acceptS3Client(
	config aws.Config,
	options ...func(*s3.Options),
) *s3.Client {
	return s3.NewFromConfig(config, options...)
}
