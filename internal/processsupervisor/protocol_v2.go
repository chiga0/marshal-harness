package processsupervisor

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

// ADR 0079 freezes a complete protocol generation. These identities are
// deliberately distinct from the exported v1 constants while the v2 producer
// remains disabled during S2.
const (
	protocolRevisionV2            = "process-supervisor/v2"
	bootstrapSchemaV2             = "marshal.process-supervisor-bootstrap.v2"
	reconnectSchemaV2             = "marshal.process-supervisor-reconnect.v2"
	handshakeSchemaV2             = "marshal.process-supervisor-handshake.v2"
	requestSchemaV2               = "marshal.process-supervisor-request.v2"
	responseSchemaV2              = "marshal.process-supervisor-response.v2"
	journalSchemaV2               = "marshal.process-supervisor-journal.v2"
	launchChildProtocolRevisionV2 = "process-supervisor-launch-child/v2"
	launchChildSchemaV2           = "marshal.process-supervisor-launch-child.v2"
	observerIdentityV2            = "darwin-fixed-process-supervisor/v2"
	mechanicsIdentityV2           = "darwin-posix-spawn-setexec/v1"
	commandRecoveryRevisionV2     = "process-supervisor-command-recovery/v2"
	commandGenesisDigestV2        = "sha256:d2b74e69e8f7dc7d2f7718a9a1e3691dd2c32e295cd0a3a3f73daee769306ee9"
	journalGenesisDigestV2        = "sha256:24d02077bdcae6909a74214a4c722b0512c26ad001a610823e336fb592459dee"
	journalFileNameV2             = "process-supervisor-v2.journal"
)

// ProtocolGenerationContract is the path-free identity set frozen for one
// Supervisor protocol generation. It carries no authority and cannot enable a
// producer; S3 will consume the exact S2 contract after cutover admission.
type ProtocolGenerationContract struct {
	ProtocolRevision            string
	BootstrapSchema             string
	ReconnectSchema             string
	HandshakeSchema             string
	RequestSchema               string
	ResponseSchema              string
	JournalSchema               string
	LaunchChildProtocolRevision string
	LaunchChildSchema           string
	ObserverIdentity            string
	MechanicsIdentity           string
	CommandRecoveryRevision     string
	CommandGenesisDigest        string
	JournalGenesisDigest        string
	JournalFileName             string
}

var dormantV2ProtocolContract = ProtocolGenerationContract{
	ProtocolRevision:            protocolRevisionV2,
	BootstrapSchema:             bootstrapSchemaV2,
	ReconnectSchema:             reconnectSchemaV2,
	HandshakeSchema:             handshakeSchemaV2,
	RequestSchema:               requestSchemaV2,
	ResponseSchema:              responseSchemaV2,
	JournalSchema:               journalSchemaV2,
	LaunchChildProtocolRevision: launchChildProtocolRevisionV2,
	LaunchChildSchema:           launchChildSchemaV2,
	ObserverIdentity:            observerIdentityV2,
	MechanicsIdentity:           mechanicsIdentityV2,
	CommandRecoveryRevision:     commandRecoveryRevisionV2,
	CommandGenesisDigest:        commandGenesisDigestV2,
	JournalGenesisDigest:        journalGenesisDigestV2,
	JournalFileName:             journalFileNameV2,
}

// DormantV2ProtocolContract returns the S2 identity contract without
// selecting it for production. Returning a value prevents mutation of the
// package-owned contract.
func DormantV2ProtocolContract() ProtocolGenerationContract {
	return dormantV2ProtocolContract
}

// ValidateDormantV2ProtocolMessage applies the exact closed decoder for one
// S2 wire kind. It is deliberately byte-oriented: callers cannot construct a
// partially validated v2 object or reuse a v1 decoder.
func ValidateDormantV2ProtocolMessage(kind string, raw []byte) error {
	switch kind {
	case "bootstrap":
		var value bootstrapRequestV2
		if strictCanonicalDecode(raw, &value) != nil {
			return ErrInvalid
		}
		return value.validate()
	case "reconnect":
		var value reconnectRequestV2
		if strictCanonicalDecode(raw, &value) != nil {
			return ErrInvalid
		}
		return value.validate()
	case "handshake":
		var value handshakeResponseV2
		if strictCanonicalDecode(raw, &value) != nil {
			return ErrInvalid
		}
		return value.validate()
	case "request":
		var value requestV2
		if strictCanonicalDecode(raw, &value) != nil {
			return ErrInvalid
		}
		return value.validate()
	case "response":
		var value responseV2
		if strictCanonicalDecode(raw, &value) != nil {
			return ErrInvalid
		}
		return value.validate()
	case "launch-child":
		var value launchChildSpecV2
		if strictCanonicalDecode(raw, &value) != nil {
			return ErrInvalid
		}
		return value.validate()
	default:
		return ErrInvalid
	}
}

type launchChildObjectV2 struct {
	FD     int            `json:"fd"`
	Object HeldObjectSpec `json:"object"`
}

type launchChildSpecV2 struct {
	SchemaVersion               string                `json:"schemaVersion"`
	ProtocolRevision            string                `json:"protocolRevision"`
	LaunchChildProtocolRevision string                `json:"launchChildProtocolRevision"`
	MechanicsIdentity           string                `json:"mechanicsIdentity"`
	ParentPID                   int                   `json:"parentPid"`
	Runtime                     launchChildObjectV2   `json:"runtime"`
	WorkingDirectory            launchChildObjectV2   `json:"workingDirectory"`
	Marshal                     launchChildObjectV2   `json:"marshal"`
	MaterialRoots               []launchChildObjectV2 `json:"materialRoots"`
	LaunchMaterials             []launchChildObjectV2 `json:"launchMaterials"`
	Argv                        []string              `json:"argv"`
	Environment                 []string              `json:"environment"`
}

func (spec launchChildSpecV2) canonical() ([]byte, error) {
	if spec.validate() != nil {
		return nil, ErrInvalid
	}
	return canonicalValue(spec)
}

func (spec launchChildSpecV2) validate() error {
	const (
		workingDirectoryFD = 6
		runtimeFD          = 7
		marshalFD          = 8
		closureFD          = 9
	)
	if !validV2Binding(spec.SchemaVersion, launchChildSchemaV2, spec.ProtocolRevision, spec.LaunchChildProtocolRevision, spec.MechanicsIdentity) ||
		spec.ParentPID <= 1 || uint64(spec.ParentPID) > maxSafeJSONInteger || spec.Runtime.FD != runtimeFD || spec.WorkingDirectory.FD != workingDirectoryFD || spec.Marshal.FD != marshalFD ||
		spec.Runtime.Object.validate("runtime", "regular") != nil || spec.WorkingDirectory.Object.validate("working-directory", "directory") != nil || spec.Marshal.Object.validate("marshal", "regular") != nil ||
		len(spec.Argv) == 0 || len(spec.Argv) > MaxArgvEntries || spec.Argv[0] != spec.Runtime.Object.CanonicalPath || !filepath.IsAbs(spec.Argv[0]) || filepath.Clean(spec.Argv[0]) != spec.Argv[0] || len(spec.Environment) > MaxEnvironmentKeys {
		return ErrInvalid
	}
	argvBytes := 0
	for _, argument := range spec.Argv {
		if !utf8.ValidString(argument) || strings.ContainsRune(argument, 0) {
			return ErrInvalid
		}
		argvBytes += len(argument)
	}
	if argvBytes > MaxArgvBytes {
		return ErrInvalid
	}
	environmentBytes := 0
	previousKey := ""
	for _, entry := range spec.Environment {
		key, value, found := strings.Cut(entry, "=")
		if !found || !validEnvironmentKey(key) || !utf8.ValidString(value) || strings.ContainsRune(value, 0) || previousKey != "" && previousKey >= key {
			return ErrInvalid
		}
		previousKey = key
		environmentBytes += len(value)
	}
	if environmentBytes > MaxEnvironmentBytes {
		return ErrInvalid
	}
	next := closureFD
	roles := map[string]struct{}{"runtime": {}, "working-directory": {}, "marshal": {}}
	objects := map[[2]uint64]struct{}{
		{spec.Runtime.Object.Device, spec.Runtime.Object.Inode}:                   {},
		{spec.WorkingDirectory.Object.Device, spec.WorkingDirectory.Object.Inode}: {},
		{spec.Marshal.Object.Device, spec.Marshal.Object.Inode}:                   {},
	}
	if len(objects) != 3 {
		return ErrInvalid
	}
	for index, object := range append(append([]launchChildObjectV2(nil), spec.MaterialRoots...), spec.LaunchMaterials...) {
		kind := "directory"
		if index >= len(spec.MaterialRoots) {
			kind = "regular"
		}
		identity := [2]uint64{object.Object.Device, object.Object.Inode}
		if object.FD != next || !validMaterialRole(object.Object.Role) || object.Object.validate(object.Object.Role, kind) != nil {
			return ErrInvalid
		}
		if _, exists := roles[object.Object.Role]; exists {
			return ErrInvalid
		}
		if _, exists := objects[identity]; exists {
			return ErrInvalid
		}
		roles[object.Object.Role] = struct{}{}
		objects[identity] = struct{}{}
		next++
	}
	return nil
}

// ValidateDormantV2ResponseBinding proves that two exact canonical v2 frames
// belong to the same command. It performs no transport or journal mutation.
func ValidateDormantV2ResponseBinding(responseRaw, requestRaw []byte) error {
	var response responseV2
	var request requestV2
	if strictCanonicalDecode(responseRaw, &response) != nil || strictCanonicalDecode(requestRaw, &request) != nil {
		return ErrInvalid
	}
	return validateV2ResponseBinding(response, request)
}

type bootstrapRequestV2 struct {
	SchemaVersion               string                   `json:"schemaVersion"`
	ProtocolRevision            string                   `json:"protocolRevision"`
	LaunchChildProtocolRevision string                   `json:"launchChildProtocolRevision"`
	MechanicsIdentity           string                   `json:"mechanicsIdentity"`
	SessionID                   string                   `json:"sessionId"`
	SessionNonce                string                   `json:"sessionNonce"`
	OwnerEpoch                  uint64                   `json:"ownerEpoch"`
	Authority                   AuthorityTuple           `json:"authority"`
	LaunchAuthorizedFact        string                   `json:"launchAuthorizedFactDigest"`
	CurrentAuthorityHead        string                   `json:"currentAuthorityHead"`
	ControlDirectoryIdentity    ControlDirectoryIdentity `json:"controlDirectoryIdentity"`
	Core                        CoreIdentity             `json:"core"`
}

func (request bootstrapRequestV2) validate() error {
	if !validV2Binding(request.SchemaVersion, bootstrapSchemaV2, request.ProtocolRevision, request.LaunchChildProtocolRevision, request.MechanicsIdentity) ||
		!validID(request.SessionID) || !hex64Pattern.MatchString(request.SessionNonce) || request.OwnerEpoch == 0 || request.OwnerEpoch > maxSafeJSONInteger ||
		!validDigest(request.LaunchAuthorizedFact) || !validDigest(request.CurrentAuthorityHead) || request.Authority.validate() != nil ||
		request.ControlDirectoryIdentity.validate() != nil || request.Core.UID == 0 || request.Core.Process.validate() != nil || request.Core.Binary.validate() != nil {
		return ErrInvalid
	}
	return nil
}

type reconnectRequestV2 struct {
	SchemaVersion                  string       `json:"schemaVersion"`
	ProtocolRevision               string       `json:"protocolRevision"`
	LaunchChildProtocolRevision    string       `json:"launchChildProtocolRevision"`
	MechanicsIdentity              string       `json:"mechanicsIdentity"`
	SessionID                      string       `json:"sessionId"`
	SessionNonce                   string       `json:"sessionNonce"`
	PreviousOwnerEpoch             uint64       `json:"previousOwnerEpoch"`
	OwnerEpoch                     uint64       `json:"ownerEpoch"`
	PreviousAuthorityHead          string       `json:"previousAuthorityHead"`
	CurrentAuthorityHead           string       `json:"currentAuthorityHead"`
	ControlOwnerAcquiredFactDigest string       `json:"controlOwnerAcquiredFactDigest"`
	Core                           CoreIdentity `json:"core"`
	LastOwnerEpoch                 uint64       `json:"lastOwnerEpoch"`
	LastAuthorityHead              string       `json:"lastAuthorityHead"`
	LastCommandSequence            uint64       `json:"lastCommandSequence"`
	LastCommandHead                string       `json:"lastCommandHead"`
	LastJournalSequence            uint64       `json:"lastJournalSequence"`
	LastJournalHead                string       `json:"lastJournalHead"`
	PendingRequest                 *requestV2   `json:"pendingRequest,omitempty"`
}

func (request reconnectRequestV2) validate() error {
	if !validV2Binding(request.SchemaVersion, reconnectSchemaV2, request.ProtocolRevision, request.LaunchChildProtocolRevision, request.MechanicsIdentity) ||
		!validID(request.SessionID) || !hex64Pattern.MatchString(request.SessionNonce) || request.PreviousOwnerEpoch == 0 || request.PreviousOwnerEpoch > maxSafeJSONInteger ||
		request.OwnerEpoch <= request.PreviousOwnerEpoch || request.OwnerEpoch > maxSafeJSONInteger || !validDigest(request.PreviousAuthorityHead) || !validDigest(request.CurrentAuthorityHead) ||
		!validDigest(request.ControlOwnerAcquiredFactDigest) || request.Core.UID == 0 || request.Core.Process.validate() != nil || request.Core.Binary.validate() != nil ||
		request.LastOwnerEpoch == 0 || request.LastOwnerEpoch > maxSafeJSONInteger || !validDigest(request.LastAuthorityHead) || request.LastCommandSequence > maxSafeJSONInteger ||
		!validDigest(request.LastCommandHead) || request.LastJournalSequence == 0 || request.LastJournalSequence > maxSafeJSONInteger || !validDigest(request.LastJournalHead) {
		return ErrInvalid
	}
	if request.PendingRequest != nil && request.PendingRequest.validate() != nil {
		return ErrInvalid
	}
	return nil
}

type handshakeResponseV2 struct {
	SchemaVersion               string                `json:"schemaVersion"`
	ProtocolRevision            string                `json:"protocolRevision"`
	LaunchChildProtocolRevision string                `json:"launchChildProtocolRevision"`
	MechanicsIdentity           string                `json:"mechanicsIdentity"`
	Status                      string                `json:"status"`
	ReasonCode                  string                `json:"reasonCode"`
	SessionID                   string                `json:"sessionId"`
	SessionNonceDigest          string                `json:"sessionNonceDigest"`
	OwnerEpoch                  uint64                `json:"ownerEpoch"`
	CurrentAuthorityHead        string                `json:"currentAuthorityHead"`
	CommandSequence             uint64                `json:"commandSequence"`
	CommandHead                 string                `json:"commandHead"`
	JournalSequence             uint64                `json:"journalSequence"`
	JournalHead                 string                `json:"journalHead"`
	ObserverIdentity            string                `json:"observerIdentity"`
	ObservedAt                  string                `json:"observedAt"`
	SupervisorProcess           ProcessIdentity       `json:"supervisorProcess"`
	SupervisorBinary            BinaryIdentity        `json:"supervisorBinary"`
	ControlSocket               ControlSocketIdentity `json:"controlSocket"`
	ControlFiles                SessionControlFiles   `json:"controlFiles"`
	Reconciliation              ReconciliationState   `json:"reconciliation,omitempty"`
	ReplayedResponse            *responseV2           `json:"replayedResponse,omitempty"`
}

func (response handshakeResponseV2) validate() error {
	observedAt, err := time.Parse(time.RFC3339Nano, response.ObservedAt)
	if err != nil || observedAt.Location() != time.UTC || observedAt.Format(time.RFC3339Nano) != response.ObservedAt ||
		!validV2Binding(response.SchemaVersion, handshakeSchemaV2, response.ProtocolRevision, response.LaunchChildProtocolRevision, response.MechanicsIdentity) ||
		response.Status != "ok" || !validID(response.ReasonCode) || !validID(response.SessionID) || !validDigest(response.SessionNonceDigest) ||
		response.OwnerEpoch == 0 || response.OwnerEpoch > maxSafeJSONInteger || !validDigest(response.CurrentAuthorityHead) || response.CommandSequence > maxSafeJSONInteger ||
		!validDigest(response.CommandHead) || response.JournalSequence == 0 || response.JournalSequence > maxSafeJSONInteger || !validDigest(response.JournalHead) ||
		response.ObserverIdentity != observerIdentityV2 || response.SupervisorProcess.validate() != nil || response.SupervisorBinary.validate() != nil ||
		response.ControlSocket.validate() != nil || response.ControlFiles.validate() != nil {
		return ErrInvalid
	}
	switch response.Reconciliation {
	case "":
		if response.ReplayedResponse != nil {
			return ErrInvalid
		}
	case ReconciliationUnchanged:
		if response.ReplayedResponse != nil && response.ReplayedResponse.validate() != nil {
			return ErrInvalid
		}
	case ReconciliationIntentPending:
		if response.ReplayedResponse != nil {
			return ErrInvalid
		}
	case ReconciliationReceiptCommitted:
		if response.ReplayedResponse == nil || response.ReplayedResponse.validate() != nil {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

type requestV2 struct {
	SchemaVersion               string          `json:"schemaVersion"`
	ProtocolRevision            string          `json:"protocolRevision"`
	LaunchChildProtocolRevision string          `json:"launchChildProtocolRevision"`
	MechanicsIdentity           string          `json:"mechanicsIdentity"`
	SessionID                   string          `json:"sessionId"`
	Command                     CommandName     `json:"command"`
	CommandID                   string          `json:"commandId"`
	Sequence                    uint64          `json:"sequence"`
	PreviousCommandDigest       string          `json:"previousCommandDigest"`
	CurrentAuthorityHead        string          `json:"currentAuthorityHead"`
	RequestDigest               string          `json:"requestDigest"`
	Deadline                    string          `json:"deadline"`
	Payload                     json.RawMessage `json:"payload"`
}

type requestDigestInputV2 struct {
	SchemaVersion               string          `json:"schemaVersion"`
	ProtocolRevision            string          `json:"protocolRevision"`
	LaunchChildProtocolRevision string          `json:"launchChildProtocolRevision"`
	MechanicsIdentity           string          `json:"mechanicsIdentity"`
	SessionID                   string          `json:"sessionId"`
	Command                     CommandName     `json:"command"`
	CommandID                   string          `json:"commandId"`
	Sequence                    uint64          `json:"sequence"`
	PreviousCommandDigest       string          `json:"previousCommandDigest"`
	CurrentAuthorityHead        string          `json:"currentAuthorityHead"`
	Deadline                    string          `json:"deadline"`
	Payload                     json.RawMessage `json:"payload"`
}

func (request requestV2) validate() error {
	if !validV2Binding(request.SchemaVersion, requestSchemaV2, request.ProtocolRevision, request.LaunchChildProtocolRevision, request.MechanicsIdentity) ||
		!validID(request.SessionID) || !validCommand(request.Command) || !validID(request.CommandID) || request.Sequence == 0 || request.Sequence > maxSafeJSONInteger ||
		!validDigest(request.PreviousCommandDigest) || !validDigest(request.CurrentAuthorityHead) || !validDigest(request.RequestDigest) {
		return ErrInvalid
	}
	if _, err := parseDeadline(request.Deadline); err != nil {
		return ErrInvalid
	}
	projection := requestProjection{Command: request.Command, CommandID: request.CommandID, Sequence: request.Sequence, RequestDigest: request.RequestDigest, PreviousCommandDigest: request.PreviousCommandDigest, CurrentAuthorityHead: request.CurrentAuthorityHead, Deadline: request.Deadline}
	if _, err := decodePayload(request.Command, request.Payload, &projection); err != nil || validateProjection(projection) != nil {
		return ErrInvalid
	}
	want, err := digestValue(requestDigestInputV2{
		SchemaVersion: request.SchemaVersion, ProtocolRevision: request.ProtocolRevision, LaunchChildProtocolRevision: request.LaunchChildProtocolRevision,
		MechanicsIdentity: request.MechanicsIdentity, SessionID: request.SessionID, Command: request.Command, CommandID: request.CommandID, Sequence: request.Sequence,
		PreviousCommandDigest: request.PreviousCommandDigest, CurrentAuthorityHead: request.CurrentAuthorityHead, Deadline: request.Deadline, Payload: request.Payload,
	})
	if err != nil || want != request.RequestDigest {
		return ErrConflict
	}
	return nil
}

type responseV2 struct {
	SchemaVersion               string          `json:"schemaVersion"`
	ProtocolRevision            string          `json:"protocolRevision"`
	LaunchChildProtocolRevision string          `json:"launchChildProtocolRevision"`
	MechanicsIdentity           string          `json:"mechanicsIdentity"`
	SessionID                   string          `json:"sessionId"`
	Command                     CommandName     `json:"command"`
	CommandID                   string          `json:"commandId"`
	Sequence                    uint64          `json:"sequence"`
	RequestDigest               string          `json:"requestDigest"`
	Status                      string          `json:"status"`
	ReasonCode                  string          `json:"reasonCode"`
	ReceiptDigest               string          `json:"receiptDigest"`
	ObservationDigest           string          `json:"observationDigest"`
	CommandHead                 string          `json:"commandHead"`
	Payload                     json.RawMessage `json:"payload"`
}

type mechanicsReceiptInputV2 struct {
	SchemaVersion               string          `json:"schemaVersion"`
	ProtocolRevision            string          `json:"protocolRevision"`
	LaunchChildProtocolRevision string          `json:"launchChildProtocolRevision"`
	MechanicsIdentity           string          `json:"mechanicsIdentity"`
	Result                      MechanicsResult `json:"result"`
}

type mechanicsObservationInputV2 struct {
	SchemaVersion               string      `json:"schemaVersion"`
	ProtocolRevision            string      `json:"protocolRevision"`
	LaunchChildProtocolRevision string      `json:"launchChildProtocolRevision"`
	MechanicsIdentity           string      `json:"mechanicsIdentity"`
	ObserverIdentity            string      `json:"observerIdentity"`
	Command                     CommandName `json:"command"`
	SourceDigest                string      `json:"sourceDigest"`
}

func mechanicsReceiptDigestV2(result MechanicsResult) (string, error) {
	return digestValue(mechanicsReceiptInputV2{
		SchemaVersion: responseSchemaV2, ProtocolRevision: protocolRevisionV2, LaunchChildProtocolRevision: launchChildProtocolRevisionV2,
		MechanicsIdentity: mechanicsIdentityV2, Result: result,
	})
}

func mechanicsObservationDigestV2(command CommandName, sourceDigest string) (string, error) {
	if !validCommand(command) || !validDigest(sourceDigest) {
		return "", ErrInvalid
	}
	return digestValue(mechanicsObservationInputV2{
		SchemaVersion: responseSchemaV2, ProtocolRevision: protocolRevisionV2, LaunchChildProtocolRevision: launchChildProtocolRevisionV2,
		MechanicsIdentity: mechanicsIdentityV2, ObserverIdentity: observerIdentityV2, Command: command, SourceDigest: sourceDigest,
	})
}

func (response responseV2) validate() error {
	if !validV2Binding(response.SchemaVersion, responseSchemaV2, response.ProtocolRevision, response.LaunchChildProtocolRevision, response.MechanicsIdentity) ||
		!validID(response.SessionID) || !validCommand(response.Command) || !validID(response.CommandID) || response.Sequence == 0 || response.Sequence > maxSafeJSONInteger ||
		!validDigest(response.RequestDigest) || response.Status != "ok" && response.Status != "rejected" || !validID(response.ReasonCode) ||
		!validDigest(response.ReceiptDigest) || !validDigest(response.ObservationDigest) || !validDigest(response.CommandHead) {
		return ErrInvalid
	}
	var result MechanicsResult
	if strictCanonicalDecode(response.Payload, &result) != nil || validateMechanicsResult(result) != nil || result.ReasonCode != response.ReasonCode || result.ObservationDigest != response.ObservationDigest {
		return ErrInvalid
	}
	receipt, err := mechanicsReceiptDigestV2(result)
	if err != nil || receipt != response.ReceiptDigest || response.Status == "ok" && result.Disposition != "ok" || response.Status == "rejected" && result.Disposition != "rejected" {
		return ErrConflict
	}
	return nil
}

func validateV2ResponseBinding(response responseV2, request requestV2) error {
	if request.validate() != nil || response.validate() != nil || response.SessionID != request.SessionID || response.Command != request.Command || response.CommandID != request.CommandID || response.Sequence != request.Sequence || response.RequestDigest != request.RequestDigest {
		return ErrConflict
	}
	projection := requestProjection{Command: request.Command, CommandID: request.CommandID, Sequence: request.Sequence, RequestDigest: request.RequestDigest, PreviousCommandDigest: request.PreviousCommandDigest, CurrentAuthorityHead: request.CurrentAuthorityHead, Deadline: request.Deadline}
	_, err := decodePayload(request.Command, request.Payload, &projection)
	if err != nil || validateV2ResponseObservation(response, projection) != nil {
		return ErrConflict
	}
	commandHead, err := digestValue(struct {
		Previous string `json:"previousCommandDigest"`
		Request  string `json:"requestDigest"`
		Receipt  string `json:"receiptDigest"`
	}{request.PreviousCommandDigest, request.RequestDigest, response.ReceiptDigest})
	if err != nil || commandHead != response.CommandHead {
		return ErrConflict
	}
	return nil
}

func validateV2ResponseObservation(response responseV2, request requestProjection) error {
	var result MechanicsResult
	if strictCanonicalDecode(response.Payload, &result) != nil {
		return ErrConflict
	}
	source, err := v2ObservationSource(response.Status, response.Command, result, request)
	if err != nil {
		return ErrConflict
	}
	want, err := mechanicsObservationDigestV2(response.Command, source)
	if err != nil || want != response.ObservationDigest || want != result.ObservationDigest {
		return ErrConflict
	}
	return nil
}

func v2ObservationSource(status string, command CommandName, result MechanicsResult, request requestProjection) (string, error) {
	emptyTranscript := result.TranscriptDigest == "" && result.StdoutBytes == 0 && result.StderrBytes == 0 && !result.Truncated
	if status == "rejected" {
		if string(result.Payload) != "{}" || !emptyTranscript {
			return "", ErrInvalid
		}
		return digestValue(struct {
			Disposition string `json:"disposition"`
			ReasonCode  string `json:"reasonCode"`
		}{result.Disposition, result.ReasonCode})
	}
	switch command {
	case CommandBindAuthority:
		if string(result.Payload) != "{}" || !emptyTranscript || !validDigest(request.SupervisorStartedFactDigest) {
			return "", ErrInvalid
		}
		return request.SupervisorStartedFactDigest, nil
	case CommandAbortUnbound:
		if string(result.Payload) != "{}" || !emptyTranscript || !validDigest(request.AuthorityAbsenceProofDigest) {
			return "", ErrInvalid
		}
		return request.AuthorityAbsenceProofDigest, nil
	default:
		var report ProcessReport
		if strictCanonicalDecode(result.Payload, &report) != nil || ValidateDormantV2ProcessReport(report) != nil {
			return "", ErrInvalid
		}
		digest, err := digestValue(report)
		if err != nil {
			return "", ErrInvalid
		}
		if command == CommandCollect {
			if result.TranscriptDigest != digest || result.StdoutBytes != report.StdoutBytes || result.StderrBytes != report.StderrBytes || result.Truncated != report.TranscriptTruncated {
				return "", ErrInvalid
			}
		} else if !emptyTranscript {
			return "", ErrInvalid
		}
		return digest, nil
	}
}

// ValidateDormantV2ProcessReport applies the exact v2 observer contract while
// the producer is disabled. Historical v1 reports continue through the v1
// validator and can never be promoted into v2 evidence.
func ValidateDormantV2ProcessReport(report ProcessReport) error {
	observedAt, err := time.Parse(time.RFC3339Nano, report.ObservedAt)
	birth := time.Unix(report.Process.BirthSeconds, report.Process.BirthMicroseconds*int64(time.Microsecond)).UTC()
	if err != nil || observedAt.Location() != time.UTC || observedAt.Format(time.RFC3339Nano) != report.ObservedAt || observedAt.Before(birth) ||
		report.ObserverIdentity != observerIdentityV2 || report.Process.validate() != nil || !validDigest(report.RuntimeObjectDigest) || !validDigest(report.WorkingObjectDigest) ||
		report.SourceGateRevision != SourceGateRevisionV1 || !validDigest(report.ExactSetDigest) || report.ExitCode < -1 || uint64(maxInt(report.ExitCode, 0)) > maxSafeJSONInteger ||
		report.StdoutBytes > uint64(MaxStdoutBytes) || report.StderrBytes > uint64(MaxStderrBytes) || report.StdoutBytes+report.StderrBytes > uint64(MaxTranscriptBytes) {
		return ErrInvalid
	}
	switch report.State {
	case "exec-stopped", "running":
		if report.ExitCode != 0 || report.Signal != "" || report.StdoutDigest != "" || report.StderrDigest != "" || report.StdoutBytes != 0 || report.StderrBytes != 0 || report.TranscriptTruncated {
			return ErrInvalid
		}
	case "terminal":
		if report.Signal != "" && !validID(report.Signal) {
			return ErrInvalid
		}
		if (report.StdoutDigest == "") != (report.StderrDigest == "") || report.StdoutDigest == "" && (report.StdoutBytes != 0 || report.StderrBytes != 0 || report.TranscriptTruncated) || report.StdoutDigest != "" && (!validDigest(report.StdoutDigest) || !validDigest(report.StderrDigest)) {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

func validV2Binding(schema, expectedSchema, protocol, launchChildProtocol, mechanics string) bool {
	return schema == expectedSchema && protocol == protocolRevisionV2 && launchChildProtocol == launchChildProtocolRevisionV2 && mechanics == mechanicsIdentityV2
}
