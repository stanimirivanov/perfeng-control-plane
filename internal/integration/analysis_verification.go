package integration

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/stanimirivanov/perfeng-control-plane/internal/policy"
	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

// AnalysisVerification carries exact policy bytes and independently reviewed
// report provenance. It is not authenticated or durable merely by being valid;
// callers must establish its trusted origin before historical retention/use.
// Inputs must not be mutated concurrently with Validate or Clone.
type AnalysisVerification struct {
	PolicyBytes      []byte
	Producer         rawresult.Producer
	ContractsVersion string
}

// Validate checks bounded policy semantics, the exact native policy pin, producer
// identity and a concrete bundle version without I/O or current eligibility reads.
// Failures return only the classified validation error, never policy contents.
func (evidence AnalysisVerification) Validate(request run.Request) error {
	if request.Validate() != nil || evidence.Producer.Validate() != nil || !rawresult.ValidContractsVersion(evidence.ContractsVersion) {
		return run.ErrValidation
	}
	document, err := policy.Parse(evidence.PolicyBytes)
	if err != nil || document.Metadata.Name != request.Policy.ID || document.Metadata.Version != request.Policy.Version {
		return run.ErrValidation
	}
	digest := sha256.Sum256(evidence.PolicyBytes)
	if hex.EncodeToString(digest[:]) != request.Policy.SHA256 {
		return run.ErrValidation
	}

	return nil
}

// Clone preserves exact policy bytes and independently owns nested producer data.
func (evidence AnalysisVerification) Clone() AnalysisVerification {
	evidence.PolicyBytes = append([]byte(nil), evidence.PolicyBytes...)
	evidence.Producer = evidence.Producer.Clone()

	return evidence
}
