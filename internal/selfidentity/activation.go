package selfidentity

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

const maxActivationFreshness = 24 * time.Hour

var (
	idPattern         = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	sourceHeadPattern = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
)

type activationDigestInput struct {
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
}

// BootstrapOptions controls canonical activation rendering. Marshal returns
// the bytes to stdout; the operator remains responsible for storing them.
type BootstrapOptions struct {
	RepositoryRoot string
	ActivationID   string
	IssuedAt       time.Time
	ValidUntil     time.Time
	Build          BuildIdentity
	ExecutablePath string
}

// RenderActivation returns exact RFC 8785 bytes without a trailing newline.
func RenderActivation(options BootstrapOptions) ([]byte, error) {
	root, err := canonicalDirectory(options.RepositoryRoot)
	if err != nil {
		return nil, reject(ReasonObjectMismatch)
	}
	if !sourceHeadPattern.MatchString(options.Build.SourceHead) {
		return nil, reject(ReasonSourceMismatch)
	}
	if options.Build.SelfProfile != LocalProfile {
		return nil, reject(ReasonProfileMismatch)
	}
	issuedAt := options.IssuedAt.UTC().Truncate(time.Second)
	validUntil := options.ValidUntil.UTC().Truncate(time.Second)
	if issuedAt.IsZero() || !validUntil.After(issuedAt) || validUntil.Sub(issuedAt) > maxActivationFreshness {
		return nil, reject(ReasonOptInMissing)
	}
	activationID := options.ActivationID
	if activationID == "" {
		activationID, err = randomActivationID()
		if err != nil {
			return nil, reject(ReasonOptInMissing)
		}
	}
	if !idPattern.MatchString(activationID) {
		return nil, reject(ReasonOptInMissing)
	}
	executablePath := options.ExecutablePath
	if executablePath == "" {
		executablePath, err = currentExecutablePath()
		if err != nil {
			return nil, reject(ReasonObjectMismatch)
		}
	} else {
		executablePath, err = canonicalRegularPath(executablePath)
		if err != nil {
			return nil, reject(ReasonObjectMismatch)
		}
	}
	object, err := observeCurrentPath(executablePath, nil)
	if err != nil {
		return nil, err
	}
	repositoryIdentity, err := RepositoryIdentity(root)
	if err != nil {
		return nil, reject(ReasonObjectMismatch)
	}
	activation := LocalDogfoodActivationV2{
		SchemaVersion:           ActivationSchema,
		ActivationID:            activationID,
		IssuedAt:                issuedAt.Format(time.RFC3339),
		ValidUntil:              validUntil.Format(time.RFC3339),
		RepositoryIdentity:      repositoryIdentity,
		CanonicalRepositoryRoot: root,
		CanonicalExecutablePath: executablePath,
		ExpectedSize:            object.Size,
		ExpectedRawSHA256:       object.RawSHA256,
		ExpectedSourceHead:      options.Build.SourceHead,
		ExpectedSelfProfile:     LocalProfile,
		Scope: LocalDogfoodScopeV2{
			Network:                 "local-loopback",
			Publication:             "none",
			AdapterAuthority:        "ordinary-user",
			LifecycleCommandClasses: localDogfoodLifecycleCommands(),
		},
	}
	activation.ActivationDigest, err = digestActivation(activation)
	if err != nil {
		return nil, reject(ReasonOptInMissing)
	}
	raw, err := json.Marshal(activation)
	if err != nil {
		return nil, reject(ReasonOptInMissing)
	}
	return canonical.JSON(raw)
}

func randomActivationID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return "local-" + hex.EncodeToString(value[:]), nil
}

// RepositoryIdentity is the local-profile identity of a canonical repository
// root. It is not a remote repository, forge, or deployment identity.
func RepositoryIdentity(root string) (string, error) {
	canonicalRoot, err := canonicalDirectory(root)
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(struct {
		Profile                 string `json:"profile"`
		CanonicalRepositoryRoot string `json:"canonicalRepositoryRoot"`
	}{LocalProfile, canonicalRoot})
	if err != nil {
		return "", err
	}
	return canonical.DigestJSON(raw)
}

// DecodeActivation admits exact JCS bytes and applies all closed semantic
// checks. Any malformed, duplicate, unknown, trailing, stale or non-canonical
// input is reduced to one stable opt-in rejection.
func DecodeActivation(raw []byte, now time.Time) (LocalDogfoodActivationV2, error) {
	if len(raw) == 0 || len(raw) > maxActivationBytes {
		return LocalDogfoodActivationV2{}, reject(ReasonOptInMissing)
	}
	canonicalRaw, err := canonical.JSON(raw)
	if err != nil || !bytes.Equal(raw, canonicalRaw) {
		return LocalDogfoodActivationV2{}, reject(ReasonOptInMissing)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var activation LocalDogfoodActivationV2
	if err := decoder.Decode(&activation); err != nil {
		return LocalDogfoodActivationV2{}, reject(ReasonOptInMissing)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return LocalDogfoodActivationV2{}, reject(ReasonOptInMissing)
	}
	if activation.SchemaVersion != ActivationSchema || !idPattern.MatchString(activation.ActivationID) ||
		activation.ExpectedSelfProfile != LocalProfile || activation.Scope.Network != "local-loopback" ||
		activation.Scope.Publication != "none" || activation.Scope.AdapterAuthority != "ordinary-user" ||
		!slices.Equal(activation.Scope.LifecycleCommandClasses, localDogfoodLifecycleCommands()) ||
		!sourceHeadPattern.MatchString(activation.ExpectedSourceHead) || activation.ExpectedSize <= 0 ||
		activation.ExpectedSize > maxExecutableBytes || !validDigest(activation.ExpectedRawSHA256) ||
		!validDigest(activation.RepositoryIdentity) {
		return LocalDogfoodActivationV2{}, reject(ReasonOptInMissing)
	}
	issuedAt, issuedOK := canonicalTimestamp(activation.IssuedAt)
	validUntil, validOK := canonicalTimestamp(activation.ValidUntil)
	now = now.UTC()
	if !issuedOK || !validOK || validUntil.Sub(issuedAt) > maxActivationFreshness || !validUntil.After(issuedAt) ||
		now.Before(issuedAt) || !now.Before(validUntil) {
		return LocalDogfoodActivationV2{}, reject(ReasonOptInMissing)
	}
	root, err := canonicalDirectory(activation.CanonicalRepositoryRoot)
	if err != nil || root != activation.CanonicalRepositoryRoot {
		return LocalDogfoodActivationV2{}, reject(ReasonObjectMismatch)
	}
	executable, err := canonicalRegularPath(activation.CanonicalExecutablePath)
	if err != nil || executable != activation.CanonicalExecutablePath {
		return LocalDogfoodActivationV2{}, reject(ReasonObjectMismatch)
	}
	repositoryIdentity, err := RepositoryIdentity(root)
	if err != nil || repositoryIdentity != activation.RepositoryIdentity {
		return LocalDogfoodActivationV2{}, reject(ReasonObjectMismatch)
	}
	digest, err := digestActivation(activation)
	if err != nil || digest != activation.ActivationDigest {
		return LocalDogfoodActivationV2{}, reject(ReasonOptInMissing)
	}
	return activation, nil
}

func localDogfoodLifecycleCommands() []string {
	return []string{CommandDoctor, CommandInit, CommandTaskScaffold, CommandTaskPlan, CommandTaskStatus, CommandTaskApprovePlan, CommandTaskRun, CommandTaskVerify, CommandTaskReview, CommandControlPlaneServe, CommandControlPlaneStatus, CommandControlPlaneInspect, CommandControlPlaneStart, CommandControlPlaneCollect, CommandControlPlaneVerify, CommandControlPlaneReview, CommandControlPlaneDecision}
}

func digestActivation(activation LocalDogfoodActivationV2) (string, error) {
	input := activationDigestInput{
		SchemaVersion: activation.SchemaVersion, ActivationID: activation.ActivationID,
		IssuedAt: activation.IssuedAt, ValidUntil: activation.ValidUntil,
		RepositoryIdentity: activation.RepositoryIdentity, CanonicalRepositoryRoot: activation.CanonicalRepositoryRoot,
		CanonicalExecutablePath: activation.CanonicalExecutablePath, ExpectedSize: activation.ExpectedSize,
		ExpectedRawSHA256: activation.ExpectedRawSHA256, ExpectedSourceHead: activation.ExpectedSourceHead,
		ExpectedSelfProfile: activation.ExpectedSelfProfile, Scope: activation.Scope,
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	return canonical.DigestJSON(raw)
}

func canonicalTimestamp(value string) (time.Time, bool) {
	parsed, err := time.Parse(time.RFC3339, value)
	return parsed, err == nil && value == parsed.UTC().Format(time.RFC3339)
}

func validDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON value")
	}
	return nil
}

func canonicalDirectory(path string) (string, error) {
	if path == "" {
		path = "."
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil || resolved != abs {
		return "", errors.New("directory is not canonical")
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return "", errors.New("not a directory")
	}
	return abs, nil
}

func canonicalRegularPath(path string) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", errors.New("path is not canonical")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("not a regular file")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return "", errors.New("path contains symlink")
	}
	return path, nil
}

// currentExecutablePath canonicalizes the kernel-reported executable path.
// Darwin can report /var while the stable object is named through /private/var;
// explicit operator-supplied paths remain subject to canonicalRegularPath and
// therefore cannot use a symlink alias.
func currentExecutablePath() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", err
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", err
	}
	path, err = filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	return canonicalRegularPath(path)
}

func workingDirectoryWithinRoot(workingDirectory, root string) bool {
	workingDirectory, err := canonicalDirectory(workingDirectory)
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(root, workingDirectory)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func readActivation(path string) ([]byte, error) {
	return readActivationWithHook(path, nil)
}

func readActivationWithHook(path string, afterOpen func()) ([]byte, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, reject(ReasonOptInMissing)
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Size() <= 0 || before.Size() > maxActivationBytes {
		return nil, reject(ReasonOptInMissing)
	}
	file, err := openActivationFile(path)
	if err != nil {
		return nil, reject(ReasonOptInMissing)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) || !opened.Mode().IsRegular() || opened.Mode().Perm()&0o022 != 0 {
		return nil, reject(ReasonOptInMissing)
	}
	if afterOpen != nil {
		afterOpen()
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxActivationBytes+1))
	if err != nil || len(raw) == 0 || len(raw) > maxActivationBytes {
		return nil, reject(ReasonOptInMissing)
	}
	after, err := file.Stat()
	current, currentErr := os.Lstat(path)
	if err != nil || currentErr != nil || !os.SameFile(opened, after) || !os.SameFile(after, current) || after.Size() != int64(len(raw)) {
		return nil, reject(ReasonOptInMissing)
	}
	return raw, nil
}
