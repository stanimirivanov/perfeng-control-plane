package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/app"
)

type runtimeStub struct {
	run func(context.Context) error
}

func (runtime runtimeStub) Run(ctx context.Context) error {
	return runtime.run(ctx)
}

type resourcesStub struct {
	closeCalls int
	err        error
}

func (resources *resourcesStub) Close() error {
	resources.closeCalls++

	return resources.err
}

func TestRunProcessRunsAndClosesConfiguredProcess(t *testing.T) {
	var output bytes.Buffer
	logger := jsonLogger(&output)
	resources := &resourcesStub{}
	runCalls := 0
	build := func(ctx context.Context, actualLogger *slog.Logger) (configuredProcess, error) {
		if ctx != t.Context() || actualLogger != logger {
			t.Fatal("process builder received different dependencies")
		}

		return configuredProcess{
			runtime: runtimeStub{run: func(actual context.Context) error {
				runCalls++
				if actual != ctx {
					t.Fatal("runtime received a different context")
				}

				return nil
			}},
			resources: resources,
		}, nil
	}

	if code := runProcess(t.Context(), logger, build); code != exitSuccess {
		t.Fatalf("exit code = %d", code)
	}
	if runCalls != 1 || resources.closeCalls != 1 {
		t.Fatalf("run calls = %d, close calls = %d", runCalls, resources.closeCalls)
	}
	entries := decodeLogEntries(t, output.Bytes())
	if len(entries) != 2 || entries[0]["msg"] != processStartedMessage ||
		entries[1]["msg"] != processStoppedMessage || entries[1]["phase"] != "shutdown" {
		t.Fatalf("unexpected process lifecycle records: %#v", entries)
	}
}

func TestRunProcessReportsSafeStartupFailure(t *testing.T) {
	var output bytes.Buffer
	secret := "postgres://service:secret@example.invalid/database"
	build := func(context.Context, *slog.Logger) (configuredProcess, error) {
		return configuredProcess{}, errors.Join(
			app.ErrDependencyInitialization,
			errors.New(secret),
		)
	}

	if code := runProcess(t.Context(), jsonLogger(&output), build); code != exitFailure {
		t.Fatalf("exit code = %d", code)
	}
	if strings.Contains(output.String(), secret) || strings.Contains(output.String(), "service:secret") {
		t.Fatal("startup log disclosed error details")
	}
	entries := decodeLogEntries(t, output.Bytes())
	if len(entries) != 1 || entries[0]["msg"] != processFailedMessage ||
		entries[0]["phase"] != "startup" ||
		entries[0]["error_class"] != "dependency_initialization" {
		t.Fatalf("unexpected startup failure record: %#v", entries)
	}
}

func TestRunProcessReportsRuntimeAndShutdownFailuresIndependently(t *testing.T) {
	var output bytes.Buffer
	runtimeSecret := "runtime token=secret"
	shutdownSecret := "shutdown password=secret"
	resources := &resourcesStub{err: errors.Join(
		app.ErrResourceShutdown,
		errors.New(shutdownSecret),
	)}
	build := func(context.Context, *slog.Logger) (configuredProcess, error) {
		return configuredProcess{
			runtime: runtimeStub{run: func(context.Context) error {
				return errors.New(runtimeSecret)
			}},
			resources: resources,
		}, nil
	}

	if code := runProcess(t.Context(), jsonLogger(&output), build); code != exitFailure {
		t.Fatalf("exit code = %d", code)
	}
	if strings.Contains(output.String(), runtimeSecret) ||
		strings.Contains(output.String(), shutdownSecret) {
		t.Fatal("process log disclosed error details")
	}
	entries := decodeLogEntries(t, output.Bytes())
	if len(entries) != 3 || entries[1]["phase"] != "runtime" ||
		entries[1]["error_class"] != "unknown" ||
		entries[2]["phase"] != "shutdown" ||
		entries[2]["error_class"] != "resource_shutdown" || resources.closeCalls != 1 {
		t.Fatalf("unexpected failure records: %#v", entries)
	}
}

func TestRunProcessTreatsRequestedStartupCancellationAsSuccess(t *testing.T) {
	var output bytes.Buffer
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	build := func(context.Context, *slog.Logger) (configuredProcess, error) {
		return configuredProcess{}, context.Canceled
	}

	if code := runProcess(ctx, jsonLogger(&output), build); code != exitSuccess {
		t.Fatalf("exit code = %d", code)
	}
	entries := decodeLogEntries(t, output.Bytes())
	if len(entries) != 1 || entries[0]["msg"] != processStoppedMessage ||
		entries[0]["phase"] != "startup" {
		t.Fatalf("unexpected cancellation record: %#v", entries)
	}
}

func TestRunProcessRejectsInvalidComposition(t *testing.T) {
	if code := runProcess(t.Context(), nil, nil); code != exitFailure {
		t.Fatalf("nil logger exit code = %d", code)
	}
	var output bytes.Buffer
	if code := runProcess(t.Context(), jsonLogger(&output), nil); code != exitFailure {
		t.Fatalf("nil builder exit code = %d", code)
	}
	entries := decodeLogEntries(t, output.Bytes())
	if len(entries) != 1 || entries[0]["error_class"] != "invalid_configuration" {
		t.Fatalf("unexpected invalid-composition record: %#v", entries)
	}

	output.Reset()
	resources := &resourcesStub{}
	build := func(context.Context, *slog.Logger) (configuredProcess, error) {
		return configuredProcess{resources: resources}, nil
	}
	if code := runProcess(t.Context(), jsonLogger(&output), build); code != exitFailure {
		t.Fatalf("incomplete process exit code = %d", code)
	}
	if resources.closeCalls != 1 {
		t.Fatalf("incomplete process close calls = %d", resources.closeCalls)
	}
}

func jsonLogger(output *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(output, nil))
}

func decodeLogEntries(t *testing.T, data []byte) []map[string]any {
	t.Helper()
	lines := bytes.Split(bytes.TrimSpace(data), []byte{'\n'})
	entries := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		var entry map[string]any
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, entry)
	}

	return entries
}
