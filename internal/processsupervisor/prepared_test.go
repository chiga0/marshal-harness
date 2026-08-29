package processsupervisor

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

func productionTestControlFiles() SessionControlFiles {
	return SessionControlFiles{
		Nonce:   ControlFileIdentity{Device: 8, Inode: 10, FileType: "regular", UID: 501, GID: 20, Mode: 0o100600, LinkCount: 1},
		Journal: ControlFileIdentity{Device: 8, Inode: 11, FileType: "regular", UID: 501, GID: 20, Mode: 0o100600, LinkCount: 1},
	}
}

func productionTestAnchor() HandshakeAnchor {
	bootstrap := validBootstrap()
	return HandshakeAnchor{
		SessionID: bootstrap.SessionID, SessionNonceDigest: canonical.DigestBytes([]byte(bootstrap.SessionNonce)), Authority: bootstrap.Authority,
		OwnerEpoch: bootstrap.OwnerEpoch, CurrentAuthorityHead: bootstrap.CurrentAuthorityHead,
		CommandSequence: 0, CommandHead: CommandGenesisDigest, JournalSequence: 1, JournalHead: digest("7"),
		UID: bootstrap.Core.UID, GID: bootstrap.Core.GID, FixedBinary: bootstrap.Core.Binary,
		ControlSocket: ControlSocketIdentity{Device: 8, Inode: 9, FileType: "socket", UID: bootstrap.Core.UID, GID: bootstrap.Core.GID, Mode: 0o140600, LinkCount: 1},
		ControlFiles:  productionTestControlFiles(),
	}
}

func TestPreparedCommandFreezesExactSecretSafeProjection(t *testing.T) {
	pre := productionTestAnchor()
	payload := validSpawnPayload()
	prepared, err := PrepareCommand(pre, CommandOptions{
		Command: CommandSpawn, CommandID: "spawn-prepared", Sequence: 1,
		PreviousCommandDigest: CommandGenesisDigest, CurrentAuthorityHead: pre.CurrentAuthorityHead,
		Deadline: time.Date(2026, 8, 29, 3, 4, 5, 0, time.UTC),
	}, payload)
	if err != nil {
		t.Fatal(err)
	}
	evidence := prepared.Evidence()
	if err := evidence.Validate(); err != nil {
		t.Fatal(err)
	}
	raw, err := canonicalValue(evidence)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"secret-argument", "credential-value", "stdin-secret", "/secret/runtime", "/secret/repository", strings.Repeat("0123456789abcdef", 4)} {
		if bytes.Contains(raw, []byte(forbidden)) {
			t.Fatalf("prepared evidence contains secret/path %q", forbidden)
		}
	}

	// Mutating caller-owned input after preparation cannot alter either the
	// private request or its durable evidence.
	payload.Argv[0] = "/changed/runtime"
	payload.Environment[0] = "TOKEN=changed"
	payload.Stdin[0] = 'X'
	if got := prepared.Evidence(); got != evidence {
		t.Fatal("prepared evidence changed with caller-owned payload")
	}
	if rebuilt, err := RebuildPreparedCommand(evidence, validSpawnPayload()); err != nil || rebuilt.Evidence() != evidence {
		t.Fatalf("exact rebuild err=%v evidence=%+v", err, rebuilt.Evidence())
	}
	changed := validSpawnPayload()
	changed.Stdin = []byte("different-secret")
	if _, err := RebuildPreparedCommand(evidence, changed); !errors.Is(err, ErrConflict) && !errors.Is(err, ErrInvalid) {
		t.Fatalf("changed payload rebuild error=%v", err)
	}
}

func TestPreparedCommandRejectsMissingControlIdentityAndPreAnchorDrift(t *testing.T) {
	pre := productionTestAnchor()
	payload := ClosePayload{ProcessTerminalFactDigest: digest("1"), AllocationTerminatedDigest: digest("2"), CleanupBindingDigest: digest("3")}
	options := CommandOptions{Command: CommandClose, CommandID: "close-prepared", Sequence: 1, PreviousCommandDigest: CommandGenesisDigest, CurrentAuthorityHead: pre.CurrentAuthorityHead, Deadline: time.Date(2026, 8, 29, 3, 4, 5, 0, time.UTC)}

	missing := pre
	missing.ControlFiles = SessionControlFiles{}
	if _, err := PrepareCommand(missing, options, payload); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing control identity error=%v", err)
	}
	prepared, err := PrepareCommand(pre, options, payload)
	if err != nil {
		t.Fatal(err)
	}
	drifted := prepared.Evidence()
	drifted.PreCommand.ControlFiles.Journal.Inode++
	if _, err := RebuildPreparedCommand(drifted, payload); !errors.Is(err, ErrConflict) && !errors.Is(err, ErrInvalid) {
		t.Fatalf("drifted pre-anchor error=%v", err)
	}
}

func TestPreparedCommandEvidenceIntegrityRejectsPostCreationMutation(t *testing.T) {
	pre := productionTestAnchor()
	payload := BindAuthorityPayload{SupervisorStartedFactDigest: digest("1"), OwnerEpoch: pre.OwnerEpoch, PreviousAuthorityHead: pre.CurrentAuthorityHead, AuthorityHead: digest("2")}
	prepared, err := PrepareCommand(pre, CommandOptions{Command: CommandBindAuthority, CommandID: "bind-integrity", Sequence: 1, PreviousCommandDigest: CommandGenesisDigest, CurrentAuthorityHead: pre.CurrentAuthorityHead, Deadline: time.Date(2026, 8, 29, 3, 4, 5, 0, time.UTC)}, payload)
	if err != nil {
		t.Fatal(err)
	}

	for name, mutate := range map[string]func(*PreparedCommandEvidence){
		"projection":  func(evidence *PreparedCommandEvidence) { evidence.Projection.AuthorityHead = digest("3") },
		"pre-command": func(evidence *PreparedCommandEvidence) { evidence.PreCommand.ControlFiles.Journal.Inode++ },
		"deadline":    func(evidence *PreparedCommandEvidence) { evidence.Deadline = "2026-08-29T03:04:06Z" },
		"command-id":  func(evidence *PreparedCommandEvidence) { evidence.CommandID = "bind-integrity-forged" },
	} {
		t.Run(name, func(t *testing.T) {
			forged := prepared.Evidence()
			mutate(&forged)
			if err := forged.Validate(); !errors.Is(err, ErrConflict) {
				t.Fatalf("post-creation mutation error=%v", err)
			}
			if _, err := RebuildPreparedCommand(forged, payload); !errors.Is(err, ErrInvalid) {
				t.Fatalf("rebuild accepted post-creation mutation: %v", err)
			}
		})
	}
}
