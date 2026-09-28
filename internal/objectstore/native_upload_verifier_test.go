package objectstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/nativeworker"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func nativeUploadCandidate(content []byte) nativeworker.ArtifactCandidate {
	digest := sha256.Sum256(content)
	issuedAt := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	reservation := nativeworker.UploadReservation{
		GrantID:   "44444444-4444-4444-8444-444444444444",
		LeaseID:   "11111111-1111-4111-8111-111111111111",
		AttemptID: "22222222-2222-4222-8222-222222222222",
		RunID:     "perf-20260927-100000-a1b2c3d4",
		Slot:      "measurements",
		ObjectKey: "runs/perf-20260927-100000-a1b2c3d4/attempts/" +
			"22222222-2222-4222-8222-222222222222/measurements",
		Size: int64(len(content)), MaximumBytes: 1024,
		SHA256: fmt.Sprintf("%x", digest), MediaType: "application/json",
		IssuedAt: issuedAt, ExpiresAt: issuedAt.Add(2 * time.Minute),
	}

	return nativeworker.ArtifactCandidate{
		Reservation: reservation,
		Kind:        "measurements",
		Format:      "playwright-measurements/v2",
	}
}

func TestNativeUploadVerifierStreamsExactObject(t *testing.T) {
	content := []byte(`{"ok":1}`)
	candidate := nativeUploadCandidate(content)
	body := &trackedBody{Reader: bytes.NewReader(content)}
	getter := getObjectFunc(func(ctx context.Context, bucket, key string) (Object, error) {
		if ctx.Err() != nil || bucket != "perfeng-artifacts" ||
			key != candidate.Reservation.ObjectKey {
			t.Fatalf("GetObject() = %q, %q", bucket, key)
		}

		return Object{
			Body: body, SizeBytes: int64(len(content)), MediaType: "application/json",
		}, nil
	})
	verifier, err := NewNativeUploadVerifier(getter, "perfeng-artifacts")
	if err != nil {
		t.Fatal(err)
	}

	verified, err := verifier.VerifyNativeArtifact(context.Background(), candidate)
	if err != nil || !verified.Valid() || verified.Candidate != candidate || !body.closed {
		t.Fatalf("VerifyNativeArtifact() = %#v, %v; closed=%t", verified, err, body.closed)
	}
	wantURI := "s3://perfeng-artifacts/" + candidate.Reservation.ObjectKey
	if verified.URI != wantURI {
		t.Fatalf("URI = %q, want %q", verified.URI, wantURI)
	}
}

func TestNativeUploadVerifierRejectsMetadataAndBytes(t *testing.T) {
	content := []byte("verified")
	candidate := nativeUploadCandidate(content)
	for name, object := range map[string]Object{
		"nil body": {SizeBytes: candidate.Reservation.Size, MediaType: candidate.Reservation.MediaType},
		"size header": {
			Body:      io.NopCloser(bytes.NewReader(content)),
			SizeBytes: candidate.Reservation.Size + 1, MediaType: candidate.Reservation.MediaType,
		},
		"media type": {
			Body:      io.NopCloser(bytes.NewReader(content)),
			SizeBytes: candidate.Reservation.Size, MediaType: "text/plain",
		},
		"short body": {
			Body:      io.NopCloser(bytes.NewReader(content[:3])),
			SizeBytes: candidate.Reservation.Size, MediaType: candidate.Reservation.MediaType,
		},
		"long body": {
			Body:      io.NopCloser(bytes.NewReader(append(append([]byte{}, content...), '!'))),
			SizeBytes: candidate.Reservation.Size, MediaType: candidate.Reservation.MediaType,
		},
		"checksum": {
			Body:      io.NopCloser(strings.NewReader("tampered")),
			SizeBytes: candidate.Reservation.Size, MediaType: candidate.Reservation.MediaType,
		},
	} {
		t.Run(name, func(t *testing.T) {
			verifier, err := NewNativeUploadVerifier(
				getObjectFunc(func(context.Context, string, string) (Object, error) {
					return object, nil
				}),
				"perfeng-artifacts",
			)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = verifier.VerifyNativeArtifact(
				context.Background(), candidate,
			); !errors.Is(err, ErrObjectMismatch) ||
				!errors.Is(err, nativeworker.ErrArtifactVerification) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestNativeUploadVerifierPreservesStorageAndContextErrors(t *testing.T) {
	content := []byte("verified")
	candidate := nativeUploadCandidate(content)
	missingVerifier, err := NewNativeUploadVerifier(
		getObjectFunc(func(context.Context, string, string) (Object, error) {
			return Object{}, ErrObjectNotFound
		}),
		"perfeng-artifacts",
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = missingVerifier.VerifyNativeArtifact(
		context.Background(), candidate,
	); !errors.Is(err, ErrObjectNotFound) ||
		!errors.Is(err, nativeworker.ErrArtifactVerification) {
		t.Fatalf("missing-object error = %v", err)
	}

	want := errors.New("safe storage error")
	verifier, err := NewNativeUploadVerifier(
		getObjectFunc(func(context.Context, string, string) (Object, error) {
			return Object{}, want
		}),
		"perfeng-artifacts",
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = verifier.VerifyNativeArtifact(
		context.Background(), candidate,
	); !errors.Is(err, want) {
		t.Fatalf("storage error = %v", err)
	}

	readFailure := &trackedBody{Reader: errorReader{err: want}}
	verifier, err = NewNativeUploadVerifier(
		getObjectFunc(func(context.Context, string, string) (Object, error) {
			return Object{
				Body:      readFailure,
				SizeBytes: candidate.Reservation.Size,
				MediaType: candidate.Reservation.MediaType,
			}, nil
		}),
		"perfeng-artifacts",
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = verifier.VerifyNativeArtifact(
		context.Background(), candidate,
	); !errors.Is(err, want) || !readFailure.closed {
		t.Fatalf("read error = %v; closed=%t", err, readFailure.closed)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = verifier.VerifyNativeArtifact(canceled, candidate); !errors.Is(err, context.Canceled) {
		t.Fatalf("context error = %v", err)
	}
}

func TestNewNativeUploadVerifierRejectsInvalidConfiguration(t *testing.T) {
	getter := getObjectFunc(func(context.Context, string, string) (Object, error) {
		return Object{}, nil
	})
	if _, err := NewNativeUploadVerifier(nil, "perfeng-artifacts"); !errors.Is(err, run.ErrValidation) {
		t.Fatal(err)
	}
	if _, err := NewNativeUploadVerifier(getter, "UPPER"); !errors.Is(err, run.ErrValidation) {
		t.Fatal(err)
	}
}
