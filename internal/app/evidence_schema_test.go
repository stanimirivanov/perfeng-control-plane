package app

import (
	"context"
	"errors"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/reconcile"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

type schemaPlanFixture struct {
	reconcile.ReportPlanResolver
	version string
	err     error
	calls   int
}

func (s *schemaPlanFixture) ResolveContractsVersion(context.Context, string, run.Run) (string, error) {
	s.calls++
	return s.version, s.err
}

type schemaTrustFixture struct {
	ArtifactTrust
	version string
	calls   int
}

func (s *schemaTrustFixture) ResolveContractsVersion(context.Context, string, run.Run) (string, error) {
	s.calls++
	return s.version, nil
}

type schemaIOProbe struct {
	inertArtifactStorage
	calls int
}

func (s *schemaIOProbe) ResolveRawManifest(context.Context, string, run.Run) (run.Artifact, error) {
	s.calls++
	return run.Artifact{}, run.ErrUnavailable
}
func (s *schemaIOProbe) ResolveNormalizedManifest(context.Context, string, run.Run) (run.Artifact, error) {
	s.calls++
	return run.Artifact{}, run.ErrUnavailable
}
func (s *schemaIOProbe) ResolveReportManifest(context.Context, string, run.Run) (run.Artifact, error) {
	s.calls++
	return run.Artifact{}, run.ErrUnavailable
}

func collectSchemaStage(collectors ArtifactCollectors, current run.Run, evidence *mixedEvidence, stage string) error {
	switch stage {
	case "raw":
		current.State = run.StateCollecting
		_, err := collectors.Raw.CollectRawArtifacts(context.Background(), "operator", current)
		return err
	case "normalized":
		current.State = run.StateAnalyzing
		_, err := collectors.Normalized.CollectNormalizedArtifact(context.Background(), "operator", current, reconcile.AnalysisInput{Manifest: evidence.raw, Sources: evidence.sources})
		return err
	default:
		current.State = run.StateReporting
		_, err := collectors.Report.CollectReportArtifact(context.Background(), "operator", current, reconcile.ReportingInput{Candidate: evidence.normalized})
		return err
	}
}

func TestEvidenceCollectorsUseRetainedSchemaBeforeLegacyVersion(t *testing.T) {
	config, entries, currents := mixedBundleFixture(t)
	loaded, err := NewRuntimeRegistry(config, ArtifactVerificationConfig{ContractsVersion: RegistryContractsVersion})
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"raw", "normalized"} {
		for _, fallback := range []bool{false, true} {
			plans := &schemaPlanFixture{version: entries[0].ContractsVersion}
			trust := &schemaTrustFixture{ArtifactTrust: loaded, version: "0.22.0"}
			expectedLegacy := 0
			if fallback {
				plans.err = run.ErrNotFound
				trust.version = entries[0].ContractsVersion
				expectedLegacy = 1
			}
			evidence := newMixedEvidence(t, entries[0], currents[0], "none")
			collectors, err := NewArtifactCollectors(evidence, trust, missingMixedBaseline{}, plans, ArtifactVerificationConfig{ContractsVersion: RegistryContractsVersion})
			if err != nil {
				t.Fatal(err)
			}
			if err = collectSchemaStage(collectors, currents[0], evidence, stage); err != nil || plans.calls != 1 || trust.calls != expectedLegacy {
				t.Fatal("retained schema or explicit ordinary fallback lost", stage, fallback, err, plans.calls, trust.calls)
			}
		}
	}
}

func TestKnownBrokenRerunSchemaStopsEveryCollectorBeforeIO(t *testing.T) {
	_, entries, currents := mixedBundleFixture(t)
	evidence := newMixedEvidence(t, entries[0], currents[0], "none")
	plans, err := composeReportInputs(true, &rerunReportCompositionStore{}, &reportCompositionTrust{})
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"raw", "normalized", "report"} {
		probe := &schemaIOProbe{}
		trust := &schemaTrustFixture{ArtifactTrust: inertArtifactTrust{}, version: entries[0].ContractsVersion}
		collectors, err := NewArtifactCollectors(probe, trust, missingMixedBaseline{}, plans, ArtifactVerificationConfig{ContractsVersion: RegistryContractsVersion})
		if err != nil {
			t.Fatal(err)
		}
		if err = collectSchemaStage(collectors, currents[0], evidence, stage); !errors.Is(err, run.ErrUnavailable) || probe.calls != 0 || trust.calls != 0 {
			t.Fatal("known broken rerun used legacy schema or storage", stage, err, probe.calls, trust.calls)
		}
	}
}

func TestRetainedSchemaCannotBypassUniformCollectorConstraint(t *testing.T) {
	_, entries, currents := mixedBundleFixture(t)
	evidence := newMixedEvidence(t, entries[0], currents[0], "none")
	for stage, expected := range map[string]error{"raw": reconcile.ErrInvalidArtifacts, "normalized": reconcile.ErrAnalysisFailed, "report": reconcile.ErrReportFailed} {
		probe := &schemaIOProbe{}
		plans := &schemaPlanFixture{version: entries[0].ContractsVersion}
		trust := &schemaTrustFixture{ArtifactTrust: inertArtifactTrust{}, version: "0.22.0"}
		collectors, err := NewArtifactCollectors(probe, trust, missingMixedBaseline{}, plans, ArtifactVerificationConfig{ContractsVersion: "0.22.0"})
		if err != nil {
			t.Fatal(err)
		}
		if err = collectSchemaStage(collectors, currents[0], evidence, stage); !errors.Is(err, expected) || probe.calls != 0 || trust.calls != 0 || plans.calls != 1 {
			t.Fatal("retained schema bypassed uniform constraint", stage, err, probe.calls, trust.calls, plans.calls)
		}
	}
}
