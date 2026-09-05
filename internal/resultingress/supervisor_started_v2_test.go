package resultingress

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/chiga0/marshal-harness/internal/processsupervisor"
)

func testInitialStartedV2(t *testing.T, prepared SupervisorBootstrapPrepared, fact string) ProcessSupervisorStarted {
	t.Helper()
	r, g := prepared.Request, processsupervisor.DormantV2ProtocolContract()
	socket := processsupervisor.ControlSocketIdentity{Device: r.ControlDirectoryIdentity.Device, Inode: r.ControlDirectoryIdentity.Inode + 100, FileType: "socket", UID: r.Core.UID, GID: r.Core.GID, Mode: 0140600, LinkCount: 1}
	files := attemptTestControlFiles(socket.Device, socket.Inode+1)
	files.Nonce.UID, files.Nonce.GID, files.Journal.UID, files.Journal.GID = r.Core.UID, r.Core.GID, r.Core.UID, r.Core.GID
	process := r.Core.Process
	process.PID += 100
	process.SessionID, process.ProcessGroupID = process.PID, process.PID
	// The fixture independently constructs the public ADR 0079 initial record;
	// production validation must recompute it rather than accept an echo hash.
	head, err := canonicalDigest(map[string]any{"schemaVersion": g.JournalSchema, "protocolRevision": g.ProtocolRevision, "launchChildProtocolRevision": g.LaunchChildProtocolRevision,
		"mechanicsIdentity": g.MechanicsIdentity, "journalSequence": 1, "kind": "session-created", "sessionId": r.SessionID, "sessionNonceDigest": r.SessionNonceDigest,
		"authority": r.Authority, "ownerEpoch": r.OwnerEpoch, "currentAuthorityHead": r.CurrentAuthorityHead, "previousRecordDigest": g.JournalGenesisDigest})
	if err != nil {
		t.Fatal(err)
	}
	a := processsupervisor.SessionAnchorV2{Generation: g, ControlDirectory: r.ControlDirectoryIdentity, Binding: processsupervisor.HandshakeAnchor{SessionID: r.SessionID, SessionNonceDigest: r.SessionNonceDigest,
		Authority: r.Authority, OwnerEpoch: r.OwnerEpoch, CurrentAuthorityHead: r.CurrentAuthorityHead, CommandHead: g.CommandGenesisDigest, JournalSequence: 1, JournalHead: head,
		UID: r.Core.UID, GID: r.Core.GID, FixedBinary: r.Core.Binary, ControlSocket: socket, ControlFiles: files}}
	h := processsupervisor.HandshakeResponseV2{SchemaVersion: g.HandshakeSchema, ProtocolRevision: g.ProtocolRevision, LaunchChildProtocolRevision: g.LaunchChildProtocolRevision, MechanicsIdentity: g.MechanicsIdentity,
		Status: "ok", ReasonCode: "process-supervisor-ready", SessionID: r.SessionID, SessionNonceDigest: r.SessionNonceDigest, OwnerEpoch: r.OwnerEpoch, CurrentAuthorityHead: r.CurrentAuthorityHead,
		CommandHead: g.CommandGenesisDigest, JournalSequence: 1, JournalHead: head, ObserverIdentity: g.ObserverIdentity, ObservedAt: "2026-09-05T00:00:00Z",
		SupervisorProcess: process, SupervisorBinary: r.Core.Binary, ControlSocket: socket, ControlFiles: files}
	peer := processsupervisor.CoreIdentity{UID: r.Core.UID, GID: r.Core.GID, Process: process, Binary: r.Core.Binary}
	started, err := NewProcessSupervisorStartedV2FromBootstrap(fact, prepared, h, a, peer)
	if err != nil {
		t.Fatal(err)
	}
	return started
}

func TestStartedV2RejectsSelfConsistentFakeGenesisAndGenerationLoss(t *testing.T) {
	owner, request := testBootstrapV2Input()
	prepared, err := NewSupervisorBootstrapPreparedV2(owner, request)
	if err != nil {
		t.Fatal(err)
	}
	started := testInitialStartedV2(t, prepared, attemptTestDigest("prepared-fact"))
	for name, change := range map[string]func(*ProcessSupervisorStarted){
		"fake-genesis": func(s *ProcessSupervisorStarted) {
			s.V2.Handshake.JournalHead = attemptTestDigest("fake-initial-record")
			s.V2.Anchor.Binding.JournalHead = s.V2.Handshake.JournalHead
		},
		"legacy-and-v2":   func(s *ProcessSupervisorStarted) { s.Handshake.SchemaVersion = processsupervisor.HandshakeSchema },
		"wrong-owner":     func(s *ProcessSupervisorStarted) { s.Owner.OwnerEpoch++ },
		"wrong-directory": func(s *ProcessSupervisorStarted) { s.ControlDirectory.Inode++ },
	} {
		t.Run(name, func(t *testing.T) {
			changed := started
			change(&changed)
			if changed.Validate() == nil {
				t.Fatal("forged started accepted")
			}
		})
	}
	for i := 0; i < reflect.TypeOf(started.V2.Anchor.Generation).NumField(); i++ {
		changed := started
		reflect.ValueOf(&changed.V2.Anchor.Generation).Elem().Field(i).SetString("")
		if changed.Validate() == nil {
			t.Fatalf("generation field %d ignored", i)
		}
	}
	projected := projectSupervisorMechanicsAnchorV2(started.V2.Anchor)
	if projected.Validate() != nil || supervisorSessionAnchorV2(projected) != started.V2.Anchor {
		t.Fatal("mechanics projection lost binding")
	}
	if supervisorHandshakeAnchor(projected) != (processsupervisor.HandshakeAnchor{}) {
		t.Fatal("v2 silently projected to legacy consumer")
	}
	legacyIntent := SupervisorCommandIntent{ProtocolRevision: processsupervisor.ProtocolRevision, SessionID: projected.SessionID, Command: processsupervisor.CommandBindAuthority, CommandID: "mixed-bind",
		Sequence: 1, PreviousCommandHead: projected.CommandHead, CurrentAuthorityHead: projected.CurrentAuthorityHead, Deadline: "2026-09-05T01:00:00Z", RequestDigest: attemptTestDigest("request"), PayloadDigest: attemptTestDigest("payload"),
		PreCommand: projected, Rebuild: SupervisorCommandRebuildProjection{SupervisorStartedFactDigest: attemptTestDigest("started"), OwnerEpoch: projected.OwnerEpoch, PreviousAuthorityHead: projected.CurrentAuthorityHead, AuthorityHead: attemptTestDigest("bound")}}
	if legacyIntent.Validate() == nil {
		t.Fatal("v1 intent accepted a v2 anchor")
	}
	legacyIntent.PreCommand.Generation = processsupervisor.ProtocolGenerationContract{}
	legacyIntent.PreCommand.ControlDirectory = processsupervisor.ControlDirectoryIdentity{}
	if legacyIntent.Validate() != nil {
		t.Fatal("negative fixture would fail independently of generation")
	}
	current := projected
	current.OwnerEpoch++
	current.CurrentAuthorityHead = attemptTestDigest("new-owner-head")
	if (SupervisorReconnectEvidence{Previous: projected, Current: current, Reconciliation: processsupervisor.ReconciliationUnchanged}).Validate() == nil {
		t.Fatal("legacy reconnect accepted v2 anchors")
	}
	// Legacy serialization has no new subprojection, preserving old digests.
	legacy := ProcessSupervisorStarted{Owner: owner, LaunchAuthorizedFactDigest: prepared.LaunchAuthorizedFactDigest, BootstrapPreparedFactDigest: "legacy", ControlDirectory: prepared.ControlDirectory,
		Handshake: processsupervisor.HandshakeResponse{SchemaVersion: processsupervisor.HandshakeSchema}}
	raw, _ := json.Marshal(legacy)
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields["v2"] != nil || fields["handshake"] == nil {
		t.Fatal("legacy shape changed")
	}
}

func TestStartedV2ObjectReuseChecksIncludeLegacyHistory(t *testing.T) {
	owner, request := testBootstrapV2Input()
	prepared, err := NewSupervisorBootstrapPreparedV2(owner, request)
	if err != nil {
		t.Fatal(err)
	}
	v2 := testInitialStartedV2(t, prepared, attemptTestDigest("prepared-fact"))
	legacy := ProcessSupervisorStarted{ControlDirectory: v2.ControlDirectory, Handshake: processsupervisor.HandshakeResponse{SessionID: "unrelated", SupervisorProcess: v2.V2.Handshake.SupervisorProcess, ControlSocket: v2.V2.Handshake.ControlSocket}}
	if !supervisorStartedObjectsConflict(legacy, v2) || !supervisorStartedObjectsConflict(v2, legacy) {
		t.Fatal("cross-generation reuse escaped")
	}
}
