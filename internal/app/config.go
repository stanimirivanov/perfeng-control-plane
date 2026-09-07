// Package app owns executable configuration and dependency composition.
package app

import (
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// ErrInvalidConfiguration classifies missing, malformed or contradictory
// executable configuration without exposing configured values.
var ErrInvalidConfiguration = errors.New("invalid runtime configuration")

// ConfigurationError identifies one invalid variable or configuration component.
type ConfigurationError struct {
	Name string
}

// Error returns only the public configuration name, never its value.
func (err *ConfigurationError) Error() string {
	return "invalid runtime configuration: " + err.Name
}

// Unwrap supports classification with ErrInvalidConfiguration.
func (err *ConfigurationError) Unwrap() error {
	return ErrInvalidConfiguration
}

type lookupEnvironment func(string) (string, bool)

func requiredValue(lookup lookupEnvironment, name string) (string, error) {
	value, exists := lookup(name)
	if !exists || !validRequiredValue(value) {
		return "", configurationError(name)
	}

	return value, nil
}

func optionalValue(lookup lookupEnvironment, name string) string {
	value, _ := lookup(name)

	return value
}

func requiredBoolean(lookup lookupEnvironment, name string) (bool, error) {
	value, err := requiredValue(lookup, name)
	if err != nil {
		return false, err
	}
	switch value {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, configurationError(name)
	}
}

func requiredDuration(lookup lookupEnvironment, name string) (time.Duration, error) {
	value, err := requiredValue(lookup, name)
	if err != nil {
		return 0, err
	}
	duration, err := time.ParseDuration(value)
	if err != nil || !validRuntimeDuration(duration) {
		return 0, configurationError(name)
	}

	return duration, nil
}

func requiredPositiveInteger(lookup lookupEnvironment, name string) (int64, error) {
	value, err := requiredValue(lookup, name)
	if err != nil {
		return 0, err
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 1 {
		return 0, configurationError(name)
	}

	return parsed, nil
}

func requiredBoundedInteger(
	lookup lookupEnvironment,
	name string,
	minimum int,
	maximum int,
) (int, error) {
	value, err := requiredValue(lookup, name)
	if err != nil {
		return 0, err
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < minimum || parsed > maximum {
		return 0, configurationError(name)
	}

	return parsed, nil
}

func configurationError(name string) error {
	return &ConfigurationError{Name: name}
}

func validRequiredValue(value string) bool {
	return value != "" && strings.TrimSpace(value) == value &&
		strings.IndexFunc(value, unicode.IsControl) < 0
}

func validRuntimeDuration(duration time.Duration) bool {
	return duration > 0 && duration <= 10*time.Minute
}
