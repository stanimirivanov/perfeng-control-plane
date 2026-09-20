// Package nativeworker owns server-reviewed native execution assignments.
package nativeworker

import (
	"net/url"
	"regexp"

	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

const maximumArtifactBytes int64 = 512 * 1024 * 1024

var mediaTypePattern = regexp.MustCompile(`^[a-z0-9.+-]+/[a-z0-9.+-]+$`)

// Target is the reviewed system under test selected by the control plane.
type Target struct {
	EnvironmentID string `json:"environmentId"`
	BaseURL       string `json:"baseUrl"`
}

// ArtifactSlot defines one bounded output expected from a native execution.
type ArtifactSlot struct {
	Slot         string `json:"slot"`
	Kind         string `json:"kind"`
	MediaType    string `json:"mediaType"`
	Format       string `json:"format"`
	MaximumBytes int64  `json:"maximumBytes"`
	Required     bool   `json:"required"`
}

// ExecutionArtifact is the Native Worker API v1 transport identity. Source
// checkout fields intentionally differ from the raw-result producer envelope.
type ExecutionArtifact struct {
	Kind             string `json:"kind"`
	Repository       string `json:"repository,omitempty"`
	Revision         string `json:"revision,omitempty"`
	Lockfile         string `json:"lockfile,omitempty"`
	LockfileSHA256   string `json:"lockfileSha256,omitempty"`
	URL              string `json:"url,omitempty"`
	ArchiveSHA256    string `json:"archiveSha256,omitempty"`
	ExecutableSHA256 string `json:"executableSha256,omitempty"`
}

// AssignmentTemplate contains reviewed values that cannot be derived from the
// accepted Run or immutable producer provenance.
type AssignmentTemplate struct {
	BaseURL        string         `json:"baseUrl"`
	Browser        string         `json:"browser"`
	DiagnosticMode string         `json:"diagnosticMode"`
	ArtifactPlan   []ArtifactSlot `json:"artifactPlan"`
}

// Assignment is the immutable execution description nested in a native lease.
type Assignment struct {
	CatalogueTestID   string            `json:"catalogueTestId"`
	WorkloadID        string            `json:"workloadId"`
	Profile           string            `json:"profile"`
	Target            Target            `json:"target"`
	Browser           string            `json:"browser"`
	DiagnosticMode    string            `json:"diagnosticMode"`
	ExecutionArtifact ExecutionArtifact `json:"executionArtifact"`
}

// Plan contains the assignment and its sibling lease artifact plan.
type Plan struct {
	Assignment   Assignment     `json:"assignment"`
	ArtifactPlan []ArtifactSlot `json:"artifactPlan"`
}

// Validate checks values that must be reviewed before a native Run is admitted.
func (template AssignmentTemplate) Validate() error {
	if !validBaseURL(template.BaseURL) || !validBrowser(template.Browser) ||
		!validDiagnosticMode(template.DiagnosticMode) || !validArtifactPlan(template.ArtifactPlan) {
		return run.ErrValidation
	}

	return nil
}

// Clone returns an independently owned assignment template.
func (template AssignmentTemplate) Clone() AssignmentTemplate {
	template.ArtifactPlan = append([]ArtifactSlot(nil), template.ArtifactPlan...)

	return template
}

// Resolve combines reviewed template values with immutable Run and registry identities.
func (template AssignmentTemplate) Resolve(
	testID string,
	workloadID string,
	profile string,
	environmentID string,
	artifact rawresult.ExecutionArtifact,
) (Plan, error) {
	assignment := Assignment{
		CatalogueTestID: testID, WorkloadID: workloadID, Profile: profile,
		Target:  Target{EnvironmentID: environmentID, BaseURL: template.BaseURL},
		Browser: template.Browser, DiagnosticMode: template.DiagnosticMode,
		ExecutionArtifact: executionArtifact(artifact),
	}
	plan := Plan{
		Assignment:   assignment,
		ArtifactPlan: append([]ArtifactSlot(nil), template.ArtifactPlan...),
	}
	if plan.Validate() != nil {
		return Plan{}, run.ErrValidation
	}

	return plan, nil
}

// Validate checks a resolved assignment against its Native Worker API v1 shape.
func (assignment Assignment) Validate() error {
	if !rawresult.ValidResourceID(assignment.CatalogueTestID) ||
		!rawresult.ValidResourceID(assignment.WorkloadID) ||
		!rawresult.ValidResourceID(assignment.Profile) ||
		!rawresult.ValidResourceID(assignment.Target.EnvironmentID) ||
		!validBaseURL(assignment.Target.BaseURL) || !validBrowser(assignment.Browser) ||
		!validDiagnosticMode(assignment.DiagnosticMode) ||
		!validExecutionArtifact(assignment.ExecutionArtifact) {
		return run.ErrValidation
	}

	return nil
}

// Validate checks the complete reviewed native work plan.
func (plan Plan) Validate() error {
	if plan.Assignment.Validate() != nil || !validArtifactPlan(plan.ArtifactPlan) {
		return run.ErrValidation
	}

	return nil
}

// Clone returns an independently owned native plan.
func (plan Plan) Clone() Plan {
	plan.ArtifactPlan = append([]ArtifactSlot(nil), plan.ArtifactPlan...)

	return plan
}

func validBaseURL(value string) bool {
	parsed, err := url.ParseRequestURI(value)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil &&
		parsed.RawQuery == "" && !parsed.ForceQuery && parsed.Fragment == ""
}

func validBrowser(value string) bool {
	return value == "chromium" || value == "chrome" || value == "edge"
}

func validDiagnosticMode(value string) bool {
	switch value {
	case "lightweight", "trace", "heap", "cpu-profile":
		return true
	default:
		return false
	}
}

func validArtifactPlan(plan []ArtifactSlot) bool {
	if len(plan) == 0 || len(plan) > 16 {
		return false
	}
	seen := make(map[string]struct{}, len(plan))
	for _, slot := range plan {
		if !rawresult.ValidResourceID(slot.Slot) || !rawresult.ValidResourceID(slot.Kind) ||
			!mediaTypePattern.MatchString(slot.MediaType) || !rawresult.ValidResourceID(slot.Format) ||
			slot.MaximumBytes < 1 || slot.MaximumBytes > maximumArtifactBytes {
			return false
		}
		if _, exists := seen[slot.Slot]; exists {
			return false
		}
		seen[slot.Slot] = struct{}{}
	}

	return true
}

func executionArtifact(artifact rawresult.ExecutionArtifact) ExecutionArtifact {
	result := ExecutionArtifact{
		Kind: artifact.Kind, URL: artifact.URL,
		ArchiveSHA256: artifact.ArchiveSHA256, ExecutableSHA256: artifact.ExecutableSHA256,
	}
	if artifact.DependencyLock != nil {
		result.Repository = artifact.Repository
		result.Revision = artifact.GitSHA
		result.Lockfile = artifact.DependencyLock.Path
		result.LockfileSHA256 = artifact.DependencyLock.SHA256
	}

	return result
}

func validExecutionArtifact(artifact ExecutionArtifact) bool {
	switch artifact.Kind {
	case "source-checkout":
		if artifact.URL != "" || artifact.ArchiveSHA256 != "" || artifact.ExecutableSHA256 != "" {
			return false
		}
		return (rawresult.ExecutionArtifact{
			Kind: artifact.Kind, Repository: artifact.Repository, GitSHA: artifact.Revision,
			DependencyLock: &rawresult.DependencyLock{
				Path: artifact.Lockfile, SHA256: artifact.LockfileSHA256,
			},
		}).Validate() == nil
	case "release-archive":
		if artifact.Repository != "" || artifact.Revision != "" || artifact.Lockfile != "" ||
			artifact.LockfileSHA256 != "" {
			return false
		}
		return (rawresult.ExecutionArtifact{
			Kind: artifact.Kind, URL: artifact.URL,
			ArchiveSHA256: artifact.ArchiveSHA256, ExecutableSHA256: artifact.ExecutableSHA256,
		}).Validate() == nil
	default:
		return false
	}
}
