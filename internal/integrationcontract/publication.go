package integrationcontract

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/analysisresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/integration"
	"github.com/stanimirivanov/perfeng-control-plane/internal/policy"
	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

// PublicationInput supplies independently trusted scope, retained admission,
// registered report/candidate references and producer/bundle provenance. Do not
// derive these authorities from ReportBytes. PolicyBytes must be the exact frozen
// policy; the builder never resolves current eligibility. Caller owns all inputs
// and must not mutate them concurrently. EventID/PublishedAt must later be retained
// with the publication rather than regenerated on retry.
type PublicationInput struct {
	Admission                integration.Record
	Provider                 string
	Principal                contract.CatalogIdentity
	EventID                  string
	PublishedAt              time.Time
	Report, Candidate        run.Artifact
	ReportBytes, PolicyBytes []byte
	Producer                 rawresult.Producer
	ContractsVersion         string
}

// BuildAnalysisPublication binds exact evidence bytes to retained native admission
// and returns owned publication claims or zero output/redacted errors. It is
// deterministic/concurrent-safe and performs no I/O, clock lookup or activation.
// It verifies consistency, not the authenticity of caller-supplied authorities.
func BuildAnalysisPublication(ctx context.Context, input PublicationInput) (contract.AnalysisPublication, error) {
	if err := ctx.Err(); err != nil {
		return contract.AnalysisPublication{}, err
	}
	admission, native := input.Admission.Disposition, input.Admission.Native
	if native == nil || input.Admission.Run == nil || admission.Type != "dev.perfeng.integration.accepted.v1" ||
		admission.Source != input.Provider || admission.Data.Context.Principal != input.Principal || admission.Data.Run == nil ||
		native.RunID != input.Admission.Run.ID || native.TestID != input.Admission.Run.Request.TestSuite ||
		input.Admission.Run.Request.Validate() != nil ||
		native.Policy != (contract.AnalysisNativeIdentity{ID: input.Admission.Run.Request.Policy.ID, Version: input.Admission.Run.Request.Policy.Version, SHA256: input.Admission.Run.Request.Policy.SHA256}) {
		return contract.AnalysisPublication{}, contract.ErrAnalysisPublication
	}
	if _, err := EncodeIntegrationMessage(admission); err != nil {
		return contract.AnalysisPublication{}, contract.ErrAnalysisPublication
	}
	if len(input.ReportBytes) > 1<<20 || len(input.PolicyBytes) > 1<<20 || input.Report.Validate() != nil ||
		input.Report.RunID != native.RunID || input.Report.Kind != "normalized" || input.Report.Format != "analysis-result/v1" ||
		input.Report.MediaType != "application/json" || input.Report.SizeBytes < 1 || input.Report.SizeBytes != int64(len(input.ReportBytes)) ||
		publicationDigest(input.ReportBytes) != input.Report.SHA256 || publicationDigest(input.PolicyBytes) != native.Policy.SHA256 {
		return contract.AnalysisPublication{}, contract.ErrAnalysisPublication
	}
	manifest, err := analysisresult.Parse(input.ReportBytes, native.RunID, input.ContractsVersion)
	if err != nil || !publicationUnicode(input.ReportBytes) || !publicationNumbers(input.ReportBytes) ||
		manifest.TestID != native.TestID || manifest.CandidateArtifact != input.Candidate ||
		!manifest.Producer.Equal(input.Producer) || manifest.Policy.ID != native.Policy.ID || manifest.Policy.Version != native.Policy.Version || manifest.Policy.SHA256 != native.Policy.SHA256 {
		return contract.AnalysisPublication{}, contract.ErrAnalysisPublication
	}
	accepted, err := time.Parse(time.RFC3339Nano, admission.Time)
	if err != nil {
		return contract.AnalysisPublication{}, contract.ErrAnalysisPublication
	}
	created, err := time.Parse(time.RFC3339Nano, manifest.CreatedAt)
	if err != nil || created.Before(accepted) || input.PublishedAt.Before(created) {
		return contract.AnalysisPublication{}, contract.ErrAnalysisPublication
	}
	resolutions := []policy.BaselineResolution{}
	var baselineArtifact *run.Artifact
	if native.Baseline != nil {
		raw, err := json.Marshal(native.Baseline.Artifact)
		var artifact run.Artifact
		if err != nil || json.Unmarshal(raw, &artifact) != nil {
			return contract.AnalysisPublication{}, contract.ErrAnalysisPublication
		}
		baselineArtifact = &artifact
		if artifact.ID == input.Report.ID || artifact.URI == input.Report.URI {
			return contract.AnalysisPublication{}, contract.ErrAnalysisPublication
		}
		resolutions = append(resolutions, policy.BaselineResolution{ID: native.Baseline.Identity.ID, Version: native.Baseline.Identity.Version, Artifact: baselineArtifact})
	}
	for _, reference := range manifest.ReferenceArtifacts {
		if baselineArtifact == nil || reference != *baselineArtifact {
			return contract.AnalysisPublication{}, contract.ErrAnalysisPublication
		}
	}
	for _, reference := range append([]run.Artifact{input.Candidate}, manifest.ReferenceArtifacts...) {
		if reference.ID == input.Report.ID || reference.URI == input.Report.URI {
			return contract.AnalysisPublication{}, contract.ErrAnalysisPublication
		}
	}
	if err := (policy.VerdictApprover{}).ApproveReportVerdicts(ctx, input.PolicyBytes, resolutions, manifest); err != nil {
		if ctx.Err() != nil {
			return contract.AnalysisPublication{}, ctx.Err()
		}
		return contract.AnalysisPublication{}, contract.ErrAnalysisPublication
	}
	message := contract.AnalysisPublication{SpecVersion: "1.0", ID: input.EventID, Source: input.Provider,
		Type: "dev.perfeng.integration.analysis-published.v1", Time: input.PublishedAt.UTC().Format(time.RFC3339Nano),
		DataContentType: "application/json", DataSchema: analysisPublicationSchemaURI,
		Data: contract.AnalysisPublicationData{Context: admission.Data.Context, Run: *admission.Data.Run, Native: *native,
			Artifact: contract.AnalysisArtifactClaim{ID: input.Report.ID, RunID: input.Report.RunID, Kind: "analysis", URI: input.Report.URI,
				SHA256: input.Report.SHA256, SizeBytes: json.Number(strconv.FormatInt(input.Report.SizeBytes, 10)), MediaType: input.Report.MediaType, Format: input.Report.Format}}}
	encoded, err := json.Marshal(message)
	if err != nil {
		return contract.AnalysisPublication{}, contract.ErrAnalysisPublication
	}
	// One schema-validated decode also owns context and baseline pointers.
	owned, err := DecodeAnalysisPublication(encoded)
	if err != nil {
		return contract.AnalysisPublication{}, contract.ErrAnalysisPublication
	}
	if err := ctx.Err(); err != nil {
		return contract.AnalysisPublication{}, err
	}

	return owned, nil
}

func publicationDigest(raw []byte) string {
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}
