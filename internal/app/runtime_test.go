package app

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"
)

type inertHTTPServer struct {
	started     chan struct{}
	stopped     chan struct{}
	serveError  error
	shutdownErr error
	stopOnce    sync.Once
}

func newInertHTTPServer() *inertHTTPServer {
	return &inertHTTPServer{started: make(chan struct{}), stopped: make(chan struct{})}
}

func (server *inertHTTPServer) ListenAndServe() error {
	close(server.started)
	if server.serveError != nil {
		return server.serveError
	}
	<-server.stopped

	return http.ErrServerClosed
}

func (server *inertHTTPServer) Shutdown(ctx context.Context) error {
	server.stopOnce.Do(func() { close(server.stopped) })
	if server.shutdownErr != nil {
		return server.shutdownErr
	}

	return ctx.Err()
}

type inertRuntimeWorker struct {
	started chan struct{}
	err     error
}

type blockedRuntimeWorker struct {
	started chan struct{}
	release chan struct{}
}

func (runtimeWorker *blockedRuntimeWorker) Run(ctx context.Context) error {
	close(runtimeWorker.started)
	<-runtimeWorker.release

	return ctx.Err()
}

func newInertRuntimeWorker() *inertRuntimeWorker {
	return &inertRuntimeWorker{started: make(chan struct{})}
}

func (runtimeWorker *inertRuntimeWorker) Run(ctx context.Context) error {
	close(runtimeWorker.started)
	if runtimeWorker.err != nil {
		return runtimeWorker.err
	}
	<-ctx.Done()

	return ctx.Err()
}

func TestRuntimeCoordinatesGracefulCallerCancellation(t *testing.T) {
	server := newInertHTTPServer()
	runtimeWorker := newInertRuntimeWorker()
	runtime, err := NewRuntime(server, runtimeWorker, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runtime.Run(ctx) }()

	awaitStarted(t, server.started, "HTTP server")
	awaitStarted(t, runtimeWorker.started, "reconciliation worker")
	cancel()
	if err := awaitRuntime(t, done); err != nil {
		t.Fatalf("runtime error = %v", err)
	}
}

func TestRuntimeCancelsPeerAndClassifiesUnexpectedExit(t *testing.T) {
	failure := errors.New("provider detail")
	tests := map[string]struct {
		serverError error
		workerError error
	}{
		"HTTP server":           {serverError: failure},
		"reconciliation worker": {workerError: failure},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			server := newInertHTTPServer()
			server.serveError = test.serverError
			runtimeWorker := newInertRuntimeWorker()
			runtimeWorker.err = test.workerError
			runtime, err := NewRuntime(server, runtimeWorker, time.Second)
			if err != nil {
				t.Fatal(err)
			}

			err = runtime.Run(context.Background())
			if !errors.Is(err, ErrRuntimeFailure) || errors.Is(err, failure) {
				t.Fatalf("runtime error = %v", err)
			}
			awaitStarted(t, server.stopped, "HTTP shutdown")
			awaitStarted(t, runtimeWorker.started, "reconciliation worker")
		})
	}
}

func TestRuntimeBoundsPeerShutdown(t *testing.T) {
	server := newInertHTTPServer()
	runtimeWorker := &blockedRuntimeWorker{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	runtime, err := NewRuntime(server, runtimeWorker, 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runtime.Run(ctx) }()
	awaitStarted(t, server.started, "HTTP server")
	awaitStarted(t, runtimeWorker.started, "reconciliation worker")
	cancel()

	if err := awaitRuntime(t, done); !errors.Is(err, ErrRuntimeFailure) {
		t.Fatalf("runtime error = %v", err)
	}
	close(runtimeWorker.release)
}

func TestRuntimeClassifiesShutdownFailure(t *testing.T) {
	server := newInertHTTPServer()
	server.shutdownErr = errors.New("listener detail")
	runtimeWorker := newInertRuntimeWorker()
	runtime, err := NewRuntime(server, runtimeWorker, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runtime.Run(ctx) }()
	awaitStarted(t, server.started, "HTTP server")
	awaitStarted(t, runtimeWorker.started, "reconciliation worker")
	cancel()

	if err := awaitRuntime(t, done); !errors.Is(err, ErrRuntimeFailure) {
		t.Fatalf("runtime error = %v", err)
	}
}

func TestNewRuntimeRejectsInvalidDependencies(t *testing.T) {
	server := newInertHTTPServer()
	runtimeWorker := newInertRuntimeWorker()
	for name, test := range map[string]struct {
		server  ServingHTTPServer
		worker  ReconciliationWorker
		timeout time.Duration
	}{
		"server":  {worker: runtimeWorker, timeout: time.Second},
		"worker":  {server: server, timeout: time.Second},
		"timeout": {server: server, worker: runtimeWorker},
	} {
		t.Run(name, func(t *testing.T) {
			if runtime, err := NewRuntime(test.server, test.worker, test.timeout); runtime != nil ||
				!errors.Is(err, ErrInvalidConfiguration) {
				t.Fatalf("runtime = %#v, error = %v", runtime, err)
			}
		})
	}

	var runtime *Runtime
	if err := runtime.Run(context.Background()); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("nil runtime error = %v", err)
	}
	valid, err := NewRuntime(server, runtimeWorker, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := valid.Run(nil); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("nil context error = %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := valid.Run(cancelled); err != nil {
		t.Fatalf("already cancelled error = %v", err)
	}
}

func awaitStarted(t *testing.T, started <-chan struct{}, component string) {
	t.Helper()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatalf("%s did not start", component)
	}
}

func awaitRuntime(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(time.Second):
		t.Fatal("runtime did not stop")

		return nil
	}
}
