package qoder

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"runtime"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

type exactOSTrustProviderFixture struct{}

func (exactOSTrustProviderFixture) CurrentTrustRoot(context.Context) ([]byte, []byte, error) {
	return nil, nil, nil
}

type exactHostProviderFixture struct{}

func (exactHostProviderFixture) CurrentHostIdentity(context.Context) ([]byte, error) { return nil, nil }

type exactFenceProviderFixture struct{}

func (exactFenceProviderFixture) Genesis(context.Context, ConsumerFenceGenesisRequest) ([]byte, error) {
	return nil, nil
}
func (exactFenceProviderFixture) CompareAndAdvance(context.Context, ConsumerFenceAdvanceRequest) ([]byte, error) {
	return nil, nil
}

func TestExactAuthorityProvidersRequireThreeIndependentSeams(t *testing.T) {
	osTrust, host, fence := exactOSTrustProviderFixture{}, exactHostProviderFixture{}, exactFenceProviderFixture{}
	if _, err := NewExactAuthorityProviders(osTrust, host, fence); err != nil {
		t.Fatal(err)
	}
	for _, build := range []func() (*ExactAuthorityProviders, error){
		func() (*ExactAuthorityProviders, error) { return NewExactAuthorityProviders(nil, host, fence) },
		func() (*ExactAuthorityProviders, error) { return NewExactAuthorityProviders(osTrust, nil, fence) },
		func() (*ExactAuthorityProviders, error) { return NewExactAuthorityProviders(osTrust, host, nil) },
	} {
		if _, err := build(); err == nil {
			t.Fatal("incomplete exact authority provider set was accepted")
		}
	}
}

func TestExactAuthorityDecoderRejectsNonCanonicalOrOpenDocuments(t *testing.T) {
	identity, _ := exactHostIdentityFixture(t)
	document := exactCanonicalDocument(t, identity)
	if _, err := DecodeHostAttestationIdentity(document); err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err := json.Unmarshal(document, &object); err != nil {
		t.Fatal(err)
	}
	object["unknown"] = true
	unknown, _ := json.Marshal(object)
	unknown, _ = canonical.JSON(unknown)
	duplicate := bytes.Replace(document, []byte(`"kind":"HostAttestationIdentity"`), []byte(`"kind":"HostAttestationIdentity","kind":"HostAttestationIdentity"`), 1)
	for name, invalid := range map[string][]byte{
		"empty":      nil,
		"whitespace": append([]byte(" \n"), document...),
		"unknown":    unknown,
		"duplicate":  duplicate,
		"trailing":   append(append([]byte(nil), document...), '\n'),
		"oversized":  bytes.Repeat([]byte{'x'}, exactAuthorityDocumentLimit+1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeHostAttestationIdentity(invalid); err == nil {
				t.Fatal("invalid exact authority document was accepted")
			}
		})
	}
}

func TestExactOSTrustRootAndAnchorValidation(t *testing.T) {
	now := time.Now().UTC()
	operatorPublic, operatorPrivate, _ := ed25519.GenerateKey(rand.Reader)
	rootPublic, _, _ := ed25519.GenerateKey(rand.Reader)
	genesis := QoderOSTrustRootRecord{
		APIVersion: exactAuthorityAPIVersion, Kind: "QoderOSTrustRootRecord", SchemaVersion: 1,
		TrustDomainID: "host-domain", Role: "trust-ledger-operator", KeyID: "operator-0", Operation: "activate",
		PublicKeyEncoding: exactSignatureEncoding, Ed25519PublicKey: base64.RawURLEncoding.EncodeToString(rootPublic), PublicKeyDigest: digestBytes(rootPublic),
		EffectiveAt: candidateExactTimestamp(now.Add(-time.Second)), AnchorProviderIdentity: "os-anchor", RecordDigest: "",
	}
	genesis.RecordDigest = digestRecordWithoutFields(genesis, "authorizationSignature", "recordDigest")
	if err := ValidateQoderOSTrustRootRecord(genesis, "", 0, nil, now); err != nil {
		t.Fatal(err)
	}
	previous := genesis.RecordDigest
	authorizerID := "operator-0"
	authorizerEpoch := uint64(0)
	algorithm, encoding := exactSignatureAlgorithm, exactSignatureEncoding
	record := QoderOSTrustRootRecord{
		APIVersion: exactAuthorityAPIVersion, Kind: "QoderOSTrustRootRecord", SchemaVersion: 1,
		TrustDomainID: "host-domain", RootSequence: 1, Role: "host-attestation-provider", KeyID: "host-provider-0", Operation: "activate",
		PublicKeyEncoding: exactSignatureEncoding, Ed25519PublicKey: base64.RawURLEncoding.EncodeToString(rootPublic), PublicKeyDigest: digestBytes(rootPublic),
		PreviousRecordDigest: &previous, EffectiveAt: candidateExactTimestamp(now.Add(-time.Second)), AnchorProviderIdentity: "os-anchor", AnchorProviderCounter: 1,
		AuthorizingKeyID: &authorizerID, AuthorizingKeyEpoch: &authorizerEpoch, AuthorizationSignatureAlgorithm: &algorithm, AuthorizationSignatureEncoding: &encoding,
	}
	record.RecordDigest = digestRecordWithoutFields(record, "authorizationSignature", "recordDigest")
	signature := base64.RawURLEncoding.EncodeToString(ed25519.Sign(operatorPrivate, []byte(osTrustRootSigningDomain+record.RecordDigest)))
	record.AuthorizationSignature = &signature
	if err := ValidateQoderOSTrustRootRecord(record, "operator-0", 0, operatorPublic, now); err != nil {
		t.Fatal(err)
	}
	tampered := record
	tampered.Role = "evidence"
	if err := ValidateQoderOSTrustRootRecord(tampered, "operator-0", 0, operatorPublic, now); err == nil {
		t.Fatal("unknown OS trust role was accepted")
	}

	anchorPublic, anchorPrivate, _ := ed25519.GenerateKey(rand.Reader)
	anchor := QoderOSTrustAnchorReceipt{
		APIVersion: exactAuthorityAPIVersion, Kind: "QoderOSTrustAnchorReceipt", SchemaVersion: 1,
		AnchorProviderIdentity: "os-anchor", TrustDomainID: record.TrustDomainID, RootSequence: record.RootSequence, RootRecordDigest: record.RecordDigest,
		PreviousRootRecordDigest: record.PreviousRecordDigest, AnchorProviderCounter: record.AnchorProviderCounter, ObservedAt: candidateExactTimestamp(now.Add(-time.Second)),
		ProviderKeyID: "anchor-key", SignatureAlgorithm: exactSignatureAlgorithm, SignatureEncoding: exactSignatureEncoding,
	}
	anchor.RecordDigest = digestRecordWithoutFields(anchor, "signature", "recordDigest")
	anchor.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(anchorPrivate, []byte(osTrustAnchorSigningDomain+anchor.RecordDigest)))
	if err := ValidateQoderOSTrustAnchorReceipt(anchor, record, "os-anchor", "anchor-key", 0, anchorPublic, now); err != nil {
		t.Fatal(err)
	}
	anchor.RootRecordDigest = digest("wrong")
	if err := ValidateQoderOSTrustAnchorReceipt(anchor, record, "os-anchor", "anchor-key", 0, anchorPublic, now); err == nil {
		t.Fatal("anchor receipt detached from the exact root was accepted")
	}
}

type exactLedgerFixture struct {
	now             time.Time
	providerPublic  ed25519.PublicKey
	providerPrivate ed25519.PrivateKey
	keys            map[string]ed25519.PrivateKey
	records         []QoderOSTrustRootRecord
	receipts        []QoderOSTrustAnchorReceipt
}

func newExactLedgerFixture(t *testing.T) *exactLedgerFixture {
	t.Helper()
	providerPublic, providerPrivate, _ := ed25519.GenerateKey(rand.Reader)
	fixture := &exactLedgerFixture{now: time.Now().UTC(), providerPublic: providerPublic, providerPrivate: providerPrivate, keys: map[string]ed25519.PrivateKey{}}
	fixture.appendActivate(t, "trust-ledger-operator", "operator-0", 0, "")
	fixture.appendActivate(t, "host-attestation-provider", "host-0", 0, "operator-0")
	fixture.appendActivate(t, "host-attestation-provider", "host-1", 1, "host-0")
	fixture.appendRevoke(t, "host-attestation-provider", "host-0", "host-1")
	fixture.appendActivate(t, "consumer-fence-provider", "fence-0", 0, "operator-0")
	return fixture
}

func (fixture *exactLedgerFixture) appendActivate(t *testing.T, role, keyID string, epoch uint64, authorizer string) {
	t.Helper()
	publicKey, privateKey, _ := ed25519.GenerateKey(rand.Reader)
	fixture.keys[keyID] = privateKey
	fixture.appendRecord(t, role, keyID, epoch, "activate", publicKey, authorizer)
}

func (fixture *exactLedgerFixture) appendRevoke(t *testing.T, role, keyID, authorizer string) {
	t.Helper()
	privateKey := fixture.keys[keyID]
	fixture.appendRecord(t, role, keyID, fixture.keyEpoch(keyID), "revoke", privateKey.Public().(ed25519.PublicKey), authorizer)
}

func (fixture *exactLedgerFixture) appendRecord(t *testing.T, role, keyID string, epoch uint64, operation string, publicKey ed25519.PublicKey, authorizer string) {
	t.Helper()
	sequence := uint64(len(fixture.records))
	record := QoderOSTrustRootRecord{
		APIVersion: exactAuthorityAPIVersion, Kind: "QoderOSTrustRootRecord", SchemaVersion: 1,
		TrustDomainID: "host-domain", RootSequence: sequence, Role: role, KeyID: keyID, KeyEpoch: epoch, Operation: operation,
		PublicKeyEncoding: exactSignatureEncoding, Ed25519PublicKey: base64.RawURLEncoding.EncodeToString(publicKey), PublicKeyDigest: digestBytes(publicKey),
		EffectiveAt: candidateExactTimestamp(fixture.now.Add(-time.Second)), AnchorProviderIdentity: "os-anchor", AnchorProviderCounter: sequence,
	}
	if sequence > 0 {
		previous := fixture.records[sequence-1].RecordDigest
		record.PreviousRecordDigest = &previous
		authorizerID, authorizerEpoch := authorizer, fixture.keyEpoch(authorizer)
		algorithm, encoding := exactSignatureAlgorithm, exactSignatureEncoding
		record.AuthorizingKeyID, record.AuthorizingKeyEpoch = &authorizerID, &authorizerEpoch
		record.AuthorizationSignatureAlgorithm, record.AuthorizationSignatureEncoding = &algorithm, &encoding
	}
	fixture.resignRecord(t, &record, authorizer)
	fixture.records = append(fixture.records, record)
	fixture.receipts = append(fixture.receipts, fixture.anchorReceipt(t, record))
}

func (fixture *exactLedgerFixture) resignRecord(t *testing.T, record *QoderOSTrustRootRecord, authorizer string) {
	t.Helper()
	record.RecordDigest = digestRecordWithoutFields(*record, "authorizationSignature", "recordDigest")
	if authorizer == "" {
		record.AuthorizationSignature = nil
		return
	}
	signature := base64.RawURLEncoding.EncodeToString(ed25519.Sign(fixture.keys[authorizer], []byte(osTrustRootSigningDomain+record.RecordDigest)))
	record.AuthorizationSignature = &signature
}

func (fixture *exactLedgerFixture) anchorReceipt(t *testing.T, record QoderOSTrustRootRecord) QoderOSTrustAnchorReceipt {
	t.Helper()
	receipt := QoderOSTrustAnchorReceipt{
		APIVersion: exactAuthorityAPIVersion, Kind: "QoderOSTrustAnchorReceipt", SchemaVersion: 1,
		AnchorProviderIdentity: "os-anchor", TrustDomainID: record.TrustDomainID, RootSequence: record.RootSequence, RootRecordDigest: record.RecordDigest,
		PreviousRootRecordDigest: record.PreviousRecordDigest, AnchorProviderCounter: record.AnchorProviderCounter, ObservedAt: candidateExactTimestamp(fixture.now.Add(-time.Second)),
		ProviderKeyID: "anchor-key", SignatureAlgorithm: exactSignatureAlgorithm, SignatureEncoding: exactSignatureEncoding,
	}
	if len(fixture.receipts) > 0 {
		previous := fixture.receipts[len(fixture.receipts)-1].RecordDigest
		receipt.PreviousAnchorReceiptDigest = &previous
	}
	receipt.RecordDigest = digestRecordWithoutFields(receipt, "signature", "recordDigest")
	receipt.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(fixture.providerPrivate, []byte(osTrustAnchorSigningDomain+receipt.RecordDigest)))
	return receipt
}

func (fixture *exactLedgerFixture) keyEpoch(keyID string) uint64 {
	for _, record := range fixture.records {
		if record.KeyID == keyID && record.Operation == "activate" {
			return record.KeyEpoch
		}
	}
	return 0
}

func TestExactOSTrustLedgerReplayEnforcesActiveSet(t *testing.T) {
	fixture := newExactLedgerFixture(t)
	state, err := ReplayQoderOSTrustRootLedger(fixture.records, fixture.receipts, "os-anchor", "anchor-key", 0, fixture.providerPublic, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	if state.RootSequence != 4 || len(state.ActiveKeys["host-attestation-provider"]) != 1 || state.ActiveKeys["host-attestation-provider"][0].KeyID != "host-1" {
		t.Fatalf("unexpected active set: %+v", state)
	}

	t.Run("cross role authorization", func(t *testing.T) {
		broken := newExactLedgerFixture(t)
		record := &broken.records[2]
		authorizer, epoch := "operator-0", uint64(0)
		record.AuthorizingKeyID, record.AuthorizingKeyEpoch = &authorizer, &epoch
		broken.resignRecord(t, record, authorizer)
		broken.receipts[2] = broken.anchorReceiptForIndex(t, 2)
		if _, err := ReplayQoderOSTrustRootLedger(broken.records, broken.receipts, "os-anchor", "anchor-key", 0, broken.providerPublic, broken.now); err == nil {
			t.Fatal("cross-role authorization was accepted")
		}
	})
	t.Run("last active revoke", func(t *testing.T) {
		broken := newExactLedgerFixture(t)
		broken.records, broken.receipts = broken.records[:2], broken.receipts[:2]
		broken.appendRevoke(t, "host-attestation-provider", "host-0", "host-0")
		if _, err := ReplayQoderOSTrustRootLedger(broken.records, broken.receipts, "os-anchor", "anchor-key", 0, broken.providerPublic, broken.now); err == nil {
			t.Fatal("last active role key was revoked")
		}
	})
	t.Run("revoked authorizer", func(t *testing.T) {
		broken := newExactLedgerFixture(t)
		broken.appendActivate(t, "host-attestation-provider", "host-2", 2, "host-0")
		if _, err := ReplayQoderOSTrustRootLedger(broken.records, broken.receipts, "os-anchor", "anchor-key", 0, broken.providerPublic, broken.now); err == nil {
			t.Fatal("revoked key authorized a new record")
		}
	})
	for name, mutate := range map[string]func(*exactLedgerFixture){
		"sequence jump": func(value *exactLedgerFixture) { value.records[2].RootSequence++ },
		"previous mismatch": func(value *exactLedgerFixture) {
			changed := digest("f")
			value.records[2].PreviousRecordDigest = &changed
		},
		"epoch jump":   func(value *exactLedgerFixture) { value.records[2].KeyEpoch++ },
		"key id reuse": func(value *exactLedgerFixture) { value.records[2].KeyID = "host-0" },
		"public key reuse": func(value *exactLedgerFixture) {
			publicKey := value.keys["host-0"].Public().(ed25519.PublicKey)
			value.records[2].Ed25519PublicKey = base64.RawURLEncoding.EncodeToString(publicKey)
			value.records[2].PublicKeyDigest = digestBytes(publicKey)
		},
		"future effective time": func(value *exactLedgerFixture) {
			value.records[2].EffectiveAt = candidateExactTimestamp(value.now.Add(time.Hour))
		},
	} {
		t.Run(name, func(t *testing.T) {
			broken := newExactLedgerFixture(t)
			mutate(broken)
			broken.resignRecord(t, &broken.records[2], "host-0")
			broken.receipts[2] = broken.anchorReceiptForIndex(t, 2)
			if _, err := ReplayQoderOSTrustRootLedger(broken.records, broken.receipts, "os-anchor", "anchor-key", 0, broken.providerPublic, broken.now); err == nil {
				t.Fatal("invalid trust ledger was accepted")
			}
		})
	}
}

func (fixture *exactLedgerFixture) anchorReceiptForIndex(t *testing.T, index int) QoderOSTrustAnchorReceipt {
	t.Helper()
	saved := fixture.receipts
	fixture.receipts = saved[:index]
	receipt := fixture.anchorReceipt(t, fixture.records[index])
	fixture.receipts = saved
	return receipt
}

func (fixture *exactLedgerFixture) anchorReceiptForRecord(t *testing.T, record QoderOSTrustRootRecord, index int) QoderOSTrustAnchorReceipt {
	t.Helper()
	saved := fixture.receipts
	fixture.receipts = saved[:index]
	receipt := fixture.anchorReceipt(t, record)
	fixture.receipts = saved
	return receipt
}

func TestExactRootAndAnchorRejectIdentityAndIntegerOverflow(t *testing.T) {
	fixture := newExactLedgerFixture(t)
	root := fixture.records[0]
	root.RootSequence = candidateMaxJSONInteger + 1
	root.AnchorProviderCounter = root.RootSequence
	root.RecordDigest = digestRecordWithoutFields(root, "authorizationSignature", "recordDigest")
	if err := ValidateQoderOSTrustRootRecord(root, "", 0, nil, fixture.now); err == nil {
		t.Fatal("root sequence above exact JSON integer bound was accepted")
	}
	root = fixture.records[0]
	root.AnchorProviderIdentity = "replacement-anchor"
	root.RecordDigest = digestRecordWithoutFields(root, "authorizationSignature", "recordDigest")
	receipt := fixture.anchorReceiptForRecord(t, root, 0)
	if err := ValidateQoderOSTrustAnchorReceipt(receipt, root, "os-anchor", "anchor-key", 0, fixture.providerPublic, fixture.now); err == nil {
		t.Fatal("root anchor provider identity mismatch was accepted")
	}
	receipt = fixture.receipts[0]
	receipt.AnchorProviderCounter = candidateMaxJSONInteger + 1
	receipt.RecordDigest = digestRecordWithoutFields(receipt, "signature", "recordDigest")
	receipt.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(fixture.providerPrivate, []byte(osTrustAnchorSigningDomain+receipt.RecordDigest)))
	if err := ValidateQoderOSTrustAnchorReceipt(receipt, fixture.records[0], "os-anchor", "anchor-key", 0, fixture.providerPublic, fixture.now); err == nil {
		t.Fatal("anchor counter above exact JSON integer bound was accepted")
	}
}

func TestExactHostAttestationValidation(t *testing.T) {
	identity, publicKey := exactHostIdentityFixture(t)
	now, err := time.Parse(time.RFC3339Nano, identity.IssuedAt)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateHostAttestationIdentity(identity, "host-provider", "provider-key", 2, publicKey, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*HostAttestationIdentity){
		"wrong OS":         func(value *HostAttestationIdentity) { value.OS = "wrong" },
		"future":           func(value *HostAttestationIdentity) { value.IssuedAt = candidateExactTimestamp(now.Add(time.Hour)) },
		"noncanonical sig": func(value *HostAttestationIdentity) { value.Signature += "\r\n" },
		"identity tamper":  func(value *HostAttestationIdentity) { value.HostKeyID = "replacement" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := identity
			mutate(&changed)
			if err := ValidateHostAttestationIdentity(changed, "host-provider", "provider-key", 2, publicKey, now.Add(time.Second)); err == nil {
				t.Fatal("invalid host identity was accepted")
			}
		})
	}
}

func TestExactConsumerFenceReceiptsBindRequests(t *testing.T) {
	now := time.Now().UTC()
	publicKey, privateKey, _ := ed25519.GenerateKey(rand.Reader)
	genesisRequest := ConsumerFenceGenesisRequest{ConsumerInstanceID: "consumer-1", RepositoryIdentity: "repository-1"}
	genesis := ConsumerFenceReceipt{
		APIVersion: exactAuthorityAPIVersion, Kind: "QoderConsumerFenceGenesisReceipt", SchemaVersion: 1,
		ProviderIdentity: "fence-provider", ConsumerInstanceID: genesisRequest.ConsumerInstanceID, RepositoryIdentity: genesisRequest.RepositoryIdentity,
		TransactionID: "genesis", ObservedAt: candidateExactTimestamp(now.Add(-time.Second)), ProviderKeyID: "fence-key", SignatureAlgorithm: exactSignatureAlgorithm, SignatureEncoding: exactSignatureEncoding,
	}
	genesis.RecordDigest = digestRecordWithoutFields(genesis, "signature", "recordDigest")
	genesis.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, []byte(fenceGenesisSigningDomain+genesis.RecordDigest)))
	if err := ValidateConsumerFenceGenesisReceipt(genesis, genesisRequest, "fence-provider", "fence-key", 0, publicKey, now); err != nil {
		t.Fatal(err)
	}
	previous := genesis.RecordDigest
	request := ConsumerFenceAdvanceRequest{ConsumerInstanceID: "consumer-1", RepositoryIdentity: "repository-1", TransactionID: "txn-1", PreparedRecordDigest: digest("a"), AuthorityGeneration: 1, ConfigDigest: digest("b"), ExpectedPreviousReceiptDigest: &previous}
	prepared, config := request.PreparedRecordDigest, request.ConfigDigest
	receipt := ConsumerFenceReceipt{
		APIVersion: exactAuthorityAPIVersion, Kind: "QoderConsumerFenceAdvanceReceipt", SchemaVersion: 1,
		ProviderIdentity: "fence-provider", ConsumerInstanceID: request.ConsumerInstanceID, RepositoryIdentity: request.RepositoryIdentity, ProviderCounter: 1,
		TransactionID: request.TransactionID, PreparedRecordDigest: &prepared, AuthorityGeneration: request.AuthorityGeneration, ConfigDigest: &config, PreviousReceiptDigest: &previous,
		ObservedAt: candidateExactTimestamp(now.Add(-time.Second)), ProviderKeyID: "fence-key", SignatureAlgorithm: exactSignatureAlgorithm, SignatureEncoding: exactSignatureEncoding,
	}
	receipt.RecordDigest = digestRecordWithoutFields(receipt, "signature", "recordDigest")
	receipt.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, []byte(fenceAdvanceSigningDomain+receipt.RecordDigest)))
	if err := ValidateConsumerFenceReceipt(receipt, request, "fence-provider", "fence-key", 0, publicKey, now); err != nil {
		t.Fatal(err)
	}
	request.TransactionID = "txn-2"
	if err := ValidateConsumerFenceReceipt(receipt, request, "fence-provider", "fence-key", 0, publicKey, now); err == nil {
		t.Fatal("fence receipt replayed for a different transaction")
	}
	for name, verify := range map[string]func() error{
		"empty provider identity genesis": func() error {
			return ValidateConsumerFenceGenesisReceipt(genesis, genesisRequest, "", "fence-key", 0, publicKey, now)
		},
		"empty provider key genesis": func() error {
			return ValidateConsumerFenceGenesisReceipt(genesis, genesisRequest, "fence-provider", "", 0, publicKey, now)
		},
		"provider epoch overflow genesis": func() error {
			return ValidateConsumerFenceGenesisReceipt(genesis, genesisRequest, "fence-provider", "fence-key", candidateMaxJSONInteger+1, publicKey, now)
		},
		"advance counter overflow": func() error {
			overflow := request
			overflow.TransactionID = "txn-1"
			overflow.ExpectedProviderCounter = candidateMaxJSONInteger
			return ValidateConsumerFenceReceipt(receipt, overflow, "fence-provider", "fence-key", 0, publicKey, now)
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := verify(); err == nil {
				t.Fatal("invalid fence provider identity or counter was accepted")
			}
		})
	}
}

func exactHostIdentityFixture(t *testing.T) (HostAttestationIdentity, ed25519.PublicKey) {
	t.Helper()
	publicKey, privateKey, _ := ed25519.GenerateKey(rand.Reader)
	now := time.Now().UTC()
	identity := HostAttestationIdentity{
		APIVersion: exactAuthorityAPIVersion, Kind: "HostAttestationIdentity", SchemaVersion: 1,
		ProviderIdentity: "host-provider", HostKeyID: "host-key", HostPublicKeyEncoding: exactSignatureEncoding,
		HostPublicKeyDigest: digest("a"), OSAttestedMachineIdentityDigest: digest("b"), OS: runtime.GOOS, Arch: runtime.GOARCH,
		IssuedAt: candidateExactTimestamp(now), ProviderKeyID: "provider-key", ProviderKeyEpoch: 2, SignatureAlgorithm: exactSignatureAlgorithm, SignatureEncoding: exactSignatureEncoding,
	}
	identity.RecordDigest = digestRecordWithoutFields(identity, "signature", "recordDigest")
	identity.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, []byte(hostIdentitySigningDomain+identity.RecordDigest)))
	return identity, publicKey
}

func exactCanonicalDocument(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	data, err = canonical.JSON(data)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
