package codex

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"sort"
	"strings"
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

// FreshHostAttestation always sources a new nonce from the supplied CSPRNG.
// The stable host identity is compared by digest; nonce/signature bytes are
// intentionally operation-specific and are never required to equal another
// valid attestation.
func FreshHostAttestation(ctx context.Context, identity LinuxHostIdentityV1, attestor TPMHostAttestor, verifier TPMHostAttestationVerifier, random io.Reader) (LinuxHostAttestationV1, error) {
	if ctx == nil || attestor == nil || verifier == nil {
		return LinuxHostAttestationV1{}, errors.New("codex TPM attestation dependency is unavailable")
	}
	if err := identity.Validate(); err != nil {
		return LinuxHostAttestationV1{}, err
	}
	if random == nil {
		random = rand.Reader
	}
	nonce := make([]byte, 32)
	if _, err := io.ReadFull(random, nonce); err != nil {
		return LinuxHostAttestationV1{}, errors.New("codex fresh host nonce is unavailable")
	}
	challengeDigest, err := hostChallengeDigest(identity, nonce)
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

func ValidateHostAttestation(attestation LinuxHostAttestationV1, expectedIdentityDigest string, verifier TPMHostAttestationVerifier) error {
	if attestation.SchemaVersion != hostAttestSchema || attestation.ChallengeAlgorithm != "TPM2_ECDSA_P256_SHA256" || verifier == nil {
		return errors.New("codex host attestation is invalid")
	}
	identityDigest, err := HostIdentityDigest(attestation.HostIdentity)
	if err != nil || identityDigest != expectedIdentityDigest {
		return errors.New("codex host attestation identity differs")
	}
	nonce, err := decodeNonce(attestation.ChallengeNonce)
	if err != nil {
		return err
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
	return nil
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

type FileIdentityV1 struct {
	DeviceMajor   uint64 `json:"deviceMajor"`
	DeviceMinor   uint64 `json:"deviceMinor"`
	Inode         uint64 `json:"inode"`
	MountIDUnique uint64 `json:"mountIdUnique"`
	Size          uint64 `json:"size"`
	SHA256        string `json:"sha256"`
}

type TopologyRoleV1 struct {
	Actor    string         `json:"actor"`
	Identity FileIdentityV1 `json:"identity"`
}

type TopologySnapshotV1 struct {
	SchemaVersion string           `json:"schemaVersion"`
	Phase         string           `json:"phase"`
	Observer      string           `json:"observer"`
	FixedRoots    []TopologyRoleV1 `json:"fixedRoots"`
	Executables   []TopologyRoleV1 `json:"executables"`
}

var topologyPhases = []string{"T0", "T1", "T2", "T3", "T4"}

func (snapshot TopologySnapshotV1) Digest() (string, error) {
	if err := snapshot.Validate(); err != nil {
		return "", err
	}
	return canonicalDigest(snapshot)
}

func (snapshot TopologySnapshotV1) Validate() error {
	if snapshot.SchemaVersion != "marshal.codex.topology-snapshot.v1" || !containsString(topologyPhases, snapshot.Phase) {
		return errors.New("codex topology phase is invalid")
	}
	expectedObserver := map[string]string{"T0": "consumer", "T1": "launcher", "T2": "launcher", "T3": "launch-receipt-authority", "T4": "consumer"}[snapshot.Phase]
	if snapshot.Observer != expectedObserver {
		return errors.New("codex topology phase observer is invalid")
	}
	if err := validateTopologyRoles(snapshot.FixedRoots); err != nil {
		return err
	}
	if err := validateTopologyRoles(snapshot.Executables); err != nil {
		return err
	}
	expected := map[string][]string{
		"T0": {"source"}, "T1": {"sealed", "source"}, "T2": {"sealed", "source"},
		"T3": {"child", "sealed", "source"}, "T4": {"child", "sealed", "source"},
	}[snapshot.Phase]
	actual := make([]string, len(snapshot.Executables))
	for index := range snapshot.Executables {
		actual[index] = snapshot.Executables[index].Actor
	}
	if !equalStrings(actual, expected) {
		return errors.New("codex topology executable membership is invalid")
	}
	return nil
}

func ValidateTopologyTransition(snapshots []TopologySnapshotV1) error {
	if len(snapshots) != len(topologyPhases) {
		return errors.New("codex topology transition is incomplete")
	}
	var fixed []TopologyRoleV1
	identities := make(map[string]FileIdentityV1)
	for index, snapshot := range snapshots {
		if snapshot.Phase != topologyPhases[index] {
			return errors.New("codex topology phase order is invalid")
		}
		if err := snapshot.Validate(); err != nil {
			return err
		}
		if index == 0 {
			fixed = snapshot.FixedRoots
		} else if !equalTopologyRoles(fixed, snapshot.FixedRoots) {
			return errors.New("codex fixed root identity changed")
		}
		for _, role := range snapshot.Executables {
			if old, exists := identities[role.Actor]; exists && old != role.Identity {
				return errors.New("codex executable identity changed across topology phases")
			}
			identities[role.Actor] = role.Identity
		}
	}
	source, sourceOK := identities["source"]
	sealed, sealedOK := identities["sealed"]
	child, childOK := identities["child"]
	if !sourceOK || !sealedOK || !childOK || source.SHA256 != sealed.SHA256 || source.SHA256 != child.SHA256 || source.Size != sealed.Size || source.Size != child.Size {
		return errors.New("codex source, sealed, and child bytes differ")
	}
	if sealed.DeviceMajor != child.DeviceMajor || sealed.DeviceMinor != child.DeviceMinor || sealed.Inode != child.Inode || sealed.MountIDUnique != child.MountIDUnique {
		return errors.New("codex sealed and child executable identity differ")
	}
	if source.DeviceMajor == sealed.DeviceMajor && source.DeviceMinor == sealed.DeviceMinor && source.Inode == sealed.Inode && source.MountIDUnique == sealed.MountIDUnique {
		return errors.New("codex source and sealed identities unexpectedly alias")
	}
	return nil
}

func validateTopologyRoles(roles []TopologyRoleV1) error {
	if len(roles) == 0 {
		return errors.New("codex topology role set is empty")
	}
	for index, role := range roles {
		if !validID(role.Actor) || !validDigest(role.Identity.SHA256) || role.Identity.Size == 0 || role.Identity.Inode == 0 || role.Identity.MountIDUnique == 0 {
			return errors.New("codex topology identity is invalid")
		}
		if index > 0 && roles[index-1].Actor >= role.Actor {
			return errors.New("codex topology roles are not canonical")
		}
	}
	return nil
}

func equalTopologyRoles(left, right []TopologyRoleV1) bool {
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

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
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

func SortedTopologyRoles(roles ...TopologyRoleV1) []TopologyRoleV1 {
	result := append([]TopologyRoleV1(nil), roles...)
	sort.Slice(result, func(i, j int) bool { return result[i].Actor < result[j].Actor })
	return result
}
