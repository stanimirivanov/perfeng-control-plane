package nativeworker

import (
	"encoding/json"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/jsondocument"
	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

// Capabilities describes the execution features reported by a native worker.
type Capabilities struct {
	Capacity           int      `json:"capacity"`
	Tool               string   `json:"tool"`
	OperatingSystem    string   `json:"operatingSystem"`
	Architecture       string   `json:"architecture"`
	Browsers           []string `json:"browsers"`
	RunnerContract     int      `json:"runnerContract"`
	ExecutionArtifacts []string `json:"executionArtifacts"`
	ArtifactKinds      []string `json:"artifactKinds"`
	DiagnosticModes    []string `json:"diagnosticModes"`
}

// Presence is the contract-validated state reported by a native worker.
// QualificationEvidence is canonical JSON suitable for comparison with the
// server-owned approved evidence.
type Presence struct {
	ProtocolVersion       int             `json:"protocolVersion"`
	AgentVersion          string          `json:"agentVersion"`
	Availability          string          `json:"availability"`
	Capabilities          Capabilities    `json:"capabilities"`
	QualificationEvidence json.RawMessage `json:"qualificationEvidence"`
}

// ParseV2Presence validates and decodes one bounded Native Worker API v2
// presence document.
func ParseV2Presence(document []byte, maximumBytes int) (Presence, error) {
	return parsePresence(document, maximumBytes, contract.ValidateNativeWorkerV2Presence)
}

func parsePresence(
	document []byte,
	maximumBytes int,
	validate func(any) error,
) (Presence, error) {
	if !jsondocument.Valid(document, maximumBytes) {
		return Presence{}, run.ErrValidation
	}
	var value any
	if !jsondocument.Decode(document, &value) || validate(value) != nil {
		return Presence{}, run.ErrValidation
	}
	var presence Presence
	if !jsondocument.Decode(document, &presence) {
		return Presence{}, run.ErrValidation
	}
	var evidence any
	if !jsondocument.Decode(presence.QualificationEvidence, &evidence) {
		return Presence{}, run.ErrValidation
	}
	canonical, err := json.Marshal(evidence)
	if err != nil {
		return Presence{}, run.ErrValidation
	}
	presence.QualificationEvidence = canonical

	return presence, nil
}

func (capabilities Capabilities) validForRunnerContract(version int) bool {
	validMode := validDiagnosticMode
	if version == 2 {
		validMode = func(value string) bool { return value == "baseline" }
	}

	return capabilities.Capacity == 1 && capabilities.Tool == "playwright" &&
		capabilities.OperatingSystem == "linux" &&
		(capabilities.Architecture == "x64" || capabilities.Architecture == "arm64") &&
		capabilities.RunnerContract == version &&
		validSet(capabilities.Browsers, 1, 4, validBrowser) &&
		validSet(capabilities.ExecutionArtifacts, 1, 2, validNativeArtifactKind) &&
		validSet(capabilities.ArtifactKinds, 1, 16, rawresult.ValidResourceID) &&
		validSet(capabilities.DiagnosticModes, 1, 8, validMode)
}

func validNativeArtifactKind(value string) bool {
	return value == "source-checkout" || value == "release-archive"
}

func validSet(values []string, minimum int, maximum int, valid func(string) bool) bool {
	if len(values) < minimum || len(values) > maximum {
		return false
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !valid(value) {
			return false
		}
		if _, exists := seen[value]; exists {
			return false
		}
		seen[value] = struct{}{}
	}

	return true
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}

	return false
}
