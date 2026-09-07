// Package app owns executable configuration and dependency composition.
package app

import (
	"errors"
	"net/url"
	"os"
	"strconv"
	"strings"
	"unicode"

	"github.com/stanimirivanov/perfeng-control-plane/internal/objectstore"
)

const (
	s3RegionVariable               = "PERFENG_S3_REGION"
	s3EndpointVariable             = "PERFENG_S3_ENDPOINT"
	s3PathStyleVariable            = "PERFENG_S3_PATH_STYLE"
	s3AllowHTTPVariable            = "PERFENG_S3_ALLOW_HTTP"
	artifactBucketVariable         = "PERFENG_ARTIFACT_BUCKET"
	artifactRawFormatVariable      = "PERFENG_RAW_MANIFEST_FORMAT"
	artifactMaximumReadVariable    = "PERFENG_ARTIFACT_MAX_READ_BYTES"
	artifactStoragePolicyComponent = "artifact storage policy"
)

// ErrInvalidConfiguration classifies missing, malformed or contradictory
// executable configuration without exposing configured values.
var ErrInvalidConfiguration = errors.New("invalid runtime configuration")

// ConfigurationError identifies one invalid variable or configuration component.
type ConfigurationError struct {
	Name string
}

// Error returns only the public configuration name, never its value.
func (err *ConfigurationError) Error() string {
	return "invalid runtime configuration: " + err.Name
}

// Unwrap supports classification with ErrInvalidConfiguration.
func (err *ConfigurationError) Unwrap() error {
	return ErrInvalidConfiguration
}

// S3ClientConfig defines endpoint and signing settings for a future AWS SDK
// client. Credentials remain in the SDK provider chain and are never loaded
// into this value.
type S3ClientConfig struct {
	Region       string
	Endpoint     string
	UsePathStyle bool
	AllowHTTP    bool
}

// Validate rejects ambiguous regions, endpoint paths and accidental plaintext transport.
func (config S3ClientConfig) Validate() error {
	if config.Region == "" || strings.TrimSpace(config.Region) != config.Region ||
		strings.IndexFunc(config.Region, unicode.IsControl) >= 0 {
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

type lookupEnvironment func(string) (string, bool)

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

func requiredValue(lookup lookupEnvironment, name string) (string, error) {
	value, exists := lookup(name)
	if !exists || value == "" || strings.TrimSpace(value) != value ||
		strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return "", configurationError(name)
	}

	return value, nil
}

func optionalValue(lookup lookupEnvironment, name string) string {
	value, _ := lookup(name)

	return value
}

func requiredBoolean(lookup lookupEnvironment, name string) (bool, error) {
	value, err := requiredValue(lookup, name)
	if err != nil {
		return false, err
	}
	switch value {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, configurationError(name)
	}
}

func requiredPositiveInteger(lookup lookupEnvironment, name string) (int64, error) {
	value, err := requiredValue(lookup, name)
	if err != nil {
		return 0, err
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 1 {
		return 0, configurationError(name)
	}

	return parsed, nil
}

func configurationError(name string) error {
	return &ConfigurationError{Name: name}
}
