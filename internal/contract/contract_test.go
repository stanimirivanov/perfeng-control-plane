package contract

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
)

func TestPinnedSnapshot(t *testing.T) {
	b, err := Files.ReadFile("snapshot/lock.json")
	if err != nil {
		t.Fatal(err)
	}
	var lock struct {
		Commit        string
		BundleVersion string
		APIVersion    string
		SHA256        map[string]string
	}
	if err := json.Unmarshal(b, &lock); err != nil {
		t.Fatal(err)
	}
	if lock.Commit != "305402970f286c5f84c8d2577e9f1ab3292c4b9c" ||
		lock.BundleVersion != "0.8.0" || lock.APIVersion != "0.3.0" || len(lock.SHA256) != 8 {
		t.Fatal("unexpected contract provenance")
	}
	for name, expected := range lock.SHA256 {
		b, err := Files.ReadFile("snapshot/" + name)
		if err != nil {
			t.Fatal(err)
		}
		actual := sha256.Sum256(b)
		if hex.EncodeToString(actual[:]) != expected {
			t.Fatalf("snapshot checksum mismatch: %s", name)
		}
	}
}

func TestBaselineFixturesAndStrictSchemas(t *testing.T) {
	for _, test := range []struct {
		fixture  string
		validate func(any) error
	}{
		{"baseline-create.json", ValidateBaselineCreate},
		{"baseline-transition.json", ValidateBaselineTransition},
	} {
		b, err := Files.ReadFile("snapshot/examples/" + test.fixture)
		if err != nil {
			t.Fatal(err)
		}
		var value map[string]any
		decoder := json.NewDecoder(bytes.NewReader(b))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			t.Fatal(err)
		}
		if err := test.validate(value); err != nil {
			t.Fatalf("%s: %v", test.fixture, err)
		}
		value["actor"] = "caller-controlled"
		if test.validate(value) == nil {
			t.Fatalf("%s accepted an unknown actor", test.fixture)
		}
	}
}

func TestCreateFixtureAndStrictSchema(t *testing.T) {
	b, err := Files.ReadFile("snapshot/examples/create.json")
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCreate(v); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(map[string]any){
		"unknown":        func(v map[string]any) { v["command"] = "sh" },
		"case":           func(v map[string]any) { v["TestSuite"] = v["testSuite"]; delete(v, "testSuite") },
		"missing":        func(v map[string]any) { delete(v, "policy") },
		"null":           func(v map[string]any) { v["candidate"] = nil },
		"profile":        func(v map[string]any) { v["profile"] = "nightly" },
		"floating":       func(v map[string]any) { v["candidate"].(map[string]any)["image"] = "ghcr.io/example/api:latest" },
		"version":        func(v map[string]any) { v["catalogue"].(map[string]any)["version"] = "1.0.0-rc1" },
		"hash":           func(v map[string]any) { v["policy"].(map[string]any)["sha256"] = "abcd" },
		"nested-unknown": func(v map[string]any) { v["environment"].(map[string]any)["url"] = "http://localhost" },
	} {
		t.Run(name, func(t *testing.T) {
			var copy map[string]any
			if err := json.Unmarshal(b, &copy); err != nil {
				t.Fatal(err)
			}
			mutate(copy)
			if ValidateCreate(copy) == nil {
				t.Fatal("accepted invalid request")
			}
		})
	}
}

func TestPrepareSchemas(t *testing.T) {
	valid := map[string]schema{
		"CreateRun": {
			Type: "object",
			Properties: map[string]schema{
				"name": {Ref: "#/components/schemas/Name"},
			},
		},
		"RunId": {Type: "string", Pattern: "^run-[0-9]+$"},
		"Name":  {Type: "string", Pattern: "^[a-z]+$"},
	}

	prepared, err := prepareSchemas(valid, "CreateRun", "RunId")
	if err != nil {
		t.Fatal(err)
	}
	if prepared["RunId"].pattern == nil ||
		prepared["Name"].pattern == nil ||
		!prepared["RunId"].pattern.MatchString("run-42") {
		t.Fatal("schema patterns were not prepared")
	}

	for name, definitions := range map[string]map[string]schema{
		"invalid-pattern": {
			"CreateRun": {Type: "string", Pattern: "["},
			"RunId":     valid["RunId"],
		},
		"dangling-reference": {
			"CreateRun": {Ref: "#/components/schemas/Missing"},
			"RunId":     valid["RunId"],
		},
		"unsupported-request-schema": {
			"CreateRun": {Type: "boolean"},
			"RunId":     valid["RunId"],
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := prepareSchemas(definitions, "CreateRun", "RunId"); err == nil {
				t.Fatal("invalid validation schema accepted")
			}
		})
	}
}
