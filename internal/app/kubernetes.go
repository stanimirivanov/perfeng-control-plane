package app

import (
	"context"

	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	platformkubernetes "github.com/stanimirivanov/perfeng-control-plane/internal/kubernetes"
)

// KubernetesClients contains only the namespaced operations required by
// execution dispatch, observation, cancellation and termination verification.
type KubernetesClients struct {
	Namespace string
	Jobs      platformkubernetes.Jobs
	Pods      platformkubernetes.Pods
}

type loadKubernetesRESTConfig func() (*rest.Config, error)
type newKubernetesClientset func(*rest.Config) (clientset.Interface, error)

// NewKubernetesClients loads in-cluster credentials and constructs namespaced
// clients without making an API-server request.
func NewKubernetesClients(
	ctx context.Context,
	config KubernetesConfig,
) (KubernetesClients, error) {
	return newKubernetesClients(ctx, config, rest.InClusterConfig, kubernetesClientsetFromConfig)
}

func kubernetesClientsetFromConfig(config *rest.Config) (clientset.Interface, error) {
	return clientset.NewForConfig(config)
}

func newKubernetesClients(
	ctx context.Context,
	config KubernetesConfig,
	load loadKubernetesRESTConfig,
	newClientset newKubernetesClientset,
) (KubernetesClients, error) {
	if err := ctx.Err(); err != nil {
		return KubernetesClients{}, err
	}
	if config.Validate() != nil || load == nil || newClientset == nil {
		return KubernetesClients{}, ErrInvalidConfiguration
	}

	restConfig, err := load()
	if err != nil {
		return KubernetesClients{}, classifyDependencyInitialization(ctx, err)
	}
	if err := ctx.Err(); err != nil {
		return KubernetesClients{}, err
	}
	if restConfig == nil {
		return KubernetesClients{}, ErrDependencyInitialization
	}
	restConfig = rest.CopyConfig(restConfig)
	restConfig.Timeout = config.RequestTimeout
	restConfig.QPS = config.QPS
	restConfig.Burst = config.Burst
	restConfig.UserAgent = "perfeng-control-plane"

	clients, err := newClientset(restConfig)
	if err != nil {
		return KubernetesClients{}, classifyDependencyInitialization(ctx, err)
	}
	if clients == nil {
		return KubernetesClients{}, ErrDependencyInitialization
	}

	return KubernetesClients{
		Namespace: config.Namespace,
		Jobs:      clients.BatchV1().Jobs(config.Namespace),
		Pods:      clients.CoreV1().Pods(config.Namespace),
	}, nil
}
