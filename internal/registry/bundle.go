package registry

import (
	"io"
	"io/fs"
	"strings"

	batchv1 "k8s.io/api/batch/v1"

	"github.com/stanimirivanov/perfeng-control-plane/internal/baseline"
	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/jsondocument"
	"github.com/stanimirivanov/perfeng-control-plane/internal/nativeworker"
	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/reconcile"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

const (
	maximumRegistryBundleBytes = 4 << 20
	maximumJobTemplateBytes    = 1 << 20
	maximumPolicyFileBytes     = 1 << 20
)

type reportPolicyBundle struct {
	PerformanceCatalog *CatalogConfig            `json:"performanceCatalog,omitempty"`
	SchemaVersion      int                       `json:"schemaVersion"`
	Entries            []reportPolicyBundleEntry `json:"entries"`
}

type reportPolicyBundleEntry struct {
	Integration        *IntegrationBinding            `json:"integration,omitempty"`
	CatalogOwnerRoute  *contract.CatalogOwnerRoute    `json:"catalogOwnerRoute,omitempty"`
	Policy             string                         `json:"policy"`
	TestID             string                         `json:"testId"`
	ContractsVersion   string                         `json:"contractsVersion"`
	Catalogue          run.Reference                  `json:"catalogue"`
	Profile            string                         `json:"profile"`
	RawProducer        rawresult.Producer             `json:"rawProducer"`
	NormalizerProducer rawresult.Producer             `json:"normalizerProducer"`
	Normalization      normalizationBundleConfig      `json:"normalization"`
	ReportProducer     rawresult.Producer             `json:"reportProducer"`
	ExecutionTemplate  string                         `json:"executionTemplate,omitempty"`
	NativeBaseline     *nativeworker.BaselineTemplate `json:"nativeBaseline,omitempty"`
	Benchmark          *benchmarkBundleConfig         `json:"benchmark,omitempty"`
	Workload           rawresult.Identity             `json:"workload"`
	Environment        baseline.Environment           `json:"environment"`
	Dataset            baseline.Dataset               `json:"dataset"`
	CandidateImages    []string                       `json:"candidateImages"`
	Principals         []string                       `json:"principals"`
}

type normalizationBundleConfig struct {
	SourceFormat string `json:"sourceFormat"`
	Layout       string `json:"layout"`
}

// LoadReportPolicyRegistry constructs immutable registry state from one strict,
// reviewed bundle manifest and its filesystem-relative policy and Job files.
func LoadReportPolicyRegistry(source fs.FS, manifestPath string) (*ReportPolicyRegistry, error) {
	if source == nil || !validBundlePath(manifestPath) {
		return nil, run.ErrValidation
	}
	data, err := readBundleFile(source, manifestPath, maximumRegistryBundleBytes)
	if err != nil || !jsondocument.Valid(data, maximumRegistryBundleBytes) {
		return nil, run.ErrValidation
	}
	var bundle reportPolicyBundle
	if !jsondocument.Decode(data, &bundle) || bundle.SchemaVersion != 1 || len(bundle.Entries) == 0 {
		return nil, run.ErrValidation
	}

	entries := make([]ReportPolicyEntry, 0, len(bundle.Entries))
	for _, configured := range bundle.Entries {
		entry, err := loadReportPolicyEntry(source, configured)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}

	registry, err := NewReportPolicyRegistry(entries)
	if err != nil {
		return nil, err
	}
	if bundle.PerformanceCatalog != nil {
		if !bundle.PerformanceCatalog.valid() {
			return nil, run.ErrValidation
		}
		copy := *bundle.PerformanceCatalog
		registry.catalog = &copy
	}
	for _, entry := range entries {
		if entry.Integration != nil && registry.catalog == nil {
			return nil, run.ErrValidation
		}
	}
	return registry, nil
}

func loadReportPolicyEntry(
	source fs.FS,
	configured reportPolicyBundleEntry,
) (ReportPolicyEntry, error) {
	policyBytes, err := readBundleFile(source, configured.Policy, maximumPolicyFileBytes)
	if err != nil {
		return ReportPolicyEntry{}, run.ErrValidation
	}
	template, err := loadExecutionTemplate(source, configured.ExecutionTemplate)
	if err != nil {
		return ReportPolicyEntry{}, err
	}
	benchmarkPlan, err := loadBenchmarkPlan(source, configured, template)
	if err != nil {
		return ReportPolicyEntry{}, err
	}

	return ReportPolicyEntry{
		Integration:       configured.Integration,
		CatalogOwnerRoute: configured.CatalogOwnerRoute,
		PolicyBytes:       policyBytes, TestID: configured.TestID,
		ContractsVersion: configured.ContractsVersion,
		Catalogue:        configured.Catalogue, Profile: configured.Profile,
		RawProducer: configured.RawProducer, NormalizerProducer: configured.NormalizerProducer,
		Normalization: reconcile.NormalizationSpec{
			SourceFormat: configured.Normalization.SourceFormat,
			Layout:       configured.Normalization.Layout,
		},
		ReportProducer: configured.ReportProducer, ExecutionTemplate: template,
		NativeBaseline: configured.NativeBaseline,
		Benchmark:      benchmarkPlan,
		Workload:       configured.Workload, Environment: configured.Environment,
		Dataset: configured.Dataset, CandidateImages: configured.CandidateImages,
		Principals: configured.Principals,
	}, nil
}

func loadExecutionTemplate(source fs.FS, name string) (*batchv1.Job, error) {
	if name == "" {
		return nil, nil
	}
	data, err := readBundleFile(source, name, maximumJobTemplateBytes)
	if err != nil || !jsondocument.Valid(data, maximumJobTemplateBytes) {
		return nil, run.ErrValidation
	}
	var template batchv1.Job
	if !jsondocument.Decode(data, &template) {
		return nil, run.ErrValidation
	}

	return &template, nil
}

func readBundleFile(source fs.FS, name string, maximumBytes int64) ([]byte, error) {
	if !validBundlePath(name) || maximumBytes < 1 {
		return nil, run.ErrValidation
	}
	file, err := source.Open(name)
	if err != nil {
		return nil, run.ErrValidation
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maximumBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || int64(len(data)) > maximumBytes {
		return nil, run.ErrValidation
	}

	return data, nil
}

func validBundlePath(name string) bool {
	return name != "." && fs.ValidPath(name) && !strings.ContainsRune(name, '\\')
}
