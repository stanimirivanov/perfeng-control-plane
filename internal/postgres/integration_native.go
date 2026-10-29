package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/integration"
	"github.com/stanimirivanov/perfeng-control-plane/internal/jsondocument"
	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

var _ integration.NativeRepository = (*Repository)(nil)

// AdmitIntegrationResolved retains native binding with new accepted admission.
// Review runs under the same event/request and baseline approval locks as legacy
// admission. Replay never invokes review or augments legacy unbound rows. Calls
// are concurrent-safe; commit errors can be uncertain and require identical retry.
func (r *Repository) AdmitIntegrationResolved(ctx context.Context, principal, provider string, message contract.IntegrationMessage, review integration.ResolvedReview) (integration.Record, error) {
	if review == nil {
		return integration.Record{}, run.ErrValidation
	}
	var resolved integration.AdmissionResolution
	bridge := func(ctx context.Context, principal string, claims contract.IntegrationContext, read func(context.Context, string, string, string) (baseline.Record, error)) (run.Request, run.ExecutionTarget, error) {
		var locked []byte
		reads := 0
		var err error
		resolved, err = review(ctx, principal, claims, func(ctx context.Context, owner, id, version string) (baseline.Record, error) {
			record, err := read(ctx, owner, id, version)
			if err == nil {
				reads++
				locked, err = json.Marshal(record)
			}

			return record, err
		})
		if err != nil {
			return run.Request{}, "", err
		}
		if resolved.Baseline != nil {
			encoded, err := json.Marshal(resolved.Baseline)
			if err != nil || reads != 1 || !bytes.Equal(encoded, locked) {
				return run.Request{}, "", run.ErrUnavailable
			}
		} else if reads != 0 {
			return run.Request{}, "", run.ErrUnavailable
		}

		return resolved.Request, resolved.Target, nil
	}

	return r.admitIntegration(ctx, principal, provider, message, bridge, func(record integration.Record) (contract.AnalysisNativeBinding, error) {
		return freezeIntegrationNative(record, resolved)
	})
}

func freezeIntegrationNative(record integration.Record, resolved integration.AdmissionResolution) (contract.AnalysisNativeBinding, error) {
	if record.Run == nil || resolved.Request != record.Run.Request || !resolved.Target.Valid() || resolved.Workload.Validate() != nil {
		return contract.AnalysisNativeBinding{}, run.ErrUnavailable
	}
	identity := func(value run.Reference) contract.AnalysisNativeIdentity {
		return contract.AnalysisNativeIdentity{ID: value.ID, Version: value.Version, SHA256: value.SHA256}
	}
	result := contract.AnalysisNativeBinding{RunID: record.Run.ID, TestID: resolved.Request.TestSuite,
		Workload: contract.AnalysisNativeIdentity{ID: resolved.Workload.ID, Version: resolved.Workload.Version, SHA256: resolved.Workload.SHA256}, Policy: identity(resolved.Request.Policy)}
	claims := record.Disposition.Data.Context
	if (claims.Baseline == nil) != (resolved.Baseline == nil) {
		return contract.AnalysisNativeBinding{}, run.ErrUnavailable
	}
	if source := resolved.Baseline; source != nil {
		if source.Validate() != nil || source.State != baseline.StateApproved || source.Workload != resolved.Workload || source.TestID != result.TestID ||
			(source.Environment.ID != resolved.Request.Environment.ID || source.Environment.Version != resolved.Request.Environment.Version || source.Environment.SHA256 != resolved.Request.Environment.SHA256) {
			return contract.AnalysisNativeBinding{}, run.ErrUnavailable
		}
		encoded, err := json.Marshal(source)
		if err != nil {
			return contract.AnalysisNativeBinding{}, run.ErrUnavailable
		}
		digest := sha256.Sum256(append(encoded, '\n'))
		pin := contract.CatalogReference{CatalogIdentity: contract.CatalogIdentity{Issuer: record.Disposition.Source, ID: source.ID}, Revision: source.Version + ":" + strconv.FormatInt(source.Revision, 10), SHA256: hex.EncodeToString(digest[:])}
		if *claims.Baseline != pin {
			return contract.AnalysisNativeBinding{}, run.ErrUnavailable
		}
		artifact := source.Artifact
		result.Baseline = &contract.AnalysisBaselineClaim{Identity: contract.AnalysisNativeIdentity{ID: source.ID, Version: source.Version, SHA256: pin.SHA256}, Artifact: contract.AnalysisArtifactClaim{
			ID: artifact.ID, RunID: artifact.RunID, Kind: artifact.Kind, URI: artifact.URI, SHA256: artifact.SHA256,
			SizeBytes: json.Number(strconv.FormatInt(artifact.SizeBytes, 10)), MediaType: artifact.MediaType, Format: artifact.Format}}
	}

	return result, nil
}

func restoreIntegrationNative(raw []byte, record integration.Record) (*contract.AnalysisNativeBinding, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if !jsondocument.Valid(raw, contract.AnalysisPublicationMaximumBytes) {
		return nil, run.ErrUnavailable
	}
	var result contract.AnalysisNativeBinding
	_, complete := jsondocument.ExactObject(raw, "runId", "testId", "workload", "policy", "baseline")
	if !jsondocument.Decode(raw, &result) || record.Run == nil ||
		!complete || !rawresult.ValidResourceID(result.TestID) ||
		result.RunID != record.Run.ID || result.TestID != record.Run.Request.TestSuite ||
		result.Policy != (contract.AnalysisNativeIdentity{ID: record.Run.Request.Policy.ID, Version: record.Run.Request.Policy.Version, SHA256: record.Run.Request.Policy.SHA256}) ||
		(rawresult.Identity{ID: result.Workload.ID, Version: result.Workload.Version, SHA256: result.Workload.SHA256}).Validate() != nil ||
		(result.Baseline == nil) != (record.Disposition.Data.Context.Baseline == nil) {
		return nil, run.ErrUnavailable
	}
	if value := result.Baseline; value != nil {
		pin := record.Disposition.Data.Context.Baseline
		encoded, err := json.Marshal(value.Artifact)
		var artifact run.Artifact
		if err != nil || json.Unmarshal(encoded, &artifact) != nil || artifact.Validate() != nil ||
			artifact.Kind != "normalized" || artifact.Format != "normalized-result/v1" || artifact.MediaType != "application/json" ||
			value.Identity.SHA256 != pin.SHA256 || value.Identity.ID != pin.ID || !strings.HasPrefix(pin.Revision, value.Identity.Version+":") ||
			(rawresult.Identity{ID: value.Identity.ID, Version: value.Identity.Version, SHA256: value.Identity.SHA256}).Validate() != nil {
			return nil, run.ErrUnavailable
		}
	}

	return &result, nil
}
