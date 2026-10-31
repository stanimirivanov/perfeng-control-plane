package postgres

import (
	"errors"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/httpapi"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func TestNativeWorkerPresencePersistsV2Only(t *testing.T) {
	repository := openTest(t, testDatabase(t))
	if err := repository.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	document, err := contract.Files.ReadFile("snapshot/api/native-worker/v2/examples/presence.json")
	if err != nil {
		t.Fatal(err)
	}
	record := httpapi.NativeWorkerPresenceRecord{
		WorkerID: "linux-workstation-01", ObservedDocument: document,
		Availability: "AVAILABLE", Qualified: true, AuthorizedToAcquire: true,
	}
	if _, err := repository.RecordNativeWorkerV2Presence(testContext, record); err != nil {
		t.Fatal(err)
	}
	var version string
	if err := repository.db.QueryRow(`
		SELECT presence->>'protocolVersion'
		FROM perfeng_control.native_worker_presence_v2 WHERE worker_id=$1
	`, record.WorkerID).Scan(&version); err != nil || version != "2" {
		t.Fatalf("stored protocol version = %q, err=%v", version, err)
	}
	observation, err := repository.EligibleNativeWorkerV2(testContext, record.WorkerID, 2*time.Minute)
	if err != nil || observation.Presence.ProtocolVersion != 2 {
		t.Fatalf("eligible worker = %#v, err=%v", observation, err)
	}
	record.ObservedDocument = []byte(`{"protocolVersion":1}`)
	if _, err := repository.RecordNativeWorkerV2Presence(testContext, record); !errors.Is(err, run.ErrValidation) {
		t.Fatalf("retired protocol accepted by v2 storage: %v", err)
	}
}
