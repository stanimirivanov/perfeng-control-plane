package app

import (
	"net"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	httpAddressVariable            = "PERFENG_HTTP_ADDRESS"
	httpReadHeaderTimeoutVariable  = "PERFENG_HTTP_READ_HEADER_TIMEOUT"
	httpReadTimeoutVariable        = "PERFENG_HTTP_READ_TIMEOUT"
	httpWriteTimeoutVariable       = "PERFENG_HTTP_WRITE_TIMEOUT"
	httpIdleTimeoutVariable        = "PERFENG_HTTP_IDLE_TIMEOUT"
	httpShutdownTimeoutVariable    = "PERFENG_HTTP_SHUTDOWN_TIMEOUT"
	httpMaximumHeaderBytesVariable = "PERFENG_HTTP_MAX_HEADER_BYTES"
	minimumHTTPHeaderBytes         = 1024
	maximumHTTPHeaderBytes         = 1 << 20
)

// HTTPServerConfig contains bounded listener and timeout policy.
type HTTPServerConfig struct {
	Address            string
	ReadHeaderTimeout  time.Duration
	ReadTimeout        time.Duration
	WriteTimeout       time.Duration
	IdleTimeout        time.Duration
	ShutdownTimeout    time.Duration
	MaximumHeaderBytes int
}

// Validate checks listener syntax and positive bounded server limits.
func (config HTTPServerConfig) Validate() error {
	host, port, err := net.SplitHostPort(config.Address)
	portNumber, portErr := strconv.ParseUint(port, 10, 16)
	if err != nil || portErr != nil || portNumber == 0 ||
		!validRequiredValue(config.Address) || strings.IndexFunc(host, unicode.IsSpace) >= 0 ||
		!validRuntimeDuration(config.ReadHeaderTimeout) ||
		!validRuntimeDuration(config.ReadTimeout) ||
		config.ReadHeaderTimeout > config.ReadTimeout ||
		!validRuntimeDuration(config.WriteTimeout) ||
		!validRuntimeDuration(config.IdleTimeout) ||
		!validRuntimeDuration(config.ShutdownTimeout) ||
		config.MaximumHeaderBytes < minimumHTTPHeaderBytes ||
		config.MaximumHeaderBytes > maximumHTTPHeaderBytes {
		return ErrInvalidConfiguration
	}

	return nil
}

// LoadHTTPServerConfig reads and validates the bounded HTTP listener policy.
func LoadHTTPServerConfig() (HTTPServerConfig, error) {
	return loadHTTPServerConfig(os.LookupEnv)
}

func loadHTTPServerConfig(lookup lookupEnvironment) (HTTPServerConfig, error) {
	if lookup == nil {
		return HTTPServerConfig{}, configurationError("environment")
	}

	address, err := requiredValue(lookup, httpAddressVariable)
	if err != nil {
		return HTTPServerConfig{}, err
	}
	readHeaderTimeout, err := requiredDuration(lookup, httpReadHeaderTimeoutVariable)
	if err != nil {
		return HTTPServerConfig{}, err
	}
	readTimeout, err := requiredDuration(lookup, httpReadTimeoutVariable)
	if err != nil {
		return HTTPServerConfig{}, err
	}
	writeTimeout, err := requiredDuration(lookup, httpWriteTimeoutVariable)
	if err != nil {
		return HTTPServerConfig{}, err
	}
	idleTimeout, err := requiredDuration(lookup, httpIdleTimeoutVariable)
	if err != nil {
		return HTTPServerConfig{}, err
	}
	shutdownTimeout, err := requiredDuration(lookup, httpShutdownTimeoutVariable)
	if err != nil {
		return HTTPServerConfig{}, err
	}
	maximumHeaderBytes, err := requiredBoundedInteger(
		lookup,
		httpMaximumHeaderBytesVariable,
		minimumHTTPHeaderBytes,
		maximumHTTPHeaderBytes,
	)
	if err != nil {
		return HTTPServerConfig{}, err
	}

	config := HTTPServerConfig{
		Address:            address,
		ReadHeaderTimeout:  readHeaderTimeout,
		ReadTimeout:        readTimeout,
		WriteTimeout:       writeTimeout,
		IdleTimeout:        idleTimeout,
		ShutdownTimeout:    shutdownTimeout,
		MaximumHeaderBytes: maximumHeaderBytes,
	}
	if config.Validate() != nil {
		return HTTPServerConfig{}, configurationError("HTTP server policy")
	}

	return config, nil
}
