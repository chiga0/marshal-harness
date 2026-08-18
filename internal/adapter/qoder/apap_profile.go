package qoder

import (
	"crypto/ed25519"
	"errors"
	"time"

	"github.com/chiga0/marshal-harness/internal/authorityprovider"
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

type QoderAPAPProbeSession struct {
	ProviderInstanceID                      string
	ProviderSequence                        uint64
	PeerPrincipalDigest                     string
	AuthorityProfile                        authorityprovider.AuthorityProfile
	ProbeSessionID                          string
	TargetIsolationIdentityDigest           string
	CredentialIngressEndpointIdentityDigest string
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
	if err != nil || digest != receipt.RecordDigest || receipt.ProbeRunID != session.ProbeSessionID || receipt.CandidateExecutableIdentity != session.CandidateIdentity || receipt.CandidateExecutableIdentity.BinaryVersion != supportedBinary || receipt.ProbeRunChallengeDigest != session.ChallengeDigest || receipt.HostIdentityDigest != session.HostIdentityDigest {
		return QoderAPAPReceiptBinding{}, errors.New("qoder APAP receipt binding is invalid")
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
