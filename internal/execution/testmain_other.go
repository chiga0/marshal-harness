//go:build !darwin || !arm64

package execution

func inheritedTestEntry() bool { return false }
