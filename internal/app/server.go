package app

import "net/http"

// NewHTTPServer applies validated listener limits to one required handler. It
// does not bind a socket or start serving.
func NewHTTPServer(handler http.Handler, config HTTPServerConfig) (*http.Server, error) {
	if handler == nil || config.Validate() != nil {
		return nil, ErrInvalidConfiguration
	}

	return &http.Server{
		Addr:              config.Address,
		Handler:           handler,
		ReadHeaderTimeout: config.ReadHeaderTimeout,
		ReadTimeout:       config.ReadTimeout,
		WriteTimeout:      config.WriteTimeout,
		IdleTimeout:       config.IdleTimeout,
		MaxHeaderBytes:    config.MaximumHeaderBytes,
	}, nil
}
