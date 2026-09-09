package app

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/worker"
)

// ErrRuntimeFailure classifies an unexpected server, worker or shutdown exit
// without exposing provider or request details.
var ErrRuntimeFailure = errors.New("control-plane runtime failed")

// ServingHTTPServer owns the blocking HTTP serve loop and graceful shutdown.
type ServingHTTPServer interface {
	// ListenAndServe serves until shutdown or an unexpected listener failure.
	ListenAndServe() error
	// Shutdown stops accepting requests, waits for active handlers and honors ctx.
	Shutdown(context.Context) error
}

// ReconciliationWorker owns the blocking lifecycle reconciliation loop.
type ReconciliationWorker interface {
	// Run reconciles until ctx is cancelled or the worker exits unexpectedly.
	Run(context.Context) error
}

// Runtime supervises the HTTP and reconciliation loops as one process lifetime.
type Runtime struct {
	server          ServingHTTPServer
	worker          ReconciliationWorker
	shutdownTimeout time.Duration
}

type runtimeComponent string

const (
	runtimeHTTP   runtimeComponent = "HTTP server"
	runtimeWorker runtimeComponent = "reconciliation worker"
)

type runtimeResult struct {
	component runtimeComponent
	err       error
}

// NewRuntime validates the supervised services and bounded shutdown budget.
func NewRuntime(
	server ServingHTTPServer,
	worker ReconciliationWorker,
	shutdownTimeout time.Duration,
) (*Runtime, error) {
	if server == nil || worker == nil || !validRuntimeDuration(shutdownTimeout) {
		return nil, ErrInvalidConfiguration
	}

	return &Runtime{server: server, worker: worker, shutdownTimeout: shutdownTimeout}, nil
}

// Run starts both services, cancels the peer when either exits and performs a
// bounded HTTP shutdown. Caller cancellation is a successful graceful stop.
func (runtime *Runtime) Run(ctx context.Context) error {
	if runtime == nil || ctx == nil {
		return ErrInvalidConfiguration
	}
	if ctx.Err() != nil {
		return nil
	}

	runContext, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan runtimeResult, 2)
	go func() {
		results <- runtimeResult{component: runtimeHTTP, err: runtime.server.ListenAndServe()}
	}()
	go func() {
		results <- runtimeResult{component: runtimeWorker, err: runtime.worker.Run(runContext)}
	}()

	completed := make([]runtimeResult, 0, 2)
	select {
	case result := <-results:
		completed = append(completed, result)
	case <-ctx.Done():
	}
	graceful := ctx.Err() != nil
	cancel()

	shutdownContext, stopShutdown := context.WithTimeout(
		context.WithoutCancel(ctx),
		runtime.shutdownTimeout,
	)
	defer stopShutdown()
	shutdownErr := runtime.server.Shutdown(shutdownContext)
	for len(completed) < 2 {
		select {
		case result := <-results:
			completed = append(completed, result)
		case <-shutdownContext.Done():
			return ErrRuntimeFailure
		}
	}

	if shutdownErr != nil || !runtimeResultsExpected(completed, runContext.Err(), graceful) {
		return ErrRuntimeFailure
	}

	return nil
}

func runtimeResultsExpected(
	results []runtimeResult,
	shutdownCause error,
	graceful bool,
) bool {
	if len(results) != 2 || shutdownCause == nil {
		return false
	}
	for _, result := range results {
		switch result.component {
		case runtimeHTTP:
			if !errors.Is(result.err, http.ErrServerClosed) {
				return false
			}
		case runtimeWorker:
			if !errors.Is(result.err, shutdownCause) {
				return false
			}
		default:
			return false
		}
	}

	return graceful
}

var _ ServingHTTPServer = (*http.Server)(nil)
var _ ReconciliationWorker = (*worker.Worker)(nil)
