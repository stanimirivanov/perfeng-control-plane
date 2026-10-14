package run

import "testing"

func TestBenchmarkReservationCloneIsolatesCleanupOutcomeAndReceipt(t *testing.T) {
	exit := 1
	original := BenchmarkReservation{Intent: BenchmarkIntent{Plan: []byte("plan")}, Admission: []byte("admission"), Request: []byte("request"),
		Cleanup: &BenchmarkCleanup{Receipt: []byte("receipt"), Next: Change{State: StateTestFailure,
			Failure: &Failure{Code: FailureCodeToolError, Message: "original failure"}, ToolExitCode: &exit}}}
	cloned := original.Clone()
	cloned.Intent.Plan[0] = 'x'
	cloned.Admission[0] = 'x'
	cloned.Request[0] = 'x'
	cloned.Cleanup.Receipt[0] = 'x'
	cloned.Cleanup.Next.Failure.Message = "changed"
	*cloned.Cleanup.Next.ToolExitCode = 2
	if string(original.Intent.Plan) != "plan" || string(original.Admission) != "admission" || string(original.Request) != "request" ||
		string(original.Cleanup.Receipt) != "receipt" || original.Cleanup.Next.Failure.Message != "original failure" || *original.Cleanup.Next.ToolExitCode != 1 {
		t.Fatal("clone mutated retained reservation")
	}
}
