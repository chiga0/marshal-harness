package qoder

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
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
	return newExactProbeTrustFixtureWithOperator(t, time.Now().UTC(), QoderOSTrustKeyIdentity{Role: "trust-ledger-operator", KeyID: "operator-0", PublicKeyDigest: digestBytes(operatorPublic), PublicKey: operatorPublic}, operatorPrivate)
}

func newExactProbeTrustFixtureWithOperator(t *testing.T, now time.Time, operator QoderOSTrustKeyIdentity, operatorPrivate ed25519.PrivateKey) *exactProbeTrustFixture {
	t.Helper()
	f := &exactProbeTrustFixture{now: now, operator: operator, operatorPrivate: operatorPrivate, keys: map[string]ed25519.PrivateKey{}}
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

func exactEvidenceManifests(t *testing.T, now time.Time, credentialPrivate ed25519.PrivateKey) []CandidateVariantInvocationManifest {
	t.Helper()
	manifests := make([]CandidateVariantInvocationManifest, 4)
	for index := range manifests {
		variant := candidateProbeVariants("provider/model")[index]
		capabilityID := []byte("capability-id-00")
		capabilityID[len(capabilityID)-1] = byte('0' + index)
		capability := CandidateCredentialCapabilityIdentity{APIVersion: candidateReceiptAPIVersion, Kind: "QoderCredentialCapabilityIdentity", SchemaVersion: 1, ProviderIdentity: "credential-provider", CapabilityID: base64.RawURLEncoding.EncodeToString(capabilityID), ProbeRunID: "run-1", VariantID: candidateVariantID(index), CapabilityClass: "qoder-live-probe", PolicyScopeDigest: digest("a"), IssuedAt: candidateExactTimestamp(now.Add(-time.Minute)), ExpiresAt: candidateExactTimestamp(now.Add(time.Minute)), ProviderKeyID: "credential-0", SignatureAlgorithm: candidateSignatureAlgorithm, SignatureEncoding: candidateSignatureEncoding}
		capability.RecordDigest = capability.digest()
		message, _ := capability.signingBytes()
		capability.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(credentialPrivate, message))
		invocation := CandidateProbeInvocation{ProbeRunID: "run-1", ReceiptSequence: index + 1, VariantIndex: index, Arguments: buildArgs(variant.model, candidateBoundCredentialToken, candidateBoundScratchToken, variant.disableAllTools), Environment: candidateProbeEnvironment(candidateBoundScratchToken, candidateBoundCredentialToken), ExpectedModel: variant.model}
		manifest, err := candidateInvocationManifest(invocation, capability)
		if err != nil {
			t.Fatal(err)
		}
		manifests[index] = manifest
	}
	return manifests
}

func cloneExactEvidenceManifests(manifests []CandidateVariantInvocationManifest) []CandidateVariantInvocationManifest {
	cloned := append([]CandidateVariantInvocationManifest(nil), manifests...)
	for manifestIndex := range cloned {
		manifest := &cloned[manifestIndex]
		manifest.ArgvManifest.Entries = append([]CandidateArgvEntry(nil), manifest.ArgvManifest.Entries...)
		manifest.EnvironmentManifest.Entries = append([]CandidateEnvironmentEntry(nil), manifest.EnvironmentManifest.Entries...)
		for entryIndex := range manifest.EnvironmentManifest.Entries {
			if manifest.EnvironmentManifest.Entries[entryIndex].CapabilityIdentity != nil {
				capability := *manifest.EnvironmentManifest.Entries[entryIndex].CapabilityIdentity
				manifest.EnvironmentManifest.Entries[entryIndex].CapabilityIdentity = &capability
			}
		}
	}
	return cloned
}

func exactHeldObjects(t *testing.T) (CandidateBoundObject, CandidateBoundObject) {
	t.Helper()
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	executablePath := filepath.Join(root, "qodercli")
	if err := os.WriteFile(executablePath, []byte("held qodercli 1.1.23"), 0o700); err != nil {
		t.Fatal(err)
	}
	executable, err := openCandidateObject(executablePath, false)
	if err != nil {
		t.Fatal(err)
	}
	evidenceRootPath := filepath.Join(root, "evidence")
	if err := os.Mkdir(evidenceRootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	evidenceRoot, err := openCandidateObject(evidenceRootPath, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = executable.File.Close(); _ = evidenceRoot.File.Close() })
	return executable, evidenceRoot
}

func exactRootedHostIdentity(t *testing.T, now time.Time, keyID string, epoch uint64, private ed25519.PrivateKey) HostAttestationIdentity {
	t.Helper()
	identity := HostAttestationIdentity{APIVersion: exactAuthorityAPIVersion, Kind: "HostAttestationIdentity", SchemaVersion: 1, ProviderIdentity: "host-provider", HostKeyID: "host-key", HostPublicKeyEncoding: exactSignatureEncoding, HostPublicKeyDigest: digest("a"), OSAttestedMachineIdentityDigest: digest("b"), OS: runtime.GOOS, Arch: runtime.GOARCH, IssuedAt: candidateExactTimestamp(now.Add(-time.Second)), ProviderKeyID: keyID, ProviderKeyEpoch: epoch, SignatureAlgorithm: exactSignatureAlgorithm, SignatureEncoding: exactSignatureEncoding}
	identity.RecordDigest = digestRecordWithoutFields(identity, "signature", "recordDigest")
	identity.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(private, []byte(hostIdentitySigningDomain+identity.RecordDigest)))
	return identity
}

func exactFenceAdvanceReceipt(t *testing.T, now time.Time, request ConsumerFenceAdvanceRequest, keyID string, epoch uint64, private ed25519.PrivateKey) ConsumerFenceReceipt {
	t.Helper()
	receipt := ConsumerFenceReceipt{APIVersion: exactAuthorityAPIVersion, Kind: "QoderConsumerFenceAdvanceReceipt", SchemaVersion: 1, ProviderIdentity: "fence-provider", ConsumerInstanceID: request.ConsumerInstanceID, RepositoryIdentity: request.RepositoryIdentity, ProviderCounter: request.ExpectedProviderCounter + 1, TransactionID: request.TransactionID, PreparedRecordDigest: &request.PreparedRecordDigest, AuthorityGeneration: request.AuthorityGeneration, ConfigDigest: &request.ConfigDigest, PreviousReceiptDigest: request.ExpectedPreviousReceiptDigest, ObservedAt: candidateExactTimestamp(now.Add(-time.Second)), ProviderKeyID: keyID, ProviderKeyEpoch: epoch, SignatureAlgorithm: exactSignatureAlgorithm, SignatureEncoding: exactSignatureEncoding}
	receipt.RecordDigest = digestRecordWithoutFields(receipt, "signature", "recordDigest")
	receipt.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(private, []byte(fenceAdvanceSigningDomain+receipt.RecordDigest)))
	return receipt
}

func resignExactEvidence(evidence *QoderConformanceEvidenceExact, private ed25519.PrivateKey) {
	evidence.EvidenceDigest = digestRecordWithoutFields(*evidence, "signature", "evidenceDigest")
	evidence.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(private, []byte(exactEvidenceSigningDomain+evidence.EvidenceDigest)))
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
	osFixture := newExactLedgerFixture(t)
	osFixture.appendActivate(t, "credential-capability-provider", "credential-0", 0, "operator-0")
	f := newExactProbeTrustFixtureWithOperator(t, osFixture.now, QoderOSTrustKeyIdentity{Role: "trust-ledger-operator", KeyID: "operator-0", PublicKeyDigest: digestBytes(osFixture.keys["operator-0"].Public().(ed25519.PublicKey)), PublicKey: osFixture.keys["operator-0"].Public().(ed25519.PublicKey)}, osFixture.keys["operator-0"])
	trust, err := ReplayQoderProbeTrustLedger(f.records, []QoderOSTrustKeyIdentity{f.operator}, f.now)
	if err != nil {
		t.Fatal(err)
	}
	osState, err := ReplayQoderOSTrustRootLedger(osFixture.records, osFixture.receipts, "os-anchor", "anchor-key", 0, osFixture.providerPublic, f.now)
	if err != nil {
		t.Fatal(err)
	}
	host := exactRootedHostIdentity(t, f.now, "host-1", 1, osFixture.keys["host-1"])
	executable, evidenceRoot := exactHeldObjects(t)
	evidence := QoderConformanceEvidenceExact{APIVersion: exactAuthorityAPIVersion, Kind: "QoderConformanceEvidence", SchemaVersion: 1, ObservationDigest: digest("a"), ProbeRunID: "run-1", RunnerID: "runner-1", RunnerVersion: "1", ObservedAt: candidateExactTimestamp(f.now.Add(-time.Minute)), ValidUntil: candidateExactTimestamp(f.now.Add(time.Hour)), AdapterVersion: adapterVersion, CandidateExecutableIdentity: candidateExecutableReceiptIdentity(executable, supportedBinary), HostIdentity: host, AuthorityGeneration: 1, SuiteDigest: expectedProbeSuiteDigest(), ProbeArtifactDigest: digest("c"), ProbeRunChallengeDigest: digest("e"), CapabilitiesDigest: expectedCapabilitiesDigest(), ProfileDigest: expectedProbeProfileDigest(), VariantInvocationManifests: exactEvidenceManifests(t, f.now, osFixture.keys["credential-0"]), ToolPolicyDigest: expectedProbeToolPolicyDigest(), EventContract: conformanceEventContract, ProtocolVersion: qoderProtocolVersion, PermissionMode: qoderPermissionMode, TranscriptDigest: digest("3"), ReceiptDigests: []string{digest("4"), digest("5"), digest("6"), digest("7")}, AggregateReceiptDigest: digest("8"), CredentialVerified: true, LiveProtocolVerified: true, WorkspaceWriteVerified: true, ReceiptTrustLedgerTailDigest: trust.TailDigest, VerifierTrustLedgerTailDigest: trust.TailDigest, EvidenceTrustLedgerTailDigest: trust.TailDigest, OSTrustRootLedgerTailDigest: osState.RootRecordDigest, EvidenceAuthorityKeyID: "evidence-0", SignatureAlgorithm: exactSignatureAlgorithm, SignatureEncoding: exactSignatureEncoding}
	resignExactEvidence(&evidence, f.keys["evidence-0"])
	config := QoderAuthorityConfigExact{APIVersion: exactAuthorityAPIVersion, Kind: "QoderAuthorityConfig", SchemaVersion: 1, RepositoryIdentity: "repo-1", AuthorityGeneration: 1, HostIdentityDigest: host.RecordDigest, EvidenceRootIdentity: candidateRootIdentity(evidenceRoot.Identity), CurrentEvidenceDigest: evidence.EvidenceDigest, ProbeArtifactDigest: evidence.ProbeArtifactDigest, ProbeRunChallengeDigest: evidence.ProbeRunChallengeDigest, RevokedEvidenceDigests: []string{}, TrustPolicyDigest: digest("a"), ReceiptTrustLedgerTailDigest: trust.TailDigest, VerifierTrustLedgerTailDigest: trust.TailDigest, EvidenceTrustLedgerTailDigest: trust.TailDigest, OSTrustRootLedgerTailDigest: osState.RootRecordDigest, ConsumerFenceProviderIdentity: "fence-provider"}
	config.ConfigDigest = digestRecordWithoutFields(config, "configDigest")
	fenceRequest := ConsumerFenceAdvanceRequest{ConsumerInstanceID: "consumer-1", RepositoryIdentity: config.RepositoryIdentity, TransactionID: "transaction-1", PreparedRecordDigest: digest("9"), AuthorityGeneration: config.AuthorityGeneration, ConfigDigest: config.ConfigDigest}
	current := QoderExactAuthorityCurrent{OSTrustRecords: osFixture.records, OSTrustReceipts: osFixture.receipts, OSAnchorProviderIdentity: "os-anchor", OSAnchorProviderKeyID: "anchor-key", OSAnchorProviderPublicKey: osFixture.providerPublic, ProbeTrustRecords: f.records, HostIdentity: host, FenceRequest: fenceRequest, FenceReceipt: exactFenceAdvanceReceipt(t, f.now, fenceRequest, "fence-0", 0, osFixture.keys["fence-0"]), CredentialProviderIdentity: "credential-provider", Executable: executable, ExecutableVersion: supportedBinary, EvidenceRoot: evidenceRoot}
	if err := ValidateExactAuthorityBinding(config, evidence, current, f.now); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*QoderAuthorityConfigExact){"receipt tail": func(v *QoderAuthorityConfigExact) { v.ReceiptTrustLedgerTailDigest = digest("b") }, "OS tail": func(v *QoderAuthorityConfigExact) { v.OSTrustRootLedgerTailDigest = digest("c") }, "host rotation": func(v *QoderAuthorityConfigExact) { v.HostIdentityDigest = digest("d") }, "current evidence": func(v *QoderAuthorityConfigExact) { v.CurrentEvidenceDigest = digest("e") }} {
		t.Run(name, func(t *testing.T) {
			changed := config
			mutate(&changed)
			changed.ConfigDigest = digestRecordWithoutFields(changed, "configDigest")
			changedCurrent := current
			changedCurrent.FenceRequest.ConfigDigest = changed.ConfigDigest
			changedCurrent.FenceReceipt = exactFenceAdvanceReceipt(t, f.now, changedCurrent.FenceRequest, "fence-0", 0, osFixture.keys["fence-0"])
			if err := ValidateExactAuthorityBinding(changed, evidence, changedCurrent, f.now); err == nil {
				t.Fatal("drifted exact binding accepted")
			}
		})
	}
	revoked := config
	revoked.RevokedEvidenceDigests = []string{evidence.EvidenceDigest}
	revoked.ConfigDigest = digestRecordWithoutFields(revoked, "configDigest")
	revokedCurrent := current
	revokedCurrent.FenceRequest.ConfigDigest = revoked.ConfigDigest
	revokedCurrent.FenceReceipt = exactFenceAdvanceReceipt(t, f.now, revokedCurrent.FenceRequest, "fence-0", 0, osFixture.keys["fence-0"])
	if err := ValidateExactAuthorityBinding(revoked, evidence, revokedCurrent, f.now); err == nil {
		t.Fatal("revoked evidence accepted")
	}
	rotated := newExactProbeTrustFixtureWithOperator(t, osFixture.now, f.operator, osFixture.keys["operator-0"])
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
	rotatedCurrent := current
	rotatedCurrent.ProbeTrustRecords = rotated.records
	rotatedCurrent.FenceRequest.ConfigDigest = rotatedConfig.ConfigDigest
	rotatedCurrent.FenceReceipt = exactFenceAdvanceReceipt(t, f.now, rotatedCurrent.FenceRequest, "fence-0", 0, osFixture.keys["fence-0"])
	if err := ValidateExactAuthorityBinding(rotatedConfig, oldSignerEvidence, rotatedCurrent, f.now); err == nil {
		t.Fatal("evidence signed by revoked authority key accepted after rotation")
	}

	t.Run("detached authority inputs", func(t *testing.T) {
		for name, mutate := range map[string]func(*QoderExactAuthorityCurrent){
			"operator": func(v *QoderExactAuthorityCurrent) { v.ProbeTrustRecords = newExactProbeTrustFixture(t).records },
			"host signature": func(v *QoderExactAuthorityCurrent) {
				v.HostIdentity.Signature = base64.RawURLEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))
			},
			"fence signature": func(v *QoderExactAuthorityCurrent) {
				v.FenceReceipt.Signature = base64.RawURLEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))
			},
		} {
			t.Run(name, func(t *testing.T) {
				changed := current
				mutate(&changed)
				if err := ValidateExactAuthorityBinding(config, evidence, changed, f.now); err == nil {
					t.Fatal("detached authority input accepted")
				}
			})
		}
	})

	t.Run("held path and root replacement", func(t *testing.T) {
		for name, object := range map[string]CandidateBoundObject{"executable": executable, "evidence root": evidenceRoot} {
			t.Run(name, func(t *testing.T) {
				moved := object.CanonicalPath + ".held"
				if err := os.Rename(object.CanonicalPath, moved); err != nil {
					t.Fatal(err)
				}
				if name == "executable" {
					if err := os.WriteFile(object.CanonicalPath, []byte("replacement"), 0o700); err != nil {
						t.Fatal(err)
					}
				} else if err := os.Mkdir(object.CanonicalPath, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := ValidateExactAuthorityBinding(config, evidence, current, f.now); err == nil {
					t.Fatal("replaced held pathname accepted")
				}
				if name == "executable" {
					_ = os.Remove(object.CanonicalPath)
				} else {
					_ = os.Remove(object.CanonicalPath)
				}
				if err := os.Rename(moved, object.CanonicalPath); err != nil {
					t.Fatal(err)
				}
			})
		}
	})

	for name, mutate := range map[string]func(*CandidateVariantInvocationManifest){
		"argv source": func(v *CandidateVariantInvocationManifest) { v.ArgvManifest.Entries[0].Source = "caller" },
		"argv representation": func(v *CandidateVariantInvocationManifest) {
			v.ArgvManifest.Entries[0].LiteralValue = nil
		},
		"environment presence": func(v *CandidateVariantInvocationManifest) { v.EnvironmentManifest.Entries[0].Presence = "omitted" },
		"capability policy": func(v *CandidateVariantInvocationManifest) {
			for i := range v.EnvironmentManifest.Entries {
				if v.EnvironmentManifest.Entries[i].CapabilityIdentity != nil {
					v.EnvironmentManifest.Entries[i].CapabilityIdentity.PolicyScopeDigest = "invalid"
					break
				}
			}
		},
		"capability signature": func(v *CandidateVariantInvocationManifest) {
			for i := range v.EnvironmentManifest.Entries {
				if v.EnvironmentManifest.Entries[i].CapabilityIdentity != nil {
					v.EnvironmentManifest.Entries[i].CapabilityIdentity.Signature = base64.RawURLEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))
					break
				}
			}
		},
		"environment policy": func(v *CandidateVariantInvocationManifest) { v.EnvironmentManifest.PolicyDigest = digest("f") },
	} {
		t.Run("malformed manifest "+name, func(t *testing.T) {
			changedEvidence := evidence
			changedEvidence.VariantInvocationManifests = append([]CandidateVariantInvocationManifest(nil), evidence.VariantInvocationManifests...)
			manifest := &changedEvidence.VariantInvocationManifests[0]
			manifest.ArgvManifest.Entries = append([]CandidateArgvEntry(nil), manifest.ArgvManifest.Entries...)
			manifest.EnvironmentManifest.Entries = append([]CandidateEnvironmentEntry(nil), manifest.EnvironmentManifest.Entries...)
			for index := range manifest.EnvironmentManifest.Entries {
				if manifest.EnvironmentManifest.Entries[index].CapabilityIdentity != nil {
					copy := *manifest.EnvironmentManifest.Entries[index].CapabilityIdentity
					manifest.EnvironmentManifest.Entries[index].CapabilityIdentity = &copy
				}
			}
			mutate(manifest)
			resignCandidateManifest(manifest)
			resignExactEvidence(&changedEvidence, f.keys["evidence-0"])
			changedConfig := config
			changedConfig.CurrentEvidenceDigest = changedEvidence.EvidenceDigest
			changedConfig.ConfigDigest = digestRecordWithoutFields(changedConfig, "configDigest")
			changedCurrent := current
			changedCurrent.FenceRequest.ConfigDigest = changedConfig.ConfigDigest
			changedCurrent.FenceReceipt = exactFenceAdvanceReceipt(t, f.now, changedCurrent.FenceRequest, "fence-0", 0, osFixture.keys["fence-0"])
			if err := ValidateExactAuthorityBinding(changedConfig, changedEvidence, changedCurrent, f.now); err == nil {
				t.Fatal("malformed nested manifest accepted")
			}
		})
	}

	for name, mutate := range map[string]func(*CandidateVariantInvocationManifest){
		"valid signed scope drift": func(v *CandidateVariantInvocationManifest) {
			for index := range v.EnvironmentManifest.Entries {
				if v.EnvironmentManifest.Entries[index].CapabilityIdentity != nil {
					v.EnvironmentManifest.Entries[index].CapabilityIdentity.PolicyScopeDigest = digest("f")
				}
			}
		},
		"argv semantic drift": func(v *CandidateVariantInvocationManifest) {
			literal := "--replacement-output-format"
			v.ArgvManifest.Entries[0].LiteralValue = &literal
			v.ArgvManifest.Entries[0].ValueDigest = digestBytes([]byte(literal))
		},
		"environment semantic drift": func(v *CandidateVariantInvocationManifest) {
			for index := range v.EnvironmentManifest.Entries {
				entry := &v.EnvironmentManifest.Entries[index]
				if entry.Source == "fixed-policy" && entry.ValueDigest != nil {
					changed := digestBytes([]byte("replacement-value"))
					entry.ValueDigest = &changed
					break
				}
			}
		},
	} {
		t.Run("resigned semantic "+name, func(t *testing.T) {
			changedEvidence := evidence
			changedEvidence.VariantInvocationManifests = append([]CandidateVariantInvocationManifest(nil), evidence.VariantInvocationManifests...)
			manifest := &changedEvidence.VariantInvocationManifests[0]
			manifest.ArgvManifest.Entries = append([]CandidateArgvEntry(nil), manifest.ArgvManifest.Entries...)
			manifest.EnvironmentManifest.Entries = append([]CandidateEnvironmentEntry(nil), manifest.EnvironmentManifest.Entries...)
			for index := range manifest.EnvironmentManifest.Entries {
				if manifest.EnvironmentManifest.Entries[index].CapabilityIdentity != nil {
					copy := *manifest.EnvironmentManifest.Entries[index].CapabilityIdentity
					manifest.EnvironmentManifest.Entries[index].CapabilityIdentity = &copy
				}
			}
			mutate(manifest)
			for index := range manifest.EnvironmentManifest.Entries {
				capability := manifest.EnvironmentManifest.Entries[index].CapabilityIdentity
				if capability != nil {
					capability.RecordDigest = capability.digest()
					message, _ := capability.signingBytes()
					capability.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(osFixture.keys["credential-0"], message))
				}
			}
			resignCandidateManifest(manifest)
			resignExactEvidence(&changedEvidence, f.keys["evidence-0"])
			changedConfig := config
			changedConfig.CurrentEvidenceDigest = changedEvidence.EvidenceDigest
			changedConfig.ConfigDigest = digestRecordWithoutFields(changedConfig, "configDigest")
			changedCurrent := current
			changedCurrent.FenceRequest.ConfigDigest = changedConfig.ConfigDigest
			changedCurrent.FenceReceipt = exactFenceAdvanceReceipt(t, f.now, changedCurrent.FenceRequest, "fence-0", 0, osFixture.keys["fence-0"])
			if err := ValidateExactAuthorityBinding(changedConfig, changedEvidence, changedCurrent, f.now); err == nil {
				t.Fatal("fully resigned semantic drift accepted")
			}
		})
	}

	t.Run("fully resigned empty model digest in both model variants", func(t *testing.T) {
		changedEvidence := evidence
		changedEvidence.VariantInvocationManifests = cloneExactEvidenceManifests(evidence.VariantInvocationManifests)
		emptyModelDigest := digestBytes(nil)
		for _, manifestIndex := range []int{1, 3} {
			manifest := &changedEvidence.VariantInvocationManifests[manifestIndex]
			found := false
			for entryIndex := range manifest.ArgvManifest.Entries {
				entry := &manifest.ArgvManifest.Entries[entryIndex]
				if entry.Source == "model-id" {
					entry.ValueDigest = emptyModelDigest
					found = true
				}
			}
			if !found {
				t.Fatal("model-present variant has no model-id entry")
			}
			resignCandidateManifest(manifest)
		}
		resignExactEvidence(&changedEvidence, f.keys["evidence-0"])
		changedConfig := config
		changedConfig.CurrentEvidenceDigest = changedEvidence.EvidenceDigest
		changedConfig.ConfigDigest = digestRecordWithoutFields(changedConfig, "configDigest")
		changedCurrent := current
		changedCurrent.FenceRequest.ConfigDigest = changedConfig.ConfigDigest
		changedCurrent.FenceReceipt = exactFenceAdvanceReceipt(t, f.now, changedCurrent.FenceRequest, "fence-0", 0, osFixture.keys["fence-0"])
		if err := ValidateExactAuthorityBinding(changedConfig, changedEvidence, changedCurrent, f.now); err == nil {
			t.Fatal("fully resigned empty model semantics accepted for model-present variants")
		}
	})

	t.Run("fully resigned capability id replay across variants", func(t *testing.T) {
		changedEvidence := evidence
		changedEvidence.VariantInvocationManifests = cloneExactEvidenceManifests(evidence.VariantInvocationManifests)
		first, err := credentialCapabilityFromManifest(changedEvidence.VariantInvocationManifests[0].EnvironmentManifest)
		if err != nil {
			t.Fatal(err)
		}
		manifest := &changedEvidence.VariantInvocationManifests[1]
		found := false
		for entryIndex := range manifest.EnvironmentManifest.Entries {
			capability := manifest.EnvironmentManifest.Entries[entryIndex].CapabilityIdentity
			if capability == nil {
				continue
			}
			capability.CapabilityID = first.CapabilityID
			capability.RecordDigest = capability.digest()
			message, signingErr := capability.signingBytes()
			if signingErr != nil {
				t.Fatal(signingErr)
			}
			capability.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(osFixture.keys["credential-0"], message))
			found = true
		}
		if !found {
			t.Fatal("variant has no credential capability identity")
		}
		resignCandidateManifest(manifest)
		resignExactEvidence(&changedEvidence, f.keys["evidence-0"])
		changedConfig := config
		changedConfig.CurrentEvidenceDigest = changedEvidence.EvidenceDigest
		changedConfig.ConfigDigest = digestRecordWithoutFields(changedConfig, "configDigest")
		changedCurrent := current
		changedCurrent.FenceRequest.ConfigDigest = changedConfig.ConfigDigest
		changedCurrent.FenceReceipt = exactFenceAdvanceReceipt(t, f.now, changedCurrent.FenceRequest, "fence-0", 0, osFixture.keys["fence-0"])
		if err := ValidateExactAuthorityBinding(changedConfig, changedEvidence, changedCurrent, f.now); err == nil {
			t.Fatal("fully resigned credential capability replay accepted across variants")
		}
	})
}
