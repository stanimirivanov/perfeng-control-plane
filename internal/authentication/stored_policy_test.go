package authentication

import (
	"context"
	"errors"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
	"github.com/stanimirivanov/perfeng-control-plane/internal/serviceaccess"
)

type identityStoreFunc func(context.Context, serviceaccess.Identity, serviceaccess.Identity, string) (bool, error)

func (f identityStoreFunc) IdentityAdmitted(ctx context.Context, a, c serviceaccess.Identity, k string) (bool, error) {
	return f(ctx, a, c, k)
}

func TestStoredIdentityPolicyClassifiesFailuresAndCancellation(t *testing.T) {
	if _, err := NewStoredIdentityPolicy(nil); !errors.Is(err, run.ErrValidation) {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name          string
		allowed       bool
		backend, want error
	}{
		{"admitted", true, nil, nil},
		{"absent", false, nil, ErrUnauthenticated},
		{"denied", true, run.ErrForbidden, ErrUnauthenticated},
		{"unavailable", true, run.ErrUnavailable, run.ErrUnavailable},
		{"private error", true, errors.New("secret backend details"), run.ErrUnavailable},
		{"canceled", true, context.Canceled, context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy, err := NewStoredIdentityPolicy(identityStoreFunc(func(context.Context, serviceaccess.Identity, serviceaccess.Identity, string) (bool, error) {
				return tc.allowed, tc.backend
			}))
			if err != nil {
				t.Fatal(err)
			}
			if err := policy.AuthorizeIdentity(context.Background(), VerifiedIdentity{}); err != tc.want {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	policy, err := NewStoredIdentityPolicy(identityStoreFunc(func(context.Context, serviceaccess.Identity, serviceaccess.Identity, string) (bool, error) {
		cancel()
		return true, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := policy.AuthorizeIdentity(ctx, VerifiedIdentity{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
