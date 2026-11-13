// Package runrerun owns private durable rerun intent and acceptance, not HTTP or execution.
package runrerun

import (
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/runcomparison"
	"github.com/stanimirivanov/perfeng-control-plane/internal/serviceaccess"
)

// Intent pins the inspected report. Actor and Caller must come from independently
// verified authentication/delegation, never browser claims or a BFF bearer alone.
type Intent struct {
	SourceRunID, Key, ReportID, ReportSHA256, Reason string
	Actor, Caller                                    serviceaccess.Identity
	ActorKind                                        string
}

var sourcePattern = regexp.MustCompile(`^perf-[0-9]{8}-[0-9]{6}-[a-f0-9]{8}$`)
var reportPattern = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)
var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// Validate rejects malformed private intent without establishing any authority.
func (intent Intent) Validate() error {
	if !sourcePattern.MatchString(intent.SourceRunID) || !run.ValidKey(intent.Key) || !reportPattern.MatchString(intent.ReportID) || !digestPattern.MatchString(intent.ReportSHA256) || !intent.Actor.Valid() || !intent.Caller.Valid() || (intent.ActorKind != "HUMAN" && intent.ActorKind != "CI") || !utf8.ValidString(intent.Reason) || utf8.RuneCountInString(intent.Reason) > 1024 || strings.TrimSpace(intent.Reason) == "" || strings.ContainsRune(intent.Reason, 0) {
		return run.ErrValidation
	}
	return nil
}

// Acceptance preserves original child acceptance and verified attribution. Inputs
// is private retained reporting material; it is not a public RerunReceipt codec.
type Acceptance struct {
	ID                    string
	Intent                Intent
	Source                serviceaccess.Binding
	Child                 run.Run
	ChildPrincipal        string
	Report                run.Artifact
	AcceptedAt, ExpiresAt time.Time
	Inputs                runcomparison.Inputs `json:"-"`
}

// Replay compares a validated intent to immutable acceptance. Caller rotation is
// not a new actor/key scope; expiry never authorizes replacing the original child.
func (value Acceptance) Replay(intent Intent, now time.Time) (Acceptance, error) {
	original := value.Intent
	original.Caller = intent.Caller
	if original != intent || !now.Before(value.ExpiresAt) {
		return Acceptance{}, run.ErrConflict
	}
	value.Inputs = value.Inputs.Clone()
	return value, nil
}

// Validate checks private acceptance relationships, not credential/delegation or
// current evidence access. Stored data must pass this again after decoding.
func (value Acceptance) Validate() error {
	report := value.Report
	if value.Intent.Validate() != nil || !reportPattern.MatchString(value.ID) || !value.Source.Service.Valid() || !value.Source.Creator.Valid() ||
		(value.Source.CreatorKind != "HUMAN" && value.Source.CreatorKind != "CI") || value.Source.OwnerPrincipal == "" || strings.TrimSpace(value.Source.ReviewReference) == "" || value.ChildPrincipal == "" ||
		report.Validate() != nil || report.ID != value.Intent.ReportID || report.SHA256 != value.Intent.ReportSHA256 || report.RunID != value.Intent.SourceRunID || report.Kind != "normalized" || report.Format != "analysis-result/v1" || report.MediaType != "application/json" || report.SizeBytes < 1 || report.SizeBytes > 1048576 ||
		!sourcePattern.MatchString(value.Child.ID) || value.Child.ID == value.Intent.SourceRunID || value.Child.State != run.StateCreated || value.Child.Revision != 1 || value.Child.Request.Validate() != nil || value.AcceptedAt.IsZero() || !value.Child.CreatedAt.Equal(value.AcceptedAt) || !value.Child.UpdatedAt.Equal(value.AcceptedAt) || value.ExpiresAt.Before(value.AcceptedAt.Add(24*time.Hour)) ||
		value.Inputs.Validate(run.Run{ID: value.Intent.SourceRunID, Request: value.Child.Request}) != nil {
		return run.ErrValidation
	}
	return nil
}
