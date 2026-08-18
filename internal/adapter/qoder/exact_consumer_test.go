package qoder

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"
	"time"
)

type exactProbeTrustFixture struct {
	now             time.Time
	operator        QoderOSTrustKeyIdentity
	operatorPrivate ed25519.PrivateKey
	keys            map[string]ed25519.PrivateKey
	records         []QoderProbeTrustKeyRecord
}

func newExactProbeTrustFixture(t *testing.T) *exactProbeTrustFixture {
	t.Helper()
	operatorPublic, operatorPrivate, _ := ed25519.GenerateKey(rand.Reader)
	f := &exactProbeTrustFixture{now: time.Now().UTC(), operator: QoderOSTrustKeyIdentity{Role: "trust-ledger-operator", KeyID: "operator-0", PublicKeyDigest: digestBytes(operatorPublic), PublicKey: operatorPublic}, operatorPrivate: operatorPrivate, keys: map[string]ed25519.PrivateKey{}}
	f.activate(t, "receipt", "receipt-0", 0)
	f.activate(t, "verifier", "verifier-0", 0)
	f.activate(t, "evidence", "evidence-0", 0)
	return f
}

func (f *exactProbeTrustFixture) activate(t *testing.T, role, id string, epoch uint64) {
	t.Helper()
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	f.keys[id] = private
	f.append(t, role, id, epoch, "activate", public)
}
func (f *exactProbeTrustFixture) revoke(t *testing.T, role, id string) {
	t.Helper()
	private := f.keys[id]
	f.append(t, role, id, f.keyEpoch(id), "revoke", private.Public().(ed25519.PublicKey))
}
func (f *exactProbeTrustFixture) keyEpoch(id string) uint64 {
	for _, r := range f.records {
		if r.KeyID == id && r.Operation == "activate" {
			return r.KeyEpoch
		}
	}
	return 0
}
func (f *exactProbeTrustFixture) append(t *testing.T, role, id string, epoch uint64, operation string, public ed25519.PublicKey) {
	t.Helper()
	i := uint64(len(f.records))
	r := QoderProbeTrustKeyRecord{APIVersion: exactAuthorityAPIVersion, Kind: "QoderProbeTrustKeyRecord", SchemaVersion: 1, LedgerEpoch: i, Role: role, KeyID: id, KeyEpoch: epoch, Operation: operation, PublicKeyEncoding: exactSignatureEncoding, Ed25519PublicKey: base64.RawURLEncoding.EncodeToString(public), PublicKeyDigest: digestBytes(public), EffectiveAt: candidateExactTimestamp(f.now.Add(-time.Second)), OperatorKeyID: f.operator.KeyID, OperatorKeyEpoch: f.operator.KeyEpoch, OperatorSignatureAlgorithm: exactSignatureAlgorithm, OperatorSignatureEncoding: exactSignatureEncoding}
	if i > 0 {
		previous := f.records[i-1].RecordDigest
		r.PreviousRecordDigest = &previous
	}
	f.resign(&r)
	f.records = append(f.records, r)
}
func (f *exactProbeTrustFixture) resign(r *QoderProbeTrustKeyRecord) {
	r.RecordDigest = digestRecordWithoutFields(*r, "operatorSignature", "recordDigest")
	r.OperatorSignature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(f.operatorPrivate, []byte(probeTrustSigningDomain+r.RecordDigest)))
}

func exactEvidenceManifests() []CandidateVariantInvocationManifest {
	manifests := make([]CandidateVariantInvocationManifest, 4)
	for index := range manifests {
		manifests[index] = CandidateVariantInvocationManifest{ReceiptSequence: uint64(index + 1), VariantID: candidateVariantID(index)}
		resignCandidateManifest(&manifests[index])
	}
	return manifests
}

func exactEvidenceExecutableIdentity() CandidateExecutableReceiptIdentity {
	pathBytes := []byte("/opt/qoder/qodercli")
	return CandidateExecutableReceiptIdentity{RealpathBytes: CandidateCanonicalPathBytes{Encoding: exactSignatureEncoding, Bytes: base64.RawURLEncoding.EncodeToString(pathBytes), Digest: digestBytes(pathBytes)}, Device: 1, Inode: 2, Digest: digest("e"), BinaryVersion: supportedBinary}
}

func TestExactProbeTrustLedgerReplayAndRotation(t *testing.T) {
	f := newExactProbeTrustFixture(t)
	f.activate(t, "evidence", "evidence-1", 1)
	f.revoke(t, "evidence", "evidence-0")
	state, err := ReplayQoderProbeTrustLedger(f.records, []QoderOSTrustKeyIdentity{f.operator}, f.now)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.ActiveKeys["evidence"]) != 1 || state.ActiveKeys["evidence"][0].KeyID != "evidence-1" {
		t.Fatalf("wrong evidence active set: %+v", state.ActiveKeys)
	}
	badOperator := f.operator
	badOperator.PublicKey = badOperator.PublicKey[:ed25519.PublicKeySize-1]
	if _, err := ReplayQoderProbeTrustLedger(f.records, []QoderOSTrustKeyIdentity{badOperator}, f.now); err == nil {
		t.Fatal("malformed OS-root operator key accepted")
	}
	for name, mutate := range map[string]func(*exactProbeTrustFixture){
		"bootstrap order":   func(v *exactProbeTrustFixture) { v.records[0].Role = "evidence" },
		"epoch jump":        func(v *exactProbeTrustFixture) { v.records[3].KeyEpoch = 3 },
		"previous rollback": func(v *exactProbeTrustFixture) { changed := digest("f"); v.records[3].PreviousRecordDigest = &changed },
		"key id reuse":      func(v *exactProbeTrustFixture) { v.records[3].KeyID = "evidence-0" },
		"public key reuse": func(v *exactProbeTrustFixture) {
			p := v.keys["evidence-0"].Public().(ed25519.PublicKey)
			v.records[3].Ed25519PublicKey = base64.RawURLEncoding.EncodeToString(p)
			v.records[3].PublicKeyDigest = digestBytes(p)
		},
		"future record": func(v *exactProbeTrustFixture) {
			v.records[3].EffectiveAt = candidateExactTimestamp(v.now.Add(time.Hour))
		},
	} {
		t.Run(name, func(t *testing.T) {
			broken := newExactProbeTrustFixture(t)
			broken.activate(t, "evidence", "evidence-1", 1)
			mutate(broken)
			broken.resign(&broken.records[len(broken.records)-1])
			if _, err := ReplayQoderProbeTrustLedger(broken.records, []QoderOSTrustKeyIdentity{broken.operator}, broken.now); err == nil {
				t.Fatal("invalid probe trust ledger accepted")
			}
		})
	}
	t.Run("last active revoke", func(t *testing.T) {
		broken := newExactProbeTrustFixture(t)
		broken.revoke(t, "evidence", "evidence-0")
		if _, err := ReplayQoderProbeTrustLedger(broken.records, []QoderOSTrustKeyIdentity{broken.operator}, broken.now); err == nil {
			t.Fatal("last evidence key revoked")
		}
	})
}

func TestExactAuthorityBindingPinsFourTailsAndCurrentEvidence(t *testing.T) {
	f := newExactProbeTrustFixture(t)
	trust, err := ReplayQoderProbeTrustLedger(f.records, []QoderOSTrustKeyIdentity{f.operator}, f.now)
	if err != nil {
		t.Fatal(err)
	}
	host, _ := exactHostIdentityFixture(t)
	osState := QoderOSTrustLedgerState{RootRecordDigest: digest("d")}
	evidence := QoderConformanceEvidenceExact{APIVersion: exactAuthorityAPIVersion, Kind: "QoderConformanceEvidence", SchemaVersion: 1, ObservationDigest: digest("a"), ProbeRunID: "run-1", RunnerID: "runner-1", RunnerVersion: "1", ObservedAt: candidateExactTimestamp(f.now.Add(-time.Minute)), ValidUntil: candidateExactTimestamp(f.now.Add(time.Hour)), AdapterVersion: adapterVersion, CandidateExecutableIdentity: exactEvidenceExecutableIdentity(), HostIdentity: host, AuthorityGeneration: 1, SuiteDigest: expectedProbeSuiteDigest(), ProbeArtifactDigest: digest("c"), ProbeRunChallengeDigest: digest("e"), CapabilitiesDigest: expectedCapabilitiesDigest(), ProfileDigest: expectedProbeProfileDigest(), VariantInvocationManifests: exactEvidenceManifests(), ToolPolicyDigest: expectedProbeToolPolicyDigest(), EventContract: conformanceEventContract, ProtocolVersion: qoderProtocolVersion, PermissionMode: qoderPermissionMode, TranscriptDigest: digest("3"), ReceiptDigests: []string{digest("4"), digest("5"), digest("6"), digest("7")}, AggregateReceiptDigest: digest("8"), CredentialVerified: true, LiveProtocolVerified: true, WorkspaceWriteVerified: true, ReceiptTrustLedgerTailDigest: trust.TailDigest, VerifierTrustLedgerTailDigest: trust.TailDigest, EvidenceTrustLedgerTailDigest: trust.TailDigest, OSTrustRootLedgerTailDigest: osState.RootRecordDigest, EvidenceAuthorityKeyID: "evidence-0", SignatureAlgorithm: exactSignatureAlgorithm, SignatureEncoding: exactSignatureEncoding}
	evidence.EvidenceDigest = digestRecordWithoutFields(evidence, "signature", "evidenceDigest")
	evidence.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(f.keys["evidence-0"], []byte(exactEvidenceSigningDomain+evidence.EvidenceDigest)))
	config := QoderAuthorityConfigExact{APIVersion: exactAuthorityAPIVersion, Kind: "QoderAuthorityConfig", SchemaVersion: 1, RepositoryIdentity: "repo-1", AuthorityGeneration: 1, HostIdentityDigest: host.RecordDigest, EvidenceRootIdentity: candidateRootIdentity(CandidateObjectIdentity{Device: 1, Inode: 2}), CurrentEvidenceDigest: evidence.EvidenceDigest, ProbeArtifactDigest: evidence.ProbeArtifactDigest, ProbeRunChallengeDigest: evidence.ProbeRunChallengeDigest, RevokedEvidenceDigests: []string{}, TrustPolicyDigest: digest("a"), ReceiptTrustLedgerTailDigest: trust.TailDigest, VerifierTrustLedgerTailDigest: trust.TailDigest, EvidenceTrustLedgerTailDigest: trust.TailDigest, OSTrustRootLedgerTailDigest: osState.RootRecordDigest, ConsumerFenceProviderIdentity: "fence-provider"}
	config.ConfigDigest = digestRecordWithoutFields(config, "configDigest")
	if err := ValidateExactAuthorityBinding(config, evidence, trust, trust, trust, osState, host, f.now); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*QoderAuthorityConfigExact){"receipt tail": func(v *QoderAuthorityConfigExact) { v.ReceiptTrustLedgerTailDigest = digest("b") }, "OS tail": func(v *QoderAuthorityConfigExact) { v.OSTrustRootLedgerTailDigest = digest("c") }, "host rotation": func(v *QoderAuthorityConfigExact) { v.HostIdentityDigest = digest("d") }, "current evidence": func(v *QoderAuthorityConfigExact) { v.CurrentEvidenceDigest = digest("e") }} {
		t.Run(name, func(t *testing.T) {
			changed := config
			mutate(&changed)
			changed.ConfigDigest = digestRecordWithoutFields(changed, "configDigest")
			if err := ValidateExactAuthorityBinding(changed, evidence, trust, trust, trust, osState, host, f.now); err == nil {
				t.Fatal("drifted exact binding accepted")
			}
		})
	}
	revoked := config
	revoked.RevokedEvidenceDigests = []string{evidence.EvidenceDigest}
	revoked.ConfigDigest = digestRecordWithoutFields(revoked, "configDigest")
	if err := ValidateExactAuthorityBinding(revoked, evidence, trust, trust, trust, osState, host, f.now); err == nil {
		t.Fatal("revoked evidence accepted")
	}
	rotated := newExactProbeTrustFixture(t)
	rotated.activate(t, "evidence", "evidence-1", 1)
	rotated.revoke(t, "evidence", "evidence-0")
	rotatedTrust, err := ReplayQoderProbeTrustLedger(rotated.records, []QoderOSTrustKeyIdentity{rotated.operator}, rotated.now)
	if err != nil {
		t.Fatal(err)
	}
	oldSignerEvidence := evidence
	oldSignerEvidence.EvidenceTrustLedgerTailDigest = rotatedTrust.TailDigest
	oldSignerEvidence.EvidenceDigest = digestRecordWithoutFields(oldSignerEvidence, "signature", "evidenceDigest")
	oldSignerEvidence.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(f.keys["evidence-0"], []byte(exactEvidenceSigningDomain+oldSignerEvidence.EvidenceDigest)))
	rotatedConfig := config
	rotatedConfig.EvidenceTrustLedgerTailDigest = rotatedTrust.TailDigest
	rotatedConfig.CurrentEvidenceDigest = oldSignerEvidence.EvidenceDigest
	rotatedConfig.ConfigDigest = digestRecordWithoutFields(rotatedConfig, "configDigest")
	if err := ValidateExactAuthorityBinding(rotatedConfig, oldSignerEvidence, trust, trust, rotatedTrust, osState, host, f.now); err == nil {
		t.Fatal("evidence signed by revoked authority key accepted after rotation")
	}
}
