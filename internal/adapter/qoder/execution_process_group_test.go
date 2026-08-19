//go:build darwin

package qoder

import (
	"context"
	"testing"
)

// TestVersionProbeFreezesConfiguredProcessGroup exercises a native process
// that exits quickly enough to race a parent-side Getpgid lookup on Darwin.
// Setpgid with the zero-value Pgid already guarantees that the child leads a
// process group whose id is its pid, so a successful Start must not depend on
// observing the child again before it exits.
func TestVersionProbeFreezesConfiguredProcessGroup(t *testing.T) {
	const iterations = 128
	configDir := t.TempDir()
	for iteration := 0; iteration < iterations; iteration++ {
		if _, err := runBoundedVersionProbe(context.Background(), "/usr/bin/true", configDir, []string{"PATH=/usr/bin:/bin"}); err != nil {
			t.Fatalf("iteration %d: %v", iteration, err)
		}
	}
}
