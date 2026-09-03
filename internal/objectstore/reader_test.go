package objectstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

const objectRunID = "perf-20260903-120000-1234abcd"

type getObjectFunc func(context.Context, string, string) (Object, error)

func (get getObjectFunc) GetObject(ctx context.Context, bucket, key string) (Object, error) {
	return get(ctx, bucket, key)
}

type trackedBody struct {
	io.Reader
	closeErr error
	closed   bool
}

func (body *trackedBody) Close() error {
	body.closed = true

	return body.closeErr
}

func artifactFor(content []byte) run.Artifact {
	digest := sha256.Sum256(content)

	return run.Artifact{
		ID:        "10000000-0000-4000-8000-000000000001",
		RunID:     objectRunID,
		Kind:      "raw",
		URI:       "s3://perfeng-artifacts/runs/" + objectRunID + "/summary.json",
		SHA256:    hex.EncodeToString(digest[:]),
		SizeBytes: int64(len(content)),
		MediaType: "application/json",
		Format:    "k6-summary-json",
	}
}

func TestReaderReturnsOnlyVerifiedBytes(t *testing.T) {
	want := []byte(`{"metrics":{"http_reqs":{"count":1}}}`)
	reference := artifactFor(want)
	body := &trackedBody{Reader: bytes.NewReader(want)}
	getter := getObjectFunc(func(ctx context.Context, bucket, key string) (Object, error) {
		if err := ctx.Err(); err != nil {
			return Object{}, err
		}
		if bucket != "perfeng-artifacts" || key != "runs/"+objectRunID+"/summary.json" {
			t.Fatalf("location = %s/%s", bucket, key)
		}

		return Object{Body: body, SizeBytes: int64(len(want)), MediaType: "application/json"}, nil
	})
	reader, err := NewReader(getter, "perfeng-artifacts", 1024)
	if err != nil {
		t.Fatal(err)
	}

	actual, err := reader.Read(context.Background(), reference)
	if err != nil || !bytes.Equal(actual, want) || !body.closed {
		t.Fatalf("Read() = %q, %v; closed=%t", actual, err, body.closed)
	}
	actual[0] = 'X'
	if want[0] == 'X' {
		t.Fatal("returned bytes alias the storage fixture")
	}
}

func TestReaderRejectsReferenceAndLocationPolicyViolations(t *testing.T) {
	content := []byte("{}")
	valid := artifactFor(content)
	called := false
	getter := getObjectFunc(func(context.Context, string, string) (Object, error) {
		called = true

		return Object{}, nil
	})
	reader, err := NewReader(getter, "perfeng-artifacts", 1024)
	if err != nil {
		t.Fatal(err)
	}

	for name, mutate := range map[string]func(*run.Artifact){
		"invalid reference": func(value *run.Artifact) { value.SHA256 = "bad" },
		"https":             func(value *run.Artifact) { value.URI = strings.Replace(value.URI, "s3://", "https://", 1) },
		"other bucket": func(value *run.Artifact) {
			value.URI = strings.Replace(value.URI, "perfeng-artifacts", "other-bucket", 1)
		},
		"port": func(value *run.Artifact) {
			value.URI = strings.Replace(value.URI, "perfeng-artifacts", "perfeng-artifacts:9000", 1)
		},
		"other run": func(value *run.Artifact) {
			value.URI = "s3://perfeng-artifacts/runs/perf-20260903-120000-deadbeef/summary.json"
		},
		"traversal": func(value *run.Artifact) {
			value.URI = "s3://perfeng-artifacts/runs/" + objectRunID + "/../summary.json"
		},
		"encoded path": func(value *run.Artifact) {
			value.URI = "s3://perfeng-artifacts/runs/" + objectRunID + "/summary%2ejson"
		},
		"empty object": func(value *run.Artifact) { value.URI = "s3://perfeng-artifacts/runs/" + objectRunID + "/" },
		"too large":    func(value *run.Artifact) { value.SizeBytes = 1025 },
	} {
		t.Run(name, func(t *testing.T) {
			reference := valid
			mutate(&reference)
			if _, err := reader.Read(context.Background(), reference); !errors.Is(err, run.ErrValidation) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	if called {
		t.Fatal("invalid location reached object storage")
	}
}

func TestReaderRejectsMetadataAndContentMismatch(t *testing.T) {
	content := []byte("verified")
	reference := artifactFor(content)
	for name, object := range map[string]Object{
		"nil body":    {SizeBytes: reference.SizeBytes, MediaType: reference.MediaType},
		"size header": {Body: io.NopCloser(bytes.NewReader(content)), SizeBytes: reference.SizeBytes + 1, MediaType: reference.MediaType},
		"media type":  {Body: io.NopCloser(bytes.NewReader(content)), SizeBytes: reference.SizeBytes, MediaType: "text/plain"},
		"short body":  {Body: io.NopCloser(bytes.NewReader(content[:3])), SizeBytes: reference.SizeBytes, MediaType: reference.MediaType},
		"long body":   {Body: io.NopCloser(bytes.NewReader(append(append([]byte{}, content...), '!'))), SizeBytes: reference.SizeBytes, MediaType: reference.MediaType},
		"checksum":    {Body: io.NopCloser(strings.NewReader("tampered")), SizeBytes: reference.SizeBytes, MediaType: reference.MediaType},
	} {
		t.Run(name, func(t *testing.T) {
			reader, err := NewReader(getObjectFunc(func(context.Context, string, string) (Object, error) {
				return object, nil
			}), "perfeng-artifacts", 1024)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := reader.Read(context.Background(), reference); !errors.Is(err, ErrObjectMismatch) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestReaderPreservesStorageReadCloseAndContextErrors(t *testing.T) {
	content := []byte("{}")
	reference := artifactFor(content)
	want := errors.New("safe storage error")
	reader, err := NewReader(getObjectFunc(func(context.Context, string, string) (Object, error) {
		return Object{}, want
	}), "perfeng-artifacts", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Read(context.Background(), reference); !errors.Is(err, want) {
		t.Fatalf("storage error = %v", err)
	}

	readFailure := &trackedBody{Reader: errorReader{err: want}}
	reader, err = NewReader(getObjectFunc(func(context.Context, string, string) (Object, error) {
		return Object{Body: readFailure, SizeBytes: reference.SizeBytes, MediaType: reference.MediaType}, nil
	}), "perfeng-artifacts", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Read(context.Background(), reference); !errors.Is(err, want) || !readFailure.closed {
		t.Fatalf("read error = %v; closed=%t", err, readFailure.closed)
	}

	closeFailure := &trackedBody{Reader: bytes.NewReader(content), closeErr: want}
	reader, err = NewReader(getObjectFunc(func(context.Context, string, string) (Object, error) {
		return Object{Body: closeFailure, SizeBytes: reference.SizeBytes, MediaType: reference.MediaType}, nil
	}), "perfeng-artifacts", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Read(context.Background(), reference); !errors.Is(err, want) {
		t.Fatalf("close error = %v", err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	reader, err = NewReader(getObjectFunc(func(context.Context, string, string) (Object, error) {
		t.Fatal("cancelled read reached storage")
		return Object{}, nil
	}), "perfeng-artifacts", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Read(cancelled, reference); !errors.Is(err, context.Canceled) {
		t.Fatalf("context error = %v", err)
	}
}

func TestNewReaderRejectsInvalidConfiguration(t *testing.T) {
	getter := getObjectFunc(func(context.Context, string, string) (Object, error) {
		return Object{}, nil
	})
	for _, bucket := range []string{"", "ab", "UPPER", ".hidden", "trailing.", "two..dots", "name_with_underscore"} {
		if _, err := NewReader(getter, bucket, 1); !errors.Is(err, run.ErrValidation) {
			t.Fatalf("bucket %q error = %v", bucket, err)
		}
	}
	if _, err := NewReader(nil, "perfeng-artifacts", 1); !errors.Is(err, run.ErrValidation) {
		t.Fatal(err)
	}
	if _, err := NewReader(getter, "perfeng-artifacts", 0); !errors.Is(err, run.ErrValidation) {
		t.Fatal(err)
	}
	if _, err := NewReader(getter, "perfeng-artifacts", maximumReadSize+1); !errors.Is(err, run.ErrValidation) {
		t.Fatal(err)
	}
}

type errorReader struct{ err error }

func (reader errorReader) Read([]byte) (int, error) { return 0, reader.err }
