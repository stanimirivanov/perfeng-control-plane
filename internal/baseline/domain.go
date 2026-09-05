// Package baseline owns versioned performance baseline lifecycle rules.
package baseline

import (
	"errors"
	"math"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

// ErrTransition identifies a lifecycle edge that the baseline contract does not allow.
var ErrTransition = errors.New("invalid baseline transition")

// State identifies a baseline's qualification and approval lifecycle state.
type State string

const (
	// StateCandidate is not eligible for use as a comparison anchor.
	StateCandidate State = "CANDIDATE"
	// StateQualified has passed evidence checks but still requires approval.
	StateQualified State = "QUALIFIED"
	// StateApproved is eligible for explicit policy selection.
	StateApproved State = "APPROVED"
	// StateRetired is terminal and cannot be selected for new comparisons.
	StateRetired State = "RETIRED"
)

// QualificationStatus identifies whether baseline evidence passed review.
type QualificationStatus string

const (
	// QualificationPending has no recorded sample-count or variability evidence.
	QualificationPending QualificationStatus = "PENDING"
	// QualificationPassed records evidence that met the reviewed policy.
	QualificationPassed QualificationStatus = "PASSED"
	// QualificationFailed records at least one safe rejection reason.
	QualificationFailed QualificationStatus = "FAILED"
)

var (
	gitSHA      = regexp.MustCompile(`^[A-Fa-f0-9]{40}$`)
	fingerprint = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

const maximumRevision int64 = 9007199254740991

// Software identifies the exact candidate represented by a baseline.
type Software struct {
	GitSHA  string `json:"gitSha"`
	Image   string `json:"image"`
	Version string `json:"version,omitempty"`
}

// Environment binds a reviewed definition to its observed fingerprint.
type Environment struct {
	rawresult.Identity
	Fingerprint string `json:"fingerprint"`
}

// Dataset records either no external data or one versioned deterministic set.
type Dataset struct {
	Kind    string `json:"kind"`
	ID      string `json:"id,omitempty"`
	Version string `json:"version,omitempty"`
	SHA256  string `json:"sha256,omitempty"`
	Seed    *int64 `json:"seed,omitempty"`
}

// Qualification records observed evidence, not universal acceptance thresholds.
type Qualification struct {
	Status      QualificationStatus `json:"status"`
	Reasons     []string            `json:"reasons"`
	SampleCount *int64              `json:"sampleCount,omitempty"`
	MaximumCV   *float64            `json:"maximumCv,omitempty"`
}

// Event is one append-only lifecycle decision.
type Event struct {
	State  State     `json:"state"`
	At     time.Time `json:"at"`
	Actor  string    `json:"actor"`
	Reason string    `json:"reason"`
}

// Record is one immutable baseline version and its approval history.
type Record struct {
	SchemaVersion int                `json:"schemaVersion"`
	Kind          string             `json:"kind"`
	ID            string             `json:"id"`
	Version       string             `json:"version"`
	Revision      int64              `json:"revision"`
	State         State              `json:"state"`
	TestID        string             `json:"testId"`
	SourceRunID   string             `json:"sourceRunId"`
	Artifact      run.Artifact       `json:"artifact"`
	Software      Software           `json:"software"`
	Workload      rawresult.Identity `json:"workload"`
	Environment   Environment        `json:"environment"`
	Dataset       Dataset            `json:"dataset"`
	Qualification Qualification      `json:"qualification"`
	CreatedAt     time.Time          `json:"createdAt"`
	Lifecycle     []Event            `json:"lifecycle"`
}

// Create supplies immutable identity and provenance for a new candidate.
type Create struct {
	ID          string
	Version     string
	TestID      string
	SourceRunID string
	Artifact    run.Artifact
	Software    Software
	Workload    rawresult.Identity
	Environment Environment
	Dataset     Dataset
	Actor       string
	Reason      string
}

// Change requests one explicit forward lifecycle transition.
type Change struct {
	State         State
	Qualification *Qualification
	Actor         string
	Reason        string
}

// Selection pins one policy reference and the trusted compatibility dimensions
// of the candidate evidence. It never implies a latest-version lookup.
type Selection struct {
	ID          string
	Version     string
	TestID      string
	Workload    rawresult.Identity
	Environment Environment
	Dataset     Dataset
}

// New creates revision one in CANDIDATE with pending qualification.
func New(input Create, now time.Time) (Record, error) {
	record := Record{
		SchemaVersion: 1,
		Kind:          "PerformanceBaseline",
		ID:            input.ID,
		Version:       input.Version,
		Revision:      1,
		State:         StateCandidate,
		TestID:        input.TestID,
		SourceRunID:   input.SourceRunID,
		Artifact:      input.Artifact,
		Software:      input.Software,
		Workload:      input.Workload,
		Environment:   input.Environment,
		Dataset:       cloneDataset(input.Dataset),
		Qualification: Qualification{
			Status:  QualificationPending,
			Reasons: []string{},
		},
		CreatedAt: now,
		Lifecycle: []Event{{
			State: StateCandidate, At: now, Actor: input.Actor, Reason: input.Reason,
		}},
	}
	if record.Validate() != nil {
		return Record{}, run.ErrValidation
	}

	return record, nil
}

// Transition applies one revision-checked lifecycle decision and appends its audit event.
func (record Record) Transition(expectedRevision int64, change Change, now time.Time) (Record, error) {
	if record.Revision != expectedRevision {
		return Record{}, run.ErrRevision
	}
	if record.Validate() != nil {
		return Record{}, run.ErrValidation
	}
	if record.Revision >= maximumRevision || !allowedTransition(record.State, change.State) {
		return Record{}, ErrTransition
	}
	if !validAuditText(change.Actor, 128) || !validAuditText(change.Reason, 1024) ||
		!validTimestamp(now) || now.Before(record.Lifecycle[len(record.Lifecycle)-1].At) {
		return Record{}, run.ErrValidation
	}

	next := record.Clone()
	switch change.State {
	case StateQualified:
		if change.Qualification == nil || change.Qualification.Status != QualificationPassed ||
			change.Qualification.Validate() != nil {
			return Record{}, run.ErrValidation
		}
		next.Qualification = change.Qualification.Clone()
	case StateRetired:
		if change.Qualification != nil {
			if record.State != StateCandidate ||
				change.Qualification.Status != QualificationFailed ||
				change.Qualification.Validate() != nil {
				return Record{}, run.ErrValidation
			}
			next.Qualification = change.Qualification.Clone()
		}
	default:
		if change.Qualification != nil {
			return Record{}, run.ErrValidation
		}
	}

	next.State = change.State
	next.Revision++
	next.Lifecycle = append(next.Lifecycle, Event{
		State: change.State, At: now, Actor: change.Actor, Reason: change.Reason,
	})
	if next.Validate() != nil {
		return Record{}, run.ErrValidation
	}

	return next, nil
}

// Validate checks baseline identity, evidence, lifecycle and qualification consistency.
func (record Record) Validate() error {
	if record.SchemaVersion != 1 || record.Kind != "PerformanceBaseline" ||
		!rawresult.ValidResourceID(record.ID) ||
		!rawresult.ValidContractsVersion(record.Version) || record.Revision < 1 ||
		record.Revision > maximumRevision ||
		!rawresult.ValidResourceID(record.TestID) || !contract.ValidID(record.SourceRunID) ||
		!validArtifact(record.Artifact, record.SourceRunID) || !record.Software.valid() ||
		record.Workload.Validate() != nil || record.Environment.Validate() != nil ||
		record.Dataset.Validate() != nil || record.Qualification.Validate() != nil ||
		!validTimestamp(record.CreatedAt) || len(record.Lifecycle) == 0 ||
		record.Revision != int64(len(record.Lifecycle)) {
		return run.ErrValidation
	}
	if record.State == StateCandidate && record.Qualification.Status == QualificationPassed {
		return run.ErrValidation
	}
	if (record.State == StateQualified || record.State == StateApproved) &&
		record.Qualification.Status != QualificationPassed {
		return run.ErrValidation
	}
	if record.Lifecycle[0].State != StateCandidate ||
		!record.Lifecycle[0].At.Equal(record.CreatedAt) ||
		record.Lifecycle[len(record.Lifecycle)-1].State != record.State {
		return run.ErrValidation
	}
	for index, event := range record.Lifecycle {
		if !validTimestamp(event.At) || !validAuditText(event.Actor, 128) ||
			!validAuditText(event.Reason, 1024) {
			return run.ErrValidation
		}
		if index > 0 && (!allowedTransition(record.Lifecycle[index-1].State, event.State) ||
			event.At.Before(record.Lifecycle[index-1].At)) {
			return run.ErrValidation
		}
	}

	return nil
}

// Clone returns a record without shared mutable qualification or lifecycle data.
func (record Record) Clone() Record {
	record.Dataset = cloneDataset(record.Dataset)
	record.Qualification = record.Qualification.Clone()
	record.Lifecycle = append([]Event(nil), record.Lifecycle...)

	return record
}

// MatchesSelection reports whether an approved Record is the exact policy-selected
// version and is compatible with the candidate evidence dimensions.
func (record Record) MatchesSelection(selection Selection) bool {
	return record.Validate() == nil && selection.Validate() == nil &&
		record.State == StateApproved && record.ID == selection.ID &&
		record.Version == selection.Version && record.TestID == selection.TestID &&
		record.Workload == selection.Workload && record.Environment == selection.Environment &&
		record.Dataset.equal(selection.Dataset)
}

// Validate checks the pinned identity and every required compatibility dimension.
func (selection Selection) Validate() error {
	if !rawresult.ValidResourceID(selection.ID) ||
		!rawresult.ValidContractsVersion(selection.Version) ||
		!rawresult.ValidResourceID(selection.TestID) ||
		selection.Workload.Validate() != nil || selection.Environment.Validate() != nil ||
		selection.Dataset.Validate() != nil {
		return run.ErrValidation
	}

	return nil
}

// Validate checks qualification status, reasons and observed evidence.
func (qualification Qualification) Validate() error {
	switch qualification.Status {
	case QualificationPending:
		if qualification.SampleCount != nil || qualification.MaximumCV != nil {
			return run.ErrValidation
		}
	case QualificationPassed:
		if qualification.SampleCount == nil || *qualification.SampleCount < 1 ||
			qualification.MaximumCV == nil || *qualification.MaximumCV < 0 ||
			math.IsNaN(*qualification.MaximumCV) || math.IsInf(*qualification.MaximumCV, 0) {
			return run.ErrValidation
		}
	case QualificationFailed:
		if len(qualification.Reasons) == 0 ||
			(qualification.SampleCount != nil && *qualification.SampleCount < 1) ||
			(qualification.MaximumCV != nil && (*qualification.MaximumCV < 0 ||
				math.IsNaN(*qualification.MaximumCV) || math.IsInf(*qualification.MaximumCV, 0))) {
			return run.ErrValidation
		}
	default:
		return run.ErrValidation
	}
	seen := make(map[string]struct{}, len(qualification.Reasons))
	for _, reason := range qualification.Reasons {
		if !validText(reason) {
			return run.ErrValidation
		}
		if _, exists := seen[reason]; exists {
			return run.ErrValidation
		}
		seen[reason] = struct{}{}
	}

	return nil
}

// Clone returns qualification evidence without shared pointers or reason storage.
func (qualification Qualification) Clone() Qualification {
	qualification.Reasons = append([]string(nil), qualification.Reasons...)
	if qualification.SampleCount != nil {
		value := *qualification.SampleCount
		qualification.SampleCount = &value
	}
	if qualification.MaximumCV != nil {
		value := *qualification.MaximumCV
		qualification.MaximumCV = &value
	}

	return qualification
}

// Validate checks the none or versioned dataset identity.
func (dataset Dataset) Validate() error {
	switch dataset.Kind {
	case "none":
		if dataset.ID != "" || dataset.Version != "" || dataset.SHA256 != "" || dataset.Seed != nil {
			return run.ErrValidation
		}
	case "versioned":
		identity := rawresult.Identity{
			ID: dataset.ID, Version: dataset.Version, SHA256: dataset.SHA256,
		}
		if identity.Validate() != nil || dataset.Seed == nil || *dataset.Seed < 0 {
			return run.ErrValidation
		}
	default:
		return run.ErrValidation
	}

	return nil
}

func (dataset Dataset) equal(other Dataset) bool {
	if dataset.Kind != other.Kind || dataset.ID != other.ID ||
		dataset.Version != other.Version || dataset.SHA256 != other.SHA256 {
		return false
	}
	if dataset.Seed == nil || other.Seed == nil {
		return dataset.Seed == nil && other.Seed == nil
	}

	return *dataset.Seed == *other.Seed
}

func (software Software) valid() bool {
	return gitSHA.MatchString(software.GitSHA) && rawresult.ValidImage(software.Image) &&
		(software.Version == "" || validText(software.Version))
}

// Validate checks the pinned definition identity and observed fingerprint.
func (environment Environment) Validate() error {
	if environment.Identity.Validate() != nil || !fingerprint.MatchString(environment.Fingerprint) {
		return run.ErrValidation
	}

	return nil
}

func validArtifact(artifact run.Artifact, sourceRunID string) bool {
	return artifact.Validate() == nil && artifact.RunID == sourceRunID &&
		artifact.Kind == "normalized" && artifact.MediaType == "application/json" &&
		artifact.Format == "normalized-result/v1"
}

func allowedTransition(from, to State) bool {
	switch from {
	case StateCandidate:
		return to == StateQualified || to == StateRetired
	case StateQualified:
		return to == StateApproved || to == StateRetired
	case StateApproved:
		return to == StateRetired
	default:
		return false
	}
}

func validAuditText(value string, maximum int) bool {
	return validText(value) &&
		utf8.RuneCountInString(value) <= maximum
}

func validText(value string) bool {
	return utf8.ValidString(value) && strings.TrimSpace(value) != ""
}

func validTimestamp(value time.Time) bool {
	return rawresult.ValidTimestamp(value.Format(time.RFC3339Nano))
}

func cloneDataset(dataset Dataset) Dataset {
	if dataset.Seed != nil {
		value := *dataset.Seed
		dataset.Seed = &value
	}

	return dataset
}
