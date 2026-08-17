package codex

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
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"golang.org/x/sys/unix"
)

// ConformanceEvidence 是独立 runner 签名且内容寻址的 authority 记录。
// Adapter 只接受其 digest，并从构造时固定的私有 store 解析与验签。
type ConformanceEvidence struct {
	EvidenceDigest       string `json:"evidenceDigest"`
	RunnerID             string `json:"runnerId"`
	RunnerVersion        string `json:"runnerVersion"`
	ObservedAt           string `json:"observedAt"`
	ValidUntil           string `json:"validUntil"`
	AdapterVersion       string `json:"adapterVersion"`
	Executable           string `json:"executable"`
	ExecutableDigest     string `json:"executableDigest"`
	BinaryVersion        string `json:"binaryVersion"`
	CapabilitiesDigest   string `json:"capabilitiesDigest"`
	TranscriptDigest     string `json:"transcriptDigest"`
	CredentialVerified   bool   `json:"credentialVerified"`
	LiveProtocolVerified bool   `json:"liveProtocolVerified"`
	EventContract        string `json:"eventContract"`
	CodexCLIVersion      string `json:"codexCliVersion"`
	ProtocolVersion      string `json:"protocolVersion"`
	PermissionMode       string `json:"permissionMode"`
	TrustRootKeyID       string `json:"trustRootKeyId"`
	Signature            string `json:"signature"`
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
		return errors.New("codex conformance evidence digest mismatch")
	}
	if evidence.RunnerID == "" || evidence.RunnerID == adapterID || evidence.RunnerVersion == "" || evidence.TrustRootKeyID == "" {
		return errors.New("codex conformance evidence lacks independent runner provenance")
	}
	observedAt, err := time.Parse(time.RFC3339Nano, evidence.ObservedAt)
	if err != nil || observedAt.After(now) || now.Sub(observedAt) > maxConformanceAge {
		return errors.New("codex conformance evidence observedAt is invalid")
	}
	validUntil, err := time.Parse(time.RFC3339Nano, evidence.ValidUntil)
	if err != nil || !now.Before(validUntil) || !observedAt.Before(validUntil) || validUntil.Sub(observedAt) > maxConformanceTTL {
		return errors.New("codex conformance evidence is expired or has an invalid validity window")
	}
	if evidence.AdapterVersion != adapterVersion || evidence.CapabilitiesDigest != expectedCapabilitiesDigest() ||
		!validSHA256Digest(evidence.ExecutableDigest) || !validSHA256Digest(evidence.TranscriptDigest) ||
		!evidence.CredentialVerified || !evidence.LiveProtocolVerified {
		return errors.New("codex conformance evidence does not bind the complete verified adapter contract")
	}
	if evidence.EventContract != conformanceEventContract || evidence.CodexCLIVersion != evidence.BinaryVersion ||
		evidence.ProtocolVersion != codexProtocolVersion || evidence.PermissionMode != codexPermissionMode {
		return errors.New("codex conformance evidence carries a different live protocol contract")
	}
	publicKey, ok := trustRoots[evidence.TrustRootKeyID]
	if !ok || len(publicKey) != ed25519.PublicKeySize {
		return errors.New("codex conformance evidence trust root is not configured")
	}
	signature, err := base64.StdEncoding.DecodeString(evidence.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return errors.New("codex conformance evidence signature is malformed")
	}
	message, err := evidence.signingBytes()
	if err != nil || !ed25519.Verify(publicKey, message, signature) {
		return errors.New("codex conformance evidence signature is not trusted")
	}
	return nil
}

func validSHA256Digest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, character := range value[len("sha256:"):] {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

// AuthorityEvidenceStore 从私有、内容寻址目录读取不可变的签名 evidence。
type AuthorityEvidenceStore struct {
	root       string
	directory  *os.File
	trustRoots map[string]ed25519.PublicKey
}

func NewAuthorityEvidenceStore(root string, trustRoots map[string]ed25519.PublicKey) (*AuthorityEvidenceStore, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || len(trustRoots) == 0 {
		return nil, errors.New("codex conformance authority requires an absolute root and trust roots")
	}
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, errors.New("codex conformance authority root must be a real directory")
	}
	linkInfo, err := os.Lstat(real)
	if err != nil || linkInfo.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("codex conformance authority root must be a real directory")
	}
	directory, err := os.Open(real)
	if err != nil {
		return nil, err
	}
	info, err := directory.Stat()
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		directory.Close()
		return nil, errors.New("codex conformance authority root must be a private directory")
	}
	keys := make(map[string]ed25519.PublicKey, len(trustRoots))
	for id, key := range trustRoots {
		if strings.TrimSpace(id) == "" || len(key) != ed25519.PublicKeySize {
			directory.Close()
			return nil, errors.New("codex conformance authority trust root is invalid")
		}
		keys[id] = append(ed25519.PublicKey(nil), key...)
	}
	return &AuthorityEvidenceStore{root: real, directory: directory, trustRoots: keys}, nil
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
		return ConformanceEvidence{}, fmt.Errorf("resolve codex authority evidence: %w", err)
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0o077 != 0 {
		return ConformanceEvidence{}, errors.New("codex authority evidence must be a private regular file")
	}
	data, err := readBoundedFile(file, maxResultBytes)
	if err != nil {
		return ConformanceEvidence{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var evidence ConformanceEvidence
	if err := decoder.Decode(&evidence); err != nil {
		return ConformanceEvidence{}, errors.New("codex authority evidence document is invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ConformanceEvidence{}, errors.New("codex authority evidence document is invalid")
	}
	if evidence.EvidenceDigest != digest {
		return ConformanceEvidence{}, errors.New("codex authority evidence path does not match its digest")
	}
	if err := evidence.validate(now, store.trustRoots); err != nil {
		return ConformanceEvidence{}, err
	}
	return evidence, nil
}
