// Package selfidentity implements Marshal's low-assurance Darwin local
// dogfood self-identity gate. It deliberately does not implement managed or
// release authority.
package selfidentity

import (
	"errors"
)

const (
	ActivationSchema     = "marshal.local-dogfood-activation.v2"
	ObservationSchema    = "marshal.local-self-identity-observation.v2"
	AttemptBindingSchema = "marshal.local-self-identity-binding.v2"
	LocalProfile         = "darwin-local-dogfood"
	ActivationEnv        = "MARSHAL_LOCAL_DOGFOOD_ACTIVATION"

	CommandDoctor          = "doctor"
	CommandInit            = "init"
	CommandTaskScaffold    = "task-scaffold"
	CommandTaskPlan        = "task-plan"
	CommandTaskStatus      = "task-status"
	CommandTaskApprovePlan = "task-approve-plan"
	CommandTaskRun         = "task-run"
	CommandTaskVerify      = "task-verify"
	CommandTaskReview      = "task-review"

	ReasonObserved                   = "self-local-identity-observed"
	ReasonOptInMissing               = "self-local-opt-in-missing"
	ReasonObjectMismatch             = "self-local-object-mismatch"
	ReasonSourceMismatch             = "self-local-source-mismatch"
	ReasonProfileMismatch            = "self-local-profile-mismatch"
	ReasonPublicationDenied          = "self-local-publication-denied"
	ReasonCredentialedEffectDenied   = "self-local-credentialed-effect-denied"
	ReasonRemoteSurfaceDenied        = "self-local-remote-surface-denied"
	ReasonCrossProfileEvidence       = "self-local-cross-profile-evidence"
	ReasonExecKilledBeforeDiagnostic = "self-local-exec-killed-before-diagnostic"
	ReasonCommandDenied              = "self-local-command-denied"
)

const (
	maxActivationBytes = 64 << 10
	maxExecutableBytes = 256 << 20
)

// GateError exposes only a closed reason code. It never wraps path- or
// input-derived errors, so CLI diagnostics cannot leak local paths or payloads.
type GateError struct {
	ReasonCode string
}

func (e *GateError) Error() string { return "local dogfood self gate: " + e.ReasonCode }

func reason(err error) string {
	var gate *GateError
	if errors.As(err, &gate) {
		return gate.ReasonCode
	}
	return ReasonObjectMismatch
}

func reject(code string) error { return &GateError{ReasonCode: code} }

// ReasonCode returns the stable, non-sensitive rejection identity.
func ReasonCode(err error) string { return reason(err) }

// BuildIdentity is the immutable build metadata consumed by the self gate.
type BuildIdentity struct {
	SourceHead  string
	SelfProfile string
}

// LocalDogfoodScopeV2 is deliberately closed to the currently admitted local
// dogfood surfaces; later phases must extend it explicitly rather than infer
// authority from a profile name.
type LocalDogfoodScopeV2 struct {
	Network                 string   `json:"network"`
	Publication             string   `json:"publication"`
	AdapterAuthority        string   `json:"adapterAuthority"`
	LifecycleCommandClasses []string `json:"lifecycleCommandClasses"`
}

// LocalDogfoodActivationV2 is operator-owned opt-in. Marshal may render its
// canonical bytes, but never writes the activation file.
type LocalDogfoodActivationV2 struct {
	SchemaVersion           string              `json:"schemaVersion"`
	ActivationID            string              `json:"activationId"`
	IssuedAt                string              `json:"issuedAt"`
	ValidUntil              string              `json:"validUntil"`
	RepositoryIdentity      string              `json:"repositoryIdentity"`
	CanonicalRepositoryRoot string              `json:"canonicalRepositoryRoot"`
	CanonicalExecutablePath string              `json:"canonicalExecutablePath"`
	ExpectedSize            int64               `json:"expectedSize"`
	ExpectedRawSHA256       string              `json:"expectedRawSHA256"`
	ExpectedSourceHead      string              `json:"expectedSourceHead"`
	ExpectedSelfProfile     string              `json:"expectedSelfProfile"`
	Scope                   LocalDogfoodScopeV2 `json:"scope"`
	ActivationDigest        string              `json:"activationDigest"`
}

type CurrentPathObjectV2 struct {
	CanonicalPath   string `json:"canonicalPath"`
	Device          string `json:"device"`
	Inode           string `json:"inode"`
	Size            int64  `json:"size"`
	RawSHA256       string `json:"rawSHA256"`
	PathRechecked   bool   `json:"pathRechecked"`
	ObservationKind string `json:"observationKind"`
}

// LocalSelfIdentityObservationV2 is a Core-owned, process-local fact. Its
// name and fields intentionally avoid install-receipt/current/held authority.
type LocalSelfIdentityObservationV2 struct {
	SchemaVersion           string              `json:"schemaVersion"`
	ActivationDigest        string              `json:"activationDigest"`
	ProcessID               int                 `json:"processId"`
	ProcessExecutablePath   string              `json:"processExecutablePath"`
	RepositoryIdentity      string              `json:"repositoryIdentity"`
	CanonicalRepositoryRoot string              `json:"canonicalRepositoryRoot"`
	CurrentPathObject       CurrentPathObjectV2 `json:"currentPathObject"`
	SourceHead              string              `json:"sourceHead"`
	SelfProfile             string              `json:"selfProfile"`
	ObservedAt              string              `json:"observedAt"`
	Status                  string              `json:"status"`
	ReasonCode              string              `json:"reasonCode"`
	IdentitySubjectDigest   string              `json:"identitySubjectDigest"`
	ObservationDigest       string              `json:"observationDigest"`
}

// LocalSelfIdentityBindingV2 is the closed projection copied into a local
// WorkerRequest. It points at Core's persisted dispatch observation; an
// Adapter cannot mint or refresh any of these fields.
type LocalSelfIdentityBindingV2 struct {
	SchemaVersion             string `json:"schemaVersion"`
	SelfProfile               string `json:"selfProfile"`
	ActivationDigest          string `json:"activationDigest"`
	IdentitySubjectDigest     string `json:"identitySubjectDigest"`
	DispatchObservationDigest string `json:"dispatchObservationDigest"`
}

func (a LocalDogfoodActivationV2) permits(commandClass string) bool {
	for _, candidate := range a.Scope.LifecycleCommandClasses {
		if candidate == commandClass {
			return true
		}
	}
	return false
}
