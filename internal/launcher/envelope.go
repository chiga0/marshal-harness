// Package launcher implements the one-use launch envelope used to start a
// native Worker TUI without exposing its environment in a terminal command.
package launcher

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/domain"
)

const (
	envelopeVersion  = 1
	maxEnvelopeBytes = 1 << 20
)

var (
	environmentKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	envelopeNamePattern   = regexp.MustCompile(`^\.launch-[A-Za-z0-9]+-([0-9a-f]{64})\.json$`)
	forbiddenEnvironment  = map[string]bool{
		"GH_TOKEN": true, "GITHUB_TOKEN": true, "GH_CONFIG_DIR": true, "MARSHAL_GH_CONFIG_DIR": true, "MARSHAL_GH_PATH": true, "GIT_ASKPASS": true, "SSH_AUTH_SOCK": true,
		"AWS_ACCESS_KEY_ID": true, "AWS_SECRET_ACCESS_KEY": true, "AWS_SESSION_TOKEN": true, "AWS_SECURITY_TOKEN": true,
		"AWS_PROFILE": true, "AWS_SHARED_CREDENTIALS_FILE": true,
		"GOOGLE_APPLICATION_CREDENTIALS": true, "CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE": true,
		"AZURE_CLIENT_SECRET": true, "AZURE_CONFIG_DIR": true, "ARM_CLIENT_SECRET": true,
		"KUBECONFIG": true, "NPM_TOKEN": true, "NPM_CONFIG_USERCONFIG": true, "DOCKER_AUTH_CONFIG": true,
	}
)

type Executable struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
}

type Envelope struct {
	Version          int        `json:"version"`
	RunID            string     `json:"runId"`
	AttemptID        string     `json:"attemptId"`
	Executable       Executable `json:"executable"`
	Arguments        []string   `json:"arguments"`
	WorkingDirectory string     `json:"workingDirectory"`
	Environment      []string   `json:"environment"`
	CreatedAt        time.Time  `json:"createdAt"`
	ExpiresAt        time.Time  `json:"expiresAt"`
}

type SealRequest struct {
	RunID, AttemptID         string
	Executable               string
	ExpectedExecutableDigest string
	Arguments                []string
	WorkingDirectory         string
	Environment              []string
	Now, ExpiresAt           time.Time
}

type Reference struct {
	Path   string
	Digest string
}

type ConsumeRequest struct {
	RunID, AttemptID string
	Path             string
	ExpectedDigest   string
	Now              time.Time
}

func Seal(stateRoot string, request SealRequest) (Reference, error) {
	root, err := canonicalDirectory(stateRoot)
	if err != nil {
		return Reference{}, fmt.Errorf("state root: %w", err)
	}
	if err := validateIdentity(request.RunID, request.AttemptID); err != nil {
		return Reference{}, err
	}
	if request.Now.IsZero() || !request.ExpiresAt.After(request.Now) {
		return Reference{}, errors.New("launch envelope requires a future expiry")
	}
	attemptDirectory := filepath.Join(root, "runs", request.RunID, "attempts", request.AttemptID)
	if err := exactDirectory(attemptDirectory, false); err != nil {
		return Reference{}, fmt.Errorf("attempt directory: %w", err)
	}
	runtimeDirectory := filepath.Join(attemptDirectory, "runtime")
	if err := prepareRuntimeDirectory(runtimeDirectory); err != nil {
		return Reference{}, err
	}
	executable, err := executableIdentity(request.Executable)
	if err != nil {
		return Reference{}, err
	}
	if request.ExpectedExecutableDigest == "" || executable.Digest != request.ExpectedExecutableDigest {
		return Reference{}, errors.New("executable digest does not match frozen identity")
	}
	workingDirectory, err := canonicalDirectory(request.WorkingDirectory)
	if err != nil {
		return Reference{}, fmt.Errorf("working directory: %w", err)
	}
	if err := validateArguments(request.Arguments); err != nil {
		return Reference{}, err
	}
	if err := validateEnvironment(request.Environment); err != nil {
		return Reference{}, err
	}
	envelope := Envelope{
		Version: envelopeVersion, RunID: request.RunID, AttemptID: request.AttemptID,
		Executable: executable, Arguments: append([]string(nil), request.Arguments...),
		WorkingDirectory: workingDirectory, Environment: append([]string(nil), request.Environment...),
		CreatedAt: request.Now.UTC(), ExpiresAt: request.ExpiresAt.UTC(),
	}
	data, err := json.Marshal(envelope)
	if err != nil {
		return Reference{}, err
	}
	data = append(data, '\n')
	if len(data) > maxEnvelopeBytes {
		return Reference{}, errors.New("launch envelope exceeds size limit")
	}
	temporary, err := os.CreateTemp(runtimeDirectory, ".launch-*.tmp")
	if err != nil {
		return Reference{}, err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return Reference{}, err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return Reference{}, err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return Reference{}, err
	}
	if err := temporary.Close(); err != nil {
		return Reference{}, err
	}
	digest := digestBytes(data)
	target := strings.TrimSuffix(temporaryPath, ".tmp") + "-" + strings.TrimPrefix(digest, "sha256:") + ".json"
	// Link publishes the fully synced inode atomically and refuses to replace
	// an attacker-created target. A same-directory rename would overwrite an
	// existing path on Unix.
	if err := os.Link(temporaryPath, target); err != nil {
		return Reference{}, err
	}
	if err := os.Remove(temporaryPath); err != nil {
		_ = os.Remove(target)
		return Reference{}, err
	}
	if err := syncDirectory(runtimeDirectory); err != nil {
		_ = os.Remove(target)
		return Reference{}, err
	}
	return Reference{Path: target, Digest: digest}, nil
}

// ConsumePath derives every non-secret reference field from the exact sealed
// path. This keeps the visible terminal command limited to the trusted
// launcher and one envelope path.
func ConsumePath(path string, now time.Time) (Envelope, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return Envelope{}, errors.New("launch envelope path must be clean and absolute")
	}
	match := envelopeNamePattern.FindStringSubmatch(filepath.Base(path))
	if match == nil {
		return Envelope{}, errors.New("launch envelope filename does not bind a digest")
	}
	runtimeDirectory := filepath.Dir(path)
	if filepath.Base(runtimeDirectory) != "runtime" {
		return Envelope{}, errors.New("launch envelope is not in a runtime directory")
	}
	attemptDirectory := filepath.Dir(runtimeDirectory)
	attemptsDirectory := filepath.Dir(attemptDirectory)
	runDirectory := filepath.Dir(attemptsDirectory)
	runsDirectory := filepath.Dir(runDirectory)
	if filepath.Base(attemptsDirectory) != "attempts" || filepath.Base(runsDirectory) != "runs" {
		return Envelope{}, errors.New("launch envelope path has an invalid run hierarchy")
	}
	return Consume(filepath.Dir(runsDirectory), ConsumeRequest{
		RunID: filepath.Base(runDirectory), AttemptID: filepath.Base(attemptDirectory),
		Path: path, ExpectedDigest: "sha256:" + match[1], Now: now,
	})
}

// Consume verifies and removes the exact envelope before returning its launch
// data. Callers must fail closed if the subsequent exec fails; an envelope is
// deliberately not reusable.
func Consume(stateRoot string, request ConsumeRequest) (Envelope, error) {
	root, err := canonicalDirectory(stateRoot)
	if err != nil {
		return Envelope{}, fmt.Errorf("state root: %w", err)
	}
	if err := validateIdentity(request.RunID, request.AttemptID); err != nil {
		return Envelope{}, err
	}
	if request.Now.IsZero() {
		return Envelope{}, errors.New("consume time is required")
	}
	runtimeDirectory := filepath.Join(root, "runs", request.RunID, "attempts", request.AttemptID, "runtime")
	if err := exactDirectory(runtimeDirectory, true); err != nil {
		return Envelope{}, fmt.Errorf("runtime directory: %w", err)
	}
	nameMatch := envelopeNamePattern.FindStringSubmatch(filepath.Base(request.Path))
	if !filepath.IsAbs(request.Path) || filepath.Clean(request.Path) != request.Path || filepath.Dir(request.Path) != runtimeDirectory || nameMatch == nil {
		return Envelope{}, errors.New("launch envelope path is outside the exact attempt runtime")
	}
	if request.ExpectedDigest != "sha256:"+nameMatch[1] {
		return Envelope{}, errors.New("launch envelope reference digest mismatch")
	}
	before, err := os.Lstat(request.Path)
	if err != nil {
		return Envelope{}, err
	}
	if !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 || before.Mode().Perm() != 0o600 || before.Size() > maxEnvelopeBytes {
		return Envelope{}, errors.New("launch envelope must be an owner-only bounded regular file")
	}
	file, err := os.Open(request.Path)
	if err != nil {
		return Envelope{}, err
	}
	opened, statErr := file.Stat()
	if statErr != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		_ = file.Close()
		return Envelope{}, errors.New("launch envelope identity changed while opening")
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxEnvelopeBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || len(data) > maxEnvelopeBytes {
		return Envelope{}, errors.New("read bounded launch envelope")
	}
	if digestBytes(data) != request.ExpectedDigest {
		return Envelope{}, errors.New("launch envelope digest mismatch")
	}
	var envelope Envelope
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return Envelope{}, fmt.Errorf("decode launch envelope: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return Envelope{}, err
	}
	if err := validateConsumedEnvelope(envelope, request, root); err != nil {
		return Envelope{}, err
	}
	current, err := os.Lstat(request.Path)
	if err != nil || current.Mode()&os.ModeSymlink != 0 || !current.Mode().IsRegular() || current.Mode().Perm() != 0o600 || !os.SameFile(opened, current) {
		return Envelope{}, errors.New("launch envelope identity changed before consume")
	}
	if err := os.Remove(request.Path); err != nil {
		return Envelope{}, fmt.Errorf("remove consumed launch envelope: %w", err)
	}
	if err := syncDirectory(runtimeDirectory); err != nil {
		return Envelope{}, err
	}
	return envelope, nil
}

func validateConsumedEnvelope(envelope Envelope, request ConsumeRequest, root string) error {
	if envelope.Version != envelopeVersion || envelope.RunID != request.RunID || envelope.AttemptID != request.AttemptID {
		return errors.New("launch envelope identity mismatch")
	}
	if envelope.CreatedAt.IsZero() || !envelope.ExpiresAt.After(envelope.CreatedAt) || request.Now.Before(envelope.CreatedAt) || !request.Now.Before(envelope.ExpiresAt) {
		return errors.New("launch envelope is expired or has invalid time bounds")
	}
	if err := validateArguments(envelope.Arguments); err != nil {
		return err
	}
	if err := validateEnvironment(envelope.Environment); err != nil {
		return err
	}
	executable, err := executableIdentity(envelope.Executable.Path)
	if err != nil || executable != envelope.Executable {
		return errors.New("launch executable identity mismatch")
	}
	workingDirectory, err := canonicalDirectory(envelope.WorkingDirectory)
	if err != nil || workingDirectory != envelope.WorkingDirectory {
		return errors.New("launch working directory identity mismatch")
	}
	// root is intentionally used to ensure it remains an existing canonical
	// directory for the complete consume operation.
	if _, err := os.Stat(root); err != nil {
		return errors.New("state root became unavailable")
	}
	return nil
}

func validateIdentity(runID, attemptID string) error {
	if err := domain.ValidateID(runID); err != nil {
		return err
	}
	return domain.ValidateID(attemptID)
}

func prepareRuntimeDirectory(path string) error {
	if info, err := os.Lstat(path); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
			return errors.New("runtime directory must be an owner-only real directory")
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(path, 0o700); err != nil {
			return err
		}
		if err := os.Chmod(path, 0o700); err != nil {
			return err
		}
	} else {
		return err
	}
	return exactDirectory(path, true)
}

func exactDirectory(path string, ownerOnly bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || ownerOnly && info.Mode().Perm() != 0o700 {
		return errors.New("path is not the required real directory")
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil || real != path {
		return errors.New("directory path contains a symlink")
	}
	return nil
}

func canonicalDirectory(path string) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", errors.New("directory path must be clean and absolute")
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(real)
	if err != nil || !info.IsDir() {
		return "", errors.New("path is not an existing directory")
	}
	return real, nil
}

func executableIdentity(path string) (Executable, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return Executable{}, errors.New("executable path must be clean and absolute")
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return Executable{}, err
	}
	before, err := os.Lstat(real)
	if err != nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 || before.Mode().Perm()&0o111 == 0 {
		return Executable{}, errors.New("executable must be an executable regular file")
	}
	file, err := os.Open(real)
	if err != nil {
		return Executable{}, err
	}
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		_ = file.Close()
		return Executable{}, errors.New("executable identity changed while opening")
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, file)
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil {
		return Executable{}, errors.New("digest executable")
	}
	current, err := os.Lstat(real)
	if err != nil || current.Mode()&os.ModeSymlink != 0 || !current.Mode().IsRegular() || current.Mode().Perm()&0o111 == 0 || !os.SameFile(opened, current) {
		return Executable{}, errors.New("executable identity changed while digesting")
	}
	return Executable{Path: real, Digest: "sha256:" + hex.EncodeToString(hash.Sum(nil))}, nil
}

func validateArguments(arguments []string) error {
	for _, argument := range arguments {
		if strings.ContainsRune(argument, 0) {
			return errors.New("launch argument contains NUL")
		}
	}
	return nil
}

func validateEnvironment(environment []string) error {
	seen := map[string]bool{}
	for _, entry := range environment {
		if strings.ContainsRune(entry, 0) {
			return errors.New("launch environment contains NUL")
		}
		key, _, found := strings.Cut(entry, "=")
		if !found || !environmentKeyPattern.MatchString(key) || seen[key] {
			return errors.New("launch environment contains an invalid or duplicate key")
		}
		if forbiddenEnvironment[key] {
			return fmt.Errorf("launch environment contains forbidden credential key %s", key)
		}
		seen[key] = true
	}
	return nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("launch envelope contains trailing JSON")
	}
	return nil
}

func digestBytes(data []byte) string {
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	err = directory.Sync()
	closeErr := directory.Close()
	if err != nil {
		return err
	}
	return closeErr
}
