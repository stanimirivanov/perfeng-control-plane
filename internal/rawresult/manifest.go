// Package rawresult parses and validates versioned raw-result manifests.
package rawresult

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/jsondocument"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

const maximumManifestBytes = 1 << 20

var (
	versionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	resourceID     = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)
	hashPattern    = regexp.MustCompile(`^[a-f0-9]{64}$`)
	imagePattern   = regexp.MustCompile(
		`^[a-z0-9][a-z0-9._:/-]*@sha256:[a-f0-9]{64}$`,
	)
	gitSHAPattern       = regexp.MustCompile(`^[a-f0-9]{40}$`)
	relativePathPattern = regexp.MustCompile(`^[A-Za-z0-9_./-]+$`)
	releaseURLPattern   = regexp.MustCompile(`^https://[^\s@?#]+$`)
	timestampPattern    = regexp.MustCompile(
		`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:[0-5]\d(?:\.\d{1,6})?(?:Z|[+-]\d{2}:\d{2})$`,
	)
)

// Manifest is the common transport shape of raw-result/v1 and raw-result/v2.
// Its producer claims still require approval and remote byte verification.
type Manifest struct {
	SchemaVersion     int            `json:"schemaVersion"`
	Kind              string         `json:"kind"`
	ContractsVersion  string         `json:"contractsVersion"`
	RunID             string         `json:"runId"`
	TestID            string         `json:"testId"`
	Workload          Identity       `json:"workload"`
	Producer          Producer       `json:"producer"`
	MeasurementWindow Window         `json:"measurementWindow"`
	CreatedAt         string         `json:"createdAt"`
	Artifacts         []run.Artifact `json:"artifacts"`
}

// Identity pins one published workload definition.
type Identity struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
}

// Producer identifies the tool and immutable execution artifact that emitted
// raw evidence. Image is used only by raw-result/v1; Artifact is used only by
// raw-result/v2.
type Producer struct {
	Name     string             `json:"name"`
	Version  string             `json:"version"`
	Image    string             `json:"image,omitempty"`
	Artifact *ExecutionArtifact `json:"artifact,omitempty"`
}

// Clone returns an independently owned producer identity.
func (producer Producer) Clone() Producer {
	if producer.Artifact != nil {
		artifact := *producer.Artifact
		if artifact.DependencyLock != nil {
			lock := *artifact.DependencyLock
			artifact.DependencyLock = &lock
		}
		producer.Artifact = &artifact
	}

	return producer
}

// Equal reports whether two producers identify the same execution bytes.
func (producer Producer) Equal(other Producer) bool {
	if producer.Name != other.Name || producer.Version != other.Version || producer.Image != other.Image {
		return false
	}
	if producer.Artifact == nil || other.Artifact == nil {
		return producer.Artifact == nil && other.Artifact == nil
	}

	return producer.Artifact.equal(*other.Artifact)
}

// ExecutionArtifact is the discriminated raw-result/v2 producer artifact.
type ExecutionArtifact struct {
	Kind             string          `json:"kind"`
	Image            string          `json:"image,omitempty"`
	Repository       string          `json:"repository,omitempty"`
	GitSHA           string          `json:"gitSha,omitempty"`
	DependencyLock   *DependencyLock `json:"dependencyLock,omitempty"`
	URL              string          `json:"url,omitempty"`
	ArchiveSHA256    string          `json:"archiveSha256,omitempty"`
	ExecutableSHA256 string          `json:"executableSha256,omitempty"`
}

// DependencyLock identifies the exact native dependency-lock bytes.
type DependencyLock struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

func (artifact ExecutionArtifact) equal(other ExecutionArtifact) bool {
	if artifact.Kind != other.Kind || artifact.Image != other.Image ||
		artifact.Repository != other.Repository || artifact.GitSHA != other.GitSHA ||
		artifact.URL != other.URL || artifact.ArchiveSHA256 != other.ArchiveSHA256 ||
		artifact.ExecutableSHA256 != other.ExecutableSHA256 {
		return false
	}
	if artifact.DependencyLock == nil || other.DependencyLock == nil {
		return artifact.DependencyLock == nil && other.DependencyLock == nil
	}

	return *artifact.DependencyLock == *other.DependencyLock
}

// Window is the producer-declared half-open measurement interval.
type Window struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

// Parse rejects malformed or unsupported envelopes and returns an isolated
// manifest only when its structure matches the expected Run and contract bundle.
func Parse(data []byte, expectedRunID, expectedContractsVersion string) (Manifest, error) {
	if !jsondocument.Valid(data, maximumManifestBytes) || !contract.ValidID(expectedRunID) ||
		!ValidContractsVersion(expectedContractsVersion) || !hasExactFields(data) {
		return Manifest{}, run.ErrValidation
	}

	var manifest Manifest
	if !jsondocument.Decode(data, &manifest) || manifest.Validate(expectedRunID, expectedContractsVersion) != nil {
		return Manifest{}, run.ErrValidation
	}

	return manifest, nil
}

// Validate checks a supported raw-result structure without approving semantic
// provenance or asserting that referenced remote bytes exist.
func (manifest Manifest) Validate(expectedRunID, expectedContractsVersion string) error {
	if manifest.Kind != "RawResult" ||
		manifest.ContractsVersion != expectedContractsVersion || manifest.RunID != expectedRunID ||
		!ValidContractsVersion(manifest.ContractsVersion) || !contract.ValidID(manifest.RunID) ||
		!ValidResourceID(manifest.TestID) ||
		manifest.Workload.Validate() != nil || !manifest.validProducer() ||
		len(manifest.Artifacts) == 0 {
		return run.ErrValidation
	}

	start, validStart := validTimestamp(manifest.MeasurementWindow.Start)
	end, validEnd := validTimestamp(manifest.MeasurementWindow.End)
	created, validCreated := validTimestamp(manifest.CreatedAt)
	if !validStart || !validEnd || !validCreated || !start.Before(end) || created.Before(end) {
		return run.ErrValidation
	}

	ids := make(map[string]struct{}, len(manifest.Artifacts))
	locations := make(map[string]struct{}, len(manifest.Artifacts))
	for _, artifact := range manifest.Artifacts {
		if artifact.RunID != manifest.RunID || artifact.Kind != "raw" || artifact.Validate() != nil {
			return run.ErrValidation
		}
		if _, exists := ids[artifact.ID]; exists {
			return run.ErrValidation
		}
		if _, exists := locations[artifact.URI]; exists {
			return run.ErrValidation
		}
		ids[artifact.ID] = struct{}{}
		locations[artifact.URI] = struct{}{}
	}

	return nil
}

func (manifest Manifest) validProducer() bool {
	switch manifest.SchemaVersion {
	case 1:
		return manifest.Producer.Validate() == nil
	case 2:
		return manifest.Producer.ValidateExecution() == nil
	default:
		return false
	}
}

// ValidContractsVersion reports whether a bundle version has the exact numeric
// three-part form required by the transport contracts.
func ValidContractsVersion(version string) bool {
	return versionPattern.MatchString(version)
}

// ValidResourceID reports whether value has the shared contract identifier shape.
func ValidResourceID(value string) bool {
	return resourceID.MatchString(value)
}

// Validate checks the shared immutable resource identity shape.
func (identity Identity) Validate() error {
	if !ValidResourceID(identity.ID) || !versionPattern.MatchString(identity.Version) ||
		!hashPattern.MatchString(identity.SHA256) {
		return run.ErrValidation
	}

	return nil
}

// Validate checks the shared producer identity and digest-pinned image shape.
func (producer Producer) Validate() error {
	if !ValidResourceID(producer.Name) || !versionPattern.MatchString(producer.Version) ||
		!ValidImage(producer.Image) || producer.Artifact != nil {
		return run.ErrValidation
	}

	return nil
}

// ValidateExecution checks a raw-result/v2 producer and its discriminated
// execution artifact.
func (producer Producer) ValidateExecution() error {
	if !ValidResourceID(producer.Name) || !versionPattern.MatchString(producer.Version) ||
		producer.Image != "" || producer.Artifact == nil || producer.Artifact.Validate() != nil {
		return run.ErrValidation
	}

	return nil
}

// Validate checks that an execution artifact contains exactly the fields for
// its declared kind.
func (artifact ExecutionArtifact) Validate() error {
	switch artifact.Kind {
	case "oci-image":
		if !ValidImage(artifact.Image) || artifact.Repository != "" || artifact.GitSHA != "" ||
			artifact.DependencyLock != nil || artifact.hasReleaseArchiveFields() {
			return run.ErrValidation
		}
	case "source-checkout":
		if artifact.Image != "" || !validRepository(artifact.Repository) ||
			!gitSHAPattern.MatchString(artifact.GitSHA) || artifact.DependencyLock == nil ||
			artifact.DependencyLock.Validate() != nil || artifact.hasReleaseArchiveFields() {
			return run.ErrValidation
		}
	case "release-archive":
		if artifact.Image != "" || artifact.Repository != "" || artifact.GitSHA != "" ||
			artifact.DependencyLock != nil || !validReleaseURL(artifact.URL) ||
			!hashPattern.MatchString(artifact.ArchiveSHA256) ||
			!hashPattern.MatchString(artifact.ExecutableSHA256) {
			return run.ErrValidation
		}
	default:
		return run.ErrValidation
	}

	return nil
}

func (artifact ExecutionArtifact) hasReleaseArchiveFields() bool {
	return artifact.URL != "" || artifact.ArchiveSHA256 != "" || artifact.ExecutableSHA256 != ""
}

// Validate checks a native dependency-lock identity.
func (lock DependencyLock) Validate() error {
	if !validRelativePath(lock.Path) || !hashPattern.MatchString(lock.SHA256) {
		return run.ErrValidation
	}

	return nil
}

// ValidImage reports whether value is an immutable digest-pinned image reference.
func ValidImage(value string) bool {
	return imagePattern.MatchString(value)
}

// ValidTimestamp reports whether value is a timestamp accepted by the contracts.
func ValidTimestamp(value string) bool {
	_, valid := validTimestamp(value)

	return valid
}

func hasExactFields(data []byte) bool {
	root, valid := jsondocument.ExactObject(data,
		"schemaVersion", "kind", "contractsVersion", "runId", "testId", "workload",
		"producer", "measurementWindow", "createdAt", "artifacts")
	if !valid {
		return false
	}

	if _, valid = jsondocument.ExactObject(root["workload"], "id", "version", "sha256"); !valid {
		return false
	}
	if !validProducerShape(root["producer"]) {
		return false
	}
	if _, valid = jsondocument.ExactObject(root["measurementWindow"], "start", "end"); !valid {
		return false
	}

	var artifacts []json.RawMessage
	if json.Unmarshal(root["artifacts"], &artifacts) != nil {
		return false
	}
	for _, artifact := range artifacts {
		if _, valid = jsondocument.ExactObject(
			artifact, "id", "runId", "kind", "uri", "sha256", "sizeBytes", "mediaType", "format",
		); !valid {
			return false
		}
	}

	return true
}

func validProducerShape(data json.RawMessage) bool {
	if _, valid := jsondocument.ExactObject(data, "name", "version", "image"); valid {
		return true
	}
	producer, valid := jsondocument.ExactObject(data, "name", "version", "artifact")
	if !valid {
		return false
	}

	var discriminator struct {
		Kind string `json:"kind"`
	}
	if json.Unmarshal(producer["artifact"], &discriminator) != nil {
		return false
	}
	switch discriminator.Kind {
	case "oci-image":
		_, valid = jsondocument.ExactObject(producer["artifact"], "kind", "image")
	case "source-checkout":
		var artifact map[string]json.RawMessage
		artifact, valid = jsondocument.ExactObject(
			producer["artifact"], "kind", "repository", "gitSha", "dependencyLock",
		)
		if valid {
			_, valid = jsondocument.ExactObject(artifact["dependencyLock"], "path", "sha256")
		}
	case "release-archive":
		_, valid = jsondocument.ExactObject(
			producer["artifact"], "kind", "url", "archiveSha256", "executableSha256",
		)
	default:
		return false
	}

	return valid
}

func validRepository(value string) bool {
	parsed, err := url.ParseRequestURI(value)
	return err == nil && parsed.Scheme == "https" && parsed.Host != ""
}

func validReleaseURL(value string) bool {
	if !releaseURLPattern.MatchString(value) {
		return false
	}
	parsed, err := url.ParseRequestURI(value)

	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil &&
		parsed.RawQuery == "" && parsed.Fragment == ""
}

func validRelativePath(value string) bool {
	if strings.HasPrefix(value, "/") || !relativePathPattern.MatchString(value) {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == "." || part == ".." {
			return false
		}
	}

	return true
}

func validTimestamp(value string) (time.Time, bool) {
	if !timestampPattern.MatchString(value) {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)

	return parsed, err == nil
}
