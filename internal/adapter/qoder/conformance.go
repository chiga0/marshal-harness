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
	"github.com/chiga0/marshal-harness/internal/port"
	"golang.org/x/sys/unix"
)

const maxConformanceValidity = 24 * time.Hour

// ConformanceEvidence is an independently signed, content-addressed authority
// record. It is data an external conformance runner may produce, but Adapter
// callers cannot authorize execution by passing this value directly. The
// candidate consumer resolves it only from current private authority config;
// production activation remains disabled while ADR 0034 is Proposed.
type ConformanceEvidence struct {
	EvidenceDigest                  string   `json:"evidenceDigest"`
	RunnerID                        string   `json:"runnerId"`
	RunnerVersion                   string   `json:"runnerVersion"`
	ObservedAt                      string   `json:"observedAt"`
	ValidUntil                      string   `json:"validUntil"`
	AdapterVersion                  string   `json:"adapterVersion"`
	Executable                      string   `json:"executable"`
	ExecutableDigest                string   `json:"executableDigest"`
	BinaryVersion                   string   `json:"binaryVersion"`
	HostOS                          string   `json:"hostOs"`
	HostArch                        string   `json:"hostArch"`
	HostFingerprint                 string   `json:"hostFingerprint"`
	AuthorityGeneration             uint64   `json:"authorityGeneration"`
	ProbeSuiteDigest                string   `json:"probeSuiteDigest"`
	ProbeArtifactDigest             string   `json:"probeArtifactDigest"`
	ChallengeDigest                 string   `json:"challengeDigest"`
	CapabilitiesDigest              string   `json:"capabilitiesDigest"`
	ProbeProfileDigest              string   `json:"probeProfileDigest"`
	ArgvDigest                      string   `json:"argvDigest"`
	EnvironmentDigest               string   `json:"environmentDigest"`
	ToolPolicyDigest                string   `json:"toolPolicyDigest"`
	WorkerResultTransportDigest     string   `json:"workerResultTransportDigest"`
	TranscriptDigest                string   `json:"transcriptDigest"`
	ExecutionReceiptDigest          string   `json:"executionReceiptDigest"`
	ExecutionReceiptDigests         []string `json:"executionReceiptDigests"`
	EvidenceClass                   string   `json:"evidenceClass"`
	ReceiptAuthorityKeyID           string   `json:"receiptAuthorityKeyId"`
	ReceiptAuthorityPublicKeyDigest string   `json:"receiptAuthorityPublicKeyDigest"`
	VerifierKeyID                   string   `json:"verifierKeyId"`
	VerifierPublicKeyDigest         string   `json:"verifierPublicKeyDigest"`
	CredentialVerified              bool     `json:"credentialVerified"`
	LiveProtocolVerified            bool     `json:"liveProtocolVerified"`
	WorkspaceWriteVerified          bool     `json:"workspaceWriteVerified"`
	EventContract                   string   `json:"eventContract"`
	QoderCLIVersion                 string   `json:"qodercliVersion"`
	ProtocolVersion                 string   `json:"protocolVersion"`
	PermissionMode                  string   `json:"permissionMode"`
	TrustRootKeyID                  string   `json:"trustRootKeyId"`
	Signature                       string   `json:"signature"`
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
	if evidence.AdapterVersion != adapterVersion || evidence.HostOS != runtime.GOOS || evidence.HostArch != runtime.GOARCH || evidence.ProbeSuiteDigest != expectedProbeSuiteDigest() || evidence.CapabilitiesDigest != expectedCapabilitiesDigest() || evidence.ProbeProfileDigest != expectedProbeProfileDigest() || evidence.ArgvDigest != expectedProbeArgvDigest() || evidence.EnvironmentDigest != expectedProbeEnvironmentDigest() || evidence.ToolPolicyDigest != expectedProbeToolPolicyDigest() || evidence.WorkerResultTransportDigest != expectedWorkerResultTransportDigest() || !validSHA256Digest(evidence.HostFingerprint) || !validSHA256Digest(evidence.ProbeArtifactDigest) || !validSHA256Digest(evidence.ChallengeDigest) || !validSHA256Digest(evidence.ExecutableDigest) || !validSHA256Digest(evidence.TranscriptDigest) || !validSHA256Digest(evidence.ExecutionReceiptDigest) || len(evidence.ExecutionReceiptDigests) != 4 || evidence.EvidenceClass != candidateEvidenceClassLive || evidence.ReceiptAuthorityKeyID == "" || !validSHA256Digest(evidence.ReceiptAuthorityPublicKeyDigest) || evidence.VerifierKeyID == "" || !validSHA256Digest(evidence.VerifierPublicKeyDigest) || evidence.AuthorityGeneration == 0 || !evidence.CredentialVerified || !evidence.LiveProtocolVerified || !evidence.WorkspaceWriteVerified {
		return errors.New("qoder conformance evidence does not bind the complete verified adapter contract")
	}
	for _, receiptDigest := range evidence.ExecutionReceiptDigests {
		if !validSHA256Digest(receiptDigest) {
			return errors.New("qoder conformance evidence receipt digest is invalid")
		}
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
	RunnerID                        string            `json:"runnerId"`
	RunnerVersion                   string            `json:"runnerVersion"`
	ObservedAt                      time.Time         `json:"observedAt"`
	ValidUntil                      time.Time         `json:"validUntil"`
	AdapterVersion                  string            `json:"adapterVersion"`
	Executable                      string            `json:"executable"`
	ExecutableDigest                string            `json:"executableDigest"`
	BinaryVersion                   string            `json:"binaryVersion"`
	QoderCLIVersion                 string            `json:"qodercliVersion"`
	HostOS                          string            `json:"hostOs"`
	HostArch                        string            `json:"hostArch"`
	HostFingerprint                 string            `json:"hostFingerprint"`
	AuthorityGeneration             uint64            `json:"authorityGeneration"`
	ProbeSuiteDigest                string            `json:"probeSuiteDigest"`
	ProbeArtifactDigest             string            `json:"probeArtifactDigest"`
	ChallengeDigest                 string            `json:"challengeDigest"`
	CapabilitiesDigest              string            `json:"capabilitiesDigest"`
	ProbeProfileDigest              string            `json:"probeProfileDigest"`
	ArgvDigest                      string            `json:"argvDigest"`
	EnvironmentDigest               string            `json:"environmentDigest"`
	ToolPolicyDigest                string            `json:"toolPolicyDigest"`
	WorkerResultTransportDigest     string            `json:"workerResultTransportDigest"`
	TranscriptDigest                string            `json:"transcriptDigest"`
	ExecutionReceiptDigest          string            `json:"executionReceiptDigest"`
	ExecutionReceiptDigests         []string          `json:"executionReceiptDigests"`
	ExecutionReceipts               []json.RawMessage `json:"executionReceipts"`
	EvidenceClass                   string            `json:"evidenceClass"`
	ReceiptAuthorityKeyID           string            `json:"receiptAuthorityKeyId"`
	ReceiptAuthorityPublicKeyDigest string            `json:"receiptAuthorityPublicKeyDigest"`
	VerifierKeyID                   string            `json:"verifierKeyId"`
	VerifierPublicKeyDigest         string            `json:"verifierPublicKeyDigest"`
	VerifierSignature               string            `json:"verifierSignature"`
	CredentialVerified              bool              `json:"credentialVerified"`
	LiveProtocolVerified            bool              `json:"liveProtocolVerified"`
	WorkspaceWriteVerified          bool              `json:"workspaceWriteVerified"`
	EventContract                   string            `json:"eventContract"`
	ProtocolVersion                 string            `json:"protocolVersion"`
	PermissionMode                  string            `json:"permissionMode"`
	TrustRootKeyID                  string            `json:"trustRootKeyId"`
}

// EncodeLiveConformanceObservation is the verifier-to-signer boundary. It
// emits only the closed, non-sensitive typed observation; credentials,
// private keys, stderr and transcript contents have no field in this format.
func EncodeLiveConformanceObservation(observation LiveConformanceObservation) ([]byte, string, error) {
	if err := validateLiveConformanceObservation(observation, time.Now().UTC()); err != nil {
		return nil, "", err
	}
	data, err := json.Marshal(observation)
	if err != nil {
		return nil, "", err
	}
	canonicalData, err := canonical.JSON(data)
	if err != nil {
		return nil, "", err
	}
	return canonicalData, digestBytes(canonicalData), nil
}

// LiveConformanceContract is the public, non-secret frozen contract an
// independent verifier must actually exercise and echo in its observation.
type LiveConformanceContract struct {
	AdapterVersion              string
	ProbeSuiteDigest            string
	CapabilitiesDigest          string
	ProbeProfileDigest          string
	ArgvDigest                  string
	EnvironmentDigest           string
	ToolPolicyDigest            string
	WorkerResultTransportDigest string
	EventContract               string
	ProtocolVersion             string
	PermissionMode              string
}

func FrozenLiveConformanceContract() LiveConformanceContract {
	return LiveConformanceContract{
		AdapterVersion:   adapterVersion,
		ProbeSuiteDigest: expectedProbeSuiteDigest(), CapabilitiesDigest: expectedCapabilitiesDigest(), ProbeProfileDigest: expectedProbeProfileDigest(),
		ArgvDigest: expectedProbeArgvDigest(), EnvironmentDigest: expectedProbeEnvironmentDigest(), ToolPolicyDigest: expectedProbeToolPolicyDigest(), WorkerResultTransportDigest: expectedWorkerResultTransportDigest(),
		EventContract: conformanceEventContract, ProtocolVersion: qoderProtocolVersion, PermissionMode: qoderPermissionMode,
	}
}

// SignLiveConformanceObservation is the authority-signer boundary. The signer
// receives a closed verifier document, independently decodes and validates
// every observed field, and only then signs evidence. The verifier never
// receives this function's private key.
func SignLiveConformanceObservation(document []byte, privateKey ed25519.PrivateKey) ([]byte, string, error) {
	return nil, "", port.Permanent(ErrConformancePending)
}

func validateLiveConformanceObservation(observation LiveConformanceObservation, now time.Time) error {
	if observation.RunnerID == "" || observation.RunnerID == adapterID || observation.RunnerVersion == "" || observation.TrustRootKeyID == "" {
		return errors.New("qoder live conformance observation lacks independent verifier provenance")
	}
	if observation.AdapterVersion == "" || observation.BinaryVersion == "" || observation.QoderCLIVersion == "" || observation.HostOS == "" || observation.HostArch == "" || !filepath.IsAbs(observation.Executable) || filepath.Clean(observation.Executable) != observation.Executable || !validSHA256Digest(observation.ExecutableDigest) || !validSHA256Digest(observation.HostFingerprint) || !validSHA256Digest(observation.ProbeArtifactDigest) || !validSHA256Digest(observation.ChallengeDigest) || !validSHA256Digest(observation.WorkerResultTransportDigest) || !validSHA256Digest(observation.TranscriptDigest) || !validSHA256Digest(observation.ExecutionReceiptDigest) || len(observation.ExecutionReceiptDigests) != 4 || len(observation.ExecutionReceipts) != 4 || observation.EvidenceClass != candidateEvidenceClassLive || observation.ReceiptAuthorityKeyID == "" || !validSHA256Digest(observation.ReceiptAuthorityPublicKeyDigest) || observation.VerifierKeyID == "" || !validSHA256Digest(observation.VerifierPublicKeyDigest) || observation.VerifierSignature == "" || !isSupportedBinaryVersion(observation.BinaryVersion) {
		return errors.New("qoder live conformance observation identity is invalid")
	}
	for _, receiptDigest := range observation.ExecutionReceiptDigests {
		if !validSHA256Digest(receiptDigest) {
			return errors.New("qoder live conformance observation receipt digest is invalid")
		}
	}
	if observation.ObservedAt.IsZero() || observation.ValidUntil.IsZero() || observation.ObservedAt.After(now) || !now.Before(observation.ValidUntil) || now.Sub(observation.ObservedAt) > maxConformanceValidity || !observation.ObservedAt.Before(observation.ValidUntil) || observation.ValidUntil.Sub(observation.ObservedAt) > maxConformanceValidity || observation.AuthorityGeneration == 0 || !observation.CredentialVerified || !observation.LiveProtocolVerified || !observation.WorkspaceWriteVerified {
		return errors.New("qoder live conformance observation did not pass the frozen probe")
	}
	if observation.AdapterVersion != adapterVersion || observation.QoderCLIVersion != observation.BinaryVersion || observation.ProbeSuiteDigest != expectedProbeSuiteDigest() || observation.CapabilitiesDigest != expectedCapabilitiesDigest() || observation.ProbeProfileDigest != expectedProbeProfileDigest() || observation.ArgvDigest != expectedProbeArgvDigest() || observation.EnvironmentDigest != expectedProbeEnvironmentDigest() || observation.ToolPolicyDigest != expectedProbeToolPolicyDigest() || observation.WorkerResultTransportDigest != expectedWorkerResultTransportDigest() || observation.EventContract != conformanceEventContract || observation.ProtocolVersion != qoderProtocolVersion || observation.PermissionMode != qoderPermissionMode {
		return errors.New("qoder live conformance observation does not bind the frozen probe contract")
	}
	return nil
}

func liveObservationSigningBytes(observation LiveConformanceObservation) ([]byte, error) {
	unsigned := observation
	unsigned.VerifierSignature = ""
	data, err := json.Marshal(unsigned)
	if err != nil {
		return nil, err
	}
	return canonical.JSON(data)
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
	if err != nil || !info.IsDir() || !privateDirectory(stat, os.Geteuid()) {
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
	fd, err := unix.Openat(int(store.directory.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return ConformanceEvidence{}, fmt.Errorf("resolve qoder authority evidence: %w", err)
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || !privateRegularFile(stat, os.Geteuid()) {
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
