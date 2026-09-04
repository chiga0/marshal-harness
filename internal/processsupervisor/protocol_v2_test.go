package processsupervisor

import (
	"bytes"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

func TestDormantV2ProtocolContractIsExactAndDistinct(t *testing.T) {
	contract := DormantV2ProtocolContract()
	if contract.ProtocolRevision != "process-supervisor/v2" || contract.BootstrapSchema != "marshal.process-supervisor-bootstrap.v2" ||
		contract.ReconnectSchema != "marshal.process-supervisor-reconnect.v2" || contract.HandshakeSchema != "marshal.process-supervisor-handshake.v2" ||
		contract.RequestSchema != "marshal.process-supervisor-request.v2" || contract.ResponseSchema != "marshal.process-supervisor-response.v2" ||
		contract.JournalSchema != "marshal.process-supervisor-journal.v2" || contract.LaunchChildProtocolRevision != "process-supervisor-launch-child/v2" ||
		contract.LaunchChildSchema != "marshal.process-supervisor-launch-child.v2" || contract.ObserverIdentity != "darwin-fixed-process-supervisor/v2" ||
		contract.MechanicsIdentity != "darwin-posix-spawn-setexec/v1" || contract.CommandRecoveryRevision != "process-supervisor-command-recovery/v2" ||
		contract.JournalFileName != "process-supervisor-v2.journal" {
		t.Fatalf("v2 contract drift: %+v", contract)
	}
	if contract.CommandGenesisDigest != canonical.DigestBytes([]byte("marshal/process-supervisor-command/v2\x00genesis")) ||
		contract.JournalGenesisDigest != canonical.DigestBytes([]byte("marshal/process-supervisor-journal/v2\x00genesis")) {
		t.Fatal("v2 genesis digest drift")
	}
	if contract.ProtocolRevision == ProtocolRevision || contract.JournalSchema == JournalSchema || contract.JournalFileName == JournalFileName ||
		contract.CommandGenesisDigest == CommandGenesisDigest || contract.JournalGenesisDigest == JournalGenesisDigest {
		t.Fatal("v2 identity aliases v1")
	}
}

func TestDormantV2ClosedDecodersRejectV1AndMixedBindings(t *testing.T) {
	v2 := validBootstrapV2()
	raw := mustCanonical(v2)
	if err := ValidateDormantV2ProtocolMessage("bootstrap", raw); err != nil {
		t.Fatalf("valid v2 bootstrap rejected: %v", err)
	}
	if err := ValidateDormantV2ProtocolMessage("bootstrap", mustCanonical(validBootstrap())); err == nil {
		t.Fatal("v2 decoder accepted v1 bootstrap")
	}
	var legacy BootstrapRequest
	if strictCanonicalDecode(raw, &legacy) == nil {
		t.Fatal("v1 decoder accepted v2 bootstrap fields")
	}

	for name, mutate := range map[string]func(*bootstrapRequestV2){
		"protocol": func(value *bootstrapRequestV2) { value.ProtocolRevision = ProtocolRevision },
		"launch-child": func(value *bootstrapRequestV2) {
			value.LaunchChildProtocolRevision = "process-supervisor-launch-child/v1"
		},
		"mechanics": func(value *bootstrapRequestV2) { value.MechanicsIdentity = "darwin-ptrace-exec-stop/v1" },
		"schema":    func(value *bootstrapRequestV2) { value.SchemaVersion = BootstrapSchema },
	} {
		t.Run(name, func(t *testing.T) {
			changed := v2
			mutate(&changed)
			if err := ValidateDormantV2ProtocolMessage("bootstrap", mustCanonical(changed)); err == nil {
				t.Fatal("mixed generation accepted")
			}
		})
	}
	withUnknown := bytes.TrimSuffix(raw, []byte("}"))
	withUnknown = append(withUnknown, []byte(`,"unknown":true}`)...)
	if err := ValidateDormantV2ProtocolMessage("bootstrap", withUnknown); err == nil {
		t.Fatal("unknown v2 field accepted")
	}
	if err := ValidateDormantV2ProtocolMessage("unknown", raw); err == nil {
		t.Fatal("unknown v2 message kind accepted")
	}
}

func TestDormantV2RequestResponseBinding(t *testing.T) {
	request := validRequestV2(t)
	response := validResponseV2(t, request)
	requestRaw, responseRaw := mustCanonical(request), mustCanonical(response)
	if err := ValidateDormantV2ProtocolMessage("request", requestRaw); err != nil {
		t.Fatalf("request rejected: %v", err)
	}
	if err := ValidateDormantV2ProtocolMessage("response", responseRaw); err != nil {
		t.Fatalf("response rejected: %v", err)
	}
	if err := ValidateDormantV2ResponseBinding(responseRaw, requestRaw); err != nil {
		t.Fatalf("binding rejected: %v", err)
	}

	changed := request
	changed.MechanicsIdentity = "darwin-ptrace-exec-stop/v1"
	if err := ValidateDormantV2ResponseBinding(responseRaw, mustCanonical(changed)); err == nil {
		t.Fatal("mixed request binding accepted")
	}
	changed = request
	changed.Deadline = time.Date(2026, 9, 4, 10, 0, 1, 0, time.UTC).Format(time.RFC3339Nano)
	if err := ValidateDormantV2ProtocolMessage("request", mustCanonical(changed)); err == nil {
		t.Fatal("request digest did not bind deadline")
	}
}

func TestDormantV2HandshakeRequiresControlFilesAndExactObserver(t *testing.T) {
	bootstrap := validBootstrapV2()
	response := handshakeResponseV2{
		SchemaVersion: handshakeSchemaV2, ProtocolRevision: protocolRevisionV2, LaunchChildProtocolRevision: launchChildProtocolRevisionV2, MechanicsIdentity: mechanicsIdentityV2,
		Status: "ok", ReasonCode: "process-supervisor-ready", SessionID: bootstrap.SessionID, SessionNonceDigest: canonical.DigestBytes([]byte(bootstrap.SessionNonce)), OwnerEpoch: bootstrap.OwnerEpoch,
		CurrentAuthorityHead: bootstrap.CurrentAuthorityHead, CommandHead: commandGenesisDigestV2, JournalSequence: 1, JournalHead: digest("v2-journal-head"), ObserverIdentity: observerIdentityV2,
		ObservedAt: "2026-09-04T10:00:00Z", SupervisorProcess: bootstrap.Core.Process, SupervisorBinary: bootstrap.Core.Binary,
		ControlSocket: ControlSocketIdentity{Device: 8, Inode: 9, FileType: "socket", UID: 501, GID: 20, Mode: 0o140600, LinkCount: 1}, ControlFiles: productionTestControlFiles(),
	}
	if err := ValidateDormantV2ProtocolMessage("handshake", mustCanonical(response)); err != nil {
		t.Fatalf("valid handshake rejected: %v", err)
	}
	missing := response
	missing.ControlFiles = SessionControlFiles{}
	if err := ValidateDormantV2ProtocolMessage("handshake", mustCanonical(missing)); err == nil {
		t.Fatal("missing v2 control files accepted")
	}
	wrong := response
	wrong.ObserverIdentity = "darwin-fixed-process-supervisor/v1"
	if err := ValidateDormantV2ProtocolMessage("handshake", mustCanonical(wrong)); err == nil {
		t.Fatal("v1 observer accepted by v2 handshake")
	}
}

func TestDormantV2ReconnectRejectsPendingV1Request(t *testing.T) {
	bootstrap := validBootstrapV2()
	request := reconnectRequestV2{
		SchemaVersion: reconnectSchemaV2, ProtocolRevision: protocolRevisionV2, LaunchChildProtocolRevision: launchChildProtocolRevisionV2, MechanicsIdentity: mechanicsIdentityV2,
		SessionID: bootstrap.SessionID, SessionNonce: bootstrap.SessionNonce, PreviousOwnerEpoch: 1, OwnerEpoch: 2, PreviousAuthorityHead: digest("previous-authority"), CurrentAuthorityHead: digest("current-authority"),
		ControlOwnerAcquiredFactDigest: digest("owner-acquired"), Core: bootstrap.Core, LastOwnerEpoch: 1, LastAuthorityHead: digest("previous-authority"), LastCommandHead: commandGenesisDigestV2,
		LastJournalSequence: 1, LastJournalHead: digest("v2-journal-head"),
	}
	if err := ValidateDormantV2ProtocolMessage("reconnect", mustCanonical(request)); err != nil {
		t.Fatalf("valid reconnect rejected: %v", err)
	}
	pending := validRequestV2(t)
	request.PendingRequest = &pending
	if err := ValidateDormantV2ProtocolMessage("reconnect", mustCanonical(request)); err != nil {
		t.Fatalf("valid pending v2 request rejected: %v", err)
	}
	raw := mustCanonical(request)
	var generic map[string]any
	if strictCanonicalDecode(raw, &generic) != nil {
		t.Fatal("fixture decode failed")
	}
	generic["pendingRequest"] = map[string]any{"protocolRevision": ProtocolRevision}
	if err := ValidateDormantV2ProtocolMessage("reconnect", mustCanonical(generic)); err == nil {
		t.Fatal("pending v1 request accepted")
	}
}

func TestDormantV2LaunchChildSpecIsClosedAndGenerationBound(t *testing.T) {
	payload := validSpawnPayload()
	bootstrap := validBootstrap()
	spec := launchChildSpecV2{
		SchemaVersion: launchChildSchemaV2, ProtocolRevision: protocolRevisionV2, LaunchChildProtocolRevision: launchChildProtocolRevisionV2, MechanicsIdentity: mechanicsIdentityV2,
		ParentPID: bootstrap.Core.Process.PID,
		Runtime:   launchChildObjectV2{FD: 7, Object: payload.Runtime}, WorkingDirectory: launchChildObjectV2{FD: 6, Object: payload.WorkingDirectory},
		Marshal: launchChildObjectV2{FD: 8, Object: HeldObjectSpec{Role: "marshal", CanonicalPath: bootstrap.Core.Binary.CanonicalPath, Device: bootstrap.Core.Binary.Device, Inode: bootstrap.Core.Binary.Inode, FileType: bootstrap.Core.Binary.FileType, UID: bootstrap.Core.Binary.UID, GID: bootstrap.Core.Binary.GID, Mode: bootstrap.Core.Binary.Mode, LinkCount: bootstrap.Core.Binary.LinkCount, Size: bootstrap.Core.Binary.Size, RawSHA256: bootstrap.Core.Binary.RawSHA256}},
		Argv:    append([]string(nil), payload.Argv...), Environment: append([]string(nil), payload.Environment...),
	}
	if err := ValidateDormantV2ProtocolMessage("launch-child", mustCanonical(spec)); err != nil {
		t.Fatalf("valid launch child rejected: %v", err)
	}
	for name, mutate := range map[string]func(*launchChildSpecV2){
		"schema":   func(value *launchChildSpecV2) { value.SchemaVersion = "marshal.process-supervisor-launch-child.v1" },
		"protocol": func(value *launchChildSpecV2) { value.ProtocolRevision = ProtocolRevision },
		"child-protocol": func(value *launchChildSpecV2) {
			value.LaunchChildProtocolRevision = "process-supervisor-launch-child/v1"
		},
		"mechanics": func(value *launchChildSpecV2) { value.MechanicsIdentity = "darwin-ptrace-exec-stop/v1" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := spec
			mutate(&changed)
			if err := ValidateDormantV2ProtocolMessage("launch-child", mustCanonical(changed)); err == nil {
				t.Fatal("mixed launch child accepted")
			}
		})
	}
}

func validBootstrapV2() bootstrapRequestV2 {
	legacy := validBootstrap()
	return bootstrapRequestV2{
		SchemaVersion: bootstrapSchemaV2, ProtocolRevision: protocolRevisionV2, LaunchChildProtocolRevision: launchChildProtocolRevisionV2, MechanicsIdentity: mechanicsIdentityV2,
		SessionID: legacy.SessionID, SessionNonce: legacy.SessionNonce, OwnerEpoch: legacy.OwnerEpoch, Authority: legacy.Authority, LaunchAuthorizedFact: legacy.LaunchAuthorizedFact,
		CurrentAuthorityHead: legacy.CurrentAuthorityHead, ControlDirectoryIdentity: legacy.ControlDirectoryIdentity, Core: legacy.Core,
	}
}

func validRequestV2(t *testing.T) requestV2 {
	t.Helper()
	payload := BindAuthorityPayload{SupervisorStartedFactDigest: digest("v2-supervisor-started"), OwnerEpoch: 1, PreviousAuthorityHead: digest("v2-authority-a"), AuthorityHead: digest("v2-authority-b")}
	request := requestV2{
		SchemaVersion: requestSchemaV2, ProtocolRevision: protocolRevisionV2, LaunchChildProtocolRevision: launchChildProtocolRevisionV2, MechanicsIdentity: mechanicsIdentityV2,
		SessionID: "session-v2", Command: CommandBindAuthority, CommandID: "bind-v2", Sequence: 1, PreviousCommandDigest: commandGenesisDigestV2,
		CurrentAuthorityHead: payload.PreviousAuthorityHead, Deadline: "2026-09-04T10:00:00Z", Payload: mustCanonical(payload),
	}
	var err error
	request.RequestDigest, err = digestValue(requestDigestInputV2{
		SchemaVersion: request.SchemaVersion, ProtocolRevision: request.ProtocolRevision, LaunchChildProtocolRevision: request.LaunchChildProtocolRevision,
		MechanicsIdentity: request.MechanicsIdentity, SessionID: request.SessionID, Command: request.Command, CommandID: request.CommandID, Sequence: request.Sequence,
		PreviousCommandDigest: request.PreviousCommandDigest, CurrentAuthorityHead: request.CurrentAuthorityHead, Deadline: request.Deadline, Payload: request.Payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func validResponseV2(t *testing.T, request requestV2) responseV2 {
	t.Helper()
	source := digest("v2-supervisor-started")
	observation, err := mechanicsObservationDigestV2(request.Command, source)
	if err != nil {
		t.Fatal(err)
	}
	result := MechanicsResult{Disposition: "ok", ReasonCode: "authority-bound", ObservationDigest: observation, Payload: canonicalEmptyPayload()}
	receipt, err := mechanicsReceiptDigestV2(result)
	if err != nil {
		t.Fatal(err)
	}
	commandHead, err := digestValue(struct {
		Previous string `json:"previousCommandDigest"`
		Request  string `json:"requestDigest"`
		Receipt  string `json:"receiptDigest"`
	}{request.PreviousCommandDigest, request.RequestDigest, receipt})
	if err != nil {
		t.Fatal(err)
	}
	return responseV2{
		SchemaVersion: responseSchemaV2, ProtocolRevision: protocolRevisionV2, LaunchChildProtocolRevision: launchChildProtocolRevisionV2, MechanicsIdentity: mechanicsIdentityV2,
		SessionID: request.SessionID, Command: request.Command, CommandID: request.CommandID, Sequence: request.Sequence, RequestDigest: request.RequestDigest,
		Status: "ok", ReasonCode: result.ReasonCode, ReceiptDigest: receipt, ObservationDigest: result.ObservationDigest, CommandHead: commandHead, Payload: mustCanonical(result),
	}
}
