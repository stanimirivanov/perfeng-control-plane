package nativeworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

type uploadGrantStoreStub struct {
	workerID    string
	request     UploadRequest
	ttl         time.Duration
	reservation UploadReservation
	err         error
}

type uploadSignerStub struct {
	reservation   UploadReservation
	authorization UploadAuthorization
	err           error
}

func (signer *uploadSignerStub) PresignNativeUpload(
	_ context.Context,
	reservation UploadReservation,
) (UploadAuthorization, error) {
	signer.reservation = reservation

	return signer.authorization, signer.err
}

func (store *uploadGrantStoreStub) ReserveNativeUploadGrant(
	_ context.Context,
	workerID string,
	request UploadRequest,
	ttl time.Duration,
) (UploadReservation, error) {
	store.workerID = workerID
	store.request = request
	store.ttl = ttl

	return store.reservation, store.err
}

func validUploadRequest() UploadRequest {
	return UploadRequest{
		LeaseID:             "11111111-1111-4111-8111-111111111111",
		AttemptID:           "22222222-2222-4222-8222-222222222222",
		ExpectedRunRevision: 5,
		Slot:                "measurements",
		Size:                4096,
		SHA256:              strings.Repeat("f", 64),
		MediaType:           "application/json",
		Capability:          strings.Repeat("a", 64),
	}
}

func validUploadReservation() UploadReservation {
	issuedAt := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)

	return UploadReservation{
		GrantID:      "44444444-4444-4444-8444-444444444444",
		LeaseID:      "11111111-1111-4111-8111-111111111111",
		AttemptID:    "22222222-2222-4222-8222-222222222222",
		RunID:        "perf-20260926-120000-a1b2c3d4",
		Slot:         "measurements",
		ObjectKey:    "runs/perf-20260926-120000-a1b2c3d4/attempts/22222222-2222-4222-8222-222222222222/measurements",
		Size:         4096,
		MaximumBytes: 1 << 20,
		SHA256:       strings.Repeat("f", 64),
		MediaType:    "application/json",
		IssuedAt:     issuedAt,
		ExpiresAt:    issuedAt.Add(2 * time.Minute),
	}
}

func validUploadAuthorization() UploadAuthorization {
	reservation := validUploadReservation()

	return UploadAuthorization{
		URL: "https://objects.perfeng.invalid/perfeng-artifacts/" + reservation.ObjectKey +
			"?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Signature=redacted",
		Headers: UploadHeaders{
			ContentType:    "application/json",
			ChecksumSHA256: "//////////////////////////////////////////8=",
		},
		Method: "PUT", ExpiresAt: reservation.ExpiresAt.Add(-time.Second),
	}
}

func TestUploadGrantReserverValidatesAndDelegatesPolicy(t *testing.T) {
	store := &uploadGrantStoreStub{reservation: validUploadReservation()}
	reserver, err := NewUploadGrantReserver(store, 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	request := validUploadRequest()
	reservation, err := reserver.Reserve(
		context.Background(),
		"windows-laptop-01",
		request,
	)
	if err != nil || reservation != store.reservation ||
		store.workerID != "windows-laptop-01" || store.request != request ||
		store.ttl != 2*time.Minute {
		t.Fatalf("reservation = %#v, worker = %q, ttl = %s, error = %v",
			reservation, store.workerID, store.ttl, err)
	}

	for name, mutate := range map[string]func(*UploadRequest){
		"lease":      func(value *UploadRequest) { value.LeaseID = "invalid" },
		"attempt":    func(value *UploadRequest) { value.AttemptID = "invalid" },
		"revision":   func(value *UploadRequest) { value.ExpectedRunRevision = 0 },
		"slot":       func(value *UploadRequest) { value.Slot = "invalid_slot" },
		"size":       func(value *UploadRequest) { value.Size = 0 },
		"oversized":  func(value *UploadRequest) { value.Size = maximumArtifactBytes + 1 },
		"checksum":   func(value *UploadRequest) { value.SHA256 = strings.Repeat("A", 64) },
		"media type": func(value *UploadRequest) { value.MediaType = "Application/JSON" },
		"capability": func(value *UploadRequest) { value.Capability = "short" },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := validUploadRequest()
			mutate(&invalid)
			if _, err := reserver.Reserve(
				context.Background(), "windows-laptop-01", invalid,
			); !errors.Is(err, run.ErrValidation) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestUploadRequestFormattingDoesNotExposeSensitiveValues(t *testing.T) {
	request := validUploadRequest()
	for _, formatted := range []string{fmt.Sprint(request), fmt.Sprintf("%#v", request)} {
		if strings.Contains(formatted, request.Capability) ||
			strings.Contains(formatted, request.SHA256) {
			t.Fatal("diagnostic formatting exposed upload authority or checksum")
		}
	}
}

func TestUploadReservationValidation(t *testing.T) {
	valid := validUploadReservation()
	if !valid.Valid() {
		t.Fatal("valid upload reservation rejected")
	}
	for name, mutate := range map[string]func(*UploadReservation){
		"grant":      func(value *UploadReservation) { value.GrantID = "invalid" },
		"object key": func(value *UploadReservation) { value.ObjectKey += "?signature=secret" },
		"size":       func(value *UploadReservation) { value.Size = value.MaximumBytes + 1 },
		"checksum":   func(value *UploadReservation) { value.SHA256 = strings.Repeat("A", 64) },
		"media type": func(value *UploadReservation) { value.MediaType = "Application/JSON" },
		"expiry":     func(value *UploadReservation) { value.ExpiresAt = value.IssuedAt },
		"lifetime":   func(value *UploadReservation) { value.ExpiresAt = value.IssuedAt.Add(5*time.Minute + time.Second) },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := valid
			mutate(&invalid)
			if invalid.Valid() {
				t.Fatal("invalid upload reservation accepted")
			}
		})
	}
}

func TestNewUploadGrantReserverRejectsInvalidPolicy(t *testing.T) {
	store := &uploadGrantStoreStub{}
	for name, test := range map[string]struct {
		store UploadGrantStore
		ttl   time.Duration
	}{
		"store":      {},
		"short":      {store: store, ttl: time.Millisecond},
		"long":       {store: store, ttl: 5*time.Minute + time.Second},
		"fractional": {store: store, ttl: time.Second + time.Millisecond},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewUploadGrantReserver(test.store, test.ttl); !errors.Is(err, run.ErrValidation) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestUploadGrantIssuerReservesBeforeSigning(t *testing.T) {
	reservation := validUploadReservation()
	store := &uploadGrantStoreStub{reservation: reservation}
	signer := &uploadSignerStub{authorization: validUploadAuthorization()}
	issuer, err := NewUploadGrantIssuer(store, signer, 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := issuer.Issue(context.Background(), "windows-laptop-01", validUploadRequest())
	if err != nil || !grant.Valid() || signer.reservation != reservation ||
		grant.GrantID != reservation.GrantID || grant.UploadURL != signer.authorization.URL ||
		grant.ExpiresAt != signer.authorization.ExpiresAt {
		t.Fatalf("grant = %#v, signed reservation = %#v, error = %v", grant, signer.reservation, err)
	}

	document, err := json.Marshal(grant)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{
		`"uploadUrl"`, `"uploadHeaders"`, `"content-type"`,
		`"x-amz-checksum-sha256"`, `"method":"PUT"`,
	} {
		if !strings.Contains(string(document), field) {
			t.Fatalf("upload grant omits %s: %s", field, document)
		}
	}
}

func TestUploadGrantIssuerRetainsReservationWhenSigningFails(t *testing.T) {
	reservation := validUploadReservation()
	store := &uploadGrantStoreStub{reservation: reservation}
	signingErr := errors.New("safe signing failure")
	signer := &uploadSignerStub{err: signingErr}
	issuer, err := NewUploadGrantIssuer(store, signer, 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = issuer.Issue(
		context.Background(), "windows-laptop-01", validUploadRequest(),
	); !errors.Is(err, signingErr) || signer.reservation != reservation || store.request.LeaseID == "" {
		t.Fatalf("signed reservation = %#v, store request = %#v, error = %v",
			signer.reservation, store.request, err)
	}

	signer.err = nil
	signer.authorization = validUploadAuthorization()
	signer.authorization.URL = "http://objects.invalid/secret?signature=secret"
	if _, err = issuer.Issue(
		context.Background(), "windows-laptop-01", validUploadRequest(),
	); err == nil || strings.Contains(err.Error(), signer.authorization.URL) {
		t.Fatalf("unsafe invalid-authorization error = %v", err)
	}
}

func TestUploadGrantDiagnosticsRedactDeliveryMaterial(t *testing.T) {
	authorization := validUploadAuthorization()
	grant := UploadGrant{
		GrantID:       validUploadReservation().GrantID,
		RunID:         validUploadReservation().RunID,
		Slot:          validUploadReservation().Slot,
		UploadURL:     authorization.URL,
		UploadHeaders: authorization.Headers,
	}
	for _, formatted := range []string{
		fmt.Sprint(authorization), fmt.Sprintf("%#v", authorization),
		fmt.Sprint(authorization.Headers), fmt.Sprintf("%#v", authorization.Headers),
		fmt.Sprint(grant), fmt.Sprintf("%#v", grant),
	} {
		if strings.Contains(formatted, authorization.URL) ||
			strings.Contains(formatted, authorization.Headers.ChecksumSHA256) {
			t.Fatal("diagnostic formatting exposed upload delivery material")
		}
	}
}

func TestNewUploadGrantIssuerRejectsMissingDependencies(t *testing.T) {
	store := &uploadGrantStoreStub{}
	signer := &uploadSignerStub{}
	for name, test := range map[string]struct {
		store  UploadGrantStore
		signer UploadSigner
		ttl    time.Duration
	}{
		"store":  {signer: signer, ttl: 2 * time.Minute},
		"signer": {store: store, ttl: 2 * time.Minute},
		"ttl":    {store: store, signer: signer, ttl: time.Millisecond},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewUploadGrantIssuer(
				test.store, test.signer, test.ttl,
			); !errors.Is(err, run.ErrValidation) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}
