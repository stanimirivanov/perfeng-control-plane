package app

import (
	"log/slog"

	"github.com/stanimirivanov/perfeng-control-plane/internal/httpapi"
)

// NewApplicationRuntime composes the complete HTTP and reconciliation service
// from acquired resources without starting loops or accessing external systems.
func NewApplicationRuntime(
	config ApplicationConfig,
	resources *StartupResources,
	logger *slog.Logger,
) (*Runtime, error) {
	if config.Validate() != nil || !validRuntimeResources(config, resources) || logger == nil {
		return nil, ErrInvalidConfiguration
	}

	controllers, err := NewKubernetesControllers(resources.Kubernetes)
	if err != nil {
		return nil, ErrInvalidConfiguration
	}
	collectors, err := NewArtifactCollectors(
		resources.ArtifactStorage,
		resources.Registry,
		resources.Database,
		config.ArtifactVerification,
	)
	if err != nil {
		return nil, ErrInvalidConfiguration
	}
	executors, err := NewKubernetesExecutors(
		resources.Registry,
		resources.Database,
		controllers,
		collectors,
		resources.ExecutorConfig,
	)
	if err != nil {
		return nil, ErrInvalidConfiguration
	}
	handler, err := NewAPIHandler(
		resources.Database,
		resources.Authentication,
		resources.Registry,
	)
	if err != nil {
		return nil, ErrInvalidConfiguration
	}
	nativeWorkerHandler, err := NewNativeWorkerAPIHandler(
		resources.Database,
		resources.Authentication,
		resources.Registry,
		resources.NativeUploadSigner,
	)
	if err != nil {
		return nil, ErrInvalidConfiguration
	}
	serviceHandler, err := httpapi.NewServiceHandler(handler, nativeWorkerHandler)
	if err != nil {
		return nil, ErrInvalidConfiguration
	}
	server, err := NewHTTPServer(serviceHandler, config.HTTPServer)
	if err != nil {
		return nil, ErrInvalidConfiguration
	}
	reporter, err := NewWorkerEventReporter(logger)
	if err != nil {
		return nil, ErrInvalidConfiguration
	}
	reconciliationWorker, err := NewReconciliationWorker(
		ReconciliationDependencies{
			Store:       resources.Database,
			Jobs:        resources.Registry,
			Controllers: controllers,
			Collectors:  collectors,
			Executors:   executors,
		},
		reporter.Report,
		config.Worker,
		config.Reconciliation,
	)
	if err != nil {
		return nil, ErrInvalidConfiguration
	}

	runtime, err := NewRuntime(
		server,
		reconciliationWorker,
		config.HTTPServer.ShutdownTimeout,
	)
	if err != nil {
		return nil, ErrInvalidConfiguration
	}

	return runtime, nil
}

func validRuntimeResources(config ApplicationConfig, resources *StartupResources) bool {
	return resources != nil && resources.Authentication != nil &&
		resources.Database != nil && resources.Registry != nil &&
		resources.ArtifactStorage != nil &&
		resources.NativeUploadSigner != nil &&
		resources.Kubernetes.Namespace == config.Kubernetes.Namespace &&
		resources.Kubernetes.Jobs != nil && resources.Kubernetes.Pods != nil &&
		resources.ExecutorConfig.Validate() == nil
}
