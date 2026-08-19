package authorityprovider

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func journalTransaction(status LaunchStatus) LaunchTransaction {
	return LaunchTransaction{
		LaunchTransactionID: "launch-txn-journal", AttemptID: "attempt-journal", LaunchNonce: "nonce-journal", Status: status,
	}
}

func TestDurableLaunchJournalHydratesAndChains(t *testing.T) {
	dir := t.TempDir()
	dir = filepath.Join(dir, "journal")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "launch.journal")
	journal, err := OpenDurableLaunchJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	prepared := journalTransaction(LaunchPending)
	if err := journal.Append("prepared", prepared, 0); err != nil {
		t.Fatal(err)
	}
	released := prepared
	released.Status = LaunchReleased
	if err := journal.Append("committed", released, 1); err != nil {
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenDurableLaunchJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	snapshot, err := reopened.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Sequence != 2 || len(snapshot.Transactions) != 1 || snapshot.Transactions[0].Status != LaunchReleased {
		t.Fatalf("hydrated snapshot = %#v", snapshot)
	}
}

func TestDurableLaunchJournalRejectsInvalidHistory(t *testing.T) {
	dir := t.TempDir()
	dir = filepath.Join(dir, "journal")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "launch.journal")
	journal, err := OpenDurableLaunchJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.Append("prepared", journalTransaction(LaunchPending), 0); err != nil {
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	contents = []byte(strings.Replace(string(contents), "launch-txn-journal", "launch-txn-tampered", 1))
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenDurableLaunchJournal(path); err == nil {
		t.Fatal("tampered journal was accepted")
	}
	if err := os.WriteFile(path, []byte(`{"schemaVersion":"marshal.agent-production-authority.launch-journal.v1"`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenDurableLaunchJournal(path); err == nil {
		t.Fatal("partial journal was accepted")
	}
}
