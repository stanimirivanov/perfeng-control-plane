package reconcile

import (
	"context"
	"errors"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

type contractsVersionFunc func(context.Context, string, run.Run) (string, error)

func (resolve contractsVersionFunc) ResolveContractsVersion(
	ctx context.Context,
	principal string,
	current run.Run,
) (string, error) {
	return resolve(ctx, principal, current)
}

func fixedContractsVersion(version string) ContractsVersionResolver {
	return contractsVersionFunc(func(context.Context, string, run.Run) (string, error) {
		return version, nil
	})
}

func TestContractsResolutionFailsClosedBeforeEvidenceIO(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, version string
		err           error
		operational   error
	}{
		{name: "empty version"},
		{name: "mutable version", version: "latest"},
		{name: "registry sentinel is not a version", version: "registry"},
		{name: "forbidden", err: run.ErrForbidden},
		{name: "invalid context", err: run.ErrValidation},
		{name: "unavailable", err: run.ErrUnavailable, operational: run.ErrUnavailable},
		{name: "cancellation", err: context.Canceled, operational: context.Canceled},
		{name: "deadline", err: context.DeadlineExceeded, operational: context.DeadlineExceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			contracts := contractsVersionFunc(func(context.Context, string, run.Run) (string, error) {
				return test.version, test.err
			})
			reader := readArtifactFunc(func(context.Context, run.Artifact) ([]byte, error) {
				t.Fatal("evidence read before contracts authorization")
				return nil, nil
			})
			raw, err := NewVerifiedRawCollector(resolveRawManifestFunc(func(context.Context, string, run.Run) (run.Artifact, error) {
				t.Fatal("raw publication resolved before contracts authorization")
				return run.Artifact{}, nil
			}), reader, acceptingRawApprover(), contracts)
			if err != nil {
				t.Fatal(err)
			}
			_, err = raw.CollectRawArtifacts(context.Background(), "operator", boundClaim(run.StateCollecting).Run)
			assertContractsFailure(t, err, ErrInvalidArtifacts, test.operational)

			current, input, _, _ := normalizedCollectorFixture()
			normalized, err := NewVerifiedNormalizedCollector(resolveNormalizedManifestFunc(func(context.Context, string, run.Run) (run.Artifact, error) {
				t.Fatal("normalized publication resolved before contracts authorization")
				return run.Artifact{}, nil
			}), reader, acceptingNormalizedApprover(), contracts)
			if err != nil {
				t.Fatal(err)
			}
			_, err = normalized.CollectNormalizedArtifact(context.Background(), "operator", current, input)
			assertContractsFailure(t, err, ErrAnalysisFailed, test.operational)

			reportRun, reportInput, _, _ := reportCollectorFixture()
			report, err := NewVerifiedReportCollector(resolveReportManifestFunc(func(context.Context, string, run.Run) (run.Artifact, error) {
				t.Fatal("report publication resolved before contracts authorization")
				return run.Artifact{}, nil
			}), reader, acceptingReportApprover(), contracts)
			if err != nil {
				t.Fatal(err)
			}
			_, err = report.CollectReportArtifact(context.Background(), "operator", reportRun, reportInput)
			assertContractsFailure(t, err, ErrReportFailed, test.operational)
		})
	}
}

func assertContractsFailure(t *testing.T, actual, invalid, operational error) {
	t.Helper()
	want := invalid
	if operational != nil {
		want = operational
	}
	if !errors.Is(actual, want) {
		t.Fatalf("error = %v, want %v", actual, want)
	}
}
