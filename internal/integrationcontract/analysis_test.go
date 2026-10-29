package integrationcontract

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
)

func publicationFixture(t *testing.T) []byte {
	t.Helper()
	raw, err := contract.Files.ReadFile("snapshot/examples/performance-analysis/published.json")
	if err != nil {
		t.Fatal(err)
	}

	return raw
}

func TestAnalysisPublicationRoundTripOwnedAndConcurrent(t *testing.T) {
	t.Parallel()
	for range 4 {
		t.Run("decode", func(t *testing.T) {
			t.Parallel()
			raw := publicationFixture(t)
			message, err := DecodeAnalysisPublication(raw)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := EncodeAnalysisPublication(message)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeIntegrationMessage(raw); err == nil {
				t.Fatal("existing union expanded")
			}
			other, err := DecodeAnalysisPublication(encoded)
			if err != nil || !reflect.DeepEqual(message, other) {
				t.Fatal("publication changed", err)
			}
			message.Data.Context.Evidence[0].ID = "changed"
			message.Data.Native.Baseline.Identity.ID = "changed"
			raw[0] = '!'
			if other.Data.Context.Evidence[0].ID == "changed" || other.Data.Native.Baseline.Identity.ID == "changed" {
				t.Fatal("mutable publication values shared")
			}
		})
	}
}

func TestAnalysisPublicationPreservesExactNumbersUnicodeAndNullableBaseline(t *testing.T) {
	raw := bytes.Replace(publicationFixture(t), []byte(`"sizeBytes": 2362`), []byte(`"sizeBytes": 2.362e3`), 1)
	message, err := DecodeAnalysisPublication(raw)
	if err != nil || message.Data.Artifact.SizeBytes.String() != "2.362e3" {
		t.Fatal("exact size syntax lost", err)
	}
	message.Data.Context.Baseline, message.Data.Native.Baseline = nil, nil
	message.ID = "valid-😀-literal-\\ud800"
	encoded, err := EncodeAnalysisPublication(message)
	if err != nil {
		t.Fatal(err)
	}
	encoded = bytes.Replace(encoded, []byte("😀"), []byte(`\ud83d\ude00`), 1)
	decoded, err := DecodeAnalysisPublication(encoded)
	if err != nil || decoded.ID != message.ID || decoded.Data.Native.Baseline != nil {
		t.Fatal("valid Unicode/null baseline changed", err)
	}
}

func TestAnalysisPublicationRejectsMalformedOrUnsupportedClaims(t *testing.T) {
	raw := publicationFixture(t)
	for _, bad := range [][]byte{
		append([]byte(`{"specversion":"1.0",`), raw[1:]...),
		append([]byte(`{"secret":"private-value",`), raw[1:]...),
		append(append([]byte(nil), raw...), []byte(`{}`)...),
		bytes.Repeat([]byte(" "), contract.AnalysisPublicationMaximumBytes+1),
		[]byte(strings.Repeat("[", 66) + "0" + strings.Repeat("]", 66)),
		bytes.Replace(raw, []byte("analysis-publication-1"), []byte(`\ud800`), 1),
		bytes.Replace(raw, []byte("analysis-publication-1"), []byte{0xff}, 1),
		bytes.Replace(raw, []byte(`"sizeBytes": 2362`), []byte(`"sizeBytes": 1e-999999999`), 1),
		bytes.Replace(raw, []byte(`"sizeBytes": 2362`), []byte(`"sizeBytes": `+strings.Repeat("1", 129)), 1),
	} {
		got, err := DecodeAnalysisPublication(bad)
		if !errors.Is(err, contract.ErrAnalysisPublication) || !reflect.DeepEqual(got, contract.AnalysisPublication{}) || strings.Contains(err.Error(), "private-value") {
			t.Fatal("invalid publication exposed partial claims", err)
		}
	}
	unsupported := bytes.Replace(raw, []byte("analysis-published.v1"), []byte("analysis-published.v2"), 1)
	if _, err := DecodeAnalysisPublication(unsupported); !errors.Is(err, contract.ErrAnalysisPublicationVersion) {
		t.Fatal("unsupported version not classified", err)
	}
}

func TestAnalysisPublicationRejectsOutboundAndContextContradictions(t *testing.T) {
	for _, mutate := range []func(*contract.AnalysisPublication){
		func(m *contract.AnalysisPublication) { m.Source = "https://other.example" },
		func(m *contract.AnalysisPublication) { m.Data.Context.Attempt = m.Data.Context.Request },
		func(m *contract.AnalysisPublication) { m.Data.Context.Deadline = m.Data.Context.RequestedAt },
		func(m *contract.AnalysisPublication) { m.Data.Native.Baseline = nil },
		func(m *contract.AnalysisPublication) { m.Data.Artifact.RunID = "perf-20260902-130000-ffffffff" },
		func(m *contract.AnalysisPublication) { m.Data.Artifact.Kind = "normalized" },
		func(m *contract.AnalysisPublication) { m.Data.Artifact.SizeBytes = "0" },
		func(m *contract.AnalysisPublication) {
			m.Data.Artifact.URI = "https://user:password@example.com/analysis.json"
		},
		func(m *contract.AnalysisPublication) {
			m.Data.Native.Baseline.Artifact.URI = "https://user:password@example.com/baseline.json"
		},
		func(m *contract.AnalysisPublication) { m.Time = "2026-09-01T13:00:00Z" },
	} {
		message, err := DecodeAnalysisPublication(publicationFixture(t))
		if err != nil {
			t.Fatal(err)
		}
		mutate(&message)
		if encoded, err := EncodeAnalysisPublication(message); !errors.Is(err, contract.ErrAnalysisPublication) || len(encoded) != 0 {
			t.Fatal("invalid outbound publication accepted", err)
		}
	}
}

func TestAnalysisPublicationPinsAndOfflineResolution(t *testing.T) {
	raw, err := contract.Files.ReadFile("snapshot/lock.json")
	if err != nil {
		t.Fatal(err)
	}
	var lock struct {
		PerformanceAnalysis struct {
			Commit, BundleVersion string
			Files                 []string
		}
	}
	if json.Unmarshal(raw, &lock) != nil || lock.PerformanceAnalysis.Commit != "cd2442f433ce93d9a886c14ad6ff8aa8488a623b" || lock.PerformanceAnalysis.BundleVersion != "0.25.0" || len(lock.PerformanceAnalysis.Files) != 2 {
		t.Fatal("unexpected publication provenance")
	}
	for _, reference := range []string{"https://example.invalid/schema.json", "file:///private/schema.json"} {
		if _, err := compileAnalysisPublicationSchema([]byte(`{"$ref":"` + reference + `"}`)); err == nil {
			t.Fatal("external schema resolution enabled")
		}
	}
}
