package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/stanimirivanov/perfeng-control-plane/internal/objectstore"
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
	) objectstore.S3API {
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

	access, err := newArtifactStorage(context.Background(), config, load, newClient)
	if err != nil {
		t.Fatal(err)
	}
	if access == nil || loadCalls != 1 || clientCalls != 1 {
		t.Fatalf("access = %v, load calls = %d, client calls = %d", access, loadCalls, clientCalls)
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
	) objectstore.S3API {
		configured := s3.Options{}
		for _, apply := range options {
			apply(&configured)
		}
		if configured.BaseEndpoint != nil || configured.UsePathStyle {
			t.Fatalf("S3 client config = %#v", configured)
		}

		return s3.NewFromConfig(awsConfig, options...)
	})

	access, err := newArtifactStorage(
		context.Background(),
		config,
		acceptAWSConfig,
		newClient,
	)
	if err != nil || access == nil {
		t.Fatalf("NewArtifactStorage() = %v, %v", access, err)
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
			newClient: func(aws.Config, ...func(*s3.Options)) objectstore.S3API {
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
) objectstore.S3API {
	return s3.NewFromConfig(config, options...)
}
