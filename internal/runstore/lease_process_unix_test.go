//go:build darwin || linux

package runstore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLeaseOwnerProcessAliveTreatsHeldLeaseAsLiveDuringOwnerHandoff(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	lease, err := store.Acquire("run:owner-handoff")
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}

	ownerPath := filepath.Join(root, "runs", "run:owner-handoff", "lease.lock.owner")
	data, err := os.ReadFile(ownerPath)
	if err != nil {
		t.Fatal(err)
	}
	var staleOwner leaseOwnerRecord
	if err := json.Unmarshal(data, &staleOwner); err != nil {
		t.Fatal(err)
	}
	staleOwner.PID = 99999999
	data, err = json.Marshal(staleOwner)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ownerPath, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	// acquireLeaseFile deliberately stops after taking the exact lease.lock
	// descriptor. This is the real Acquire handoff window before the new owner
	// record is atomically installed; the old record still names a dead PID.
	leaseFile, runDirectory, _, _, locked, err := acquireLeaseFile(root, "run:owner-handoff")
	if err != nil {
		t.Fatal(err)
	}
	if !locked {
		t.Fatal("failed to hold lease during owner handoff")
	}
	t.Cleanup(func() {
		_ = releaseLeaseFile(leaseFile)
		_ = runDirectory.Close()
	})

	alive, err := store.LeaseOwnerProcessAlive("run:owner-handoff")
	if err != nil || !alive {
		t.Fatalf("owner handoff probe = %v, %v; held descriptor must win over stale PID", alive, err)
	}
	if err := os.Remove(ownerPath); err != nil {
		t.Fatal(err)
	}
	if held, err := store.LeaseHeld("run:owner-handoff"); err != nil || !held {
		t.Fatalf("lease handoff probe = %v, %v; held descriptor must win over transient owner record", held, err)
	}
	if alive, err := store.LeaseOwnerProcessAlive("run:owner-handoff"); err != nil || !alive {
		t.Fatalf("owner handoff without record = %v, %v; held descriptor must remain live", alive, err)
	}
}

func TestLeaseOwnerProcessAliveRejectsMismatchedOwnerIdentity(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	lease, err := store.Acquire("run:owner-identity")
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}

	ownerPath := filepath.Join(root, "runs", "run:owner-identity", "lease.lock.owner")
	data, err := os.ReadFile(ownerPath)
	if err != nil {
		t.Fatal(err)
	}
	var owner leaseOwnerRecord
	if err := json.Unmarshal(data, &owner); err != nil {
		t.Fatal(err)
	}
	owner.Inode++
	data, err = json.Marshal(owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ownerPath, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	if alive, err := store.LeaseOwnerProcessAlive("run:owner-identity"); err == nil || alive {
		t.Fatalf("mismatched owner identity probe = %v, %v; want fail-closed error", alive, err)
	}
}
