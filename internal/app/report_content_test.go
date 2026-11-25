package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/stanimirivanov/perfeng-control-plane/internal/authentication"
	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/objectstore"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/serviceaccess"
)

type reportDatabase struct {
	Database
	registration run.Artifact
}

func (db reportDatabase) GetArtifact(_ context.Context, principal, runID, artifactID string) (run.Artifact, error) {
	if principal == "owner" && db.registration.ID == artifactID && db.registration.RunID == runID {
		return db.registration, nil
	}
	return run.Artifact{}, run.ErrNotFound
}

type reportBucket struct {
	objectstore.S3HeadObjectAPI
	bytes []byte
	key   string
	calls int
	block bool
}

func (bucket *reportBucket) GetObject(ctx context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	bucket.calls++
	if bucket.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if _, ok := ctx.Deadline(); !ok || aws.ToString(in.Key) != bucket.key || aws.ToString(in.Bucket) != "perfeng-example" {
		return nil, run.ErrValidation
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(bucket.bytes)), ContentType: aws.String("application/json"), ContentLength: aws.Int64(int64(len(bucket.bytes)))}, nil
}

func TestApplicationServesPinnedReportContent(t *testing.T) {
	data, err := contract.ReportContentFiles.ReadFile("report-content/tests/fixtures/report-content/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Run     struct{ Registrations []run.Artifact }
		Headers map[string]string
		Body    string
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	registration := fixture.Run.Registrations[0]
	original, err := contract.ReportContentFiles.ReadFile("report-content/" + fixture.Body)
	if err != nil {
		t.Fatal(err)
	}
	config := validApplicationConfig(t)
	config.HTTPServer.WriteTimeout = 500 * time.Millisecond
	resources := validRuntimeResourcesForTest(t, config)
	resources.Database = reportDatabase{registration: registration}
	bucket := &reportBucket{bytes: bytes.Clone(original), key: "runs/" + registration.RunID + "/analysis/result.json"}
	resources.ArtifactStorage, err = objectstore.NewS3ArtifactAccess(bucket, objectstore.S3ArtifactAccessConfig{Bucket: "perfeng-example", RawManifestFormat: "raw-result/v2", MaximumReadBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	credentials := fmt.Sprintf(`{"schemaVersion":1,"credentials":[{"tokenSha256":"%x","principal":"owner","permissions":["runs:read"]},{"tokenSha256":"%x","principal":"peer","permissions":["runs:read"]}]}`, sha256.Sum256([]byte("active")), sha256.Sum256([]byte("peer")))
	resources.Authentication, err = authentication.LoadStatic(fstest.MapFS{"credentials.json": {Data: []byte(credentials)}}, "credentials.json")
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewApplicationRuntime(config, resources, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	path := "/v1/runs/" + registration.RunID + "/artifacts/" + registration.ID + "/report-content"
	for _, tc := range []struct {
		token         string
		status, calls int
	}{
		{"retired", 401, 0}, {"peer", 404, 0}, {"active", 200, 1}, {"active", 503, 2},
	} {
		if tc.status == 503 {
			bucket.bytes[0] ^= 1
		}
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+tc.token)
		w := httptest.NewRecorder()
		runtime.server.(*http.Server).Handler.ServeHTTP(w, req)
		if w.Code != tc.status || bucket.calls != tc.calls {
			t.Fatalf("status %d, object reads %d, want %d/%d", w.Code, bucket.calls, tc.status, tc.calls)
		}
		if tc.status == 200 {
			if !bytes.Equal(w.Body.Bytes(), original) {
				t.Fatal("original bytes changed")
			}
			for name, value := range fixture.Headers {
				if w.Header().Get(name) != value {
					t.Errorf("%s = %q, want %q", name, w.Header().Get(name), value)
				}
			}
		} else if w.Header().Get("Content-Digest") != "" || bytes.Contains(w.Body.Bytes(), []byte("candidateArtifact")) {
			t.Fatal("report leaked on failure")
		}
	}

	t.Run("blocked storage returns an HTTP error before the write deadline", func(t *testing.T) {
		bucket.block = true
		server := httptest.NewUnstartedServer(nil)
		server.Config = runtime.server.(*http.Server)
		server.Start()
		defer server.Close()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer active")
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		body, err := io.ReadAll(res.Body)
		if err != nil || res.StatusCode != http.StatusServiceUnavailable || !bytes.Contains(body, []byte(`"UNAVAILABLE"`)) || res.Header.Get("Content-Digest") != "" {
			t.Fatalf("status %d, body %q, read error %v", res.StatusCode, body, err)
		}
	})
}

type reportVerifier struct{ claims authentication.SignedIdentity }

func (v reportVerifier) VerifyIdentityCredential(_ context.Context, token string) (authentication.SignedIdentity, error) {
	if token != "named" {
		return authentication.SignedIdentity{}, authentication.ErrUnauthenticated
	}
	return v.claims, nil
}

type reportAdmission struct{ delegated bool }

func (p *reportAdmission) IdentityAdmission(context.Context, serviceaccess.Identity, serviceaccess.Identity, string) (bool, bool, error) {
	return true, p.delegated, nil
}

type reportServiceDB struct {
	Database
	binding      serviceaccess.Binding
	bindingReads int
}

func (db *reportServiceDB) GetServiceRunBinding(context.Context, serviceaccess.Identity, string) (serviceaccess.Binding, error) {
	db.bindingReads++
	return db.binding, nil
}

func TestReportRuntimeRechecksDelegationAndNeverFallsBack(t *testing.T) {
	config := validApplicationConfig(t)
	resources := validRuntimeResourcesForTest(t, config)
	now := time.Now()
	issuer := "https://identity.example"
	admission := &reportAdmission{}
	policy, err := authentication.NewStoredIdentityPolicy(admission)
	if err != nil {
		t.Fatal(err)
	}
	verifier := reportVerifier{authentication.SignedIdentity{Issuer: issuer, Subject: "operator", AuthorizedParty: "delegate", Audience: []string{"api"}, AccessToken: true, IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Minute)}}
	auth, err := authentication.NewIdentityAuthenticator(authentication.IdentityProfile{Issuer: issuer, Audience: "api", Clients: map[string]string{"delegate": "HUMAN"}}, verifier, policy, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	resources.NamedReportIdentity = &NamedIdentityAuthentication{authenticator: auth}
	db := &reportServiceDB{binding: serviceaccess.Binding{Creator: serviceaccess.Identity{Issuer: issuer, ID: "different-creator"}, CreatorKind: "HUMAN"}}
	resources.Database = db
	credentials := fmt.Sprintf(`{"schemaVersion":1,"credentials":[{"tokenSha256":"%x","principal":"owner","permissions":["runs:read"]}]}`, sha256.Sum256([]byte("legacy")))
	resources.Authentication, err = authentication.LoadStatic(fstest.MapFS{"credentials.json": {Data: []byte(credentials)}}, "credentials.json")
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewApplicationRuntime(config, resources, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		token         string
		delegated     bool
		status, reads int
	}{
		{"legacy", true, 401, 0}, {"named", true, 404, 1}, {"named", false, 404, 1},
	} {
		admission.delegated = tc.delegated
		req := httptest.NewRequest(http.MethodGet, "/v1/runs/perf-20260902-130000-a1b2c3d5/artifacts/88888888-8888-4888-8888-888888888888/report-content", nil)
		req.Header.Set("Authorization", "Bearer "+tc.token)
		w := httptest.NewRecorder()
		runtime.server.(*http.Server).Handler.ServeHTTP(w, req)
		if w.Code != tc.status || db.bindingReads != tc.reads {
			t.Fatalf("status %d, binding reads %d, want %d/%d", w.Code, db.bindingReads, tc.status, tc.reads)
		}
	}
}
