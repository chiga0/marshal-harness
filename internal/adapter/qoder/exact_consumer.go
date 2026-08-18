package qoder

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"path/filepath"
	"sort"
	"time"
)

const (
	probeTrustSigningDomain    = "marshal-qoder-trust-key-v1\x00"
	exactEvidenceSigningDomain = "marshal-qoder-evidence-v1\x00"
)

type QoderProbeTrustKeyRecord struct {
	APIVersion                 string  `json:"apiVersion"`
	Kind                       string  `json:"kind"`
	SchemaVersion              uint64  `json:"schemaVersion"`
	LedgerEpoch                uint64  `json:"ledgerEpoch"`
	Role                       string  `json:"role"`
	KeyID                      string  `json:"keyId"`
	KeyEpoch                   uint64  `json:"keyEpoch"`
	Operation                  string  `json:"operation"`
	PublicKeyEncoding          string  `json:"publicKeyEncoding"`
	Ed25519PublicKey           string  `json:"ed25519PublicKey"`
	PublicKeyDigest            string  `json:"publicKeyDigest"`
	PreviousRecordDigest       *string `json:"previousRecordDigest"`
	EffectiveAt                string  `json:"effectiveAt"`
	OperatorKeyID              string  `json:"operatorKeyId"`
	OperatorKeyEpoch           uint64  `json:"operatorKeyEpoch"`
	OperatorSignatureAlgorithm string  `json:"operatorSignatureAlgorithm"`
	OperatorSignatureEncoding  string  `json:"operatorSignatureEncoding"`
	OperatorSignature          string  `json:"operatorSignature"`
	RecordDigest               string  `json:"recordDigest"`
}

type QoderProbeTrustState struct {
	TailDigest  string
	LedgerEpoch uint64
	ActiveKeys  map[string][]QoderOSTrustKeyIdentity
}

type QoderAuthorityConfigExact struct {
	APIVersion                    string                `json:"apiVersion"`
	Kind                          string                `json:"kind"`
	SchemaVersion                 uint64                `json:"schemaVersion"`
	RepositoryIdentity            string                `json:"repositoryIdentity"`
	AuthorityGeneration           uint64                `json:"authorityGeneration"`
	HostIdentityDigest            string                `json:"hostIdentityDigest"`
	EvidenceRootIdentity          CandidateRootIdentity `json:"evidenceRootIdentity"`
	CurrentEvidenceDigest         string                `json:"currentEvidenceDigest"`
	ProbeArtifactDigest           string                `json:"probeArtifactDigest"`
	ProbeRunChallengeDigest       string                `json:"probeRunChallengeDigest"`
	RevokedEvidenceDigests        []string              `json:"revokedEvidenceDigests"`
	TrustPolicyDigest             string                `json:"trustPolicyDigest"`
	ReceiptTrustLedgerTailDigest  string                `json:"receiptTrustLedgerTailDigest"`
	VerifierTrustLedgerTailDigest string                `json:"verifierTrustLedgerTailDigest"`
	EvidenceTrustLedgerTailDigest string                `json:"evidenceTrustLedgerTailDigest"`
	OSTrustRootLedgerTailDigest   string                `json:"osTrustRootLedgerTailDigest"`
	ConsumerFenceProviderIdentity string                `json:"consumerFenceProviderIdentity"`
	ConfigDigest                  string                `json:"configDigest"`
}

type QoderConformanceEvidenceExact struct {
	APIVersion                    string                               `json:"apiVersion"`
	Kind                          string                               `json:"kind"`
	SchemaVersion                 uint64                               `json:"schemaVersion"`
	EvidenceDigest                string                               `json:"evidenceDigest"`
	ObservationDigest             string                               `json:"observationDigest"`
	ProbeRunID                    string                               `json:"probeRunId"`
	RunnerID                      string                               `json:"runnerId"`
	RunnerVersion                 string                               `json:"runnerVersion"`
	ObservedAt                    string                               `json:"observedAt"`
	ValidUntil                    string                               `json:"validUntil"`
	AdapterVersion                string                               `json:"adapterVersion"`
	CandidateExecutableIdentity   CandidateExecutableReceiptIdentity   `json:"candidateExecutableIdentity"`
	HostIdentity                  HostAttestationIdentity              `json:"hostIdentity"`
	AuthorityGeneration           uint64                               `json:"authorityGeneration"`
	SuiteDigest                   string                               `json:"suiteDigest"`
	ProbeArtifactDigest           string                               `json:"probeArtifactDigest"`
	ProbeRunChallengeDigest       string                               `json:"probeRunChallengeDigest"`
	CapabilitiesDigest            string                               `json:"capabilitiesDigest"`
	ProfileDigest                 string                               `json:"profileDigest"`
	VariantInvocationManifests    []CandidateVariantInvocationManifest `json:"variantInvocationManifests"`
	ToolPolicyDigest              string                               `json:"toolPolicyDigest"`
	EventContract                 string                               `json:"eventContract"`
	ProtocolVersion               string                               `json:"protocolVersion"`
	PermissionMode                string                               `json:"permissionMode"`
	TranscriptDigest              string                               `json:"transcriptDigest"`
	ReceiptDigests                []string                             `json:"receiptDigests"`
	AggregateReceiptDigest        string                               `json:"aggregateReceiptDigest"`
	CredentialVerified            bool                                 `json:"credentialVerified"`
	LiveProtocolVerified          bool                                 `json:"liveProtocolVerified"`
	WorkspaceWriteVerified        bool                                 `json:"workspaceWriteVerified"`
	ReceiptTrustLedgerTailDigest  string                               `json:"receiptTrustLedgerTailDigest"`
	VerifierTrustLedgerTailDigest string                               `json:"verifierTrustLedgerTailDigest"`
	EvidenceTrustLedgerTailDigest string                               `json:"evidenceTrustLedgerTailDigest"`
	OSTrustRootLedgerTailDigest   string                               `json:"osTrustRootLedgerTailDigest"`
	EvidenceAuthorityKeyID        string                               `json:"evidenceAuthorityKeyId"`
	EvidenceAuthorityKeyEpoch     uint64                               `json:"evidenceAuthorityKeyEpoch"`
	SignatureAlgorithm            string                               `json:"signatureAlgorithm"`
	SignatureEncoding             string                               `json:"signatureEncoding"`
	Signature                     string                               `json:"signature"`
}

func DecodeQoderProbeTrustKeyRecord(document []byte) (QoderProbeTrustKeyRecord, error) {
	return decodeExactAuthorityDocument[QoderProbeTrustKeyRecord](document)
}
func DecodeQoderAuthorityConfigExact(document []byte) (QoderAuthorityConfigExact, error) {
	return decodeExactAuthorityDocument[QoderAuthorityConfigExact](document)
}
func DecodeQoderConformanceEvidenceExact(document []byte) (QoderConformanceEvidenceExact, error) {
	return decodeExactAuthorityDocument[QoderConformanceEvidenceExact](document)
}

func ReplayQoderProbeTrustLedger(records []QoderProbeTrustKeyRecord, operatorKeys []QoderOSTrustKeyIdentity, now time.Time) (QoderProbeTrustState, error) {
	if len(records) < 3 || len(records) > 4096 || len(operatorKeys) == 0 {
		return QoderProbeTrustState{}, errors.New("qoder probe trust ledger is incomplete")
	}
	operators := map[string]QoderOSTrustKeyIdentity{}
	operatorDigests := map[string]bool{}
	for _, key := range operatorKeys {
		if key.Role != "trust-ledger-operator" || !validCandidateASCII(key.KeyID) || key.KeyEpoch > candidateMaxJSONInteger || !validSHA256Digest(key.PublicKeyDigest) || len(key.PublicKey) != ed25519.PublicKeySize || digestBytes(key.PublicKey) != key.PublicKeyDigest || operators[key.KeyID].KeyID != "" || operatorDigests[key.PublicKeyDigest] {
			return QoderProbeTrustState{}, errors.New("qoder probe trust operator set is invalid")
		}
		operators[key.KeyID] = key
		operatorDigests[key.PublicKeyDigest] = true
	}
	active := map[string]map[string]QoderOSTrustKeyIdentity{}
	seenID, seenDigest, lastEpoch := map[string]bool{}, map[string]bool{}, map[string]uint64{}
	started := map[string]bool{}
	var previous string
	for i, record := range records {
		if record.APIVersion != exactAuthorityAPIVersion || record.Kind != "QoderProbeTrustKeyRecord" || record.SchemaVersion != 1 || record.LedgerEpoch != uint64(i) || record.LedgerEpoch > candidateMaxJSONInteger || !validProbeTrustRole(record.Role) || !validCandidateASCII(record.KeyID) || record.KeyEpoch > candidateMaxJSONInteger || (record.Operation != "activate" && record.Operation != "revoke") || record.PublicKeyEncoding != exactSignatureEncoding || !validSHA256Digest(record.PublicKeyDigest) || !validCandidateTimestamp(record.EffectiveAt) || !validCandidateASCII(record.OperatorKeyID) || record.OperatorKeyEpoch > candidateMaxJSONInteger || record.OperatorSignatureAlgorithm != exactSignatureAlgorithm || record.OperatorSignatureEncoding != exactSignatureEncoding || record.RecordDigest != digestRecordWithoutFields(record, "operatorSignature", "recordDigest") {
			return QoderProbeTrustState{}, errors.New("qoder probe trust record is invalid")
		}
		effective, _ := time.Parse(time.RFC3339Nano, record.EffectiveAt)
		if effective.After(now) {
			return QoderProbeTrustState{}, errors.New("qoder probe trust record is future-dated")
		}
		if i == 0 {
			if record.PreviousRecordDigest != nil {
				return QoderProbeTrustState{}, errors.New("qoder probe trust genesis is invalid")
			}
		} else if record.PreviousRecordDigest == nil || *record.PreviousRecordDigest != previous {
			return QoderProbeTrustState{}, errors.New("qoder probe trust chain is invalid")
		}
		operator, ok := operators[record.OperatorKeyID]
		if !ok || operator.KeyEpoch != record.OperatorKeyEpoch {
			return QoderProbeTrustState{}, errors.New("qoder probe trust operator is inactive")
		}
		sig, err := decodeCandidateRawURL(record.OperatorSignature)
		if err != nil || len(sig) != ed25519.SignatureSize || !ed25519.Verify(operator.PublicKey, []byte(probeTrustSigningDomain+record.RecordDigest), sig) {
			return QoderProbeTrustState{}, errors.New("qoder probe trust signature is not trusted")
		}
		pub, err := decodeCandidateRawURL(record.Ed25519PublicKey)
		if err != nil || len(pub) != ed25519.PublicKeySize || digestBytes(pub) != record.PublicKeyDigest {
			return QoderProbeTrustState{}, errors.New("qoder probe trust public key is invalid")
		}
		if active[record.Role] == nil {
			active[record.Role] = map[string]QoderOSTrustKeyIdentity{}
		}
		if record.Operation == "activate" {
			expected := uint64(0)
			if started[record.Role] {
				if lastEpoch[record.Role] == candidateMaxJSONInteger {
					return QoderProbeTrustState{}, errors.New("qoder probe trust epoch exhausted")
				}
				expected = lastEpoch[record.Role] + 1
			}
			if record.KeyEpoch != expected || seenID[record.KeyID] || seenDigest[record.PublicKeyDigest] {
				return QoderProbeTrustState{}, errors.New("qoder probe trust activation reuses identity or skips epoch")
			}
			if i < 3 && (record.Operation != "activate" || record.KeyEpoch != 0 || record.Role != []string{"receipt", "verifier", "evidence"}[i]) {
				return QoderProbeTrustState{}, errors.New("qoder probe trust bootstrap order is invalid")
			}
			key := QoderOSTrustKeyIdentity{Role: record.Role, KeyID: record.KeyID, KeyEpoch: record.KeyEpoch, PublicKeyDigest: record.PublicKeyDigest, PublicKey: append(ed25519.PublicKey(nil), pub...)}
			active[record.Role][record.KeyID] = key
			seenID[record.KeyID] = true
			seenDigest[record.PublicKeyDigest] = true
			lastEpoch[record.Role] = record.KeyEpoch
			started[record.Role] = true
		} else {
			target, ok := active[record.Role][record.KeyID]
			if !ok || target.KeyEpoch != record.KeyEpoch || target.PublicKeyDigest != record.PublicKeyDigest || !bytes.Equal(target.PublicKey, pub) || len(active[record.Role]) <= 1 {
				return QoderProbeTrustState{}, errors.New("qoder probe trust revocation is invalid")
			}
			delete(active[record.Role], record.KeyID)
		}
		previous = record.RecordDigest
	}
	result := map[string][]QoderOSTrustKeyIdentity{}
	for role, keys := range active {
		for _, key := range keys {
			result[role] = append(result[role], key)
		}
		sort.Slice(result[role], func(i, j int) bool { return result[role][i].KeyEpoch < result[role][j].KeyEpoch })
	}
	return QoderProbeTrustState{TailDigest: previous, LedgerEpoch: uint64(len(records) - 1), ActiveKeys: result}, nil
}

func ValidateExactAuthorityBinding(config QoderAuthorityConfigExact, evidence QoderConformanceEvidenceExact, receiptTrust, verifierTrust, evidenceTrust QoderProbeTrustState, osTrust QoderOSTrustLedgerState, host HostAttestationIdentity, now time.Time) error {
	if config.APIVersion != exactAuthorityAPIVersion || config.Kind != "QoderAuthorityConfig" || config.SchemaVersion != 1 || !validCandidateASCII(config.RepositoryIdentity) || config.AuthorityGeneration == 0 || config.AuthorityGeneration > candidateMaxJSONInteger || config.ConfigDigest != digestRecordWithoutFields(config, "configDigest") || !validSHA256Digest(config.HostIdentityDigest) || !validSHA256Digest(config.CurrentEvidenceDigest) || !validSHA256Digest(config.ProbeArtifactDigest) || !validSHA256Digest(config.ProbeRunChallengeDigest) || !validSHA256Digest(config.TrustPolicyDigest) || !validSHA256Digest(config.ReceiptTrustLedgerTailDigest) || !validSHA256Digest(config.VerifierTrustLedgerTailDigest) || !validSHA256Digest(config.EvidenceTrustLedgerTailDigest) || !validSHA256Digest(config.OSTrustRootLedgerTailDigest) || !validCandidateASCII(config.ConsumerFenceProviderIdentity) || !validCandidateSortedDigests(config.RevokedEvidenceDigests, 0, 4096) || !validExactRootIdentity(config.EvidenceRootIdentity) {
		return errors.New("qoder exact authority config is invalid")
	}
	if !validCurrentTrustState(receiptTrust, "receipt") || !validCurrentTrustState(verifierTrust, "verifier") || !validCurrentTrustState(evidenceTrust, "evidence") || !validSHA256Digest(osTrust.RootRecordDigest) || host.RecordDigest != digestRecordWithoutFields(host, "signature", "recordDigest") || config.CurrentEvidenceDigest != evidence.EvidenceDigest || containsDigest(config.RevokedEvidenceDigests, evidence.EvidenceDigest) || config.HostIdentityDigest != host.RecordDigest || config.AuthorityGeneration != evidence.AuthorityGeneration || config.ProbeArtifactDigest != evidence.ProbeArtifactDigest || config.ProbeRunChallengeDigest != evidence.ProbeRunChallengeDigest || config.ReceiptTrustLedgerTailDigest != receiptTrust.TailDigest || config.VerifierTrustLedgerTailDigest != verifierTrust.TailDigest || config.EvidenceTrustLedgerTailDigest != evidenceTrust.TailDigest || config.OSTrustRootLedgerTailDigest != osTrust.RootRecordDigest || evidence.ReceiptTrustLedgerTailDigest != receiptTrust.TailDigest || evidence.VerifierTrustLedgerTailDigest != verifierTrust.TailDigest || evidence.EvidenceTrustLedgerTailDigest != evidenceTrust.TailDigest || evidence.OSTrustRootLedgerTailDigest != osTrust.RootRecordDigest || evidence.HostIdentity.RecordDigest != host.RecordDigest || evidence.HostIdentity.RecordDigest != digestRecordWithoutFields(evidence.HostIdentity, "signature", "recordDigest") {
		return errors.New("qoder exact authority binding differs from current trust state")
	}
	if evidence.APIVersion != exactAuthorityAPIVersion || evidence.Kind != "QoderConformanceEvidence" || evidence.SchemaVersion != 1 || evidence.EvidenceDigest != digestRecordWithoutFields(evidence, "signature", "evidenceDigest") || evidence.SignatureAlgorithm != exactSignatureAlgorithm || evidence.SignatureEncoding != exactSignatureEncoding || !validCandidateASCII(evidence.EvidenceAuthorityKeyID) || evidence.EvidenceAuthorityKeyEpoch > candidateMaxJSONInteger || !validCandidateASCII(evidence.ProbeRunID) || !validCandidateASCII(evidence.RunnerID) || !validCandidateASCII(evidence.RunnerVersion) || evidence.AdapterVersion != adapterVersion || evidence.SuiteDigest != expectedProbeSuiteDigest() || evidence.CapabilitiesDigest != expectedCapabilitiesDigest() || evidence.ProfileDigest != expectedProbeProfileDigest() || evidence.ToolPolicyDigest != expectedProbeToolPolicyDigest() || evidence.EventContract != conformanceEventContract || evidence.ProtocolVersion != qoderProtocolVersion || evidence.PermissionMode != qoderPermissionMode || !validSHA256Digest(evidence.ObservationDigest) || !validSHA256Digest(evidence.ProbeArtifactDigest) || !validSHA256Digest(evidence.ProbeRunChallengeDigest) || !validSHA256Digest(evidence.TranscriptDigest) || !validSHA256Digest(evidence.AggregateReceiptDigest) || !evidence.CredentialVerified || !evidence.LiveProtocolVerified || !evidence.WorkspaceWriteVerified || len(evidence.ReceiptDigests) != 4 || len(evidence.VariantInvocationManifests) != 4 || !validExactExecutableIdentity(evidence.CandidateExecutableIdentity) {
		return errors.New("qoder exact conformance evidence is invalid")
	}
	for index, receiptDigest := range evidence.ReceiptDigests {
		manifest := evidence.VariantInvocationManifests[index]
		if !validSHA256Digest(receiptDigest) || manifest.ReceiptSequence != uint64(index+1) || manifest.VariantID != candidateVariantID(index) || manifest.ManifestDigest != digestRecordWithoutFields(manifest, "manifestDigest") || manifest.ArgvManifest.ManifestDigest != digestRecordWithoutFields(manifest.ArgvManifest, "manifestDigest") || manifest.EnvironmentManifest.ManifestDigest != digestRecordWithoutFields(manifest.EnvironmentManifest, "manifestDigest") {
			return errors.New("qoder exact conformance evidence manifest is invalid")
		}
	}
	observed, oerr := time.Parse(time.RFC3339Nano, evidence.ObservedAt)
	validUntil, verr := time.Parse(time.RFC3339Nano, evidence.ValidUntil)
	if oerr != nil || verr != nil || !validCandidateTimestamp(evidence.ObservedAt) || !validCandidateTimestamp(evidence.ValidUntil) || observed.After(now) || !now.Before(validUntil) || validUntil.Sub(observed) > maxConformanceValidity {
		return errors.New("qoder exact conformance evidence is stale")
	}
	key, ok := findTrustKey(evidenceTrust.ActiveKeys["evidence"], evidence.EvidenceAuthorityKeyID, evidence.EvidenceAuthorityKeyEpoch)
	sig, err := decodeCandidateRawURL(evidence.Signature)
	if !ok || len(key.PublicKey) != ed25519.PublicKeySize || digestBytes(key.PublicKey) != key.PublicKeyDigest || err != nil || len(sig) != ed25519.SignatureSize || !ed25519.Verify(key.PublicKey, []byte(exactEvidenceSigningDomain+evidence.EvidenceDigest), sig) {
		return errors.New("qoder exact conformance evidence signature is not trusted")
	}
	return nil
}

func validExactRootIdentity(identity CandidateRootIdentity) bool {
	return identity.Device <= candidateMaxJSONInteger && identity.Inode <= candidateMaxJSONInteger && identity == candidateRootIdentity(CandidateObjectIdentity{Device: identity.Device, Inode: identity.Inode})
}

func validExactExecutableIdentity(identity CandidateExecutableReceiptIdentity) bool {
	pathBytes, err := decodeCandidateRawURL(identity.RealpathBytes.Bytes)
	return err == nil && identity.RealpathBytes.Encoding == exactSignatureEncoding && len(pathBytes) > 0 && len(pathBytes) <= 4096 && pathBytes[0] == '/' && bytes.IndexByte(pathBytes, 0) < 0 && filepath.Clean(string(pathBytes)) == string(pathBytes) && string(pathBytes) != "/" && identity.RealpathBytes.Digest == digestBytes(pathBytes) && identity.Device <= candidateMaxJSONInteger && identity.Inode <= candidateMaxJSONInteger && validSHA256Digest(identity.Digest) && validCandidateASCII(identity.BinaryVersion) && isSupportedBinaryVersion(identity.BinaryVersion)
}

func validCurrentTrustState(state QoderProbeTrustState, role string) bool {
	if !validSHA256Digest(state.TailDigest) || state.LedgerEpoch > candidateMaxJSONInteger || len(state.ActiveKeys[role]) == 0 {
		return false
	}
	seenID, seenDigest := map[string]bool{}, map[string]bool{}
	for _, key := range state.ActiveKeys[role] {
		if key.Role != role || !validCandidateASCII(key.KeyID) || key.KeyEpoch > candidateMaxJSONInteger || !validSHA256Digest(key.PublicKeyDigest) || len(key.PublicKey) != ed25519.PublicKeySize || digestBytes(key.PublicKey) != key.PublicKeyDigest || seenID[key.KeyID] || seenDigest[key.PublicKeyDigest] {
			return false
		}
		seenID[key.KeyID], seenDigest[key.PublicKeyDigest] = true, true
	}
	return true
}

func validProbeTrustRole(role string) bool {
	return role == "receipt" || role == "verifier" || role == "evidence"
}
func containsDigest(values []string, target string) bool {
	i := sort.SearchStrings(values, target)
	return i < len(values) && values[i] == target
}
func findTrustKey(keys []QoderOSTrustKeyIdentity, id string, epoch uint64) (QoderOSTrustKeyIdentity, bool) {
	for _, key := range keys {
		if key.KeyID == id && key.KeyEpoch == epoch {
			return key, true
		}
	}
	return QoderOSTrustKeyIdentity{}, false
}
