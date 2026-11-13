package runrerun

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"

	"github.com/stanimirivanov/perfeng-control-plane/internal/serviceaccess"
)

func TestIntentRejectsUntrustedOrUnboundedAttribution(t *testing.T) {
	valid := Intent{SourceRunID: "perf-20260902-130000-a1b2c3d5", Key: "rerun-maya-123456", ReportID: "88888888-8888-4888-8888-888888888888", ReportSHA256: strings.Repeat("a", 64), Reason: "Investigate regression", Actor: serviceaccess.Identity{Issuer: "urn:people", ID: "maya"}, ActorKind: "HUMAN", Caller: serviceaccess.Identity{Issuer: "urn:clients", ID: "console"}}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Intent){func(v *Intent) { v.Key = "short" }, func(v *Intent) { v.ActorKind = "ADMIN" }, func(v *Intent) { v.Caller.Issuer = "not a URI" }, func(v *Intent) { v.Reason = " " }, func(v *Intent) { v.Reason = strings.Repeat("x", 1025) }, func(v *Intent) { v.ReportID = "not-an-id" }, func(v *Intent) { v.ReportSHA256 = "unverified" }, func(v *Intent) { v.SourceRunID = "arbitrary" }} {
		value := valid
		change(&value)
		if value.Validate() == nil {
			t.Fatal("invalid intent admitted", value)
		}
	}
}

func TestReplayDeadlineAndCallerRotationPreserveOriginalAcceptance(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	original := Acceptance{ID: "accepted", Intent: Intent{Key: "original-intent-01", Reason: "investigate", Caller: serviceaccess.Identity{Issuer: "urn:clients", ID: "original"}}, AcceptedAt: now, ExpiresAt: now.Add(24 * time.Hour)}
	rotated := original.Intent
	rotated.Caller.ID = "renewed"
	got, err := original.Replay(rotated, now)
	if err != nil || got.Intent.Caller != original.Intent.Caller {
		t.Fatal("replay rewrote caller", err)
	}
	if _, err = original.Replay(rotated, original.ExpiresAt); !errors.Is(err, run.ErrConflict) {
		t.Fatal("expired key reused", err)
	}
	rotated.Reason = "new intent"
	if _, err = original.Replay(rotated, now); !errors.Is(err, run.ErrConflict) {
		t.Fatal("changed intent accepted", err)
	}
}
