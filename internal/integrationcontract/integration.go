// Package integrationcontract validates the pinned performance integration wire boundary.
package integrationcontract

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/jsondocument"
)

const integrationSchemaURI = "https://perfeng.dev/schemas/performance-integration/v1/performance-integration.schema.json"

var integrationSchema = loadIntegrationSchema()

func loadIntegrationSchema() *jsonschema.Schema {
	data, err := contract.Files.ReadFile("snapshot/schemas/performance-integration/v1/performance-integration.schema.json")
	if err != nil {
		panic(err)
	}
	compiled, err := compileIntegrationSchema(data)
	if err != nil {
		panic(err)
	}

	return compiled
}

func compileIntegrationSchema(data []byte) (*jsonschema.Schema, error) {
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	// No network or filesystem loaders: all references must resolve in this resource
	// or the validator's embedded standard metaschemas.
	compiler.UseLoader(jsonschema.SchemeURLLoader{})
	if err := compiler.AddResource(integrationSchemaURI, document); err != nil {
		return nil, err
	}

	return compiler.Compile(integrationSchemaURI)
}

// DecodeIntegrationMessage validates bounded unique-key JSON, the complete pinned
// schema and published per-message identity/time rules. It returns owned values or
// a zero message on failure, and is safe to call concurrently. It performs no I/O,
// wall-clock lookup, authentication, admission or cross-message state validation.
func DecodeIntegrationMessage(raw []byte) (contract.IntegrationMessage, error) {
	if !jsondocument.Valid(raw, contract.IntegrationMaximumBytes) {
		return contract.IntegrationMessage{}, contract.ErrIntegration
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if header, ok := document.(map[string]any); ok {
		for field, expected := range map[string]string{"specversion": "1.0", "dataschema": integrationSchemaURI} {
			if value, ok := header[field].(string); ok && value != "" && value != expected {
				return contract.IntegrationMessage{}, errors.Join(contract.ErrIntegration, contract.ErrIntegrationVersion)
			}
		}
		if kind, ok := header["type"].(string); ok && strings.HasPrefix(kind, "dev.perfeng.integration.") && !strings.HasSuffix(kind, ".v1") {
			return contract.IntegrationMessage{}, errors.Join(contract.ErrIntegration, contract.ErrIntegrationVersion)
		}
	}
	if err != nil || integrationSchema.Validate(document) != nil {
		return contract.IntegrationMessage{}, contract.ErrIntegration
	}
	var message contract.IntegrationMessage
	if !jsondocument.Decode(raw, &message) || !message.ValidContext() {
		return contract.IntegrationMessage{}, contract.ErrIntegration
	}

	return message, nil
}

// EncodeIntegrationMessage validates outbound transport values before returning
// bytes. Callers must not mutate message-owned slices/pointers concurrently.
// No partial output is returned for invalid or unsupported messages.
func EncodeIntegrationMessage(message contract.IntegrationMessage) ([]byte, error) {
	raw, err := json.Marshal(message)
	if err != nil {
		return nil, contract.ErrIntegration
	}
	if _, err := DecodeIntegrationMessage(raw); err != nil {
		return nil, err
	}

	return raw, nil
}
