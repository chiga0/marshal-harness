//go:build !darwin || !arm64

package productionruntime

func inheritedTestEntry() bool { return false }
