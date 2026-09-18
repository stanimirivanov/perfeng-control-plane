package httpapi

import (
	"errors"
	"net/http"
	"strings"
)

const (
	livenessPath  = "/livez"
	readinessPath = "/readyz"
	metricsPath   = "/metrics"
)

// ServiceHandler serves operational endpoints beside the authenticated API.
// Operational responses contain no dependency or request-specific details.
type ServiceHandler struct {
	api          http.Handler
	nativeWorker http.Handler
	metrics      httpMetrics
}

// NewServiceHandler constructs the complete service HTTP boundary.
func NewServiceHandler(api, nativeWorker http.Handler) (*ServiceHandler, error) {
	if api == nil || nativeWorker == nil {
		return nil, errors.New("API and native worker handlers are required")
	}
	return &ServiceHandler{api: api, nativeWorker: nativeWorker}, nil
}

func (handler *ServiceHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == livenessPath || r.URL.Path == readinessPath:
		serveHealth(w, r)
	case r.URL.Path == metricsPath:
		handler.serveMetrics(w, r)
	case strings.HasPrefix(r.URL.Path, "/native-worker/"):
		handler.metrics.observe(handler.nativeWorker, w, r)
	default:
		handler.metrics.observe(handler.api, w, r)
	}
}

func serveHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	setOperationalHeaders(w, "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

func (handler *ServiceHandler) serveMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	setOperationalHeaders(w, "text/plain; version=0.0.4; charset=utf-8")
	_ = handler.metrics.writePrometheus(w)
}

func methodNotAllowed(w http.ResponseWriter) {
	w.Header().Set("Allow", http.MethodGet)
	setOperationalHeaders(w, "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusMethodNotAllowed)
	_, _ = w.Write([]byte("method not allowed\n"))
}

func setOperationalHeaders(w http.ResponseWriter, contentType string) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}
