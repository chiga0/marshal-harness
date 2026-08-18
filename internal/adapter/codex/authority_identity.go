package codex

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"sync"
)

// TPMHostAttestor is the narrow package boundary to a hardware-backed host
// key. It accepts a consumer-generated fresh nonce and never exports private
// material. Production wiring remains hard-disabled until a separately
// reviewed TPM implementation and live evidence exist.
type TPMHostAttestor interface {
	Attest(context.Context, LinuxHostIdentityV1, []byte) ([]byte, error)
}

// TPMHostAttestationVerifier verifies an r||s signature against the TPM2B
// public area pinned in the stable host identity. Keeping verification behind
// this seam prevents this core slice from pretending a generic P-256 key is a
// validated hardware TPM identity.
type TPMHostAttestationVerifier interface {
	Verify(LinuxHostIdentityV1, []byte, []byte) error
}

func (identity LinuxHostIdentityV1) Validate() error {
	if identity.SchemaVersion != hostIdentitySchema || identity.OS != "linux" ||
		(identity.Arch != "amd64" && identity.Arch != "arm64") ||
		!validDigest(identity.MachineIDDigest) || !validDigest(identity.TPMEKCertificateDigest) ||
		!validDigest(identity.TPMHostKeyPublicDigest) || !validDigest(identity.TPMHostKeyQualifiedNameDigest) {
		return errors.New("codex stable host identity is invalid")
	}
	if _, err := decodeNonce(identity.BootstrapID); err != nil {
		return err
	}
	publicArea, err := base64.StdEncoding.DecodeString(identity.TPMHostKeyPublic)
	if err != nil || len(publicArea) == 0 || len(publicArea) > 1024 || base64.StdEncoding.EncodeToString(publicArea) != identity.TPMHostKeyPublic {
		return errors.New("codex TPM public area is invalid")
	}
	if canonicalDigestBytes(publicArea) != identity.TPMHostKeyPublicDigest {
		return errors.New("codex TPM public area digest differs")
	}
	return nil
}

func HostIdentityDigest(identity LinuxHostIdentityV1) (string, error) {
	if err := identity.Validate(); err != nil {
		return "", err
	}
	return canonicalDigest(identity)
}

// HostAttestationNonceFence consumes caller-generated nonces exactly once.
// It is consumer-owned and is never populated from an attestation payload.
type HostAttestationNonceFence struct {
	mu       sync.Mutex
	consumed map[string]struct{}
}

func NewHostAttestationNonceFence() *HostAttestationNonceFence {
	return &HostAttestationNonceFence{consumed: make(map[string]struct{})}
}

func (fence *HostAttestationNonceFence) consume(nonce []byte) error {
	if fence == nil || len(nonce) != 32 {
		return errors.New("codex host attestation nonce fence is unavailable")
	}
	encoded := base64.RawURLEncoding.EncodeToString(nonce)
	fence.mu.Lock()
	defer fence.mu.Unlock()
	if _, exists := fence.consumed[encoded]; exists {
		return errors.New("codex host attestation nonce replayed")
	}
	fence.consumed[encoded] = struct{}{}
	return nil
}

// FreshHostAttestation signs only the consumer-provided expected nonce. Nonce
// generation belongs to the caller/Core boundary, never to the attestor.
func FreshHostAttestation(ctx context.Context, identity LinuxHostIdentityV1, expectedNonce []byte, attestor TPMHostAttestor, verifier TPMHostAttestationVerifier) (LinuxHostAttestationV1, error) {
	if ctx == nil || attestor == nil || verifier == nil {
		return LinuxHostAttestationV1{}, errors.New("codex TPM attestation dependency is unavailable")
	}
	if err := identity.Validate(); err != nil {
		return LinuxHostAttestationV1{}, err
	}
	if len(expectedNonce) != 32 {
		return LinuxHostAttestationV1{}, errors.New("codex expected host nonce is invalid")
	}
	nonce := append([]byte(nil), expectedNonce...)
	challengeDigest, err := hostChallengeDigest(identity, expectedNonce)
	if err != nil {
		return LinuxHostAttestationV1{}, err
	}
	signature, err := attestor.Attest(ctx, identity, challengeDigest)
	if err != nil || len(signature) != 64 {
		return LinuxHostAttestationV1{}, errors.New("codex TPM host challenge failed")
	}
	if err := verifier.Verify(identity, challengeDigest, signature); err != nil {
		return LinuxHostAttestationV1{}, errors.New("codex TPM host challenge is invalid")
	}
	return LinuxHostAttestationV1{
		SchemaVersion: hostAttestSchema, HostIdentity: identity,
		ChallengeNonce:     base64.RawURLEncoding.EncodeToString(nonce),
		ChallengeAlgorithm: "TPM2_ECDSA_P256_SHA256",
		ChallengeSignature: base64.StdEncoding.EncodeToString(signature),
	}, nil
}

func ValidateHostAttestation(attestation LinuxHostAttestationV1, expectedIdentityDigest string, expectedNonce []byte, verifier TPMHostAttestationVerifier, nonceFence *HostAttestationNonceFence) error {
	if attestation.SchemaVersion != hostAttestSchema || attestation.ChallengeAlgorithm != "TPM2_ECDSA_P256_SHA256" || verifier == nil {
		return errors.New("codex host attestation is invalid")
	}
	identityDigest, err := HostIdentityDigest(attestation.HostIdentity)
	if err != nil || identityDigest != expectedIdentityDigest {
		return errors.New("codex host attestation identity differs")
	}
	nonce, err := decodeNonce(attestation.ChallengeNonce)
	if err != nil || !bytes.Equal(nonce, expectedNonce) {
		return errors.New("codex host attestation nonce differs")
	}
	signature, err := base64.StdEncoding.DecodeString(attestation.ChallengeSignature)
	if err != nil || len(signature) != 64 || base64.StdEncoding.EncodeToString(signature) != attestation.ChallengeSignature {
		return errors.New("codex host attestation signature is invalid")
	}
	challengeDigest, err := hostChallengeDigest(attestation.HostIdentity, nonce)
	if err != nil {
		return err
	}
	if err := verifier.Verify(attestation.HostIdentity, challengeDigest, signature); err != nil {
		return errors.New("codex host attestation signature is invalid")
	}
	return nonceFence.consume(nonce)
}

func hostChallengeDigest(identity LinuxHostIdentityV1, nonce []byte) ([]byte, error) {
	identityDigest, err := HostIdentityDigest(identity)
	if err != nil || len(nonce) != 32 {
		return nil, errors.New("codex host challenge projection is invalid")
	}
	projection := struct {
		ChallengeNonce     string `json:"challengeNonce"`
		HostIdentityDigest string `json:"hostIdentityDigest"`
	}{base64.RawURLEncoding.EncodeToString(nonce), identityDigest}
	digest, err := canonicalDigest(projection)
	if err != nil {
		return nil, err
	}
	decoded, err := hex.DecodeString(strings.TrimPrefix(digest, "sha256:"))
	if err != nil || len(decoded) != sha256.Size {
		return nil, errors.New("codex host challenge projection is invalid")
	}
	return decoded, nil
}

type MountObjectIdentityV1 struct {
	Role          string  `json:"role"`
	DeviceMajor   uint64  `json:"deviceMajor"`
	DeviceMinor   uint64  `json:"deviceMinor"`
	Inode         uint64  `json:"inode"`
	MountIDUnique uint64  `json:"mountIdUnique"`
	Mode          uint32  `json:"mode"`
	UID           uint32  `json:"uid"`
	GID           uint32  `json:"gid"`
	Size          uint64  `json:"size"`
	SHA256        *string `json:"sha256"`
}

type TopologyObjectV1 struct {
	Identity      MountObjectIdentityV1   `json:"identity"`
	AncestorChain []MountObjectIdentityV1 `json:"ancestorChain"`
}

type TopologySnapshotV1 struct {
	SchemaVersion        string             `json:"schemaVersion"`
	MountNamespaceDevice uint64             `json:"mountNamespaceDevice"`
	MountNamespaceInode  uint64             `json:"mountNamespaceInode"`
	Phase                string             `json:"phase"`
	FixedRoots           []TopologyObjectV1 `json:"fixedRoots"`
	Executables          []TopologyObjectV1 `json:"executables"`
}

var topologyPhases = []string{"consumer-open", "launcher-pre-seal", "child-pre-exec", "child-post-exec-barrier", "consumer-receipt-accept"}
var fixedRootRoles = []string{"authorityRoot", "fenceRoot", "worktree", "controlRoot", "controlInput", "controlOutput"}
var executableRoles = [][]string{{"sourceExecutable"}, {"sourceExecutable", "sealedExecutable"}, {"sourceExecutable", "sealedExecutable"}, {"sourceExecutable", "sealedExecutable", "childExecutable"}, {"sourceExecutable", "sealedExecutable", "childExecutable"}}

func (snapshot TopologySnapshotV1) Digest() (string, error) {
	if err := snapshot.Validate(); err != nil {
		return "", err
	}
	return canonicalDigest(snapshot)
}

func (snapshot TopologySnapshotV1) Validate() error {
	phaseIndex := stringIndex(topologyPhases, snapshot.Phase)
	if snapshot.SchemaVersion != "marshal.codex.topology-snapshot.v1" || phaseIndex < 0 || snapshot.MountNamespaceInode == 0 {
		return errors.New("codex topology phase is invalid")
	}
	if err := validateTopologyObjects(snapshot.FixedRoots, fixedRootRoles); err != nil {
		return err
	}
	if err := validateTopologyObjects(snapshot.Executables, executableRoles[phaseIndex]); err != nil {
		return err
	}
	return nil
}

func ValidateTopologyTransition(snapshots []TopologySnapshotV1) error {
	if len(snapshots) != len(topologyPhases) {
		return errors.New("codex topology transition is incomplete")
	}
	var fixed []TopologyObjectV1
	var namespaceDevice, namespaceInode uint64
	identities := make(map[string]TopologyObjectV1)
	for index, snapshot := range snapshots {
		if snapshot.Phase != topologyPhases[index] {
			return errors.New("codex topology phase order is invalid")
		}
		if err := snapshot.Validate(); err != nil {
			return err
		}
		if index == 0 {
			fixed = snapshot.FixedRoots
			namespaceDevice, namespaceInode = snapshot.MountNamespaceDevice, snapshot.MountNamespaceInode
		} else if !reflect.DeepEqual(fixed, snapshot.FixedRoots) || snapshot.MountNamespaceDevice != namespaceDevice || snapshot.MountNamespaceInode != namespaceInode {
			return errors.New("codex fixed root identity changed")
		}
		for _, object := range snapshot.Executables {
			role := object.Identity.Role
			if old, exists := identities[role]; exists && !reflect.DeepEqual(old, object) {
				return errors.New("codex executable identity changed across topology phases")
			}
			identities[role] = object
		}
	}
	source, sourceOK := identities["sourceExecutable"]
	sealed, sealedOK := identities["sealedExecutable"]
	child, childOK := identities["childExecutable"]
	if !sourceOK || !sealedOK || !childOK || source.Identity.SHA256 == nil || sealed.Identity.SHA256 == nil || child.Identity.SHA256 == nil || *source.Identity.SHA256 != *sealed.Identity.SHA256 || *source.Identity.SHA256 != *child.Identity.SHA256 || source.Identity.Size != sealed.Identity.Size || source.Identity.Size != child.Identity.Size {
		return errors.New("codex source, sealed, and child bytes differ")
	}
	if sealed.Identity.DeviceMajor != child.Identity.DeviceMajor || sealed.Identity.DeviceMinor != child.Identity.DeviceMinor || sealed.Identity.Inode != child.Identity.Inode || sealed.Identity.MountIDUnique != child.Identity.MountIDUnique || sealed.Identity.Size != child.Identity.Size {
		return errors.New("codex sealed and child executable identity differ")
	}
	if source.Identity.DeviceMajor == sealed.Identity.DeviceMajor && source.Identity.DeviceMinor == sealed.Identity.DeviceMinor && source.Identity.Inode == sealed.Identity.Inode && source.Identity.MountIDUnique == sealed.Identity.MountIDUnique {
		return errors.New("codex source and sealed identities unexpectedly alias")
	}
	return nil
}

func validateTopologyObjects(objects []TopologyObjectV1, roles []string) error {
	if len(objects) != len(roles) {
		return errors.New("codex topology object cardinality is invalid")
	}
	for index, object := range objects {
		isExecutable := strings.HasSuffix(roles[index], "Executable")
		if object.Identity.Role != roles[index] || !validID(object.Identity.Role) || object.Identity.Inode == 0 || object.Identity.MountIDUnique == 0 || len(object.AncestorChain) < 1 || len(object.AncestorChain) > 256 || isExecutable && (object.Identity.SHA256 == nil || object.Identity.Size == 0) || !isExecutable && object.Identity.SHA256 != nil {
			return errors.New("codex topology identity is invalid")
		}
		if object.Identity.SHA256 != nil && !validDigest(*object.Identity.SHA256) {
			return errors.New("codex topology content digest is invalid")
		}
		seen := make(map[[3]uint64]struct{}, len(object.AncestorChain))
		for _, ancestor := range object.AncestorChain {
			if !validID(ancestor.Role) || ancestor.Inode == 0 || ancestor.MountIDUnique == 0 || ancestor.SHA256 != nil {
				return errors.New("codex topology ancestor identity is invalid")
			}
			key := [3]uint64{ancestor.DeviceMajor<<32 | ancestor.DeviceMinor, ancestor.Inode, ancestor.MountIDUnique}
			if _, duplicate := seen[key]; duplicate {
				return errors.New("codex topology ancestor chain loops")
			}
			seen[key] = struct{}{}
		}
	}
	return nil
}

func stringIndex(values []string, target string) int {
	for index, value := range values {
		if value == target {
			return index
		}
	}
	return -1
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func canonicalDigestBytes(value []byte) string {
	return digestBytesHex(value)
}
