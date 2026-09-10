// control-plane runs the authenticated API and reconciliation worker.
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/stanimirivanov/perfeng-control-plane/internal/app"
)

const (
	exitSuccess = 0
	exitFailure = 1

	processStartedMessage = "control-plane process started"
	processStoppedMessage = "control-plane process stopped"
	processFailedMessage  = "control-plane process failed"
)

type processPhase string

const (
	phaseStartup  processPhase = "startup"
	phaseRuntime  processPhase = "runtime"
	phaseShutdown processPhase = "shutdown"
)

type processRuntime interface {
	// Run owns the service loops until cancellation or an unexpected failure.
	Run(context.Context) error
}

type processResources interface {
	// Close releases all resources owned by the process exactly once.
	Close() error
}

type configuredProcess struct {
	runtime   processRuntime
	resources processResources
}

type buildProcess func(context.Context, *slog.Logger) (configuredProcess, error)

func main() {
	os.Exit(execute())
}

func execute() int {
	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	return runProcess(ctx, logger, buildConfiguredProcess)
}

func buildConfiguredProcess(
	ctx context.Context,
	logger *slog.Logger,
) (configuredProcess, error) {
	config, err := app.LoadApplicationConfig()
	if err != nil {
		return configuredProcess{}, err
	}
	resources, err := app.NewStartupResources(ctx, config)
	if err != nil {
		return configuredProcess{}, err
	}
	runtime, err := app.NewApplicationRuntime(config, resources, logger)
	if err != nil {
		if closeErr := resources.Close(); closeErr != nil {
			return configuredProcess{}, closeErr
		}

		return configuredProcess{}, err
	}

	return configuredProcess{runtime: runtime, resources: resources}, nil
}

func runProcess(ctx context.Context, logger *slog.Logger, build buildProcess) int {
	if ctx == nil || logger == nil {
		return exitFailure
	}
	if build == nil {
		reportProcessFailure(logger, phaseStartup, app.ErrInvalidConfiguration)

		return exitFailure
	}

	process, err := build(ctx, logger)
	if err != nil {
		if cancellationRequested(ctx, err) {
			reportProcessStopped(logger, phaseStartup)

			return exitSuccess
		}
		reportProcessFailure(logger, phaseStartup, err)

		return exitFailure
	}
	if process.runtime == nil || process.resources == nil {
		reportProcessFailure(logger, phaseStartup, app.ErrInvalidConfiguration)
		if process.resources != nil {
			if closeErr := process.resources.Close(); closeErr != nil {
				reportProcessFailure(logger, phaseShutdown, closeErr)
			}
		}

		return exitFailure
	}

	logger.Info(processStartedMessage, "component", "control_plane")
	runtimeErr := process.runtime.Run(ctx)
	shutdownErr := process.resources.Close()
	failed := false
	if runtimeErr != nil && !cancellationRequested(ctx, runtimeErr) {
		reportProcessFailure(logger, phaseRuntime, runtimeErr)
		failed = true
	}
	if shutdownErr != nil {
		reportProcessFailure(logger, phaseShutdown, shutdownErr)
		failed = true
	}
	if failed {
		return exitFailure
	}

	reportProcessStopped(logger, phaseShutdown)

	return exitSuccess
}

func reportProcessStopped(logger *slog.Logger, phase processPhase) {
	logger.Info(
		processStoppedMessage,
		"component", "control_plane",
		"phase", string(phase),
	)
}

func reportProcessFailure(logger *slog.Logger, phase processPhase, err error) {
	logger.Error(
		processFailedMessage,
		"component", "control_plane",
		"phase", string(phase),
		"error_class", processErrorClass(err),
	)
}

func processErrorClass(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, app.ErrInvalidConfiguration):
		return "invalid_configuration"
	case errors.Is(err, app.ErrDependencyInitialization):
		return "dependency_initialization"
	case errors.Is(err, app.ErrRuntimeFailure):
		return "runtime_failure"
	case errors.Is(err, app.ErrResourceShutdown):
		return "resource_shutdown"
	default:
		return "unknown"
	}
}

func cancellationRequested(ctx context.Context, err error) bool {
	return ctx.Err() != nil && errors.Is(err, ctx.Err())
}
