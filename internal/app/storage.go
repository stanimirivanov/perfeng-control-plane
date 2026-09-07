package app

import (
	"context"
	"errors"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/stanimirivanov/perfeng-control-plane/internal/objectstore"
)

// ErrDependencyInitialization classifies a startup dependency failure without
// exposing provider responses, credential locations or configured endpoints.
var ErrDependencyInitialization = errors.New("runtime dependency initialization failed")

type loadAWSConfig func(
	context.Context,
	...func(*awsconfig.LoadOptions) error,
) (aws.Config, error)

type newS3Client func(aws.Config, ...func(*s3.Options)) objectstore.S3API

// NewArtifactStorage loads the standard AWS configuration and composes the S3
// artifact adapters. Loading config may inspect environment or shared files;
// no S3 request is made until an adapter operation is called.
func NewArtifactStorage(
	ctx context.Context,
	config ArtifactStorageConfig,
) (*objectstore.S3ArtifactAccess, error) {
	return newArtifactStorage(ctx, config, awsconfig.LoadDefaultConfig, s3ClientFromConfig)
}

func newArtifactStorage(
	ctx context.Context,
	config ArtifactStorageConfig,
	load loadAWSConfig,
	newClient newS3Client,
) (*objectstore.S3ArtifactAccess, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if config.Validate() != nil || load == nil || newClient == nil {
		return nil, ErrInvalidConfiguration
	}

	awsConfig, err := load(
		ctx,
		awsconfig.WithRegion(config.Client.Region),
		awsconfig.WithRetryMode(aws.RetryModeStandard),
		awsconfig.WithRetryMaxAttempts(config.Client.MaxAttempts),
	)
	if err != nil {
		return nil, classifyDependencyInitialization(ctx, err)
	}

	client := newClient(awsConfig, func(options *s3.Options) {
		options.UsePathStyle = config.Client.UsePathStyle
		if config.Client.Endpoint != "" {
			options.BaseEndpoint = aws.String(config.Client.Endpoint)
		}
	})
	if client == nil {
		return nil, ErrDependencyInitialization
	}
	access, err := objectstore.NewS3ArtifactAccess(client, config.Access)
	if err != nil {
		return nil, ErrInvalidConfiguration
	}

	return access, nil
}

func s3ClientFromConfig(
	config aws.Config,
	options ...func(*s3.Options),
) objectstore.S3API {
	return s3.NewFromConfig(config, options...)
}

func classifyDependencyInitialization(ctx context.Context, err error) error {
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}

	return ErrDependencyInitialization
}
