// Package contract holds the reviewed, pinned run-management boundary.
package contract

import (
	"embed"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
)

//go:embed snapshot
var Files embed.FS

type schema struct {
	Ref        string            `json:"$ref"`
	Type       string            `json:"type"`
	Pattern    string            `json:"pattern"`
	Enum       []string          `json:"enum"`
	Required   []string          `json:"required"`
	Properties map[string]schema `json:"properties"`
}

var schemas map[string]schema
var transitions map[string][]string

func init() {
	var document struct {
		Components struct {
			Schemas map[string]schema `json:"schemas"`
		} `json:"components"`
	}
	read := func(path string, target any) {
		b, err := Files.ReadFile("snapshot/" + path)
		if err != nil {
			panic(err)
		}
		if err = json.Unmarshal(b, target); err != nil {
			panic(err)
		}
	}
	read("openapi.json", &document)
	schemas = document.Components.Schemas
	read("transitions.json", &transitions)
}

// ValidateCreate checks only the object/string subset used by CreateRun.
// This is not a general JSON Schema or response validator.
func ValidateCreate(value any) error { return validate(schemas["CreateRun"], value) }

func validate(s schema, value any) error {
	if s.Ref != "" {
		const prefix = "#/components/schemas/"
		if len(s.Ref) <= len(prefix) || s.Ref[:len(prefix)] != prefix {
			return fmt.Errorf("unsupported reference")
		}
		resolved, ok := schemas[s.Ref[len(prefix):]]
		if !ok {
			return fmt.Errorf("unknown reference")
		}
		return validate(resolved, value)
	}
	switch s.Type {
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("expected object")
		}
		for _, key := range s.Required {
			if _, ok := object[key]; !ok {
				return fmt.Errorf("required property missing")
			}
		}
		for key, child := range object {
			property, ok := s.Properties[key]
			if !ok {
				return fmt.Errorf("unknown property")
			}
			if err := validate(property, child); err != nil {
				return err
			}
		}
	case "string":
		str, ok := value.(string)
		if !ok {
			return fmt.Errorf("expected string")
		}
		if s.Pattern != "" && !regexp.MustCompile(s.Pattern).MatchString(str) {
			return fmt.Errorf("invalid string")
		}
		if len(s.Enum) > 0 && !slices.Contains(s.Enum, str) {
			return fmt.Errorf("invalid value")
		}
	default:
		return fmt.Errorf("unsupported schema type")
	}
	return nil
}

func ValidID(id string) bool             { return regexp.MustCompile(schemas["RunId"].Pattern).MatchString(id) }
func CanTransition(from, to string) bool { return slices.Contains(transitions[from], to) }
func Terminal(state string) bool {
	next, ok := transitions[state]
	return ok && len(next) == 0
}
