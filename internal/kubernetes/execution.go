package kubernetes

import (
	"context"
	"errors"
	"regexp"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

var executionUIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:-]{0,127}$`)

// ErrExecutionConflict means a Run is already bound to a different Kubernetes
// Job identity. The existing binding must not be overwritten.
var ErrExecutionConflict = errors.New("run is bound to a different Kubernetes execution")

// Execution is the durable identity required to observe or stop one Job after
// the dispatching process exits. It deliberately excludes transient create/adopt
// information and mutable Job status.
type Execution struct {
	RunID      string
	Namespace  string
	JobName    string
	UID        types.UID
	SpecSHA256 string
}

// Valid checks the persisted identity's syntax, not its existence in Kubernetes.
func (execution Execution) Valid() bool {
	return contract.ValidID(execution.RunID) &&
		execution.JobName == execution.RunID &&
		len(validation.IsDNS1123Label(execution.Namespace)) == 0 &&
		executionUIDPattern.MatchString(string(execution.UID)) &&
		fingerprintPattern.MatchString(execution.SpecSHA256)
}

// ExecutionStore persists immutable Job identities under reconciliation-lease
// ownership. A missing binding is distinct from a storage or ownership failure.
type ExecutionStore interface {
	// BindExecution preserves the first identity while lease is current. An
	// identical retry is a no-op; a different identity returns ErrExecutionConflict.
	BindExecution(context.Context, run.Lease, Execution) error
	// GetExecution returns the binding and true, or a zero value and false when
	// no binding exists. Lease loss is an error, not an absent binding.
	GetExecution(context.Context, run.Lease) (Execution, bool, error)
}
