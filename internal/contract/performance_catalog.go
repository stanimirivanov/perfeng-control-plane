package contract

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// CatalogIdentity preserves an authority-issued opaque identity.
type CatalogIdentity struct {
	Issuer string `json:"issuer"`
	ID     string `json:"id"`
}

// CatalogReference pins the exact immutable producer artifact.
type CatalogReference struct {
	CatalogIdentity
	Revision string `json:"revision"`
	SHA256   string `json:"sha256"`
}

// CatalogOwnerRoute contains reviewed routing identifiers, never destinations.
type CatalogOwnerRoute struct {
	Reference CatalogReference `json:"reference"`
	Owner     CatalogIdentity  `json:"owner"`
	Route     CatalogIdentity  `json:"route"`
}

// CatalogProjection is safe metadata, validated against the pinned schema before publication.
type CatalogProjection struct {
	Issuer, PrincipalIssuer, ProducerVersion string
	Items                                    []map[string]any
}

var catalogSchemas = loadCatalogSchemas()

func loadCatalogSchemas() map[string]schema {
	b, err := Files.ReadFile("snapshot/schemas/performance-catalog/v1/performance-catalog.schema.json")
	if err != nil {
		panic(err)
	}
	// Only local reference syntax changes; the pinned document remains byte-identical.
	b = bytes.ReplaceAll(b, []byte("#/$defs/"), []byte("#/components/schemas/"))
	var value any
	if err = json.Unmarshal(b, &value); err != nil {
		panic(err)
	}
	normalizeCatalogLiterals(value)
	b, err = json.Marshal(value)
	if err != nil {
		panic(err)
	}
	var document struct {
		Definitions map[string]schema `json:"$defs"`
	}
	if err = json.Unmarshal(b, &document); err != nil {
		panic(err)
	}
	var root schema
	if err = json.Unmarshal(b, &root); err != nil {
		panic(err)
	}
	document.Definitions["PerformanceCatalog"] = root
	prepared, err := prepareSchemas(document.Definitions, "PerformanceCatalog")
	if err != nil {
		panic(err)
	}
	return prepared
}

// Effect emits singleton numeric/Boolean enums; the existing validator models
// those exact literals as const. Multi-value/string enums remain unchanged.
func normalizeCatalogLiterals(value any) {
	switch item := value.(type) {
	case map[string]any:
		if values, ok := item["enum"].([]any); ok && len(values) == 1 {
			if _, stringValue := values[0].(string); !stringValue {
				item["const"] = values[0]
				delete(item, "enum")
			}
		}
		for _, child := range item {
			normalizeCatalogLiterals(child)
		}
	case []any:
		for _, child := range item {
			normalizeCatalogLiterals(child)
		}
	}
}

// ValidateCatalogPage checks shape, bounded fields and absolute issuer identities offline.
func ValidateCatalogPage(value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.UseNumber()
	if err = decoder.Decode(&value); err != nil {
		return err
	}
	if err := validateWithSchemas(catalogSchemas["PerformanceCatalog"], value, catalogSchemas); err != nil {
		return err
	}
	return catalogIssuers(value)
}

// ValidateCatalogResource rejects private fields and malformed immutable identities.
func ValidateCatalogResource(value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	var decoded any
	if err = json.Unmarshal(b, &decoded); err != nil {
		return err
	}
	if err = validateWithSchemas(*catalogSchemas["PerformanceCatalog"].Properties["items"].itemsSchema, decoded, catalogSchemas); err != nil {
		return err
	}
	return catalogIssuers(decoded)
}

func catalogIssuers(value any) error {
	switch item := value.(type) {
	case map[string]any:
		for key, child := range item {
			if key == "issuer" {
				raw, ok := child.(string)
				parsed, err := url.Parse(raw)
				if !ok || err != nil || !parsed.IsAbs() || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
					return fmt.Errorf("invalid catalog issuer")
				}
			}
			if err := catalogIssuers(child); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range item {
			if err := catalogIssuers(child); err != nil {
				return err
			}
		}
	}
	return nil
}

// CatalogDigest implements the contract's restricted canonical resource-array format.
// It rejects numeric/Boolean values: resources contain only strings, objects, arrays and null.
func CatalogDigest(value any) (string, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	var decoded any
	if err = json.Unmarshal(b, &decoded); err != nil {
		return "", err
	}
	if _, array := decoded.([]any); !array {
		return "", fmt.Errorf("expected catalog resource array")
	}
	var output strings.Builder
	if err = catalogCanonical(&output, decoded); err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(output.String()))
	return hex.EncodeToString(digest[:]), nil
}

func catalogCanonical(output *strings.Builder, value any) error {
	switch item := value.(type) {
	case nil:
		output.WriteString("null")
	case string:
		output.WriteByte('"')
		for _, character := range item {
			switch character {
			case '"', '\\':
				output.WriteByte('\\')
				output.WriteRune(character)
			case '\b':
				output.WriteString(`\b`)
			case '\f':
				output.WriteString(`\f`)
			case '\n':
				output.WriteString(`\n`)
			case '\r':
				output.WriteString(`\r`)
			case '\t':
				output.WriteString(`\t`)
			default:
				if character < 32 {
					fmt.Fprintf(output, `\u%04x`, character)
				} else {
					output.WriteRune(character)
				}
			}
		}
		output.WriteByte('"')
	case []any:
		output.WriteByte('[')
		for index, child := range item {
			if index > 0 {
				output.WriteByte(',')
			}
			if err := catalogCanonical(output, child); err != nil {
				return err
			}
		}
		output.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(item))
		for key := range item {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		output.WriteByte('{')
		for index, key := range keys {
			if index > 0 {
				output.WriteByte(',')
			}
			if err := catalogCanonical(output, key); err != nil {
				return err
			}
			output.WriteByte(':')
			if err := catalogCanonical(output, item[key]); err != nil {
				return err
			}
		}
		output.WriteByte('}')
	default:
		return fmt.Errorf("unsupported catalog digest value")
	}
	return nil
}
