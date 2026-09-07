package app

import (
	"net/url"
	"os"
	"strings"
	"unicode"

	"github.com/stanimirivanov/perfeng-control-plane/internal/objectstore"
)

const (
	s3RegionVariable               = "PERFENG_S3_REGION"
	s3EndpointVariable             = "PERFENG_S3_ENDPOINT"
	s3PathStyleVariable            = "PERFENG_S3_PATH_STYLE"
	s3AllowHTTPVariable            = "PERFENG_S3_ALLOW_HTTP"
	s3MaximumAttemptsVariable      = "PERFENG_S3_MAX_ATTEMPTS"
	artifactBucketVariable         = "PERFENG_ARTIFACT_BUCKET"
	artifactRawFormatVariable      = "PERFENG_RAW_MANIFEST_FORMAT"
	artifactMaximumReadVariable    = "PERFENG_ARTIFACT_MAX_READ_BYTES"
	artifactStoragePolicyComponent = "artifact storage policy"
)

// S3ClientConfig defines endpoint, signing and retry settings for the AWS SDK
// client. Credentials remain in the SDK provider chain and are never loaded
// into this value.
type S3ClientConfig struct {
	Region       string
	Endpoint     string
	UsePathStyle bool
	AllowHTTP    bool
	MaxAttempts  int
}

// Validate rejects ambiguous regions, endpoint paths and accidental plaintext transport.
func (config S3ClientConfig) Validate() error {
	if config.Region == "" || strings.TrimSpace(config.Region) != config.Region ||
		strings.IndexFunc(config.Region, unicode.IsControl) >= 0 ||
		config.MaxAttempts < 1 || config.MaxAttempts > 10 {
		return ErrInvalidConfiguration
	}
	if config.Endpoint == "" {
		if config.AllowHTTP {
			return ErrInvalidConfiguration
		}

		return nil
	}
	if strings.TrimSpace(config.Endpoint) != config.Endpoint {
		return ErrInvalidConfiguration
	}
	endpoint, err := url.Parse(config.Endpoint)
	if err != nil || endpoint.Opaque != "" || endpoint.Hostname() == "" ||
		endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" ||
		(endpoint.Path != "" && endpoint.Path != "/") {
		return ErrInvalidConfiguration
	}
	if endpoint.Scheme == "https" {
		return nil
	}
	if endpoint.Scheme == "http" && config.AllowHTTP {
		return nil
	}

	return ErrInvalidConfiguration
}

// ArtifactStorageConfig contains client settings and the shared artifact access policy.
type ArtifactStorageConfig struct {
	Client S3ClientConfig
	Access objectstore.S3ArtifactAccessConfig
}

// Validate checks both client settings and artifact access policy.
func (config ArtifactStorageConfig) Validate() error {
	if config.Client.Validate() != nil || config.Access.Validate() != nil {
		return ErrInvalidConfiguration
	}

	return nil
}

// LoadArtifactStorageConfig reads and validates artifact storage configuration
// from the process environment. Every policy value is explicit; credentials
// remain the responsibility of the AWS SDK provider chain.
func LoadArtifactStorageConfig() (ArtifactStorageConfig, error) {
	return loadArtifactStorageConfig(os.LookupEnv)
}

func loadArtifactStorageConfig(lookup lookupEnvironment) (ArtifactStorageConfig, error) {
	if lookup == nil {
		return ArtifactStorageConfig{}, configurationError("environment")
	}

	region, err := requiredValue(lookup, s3RegionVariable)
	if err != nil {
		return ArtifactStorageConfig{}, err
	}
	endpoint := optionalValue(lookup, s3EndpointVariable)
	pathStyle, err := requiredBoolean(lookup, s3PathStyleVariable)
	if err != nil {
		return ArtifactStorageConfig{}, err
	}
	allowHTTP, err := requiredBoolean(lookup, s3AllowHTTPVariable)
	if err != nil {
		return ArtifactStorageConfig{}, err
	}
	maximumAttempts, err := requiredBoundedInteger(lookup, s3MaximumAttemptsVariable, 1, 10)
	if err != nil {
		return ArtifactStorageConfig{}, err
	}
	bucket, err := requiredValue(lookup, artifactBucketVariable)
	if err != nil {
		return ArtifactStorageConfig{}, err
	}
	rawFormat, err := requiredValue(lookup, artifactRawFormatVariable)
	if err != nil {
		return ArtifactStorageConfig{}, err
	}
	maximumReadBytes, err := requiredPositiveInteger(lookup, artifactMaximumReadVariable)
	if err != nil {
		return ArtifactStorageConfig{}, err
	}

	config := ArtifactStorageConfig{
		Client: S3ClientConfig{
			Region:       region,
			Endpoint:     endpoint,
			UsePathStyle: pathStyle,
			AllowHTTP:    allowHTTP,
			MaxAttempts:  maximumAttempts,
		},
		Access: objectstore.S3ArtifactAccessConfig{
			Bucket:            bucket,
			RawManifestFormat: rawFormat,
			MaximumReadBytes:  maximumReadBytes,
		},
	}
	if config.Client.Validate() != nil {
		return ArtifactStorageConfig{}, configurationError(s3EndpointVariable)
	}
	if config.Access.Validate() != nil {
		return ArtifactStorageConfig{}, configurationError(artifactStoragePolicyComponent)
	}

	return config, nil
}
