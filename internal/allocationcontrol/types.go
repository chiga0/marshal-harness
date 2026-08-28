package allocationcontrol

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

const (
	ProtocolRevision       = "marshal-allocation-control/v1"
	MarkerSchema           = "AllocationIdentityMarkerV1"
	ProvisionSchema        = "AllocationProvisionIntentV1"
	PreparedSchema         = "AllocationStagingPreparedV1"
	ProvisionReceiptSchema = "AllocationProvisionReceiptV1"
	TerminateRequestSchema = "TerminateRequestV1"
	TerminateSchema        = "AllocationTerminateIntentV1"
	TerminateReceiptSchema = "AllocationTerminateReceiptV1"

	DispositionApplied  = "applied"
	ObjectTypeDirectory = "directory"
	ObjectTypeRegular   = "regular"
	maxSafeJSONInteger  = uint64(1<<53 - 1)
)

// AllocationBindingV1 is the complete authority/Attempt/allocation tuple
// common to one allocation effect. FencingTokenDigest deliberately excludes
// the raw bearer value from the journal and marker.
type AllocationBindingV1 struct {
	AuthorityNamespaceID string `json:"authorityNamespaceId"`
	TaskID               string `json:"taskId"`
	RunID                string `json:"runId"`
	AttemptID            string `json:"attemptId"`
	AllocationID         string `json:"allocationId"`
	LeaseID              string `json:"leaseId"`
	Generation           int64  `json:"generation"`
	FencingTokenDigest   string `json:"fencingTokenDigest"`
	CommandID            string `json:"commandId"`
	IdempotencyKey       string `json:"idempotencyKey"`
}

// AllocationStoreScopeV1 gives one recovery journal and object namespace a
// single mechanically enforced authority/allocation owner. Different effects
// use distinct deterministic subdirectories under the configured state root.
type AllocationStoreScopeV1 struct {
	AuthorityNamespaceID string `json:"authorityNamespaceId"`
	TaskID               string `json:"taskId"`
	RunID                string `json:"runId"`
	AttemptID            string `json:"attemptId"`
	AllocationID         string `json:"allocationId"`
}

func (scope AllocationStoreScopeV1) Validate() error {
	for _, value := range []string{scope.AuthorityNamespaceID, scope.TaskID, scope.RunID, scope.AttemptID, scope.AllocationID} {
		if !validText(value) {
			return ErrInvalid
		}
	}
	return nil
}

func (scope AllocationStoreScopeV1) Matches(binding AllocationBindingV1) bool {
	return scope.Validate() == nil && binding.Validate() == nil && scope.AuthorityNamespaceID == binding.AuthorityNamespaceID && scope.TaskID == binding.TaskID && scope.RunID == binding.RunID && scope.AttemptID == binding.AttemptID && scope.AllocationID == binding.AllocationID
}

func StoreScopeForBinding(binding AllocationBindingV1) (AllocationStoreScopeV1, error) {
	scope := AllocationStoreScopeV1{
		AuthorityNamespaceID: binding.AuthorityNamespaceID,
		TaskID:               binding.TaskID,
		RunID:                binding.RunID,
		AttemptID:            binding.AttemptID,
		AllocationID:         binding.AllocationID,
	}
	if binding.Validate() != nil || scope.Validate() != nil {
		return AllocationStoreScopeV1{}, ErrInvalid
	}
	return scope, nil
}

func (scope AllocationStoreScopeV1) directoryName() (string, error) {
	if scope.Validate() != nil {
		return "", ErrInvalid
	}
	digest, err := digestValue(scope)
	if err != nil {
		return "", err
	}
	return "scope-" + strings.TrimPrefix(digest, "sha256:"), nil
}

func (binding AllocationBindingV1) Validate() error {
	for _, value := range []string{binding.AuthorityNamespaceID, binding.TaskID, binding.RunID, binding.AttemptID, binding.AllocationID, binding.LeaseID, binding.CommandID, binding.IdempotencyKey} {
		if !validText(value) {
			return ErrInvalid
		}
	}
	if binding.Generation < 1 || uint64(binding.Generation) > maxSafeJSONInteger || !validDigest(binding.FencingTokenDigest) {
		return ErrInvalid
	}
	return nil
}

// SandboxRequirementsV1 freezes the two-dimensional sandbox requirement.
type SandboxRequirementsV1 struct {
	AccessMode            string `json:"accessMode"`
	MinimumAssuranceLevel string `json:"minimumAssuranceLevel"`
}

func (requirements SandboxRequirementsV1) Validate() error {
	if requirements.AccessMode != "read-only" && requirements.AccessMode != "workspace-write" {
		return ErrInvalid
	}
	if requirements.MinimumAssuranceLevel != "workspace-write" && requirements.MinimumAssuranceLevel != "hardened" {
		return ErrInvalid
	}
	return nil
}

// ObjectIdentityV1 is the descriptor-observed identity of one filesystem
// object. Device/Inode are canonical decimal strings so RFC 8785 cannot round
// uint64 identities through the JSON number domain. Mode is the raw stat mode,
// including its type bits.
type ObjectIdentityV1 struct {
	Device string `json:"device"`
	Inode  string `json:"inode"`
	Mode   uint32 `json:"mode"`
	UID    uint32 `json:"uid"`
	GID    uint32 `json:"gid"`
	Size   int64  `json:"size"`
	Nlink  uint64 `json:"nlink"`
	Type   string `json:"type"`
}

func (identity ObjectIdentityV1) Validate(expectedType string) error {
	if !validPositiveDecimalUint64(identity.Device) || !validPositiveDecimalUint64(identity.Inode) || identity.Mode == 0 || identity.Size < 0 || uint64(identity.Size) > maxSafeJSONInteger || identity.Nlink == 0 || identity.Nlink > maxSafeJSONInteger || identity.Type != expectedType {
		return ErrInvalid
	}
	if expectedType == ObjectTypeRegular && identity.Nlink != 1 {
		return ErrInvalid
	}
	return nil
}

// AllocationIdentityMarkerV1 is the closed owner-only marker stored inside a
// staging/live/tombstone directory. It contains no path outside the held
// allocation parent and no secret-bearing fencing token.
type AllocationIdentityMarkerV1 struct {
	SchemaVersion       string              `json:"schemaVersion"`
	ProtocolRevision    string              `json:"protocolRevision"`
	Binding             AllocationBindingV1 `json:"binding"`
	StagingRelativeName string              `json:"stagingRelativeName"`
	LiveRelativeName    string              `json:"liveRelativeName"`
	MarkerRelativeName  string              `json:"markerRelativeName"`
	RequestDigest       string              `json:"requestDigest"`
	NonceDigest         string              `json:"nonceDigest"`
}

func (marker AllocationIdentityMarkerV1) Validate() error {
	if marker.SchemaVersion != MarkerSchema || marker.ProtocolRevision != ProtocolRevision || marker.Binding.Validate() != nil || !validDigest(marker.RequestDigest) || !validDigest(marker.NonceDigest) {
		return ErrInvalid
	}
	wantStaging, wantLive, _, wantMarker, err := DeriveRelativeNames(marker.Binding.AllocationID)
	if err != nil || marker.StagingRelativeName != wantStaging || marker.LiveRelativeName != wantLive || marker.MarkerRelativeName != wantMarker {
		return ErrInvalid
	}
	return nil
}

func (marker AllocationIdentityMarkerV1) Canonical() ([]byte, error) {
	if err := marker.Validate(); err != nil {
		return nil, err
	}
	return canonicalValue(marker)
}

// AllocationProvisionIntentV1 is the complete, immutable provision request
// that stage 2 will persist in the Attempt authority before this package may
// create a staging directory.
type AllocationProvisionIntentV1 struct {
	SchemaVersion              string                `json:"schemaVersion"`
	ProtocolRevision           string                `json:"protocolRevision"`
	Binding                    AllocationBindingV1   `json:"binding"`
	Requirements               SandboxRequirementsV1 `json:"requirements"`
	AllowedStoreIDs            []string              `json:"allowedStoreIds"`
	WorkDirAllowlist           []string              `json:"workDirAllowlist"`
	EnvironmentAllowlist       []string              `json:"environmentAllowlist"`
	ExpectedOwnerUID           uint32                `json:"expectedOwnerUid"`
	ExpectedDirectoryMode      uint32                `json:"expectedDirectoryMode"`
	ExpectedMarkerMode         uint32                `json:"expectedMarkerMode"`
	StagingRelativeName        string                `json:"stagingRelativeName"`
	LiveRelativeName           string                `json:"liveRelativeName"`
	MarkerRelativeName         string                `json:"markerRelativeName"`
	MarkerNonceDigest          string                `json:"markerNonceDigest"`
	ExpectedAttemptSequence    uint64                `json:"expectedAttemptSequence"`
	AttemptAuthorityFactDigest string                `json:"attemptAuthorityFactDigest"`
	RequestDigest              string                `json:"requestDigest"`
}

func (intent AllocationProvisionIntentV1) Validate() error {
	if intent.SchemaVersion != ProvisionSchema || intent.ProtocolRevision != ProtocolRevision || intent.Binding.Validate() != nil || intent.Requirements.Validate() != nil || intent.ExpectedDirectoryMode != 0o700 || intent.ExpectedMarkerMode != 0o600 || intent.ExpectedAttemptSequence == 0 || intent.ExpectedAttemptSequence > maxSafeJSONInteger || !validDigest(intent.AttemptAuthorityFactDigest) || !validDigest(intent.MarkerNonceDigest) {
		return ErrInvalid
	}
	if !sortedUniqueText(intent.AllowedStoreIDs) || !sortedUniquePaths(intent.WorkDirAllowlist) || !sortedUniqueEnvironmentKeys(intent.EnvironmentAllowlist) {
		return ErrInvalid
	}
	wantStaging, wantLive, _, wantMarker, err := DeriveRelativeNames(intent.Binding.AllocationID)
	if err != nil || intent.StagingRelativeName != wantStaging || intent.LiveRelativeName != wantLive || intent.MarkerRelativeName != wantMarker {
		return ErrInvalid
	}
	want, err := intent.digest()
	if err != nil || intent.RequestDigest != want {
		return ErrInvalid
	}
	return nil
}

func (intent AllocationProvisionIntentV1) digest() (string, error) {
	return digestValueWithoutField(intent, "requestDigest")
}

func (intent *AllocationProvisionIntentV1) Seal() error {
	if intent == nil {
		return ErrInvalid
	}
	digest, err := intent.digest()
	if err != nil {
		return err
	}
	intent.RequestDigest = digest
	return intent.Validate()
}

func (intent AllocationProvisionIntentV1) Marker() AllocationIdentityMarkerV1 {
	return AllocationIdentityMarkerV1{
		SchemaVersion: MarkerSchema, ProtocolRevision: ProtocolRevision, Binding: intent.Binding,
		StagingRelativeName: intent.StagingRelativeName, LiveRelativeName: intent.LiveRelativeName,
		MarkerRelativeName: intent.MarkerRelativeName, RequestDigest: intent.RequestDigest, NonceDigest: intent.MarkerNonceDigest,
	}
}

// AllocationStagingPreparedV1 is the exact durable observation required
// between marker fsync and the no-replace staging-to-live rename.
type AllocationStagingPreparedV1 struct {
	SchemaVersion       string                     `json:"schemaVersion"`
	ProtocolRevision    string                     `json:"protocolRevision"`
	Binding             AllocationBindingV1        `json:"binding"`
	IntentFactDigest    string                     `json:"intentFactDigest"`
	RequestDigest       string                     `json:"requestDigest"`
	StagingRelativeName string                     `json:"stagingRelativeName"`
	LiveRelativeName    string                     `json:"liveRelativeName"`
	MarkerRelativeName  string                     `json:"markerRelativeName"`
	StagingIdentity     ObjectIdentityV1           `json:"stagingIdentity"`
	MarkerIdentity      ObjectIdentityV1           `json:"markerIdentity"`
	Marker              AllocationIdentityMarkerV1 `json:"marker"`
	MarkerDigest        string                     `json:"markerDigest"`
	PreparedDigest      string                     `json:"preparedDigest"`
}

func (prepared AllocationStagingPreparedV1) Validate(intent AllocationProvisionIntentV1) error {
	if intent.Validate() != nil || prepared.SchemaVersion != PreparedSchema || prepared.ProtocolRevision != ProtocolRevision || prepared.Binding != intent.Binding || !validDigest(prepared.IntentFactDigest) || prepared.RequestDigest != intent.RequestDigest || prepared.StagingRelativeName != intent.StagingRelativeName || prepared.LiveRelativeName != intent.LiveRelativeName || prepared.MarkerRelativeName != intent.MarkerRelativeName || prepared.StagingIdentity.Validate(ObjectTypeDirectory) != nil || prepared.MarkerIdentity.Validate(ObjectTypeRegular) != nil || prepared.Marker.Validate() != nil || prepared.Marker != intent.Marker() || !validDigest(prepared.MarkerDigest) {
		return ErrInvalid
	}
	wantMarker, err := prepared.Marker.Canonical()
	if err != nil || canonical.DigestBytes(wantMarker) != prepared.MarkerDigest {
		return ErrInvalid
	}
	want, err := prepared.digest()
	if err != nil || prepared.PreparedDigest != want {
		return ErrInvalid
	}
	return nil
}

func (prepared AllocationStagingPreparedV1) digest() (string, error) {
	return digestValueWithoutField(prepared, "preparedDigest")
}

func (prepared *AllocationStagingPreparedV1) Seal() error {
	if prepared == nil {
		return ErrInvalid
	}
	digest, err := prepared.digest()
	if err != nil {
		return err
	}
	prepared.PreparedDigest = digest
	return nil
}

// AllocationProvisionReceiptV1 is emitted only after the live object and its
// parent directory are durable.
type AllocationProvisionReceiptV1 struct {
	SchemaVersion      string                     `json:"schemaVersion"`
	ProtocolRevision   string                     `json:"protocolRevision"`
	Binding            AllocationBindingV1        `json:"binding"`
	IntentFactDigest   string                     `json:"intentFactDigest"`
	PreparedFactDigest string                     `json:"preparedFactDigest"`
	RequestDigest      string                     `json:"requestDigest"`
	LiveRelativeName   string                     `json:"liveRelativeName"`
	LiveIdentity       ObjectIdentityV1           `json:"liveIdentity"`
	MarkerRelativeName string                     `json:"markerRelativeName"`
	MarkerIdentity     ObjectIdentityV1           `json:"markerIdentity"`
	Marker             AllocationIdentityMarkerV1 `json:"marker"`
	MarkerDigest       string                     `json:"markerDigest"`
	Disposition        string                     `json:"disposition"`
	ReceiptDigest      string                     `json:"receiptDigest"`
}

func (receipt AllocationProvisionReceiptV1) Validate(intent AllocationProvisionIntentV1, prepared AllocationStagingPreparedV1) error {
	if prepared.Validate(intent) != nil || receipt.SchemaVersion != ProvisionReceiptSchema || receipt.ProtocolRevision != ProtocolRevision || receipt.Binding != intent.Binding || receipt.IntentFactDigest != prepared.IntentFactDigest || !validDigest(receipt.PreparedFactDigest) || receipt.RequestDigest != intent.RequestDigest || receipt.LiveRelativeName != intent.LiveRelativeName || receipt.LiveIdentity.Validate(ObjectTypeDirectory) != nil || receipt.LiveIdentity != prepared.StagingIdentity || receipt.MarkerRelativeName != intent.MarkerRelativeName || receipt.MarkerIdentity != prepared.MarkerIdentity || receipt.Marker != prepared.Marker || receipt.MarkerDigest != prepared.MarkerDigest || receipt.Disposition != DispositionApplied {
		return ErrInvalid
	}
	want, err := receipt.digest()
	if err != nil || receipt.ReceiptDigest != want {
		return ErrInvalid
	}
	return nil
}

func (receipt AllocationProvisionReceiptV1) digest() (string, error) {
	return digestValueWithoutField(receipt, "receiptDigest")
}

func (receipt *AllocationProvisionReceiptV1) Seal() error {
	if receipt == nil {
		return ErrInvalid
	}
	digest, err := receipt.digest()
	if err != nil {
		return err
	}
	receipt.ReceiptDigest = digest
	return nil
}

// TerminateRequestV1 is the caller-controlled, closed cleanup request. Its
// digest deliberately excludes every filesystem observation made later by
// Core under held descriptors.
type TerminateRequestV1 struct {
	SchemaVersion              string              `json:"schemaVersion"`
	ProtocolRevision           string              `json:"protocolRevision"`
	Binding                    AllocationBindingV1 `json:"binding"`
	TerminalizationID          string              `json:"terminalizationId"`
	CleanupBindingDigest       string              `json:"cleanupBindingDigest"`
	ProcessTerminalFactDigest  string              `json:"processTerminalFactDigest"`
	OrchestratorID             string              `json:"orchestratorId"`
	ExpectedAttemptSequence    uint64              `json:"expectedAttemptSequence"`
	AttemptAuthorityFactDigest string              `json:"attemptAuthorityFactDigest"`
	LiveRelativeName           string              `json:"liveRelativeName"`
	TombstoneRelativeName      string              `json:"tombstoneRelativeName"`
	RequestDigest              string              `json:"requestDigest"`
}

func (request TerminateRequestV1) Validate() error {
	if request.SchemaVersion != TerminateRequestSchema || request.ProtocolRevision != ProtocolRevision || request.Binding.Validate() != nil || !validText(request.TerminalizationID) || !validText(request.OrchestratorID) || !validDigest(request.CleanupBindingDigest) || !validDigest(request.ProcessTerminalFactDigest) || request.ExpectedAttemptSequence == 0 || request.ExpectedAttemptSequence > maxSafeJSONInteger || !validDigest(request.AttemptAuthorityFactDigest) {
		return ErrInvalid
	}
	_, wantLive, wantTombstone, _, err := DeriveRelativeNames(request.Binding.AllocationID)
	if err != nil || request.LiveRelativeName != wantLive || request.TombstoneRelativeName != wantTombstone {
		return ErrInvalid
	}
	want, err := request.digest()
	if err != nil || request.RequestDigest != want {
		return ErrInvalid
	}
	return nil
}

func (request TerminateRequestV1) digest() (string, error) {
	return digestValueWithoutField(request, "requestDigest")
}

func (request *TerminateRequestV1) Seal() error {
	if request == nil {
		return ErrInvalid
	}
	digest, err := request.digest()
	if err != nil {
		return err
	}
	request.RequestDigest = digest
	return request.Validate()
}

// AllocationTerminateIntentV1 binds one already sealed caller request to the
// exact live object observed under held descriptors before any tombstone
// rename. RequestDigest is copied, never recomputed from these observations.
type AllocationTerminateIntentV1 struct {
	SchemaVersion              string                     `json:"schemaVersion"`
	ProtocolRevision           string                     `json:"protocolRevision"`
	Binding                    AllocationBindingV1        `json:"binding"`
	TerminalizationID          string                     `json:"terminalizationId"`
	CleanupBindingDigest       string                     `json:"cleanupBindingDigest"`
	ProcessTerminalFactDigest  string                     `json:"processTerminalFactDigest"`
	OrchestratorID             string                     `json:"orchestratorId"`
	ExpectedAttemptSequence    uint64                     `json:"expectedAttemptSequence"`
	AttemptAuthorityFactDigest string                     `json:"attemptAuthorityFactDigest"`
	LiveRelativeName           string                     `json:"liveRelativeName"`
	TombstoneRelativeName      string                     `json:"tombstoneRelativeName"`
	MarkerRelativeName         string                     `json:"markerRelativeName"`
	LiveIdentity               ObjectIdentityV1           `json:"liveIdentity"`
	MarkerIdentity             ObjectIdentityV1           `json:"markerIdentity"`
	Marker                     AllocationIdentityMarkerV1 `json:"marker"`
	MarkerDigest               string                     `json:"markerDigest"`
	RequestDigest              string                     `json:"requestDigest"`
}

func (intent AllocationTerminateIntentV1) Validate() error {
	if intent.SchemaVersion != TerminateSchema || intent.ProtocolRevision != ProtocolRevision || intent.Request().Validate() != nil || intent.LiveIdentity.Validate(ObjectTypeDirectory) != nil || intent.MarkerIdentity.Validate(ObjectTypeRegular) != nil || intent.Marker.Validate() != nil || !validDigest(intent.MarkerDigest) {
		return ErrInvalid
	}
	_, wantLive, wantTombstone, wantMarker, err := DeriveRelativeNames(intent.Binding.AllocationID)
	if err != nil || intent.LiveRelativeName != wantLive || intent.TombstoneRelativeName != wantTombstone || intent.MarkerRelativeName != wantMarker {
		return ErrInvalid
	}
	if !sameAllocationScope(intent.Binding, intent.Marker.Binding) {
		return ErrInvalid
	}
	markerBytes, err := intent.Marker.Canonical()
	if err != nil || canonical.DigestBytes(markerBytes) != intent.MarkerDigest {
		return ErrInvalid
	}
	return nil
}

func (intent AllocationTerminateIntentV1) Request() TerminateRequestV1 {
	return TerminateRequestV1{
		SchemaVersion: TerminateRequestSchema, ProtocolRevision: ProtocolRevision, Binding: intent.Binding,
		TerminalizationID: intent.TerminalizationID, CleanupBindingDigest: intent.CleanupBindingDigest,
		ProcessTerminalFactDigest: intent.ProcessTerminalFactDigest, OrchestratorID: intent.OrchestratorID,
		ExpectedAttemptSequence: intent.ExpectedAttemptSequence, AttemptAuthorityFactDigest: intent.AttemptAuthorityFactDigest,
		LiveRelativeName: intent.LiveRelativeName, TombstoneRelativeName: intent.TombstoneRelativeName,
		RequestDigest: intent.RequestDigest,
	}
}

func bindTerminateIntent(request TerminateRequestV1, liveIdentity, markerIdentity ObjectIdentityV1, marker AllocationIdentityMarkerV1, markerDigest string) (AllocationTerminateIntentV1, error) {
	if request.Validate() != nil {
		return AllocationTerminateIntentV1{}, ErrInvalid
	}
	_, _, _, markerName, err := DeriveRelativeNames(request.Binding.AllocationID)
	if err != nil {
		return AllocationTerminateIntentV1{}, err
	}
	intent := AllocationTerminateIntentV1{
		SchemaVersion: TerminateSchema, ProtocolRevision: ProtocolRevision, Binding: request.Binding,
		TerminalizationID: request.TerminalizationID, CleanupBindingDigest: request.CleanupBindingDigest,
		ProcessTerminalFactDigest: request.ProcessTerminalFactDigest, OrchestratorID: request.OrchestratorID,
		ExpectedAttemptSequence: request.ExpectedAttemptSequence, AttemptAuthorityFactDigest: request.AttemptAuthorityFactDigest,
		LiveRelativeName: request.LiveRelativeName, TombstoneRelativeName: request.TombstoneRelativeName,
		MarkerRelativeName: markerName, LiveIdentity: liveIdentity, MarkerIdentity: markerIdentity,
		Marker: marker, MarkerDigest: markerDigest, RequestDigest: request.RequestDigest,
	}
	if intent.Validate() != nil {
		return AllocationTerminateIntentV1{}, ErrInvalid
	}
	return intent, nil
}

// AllocationTerminateReceiptV1 proves the exact live object was atomically
// moved to the deterministic permanent tombstone.
type AllocationTerminateReceiptV1 struct {
	SchemaVersion              string                     `json:"schemaVersion"`
	ProtocolRevision           string                     `json:"protocolRevision"`
	Binding                    AllocationBindingV1        `json:"binding"`
	TerminalizationID          string                     `json:"terminalizationId"`
	CleanupBindingDigest       string                     `json:"cleanupBindingDigest"`
	ProcessTerminalFactDigest  string                     `json:"processTerminalFactDigest"`
	OrchestratorID             string                     `json:"orchestratorId"`
	ExpectedAttemptSequence    uint64                     `json:"expectedAttemptSequence"`
	AttemptAuthorityFactDigest string                     `json:"attemptAuthorityFactDigest"`
	IntentFactDigest           string                     `json:"intentFactDigest"`
	RequestDigest              string                     `json:"requestDigest"`
	LiveRelativeName           string                     `json:"liveRelativeName"`
	TombstoneRelativeName      string                     `json:"tombstoneRelativeName"`
	TombstoneIdentity          ObjectIdentityV1           `json:"tombstoneIdentity"`
	MarkerRelativeName         string                     `json:"markerRelativeName"`
	MarkerIdentity             ObjectIdentityV1           `json:"markerIdentity"`
	Marker                     AllocationIdentityMarkerV1 `json:"marker"`
	MarkerDigest               string                     `json:"markerDigest"`
	LiveAbsent                 bool                       `json:"liveAbsent"`
	TombstonePresent           bool                       `json:"tombstonePresent"`
	Disposition                string                     `json:"disposition"`
	ReceiptDigest              string                     `json:"receiptDigest"`
}

func (receipt AllocationTerminateReceiptV1) Validate(intent AllocationTerminateIntentV1) error {
	if intent.Validate() != nil || receipt.SchemaVersion != TerminateReceiptSchema || receipt.ProtocolRevision != ProtocolRevision || receipt.Binding != intent.Binding || receipt.TerminalizationID != intent.TerminalizationID || receipt.CleanupBindingDigest != intent.CleanupBindingDigest || receipt.ProcessTerminalFactDigest != intent.ProcessTerminalFactDigest || receipt.OrchestratorID != intent.OrchestratorID || receipt.ExpectedAttemptSequence != intent.ExpectedAttemptSequence || receipt.AttemptAuthorityFactDigest != intent.AttemptAuthorityFactDigest || !validDigest(receipt.IntentFactDigest) || receipt.RequestDigest != intent.RequestDigest || receipt.LiveRelativeName != intent.LiveRelativeName || receipt.TombstoneRelativeName != intent.TombstoneRelativeName || receipt.TombstoneIdentity != intent.LiveIdentity || receipt.MarkerRelativeName != intent.MarkerRelativeName || receipt.MarkerIdentity != intent.MarkerIdentity || receipt.Marker != intent.Marker || receipt.MarkerDigest != intent.MarkerDigest || !receipt.LiveAbsent || !receipt.TombstonePresent || receipt.Disposition != DispositionApplied {
		return ErrInvalid
	}
	want, err := receipt.digest()
	if err != nil || receipt.ReceiptDigest != want {
		return ErrInvalid
	}
	return nil
}

func (receipt AllocationTerminateReceiptV1) digest() (string, error) {
	return digestValueWithoutField(receipt, "receiptDigest")
}

func (receipt *AllocationTerminateReceiptV1) Seal() error {
	if receipt == nil {
		return ErrInvalid
	}
	digest, err := receipt.digest()
	if err != nil {
		return err
	}
	receipt.ReceiptDigest = digest
	return nil
}

// DeriveRelativeNames makes every filesystem name a deterministic function of
// allocation identity. Returned strings are single path components.
func DeriveRelativeNames(allocationID string) (staging, live, tombstone, marker string, err error) {
	if !validText(allocationID) {
		return "", "", "", "", ErrInvalid
	}
	digest := strings.TrimPrefix(canonical.DigestBytes([]byte("marshal-allocation\x00"+allocationID)), "sha256:")
	return "staging-" + digest, "allocation-" + digest, "tombstone-" + digest, ".marshal-allocation-identity.json", nil
}

func canonicalValue(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, ErrInvalid
	}
	result, err := canonical.JSON(raw)
	if err != nil {
		return nil, ErrInvalid
	}
	return result, nil
}

func digestValue(value any) (string, error) {
	data, err := canonicalValue(value)
	if err != nil {
		return "", err
	}
	return canonical.DigestBytes(data), nil
}

func digestValueWithoutField(value any, field string) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", ErrInvalid
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return "", ErrInvalid
	}
	if _, present := object[field]; !present {
		return "", ErrInvalid
	}
	delete(object, field)
	return digestValue(object)
}

func validText(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 512 || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if r < 0x21 || r > 0x7e {
			return false
		}
	}
	return true
}

func validRelativeName(value string) bool {
	return validPrintableASCII(value, 255) && value != "." && value != ".." && !strings.ContainsAny(value, `/\\`)
}

func validDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	raw := value[len("sha256:"):]
	decoded, err := hex.DecodeString(raw)
	return err == nil && len(decoded) == 32 && raw == strings.ToLower(raw)
}

func validPositiveDecimalUint64(value string) bool {
	parsed, err := strconv.ParseUint(value, 10, 64)
	return err == nil && parsed > 0 && strconv.FormatUint(parsed, 10) == value
}

func sortedUniqueText(values []string) bool {
	if values == nil {
		return false
	}
	for index, value := range values {
		if !validText(value) || index > 0 && values[index-1] >= value {
			return false
		}
	}
	return sort.StringsAreSorted(values)
}

func sortedUniquePaths(values []string) bool {
	if values == nil {
		return false
	}
	for index, value := range values {
		if !validPrintableASCII(value, 4096) || strings.Contains(value, `\`) || !filepath.IsAbs(value) || filepath.Clean(value) != value || index > 0 && values[index-1] >= value {
			return false
		}
	}
	return true
}

func validPrintableASCII(value string, limit int) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > limit || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r > 0x7e {
			return false
		}
	}
	return true
}

func sortedUniqueEnvironmentKeys(values []string) bool {
	if values == nil {
		return false
	}
	for index, value := range values {
		if !validEnvironmentKey(value) || index > 0 && values[index-1] >= value {
			return false
		}
	}
	return true
}

func validEnvironmentKey(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for index, r := range value {
		if !(r == '_' || r >= 'A' && r <= 'Z' || index > 0 && r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func strictCanonicalDecode(data []byte, target any) error {
	canonicalData, err := canonical.JSON(data)
	if err != nil || !bytes.Equal(canonicalData, data) {
		return ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return ErrInvalid
	}
	if decoder.More() {
		return ErrInvalid
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return ErrInvalid
	}
	return nil
}

func equalCanonical(left, right any) bool {
	a, errA := canonicalValue(left)
	b, errB := canonicalValue(right)
	return errA == nil && errB == nil && bytes.Equal(a, b)
}

func requireRelativeName(value string) error {
	if !validRelativeName(value) {
		return fmt.Errorf("%w: relative name", ErrInvalid)
	}
	return nil
}
