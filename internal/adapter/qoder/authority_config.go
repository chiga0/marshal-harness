package qoder

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/port"
	"golang.org/x/sys/unix"
)

const authorityConfigLimit = 64 << 10

// AuthorityConfig is the production binding from one Qoder adapter instance
// to authority-owned, content-addressed conformance evidence. It contains
// public verification material only; private signing keys and credentials
// must never be placed in this document or inherited by Marshal.
type AuthorityConfig struct {
	EvidenceRoot           string               `json:"evidenceRoot"`
	EvidenceDigest         string               `json:"evidenceDigest"`
	AuthorityGeneration    uint64               `json:"authorityGeneration"`
	ProbeArtifactDigest    string               `json:"probeArtifactDigest"`
	ChallengeDigest        string               `json:"challengeDigest"`
	RevokedEvidenceDigests []string             `json:"revokedEvidenceDigests"`
	TrustRoots             []AuthorityTrustRoot `json:"trustRoots"`
}

type AuthorityTrustRoot struct {
	KeyID     string `json:"keyId"`
	Algorithm string `json:"algorithm"`
	PublicKey string `json:"publicKey"`
}

// NewFromAuthorityConfig is the production admission boundary. ADR 0034 is
// still Proposed, so the candidate authority mechanism is deliberately not
// activatable by application configuration. Acceptance plus independent
// negative-matrix review must land before a later change can enable it.
func NewFromAuthorityConfig(ctx context.Context, executable string, validator *contract.Validator, configPath string) (*Adapter, error) {
	if ctx == nil || ctx.Err() != nil || strings.TrimSpace(configPath) == "" {
		return nil, port.Permanent(ErrConformancePending)
	}
	_, err := New(executable, validator)
	if err != nil {
		return nil, err
	}
	return nil, port.Permanent(ErrConformancePending)
}

func authorityFromConfig(config AuthorityConfig) (*AuthorityEvidenceStore, error) {
	trustRoots := make(map[string]ed25519.PublicKey, len(config.TrustRoots))
	for _, root := range config.TrustRoots {
		if strings.TrimSpace(root.KeyID) == "" || root.Algorithm != "ed25519" {
			return nil, errors.New("qoder conformance authority trust root is invalid")
		}
		key, decodeErr := base64.StdEncoding.DecodeString(root.PublicKey)
		if decodeErr != nil || len(key) != ed25519.PublicKeySize {
			return nil, errors.New("qoder conformance authority trust root is invalid")
		}
		if _, duplicate := trustRoots[root.KeyID]; duplicate {
			return nil, errors.New("qoder conformance authority trust root is duplicated")
		}
		trustRoots[root.KeyID] = ed25519.PublicKey(key)
	}
	store, err := NewAuthorityEvidenceStore(config.EvidenceRoot, trustRoots)
	if err != nil {
		return nil, err
	}
	return store, nil
}

func loadAuthorityConfig(path string) (AuthorityConfig, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return AuthorityConfig{}, errors.New("qoder conformance authority config path must be absolute and clean")
	}
	file, stat, err := openNoSymlinkPath(path, false)
	if err != nil {
		return AuthorityConfig{}, errors.New("open qoder conformance authority config")
	}
	defer file.Close()
	if !privateRegularFile(stat, os.Geteuid()) {
		return AuthorityConfig{}, errors.New("qoder conformance authority config must be a private regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, authorityConfigLimit+1))
	if err != nil || len(data) > authorityConfigLimit {
		return AuthorityConfig{}, errors.New("qoder conformance authority config is unreadable or oversized")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var config AuthorityConfig
	if err := decoder.Decode(&config); err != nil {
		return AuthorityConfig{}, errors.New("qoder conformance authority config is invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return AuthorityConfig{}, errors.New("qoder conformance authority config is invalid")
	}
	if !filepath.IsAbs(config.EvidenceRoot) || filepath.Clean(config.EvidenceRoot) != config.EvidenceRoot || !validSHA256Digest(config.EvidenceDigest) || !validSHA256Digest(config.ProbeArtifactDigest) || !validSHA256Digest(config.ChallengeDigest) || config.AuthorityGeneration == 0 || len(config.TrustRoots) == 0 {
		return AuthorityConfig{}, errors.New("qoder conformance authority config is incomplete")
	}
	seen := map[string]struct{}{}
	for _, digest := range config.RevokedEvidenceDigests {
		if !validSHA256Digest(digest) {
			return AuthorityConfig{}, errors.New("qoder conformance authority config has an invalid revocation")
		}
		if _, duplicate := seen[digest]; duplicate {
			return AuthorityConfig{}, errors.New("qoder conformance authority config has a duplicate revocation")
		}
		seen[digest] = struct{}{}
	}
	if _, revoked := seen[config.EvidenceDigest]; revoked {
		return AuthorityConfig{}, errors.New("qoder conformance authority evidence is revoked")
	}
	return config, nil
}

// openNoSymlinkPath walks every absolute path component through directory
// file descriptors. O_NOFOLLOW on only the leaf is insufficient because an
// attacker could substitute a symlinked parent before authority admission.
func openNoSymlinkPath(path string, directory bool) (*os.File, unix.Stat_t, error) {
	var zero unix.Stat_t
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) {
		return nil, zero, errors.New("authority path must be absolute and clean")
	}
	parts := strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator))
	current, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, zero, err
	}
	for index, part := range parts {
		flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC
		if index < len(parts)-1 || directory {
			flags |= unix.O_DIRECTORY
		} else {
			flags |= unix.O_NONBLOCK
		}
		next, openErr := unix.Openat(current, part, flags, 0)
		_ = unix.Close(current)
		if openErr != nil {
			return nil, zero, openErr
		}
		current = next
	}
	var stat unix.Stat_t
	if err := unix.Fstat(current, &stat); err != nil {
		_ = unix.Close(current)
		return nil, zero, err
	}
	return os.NewFile(uintptr(current), filepath.Base(path)), stat, nil
}

func privateRegularFile(stat unix.Stat_t, effectiveUID int) bool {
	return stat.Mode&unix.S_IFMT == unix.S_IFREG && stat.Mode&0o077 == 0 && (int(stat.Uid) == effectiveUID || stat.Uid == 0)
}

func privateDirectory(stat unix.Stat_t, effectiveUID int) bool {
	return stat.Mode&unix.S_IFMT == unix.S_IFDIR && stat.Mode&0o777 == 0o700 && (int(stat.Uid) == effectiveUID || stat.Uid == 0)
}
