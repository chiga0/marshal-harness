// Package processsupervisor implements the fixed Marshal process-supervisor
// mechanics protocol frozen by ADR 0059. It owns process mechanics only; it
// never writes the Marshal business authority ledger or decides lifecycle
// outcomes.
package processsupervisor

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"regexp"
	"time"

	"github.com/chiga0/marshal-harness/internal/launchidentity"
)

const (
	ProtocolRevision = "process-supervisor/v1"
	BootstrapSchema  = "marshal.process-supervisor-bootstrap.v1"
	ReconnectSchema  = "marshal.process-supervisor-reconnect.v1"
	HandshakeSchema  = "marshal.process-supervisor-handshake.v1"
	ResponseSchema   = "marshal.process-supervisor-response.v1"
	JournalSchema    = "marshal.process-supervisor-journal.v1"

	MaxWireFrameBytes    = 1 << 20
	MaxJournalPayload    = 256 << 10
	MaxJournalFileBytes  = 64 << 20
	MaxCommands          = 4096
	MaxArgvEntries       = 256
	MaxArgvBytes         = 256 << 10
	MaxEnvironmentKeys   = 128
	MaxEnvironmentKeyLen = 128
	MaxEnvironmentBytes  = 256 << 10
	MaxStdinBytes        = 1 << 20
	MaxStdoutBytes       = 16 << 20
	MaxStderrBytes       = 16 << 20
	MaxTranscriptBytes   = 32 << 20
	MaxDiagnosticBytes   = 64 << 10
	// RFC 8785 uses the I-JSON number domain. Reject integer values that cannot
	// be represented exactly by an IEEE-754 binary64 implementation.
	maxSafeJSONInteger = uint64(1<<53 - 1)
)

// SourceGateRevisionV1 marks a fresh S1 spawn whose current source and
// allocation live identity must be admitted by the mutation-adjacent exact
// enumeration gate. An empty revision is retained solely for historical v1
// wire/journal replay and never qualifies as S1 authority evidence.
const SourceGateRevisionV1 = "darwin-source-gate/v1"

type ReconciliationState string

const (
	ReconciliationUnchanged        ReconciliationState = "unchanged"
	ReconciliationIntentPending    ReconciliationState = "exact-intent-pending"
	ReconciliationReceiptCommitted ReconciliationState = "exact-receipt-committed"
)

type CommandName string

const (
	CommandBindAuthority CommandName = "bind-authority"
	CommandAbortUnbound  CommandName = "abort-unbound"
	CommandSpawn         CommandName = "spawn"
	CommandResume        CommandName = "resume"
	CommandInspect       CommandName = "inspect"
	CommandTerminate     CommandName = "terminate"
	CommandCollect       CommandName = "collect"
	CommandClose         CommandName = "close"
)

var (
	digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	idPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@/-]{0,255}$`)
	hex40Pattern  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	hex64Pattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)

	ErrUnavailable   = &ProtocolError{ReasonCode: "process-supervisor-platform-unavailable"}
	ErrInvalid       = &ProtocolError{ReasonCode: "process-supervisor-protocol-invalid"}
	ErrConflict      = &ProtocolError{ReasonCode: "process-supervisor-identity-conflict"}
	ErrIntervention  = &ProtocolError{ReasonCode: "process-supervisor-intervention-required"}
	ErrMechanicsOpen = &ProtocolError{ReasonCode: "process-supervisor-mechanics-unavailable"}
)

// ProtocolError exposes only a closed non-sensitive reason code. It never
// wraps payload-derived errors.
type ProtocolError struct {
	ReasonCode string
}

func (e *ProtocolError) Error() string {
	if e == nil || e.ReasonCode == "" {
		return "process supervisor rejected"
	}
	return "process supervisor rejected: " + e.ReasonCode
}

func reject(reason string) error { return &ProtocolError{ReasonCode: reason} }

func ReasonCode(err error) string {
	var closed interface{ closedReasonCode() string }
	if errors.As(err, &closed) && validID(closed.closedReasonCode()) {
		return closed.closedReasonCode()
	}
	var protocol *ProtocolError
	if errors.As(err, &protocol) && protocol.ReasonCode != "" {
		return protocol.ReasonCode
	}
	return ErrInvalid.ReasonCode
}

// AuthorityTuple is the closed Attempt/allocation/lease identity frozen for
// one supervisor session.
type AuthorityTuple struct {
	AuthorityNamespaceID string `json:"authorityNamespaceId"`
	TaskID               string `json:"taskId"`
	RunID                string `json:"runId"`
	AttemptID            string `json:"attemptId"`
	AllocationID         string `json:"allocationId"`
	LeaseID              string `json:"leaseId"`
	LeaseDigest          string `json:"leaseDigest"`
	Generation           uint64 `json:"generation"`
	FencingTokenDigest   string `json:"fencingTokenDigest"`
	OrchestratorID       string `json:"orchestratorId"`
}

func (tuple AuthorityTuple) validate() error {
	for _, value := range []string{tuple.AuthorityNamespaceID, tuple.TaskID, tuple.RunID, tuple.AttemptID, tuple.AllocationID, tuple.LeaseID, tuple.OrchestratorID} {
		if !validID(value) {
			return ErrInvalid
		}
	}
	if tuple.Generation == 0 || tuple.Generation > maxSafeJSONInteger || !validDigest(tuple.LeaseDigest) || !validDigest(tuple.FencingTokenDigest) {
		return ErrInvalid
	}
	return nil
}

// BinaryIdentity is an adjacent observation of one fixed Marshal binary. The
// peer observer compares every field and requires the peer bytes/CDHash to be
// identical to the supervisor's fixed binary.
type BinaryIdentity struct {
	CanonicalPath string `json:"canonicalPath"`
	Device        uint64 `json:"device"`
	Inode         uint64 `json:"inode"`
	FileType      string `json:"fileType"`
	UID           uint32 `json:"uid"`
	GID           uint32 `json:"gid"`
	Mode          uint32 `json:"mode"`
	LinkCount     uint64 `json:"linkCount"`
	Size          int64  `json:"size"`
	RawSHA256     string `json:"rawSHA256"`
	CDHash        string `json:"cdHash"`
	SourceHead    string `json:"sourceHead"`
	SelfProfile   string `json:"selfProfile"`
}

func (identity BinaryIdentity) validate() error {
	if !filepath.IsAbs(identity.CanonicalPath) || filepath.Clean(identity.CanonicalPath) != identity.CanonicalPath || identity.FileType != "regular" || identity.Device == 0 || identity.Inode == 0 ||
		!safeUint64(identity.Device) || !safeUint64(identity.Inode) || identity.Size <= 0 || uint64(identity.Size) > maxSafeJSONInteger || identity.LinkCount != 1 || identity.Mode&0o170000 != 0o100000 || identity.Mode&0o111 == 0 || identity.Mode&0o6000 != 0 || !validDigest(identity.RawSHA256) ||
		!hex40Pattern.MatchString(identity.CDHash) || identity.CDHash == "0000000000000000000000000000000000000000" || !hex40Pattern.MatchString(identity.SourceHead) || !validID(identity.SelfProfile) {
		return ErrInvalid
	}
	return nil
}

func sameBinaryObject(left, right BinaryIdentity) bool {
	return left.CanonicalPath == right.CanonicalPath && left.Device == right.Device && left.Inode == right.Inode && left.FileType == right.FileType && left.UID == right.UID && left.GID == right.GID && left.Mode == right.Mode && left.LinkCount == right.LinkCount && left.Size == right.Size && left.RawSHA256 == right.RawSHA256 && left.CDHash == right.CDHash && left.SourceHead == right.SourceHead && left.SelfProfile == right.SelfProfile
}

type ProcessIdentity struct {
	PID               int   `json:"pid"`
	BirthSeconds      int64 `json:"birthSeconds"`
	BirthMicroseconds int64 `json:"birthMicroseconds"`
	SessionID         int   `json:"sessionId"`
	ProcessGroupID    int   `json:"processGroupId"`
}

func (identity ProcessIdentity) validate() error {
	if identity.PID <= 0 || uint64(identity.PID) > maxSafeJSONInteger || identity.BirthSeconds <= 0 || uint64(identity.BirthSeconds) > maxSafeJSONInteger || identity.BirthMicroseconds < 0 || identity.BirthMicroseconds >= 1_000_000 || identity.SessionID <= 0 || uint64(identity.SessionID) > maxSafeJSONInteger || identity.ProcessGroupID <= 0 || uint64(identity.ProcessGroupID) > maxSafeJSONInteger {
		return ErrInvalid
	}
	return nil
}

// CoreIdentity is asserted by Core and independently checked against kernel
// peer credentials, birth identity and the fixed Marshal binary.
type CoreIdentity struct {
	UID     uint32          `json:"uid"`
	GID     uint32          `json:"gid"`
	Process ProcessIdentity `json:"process"`
	Binary  BinaryIdentity  `json:"binary"`
}

type ControlDirectoryIdentity struct {
	CanonicalPath string `json:"canonicalPath"`
	Device        uint64 `json:"device"`
	Inode         uint64 `json:"inode"`
	FileType      string `json:"fileType"`
	UID           uint32 `json:"uid"`
	GID           uint32 `json:"gid"`
	Mode          uint32 `json:"mode"`
	LinkCount     uint64 `json:"linkCount"`
}

// ControlSocketIdentity freezes the descriptor-relative rendezvous object
// created by the supervisor. The listening descriptor keeps the object live;
// Core binds this complete fstatat observation into the started fact and every
// reconnect handshake so a pathname ABA cannot silently acquire authority.
type ControlSocketIdentity struct {
	Device    uint64 `json:"device"`
	Inode     uint64 `json:"inode"`
	FileType  string `json:"fileType"`
	UID       uint32 `json:"uid"`
	GID       uint32 `json:"gid"`
	Mode      uint32 `json:"mode"`
	LinkCount uint64 `json:"linkCount"`
}

// ControlFileIdentity freezes one descriptor-relative regular control object.
// Size is deliberately excluded: the journal grows while its object identity
// remains stable. Callers validate nonce and journal lengths separately.
type ControlFileIdentity struct {
	Device    uint64 `json:"device"`
	Inode     uint64 `json:"inode"`
	FileType  string `json:"fileType"`
	UID       uint32 `json:"uid"`
	GID       uint32 `json:"gid"`
	Mode      uint32 `json:"mode"`
	LinkCount uint64 `json:"linkCount"`
}

func (identity ControlFileIdentity) validate() error {
	if identity.Device == 0 || identity.Inode == 0 || !safeUint64(identity.Device) || !safeUint64(identity.Inode) || identity.FileType != "regular" || identity.UID == 0 || identity.Mode&0o170000 != 0o100000 || identity.Mode&0o777 != 0o600 || identity.LinkCount != 1 {
		return ErrInvalid
	}
	return nil
}

// SessionControlFiles binds the nonce and mechanics journal objects. Neither
// identity contains the nonce bytes or journal contents.
type SessionControlFiles struct {
	Nonce   ControlFileIdentity `json:"nonce"`
	Journal ControlFileIdentity `json:"journal"`
}

func (files SessionControlFiles) validate() error {
	if files.Nonce.validate() != nil || files.Journal.validate() != nil || files.Nonce == files.Journal {
		return ErrInvalid
	}
	return nil
}

func ValidateSessionControlFiles(files SessionControlFiles) error { return files.validate() }

func (identity ControlSocketIdentity) validate() error {
	if identity.Device == 0 || identity.Inode == 0 || !safeUint64(identity.Device) || !safeUint64(identity.Inode) || identity.FileType != "socket" || identity.UID == 0 || identity.Mode&0o170000 != 0o140000 || identity.Mode&0o777 != 0o600 || identity.LinkCount != 1 {
		return ErrInvalid
	}
	return nil
}

func (identity ControlDirectoryIdentity) validate() error {
	if !filepath.IsAbs(identity.CanonicalPath) || filepath.Clean(identity.CanonicalPath) != identity.CanonicalPath || identity.Device == 0 || identity.Inode == 0 || !safeUint64(identity.Device) || !safeUint64(identity.Inode) || identity.FileType != "directory" || identity.LinkCount < 2 || !safeUint64(identity.LinkCount) || identity.Mode&0o170000 != 0o040000 || identity.Mode&0o077 != 0 {
		return ErrInvalid
	}
	return nil
}

// BootstrapRequest travels only on the inherited bootstrap socket. The raw
// nonce and control path never appear in argv, environment, journal or logs.
type BootstrapRequest struct {
	SchemaVersion            string                   `json:"schemaVersion"`
	ProtocolRevision         string                   `json:"protocolRevision"`
	SessionID                string                   `json:"sessionId"`
	SessionNonce             string                   `json:"sessionNonce"`
	OwnerEpoch               uint64                   `json:"ownerEpoch"`
	Authority                AuthorityTuple           `json:"authority"`
	LaunchAuthorizedFact     string                   `json:"launchAuthorizedFactDigest"`
	CurrentAuthorityHead     string                   `json:"currentAuthorityHead"`
	ControlDirectoryIdentity ControlDirectoryIdentity `json:"controlDirectoryIdentity"`
	Core                     CoreIdentity             `json:"core"`
}

func (request BootstrapRequest) validate() error {
	if request.SchemaVersion != BootstrapSchema || request.ProtocolRevision != ProtocolRevision || !validID(request.SessionID) ||
		!hex64Pattern.MatchString(request.SessionNonce) || request.OwnerEpoch == 0 || request.OwnerEpoch > maxSafeJSONInteger || !validDigest(request.LaunchAuthorizedFact) || !validDigest(request.CurrentAuthorityHead) {
		return ErrInvalid
	}
	if err := request.Authority.validate(); err != nil {
		return err
	}
	if err := request.ControlDirectoryIdentity.validate(); err != nil {
		return err
	}
	if request.Core.UID == 0 {
		// root is not part of the ordinary-user profile.
		return ErrInvalid
	}
	if err := request.Core.Process.validate(); err != nil {
		return err
	}
	return request.Core.Binary.validate()
}

// reconnectRequest is the private wire-only reconnect shape. The raw nonce is
// loaded from a held descriptor by Reconnect and never enters a public API.
type reconnectRequest struct {
	SchemaVersion         string       `json:"schemaVersion"`
	ProtocolRevision      string       `json:"protocolRevision"`
	SessionID             string       `json:"sessionId"`
	SessionNonce          string       `json:"sessionNonce"`
	PreviousOwnerEpoch    uint64       `json:"previousOwnerEpoch"`
	OwnerEpoch            uint64       `json:"ownerEpoch"`
	PreviousAuthorityHead string       `json:"previousAuthorityHead"`
	CurrentAuthorityHead  string       `json:"currentAuthorityHead"`
	ControlOwnerAcquired  string       `json:"controlOwnerAcquiredFactDigest"`
	Core                  CoreIdentity `json:"core"`
	LastOwnerEpoch        uint64       `json:"lastOwnerEpoch"`
	LastAuthorityHead     string       `json:"lastAuthorityHead"`
	LastCommandSequence   uint64       `json:"lastCommandSequence"`
	LastCommandHead       string       `json:"lastCommandHead"`
	LastJournalSequence   uint64       `json:"lastJournalSequence"`
	LastJournalHead       string       `json:"lastJournalHead"`
	PendingRequest        *Request     `json:"pendingRequest,omitempty"`
}

// HandshakeResponse is safe for the authenticated control socket. It contains
// no raw nonce or command payload.
type HandshakeResponse struct {
	SchemaVersion        string                `json:"schemaVersion"`
	ProtocolRevision     string                `json:"protocolRevision"`
	Status               string                `json:"status"`
	ReasonCode           string                `json:"reasonCode"`
	SessionID            string                `json:"sessionId"`
	SessionNonceDigest   string                `json:"sessionNonceDigest"`
	OwnerEpoch           uint64                `json:"ownerEpoch"`
	CurrentAuthorityHead string                `json:"currentAuthorityHead"`
	CommandSequence      uint64                `json:"commandSequence"`
	CommandHead          string                `json:"commandHead"`
	JournalSequence      uint64                `json:"journalSequence"`
	JournalHead          string                `json:"journalHead"`
	ObserverIdentity     string                `json:"observerIdentity"`
	ObservedAt           string                `json:"observedAt"`
	SupervisorProcess    ProcessIdentity       `json:"supervisorProcess"`
	SupervisorBinary     BinaryIdentity        `json:"supervisorBinary"`
	ControlSocket        ControlSocketIdentity `json:"controlSocket"`
	ControlFiles         SessionControlFiles   `json:"controlFiles,omitempty,omitzero"`
	Reconciliation       ReconciliationState   `json:"reconciliation,omitempty"`
	ReplayedResponse     *Response             `json:"replayedResponse,omitempty"`
}

type Request struct {
	ProtocolRevision      string          `json:"protocolRevision"`
	SessionID             string          `json:"sessionId"`
	Command               CommandName     `json:"command"`
	CommandID             string          `json:"commandId"`
	Sequence              uint64          `json:"sequence"`
	PreviousCommandDigest string          `json:"previousCommandDigest"`
	CurrentAuthorityHead  string          `json:"currentAuthorityHead"`
	RequestDigest         string          `json:"requestDigest"`
	Deadline              string          `json:"deadline"`
	Payload               json.RawMessage `json:"payload"`
}

type requestDigestInput struct {
	ProtocolRevision      string          `json:"protocolRevision"`
	SessionID             string          `json:"sessionId"`
	Command               CommandName     `json:"command"`
	CommandID             string          `json:"commandId"`
	Sequence              uint64          `json:"sequence"`
	PreviousCommandDigest string          `json:"previousCommandDigest"`
	CurrentAuthorityHead  string          `json:"currentAuthorityHead"`
	Deadline              string          `json:"deadline"`
	Payload               json.RawMessage `json:"payload"`
}

type Response struct {
	SchemaVersion     string          `json:"schemaVersion"`
	ProtocolRevision  string          `json:"protocolRevision"`
	SessionID         string          `json:"sessionId"`
	Command           CommandName     `json:"command"`
	CommandID         string          `json:"commandId"`
	Sequence          uint64          `json:"sequence"`
	RequestDigest     string          `json:"requestDigest"`
	Status            string          `json:"status"`
	ReasonCode        string          `json:"reasonCode"`
	ReceiptDigest     string          `json:"receiptDigest"`
	ObservationDigest string          `json:"observationDigest"`
	CommandHead       string          `json:"commandHead"`
	Payload           json.RawMessage `json:"payload"`
}

type BindAuthorityPayload struct {
	SupervisorStartedFactDigest string `json:"supervisorStartedFactDigest"`
	OwnerEpoch                  uint64 `json:"ownerEpoch"`
	PreviousAuthorityHead       string `json:"previousAuthorityHead"`
	AuthorityHead               string `json:"authorityHead"`
}

type AbortUnboundPayload struct {
	OwnerEpoch                  uint64 `json:"ownerEpoch"`
	PreviousAuthorityHead       string `json:"previousAuthorityHead"`
	AuthorityAbsenceProofDigest string `json:"authorityAbsenceProofDigest"`
}

type SpawnPayload struct {
	LaunchAuthorizedFactDigest  string         `json:"launchAuthorizedFactDigest"`
	SupervisorStartedFactDigest string         `json:"supervisorStartedFactDigest"`
	Runtime                     HeldObjectSpec `json:"runtime"`
	WorkingDirectory            HeldObjectSpec `json:"workingDirectory"`
	// Empty is legacy replay only; fresh S1 commands must set
	// SourceGateRevisionV1 and AllocationLiveIdentity.
	SourceGateRevision string `json:"sourceGateRevision,omitempty"`
	// AllocationLiveIdentity is the path-free identity copied from the durable
	// allocation provision receipt. It is required for a fresh spawn.
	AllocationLiveIdentity *AllocationLiveIdentity           `json:"allocationLiveIdentity,omitempty"`
	ClosureProfileID       string                            `json:"closureProfileId"`
	MaterialRoots          []launchidentity.MaterialRootV1   `json:"materialRoots"`
	LaunchMaterials        []launchidentity.LaunchMaterialV1 `json:"launchMaterials"`
	LaunchMaterialsDigest  string                            `json:"launchMaterialsDigest"`
	AgentLaunchSpecDigest  string                            `json:"agentLaunchSpecDigest"`
	ArgvDigest             string                            `json:"argvDigest"`
	EnvironmentDigest      string                            `json:"environmentDigest"`
	StdinDigest            string                            `json:"stdinDigest"`
	EnvironmentKeys        []string                          `json:"environmentKeys"`
	Argv                   []string                          `json:"argv"`
	Environment            []string                          `json:"environment"`
	Stdin                  []byte                            `json:"stdin"`
}

// AllocationLiveIdentity is the path-free current allocation directory
// identity that closes the supervisor's cwd observation to allocation
// authority. It is not a bearer and carries no locator.
type AllocationLiveIdentity struct {
	Device    uint64 `json:"device"`
	Inode     uint64 `json:"inode"`
	FileType  string `json:"fileType"`
	UID       uint32 `json:"uid"`
	GID       uint32 `json:"gid"`
	Mode      uint32 `json:"mode"`
	LinkCount uint64 `json:"linkCount"`
	Size      int64  `json:"size"`
}

func (identity AllocationLiveIdentity) valid() bool {
	return identity.Device != 0 && identity.Inode != 0 && identity.FileType == "directory" && identity.Mode&0o170000 == 0o040000 && identity.Mode&0o6000 == 0 && identity.LinkCount != 0 && identity.Size >= 0
}

func (identity AllocationLiveIdentity) matches(spec HeldObjectSpec) bool {
	return identity.valid() && spec.Role == "working-directory" && spec.FileType == "directory" && identity.Device == spec.Device && identity.Inode == spec.Inode && identity.UID == spec.UID && identity.GID == spec.GID && identity.Mode == spec.Mode && identity.LinkCount == spec.LinkCount && identity.Size == spec.Size
}

// HeldObjectSpec is the exact nofollow identity that the supervisor must open
// and retain. Role is a non-secret closure label; Path is transmitted only on
// the authenticated socket and is never projected into the journal.
type HeldObjectSpec struct {
	Role          string `json:"role"`
	CanonicalPath string `json:"canonicalPath"`
	Device        uint64 `json:"device"`
	Inode         uint64 `json:"inode"`
	FileType      string `json:"fileType"`
	UID           uint32 `json:"uid"`
	GID           uint32 `json:"gid"`
	Mode          uint32 `json:"mode"`
	LinkCount     uint64 `json:"linkCount"`
	Size          int64  `json:"size"`
	RawSHA256     string `json:"rawSHA256,omitempty"`
}

type ResumePayload struct {
	ProcessStartedFactDigest string `json:"processStartedFactDigest"`
}

type CleanupPayload struct {
	TerminalizationBarrierDigest string `json:"terminalizationBarrierDigest"`
	TerminalizationID            string `json:"terminalizationId"`
	TerminalGeneration           uint64 `json:"terminalGeneration"`
	CleanupBindingDigest         string `json:"cleanupBindingDigest"`
	ProcessStartedFactDigest     string `json:"processStartedFactDigest"`
	LastObservationDigest        string `json:"lastObservationDigest"`
}

type CollectPayload struct {
	ProcessStartedFactDigest string `json:"processStartedFactDigest"`
	LastObservationDigest    string `json:"lastObservationDigest"`
}

type ClosePayload struct {
	ProcessTerminalFactDigest  string `json:"processTerminalFactDigest"`
	AllocationTerminatedDigest string `json:"allocationTerminatedFactDigest"`
	CleanupBindingDigest       string `json:"cleanupBindingDigest"`
}

// MechanicsResult is deliberately digest-only. Implementations retain raw
// process/output material in held descriptors and owner-only data objects.
type MechanicsResult struct {
	Disposition       string          `json:"disposition"`
	ReasonCode        string          `json:"reasonCode"`
	ObservationDigest string          `json:"observationDigest"`
	TranscriptDigest  string          `json:"transcriptDigest,omitempty"`
	StdoutBytes       uint64          `json:"stdoutBytes,omitempty"`
	StderrBytes       uint64          `json:"stderrBytes,omitempty"`
	Truncated         bool            `json:"truncated,omitempty"`
	Payload           json.RawMessage `json:"payload"`
}

// ProcessReport is the one typed, secret-free mechanics observation exposed
// to Core composition. Keeping this wire shape in processsupervisor prevents
// adapters from re-declaring private JSON and accidentally weakening exact
// response/observation binding.
type ProcessReport struct {
	State               string          `json:"state"`
	ObserverIdentity    string          `json:"observerIdentity"`
	ObservedAt          string          `json:"observedAt"`
	Process             ProcessIdentity `json:"process"`
	RuntimeObjectDigest string          `json:"runtimeObjectDigest"`
	WorkingObjectDigest string          `json:"workingObjectDigest"`
	// SourceGateRevision is empty only on historical reports. New S1 reports
	// carry SourceGateRevisionV1 and a mandatory ExactSetDigest.
	SourceGateRevision string `json:"sourceGateRevision,omitempty"`
	// ExactSetDigest binds the role-keyed runtime/cwd/root/material table
	// admitted by spawn. It contains no canonical paths or raw material bytes.
	ExactSetDigest      string `json:"exactSetDigest,omitempty"`
	ExitCode            int    `json:"exitCode,omitempty"`
	Signal              string `json:"signal,omitempty"`
	StdoutDigest        string `json:"stdoutDigest,omitempty"`
	StderrDigest        string `json:"stderrDigest,omitempty"`
	StdoutBytes         uint64 `json:"stdoutBytes,omitempty"`
	StderrBytes         uint64 `json:"stderrBytes,omitempty"`
	TranscriptTruncated bool   `json:"transcriptTruncated,omitempty"`
}

// Mechanics is the next-slice integration seam. Implementations must own the
// exact held child/FD mechanics and return only closed digest observations.
type Mechanics interface {
	Spawn(context.Context, SpawnPayload) (MechanicsResult, error)
	Resume(context.Context, ResumePayload) (MechanicsResult, error)
	Inspect(context.Context, CleanupPayload) (MechanicsResult, error)
	Terminate(context.Context, CleanupPayload) (MechanicsResult, error)
	Collect(context.Context, CollectPayload) (MechanicsResult, error)
	Close(context.Context, ClosePayload) (MechanicsResult, error)
}

func commandDeadlineLimit(command CommandName) (time.Duration, bool) {
	switch command {
	case CommandBindAuthority, CommandAbortUnbound, CommandResume, CommandInspect:
		return 30 * time.Second, true
	case CommandSpawn, CommandTerminate, CommandCollect, CommandClose:
		return 2 * time.Minute, true
	default:
		return 0, false
	}
}

func validDigest(value string) bool { return digestPattern.MatchString(value) }
func validID(value string) bool     { return idPattern.MatchString(value) }
func safeUint64(value uint64) bool  { return value <= maxSafeJSONInteger }
