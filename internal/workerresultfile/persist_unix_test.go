//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package workerresultfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestPersistOnceConflictingWritersNeverReplaceWinner(t *testing.T) {
	dir := t.TempDir()
	const writers = 16
	start := make(chan struct{})
	type outcome struct {
		payload []byte
		err     error
	}
	outcomes := make(chan outcome, writers)
	var ready sync.WaitGroup
	ready.Add(writers)
	for n := 0; n < writers; n++ {
		go func(n int) {
			payload := []byte(fmt.Sprintf("payload-%02d", n))
			ready.Done()
			<-start
			outcomes <- outcome{payload: payload, err: PersistOnce(dir, payload)}
		}(n)
	}
	ready.Wait()
	close(start)
	var winner []byte
	for n := 0; n < writers; n++ {
		outcome := <-outcomes
		if outcome.err == nil {
			if winner != nil {
				t.Fatal("more than one conflicting payload won creation-once race")
			}
			winner = outcome.payload
		} else if !errors.Is(outcome.err, ErrCreationOnceViolation) {
			t.Fatalf("conflicting writer error = %v", outcome.err)
		}
	}
	installed, err := os.ReadFile(filepath.Join(dir, workerResultName))
	if err != nil {
		t.Fatal(err)
	}
	if string(installed) != string(winner) {
		t.Fatalf("installed payload = %q, winner = %q", installed, winner)
	}
	if err := PersistOnce(dir, winner); err != nil {
		t.Fatalf("byte-identical replay failed: %v", err)
	}
}

func TestPersistOnceRejectsExistingPathReplacementShapes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, dir, external string)
	}{
		{name: "different-regular", setup: func(t *testing.T, dir, _ string) {
			if err := os.WriteFile(filepath.Join(dir, workerResultName), []byte("original"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "symlink", setup: func(t *testing.T, dir, external string) {
			if err := os.Symlink(external, filepath.Join(dir, workerResultName)); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "hardlink", setup: func(t *testing.T, dir, external string) {
			if err := os.Link(external, filepath.Join(dir, workerResultName)); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			external := filepath.Join(t.TempDir(), "external-result")
			if err := os.WriteFile(external, []byte("external"), 0o600); err != nil {
				t.Fatal(err)
			}
			tc.setup(t, dir, external)
			if err := PersistOnce(dir, []byte("replacement")); !errors.Is(err, ErrCreationOnceViolation) {
				t.Fatalf("path replacement shape was not rejected: %v", err)
			}
			externalData, err := os.ReadFile(external)
			if err != nil || string(externalData) != "external" {
				t.Fatalf("external payload changed: %q, %v", externalData, err)
			}
		})
	}
}
