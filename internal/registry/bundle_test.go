package registry

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stanimirivanov/perfeng-control-plane/internal/reconcile"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

const (
	registryManifestPath = "registry.json"
	registryPolicyPath   = "policies/search.json"
	registryTemplatePath = "jobs/search.json"
)

func registryBundleFixture(t *testing.T) (fstest.MapFS, run.Run, reconcile.ReportingInput) {
	t.Helper()
	entry, current, input := reportPolicyFixture(t)
	templateBytes, err := json.Marshal(entry.ExecutionTemplate)
	if err != nil {
		t.Fatal(err)
	}
	bundle := reportPolicyBundle{
		SchemaVersion: 1,
		Entries: []reportPolicyBundleEntry{{
			Policy: registryPolicyPath, TestID: entry.TestID,
			ContractsVersion: entry.ContractsVersion,
			Catalogue:        entry.Catalogue, Profile: entry.Profile,
			RawProducer: entry.RawProducer, NormalizerProducer: entry.NormalizerProducer,
			Normalization: normalizationBundleConfig{
				SourceFormat: entry.Normalization.SourceFormat,
				Layout:       entry.Normalization.Layout,
			},
			ReportProducer: entry.ReportProducer, ExecutionTemplate: registryTemplatePath,
			Workload: entry.Workload, Environment: entry.Environment, Dataset: entry.Dataset,
			CandidateImages: entry.CandidateImages, Principals: entry.Principals,
		}},
	}
	manifestBytes, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}

	return fstest.MapFS{
		registryManifestPath: &fstest.MapFile{Data: manifestBytes, Mode: 0o444},
		registryPolicyPath:   &fstest.MapFile{Data: entry.PolicyBytes, Mode: 0o444},
		registryTemplatePath: &fstest.MapFile{Data: templateBytes, Mode: 0o444},
	}, current, input
}

func TestLoadReportPolicyRegistryBuildsExactImmutableAuthority(t *testing.T) {
	source, current, input := registryBundleFixture(t)
	wantPolicy := append([]byte(nil), source[registryPolicyPath].Data...)

	registry, err := LoadReportPolicyRegistry(source, registryManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.ApproveRun(context.Background(), "alice", current.Request); err != nil {
		t.Fatal(err)
	}
	validating := current
	validating.State = run.StateValidating
	job, err := registry.ResolveJob(context.Background(), "alice", validating)
	if err != nil || job == nil {
		t.Fatalf("ResolveJob() = %#v, %v", job, err)
	}
	trust, err := registry.ResolveReportTrust(context.Background(), "alice", current, input)
	if err != nil {
		t.Fatal(err)
	}
	if string(trust.PolicyBytes) != string(wantPolicy) {
		t.Fatal("registry changed exact policy bytes")
	}

	source[registryPolicyPath].Data[0] = 'x'
	source[registryTemplatePath].Data[0] = 'x'
	again, err := registry.ResolveReportTrust(context.Background(), "alice", current, input)
	if err != nil || string(again.PolicyBytes) != string(wantPolicy) {
		t.Fatal("registry aliases bundle file data")
	}
	againJob, err := registry.ResolveJob(context.Background(), "alice", validating)
	if err != nil || !reflect.DeepEqual(againJob, job) {
		t.Fatal("registry aliases template file data")
	}
}

func TestLoadReportPolicyRegistryRejectsMalformedBundles(t *testing.T) {
	for _, test := range []struct {
		name       string
		manifest   string
		mutate     func(fstest.MapFS)
		source     fs.FS
		useFixture bool
	}{
		{name: "nil filesystem", manifest: registryManifestPath},
		{name: "invalid manifest path", manifest: "../registry.json", source: fstest.MapFS{}},
		{name: "missing manifest", manifest: registryManifestPath, source: fstest.MapFS{}},
		{name: "duplicate field", manifest: registryManifestPath, source: fstest.MapFS{
			registryManifestPath: &fstest.MapFile{Data: []byte(`{"schemaVersion":1,"schemaVersion":1,"entries":[]}`)},
		}},
		{name: "unknown field", manifest: registryManifestPath, source: fstest.MapFS{
			registryManifestPath: &fstest.MapFile{Data: []byte(`{"schemaVersion":1,"entries":[],"unknown":true}`)},
		}},
		{name: "oversized manifest", manifest: registryManifestPath, source: fstest.MapFS{
			registryManifestPath: &fstest.MapFile{Data: []byte(strings.Repeat(" ", maximumRegistryBundleBytes+1))},
		}},
		{name: "missing policy", manifest: registryManifestPath, useFixture: true, mutate: func(source fstest.MapFS) {
			delete(source, registryPolicyPath)
		}},
		{name: "malformed policy", manifest: registryManifestPath, useFixture: true, mutate: func(source fstest.MapFS) {
			source[registryPolicyPath].Data = []byte(`{}`)
		}},
		{name: "malformed template", manifest: registryManifestPath, useFixture: true, mutate: func(source fstest.MapFS) {
			source[registryTemplatePath].Data = []byte(`{"unknown":true}`)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := test.source
			if test.useFixture {
				source, _, _ = registryBundleFixture(t)
			}
			if test.mutate != nil {
				test.mutate(source.(fstest.MapFS))
			}
			if _, err := LoadReportPolicyRegistry(source, test.manifest); !errors.Is(
				err, run.ErrValidation,
			) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestLoadReportPolicyRegistryRejectsInvalidReferencedPaths(t *testing.T) {
	for _, invalid := range []string{"", ".", "../policy.json", "/policy.json", `policies\search.json`} {
		t.Run(invalid, func(t *testing.T) {
			source, _, _ := registryBundleFixture(t)
			var bundle reportPolicyBundle
			if json.Unmarshal(source[registryManifestPath].Data, &bundle) != nil {
				t.Fatal("invalid fixture manifest")
			}
			bundle.Entries[0].Policy = invalid
			data, err := json.Marshal(bundle)
			if err != nil {
				t.Fatal(err)
			}
			source[registryManifestPath].Data = data

			if _, err := LoadReportPolicyRegistry(source, registryManifestPath); !errors.Is(
				err, run.ErrValidation,
			) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}
