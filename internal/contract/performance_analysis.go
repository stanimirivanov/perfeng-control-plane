package contract

import (
	"encoding/json"
	"errors"
	"net/url"
)

// AnalysisPublicationMaximumBytes bounds publication metadata, not artifact bytes.
const AnalysisPublicationMaximumBytes = 64 << 10

// ErrAnalysisPublication rejects invalid claims without reflecting their contents.
var ErrAnalysisPublication = errors.New("invalid performance analysis publication")

// ErrAnalysisPublicationVersion additionally classifies unsupported wire generations.
var ErrAnalysisPublicationVersion = errors.New("unsupported performance analysis publication version")

// AnalysisNativeIdentity pins a native resource without interpreting catalog aliases.
type AnalysisNativeIdentity struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
}

// AnalysisArtifactClaim is transport metadata, not a verified run.Artifact.
// Exact size syntax is retained; these fields grant no fetch or storage authority.
type AnalysisArtifactClaim struct {
	ID        string      `json:"id"`
	RunID     string      `json:"runId"`
	Kind      string      `json:"kind"`
	URI       string      `json:"uri"`
	SHA256    string      `json:"sha256"`
	SizeBytes json.Number `json:"sizeBytes"`
	MediaType string      `json:"mediaType"`
	Format    string      `json:"format"`
}

// AnalysisBaselineClaim preserves the claimed approved baseline association.
type AnalysisBaselineClaim struct {
	Identity AnalysisNativeIdentity `json:"identity"`
	Artifact AnalysisArtifactClaim  `json:"artifact"`
}

// AnalysisNativeBinding preserves explicit admission-time native association claims.
// TestID may differ from Workload.ID. Only retained authenticated evidence can
// establish this association; consumers must not reconstruct it from current state.
type AnalysisNativeBinding struct {
	RunID    string                 `json:"runId"`
	TestID   string                 `json:"testId"`
	Workload AnalysisNativeIdentity `json:"workload"`
	Policy   AnalysisNativeIdentity `json:"policy"`
	Baseline *AnalysisBaselineClaim `json:"baseline"`
}

// AnalysisPublicationData keeps immutable integration context separate from native claims.
type AnalysisPublicationData struct {
	Context  IntegrationContext    `json:"context"`
	Run      CatalogIdentity       `json:"run"`
	Native   AnalysisNativeBinding `json:"native"`
	Artifact AnalysisArtifactClaim `json:"artifact"`
}

// AnalysisPublication is a separate closed family, never an IntegrationMessage variant.
type AnalysisPublication struct {
	SpecVersion     string                  `json:"specversion"`
	ID              string                  `json:"id"`
	Source          string                  `json:"source"`
	Type            string                  `json:"type"`
	Time            string                  `json:"time"`
	DataContentType string                  `json:"datacontenttype"`
	DataSchema      string                  `json:"dataschema"`
	Data            AnalysisPublicationData `json:"data"`
}

// ValidContext checks per-publication relationships after schema validation.
// It cannot authenticate source/principal or compare retained admission/report bytes.
func (message AnalysisPublication) ValidContext() bool {
	context := IntegrationMessage{Time: message.Time, Data: IntegrationData{Context: message.Data.Context}}
	artifact, baseline := message.Data.Artifact, message.Data.Native.Baseline
	validURI := func(address string) bool {
		uri, err := url.Parse(address)
		return err == nil && uri.Hostname() != "" && uri.User == nil
	}

	return context.ValidContext() && message.Source == message.Data.Run.Issuer &&
		artifact.RunID == message.Data.Native.RunID &&
		(baseline == nil) == (message.Data.Context.Baseline == nil) &&
		validURI(artifact.URI) && (baseline == nil || validURI(baseline.Artifact.URI))
}
