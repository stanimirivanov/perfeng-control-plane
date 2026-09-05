package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/memory"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func fixture(t *testing.T) []byte {
	t.Helper()
	b, err := contract.Files.ReadFile("snapshot/examples/create.json")
	if err != nil {
		t.Fatal(err)
	}

	return b
}
func setup(t *testing.T) (*Handler, *memory.Repository) {
	t.Helper()
	repo := memory.New(nil)
	var approved run.Request
	if err := json.Unmarshal(fixture(t), &approved); err != nil {
		t.Fatal(err)
	}
	h, err := New(repo, func(_ context.Context, token string) (Identity, error) {
		switch token {
		case "alice-token", "rotated-token":
			return Identity{"alice", true, true, true}, nil
		case "bob-token":
			return Identity{"bob", true, true, true}, nil
		case "read-only":
			return Identity{"alice", false, true, false}, nil
		case "create-only":
			return Identity{"alice", true, false, false}, nil
		case "unavailable":
			return Identity{}, run.ErrUnavailable
		default:
			return Identity{}, errors.New("secret authentication diagnostic")
		}
	}, func(_ context.Context, _ string, r run.Request) error {
		if r != approved {
			return run.ErrValidation
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	return h, repo
}
func call(h http.Handler, method, path, token string, body []byte) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Idempotency-Key", "request-key-00000001")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	return w
}
func decodeRun(t *testing.T, w *httptest.ResponseRecorder) run.Run {
	t.Helper()
	var r run.Run
	if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil {
		t.Fatal(err)
	}

	return r
}

func artifactFixture(t *testing.T, runID string) artifactCollection {
	t.Helper()
	b, err := contract.Files.ReadFile("snapshot/examples/artifacts.json")
	if err != nil {
		t.Fatal(err)
	}
	var collection artifactCollection
	if err := json.Unmarshal(b, &collection); err != nil {
		t.Fatal(err)
	}
	for index := range collection.Artifacts {
		artifact := &collection.Artifacts[index]
		artifact.URI = strings.Replace(artifact.URI, artifact.RunID, runID, 1)
		artifact.RunID = runID
	}

	return collection
}
func checkError(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status=%d want=%d body=%s", w.Code, status, w.Body)
	}
	var e apiError
	if err := json.Unmarshal(w.Body.Bytes(), &e); err != nil {
		t.Fatal(err)
	}
	if e.Code != code || e.Message == "" || !regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-4[a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$`).MatchString(e.RequestID) {
		t.Fatalf("invalid error: %+v", e)
	}
	if strings.Contains(w.Body.String(), "secret") {
		t.Fatal("internal error leaked")
	}
	if w.Header().Get("Content-Type") != "application/json" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("missing safe response headers")
	}
	if status == 401 && w.Header().Get("WWW-Authenticate") != "Bearer" {
		t.Fatal("missing challenge")
	}
	if status == 503 && w.Header().Get("Retry-After") != "1" {
		t.Fatal("missing retry delay")
	}
}

func TestCreateGetCancelReplay(t *testing.T) {
	h, repo := setup(t)
	first := call(h, "POST", "/v1/runs", "alice-token", fixture(t))
	if first.Code != 201 {
		t.Fatal(first.Body)
	}
	r := decodeRun(t, first)
	location := first.Header().Get("Location")
	if location != "/v1/runs/"+r.ID || first.Header().Get("Idempotency-Key-Expires-At") == "" {
		t.Fatal("missing acceptance headers")
	}
	cancel := call(h, "POST", location+"/cancel", "alice-token", nil)
	if cancel.Code != 202 || decodeRun(t, cancel).Revision != 2 {
		t.Fatal(cancel.Body)
	}
	repeat := call(h, "POST", location+"/cancel", "alice-token", nil)
	if repeat.Body.String() != cancel.Body.String() {
		t.Fatal("cancel changed on retry")
	}
	// Reorder all properties and change whitespace, then rotate the credential.
	var object any
	if err := json.Unmarshal(fixture(t), &object); err != nil {
		t.Fatal(err)
	}
	reordered, err := json.MarshalIndent(object, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	replay := call(h, "POST", "/v1/runs", "rotated-token", reordered)
	if replay.Code != 201 || replay.Body.String() != first.Body.String() || replay.Header().Get("Location") != location || replay.Header().Get("Idempotency-Key-Expires-At") != first.Header().Get("Idempotency-Key-Expires-At") {
		t.Fatal("replay not original acceptance")
	}
	current := call(h, "GET", location, "alice-token", nil)
	if current.Code != 200 || decodeRun(t, current).State != "CANCELLING" {
		t.Fatal("replay reset current run")
	}
	_, err = repo.Advance(context.Background(), "alice", r.ID, 2, run.Change{State: "ABORTED"})
	if err != nil {
		t.Fatal(err)
	}
	aborted := call(h, "POST", location+"/cancel", "alice-token", nil)
	if aborted.Code != 200 || decodeRun(t, aborted).State != "ABORTED" {
		t.Fatal(aborted.Body)
	}
	checkError(t, call(h, "GET", location, "bob-token", nil), 404, "NOT_FOUND")
	checkError(t, call(h, "POST", location+"/cancel", "bob-token", nil), 404, "NOT_FOUND")
	checkError(t, call(h, "POST", location+"/cancel", "alice-token", []byte("{}")), 400, "BAD_REQUEST")
}

func TestListArtifacts(t *testing.T) {
	h, repo := setup(t)
	accepted := call(h, "POST", "/v1/runs", "alice-token", fixture(t))
	current := decodeRun(t, accepted)
	path := "/v1/runs/" + current.ID + "/artifacts"

	empty := call(h, http.MethodGet, path, "read-only", nil)
	if empty.Code != http.StatusOK || empty.Body.String() != "{\"artifacts\":[]}\n" {
		t.Fatalf("empty response = %d %s", empty.Code, empty.Body)
	}

	want := artifactFixture(t, current.ID)
	for index := len(want.Artifacts) - 1; index >= 0; index-- {
		if err := repo.RegisterArtifact(context.Background(), "alice", want.Artifacts[index]); err != nil {
			t.Fatal(err)
		}
	}

	response := call(h, http.MethodGet, path, "alice-token", nil)
	if response.Code != http.StatusOK {
		t.Fatal(response.Body)
	}
	var got artifactCollection
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("artifacts = %#v, want %#v", got, want)
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("artifact references were cacheable")
	}

	checkError(t, call(h, http.MethodGet, path, "bob-token", nil), 404, "NOT_FOUND")
	checkError(t, call(h, http.MethodGet, path, "create-only", nil), 403, "FORBIDDEN")
	checkError(t, call(h, http.MethodGet, path, "alice-token", []byte("{}")), 400, "BAD_REQUEST")
	checkError(
		t,
		call(h, http.MethodGet, "/v1/runs/bad/artifacts", "alice-token", nil),
		400,
		"BAD_REQUEST",
	)
}

func TestStrictBodies(t *testing.T) {
	valid := string(fixture(t))
	cases := []struct {
		name, body string
		status     int
	}{
		{"empty", "", 400}, {"malformed", "{", 400}, {"trailing", valid + " {}", 400},
		{"duplicate", strings.Replace(valid, `"testSuite":`, `"testSuite":"other","testSuite":`, 1), 400},
		{"escaped-duplicate", strings.Replace(valid, `"profile":`, `"\u0070rofile":"soak","profile":`, 1), 400},
		{"nested-duplicate", strings.Replace(valid, `"id":`, `"id":"other","id":`, 1), 400},
		{"utf8", string([]byte{'{', '"', 0xff, '"', '}'}), 400},
		{"nan", strings.Replace(valid, `"smoke"`, "NaN", 1), 400},
		{"unknown", strings.Replace(valid, "{", `{"command":"secret",`, 1), 422},
		{"case", strings.Replace(valid, "testSuite", "TestSuite", 1), 422},
		{"wrong-type", strings.Replace(valid, `"smoke"`, "true", 1), 422},
		{"null", "null", 422}, {"array", "[]", 422},
		{"nested-null", strings.Replace(valid, `"smoke"`, "null", 1), 422},
		{"unapproved", strings.Replace(valid, "checkout-api", "unapproved-suite", 1), 422},
		{"oversize", valid + strings.Repeat(" ", MaxBodyBytes), 413},
		{"deep", strings.Repeat("[", 66) + "0" + strings.Repeat("]", 66), 400},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, _ := setup(t)
			code := "BAD_REQUEST"
			if c.status == 422 {
				code = "VALIDATION_FAILED"
			}
			checkError(t, call(h, "POST", "/v1/runs", "alice-token", []byte(c.body)), c.status, code)
			// An invalid attempt must leave the key available.
			if w := call(h, "POST", "/v1/runs", "alice-token", fixture(t)); w.Code != 201 {
				t.Fatal(w.Body)
			}
		})
	}
	h, _ := setup(t)
	boundary := valid + strings.Repeat(" ", MaxBodyBytes-len(valid))
	if w := call(h, "POST", "/v1/runs", "alice-token", []byte(boundary)); w.Code != 201 {
		t.Fatal("exact limit rejected", w.Body)
	}
}

func TestHeadersAuthenticationAndPermissions(t *testing.T) {
	for _, c := range []struct {
		name, header, value string
		status              int
		code                string
	}{
		{"missing-token", "Authorization", "", 401, "UNAUTHENTICATED"},
		{"invalid-token", "Authorization", "Bearer secret", 401, "UNAUTHENTICATED"},
		{"wrong-scheme", "Authorization", "Basic alice-token", 401, "UNAUTHENTICATED"},
		{"forbidden", "Authorization", "Bearer read-only", 403, "FORBIDDEN"},
		{"auth-unavailable", "Authorization", "Bearer unavailable", 503, "UNAVAILABLE"},
		{"missing-key", "Idempotency-Key", "", 400, "BAD_REQUEST"},
		{"short-key", "Idempotency-Key", "short", 400, "BAD_REQUEST"},
		{"long-key", "Idempotency-Key", strings.Repeat("x", 129), 400, "BAD_REQUEST"},
		{"wrong-media", "Content-Type", "text/plain", 415, "BAD_REQUEST"},
		{"charset", "Content-Type", "application/json; charset=latin1", 415, "BAD_REQUEST"},
		{"encoding", "Content-Encoding", "gzip", 415, "BAD_REQUEST"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h, _ := setup(t)
			r := httptest.NewRequest("POST", "/v1/runs", bytes.NewReader(fixture(t)))
			r.Header.Set("Authorization", "Bearer alice-token")
			r.Header.Set("Idempotency-Key", "request-key-00000001")
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set(c.header, c.value)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			checkError(t, w, c.status, c.code)
		})
	}
	for _, name := range []string{"Authorization", "Idempotency-Key", "Content-Type", "Content-Encoding"} {
		t.Run("duplicate-"+name, func(t *testing.T) {
			h, _ := setup(t)
			r := httptest.NewRequest("POST", "/v1/runs", bytes.NewReader(fixture(t)))
			r.Header.Set("Authorization", "Bearer alice-token")
			r.Header.Set("Idempotency-Key", "request-key-00000001")
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Content-Encoding", "identity")
			r.Header.Add(name, r.Header.Get(name))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code < 400 {
				t.Fatal("ambiguous duplicate header accepted")
			}
		})
	}
}

func TestConflictTerminalAndDependencyFailures(t *testing.T) {
	h, repo := setup(t)
	first := call(h, "POST", "/v1/runs", "alice-token", fixture(t))
	r := decodeRun(t, first)
	// Explicit test stub, not a production resource-approval adapter.
	h.approve = func(context.Context, string, run.Request) error { return nil }
	changed := bytes.Replace(fixture(t), []byte("smoke"), []byte("soak"), 1)
	checkError(t, call(h, "POST", "/v1/runs", "alice-token", changed), 409, "IDEMPOTENCY_CONFLICT")
	for _, state := range []run.State{
		run.StateValidating,
		run.StateProvisioning,
		run.StateRunning,
		run.StateCollecting,
		run.StateAnalyzing,
		run.StateReporting,
		run.StateCompleted,
	} {
		var err error
		r, err = repo.Advance(context.Background(), "alice", r.ID, r.Revision, run.Change{State: state})
		if err != nil {
			t.Fatal(err)
		}
	}
	checkError(t, call(h, "POST", "/v1/runs/"+r.ID+"/cancel", "alice-token", nil), 409, "RUN_TERMINAL")
	checkError(t, call(h, "GET", "/v1/runs/bad", "alice-token", nil), 400, "BAD_REQUEST")
	checkError(t, call(h, "GET", "/v1/runs/perf-20260902-000000-00000000", "alice-token", nil), 404, "NOT_FOUND")
	for _, c := range []struct {
		err    error
		status int
		code   string
	}{
		{run.ErrUnavailable, 503, "UNAVAILABLE"}, {run.ErrForbidden, 403, "FORBIDDEN"},
		{context.Canceled, 503, "UNAVAILABLE"}, {context.DeadlineExceeded, 503, "UNAVAILABLE"},
		{errors.New("secret connection string"), 500, "INTERNAL_ERROR"},
	} {
		h.approve = func(context.Context, string, run.Request) error { return c.err }
		checkError(t, call(h, "POST", "/v1/runs", "alice-token", fixture(t)), c.status, c.code)
	}
	if _, err := New(repo, nil, h.approve); err == nil {
		t.Fatal("missing auth accepted")
	}
	if _, err := New(repo, h.authenticate, nil); err == nil {
		t.Fatal("missing approval accepted")
	}
	if _, err := New(nil, h.authenticate, h.approve); err == nil {
		t.Fatal("missing repository accepted")
	}
}

func FuzzParseJSON(f *testing.F) {
	for _, seed := range []string{`{"a":1}`, `{"a":1,"a":2}`, "null", "[]", "{"} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > MaxBodyBytes {
			t.Skip()
		}
		value, err := parseJSON(b)
		if err == nil {
			if !json.Valid(b) {
				t.Fatal("parser accepted invalid JSON")
			}
			if _, err := json.Marshal(value); err != nil {
				t.Fatal(err)
			}
		}
	})
}
