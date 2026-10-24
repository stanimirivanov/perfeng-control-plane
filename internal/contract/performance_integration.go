package contract

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// IntegrationMaximumBytes bounds a single integration message before decoding.
const IntegrationMaximumBytes = 64 << 10

// ErrIntegration rejects invalid protocol messages without exposing their contents.
var ErrIntegration = errors.New("invalid performance integration message")

// IntegrationContext preserves immutable correlation and producer artifact pins.
// These transport claims do not authenticate callers or establish resource approval.
type IntegrationContext struct {
	Request     CatalogIdentity      `json:"request"`
	Attempt     CatalogIdentity      `json:"attempt"`
	Principal   CatalogIdentity      `json:"principal"`
	RequestedAt string               `json:"requestedAt"`
	Deadline    string               `json:"deadline"`
	Service     CatalogReference     `json:"service"`
	Capability  CatalogReference     `json:"capability"`
	Deployment  CatalogReference     `json:"deployment"`
	Environment CatalogReference     `json:"environment"`
	Workload    CatalogReference     `json:"workload"`
	Profile     CatalogReference     `json:"profile"`
	Policy      CatalogReference     `json:"policy"`
	Baseline    *CatalogReference    `json:"baseline"`
	Evidence    []CatalogReference   `json:"evidence"`
	Candidate   IntegrationCandidate `json:"candidate"`
}

// IntegrationCandidate binds the claimed source algorithm/digest and image digest.
// The provider must independently verify their association before admission.
type IntegrationCandidate struct {
	Source struct {
		Algorithm string `json:"algorithm"`
		Digest    string `json:"digest"`
	} `json:"source"`
	ImageDigest string `json:"imageDigest"`
}

// IntegrationError carries a schema-defined safe classification, never backend details.
type IntegrationError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// IntegrationData is a closed union selected by the envelope type and validated
// against the pinned schema. Revision retains exact JSON number syntax, including
// integer-valued exponents, within the interoperable safe-integer bound.
type IntegrationData struct {
	Context  IntegrationContext `json:"context"`
	Run      *CatalogIdentity   `json:"run,omitempty"`
	Error    *IntegrationError  `json:"error,omitempty"`
	Revision *json.Number       `json:"revision,omitempty"`
	State    string             `json:"state,omitempty"`
	Reason   string             `json:"reason,omitempty"`
}

// IntegrationMessage is the structured CloudEvents transport envelope. Use the
// decoder/encoder rather than direct JSON conversion to enforce its closed union.
type IntegrationMessage struct {
	SpecVersion     string          `json:"specversion"`
	ID              string          `json:"id"`
	Source          string          `json:"source"`
	Type            string          `json:"type"`
	Time            string          `json:"time"`
	DataContentType string          `json:"datacontenttype"`
	DataSchema      string          `json:"dataschema"`
	Data            IntegrationData `json:"data"`
}

// ValidContext checks published per-message time and identity relationships after
// schema validation. It does not authenticate claims or evaluate current freshness.
func (message IntegrationMessage) ValidContext() bool {
	context := message.Data.Context
	requested, requestErr := time.Parse(time.RFC3339Nano, context.RequestedAt)
	deadline, deadlineErr := time.Parse(time.RFC3339Nano, context.Deadline)
	event, eventErr := time.Parse(time.RFC3339Nano, message.Time)
	if requestErr != nil || deadlineErr != nil || eventErr != nil ||
		!deadline.After(requested) || event.Before(requested) || context.Request == context.Attempt {
		return false
	}
	kind := strings.TrimPrefix(message.Type, "dev.perfeng.integration.")
	if (kind == "requested.v1" || kind == "cancel-requested.v1") && message.Source != context.Request.Issuer {
		return false
	}

	return kind != "accepted.v1" || event.Before(deadline)
}
