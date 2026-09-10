package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestServiceHealthIsUnauthenticatedAndMethodBound(t *testing.T) {
	service := newTestService(t)
	for _, path := range []string{livenessPath, readinessPath} {
		t.Run(path, func(t *testing.T) {
			response := httptest.NewRecorder()
			service.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
			if response.Code != http.StatusOK || response.Body.String() != "ok\n" {
				t.Fatalf("health response = %d %q", response.Code, response.Body.String())
			}
			if response.Header().Get("Cache-Control") != "no-store" ||
				response.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatal("health response lacks safe headers")
			}

			rejected := httptest.NewRecorder()
			service.ServeHTTP(rejected, httptest.NewRequest(http.MethodPost, path, nil))
			if rejected.Code != http.StatusMethodNotAllowed ||
				rejected.Header().Get("Allow") != http.MethodGet {
				t.Fatalf("method response = %d headers=%v", rejected.Code, rejected.Header())
			}
		})
	}
}

func TestServiceMetricsHaveBoundedLabelsAndExcludeOperationalTraffic(t *testing.T) {
	service := newTestService(t)
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/v1/runs/private-run-id", nil),
		httptest.NewRequest(http.MethodPost, "/v1/runs", nil),
		httptest.NewRequest("PRIVATE-METHOD", "/attacker-controlled-path", nil),
	} {
		service.ServeHTTP(httptest.NewRecorder(), request)
	}
	service.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, livenessPath, nil))

	response := httptest.NewRecorder()
	service.ServeHTTP(response, httptest.NewRequest(http.MethodGet, metricsPath, nil))
	if response.Code != http.StatusOK ||
		response.Header().Get("Content-Type") != "text/plain; version=0.0.4; charset=utf-8" {
		t.Fatalf("metrics response = %d headers=%v", response.Code, response.Header())
	}
	body := response.Body.String()
	for _, expected := range []string{
		"perfeng_control_plane_ready 1",
		`perfeng_control_plane_http_requests_total{method="GET",status_class="4xx"} 1`,
		`perfeng_control_plane_http_requests_total{method="POST",status_class="4xx"} 1`,
		`perfeng_control_plane_http_requests_total{method="OTHER",status_class="4xx"} 1`,
		"perfeng_control_plane_http_request_duration_seconds_count 3",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("metrics missing %q:\n%s", expected, body)
		}
	}
	for _, forbidden := range []string{"private-run-id", "attacker-controlled-path", "PRIVATE-METHOD"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("metrics exposed %q", forbidden)
		}
	}
}

func TestServiceMetricsAreConcurrencySafe(t *testing.T) {
	service := newTestService(t)
	const requests = 50
	var group sync.WaitGroup
	group.Add(requests)
	for range requests {
		go func() {
			defer group.Done()
			service.ServeHTTP(
				httptest.NewRecorder(),
				httptest.NewRequest(http.MethodGet, "/v1/runs/id", nil),
			)
		}()
	}
	group.Wait()

	response := httptest.NewRecorder()
	service.ServeHTTP(response, httptest.NewRequest(http.MethodGet, metricsPath, nil))
	if !strings.Contains(
		response.Body.String(),
		`perfeng_control_plane_http_requests_total{method="GET",status_class="4xx"} 50`,
	) {
		t.Fatal(response.Body.String())
	}
}

func newTestService(t *testing.T) *ServiceHandler {
	t.Helper()
	service, err := NewServiceHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func TestNewServiceHandlerRejectsMissingAPI(t *testing.T) {
	if _, err := NewServiceHandler(nil); err == nil {
		t.Fatal("NewServiceHandler(nil) succeeded")
	}
}
