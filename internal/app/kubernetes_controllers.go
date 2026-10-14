package app

import (
	"time"

	platformkubernetes "github.com/stanimirivanov/perfeng-control-plane/internal/kubernetes"
)

// KubernetesControllers contains the Kubernetes adapters used across execution,
// cancellation, normalization and reporting reconciliation.
type KubernetesControllers struct {
	Execution   *platformkubernetes.Dispatcher
	Termination *platformkubernetes.StopVerifier
	Analysis    *platformkubernetes.AnalysisDispatcher
	Reporting   *platformkubernetes.ReportingDispatcher
	Benchmark   *platformkubernetes.BenchmarkController
}

// NewKubernetesControllers binds every controller to the clients' namespace.
// Construction validates dependencies without calling the Kubernetes API.
func NewKubernetesControllers(clients KubernetesClients) (KubernetesControllers, error) {
	execution, err := platformkubernetes.NewDispatcher(clients.Jobs, clients.Namespace)
	if err != nil {
		return KubernetesControllers{}, ErrInvalidConfiguration
	}
	termination, err := platformkubernetes.NewStopVerifier(clients.Pods, clients.Namespace)
	if err != nil {
		return KubernetesControllers{}, ErrInvalidConfiguration
	}
	analysis, err := platformkubernetes.NewAnalysisDispatcher(clients.Jobs, clients.Namespace)
	if err != nil {
		return KubernetesControllers{}, ErrInvalidConfiguration
	}
	reporting, err := platformkubernetes.NewReportingDispatcher(clients.Jobs, clients.Namespace)
	if err != nil {
		return KubernetesControllers{}, ErrInvalidConfiguration
	}

	var benchmark *platformkubernetes.BenchmarkController
	if clients.Namespace == "perf-generators" && (clients.BenchmarkClient != nil || clients.BenchmarkMetrics != nil) {
		benchmark, err = platformkubernetes.NewBenchmarkController(clients.BenchmarkClient, clients.BenchmarkMetrics, clients.Namespace, time.Now)
		if err != nil {
			return KubernetesControllers{}, ErrInvalidConfiguration
		}
	}
	return KubernetesControllers{
		Execution:   execution,
		Termination: termination,
		Analysis:    analysis,
		Reporting:   reporting,
		Benchmark:   benchmark,
	}, nil
}
