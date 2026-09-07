package app

import (
	"math"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	kubernetesNamespaceVariable      = "PERFENG_KUBERNETES_NAMESPACE"
	kubernetesRequestTimeoutVariable = "PERFENG_KUBERNETES_REQUEST_TIMEOUT"
	kubernetesQPSVariable            = "PERFENG_KUBERNETES_QPS"
	kubernetesBurstVariable          = "PERFENG_KUBERNETES_BURST"
)

// KubernetesConfig contains in-cluster namespace, timeout and rate-limit policy.
type KubernetesConfig struct {
	Namespace      string
	RequestTimeout time.Duration
	QPS            float32
	Burst          int
}

// Validate checks the namespace and bounded client request policy.
func (config KubernetesConfig) Validate() error {
	if len(validation.IsDNS1123Label(config.Namespace)) != 0 ||
		!validRuntimeDuration(config.RequestTimeout) ||
		math.IsNaN(float64(config.QPS)) ||
		math.IsInf(float64(config.QPS), 0) ||
		config.QPS < 0.1 || config.QPS > 100 ||
		config.Burst < 1 || config.Burst > 1000 {
		return ErrInvalidConfiguration
	}

	return nil
}

// LoadKubernetesConfig reads explicit in-cluster client and namespace policy.
func LoadKubernetesConfig() (KubernetesConfig, error) {
	return loadKubernetesConfig(os.LookupEnv)
}

func loadKubernetesConfig(lookup lookupEnvironment) (KubernetesConfig, error) {
	if lookup == nil {
		return KubernetesConfig{}, configurationError("environment")
	}

	namespace, err := requiredValue(lookup, kubernetesNamespaceVariable)
	if err != nil {
		return KubernetesConfig{}, err
	}
	requestTimeout, err := requiredDuration(lookup, kubernetesRequestTimeoutVariable)
	if err != nil {
		return KubernetesConfig{}, err
	}
	qps, err := requiredBoundedFloat(lookup, kubernetesQPSVariable, 0.1, 100)
	if err != nil {
		return KubernetesConfig{}, err
	}
	burst, err := requiredBoundedInteger(lookup, kubernetesBurstVariable, 1, 1000)
	if err != nil {
		return KubernetesConfig{}, err
	}

	config := KubernetesConfig{
		Namespace:      namespace,
		RequestTimeout: requestTimeout,
		QPS:            float32(qps),
		Burst:          burst,
	}
	if config.Validate() != nil {
		return KubernetesConfig{}, configurationError("Kubernetes client policy")
	}

	return config, nil
}
