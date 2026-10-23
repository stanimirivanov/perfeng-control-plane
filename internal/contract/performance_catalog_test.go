package contract

import (
	"encoding/json"
	"testing"
)

func TestCatalogPinnedFixturesAndDigest(t *testing.T) {
	var lock struct {
		PerformanceCatalog struct{ Commit, BundleVersion string }
	}
	b, _ := Files.ReadFile("snapshot/lock.json")
	if err := json.Unmarshal(b, &lock); err != nil {
		t.Fatal(err)
	}
	if lock.PerformanceCatalog.Commit != "041273947703542f7fe22eab838c176df5517974" || lock.PerformanceCatalog.BundleVersion != "0.24.0" {
		t.Fatal("unreviewed catalog source")
	}
	for _, name := range []string{"pages", "empty"} {
		b, err := Files.ReadFile("snapshot/examples/performance-catalog/" + name + ".json")
		if err != nil {
			t.Fatal(err)
		}
		var pages []map[string]any
		if err = json.Unmarshal(b, &pages); err != nil {
			t.Fatal(err)
		}
		var items []any = make([]any, 0)
		for _, page := range pages {
			if err = ValidateCatalogPage(page); err != nil {
				t.Fatal(err)
			}
			items = append(items, page["items"].([]any)...)
		}
		digest, err := CatalogDigest(items)
		if err != nil {
			t.Fatal(err)
		}
		if digest != pages[0]["snapshot"].(map[string]any)["reference"].(map[string]any)["sha256"] {
			t.Fatal("cross-language digest mismatch")
		}
		pages[0]["secret"] = "never-public"
		if ValidateCatalogPage(pages[0]) == nil {
			t.Fatal("accepted private field")
		}
	}
	// Go's ordinary JSON writer escapes HTML and U+2028. The shared digest must not.
	digest, err := CatalogDigest([]any{map[string]any{"displayName": "Café<&\u2028"}})
	if err != nil || digest != "a54fef76fe8c0b47c529e99845ec1ad80a03d7385379f7ccc5a480c253f9c647" {
		t.Fatalf("Unicode canonical digest: %s, %v", digest, err)
	}
	if _, err := CatalogDigest([]any{true}); err == nil {
		t.Fatal("accepted unsupported digest value")
	}
	if _, err := CatalogDigest(nil); err == nil {
		t.Fatal("accepted missing resource array as an empty view")
	}
}

func TestCatalogNullAndLiteralConstraints(t *testing.T) {
	if err := validateWithSchemas(schema{Type: "null"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if validateWithSchemas(schema{Type: "null"}, "null", nil) == nil {
		t.Fatal("null accepted a string")
	}
	encoded, err := Files.ReadFile("snapshot/examples/performance-catalog/empty.json")
	if err != nil {
		t.Fatal(err)
	}
	var pages []map[string]any
	if err = json.Unmarshal(encoded, &pages); err != nil {
		t.Fatal(err)
	}
	page := pages[0]
	page["schemaVersion"] = 2
	if ValidateCatalogPage(page) == nil {
		t.Fatal("accepted unsupported generation")
	}
	page["schemaVersion"] = 1
	page["snapshot"].(map[string]any)["complete"] = false
	if ValidateCatalogPage(page) == nil {
		t.Fatal("accepted partial snapshot declaration")
	}
}
