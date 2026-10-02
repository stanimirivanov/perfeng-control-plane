package contract

import (
	"encoding/json"
	"io/fs"
	"testing"
)

func TestNativeWorkerV2SnapshotInventory(t *testing.T) {
	openapi := decodeContractValue(t, "snapshot/api/native-worker/v2/openapi.json")
	info, ok := openapi["info"].(map[string]any)
	if !ok || info["version"] != "2.0.0" {
		t.Fatal("unexpected Native Worker v2 API version")
	}
	paths, ok := openapi["paths"].(map[string]any)
	if !ok || len(paths) != 7 {
		t.Fatal("unexpected Native Worker v2 path inventory")
	}
	examples, err := fs.Glob(Files, "snapshot/api/native-worker/v2/examples/*.json")
	if err != nil {
		t.Fatal(err)
	}
	rejections, err := fs.Glob(Files, "snapshot/api/native-worker/v2/rejections/*.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(examples) != 16 || len(rejections) != 15 {
		t.Fatal("unexpected Native Worker v2 fixture inventory")
	}
}

func TestNativeWorkerV2RuntimeValidation(t *testing.T) {
	base := "snapshot/api/native-worker/v2/"
	for _, test := range []struct {
		name     string
		fixture  string
		validate func(any) error
	}{
		{"presence", "presence.json", ValidateNativeWorkerV2Presence},
		{"acquire", "acquire.json", ValidateNativeWorkerV2AcquireLease},
		{"lease", "lease.json", ValidateNativeWorkerV2Lease},
		{"renewal", "renewal.json", ValidateNativeWorkerV2LeaseRenewal},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := decodeContractValue(t, base+"examples/"+test.fixture)
			if err := test.validate(value); err != nil {
				t.Fatal(err)
			}
		})
	}
	lease := decodeContractValue(t, base+"examples/lease.json")
	assignment := lease["assignment"].(map[string]any)
	if err := ValidateNativeWorkerV2ExecutionRecipe(assignment["recipe"]); err != nil {
		t.Fatal(err)
	}

	rejections, err := fs.Glob(Files, base+"rejections/*.json")
	if err != nil {
		t.Fatal(err)
	}
	validated := 0
	for _, path := range rejections {
		fixture := decodeContractValue(t, path)
		var validate func(any) error
		switch fixture["schema"] {
		case "WorkerPresence":
			validate = ValidateNativeWorkerV2Presence
		case "ExecutionRecipe":
			validate = ValidateNativeWorkerV2ExecutionRecipe
		case "LeaseRenewal":
			validate = ValidateNativeWorkerV2LeaseRenewal
		default:
			continue
		}
		validated++
		if validate(fixture["value"]) == nil {
			t.Fatalf("accepted rejected Native Worker v2 fixture: %s", path)
		}
	}
	if validated != 9 {
		t.Fatalf("validated %d v2 rejections, want 9", validated)
	}
}

func TestNativeWorkerV2LeaseRejectsPartialOrChangedRecipe(t *testing.T) {
	fixture := "snapshot/api/native-worker/v2/examples/lease.json"
	for name, mutate := range map[string]func(map[string]any){
		"missing recipe": func(lease map[string]any) {
			delete(lease["assignment"].(map[string]any), "recipe")
		},
		"unsupported browser": func(lease map[string]any) {
			lease["assignment"].(map[string]any)["browser"] = "edge"
		},
		"cold cache with page reuse": func(lease map[string]any) {
			recipe := lease["assignment"].(map[string]any)["recipe"].(map[string]any)
			recipe["scenario"].(map[string]any)["pageReuse"] = "per-run"
		},
		"missing evidence digest": func(lease map[string]any) {
			recipe := lease["assignment"].(map[string]any)["recipe"].(map[string]any)
			delete(recipe["environment"].(map[string]any), "evidenceSha256")
		},
		"invalid viewport": func(lease map[string]any) {
			recipe := lease["assignment"].(map[string]any)["recipe"].(map[string]any)
			settings := recipe["browserSettings"].(map[string]any)
			settings["viewport"].(map[string]any)["width"] = json.Number("319")
		},
		"unexpected command": func(lease map[string]any) {
			recipe := lease["assignment"].(map[string]any)["recipe"].(map[string]any)
			recipe["command"] = "run"
		},
		"swapped slots": func(lease map[string]any) {
			plan := lease["artifactPlan"].([]any)
			plan[0], plan[1] = plan[1], plan[0]
		},
		"extra slot": func(lease map[string]any) {
			plan := lease["artifactPlan"].([]any)
			lease["artifactPlan"] = append(plan, plan[0])
		},
	} {
		t.Run(name, func(t *testing.T) {
			lease := decodeContractValue(t, fixture)
			mutate(lease)
			if ValidateNativeWorkerV2Lease(lease) == nil {
				t.Fatal("accepted invalid Native Worker v2 lease")
			}
		})
	}
	if ValidateNativeWorkerV2Lease(decodeContractValue(
		t, "snapshot/api/native-worker/v1/examples/lease.json",
	)) == nil {
		t.Fatal("accepted a v1 lease as v2")
	}
}

func TestNativeWorkerV2AcquisitionRejectsV1(t *testing.T) {
	if ValidateNativeWorkerV2AcquireLease(decodeContractValue(
		t, "snapshot/api/native-worker/v1/examples/acquire.json",
	)) == nil {
		t.Fatal("accepted a v1 acquisition request as v2")
	}
}

func TestSchemaPrefixItemsForbidAdditionalItems(t *testing.T) {
	var definition schema
	if err := json.Unmarshal([]byte(`{
		"type":"array","prefixItems":[{"const":"first"}],"items":false
	}`), &definition); err != nil {
		t.Fatal(err)
	}
	compiled, err := compilePatterns(definition)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateWithSchemas(compiled, []any{"first"}, nil); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range [][]any{{"second"}, {"first", "extra"}} {
		if validateWithSchemas(compiled, invalid, nil) == nil {
			t.Fatal("accepted invalid prefix-item array")
		}
	}
}
