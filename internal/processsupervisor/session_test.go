package processsupervisor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/launchidentity"
)

type fakeMechanics struct{}

func TestFrozenGenesisDigests(t *testing.T) {
	if CommandGenesisDigest != canonical.DigestBytes([]byte("marshal/process-supervisor-command/v1\x00genesis")) || JournalGenesisDigest != canonical.DigestBytes([]byte("marshal/process-supervisor-journal/v1\x00genesis")) {
		t.Fatal("frozen genesis digest drift")
	}
}

func fakeResult(reason string) MechanicsResult {
	return MechanicsResult{Disposition: "ok", ReasonCode: reason, ObservationDigest: digest("9"), Payload: canonicalEmptyPayload()}
}

func (fakeMechanics) Spawn(context.Context, SpawnPayload) (MechanicsResult, error) {
	return fakeResult("fake-spawn"), nil
}
func (fakeMechanics) Resume(context.Context, ResumePayload) (MechanicsResult, error) {
	return fakeResult("fake-resume"), nil
}
func (fakeMechanics) Inspect(context.Context, CleanupPayload) (MechanicsResult, error) {
	return fakeResult("fake-inspect"), nil
}
func (fakeMechanics) Terminate(context.Context, CleanupPayload) (MechanicsResult, error) {
	return fakeResult("fake-terminate"), nil
}
func (fakeMechanics) Collect(context.Context, CollectPayload) (MechanicsResult, error) {
	return fakeResult("fake-collect"), nil
}
func (fakeMechanics) Close(context.Context, ClosePayload) (MechanicsResult, error) {
	return fakeResult("fake-close"), nil
}

func TestSessionIsPassiveUntilDurableBindAndReplaysExactCommand(t *testing.T) {
	now := time.Date(2026, 8, 29, 1, 2, 3, 0, time.UTC)
	bootstrap := validBootstrap()
	journal, path := testJournal(t)
	session, err := NewSession(bootstrap, journal, fakeMechanics{}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	spawn := validSpawnPayload()
	preBind := commandRequest(t, bootstrap.SessionID, CommandSpawn, "spawn-before-bind", 1, CommandGenesisDigest, bootstrap.CurrentAuthorityHead, now.Add(time.Minute), spawn)
	if response := session.Handle(mustCanonical(preBind)); response.Status != "rejected" {
		t.Fatal("unbound session admitted spawn")
	}
	bind := BindAuthorityPayload{SupervisorStartedFactDigest: digest("c"), OwnerEpoch: bootstrap.OwnerEpoch, PreviousAuthorityHead: bootstrap.CurrentAuthorityHead, AuthorityHead: digest("b")}
	request := commandRequest(t, bootstrap.SessionID, CommandBindAuthority, "bind-1", 1, CommandGenesisDigest, bootstrap.CurrentAuthorityHead, now.Add(20*time.Second), bind)
	response := session.Handle(mustCanonical(request))
	if response.Status != "ok" || session.State() != "bound" {
		t.Fatalf("bind response=%+v state=%s", response, session.State())
	}
	before := journal.Snapshot().Sequence
	replayed := session.Handle(mustCanonical(request))
	replayedBytes, _ := canonicalValue(replayed)
	responseBytes, _ := canonicalValue(response)
	if string(replayedBytes) != string(responseBytes) || journal.Snapshot().Sequence != before {
		t.Fatal("exact replay did not return stored receipt without append")
	}
	changed := request
	changed.Payload = mustCanonical(BindAuthorityPayload{SupervisorStartedFactDigest: digest("d"), OwnerEpoch: bootstrap.OwnerEpoch, PreviousAuthorityHead: bootstrap.CurrentAuthorityHead, AuthorityHead: digest("b")})
	changed.RequestDigest, _ = digestValue(requestDigestInput{ProtocolRevision: changed.ProtocolRevision, SessionID: changed.SessionID, Command: changed.Command, CommandID: changed.CommandID, Sequence: changed.Sequence, PreviousCommandDigest: changed.PreviousCommandDigest, CurrentAuthorityHead: changed.CurrentAuthorityHead, Deadline: changed.Deadline, Payload: changed.Payload})
	if got := session.Handle(mustCanonical(changed)); got.Status != "rejected" || got.ReasonCode != ErrConflict.ReasonCode {
		t.Fatalf("conflicting replay = %+v", got)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), bootstrap.SessionNonce) {
		t.Fatal("raw nonce entered journal")
	}
}

func TestSessionAdvancesAuthenticatedAuthorityAnchorAndRejectsSupervisorRestart(t *testing.T) {
	now := time.Date(2026, 8, 29, 1, 2, 3, 0, time.UTC)
	bootstrap := validBootstrap()
	journal, _ := testJournal(t)
	session, err := NewSession(bootstrap, journal, fakeMechanics{}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	bind := BindAuthorityPayload{SupervisorStartedFactDigest: digest("c"), OwnerEpoch: bootstrap.OwnerEpoch, PreviousAuthorityHead: bootstrap.CurrentAuthorityHead, AuthorityHead: digest("b")}
	bindRequest := commandRequest(t, bootstrap.SessionID, CommandBindAuthority, "bind-1", 1, CommandGenesisDigest, bootstrap.CurrentAuthorityHead, now.Add(20*time.Second), bind)
	bindResponse := session.Handle(mustCanonical(bindRequest))
	spawn := validSpawnPayload()
	spawnRequest := commandRequest(t, bootstrap.SessionID, CommandSpawn, "spawn-1", 2, bindResponse.CommandHead, bind.AuthorityHead, now.Add(time.Minute), spawn)
	spawnResponse := session.Handle(mustCanonical(spawnRequest))
	if spawnResponse.Status != "ok" {
		t.Fatal("spawn rejected")
	}
	processStartedHead := digest("d")
	resume := ResumePayload{ProcessStartedFactDigest: digest("e")}
	resumeRequest := commandRequest(t, bootstrap.SessionID, CommandResume, "resume-1", 3, spawnResponse.CommandHead, processStartedHead, now.Add(20*time.Second), resume)
	if response := session.Handle(mustCanonical(resumeRequest)); response.Status != "ok" || session.authorityHead != processStartedHead {
		t.Fatalf("authority advance response=%+v head=%s", response, session.authorityHead)
	}
	if _, err := NewSession(bootstrap, journal, fakeMechanics{}, func() time.Time { return now }); !errors.Is(err, ErrIntervention) {
		t.Fatalf("supervisor restart error=%v", err)
	}
}

func TestReconnectRequiresAuthorityHeadAdvanceAndOwnerProof(t *testing.T) {
	bootstrap := validBootstrap()
	journal, _ := testJournal(t)
	session, err := NewSession(bootstrap, journal, fakeMechanics{}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	commandSequence, commandHead, journalSequence, journalHead := session.Snapshot()
	request := ReconnectRequest{SchemaVersion: ReconnectSchema, ProtocolRevision: ProtocolRevision, SessionID: bootstrap.SessionID, SessionNonce: bootstrap.SessionNonce, PreviousOwnerEpoch: bootstrap.OwnerEpoch, OwnerEpoch: bootstrap.OwnerEpoch + 1, PreviousAuthorityHead: bootstrap.CurrentAuthorityHead, CurrentAuthorityHead: bootstrap.CurrentAuthorityHead, ControlOwnerAcquired: digest("7"), Core: bootstrap.Core, LastOwnerEpoch: bootstrap.OwnerEpoch, LastAuthorityHead: bootstrap.CurrentAuthorityHead, LastCommandSequence: commandSequence, LastCommandHead: commandHead, LastJournalSequence: journalSequence, LastJournalHead: journalHead}
	if _, err := session.Reconnect(request, bootstrap.Core); !errors.Is(err, ErrConflict) {
		t.Fatalf("same-head owner epoch advance error=%v", err)
	}
	request.CurrentAuthorityHead = digest("8")
	request.ControlOwnerAcquired = ""
	if _, err := session.Reconnect(request, bootstrap.Core); !errors.Is(err, ErrConflict) {
		t.Fatalf("missing owner-acquired proof error=%v", err)
	}
	request.ControlOwnerAcquired = digest("7")
	if _, err := session.Reconnect(request, bootstrap.Core); err != nil {
		t.Fatalf("valid owner advance error=%v", err)
	}
}

func TestReconnectPendingStateIsClosedBeforeOwnerAdvance(t *testing.T) {
	for _, state := range []ReconciliationState{ReconciliationUnchanged, ReconciliationIntentPending, ReconciliationReceiptCommitted} {
		t.Run(string(state), func(t *testing.T) {
			now := time.Date(2026, 8, 29, 1, 2, 3, 0, time.UTC)
			bootstrap := validBootstrap()
			journal, _ := testJournal(t)
			session, err := NewSession(bootstrap, journal, fakeMechanics{}, func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			commandSequence, commandHead, journalSequence, journalHead := session.Snapshot()
			pending := commandRequest(t, bootstrap.SessionID, CommandAbortUnbound, "abort-reconnect", 1, commandHead, bootstrap.CurrentAuthorityHead, now.Add(20*time.Second), AbortUnboundPayload{OwnerEpoch: bootstrap.OwnerEpoch, PreviousAuthorityHead: bootstrap.CurrentAuthorityHead, AuthorityAbsenceProofDigest: digest("7")})
			projection, _, err := projectRequest(pending)
			if err != nil {
				t.Fatal(err)
			}
			switch state {
			case ReconciliationIntentPending:
				if err := journal.AppendIntent(session.journalBase(), projection); err != nil {
					t.Fatal(err)
				}
			case ReconciliationReceiptCommitted:
				if response := session.Handle(mustCanonical(pending)); response.Status != "ok" {
					t.Fatalf("pending command response=%+v", response)
				}
			}
			request := ReconnectRequest{
				SchemaVersion: ReconnectSchema, ProtocolRevision: ProtocolRevision, SessionID: bootstrap.SessionID, SessionNonce: bootstrap.SessionNonce,
				PreviousOwnerEpoch: bootstrap.OwnerEpoch, OwnerEpoch: bootstrap.OwnerEpoch + 1, PreviousAuthorityHead: session.authorityHead, CurrentAuthorityHead: digest("8"), ControlOwnerAcquired: digest("9"), Core: bootstrap.Core,
				LastOwnerEpoch: bootstrap.OwnerEpoch, LastAuthorityHead: bootstrap.CurrentAuthorityHead,
				LastCommandSequence: commandSequence, LastCommandHead: commandHead, LastJournalSequence: journalSequence, LastJournalHead: journalHead, PendingRequest: &pending,
			}
			resolution, err := session.Reconnect(request, bootstrap.Core)
			if err != nil || resolution.State != state || session.ownerEpoch != request.OwnerEpoch {
				t.Fatalf("resolution=%+v err=%v ownerEpoch=%d", resolution, err, session.ownerEpoch)
			}
			wantResponse := state != ReconciliationIntentPending
			if (resolution.Response != nil) != wantResponse {
				t.Fatalf("state=%s response=%+v", state, resolution.Response)
			}
		})
	}

	now := time.Date(2026, 8, 29, 1, 2, 3, 0, time.UTC)
	bootstrap := validBootstrap()
	journal, _ := testJournal(t)
	session, err := NewSession(bootstrap, journal, fakeMechanics{}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	commandSequence, commandHead, journalSequence, journalHead := session.Snapshot()
	original := commandRequest(t, bootstrap.SessionID, CommandAbortUnbound, "abort-conflict", 1, commandHead, bootstrap.CurrentAuthorityHead, now.Add(20*time.Second), AbortUnboundPayload{OwnerEpoch: bootstrap.OwnerEpoch, PreviousAuthorityHead: bootstrap.CurrentAuthorityHead, AuthorityAbsenceProofDigest: digest("4")})
	if response := session.Handle(mustCanonical(original)); response.Status != "ok" {
		t.Fatal("fixture command rejected")
	}
	changed := commandRequest(t, bootstrap.SessionID, CommandAbortUnbound, "abort-conflict", 1, commandHead, bootstrap.CurrentAuthorityHead, now.Add(20*time.Second), AbortUnboundPayload{OwnerEpoch: bootstrap.OwnerEpoch, PreviousAuthorityHead: bootstrap.CurrentAuthorityHead, AuthorityAbsenceProofDigest: digest("5")})
	request := ReconnectRequest{
		SchemaVersion: ReconnectSchema, ProtocolRevision: ProtocolRevision, SessionID: bootstrap.SessionID, SessionNonce: bootstrap.SessionNonce,
		PreviousOwnerEpoch: bootstrap.OwnerEpoch, OwnerEpoch: bootstrap.OwnerEpoch + 1, PreviousAuthorityHead: session.authorityHead, CurrentAuthorityHead: digest("8"), ControlOwnerAcquired: digest("9"), Core: bootstrap.Core,
		LastOwnerEpoch: bootstrap.OwnerEpoch, LastAuthorityHead: bootstrap.CurrentAuthorityHead,
		LastCommandSequence: commandSequence, LastCommandHead: commandHead, LastJournalSequence: journalSequence, LastJournalHead: journalHead, PendingRequest: &changed,
	}
	if _, err := session.Reconnect(request, bootstrap.Core); !errors.Is(err, ErrConflict) || session.ownerEpoch != bootstrap.OwnerEpoch {
		t.Fatalf("different digest err=%v ownerEpoch=%d", err, session.ownerEpoch)
	}
}

func TestReconnectRecoversAfterInstalledOwnerHandshakeIsLost(t *testing.T) {
	for _, withPendingReceipt := range []bool{false, true} {
		name := "no-pending"
		if withPendingReceipt {
			name = "pending-receipt"
		}
		t.Run(name, func(t *testing.T) {
			now := time.Date(2026, 8, 29, 2, 3, 4, 0, time.UTC)
			bootstrap := validBootstrap()
			journal, _ := testJournal(t)
			session, err := NewSession(bootstrap, journal, fakeMechanics{}, func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			bind := BindAuthorityPayload{SupervisorStartedFactDigest: digest("c"), OwnerEpoch: bootstrap.OwnerEpoch, PreviousAuthorityHead: bootstrap.CurrentAuthorityHead, AuthorityHead: digest("b")}
			bindRequest := commandRequest(t, bootstrap.SessionID, CommandBindAuthority, "bind-lost-reconnect", 1, CommandGenesisDigest, bootstrap.CurrentAuthorityHead, now.Add(20*time.Second), bind)
			if response := session.Handle(mustCanonical(bindRequest)); response.Status != "ok" {
				t.Fatalf("bind response=%+v", response)
			}
			lastCommandSequence, lastCommandHead, lastJournalSequence, lastJournalHead := session.Snapshot()
			lastAuthorityHead := session.authorityHead
			var pending *Request
			if withPendingReceipt {
				value := commandRequest(t, bootstrap.SessionID, CommandResume, "resume-lost-reconnect", lastCommandSequence+1, lastCommandHead, digest("d"), now.Add(20*time.Second), ResumePayload{ProcessStartedFactDigest: digest("e")})
				if response := session.Handle(mustCanonical(value)); response.Status != "ok" {
					t.Fatalf("pending receipt response=%+v", response)
				}
				pending = &value
			}

			// E1 is installed by the supervisor, but its handshake is discarded.
			first := ReconnectRequest{
				SchemaVersion: ReconnectSchema, ProtocolRevision: ProtocolRevision, SessionID: bootstrap.SessionID, SessionNonce: bootstrap.SessionNonce,
				PreviousOwnerEpoch: bootstrap.OwnerEpoch, OwnerEpoch: bootstrap.OwnerEpoch + 3, PreviousAuthorityHead: session.authorityHead, CurrentAuthorityHead: digest("7"), ControlOwnerAcquired: digest("8"), Core: bootstrap.Core,
				LastOwnerEpoch: bootstrap.OwnerEpoch, LastAuthorityHead: lastAuthorityHead,
				LastCommandSequence: lastCommandSequence, LastCommandHead: lastCommandHead, LastJournalSequence: lastJournalSequence, LastJournalHead: lastJournalHead, PendingRequest: pending,
			}
			firstResolution, err := session.Reconnect(first, bootstrap.Core)
			if err != nil || firstResolution.State == ReconciliationIntentPending || session.ownerEpoch != first.OwnerEpoch || session.authorityHead != first.CurrentAuthorityHead {
				t.Fatalf("first resolution=%+v err=%v owner=%d authority=%s", firstResolution, err, session.ownerEpoch, session.authorityHead)
			}

			second := first
			second.PreviousOwnerEpoch = first.OwnerEpoch
			second.OwnerEpoch = first.OwnerEpoch + 5
			second.PreviousAuthorityHead = first.CurrentAuthorityHead
			second.CurrentAuthorityHead = digest("9")
			second.ControlOwnerAcquired = digest("f")
			// The client still authenticates journal base E0, not installed fence E1.
			second.LastOwnerEpoch = bootstrap.OwnerEpoch

			for hostileName, mutate := range map[string]func(*ReconnectRequest){
				"stale previous fence":   func(value *ReconnectRequest) { value.PreviousOwnerEpoch = bootstrap.OwnerEpoch },
				"wrong previous head":    func(value *ReconnectRequest) { value.PreviousAuthorityHead = digest("d") },
				"replayed owner epoch":   func(value *ReconnectRequest) { value.OwnerEpoch = value.PreviousOwnerEpoch },
				"future historical base": func(value *ReconnectRequest) { value.LastOwnerEpoch = value.PreviousOwnerEpoch + 1 },
				"wrong historical A0":    func(value *ReconnectRequest) { value.LastAuthorityHead = digest("e") },
			} {
				t.Run(hostileName, func(t *testing.T) {
					candidate := second
					mutate(&candidate)
					if _, err := session.Reconnect(candidate, bootstrap.Core); !errors.Is(err, ErrConflict) || session.ownerEpoch != first.OwnerEpoch {
						t.Fatalf("hostile reconnect err=%v owner=%d", err, session.ownerEpoch)
					}
				})
			}

			resolution, err := session.Reconnect(second, bootstrap.Core)
			wantState := ReconciliationUnchanged
			if withPendingReceipt {
				wantState = ReconciliationReceiptCommitted
			}
			if err != nil || resolution.State != wantState || (resolution.Response != nil) != withPendingReceipt || session.ownerEpoch != second.OwnerEpoch || session.authorityHead != second.CurrentAuthorityHead {
				t.Fatalf("second resolution=%+v err=%v owner=%d authority=%s", resolution, err, session.ownerEpoch, session.authorityHead)
			}
		})
	}
}

func TestReconnectSeparatesA0JournalBaseFromAtRequestProjection(t *testing.T) {
	for _, state := range []ReconciliationState{ReconciliationUnchanged, ReconciliationIntentPending, ReconciliationReceiptCommitted} {
		t.Run(string(state), func(t *testing.T) {
			now := time.Date(2026, 8, 29, 1, 2, 3, 0, time.UTC)
			bootstrap := validBootstrap()
			journal, _ := testJournal(t)
			session, err := NewSession(bootstrap, journal, fakeMechanics{}, func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			bind := BindAuthorityPayload{SupervisorStartedFactDigest: digest("c"), OwnerEpoch: bootstrap.OwnerEpoch, PreviousAuthorityHead: bootstrap.CurrentAuthorityHead, AuthorityHead: digest("b")}
			bindRequest := commandRequest(t, bootstrap.SessionID, CommandBindAuthority, "bind-a0", 1, CommandGenesisDigest, bootstrap.CurrentAuthorityHead, now.Add(20*time.Second), bind)
			if response := session.Handle(mustCanonical(bindRequest)); response.Status != "ok" {
				t.Fatalf("bind response=%+v", response)
			}
			commandSequence, commandHead, journalSequence, journalHead := session.Snapshot()
			a0, at := session.authorityHead, digest("d")
			pending := commandRequest(t, bootstrap.SessionID, CommandResume, "resume-a0-at", commandSequence+1, commandHead, at, now.Add(20*time.Second), ResumePayload{ProcessStartedFactDigest: digest("e")})
			projection, _, err := projectRequest(pending)
			if err != nil {
				t.Fatal(err)
			}
			switch state {
			case ReconciliationIntentPending:
				if err := journal.AppendIntent(session.journalBase(), projection); err != nil {
					t.Fatal(err)
				}
			case ReconciliationReceiptCommitted:
				if response := session.Handle(mustCanonical(pending)); response.Status != "ok" {
					t.Fatalf("resume response=%+v", response)
				}
			}
			previousAuthorityHead := a0
			if state == ReconciliationReceiptCommitted {
				previousAuthorityHead = at
			}
			if session.authorityHead != previousAuthorityHead {
				t.Fatalf("fixture current authority=%s want=%s", session.authorityHead, previousAuthorityHead)
			}
			request := ReconnectRequest{
				SchemaVersion: ReconnectSchema, ProtocolRevision: ProtocolRevision, SessionID: bootstrap.SessionID, SessionNonce: bootstrap.SessionNonce,
				PreviousOwnerEpoch: bootstrap.OwnerEpoch, OwnerEpoch: bootstrap.OwnerEpoch + 1, PreviousAuthorityHead: previousAuthorityHead, CurrentAuthorityHead: digest("8"), ControlOwnerAcquired: digest("9"), Core: bootstrap.Core,
				LastOwnerEpoch: bootstrap.OwnerEpoch, LastAuthorityHead: a0, LastCommandSequence: commandSequence, LastCommandHead: commandHead, LastJournalSequence: journalSequence, LastJournalHead: journalHead, PendingRequest: &pending,
			}
			resolution, err := session.Reconnect(request, bootstrap.Core)
			if err != nil || resolution.State != state || session.ownerEpoch != request.OwnerEpoch || session.authorityHead != request.CurrentAuthorityHead {
				t.Fatalf("resolution=%+v err=%v owner=%d authority=%s", resolution, err, session.ownerEpoch, session.authorityHead)
			}
			wantReplay := state != ReconciliationIntentPending
			if (resolution.Response != nil) != wantReplay {
				t.Fatalf("state=%s replay=%+v", state, resolution.Response)
			}
			snapshot := journal.Snapshot()
			if state == ReconciliationIntentPending {
				if snapshot.pendingAuthorityHead != a0 || snapshot.pending == nil || snapshot.pending.CurrentAuthorityHead != at || session.state != sessionIntervention {
					t.Fatalf("intent base=%s projection=%+v sessionState=%s", snapshot.pendingAuthorityHead, snapshot.pending, session.state)
				}
			} else {
				stored, ok := snapshot.commands[pending.CommandID]
				if !ok || stored.AuthorityHead != a0 || stored.Projection.CurrentAuthorityHead != at {
					t.Fatalf("receipt stored=%+v ok=%v", stored, ok)
				}
			}
		})
	}
}

func TestHandshakeBindsFrozenControlSocketIdentity(t *testing.T) {
	bootstrap := validBootstrap()
	socket := ControlSocketIdentity{Device: 9, Inode: 10, FileType: "socket", UID: 501, GID: 20, Mode: 0o140600, LinkCount: 1}
	process := ProcessIdentity{PID: 200, BirthSeconds: 2, BirthMicroseconds: 3, SessionID: 99, ProcessGroupID: 99}
	binary := bootstrap.Core.Binary
	response := HandshakeResponse{SchemaVersion: HandshakeSchema, ProtocolRevision: ProtocolRevision, Status: "ok", ReasonCode: "process-supervisor-ready", SessionID: bootstrap.SessionID, SessionNonceDigest: canonical.DigestBytes([]byte(bootstrap.SessionNonce)), OwnerEpoch: 1, CurrentAuthorityHead: bootstrap.CurrentAuthorityHead, CommandHead: CommandGenesisDigest, JournalSequence: 1, JournalHead: digest("8"), ObserverIdentity: "darwin-fixed-process-supervisor-v1", ObservedAt: time.Date(2026, 8, 29, 1, 2, 3, 0, time.UTC).Format(time.RFC3339Nano), SupervisorProcess: process, SupervisorBinary: binary, ControlSocket: socket}
	anchor := HandshakeAnchor{SessionID: response.SessionID, SessionNonceDigest: response.SessionNonceDigest, Authority: bootstrap.Authority, OwnerEpoch: response.OwnerEpoch, CurrentAuthorityHead: response.CurrentAuthorityHead, CommandSequence: response.CommandSequence, CommandHead: response.CommandHead, JournalSequence: response.JournalSequence, JournalHead: response.JournalHead, UID: 501, GID: 20, FixedBinary: binary, ControlSocket: socket}
	observed := CoreIdentity{UID: 501, GID: 20, Process: process, Binary: binary}
	if err := ValidateHandshakeBinding(response, anchor, observed); err != nil {
		t.Fatalf("valid handshake binding error=%v", err)
	}
	response.ControlSocket.Inode++
	if err := ValidateHandshakeBinding(response, anchor, observed); !errors.Is(err, ErrConflict) {
		t.Fatalf("socket ABA error=%v", err)
	}
}

func TestJournalNeverStoresRawSpawnSecrets(t *testing.T) {
	now := time.Date(2026, 8, 29, 1, 2, 3, 0, time.UTC)
	bootstrap := validBootstrap()
	journal, path := testJournal(t)
	session, err := NewSession(bootstrap, journal, fakeMechanics{}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	bind := BindAuthorityPayload{SupervisorStartedFactDigest: digest("c"), OwnerEpoch: bootstrap.OwnerEpoch, PreviousAuthorityHead: bootstrap.CurrentAuthorityHead, AuthorityHead: digest("b")}
	bindRequest := commandRequest(t, bootstrap.SessionID, CommandBindAuthority, "bind-1", 1, CommandGenesisDigest, bootstrap.CurrentAuthorityHead, now.Add(20*time.Second), bind)
	bindResponse := session.Handle(mustCanonical(bindRequest))
	if bindResponse.Status != "ok" {
		t.Fatal("bind rejected")
	}
	spawn := validSpawnPayload()
	spawnRequest := commandRequest(t, bootstrap.SessionID, CommandSpawn, "spawn-1", 2, bindResponse.CommandHead, bind.AuthorityHead, now.Add(time.Minute), spawn)
	if response := session.Handle(mustCanonical(spawnRequest)); response.Status != "ok" {
		t.Fatalf("spawn rejected: %+v", response)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"secret-argument", "credential-value", "stdin-secret", "/secret/runtime", "/secret/repository"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("journal contains raw secret/path %q", forbidden)
		}
	}
}

func TestSpawnRejectsClosureDigestMismatchBeforeMechanics(t *testing.T) {
	payload := validSpawnPayload()
	payload.AgentLaunchSpecDigest = digest("f")
	if err := validateSpawnPayload(payload); !errors.Is(err, ErrInvalid) {
		t.Fatalf("closure digest mismatch error=%v", err)
	}
	payload = validSpawnPayload()
	payload.ClosureProfileID = launchidentity.Pi0843DarwinARM64Profile
	if err := validateSpawnPayload(payload); !errors.Is(err, ErrInvalid) {
		t.Fatalf("closure profile mismatch error=%v", err)
	}
}

func TestJournalRecoveryTruncatesOnlyPartialFinalFrame(t *testing.T) {
	bootstrap := validBootstrap()
	journal, path := testJournal(t)
	if err := journal.AppendSessionCreated(bootstrap.SessionID, canonical.DigestBytes([]byte(bootstrap.SessionNonce)), bootstrap.Authority, bootstrap.OwnerEpoch, bootstrap.CurrentAuthorityHead); err != nil {
		t.Fatal(err)
	}
	committedSize, _ := os.Stat(path)
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("00000020:{"); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	reopened, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := OpenJournal(reopened)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	after, _ := os.Stat(path)
	if after.Size() != committedSize.Size() || recovered.Snapshot().Sequence != 1 {
		t.Fatalf("partial recovery size=%d sequence=%d", after.Size(), recovered.Snapshot().Sequence)
	}
}

func TestJournalRecoveryTreatsMissingFinalLFAsTornFrame(t *testing.T) {
	bootstrap := validBootstrap()
	journal, path := testJournal(t)
	if err := journal.AppendSessionCreated(bootstrap.SessionID, canonical.DigestBytes([]byte(bootstrap.SessionNonce)), bootstrap.Authority, bootstrap.OwnerEpoch, bootstrap.CurrentAuthorityHead); err != nil {
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 || data[len(data)-1] != '\n' {
		t.Fatalf("read journal: %v", err)
	}
	if err := os.WriteFile(path, data[:len(data)-1], 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := OpenJournal(file)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if stat.Size() != 0 || recovered.Snapshot().Sequence != 0 {
		t.Fatalf("missing-LF recovery size=%d sequence=%d", stat.Size(), recovered.Snapshot().Sequence)
	}
}

func TestJournalPartialTailRejectsNonCanonicalOrDuplicatePrefix(t *testing.T) {
	for name, tail := range map[string]string{
		"garbage payload": "00000020:{G",
		"whitespace":      "00000020:{ ",
		"duplicate key":   "00000020:{\"a\":1,\"a\":",
	} {
		t.Run(name, func(t *testing.T) {
			bootstrap := validBootstrap()
			journal, path := testJournal(t)
			if err := journal.AppendSessionCreated(bootstrap.SessionID, canonical.DigestBytes([]byte(bootstrap.SessionNonce)), bootstrap.Authority, bootstrap.OwnerEpoch, bootstrap.CurrentAuthorityHead); err != nil {
				t.Fatal(err)
			}
			_ = journal.Close()
			file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = file.WriteString(tail)
			_ = file.Close()
			file, _ = os.OpenFile(path, os.O_RDWR, 0)
			if _, err := OpenJournal(file); !errors.Is(err, ErrIntervention) {
				t.Fatalf("OpenJournal error = %v", err)
			}
			_ = file.Close()
		})
	}
}

func TestJournalRejectsGarbageHashForkAndPendingIntent(t *testing.T) {
	for name, mutate := range map[string]func([]byte) []byte{
		"trailing garbage": func(input []byte) []byte { return append(input, 'G') },
		"hash fork": func(input []byte) []byte {
			return []byte(strings.Replace(string(input), "session-1", "session-2", 1))
		},
	} {
		t.Run(name, func(t *testing.T) {
			bootstrap := validBootstrap()
			journal, path := testJournal(t)
			if err := journal.AppendSessionCreated(bootstrap.SessionID, canonical.DigestBytes([]byte(bootstrap.SessionNonce)), bootstrap.Authority, bootstrap.OwnerEpoch, bootstrap.CurrentAuthorityHead); err != nil {
				t.Fatal(err)
			}
			_ = journal.Close()
			data, _ := os.ReadFile(path)
			if err := os.WriteFile(path, mutate(data), 0o600); err != nil {
				t.Fatal(err)
			}
			file, _ := os.OpenFile(path, os.O_RDWR, 0)
			if _, err := OpenJournal(file); !errors.Is(err, ErrIntervention) {
				t.Fatalf("OpenJournal error = %v", err)
			}
			_ = file.Close()
		})
	}

	bootstrap := validBootstrap()
	journal, _ := testJournal(t)
	if err := journal.AppendSessionCreated(bootstrap.SessionID, canonical.DigestBytes([]byte(bootstrap.SessionNonce)), bootstrap.Authority, bootstrap.OwnerEpoch, bootstrap.CurrentAuthorityHead); err != nil {
		t.Fatal(err)
	}
	projection := requestProjection{Command: CommandBindAuthority, CommandID: "bind-pending", Sequence: 1, RequestDigest: digest("1"), PreviousCommandDigest: CommandGenesisDigest, CurrentAuthorityHead: bootstrap.CurrentAuthorityHead, NextAuthorityHead: digest("2"), SupervisorStartedFactDigest: digest("3"), Deadline: time.Date(2026, 8, 29, 1, 2, 23, 0, time.UTC).Format(time.RFC3339Nano)}
	base := journalRecord{SchemaVersion: JournalSchema, SessionID: bootstrap.SessionID, SessionNonceDigest: canonical.DigestBytes([]byte(bootstrap.SessionNonce)), Authority: bootstrap.Authority, OwnerEpoch: bootstrap.OwnerEpoch, CurrentAuthorityHead: bootstrap.CurrentAuthorityHead}
	if err := journal.AppendIntent(base, projection); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSession(bootstrap, journal, fakeMechanics{}, time.Now); !errors.Is(err, ErrIntervention) {
		t.Fatalf("pending intent restart error=%v", err)
	}
}

func commandRequest(t *testing.T, sessionID string, command CommandName, commandID string, sequence uint64, previous, authorityHead string, deadline time.Time, payload any) Request {
	t.Helper()
	rawPayload := mustCanonical(payload)
	request := Request{ProtocolRevision: ProtocolRevision, SessionID: sessionID, Command: command, CommandID: commandID, Sequence: sequence, PreviousCommandDigest: previous, CurrentAuthorityHead: authorityHead, Deadline: deadline.UTC().Format(time.RFC3339Nano), Payload: rawPayload}
	request.RequestDigest, _ = digestValue(requestDigestInput{ProtocolRevision: request.ProtocolRevision, SessionID: request.SessionID, Command: request.Command, CommandID: request.CommandID, Sequence: request.Sequence, PreviousCommandDigest: request.PreviousCommandDigest, CurrentAuthorityHead: request.CurrentAuthorityHead, Deadline: request.Deadline, Payload: request.Payload})
	return request
}

func testJournal(t *testing.T) (*Journal, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), JournalFileName)
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := OpenJournal(file)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	return journal, path
}

func validBootstrap() BootstrapRequest {
	return BootstrapRequest{
		SchemaVersion: BootstrapSchema, ProtocolRevision: ProtocolRevision, SessionID: "session-1", SessionNonce: strings.Repeat("0123456789abcdef", 4), OwnerEpoch: 1,
		Authority:            AuthorityTuple{AuthorityNamespaceID: "namespace-1", TaskID: "task-1", RunID: "run-1", AttemptID: "attempt-1", AllocationID: "allocation-1", LeaseID: "lease-1", LeaseDigest: digest("1"), Generation: 1, FencingTokenDigest: digest("2"), OrchestratorID: "orchestrator-1"},
		LaunchAuthorizedFact: digest("3"), CurrentAuthorityHead: digest("a"),
		ControlDirectoryIdentity: ControlDirectoryIdentity{CanonicalPath: "/private/control", Device: 1, Inode: 2, FileType: "directory", UID: 501, GID: 20, Mode: 0o040700, LinkCount: 2},
		Core:                     CoreIdentity{UID: 501, GID: 20, Process: ProcessIdentity{PID: 100, BirthSeconds: 1, BirthMicroseconds: 1, SessionID: 99, ProcessGroupID: 99}, Binary: BinaryIdentity{CanonicalPath: "/fixed/bin/marshal", Device: 1, Inode: 3, FileType: "regular", UID: 501, GID: 20, Mode: 0o100755, LinkCount: 1, Size: 100, RawSHA256: digest("4"), CDHash: strings.Repeat("5", 40), SourceHead: strings.Repeat("6", 40), SelfProfile: "darwin-local-dogfood"}},
	}
}

func validSpawnPayload() SpawnPayload {
	argv := []string{"/secret/runtime", "secret-argument"}
	environment := []string{"TOKEN=credential-value"}
	stdin := []byte("stdin-secret")
	runtime := HeldObjectSpec{Role: "runtime", CanonicalPath: "/secret/runtime", Device: 1, Inode: 10, FileType: "regular", UID: 501, GID: 20, Mode: 0o100755, LinkCount: 1, Size: 100, RawSHA256: digest("4")}
	closure, err := launchidentity.Seal(launchidentity.SpecInput{RuntimeExecutable: launchObject(runtime), ClosureProfileID: launchidentity.NativeProfile, MaterialRoots: []launchidentity.MaterialRootV1{}, LaunchMaterials: []launchidentity.LaunchMaterialV1{}, Arguments: argv, Environment: environment, WorkingDirectory: "/secret/repository"})
	if err != nil {
		panic(err)
	}
	return SpawnPayload{
		LaunchAuthorizedFactDigest: digest("3"), SupervisorStartedFactDigest: digest("c"),
		Runtime:          runtime,
		WorkingDirectory: HeldObjectSpec{Role: "working-directory", CanonicalPath: "/secret/repository", Device: 1, Inode: 11, FileType: "directory", UID: 501, GID: 20, Mode: 0o040755, LinkCount: 2, Size: 64},
		ClosureProfileID: closure.ClosureProfileID, MaterialRoots: closure.MaterialRoots, LaunchMaterials: closure.LaunchMaterials, LaunchMaterialsDigest: closure.LaunchMaterialsDigest, AgentLaunchSpecDigest: closure.AgentLaunchSpecDigest,
		ArgvDigest: mustDigestValue(argv), EnvironmentDigest: mustDigestValue(environment), StdinDigest: canonical.DigestBytes(stdin), EnvironmentKeys: []string{"TOKEN"}, Argv: argv, Environment: environment, Stdin: stdin,
	}
}

func mustDigestValue(value any) string {
	digest, _ := digestValue(value)
	return digest
}

func digest(character string) string { return "sha256:" + strings.Repeat(character, 64) }
