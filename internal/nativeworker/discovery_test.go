package nativeworker

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
)

func discoveryExample(t *testing.T, name string) Discovery {
	t.Helper()
	document, err := contract.Files.ReadFile("snapshot/api/native-worker/v2/examples/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var value Discovery
	if err := json.Unmarshal(document, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestDiscoveryValidatesCurrentSlotAndOneClock(t *testing.T) {
	for _, name := range []string{
		"discovery-active.json", "discovery-terminal.json", "discovery-none.json",
		"discovery-available.json", "discovery-unreported.json",
	} {
		if !discoveryExample(t, name).Valid() {
			t.Fatalf("invalid discovery fixture %s", name)
		}
	}
	for name, mutate := range map[string]func(*Discovery){
		"absent clock":         func(value *Discovery) { value.ServerTime = time.Time{} },
		"unknown state":        func(value *Discovery) { value.State = "UNKNOWN" },
		"unknown availability": func(value *Discovery) { value.Availability = "UNKNOWN" },
		"missing presence":     func(value *Discovery) { value.Availability = "UNREPORTED" },
		"missing attempt":      func(value *Discovery) { value.Attempt = nil },
		"missing expiry":       func(value *Discovery) { value.SlotExpiresAt = nil },
		"equal expiry":         func(value *Discovery) { value.SlotExpiresAt = &value.ServerTime },
		"different expiry": func(value *Discovery) {
			expiry := value.SlotExpiresAt.Add(time.Second)
			value.SlotExpiresAt = &expiry
		},
		"different clock": func(value *Discovery) { value.Attempt.ServerTime = value.ServerTime.Add(-time.Second) },
		"none with slot":  func(value *Discovery) { value.State = DiscoveryNone },
		"inactive slot": func(value *Discovery) {
			value.Attempt.State = InspectionInactive
			value.Attempt.LeaseID = ""
			value.Attempt.ExpiresAt = nil
			value.Attempt.RunRevision = 0
		},
	} {
		t.Run(name, func(t *testing.T) {
			value := discoveryExample(t, "discovery-active.json")
			mutate(&value)
			if value.Valid() {
				t.Fatal("accepted inconsistent discovery")
			}
		})
	}
	terminal := discoveryExample(t, "discovery-terminal.json")
	future := terminal.ServerTime.Add(time.Second)
	terminal.Attempt.AcceptedAt = &future
	if terminal.Valid() {
		t.Fatal("accepted a future terminal outcome")
	}
}
