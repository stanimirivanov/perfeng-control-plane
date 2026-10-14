package run

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
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
}

// Clone isolates all persisted byte slices from caller mutation.
func (reservation BenchmarkReservation) Clone() BenchmarkReservation {
	reservation.Intent.Plan = bytes.Clone(reservation.Intent.Plan)
	reservation.Admission = bytes.Clone(reservation.Admission)
	reservation.Request = bytes.Clone(reservation.Request)
	return reservation
}

// ValidBenchmarkUID checks canonical UUID syntax used for cluster and namespace incarnations.
func ValidBenchmarkUID(value string) bool { return benchmarkUIDPattern.MatchString(value) }

// BenchmarkDigest hashes the exact bytes used for durable intent and request fencing.
func BenchmarkDigest(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

// BenchmarkReservationStore fences benchmark materialization with the existing Run lease.
// Holds survive lease expiry, cancellation and terminal states. No method releases or steals one.
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
}
