// verify-image validates a local control-plane image and resolves its published digest.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

const dockerTimeout = 2 * time.Minute

var (
	imagePattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9./:@_-]*$`)
	repositoryPattern = regexp.MustCompile(`^ghcr\.io/[a-z0-9_-]+/perfeng-control-plane$`)
	digestPattern     = regexp.MustCompile(`^[^@]+@sha256:[0-9a-f]{64}$`)
)

type imageMetadata struct {
	OS           string   `json:"Os"`
	Architecture string   `json:"Architecture"`
	RepoDigests  []string `json:"RepoDigests"`
	Config       struct {
		User       string            `json:"User"`
		WorkingDir string            `json:"WorkingDir"`
		Entrypoint []string          `json:"Entrypoint"`
		Labels     map[string]string `json:"Labels"`
	} `json:"Config"`
}

func main() {
	if err := execute(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func execute(arguments []string) error {
	if len(arguments) < 2 {
		return errors.New("usage: verify-image verify IMAGE [REVISION] | reference IMAGE GHCR_REPOSITORY")
	}
	action, image := arguments[0], arguments[1]
	if err := validateImageName(image); err != nil {
		return err
	}

	switch {
	case action == "verify" && len(arguments) <= 3:
		revision := ""
		if len(arguments) == 3 {
			revision = arguments[2]
		}
		return verifyImage(image, revision)
	case action == "reference" && len(arguments) == 3:
		metadata, err := inspectImage(image)
		if err != nil {
			return err
		}
		reference, err := publishedReference(metadata.RepoDigests, arguments[2])
		if err != nil {
			return err
		}
		fmt.Println(reference)
		return nil
	default:
		return errors.New("usage: verify-image verify IMAGE [REVISION] | reference IMAGE GHCR_REPOSITORY")
	}
}

func validateImageName(image string) error {
	if strings.Contains(image, "://") || !imagePattern.MatchString(image) {
		return errors.New("expected a Docker image name, not command options or a URL")
	}
	return nil
}

func validateMetadata(metadata imageMetadata, revision string) error {
	if metadata.OS != "linux" || metadata.Architecture != "amd64" {
		return errors.New("expected a Linux AMD64 image")
	}
	if metadata.Config.User != "65532:65532" {
		return errors.New("expected the unprivileged control-plane runtime identity")
	}
	if metadata.Config.WorkingDir != "/var/empty" ||
		len(metadata.Config.Entrypoint) != 1 ||
		metadata.Config.Entrypoint[0] != "/usr/local/bin/control-plane" {
		return errors.New("unexpected control-plane image process configuration")
	}
	if metadata.Config.Labels["org.opencontainers.image.title"] != "perfeng-control-plane" {
		return errors.New("unexpected control-plane image title")
	}
	if revision != "" && metadata.Config.Labels["org.opencontainers.image.revision"] != revision {
		return errors.New("control-plane image revision does not match the source revision")
	}
	return nil
}

func publishedReference(repoDigests []string, repository string) (string, error) {
	if !repositoryPattern.MatchString(repository) {
		return "", errors.New("expected the control-plane GHCR repository")
	}
	prefix := repository + "@sha256:"
	matches := make([]string, 0, 1)
	for _, value := range repoDigests {
		if strings.HasPrefix(value, prefix) && digestPattern.MatchString(value) {
			matches = append(matches, value)
		}
	}
	if len(matches) != 1 {
		return "", errors.New("a unique published registry digest is required")
	}
	return matches[0], nil
}

func inspectImage(image string) (imageMetadata, error) {
	response, err := docker("image", "inspect", image)
	if err != nil {
		return imageMetadata{}, err
	}
	var documents []imageMetadata
	if err := json.Unmarshal(response, &documents); err != nil || len(documents) != 1 {
		return imageMetadata{}, errors.New("Docker returned unexpected image metadata")
	}
	return documents[0], nil
}

func verifyImage(image, revision string) error {
	metadata, err := inspectImage(image)
	if err != nil {
		return err
	}
	if err := validateMetadata(metadata, revision); err != nil {
		return err
	}
	_, err = docker(
		"run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL",
		"--security-opt", "no-new-privileges", "--entrypoint", "/usr/local/bin/validate-registry",
		image, "-h",
	)
	if err != nil {
		return err
	}
	fmt.Println("Verified image: non-root Linux AMD64 control plane with offline registry validation.")
	return nil
}

func docker(arguments ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dockerTimeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, "docker", arguments...).Output()
	if ctx.Err() != nil {
		return nil, errors.New("Docker command timed out")
	}
	if err != nil {
		return nil, errors.New("Docker command failed; inspect the local Docker environment")
	}
	return output, nil
}
