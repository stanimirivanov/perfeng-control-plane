package run

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"time"
)

// ErrTargetReserved means another Run holds the logical benchmark target. It never reveals its owner.
var ErrTargetReserved = errors.New("benchmark target is reserved")

// ErrBenchmarkConflict means persisted benchmark identity or sealed inputs cannot be replaced.
var ErrBenchmarkConflict = errors.New("benchmark reservation conflicts with immutable inputs")

var benchmarkUIDPattern = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)
var benchmarkTargetPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)

// BenchmarkIntent binds a logical target and observed incarnation to exact approved plan bytes.
// Plan is opaque to storage; trusted orchestration owns its encoding and interpretation.
type BenchmarkIntent struct {
	TargetID   string
	ClusterUID string
	Plan       []byte
	PlanSHA256 string
}

// Valid verifies bounded content identity, not target qualification or dispatch authorization.
func (intent BenchmarkIntent) Valid() bool {
	return len(intent.TargetID) <= 63 && benchmarkTargetPattern.MatchString(intent.TargetID) &&
		ValidBenchmarkUID(intent.ClusterUID) && len(intent.Plan) > 0 && len(intent.Plan) <= 1<<20 &&
		BenchmarkDigest(intent.Plan) == intent.PlanSHA256
}

// Same identifies exact immutable retries, including content bytes rather than hash alone.
func (intent BenchmarkIntent) Same(other BenchmarkIntent) bool {
	return intent.TargetID == other.TargetID && intent.ClusterUID == other.ClusterUID &&
		intent.PlanSHA256 == other.PlanSHA256 && bytes.Equal(intent.Plan, other.Plan)
}

// BenchmarkReservation is a durable hold, not an expiring worker lease. NamespaceUID and
// Request are absent until their respective write-once materialization steps commit.
type BenchmarkReservation struct {
	RunID        string
	Intent       BenchmarkIntent
	Admission    []byte
	NamespaceUID string
	Request      []byte
	Cleanup      *BenchmarkCleanup
}

// Clone isolates all persisted byte slices from caller mutation.
func (reservation BenchmarkReservation) Clone() BenchmarkReservation {
	reservation.Intent.Plan = bytes.Clone(reservation.Intent.Plan)
	reservation.Admission = bytes.Clone(reservation.Admission)
	reservation.Request = bytes.Clone(reservation.Request)
	if reservation.Cleanup != nil {
		cleanup := *reservation.Cleanup
		cleanup.Receipt = bytes.Clone(cleanup.Receipt)
		if cleanup.Next.Failure != nil {
			failure := *cleanup.Next.Failure
			cleanup.Next.Failure = &failure
		}
		if cleanup.Next.ToolExitCode != nil {
			exit := *cleanup.Next.ToolExitCode
			cleanup.Next.ToolExitCode = &exit
		}
		reservation.Cleanup = &cleanup
	}
	return reservation
}

// BenchmarkCleanup records the irreversible dispatch cutoff and its audit trail.
// Ready is calculated using database time; callers must not substitute a local clock.
// ReleasedAt is zero until verified cleanup and the lifecycle outcome commit together.
type BenchmarkCleanup struct {
	StartedAt  time.Time
	FencedAt   time.Time
	NotBefore  time.Time
	Ready      bool
	Next       Change
	Receipt    []byte
	ReleasedAt time.Time
}

// BenchmarkCleanupGrace exceeds the reviewed API server's one-minute mutation
// request bound. The quota fence remains after this drain period and release.
const BenchmarkCleanupGrace = 2 * time.Minute

// BenchmarkCleanupProof is an internal adapter attestation, not a public cleanup
// capability. Storage checks exact reservation identity before retaining its bytes.
type BenchmarkCleanupProof struct {
	SchemaVersion    int    `json:"schemaVersion"`
	RunID            string `json:"runId"`
	PlanSHA256       string `json:"planSha256"`
	ClusterUID       string `json:"clusterUid"`
	NamespaceUID     string `json:"namespaceUid"`
	ExecutorStopped  bool   `json:"executorStopped"`
	WorkloadStopped  bool   `json:"workloadStopped"`
	AuthorityRemoved bool   `json:"authorityRemoved"`
	NamespaceFenced  bool   `json:"namespaceFenced"`
}

// Matches rejects incomplete proofs and attestations for a different held incarnation.
func (proof BenchmarkCleanupProof) Matches(reservation BenchmarkReservation) bool {
	return proof.SchemaVersion == 1 && proof.RunID == reservation.RunID && proof.PlanSHA256 == reservation.Intent.PlanSHA256 &&
		proof.ClusterUID == reservation.Intent.ClusterUID && proof.NamespaceUID == reservation.NamespaceUID && proof.ExecutorStopped &&
		proof.WorkloadStopped && proof.AuthorityRemoved && proof.NamespaceFenced
}

// ValidBenchmarkUID checks canonical UUID syntax used for cluster and namespace incarnations.
func ValidBenchmarkUID(value string) bool { return benchmarkUIDPattern.MatchString(value) }

// BenchmarkDigest hashes the exact bytes used for durable intent and request fencing.
func BenchmarkDigest(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

// BenchmarkReservationStore fences benchmark materialization with the existing Run lease.
// Holds survive lease expiry and terminal states until verified cleanup is committed.
// Implementations serialize with lifecycle writes, honor cancellation and isolate returned bytes.
// External API calls must not occur inside these operations.
type BenchmarkReservationStore interface {
	// ReserveBenchmark atomically obtains the target for a PROVISIONING Run at expectedRevision.
	// Exact same-Run retries preserve the first admission bytes. Another Run returns ErrTargetReserved;
	// changing intent returns ErrBenchmarkConflict. Lease loss precedes revision/state errors.
	ReserveBenchmark(context.Context, Lease, int64, BenchmarkIntent, []byte) (BenchmarkReservation, error)
	// GetBenchmarkReservation returns this Run's hold and true, or false when absent, under a current
	// lease. It never exposes another principal's hold or interprets lease loss as absence.
	GetBenchmarkReservation(context.Context, Lease) (BenchmarkReservation, bool, error)
	// BindBenchmarkNamespace preserves the first observed namespace UID under PROVISIONING/revision
	// and intent fencing. An identical retry succeeds; a replacement UID conflicts permanently.
	BindBenchmarkNamespace(context.Context, Lease, int64, string, string) (BenchmarkReservation, error)
	// SealBenchmarkRequest preserves exact final request bytes after the namespace UID is bound.
	// It rechecks PROVISIONING, revision and intent; different bytes cannot replace the first request.
	SealBenchmarkRequest(context.Context, Lease, int64, string, []byte) (BenchmarkReservation, error)
	// BeginBenchmarkCleanup irreversibly forbids dispatch/materialization, preserves the first
	// database-time cutoff and freezes the intended lifecycle outcome. Cancellation wins
	// over that outcome at completion. Lease loss precedes revision and immutable-intent errors.
	BeginBenchmarkCleanup(context.Context, Lease, int64, string, Change) (BenchmarkReservation, error)
	// BindBenchmarkCleanupNamespace records a previously ambiguous namespace create during cleanup.
	// It is write-once, never creates a namespace and requires current revision/intent authority.
	BindBenchmarkCleanupNamespace(context.Context, Lease, int64, string, string) (BenchmarkReservation, error)
	// RecordBenchmarkCleanupFence starts the write-once database drain clock only after
	// the namespace quota barrier has been confirmed. Retries never shorten or renew it.
	RecordBenchmarkCleanupFence(context.Context, Lease, int64, string) (BenchmarkReservation, error)
	// FinishBenchmarkCleanup accepts a bounded verification receipt only after the database drain
	// deadline. Release and the lifecycle outcome are atomic; cancellation overrides the stored
	// outcome, while an already-terminal Run retains its snapshot. Audit records are never deleted.
	FinishBenchmarkCleanup(context.Context, Lease, int64, string, []byte) (Run, error)
}
