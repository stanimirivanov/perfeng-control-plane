package contract

import (
	"encoding/json"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
)

func candidateFiles(t *testing.T) fstest.MapFS {
	t.Helper()
	files := fstest.MapFS{}
	if err := fs.WalkDir(Files, "snapshot", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, readErr := Files.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		files[strings.TrimPrefix(path, "snapshot/")] = &fstest.MapFile{Data: data}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return files
}

func TestAPICandidateAdmission(t *testing.T) {
	if err := AdmitAPICandidate(candidateFiles(t)); err != nil {
		t.Fatal(err)
	}
}

func TestAPICandidateRejectsLoaderIncompatibleChanges(t *testing.T) {
	for _, test := range []struct {
		name, file string
		mutate     func(map[string]json.RawMessage)
	}{
		{"renamed Run root", "openapi.json", func(s map[string]json.RawMessage) { s["RunCreate"] = s["CreateRun"]; delete(s, "CreateRun") }},
		{"renamed native root", "api/native-worker/v2/openapi.json", func(s map[string]json.RawMessage) { s["Presence"] = s["WorkerPresence"]; delete(s, "WorkerPresence") }},
		{"missing reference", "openapi.json", func(s map[string]json.RawMessage) {
			s["CreateRun"] = json.RawMessage(`{"$ref":"#/components/schemas/Missing"}`)
		}},
		{"external reference", "openapi.json", func(s map[string]json.RawMessage) {
			s["CreateRun"] = json.RawMessage(`{"$ref":"https://example.com/CreateRun"}`)
		}},
		{"requiredness drift", "openapi.json", func(s map[string]json.RawMessage) {
			s["CreateRun"] = json.RawMessage(`{"type":"object","required":["unpublished"],"additionalProperties":false}`)
		}},
		{"unknown field accepted", "openapi.json", func(s map[string]json.RawMessage) { s["CreateRun"] = json.RawMessage(`{"type":"object"}`) }},
		{"malformed pattern", "openapi.json", func(s map[string]json.RawMessage) { s["RunId"] = json.RawMessage(`{"type":"string","pattern":"["}`) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			files := candidateFiles(t)
			var document struct {
				Components struct {
					Schemas map[string]json.RawMessage `json:"schemas"`
				} `json:"components"`
			}
			if err := json.Unmarshal(files[test.file].Data, &document); err != nil {
				t.Fatal(err)
			}
			test.mutate(document.Components.Schemas)
			data, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			files[test.file] = &fstest.MapFile{Data: data}
			if err := AdmitAPICandidate(files); err == nil {
				t.Fatal("incompatible candidate admitted")
			}
			if err := ValidateCreate(decodeContractValue(t, "snapshot/examples/create.json")); err != nil {
				t.Fatalf("candidate admission changed runtime schemas: %v", err)
			}
		})
	}
}
