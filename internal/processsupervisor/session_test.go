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
	request := ReconnectRequest{SchemaVersion: ReconnectSchema, ProtocolRevision: ProtocolRevision, SessionID: bootstrap.SessionID, SessionNonce: bootstrap.SessionNonce, PreviousOwnerEpoch: bootstrap.OwnerEpoch, OwnerEpoch: bootstrap.OwnerEpoch + 1, PreviousAuthorityHead: bootstrap.CurrentAuthorityHead, CurrentAuthorityHead: bootstrap.CurrentAuthorityHead, ControlOwnerAcquired: digest("7"), Core: bootstrap.Core}
	if err := session.Reconnect(request, bootstrap.Core); !errors.Is(err, ErrConflict) {
		t.Fatalf("same-head owner epoch advance error=%v", err)
	}
	request.CurrentAuthorityHead = digest("8")
	request.ControlOwnerAcquired = ""
	if err := session.Reconnect(request, bootstrap.Core); !errors.Is(err, ErrConflict) {
		t.Fatalf("missing owner-acquired proof error=%v", err)
	}
	request.ControlOwnerAcquired = digest("7")
	if err := session.Reconnect(request, bootstrap.Core); err != nil {
		t.Fatalf("valid owner advance error=%v", err)
	}
}

func TestHandshakeBindsFrozenControlSocketIdentity(t *testing.T) {
	bootstrap := validBootstrap()
	socket := ControlSocketIdentity{Device: 9, Inode: 10, FileType: "socket", UID: 501, GID: 20, Mode: 0o140600, LinkCount: 1}
	process := ProcessIdentity{PID: 200, BirthSeconds: 2, BirthMicroseconds: 3, SessionID: 99, ProcessGroupID: 99}
	binary := bootstrap.Core.Binary
	response := HandshakeResponse{SchemaVersion: HandshakeSchema, ProtocolRevision: ProtocolRevision, Status: "ok", ReasonCode: "process-supervisor-ready", SessionID: bootstrap.SessionID, SessionNonceDigest: canonical.DigestBytes([]byte(bootstrap.SessionNonce)), OwnerEpoch: 1, CurrentAuthorityHead: bootstrap.CurrentAuthorityHead, CommandHead: CommandGenesisDigest, JournalSequence: 1, JournalHead: digest("8"), ObserverIdentity: "darwin-fixed-process-supervisor-v1", ObservedAt: time.Date(2026, 8, 29, 1, 2, 3, 0, time.UTC).Format(time.RFC3339Nano), SupervisorProcess: process, SupervisorBinary: binary, ControlSocket: socket}
	anchor := HandshakeAnchor{SessionID: response.SessionID, SessionNonceDigest: response.SessionNonceDigest, OwnerEpoch: response.OwnerEpoch, CurrentAuthorityHead: response.CurrentAuthorityHead, CommandSequence: response.CommandSequence, CommandHead: response.CommandHead, JournalSequence: response.JournalSequence, JournalHead: response.JournalHead, UID: 501, GID: 20, FixedBinary: binary, ControlSocket: socket}
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
		SchemaVersion: BootstrapSchema, ProtocolRevision: ProtocolRevision, SessionID: "session-1", SessionNonce: strings.Repeat("a", 64), OwnerEpoch: 1,
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
