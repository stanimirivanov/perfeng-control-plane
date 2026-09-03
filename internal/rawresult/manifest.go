// Package rawresult parses and validates raw-result/v1 manifests.
package rawresult

import (
	"bytes"
	"encoding/json"
	"io"
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
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
	if len(data) == 0 || len(data) > maximumManifestBytes || !utf8.Valid(data) ||
		!contract.ValidID(expectedRunID) || !versionPattern.MatchString(expectedContractsVersion) ||
		uniqueJSON(data) != nil || !hasExactFields(data) {
		return Manifest{}, run.ErrValidation
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, run.ErrValidation
	}
	if err := requireJSONEnd(decoder); err != nil ||
		manifest.Validate(expectedRunID, expectedContractsVersion) != nil {
		return Manifest{}, run.ErrValidation
	}

	return manifest, nil
}

// Validate checks raw-result/v1 structure without approving semantic provenance
// or asserting that referenced remote bytes exist.
func (manifest Manifest) Validate(expectedRunID, expectedContractsVersion string) error {
	if manifest.SchemaVersion != 1 || manifest.Kind != "RawResult" ||
		manifest.ContractsVersion != expectedContractsVersion || manifest.RunID != expectedRunID ||
		!versionPattern.MatchString(manifest.ContractsVersion) || !contract.ValidID(manifest.RunID) ||
		!resourceID.MatchString(manifest.TestID) ||
		!validIdentity(manifest.Workload) || !validProducer(manifest.Producer) ||
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

func validIdentity(identity Identity) bool {
	return resourceID.MatchString(identity.ID) && versionPattern.MatchString(identity.Version) &&
		hashPattern.MatchString(identity.SHA256)
}

func validProducer(producer Producer) bool {
	return resourceID.MatchString(producer.Name) && versionPattern.MatchString(producer.Version) &&
		imagePattern.MatchString(producer.Image)
}

func hasExactFields(data []byte) bool {
	var root map[string]json.RawMessage
	if json.Unmarshal(data, &root) != nil || !exactKeys(root,
		"schemaVersion", "kind", "contractsVersion", "runId", "testId", "workload",
		"producer", "measurementWindow", "createdAt", "artifacts") {
		return false
	}

	if !exactObject(root["workload"], "id", "version", "sha256") ||
		!exactObject(root["producer"], "name", "version", "image") ||
		!exactObject(root["measurementWindow"], "start", "end") {
		return false
	}

	var artifacts []json.RawMessage
	if json.Unmarshal(root["artifacts"], &artifacts) != nil {
		return false
	}
	for _, artifact := range artifacts {
		if !exactObject(artifact, "id", "runId", "kind", "uri", "sha256", "sizeBytes", "mediaType", "format") {
			return false
		}
	}

	return true
}

func exactObject(data json.RawMessage, keys ...string) bool {
	var object map[string]json.RawMessage
	return json.Unmarshal(data, &object) == nil && exactKeys(object, keys...)
}

func exactKeys(object map[string]json.RawMessage, keys ...string) bool {
	if len(object) != len(keys) {
		return false
	}
	for _, key := range keys {
		if _, exists := object[key]; !exists {
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

func uniqueJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := consumeUniqueValue(decoder, 0); err != nil {
		return err
	}

	return requireJSONEnd(decoder)
}

func consumeUniqueValue(decoder *json.Decoder, depth int) error {
	if depth > 64 {
		return run.ErrValidation
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}

	switch delimiter {
	case '{':
		return consumeUniqueObject(decoder, depth)
	case '[':
		return consumeUniqueArray(decoder, depth)
	default:
		return run.ErrValidation
	}
}

func consumeUniqueObject(decoder *json.Decoder, depth int) error {
	keys := make(map[string]struct{})
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := keyToken.(string)
		if !ok {
			return run.ErrValidation
		}
		if _, exists := keys[key]; exists {
			return run.ErrValidation
		}
		keys[key] = struct{}{}
		if err := consumeUniqueValue(decoder, depth+1); err != nil {
			return err
		}
	}

	return consumeEnd(decoder)
}

func consumeUniqueArray(decoder *json.Decoder, depth int) error {
	for decoder.More() {
		if err := consumeUniqueValue(decoder, depth+1); err != nil {
			return err
		}
	}

	return consumeEnd(decoder)
}

func consumeEnd(decoder *json.Decoder) error {
	_, err := decoder.Token()

	return err
}

func requireJSONEnd(decoder *json.Decoder) error {
	if _, err := decoder.Token(); err != io.EOF {
		return run.ErrValidation
	}

	return nil
}
