package app

import (
	"context"
	"errors"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/normalizedresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/reconcile"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

type inertArtifactStorage struct{}

func (inertArtifactStorage) ResolveRawManifest(
	context.Context,
	string,
	run.Run,
) (run.Artifact, error) {
	panic("artifact storage called during construction")
}

func (inertArtifactStorage) ResolveNormalizedManifest(
	context.Context,
	string,
	run.Run,
) (run.Artifact, error) {
	panic("artifact storage called during construction")
}

func (inertArtifactStorage) ResolveReportManifest(
	context.Context,
	string,
	run.Run,
) (run.Artifact, error) {
	panic("artifact storage called during construction")
}

func (inertArtifactStorage) Read(context.Context, run.Artifact) ([]byte, error) {
	panic("artifact storage called during construction")
}

type inertArtifactTrust struct{}

func (inertArtifactTrust) ResolveContractsVersion(context.Context, string, run.Run) (string, error) {
	panic("artifact trust called during construction")
}

func (inertArtifactTrust) ApproveRawManifest(
	context.Context,
	string,
	run.Run,
	rawresult.Manifest,
) error {
	panic("artifact trust called during construction")
}

func (inertArtifactTrust) ApproveNormalizedManifest(
	context.Context,
	string,
	run.Run,
	reconcile.AnalysisInput,
	normalizedresult.Manifest,
) error {
	panic("artifact trust called during construction")
}

func (inertArtifactTrust) ResolveReportTrust(
	context.Context,
	string,
	run.Run,
	reconcile.ReportingInput,
) (reconcile.ReportTrust, error) {
	panic("artifact trust called during construction")
}

type inertBaselineRepository struct{}

func (inertBaselineRepository) CreateBaseline(
	context.Context,
	string,
	baseline.Create,
) (baseline.Record, error) {
	panic("baseline repository called during construction")
}

func (inertBaselineRepository) GetBaseline(
	context.Context,
	string,
	string,
	string,
) (baseline.Record, error) {
	panic("baseline repository called during construction")
}

func (inertBaselineRepository) ResolveApprovedBaseline(
	context.Context,
	string,
	baseline.Selection,
) (baseline.Record, bool, error) {
	panic("baseline repository called during construction")
}

func (inertBaselineRepository) TransitionBaseline(
	context.Context,
	string,
	string,
	string,
	int64,
	baseline.Change,
) (baseline.Record, error) {
	panic("baseline repository called during construction")
}

func TestNewArtifactCollectorsComposesAllVerificationBoundaries(t *testing.T) {
	collectors, err := NewArtifactCollectors(
		inertArtifactStorage{},
		inertArtifactTrust{},
		inertBaselineRepository{},
		ArtifactVerificationConfig{ContractsVersion: "0.12.0"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if collectors.Raw == nil || collectors.Normalized == nil || collectors.Report == nil {
		t.Fatalf("collectors = %#v", collectors)
	}
}

func TestNewArtifactCollectorsRejectsInvalidDependencies(t *testing.T) {
	storage := ArtifactStorage(inertArtifactStorage{})
	trust := ArtifactTrust(inertArtifactTrust{})
	baselines := baseline.Repository(inertBaselineRepository{})
	config := ArtifactVerificationConfig{ContractsVersion: "0.12.0"}

	tests := map[string]func() (ArtifactCollectors, error){
		"storage": func() (ArtifactCollectors, error) {
			return NewArtifactCollectors(nil, trust, baselines, config)
		},
		"trust": func() (ArtifactCollectors, error) {
			return NewArtifactCollectors(storage, nil, baselines, config)
		},
		"baselines": func() (ArtifactCollectors, error) {
			return NewArtifactCollectors(storage, trust, nil, config)
		},
		"configuration": func() (ArtifactCollectors, error) {
			return NewArtifactCollectors(storage, trust, baselines, ArtifactVerificationConfig{})
		},
	}
	for name, build := range tests {
		t.Run(name, func(t *testing.T) {
			collectors, err := build()
			if !errors.Is(err, ErrInvalidConfiguration) {
				t.Fatalf("collectors = %#v, error = %v", collectors, err)
			}
		})
	}
}

var _ ArtifactStorage = inertArtifactStorage{}
var _ ArtifactTrust = inertArtifactTrust{}
var _ baseline.Repository = inertBaselineRepository{}
