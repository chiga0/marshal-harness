//go:build darwin

package processsupervisor

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"golang.org/x/sys/unix"
)

type boundaryDriftMechanics struct {
	fakeMechanics
	drift func()
}

type interventionReplayMechanics struct{ fakeMechanics }

func (interventionReplayMechanics) Spawn(context.Context, SpawnPayload) (MechanicsResult, error) {
	return MechanicsResult{}, ErrIntervention
}

func (mechanics boundaryDriftMechanics) Spawn(context.Context, SpawnPayload) (MechanicsResult, error) {
	mechanics.drift()
	return fakeResult("fake-spawn"), nil
}

type partialCollectMechanics struct {
	fakeMechanics
	directory *os.File
}

func (mechanics partialCollectMechanics) Collect(context.Context, CollectPayload) (MechanicsResult, error) {
	if err := writeOwnerObject(mechanics.directory, stdoutObjectName, []byte("partial")); err != nil {
		return MechanicsResult{}, err
	}
	return MechanicsResult{}, ErrConflict
}

type transcriptCollectMechanics struct {
	fakeMechanics
	directory *os.File
	stdout    []byte
	stderr    []byte
}

func (mechanics transcriptCollectMechanics) Collect(context.Context, CollectPayload) (MechanicsResult, error) {
	report := ProcessReport{
		State: "terminal", ObserverIdentity: "darwin-fixed-process-supervisor-v1", ObservedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Process:             ProcessIdentity{PID: 200, BirthSeconds: 2, BirthMicroseconds: 3, SessionID: 99, ProcessGroupID: 99},
		RuntimeObjectDigest: digest("4"), WorkingObjectDigest: digest("5"),
		StdoutDigest: canonical.DigestBytes(mechanics.stdout), StderrDigest: canonical.DigestBytes(mechanics.stderr),
		StdoutBytes: uint64(len(mechanics.stdout)), StderrBytes: uint64(len(mechanics.stderr)),
	}
	manifest := mustCanonical(report)
	for _, object := range []struct {
		name string
		data []byte
	}{
		{name: stdoutObjectName, data: mechanics.stdout},
		{name: stderrObjectName, data: mechanics.stderr},
		{name: transcriptObjectName, data: manifest},
	} {
		if err := writeOwnerObject(mechanics.directory, object.name, object.data); err != nil {
			return MechanicsResult{}, err
		}
	}
	result := resultForReport("transcript-collected", report)
	result.TranscriptDigest = canonical.DigestBytes(manifest)
	result.StdoutBytes, result.StderrBytes = report.StdoutBytes, report.StderrBytes
	return result, nil
}

func TestHeldSessionControlFilesDescriptorRelativeIdentityAndNonce(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	directory, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	nonce := strings.Repeat("0123456789abcdef", 4)
	if err := os.WriteFile(filepath.Join(root, nonceFileName), []byte(nonce), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, JournalFileName), []byte("journal"), 0o600); err != nil {
		t.Fatal(err)
	}
	nonceFile, err := openControlFileAt(directory, nonceFileName)
	if err != nil {
		t.Fatal(err)
	}
	nonceIdentity, _, err := observeControlFile(nonceFile)
	_ = nonceFile.Close()
	if err != nil {
		t.Fatal(err)
	}
	journalFile, err := openControlFileAt(directory, JournalFileName)
	if err != nil {
		t.Fatal(err)
	}
	journalIdentity, _, err := observeControlFile(journalFile)
	_ = journalFile.Close()
	if err != nil {
		t.Fatal(err)
	}
	expected := SessionControlFiles{Nonce: nonceIdentity, Journal: journalIdentity}
	held, err := openHeldSessionControlFiles(directory, expected)
	if err != nil {
		t.Fatal(err)
	}
	defer held.close()
	got, err := readSessionNonce(held, canonical.DigestBytes([]byte(nonce)))
	if err != nil || got != nonce {
		t.Fatalf("nonce read=%q error=%v", got, err)
	}

	if err := os.Rename(filepath.Join(root, nonceFileName), filepath.Join(root, "old.nonce")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, nonceFileName), []byte(nonce), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := revalidateHeldSessionControlFiles(directory, held, expected); !errors.Is(err, ErrConflict) {
		t.Fatalf("ABA replacement error=%v", err)
	}
}

func TestHeldSessionControlFilesRejectSymlinkHardlinkAndWeakMode(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{name: "symlink", setup: func(t *testing.T, root string) {
			target := filepath.Join(root, "target")
			if err := os.WriteFile(target, []byte(strings.Repeat("a", nonceBytes)), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(root, nonceFileName)); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "hardlink", setup: func(t *testing.T, root string) {
			target := filepath.Join(root, "target")
			if err := os.WriteFile(target, []byte(strings.Repeat("a", nonceBytes)), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(target, filepath.Join(root, nonceFileName)); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "weak-mode", setup: func(t *testing.T, root string) {
			if err := os.WriteFile(filepath.Join(root, nonceFileName), []byte(strings.Repeat("a", nonceBytes)), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			test.setup(t, root)
			directory, err := os.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			defer directory.Close()
			file, err := openControlFileAt(directory, nonceFileName)
			if err == nil {
				defer file.Close()
				_, _, err = observeControlFile(file)
			}
			if !errors.Is(err, ErrConflict) {
				t.Fatalf("boundary error=%v", err)
			}
		})
	}
}

func TestInitialControlDirectoryRequiresExactEmptyObservation(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	directory, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	_, identity, err := observeControlDirectory(directory)
	if err != nil || revalidateInitialControlDirectory(directory, identity) != nil {
		t.Fatalf("initial empty observation: identity=%+v error=%v", identity, err)
	}
	if err := os.WriteFile(filepath.Join(root, nonceFileName), []byte("early"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := revalidateInitialControlDirectory(directory, identity); !errors.Is(err, ErrConflict) {
		t.Fatalf("initial frozen-name entry error=%v", err)
	}
}

func TestCommandBoundaryRejectsPreAndPostReceiptDriftWithoutResponse(t *testing.T) {
	for _, phase := range []string{"pre-command", "post-receipt"} {
		t.Run(phase, func(t *testing.T) {
			boundary, journal, bootstrap, root := commandBoundaryFixture(t)
			mechanics := boundaryDriftMechanics{drift: func() {
				if phase == "post-receipt" {
					if err := os.Chmod(filepath.Join(root, JournalFileName), 0o644); err != nil {
						t.Fatal(err)
					}
				}
			}}
			session, err := NewSession(bootstrap, journal, mechanics, time.Now)
			if err != nil {
				t.Fatal(err)
			}
			session.state = sessionBound
			session.supervisorStartedFact = digest("c")
			if phase == "pre-command" {
				if err := os.Link(filepath.Join(root, nonceFileName), filepath.Join(root, "nonce-link")); err != nil {
					t.Fatal(err)
				}
			}
			request := commandRequest(t, bootstrap.SessionID, CommandSpawn, "spawn-boundary", 1, CommandGenesisDigest, bootstrap.CurrentAuthorityHead, time.Now().Add(time.Minute), validSpawnPayload())
			response, err := handleSessionCommand(session, boundary, mustCanonical(request))
			if !errors.Is(err, ErrConflict) || response.SchemaVersion != "" || session.State() != string(sessionIntervention) {
				t.Fatalf("response=%+v error=%v state=%s", response, err, session.State())
			}
			sequence := journal.Snapshot().Sequence
			if phase == "pre-command" && sequence != 1 || phase == "post-receipt" && sequence != 3 {
				t.Fatalf("phase=%s journal sequence=%d", phase, sequence)
			}
		})
	}
}

func TestRuntimeControlBoundaryRequiresPhaseExactEntrySets(t *testing.T) {
	t.Run("pre-collect-rejects-early-output", func(t *testing.T) {
		boundary, journal, bootstrap, _ := commandBoundaryFixture(t)
		if _, err := NewSession(bootstrap, journal, fakeMechanics{}, time.Now); err != nil {
			t.Fatal(err)
		}
		if err := boundary.revalidate(journal.Snapshot()); err != nil {
			t.Fatalf("base boundary: %v", err)
		}
		if err := writeOwnerObject(boundary.directory, stdoutObjectName, []byte("early")); err != nil {
			t.Fatal(err)
		}
		if err := boundary.revalidate(journal.Snapshot()); !errors.Is(err, ErrConflict) {
			t.Fatalf("early output error=%v", err)
		}
	})

	t.Run("missing-base-entry", func(t *testing.T) {
		boundary, journal, bootstrap, _ := commandBoundaryFixture(t)
		if _, err := NewSession(bootstrap, journal, fakeMechanics{}, time.Now); err != nil {
			t.Fatal(err)
		}
		if err := unix.Unlinkat(int(boundary.directory.Fd()), controlSocket, 0); err != nil {
			t.Fatal(err)
		}
		if err := boundary.revalidate(journal.Snapshot()); !errors.Is(err, ErrConflict) {
			t.Fatalf("missing socket error=%v", err)
		}
	})

	t.Run("unknown-entry", func(t *testing.T) {
		boundary, journal, bootstrap, root := commandBoundaryFixture(t)
		if _, err := NewSession(bootstrap, journal, fakeMechanics{}, time.Now); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "unexpected"), []byte("hostile"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := boundary.revalidate(journal.Snapshot()); !errors.Is(err, ErrConflict) {
			t.Fatalf("unknown entry error=%v", err)
		}
	})
}

func TestPendingCollectAcceptsOnlyOrderedOutputPrefixes(t *testing.T) {
	boundary, journal, bootstrap, _ := commandBoundaryFixture(t)
	session, err := NewSession(bootstrap, journal, fakeMechanics{}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	request := commandRequest(t, bootstrap.SessionID, CommandCollect, "collect-pending", 1, CommandGenesisDigest, bootstrap.CurrentAuthorityHead, time.Now().Add(time.Minute), CollectPayload{ProcessStartedFactDigest: digest("d"), LastObservationDigest: digest("e")})
	projection, _, err := projectRequest(request)
	if err != nil {
		t.Fatalf("append collect intent: %v", err)
	}
	if err := journal.AppendIntent(session.journalBase(), projection); err != nil {
		t.Fatalf("append collect intent: %v", err)
	}
	anchor := HandshakeAnchor{ControlSocket: boundary.socket, ControlFiles: boundary.controlFiles}
	for _, step := range []struct {
		name string
		data []byte
	}{
		{name: "", data: nil},
		{name: stdoutObjectName, data: []byte("stdout")},
		{name: stderrObjectName, data: []byte("stderr")},
		{name: transcriptObjectName, data: []byte("transcript")},
	} {
		if step.name != "" {
			if err := writeOwnerObject(boundary.directory, step.name, step.data); err != nil {
				t.Fatalf("write %s: %v", step.name, err)
			}
		}
		if err := revalidateHeldRuntimeControlBoundary(boundary.directory, boundary.directoryIdentity, boundary.heldFiles, anchor); err != nil {
			t.Fatalf("pending prefix after %q: %v", step.name, err)
		}
	}

	t.Run("out-of-order", func(t *testing.T) {
		otherBoundary, otherJournal, otherBootstrap, _ := commandBoundaryFixture(t)
		otherSession, sessionErr := NewSession(otherBootstrap, otherJournal, fakeMechanics{}, time.Now)
		if sessionErr != nil {
			t.Fatal(sessionErr)
		}
		otherRequest := commandRequest(t, otherBootstrap.SessionID, CommandCollect, "collect-pending-order", 1, CommandGenesisDigest, otherBootstrap.CurrentAuthorityHead, time.Now().Add(time.Minute), CollectPayload{ProcessStartedFactDigest: digest("d"), LastObservationDigest: digest("e")})
		otherProjection, _, projectErr := projectRequest(otherRequest)
		if projectErr != nil {
			t.Fatalf("append other collect intent: %v", projectErr)
		}
		if err := otherJournal.AppendIntent(otherSession.journalBase(), otherProjection); err != nil {
			t.Fatalf("append other collect intent: %v", err)
		}
		if err := writeOwnerObject(otherBoundary.directory, stderrObjectName, []byte("stderr")); err != nil {
			t.Fatal(err)
		}
		if err := otherBoundary.revalidate(otherJournal.Snapshot()); !errors.Is(err, ErrConflict) {
			t.Fatalf("out-of-order prefix error=%v", err)
		}
	})
}

func TestSuccessfulCollectRequiresExactSixForReconnectTranscriptAndClose(t *testing.T) {
	boundary, journal, root := successfulCollectBoundaryFixture(t)
	anchor := HandshakeAnchor{ControlSocket: boundary.socket, ControlFiles: boundary.controlFiles}
	if err := revalidateHeldRuntimeControlBoundary(boundary.directory, boundary.directoryIdentity, boundary.heldFiles, anchor); err != nil {
		t.Fatalf("reconnect/close boundary: %v", err)
	}
	if err := revalidateTranscriptBoundary(boundary.directory, boundary.directoryIdentity, anchor); err != nil {
		t.Fatalf("transcript boundary: %v", err)
	}
	if err := unix.Unlinkat(int(boundary.directory.Fd()), stderrObjectName, 0); err != nil {
		t.Fatal(err)
	}
	if err := boundary.revalidate(journal.Snapshot()); !errors.Is(err, ErrConflict) {
		t.Fatalf("successful collect accepted missing stderr: %v", err)
	}
	if err := writeOwnerObject(boundary.directory, stderrObjectName, []byte("stderr")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "unexpected-after-collect"), []byte("hostile"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := boundary.revalidate(journal.Snapshot()); !errors.Is(err, ErrConflict) {
		t.Fatalf("successful collect accepted extra entry: %v", err)
	}
}

func TestCollectedOutputObjectAndContentDriftFailClosed(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, sessionControlBoundary, string)
	}{
		{name: "symlink", mutate: func(t *testing.T, boundary sessionControlBoundary, root string) {
			if err := unix.Unlinkat(int(boundary.directory.Fd()), stdoutObjectName, 0); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(root, stderrObjectName), filepath.Join(root, stdoutObjectName)); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "hardlink", mutate: func(t *testing.T, _ sessionControlBoundary, root string) {
			if err := os.Link(filepath.Join(root, stdoutObjectName), filepath.Join(t.TempDir(), "stdout-alias")); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "weak-mode", mutate: func(t *testing.T, _ sessionControlBoundary, root string) {
			if err := os.Chmod(filepath.Join(root, stdoutObjectName), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "content-drift-same-size", mutate: func(t *testing.T, _ sessionControlBoundary, root string) {
			if err := os.WriteFile(filepath.Join(root, stdoutObjectName), []byte("STDOUT"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "size-drift", mutate: func(t *testing.T, _ sessionControlBoundary, root string) {
			file, err := os.OpenFile(filepath.Join(root, stdoutObjectName), os.O_WRONLY|os.O_APPEND, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := file.Write([]byte("-larger")); err != nil {
				_ = file.Close()
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			boundary, journal, root := successfulCollectBoundaryFixture(t)
			test.mutate(t, boundary, root)
			if err := boundary.revalidate(journal.Snapshot()); !errors.Is(err, ErrConflict) {
				t.Fatalf("drift error=%v", err)
			}
		})
	}
}

func TestRejectedPartialCollectKeepsReceiptAndReturnsNoResponse(t *testing.T) {
	boundary, journal, bootstrap, _ := commandBoundaryFixture(t)
	session, err := NewSession(bootstrap, journal, partialCollectMechanics{directory: boundary.directory}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	session.state = sessionBound
	session.startedFact = digest("d")
	session.lastObservation = digest("e")
	request := commandRequest(t, bootstrap.SessionID, CommandCollect, "collect-partial", 1, CommandGenesisDigest, bootstrap.CurrentAuthorityHead, time.Now().Add(time.Minute), CollectPayload{ProcessStartedFactDigest: session.startedFact, LastObservationDigest: session.lastObservation})
	response, err := handleSessionCommand(session, boundary, mustCanonical(request))
	if !errors.Is(err, ErrConflict) || response.SchemaVersion != "" || session.State() != string(sessionIntervention) || journal.Snapshot().Sequence != 3 {
		t.Fatalf("response=%+v error=%v state=%s sequence=%d", response, err, session.State(), journal.Snapshot().Sequence)
	}
}

func TestReconnectReceiptReplayPostBoundaryDriftIntervenesWithZeroWireResponse(t *testing.T) {
	boundary, journal, bootstrap, _ := commandBoundaryFixture(t)
	now := time.Date(2026, 8, 29, 1, 2, 3, 0, time.UTC)
	session, err := NewSession(bootstrap, journal, fakeMechanics{}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	commandSequence, commandHead, journalSequence, journalHead := session.Snapshot()
	pending := commandRequest(t, bootstrap.SessionID, CommandAbortUnbound, "abort-reconnect-boundary", 1, commandHead, bootstrap.CurrentAuthorityHead, now.Add(20*time.Second), AbortUnboundPayload{OwnerEpoch: bootstrap.OwnerEpoch, PreviousAuthorityHead: bootstrap.CurrentAuthorityHead, AuthorityAbsenceProofDigest: digest("7")})
	if response := session.Handle(mustCanonical(pending)); response.Status != "ok" {
		t.Fatalf("pending response=%+v", response)
	}
	reconnect := reconnectRequest{
		SchemaVersion: ReconnectSchema, ProtocolRevision: ProtocolRevision, SessionID: bootstrap.SessionID, SessionNonce: bootstrap.SessionNonce,
		PreviousOwnerEpoch: bootstrap.OwnerEpoch, OwnerEpoch: bootstrap.OwnerEpoch + 1, PreviousAuthorityHead: bootstrap.CurrentAuthorityHead, CurrentAuthorityHead: digest("8"), ControlOwnerAcquired: digest("9"), Core: bootstrap.Core,
		LastOwnerEpoch: bootstrap.OwnerEpoch, LastAuthorityHead: bootstrap.CurrentAuthorityHead,
		LastCommandSequence: commandSequence, LastCommandHead: commandHead, LastJournalSequence: journalSequence, LastJournalHead: journalHead, PendingRequest: &pending,
	}
	if err := boundary.revalidate(session.journal.Snapshot()); err != nil {
		t.Fatalf("pre-replay boundary: %v", err)
	}
	attempt := session.reconnectAttempt(reconnect, bootstrap.Core)
	if attempt.disposition != reconnectResolvedWithoutMechanics || attempt.err != nil || attempt.resolution.State != ReconciliationReceiptCommitted || attempt.resolution.Response == nil {
		t.Fatalf("attempt=%+v", attempt)
	}
	if err := writeOwnerObject(boundary.directory, stdoutObjectName, []byte("post-replay-drift")); err != nil {
		t.Fatal(err)
	}
	decision := decideReconnectWireAfterAttempt(session, boundary, attempt)
	if decision.disposition != reconnectWireSilentClose || !errors.Is(decision.err, ErrConflict) || session.State() != string(sessionIntervention) {
		t.Fatalf("decision=%+v state=%s", decision, session.State())
	}
	if session.ownerEpoch != reconnect.OwnerEpoch || session.authorityHead != reconnect.CurrentAuthorityHead {
		t.Fatalf("post-replay owner/head=%d/%s", session.ownerEpoch, session.authorityHead)
	}
	snapshot := journal.Snapshot()
	replayed, ok := snapshot.commands[pending.CommandID]
	if snapshot.Sequence != 3 || snapshot.pending != nil || !ok || replayed.Response.Status != "ok" {
		t.Fatalf("durable replay changed: sequence=%d pending=%+v replay=%+v ok=%v", snapshot.Sequence, snapshot.pending, replayed, ok)
	}
	assertZeroReconnectWire(t, decision, session, boundary)
}

func TestReconnectMechanicsInterventionPersistsReceiptAndClosesWithZeroWireResponse(t *testing.T) {
	boundary, journal, bootstrap, _ := commandBoundaryFixture(t)
	now := time.Date(2026, 8, 29, 1, 2, 3, 0, time.UTC)
	session, err := NewSession(bootstrap, journal, interventionReplayMechanics{}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	session.state = sessionBound
	session.supervisorStartedFact = digest("c")
	commandSequence, commandHead, journalSequence, journalHead := session.Snapshot()
	pending := commandRequest(t, bootstrap.SessionID, CommandSpawn, "spawn-reconnect-intervention", 1, commandHead, bootstrap.CurrentAuthorityHead, now.Add(time.Minute), validSpawnPayload())
	reconnect := reconnectRequest{
		SchemaVersion: ReconnectSchema, ProtocolRevision: ProtocolRevision, SessionID: bootstrap.SessionID, SessionNonce: bootstrap.SessionNonce,
		PreviousOwnerEpoch: bootstrap.OwnerEpoch, OwnerEpoch: bootstrap.OwnerEpoch + 1, PreviousAuthorityHead: bootstrap.CurrentAuthorityHead, CurrentAuthorityHead: digest("8"), ControlOwnerAcquired: digest("9"), Core: bootstrap.Core,
		LastOwnerEpoch: bootstrap.OwnerEpoch, LastAuthorityHead: bootstrap.CurrentAuthorityHead,
		LastCommandSequence: commandSequence, LastCommandHead: commandHead, LastJournalSequence: journalSequence, LastJournalHead: journalHead, PendingRequest: &pending,
	}
	decision := decideReconnectWire(session, boundary, reconnect, bootstrap.Core)
	if decision.disposition != reconnectWireSilentClose || !errors.Is(decision.err, ErrIntervention) || session.State() != string(sessionIntervention) {
		t.Fatalf("decision=%+v state=%s", decision, session.State())
	}
	if session.ownerEpoch != bootstrap.OwnerEpoch || session.authorityHead != bootstrap.CurrentAuthorityHead {
		t.Fatalf("failed replay changed owner/head=%d/%s", session.ownerEpoch, session.authorityHead)
	}
	snapshot := journal.Snapshot()
	replayed, ok := snapshot.commands[pending.CommandID]
	if snapshot.Sequence != 3 || snapshot.pending != nil || !ok || replayed.Response.Status != "rejected" || replayed.Response.ReasonCode != ErrIntervention.ReasonCode {
		t.Fatalf("durable intervention changed: sequence=%d pending=%+v replay=%+v ok=%v", snapshot.Sequence, snapshot.pending, replayed, ok)
	}
	assertZeroReconnectWire(t, decision, session, boundary)
}

func TestReconnectAdmissionConflictMayEmitRejectedHandshake(t *testing.T) {
	boundary, journal, bootstrap, _ := commandBoundaryFixture(t)
	session, err := NewSession(bootstrap, journal, fakeMechanics{}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	commandSequence, commandHead, journalSequence, journalHead := session.Snapshot()
	reconnect := reconnectRequest{
		SchemaVersion: ReconnectSchema, ProtocolRevision: ProtocolRevision, SessionID: bootstrap.SessionID, SessionNonce: bootstrap.SessionNonce,
		PreviousOwnerEpoch: bootstrap.OwnerEpoch, OwnerEpoch: bootstrap.OwnerEpoch, PreviousAuthorityHead: bootstrap.CurrentAuthorityHead, CurrentAuthorityHead: digest("8"), ControlOwnerAcquired: digest("9"), Core: bootstrap.Core,
		LastOwnerEpoch: bootstrap.OwnerEpoch, LastAuthorityHead: bootstrap.CurrentAuthorityHead,
		LastCommandSequence: commandSequence, LastCommandHead: commandHead, LastJournalSequence: journalSequence, LastJournalHead: journalHead,
	}
	decision := decideReconnectWire(session, boundary, reconnect, bootstrap.Core)
	if decision.disposition != reconnectWireRejected || !errors.Is(decision.err, ErrConflict) || session.State() == string(sessionIntervention) || journal.Snapshot().Sequence != 1 {
		t.Fatalf("decision=%+v state=%s sequence=%d", decision, session.State(), journal.Snapshot().Sequence)
	}
	server, peer := net.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- emitReconnectHandshake(server, decision, session, CoreIdentity{}, boundary.socket, boundary.controlFiles)
		_ = server.Close()
	}()
	reader := bufio.NewReaderSize(peer, MaxWireFrameBytes+frameHeaderBytes+1)
	raw, readErr := readFrame(reader, MaxWireFrameBytes)
	_ = peer.Close()
	if emitErr := <-done; emitErr != nil || readErr != nil {
		t.Fatalf("emit=%v read=%v", emitErr, readErr)
	}
	var response HandshakeResponse
	if strictCanonicalDecode(raw, &response) != nil || response.Status != "rejected" || response.ReasonCode != ErrConflict.ReasonCode {
		t.Fatalf("response=%+v raw=%q", response, raw)
	}
}

func assertZeroReconnectWire(t *testing.T, decision reconnectWireDecision, session *Session, boundary sessionControlBoundary) {
	t.Helper()
	server, peer := net.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- emitReconnectHandshake(server, decision, session, CoreIdentity{}, boundary.socket, boundary.controlFiles)
		_ = server.Close()
	}()
	wire, readErr := io.ReadAll(peer)
	_ = peer.Close()
	if emitErr := <-done; emitErr != nil || readErr != nil || len(wire) != 0 {
		t.Fatalf("emit=%v read=%v wire=%q", emitErr, readErr, wire)
	}
}

func TestReadHeldJournalSnapshotRejectsPartialAndTornTail(t *testing.T) {
	t.Run("partial-header", func(t *testing.T) {
		_, journal, bootstrap, _ := commandBoundaryFixture(t)
		if _, err := NewSession(bootstrap, journal, fakeMechanics{}, time.Now); err != nil {
			t.Fatal(err)
		}
		stat, err := journal.file.Stat()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := journal.file.WriteAt([]byte("0000"), stat.Size()); err != nil || journal.file.Sync() != nil {
			t.Fatalf("append partial header: %v", err)
		}
		if _, err := readHeldJournalSnapshot(journal.file); !errors.Is(err, ErrIntervention) {
			t.Fatalf("partial header error=%v", err)
		}
	})

	t.Run("torn-valid-record", func(t *testing.T) {
		_, journal, bootstrap, _ := commandBoundaryFixture(t)
		session, err := NewSession(bootstrap, journal, fakeMechanics{}, time.Now)
		if err != nil {
			t.Fatal(err)
		}
		request := commandRequest(t, bootstrap.SessionID, CommandCollect, "collect-torn", 1, CommandGenesisDigest, bootstrap.CurrentAuthorityHead, time.Now().Add(time.Minute), CollectPayload{ProcessStartedFactDigest: digest("d"), LastObservationDigest: digest("e")})
		projection, _, err := projectRequest(request)
		if err != nil {
			t.Fatalf("append collect intent: %v", err)
		}
		if err := journal.AppendIntent(session.journalBase(), projection); err != nil {
			t.Fatalf("append collect intent: %v", err)
		}
		stat, err := journal.file.Stat()
		if err != nil || stat.Size() <= 1 || journal.file.Truncate(stat.Size()-1) != nil || journal.file.Sync() != nil {
			t.Fatalf("truncate final record: %v", err)
		}
		if _, err := readHeldJournalSnapshot(journal.file); !errors.Is(err, ErrIntervention) {
			t.Fatalf("torn record error=%v", err)
		}
	})
}

func TestControlDirectoryObjectComparisonIgnoresOnlyLinkCount(t *testing.T) {
	identity := ControlDirectoryIdentity{CanonicalPath: "/private/tmp/control", Device: 1, Inode: 2, FileType: "directory", UID: 501, GID: 20, Mode: 0o040700, LinkCount: 2}
	linkGrowth := identity
	linkGrowth.LinkCount++
	if !sameControlDirectoryObject(identity, linkGrowth) {
		t.Fatal("link-count-only growth changed stable directory object")
	}
	mutations := map[string]func(*ControlDirectoryIdentity){
		"path":  func(value *ControlDirectoryIdentity) { value.CanonicalPath += "-other" },
		"dev":   func(value *ControlDirectoryIdentity) { value.Device++ },
		"inode": func(value *ControlDirectoryIdentity) { value.Inode++ },
		"type":  func(value *ControlDirectoryIdentity) { value.FileType = "regular" },
		"uid":   func(value *ControlDirectoryIdentity) { value.UID++ },
		"gid":   func(value *ControlDirectoryIdentity) { value.GID++ },
		"mode":  func(value *ControlDirectoryIdentity) { value.Mode = 0o040755 },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := identity
			mutate(&changed)
			if sameControlDirectoryObject(identity, changed) {
				t.Fatalf("accepted stable-field drift: %+v", changed)
			}
		})
	}
}

func successfulCollectBoundaryFixture(t *testing.T) (sessionControlBoundary, *Journal, string) {
	t.Helper()
	boundary, journal, bootstrap, root := commandBoundaryFixture(t)
	producer := transcriptCollectMechanics{directory: boundary.directory, stdout: []byte("stdout"), stderr: []byte("stderr")}
	session, err := NewSession(bootstrap, journal, producer, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	session.state = sessionBound
	session.startedFact = digest("d")
	session.lastObservation = digest("e")
	request := commandRequest(t, bootstrap.SessionID, CommandCollect, "collect-success", 1, CommandGenesisDigest, bootstrap.CurrentAuthorityHead, time.Now().Add(time.Minute), CollectPayload{ProcessStartedFactDigest: session.startedFact, LastObservationDigest: session.lastObservation})
	response, err := handleSessionCommand(session, boundary, mustCanonical(request))
	if err != nil || response.Status != "ok" {
		t.Fatalf("producer collect response=%+v error=%v", response, err)
	}
	return boundary, journal, root
}

func commandBoundaryFixture(t *testing.T) (sessionControlBoundary, *Journal, BootstrapRequest, string) {
	t.Helper()
	// AF_UNIX paths on Darwin are bounded. t.TempDir includes the full test and
	// subtest name, so use an explicit short root while retaining deterministic
	// cleanup instead of weakening the production socket-path boundary.
	root, err := os.MkdirTemp("/private/tmp", "marshal-ps-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Errorf("remove short control root: %v", err)
		}
	})
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	// Darwin inherits the parent directory group. /private/tmp is commonly
	// group wheel even for an ordinary user, while the production control root
	// is owned by the effective user/group and the strict held-file identity
	// gate requires that exact group.
	if err := os.Chown(root, -1, os.Getegid()); err != nil {
		t.Fatalf("set short control root group: %v", err)
	}
	directory, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = directory.Close() })
	_, directoryIdentity, err := observeControlDirectory(directory)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap := validBootstrap()
	bootstrap.ControlDirectoryIdentity = directoryIdentity
	nonceHeld, err := writeHeldOpenatExclusive(directory, nonceFileName, []byte(bootstrap.SessionNonce), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = nonceHeld.Close() })
	journalFile, err := openatExclusive(directory, JournalFileName, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := directory.Sync(); err != nil {
		_ = journalFile.Close()
		t.Fatal(err)
	}
	nonceIdentity, _, err := observeControlFile(nonceHeld)
	if err != nil {
		_ = journalFile.Close()
		t.Fatal(err)
	}
	journalIdentity, _, err := observeControlFile(journalFile)
	if err != nil {
		_ = journalFile.Close()
		t.Fatal(err)
	}
	controlFiles := SessionControlFiles{Nonce: nonceIdentity, Journal: journalIdentity}
	held := &heldSessionControlFiles{nonce: nonceHeld, journal: journalFile, identity: controlFiles}
	if err := revalidateHeldSessionControlFiles(directory, held, controlFiles); err != nil {
		_ = journalFile.Close()
		t.Fatal(err)
	}
	journal, err := OpenJournal(journalFile)
	if err != nil {
		_ = journalFile.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	listener, err := listenUnixAt(directory, controlSocket)
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	t.Cleanup(func() { _ = listener.Close() })
	if err := unix.Fchmodat(int(directory.Fd()), controlSocket, 0o600, 0); err != nil {
		t.Fatal(err)
	}
	if err := directory.Sync(); err != nil {
		t.Fatal(err)
	}
	socket, err := observeControlSocket(directory)
	if err != nil {
		t.Fatal(err)
	}
	_, finalDirectoryIdentity, err := observeControlDirectory(directory)
	if err != nil || !sameControlDirectoryObject(finalDirectoryIdentity, directoryIdentity) {
		t.Fatalf("observe final control directory: identity=%+v error=%v", finalDirectoryIdentity, err)
	}
	return sessionControlBoundary{directory: directory, directoryIdentity: finalDirectoryIdentity, socket: socket, heldFiles: held, controlFiles: controlFiles}, journal, bootstrap, root
}
