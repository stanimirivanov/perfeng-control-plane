package run

import (
	"strings"
	"testing"
	"time"
)

func TestClaimValidation(t *testing.T) {
	if !ValidClaimOptions("worker-1", 10, 30*time.Second) {
		t.Fatal("valid options rejected")
	}
	for _, worker := range []string{"", "has spaces", strings.Repeat("a", 129)} {
		if ValidClaimOptions(worker, 1, 30*time.Second) {
			t.Fatal("invalid worker accepted")
		}
	}
	for _, limit := range []int{0, -1, 101} {
		if ValidClaimOptions("worker", limit, 30*time.Second) {
			t.Fatal("invalid batch accepted")
		}
	}
	for _, ttl := range []time.Duration{0, 4 * time.Second, 301 * time.Second, 5500 * time.Millisecond} {
		if ValidLeaseTTL(ttl) {
			t.Fatal("invalid TTL accepted")
		}
	}
	for _, delay := range []time.Duration{-time.Second, 301 * time.Second, time.Millisecond} {
		if ValidRetryDelay(delay) {
			t.Fatal("invalid delay accepted")
		}
	}
	if !ValidRetryDelay(0) || !ValidRetryDelay(5*time.Minute) {
		t.Fatal("valid delay rejected")
	}
	l := Lease{RunID: "perf-20260903-120000-12345678", Principal: "alice", WorkerID: "worker", Token: strings.Repeat("a", 32)}
	if !l.Valid() {
		t.Fatal("valid lease rejected")
	}
	l.Token = ""
	if l.Valid() {
		t.Fatal("missing token accepted")
	}
}
