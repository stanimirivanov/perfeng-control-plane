package main

import (
	"strings"
	"testing"
)

func validMetadata() imageMetadata {
	metadata := imageMetadata{OS: "linux", Architecture: "amd64"}
	metadata.Config.User = "65532:65532"
	metadata.Config.WorkingDir = "/var/empty"
	metadata.Config.Entrypoint = []string{"/usr/local/bin/control-plane"}
	metadata.Config.Labels = map[string]string{
		"org.opencontainers.image.title":    "perfeng-control-plane",
		"org.opencontainers.image.revision": "abc123",
	}
	return metadata
}

func TestImageArgumentCannotBecomeDockerOptions(t *testing.T) {
	for _, image := range []string{"", "--privileged", "x;whoami", "https://example.com/image"} {
		t.Run(image, func(t *testing.T) {
			if validateImageName(image) == nil {
				t.Fatalf("validateImageName(%q) succeeded", image)
			}
		})
	}
	if err := validateImageName("ghcr.io/example/perfeng-control-plane@sha256:" + strings.Repeat("a", 64)); err != nil {
		t.Fatalf("validateImageName() error = %v", err)
	}
}

func TestRuntimeMetadataIsExact(t *testing.T) {
	metadata := validMetadata()
	if err := validateMetadata(metadata, "abc123"); err != nil {
		t.Fatalf("validateMetadata() error = %v", err)
	}

	tests := []struct {
		name   string
		change func(*imageMetadata)
	}{
		{name: "operating system", change: func(value *imageMetadata) { value.OS = "windows" }},
		{name: "architecture", change: func(value *imageMetadata) { value.Architecture = "arm64" }},
		{name: "user", change: func(value *imageMetadata) { value.Config.User = "0:0" }},
		{name: "working directory", change: func(value *imageMetadata) { value.Config.WorkingDir = "/tmp" }},
		{name: "entry point", change: func(value *imageMetadata) { value.Config.Entrypoint = []string{"sh"} }},
		{name: "title", change: func(value *imageMetadata) { value.Config.Labels["org.opencontainers.image.title"] = "wrong" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := validMetadata()
			test.change(&changed)
			if validateMetadata(changed, "abc123") == nil {
				t.Fatal("validateMetadata() succeeded")
			}
		})
	}
	if validateMetadata(metadata, "another-revision") == nil {
		t.Fatal("validateMetadata() accepted a different revision")
	}
}

func TestRegistryManifestDigestIsRequired(t *testing.T) {
	repository := "ghcr.io/example/perfeng-control-plane"
	reference := repository + "@sha256:" + strings.Repeat("a", 64)
	actual, err := publishedReference([]string{reference}, repository)
	if err != nil {
		t.Fatalf("publishedReference() error = %v", err)
	}
	if actual != reference {
		t.Fatalf("publishedReference() = %q, want %q", actual, reference)
	}

	for _, values := range [][]string{
		nil,
		{},
		{"sha256:" + strings.Repeat("a", 64)},
		{repository + ":latest"},
		{repository + "@sha256:bad"},
		{reference, reference},
	} {
		if _, err := publishedReference(values, repository); err == nil {
			t.Fatalf("publishedReference(%q) succeeded", values)
		}
	}
	if _, err := publishedReference([]string{reference}, "registry.example/perfeng-control-plane"); err == nil {
		t.Fatal("publishedReference() accepted a different registry")
	}
}
