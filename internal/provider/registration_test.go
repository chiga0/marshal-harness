package provider

import (
	"bytes"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/schemas"
)

const (
	draft202012SchemaURI = "https://json-schema.org/draft/2020-12/schema"
	registrationSchemaID = "https://marshal.dev/schemas/v1alpha1/provider-registration.schema.json"
	snapshotSchemaID     = "https://marshal.dev/schemas/v1alpha1/provider-capability-snapshot.schema.json"
	evidenceSchemaID     = "https://marshal.dev/schemas/v1alpha1/conformance-evidence.schema.json"
)

// fixedDigest derives a well-formed sha256 digest from seed material.
func fixedDigest(seed string) string {
	return canonical.DigestBytes([]byte(seed))
}

// mustCanonicalDigest canonicalizes value under RFC 8785 JCS and returns its
// sha256 digest, failing the test on any error.
func mustCanonicalDigest(value any) string {
	digest, err := canonicalDigestOf(value)
	if err != nil {
		panic(err)
	}
	return digest
}

// setRegistrationDigest recomputes the canonical content digest of
// registration after arbitrary field mutations.
func setRegistrationDigest(registration *ProviderRegistration) {
	detached := *registration
	detached.RegistrationDigest = ""
	registration.RegistrationDigest = mustCanonicalDigest(detached)
}

func mustMarshal(value any) []byte {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return raw
}

func copyDocument(document map[string]any) map[string]any {
	clone := make(map[string]any, len(document))
	for key, value := range document {
		clone[key] = value
	}
	return clone
}

func validAttestation() Attestation {
	return Attestation{
		ProviderInstanceId: "provider-instance-1",
		ConfigDigest:       fixedDigest("effective-config-1"),
		TrustRootKeyId:     "trust-root-key-1",
		TrustRootAlgorithm: "ed25519",
	}
}

func validAuthorityNamespace() authority.AuthorityNamespaceId {
	return authority.AuthorityNamespaceId{
		TenantNamespace:  "default",
		ControlPlaneId:   "default",
		AuthorityScopeId: "marshal-harness",
	}
}

func validSecurityDomain() authority.SecurityDomainId {
	return authority.SecurityDomainId{
		TenantNamespace:   "default",
		TrustDomainKind:   authority.TrustDomainKindExecution,
		IsolationDomainId: "isolation-local",
	}
}

func validRegistration() ProviderRegistration {
	registration := ProviderRegistration{
		RegistrationId:       "registration-1",
		AuthorityNamespaceId: validAuthorityNamespace(),
		SecurityDomainId:     validSecurityDomain(),
		Principal:            "principal-local-sandbox",
		ProviderType:         "sandbox",
		ProviderName:         "local-sandbox",
		ProviderVersion:      "1.0.0",
		ProtocolVersion:      "v1alpha1",
		Scope:                "repository:marshal-harness",
		IdempotencyKey:       "idempotency-key-1",
		RequestDigest:        fixedDigest("registration-request-1"),
		Attestation:          validAttestation(),
		LifecycleState:       LifecycleStateActive,
		CreatedAt:            "2026-08-12T00:00:00Z",
	}
	setRegistrationDigest(&registration)
	return registration
}

// TestProviderRegistrationRejectsEmptyRequiredFields freezes negative fixture
// (1): any empty required field fails closed.
func TestProviderRegistrationRejectsEmptyRequiredFields(t *testing.T) {
	base := validRegistration()
	cases := []struct {
		name   string
		change func(*ProviderRegistration)
	}{
		{"empty registrationId", func(r *ProviderRegistration) { r.RegistrationId = "" }},
		{"zero authorityNamespaceId", func(r *ProviderRegistration) { r.AuthorityNamespaceId = authority.AuthorityNamespaceId{} }},
		{"zero securityDomainId", func(r *ProviderRegistration) { r.SecurityDomainId = authority.SecurityDomainId{} }},
		{"empty principal", func(r *ProviderRegistration) { r.Principal = "" }},
		{"whitespace principal", func(r *ProviderRegistration) { r.Principal = "   " }},
		{"empty providerType", func(r *ProviderRegistration) { r.ProviderType = "" }},
		{"empty providerName", func(r *ProviderRegistration) { r.ProviderName = "" }},
		{"empty providerVersion", func(r *ProviderRegistration) { r.ProviderVersion = "" }},
		{"empty protocolVersion", func(r *ProviderRegistration) { r.ProtocolVersion = "" }},
		{"empty scope", func(r *ProviderRegistration) { r.Scope = "" }},
		{"empty idempotencyKey", func(r *ProviderRegistration) { r.IdempotencyKey = "" }},
		{"empty requestDigest", func(r *ProviderRegistration) { r.RequestDigest = "" }},
		{"zero attestation", func(r *ProviderRegistration) { r.Attestation = Attestation{} }},
		{"empty lifecycleState", func(r *ProviderRegistration) { r.LifecycleState = "" }},
		{"empty createdAt", func(r *ProviderRegistration) { r.CreatedAt = "" }},
		{"zero value", func(r *ProviderRegistration) { *r = ProviderRegistration{} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mutated := base
			tc.change(&mutated)
			if err := mutated.Validate(); err == nil {
				t.Fatalf("Validate accepted %s", tc.name)
			}
			if _, err := mutated.Digest(); err == nil {
				t.Fatalf("Digest accepted %s", tc.name)
			}
		})
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("Validate rejected a valid registration: %v", err)
	}

	// A memory-only record without its canonical digest binding fails closed,
	// while the content digest itself remains computable.
	missingDigest := base
	missingDigest.RegistrationDigest = ""
	if err := missingDigest.Validate(); err == nil {
		t.Fatal("Validate accepted a registration without its canonical digest binding")
	}
	if _, err := missingDigest.Digest(); err != nil {
		t.Fatalf("Digest rejected valid content fields: %v", err)
	}

	// A tampered digest binding fails closed as well.
	tampered := base
	tampered.RegistrationDigest = fixedDigest("tampered-registration-binding")
	if err := tampered.Validate(); err == nil {
		t.Fatal("Validate accepted a registration whose digest does not match its content")
	}
}

// TestProviderRegistrationRejectsSecurityDomainIdAsAuthorityNamespaceId
// freezes negative fixture (2): the owner field can never carry an actor key
// space document, and the reverse impersonation fails closed too.
func TestProviderRegistrationRejectsSecurityDomainIdAsAuthorityNamespaceId(t *testing.T) {
	registration := validRegistration()
	raw := mustMarshal(registration)

	if _, err := ParseProviderRegistration(raw); err != nil {
		t.Fatalf("ParseProviderRegistration rejected the valid baseline document: %v", err)
	}

	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("unmarshal baseline document: %v", err)
	}

	impersonated := copyDocument(document)
	impersonated["authorityNamespaceId"] = map[string]any{
		"tenantNamespace":   "default",
		"trustDomainKind":   "execution",
		"isolationDomainId": "isolation-local",
	}
	if _, err := ParseProviderRegistration(mustMarshal(impersonated)); err == nil {
		t.Fatal("ParseProviderRegistration accepted a SecurityDomainId document as the authorityNamespaceId owner")
	}

	reversed := copyDocument(document)
	reversed["securityDomainId"] = map[string]any{
		"tenantNamespace":  "default",
		"controlPlaneId":   "default",
		"authorityScopeId": "marshal-harness",
	}
	if _, err := ParseProviderRegistration(mustMarshal(reversed)); err == nil {
		t.Fatal("ParseProviderRegistration accepted an AuthorityNamespaceId document as the actor securityDomainId")
	}
}

// TestParseProviderRegistrationRejectsDuplicateMembers guards the RFC 8785
// admission: duplicate members in a registration document fail closed.
func TestParseProviderRegistrationRejectsDuplicateMembers(t *testing.T) {
	duplicated := []byte(`{"registrationId":"registration-1","registrationId":"registration-2","principal":"principal"}`)
	if _, err := ParseProviderRegistration(duplicated); err == nil {
		t.Fatal("ParseProviderRegistration accepted a document with duplicate members")
	}
	if _, err := ParseProviderRegistration([]byte(`{"registrationId":}`)); err == nil {
		t.Fatal("ParseProviderRegistration accepted malformed JSON")
	}
}

// TestProviderRegistrationRejectsUnknownLifecycleState freezes negative
// fixture (3): lifecycleState is a closed four-value enumeration.
func TestProviderRegistrationRejectsUnknownLifecycleState(t *testing.T) {
	for _, value := range []LifecycleState{"", "created", "ACTIVE", "active ", "revoke", "expired2", "lifecycle"} {
		mutated := validRegistration()
		mutated.LifecycleState = value
		setRegistrationDigest(&mutated)
		if err := mutated.Validate(); err == nil {
			t.Fatalf("Validate accepted unknown lifecycleState %q", string(value))
		}
	}
	for _, value := range []LifecycleState{LifecycleStateCreate, LifecycleStateActive, LifecycleStateRevoked, LifecycleStateExpired} {
		legal := validRegistration()
		legal.LifecycleState = value
		setRegistrationDigest(&legal)
		if err := legal.Validate(); err != nil {
			t.Fatalf("Validate rejected legal lifecycleState %q: %v", string(value), err)
		}
	}
}

// TestProviderRegistrationRejectsSameKeyDifferentDigest freezes negative
// fixture (4): the identical septuple identity plus idempotencyKey with a
// different requestDigest is a conflict and never merges.
func TestProviderRegistrationRejectsSameKeyDifferentDigest(t *testing.T) {
	existing := validRegistration()

	conflicting := validRegistration()
	conflicting.RequestDigest = fixedDigest("registration-request-conflicting")
	setRegistrationDigest(&conflicting)

	err := existing.ValidateReplay(conflicting)
	if err == nil {
		t.Fatal("ValidateReplay merged identical identity and idempotencyKey with a different requestDigest")
	}
	if !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("expected an idempotency conflict rejection, got: %v", err)
	}

	identical := validRegistration()
	if err := existing.ValidateReplay(identical); err != nil {
		t.Fatalf("ValidateReplay rejected the identical idempotent replay: %v", err)
	}
}

// TestProviderRegistrationRejectsRevokedOrExpiredReplay freezes negative
// fixture (5): revoked and expired registrations are terminal; no ordinary
// replay resurrects them or is accepted against them in the create state.
func TestProviderRegistrationRejectsRevokedOrExpiredReplay(t *testing.T) {
	for _, terminal := range []LifecycleState{LifecycleStateRevoked, LifecycleStateExpired} {
		existing := validRegistration()
		existing.LifecycleState = terminal
		setRegistrationDigest(&existing)

		replay := validRegistration()
		err := existing.ValidateReplay(replay)
		if err == nil {
			t.Fatalf("ValidateReplay resurrected a %s registration", string(terminal))
		}
		if !strings.Contains(err.Error(), string(terminal)) {
			t.Fatalf("expected the rejection to report the %s state, got: %v", string(terminal), err)
		}

		createReplay := validRegistration()
		createReplay.LifecycleState = LifecycleStateCreate
		setRegistrationDigest(&createReplay)
		if err := existing.ValidateReplay(createReplay); err == nil {
			t.Fatalf("ValidateReplay accepted a create replay against a %s registration", string(terminal))
		}
	}
}

// TestAttestationRejectsEmptyRequiredFields freezes negative fixture (12):
// any empty attestation field fails closed.
func TestAttestationRejectsEmptyRequiredFields(t *testing.T) {
	base := validAttestation()
	cases := []struct {
		name   string
		change func(*Attestation)
	}{
		{"empty providerInstanceId", func(a *Attestation) { a.ProviderInstanceId = "" }},
		{"whitespace providerInstanceId", func(a *Attestation) { a.ProviderInstanceId = "\t" }},
		{"empty configDigest", func(a *Attestation) { a.ConfigDigest = "" }},
		{"configDigest without prefix", func(a *Attestation) { a.ConfigDigest = strings.TrimPrefix(a.ConfigDigest, DigestPrefix) }},
		{"configDigest with uppercase hex", func(a *Attestation) { a.ConfigDigest = strings.ToUpper(a.ConfigDigest) }},
		{"empty trustRootKeyId", func(a *Attestation) { a.TrustRootKeyId = "" }},
		{"empty trustRootAlgorithm", func(a *Attestation) { a.TrustRootAlgorithm = "" }},
		{"zero value", func(a *Attestation) { *a = Attestation{} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mutated := base
			tc.change(&mutated)
			if err := mutated.Validate(); err == nil {
				t.Fatalf("Validate accepted %s", tc.name)
			}
		})
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("Validate rejected a valid attestation: %v", err)
	}
}

// TestProviderRegistrationDigestDeterministic freezes negative fixture (13):
// canonical digests are deterministic, member order never changes them, and
// any field change does.
func TestProviderRegistrationDigestDeterministic(t *testing.T) {
	first := validRegistration()
	second := validRegistration()

	firstDigest, err := first.Digest()
	if err != nil {
		t.Fatalf("Digest failed for the first record: %v", err)
	}
	secondDigest, err := second.Digest()
	if err != nil {
		t.Fatalf("Digest failed for the second record: %v", err)
	}
	if firstDigest != secondDigest {
		t.Fatal("identical field values produced different registration digests")
	}
	if !strings.HasPrefix(firstDigest, DigestPrefix) {
		t.Fatal("registration digest must carry the sha256 prefix")
	}

	// JCS orders object members lexicographically at every depth.
	detached := first
	detached.RegistrationDigest = ""
	payload, err := canonical.JSON(mustMarshal(detached))
	if err != nil {
		t.Fatalf("canonical payload failed: %v", err)
	}
	attestationIndex := bytes.Index(payload, []byte(`"attestation"`))
	authorityIndex := bytes.Index(payload, []byte(`"authorityNamespaceId"`))
	if attestationIndex < 0 || authorityIndex < 0 || attestationIndex >= authorityIndex {
		t.Fatal("canonical payload members are not ordered lexicographically")
	}

	// Reordered but equivalent documents keep one canonical digest.
	orderA, err := canonical.DigestJSON([]byte(`{"a":1,"b":{"y":2,"x":1}}`))
	if err != nil {
		t.Fatalf("canonical digest failed: %v", err)
	}
	orderB, err := canonical.DigestJSON([]byte(`{"b":{"x":1,"y":2},"a":1}`))
	if err != nil {
		t.Fatalf("canonical digest failed: %v", err)
	}
	if orderA != orderB {
		t.Fatal("canonical digest changed with member order")
	}

	// Any field change must change the digest.
	changed := validRegistration()
	changed.Scope = "repository:marshal-other"
	setRegistrationDigest(&changed)
	changedDigest, err := changed.Digest()
	if err != nil {
		t.Fatalf("Digest failed for the changed record: %v", err)
	}
	if changedDigest == firstDigest {
		t.Fatal("registration digest did not change with field values")
	}

	// The idempotency identity digest is deterministic and digest-sensitive.
	firstIdentity, err := first.IdempotencyDigest()
	if err != nil {
		t.Fatalf("IdempotencyDigest failed: %v", err)
	}
	secondIdentity, err := second.IdempotencyDigest()
	if err != nil {
		t.Fatalf("IdempotencyDigest failed: %v", err)
	}
	if firstIdentity != secondIdentity {
		t.Fatal("identical idempotency bindings produced different digests")
	}
	conflicting := validRegistration()
	conflicting.RequestDigest = fixedDigest("registration-request-other")
	setRegistrationDigest(&conflicting)
	conflictingIdentity, err := conflicting.IdempotencyDigest()
	if err != nil {
		t.Fatalf("IdempotencyDigest failed: %v", err)
	}
	if conflictingIdentity == firstIdentity {
		t.Fatal("idempotency digest did not change with requestDigest")
	}
}

// jsonTagNames returns the sorted json tag names declared by typ.
func jsonTagNames(t *testing.T, typ reflect.Type) []string {
	t.Helper()
	names := make([]string, 0, typ.NumField())
	for index := 0; index < typ.NumField(); index++ {
		field := typ.Field(index)
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name == "" || name == "-" {
			t.Fatalf("struct field %s lacks an explicit json tag", field.Name)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func sortedKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// requireAlignedFields asserts that the schema declares Draft 2020-12 and
// that its property names and required list align one to one with the Go
// json fields of typ.
func requireAlignedFields(t *testing.T, raw []byte, typ reflect.Type) {
	t.Helper()
	var document struct {
		Schema     string         `json:"$schema"`
		Id         string         `json:"$id"`
		Required   []string       `json:"required"`
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("decode schema document: %v", err)
	}
	if document.Schema != draft202012SchemaURI {
		t.Fatalf("schema %s must declare Draft 2020-12, got %q", document.Id, document.Schema)
	}
	want := jsonTagNames(t, typ)
	got := sortedKeys(document.Properties)
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("schema %s properties %v do not align with the Go json fields %v", document.Id, got, want)
	}
	sort.Strings(document.Required)
	if !reflect.DeepEqual(want, document.Required) {
		t.Fatalf("schema %s required list %v does not align with the Go json fields %v", document.Id, document.Required, want)
	}
}

// requireAlignedDefinition asserts that the named $defs entry aligns one to
// one with the Go json fields of typ.
func requireAlignedDefinition(t *testing.T, raw []byte, definition string, typ reflect.Type) {
	t.Helper()
	var document struct {
		Defs map[string]struct {
			Properties map[string]any `json:"properties"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("decode schema document: %v", err)
	}
	def, present := document.Defs[definition]
	if !present {
		t.Fatalf("schema is missing $defs/%s", definition)
	}
	want := jsonTagNames(t, typ)
	got := sortedKeys(def.Properties)
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("$defs/%s properties %v do not align with the Go json fields %v", definition, got, want)
	}
}

// TestProviderSchemasCompileDraft202012AndAlignFields freezes negative
// fixture (14): all three v1alpha1 schemas compile under Draft 2020-12 and
// every field name aligns one to one with the Go domain types.
func TestProviderSchemasCompileDraft202012AndAlignFields(t *testing.T) {
	files := []struct {
		path string
		id   string
		typ  reflect.Type
	}{
		{
			path: "provider-registration.schema.json",
			id:   registrationSchemaID,
			typ:  reflect.TypeOf(ProviderRegistration{}),
		},
		{
			path: "provider-capability-snapshot.schema.json",
			id:   snapshotSchemaID,
			typ:  reflect.TypeOf(ProviderCapabilitySnapshot{}),
		},
		{
			path: "conformance-evidence.schema.json",
			id:   evidenceSchemaID,
			typ:  reflect.TypeOf(ConformanceEvidence{}),
		},
	}

	compiler := jsonschema.NewCompiler()
	rawDocuments := make(map[string][]byte, len(files))
	for _, file := range files {
		raw, err := schemas.FS.ReadFile(file.path)
		if err != nil {
			t.Fatalf("read embedded schema %s: %v", file.path, err)
		}
		rawDocuments[file.path] = raw
		requireAlignedFields(t, raw, file.typ)
		document, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("schema %s is not valid JSON: %v", file.path, err)
		}
		compiler.AddResource(file.id, document)
	}
	for _, file := range files {
		if _, err := compiler.Compile(file.id); err != nil {
			t.Fatalf("schema %s failed to compile under Draft 2020-12: %v", file.path, err)
		}
	}

	requireAlignedDefinition(t, rawDocuments["provider-registration.schema.json"], "attestation", reflect.TypeOf(Attestation{}))
	requireAlignedDefinition(t, rawDocuments["provider-registration.schema.json"], "authorityNamespaceId", reflect.TypeOf(authority.AuthorityNamespaceId{}))
	requireAlignedDefinition(t, rawDocuments["provider-registration.schema.json"], "securityDomainId", reflect.TypeOf(authority.SecurityDomainId{}))
	requireAlignedDefinition(t, rawDocuments["provider-capability-snapshot.schema.json"], "attestation", reflect.TypeOf(Attestation{}))
	requireAlignedDefinition(t, rawDocuments["conformance-evidence.schema.json"], "authorityNamespaceId", reflect.TypeOf(authority.AuthorityNamespaceId{}))
	requireAlignedDefinition(t, rawDocuments["conformance-evidence.schema.json"], "securityDomainId", reflect.TypeOf(authority.SecurityDomainId{}))
}
