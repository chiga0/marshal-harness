package qoder

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"golang.org/x/sys/unix"
)

const maxConformanceValidity = 24 * time.Hour

// ConformanceEvidence is an independently signed, content-addressed authority
// record. It is data an external conformance runner may produce, but Adapter
// callers cannot authorize execution by passing this value directly:
// BindConformance accepts only a digest and resolves it through the trusted
// AuthorityEvidenceStore configured when the Adapter is constructed.
type ConformanceEvidence struct {
	EvidenceDigest         string `json:"evidenceDigest"`
	RunnerID               string `json:"runnerId"`
	RunnerVersion          string `json:"runnerVersion"`
	ObservedAt             string `json:"observedAt"`
	ValidUntil             string `json:"validUntil"`
	AdapterVersion         string `json:"adapterVersion"`
	Executable             string `json:"executable"`
	ExecutableDigest       string `json:"executableDigest"`
	BinaryVersion          string `json:"binaryVersion"`
	HostOS                 string `json:"hostOs"`
	HostArch               string `json:"hostArch"`
	HostFingerprint        string `json:"hostFingerprint"`
	AuthorityGeneration    uint64 `json:"authorityGeneration"`
	ProbeSuiteDigest       string `json:"probeSuiteDigest"`
	ProbeArtifactDigest    string `json:"probeArtifactDigest"`
	ChallengeDigest        string `json:"challengeDigest"`
	CapabilitiesDigest     string `json:"capabilitiesDigest"`
	ProbeProfileDigest     string `json:"probeProfileDigest"`
	ArgvDigest             string `json:"argvDigest"`
	EnvironmentDigest      string `json:"environmentDigest"`
	ToolPolicyDigest       string `json:"toolPolicyDigest"`
	TranscriptDigest       string `json:"transcriptDigest"`
	CredentialVerified     bool   `json:"credentialVerified"`
	LiveProtocolVerified   bool   `json:"liveProtocolVerified"`
	WorkspaceWriteVerified bool   `json:"workspaceWriteVerified"`
	EventContract          string `json:"eventContract"`
	QoderCLIVersion        string `json:"qodercliVersion"`
	ProtocolVersion        string `json:"protocolVersion"`
	PermissionMode         string `json:"permissionMode"`
	TrustRootKeyID         string `json:"trustRootKeyId"`
	Signature              string `json:"signature"`
}

func (evidence ConformanceEvidence) digest() (string, error) {
	detached := evidence
	detached.EvidenceDigest, detached.Signature = "", ""
	data, err := json.Marshal(detached)
	if err != nil {
		return "", err
	}
	canonicalData, err := canonical.JSON(data)
	if err != nil {
		return "", err
	}
	return digestBytes(canonicalData), nil
}

func (evidence ConformanceEvidence) signingBytes() ([]byte, error) {
	unsigned := evidence
	unsigned.Signature = ""
	data, err := json.Marshal(unsigned)
	if err != nil {
		return nil, err
	}
	return canonical.JSON(data)
}

func (evidence ConformanceEvidence) validate(now time.Time, trustRoots map[string]ed25519.PublicKey) error {
	computed, err := evidence.digest()
	if err != nil || evidence.EvidenceDigest != computed {
		return errors.New("qoder conformance evidence digest mismatch")
	}
	if evidence.RunnerID == "" || evidence.RunnerID == adapterID || evidence.RunnerVersion == "" || evidence.TrustRootKeyID == "" {
		return errors.New("qoder conformance evidence lacks independent runner provenance")
	}
	observedAt, err := time.Parse(time.RFC3339Nano, evidence.ObservedAt)
	if err != nil || observedAt.After(now) {
		return errors.New("qoder conformance evidence observedAt is invalid")
	}
	validUntil, err := time.Parse(time.RFC3339Nano, evidence.ValidUntil)
	if err != nil || observedAt.After(now) || !now.Before(validUntil) || !observedAt.Before(validUntil) || validUntil.Sub(observedAt) > maxConformanceValidity || now.Sub(observedAt) > maxConformanceValidity {
		return errors.New("qoder conformance evidence is expired or has an invalid validity window")
	}
	if evidence.AdapterVersion != adapterVersion || evidence.HostOS != runtime.GOOS || evidence.HostArch != runtime.GOARCH || evidence.ProbeSuiteDigest != expectedProbeSuiteDigest() || evidence.CapabilitiesDigest != expectedCapabilitiesDigest() || evidence.ProbeProfileDigest != expectedProbeProfileDigest() || evidence.ArgvDigest != expectedProbeArgvDigest() || evidence.EnvironmentDigest != expectedProbeEnvironmentDigest() || evidence.ToolPolicyDigest != expectedProbeToolPolicyDigest() || !validSHA256Digest(evidence.HostFingerprint) || !validSHA256Digest(evidence.ProbeArtifactDigest) || !validSHA256Digest(evidence.ChallengeDigest) || !validSHA256Digest(evidence.ExecutableDigest) || !validSHA256Digest(evidence.TranscriptDigest) || evidence.AuthorityGeneration == 0 || !evidence.CredentialVerified || !evidence.LiveProtocolVerified || !evidence.WorkspaceWriteVerified {
		return errors.New("qoder conformance evidence does not bind the complete verified adapter contract")
	}
	if evidence.EventContract != conformanceEventContract || evidence.QoderCLIVersion != evidence.BinaryVersion || evidence.ProtocolVersion != qoderProtocolVersion || evidence.PermissionMode != qoderPermissionMode {
		return errors.New("qoder conformance evidence carries a different live protocol contract")
	}
	publicKey, ok := trustRoots[evidence.TrustRootKeyID]
	if !ok || len(publicKey) != ed25519.PublicKeySize {
		return errors.New("qoder conformance evidence trust root is not configured")
	}
	signature, err := base64.StdEncoding.DecodeString(evidence.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return errors.New("qoder conformance evidence signature is malformed")
	}
	message, err := evidence.signingBytes()
	if err != nil || !ed25519.Verify(publicKey, message, signature) {
		return errors.New("qoder conformance evidence signature is not trusted")
	}
	return nil
}

// LiveConformanceObservation is the non-sensitive adjudicated result passed
// from an independent probe-only verifier to its signer. Transcript contents,
// credentials and private configuration are intentionally absent; only their
// content digest and the boolean verdicts cross this boundary.
type LiveConformanceObservation struct {
	RunnerID               string
	RunnerVersion          string
	ObservedAt             time.Time
	ValidUntil             time.Time
	Executable             string
	ExecutableDigest       string
	BinaryVersion          string
	HostOS                 string
	HostArch               string
	HostFingerprint        string
	AuthorityGeneration    uint64
	ProbeSuiteDigest       string
	ProbeArtifactDigest    string
	ChallengeDigest        string
	CapabilitiesDigest     string
	ProbeProfileDigest     string
	ArgvDigest             string
	EnvironmentDigest      string
	ToolPolicyDigest       string
	TranscriptDigest       string
	CredentialVerified     bool
	LiveProtocolVerified   bool
	WorkspaceWriteVerified bool
	EventContract          string
	ProtocolVersion        string
	PermissionMode         string
	TrustRootKeyID         string
}

// LiveConformanceContract is the public, non-secret frozen contract an
// independent verifier must actually exercise and echo in its observation.
type LiveConformanceContract struct {
	ProbeSuiteDigest   string
	CapabilitiesDigest string
	ProbeProfileDigest string
	ArgvDigest         string
	EnvironmentDigest  string
	ToolPolicyDigest   string
	EventContract      string
	ProtocolVersion    string
	PermissionMode     string
}

func FrozenLiveConformanceContract() LiveConformanceContract {
	return LiveConformanceContract{
		ProbeSuiteDigest: expectedProbeSuiteDigest(), CapabilitiesDigest: expectedCapabilitiesDigest(), ProbeProfileDigest: expectedProbeProfileDigest(),
		ArgvDigest: expectedProbeArgvDigest(), EnvironmentDigest: expectedProbeEnvironmentDigest(), ToolPolicyDigest: expectedProbeToolPolicyDigest(),
		EventContract: conformanceEventContract, ProtocolVersion: qoderProtocolVersion, PermissionMode: qoderPermissionMode,
	}
}

// SealConformanceEvidence lets an independent verifier produce the exact
// authority record consumed by NewFromAuthorityConfig. It does not run Qoder,
// read credentials, write the evidence store, or mutate Marshal state. The
// verifier remains responsible for running the probe in the frozen isolated
// profile and for protecting the signing key.
func SealConformanceEvidence(observation LiveConformanceObservation, privateKey ed25519.PrivateKey) ([]byte, string, error) {
	if len(privateKey) != ed25519.PrivateKeySize || observation.RunnerID == "" || observation.RunnerID == adapterID || observation.RunnerVersion == "" || observation.TrustRootKeyID == "" {
		return nil, "", errors.New("qoder live conformance observation lacks independent signer provenance")
	}
	if !filepath.IsAbs(observation.Executable) || filepath.Clean(observation.Executable) != observation.Executable || !validSHA256Digest(observation.ExecutableDigest) || !validSHA256Digest(observation.HostFingerprint) || !validSHA256Digest(observation.ProbeArtifactDigest) || !validSHA256Digest(observation.ChallengeDigest) || !validSHA256Digest(observation.TranscriptDigest) || !isSupportedBinaryVersion(observation.BinaryVersion) {
		return nil, "", errors.New("qoder live conformance observation identity is invalid")
	}
	if observation.ObservedAt.IsZero() || observation.ValidUntil.IsZero() || !observation.ObservedAt.Before(observation.ValidUntil) || observation.ValidUntil.Sub(observation.ObservedAt) > maxConformanceValidity || observation.AuthorityGeneration == 0 || !observation.CredentialVerified || !observation.LiveProtocolVerified || !observation.WorkspaceWriteVerified {
		return nil, "", errors.New("qoder live conformance observation did not pass the frozen probe")
	}
	if observation.ProbeSuiteDigest != expectedProbeSuiteDigest() || observation.CapabilitiesDigest != expectedCapabilitiesDigest() || observation.ProbeProfileDigest != expectedProbeProfileDigest() || observation.ArgvDigest != expectedProbeArgvDigest() || observation.EnvironmentDigest != expectedProbeEnvironmentDigest() || observation.ToolPolicyDigest != expectedProbeToolPolicyDigest() || observation.EventContract != conformanceEventContract || observation.ProtocolVersion != qoderProtocolVersion || observation.PermissionMode != qoderPermissionMode {
		return nil, "", errors.New("qoder live conformance observation does not bind the frozen probe contract")
	}
	evidence := ConformanceEvidence{
		RunnerID: observation.RunnerID, RunnerVersion: observation.RunnerVersion,
		ObservedAt: observation.ObservedAt.UTC().Format(time.RFC3339Nano), ValidUntil: observation.ValidUntil.UTC().Format(time.RFC3339Nano),
		AdapterVersion: adapterVersion, Executable: observation.Executable, ExecutableDigest: observation.ExecutableDigest, BinaryVersion: observation.BinaryVersion, HostOS: observation.HostOS, HostArch: observation.HostArch, HostFingerprint: observation.HostFingerprint,
		AuthorityGeneration: observation.AuthorityGeneration, ProbeSuiteDigest: observation.ProbeSuiteDigest, ProbeArtifactDigest: observation.ProbeArtifactDigest, ChallengeDigest: observation.ChallengeDigest,
		CapabilitiesDigest: observation.CapabilitiesDigest, ProbeProfileDigest: observation.ProbeProfileDigest, ArgvDigest: observation.ArgvDigest, EnvironmentDigest: observation.EnvironmentDigest, ToolPolicyDigest: observation.ToolPolicyDigest, TranscriptDigest: observation.TranscriptDigest,
		CredentialVerified: true, LiveProtocolVerified: true, WorkspaceWriteVerified: true, EventContract: observation.EventContract, QoderCLIVersion: observation.BinaryVersion,
		ProtocolVersion: observation.ProtocolVersion, PermissionMode: observation.PermissionMode, TrustRootKeyID: observation.TrustRootKeyID,
	}
	var err error
	evidence.EvidenceDigest, err = evidence.digest()
	if err != nil {
		return nil, "", err
	}
	message, err := evidence.signingBytes()
	if err != nil {
		return nil, "", err
	}
	evidence.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, message))
	data, err := json.Marshal(evidence)
	if err != nil {
		return nil, "", err
	}
	return data, evidence.EvidenceDigest, nil
}

// AuthorityEvidenceStore resolves immutable signed evidence from a private,
// content-addressed authority directory. The trust roots are copied at
// construction; changing caller-owned slices cannot change verification.
type AuthorityEvidenceStore struct {
	root       string
	directory  *os.File
	trustRoots map[string]ed25519.PublicKey
}

func NewAuthorityEvidenceStore(root string, trustRoots map[string]ed25519.PublicKey) (*AuthorityEvidenceStore, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || len(trustRoots) == 0 {
		return nil, errors.New("qoder conformance authority requires an absolute root and trust roots")
	}
	directory, stat, err := openNoSymlinkPath(root, true)
	if err != nil {
		return nil, errors.New("qoder conformance authority root must be a real directory")
	}
	info, err := directory.Stat()
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 || (int(stat.Uid) != os.Geteuid() && stat.Uid != 0) {
		directory.Close()
		return nil, errors.New("qoder conformance authority root must be a private directory")
	}
	keys := make(map[string]ed25519.PublicKey, len(trustRoots))
	for id, key := range trustRoots {
		if strings.TrimSpace(id) == "" || len(key) != ed25519.PublicKeySize {
			directory.Close()
			return nil, errors.New("qoder conformance authority trust root is invalid")
		}
		keys[id] = append(ed25519.PublicKey(nil), key...)
	}
	return &AuthorityEvidenceStore{root: root, directory: directory, trustRoots: keys}, nil
}

func (store *AuthorityEvidenceStore) Close() error {
	if store == nil || store.directory == nil {
		return nil
	}
	return store.directory.Close()
}

func (store *AuthorityEvidenceStore) resolve(ctx context.Context, digest string, now time.Time) (ConformanceEvidence, error) {
	if store == nil || store.directory == nil || ctx == nil || ctx.Err() != nil || !validSHA256Digest(digest) {
		return ConformanceEvidence{}, ErrConformancePending
	}
	name := strings.TrimPrefix(digest, "sha256:") + ".json"
	fd, err := unix.Openat(int(store.directory.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return ConformanceEvidence{}, fmt.Errorf("resolve qoder authority evidence: %w", err)
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0o077 != 0 || (int(stat.Uid) != os.Geteuid() && stat.Uid != 0) {
		return ConformanceEvidence{}, errors.New("qoder authority evidence must be a private regular file")
	}
	data, err := readBoundedFile(file, maxResultBytes)
	if err != nil {
		return ConformanceEvidence{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var evidence ConformanceEvidence
	if err := decoder.Decode(&evidence); err != nil {
		return ConformanceEvidence{}, errors.New("qoder authority evidence document is invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ConformanceEvidence{}, errors.New("qoder authority evidence document is invalid")
	}
	if evidence.EvidenceDigest != digest {
		return ConformanceEvidence{}, errors.New("qoder authority evidence path does not match its digest")
	}
	if err := evidence.validate(now, store.trustRoots); err != nil {
		return ConformanceEvidence{}, err
	}
	return evidence, nil
}

func readBoundedFile(file *os.File, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("qoder authority evidence exceeds byte limit")
	}
	return data, nil
}
