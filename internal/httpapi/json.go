package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"
)

var errJSON = errors.New("invalid JSON")

// Parse tokens before schema validation: encoding/json otherwise accepts
// duplicate keys and repairs invalid UTF-8. Object keys remain case-sensitive.
func parseJSON(b []byte) (any, error) {
	if !utf8.Valid(b) {
		return nil, errJSON
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	value, err := readValue(d, 0)
	if err != nil {
		return nil, errJSON
	}
	if _, err = d.Token(); !errors.Is(err, io.EOF) {
		return nil, errJSON
	}

	return value, nil
}

func readValue(d *json.Decoder, depth int) (any, error) {
	if depth > 64 {
		return nil, errJSON
	}
	token, err := d.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return token, nil
	}
	switch delim {
	case '{':
		object := make(map[string]any)
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return nil, err
			}
			name, ok := key.(string)
			if !ok {
				return nil, errJSON
			}
			if _, exists := object[name]; exists {
				return nil, errJSON
			}
			value, err := readValue(d, depth+1)
			if err != nil {
				return nil, err
			}
			object[name] = value
		}
		end, err := d.Token()
		if err != nil || end != json.Delim('}') {
			return nil, errJSON
		}

		return object, nil
	case '[':
		array := make([]any, 0)
		for d.More() {
			value, err := readValue(d, depth+1)
			if err != nil {
				return nil, err
			}
			array = append(array, value)
		}
		end, err := d.Token()
		if err != nil || end != json.Delim(']') {
			return nil, errJSON
		}

		return array, nil
	default:
		return nil, errJSON
	}
}
