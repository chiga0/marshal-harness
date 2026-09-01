package selfidentity

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

type identitySubjectDigestInput struct {
	ActivationDigest        string `json:"activationDigest"`
	RepositoryIdentity      string `json:"repositoryIdentity"`
	CanonicalRepositoryRoot string `json:"canonicalRepositoryRoot"`
	CanonicalExecutablePath string `json:"canonicalExecutablePath"`
	Size                    int64  `json:"size"`
	RawSHA256               string `json:"rawSHA256"`
	SourceHead              string `json:"sourceHead"`
	SelfProfile             string `json:"selfProfile"`
}

type observationDigestInput struct {
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
}

// Admit validates an operator activation and, only on success, produces a
// current-path object observation for the running process. Rejections return a
// closed typed error and no versioned observation. The successful observation
// is in-memory; self-identity admission deliberately does not write
// Run/Attempt lineage.
func Admit(activationPath, commandClass, workingDirectory string, build BuildIdentity, now time.Time) (LocalSelfIdentityObservationV2, error) {
	executablePath, err := currentExecutablePath()
	if err != nil {
		return LocalSelfIdentityObservationV2{}, reject(ReasonObjectMismatch)
	}
	return admit(activationPath, commandClass, workingDirectory, executablePath, build, now, nil)
}

func admit(activationPath, commandClass, workingDirectory, executablePath string, build BuildIdentity, now time.Time, afterObjectRead func()) (LocalSelfIdentityObservationV2, error) {
	if build.SelfProfile != LocalProfile {
		return LocalSelfIdentityObservationV2{}, reject(ReasonProfileMismatch)
	}
	if !platformSupported() {
		return LocalSelfIdentityObservationV2{}, reject(ReasonProfileMismatch)
	}
	raw, err := readActivation(activationPath)
	if err != nil {
		return LocalSelfIdentityObservationV2{}, err
	}
	activation, err := DecodeActivation(raw, now)
	if err != nil {
		return LocalSelfIdentityObservationV2{}, err
	}
	if !activation.permits(commandClass) {
		return LocalSelfIdentityObservationV2{}, reject(ReasonCommandDenied)
	}
	if activation.ExpectedSourceHead != build.SourceHead {
		return LocalSelfIdentityObservationV2{}, reject(ReasonSourceMismatch)
	}
	if activation.ExpectedSelfProfile != build.SelfProfile {
		return LocalSelfIdentityObservationV2{}, reject(ReasonProfileMismatch)
	}
	if !workingDirectoryWithinRoot(workingDirectory, activation.CanonicalRepositoryRoot) {
		return LocalSelfIdentityObservationV2{}, reject(ReasonObjectMismatch)
	}
	processPath, err := canonicalRegularPath(executablePath)
	if err != nil || processPath != activation.CanonicalExecutablePath {
		return LocalSelfIdentityObservationV2{}, reject(ReasonObjectMismatch)
	}
	object, err := observeCurrentPath(processPath, afterObjectRead)
	if err != nil {
		return LocalSelfIdentityObservationV2{}, err
	}
	if object.CanonicalPath != activation.CanonicalExecutablePath || object.Size != activation.ExpectedSize ||
		object.RawSHA256 != activation.ExpectedRawSHA256 || !object.PathRechecked {
		return LocalSelfIdentityObservationV2{}, reject(ReasonObjectMismatch)
	}
	observation := LocalSelfIdentityObservationV2{
		SchemaVersion: ActivationToObservationSchema(), ActivationDigest: activation.ActivationDigest,
		ProcessID: os.Getpid(), ProcessExecutablePath: processPath,
		RepositoryIdentity: activation.RepositoryIdentity, CanonicalRepositoryRoot: activation.CanonicalRepositoryRoot,
		CurrentPathObject: object, SourceHead: build.SourceHead, SelfProfile: build.SelfProfile,
		ObservedAt: now.UTC().Truncate(time.Second).Format(time.RFC3339), Status: "pass", ReasonCode: ReasonObserved,
	}
	observation.IdentitySubjectDigest, err = digestIdentitySubject(observation)
	if err != nil {
		return LocalSelfIdentityObservationV2{}, reject(ReasonObjectMismatch)
	}
	observation.ObservationDigest, err = digestObservation(observation)
	if err != nil {
		return LocalSelfIdentityObservationV2{}, reject(ReasonObjectMismatch)
	}
	return observation, nil
}

// ActivationToObservationSchema keeps the schema name use at the producer
// boundary explicit rather than relying on a zero-value literal.
func ActivationToObservationSchema() string { return ObservationSchema }

func digestIdentitySubject(observation LocalSelfIdentityObservationV2) (string, error) {
	input := identitySubjectDigestInput{
		ActivationDigest: observation.ActivationDigest, RepositoryIdentity: observation.RepositoryIdentity,
		CanonicalRepositoryRoot: observation.CanonicalRepositoryRoot,
		CanonicalExecutablePath: observation.CurrentPathObject.CanonicalPath,
		Size:                    observation.CurrentPathObject.Size, RawSHA256: observation.CurrentPathObject.RawSHA256,
		SourceHead: observation.SourceHead, SelfProfile: observation.SelfProfile,
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	return canonical.DigestJSON(raw)
}

func digestObservation(observation LocalSelfIdentityObservationV2) (string, error) {
	input := observationDigestInput{
		SchemaVersion: observation.SchemaVersion, ActivationDigest: observation.ActivationDigest,
		ProcessID: observation.ProcessID, ProcessExecutablePath: observation.ProcessExecutablePath,
		RepositoryIdentity: observation.RepositoryIdentity, CanonicalRepositoryRoot: observation.CanonicalRepositoryRoot,
		CurrentPathObject: observation.CurrentPathObject, SourceHead: observation.SourceHead,
		SelfProfile: observation.SelfProfile, ObservedAt: observation.ObservedAt,
		Status: observation.Status, ReasonCode: observation.ReasonCode,
		IdentitySubjectDigest: observation.IdentitySubjectDigest,
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	return canonical.DigestJSON(raw)
}

// BindingForObservation projects one validated Core observation into the
// only local identity fields an Adapter may receive.
func BindingForObservation(observation LocalSelfIdentityObservationV2) (LocalSelfIdentityBindingV2, error) {
	if err := ValidateObservation(observation); err != nil {
		return LocalSelfIdentityBindingV2{}, err
	}
	return LocalSelfIdentityBindingV2{
		SchemaVersion: AttemptBindingSchema, SelfProfile: LocalProfile,
		ActivationDigest:          observation.ActivationDigest,
		IdentitySubjectDigest:     observation.IdentitySubjectDigest,
		DispatchObservationDigest: observation.ObservationDigest,
	}, nil
}

// ValidateObservation recomputes both digests and admits only the closed,
// successful v2 shape. It is pure and does not turn a persisted observation
// into installation, location, or publication authority.
func ValidateObservation(observation LocalSelfIdentityObservationV2) error {
	if observation.SchemaVersion != ObservationSchema || observation.SelfProfile != LocalProfile ||
		observation.Status != "pass" || observation.ReasonCode != ReasonObserved ||
		observation.ProcessID <= 0 || observation.ProcessExecutablePath == "" ||
		observation.ProcessExecutablePath != observation.CurrentPathObject.CanonicalPath ||
		observation.CanonicalRepositoryRoot == "" || !validDigest(observation.RepositoryIdentity) ||
		observation.CurrentPathObject.CanonicalPath == "" || !observation.CurrentPathObject.PathRechecked ||
		observation.CurrentPathObject.Device == "" || observation.CurrentPathObject.Inode == "" ||
		observation.CurrentPathObject.Size <= 0 || !validDigest(observation.CurrentPathObject.RawSHA256) ||
		observation.CurrentPathObject.ObservationKind != "darwin-current-path-fd-object" ||
		!sourceHeadPattern.MatchString(observation.SourceHead) {
		return reject(ReasonObjectMismatch)
	}
	if _, ok := canonicalTimestamp(observation.ObservedAt); !ok {
		return reject(ReasonObjectMismatch)
	}
	subject, err := digestIdentitySubject(observation)
	if err != nil || subject != observation.IdentitySubjectDigest {
		return reject(ReasonObjectMismatch)
	}
	digest, err := digestObservation(observation)
	if err != nil || digest != observation.ObservationDigest {
		return reject(ReasonObjectMismatch)
	}
	return nil
}

// DecodeObservation admits exact JCS bytes and a closed v2 observation.
func DecodeObservation(raw []byte) (LocalSelfIdentityObservationV2, error) {
	canonicalRaw, err := canonical.JSON(raw)
	if err != nil || !bytes.Equal(raw, canonicalRaw) {
		return LocalSelfIdentityObservationV2{}, reject(ReasonObjectMismatch)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var observation LocalSelfIdentityObservationV2
	if err := decoder.Decode(&observation); err != nil {
		return LocalSelfIdentityObservationV2{}, reject(ReasonObjectMismatch)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return LocalSelfIdentityObservationV2{}, reject(ReasonObjectMismatch)
	}
	if err := ValidateObservation(observation); err != nil {
		return LocalSelfIdentityObservationV2{}, err
	}
	return observation, nil
}

// ValidateBinding requires the exact Core observation projection.
func ValidateBinding(binding LocalSelfIdentityBindingV2, observation LocalSelfIdentityObservationV2) error {
	if err := ValidateObservation(observation); err != nil {
		return err
	}
	if binding.SchemaVersion != AttemptBindingSchema || binding.SelfProfile != LocalProfile ||
		binding.ActivationDigest != observation.ActivationDigest ||
		binding.IdentitySubjectDigest != observation.IdentitySubjectDigest ||
		binding.DispatchObservationDigest != observation.ObservationDigest {
		return reject(ReasonCrossProfileEvidence)
	}
	return nil
}

// SameSubject requires two process observations to describe the same local
// activation and executable subject. Their observation digests may differ
// because a fresh observation has a later observedAt.
func SameSubject(left, right LocalSelfIdentityObservationV2) error {
	if err := ValidateObservation(left); err != nil {
		return err
	}
	if err := ValidateObservation(right); err != nil {
		return err
	}
	if left.ActivationDigest != right.ActivationDigest || left.IdentitySubjectDigest != right.IdentitySubjectDigest {
		return reject(ReasonCrossProfileEvidence)
	}
	return nil
}
