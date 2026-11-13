package app

import (
	"errors"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/postgres"
	"github.com/stanimirivanov/perfeng-control-plane/internal/reconcile"
	"github.com/stanimirivanov/perfeng-control-plane/internal/registry"
)

func TestRerunNormalizationCompositionSharesGuardAndPreservesOtherCapabilities(t *testing.T) {
	original := &registry.ReportPolicyRegistry{}
	execution, artifacts, err := composeRerunNormalization(false, nil, original, original)
	if err != nil || execution != original || artifacts != original {
		t.Fatal("disabled composition changed trust", err)
	}
	if _, _, err = composeRerunNormalization(true, &startupDatabaseStub{}, original, original); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatal("missing private evidence readers admitted", err)
	}
	if _, _, err = composeRerunNormalization(true, &postgres.Repository{}, &reportCompositionTrust{}, original); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatal("missing source-owner review admitted", err)
	}
	if _, _, err = composeRerunNormalization(true, &postgres.Repository{}, original, nil); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatal("missing artifact trust admitted", err)
	}
	raw, err := composeRerunArtifactTrust(true, &postgres.Repository{}, original)
	if err != nil {
		t.Fatal(err)
	}
	execution, artifacts, err = composeRerunNormalization(true, &postgres.Repository{}, original, raw)
	if err != nil {
		t.Fatal(err)
	}
	planned, ok := execution.(normalizationExecutionTrust)
	if !ok || planned.ExecutionTrust != original {
		t.Fatal("report execution trust lost")
	}
	approved, ok := artifacts.(normalizationArtifactTrust)
	if !ok || approved.ArtifactTrust != raw {
		t.Fatal("raw/report/schema trust lost")
	}
	first, ok := planned.plans.(*reconcile.RerunNormalizationResolver)
	if !ok {
		t.Fatal("planning guard absent")
	}
	second, ok := approved.approval.(*reconcile.RerunNormalizationResolver)
	if !ok || first != second {
		t.Fatal("planning and approval use different guards")
	}
}
