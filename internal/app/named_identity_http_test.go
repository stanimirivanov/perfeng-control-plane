package app

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/httpapi"
)

func TestNamedHTTPCompositionRequiresQualifiedDependencies(t *testing.T) {
	operation := func(http.ResponseWriter, *http.Request, httpapi.NamedIdentity) {}
	for _, runtime := range []*NamedIdentityAuthentication{nil, {}} {
		if handler, err := NewNamedIdentityBoundary(true, runtime, operation); handler != nil || !errors.Is(err, ErrInvalidConfiguration) {
			t.Fatal("unqualified verifier accepted")
		}
	}
	disabled, err := NewNamedIdentityBoundary(false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	disabled.ServeHTTP(response, httptest.NewRequest("GET", "/fixture-only", nil))
	if response.Code != 404 {
		t.Fatal("disabled composition became callable")
	}
}
