package postgres

import (
	"github.com/stanimirivanov/perfeng-control-plane/internal/integration"
	"github.com/stanimirivanov/perfeng-control-plane/internal/jsondocument"
	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

// Policy bytes use base64 JSON encoding to preserve their exact original bytes.
// This is private persistence format, not a shared interchange contract.
type verificationDocument struct {
	PolicyBytes      []byte             `json:"policyBytes"`
	Producer         rawresult.Producer `json:"producer"`
	ContractsVersion string             `json:"contractsVersion"`
}

func restoreIntegrationVerification(raw []byte, record integration.Record) (*integration.AnalysisVerification, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if !jsondocument.Valid(raw, 2<<20) || record.Native == nil || record.Run == nil {
		return nil, run.ErrUnavailable
	}
	var document verificationDocument
	_, complete := jsondocument.ExactObject(raw, "policyBytes", "producer", "contractsVersion")
	if !complete || !jsondocument.Decode(raw, &document) {
		return nil, run.ErrUnavailable
	}
	evidence := integration.AnalysisVerification{PolicyBytes: document.PolicyBytes, Producer: document.Producer, ContractsVersion: document.ContractsVersion}
	if evidence.Validate(record.Run.Request) != nil {
		return nil, run.ErrUnavailable
	}

	return &evidence, nil
}
