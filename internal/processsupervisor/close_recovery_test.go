package processsupervisor

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

func committedCloseFixture(t *testing.T) (*Journal, string, PreparedCommand, VerifiedCommandOutcome) {
	t.Helper()
	bootstrap := validBootstrap()
	journal, path := testJournal(t)
	if err := journal.AppendSessionCreated(bootstrap.SessionID, canonical.DigestBytes([]byte(bootstrap.SessionNonce)), bootstrap.Authority, bootstrap.OwnerEpoch, bootstrap.CurrentAuthorityHead); err != nil {
		t.Fatal(err)
	}
	snapshot := journal.Snapshot()
	pre := productionTestAnchor()
	pre.JournalSequence, pre.JournalHead = snapshot.Sequence, snapshot.Head
	payload := ClosePayload{ProcessTerminalFactDigest: digest("1"), AllocationTerminatedDigest: digest("2"), CleanupBindingDigest: digest("3")}
	prepared, err := PrepareCommand(pre, CommandOptions{Command: CommandClose, CommandID: "close-recovery", Sequence: 1, PreviousCommandDigest: CommandGenesisDigest, CurrentAuthorityHead: pre.CurrentAuthorityHead, Deadline: time.Date(2026, 8, 29, 3, 4, 5, 0, time.UTC)}, payload)
	if err != nil {
		t.Fatal(err)
	}
	projection, _, err := projectRequest(prepared.request)
	if err != nil {
		t.Fatal(err)
	}
	base := journalRecord{SchemaVersion: JournalSchema, SessionID: pre.SessionID, SessionNonceDigest: pre.SessionNonceDigest, Authority: pre.Authority, OwnerEpoch: pre.OwnerEpoch, CurrentAuthorityHead: pre.CurrentAuthorityHead}
	if err := journal.AppendIntent(base, projection); err != nil {
		t.Fatal(err)
	}
	report := ProcessReport{
		State: "terminal", ObserverIdentity: "darwin-fixed-process-supervisor-v1", ObservedAt: time.Date(2026, 8, 29, 3, 3, 5, 0, time.UTC).Format(time.RFC3339Nano),
		Process: validBootstrap().Core.Process, RuntimeObjectDigest: digest("a"), WorkingObjectDigest: digest("b"), SourceGateRevision: SourceGateRevisionV1, ExactSetDigest: digest("set"),
	}
	result := MechanicsResult{Disposition: "ok", ReasonCode: "mechanics-closed", ObservationDigest: mustDigestValue(report), Payload: mustCanonical(report)}
	response := responseForResult(t, prepared.request, result)
	if err := journal.AppendReceipt(base, projection, response); err != nil {
		t.Fatal(err)
	}
	post, err := commandPostAnchor(pre, prepared.request, response)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := verifiedCommandOutcome(prepared.request, response, CommandRecoveryEvidence{Reconciliation: ReconciliationReceiptCommitted, Replayed: true, PreCommand: pre, PostCommand: post})
	if err != nil {
		t.Fatal(err)
	}
	return journal, path, prepared, outcome
}

func TestRecoverCommittedCloseReadsExactFinalReceiptWithoutMutation(t *testing.T) {
	journal, path, prepared, want := committedCloseFixture(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := recoverCommittedCloseFromJournal(journal.file, prepared)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !canonicalEqual(got, want) || string(after) != string(before) {
		t.Fatalf("recovery got=%+v want=%+v mutated=%v", got, want, string(after) != string(before))
	}
}

func TestRecoverCommittedCloseFailsClosedOnTornTrailingOrDifferentIntent(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *Journal, string, PreparedCommand) PreparedCommand
	}{
		{name: "partial-tail", mutate: func(t *testing.T, journal *Journal, _ string, prepared PreparedCommand) PreparedCommand {
			if _, err := journal.file.WriteAt([]byte("00000020:{"), mustFileSize(t, journal.file)); err != nil {
				t.Fatal(err)
			}
			return prepared
		}},
		{name: "trailing-garbage", mutate: func(t *testing.T, journal *Journal, _ string, prepared PreparedCommand) PreparedCommand {
			if _, err := journal.file.WriteAt([]byte("G"), mustFileSize(t, journal.file)); err != nil {
				t.Fatal(err)
			}
			return prepared
		}},
		{name: "different-command", mutate: func(t *testing.T, _ *Journal, _ string, prepared PreparedCommand) PreparedCommand {
			payload := ClosePayload{ProcessTerminalFactDigest: digest("1"), AllocationTerminatedDigest: digest("2"), CleanupBindingDigest: digest("4")}
			changed, err := PrepareCommand(prepared.evidence.PreCommand, CommandOptions{Command: CommandClose, CommandID: "close-recovery", Sequence: 1, PreviousCommandDigest: CommandGenesisDigest, CurrentAuthorityHead: prepared.evidence.CurrentAuthorityHead, Deadline: mustDeadline(t, prepared.evidence.Deadline)}, payload)
			if err != nil {
				t.Fatal(err)
			}
			return changed
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			journal, _, prepared, _ := committedCloseFixture(t)
			prepared = test.mutate(t, journal, "", prepared)
			if _, err := recoverCommittedCloseFromJournal(journal.file, prepared); !errors.Is(err, ErrIntervention) && !errors.Is(err, ErrConflict) {
				t.Fatalf("recovery error=%v", err)
			}
		})
	}
}

func TestRecoverCommittedCloseRejectsIntentWithoutReceipt(t *testing.T) {
	bootstrap := validBootstrap()
	journal, _ := testJournal(t)
	if err := journal.AppendSessionCreated(bootstrap.SessionID, canonical.DigestBytes([]byte(bootstrap.SessionNonce)), bootstrap.Authority, bootstrap.OwnerEpoch, bootstrap.CurrentAuthorityHead); err != nil {
		t.Fatal(err)
	}
	snapshot := journal.Snapshot()
	pre := productionTestAnchor()
	pre.JournalSequence, pre.JournalHead = snapshot.Sequence, snapshot.Head
	payload := ClosePayload{ProcessTerminalFactDigest: digest("1"), AllocationTerminatedDigest: digest("2"), CleanupBindingDigest: digest("3")}
	prepared, err := PrepareCommand(pre, CommandOptions{Command: CommandClose, CommandID: "close-pending", Sequence: 1, PreviousCommandDigest: CommandGenesisDigest, CurrentAuthorityHead: pre.CurrentAuthorityHead, Deadline: time.Date(2026, 8, 29, 3, 4, 5, 0, time.UTC)}, payload)
	if err != nil {
		t.Fatal(err)
	}
	projection, _, _ := projectRequest(prepared.request)
	base := journalRecord{SchemaVersion: JournalSchema, SessionID: pre.SessionID, SessionNonceDigest: pre.SessionNonceDigest, Authority: pre.Authority, OwnerEpoch: pre.OwnerEpoch, CurrentAuthorityHead: pre.CurrentAuthorityHead}
	if err := journal.AppendIntent(base, projection); err != nil {
		t.Fatal(err)
	}
	if _, err := recoverCommittedCloseFromJournal(journal.file, prepared); !errors.Is(err, ErrIntervention) {
		t.Fatalf("intent-only recovery error=%v", err)
	}
}

func mustFileSize(t *testing.T, file *os.File) int64 {
	t.Helper()
	stat, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	return stat.Size()
}

func mustDeadline(t *testing.T, value string) time.Time {
	t.Helper()
	deadline, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatal(err)
	}
	return deadline
}

func canonicalEqual(left, right any) bool {
	leftBytes, leftErr := canonicalValue(left)
	rightBytes, rightErr := canonicalValue(right)
	return leftErr == nil && rightErr == nil && string(leftBytes) == string(rightBytes)
}
