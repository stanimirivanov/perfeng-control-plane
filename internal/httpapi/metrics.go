package httpapi

import (
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"
)

const (
	methodGet = iota
	methodPost
	methodOther
	methodCount
)

var (
	methodLabels    = [methodCount]string{"GET", "POST", "OTHER"}
	durationBuckets = [...]struct {
		upper time.Duration
		label string
	}{
		{5 * time.Millisecond, "0.005"},
		{10 * time.Millisecond, "0.01"},
		{25 * time.Millisecond, "0.025"},
		{50 * time.Millisecond, "0.05"},
		{100 * time.Millisecond, "0.1"},
		{250 * time.Millisecond, "0.25"},
		{500 * time.Millisecond, "0.5"},
		{time.Second, "1"},
		{2500 * time.Millisecond, "2.5"},
		{5 * time.Second, "5"},
	}
)

type httpMetrics struct {
	inFlight       atomic.Int64
	requests       [methodCount][6]atomic.Uint64
	durationCount  atomic.Uint64
	durationNanos  atomic.Uint64
	durationBucket [len(durationBuckets)]atomic.Uint64
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (recorder *statusRecorder) WriteHeader(status int) {
	if recorder.status != 0 {
		return
	}
	recorder.status = status
	recorder.ResponseWriter.WriteHeader(status)
}

func (recorder *statusRecorder) Write(body []byte) (int, error) {
	if recorder.status == 0 {
		recorder.status = http.StatusOK
	}
	return recorder.ResponseWriter.Write(body)
}

// Unwrap exposes the original writer to http.ResponseController.
func (recorder *statusRecorder) Unwrap() http.ResponseWriter {
	return recorder.ResponseWriter
}

func (metrics *httpMetrics) observe(handler http.Handler, w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	metrics.inFlight.Add(1)
	recorder := &statusRecorder{ResponseWriter: w}
	defer func() {
		metrics.inFlight.Add(-1)
		status := recorder.status
		if status == 0 {
			status = http.StatusOK
		}
		metrics.requests[methodIndex(r.Method)][statusClass(status)].Add(1)
		elapsed := time.Since(started)
		metrics.durationCount.Add(1)
		metrics.durationNanos.Add(uint64(elapsed))
		for index, bucket := range durationBuckets {
			if elapsed <= bucket.upper {
				metrics.durationBucket[index].Add(1)
			}
		}
	}()
	handler.ServeHTTP(recorder, r)
}

func (metrics *httpMetrics) writePrometheus(w io.Writer) error {
	if _, err := fmt.Fprintln(w, "# HELP perfeng_control_plane_ready Whether startup dependencies were acquired."); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "# TYPE perfeng_control_plane_ready gauge\nperfeng_control_plane_ready 1"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "# HELP perfeng_control_plane_http_requests_in_flight Current API requests.\n# TYPE perfeng_control_plane_http_requests_in_flight gauge"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "perfeng_control_plane_http_requests_in_flight %d\n", metrics.inFlight.Load()); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "# HELP perfeng_control_plane_http_requests_total Completed API requests.\n# TYPE perfeng_control_plane_http_requests_total counter"); err != nil {
		return err
	}
	for method, label := range methodLabels {
		for class := range 6 {
			if _, err := fmt.Fprintf(
				w,
				"perfeng_control_plane_http_requests_total{method=%q,status_class=%q} %d\n",
				label,
				statusClassLabel(class),
				metrics.requests[method][class].Load(),
			); err != nil {
				return err
			}
		}
	}
	if _, err := fmt.Fprintln(w, "# HELP perfeng_control_plane_http_request_duration_seconds API request duration.\n# TYPE perfeng_control_plane_http_request_duration_seconds histogram"); err != nil {
		return err
	}
	for index, bucket := range durationBuckets {
		if _, err := fmt.Fprintf(
			w,
			"perfeng_control_plane_http_request_duration_seconds_bucket{le=%q} %d\n",
			bucket.label,
			metrics.durationBucket[index].Load(),
		); err != nil {
			return err
		}
	}
	count := metrics.durationCount.Load()
	if _, err := fmt.Fprintf(w, "perfeng_control_plane_http_request_duration_seconds_bucket{le=%q} %d\n", "+Inf", count); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "perfeng_control_plane_http_request_duration_seconds_sum %.9f\n", float64(metrics.durationNanos.Load())/float64(time.Second)); err != nil {
		return err
	}
	_, err := fmt.Fprintf(w, "perfeng_control_plane_http_request_duration_seconds_count %d\n", count)
	return err
}

func methodIndex(method string) int {
	switch method {
	case http.MethodGet:
		return methodGet
	case http.MethodPost:
		return methodPost
	default:
		return methodOther
	}
}

func statusClass(status int) int {
	class := status / 100
	if class < 1 || class > 5 {
		return 0
	}
	return class
}

func statusClassLabel(class int) string {
	if class < 1 || class > 5 {
		return "other"
	}
	return fmt.Sprintf("%dxx", class)
}
