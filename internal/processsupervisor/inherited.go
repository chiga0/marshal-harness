//go:build darwin && arm64

package processsupervisor

import "context"

// InheritedInvocationKind reports whether this process was started as the
// fixed-image supervisor or launch child by inspecting the inherited
// bootstrap descriptor only. "supervisor" and "child" are the inherited
// kinds; an error means the process was started normally.
func InheritedInvocationKind() (string, error) {
	return inheritedInvocationKind()
}

// RunInheritedMain is the cmd/marshal entry for inherited invocations: it
// runs the supervisor or launch-child loop to completion. It must be called
// before any CLI work and never returns normally to the caller's CLI path.
func RunInheritedMain(ctx context.Context) error {
	return RunInherited(ctx)
}
