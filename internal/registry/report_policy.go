// Package registry resolves reviewed runtime resources from immutable startup data.
package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	batchv1 "k8s.io/api/batch/v1"

	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/kubernetes"
	"github.com/stanimirivanov/perfeng-control-plane/internal/nativeworker"
	"github.com/stanimirivanov/perfeng-control-plane/internal/normalizedresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/policy"
	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/reconcile"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

// ReportPolicyEntry binds approved runtime resources to one execution context.
type ReportPolicyEntry struct {
	Integration        *IntegrationBinding
	CatalogOwnerRoute  *contract.CatalogOwnerRoute
	PolicyBytes        []byte
	TestID             string
	ContractsVersion   string
	Catalogue          run.Reference
	Profile            string
	RawProducer        rawresult.Producer
	NormalizerProducer rawresult.Producer
	Normalization      reconcile.NormalizationSpec
	ReportProducer     rawresult.Producer
	ExecutionTemplate  *batchv1.Job
	Benchmark          *reconcile.BenchmarkPlan
	NativeBaseline     *nativeworker.BaselineTemplate
	Workload           rawresult.Identity
	Environment        baseline.Environment
	Dataset            baseline.Dataset
	CandidateImages    []string
	Principals         []string
}

// ReportPolicyRegistry authorizes Runs, resolves execution templates, approves
// execution evidence and resolves report trust from immutable reviewed entries.
type ReportPolicyRegistry struct {
	catalog *CatalogConfig
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
	integration        *IntegrationBinding
	catalogOwner       *contract.CatalogOwnerRoute
	policyBytes        []byte
	mode               string
	contractsVersion   string
	rawProducer        rawresult.Producer
	normalizerProducer rawresult.Producer
	normalization      reconcile.NormalizationSpec
	reportProducer     rawresult.Producer
	executionTemplate  *batchv1.Job
	benchmark          *reconcile.BenchmarkPlan
	executionTarget    run.ExecutionTarget
	nativeBaseline     *nativeworker.BaselineTemplate
	workload           rawresult.Identity
	baselines          []baseline.Selection
	candidates         map[string]struct{}
}

var (
	_ reconcile.JobResolver                = (*ReportPolicyRegistry)(nil)
	_ reconcile.AnalysisPlanResolver       = (*ReportPolicyRegistry)(nil)
	_ reconcile.NormalizedManifestApprover = (*ReportPolicyRegistry)(nil)
	_ reconcile.RawManifestApprover        = (*ReportPolicyRegistry)(nil)
	_ reconcile.ReportTrustResolver        = (*ReportPolicyRegistry)(nil)
	_ reconcile.ContractsVersionResolver   = (*ReportPolicyRegistry)(nil)
)

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

// ValidateContractsVersion requires every registry entry to use the exact
// contracts version configured for artifact verification.
func (registry *ReportPolicyRegistry) ValidateContractsVersion(version string) error {
	if registry == nil || len(registry.entries) == 0 || !rawresult.ValidContractsVersion(version) {
		return run.ErrValidation
	}
	for _, entry := range registry.entries {
		if entry.contractsVersion != version {
			return run.ErrValidation
		}
	}

	return nil
}

func (registry *ReportPolicyRegistry) add(entry ReportPolicyEntry) error {
	var owner *contract.CatalogOwnerRoute
	if entry.CatalogOwnerRoute != nil {
		if contract.ValidateCatalogResource(ownerResource(*entry.CatalogOwnerRoute)) != nil {
			return run.ErrValidation
		}
		copy := *entry.CatalogOwnerRoute
		owner = &copy
	}
	executionTarget, validExecution := classifyExecution(entry.RawProducer, entry.ExecutionTemplate)
	validBaseline := validNativeBaseline(executionTarget, entry)
	if !validBenchmarkEntry(entry) {
		return run.ErrValidation
	}
	document, err := policy.Parse(entry.PolicyBytes)
	if err != nil || !rawresult.ValidResourceID(entry.TestID) ||
		!rawresult.ValidContractsVersion(entry.ContractsVersion) ||
		!validReference(entry.Catalogue) || !rawresult.ValidResourceID(entry.Profile) ||
		!validExecution || !validBaseline ||
		entry.NormalizerProducer.Validate() != nil ||
		entry.Normalization.Validate() != nil ||
		entry.ReportProducer.Validate() != nil ||
		entry.Workload.Validate() != nil || entry.Environment.Validate() != nil ||
		entry.Dataset.Validate() != nil || len(entry.CandidateImages) == 0 ||
		len(entry.Principals) == 0 {
		return run.ErrValidation
	}
	candidates := make(map[string]struct{}, len(entry.CandidateImages))
	for _, image := range entry.CandidateImages {
		if !rawresult.ValidImage(image) {
			return run.ErrValidation
		}
		if _, exists := candidates[image]; exists {
			return run.ErrValidation
		}
		candidates[image] = struct{}{}
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
	integration, err := prepareIntegration(entry.Integration, run.Request{TestSuite: entry.TestID, Catalogue: entry.Catalogue, Profile: entry.Profile, Environment: environmentReference, Policy: policyReference})
	if err != nil || (integration != nil && owner == nil) {
		return run.ErrValidation
	}
	if integration != nil {
		if _, approved := candidates[integration.Candidate.Image]; !approved {
			return run.ErrValidation
		}
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
			integration:        integration,
			catalogOwner:       owner,
			policyBytes:        append([]byte(nil), entry.PolicyBytes...),
			mode:               document.Spec.Mode,
			contractsVersion:   entry.ContractsVersion,
			rawProducer:        entry.RawProducer.Clone(),
			normalizerProducer: entry.NormalizerProducer.Clone(),
			normalization:      entry.Normalization,
			reportProducer:     entry.ReportProducer.Clone(),
			executionTemplate:  cloneJob(entry.ExecutionTemplate),
			benchmark:          cloneBenchmark(entry.Benchmark),
			executionTarget:    executionTarget,
			nativeBaseline:     cloneNativeBaseline(entry.NativeBaseline),
			workload:           entry.Workload,
			baselines:          cloneSelections(baselines),
			candidates:         cloneSet(candidates),
		}
	}

	return nil
}

// ResolveAnalysisPlan selects the reviewed normalizer and its single exact raw input.
func (registry *ReportPolicyRegistry) ResolveAnalysisPlan(
	ctx context.Context,
	principal string,
	current run.Run,
	input reconcile.AnalysisInput,
) (reconcile.AnalysisPlan, error) {
	if err := ctx.Err(); err != nil {
		return reconcile.AnalysisPlan{}, err
	}
	rawArtifacts := reconcile.RawArtifactSet{Manifest: input.Manifest, Artifacts: input.Sources}
	if registry == nil || principal == "" || current.State != run.StateAnalyzing ||
		current.Request.Validate() != nil || rawArtifacts.Validate(current.ID) != nil {
		return reconcile.AnalysisPlan{}, run.ErrValidation
	}

	entry, exists := registry.lookup(principal, current.Request)
	if !exists {
		return reconcile.AnalysisPlan{}, run.ErrForbidden
	}
	if _, approved := entry.candidates[current.Request.Candidate.Image]; !approved {
		return reconcile.AnalysisPlan{}, run.ErrForbidden
	}

	source, selected := analysisSource(input.Sources, entry.normalization.SourceFormat)
	if !selected {
		return reconcile.AnalysisPlan{}, run.ErrForbidden
	}
	plan := reconcile.AnalysisPlan{
		Source: source, Producer: entry.normalizerProducer.Clone(), Spec: entry.normalization,
	}
	if plan.Validate(current.ID) != nil {
		return reconcile.AnalysisPlan{}, run.ErrForbidden
	}

	return plan, nil
}

// ApproveNormalizedManifest binds normalizer claims to approved raw evidence and Run context.
func (registry *ReportPolicyRegistry) ApproveNormalizedManifest(
	ctx context.Context,
	principal string,
	current run.Run,
	input reconcile.AnalysisInput,
	manifest normalizedresult.Manifest,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	rawArtifacts := reconcile.RawArtifactSet{Manifest: input.Manifest, Artifacts: input.Sources}
	if registry == nil || principal == "" || current.State != run.StateAnalyzing ||
		current.Request.Validate() != nil ||
		rawArtifacts.Validate(current.ID) != nil ||
		manifest.Validate(current.ID, manifest.ContractsVersion) != nil {
		return run.ErrValidation
	}

	entry, exists := registry.lookup(principal, current.Request)
	if !exists {
		return run.ErrForbidden
	}
	source, selected := analysisSource(input.Sources, entry.normalization.SourceFormat)
	if manifest.ContractsVersion != entry.contractsVersion ||
		manifest.TestID != current.Request.TestSuite || manifest.Workload != entry.workload ||
		!manifest.Producer.Equal(entry.normalizerProducer) ||
		!selected || !sameArtifactSet(manifest.SourceArtifacts, []run.Artifact{source}) {
		return run.ErrForbidden
	}

	return nil
}

// ResolveJob returns the approved reusable execution template for an exact Run context.
func (registry *ReportPolicyRegistry) ResolveJob(
	ctx context.Context,
	principal string,
	current run.Run,
) (*batchv1.Job, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if registry == nil || principal == "" ||
		(current.State != run.StateValidating && current.State != run.StateProvisioning &&
			current.State != run.StateCancelling) ||
		current.Request.Validate() != nil {
		return nil, run.ErrValidation
	}

	entry, exists := registry.lookup(principal, current.Request)
	if !exists || entry.executionTemplate == nil {
		return nil, run.ErrForbidden
	}
	if _, approved := entry.candidates[current.Request.Candidate.Image]; !approved {
		return nil, run.ErrForbidden
	}
	if entry.benchmark != nil && current.State != run.StateValidating {
		return nil, run.ErrForbidden
	}

	return entry.executionTemplate.DeepCopy(), nil
}

// ApproveRawManifest binds raw producer claims to the accepted execution context.
func (registry *ReportPolicyRegistry) ApproveRawManifest(
	ctx context.Context,
	principal string,
	current run.Run,
	manifest rawresult.Manifest,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if registry == nil || principal == "" || current.State != run.StateCollecting ||
		current.Request.Validate() != nil ||
		manifest.Validate(current.ID, manifest.ContractsVersion) != nil {
		return run.ErrValidation
	}

	entry, exists := registry.lookup(principal, current.Request)
	if !exists || manifest.ContractsVersion != entry.contractsVersion ||
		manifest.TestID != current.Request.TestSuite || manifest.Workload != entry.workload ||
		!manifest.Producer.Equal(entry.rawProducer) {
		return run.ErrForbidden
	}

	return nil
}

// ApproveRun authorizes one exact request context and candidate image.
func (registry *ReportPolicyRegistry) ApproveRun(
	ctx context.Context,
	principal string,
	request run.Request,
) (run.ExecutionTarget, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if registry == nil || principal == "" || request.Validate() != nil {
		return "", run.ErrValidation
	}

	entry, exists := registry.lookup(principal, request)
	if !exists {
		return "", run.ErrForbidden
	}
	if _, approved := entry.candidates[request.Candidate.Image]; !approved {
		return "", run.ErrForbidden
	}
	if entry.executionTarget == run.ExecutionTargetNativeWindows && entry.nativeBaseline == nil {
		return "", run.ErrForbidden
	}

	return entry.executionTarget, nil
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

	entry, exists := registry.lookup(principal, current.Request)
	if !exists {
		return reconcile.ReportTrust{}, run.ErrForbidden
	}

	return reconcile.ReportTrust{
		PolicyBytes: append([]byte(nil), entry.policyBytes...),
		PolicyMode:  entry.mode, Producer: entry.reportProducer,
		Baselines: cloneSelections(entry.baselines),
	}, nil
}

func (registry *ReportPolicyRegistry) lookup(
	principal string,
	request run.Request,
) (reportPolicyValue, bool) {
	entry, exists := registry.entries[reportPolicyKey{
		principal: principal, testID: request.TestSuite,
		catalogue: request.Catalogue, profile: request.Profile,
		environment: request.Environment, policy: request.Policy,
	}]

	return entry, exists
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

func analysisSource(artifacts []run.Artifact, format string) (run.Artifact, bool) {
	var source run.Artifact
	for _, artifact := range artifacts {
		if artifact.Format != format {
			continue
		}
		if source.ID != "" {
			return run.Artifact{}, false
		}
		source = artifact
	}

	return source, source.ID != ""
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

func cloneSet(values map[string]struct{}) map[string]struct{} {
	clone := make(map[string]struct{}, len(values))
	for value := range values {
		clone[value] = struct{}{}
	}

	return clone
}

func jobUsesImage(template *batchv1.Job, image string) bool {
	if template == nil {
		return false
	}
	for _, container := range template.Spec.Template.Spec.InitContainers {
		if container.Image == image {
			return true
		}
	}
	for _, container := range template.Spec.Template.Spec.Containers {
		if container.Image == image {
			return true
		}
	}

	return false
}

func classifyExecution(
	producer rawresult.Producer,
	template *batchv1.Job,
) (run.ExecutionTarget, bool) {
	image := producer.Image
	if producer.Validate() != nil {
		if producer.ValidateExecution() != nil {
			return "", false
		}
		if producer.Artifact.Kind != "oci-image" {
			return run.ExecutionTargetNativeWindows, template == nil
		}
		image = producer.Artifact.Image
	}

	return run.ExecutionTargetKubernetes,
		kubernetes.ValidateReusableJobTemplate(template) == nil && jobUsesImage(template, image)
}

func validNativeBaseline(target run.ExecutionTarget, entry ReportPolicyEntry) bool {
	if entry.NativeBaseline == nil {
		return true
	}
	return target == run.ExecutionTargetNativeWindows &&
		entry.ContractsVersion == nativeworker.BaselineContractsVersion &&
		entry.RawProducer.Name == "playwright" &&
		entry.RawProducer.Artifact != nil &&
		entry.RawProducer.Artifact.Kind == "source-checkout" &&
		entry.NativeBaseline.Validate() == nil
}

func cloneNativeBaseline(template *nativeworker.BaselineTemplate) *nativeworker.BaselineTemplate {
	if template == nil {
		return nil
	}
	clone := *template

	return &clone
}

func cloneJob(template *batchv1.Job) *batchv1.Job {
	if template == nil {
		return nil
	}

	return template.DeepCopy()
}

func sameArtifactSet(actual, expected []run.Artifact) bool {
	if len(actual) != len(expected) {
		return false
	}
	remaining := make(map[run.Artifact]struct{}, len(expected))
	for _, artifact := range expected {
		remaining[artifact] = struct{}{}
	}
	for _, artifact := range actual {
		if _, exists := remaining[artifact]; !exists {
			return false
		}
		delete(remaining, artifact)
	}

	return len(remaining) == 0
}
