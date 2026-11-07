package contract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
)

func loadAPIFamilies(source fs.FS) (map[string]schema, map[string]schema, error) {
	run, err := loadAPISchemas(source, "openapi.json",
		"CreateRun", "RunId", "CreateBaseline", "BaselineTransition", "ResourceId", "Version")
	if err != nil {
		return nil, nil, err
	}
	native, err := loadAPISchemas(source, "api/native-worker/v2/openapi.json",
		"WorkerPresence", "PresenceStatus", "AcquireLease", "Lease", "ExecutionRecipe", "LeaseRenewal",
		"RenewLease", "ReleaseLease", "UploadGrantRequest", "UploadGrant",
		"RegisterArtifact", "ArtifactReference", "TerminalReport", "TerminalAck",
		"InspectAttempt", "AttemptInspection", "DiscoverAttempt", "AttemptDiscovery", "Error")
	if err != nil {
		return nil, nil, err
	}
	return run, native, nil
}

func loadAPISchemas(source fs.FS, path string, roots ...string) (map[string]schema, error) {
	data, err := fs.ReadFile(source, path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var document struct {
		Components struct {
			Schemas map[string]schema `json:"schemas"`
		} `json:"components"`
	}
	if err = json.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	prepared, err := prepareSchemas(document.Components.Schemas, roots...)
	if err != nil {
		return nil, fmt.Errorf("prepare %s: %w", path, err)
	}
	return prepared, nil
}

// AdmitAPICandidate checks the Run/native roots with the production loader and
// replays retained embedded fixtures. It does not select or install the candidate,
// establish upstream provenance, or prove equivalence for untested payloads.
func AdmitAPICandidate(source fs.FS) error {
	run, native, err := loadAPIFamilies(source)
	if err != nil {
		return err
	}
	for _, fixture := range []struct {
		path, root string
		schemas    map[string]schema
	}{
		{"examples/create.json", "CreateRun", run},
		{"examples/baseline-create.json", "CreateBaseline", run},
		{"examples/baseline-transition.json", "BaselineTransition", run},
		{"api/native-worker/v2/examples/presence.json", "WorkerPresence", native},
		{"api/native-worker/v2/examples/acquire.json", "AcquireLease", native},
		{"api/native-worker/v2/examples/renew.json", "RenewLease", native},
		{"api/native-worker/v2/examples/terminal-completed.json", "TerminalReport", native},
	} {
		data, readErr := Files.ReadFile("snapshot/" + fixture.path)
		if readErr != nil {
			return readErr
		}
		var value map[string]any
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		if err = decoder.Decode(&value); err != nil {
			return fmt.Errorf("decode retained fixture %s: %w", fixture.path, err)
		}
		if err = validateWithSchemas(fixture.schemas[fixture.root], value, fixture.schemas); err != nil {
			return fmt.Errorf("candidate rejects retained fixture %s: %w", fixture.path, err)
		}
		value["__admission_unknown"] = true
		if validateWithSchemas(fixture.schemas[fixture.root], value, fixture.schemas) == nil {
			return fmt.Errorf("candidate admits unknown field in %s", fixture.root)
		}
	}
	return nil
}
