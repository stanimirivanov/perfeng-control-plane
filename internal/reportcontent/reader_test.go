package reportcontent_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/objectstore"
	"github.com/stanimirivanov/perfeng-control-plane/internal/reportcontent"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

type fixture struct {
	RunID      string `json:"runId"`
	ArtifactID string `json:"artifactId"`
	Run        struct {
		Registrations []run.Artifact `json:"registrations"`
	} `json:"run"`
	Body              string `json:"body"`
	RegistrationCases []struct {
		Name     string          `json:"name"`
		Override json.RawMessage `json:"override"`
		Status   int             `json:"status"`
	} `json:"registrationCases"`
}

func retained(t *testing.T) (fixture, []byte) {
	t.Helper()
	data, err := contract.ReportContentFiles.ReadFile("report-content/tests/fixtures/report-content/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var f fixture
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	body, err := contract.ReportContentFiles.ReadFile("report-content/" + f.Body)
	if err != nil {
		t.Fatal(err)
	}
	return f, body
}

type registration struct {
	ref   run.Artifact
	owner string
}

func (r registration) GetArtifact(_ context.Context, principal, _, _ string) (run.Artifact, error) {
	if principal != r.owner {
		return run.Artifact{}, run.ErrNotFound
	}
	return r.ref, nil
}

type getObject func(context.Context, string, string) (objectstore.Object, error)

func (get getObject) GetObject(ctx context.Context, bucket, key string) (objectstore.Object, error) {
	return get(ctx, bucket, key)
}

func newReader(t *testing.T, ref run.Artifact, get getObject) *reportcontent.Reader {
	t.Helper()
	objects, err := objectstore.NewReader(get, "perfeng-example", reportcontent.MaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := reportcontent.New(registration{ref, "owner"}, objects, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return reader
}

func original(body []byte) objectstore.Object {
	return objectstore.Object{Body: io.NopCloser(bytes.NewReader(body)), SizeBytes: int64(len(body)), MediaType: "application/json"}
}

func TestPinnedRegistrationCases(t *testing.T) {
	t.Parallel()
	f, body := retained(t)
	ref := f.Run.Registrations[0]
	reader := newReader(t, ref, func(ctx context.Context, bucket, key string) (objectstore.Object, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("unbounded object lookup")
		}
		if "s3://"+bucket+"/"+key != ref.URI {
			t.Fatal("did not resolve registered URI")
		}
		return original(body), nil
	})
	content, err := reader.Read(t.Context(), "owner", f.RunID, f.ArtifactID)
	if err != nil || !bytes.Equal(content, body) {
		t.Fatalf("expected exact retained bytes, got %d bytes and %v", len(content), err)
	}
	for _, c := range f.RegistrationCases {
		t.Run(c.Name, func(t *testing.T) {
			mutated := ref
			if err := json.Unmarshal(c.Override, &mutated); err != nil {
				t.Fatal(err)
			}
			wanted := map[int]error{404: run.ErrNotFound, 422: run.ErrValidation, 503: run.ErrUnavailable}[c.Status]
			if wanted == nil {
				t.Fatalf("unhandled portable status %d", c.Status)
			}
			reader := newReader(t, mutated, func(context.Context, string, string) (objectstore.Object, error) {
				if c.Status != 503 {
					t.Fatal("storage consulted before association and size checks")
				}
				return original(body), nil
			})
			got, err := reader.Read(t.Context(), "owner", f.RunID, f.ArtifactID)
			if got != nil || !errors.Is(err, wanted) {
				t.Fatalf("got %d bytes and %v, want %v", len(got), err, wanted)
			}
		})
	}
}

func TestRetainedReportPreservesLongDecimal(t *testing.T) {
	t.Parallel()
	f, body := retained(t)
	for _, number := range []string{"140." + strings.Repeat("0", 130), "1" + strings.Repeat("0", 310)} {
		data := bytes.Replace(body, []byte(`"value": 140`), []byte(`"value": `+number), 1)
		ref := f.Run.Registrations[0]
		ref.SizeBytes = int64(len(data))
		digest := sha256.Sum256(data)
		ref.SHA256 = hex.EncodeToString(digest[:])
		reader := newReader(t, ref, func(context.Context, string, string) (objectstore.Object, error) { return original(data), nil })
		got, err := reader.Read(t.Context(), "owner", f.RunID, f.ArtifactID)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("report with %d-byte decimal: got %d bytes, %v", len(number), len(got), err)
		}
	}
}

func TestEligibleOriginalBodyFailures(t *testing.T) {
	t.Parallel()
	f, body := retained(t)
	foreign := "perf-20260901-130000-a1b2c3d5"
	_, candidateOffset, _ := bytes.Cut(body, []byte(`"candidateArtifact":`))
	candidateStart := len(body) - len(candidateOffset)
	foreignCandidate := append(bytes.Clone(body[:candidateStart]), bytes.Replace(candidateOffset, []byte(f.RunID), []byte(foreign), 1)...)
	for name, data := range map[string][]byte{
		"missing required field":     bytes.Replace(body, []byte(`"method":`), []byte(`"other":`), 1),
		"invalid timestamp format":   bytes.Replace(body, []byte("2026-09-02T13"), []byte("2026-02-31T13"), 1),
		"truncated document":         body[:len(body)-3],
		"duplicate fields":           bytes.Replace(body, []byte(`"blocking": false`), []byte(`"blocking": true, "blocking": false`), 1),
		"foreign report":             bytes.Replace(body, []byte(f.RunID), []byte(foreign), 1),
		"foreign candidate":          foreignCandidate,
		"oversized numeric exponent": bytes.Replace(body, []byte(`"samples": 2`), []byte(`"samples": 1e-999999999`), 1),
	} {
		t.Run(name, func(t *testing.T) {
			ref := f.Run.Registrations[0]
			ref.SizeBytes = int64(len(data))
			digest := sha256.Sum256(data)
			ref.SHA256 = hex.EncodeToString(digest[:])
			reader := newReader(t, ref, func(context.Context, string, string) (objectstore.Object, error) { return original(data), nil })
			got, err := reader.Read(t.Context(), "owner", f.RunID, f.ArtifactID)
			if got != nil || !errors.Is(err, run.ErrUnavailable) {
				t.Fatalf("got %d bytes and %v", len(got), err)
			}
		})
	}
}

func TestBoundedStorageFailures(t *testing.T) {
	t.Parallel()
	f, body := retained(t)
	ref := f.Run.Registrations[0]
	for name, get := range map[string]getObject{
		"lost object": func(context.Context, string, string) (objectstore.Object, error) {
			return objectstore.Object{}, objectstore.ErrObjectNotFound
		},
		"truncated stream": func(context.Context, string, string) (objectstore.Object, error) {
			obj := original(body)
			obj.Body = io.NopCloser(bytes.NewReader(body[:len(body)-1]))
			return obj, nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			reader := newReader(t, ref, get)
			got, err := reader.Read(t.Context(), "owner", f.RunID, f.ArtifactID)
			if got != nil || !errors.Is(err, run.ErrUnavailable) {
				t.Fatalf("got %d bytes and %v", len(got), err)
			}
		})
	}
	withoutStorage, err := reportcontent.New(registration{ref, "owner"}, nil, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for principal, want := range map[string]error{"owner": run.ErrUnavailable, "foreign": run.ErrNotFound} {
		got, err := withoutStorage.Read(t.Context(), principal, f.RunID, f.ArtifactID)
		if got != nil || !errors.Is(err, want) {
			t.Fatalf("got %d bytes and %v for %s, want %v", len(got), err, principal, want)
		}
	}
}

func TestOverflowStopsAfterOneExtraByte(t *testing.T) {
	t.Parallel()
	f, body := retained(t)
	ref := f.Run.Registrations[0]
	const extra = 128
	stream := bytes.NewReader(append(bytes.Clone(body), bytes.Repeat([]byte{' '}, extra)...))
	reader := newReader(t, ref, func(context.Context, string, string) (objectstore.Object, error) {
		obj := original(body)
		obj.Body = io.NopCloser(stream)
		return obj, nil
	})
	got, err := reader.Read(t.Context(), "owner", f.RunID, f.ArtifactID)
	if got != nil || !errors.Is(err, run.ErrUnavailable) || stream.Len() != extra-1 {
		t.Fatalf("got %d bytes, %v, %d unread bytes", len(got), err, stream.Len())
	}
}

func TestStorageDeadline(t *testing.T) {
	t.Parallel()
	f, _ := retained(t)
	synctest.Test(t, func(t *testing.T) {
		reader := newReader(t, f.Run.Registrations[0], func(ctx context.Context, _, _ string) (objectstore.Object, error) {
			<-ctx.Done()
			return objectstore.Object{}, ctx.Err()
		})
		got, err := reader.Read(t.Context(), "owner", f.RunID, f.ArtifactID)
		if got != nil || !errors.Is(err, run.ErrUnavailable) {
			t.Fatalf("got %d bytes and %v", len(got), err)
		}
	})
}
