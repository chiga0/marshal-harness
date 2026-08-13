package planning

// Run-scoped dependsOn admission (issue #23, phase 1).
//
// Phase-2 scope, intentionally NOT implemented in this file: task-scoped
// dependencies, TaskSpec/PolicySnapshot schema wiring for the admission,
// dependsOn and preconditions fields, and the preconditions executor.

import (
	"errors"
	"os"

	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/port"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

// Run dependency resolution errors are fixed category strings. Every reported
// error additionally names the failing runId and, when one applies, the
// failing field, so callers can locate the violated condition without
// inspecting run state themselves.
const (
	ErrDependencyRunNotFound    = "resolve run dependencies: depended-on run not found"
	ErrDependencyRunUnreadable  = "resolve run dependencies: depended-on run state is unreadable"
	ErrDependencyStateMismatch  = "resolve run dependencies: depended-on run state does not match the required state"
	ErrDependencyBaseMismatch   = "resolve run dependencies: depended-on run baseSha does not match"
	ErrDependencyDigestMismatch = "resolve run dependencies: depended-on run specDigest does not match"
)

// RunDependency declares one run-scoped dependency. The depended-on run must
// exist and its inspected state must be exactly RequiredState; when BaseSHA
// or SpecDigest is non-empty, the inspected run must additionally carry
// exactly that frozen value. An empty BaseSHA or SpecDigest disables the
// corresponding check.
type RunDependency struct {
	RunID         string
	RequiredState domain.State
	BaseSHA       string
	SpecDigest    string
}

// ResolveRunDependencies resolves every dependency in order against the run
// store using the read-only, lease-free Inspect. It is a pure read-only
// resolver: it never acquires a lease, never appends an event, and never
// writes any state. Every failure is fail-closed and reports the fixed error
// category together with the failing runId and field: a missing or
// unreadable run, a state mismatch, or a baseSha or specDigest mismatch. An
// empty dependency list resolves successfully.
func ResolveRunDependencies(store *runstore.Store, deps []RunDependency) error {
	for _, dep := range deps {
		state, err := store.Inspect(dep.RunID)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return port.Permanentf("%s: runId=%s", ErrDependencyRunNotFound, dep.RunID)
			}
			return port.Permanentf("%s: runId=%s", ErrDependencyRunUnreadable, dep.RunID)
		}
		if state.State != dep.RequiredState {
			return port.Permanentf("%s: runId=%s field=state", ErrDependencyStateMismatch, dep.RunID)
		}
		if dep.BaseSHA != "" && state.BaseSHA != dep.BaseSHA {
			return port.Permanentf("%s: runId=%s field=baseSha", ErrDependencyBaseMismatch, dep.RunID)
		}
		if dep.SpecDigest != "" && state.SpecDigest != dep.SpecDigest {
			return port.Permanentf("%s: runId=%s field=specDigest", ErrDependencyDigestMismatch, dep.RunID)
		}
	}
	return nil
}
