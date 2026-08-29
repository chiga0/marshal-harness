package allocationcontrol

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

const (
	ExistingWorktreeProtocolRevision     = "existing-worktree-binding/v1"
	ExistingWorktreeBindRequestSchema    = "ExistingWorktreeBindRequestV1"
	ExistingWorktreeBindIntentSchema     = "ExistingWorktreeBindIntentV1"
	ExistingWorktreeBindReceiptSchema    = "ExistingWorktreeBindReceiptV1"
	ExistingWorktreeReleaseRequestSchema = "ExistingWorktreeReleaseRequestV1"
	ExistingWorktreeReleaseIntentSchema  = "ExistingWorktreeReleaseIntentV1"
	ExistingWorktreeReleaseReceiptSchema = "ExistingWorktreeReleaseReceiptV1"
	ExistingWorktreeProjectionSchema     = "ExistingWorktreeProjectionRecordV1"

	ExistingWorktreeFactBindIntent     ExistingWorktreeFactKind = "bind-intent"
	ExistingWorktreeFactBindReceipt    ExistingWorktreeFactKind = "bind-receipt"
	ExistingWorktreeFactReleaseIntent  ExistingWorktreeFactKind = "release-intent"
	ExistingWorktreeFactReleaseReceipt ExistingWorktreeFactKind = "release-receipt"

	ExistingWorktreeProjectionDirectory = "existing-worktree-bindings"
)

type ExistingWorktreeFactKind string

// ExistingWorktreeBindingV1 binds the logical allocation to the repository
// owner, exact Run/Attempt reservation and fencing lineage. It deliberately
// contains no bearer token and no filesystem path.
type ExistingWorktreeBindingV1 struct {
	AuthorityNamespaceID    string `json:"authorityNamespaceId"`
	RepositoryOwnerDigest   string `json:"repositoryOwnerDigest"`
	TaskID                  string `json:"taskId"`
	RunID                   string `json:"runId"`
	AttemptID               string `json:"attemptId"`
	ReservationFactDigest   string `json:"reservationFactDigest"`
	AttemptOpenedFactDigest string `json:"attemptOpenedFactDigest"`
	AllocationID            string `json:"allocationId"`
	LeaseID                 string `json:"leaseId"`
	Generation              int64  `json:"generation"`
	FencingTokenDigest      string `json:"fencingTokenDigest"`
	FrozenInputsDigest      string `json:"frozenInputsDigest"`
	ExpectedAttemptSequence uint64 `json:"expectedAttemptSequence"`
}

func (binding ExistingWorktreeBindingV1) Validate() error {
	for _, value := range []string{binding.AuthorityNamespaceID, binding.TaskID, binding.RunID, binding.AttemptID, binding.AllocationID, binding.LeaseID} {
		if !validText(value) {
			return ErrInvalid
		}
	}
	for _, value := range []string{binding.RepositoryOwnerDigest, binding.ReservationFactDigest, binding.AttemptOpenedFactDigest, binding.FencingTokenDigest, binding.FrozenInputsDigest} {
		if !validDigest(value) {
			return ErrInvalid
		}
	}
	if binding.Generation < 1 || uint64(binding.Generation) > maxSafeJSONInteger || binding.ExpectedAttemptSequence == 0 || binding.ExpectedAttemptSequence > maxSafeJSONInteger {
		return ErrInvalid
	}
	return nil
}

func (binding ExistingWorktreeBindingV1) Digest() (string, error) {
	if binding.Validate() != nil {
		return "", ErrInvalid
	}
	return digestValue(binding)
}

// DescriptorBoundRunV1 is the narrow hand-off from RunStore AcquireExisting.
// Directory must be a held descriptor supplied while its lease borrow is
// active; allocationcontrol never opens or creates a Run from a pathname.
type DescriptorBoundRunV1 struct {
	RunID               string
	Directory           *os.File
	DirectoryIdentity   ObjectIdentityV1
	AuthorityHeadDigest string
}

// ExistingWorktreeCurrentAuthorityV1 is the narrow current-ledger view the
// RB1 session must derive while holding the repository owner, Run lease and
// RB1 transaction. Release-only fields are either all empty or all present.
type ExistingWorktreeCurrentAuthorityV1 struct {
	AuthorityNamespaceID       string
	RepositoryOwnerDigest      string
	TaskID                     string
	RunID                      string
	RunAuthorityHeadDigest     string
	AttemptID                  string
	AttemptAuthorityHeadDigest string
	ReservationFactDigest      string
	AttemptOpenedFactDigest    string
	AllocationID               string
	LeaseID                    string
	Generation                 int64
	FencingTokenDigest         string
	FrozenInputsDigest         string
	ExpectedAttemptSequence    uint64
	WorktreePath               string
	ExpectedWorktreeIdentity   ObjectIdentityV1
	ExpectedBaseSHA            string
	TerminalizationID          string
	CleanupBindingDigest       string
	ProcessTerminalFactDigest  string
	CleanupDisposition         string
}

func (current ExistingWorktreeCurrentAuthorityV1) validateBinding(binding ExistingWorktreeBindingV1, run DescriptorBoundRunV1) error {
	if binding.Validate() != nil || run.validate(binding) != nil || current.AuthorityNamespaceID != binding.AuthorityNamespaceID || current.RepositoryOwnerDigest != binding.RepositoryOwnerDigest || current.TaskID != binding.TaskID || current.RunID != binding.RunID || current.RunID != run.RunID || current.RunAuthorityHeadDigest != run.AuthorityHeadDigest || current.AttemptID != binding.AttemptID || current.ReservationFactDigest != binding.ReservationFactDigest || current.AttemptOpenedFactDigest != binding.AttemptOpenedFactDigest || current.AllocationID != binding.AllocationID || current.LeaseID != binding.LeaseID || current.Generation != binding.Generation || current.FencingTokenDigest != binding.FencingTokenDigest || current.FrozenInputsDigest != binding.FrozenInputsDigest || current.ExpectedAttemptSequence != binding.ExpectedAttemptSequence || !validDigest(current.AttemptAuthorityHeadDigest) {
		return ErrAuthorityConflict
	}
	return nil
}

func (current ExistingWorktreeCurrentAuthorityV1) validateBind(request ExistingWorktreeBindRequestV1, run DescriptorBoundRunV1) error {
	if request.Validate() != nil || current.validateBinding(request.Binding, run) != nil || current.WorktreePath != request.WorktreePath || !sameDirectoryObject(current.ExpectedWorktreeIdentity, request.ExpectedWorktreeIdentity) || current.ExpectedBaseSHA != request.ExpectedBaseSHA || current.TerminalizationID != "" || current.CleanupBindingDigest != "" || current.ProcessTerminalFactDigest != "" || current.CleanupDisposition != "" {
		return ErrAuthorityConflict
	}
	return nil
}

func (current ExistingWorktreeCurrentAuthorityV1) validateRelease(request ExistingWorktreeReleaseRequestV1, run DescriptorBoundRunV1) error {
	if request.Validate() != nil || current.validateBinding(request.Binding, run) != nil || current.RunAuthorityHeadDigest != request.RunAuthorityHeadDigest || current.TerminalizationID != request.TerminalizationID || current.CleanupBindingDigest != request.CleanupBindingDigest || current.ProcessTerminalFactDigest != request.ProcessTerminalFactDigest || current.CleanupDisposition != request.CleanupDisposition {
		return ErrAuthorityConflict
	}
	return nil
}

// ValidateExistingWorktreeCurrentBind/Release are narrow composition helpers
// for ResultIngress. They expose no authority: the caller must already be
// inside the held current-owner/Run/reservation verifier callback.
func ValidateExistingWorktreeCurrentBind(current ExistingWorktreeCurrentAuthorityV1, request ExistingWorktreeBindRequestV1, run DescriptorBoundRunV1) error {
	return current.validateBind(request, run)
}

func ValidateExistingWorktreeCurrentRelease(current ExistingWorktreeCurrentAuthorityV1, request ExistingWorktreeReleaseRequestV1, run DescriptorBoundRunV1) error {
	return current.validateRelease(request, run)
}

// ExistingWorktreeDescriptorGraphV1 is borrowed from the RB1 session while
// the repository owner lock is held. Locator strings may only be traversed
// descriptor-relative from FilesystemRoot. RepositoryRoot and its current
// parent/name edge are used for the fixed projection graph.
type ExistingWorktreeDescriptorGraphV1 struct {
	FilesystemRoot                 *os.File
	FilesystemRootIdentity         ObjectIdentityV1
	RepositoryParent               *os.File
	RepositoryRoot                 *os.File
	RepositoryCurrentName          CurrentNameIdentityV1
	RepositoryCommonGitDirectory   *os.File
	RepositoryCommonGitCurrentName CurrentNameIdentityV1
}

func (run DescriptorBoundRunV1) validate(binding ExistingWorktreeBindingV1) error {
	if run.Directory == nil || run.RunID != binding.RunID || !validText(run.RunID) || run.DirectoryIdentity.Validate(ObjectTypeDirectory) != nil || !validDigest(run.AuthorityHeadDigest) {
		return ErrInvalid
	}
	return nil
}

// CurrentNameIdentityV1 proves that a held object is still reachable through
// the exact nofollow parent/name edge observed during admission.
type CurrentNameIdentityV1 struct {
	ParentIdentity       ObjectIdentityV1 `json:"parentIdentity"`
	ParentMutationDigest string           `json:"parentMutationDigest"`
	RelativeName         string           `json:"relativeName"`
	ObjectIdentity       ObjectIdentityV1 `json:"objectIdentity"`
	ObjectMutationDigest string           `json:"objectMutationDigest"`
}

func (identity CurrentNameIdentityV1) Validate(objectType string) error {
	if identity.ParentIdentity.Validate(ObjectTypeDirectory) != nil || !validDigest(identity.ParentMutationDigest) || identity.ObjectIdentity.Validate(objectType) != nil || !validDigest(identity.ObjectMutationDigest) || !validExistingRelativeName(identity.RelativeName) {
		return ErrInvalid
	}
	return nil
}

// ExistingGitWorktreeIdentityV1 is the material Git identity observed without
// modifying the target or its administration area.
type ExistingGitWorktreeIdentityV1 struct {
	DotGitIdentity                ObjectIdentityV1      `json:"dotGitIdentity"`
	DotGitDigest                  string                `json:"dotGitDigest"`
	DotGitMutationDigest          string                `json:"dotGitMutationDigest"`
	AdminCurrentName              CurrentNameIdentityV1 `json:"adminCurrentName"`
	AdminLocatorDigest            string                `json:"adminLocatorDigest"`
	AdminGitdirIdentity           ObjectIdentityV1      `json:"adminGitdirIdentity"`
	AdminGitdirDigest             string                `json:"adminGitdirDigest"`
	AdminGitdirMutationDigest     string                `json:"adminGitdirMutationDigest"`
	CommonDirFileIdentity         ObjectIdentityV1      `json:"commonDirFileIdentity"`
	CommonDirFileDigest           string                `json:"commonDirFileDigest"`
	CommonDirFileMutationDigest   string                `json:"commonDirFileMutationDigest"`
	CommonDirectoryIdentity       ObjectIdentityV1      `json:"commonDirectoryIdentity"`
	CommonDirectoryMutationDigest string                `json:"commonDirectoryMutationDigest"`
	CommonDirectoryLocatorDigest  string                `json:"commonDirectoryLocatorDigest"`
	HeadIdentity                  ObjectIdentityV1      `json:"headIdentity"`
	HeadDigest                    string                `json:"headDigest"`
	HeadMutationDigest            string                `json:"headMutationDigest"`
	IndexIdentity                 ObjectIdentityV1      `json:"indexIdentity"`
	IndexDigest                   string                `json:"indexDigest"`
	IndexMutationDigest           string                `json:"indexMutationDigest"`
	HeadSHA                       string                `json:"headSha"`
	CleanStatusDigest             string                `json:"cleanStatusDigest"`
}

func (identity ExistingGitWorktreeIdentityV1) Validate() error {
	if identity.DotGitIdentity.Validate(ObjectTypeRegular) != nil || !validDigest(identity.DotGitDigest) || !validDigest(identity.DotGitMutationDigest) || identity.AdminCurrentName.Validate(ObjectTypeDirectory) != nil || !validDigest(identity.AdminLocatorDigest) || identity.AdminGitdirIdentity.Validate(ObjectTypeRegular) != nil || !validDigest(identity.AdminGitdirDigest) || !validDigest(identity.AdminGitdirMutationDigest) || identity.CommonDirFileIdentity.Validate(ObjectTypeRegular) != nil || !validDigest(identity.CommonDirFileDigest) || !validDigest(identity.CommonDirFileMutationDigest) || identity.CommonDirectoryIdentity.Validate(ObjectTypeDirectory) != nil || !validDigest(identity.CommonDirectoryMutationDigest) || !validDigest(identity.CommonDirectoryLocatorDigest) || identity.HeadIdentity.Validate(ObjectTypeRegular) != nil || !validDigest(identity.HeadDigest) || !validDigest(identity.HeadMutationDigest) || identity.IndexIdentity.Validate(ObjectTypeRegular) != nil || !validDigest(identity.IndexDigest) || !validDigest(identity.IndexMutationDigest) || !validGitObjectID(identity.HeadSHA) || !validDigest(identity.CleanStatusDigest) {
		return ErrInvalid
	}
	return nil
}

type ExistingWorktreeObservationV1 struct {
	TargetCurrentName    CurrentNameIdentityV1         `json:"targetCurrentName"`
	TargetLocatorDigest  string                        `json:"targetLocatorDigest"`
	Git                  ExistingGitWorktreeIdentityV1 `json:"git"`
	TargetIdentityDigest string                        `json:"targetIdentityDigest"`
	ObservationDigest    string                        `json:"observationDigest"`
}

func (observation ExistingWorktreeObservationV1) Validate() error {
	if observation.TargetCurrentName.Validate(ObjectTypeDirectory) != nil || !validDigest(observation.TargetLocatorDigest) || observation.Git.Validate() != nil || !validDigest(observation.TargetIdentityDigest) || !validDigest(observation.ObservationDigest) {
		return ErrInvalid
	}
	targetDigest, err := digestValue(observation.TargetCurrentName)
	if err != nil || targetDigest != observation.TargetIdentityDigest {
		return ErrInvalid
	}
	want, err := digestValueWithoutField(observation, "observationDigest")
	if err != nil || want != observation.ObservationDigest {
		return ErrInvalid
	}
	return nil
}

func (observation *ExistingWorktreeObservationV1) Seal() error {
	if observation == nil || observation.TargetCurrentName.Validate(ObjectTypeDirectory) != nil || observation.Git.Validate() != nil {
		return ErrInvalid
	}
	target, err := digestValue(observation.TargetCurrentName)
	if err != nil {
		return err
	}
	observation.TargetIdentityDigest = target
	digest, err := digestValueWithoutField(*observation, "observationDigest")
	if err != nil {
		return err
	}
	observation.ObservationDigest = digest
	return observation.Validate()
}

type ExistingWorktreeBindRequestV1 struct {
	SchemaVersion            string                    `json:"schemaVersion"`
	ProtocolRevision         string                    `json:"protocolRevision"`
	Binding                  ExistingWorktreeBindingV1 `json:"binding"`
	WorktreePath             string                    `json:"worktreePath"`
	ExpectedWorktreeIdentity ObjectIdentityV1          `json:"expectedWorktreeIdentity"`
	ExpectedBaseSHA          string                    `json:"expectedBaseSha"`
	RunDirectoryIdentity     ObjectIdentityV1          `json:"runDirectoryIdentity"`
	RunAuthorityHeadDigest   string                    `json:"runAuthorityHeadDigest"`
	RequestDigest            string                    `json:"requestDigest"`
}

func (request ExistingWorktreeBindRequestV1) Validate() error {
	if request.SchemaVersion != ExistingWorktreeBindRequestSchema || request.ProtocolRevision != ExistingWorktreeProtocolRevision || request.Binding.Validate() != nil || !validCanonicalAbsolutePath(request.WorktreePath) || request.ExpectedWorktreeIdentity.Validate(ObjectTypeDirectory) != nil || !validGitObjectID(request.ExpectedBaseSHA) || request.RunDirectoryIdentity.Validate(ObjectTypeDirectory) != nil || !validDigest(request.RunAuthorityHeadDigest) {
		return ErrInvalid
	}
	want, err := digestValueWithoutField(request, "requestDigest")
	if err != nil || want != request.RequestDigest {
		return ErrInvalid
	}
	return nil
}

func (request *ExistingWorktreeBindRequestV1) Seal() error {
	if request == nil {
		return ErrInvalid
	}
	request.SchemaVersion = ExistingWorktreeBindRequestSchema
	request.ProtocolRevision = ExistingWorktreeProtocolRevision
	digest, err := digestValueWithoutField(*request, "requestDigest")
	if err != nil {
		return err
	}
	request.RequestDigest = digest
	return request.Validate()
}

type ExistingWorktreeBindIntentV1 struct {
	SchemaVersion            string                        `json:"schemaVersion"`
	ProtocolRevision         string                        `json:"protocolRevision"`
	Request                  ExistingWorktreeBindRequestV1 `json:"request"`
	Observation              ExistingWorktreeObservationV1 `json:"observation"`
	BindingDigest            string                        `json:"bindingDigest"`
	PredecessorRB1HeadDigest string                        `json:"predecessorRb1HeadDigest"`
	IntentDigest             string                        `json:"intentDigest"`
}

func (intent ExistingWorktreeBindIntentV1) Validate() error {
	if intent.SchemaVersion != ExistingWorktreeBindIntentSchema || intent.ProtocolRevision != ExistingWorktreeProtocolRevision || intent.Request.Validate() != nil || intent.Observation.Validate() != nil || !sameDirectoryObject(intent.Request.ExpectedWorktreeIdentity, intent.Observation.TargetCurrentName.ObjectIdentity) || intent.Request.ExpectedBaseSHA != intent.Observation.Git.HeadSHA || !validDigest(intent.BindingDigest) || !validDigest(intent.PredecessorRB1HeadDigest) {
		return ErrInvalid
	}
	bindingDigest, err := intent.Request.Binding.Digest()
	if err != nil || bindingDigest != intent.BindingDigest {
		return ErrInvalid
	}
	want, err := digestValueWithoutField(intent, "intentDigest")
	if err != nil || want != intent.IntentDigest {
		return ErrInvalid
	}
	return nil
}

func (intent *ExistingWorktreeBindIntentV1) Seal() error {
	if intent == nil {
		return ErrInvalid
	}
	intent.SchemaVersion = ExistingWorktreeBindIntentSchema
	intent.ProtocolRevision = ExistingWorktreeProtocolRevision
	digest, err := digestValueWithoutField(*intent, "intentDigest")
	if err != nil {
		return err
	}
	intent.IntentDigest = digest
	return intent.Validate()
}

type ExistingWorktreeBindReceiptV1 struct {
	SchemaVersion            string                        `json:"schemaVersion"`
	ProtocolRevision         string                        `json:"protocolRevision"`
	Binding                  ExistingWorktreeBindingV1     `json:"binding"`
	RequestDigest            string                        `json:"requestDigest"`
	IntentFactDigest         string                        `json:"intentFactDigest"`
	Observation              ExistingWorktreeObservationV1 `json:"observation"`
	BindingDigest            string                        `json:"bindingDigest"`
	PredecessorRB1HeadDigest string                        `json:"predecessorRb1HeadDigest"`
	Disposition              string                        `json:"disposition"`
	ReceiptDigest            string                        `json:"receiptDigest"`
}

func (receipt ExistingWorktreeBindReceiptV1) Validate(intent ExistingWorktreeBindIntentV1) error {
	if intent.Validate() != nil || receipt.SchemaVersion != ExistingWorktreeBindReceiptSchema || receipt.ProtocolRevision != ExistingWorktreeProtocolRevision || receipt.Binding != intent.Request.Binding || receipt.RequestDigest != intent.Request.RequestDigest || !validDigest(receipt.IntentFactDigest) || !equalCanonical(receipt.Observation, intent.Observation) || receipt.BindingDigest != intent.BindingDigest || !validDigest(receipt.PredecessorRB1HeadDigest) || receipt.Disposition != DispositionApplied {
		return ErrInvalid
	}
	want, err := digestValueWithoutField(receipt, "receiptDigest")
	if err != nil || want != receipt.ReceiptDigest {
		return ErrInvalid
	}
	return nil
}

func (receipt *ExistingWorktreeBindReceiptV1) Seal() error {
	if receipt == nil {
		return ErrInvalid
	}
	receipt.SchemaVersion = ExistingWorktreeBindReceiptSchema
	receipt.ProtocolRevision = ExistingWorktreeProtocolRevision
	digest, err := digestValueWithoutField(*receipt, "receiptDigest")
	if err != nil {
		return err
	}
	receipt.ReceiptDigest = digest
	return nil
}

type ExistingWorktreeReleaseRequestV1 struct {
	SchemaVersion              string                    `json:"schemaVersion"`
	ProtocolRevision           string                    `json:"protocolRevision"`
	Binding                    ExistingWorktreeBindingV1 `json:"binding"`
	BindingReceiptDigest       string                    `json:"bindingReceiptDigest"`
	TerminalizationID          string                    `json:"terminalizationId"`
	CleanupBindingDigest       string                    `json:"cleanupBindingDigest"`
	ProcessTerminalFactDigest  string                    `json:"processTerminalFactDigest"`
	CleanupDisposition         string                    `json:"cleanupDisposition"`
	RunAuthorityHeadDigest     string                    `json:"runAuthorityHeadDigest"`
	AttemptAuthorityHeadDigest string                    `json:"attemptAuthorityHeadDigest"`
	RequestDigest              string                    `json:"requestDigest"`
}

func (request ExistingWorktreeReleaseRequestV1) Validate() error {
	if request.SchemaVersion != ExistingWorktreeReleaseRequestSchema || request.ProtocolRevision != ExistingWorktreeProtocolRevision || request.Binding.Validate() != nil || !validDigest(request.BindingReceiptDigest) || !validText(request.TerminalizationID) || !validDigest(request.CleanupBindingDigest) || !validDigest(request.ProcessTerminalFactDigest) || request.CleanupDisposition != "preserved" || !validDigest(request.RunAuthorityHeadDigest) || !validDigest(request.AttemptAuthorityHeadDigest) {
		return ErrInvalid
	}
	want, err := digestValueWithoutField(request, "requestDigest")
	if err != nil || want != request.RequestDigest {
		return ErrInvalid
	}
	return nil
}

func (request *ExistingWorktreeReleaseRequestV1) Seal() error {
	if request == nil {
		return ErrInvalid
	}
	request.SchemaVersion = ExistingWorktreeReleaseRequestSchema
	request.ProtocolRevision = ExistingWorktreeProtocolRevision
	digest, err := digestValueWithoutField(*request, "requestDigest")
	if err != nil {
		return err
	}
	request.RequestDigest = digest
	return request.Validate()
}

type ExistingWorktreeReleaseIntentV1 struct {
	SchemaVersion            string                           `json:"schemaVersion"`
	ProtocolRevision         string                           `json:"protocolRevision"`
	Request                  ExistingWorktreeReleaseRequestV1 `json:"request"`
	TargetIdentityDigest     string                           `json:"targetIdentityDigest"`
	BindingDigest            string                           `json:"bindingDigest"`
	PredecessorRB1HeadDigest string                           `json:"predecessorRb1HeadDigest"`
	IntentDigest             string                           `json:"intentDigest"`
}

func (intent ExistingWorktreeReleaseIntentV1) Validate() error {
	if intent.SchemaVersion != ExistingWorktreeReleaseIntentSchema || intent.ProtocolRevision != ExistingWorktreeProtocolRevision || intent.Request.Validate() != nil || !validDigest(intent.TargetIdentityDigest) || !validDigest(intent.BindingDigest) || !validDigest(intent.PredecessorRB1HeadDigest) {
		return ErrInvalid
	}
	bindingDigest, err := intent.Request.Binding.Digest()
	if err != nil || bindingDigest != intent.BindingDigest {
		return ErrInvalid
	}
	want, err := digestValueWithoutField(intent, "intentDigest")
	if err != nil || want != intent.IntentDigest {
		return ErrInvalid
	}
	return nil
}

func (intent *ExistingWorktreeReleaseIntentV1) Seal() error {
	if intent == nil {
		return ErrInvalid
	}
	intent.SchemaVersion = ExistingWorktreeReleaseIntentSchema
	intent.ProtocolRevision = ExistingWorktreeProtocolRevision
	digest, err := digestValueWithoutField(*intent, "intentDigest")
	if err != nil {
		return err
	}
	intent.IntentDigest = digest
	return intent.Validate()
}

type ExistingWorktreeReleaseReceiptV1 struct {
	SchemaVersion            string                    `json:"schemaVersion"`
	ProtocolRevision         string                    `json:"protocolRevision"`
	Binding                  ExistingWorktreeBindingV1 `json:"binding"`
	RequestDigest            string                    `json:"requestDigest"`
	ReleaseIntentFactDigest  string                    `json:"releaseIntentFactDigest"`
	BindingReceiptDigest     string                    `json:"bindingReceiptDigest"`
	TargetIdentityDigest     string                    `json:"targetIdentityDigest"`
	PredecessorRB1HeadDigest string                    `json:"predecessorRb1HeadDigest"`
	Disposition              string                    `json:"disposition"`
	ReceiptDigest            string                    `json:"receiptDigest"`
}

func (receipt ExistingWorktreeReleaseReceiptV1) Validate(intent ExistingWorktreeReleaseIntentV1) error {
	if intent.Validate() != nil || receipt.SchemaVersion != ExistingWorktreeReleaseReceiptSchema || receipt.ProtocolRevision != ExistingWorktreeProtocolRevision || receipt.Binding != intent.Request.Binding || receipt.RequestDigest != intent.Request.RequestDigest || !validDigest(receipt.ReleaseIntentFactDigest) || receipt.BindingReceiptDigest != intent.Request.BindingReceiptDigest || receipt.TargetIdentityDigest != intent.TargetIdentityDigest || !validDigest(receipt.PredecessorRB1HeadDigest) || receipt.Disposition != "released" {
		return ErrInvalid
	}
	want, err := digestValueWithoutField(receipt, "receiptDigest")
	if err != nil || want != receipt.ReceiptDigest {
		return ErrInvalid
	}
	return nil
}

func (receipt *ExistingWorktreeReleaseReceiptV1) Seal() error {
	if receipt == nil {
		return ErrInvalid
	}
	receipt.SchemaVersion = ExistingWorktreeReleaseReceiptSchema
	receipt.ProtocolRevision = ExistingWorktreeProtocolRevision
	digest, err := digestValueWithoutField(*receipt, "receiptDigest")
	if err != nil {
		return err
	}
	receipt.ReceiptDigest = digest
	return nil
}

// ExistingWorktreeAttemptFactV1 is a read-only projection of one fact from
// the shared ResultIngress Attempt ledger.  It deliberately has no private
// sequence or genesis: AttemptRevision/PreviousAttemptHeadDigest and the
// outer AttemptFactDigest are the only authority chain.
type ExistingWorktreeAttemptFactV1 struct {
	AttemptKey                string                   `json:"attemptKey"`
	AttemptRevision           uint64                   `json:"attemptRevision"`
	Kind                      ExistingWorktreeFactKind `json:"kind"`
	PreviousAttemptHeadDigest string                   `json:"previousAttemptHeadDigest"`
	Payload                   json.RawMessage          `json:"payload"`
	PayloadDigest             string                   `json:"payloadDigest"`
	AttemptFactDigest         string                   `json:"attemptFactDigest"`
}

// ExistingWorktreeProjectionRecordV1 is intentionally path-free. It contains
// only the closed identifiers needed to prove an exact RB1 prefix.
type ExistingWorktreeProjectionRecordV1 struct {
	SchemaVersion            string                   `json:"schemaVersion"`
	ProtocolRevision         string                   `json:"protocolRevision"`
	AttemptRevision          uint64                   `json:"attemptRevision"`
	Kind                     ExistingWorktreeFactKind `json:"kind"`
	AttemptFactDigest        string                   `json:"attemptFactDigest"`
	AuthorityPayloadDigest   string                   `json:"authorityPayloadDigest"`
	BindingDigest            string                   `json:"bindingDigest"`
	TargetIdentityDigest     string                   `json:"targetIdentityDigest"`
	RequestDigest            string                   `json:"requestDigest"`
	PreviousProjectionDigest string                   `json:"previousProjectionDigest"`
	ProjectionDigest         string                   `json:"projectionDigest"`
}

func (record ExistingWorktreeProjectionRecordV1) Validate() error {
	if record.SchemaVersion != ExistingWorktreeProjectionSchema || record.ProtocolRevision != ExistingWorktreeProtocolRevision || record.AttemptRevision == 0 || record.AttemptRevision > maxSafeJSONInteger || !validExistingWorktreeFactKind(record.Kind) || !validDigest(record.AttemptFactDigest) || !validDigest(record.AuthorityPayloadDigest) || !validDigest(record.BindingDigest) || !validDigest(record.TargetIdentityDigest) || !validDigest(record.RequestDigest) || !validDigest(record.PreviousProjectionDigest) || !validDigest(record.ProjectionDigest) {
		return ErrInvalid
	}
	want, err := digestValueWithoutField(record, "projectionDigest")
	if err != nil || want != record.ProjectionDigest {
		return ErrInvalid
	}
	return nil
}

func (record *ExistingWorktreeProjectionRecordV1) Seal() error {
	if record == nil {
		return ErrInvalid
	}
	record.SchemaVersion = ExistingWorktreeProjectionSchema
	record.ProtocolRevision = ExistingWorktreeProtocolRevision
	digest, err := digestValueWithoutField(*record, "projectionDigest")
	if err != nil {
		return err
	}
	record.ProjectionDigest = digest
	return record.Validate()
}

func (fact ExistingWorktreeAttemptFactV1) Validate() error {
	if !validDigest(fact.AttemptKey) || fact.AttemptRevision == 0 || fact.AttemptRevision > maxSafeJSONInteger || !validExistingWorktreeFactKind(fact.Kind) || !validDigest(fact.PreviousAttemptHeadDigest) || len(fact.Payload) == 0 || !validDigest(fact.PayloadDigest) || !validDigest(fact.AttemptFactDigest) {
		return ErrInvalid
	}
	canonicalPayload, err := canonicalValue(json.RawMessage(fact.Payload))
	if err != nil || canonicalPayload == nil || !json.Valid(fact.Payload) {
		return ErrInvalid
	}
	// canonicalValue on RawMessage canonicalizes the embedded value.
	if string(canonicalPayload) != string(fact.Payload) || digestBytes(fact.Payload) != fact.PayloadDigest {
		return ErrInvalid
	}
	return nil
}

func validExistingWorktreeFactKind(kind ExistingWorktreeFactKind) bool {
	return kind == ExistingWorktreeFactBindIntent || kind == ExistingWorktreeFactBindReceipt || kind == ExistingWorktreeFactReleaseIntent || kind == ExistingWorktreeFactReleaseReceipt
}

func validGitObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, r := range value {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func validCanonicalAbsolutePath(value string) bool {
	return value != "" && filepath.IsAbs(value) && filepath.Clean(value) == value && value != string(filepath.Separator) && !strings.ContainsRune(value, 0)
}

func digestBytes(value []byte) string {
	return canonical.DigestBytes(value)
}

func validExistingRelativeName(value string) bool {
	return validPrintableASCII(value, 255) && value != "." && value != ".." && !strings.ContainsAny(value, `/\\`)
}
