//go:build darwin

package processsupervisor

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDescriptorPathFromFreshGoroutines(t *testing.T) {
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	fd := int(file.Fd())
	// Fresh goroutines exercise stack growth around the descriptor syscall.
	// No subprocess, new binary or fallback pathname lookup is involved.
	for i := 0; i < 128; i++ {
		done := make(chan error, 1)
		go func() {
			got, err := descriptorPath(fd)
			if err != nil || got != path {
				done <- fmt.Errorf("descriptor path %q, want %q: %v", got, path, err)
				return
			}
			done <- nil
		}()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if i%16 == 0 {
			runtime.GC()
		}
	}
	if _, err := descriptorPath(-1); err == nil {
		t.Fatal("invalid descriptor acquired a path")
	}
}
