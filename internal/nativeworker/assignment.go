// Package nativeworker owns server-reviewed native execution assignments.
package nativeworker

import (
	"net/url"
	"regexp"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
)

const (
	maximumArtifactBytes           int64 = 512 * 1024 * 1024
	playwrightMeasurementsV2Format       = "playwright-measurements/v2"
	rawResultV2Format                    = "raw-result/v2"
)

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

// ExecutionArtifact is the Native Worker transport identity. Source
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
			!mediaTypePattern.MatchString(slot.MediaType) || !validArtifactSlotFormat(slot.Slot, slot.Format) ||
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

func validArtifactSlotFormat(slot string, format string) bool {
	if !contract.ValidNativeArtifactFormat(format) {
		return false
	}
	switch slot {
	case "measurements":
		return format == playwrightMeasurementsV2Format
	case "manifest":
		return format == rawResultV2Format
	default:
		return true
	}
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
