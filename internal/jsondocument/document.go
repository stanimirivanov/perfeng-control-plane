// Package jsondocument validates bounded contract JSON before typed decoding.
package jsondocument

import (
	"bytes"
	"encoding/json"
	"io"
	"unicode/utf8"
)

const maximumDepth = 64

// Valid reports whether data is one bounded UTF-8 JSON value with unique object
// keys and no nesting beyond the shared parser limit.
func Valid(data []byte, maximumBytes int) bool {
	if len(data) == 0 || maximumBytes < 1 || len(data) > maximumBytes || !utf8.Valid(data) {
		return false
	}

	decoder := json.NewDecoder(bytes.NewReader(data))

	return consumeUniqueValue(decoder, 0) == nil && atEnd(decoder)
}

// Decode reads one JSON value into destination, rejecting unknown typed fields
// and trailing content. Call Valid first to enforce duplicate-key and size rules.
func Decode(data []byte, destination any) bool {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	return decoder.Decode(destination) == nil && atEnd(decoder)
}

// ExactObject decodes an object and requires every named field exactly once.
// Duplicate keys must already have been rejected by Valid.
func ExactObject(data json.RawMessage, fields ...string) (map[string]json.RawMessage, bool) {
	var object map[string]json.RawMessage
	if json.Unmarshal(data, &object) != nil || len(object) != len(fields) {
		return nil, false
	}
	for _, field := range fields {
		if _, exists := object[field]; !exists {
			return nil, false
		}
	}

	return object, true
}

// ObjectFields decodes an object with exact required fields and a closed set of
// optional fields. Duplicate keys must already have been rejected by Valid.
func ObjectFields(
	data json.RawMessage,
	required []string,
	optional ...string,
) (map[string]json.RawMessage, bool) {
	var object map[string]json.RawMessage
	if json.Unmarshal(data, &object) != nil {
		return nil, false
	}
	allowed := make(map[string]struct{}, len(required)+len(optional))
	for _, field := range required {
		allowed[field] = struct{}{}
		if _, exists := object[field]; !exists {
			return nil, false
		}
	}
	for _, field := range optional {
		allowed[field] = struct{}{}
	}
	for field := range object {
		if _, exists := allowed[field]; !exists {
			return nil, false
		}
	}

	return object, true
}

func consumeUniqueValue(decoder *json.Decoder, depth int) error {
	if depth > maximumDepth {
		return io.ErrUnexpectedEOF
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}

	switch delimiter {
	case '{':
		return consumeUniqueObject(decoder, depth)
	case '[':
		return consumeUniqueArray(decoder, depth)
	default:
		return io.ErrUnexpectedEOF
	}
}

func consumeUniqueObject(decoder *json.Decoder, depth int) error {
	keys := make(map[string]struct{})
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := keyToken.(string)
		if !ok {
			return io.ErrUnexpectedEOF
		}
		if _, exists := keys[key]; exists {
			return io.ErrUnexpectedEOF
		}
		keys[key] = struct{}{}
		if err := consumeUniqueValue(decoder, depth+1); err != nil {
			return err
		}
	}

	return consumeEnd(decoder)
}

func consumeUniqueArray(decoder *json.Decoder, depth int) error {
	for decoder.More() {
		if err := consumeUniqueValue(decoder, depth+1); err != nil {
			return err
		}
	}

	return consumeEnd(decoder)
}

func consumeEnd(decoder *json.Decoder) error {
	_, err := decoder.Token()
	return err
}

func atEnd(decoder *json.Decoder) bool {
	_, err := decoder.Token()
	return err == io.EOF
}
