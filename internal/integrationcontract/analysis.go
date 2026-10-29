package integrationcontract

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/jsondocument"
)

const analysisPublicationSchemaURI = "https://perfeng.dev/schemas/performance-analysis/v1/performance-analysis.schema.json"

var analysisPublicationSchema = func() *jsonschema.Schema {
	raw, err := contract.Files.ReadFile("snapshot/schemas/performance-analysis/v1/performance-analysis.schema.json")
	if err != nil {
		panic(err)
	}
	schema, err := compileAnalysisPublicationSchema(raw)
	if err != nil {
		panic(err)
	}

	return schema
}()

func compileAnalysisPublicationSchema(raw []byte) (*jsonschema.Schema, error) {
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	compiler.UseLoader(jsonschema.SchemeURLLoader{})
	if err := compiler.AddResource(analysisPublicationSchemaURI, document); err != nil {
		return nil, err
	}

	return compiler.Compile(analysisPublicationSchemaURI)
}

// DecodeAnalysisPublication returns owned conforming claims or zero output and a
// redacted error. Calls are concurrent-safe and use no I/O, clock or credentials.
// Authentication, admission/native binding, artifact bytes, policy and replay
// checks remain caller duties before any decision or publication authority.
func DecodeAnalysisPublication(raw []byte) (contract.AnalysisPublication, error) {
	if !jsondocument.Valid(raw, contract.AnalysisPublicationMaximumBytes) || !publicationUnicode(raw) || !publicationNumbers(raw) {
		return contract.AnalysisPublication{}, contract.ErrAnalysisPublication
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if header, ok := document.(map[string]any); ok {
		for field, expected := range map[string]string{"specversion": "1.0", "dataschema": analysisPublicationSchemaURI} {
			if value, ok := header[field].(string); ok && value != expected {
				return contract.AnalysisPublication{}, errors.Join(contract.ErrAnalysisPublication, contract.ErrAnalysisPublicationVersion)
			}
		}
		if kind, ok := header["type"].(string); ok && strings.HasPrefix(kind, "dev.perfeng.integration.") && !strings.HasSuffix(kind, ".v1") {
			return contract.AnalysisPublication{}, errors.Join(contract.ErrAnalysisPublication, contract.ErrAnalysisPublicationVersion)
		}
	}
	if err != nil || analysisPublicationSchema.Validate(document) != nil {
		return contract.AnalysisPublication{}, contract.ErrAnalysisPublication
	}
	var message contract.AnalysisPublication
	if !jsondocument.Decode(raw, &message) || !message.ValidContext() {
		return contract.AnalysisPublication{}, contract.ErrAnalysisPublication
	}

	return message, nil
}

// EncodeAnalysisPublication validates all outbound claims without normalizing
// numeric syntax or authenticating evidence. Do not mutate inputs concurrently.
func EncodeAnalysisPublication(message contract.AnalysisPublication) ([]byte, error) {
	raw, err := json.Marshal(message)
	if err != nil {
		return nil, contract.ErrAnalysisPublication
	}
	if _, err := DecodeAnalysisPublication(raw); err != nil {
		return nil, err
	}

	return raw, nil
}

// Valid JSON is already established. Reject escaped unpaired surrogates that
// Go would replace, preserving exact identity claims across implementations.
func publicationUnicode(raw []byte) bool {
	for index := 0; index < len(raw); index++ {
		if raw[index] != '\\' {
			continue
		}
		index++
		if raw[index] != 'u' {
			continue
		}
		code, err := strconv.ParseUint(string(raw[index+1:index+5]), 16, 16)
		if err != nil {
			return false
		}
		if utf16.IsSurrogate(rune(code)) {
			if index+11 > len(raw) || string(raw[index+5:index+7]) != `\u` {
				return false
			}
			low, err := strconv.ParseUint(string(raw[index+7:index+11]), 16, 16)
			if err != nil || utf16.DecodeRune(rune(code), rune(low)) == utf8.RuneError {
				return false
			}
			index += 6
		}
		index += 4
	}

	return true
}

// Bound rational expansion before the schema validator performs arithmetic.
func publicationNumbers(raw []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	for {
		token, err := decoder.Token()
		if err != nil {
			return errors.Is(err, io.EOF)
		}
		if number, ok := token.(json.Number); ok {
			if len(number) > 128 {
				return false
			}
			if index := strings.IndexAny(string(number), "eE"); index >= 0 {
				exponent, err := strconv.Atoi(string(number[index+1:]))
				if err != nil || exponent < -999 || exponent > 999 {
					return false
				}
			}
		}
	}
}
