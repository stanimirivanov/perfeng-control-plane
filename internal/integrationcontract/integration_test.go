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

func integrationFixture(t *testing.T, name string) []json.RawMessage {
	t.Helper()
	raw, err := contract.Files.ReadFile("snapshot/examples/performance-integration/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var messages []json.RawMessage
	if err := json.Unmarshal(raw, &messages); err != nil {
		t.Fatal(err)
	}

	return messages
}

func TestIntegrationPinnedExchangesRoundTripConcurrently(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"completed", "cancelled", "rejected"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			for _, raw := range integrationFixture(t, name) {
				message, err := DecodeIntegrationMessage(raw)
				if err != nil {
					t.Fatal(err)
				}
				encoded, err := EncodeIntegrationMessage(message)
				if err != nil {
					t.Fatal(err)
				}
				var before, after any
				if json.Unmarshal(raw, &before) != nil || json.Unmarshal(encoded, &after) != nil || !reflect.DeepEqual(before, after) {
					t.Fatal("portable message changed")
				}
			}
		})
	}
}

func TestIntegrationComponentProvenance(t *testing.T) {
	t.Parallel()
	raw, err := contract.Files.ReadFile("snapshot/lock.json")
	if err != nil {
		t.Fatal(err)
	}
	var lock struct {
		PerformanceIntegration struct{ Commit, BundleVersion, APIVersion string }
	}
	if json.Unmarshal(raw, &lock) != nil || lock.PerformanceIntegration.Commit != "041273947703542f7fe22eab838c176df5517974" || lock.PerformanceIntegration.BundleVersion != "0.24.0" || lock.PerformanceIntegration.APIVersion != "1.0.0" {
		t.Fatal("unexpected integration provenance")
	}
}

func TestIntegrationRejectsClosedShapeAndPerMessageConflicts(t *testing.T) {
	t.Parallel()
	raw := integrationFixture(t, "completed")[0]
	cases := map[string]func(map[string]any, map[string]any){
		"unknown envelope field": func(m, c map[string]any) { m["token"] = "private-value" },
		"unknown context field":  func(m, c map[string]any) { c["command"] = "private-value" },
		"unsupported schema": func(m, c map[string]any) {
			m["dataschema"] = "https://perfeng.dev/schemas/performance-integration/v2/schema.json"
		},
		"unsupported type":      func(m, c map[string]any) { m["type"] = "dev.perfeng.integration.requested.v2" },
		"wrong source":          func(m, c map[string]any) { m["source"] = "https://wrong.example" },
		"same attempt":          func(m, c map[string]any) { c["attempt"] = c["request"] },
		"deadline equality":     func(m, c map[string]any) { c["deadline"] = c["requestedAt"] },
		"event before creation": func(m, c map[string]any) { m["time"] = "2026-10-21T10:00:00Z" },
		"invalid date":          func(m, c map[string]any) { m["time"] = "2026-02-30T10:00:00Z" },
		"missing baseline":      func(m, c map[string]any) { delete(c, "baseline") },
		"null evidence":         func(m, c map[string]any) { c["evidence"] = nil },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			var message map[string]any
			if err := json.Unmarshal(raw, &message); err != nil {
				t.Fatal(err)
			}
			mutate(message, message["data"].(map[string]any)["context"].(map[string]any))
			encoded, err := json.Marshal(message)
			if err != nil {
				t.Fatal(err)
			}
			assertInvalidIntegration(t, encoded)
		})
	}
	assertInvalidIntegration(t, bytes.Replace(raw, []byte(`"specversion"`), []byte(`"id":"duplicate","specversion"`), 1))
	assertInvalidIntegration(t, append(append([]byte{}, raw...), []byte(` {}`)...))
	assertInvalidIntegration(t, append(append([]byte{}, raw...), bytes.Repeat([]byte(" "), contract.IntegrationMaximumBytes)...))
	if encoded, err := EncodeIntegrationMessage(contract.IntegrationMessage{}); err == nil || encoded != nil {
		t.Fatal("encoded zero message")
	}
}

func assertInvalidIntegration(t *testing.T, raw []byte) {
	t.Helper()
	message, err := DecodeIntegrationMessage(raw)
	if !errors.Is(err, contract.ErrIntegration) || !reflect.DeepEqual(message, contract.IntegrationMessage{}) || strings.Contains(err.Error(), "private-value") {
		t.Fatalf("unsafe validation result: %v", err)
	}
}

func TestIntegrationTimeAndNumericCompatibility(t *testing.T) {
	t.Parallel()
	fixtures := integrationFixture(t, "completed")
	accepted, err := DecodeIntegrationMessage(fixtures[1])
	if err != nil {
		t.Fatal(err)
	}
	accepted.Time = accepted.Data.Context.Deadline
	if raw, err := EncodeIntegrationMessage(accepted); err == nil || raw != nil {
		t.Fatal("accepted at deadline")
	}
	lifecycle, err := DecodeIntegrationMessage(fixtures[len(fixtures)-1])
	if err != nil {
		t.Fatal(err)
	}
	lifecycle.Time = "2026-10-23T10:00:00.123456+00:00"
	for _, number := range []string{"1e0", "9007199254740991"} {
		revision := json.Number(number)
		lifecycle.Data.Revision = &revision
		raw, err := EncodeIntegrationMessage(lifecycle)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := DecodeIntegrationMessage(raw)
		if err != nil || *decoded.Data.Revision != revision {
			t.Fatal("lost integer representation or late evidence")
		}
	}
	for _, number := range []string{"1.5", "9007199254740992"} {
		revision := json.Number(number)
		lifecycle.Data.Revision = &revision
		if raw, err := EncodeIntegrationMessage(lifecycle); err == nil || raw != nil {
			t.Fatal("accepted unsafe revision")
		}
	}
	request, err := DecodeIntegrationMessage(fixtures[0])
	if err != nil {
		t.Fatal(err)
	}
	request.Data.Context.Baseline = nil
	request.Data.Context.Evidence = []contract.CatalogReference{}
	request.Data.Context.Candidate.Source.Algorithm = "git-sha256"
	request.Data.Context.Candidate.Source.Digest = strings.Repeat("d", 64)
	if _, err := EncodeIntegrationMessage(request); err != nil {
		t.Fatal("wire decoder claimed provider admission authority", err)
	}
}

func TestIntegrationSchemaResolutionIsOffline(t *testing.T) {
	t.Parallel()
	for _, ref := range []string{"https://127.0.0.1/should-not-fetch.json", "file:///should-not-read.json"} {
		_, err := compileIntegrationSchema([]byte(`{"$ref":"` + ref + `"}`))
		if err == nil || !strings.Contains(err.Error(), "no URLLoader registered") {
			t.Fatalf("external resolver was not denied: %v", err)
		}
	}
}
