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
	"golang.org/x/sys/unix"
)

const authorityConfigLimit = 64 << 10

// AuthorityConfig is the production binding from one Qoder adapter instance
// to authority-owned, content-addressed conformance evidence. It contains
// public verification material only; private signing keys and credentials
// must never be placed in this document or inherited by Marshal.
type AuthorityConfig struct {
	EvidenceRoot   string               `json:"evidenceRoot"`
	EvidenceDigest string               `json:"evidenceDigest"`
	TrustRoots     []AuthorityTrustRoot `json:"trustRoots"`
}

type AuthorityTrustRoot struct {
	KeyID     string `json:"keyId"`
	Algorithm string `json:"algorithm"`
	PublicKey string `json:"publicKey"`
}

// NewFromAuthorityConfig constructs a Qoder adapter from a private,
// no-symlink authority config and immediately binds the exact signed evidence
// digest named by that config. Invalid, missing, stale, substituted or
// untrusted authority material fails construction closed.
func NewFromAuthorityConfig(ctx context.Context, executable string, validator *contract.Validator, configPath string) (*Adapter, error) {
	config, err := loadAuthorityConfig(configPath)
	if err != nil {
		return nil, err
	}
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
	adapter, err := NewWithConformanceAuthority(executable, validator, store)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	if err := adapter.BindConformance(ctx, config.EvidenceDigest); err != nil {
		_ = store.Close()
		return nil, err
	}
	return adapter, nil
}

func loadAuthorityConfig(path string) (AuthorityConfig, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return AuthorityConfig{}, errors.New("qoder conformance authority config path must be absolute and clean")
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return AuthorityConfig{}, errors.New("open qoder conformance authority config")
	}
	file := os.NewFile(uintptr(fd), filepath.Base(path))
	defer file.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0o077 != 0 {
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
	if !filepath.IsAbs(config.EvidenceRoot) || filepath.Clean(config.EvidenceRoot) != config.EvidenceRoot || !validSHA256Digest(config.EvidenceDigest) || len(config.TrustRoots) == 0 {
		return AuthorityConfig{}, errors.New("qoder conformance authority config is incomplete")
	}
	return config, nil
}
