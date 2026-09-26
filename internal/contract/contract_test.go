package contract

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"strings"
	"testing"
)

func TestPinnedSnapshot(t *testing.T) {
	b, err := Files.ReadFile("snapshot/lock.json")
	if err != nil {
		t.Fatal(err)
	}
	var lock struct {
		Commit                  string
		BundleVersion           string
		RunManagementAPIVersion string
		NativeWorkerAPIVersion  string
		SHA256                  map[string]string
	}
	if err := json.Unmarshal(b, &lock); err != nil {
		t.Fatal(err)
	}
	if lock.Commit != "4a076973f05d78cbc4e1fc62a6ca26d68de3794c" ||
		lock.BundleVersion != "0.15.0" || lock.RunManagementAPIVersion != "0.4.0" ||
		lock.NativeWorkerAPIVersion != "1.0.0" || len(lock.SHA256) != 36 {
		t.Fatal("unexpected contract provenance")
	}
	actualFiles := make(map[string]struct{}, len(lock.SHA256))
	if err := fs.WalkDir(Files, "snapshot", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || path == "snapshot/lock.json" {
			return nil
		}
		name := strings.TrimPrefix(path, "snapshot/")
		if _, exists := lock.SHA256[name]; !exists {
			t.Fatalf("snapshot file is absent from checksum ledger: %s", name)
		}
		actualFiles[name] = struct{}{}

		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(actualFiles) != len(lock.SHA256) {
		t.Fatal("checksum ledger contains an absent snapshot file")
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

func TestNativeWorkerSnapshotInventory(t *testing.T) {
	verifyNativeWorkerOpenAPI(t)
	verifyNativeWorkerFixtures(t)
}

func TestNativeWorkerPresenceRuntimeValidation(t *testing.T) {
	presence := decodeContractValue(t, "snapshot/api/native-worker/v1/examples/presence.json")
	if err := ValidateWorkerPresence(presence); err != nil {
		t.Fatal(err)
	}

	rejections, err := fs.Glob(Files, "snapshot/api/native-worker/v1/rejections/*.json")
	if err != nil {
		t.Fatal(err)
	}
	validated := 0
	for _, path := range rejections {
		fixture := decodeContractValue(t, path)
		if fixture["schema"] != "WorkerPresence" {
			continue
		}
		validated++
		if ValidateWorkerPresence(fixture["value"]) == nil {
			t.Fatalf("accepted rejected WorkerPresence fixture: %s", path)
		}
	}
	if validated != 3 {
		t.Fatalf("validated %d WorkerPresence rejections, want 3", validated)
	}

	capabilities := presence["capabilities"].(map[string]any)
	capabilities["browsers"] = []any{"edge", "chrome", "chromium", "edge"}
	if ValidateWorkerPresence(presence) == nil {
		t.Fatal("accepted a duplicate browser capability")
	}

	presence = decodeContractValue(t, "snapshot/api/native-worker/v1/examples/presence.json")
	capabilities = presence["capabilities"].(map[string]any)
	capabilities["artifactKinds"] = []any{
		"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l", "m", "n", "o", "p", "q",
	}
	if ValidateWorkerPresence(presence) == nil {
		t.Fatal("accepted an oversized artifact-kind capability list")
	}
}

func TestNativeWorkerAcquireRuntimeValidation(t *testing.T) {
	acquire := decodeContractValue(t, "snapshot/api/native-worker/v1/examples/acquire.json")
	if err := ValidateAcquireLease(acquire); err != nil {
		t.Fatal(err)
	}

	for name, mutate := range map[string]func(map[string]any){
		"protocol": func(value map[string]any) { value["protocolVersion"] = json.Number("2") },
		"wait":     func(value map[string]any) { value["maxWaitSeconds"] = json.Number("31") },
		"unknown":  func(value map[string]any) { value["workerId"] = "caller-selected" },
		"missing":  func(value map[string]any) { delete(value, "maxWaitSeconds") },
	} {
		t.Run(name, func(t *testing.T) {
			value := decodeContractValue(t, "snapshot/api/native-worker/v1/examples/acquire.json")
			mutate(value)
			if ValidateAcquireLease(value) == nil {
				t.Fatal("accepted invalid acquisition request")
			}
		})
	}
}

func TestNativeWorkerRenewRuntimeValidation(t *testing.T) {
	renewal := decodeContractValue(t, "snapshot/api/native-worker/v1/examples/renew.json")
	if err := ValidateRenewLease(renewal); err != nil {
		t.Fatal(err)
	}

	for name, mutate := range map[string]func(map[string]any){
		"revision": func(value map[string]any) { value["expectedRunRevision"] = json.Number("0") },
		"phase":    func(value map[string]any) { value["phase"] = "COMPLETED" },
		"unknown":  func(value map[string]any) { value["leaseCapability"] = "caller-selected" },
		"missing":  func(value map[string]any) { delete(value, "attemptId") },
	} {
		t.Run(name, func(t *testing.T) {
			value := decodeContractValue(t, "snapshot/api/native-worker/v1/examples/renew.json")
			mutate(value)
			if ValidateRenewLease(value) == nil {
				t.Fatal("accepted invalid renewal request")
			}
		})
	}
}

func decodeContractValue(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := Files.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		t.Fatal(err)
	}

	return value
}

func verifyNativeWorkerOpenAPI(t *testing.T) {
	t.Helper()

	b, err := Files.ReadFile("snapshot/api/native-worker/v1/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		OpenAPI string
		Info    struct{ Version string }
		Paths   map[string]struct {
			Post struct {
				OperationID string `json:"operationId"`
			}
		}
	}
	if err := json.Unmarshal(b, &document); err != nil {
		t.Fatal(err)
	}
	if document.OpenAPI != "3.1.1" || document.Info.Version != "1.0.0" ||
		len(document.Paths) != 7 {
		t.Fatal("unexpected Native Worker API identity or path inventory")
	}
	for _, path := range document.Paths {
		if path.Post.OperationID == "" {
			t.Fatal("Native Worker path lacks its POST operation")
		}
	}
}

func verifyNativeWorkerFixtures(t *testing.T) {
	t.Helper()

	examples, err := fs.Glob(Files, "snapshot/api/native-worker/v1/examples/*.json")
	if err != nil {
		t.Fatal(err)
	}
	rejections, err := fs.Glob(Files, "snapshot/api/native-worker/v1/rejections/*.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(examples) != 16 || len(rejections) != 10 {
		t.Fatal("unexpected Native Worker conformance fixture inventory")
	}

	b, err := Files.ReadFile("snapshot/api/native-worker/v1/http-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		OperationID string `json:"operationId"`
		Request     string
		Response    string
	}
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) != 10 {
		t.Fatal("unexpected Native Worker HTTP fixture inventory")
	}
	for _, fixture := range cases {
		if fixture.OperationID == "" || fixture.Request == "" {
			t.Fatal("Native Worker HTTP fixture lacks operation or request")
		}
		if _, err := Files.ReadFile("snapshot/api/native-worker/v1/examples/" + fixture.Request + ".json"); err != nil {
			t.Fatal(err)
		}
		if fixture.Response != "" {
			if _, err := Files.ReadFile("snapshot/api/native-worker/v1/examples/" + fixture.Response + ".json"); err != nil {
				t.Fatal(err)
			}
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
