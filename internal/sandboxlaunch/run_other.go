//go:build !darwin

package sandboxlaunch

// RunChild refuses the private helper protocol outside Darwin. Linux remains
// truthfully unprofiled until its own process-control contract is implemented.
func RunChild() error { return ErrUnsupported }
