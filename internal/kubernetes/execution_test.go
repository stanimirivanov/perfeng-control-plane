package kubernetes

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/types"
)

func TestExecutionValidation(t *testing.T) {
	valid := Execution{
		RunID:      testRunID,
		Namespace:  "perf-runs",
		JobName:    testRunID,
		UID:        types.UID("af92431e-e3b0-4e34-9dba-bc44f3c28ca9"),
		SpecSHA256: strings.Repeat("a", 64),
	}
	if !valid.Valid() {
		t.Fatal("valid execution identity rejected")
	}

	for name, mutate := range map[string]func(*Execution){
		"run":         func(value *Execution) { value.RunID = "invalid" },
		"namespace":   func(value *Execution) { value.Namespace = "Invalid" },
		"job-name":    func(value *Execution) { value.JobName = "other" },
		"empty-uid":   func(value *Execution) { value.UID = "" },
		"unsafe-uid":  func(value *Execution) { value.UID = "line\nbreak" },
		"fingerprint": func(value *Execution) { value.SpecSHA256 = "invalid" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			mutate(&candidate)
			if candidate.Valid() {
				t.Fatal("invalid execution identity accepted")
			}
		})
	}
}

func TestDispatchExecutionExcludesCreateResult(t *testing.T) {
	dispatch := Dispatch{
		RunID: testRunID, Namespace: "perf-runs", JobName: testRunID,
		UID: "job-uid", SpecSHA256: strings.Repeat("a", 64), Created: true,
	}
	execution := dispatch.Execution()
	if !execution.Valid() || execution.RunID != dispatch.RunID || execution.UID != dispatch.UID {
		t.Fatal("dispatch did not produce its durable identity")
	}
}
