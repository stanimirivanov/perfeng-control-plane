package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMarkdownLinksReportMissingTargetsAndAnchors(t *testing.T) {
	t.Parallel()
	root := fixtureRepository(t)
	writeFixture(t, root, "docs/guide.md", "# Guide\n\n## TL;DR\n\n[missing](none.md) [section](../README.md#missing)\n")
	violations, err := checkRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	joined := renderViolations(violations)
	if !strings.Contains(joined, "does not resolve") || !strings.Contains(joined, "missing heading") {
		t.Fatalf("violations = %s, want actionable link failures", joined)
	}
}

func TestRepositoryFixturePasses(t *testing.T) {
	t.Parallel()
	root := fixtureRepository(t)
	violations, err := checkRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Fatalf("violations = %s", renderViolations(violations))
	}
}

func TestActionPolicyAcceptsMajorsAndLocalSourcesButChecksCompositeActions(t *testing.T) {
	t.Parallel()
	root := fixtureRepository(t)
	writeFixture(t, root, ".github/workflows/nested/validate.yml", `jobs:
  validate:
    steps:
      - uses: "actions/checkout@v7" # semantic major
      - uses: './.github/actions/local'
      - uses: docker://alpine:3.22
      - uses: owner/unversioned
      - run: |-
          echo 'uses: owner/not-an-action@abcdef'
`)
	writeFixture(t, root, ".github/actions/example/action.yaml", `runs:
  using: composite
  steps:
    - uses: 'owner/action@abcdef'
`)

	violations, err := checkRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	rendered := renderViolations(violations)
	for _, expected := range []string{
		".github/actions/example/action.yaml:4: [workflow.action-version]",
		".github/workflows/nested/validate.yml:7: [workflow.action-version]",
		`action reference "owner/action@abcdef" is not a semantic major tag`,
		`action reference "owner/unversioned" is not a semantic major tag`,
	} {
		if !strings.Contains(rendered, expected) {
			t.Errorf("violations = %s, want %q", rendered, expected)
		}
	}
	for _, unexpected := range []string{"not-an-action", "actions/checkout", "docker://", "actions/local"} {
		if strings.Contains(rendered, unexpected) {
			t.Errorf("violations = %s, did not want %q", rendered, unexpected)
		}
	}
	if len(violations) != 2 {
		t.Fatalf("violations = %s, want exactly two action-policy violations", rendered)
	}
}

func fixtureRepository(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFixture(t, root, "README.md", "# Example\n\n## TL;DR\n\n- Example.\n")
	writeFixture(t, root, "CONTRIBUTING.md", "# Contributing\n\n## TL;DR\n\n- Policy.\n")
	writeFixture(t, root, "docs/decisions/README.md", "# Architecture decision records\n\n## TL;DR\n\n- Decisions.\n\n## Index\n\n| ADR | Decision | Status |\n|:--|:--|:--|\n")
	writeFixture(t, root, ".github/ISSUE_TEMPLATE/work-item.md", "**Repository:** perfeng-control-plane\n**Milestone:** MNN — Outcome\n\n## Goal\n\n## Scope\n\n## Design decisions\n\n## Security and operations\n\n## Acceptance criteria\n\n## Verification\n\n## Out of scope\n")
	writeFixture(t, root, ".github/PULL_REQUEST_TEMPLATE.md", "## Linked work\n\n## Outcome and scope\n\n## Design, compatibility and operations\n\n## Verification\n\nChecks not run, blocking reason and residual risk:\n\n## Limitations and review\n")
	return root
}

func writeFixture(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func renderViolations(violations []violation) string {
	var rendered []string
	for _, item := range violations {
		rendered = append(rendered, item.String())
	}
	return strings.Join(rendered, "\n")
}
