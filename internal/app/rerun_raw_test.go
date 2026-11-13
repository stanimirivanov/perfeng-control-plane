package app

import (
	"errors"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/postgres"
	"github.com/stanimirivanov/perfeng-control-plane/internal/reconcile"
	"github.com/stanimirivanov/perfeng-control-plane/internal/registry"
)

func TestRerunRawCompositionPreservesOtherTrustAndRequiresCapabilities(t *testing.T) {
	original := &registry.ReportPolicyRegistry{}
	disabled, err := composeRerunArtifactTrust(false, nil, original)
	if err != nil || disabled != original {
		t.Fatal("disabled composition changed trust", err)
	}
	if _, err = composeRerunArtifactTrust(true, &startupDatabaseStub{}, original); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatal("missing private readers admitted", err)
	}
	if _, err = composeRerunArtifactTrust(true, &postgres.Repository{}, inertArtifactTrust{}); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatal("missing source-owner review admitted", err)
	}
	enabled, err := composeRerunArtifactTrust(true, &postgres.Repository{}, original)
	if err != nil {
		t.Fatal(err)
	}
	wrapped, ok := enabled.(rerunArtifactTrust)
	if !ok || wrapped.ArtifactTrust != original {
		t.Fatal("normalized/report/schema trust was replaced")
	}
	if _, ok = wrapped.raw.(*reconcile.RerunRawApprover); !ok {
		t.Fatal("known reruns lack raw approval guard")
	}
}
