package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/httpapi"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

var _ httpapi.NativeWorkerPresenceStore = (*Repository)(nil)

// RecordNativeWorkerPresence atomically replaces one authenticated worker's
// last-seen report and returns the database observation time used by the row.
func (r *Repository) RecordNativeWorkerPresence(
	ctx context.Context,
	record httpapi.NativeWorkerPresenceRecord,
) (time.Time, error) {
	if !validNativeWorkerPresenceRecord(record) {
		return time.Time{}, run.ErrValidation
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()

	var observedAt time.Time
	err := r.db.QueryRowContext(ctx, `
		INSERT INTO perfeng_control.native_worker_presence(
			worker_id,observed_at,availability,qualified,authorized_to_acquire,presence
		) VALUES ($1,clock_timestamp(),$2,$3,$4,$5)
		ON CONFLICT (worker_id) DO UPDATE SET
			observed_at=clock_timestamp(),
			availability=EXCLUDED.availability,
			qualified=EXCLUDED.qualified,
			authorized_to_acquire=EXCLUDED.authorized_to_acquire,
			presence=EXCLUDED.presence
		RETURNING observed_at`,
		record.WorkerID, record.Availability, record.Qualified,
		record.AuthorizedToAcquire, []byte(record.ObservedDocument),
	).Scan(&observedAt)
	if err != nil {
		return time.Time{}, storageError(err)
	}

	return observedAt.UTC(), nil
}

func validNativeWorkerPresenceRecord(record httpapi.NativeWorkerPresenceRecord) bool {
	if !contract.ValidNativeWorkerIdentifier(record.WorkerID) ||
		len(record.ObservedDocument) == 0 || len(record.ObservedDocument) > httpapi.MaxBodyBytes {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(record.ObservedDocument))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil || contract.ValidateWorkerPresence(value) != nil {
		return false
	}
	object, ok := value.(map[string]any)
	if !ok || object["availability"] != record.Availability {
		return false
	}
	var trailing any

	return decoder.Decode(&trailing) == io.EOF
}
