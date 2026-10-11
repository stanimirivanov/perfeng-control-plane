package nativeworker

import "time"

// DiscoveryState describes current worker capacity, never acquisition history.
type DiscoveryState string

const (
	// DiscoveryNone means no owned v2 lease occupies the measurement slot.
	DiscoveryNone DiscoveryState = "NONE"
	// DiscoveryFound identifies the single currently occupying attempt.
	DiscoveryFound DiscoveryState = "FOUND"
)

// Discovery is a consistent, non-secret observation of an authenticated worker.
// Availability is stored intent, not a freshness or acquisition eligibility check.
// UNREPORTED denotes absent presence; NONE is not permission to delete evidence.
type Discovery struct {
	State         DiscoveryState `json:"state"`
	ServerTime    time.Time      `json:"serverTime"`
	Availability  string         `json:"availability"`
	SlotExpiresAt *time.Time     `json:"slotExpiresAt,omitempty"`
	Attempt       *Inspection    `json:"attempt,omitempty"`
}

// Valid checks the closed union, exclusive expiry and common observation clock.
func (discovery Discovery) Valid() bool {
	if discovery.ServerTime.IsZero() {
		return false
	}
	switch discovery.Availability {
	case "UNREPORTED", "AVAILABLE", "DRAINING", "UNAVAILABLE":
	default:
		return false
	}
	switch discovery.State {
	case DiscoveryNone:
		return discovery.SlotExpiresAt == nil && discovery.Attempt == nil
	case DiscoveryFound:
		if discovery.Availability == "UNREPORTED" || discovery.SlotExpiresAt == nil ||
			!discovery.SlotExpiresAt.After(discovery.ServerTime) || discovery.Attempt == nil ||
			!discovery.Attempt.Valid() || !discovery.Attempt.ServerTime.Equal(discovery.ServerTime) {
			return false
		}
		return discovery.Attempt.State == InspectionTerminal ||
			(discovery.Attempt.State == InspectionActive &&
				discovery.Attempt.ExpiresAt.Equal(*discovery.SlotExpiresAt))
	default:
		return false
	}
}
