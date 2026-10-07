package nativeworker

import (
	"regexp"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

const (
	maximumCandidateBatch = 100
	// MaximumPresenceBytes is the Native Worker API presence document limit.
	MaximumPresenceBytes = 65536
)

var nativeLeaseUUIDPattern = regexp.MustCompile(
	`^[a-f0-9]{8}-[a-f0-9]{4}-4[a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$`,
)

// Candidate is one principal-owned Run considered by the privileged native
// scheduler. Run is an isolated snapshot.
type Candidate struct {
	Principal string
	Run       run.Run
}

// PresenceObservation is one database-timestamped worker report used for
// scheduling. Presence is contract validated and independently owned.
type PresenceObservation struct {
	WorkerID   string
	ObservedAt time.Time
	Presence   Presence
}

// ValidPresenceMaximumAge accepts whole-second freshness windows from five
// seconds through five minutes.
func ValidPresenceMaximumAge(value time.Duration) bool {
	return value >= 5*time.Second && value <= 5*time.Minute && value%time.Second == 0
}

// ValidLeasePolicy accepts a contract-compatible TTL and a whole-second renewal
// interval of at least five seconds and no more than half the TTL.
func ValidLeasePolicy(ttl time.Duration, renewAfter time.Duration) bool {
	return run.ValidLeaseTTL(ttl) && renewAfter >= 5*time.Second &&
		renewAfter <= ttl/2 && renewAfter%time.Second == 0
}

// ValidCandidateLimit reports whether a scheduler batch is bounded.
func ValidCandidateLimit(limit int) bool {
	return limit >= 1 && limit <= maximumCandidateBatch
}

// ValidLeaseIdentifier reports whether value is a lowercase version-4 UUID
// issued by the native lease implementation.
func ValidLeaseIdentifier(value string) bool {
	return nativeLeaseUUIDPattern.MatchString(value)
}

// ValidLeaseCapability reports whether opaque authority can be transported as
// one bounded visible-ASCII HTTP header value.
func ValidLeaseCapability(value string) bool {
	if len(value) < 32 || len(value) > 4096 {
		return false
	}
	for index := range len(value) {
		if value[index] < 0x21 || value[index] > 0x7e || value[index] == ',' {
			return false
		}
	}

	return true
}
