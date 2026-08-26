package selfidentity

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

type identitySubjectDigestInput struct {
	ActivationDigest        string `json:"activationDigest"`
	RepositoryIdentity      string `json:"repositoryIdentity"`
	CanonicalRepositoryRoot string `json:"canonicalRepositoryRoot"`
	CanonicalExecutablePath string `json:"canonicalExecutablePath"`
	Device                  string `json:"device"`
	Inode                   string `json:"inode"`
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
	CurrentPathObject       CurrentPathObjectV1 `json:"currentPathObject"`
	SourceHead              string              `json:"sourceHead"`
	SelfProfile             string              `json:"selfProfile"`
	ObservedAt              string              `json:"observedAt"`
	Status                  string              `json:"status"`
	ReasonCode              string              `json:"reasonCode"`
	IdentitySubjectDigest   string              `json:"identitySubjectDigest"`
}

// Admit validates an operator activation and produces a current-path object
// observation for the running process. The returned observation is in-memory;
// LD-1 deliberately does not write Run/Attempt lineage.
func Admit(activationPath, commandClass, workingDirectory string, build BuildIdentity, now time.Time) (LocalSelfIdentityObservationV1, error) {
	executablePath, err := currentExecutablePath()
	if err != nil {
		return failedObservation(build, now, ReasonObjectMismatch), reject(ReasonObjectMismatch)
	}
	return admit(activationPath, commandClass, workingDirectory, executablePath, build, now, nil)
}

func admit(activationPath, commandClass, workingDirectory, executablePath string, build BuildIdentity, now time.Time, afterObjectRead func()) (LocalSelfIdentityObservationV1, error) {
	if build.SelfProfile != LocalProfile {
		return failedObservation(build, now, ReasonProfileMismatch), reject(ReasonProfileMismatch)
	}
	if !platformSupported() {
		return failedObservation(build, now, ReasonProfileMismatch), reject(ReasonProfileMismatch)
	}
	raw, err := readActivation(activationPath)
	if err != nil {
		return failedObservation(build, now, reason(err)), err
	}
	activation, err := DecodeActivation(raw, now)
	if err != nil {
		return failedObservation(build, now, reason(err)), err
	}
	if !activation.permits(commandClass) {
		return failedObservation(build, now, ReasonCommandDenied), reject(ReasonCommandDenied)
	}
	if activation.ExpectedSourceHead != build.SourceHead {
		return failedObservation(build, now, ReasonSourceMismatch), reject(ReasonSourceMismatch)
	}
	if activation.ExpectedSelfProfile != build.SelfProfile {
		return failedObservation(build, now, ReasonProfileMismatch), reject(ReasonProfileMismatch)
	}
	if !workingDirectoryWithinRoot(workingDirectory, activation.CanonicalRepositoryRoot) {
		return failedObservation(build, now, ReasonObjectMismatch), reject(ReasonObjectMismatch)
	}
	processPath, err := canonicalRegularPath(executablePath)
	if err != nil || processPath != activation.CanonicalExecutablePath {
		return failedObservation(build, now, ReasonObjectMismatch), reject(ReasonObjectMismatch)
	}
	object, err := observeCurrentPath(processPath, afterObjectRead)
	if err != nil {
		return failedObservation(build, now, reason(err)), err
	}
	if object.CanonicalPath != activation.CanonicalExecutablePath || object.Device != activation.ExpectedDevice ||
		object.Inode != activation.ExpectedInode || object.Size != activation.ExpectedSize ||
		object.RawSHA256 != activation.ExpectedRawSHA256 || !object.PathRechecked {
		return failedObservation(build, now, ReasonObjectMismatch), reject(ReasonObjectMismatch)
	}
	observation := LocalSelfIdentityObservationV1{
		SchemaVersion: ActivationToObservationSchema(), ActivationDigest: activation.ActivationDigest,
		ProcessID: os.Getpid(), ProcessExecutablePath: processPath,
		RepositoryIdentity: activation.RepositoryIdentity, CanonicalRepositoryRoot: activation.CanonicalRepositoryRoot,
		CurrentPathObject: object, SourceHead: build.SourceHead, SelfProfile: build.SelfProfile,
		ObservedAt: now.UTC().Truncate(time.Second).Format(time.RFC3339), Status: "pass", ReasonCode: ReasonObserved,
	}
	observation.IdentitySubjectDigest, err = digestIdentitySubject(observation)
	if err != nil {
		return failedObservation(build, now, ReasonObjectMismatch), reject(ReasonObjectMismatch)
	}
	observation.ObservationDigest, err = digestObservation(observation)
	if err != nil {
		return failedObservation(build, now, ReasonObjectMismatch), reject(ReasonObjectMismatch)
	}
	return observation, nil
}

// ActivationToObservationSchema keeps the schema name use at the producer
// boundary explicit rather than relying on a zero-value literal.
func ActivationToObservationSchema() string { return ObservationSchema }

func failedObservation(build BuildIdentity, now time.Time, reasonCode string) LocalSelfIdentityObservationV1 {
	return LocalSelfIdentityObservationV1{
		SchemaVersion: ObservationSchema, ProcessID: os.Getpid(), SourceHead: build.SourceHead,
		SelfProfile: build.SelfProfile, ObservedAt: now.UTC().Truncate(time.Second).Format(time.RFC3339),
		Status: "fail", ReasonCode: reasonCode,
	}
}

func digestIdentitySubject(observation LocalSelfIdentityObservationV1) (string, error) {
	input := identitySubjectDigestInput{
		ActivationDigest: observation.ActivationDigest, RepositoryIdentity: observation.RepositoryIdentity,
		CanonicalRepositoryRoot: observation.CanonicalRepositoryRoot,
		CanonicalExecutablePath: observation.CurrentPathObject.CanonicalPath,
		Device:                  observation.CurrentPathObject.Device, Inode: observation.CurrentPathObject.Inode,
		Size: observation.CurrentPathObject.Size, RawSHA256: observation.CurrentPathObject.RawSHA256,
		SourceHead: observation.SourceHead, SelfProfile: observation.SelfProfile,
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	return canonical.DigestJSON(raw)
}

func digestObservation(observation LocalSelfIdentityObservationV1) (string, error) {
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

func executablePathNamesObject(path string, object CurrentPathObjectV1) bool {
	current, err := observePathIdentity(path)
	return err == nil && filepath.Clean(path) == path && current.Device == object.Device &&
		current.Inode == object.Inode && current.Size == object.Size
}
