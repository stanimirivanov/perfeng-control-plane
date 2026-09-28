package nativeworker

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func assignmentTemplate() AssignmentTemplate {
	return AssignmentTemplate{
		BaseURL: "https://sut.perfeng.invalid/search", Browser: "edge",
		DiagnosticMode: "trace",
		ArtifactPlan: []ArtifactSlot{
			{Slot: "measurements", Kind: "measurements", MediaType: "application/json", Format: playwrightMeasurementsV2Format, MaximumBytes: 1 << 20, Required: true},
			{Slot: "manifest", Kind: "manifest", MediaType: "application/json", Format: rawResultV2Format, MaximumBytes: 1 << 20, Required: true},
		},
	}
}

func sourceCheckout() rawresult.ExecutionArtifact {
	return rawresult.ExecutionArtifact{
		Kind: "source-checkout", Repository: "https://github.com/example/perfeng-playwright",
		GitSHA: strings.Repeat("a", 40),
		DependencyLock: &rawresult.DependencyLock{
			Path: "pnpm-lock.yaml", SHA256: strings.Repeat("b", 64),
		},
	}
}

func TestAssignmentTemplateResolvesAnIsolatedNativeAssignment(t *testing.T) {
	template := assignmentTemplate()
	artifact := sourceCheckout()
	plan, err := template.Resolve(
		"search-journey", "browser-smoke", "windows-mainstream", "local-demo", artifact,
	)
	if err != nil || plan.Validate() != nil {
		t.Fatal("valid assignment was not resolved", err)
	}
	assignment := plan.Assignment
	if assignment.Target.BaseURL != template.BaseURL || assignment.Browser != "edge" ||
		assignment.ExecutionArtifact.Lockfile != "pnpm-lock.yaml" {
		t.Fatalf("assignment = %#v", assignment)
	}

	template.ArtifactPlan[0].Slot = "changed"
	artifact.DependencyLock.Path = "changed"
	if plan.ArtifactPlan[0].Slot != "measurements" ||
		assignment.ExecutionArtifact.Lockfile != "pnpm-lock.yaml" {
		t.Fatal("resolved assignment aliases caller-owned values")
	}
	clone := plan.Clone()
	clone.ArtifactPlan[0].Slot = "clone-changed"
	clone.Assignment.ExecutionArtifact.Lockfile = "clone-changed"
	if plan.ArtifactPlan[0].Slot != "measurements" ||
		assignment.ExecutionArtifact.Lockfile != "pnpm-lock.yaml" {
		t.Fatal("assignment clone aliases the original")
	}
	encoded, err := json.Marshal(assignment)
	if err != nil {
		t.Fatal(err)
	}
	transport := string(encoded)
	for _, field := range []string{`"revision"`, `"lockfile"`, `"lockfileSha256"`} {
		if !strings.Contains(transport, field) {
			t.Fatalf("native transport omits %s: %s", field, transport)
		}
	}
	for _, field := range []string{`"gitSha"`, `"dependencyLock"`} {
		if strings.Contains(transport, field) {
			t.Fatalf("raw-result field %s leaked into native transport: %s", field, transport)
		}
	}
}

func TestAssignmentTemplateRejectsInvalidReviewedValues(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*AssignmentTemplate)
	}{
		{name: "HTTP target", mutate: func(value *AssignmentTemplate) { value.BaseURL = "http://sut.invalid" }},
		{name: "target credentials", mutate: func(value *AssignmentTemplate) { value.BaseURL = "https://user@sut.invalid" }},
		{name: "browser", mutate: func(value *AssignmentTemplate) { value.Browser = "firefox" }},
		{name: "diagnostic mode", mutate: func(value *AssignmentTemplate) { value.DiagnosticMode = "everything" }},
		{name: "empty plan", mutate: func(value *AssignmentTemplate) { value.ArtifactPlan = nil }},
		{name: "duplicate slot", mutate: func(value *AssignmentTemplate) { value.ArtifactPlan[1].Slot = value.ArtifactPlan[0].Slot }},
		{name: "media type", mutate: func(value *AssignmentTemplate) { value.ArtifactPlan[0].MediaType = "Application/JSON" }},
		{name: "artifact format", mutate: func(value *AssignmentTemplate) { value.ArtifactPlan[0].Format = "Playwright-measurements/v2" }},
		{name: "obsolete artifact format", mutate: func(value *AssignmentTemplate) { value.ArtifactPlan[0].Format = "playwright-measurements-v2" }},
		{name: "size", mutate: func(value *AssignmentTemplate) { value.ArtifactPlan[0].MaximumBytes = maximumArtifactBytes + 1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			template := assignmentTemplate()
			test.mutate(&template)
			if !errors.Is(template.Validate(), run.ErrValidation) {
				t.Fatal("invalid template accepted")
			}
		})
	}
}

func TestAssignmentRejectsContainerAndInvalidNativeArtifacts(t *testing.T) {
	template := assignmentTemplate()
	for _, artifact := range []rawresult.ExecutionArtifact{
		{Kind: "oci-image", Image: "ghcr.io/example/runner@sha256:" + strings.Repeat("c", 64)},
		{Kind: "source-checkout"},
	} {
		if _, err := template.Resolve(
			"search-journey", "browser-smoke", "windows-mainstream", "local-demo", artifact,
		); !errors.Is(err, run.ErrValidation) {
			t.Fatalf("artifact %#v error = %v", artifact, err)
		}
	}
}

func TestPlanMatchesOnlyAssignedArtifactDeclarations(t *testing.T) {
	plan, err := assignmentTemplate().Resolve(
		"search-journey",
		"browser-smoke",
		"windows-mainstream",
		"local-demo",
		sourceCheckout(),
	)
	if err != nil {
		t.Fatal(err)
	}
	slot, matched := plan.MatchArtifactSlot("measurements", "application/json", 4096)
	if !matched || slot != plan.ArtifactPlan[0] {
		t.Fatal("assigned artifact declaration was not matched")
	}
	for name, declaration := range map[string]struct {
		name      string
		mediaType string
		size      int64
	}{
		"unknown slot": {name: "trace", mediaType: "application/json", size: 4096},
		"media type":   {name: "measurements", mediaType: "application/x-ndjson", size: 4096},
		"empty object": {name: "measurements", mediaType: "application/json", size: 0},
		"oversized":    {name: "measurements", mediaType: "application/json", size: 1<<20 + 1},
	} {
		t.Run(name, func(t *testing.T) {
			if _, matched := plan.MatchArtifactSlot(
				declaration.name,
				declaration.mediaType,
				declaration.size,
			); matched {
				t.Fatal("unassigned artifact declaration was matched")
			}
		})
	}
}

func TestAssignmentMatchesPinnedNativeWorkerLeaseFixture(t *testing.T) {
	document, err := contract.Files.ReadFile("snapshot/api/native-worker/v1/examples/lease.json")
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Assignment   json.RawMessage `json:"assignment"`
		ArtifactPlan []ArtifactSlot  `json:"artifactPlan"`
	}
	if json.Unmarshal(document, &envelope) != nil {
		t.Fatal("invalid pinned lease fixture")
	}
	var assignment Assignment
	if err := json.Unmarshal(envelope.Assignment, &assignment); err != nil {
		t.Fatal(err)
	}
	if err := assignment.Validate(); err != nil {
		t.Fatalf("pinned lease assignment does not match the runtime model: %#v", assignment)
	}
	plan := Plan{Assignment: assignment, ArtifactPlan: envelope.ArtifactPlan}
	if err := plan.Validate(); err != nil {
		t.Fatal("pinned lease artifact plan does not match the runtime model")
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	var lease map[string]any
	var got any
	if json.Unmarshal(document, &lease) != nil || json.Unmarshal(encoded, &got) != nil {
		t.Fatal("could not compare the pinned lease plan")
	}
	want := map[string]any{
		"assignment": lease["assignment"], "artifactPlan": lease["artifactPlan"],
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("native plan transport changed: got %s", encoded)
	}
}
