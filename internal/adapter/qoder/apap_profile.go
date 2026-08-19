package qoder

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/chiga0/marshal-harness/internal/authorityprovider"
	"github.com/chiga0/marshal-harness/internal/canonical"
)

const (
	qoderAPAPResponseDomain = "marshal-qoder-apap-response-v1\x00"
	qoderAPAPResponseUsage  = "qoder-apap-response"
)

// QoderAPAPAuthority is verifier-owned current state. It contains public
// verification material and held objects only; it is not an activation or
// registry record.
type QoderAPAPAuthority struct {
	ProviderInstanceID string
	ProviderSequence   uint64
	Peer               authorityprovider.PeerIdentity
	ResponseKeys       authorityprovider.KeyResolver
	Config             QoderAuthorityConfigExact
	Evidence           QoderConformanceEvidenceExact
	Current            QoderExactAuthorityCurrent
}

type QoderAPAPSignedResponse struct {
	Document  []byte
	Signature authorityprovider.SignedObjectEnvelopeV1
}

// qoderAPAPResponseBinding is the object signed by the response authority.
// The shared APAP response envelope identifies command fields, while this
// outer bridge envelope binds it to the exact request instance issued by this
// bridge (including nonce and time bounds).
type qoderAPAPResponseBinding struct {
	RequestEnvelopeDigest string          `json:"requestEnvelopeDigest"`
	Response              json.RawMessage `json:"response"`
}

// SealQoderAPAPResponseBinding produces the exact document that a Qoder APAP
// response authority must sign. It carries no secret material.
func SealQoderAPAPResponseBinding(requestEnvelopeDigest string, response []byte) ([]byte, error) {
	if !validSHA256Digest(requestEnvelopeDigest) {
		return nil, errors.New("qoder APAP response request binding is invalid")
	}
	canonicalResponse, err := canonical.JSON(response)
	if err != nil {
		return nil, errors.New("qoder APAP response envelope is invalid")
	}
	return canonical.JSON(mustCandidateJSON(qoderAPAPResponseBinding{RequestEnvelopeDigest: requestEnvelopeDigest, Response: canonicalResponse}))
}

func decodeQoderAPAPResponseBinding(document []byte) (qoderAPAPResponseBinding, error) {
	var binding qoderAPAPResponseBinding
	canonicalDocument, err := canonical.JSON(document)
	if err != nil || !bytes.Equal(canonicalDocument, document) {
		return binding, errors.New("qoder APAP signed response is not canonical")
	}
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&binding); err != nil {
		return binding, errors.New("qoder APAP signed response shape is invalid")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) || !validSHA256Digest(binding.RequestEnvelopeDigest) || len(binding.Response) == 0 {
		return binding, errors.New("qoder APAP signed response shape is invalid")
	}
	return binding, nil
}

type QoderAPAPProbeSession struct {
	ProviderInstanceID                      string
	ProviderSequence                        uint64
	PeerPrincipalDigest                     string
	AuthorityProfile                        authorityprovider.AuthorityProfile
	ProbeSessionID                          string
	TargetIsolationIdentityDigest           string
	CredentialIngressEndpointIdentityDigest string
	ScratchRootIdentities                   []CandidateRootIdentity
	CredentialRootIdentity                  CandidateRootIdentity
	BusinessRootIdentities                  []CandidateRootIdentity
	VariantTopologyDigests                  []string
	CandidateIdentity                       CandidateExecutableReceiptIdentity
	CandidateIdentityDigest                 string
	EvidenceDigest                          string
	AuthorityGeneration                     uint64
	HostIdentityDigest                      string
	ProbeArtifactDigest                     string
	ChallengeDigest                         string
	RequestEnvelopeDigest                   string
	ResponseEnvelopeDigest                  string
	IssuedAt                                time.Time
	ExpiresAt                               time.Time
}

// QoderAPAPHeldProbeBinding is verifier-owned input measured from held
// objects. Endpoint identities and ADR 0034 root/topology identities remain
// separate digest domains and are never inferred from each other.
type QoderAPAPHeldProbeBinding struct {
	ScratchRootIdentities                   []CandidateRootIdentity
	CredentialRootIdentity                  CandidateRootIdentity
	BusinessRootIdentities                  []CandidateRootIdentity
	VariantTopologyDigests                  []string
	TargetIsolationIdentityDigest           string
	CredentialIngressEndpointIdentityDigest string
}

func validateQoderAPAPHeldProbeBinding(binding QoderAPAPHeldProbeBinding) error {
	if len(binding.ScratchRootIdentities) != 4 || len(binding.VariantTopologyDigests) != 4 || len(binding.BusinessRootIdentities) == 0 || len(binding.BusinessRootIdentities) > 256 || !validExactRootIdentity(binding.CredentialRootIdentity) || !validSHA256Digest(binding.TargetIsolationIdentityDigest) || !validSHA256Digest(binding.CredentialIngressEndpointIdentityDigest) || binding.TargetIsolationIdentityDigest == binding.CredentialIngressEndpointIdentityDigest || binding.TargetIsolationIdentityDigest == binding.CredentialRootIdentity.IdentityDigest || binding.CredentialIngressEndpointIdentityDigest == binding.CredentialRootIdentity.IdentityDigest {
		return errors.New("qoder APAP held probe binding is invalid")
	}
	seenRoots := map[CandidateRootIdentity]struct{}{binding.CredentialRootIdentity: {}}
	seenTopology := map[string]struct{}{}
	for index, root := range binding.ScratchRootIdentities {
		if !validExactRootIdentity(root) || root.IdentityDigest == binding.TargetIsolationIdentityDigest || root.IdentityDigest == binding.CredentialIngressEndpointIdentityDigest {
			return errors.New("qoder APAP held scratch binding is invalid")
		}
		if _, duplicate := seenRoots[root]; duplicate {
			return errors.New("qoder APAP held scratch binding is replayed")
		}
		seenRoots[root] = struct{}{}
		topology := binding.VariantTopologyDigests[index]
		if !validSHA256Digest(topology) || topology == binding.TargetIsolationIdentityDigest || topology == binding.CredentialIngressEndpointIdentityDigest {
			return errors.New("qoder APAP held topology binding is invalid")
		}
		if _, duplicate := seenTopology[topology]; duplicate {
			return errors.New("qoder APAP held topology binding is replayed")
		}
		seenTopology[topology] = struct{}{}
	}
	for index, root := range binding.BusinessRootIdentities {
		if !validExactRootIdentity(root) || root == binding.CredentialRootIdentity || root.IdentityDigest == binding.TargetIsolationIdentityDigest || root.IdentityDigest == binding.CredentialIngressEndpointIdentityDigest {
			return errors.New("qoder APAP held business-root binding is invalid")
		}
		if _, duplicate := seenRoots[root]; duplicate {
			return errors.New("qoder APAP held root roles alias")
		}
		seenRoots[root] = struct{}{}
		if index > 0 {
			left, _ := canonical.JSON(mustCandidateJSON(binding.BusinessRootIdentities[index-1]))
			right, _ := canonical.JSON(mustCandidateJSON(root))
			if bytes.Compare(left, right) >= 0 {
				return errors.New("qoder APAP held business-root binding is not canonical")
			}
		}
	}
	return nil
}

func cloneQoderAPAPHeldProbeBinding(binding QoderAPAPHeldProbeBinding) QoderAPAPHeldProbeBinding {
	binding.ScratchRootIdentities = append([]CandidateRootIdentity(nil), binding.ScratchRootIdentities...)
	binding.BusinessRootIdentities = append([]CandidateRootIdentity(nil), binding.BusinessRootIdentities...)
	binding.VariantTopologyDigests = append([]string(nil), binding.VariantTopologyDigests...)
	return binding
}

type QoderAPAPReceiptBinding struct {
	Session       QoderAPAPProbeSession
	Receipt       CandidateExecutionReceipt
	ReceiptDigest string
}

func validateQoderAPAPAuthority(authority QoderAPAPAuthority, now time.Time) (CandidateExecutableReceiptIdentity, QoderProbeTrustState, error) {
	if now.IsZero() || !validCandidateASCII(authority.ProviderInstanceID) || authority.ProviderSequence > candidateMaxJSONInteger || authority.Peer.Role != authorityprovider.PrincipalVerifierController || !validSHA256Digest(authority.Peer.PrincipalDigest) || authority.ResponseKeys == nil || authority.Current.ExecutableVersion != supportedBinary {
		return CandidateExecutableReceiptIdentity{}, QoderProbeTrustState{}, errors.New("qoder APAP authority identity is invalid")
	}
	if err := ValidateExactAuthorityBinding(authority.Config, authority.Evidence, authority.Current, now); err != nil {
		return CandidateExecutableReceiptIdentity{}, QoderProbeTrustState{}, errors.New("qoder APAP exact authority is invalid")
	}
	identity, err := currentExecutableIdentity(authority.Current.Executable, authority.Current.ExecutableVersion)
	if err != nil || identity.BinaryVersion != supportedBinary || identity != authority.Evidence.CandidateExecutableIdentity || authority.Config.AuthorityGeneration != authority.Evidence.AuthorityGeneration || authority.Config.CurrentEvidenceDigest != authority.Evidence.EvidenceDigest || containsDigest(authority.Config.RevokedEvidenceDigests, authority.Evidence.EvidenceDigest) {
		return CandidateExecutableReceiptIdentity{}, QoderProbeTrustState{}, errors.New("qoder APAP current executable or evidence is invalid")
	}
	osTrust, err := ReplayQoderOSTrustRootLedger(authority.Current.OSTrustRecords, authority.Current.OSTrustReceipts, authority.Current.OSAnchorProviderIdentity, authority.Current.OSAnchorProviderKeyID, authority.Current.OSAnchorProviderKeyEpoch, authority.Current.OSAnchorProviderPublicKey, now)
	if err != nil {
		return CandidateExecutableReceiptIdentity{}, QoderProbeTrustState{}, errors.New("qoder APAP OS trust is invalid")
	}
	probeTrust, err := ReplayQoderProbeTrustLedger(authority.Current.ProbeTrustRecords, osTrust.ActiveKeys["trust-ledger-operator"], now)
	if err != nil || !validCurrentTrustState(probeTrust, "receipt") {
		return CandidateExecutableReceiptIdentity{}, QoderProbeTrustState{}, errors.New("qoder APAP receipt trust is invalid")
	}
	return identity, probeTrust, nil
}

func bindQoderAPAPReceipt(session QoderAPAPProbeSession, document []byte, trust QoderProbeTrustState, now time.Time) (QoderAPAPReceiptBinding, error) {
	receipt, err := decodeCandidateExecutionReceipt(document)
	if err != nil || validateCandidateExactReceipt(receipt) != nil {
		return QoderAPAPReceiptBinding{}, errors.New("qoder APAP receipt is not an exact ADR 0034 object")
	}
	digest, err := receipt.digest()
	index := int(receipt.ReceiptSequence) - 1
	capability, capabilityErr := credentialCapabilityFromManifest(receipt.InvocationManifest.EnvironmentManifest)
	expectedManifest, _, manifestErr := exactExpectedInvocationManifest(index, session.ProbeSessionID, receipt.InvocationManifest, capability, "")
	if err != nil || digest != receipt.RecordDigest || receipt.ProbeRunID != session.ProbeSessionID || receipt.ReceiptSequence < 1 || receipt.ReceiptSequence > 4 || index >= len(session.ScratchRootIdentities) || index >= len(session.VariantTopologyDigests) || receipt.VariantID != candidateVariantID(index) || receipt.VariantChallengeDigest != candidateVariantChallenge(session.ChallengeDigest, index) || receipt.CandidateExecutableIdentity != session.CandidateIdentity || receipt.CandidateExecutableIdentity.BinaryVersion != supportedBinary || receipt.ProbeRunChallengeDigest != session.ChallengeDigest || receipt.HostIdentityDigest != session.HostIdentityDigest || receipt.ScratchRootIdentity != session.ScratchRootIdentities[index] || receipt.CredentialRootIdentity != session.CredentialRootIdentity || !equalCandidateRootIdentities(receipt.BusinessRootIdentities, session.BusinessRootIdentities) || receipt.TopologyDigest != session.VariantTopologyDigests[index] {
		return QoderAPAPReceiptBinding{}, errors.New("qoder APAP receipt binding is invalid")
	}
	if capabilityErr != nil || manifestErr != nil || !candidateManifestsEqual(receipt.InvocationManifest, expectedManifest) {
		return QoderAPAPReceiptBinding{}, errors.New("qoder APAP receipt invocation manifest is invalid")
	}
	if receipt.IsolationProfileDigest != candidateObservedProfileDigest() || receipt.ProtocolVersion != qoderProtocolVersion || receipt.PermissionMode != qoderPermissionMode || receipt.EventContract != conformanceEventContract || receipt.WorkerResultTransportDigest != expectedWorkerResultTransportDigest() {
		return QoderAPAPReceiptBinding{}, errors.New("qoder APAP receipt runtime contract is invalid")
	}
	started, startErr := time.Parse(time.RFC3339Nano, receipt.StartedAt)
	completed, err := time.Parse(time.RFC3339Nano, receipt.CompletedAt)
	if startErr != nil || err != nil || started.Before(session.IssuedAt) || completed.Before(started) || completed.Sub(started) > candidateReceiptMaxExecution || completed.After(now) || completed.After(session.ExpiresAt) {
		return QoderAPAPReceiptBinding{}, errors.New("qoder APAP receipt is expired or future-dated")
	}
	key, ok := qoderAPAPReceiptKey(trust.ActiveKeys["receipt"], receipt.ReceiptAuthorityKeyID, receipt.ReceiptAuthorityKeyEpoch)
	if !ok {
		return QoderAPAPReceiptBinding{}, errors.New("qoder APAP receipt authority is revoked or foreign")
	}
	signature, err := decodeCandidateRawURL(receipt.Signature)
	message, messageErr := receipt.signingBytes()
	if err != nil || messageErr != nil || len(signature) != ed25519.SignatureSize || !ed25519.Verify(key.PublicKey, message, signature) {
		return QoderAPAPReceiptBinding{}, errors.New("qoder APAP receipt signature is not trusted")
	}
	return QoderAPAPReceiptBinding{Session: session, Receipt: receipt, ReceiptDigest: receipt.RecordDigest}, nil
}

func qoderAPAPReceiptKey(keys []QoderOSTrustKeyIdentity, id string, epoch uint64) (QoderOSTrustKeyIdentity, bool) {
	for _, key := range keys {
		if key.KeyID == id && key.KeyEpoch == epoch {
			return key, true
		}
	}
	return QoderOSTrustKeyIdentity{}, false
}

func qoderAPAPCandidateIdentityDigest(identity CandidateExecutableReceiptIdentity) string {
	return digestRecordWithoutFields(identity)
}
