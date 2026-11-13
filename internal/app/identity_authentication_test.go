package app

import (
	"context"
	"crypto/x509"
	"errors"
	"testing"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/authentication"
	"github.com/stanimirivanov/perfeng-control-plane/internal/serviceaccess"
)

type identityPolicyStoreStub struct{}

func (identityPolicyStoreStub) IdentityAdmitted(context.Context, serviceaccess.Identity, serviceaccess.Identity, string) (bool, error) {
	return false, nil
}

func TestNamedIdentityAuthenticationConstruction(t *testing.T) {
	valid := NamedIdentityConfig{
		Profile:      authentication.IdentityProfile{Issuer: "https://identity.example/realm", Audience: "api", Clients: map[string]string{"delegate": "HUMAN"}},
		JWKSEndpoint: "https://identity.example/realm/keys", Roots: x509.NewCertPool(),
	}
	for _, tc := range []struct {
		name   string
		change func(*NamedIdentityConfig)
	}{
		{"invalid issuer", func(c *NamedIdentityConfig) { c.Profile.Issuer = "http://identity.example" }},
		{"foreign keys", func(c *NamedIdentityConfig) { c.JWKSEndpoint = "https://other.example/keys" }},
		{"missing roots", func(c *NamedIdentityConfig) { c.Roots = nil }},
		{"missing audience", func(c *NamedIdentityConfig) { c.Profile.Audience = "" }},
		{"missing clients", func(c *NamedIdentityConfig) { c.Profile.Clients = nil }},
		{"invalid kind", func(c *NamedIdentityConfig) { c.Profile.Clients = map[string]string{"delegate": "ADMIN"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := valid
			tc.change(&config)
			if got, err := NewNamedIdentityAuthentication(config, identityPolicyStoreStub{}, time.Now); got != nil || !errors.Is(err, ErrInvalidConfiguration) {
				t.Fatalf("construction = %v, %v", got, err)
			}
		})
	}
	if got, err := NewNamedIdentityAuthentication(valid, nil, time.Now); got != nil || !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatal(got, err)
	}
	if got, err := NewNamedIdentityAuthentication(valid, identityPolicyStoreStub{}, nil); got != nil || !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatal(got, err)
	}
	// The host is intentionally not contacted: construction must perform no I/O.
	got, err := NewNamedIdentityAuthentication(valid, identityPolicyStoreStub{}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	got.Stop()
	got.Stop()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if identity, err := got.AuthenticateIdentity(ctx, "credential"); identity != (authentication.VerifiedIdentity{}) || !errors.Is(err, context.Canceled) {
		t.Fatal(identity, err)
	}
}
