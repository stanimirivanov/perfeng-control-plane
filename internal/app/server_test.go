package app

import (
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestNewHTTPServerAppliesBoundedPolicy(t *testing.T) {
	handler := http.NewServeMux()
	config := validHTTPServerConfig()

	server, err := NewHTTPServer(handler, config)
	if err != nil {
		t.Fatal(err)
	}
	if server.Addr != config.Address || server.Handler != handler ||
		server.ReadHeaderTimeout != config.ReadHeaderTimeout ||
		server.ReadTimeout != config.ReadTimeout || server.WriteTimeout != config.WriteTimeout ||
		server.IdleTimeout != config.IdleTimeout || server.MaxHeaderBytes != config.MaximumHeaderBytes {
		t.Fatalf("server = %#v", server)
	}
}

func TestNewHTTPServerRejectsInvalidDependencies(t *testing.T) {
	valid := validHTTPServerConfig()
	invalid := valid
	invalid.ReadHeaderTimeout = 0
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})

	for name, test := range map[string]struct {
		handler http.Handler
		config  HTTPServerConfig
	}{
		"handler": {config: valid},
		"config":  {handler: handler, config: invalid},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewHTTPServer(test.handler, test.config); !errors.Is(err, ErrInvalidConfiguration) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func validHTTPServerConfig() HTTPServerConfig {
	return HTTPServerConfig{
		Address:            ":8080",
		ReadHeaderTimeout:  5 * time.Second,
		ReadTimeout:        15 * time.Second,
		WriteTimeout:       30 * time.Second,
		IdleTimeout:        60 * time.Second,
		ShutdownTimeout:    30 * time.Second,
		MaximumHeaderBytes: 32768,
	}
}
