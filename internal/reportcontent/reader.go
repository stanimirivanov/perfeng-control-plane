// Package reportcontent resolves and validates exact retained Analysis Report bytes.
package reportcontent

import (
	"bytes"
	"context"
	"sync"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/jsondocument"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

// MaxBytes is the maximum registered and delivered report size in Run API 0.9.0.
const MaxBytes = 1 << 20

// Registrations looks up immutable direct registrations within an owned Run.
type Registrations interface {
	// GetArtifact returns a single principal-scoped registration or run.ErrNotFound.
	GetArtifact(ctx context.Context, principal, runID, artifactID string) (run.Artifact, error)
}

// Objects resolves retained references using the provider's approved storage.
type Objects interface {
	// Read honors cancellation, bounds the stream to registered size plus one,
	// and returns original bytes only after verifying registered length and SHA-256.
	Read(context.Context, run.Artifact) ([]byte, error)
}

// Reader binds a scoped registration to its schema-validated original object.
// It is safe for concurrent use with concurrent-safe dependencies.
type Reader struct {
	registrations Registrations
	objects       Objects
	timeout       time.Duration
	schema        *jsonschema.Schema
}

// New admits the embedded schema offline and requires a finite read deadline.
// An unconfigured object provider yields scoped unavailability after association.
func New(registrations Registrations, objects Objects, timeout time.Duration) (*Reader, error) {
	if registrations == nil || timeout <= 0 {
		return nil, run.ErrValidation
	}
	schema, err := loadSchema()
	if err != nil {
		return nil, err
	}

	return &Reader{registrations: registrations, objects: objects, timeout: timeout, schema: schema}, nil
}

var loadSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	const uri = "https://perfeng.dev/schemas/analysis/v1/analysis-result.schema.json"
	raw, err := contract.ReportContentFiles.ReadFile("report-content/schemas/analysis/v1/analysis-result.schema.json")
	if err != nil {
		return nil, err
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	compiler.UseLoader(jsonschema.SchemeURLLoader{})
	if err := compiler.AddResource(uri, document); err != nil {
		return nil, err
	}

	return compiler.Compile(uri)
})

// Read requires the principal already authorized for this Run. It returns
// independently owned bytes or a zero body. An ineligible registration is
// invisible, advertised overflow is invalid, and object failures are unavailable.
func (r *Reader) Read(ctx context.Context, principal, runID, artifactID string) ([]byte, error) {
	if principal == "" {
		return nil, run.ErrNotFound
	}
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	ref, err := r.registrations.GetArtifact(ctx, principal, runID, artifactID)
	if err != nil {
		return nil, err
	}
	if ref.ID != artifactID || ref.RunID != runID || ref.Kind != "normalized" ||
		ref.Format != "analysis-result/v1" || ref.MediaType != "application/json" {
		return nil, run.ErrNotFound
	}
	if ref.SizeBytes > MaxBytes {
		return nil, run.ErrValidation
	}
	if r.objects == nil || ref.SizeBytes <= 0 || ref.Validate() != nil || ctx.Err() != nil {
		return nil, run.ErrUnavailable
	}

	raw, err := r.objects.Read(ctx, ref)
	if err != nil || !r.valid(raw, runID) || ctx.Err() != nil {
		return nil, run.ErrUnavailable
	}

	return raw, nil
}

func (r *Reader) valid(raw []byte, runID string) bool {
	if !jsondocument.ValidSchemaInput(raw, MaxBytes) {
		return false
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil || r.schema.Validate(document) != nil {
		return false
	}
	report, ok := document.(map[string]any)
	if !ok || report["runId"] != runID {
		return false
	}
	candidate, ok := report["candidateArtifact"].(map[string]any)

	return ok && candidate["runId"] == runID
}
