package httpapi

import (
	"context"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

func TestIntegrationCancellationBindsIdentityContextAndTerminalState(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		state                     run.State
		permit                    bool
		pathID, issuer, principal string
		status                    int
		calls                     int
	}{
		{"active", run.StateCancelling, true, "", "", "alice", 202, 1},
		{"unstarted", run.StateAborted, true, "", "", "alice", 200, 1},
		{"completed", run.StateCompleted, true, "", "", "alice", 200, 1},
		{"failed", run.StateTestFailure, true, "", "", "alice", 200, 1},
		{"permission", run.StateCancelling, false, "", "", "alice", 403, 0},
		{"path", run.StateCancelling, true, "other", "", "alice", 400, 0},
		{"issuer", run.StateCancelling, true, "", "https://other.example", "alice", 400, 0},
		{"principal", run.StateCancelling, true, "", "", "bob", 403, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			message, store, options := integrationHTTPFixture(t)
			message.Type = "dev.perfeng.integration.cancel-requested.v1"
			message.Data.Reason = "user"
			store.result.Run.State = tc.state
			raw, err := options.Encode(message)
			if err != nil {
				t.Fatal(err)
			}
			id, issuer := message.Data.Context.Request.ID, message.Data.Context.Request.Issuer
			if tc.pathID != "" {
				id = tc.pathID
			}
			if tc.issuer != "" {
				issuer = tc.issuer
			}
			path := integrationPath + "/" + url.PathEscape(id) + "/cancellation?issuer=" + url.QueryEscape(issuer)
			h, err := NewIntegrationHandler(store, func(context.Context, string) (Identity, error) {
				return Identity{Principal: tc.principal, CancelRun: tc.permit}, nil
			}, options)
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("POST", path, strings.NewReader(string(raw)))
			r.Header.Set("Authorization", "Bearer test")
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status || store.calls != tc.calls {
				t.Fatal(w.Code, store.calls, w.Body.String())
			}
			if tc.status == 200 || tc.status == 202 {
				response, err := options.Decode(w.Body.Bytes())
				if err != nil || response.Type != "dev.perfeng.integration.lifecycle.v1" {
					t.Fatal("invalid cancellation response", err)
				}
			}
		})
	}
}
