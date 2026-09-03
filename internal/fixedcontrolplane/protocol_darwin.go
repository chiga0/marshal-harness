//go:build darwin && arm64

package fixedcontrolplane

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/processsupervisor"
	"github.com/chiga0/marshal-harness/internal/productionruntime"
	"github.com/chiga0/marshal-harness/internal/resultingress"
)

const (
	maxHandshakeFrame = 16 << 10
	handshakeTimeout  = 5 * time.Second
)

type challengeFrame struct {
	SchemaVersion          string `json:"schemaVersion"`
	ProtocolRevision       string `json:"protocolRevision"`
	Nonce                  string `json:"nonce"`
	ExpiresAt              string `json:"expiresAt"`
	OwnerEpoch             uint64 `json:"ownerEpoch"`
	OwnerFactDigest        string `json:"ownerFactDigest"`
	OwnerAcquisitionDigest string `json:"ownerAcquisitionDigest"`
	RepositoryDigest       string `json:"repositoryDigest"`
	AuthorityRootDigest    string `json:"authorityRootDigest"`
	ServerIdentityDigest   string `json:"serverIdentityDigest"`
	SocketIdentityDigest   string `json:"socketIdentityDigest"`
}

type proofFrame struct {
	SchemaVersion        string         `json:"schemaVersion"`
	ProtocolRevision     string         `json:"protocolRevision"`
	ChallengeDigest      string         `json:"challengeDigest"`
	ClientIdentityDigest string         `json:"clientIdentityDigest"`
	Binding              RequestBinding `json:"binding"`
	Proof                string         `json:"proof"`
}

type acceptedFrame struct {
	SchemaVersion    string `json:"schemaVersion"`
	ProtocolRevision string `json:"protocolRevision"`
	ChallengeDigest  string `json:"challengeDigest"`
	ProofDigest      string `json:"proofDigest"`
}

func validateSnapshot(snapshot productionruntime.FixedEndpointSnapshot) error {
	digest, err := resultingress.ControlOwnerAcquisitionDigest(snapshot.Acquisition)
	if err != nil || digest != snapshot.OwnerAcquisitionDigest || snapshot.RepositoryDigest != snapshot.Acquisition.Scope.RepositoryIdentityDigest || snapshot.FixedMarshalPath != snapshot.Acquisition.OwnerBinary.CanonicalPath || !digestPattern.MatchString(snapshot.OwnerFactDigest) || !digestPattern.MatchString(snapshot.AuthorityRootDigest) || !filepath.IsAbs(snapshot.ControlPath) || filepath.Clean(snapshot.ControlPath) != snapshot.ControlPath || strings.IndexByte(snapshot.ControlPath, 0) >= 0 {
		return ErrInvalid
	}
	return nil
}

func expectedServer(snapshot productionruntime.FixedEndpointSnapshot) processsupervisor.CoreIdentity {
	return processsupervisor.CoreIdentity{UID: snapshot.Acquisition.OwnerUID, GID: snapshot.Acquisition.OwnerGID, Process: snapshot.Acquisition.OwnerProcess, Binary: snapshot.Acquisition.OwnerBinary}
}

func canonicalBytes(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, ErrInvalid
	}
	raw, err = canonical.JSON(raw)
	if err != nil || len(raw) == 0 || len(raw) > maxHandshakeFrame {
		return nil, ErrInvalid
	}
	return raw, nil
}

func decodeClosed(raw []byte, target any) error {
	canonicalRaw, err := canonical.JSON(raw)
	if err != nil || !bytes.Equal(raw, canonicalRaw) {
		return ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil {
		return ErrInvalid
	}
	var extra json.RawMessage
	if decoder.Decode(&extra) == nil {
		return ErrInvalid
	}
	return nil
}

func identityDigest(identity processsupervisor.CoreIdentity) (string, error) {
	raw, err := canonicalBytes(identity)
	if err != nil {
		return "", err
	}
	return canonical.DigestBytes(raw), nil
}

func proofDigest(token []byte, challenge challengeFrame, clientDigest string, binding RequestBinding) (string, error) {
	value := struct {
		ProtocolRevision     string         `json:"protocolRevision"`
		Challenge            challengeFrame `json:"challenge"`
		ClientIdentityDigest string         `json:"clientIdentityDigest"`
		Binding              RequestBinding `json:"binding"`
	}{ProtocolRevision: ProtocolRevision, Challenge: challenge, ClientIdentityDigest: clientDigest, Binding: binding}
	raw, err := canonicalBytes(value)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, token)
	_, _ = mac.Write(raw)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func exactHex(value string, bytes int) bool {
	raw, err := hex.DecodeString(value)
	return err == nil && len(raw) == bytes && hex.EncodeToString(raw) == value
}
