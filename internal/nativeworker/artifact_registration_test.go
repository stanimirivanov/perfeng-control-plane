package nativeworker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

type artifactRegistrationStoreStub struct {
	prepare func(context.Context, string, ArtifactRegistrationRequest) (ArtifactCandidate, error)
	accept  func(context.Context, string, ArtifactRegistrationRequest, VerifiedArtifact) (ArtifactReference, error)
}

func (store artifactRegistrationStoreStub) PrepareNativeArtifact(
	ctx context.Context,
	workerID string,
	request ArtifactRegistrationRequest,
) (ArtifactCandidate, error) {
	return store.prepare(ctx, workerID, request)
}

func (store artifactRegistrationStoreStub) RegisterNativeArtifact(
	ctx context.Context,
	workerID string,
	request ArtifactRegistrationRequest,
	verified VerifiedArtifact,
) (ArtifactReference, error) {
	return store.accept(ctx, workerID, request, verified)
}

type artifactVerifierFunc func(context.Context, ArtifactCandidate) (VerifiedArtifact, error)

func (verify artifactVerifierFunc) VerifyNativeArtifact(
	ctx context.Context,
	candidate ArtifactCandidate,
) (VerifiedArtifact, error) {
	return verify(ctx, candidate)
}

func nativeArtifactRegistrationFixture() (
	ArtifactRegistrationRequest,
	ArtifactCandidate,
	ArtifactReference,
) {
	issuedAt := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	reservation := UploadReservation{
		GrantID:   "44444444-4444-4444-8444-444444444444",
		LeaseID:   "11111111-1111-4111-8111-111111111111",
		AttemptID: "22222222-2222-4222-8222-222222222222",
		RunID:     "perf-20260927-100000-a1b2c3d4",
		Slot:      "measurements",
		ObjectKey: "runs/perf-20260927-100000-a1b2c3d4/attempts/" +
			"22222222-2222-4222-8222-222222222222/measurements",
		Size: 8, MaximumBytes: 1024, SHA256: strings.Repeat("f", 64),
		MediaType: "application/json", IssuedAt: issuedAt, ExpiresAt: issuedAt.Add(2 * time.Minute),
	}
	request := ArtifactRegistrationRequest{
		LeaseID: reservation.LeaseID, AttemptID: reservation.AttemptID,
		ExpectedRunRevision: 5, GrantID: reservation.GrantID,
		Slot: reservation.Slot, Size: reservation.Size, SHA256: reservation.SHA256,
		Capability: strings.Repeat("c", 64),
	}
	candidate := ArtifactCandidate{
		Reservation: reservation,
		Kind:        "measurements",
		Format:      "playwright-measurements/v2",
	}
	reference := ArtifactReference{
		ArtifactID: "33333333-3333-4333-8333-333333333333",
		RunID:      reservation.RunID, AttemptID: reservation.AttemptID,
		Slot: reservation.Slot, Kind: candidate.Kind,
		URI:  "s3://perfeng-artifacts/" + reservation.ObjectKey,
		Size: reservation.Size, SHA256: reservation.SHA256,
		MediaType: reservation.MediaType, RegisteredAt: issuedAt.Add(time.Minute),
		Format: candidate.Format,
	}

	return request, candidate, reference
}

func TestArtifactRegistrarVerifiesBeforeFencedRegistration(t *testing.T) {
	request, candidate, reference := nativeArtifactRegistrationFixture()
	steps := make([]string, 0, 3)
	store := artifactRegistrationStoreStub{
		prepare: func(
			_ context.Context,
			workerID string,
			actual ArtifactRegistrationRequest,
		) (ArtifactCandidate, error) {
			steps = append(steps, "prepare")
			if workerID != "linux-workstation-01" || actual != request {
				t.Fatal("prepare received different authority")
			}
			return candidate, nil
		},
		accept: func(
			_ context.Context,
			workerID string,
			actual ArtifactRegistrationRequest,
			verified VerifiedArtifact,
		) (ArtifactReference, error) {
			steps = append(steps, "accept")
			if workerID != "linux-workstation-01" || actual != request ||
				verified.Candidate != candidate || verified.URI != reference.URI {
				t.Fatal("accept received different verified evidence")
			}
			return reference, nil
		},
	}
	verifier := artifactVerifierFunc(func(
		_ context.Context,
		actual ArtifactCandidate,
	) (VerifiedArtifact, error) {
		steps = append(steps, "verify")
		if actual != candidate {
			t.Fatal("verifier received different candidate")
		}
		return VerifiedArtifact{Candidate: candidate, URI: reference.URI}, nil
	})
	registrar, err := NewArtifactRegistrar(store, verifier)
	if err != nil {
		t.Fatal(err)
	}

	actual, err := registrar.Register(
		context.Background(),
		"linux-workstation-01",
		request,
	)
	if err != nil || actual != reference || strings.Join(steps, ",") != "prepare,verify,accept" {
		t.Fatalf("Register() = %#v, %v; steps = %v", actual, err, steps)
	}
}

func TestArtifactRegistrarRejectsChangedVerification(t *testing.T) {
	request, candidate, _ := nativeArtifactRegistrationFixture()
	called := false
	store := artifactRegistrationStoreStub{
		prepare: func(context.Context, string, ArtifactRegistrationRequest) (ArtifactCandidate, error) {
			return candidate, nil
		},
		accept: func(
			context.Context,
			string,
			ArtifactRegistrationRequest,
			VerifiedArtifact,
		) (ArtifactReference, error) {
			called = true
			return ArtifactReference{}, nil
		},
	}
	verifier := artifactVerifierFunc(func(
		context.Context,
		ArtifactCandidate,
	) (VerifiedArtifact, error) {
		changed := candidate
		changed.Kind = "trace"
		return VerifiedArtifact{
			Candidate: changed,
			URI:       "s3://perfeng-artifacts/" + changed.Reservation.ObjectKey,
		}, nil
	})
	registrar, err := NewArtifactRegistrar(store, verifier)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = registrar.Register(
		context.Background(), "linux-workstation-01", request,
	); err == nil || called {
		t.Fatalf("changed verification error = %v; accepted = %t", err, called)
	}
}

func TestArtifactRegistrationValidationAndDiagnostics(t *testing.T) {
	request, candidate, reference := nativeArtifactRegistrationFixture()
	if !request.Valid() || !candidate.Valid() || !reference.Valid() {
		t.Fatal("valid registration fixture rejected")
	}
	for _, formatted := range []string{fmt.Sprint(request), fmt.Sprintf("%#v", request)} {
		if strings.Contains(formatted, request.Capability) || strings.Contains(formatted, request.SHA256) {
			t.Fatal("diagnostic formatting exposed registration authority or checksum")
		}
	}

	invalidRequest := request
	invalidRequest.Size = 0
	if invalidRequest.Valid() {
		t.Fatal("invalid registration request accepted")
	}
	invalidCandidate := candidate
	invalidCandidate.Format = ""
	if invalidCandidate.Valid() {
		t.Fatal("invalid artifact candidate accepted")
	}
	invalidCandidate = candidate
	invalidCandidate.Format = "playwright-measurements-v2"
	if invalidCandidate.Valid() {
		t.Fatal("obsolete artifact format accepted")
	}
	for name, uri := range map[string]string{
		"https": "https://perfeng-artifacts/" + candidate.Reservation.ObjectKey,
		"port":  "s3://perfeng-artifacts:9000/" + candidate.Reservation.ObjectKey,
		"query": reference.URI + "?signature=secret",
		"key":   "s3://perfeng-artifacts/runs/another/object",
	} {
		t.Run(name, func(t *testing.T) {
			changed := reference
			changed.URI = uri
			if changed.Valid() {
				t.Fatal("invalid native artifact URI accepted")
			}
		})
	}
}

func TestNewArtifactRegistrarRejectsMissingDependencies(t *testing.T) {
	request, candidate, _ := nativeArtifactRegistrationFixture()
	store := artifactRegistrationStoreStub{
		prepare: func(context.Context, string, ArtifactRegistrationRequest) (ArtifactCandidate, error) {
			return candidate, nil
		},
		accept: func(
			context.Context,
			string,
			ArtifactRegistrationRequest,
			VerifiedArtifact,
		) (ArtifactReference, error) {
			return ArtifactReference{}, nil
		},
	}
	verifier := artifactVerifierFunc(func(
		context.Context,
		ArtifactCandidate,
	) (VerifiedArtifact, error) {
		return VerifiedArtifact{}, nil
	})
	if _, err := NewArtifactRegistrar(nil, verifier); !errors.Is(err, run.ErrValidation) {
		t.Fatal(err)
	}
	if _, err := NewArtifactRegistrar(store, nil); !errors.Is(err, run.ErrValidation) {
		t.Fatal(err)
	}
	var registrar *ArtifactRegistrar
	if _, err := registrar.Register(
		context.Background(), "linux-workstation-01", request,
	); !errors.Is(err, run.ErrValidation) {
		t.Fatal(err)
	}
}
