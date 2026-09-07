package app

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestLoadHTTPServerConfigReturnsExplicitLimits(t *testing.T) {
	config, err := loadHTTPServerConfig(mapLookup(validHTTPServerEnvironment()))
	if err != nil {
		t.Fatal(err)
	}
	if want := validHTTPServerConfig(); !reflect.DeepEqual(config, want) {
		t.Fatalf("config = %#v", config)
	}
}

func TestLoadHTTPServerConfigRejectsInvalidEnvironment(t *testing.T) {
	tests := map[string]func(map[string]string){
		"missing address": func(values map[string]string) {
			delete(values, httpAddressVariable)
		},
		"missing port": func(values map[string]string) {
			values[httpAddressVariable] = "localhost"
		},
		"zero port": func(values map[string]string) {
			values[httpAddressVariable] = ":0"
		},
		"space in host": func(values map[string]string) {
			values[httpAddressVariable] = "invalid host:8080"
		},
		"invalid duration": func(values map[string]string) {
			values[httpReadTimeoutVariable] = "soon"
		},
		"zero duration": func(values map[string]string) {
			values[httpIdleTimeoutVariable] = "0s"
		},
		"long duration": func(values map[string]string) {
			values[httpShutdownTimeoutVariable] = "11m"
		},
		"header exceeds read": func(values map[string]string) {
			values[httpReadHeaderTimeoutVariable] = "20s"
			values[httpReadTimeoutVariable] = "10s"
		},
		"small header limit": func(values map[string]string) {
			values[httpMaximumHeaderBytesVariable] = "1023"
		},
		"large header limit": func(values map[string]string) {
			values[httpMaximumHeaderBytesVariable] = "1048577"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			values := validHTTPServerEnvironment()
			mutate(values)

			if _, err := loadHTTPServerConfig(mapLookup(values)); !errors.Is(err, ErrInvalidConfiguration) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestHTTPServerConfigAcceptsListenerAddressForms(t *testing.T) {
	for _, address := range []string{":8080", "127.0.0.1:8080", "localhost:8080", "[::1]:8080"} {
		config := validHTTPServerConfig()
		config.Address = address
		if err := config.Validate(); err != nil {
			t.Fatalf("address %q: %v", address, err)
		}
	}
}

func TestLoadHTTPServerConfigRejectsNilEnvironment(t *testing.T) {
	if _, err := loadHTTPServerConfig(nil); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("error = %v", err)
	}
}

func validHTTPServerEnvironment() map[string]string {
	return map[string]string{
		httpAddressVariable:            ":8080",
		httpReadHeaderTimeoutVariable:  (5 * time.Second).String(),
		httpReadTimeoutVariable:        (15 * time.Second).String(),
		httpWriteTimeoutVariable:       (30 * time.Second).String(),
		httpIdleTimeoutVariable:        (60 * time.Second).String(),
		httpShutdownTimeoutVariable:    (30 * time.Second).String(),
		httpMaximumHeaderBytesVariable: "32768",
	}
}
