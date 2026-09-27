package nativeworker

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

type terminalStoreStub struct {
	workerID string
	request  TerminalRequest
	result   TerminalAcknowledgement
	err      error
}

func (store *terminalStoreStub) ReportNativeTerminal(
	_ context.Context,
	workerID string,
	request TerminalRequest,
) (TerminalAcknowledgement, error) {
	store.workerID = workerID
	store.request = request

	return store.result, store.err
}

func validTerminalRequest() TerminalRequest {
	return TerminalRequest{
		Kind: TerminalCompleted, LeaseID: "11111111-1111-4111-8111-111111111111",
		AttemptID:           "22222222-2222-4222-8222-222222222222",
		ExpectedRunRevision: 6,
		ManifestArtifactID:  "33333333-3333-4333-8333-333333333333",
		Capability:          strings.Repeat("a", 64),
	}
}

func TestTerminalReporterValidatesAndDelegates(t *testing.T) {
	request := validTerminalRequest()
	store := &terminalStoreStub{result: TerminalAcknowledgement{
		AttemptID: request.AttemptID, RunRevision: 7, Disposition: TerminalAccepted,
	}}
	reporter, err := NewTerminalReporter(store)
	if err != nil {
		t.Fatal(err)
	}
	result, err := reporter.Report(context.Background(), "windows-laptop-01", request)
	if err != nil || result != store.result || store.workerID != "windows-laptop-01" ||
		!reflect.DeepEqual(store.request, request) {
		t.Fatalf("result = %#v, worker = %q, request = %#v, error = %v",
			result, store.workerID, store.request, err)
	}

	failed := request
	failed.Kind = TerminalFailed
	failed.ManifestArtifactID = ""
	failed.Failure = &TerminalFailure{
		Code: TerminalFailureTool, Message: "reviewed tool failed", Retryable: false,
	}
	if !failed.Valid() {
		t.Fatal("valid failed terminal report rejected")
	}
	for name, mutate := range map[string]func(*TerminalRequest){
		"kind":       func(value *TerminalRequest) { value.Kind = "UNKNOWN" },
		"lease":      func(value *TerminalRequest) { value.LeaseID = "invalid" },
		"attempt":    func(value *TerminalRequest) { value.AttemptID = "invalid" },
		"revision":   func(value *TerminalRequest) { value.ExpectedRunRevision = 0 },
		"manifest":   func(value *TerminalRequest) { value.ManifestArtifactID = "" },
		"failure":    func(value *TerminalRequest) { value.Failure = &TerminalFailure{} },
		"capability": func(value *TerminalRequest) { value.Capability = "short" },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := request
			mutate(&invalid)
			if _, err := reporter.Report(
				context.Background(), "windows-laptop-01", invalid,
			); !errors.Is(err, run.ErrValidation) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	if _, err := NewTerminalReporter(nil); !errors.Is(err, run.ErrValidation) {
		t.Fatalf("nil store error = %v", err)
	}
}

func TestTerminalRequestFormattingRedactsSecretsAndMessage(t *testing.T) {
	request := validTerminalRequest()
	request.Kind = TerminalFailed
	request.ManifestArtifactID = ""
	request.Failure = &TerminalFailure{
		Code: TerminalFailureCapture, Message: "private diagnostic", Retryable: true,
	}
	for _, formatted := range []string{fmt.Sprint(request), fmt.Sprintf("%#v", request)} {
		if strings.Contains(formatted, request.Capability) ||
			strings.Contains(formatted, request.Failure.Message) {
			t.Fatal("diagnostic formatting exposed terminal secret or message")
		}
	}
}

func TestTerminalFailureAndAcknowledgementValidation(t *testing.T) {
	for _, code := range []TerminalFailureCode{
		TerminalFailurePreparation,
		TerminalFailureTool,
		TerminalFailureCapture,
		TerminalFailureUpload,
		TerminalFailureHostUnqualified,
	} {
		if !(TerminalFailure{Code: code, Message: "safe", Retryable: true}).Valid() {
			t.Fatalf("failure code %q rejected", code)
		}
	}
	for _, failure := range []TerminalFailure{
		{Code: "UNKNOWN", Message: "safe"},
		{Code: TerminalFailureTool},
		{Code: TerminalFailureTool, Message: strings.Repeat("x", 1025)},
		{Code: TerminalFailureTool, Message: string([]byte{0xff})},
	} {
		if failure.Valid() {
			t.Fatalf("invalid failure accepted: %#v", failure)
		}
	}

	valid := TerminalAcknowledgement{
		AttemptID:   "22222222-2222-4222-8222-222222222222",
		RunRevision: 7, Disposition: TerminalAccepted,
	}
	if !valid.Valid() {
		t.Fatal("valid terminal acknowledgement rejected")
	}
	valid.Disposition = TerminalAlreadyAccepted
	if !valid.Valid() {
		t.Fatal("valid retry acknowledgement rejected")
	}
	valid.Disposition = "UNKNOWN"
	if valid.Valid() {
		t.Fatal("unknown terminal disposition accepted")
	}
}
