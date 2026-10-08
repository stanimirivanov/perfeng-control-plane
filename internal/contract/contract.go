// Package contract holds reviewed, pinned API and data-contract boundaries.
package contract

import (
	"bytes"
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

// Files contains the reviewed contract snapshot embedded into
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
	Items                json.RawMessage   `json:"items"`
	PrefixItems          []schema          `json:"prefixItems"`
	If                   *schema           `json:"if"`
	Then                 *schema           `json:"then"`
	Else                 *schema           `json:"else"`
	Not                  *schema           `json:"not"`
	MinLength            *int              `json:"minLength"`
	MaxLength            *int              `json:"maxLength"`
	Minimum              *float64          `json:"minimum"`
	Maximum              *float64          `json:"maximum"`
	MinItems             *int              `json:"minItems"`
	MaxItems             *int              `json:"maxItems"`
	UniqueItems          bool              `json:"uniqueItems"`
	pattern              *regexp.Regexp
	itemsSchema          *schema
	itemsForbidden       bool
}

var schemas map[string]schema
var nativeWorkerV2Schemas map[string]schema
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
	var nativeWorkerV2Document struct {
		Components struct {
			Schemas map[string]schema `json:"schemas"`
		} `json:"components"`
	}
	read("api/native-worker/v2/openapi.json", &nativeWorkerV2Document)
	nativeWorkerV2Schemas, err = prepareSchemas(
		nativeWorkerV2Document.Components.Schemas,
		"WorkerPresence", "PresenceStatus", "AcquireLease", "Lease", "ExecutionRecipe", "LeaseRenewal",
		"RenewLease", "ReleaseLease", "UploadGrantRequest", "UploadGrant",
		"RegisterArtifact", "ArtifactReference", "TerminalReport", "TerminalAck",
		"InspectAttempt", "AttemptInspection", "Error",
	)
	if err != nil {
		panic(err)
	}
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
	if len(definition.Items) != 0 {
		if bytes.Equal(bytes.TrimSpace(definition.Items), []byte("false")) {
			definition.itemsForbidden = true
		} else {
			var item schema
			if err := json.Unmarshal(definition.Items, &item); err != nil {
				return schema{}, fmt.Errorf("items: %w", err)
			}
			compiled, err := compilePatterns(item)
			if err != nil {
				return schema{}, fmt.Errorf("items: %w", err)
			}
			definition.itemsSchema = &compiled
		}
	}
	for index, child := range definition.PrefixItems {
		compiled, err := compilePatterns(child)
		if err != nil {
			return schema{}, fmt.Errorf("prefixItems: %w", err)
		}
		definition.PrefixItems[index] = compiled
	}
	for _, child := range []*schema{definition.If, definition.Then, definition.Else, definition.Not} {
		if child == nil {
			continue
		}
		compiled, err := compilePatterns(*child)
		if err != nil {
			return schema{}, err
		}
		*child = compiled
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
	if definition.itemsSchema != nil {
		if err := validateSupportedSchema(*definition.itemsSchema, all, visited); err != nil {
			return fmt.Errorf("items: %w", err)
		}
	}
	for _, child := range definition.PrefixItems {
		if err := validateSupportedSchema(child, all, visited); err != nil {
			return fmt.Errorf("prefixItems: %w", err)
		}
	}
	for _, child := range []*schema{definition.If, definition.Then, definition.Else, definition.Not} {
		if child != nil {
			if err := validateSupportedSchema(*child, all, visited); err != nil {
				return err
			}
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
		if definition.Type == "" && len(definition.Properties) == 0 &&
			len(definition.Required) == 0 && definition.AdditionalProperties == nil {
			if definition.Const == nil && len(definition.Enum) == 0 &&
				len(definition.AllOf) == 0 && len(definition.OneOf) == 0 &&
				definition.If == nil && definition.Not == nil &&
				len(definition.Items) == 0 && len(definition.PrefixItems) == 0 &&
				definition.MinItems == nil && definition.MaxItems == nil {
				return fmt.Errorf("unsupported empty schema")
			}

			return nil
		}
		for name, property := range definition.Properties {
			if err := validateSupportedSchema(property, all, visited); err != nil {
				return fmt.Errorf("property %s: %w", name, err)
			}
		}
	case "string", "integer", "number", "boolean", "array":
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

// ValidateNativeWorkerV2Presence checks the candidate v2 presence document.
// It does not make the server's qualification or authorization decision.
func ValidateNativeWorkerV2Presence(value any) error {
	return validateWithSchemas(nativeWorkerV2Schemas["WorkerPresence"], value, nativeWorkerV2Schemas)
}

// ValidateNativeWorkerV2PresenceStatus checks a candidate v2 presence response.
func ValidateNativeWorkerV2PresenceStatus(value any) error {
	return validateWithSchemas(nativeWorkerV2Schemas["PresenceStatus"], value, nativeWorkerV2Schemas)
}

// ValidateNativeWorkerV2AcquireLease checks a candidate v2 acquisition request.
func ValidateNativeWorkerV2AcquireLease(value any) error {
	return validateWithSchemas(nativeWorkerV2Schemas["AcquireLease"], value, nativeWorkerV2Schemas)
}

// ValidateNativeWorkerV2Lease checks a candidate v2 lease including its closed
// execution recipe and ordered artifact plan.
func ValidateNativeWorkerV2Lease(value any) error {
	return validateWithSchemas(nativeWorkerV2Schemas["Lease"], value, nativeWorkerV2Schemas)
}

// ValidateNativeWorkerV2ExecutionRecipe checks the candidate v2 recipe shape.
func ValidateNativeWorkerV2ExecutionRecipe(value any) error {
	return validateWithSchemas(nativeWorkerV2Schemas["ExecutionRecipe"], value, nativeWorkerV2Schemas)
}

// ValidateNativeWorkerV2LeaseRenewal checks cancellation and stop-deadline
// consistency in a candidate v2 renewal response.
func ValidateNativeWorkerV2LeaseRenewal(value any) error {
	return validateWithSchemas(nativeWorkerV2Schemas["LeaseRenewal"], value, nativeWorkerV2Schemas)
}

// ValidateNativeWorkerV2RenewLease checks a candidate v2 renewal request.
func ValidateNativeWorkerV2RenewLease(value any) error {
	return validateWithSchemas(nativeWorkerV2Schemas["RenewLease"], value, nativeWorkerV2Schemas)
}

// ValidateNativeWorkerV2ReleaseLease checks a candidate v2 release request.
func ValidateNativeWorkerV2ReleaseLease(value any) error {
	return validateWithSchemas(nativeWorkerV2Schemas["ReleaseLease"], value, nativeWorkerV2Schemas)
}

// ValidateNativeWorkerV2UploadGrantRequest checks a candidate v2 upload request.
func ValidateNativeWorkerV2UploadGrantRequest(value any) error {
	return validateWithSchemas(nativeWorkerV2Schemas["UploadGrantRequest"], value, nativeWorkerV2Schemas)
}

// ValidateNativeWorkerV2UploadGrant checks a candidate v2 upload grant response.
func ValidateNativeWorkerV2UploadGrant(value any) error {
	return validateWithSchemas(nativeWorkerV2Schemas["UploadGrant"], value, nativeWorkerV2Schemas)
}

// ValidateNativeWorkerV2RegisterArtifact checks a candidate v2 artifact registration.
func ValidateNativeWorkerV2RegisterArtifact(value any) error {
	return validateWithSchemas(nativeWorkerV2Schemas["RegisterArtifact"], value, nativeWorkerV2Schemas)
}

// ValidateNativeWorkerV2ArtifactReference checks a candidate v2 artifact response.
func ValidateNativeWorkerV2ArtifactReference(value any) error {
	return validateWithSchemas(nativeWorkerV2Schemas["ArtifactReference"], value, nativeWorkerV2Schemas)
}

// ValidateNativeWorkerV2TerminalReport checks a candidate v2 terminal outcome.
func ValidateNativeWorkerV2TerminalReport(value any) error {
	return validateWithSchemas(nativeWorkerV2Schemas["TerminalReport"], value, nativeWorkerV2Schemas)
}

// ValidateNativeWorkerV2TerminalAcknowledgement checks a candidate v2 terminal acknowledgement.
func ValidateNativeWorkerV2TerminalAcknowledgement(value any) error {
	return validateWithSchemas(nativeWorkerV2Schemas["TerminalAck"], value, nativeWorkerV2Schemas)
}

// ValidateNativeWorkerV2InspectAttempt checks a worker-scoped v2 inspection request.
func ValidateNativeWorkerV2InspectAttempt(value any) error {
	return validateWithSchemas(nativeWorkerV2Schemas["InspectAttempt"], value, nativeWorkerV2Schemas)
}

// ValidateNativeWorkerV2AttemptInspection checks the closed read-only v2 snapshot.
func ValidateNativeWorkerV2AttemptInspection(value any) error {
	return validateWithSchemas(nativeWorkerV2Schemas["AttemptInspection"], value, nativeWorkerV2Schemas)
}

// ValidateNativeWorkerV2Error checks a candidate v2 protocol error response.
func ValidateNativeWorkerV2Error(value any) error {
	return validateWithSchemas(nativeWorkerV2Schemas["Error"], value, nativeWorkerV2Schemas)
}

// ValidateWorkerQualificationEvidence checks server-configured evidence against
// the same pinned schema used by WorkerPresence.
func ValidateWorkerQualificationEvidence(value any) error {
	return validateWithSchemas(
		nativeWorkerV2Schemas["QualificationEvidence"],
		value,
		nativeWorkerV2Schemas,
	)
}

// ValidNativeWorkerIdentifier reports whether value satisfies the shared
// Native Worker API Identifier schema.
func ValidNativeWorkerIdentifier(value string) bool {
	return validateWithSchemas(nativeWorkerV2Schemas["Identifier"], value, nativeWorkerV2Schemas) == nil
}

// ValidNativeArtifactFormat reports whether value satisfies the shared
// artifact-format grammar embedded in Native Worker API v2.
func ValidNativeArtifactFormat(value string) bool {
	return validateWithSchemas(nativeWorkerV2Schemas["ArtifactFormat"], value, nativeWorkerV2Schemas) == nil
}

func validate(s schema, value any) error {
	return validateWithSchemas(s, value, schemas)
}

func validateWithSchemas(s schema, value any, all map[string]schema) error {
	for _, child := range s.AllOf {
		if err := validateWithSchemas(child, value, all); err != nil {
			return err
		}
	}
	if len(s.OneOf) > 0 {
		matches := 0
		for _, child := range s.OneOf {
			if validateWithSchemas(child, value, all) == nil {
				matches++
			}
		}
		if matches != 1 {
			return fmt.Errorf("expected exactly one matching schema")
		}
	}
	if s.If != nil {
		branch := s.Else
		if validateWithSchemas(*s.If, value, all) == nil {
			branch = s.Then
		}
		if branch != nil {
			if err := validateWithSchemas(*branch, value, all); err != nil {
				return err
			}
		}
	}
	if s.Not != nil && validateWithSchemas(*s.Not, value, all) == nil {
		return fmt.Errorf("excluded value")
	}
	if s.Ref != "" {
		name, ok := schemaReference(s.Ref)
		if !ok {
			return fmt.Errorf("unsupported reference")
		}
		resolved, ok := all[name]
		if !ok {
			return fmt.Errorf("unknown reference")
		}

		return validateWithSchemas(resolved, value, all)
	}
	if s.Const != nil && fmt.Sprint(value) != fmt.Sprint(s.Const) {
		return fmt.Errorf("invalid value")
	}
	if s.Type == "" && len(s.Enum) > 0 {
		str, ok := value.(string)
		if !ok || !slices.Contains(s.Enum, str) {
			return fmt.Errorf("invalid value")
		}

		return nil
	}
	kind := s.Type
	if kind == "" {
		switch {
		case len(s.Properties) > 0 || len(s.Required) > 0 || s.AdditionalProperties != nil:
			kind = "object"
		case s.MinItems != nil || s.MaxItems != nil || len(s.PrefixItems) > 0 || len(s.Items) > 0:
			kind = "array"
		default:
			return nil
		}
	}
	switch kind {
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
				if s.AdditionalProperties != nil && !*s.AdditionalProperties {
					return fmt.Errorf("unknown property")
				}
				continue
			}
			if err := validateWithSchemas(property, child, all); err != nil {
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
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("expected boolean")
		}
	case "array":
		array, ok := value.([]any)
		if !ok {
			return fmt.Errorf("expected array")
		}
		if s.MinItems != nil && len(array) < *s.MinItems {
			return fmt.Errorf("too few items")
		}
		if s.MaxItems != nil && len(array) > *s.MaxItems {
			return fmt.Errorf("too many items")
		}
		seen := make(map[string]struct{}, len(array))
		for index, item := range array {
			if index < len(s.PrefixItems) {
				if err := validateWithSchemas(s.PrefixItems[index], item, all); err != nil {
					return err
				}
			} else if s.itemsForbidden {
				return fmt.Errorf("additional item")
			} else if s.itemsSchema != nil {
				if err := validateWithSchemas(*s.itemsSchema, item, all); err != nil {
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
