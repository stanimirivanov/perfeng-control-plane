package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/integration"
	"github.com/stanimirivanov/perfeng-control-plane/internal/integrationcontract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func TestPublicationAssociationOffline(t *testing.T) {
	raw, err := contract.Files.ReadFile("snapshot/examples/performance-analysis/published.json")
	if err != nil {
		t.Fatal(err)
	}
	message, err := integrationcontract.DecodeAnalysisPublication(raw)
	if err != nil {
		t.Fatal(err)
	}
	var artifact run.Artifact
	claim := message.Data.Artifact
	claim.Kind = "normalized"
	raw, err = json.Marshal(claim)
	if err != nil || json.Unmarshal(raw, &artifact) != nil {
		t.Fatal("invalid fixture artifact", err)
	}
	record := integration.Record{Run: &run.Run{ID: artifact.RunID}, Native: &message.Data.Native,
		Disposition: contract.IntegrationMessage{Source: message.Source, Time: message.Time,
			Data: contract.IntegrationData{Context: message.Data.Context, Run: &message.Data.Run}}}
	if !publicationMatches(message, record, artifact) {
		t.Fatal("retained association rejected")
	}
	for _, alter := range []func(*contract.AnalysisPublication){
		func(m *contract.AnalysisPublication) { m.Data.Artifact.Kind = "normalized" },
		func(m *contract.AnalysisPublication) { m.Data.Artifact.SHA256 = strings.Repeat("f", 64) },
		func(m *contract.AnalysisPublication) { m.Data.Native.TestID += "-forged" },
		func(m *contract.AnalysisPublication) { m.Data.Context.Request.ID += "-forged" },
		func(m *contract.AnalysisPublication) { m.Time = m.Data.Context.RequestedAt },
	} {
		changed := message
		alter(&changed)
		if publicationMatches(changed, record, artifact) {
			t.Fatal("changed association accepted")
		}
	}
	record.Native = nil
	if publicationMatches(message, record, artifact) {
		t.Fatal("legacy admission inferred native evidence")
	}
}

// This fixture mocks trusted report verification, not its byte/policy/provenance
// checks: those belong to the offline builder tests. Here the database is real.
func publicationFixture(t *testing.T, r *Repository, suffix string) contract.AnalysisPublication {
	t.Helper()
	request := integrationRequest(t, r, suffix)
	request.Data.Context.Baseline = nil
	review := func(context.Context, string, contract.IntegrationContext, func(context.Context, string, string, string) (baseline.Record, error)) (integration.AdmissionResolution, error) {
		return integration.AdmissionResolution{Request: testRequest(t), Target: run.ExecutionTargetKubernetes,
			Workload: rawresult.Identity{ID: "retained-workload", Version: "1.0.0", SHA256: strings.Repeat("b", 64)}}, nil
	}
	record, err := r.AdmitIntegrationResolved(testContext, "alice", "https://provider.example/identity", request, review)
	if err != nil || record.Run == nil || record.Native == nil {
		t.Fatal("native admission", err)
	}
	artifact := artifactFor(record.Run.ID)
	digest := sha256.Sum256([]byte(suffix))
	uuid := hex.EncodeToString(digest[:16])
	artifact.ID = uuid[:8] + "-" + uuid[8:12] + "-4" + uuid[13:16] + "-8" + uuid[17:20] + "-" + uuid[20:]
	artifact.Kind, artifact.Format = "normalized", "analysis-result/v1"
	if err := r.RegisterArtifact(testContext, "alice", artifact); err != nil {
		t.Fatal(err)
	}
	var claim contract.AnalysisArtifactClaim
	raw, err := json.Marshal(artifact)
	if err != nil || json.Unmarshal(raw, &claim) != nil {
		t.Fatal("artifact claim", err)
	}
	claim.Kind = "analysis"
	message := contract.AnalysisPublication{SpecVersion: "1.0", ID: "publication-" + suffix, Source: record.Disposition.Source,
		Type: "dev.perfeng.integration.analysis-published.v1", Time: record.Disposition.Time, DataContentType: "application/json",
		DataSchema: "https://perfeng.dev/schemas/performance-analysis/v1/performance-analysis.schema.json",
		Data:       contract.AnalysisPublicationData{Context: record.Disposition.Data.Context, Run: *record.Disposition.Data.Run, Native: *record.Native, Artifact: claim}}
	if _, err := integrationcontract.EncodeAnalysisPublication(message); err != nil {
		t.Fatal(err)
	}

	return message
}

func TestAnalysisPublicationConcurrentReplayScopedReadAndReopen(t *testing.T) {
	dsn := testDatabase(t)
	r := openTest(t, dsn)
	// Upgrade representative bound admission/report data from schema 0020.
	if _, err := r.db.Exec(`CREATE SCHEMA perfeng_control; CREATE TABLE perfeng_control.schema_migrations(version text PRIMARY KEY,sha256 text NOT NULL,applied_at timestamptz NOT NULL DEFAULT clock_timestamp())`); err != nil {
		t.Fatal(err)
	}
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() >= "0021_" {
			break
		}
		content, err := migrations.ReadFile("migrations/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.db.Exec(string(content)); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(content)
		if _, err := r.db.Exec(`INSERT INTO perfeng_control.schema_migrations(version,sha256) VALUES($1,$2)`, entry.Name(), hex.EncodeToString(digest[:])); err != nil {
			t.Fatal(err)
		}
	}
	message := publicationFixture(t, r, "publication-replay")
	if err := r.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	if err := r.Migrate(testContext); err != nil {
		t.Fatal("repeat migration", err)
	}
	identity := message.Data.Context.Request
	if _, err := r.GetAnalysisPublication(testContext, "alice", identity); !errors.Is(err, run.ErrNotFound) {
		t.Fatal("unpublished request visible", err)
	}
	var calls atomic.Int32
	verify := func(_ context.Context, record integration.Record, proposed contract.AnalysisPublication) ([]byte, error) {
		calls.Add(1)
		if record.Native == nil || record.Native.RunID != proposed.Data.Native.RunID {
			t.Error("verifier did not receive retained admission")
		}

		return integrationcontract.EncodeAnalysisPublication(proposed)
	}
	expected, err := integrationcontract.EncodeAnalysisPublication(message)
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	for range 3 {
		wait.Go(func() {
			got, err := r.RetainAnalysisPublication(testContext, "alice", message, verify)
			if err != nil || !bytes.Equal(got, expected) {
				t.Error("concurrent retention", err)
			}
		})
	}
	wait.Wait()
	if calls.Load() != 1 {
		t.Fatal("exact replay reverified", calls.Load())
	}
	reopened := openTest(t, dsn)
	got, err := reopened.GetAnalysisPublication(testContext, "alice", identity)
	if err != nil || !bytes.Equal(got, expected) {
		t.Fatal("reopened read", err)
	}
	got[0] = '!'
	if got, err := reopened.RetainAnalysisPublication(testContext, "alice", message, nil); err != nil || !bytes.Equal(got, expected) {
		t.Fatal("replay lost original owned bytes", err)
	}
	for _, principal := range []string{"bob", "unknown"} {
		if got, err := r.GetAnalysisPublication(testContext, principal, identity); !errors.Is(err, run.ErrNotFound) || got != nil {
			t.Fatal("cross-principal disclosure", err)
		}
	}
	changed := message
	changed.ID += "-changed"
	if got, err := r.RetainAnalysisPublication(testContext, "alice", changed, verify); !errors.Is(err, run.ErrConflict) || got != nil {
		t.Fatal("second publication accepted", err)
	}
	other := publicationFixture(t, r, "publication-other")
	other.ID = message.ID
	if got, err := r.RetainAnalysisPublication(testContext, "alice", other, verify); !errors.Is(err, run.ErrConflict) || got != nil {
		t.Fatal("event reused for another request", err)
	}
}

func TestAnalysisPublicationRollbackAndSQLGuards(t *testing.T) {
	r := openTest(t, testDatabase(t))
	if err := r.Migrate(testContext); err != nil {
		t.Fatal(err)
	}
	message := publicationFixture(t, r, "publication-rollback")
	if got, err := r.RetainAnalysisPublication(testContext, "bob", message, nil); !errors.Is(err, run.ErrForbidden) || got != nil {
		t.Fatal("cross-principal write accepted", err)
	}
	for _, alter := range []func(*contract.AnalysisPublication){
		func(m *contract.AnalysisPublication) { m.Data.Native.TestID += "-forged" },
		func(m *contract.AnalysisPublication) { m.Data.Artifact.SHA256 = strings.Repeat("f", 64) },
	} {
		changed := message
		alter(&changed)
		if got, err := r.RetainAnalysisPublication(testContext, "alice", changed, nil); !errors.Is(err, run.ErrConflict) || got != nil {
			t.Fatal("unbound proposal accepted", err)
		}
	}
	for _, verify := range []integration.PublicationVerifier{nil,
		func(context.Context, integration.Record, contract.AnalysisPublication) ([]byte, error) {
			return nil, errors.New("private evidence detail")
		},
		func(_ context.Context, _ integration.Record, proposed contract.AnalysisPublication) ([]byte, error) {
			proposed.ID += "-different"
			return integrationcontract.EncodeAnalysisPublication(proposed)
		},
	} {
		if got, err := r.RetainAnalysisPublication(testContext, "alice", message, verify); !errors.Is(err, run.ErrValidation) || got != nil || strings.Contains(err.Error(), "private") {
			t.Fatal("unverified proposal retained or detail leaked", err)
		}
		if _, err := r.GetAnalysisPublication(testContext, "alice", message.Data.Context.Request); !errors.Is(err, run.ErrNotFound) {
			t.Fatal("rollback leaked publication", err)
		}
	}
	raw, err := integrationcontract.EncodeAnalysisPublication(message)
	if err != nil {
		t.Fatal(err)
	}
	insert := `INSERT INTO perfeng_control.integration_analysis_publications(principal,issuer,request_id,source,event_id,artifact_id,payload,digest) VALUES('alice',$1,$2,$3,$4,$5,$6,$7)`
	for _, forged := range []string{strings.Replace(string(raw), message.Data.Native.TestID, "forged-test", 1), string(raw)} {
		digest := sha256.Sum256([]byte(forged))
		checksum := hex.EncodeToString(digest[:])
		if forged == string(raw) {
			checksum = strings.Repeat("0", 64)
		}
		_, err := r.db.Exec(insert, message.Data.Context.Request.Issuer, message.Data.Context.Request.ID, message.Source, message.ID, message.Data.Artifact.ID, []byte(forged), checksum)
		var blocked interface{ SQLState() string }
		if !errors.As(err, &blocked) || blocked.SQLState() != "23514" {
			t.Fatal("SQL forgery accepted", err)
		}
	}
	verify := func(_ context.Context, _ integration.Record, proposed contract.AnalysisPublication) ([]byte, error) {
		return integrationcontract.EncodeAnalysisPublication(proposed)
	}
	if _, err := r.RetainAnalysisPublication(testContext, "alice", message, verify); err != nil {
		t.Fatal(err)
	}
	// Scoped absence is identical for an unknown request, not just another owner.
	unknown := message.Data.Context.Request
	unknown.ID += "-unknown"
	if got, err := r.GetAnalysisPublication(testContext, "alice", unknown); !errors.Is(err, run.ErrNotFound) || got != nil {
		t.Fatal("unknown request disclosed", err)
	}
	for _, statement := range []string{
		`UPDATE perfeng_control.integration_analysis_publications SET event_id='replaced'`,
		`DELETE FROM perfeng_control.integration_analysis_publications`,
		`TRUNCATE perfeng_control.integration_analysis_publications CASCADE`,
	} {
		_, err := r.db.Exec(statement)
		var blocked interface{ SQLState() string }
		if !errors.As(err, &blocked) || blocked.SQLState() != "55000" {
			t.Fatal("SQL mutation accepted", err)
		}
	}
}
