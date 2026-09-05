// Package contract holds the reviewed, pinned run-management boundary.
package contract

import (
	"embed"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Files contains the reviewed run-management contract snapshot embedded into
// the control-plane binary; callers must use paths below snapshot/.
//
//go:embed snapshot
var Files embed.FS

type schema struct {
	Ref                  string            `json:"$ref"`
	Type                 string            `json:"type"`
	Pattern              string            `json:"pattern"`
	Enum                 []string          `json:"enum"`
	Const                any               `json:"const"`
	Required             []string          `json:"required"`
	Properties           map[string]schema `json:"properties"`
	AdditionalProperties *bool             `json:"additionalProperties"`
	AllOf                []schema          `json:"allOf"`
	OneOf                []schema          `json:"oneOf"`
	Items                *schema           `json:"items"`
	MinLength            *int              `json:"minLength"`
	MaxLength            *int              `json:"maxLength"`
	Minimum              *float64          `json:"minimum"`
	Maximum              *float64          `json:"maximum"`
	MinItems             *int              `json:"minItems"`
	UniqueItems          bool              `json:"uniqueItems"`
	pattern              *regexp.Regexp
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
	prepared, err := prepareSchemas(
		document.Components.Schemas,
		"CreateRun", "RunId", "CreateBaseline", "BaselineTransition", "ResourceId", "Version",
	)
	if err != nil {
		panic(err)
	}
	schemas = prepared
	read("transitions.json", &transitions)
}

func prepareSchemas(source map[string]schema, roots ...string) (map[string]schema, error) {
	prepared := make(map[string]schema, len(source))
	for name, definition := range source {
		compiled, err := compilePatterns(definition)
		if err != nil {
			return nil, fmt.Errorf("schema %s: %w", name, err)
		}
		prepared[name] = compiled
	}

	for _, root := range roots {
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
	for index, child := range definition.AllOf {
		compiled, err := compilePatterns(child)
		if err != nil {
			return schema{}, fmt.Errorf("allOf: %w", err)
		}
		definition.AllOf[index] = compiled
	}
	for index, child := range definition.OneOf {
		compiled, err := compilePatterns(child)
		if err != nil {
			return schema{}, fmt.Errorf("oneOf: %w", err)
		}
		definition.OneOf[index] = compiled
	}
	if definition.Items != nil {
		compiled, err := compilePatterns(*definition.Items)
		if err != nil {
			return schema{}, fmt.Errorf("items: %w", err)
		}
		definition.Items = &compiled
	}

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
	for _, child := range append(definition.AllOf, definition.OneOf...) {
		if err := validateSupportedSchema(child, all, visited); err != nil {
			return err
		}
	}
	if definition.Items != nil {
		if err := validateSupportedSchema(*definition.Items, all, visited); err != nil {
			return fmt.Errorf("items: %w", err)
		}
	}
	if definition.Ref != "" {
		name, ok := schemaReference(definition.Ref)
		if !ok {
			return fmt.Errorf("unsupported reference")
		}

		return validateSchemaDefinition(name, all, visited)
	}

	switch definition.Type {
	case "object", "":
		if definition.Type == "" && len(definition.Properties) == 0 {
			if definition.Const == nil && len(definition.AllOf) == 0 && len(definition.OneOf) == 0 {
				return fmt.Errorf("unsupported empty schema")
			}

			return nil
		}
		for name, property := range definition.Properties {
			if err := validateSupportedSchema(property, all, visited); err != nil {
				return fmt.Errorf("property %s: %w", name, err)
			}
		}
	case "string", "integer", "number", "array":
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

// ValidateCreate checks a decoded value against the pinned CreateRun schema.
func ValidateCreate(value any) error { return validate(schemas["CreateRun"], value) }

// ValidateBaselineCreate checks a decoded value against CreateBaseline.
func ValidateBaselineCreate(value any) error { return validate(schemas["CreateBaseline"], value) }

// ValidateBaselineTransition checks a decoded value against BaselineTransition.
func ValidateBaselineTransition(value any) error {
	return validate(schemas["BaselineTransition"], value)
}

func validate(s schema, value any) error {
	for _, child := range s.AllOf {
		if err := validate(child, value); err != nil {
			return err
		}
	}
	if len(s.OneOf) > 0 {
		matches := 0
		for _, child := range s.OneOf {
			if validate(child, value) == nil {
				matches++
			}
		}
		if matches != 1 {
			return fmt.Errorf("expected exactly one matching schema")
		}
	}
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
	if s.Const != nil && fmt.Sprint(value) != fmt.Sprint(s.Const) {
		return fmt.Errorf("invalid value")
	}
	switch s.Type {
	case "object", "":
		if s.Type == "" && len(s.Properties) == 0 {
			return nil
		}
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
				if s.AdditionalProperties != nil && !*s.AdditionalProperties {
					return fmt.Errorf("unknown property")
				}
				continue
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
		length := utf8.RuneCountInString(str)
		if s.MinLength != nil && length < *s.MinLength || s.MaxLength != nil && length > *s.MaxLength {
			return fmt.Errorf("invalid string length")
		}
	case "integer":
		number, ok := value.(json.Number)
		if !ok {
			return fmt.Errorf("expected integer")
		}
		integer, err := number.Int64()
		if err != nil || !withinBounds(float64(integer), s) {
			return fmt.Errorf("invalid integer")
		}
	case "number":
		number, ok := value.(json.Number)
		if !ok {
			return fmt.Errorf("expected number")
		}
		decimal, err := strconv.ParseFloat(string(number), 64)
		if err != nil || math.IsInf(decimal, 0) || math.IsNaN(decimal) || !withinBounds(decimal, s) {
			return fmt.Errorf("invalid number")
		}
	case "array":
		array, ok := value.([]any)
		if !ok {
			return fmt.Errorf("expected array")
		}
		if s.MinItems != nil && len(array) < *s.MinItems {
			return fmt.Errorf("too few items")
		}
		seen := make(map[string]struct{}, len(array))
		for _, item := range array {
			if s.Items != nil {
				if err := validate(*s.Items, item); err != nil {
					return err
				}
			}
			if s.UniqueItems {
				encoded, _ := json.Marshal(item)
				key := string(encoded)
				if _, exists := seen[key]; exists {
					return fmt.Errorf("duplicate item")
				}
				seen[key] = struct{}{}
			}
		}
	default:
		return fmt.Errorf("unsupported schema type")
	}

	return nil
}

func withinBounds(value float64, s schema) bool {
	return (s.Minimum == nil || value >= *s.Minimum) &&
		(s.Maximum == nil || value <= *s.Maximum)
}

// ValidID reports whether id has the pinned Run identifier syntax.
func ValidID(id string) bool { return schemas["RunId"].pattern.MatchString(id) }

// ValidResourceID reports whether id has the pinned resource identifier syntax.
func ValidResourceID(id string) bool { return schemas["ResourceId"].pattern.MatchString(id) }

// ValidVersion reports whether version has the pinned baseline version syntax.
func ValidVersion(version string) bool { return schemas["Version"].pattern.MatchString(version) }

// CanTransition reports whether the pinned lifecycle permits the directed edge.
func CanTransition(from, to string) bool { return slices.Contains(transitions[from], to) }

// Terminal reports whether the pinned lifecycle identifies state as terminal.
func Terminal(state string) bool {
	next, ok := transitions[state]
	return ok && len(next) == 0
}
