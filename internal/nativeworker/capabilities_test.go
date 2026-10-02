package nativeworker

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
)

func TestParsePresenceUsesThePinnedContractAndCanonicalEvidence(t *testing.T) {
	document, err := contract.Files.ReadFile("snapshot/api/native-worker/v1/examples/presence.json")
	if err != nil {
		t.Fatal(err)
	}
	presence, err := ParsePresence(document, len(document))
	if err != nil {
		t.Fatal("pinned worker presence was rejected", err)
	}
	if presence.ProtocolVersion != 1 || presence.AgentVersion != "1.0.0" ||
		presence.Availability != "AVAILABLE" || presence.Capabilities.Architecture != "x64" {
		t.Fatalf("presence = %#v", presence)
	}
	if bytes.Contains(presence.QualificationEvidence, []byte("\n")) {
		t.Fatalf("qualification evidence is not canonical: %s", presence.QualificationEvidence)
	}
	var evidence map[string]any
	if json.Unmarshal(presence.QualificationEvidence, &evidence) != nil ||
		evidence["environmentId"] != "windows-mainstream" {
		t.Fatalf("qualification evidence = %s", presence.QualificationEvidence)
	}
}

func TestParsePresenceRejectsInvalidDocuments(t *testing.T) {
	document, err := contract.Files.ReadFile("snapshot/api/native-worker/v1/examples/presence.json")
	if err != nil {
		t.Fatal(err)
	}
	duplicate := bytes.Replace(document, []byte(`"capacity": 1`),
		[]byte(`"capacity": 1, "capacity": 1`), 1)
	unknown := bytes.Replace(document, []byte(`"protocolVersion": 1`),
		[]byte(`"protocolVersion": 1, "workerId": "worker-controlled"`), 1)
	for _, test := range []struct {
		name     string
		document []byte
		limit    int
	}{
		{name: "empty", document: nil, limit: len(document)},
		{name: "oversize", document: document, limit: len(document) - 1},
		{name: "duplicate field", document: duplicate, limit: len(duplicate)},
		{name: "unknown field", document: unknown, limit: len(unknown)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ParsePresence(test.document, test.limit); err == nil {
				t.Fatal("invalid presence was accepted")
			}
		})
	}
}

func TestParseV2PresenceKeepsProtocolGenerationsDistinct(t *testing.T) {
	v2, err := contract.Files.ReadFile("snapshot/api/native-worker/v2/examples/presence.json")
	if err != nil {
		t.Fatal(err)
	}
	presence, err := ParseV2Presence(v2, len(v2))
	if err != nil || presence.ProtocolVersion != 2 ||
		presence.Capabilities.RunnerContract != 2 || presence.Availability != "AVAILABLE" {
		t.Fatalf("v2 presence = %#v, err = %v", presence, err)
	}
	if bytes.Contains(presence.QualificationEvidence, []byte("\n")) {
		t.Fatal("v2 qualification evidence is not canonical")
	}
	if _, err := ParsePresence(v2, len(v2)); err == nil {
		t.Fatal("v2 presence accepted as v1")
	}
	v1, err := contract.Files.ReadFile("snapshot/api/native-worker/v1/examples/presence.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseV2Presence(v1, len(v1)); err == nil {
		t.Fatal("v1 presence accepted as v2")
	}
	if _, err := ParseV2Presence(v2, len(v2)-1); err == nil {
		t.Fatal("oversized v2 presence accepted")
	}
}

func TestCapabilitiesSupportEveryReviewedPlanRequirement(t *testing.T) {
	document, err := contract.Files.ReadFile("snapshot/api/native-worker/v1/examples/presence.json")
	if err != nil {
		t.Fatal(err)
	}
	presence, err := ParsePresence(document, len(document))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := assignmentTemplate().Resolve(
		"search-journey", "browser-smoke", "windows-mainstream", "local-demo", sourceCheckout(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !presence.Capabilities.Supports(plan) {
		t.Fatal("compatible pinned capabilities rejected the reviewed plan")
	}

	for _, test := range []struct {
		name   string
		mutate func(*Capabilities, *Plan)
	}{
		{name: "invalid capabilities", mutate: func(capabilities *Capabilities, _ *Plan) {
			capabilities.Capacity = 2
		}},
		{name: "browser", mutate: func(_ *Capabilities, plan *Plan) {
			plan.Assignment.Browser = "chrome"
		}},
		{name: "diagnostic mode", mutate: func(_ *Capabilities, plan *Plan) {
			plan.Assignment.DiagnosticMode = "cpu-profile"
		}},
		{name: "execution artifact", mutate: func(capabilities *Capabilities, _ *Plan) {
			capabilities.ExecutionArtifacts = []string{"release-archive"}
		}},
		{name: "artifact kind", mutate: func(capabilities *Capabilities, _ *Plan) {
			capabilities.ArtifactKinds = []string{"measurements"}
		}},
		{name: "invalid plan", mutate: func(_ *Capabilities, plan *Plan) {
			plan.Assignment.Target.BaseURL = "http://sut.invalid"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			capabilities := presence.Capabilities
			capabilities.Browsers = append([]string(nil), capabilities.Browsers...)
			capabilities.ExecutionArtifacts = append([]string(nil), capabilities.ExecutionArtifacts...)
			capabilities.ArtifactKinds = append([]string(nil), capabilities.ArtifactKinds...)
			capabilities.DiagnosticModes = append([]string(nil), capabilities.DiagnosticModes...)
			candidate := plan.Clone()
			test.mutate(&capabilities, &candidate)
			if capabilities.Supports(candidate) {
				t.Fatal("incompatible capabilities accepted the reviewed plan")
			}
		})
	}
}

func TestCapabilitiesRejectDuplicateAndUnsupportedValues(t *testing.T) {
	capabilities := Capabilities{
		Capacity: 1, Tool: "playwright", OperatingSystem: "windows", Architecture: "x64",
		Browsers: []string{"edge"}, RunnerContract: 1,
		ExecutionArtifacts: []string{"source-checkout"}, ArtifactKinds: []string{"manifest"},
		DiagnosticModes: []string{"trace"},
	}
	plan, err := assignmentTemplate().Resolve(
		"search-journey", "browser-smoke", "windows-mainstream", "local-demo", sourceCheckout(),
	)
	if err != nil {
		t.Fatal(err)
	}
	plan.ArtifactPlan = plan.ArtifactPlan[1:]
	if !capabilities.Supports(plan) {
		t.Fatal("minimal valid capabilities rejected the reviewed plan")
	}

	capabilities.Browsers = []string{"edge", "edge"}
	if capabilities.Supports(plan) {
		t.Fatal("duplicate capabilities were accepted")
	}
	capabilities.Browsers = []string{"firefox"}
	if capabilities.Supports(plan) {
		t.Fatal("unsupported capabilities were accepted")
	}
	capabilities.Browsers = []string{strings.Repeat("x", 65)}
	if capabilities.Supports(plan) {
		t.Fatal("invalid capability values were accepted")
	}
}
