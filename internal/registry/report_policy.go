// Package registry resolves reviewed runtime resources from immutable startup data.
package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/policy"
	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/reconcile"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

// ReportPolicyEntry binds an approved policy to one exact execution context.
type ReportPolicyEntry struct {
	PolicyBytes []byte
	TestID      string
	Catalogue   run.Reference
	Profile     string
	Producer    rawresult.Producer
	Workload    rawresult.Identity
	Environment baseline.Environment
	Dataset     baseline.Dataset
	Principals  []string
}

// ReportPolicyRegistry resolves report trust from an immutable reviewed entry set.
type ReportPolicyRegistry struct {
	entries map[reportPolicyKey]reportPolicyValue
}

type reportPolicyKey struct {
	principal   string
	testID      string
	catalogue   run.Reference
	profile     string
	environment run.Reference
	policy      run.Reference
}

type reportPolicyValue struct {
	policyBytes []byte
	mode        string
	producer    rawresult.Producer
	baselines   []baseline.Selection
}

var _ reconcile.ReportTrustResolver = (*ReportPolicyRegistry)(nil)

// NewReportPolicyRegistry validates and isolates every approved entry.
func NewReportPolicyRegistry(entries []ReportPolicyEntry) (*ReportPolicyRegistry, error) {
	if len(entries) == 0 {
		return nil, run.ErrValidation
	}

	registry := &ReportPolicyRegistry{
		entries: make(map[reportPolicyKey]reportPolicyValue),
	}
	for _, entry := range entries {
		if err := registry.add(entry); err != nil {
			return nil, err
		}
	}

	return registry, nil
}

func (registry *ReportPolicyRegistry) add(entry ReportPolicyEntry) error {
	document, err := policy.Parse(entry.PolicyBytes)
	if err != nil || !rawresult.ValidResourceID(entry.TestID) || !validReference(entry.Catalogue) ||
		!rawresult.ValidResourceID(entry.Profile) || entry.Producer.Validate() != nil ||
		entry.Workload.Validate() != nil || entry.Environment.Validate() != nil ||
		entry.Dataset.Validate() != nil || len(entry.Principals) == 0 {
		return run.ErrValidation
	}

	digest := sha256.Sum256(entry.PolicyBytes)
	policyReference := run.Reference{
		ID: document.Metadata.Name, Version: document.Metadata.Version,
		SHA256: hex.EncodeToString(digest[:]),
	}
	environmentReference := run.Reference{
		ID: entry.Environment.ID, Version: entry.Environment.Version,
		SHA256: entry.Environment.SHA256,
	}
	baselines := make([]baseline.Selection, 0, len(document.Spec.Rules))
	for _, reference := range document.BaselineReferences() {
		baselines = append(baselines, baseline.Selection{
			ID: reference.BaselineID, Version: reference.Version,
			TestID: entry.TestID, Workload: entry.Workload,
			Environment: entry.Environment, Dataset: cloneDataset(entry.Dataset),
		})
	}

	seenPrincipals := make(map[string]struct{}, len(entry.Principals))
	for _, principal := range entry.Principals {
		if strings.TrimSpace(principal) == "" {
			return run.ErrValidation
		}
		if _, exists := seenPrincipals[principal]; exists {
			return run.ErrValidation
		}
		seenPrincipals[principal] = struct{}{}

		key := reportPolicyKey{
			principal: principal, testID: entry.TestID,
			catalogue: entry.Catalogue, profile: entry.Profile,
			environment: environmentReference, policy: policyReference,
		}
		if _, exists := registry.entries[key]; exists {
			return run.ErrValidation
		}
		registry.entries[key] = reportPolicyValue{
			policyBytes: append([]byte(nil), entry.PolicyBytes...),
			mode:        document.Spec.Mode, producer: entry.Producer,
			baselines: cloneSelections(baselines),
		}
	}

	return nil
}

// ResolveReportTrust returns exact policy, producer and comparison context for a Run.
func (registry *ReportPolicyRegistry) ResolveReportTrust(
	ctx context.Context,
	principal string,
	current run.Run,
	input reconcile.ReportingInput,
) (reconcile.ReportTrust, error) {
	if err := ctx.Err(); err != nil {
		return reconcile.ReportTrust{}, err
	}
	if registry == nil || principal == "" || current.State != run.StateReporting ||
		current.Request.Validate() != nil || !validCandidate(current.ID, input.Candidate) {
		return reconcile.ReportTrust{}, run.ErrValidation
	}

	key := reportPolicyKey{
		principal: principal, testID: current.Request.TestSuite,
		catalogue: current.Request.Catalogue, profile: current.Request.Profile,
		environment: current.Request.Environment, policy: current.Request.Policy,
	}
	entry, exists := registry.entries[key]
	if !exists {
		return reconcile.ReportTrust{}, run.ErrForbidden
	}

	return reconcile.ReportTrust{
		PolicyBytes: append([]byte(nil), entry.policyBytes...),
		PolicyMode:  entry.mode, Producer: entry.producer,
		Baselines: cloneSelections(entry.baselines),
	}, nil
}

func validReference(reference run.Reference) bool {
	return (rawresult.Identity{
		ID: reference.ID, Version: reference.Version, SHA256: reference.SHA256,
	}).Validate() == nil
}

func validCandidate(runID string, artifact run.Artifact) bool {
	return artifact.Validate() == nil && artifact.RunID == runID &&
		artifact.Kind == "normalized" && artifact.MediaType == "application/json" &&
		artifact.Format == "normalized-result/v1"
}

func cloneSelections(selections []baseline.Selection) []baseline.Selection {
	clone := append([]baseline.Selection(nil), selections...)
	for index := range clone {
		clone[index].Dataset = cloneDataset(clone[index].Dataset)
	}

	return clone
}

func cloneDataset(dataset baseline.Dataset) baseline.Dataset {
	if dataset.Seed != nil {
		seed := *dataset.Seed
		dataset.Seed = &seed
	}

	return dataset
}
