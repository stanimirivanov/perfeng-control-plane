package kubernetes

import (
	"context"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

// benchmarkRequestBound verifies the reviewed kubeadm API-server command, rather
// than mistaking the client's HTTP timeout for a server-side mutation deadline.
func (controller *BenchmarkController) benchmarkRequestBound(ctx context.Context) error {
	pods, err := controller.client.CoreV1().Pods("kube-system").List(ctx, metav1.ListOptions{LabelSelector: "component=kube-apiserver", Limit: 2})
	if err != nil {
		return classifyAPIError("benchmark API request bound", err)
	}
	if pods.Continue != "" || len(pods.Items) != 1 {
		return run.ErrUnavailable
	}
	pod := pods.Items[0]
	if pod.DeletionTimestamp != nil || len(pod.Spec.Containers) != 1 {
		return run.ErrBenchmarkConflict
	}
	container := pod.Spec.Containers[0]
	if len(container.Command) == 0 || container.Command[0] != "kube-apiserver" {
		return run.ErrBenchmarkConflict
	}
	arguments := append(append([]string{}, container.Command[1:]...), container.Args...)
	seen := false
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		var value string
		if strings.HasPrefix(argument, "--request-timeout=") {
			value = strings.TrimPrefix(argument, "--request-timeout=")
		} else if argument == "--request-timeout" && index+1 < len(arguments) {
			index++
			value = arguments[index]
		} else if argument == "--request-timeout" {
			return run.ErrBenchmarkConflict
		} else {
			continue
		}
		if seen {
			return run.ErrBenchmarkConflict
		}
		seen = true
		parsed, err := time.ParseDuration(value)
		if err != nil || parsed <= 0 || parsed > time.Minute {
			return run.ErrBenchmarkConflict
		}
	}
	return nil
}
