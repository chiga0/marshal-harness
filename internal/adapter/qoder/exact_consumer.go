package qoder

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"golang.org/x/sys/unix"
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

// QoderExactAuthorityCurrent contains only current, held consumer inputs. The
// consumer replays both ledgers itself; callers cannot detach an active key
// set, host identity, or fence receipt from the records that authorized it.
type QoderExactAuthorityCurrent struct {
	OSTrustRecords             []QoderOSTrustRootRecord
	OSTrustReceipts            []QoderOSTrustAnchorReceipt
	OSAnchorProviderIdentity   string
	OSAnchorProviderKeyID      string
	OSAnchorProviderKeyEpoch   uint64
	OSAnchorProviderPublicKey  ed25519.PublicKey
	ProbeTrustRecords          []QoderProbeTrustKeyRecord
	HostIdentity               HostAttestationIdentity
	FenceRequest               ConsumerFenceAdvanceRequest
	FenceReceipt               ConsumerFenceReceipt
	CredentialProviderIdentity string
	Executable                 CandidateBoundObject
	ExecutableVersion          string
	EvidenceRoot               CandidateBoundObject
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

func ValidateExactAuthorityBinding(config QoderAuthorityConfigExact, evidence QoderConformanceEvidenceExact, current QoderExactAuthorityCurrent, now time.Time) error {
	osTrust, err := ReplayQoderOSTrustRootLedger(current.OSTrustRecords, current.OSTrustReceipts, current.OSAnchorProviderIdentity, current.OSAnchorProviderKeyID, current.OSAnchorProviderKeyEpoch, current.OSAnchorProviderPublicKey, now)
	if err != nil {
		return errors.New("qoder exact OS trust state is invalid")
	}
	probeTrust, err := ReplayQoderProbeTrustLedger(current.ProbeTrustRecords, osTrust.ActiveKeys["trust-ledger-operator"], now)
	if err != nil || !validCurrentTrustState(probeTrust, "receipt") || !validCurrentTrustState(probeTrust, "verifier") || !validCurrentTrustState(probeTrust, "evidence") {
		return errors.New("qoder exact probe trust state is invalid")
	}
	hostKey, ok := findTrustKey(osTrust.ActiveKeys["host-attestation-provider"], current.HostIdentity.ProviderKeyID, current.HostIdentity.ProviderKeyEpoch)
	if !ok || ValidateHostAttestationIdentity(current.HostIdentity, current.HostIdentity.ProviderIdentity, hostKey.KeyID, hostKey.KeyEpoch, hostKey.PublicKey, now) != nil {
		return errors.New("qoder exact host identity is not OS-root trusted")
	}
	fenceKey, ok := findTrustKey(osTrust.ActiveKeys["consumer-fence-provider"], current.FenceReceipt.ProviderKeyID, current.FenceReceipt.ProviderKeyEpoch)
	if !ok || current.FenceReceipt.ProviderIdentity != config.ConsumerFenceProviderIdentity || ValidateConsumerFenceReceipt(current.FenceReceipt, current.FenceRequest, config.ConsumerFenceProviderIdentity, fenceKey.KeyID, fenceKey.KeyEpoch, fenceKey.PublicKey, now) != nil || current.FenceRequest.RepositoryIdentity != config.RepositoryIdentity || current.FenceRequest.AuthorityGeneration != config.AuthorityGeneration || current.FenceRequest.ConfigDigest != config.ConfigDigest {
		return errors.New("qoder exact consumer fence is not OS-root trusted")
	}
	executableIdentity, err := currentExecutableIdentity(current.Executable, current.ExecutableVersion)
	if err != nil || executableIdentity != evidence.CandidateExecutableIdentity {
		return errors.New("qoder exact executable differs from the current held object")
	}
	if err := validateCurrentHeldRoot(current.EvidenceRoot, config.EvidenceRootIdentity); err != nil {
		return errors.New("qoder exact evidence root differs from the current held object")
	}
	if config.APIVersion != exactAuthorityAPIVersion || config.Kind != "QoderAuthorityConfig" || config.SchemaVersion != 1 || !validCandidateASCII(config.RepositoryIdentity) || config.AuthorityGeneration == 0 || config.AuthorityGeneration > candidateMaxJSONInteger || config.ConfigDigest != digestRecordWithoutFields(config, "configDigest") || !validSHA256Digest(config.HostIdentityDigest) || !validSHA256Digest(config.CurrentEvidenceDigest) || !validSHA256Digest(config.ProbeArtifactDigest) || !validSHA256Digest(config.ProbeRunChallengeDigest) || !validSHA256Digest(config.TrustPolicyDigest) || !validSHA256Digest(config.ReceiptTrustLedgerTailDigest) || !validSHA256Digest(config.VerifierTrustLedgerTailDigest) || !validSHA256Digest(config.EvidenceTrustLedgerTailDigest) || !validSHA256Digest(config.OSTrustRootLedgerTailDigest) || !validCandidateASCII(config.ConsumerFenceProviderIdentity) || !validCandidateSortedDigests(config.RevokedEvidenceDigests, 0, 4096) || !validExactRootIdentity(config.EvidenceRootIdentity) {
		return errors.New("qoder exact authority config is invalid")
	}
	if config.CurrentEvidenceDigest != evidence.EvidenceDigest || containsDigest(config.RevokedEvidenceDigests, evidence.EvidenceDigest) || config.HostIdentityDigest != current.HostIdentity.RecordDigest || config.AuthorityGeneration != evidence.AuthorityGeneration || config.ProbeArtifactDigest != evidence.ProbeArtifactDigest || config.ProbeRunChallengeDigest != evidence.ProbeRunChallengeDigest || config.ReceiptTrustLedgerTailDigest != probeTrust.TailDigest || config.VerifierTrustLedgerTailDigest != probeTrust.TailDigest || config.EvidenceTrustLedgerTailDigest != probeTrust.TailDigest || config.OSTrustRootLedgerTailDigest != osTrust.RootRecordDigest || evidence.ReceiptTrustLedgerTailDigest != probeTrust.TailDigest || evidence.VerifierTrustLedgerTailDigest != probeTrust.TailDigest || evidence.EvidenceTrustLedgerTailDigest != probeTrust.TailDigest || evidence.OSTrustRootLedgerTailDigest != osTrust.RootRecordDigest || evidence.HostIdentity.RecordDigest != current.HostIdentity.RecordDigest {
		return errors.New("qoder exact authority binding differs from current trust state")
	}
	if evidence.APIVersion != exactAuthorityAPIVersion || evidence.Kind != "QoderConformanceEvidence" || evidence.SchemaVersion != 1 || evidence.EvidenceDigest != digestRecordWithoutFields(evidence, "signature", "evidenceDigest") || evidence.SignatureAlgorithm != exactSignatureAlgorithm || evidence.SignatureEncoding != exactSignatureEncoding || !validCandidateASCII(evidence.EvidenceAuthorityKeyID) || evidence.EvidenceAuthorityKeyEpoch > candidateMaxJSONInteger || !validCandidateASCII(evidence.ProbeRunID) || !validCandidateASCII(evidence.RunnerID) || !validCandidateASCII(evidence.RunnerVersion) || evidence.AdapterVersion != adapterVersion || evidence.SuiteDigest != expectedProbeSuiteDigest() || evidence.CapabilitiesDigest != expectedCapabilitiesDigest() || evidence.ProfileDigest != expectedProbeProfileDigest() || evidence.ToolPolicyDigest != expectedProbeToolPolicyDigest() || evidence.EventContract != conformanceEventContract || evidence.ProtocolVersion != qoderProtocolVersion || evidence.PermissionMode != qoderPermissionMode || !validSHA256Digest(evidence.ObservationDigest) || !validSHA256Digest(evidence.ProbeArtifactDigest) || !validSHA256Digest(evidence.ProbeRunChallengeDigest) || !validSHA256Digest(evidence.TranscriptDigest) || !validSHA256Digest(evidence.AggregateReceiptDigest) || !evidence.CredentialVerified || !evidence.LiveProtocolVerified || !evidence.WorkspaceWriteVerified || len(evidence.ReceiptDigests) != 4 || len(evidence.VariantInvocationManifests) != 4 || !validExactExecutableIdentity(evidence.CandidateExecutableIdentity) {
		return errors.New("qoder exact conformance evidence is invalid")
	}
	var modelDigest string
	capabilityIDs := make(map[string]struct{}, len(evidence.VariantInvocationManifests))
	for index, receiptDigest := range evidence.ReceiptDigests {
		manifest := evidence.VariantInvocationManifests[index]
		if !validSHA256Digest(receiptDigest) || manifest.ReceiptSequence != uint64(index+1) || validateCandidateInvocationManifest(manifest, evidence.ProbeRunID, candidateVariantID(index)) != nil {
			return errors.New("qoder exact conformance evidence manifest is invalid")
		}
		capability, capabilityErr := credentialCapabilityFromManifest(manifest.EnvironmentManifest)
		credentialKey, credentialKeyOK := findTrustKey(osTrust.ActiveKeys["credential-capability-provider"], capability.ProviderKeyID, capability.ProviderKeyEpoch)
		issuedAt, issuedErr := time.Parse(time.RFC3339Nano, capability.IssuedAt)
		expiresAt, expiresErr := time.Parse(time.RFC3339Nano, capability.ExpiresAt)
		observedAt, observedErr := time.Parse(time.RFC3339Nano, evidence.ObservedAt)
		if capabilityErr != nil || !credentialKeyOK || !validCandidateASCII(current.CredentialProviderIdentity) || capability.ProviderIdentity != current.CredentialProviderIdentity || capability.CapabilityClass != "qoder-live-probe" || capability.PolicyScopeDigest != config.TrustPolicyDigest || issuedErr != nil || expiresErr != nil || observedErr != nil || issuedAt.After(observedAt) || !observedAt.Before(expiresAt) || verifyCandidateCredentialCapability(capability, candidateAuthorityPolicy{credentialProviderKeyID: credentialKey.KeyID, credentialProviderKeyEpoch: credentialKey.KeyEpoch, credentialProviderPublicKey: credentialKey.PublicKey}) != nil {
			return errors.New("qoder exact credential capability is not OS-root trusted")
		}
		if _, duplicate := capabilityIDs[capability.CapabilityID]; capability.CapabilityID == "" || duplicate {
			return errors.New("qoder exact credential capability identity is empty or replayed across variants")
		}
		capabilityIDs[capability.CapabilityID] = struct{}{}
		expected, nextModelDigest, expectedErr := exactExpectedInvocationManifest(index, evidence.ProbeRunID, manifest, capability, modelDigest)
		if expectedErr != nil || !candidateManifestsEqual(manifest, expected) {
			return errors.New("qoder exact invocation manifest differs from the frozen matrix")
		}
		modelDigest = nextModelDigest
	}
	observed, oerr := time.Parse(time.RFC3339Nano, evidence.ObservedAt)
	validUntil, verr := time.Parse(time.RFC3339Nano, evidence.ValidUntil)
	if oerr != nil || verr != nil || !validCandidateTimestamp(evidence.ObservedAt) || !validCandidateTimestamp(evidence.ValidUntil) || observed.After(now) || !now.Before(validUntil) || validUntil.Sub(observed) > maxConformanceValidity {
		return errors.New("qoder exact conformance evidence is stale")
	}
	key, ok := findTrustKey(probeTrust.ActiveKeys["evidence"], evidence.EvidenceAuthorityKeyID, evidence.EvidenceAuthorityKeyEpoch)
	sig, err := decodeCandidateRawURL(evidence.Signature)
	if !ok || len(key.PublicKey) != ed25519.PublicKeySize || digestBytes(key.PublicKey) != key.PublicKeyDigest || err != nil || len(sig) != ed25519.SignatureSize || !ed25519.Verify(key.PublicKey, []byte(exactEvidenceSigningDomain+evidence.EvidenceDigest), sig) {
		return errors.New("qoder exact conformance evidence signature is not trusted")
	}
	return nil
}

func exactExpectedInvocationManifest(index int, probeRunID string, observed CandidateVariantInvocationManifest, capability CandidateCredentialCapabilityIdentity, sharedModelDigest string) (CandidateVariantInvocationManifest, string, error) {
	variants := candidateProbeVariants("$MODEL")
	if index < 0 || index >= len(variants) {
		return CandidateVariantInvocationManifest{}, sharedModelDigest, errors.New("qoder exact invocation variant is invalid")
	}
	variant := variants[index]
	invocation := CandidateProbeInvocation{ProbeRunID: probeRunID, ReceiptSequence: index + 1, VariantIndex: index, Arguments: buildArgs(variant.model, candidateBoundCredentialToken, candidateBoundScratchToken, variant.disableAllTools), Environment: candidateProbeEnvironment(candidateBoundScratchToken, candidateBoundCredentialToken), ExpectedModel: variant.model}
	expected, err := candidateInvocationManifest(invocation, capability)
	if err != nil {
		return CandidateVariantInvocationManifest{}, sharedModelDigest, err
	}
	if variant.model == "" {
		return expected, sharedModelDigest, nil
	}
	var observedModelDigest string
	for _, entry := range observed.ArgvManifest.Entries {
		if entry.Source == "model-id" {
			if observedModelDigest != "" {
				return CandidateVariantInvocationManifest{}, sharedModelDigest, errors.New("qoder exact invocation has duplicate model identity")
			}
			observedModelDigest = entry.ValueDigest
		}
	}
	if !validSHA256Digest(observedModelDigest) || observedModelDigest == digestBytes(nil) || sharedModelDigest != "" && sharedModelDigest != observedModelDigest {
		return CandidateVariantInvocationManifest{}, sharedModelDigest, errors.New("qoder exact invocation model identity differs")
	}
	for entryIndex := range expected.ArgvManifest.Entries {
		if expected.ArgvManifest.Entries[entryIndex].Source == "model-id" {
			expected.ArgvManifest.Entries[entryIndex].ValueDigest = observedModelDigest
		}
	}
	recomputeExactCandidateManifestDigests(&expected)
	return expected, observedModelDigest, nil
}

func recomputeExactCandidateManifestDigests(manifest *CandidateVariantInvocationManifest) {
	manifest.ArgvManifest.ManifestDigest = digestRecordWithoutFields(manifest.ArgvManifest, "manifestDigest")
	manifest.EnvironmentManifest.ManifestDigest = digestRecordWithoutFields(manifest.EnvironmentManifest, "manifestDigest")
	manifest.ManifestDigest = digestRecordWithoutFields(*manifest, "manifestDigest")
}

func currentExecutableIdentity(object CandidateBoundObject, version string) (CandidateExecutableReceiptIdentity, error) {
	var stat unix.Stat_t
	if object.File == nil || unix.Fstat(int(object.File.Fd()), &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0o111 == 0 || (int(stat.Uid) != os.Geteuid() && stat.Uid != 0) || stat.Size <= 0 || stat.Size > 128<<20 || verifyBoundObjectIdentity(object) != nil || !validCandidateASCII(version) || !isSupportedBinaryVersion(version) || !filepath.IsAbs(object.CanonicalPath) || filepath.Clean(object.CanonicalPath) != object.CanonicalPath || !validSHA256Digest(object.Digest) || !heldPathStillNamesObject(object) {
		return CandidateExecutableReceiptIdentity{}, errors.New("qoder current held executable is invalid")
	}
	position, err := object.File.Seek(0, io.SeekCurrent)
	if err != nil {
		return CandidateExecutableReceiptIdentity{}, err
	}
	if _, err := object.File.Seek(0, io.SeekStart); err != nil {
		return CandidateExecutableReceiptIdentity{}, err
	}
	hash := sha256.New()
	copied, copyErr := io.Copy(hash, io.LimitReader(object.File, stat.Size+1))
	_, restoreErr := object.File.Seek(position, io.SeekStart)
	if copyErr != nil || copied != stat.Size || restoreErr != nil || "sha256:"+hex.EncodeToString(hash.Sum(nil)) != object.Digest {
		return CandidateExecutableReceiptIdentity{}, errors.New("qoder current held executable digest is invalid")
	}
	return candidateExecutableReceiptIdentity(object, version), nil
}

func validateCurrentHeldRoot(object CandidateBoundObject, expected CandidateRootIdentity) error {
	var stat unix.Stat_t
	if object.File == nil || unix.Fstat(int(object.File.Fd()), &stat) != nil || !privateDirectory(stat, os.Geteuid()) || verifyBoundObjectIdentity(object) != nil || !filepath.IsAbs(object.CanonicalPath) || filepath.Clean(object.CanonicalPath) != object.CanonicalPath || !heldPathStillNamesObject(object) || candidateRootIdentity(object.Identity) != expected {
		return errors.New("qoder current held root is invalid")
	}
	info, err := object.File.Stat()
	if err != nil || !info.IsDir() {
		return errors.New("qoder current held root is not a directory")
	}
	return nil
}

func heldPathStillNamesObject(object CandidateBoundObject) bool {
	if object.File == nil {
		return false
	}
	held, err := object.File.Stat()
	if err != nil {
		return false
	}
	named, err := os.Lstat(object.CanonicalPath)
	return err == nil && named.Mode()&os.ModeSymlink == 0 && os.SameFile(held, named)
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
