// Package rawresult parses and validates raw-result/v1 manifests.
package rawresult

import (
	"encoding/json"
	"regexp"
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
	timestampPattern = regexp.MustCompile(
		`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:[0-5]\d(?:\.\d{1,6})?(?:Z|[+-]\d{2}:\d{2})$`,
	)
)

// Manifest is the transport shape of a raw-result/v1 envelope. It represents
// producer claims that still require approval and remote byte verification.
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

// Producer identifies the tool and immutable image that emitted raw evidence.
type Producer struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Image   string `json:"image"`
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

// Validate checks raw-result/v1 structure without approving semantic provenance
// or asserting that referenced remote bytes exist.
func (manifest Manifest) Validate(expectedRunID, expectedContractsVersion string) error {
	if manifest.SchemaVersion != 1 || manifest.Kind != "RawResult" ||
		manifest.ContractsVersion != expectedContractsVersion || manifest.RunID != expectedRunID ||
		!ValidContractsVersion(manifest.ContractsVersion) || !contract.ValidID(manifest.RunID) ||
		!ValidResourceID(manifest.TestID) ||
		manifest.Workload.Validate() != nil || manifest.Producer.Validate() != nil ||
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
		!imagePattern.MatchString(producer.Image) {
		return run.ErrValidation
	}

	return nil
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
	if _, valid = jsondocument.ExactObject(root["producer"], "name", "version", "image"); !valid {
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

func validTimestamp(value string) (time.Time, bool) {
	if !timestampPattern.MatchString(value) {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)

	return parsed, err == nil
}
