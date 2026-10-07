package nativeworker

import (
	"bytes"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
)

func TestParseV2PresenceValidatesContractAndCanonicalEvidence(t *testing.T) {
	v2, err := contract.Files.ReadFile("snapshot/api/native-worker/v2/examples/presence.json")
	if err != nil {
		t.Fatal(err)
	}
	presence, err := ParseV2Presence(v2, len(v2))
	if err != nil || presence.ProtocolVersion != 2 ||
		presence.Capabilities.RunnerContract != 2 || presence.Availability != "AVAILABLE" {
		t.Fatalf("v2 presence = %#v, err = %v", presence, err)
	}
	if bytes.Contains(presence.QualificationEvidence, []byte("\n")) {
		t.Fatal("v2 qualification evidence is not canonical")
	}
	if _, err := ParseV2Presence(v2, len(v2)-1); err == nil {
		t.Fatal("oversized v2 presence accepted")
	}
	for name, document := range map[string][]byte{
		"retired protocol":         bytes.Replace(v2, []byte(`"protocolVersion": 2`), []byte(`"protocolVersion": 1`), 1),
		"duplicate capacity":       bytes.Replace(v2, []byte(`"capacity": 1`), []byte(`"capacity": 1, "capacity": 1`), 1),
		"worker-selected identity": bytes.Replace(v2, []byte(`"protocolVersion": 2`), []byte(`"protocolVersion": 2, "workerId": "worker-controlled"`), 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseV2Presence(document, len(document)); err == nil {
				t.Fatal("invalid v2 presence accepted")
			}
		})
	}
}
