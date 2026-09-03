// Package contract holds the reviewed, pinned run-management boundary.
package contract

import (
	"embed"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
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
	pattern    *regexp.Regexp
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
	prepared, err := prepareSchemas(document.Components.Schemas)
	if err != nil {
		panic(err)
	}
	schemas = prepared
	read("transitions.json", &transitions)
}

func prepareSchemas(source map[string]schema) (map[string]schema, error) {
	prepared := make(map[string]schema, len(source))
	for name, definition := range source {
		compiled, err := compilePatterns(definition)
		if err != nil {
			return nil, fmt.Errorf("schema %s: %w", name, err)
		}
		prepared[name] = compiled
	}

	for _, root := range []string{"CreateRun", "RunId"} {
		if err := validateSchemaDefinition(root, prepared, make(map[string]bool)); err != nil {
			return nil, err
		}
	}

	return prepared, nil
}

func compilePatterns(definition schema) (schema, error) {
	if definition.Pattern != "" {
		compiled, err := regexp.Compile(definition.Pattern)
		if err != nil {
			return schema{}, fmt.Errorf("invalid pattern: %w", err)
		}
		definition.pattern = compiled
	}

	properties := make(map[string]schema, len(definition.Properties))
	for name, property := range definition.Properties {
		compiled, err := compilePatterns(property)
		if err != nil {
			return schema{}, fmt.Errorf("property %s: %w", name, err)
		}
		properties[name] = compiled
	}
	definition.Properties = properties

	return definition, nil
}

func validateSchemaDefinition(
	name string,
	all map[string]schema,
	visited map[string]bool,
) error {
	if visited[name] {
		return nil
	}

	definition, ok := all[name]
	if !ok {
		return fmt.Errorf("unknown schema %s", name)
	}
	visited[name] = true

	return validateSupportedSchema(definition, all, visited)
}

func validateSupportedSchema(
	definition schema,
	all map[string]schema,
	visited map[string]bool,
) error {
	if definition.Ref != "" {
		name, ok := schemaReference(definition.Ref)
		if !ok {
			return fmt.Errorf("unsupported reference")
		}
		return validateSchemaDefinition(name, all, visited)
	}

	switch definition.Type {
	case "object":
		for name, property := range definition.Properties {
			if err := validateSupportedSchema(property, all, visited); err != nil {
				return fmt.Errorf("property %s: %w", name, err)
			}
		}
	case "string":
	default:
		return fmt.Errorf("unsupported schema type %q", definition.Type)
	}

	return nil
}

func schemaReference(ref string) (string, bool) {
	const prefix = "#/components/schemas/"
	name, ok := strings.CutPrefix(ref, prefix)

	return name, ok && name != ""
}

// ValidateCreate checks only the object/string subset used by CreateRun.
// This is not a general JSON Schema or response validator.
func ValidateCreate(value any) error { return validate(schemas["CreateRun"], value) }

func validate(s schema, value any) error {
	if s.Ref != "" {
		name, ok := schemaReference(s.Ref)
		if !ok {
			return fmt.Errorf("unsupported reference")
		}
		resolved, ok := schemas[name]
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
		if s.pattern != nil && !s.pattern.MatchString(str) {
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

func ValidID(id string) bool             { return schemas["RunId"].pattern.MatchString(id) }
func CanTransition(from, to string) bool { return slices.Contains(transitions[from], to) }
func Terminal(state string) bool {
	next, ok := transitions[state]
	return ok && len(next) == 0
}
