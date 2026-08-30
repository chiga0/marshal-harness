//go:build !darwin || !arm64

package cli

func inheritedTestEntry() bool { return false }
