package app

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/objectstore"
)

func TestLoadArtifactStorageConfigReturnsExplicitPolicy(t *testing.T) {
	values := validArtifactStorageEnvironment()

	config, err := loadArtifactStorageConfig(mapLookup(values))
	if err != nil {
		t.Fatal(err)
	}
	want := ArtifactStorageConfig{
		Client: S3ClientConfig{
			Region:       "us-east-1",
			Endpoint:     "http://seaweedfs-s3.perf-storage.svc.cluster.local:9000",
			UsePathStyle: true,
			AllowHTTP:    true,
		},
		Access: objectstore.S3ArtifactAccessConfig{
			Bucket:            "perfeng-artifacts",
			RawManifestFormat: "raw-result/v2",
			MaximumReadBytes:  67108864,
		},
	}
	if !reflect.DeepEqual(config, want) {
		t.Fatalf("config = %#v", config)
	}
}

func TestLoadArtifactStorageConfigAcceptsDefaultAWSEndpoint(t *testing.T) {
	values := validArtifactStorageEnvironment()
	values[s3EndpointVariable] = ""
	values[s3AllowHTTPVariable] = "false"
	values[s3PathStyleVariable] = "false"

	config, err := loadArtifactStorageConfig(mapLookup(values))
	if err != nil {
		t.Fatal(err)
	}
	if config.Client.Endpoint != "" || config.Client.AllowHTTP || config.Client.UsePathStyle {
		t.Fatalf("client config = %#v", config.Client)
	}
}

func TestLoadArtifactStorageConfigRejectsInvalidEnvironment(t *testing.T) {
	tests := map[string]func(map[string]string){
		"missing region": func(values map[string]string) {
			delete(values, s3RegionVariable)
		},
		"padded region": func(values map[string]string) {
			values[s3RegionVariable] = " us-east-1"
		},
		"invalid endpoint": func(values map[string]string) {
			values[s3EndpointVariable] = "https://example.com/path"
		},
		"HTTP without consent": func(values map[string]string) {
			values[s3AllowHTTPVariable] = "false"
		},
		"invalid path style": func(values map[string]string) {
			values[s3PathStyleVariable] = "1"
		},
		"invalid HTTP flag": func(values map[string]string) {
			values[s3AllowHTTPVariable] = "TRUE"
		},
		"invalid bucket": func(values map[string]string) {
			values[artifactBucketVariable] = "Invalid"
		},
		"invalid raw format": func(values map[string]string) {
			values[artifactRawFormatVariable] = "raw-result/v3"
		},
		"zero read limit": func(values map[string]string) {
			values[artifactMaximumReadVariable] = "0"
		},
		"large read limit": func(values map[string]string) {
			values[artifactMaximumReadVariable] = "268435457"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			values := validArtifactStorageEnvironment()
			mutate(values)

			_, err := loadArtifactStorageConfig(mapLookup(values))
			if !errors.Is(err, ErrInvalidConfiguration) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestLoadArtifactStorageConfigDoesNotExposeValues(t *testing.T) {
	secret := "http://access-key:secret-key@example.invalid/path"
	values := validArtifactStorageEnvironment()
	values[s3EndpointVariable] = secret

	_, err := loadArtifactStorageConfig(mapLookup(values))
	if !errors.Is(err, ErrInvalidConfiguration) || strings.Contains(err.Error(), secret) ||
		strings.Contains(err.Error(), "access-key") || strings.Contains(err.Error(), "secret-key") {
		t.Fatalf("error = %v", err)
	}
}

func TestArtifactStorageConfigValidatesProgrammaticValues(t *testing.T) {
	valid := ArtifactStorageConfig{
		Client: S3ClientConfig{Region: "us-east-1"},
		Access: objectstore.S3ArtifactAccessConfig{
			Bucket:            "perfeng-artifacts",
			RawManifestFormat: "raw-result/v2",
			MaximumReadBytes:  1,
		},
	}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}

	invalid := valid
	invalid.Client.AllowHTTP = true
	if err := invalid.Validate(); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("error = %v", err)
	}
}

func TestS3ClientConfigValidatesEndpointPolicy(t *testing.T) {
	accepted := []S3ClientConfig{
		{Region: "us-east-1"},
		{Region: "us-east-1", Endpoint: "https://s3.example.com"},
		{Region: "us-east-1", Endpoint: "https://s3.example.com/", AllowHTTP: true},
		{Region: "us-east-1", Endpoint: "http://seaweedfs:9000", AllowHTTP: true},
	}
	for _, config := range accepted {
		if err := config.Validate(); err != nil {
			t.Fatalf("accepted config %#v: %v", config, err)
		}
	}

	rejected := []S3ClientConfig{
		{},
		{Region: " us-east-1"},
		{Region: "us-east-1", AllowHTTP: true},
		{Region: "us-east-1", Endpoint: "http://seaweedfs:9000"},
		{Region: "us-east-1", Endpoint: "ftp://s3.example.com"},
		{Region: "us-east-1", Endpoint: "https://user:secret@s3.example.com"},
		{Region: "us-east-1", Endpoint: "https://s3.example.com/path"},
		{Region: "us-east-1", Endpoint: "https://s3.example.com?mode=test"},
		{Region: "us-east-1", Endpoint: "https://s3.example.com#fragment"},
	}
	for _, config := range rejected {
		if err := config.Validate(); !errors.Is(err, ErrInvalidConfiguration) {
			t.Fatalf("rejected config %#v: %v", config, err)
		}
	}
}

func TestLoadArtifactStorageConfigRejectsNilEnvironment(t *testing.T) {
	if _, err := loadArtifactStorageConfig(nil); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("error = %v", err)
	}
}

func validArtifactStorageEnvironment() map[string]string {
	return map[string]string{
		s3RegionVariable:            "us-east-1",
		s3EndpointVariable:          "http://seaweedfs-s3.perf-storage.svc.cluster.local:9000",
		s3PathStyleVariable:         "true",
		s3AllowHTTPVariable:         "true",
		artifactBucketVariable:      "perfeng-artifacts",
		artifactRawFormatVariable:   "raw-result/v2",
		artifactMaximumReadVariable: "67108864",
	}
}

func mapLookup(values map[string]string) lookupEnvironment {
	return func(name string) (string, bool) {
		value, exists := values[name]

		return value, exists
	}
}
