package authority

import (
	"bytes"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	marshalSchemas "github.com/chiga0/marshal-harness/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func validIntent() SideEffectIntent {
	return SideEffectIntent{
		AuthorityNamespaceId: validNamespace(),
		EffectId:             "effect:1",
		OwnerIdentity:        "core",
		Port:                 "sandbox-control",
		Operation:            "allocate",
		TargetRef:            "sandbox:run-1",
		TargetDigest:         digestBytes([]byte("target")),
		RequestDigest:        digestBytes([]byte("request")),
		CommandId:            "command:1",
		IdempotencyKey:       "idempotency:1",
		PolicyDigest:         digestBytes([]byte("policy")),
		AuthorizationDigest:  digestBytes([]byte("authorization")),
		Purpose:              "forward",
		DispositionClass:     DispositionClassSandboxProvision,
		Deadline:             "2026-12-31T00:00:00Z",
	}
}

func validReceipt() SideEffectReceipt {
	return SideEffectReceipt{
		AuthorityNamespaceId:     validNamespace(),
		IntentDigest:             digestBytes([]byte("intent")),
		Disposition:              DispositionApplied,
		ProviderResourceIdentity: "sandbox:resource-1",
		ObservedDigest:           digestBytes([]byte("observed")),
		ActorProvenance:          ActorProvenance{SecurityDomainId: validSecurityDomain()},
		ReconcileIdentity:        "reconcile:1",
	}
}

func validReconcileRecord() ReconcileRecord {
	return ReconcileRecord{
		AuthorityNamespaceId: validNamespace(),
		Observation:          ObservationApplied,
		Decision:             DecisionAccept,
		IntentDigest:         digestBytes([]byte("intent")),
		ReceiptDigest:        digestBytes([]byte("receipt")),
	}
}

func TestSideEffectIntentRejectsMissingAuthorityNamespaceId(t *testing.T) {
	intent := validIntent()
	intent.AuthorityNamespaceId = AuthorityNamespaceId{}
	if err := intent.Validate(); err == nil {
		t.Fatal("Validate accepted a zero-value AuthorityNamespaceId")
	}
	if _, err := intent.Canonical(); err == nil {
		t.Fatal("Canonical accepted a zero-value AuthorityNamespaceId")
	}

	document := map[string]any{
		"effectId":            "effect:1",
		"ownerIdentity":       "core",
		"port":                "sandbox-control",
		"operation":           "allocate",
		"targetRef":           "sandbox:run-1",
		"targetDigest":        digestBytes([]byte("target")),
		"requestDigest":       digestBytes([]byte("request")),
		"commandId":           "command:1",
		"idempotencyKey":      "idempotency:1",
		"policyDigest":        digestBytes([]byte("policy")),
		"authorizationDigest": digestBytes([]byte("authorization")),
		"purpose":             "forward",
		"dispositionClass":    "sandbox-provision",
		"deadline":            "2026-12-31T00:00:00Z",
	}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("marshal document without authorityNamespaceId: %v", err)
	}
	var decoded SideEffectIntent
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal document without authorityNamespaceId: %v", err)
	}
	if err := decoded.Validate(); err == nil {
		t.Fatal("Validate accepted an intent JSON missing authorityNamespaceId")
	}

	partial := validIntent()
	partial.AuthorityNamespaceId.ControlPlaneId = ""
	if err := partial.Validate(); err == nil {
		t.Fatal("Validate accepted an intent whose authorityNamespaceId is partially empty")
	}
}

func TestSideEffectIntentRejectsSecurityDomainIdAsOwner(t *testing.T) {
	impersonatedOwner, err := json.Marshal(map[string]any{
		"tenantNamespace":   "default",
		"trustDomainKind":   "execution",
		"isolationDomainId": "isolation-1",
	})
	if err != nil {
		t.Fatalf("marshal impersonated owner: %v", err)
	}

	intentDoc := map[string]any{
		"authorityNamespaceId": json.RawMessage(impersonatedOwner),
		"effectId":             "effect:1",
		"ownerIdentity":        "core",
		"port":                 "sandbox-control",
		"operation":            "allocate",
		"targetRef":            "sandbox:run-1",
		"targetDigest":         digestBytes([]byte("target")),
		"requestDigest":        digestBytes([]byte("request")),
		"commandId":            "command:1",
		"idempotencyKey":       "idempotency:1",
		"policyDigest":         digestBytes([]byte("policy")),
		"authorizationDigest":  digestBytes([]byte("authorization")),
		"purpose":              "forward",
		"dispositionClass":     "sandbox-provision",
		"deadline":             "2026-12-31T00:00:00Z",
	}
	intentRaw, err := json.Marshal(intentDoc)
	if err != nil {
		t.Fatalf("marshal impersonated intent: %v", err)
	}
	var intent SideEffectIntent
	if err := json.Unmarshal(intentRaw, &intent); err != nil {
		t.Fatalf("unmarshal impersonated intent: %v", err)
	}
	if err := intent.Validate(); err == nil {
		t.Fatal("SideEffectIntent.Validate accepted a SecurityDomainId as authority owner")
	}

	receiptDoc := map[string]any{
		"authorityNamespaceId":     json.RawMessage(impersonatedOwner),
		"intentDigest":             digestBytes([]byte("intent")),
		"disposition":              "applied",
		"providerResourceIdentity": "sandbox:resource-1",
		"observedDigest":           digestBytes([]byte("observed")),
		"actorProvenance":          map[string]any{"securityDomainId": json.RawMessage(impersonatedOwner)},
		"reconcileIdentity":        "reconcile:1",
	}
	receiptRaw, err := json.Marshal(receiptDoc)
	if err != nil {
		t.Fatalf("marshal impersonated receipt: %v", err)
	}
	var receipt SideEffectReceipt
	if err := json.Unmarshal(receiptRaw, &receipt); err != nil {
		t.Fatalf("unmarshal impersonated receipt: %v", err)
	}
	if err := receipt.Validate(); err == nil {
		t.Fatal("SideEffectReceipt.Validate accepted a SecurityDomainId as authority owner")
	}
	if err := validReceipt().Validate(); err != nil {
		t.Fatalf("Validate rejected a valid receipt: %v", err)
	}

	reconcileDoc := map[string]any{
		"authorityNamespaceId": json.RawMessage(impersonatedOwner),
		"observation":          "applied",
		"decision":             "accept",
		"intentDigest":         digestBytes([]byte("intent")),
		"receiptDigest":        digestBytes([]byte("receipt")),
	}
	reconcileRaw, err := json.Marshal(reconcileDoc)
	if err != nil {
		t.Fatalf("marshal impersonated reconcile record: %v", err)
	}
	var record ReconcileRecord
	if err := json.Unmarshal(reconcileRaw, &record); err != nil {
		t.Fatalf("unmarshal impersonated reconcile record: %v", err)
	}
	if err := record.Validate(); err == nil {
		t.Fatal("ReconcileRecord.Validate accepted a SecurityDomainId as authority owner")
	}
	if err := validReconcileRecord().Validate(); err != nil {
		t.Fatalf("Validate rejected a valid reconcile record: %v", err)
	}
}

func TestSideEffectIntentRejectsEmptyRequiredFields(t *testing.T) {
	textCases := []struct {
		name   string
		change func(*SideEffectIntent)
	}{
		{"effectId", func(i *SideEffectIntent) { i.EffectId = "" }},
		{"ownerIdentity", func(i *SideEffectIntent) { i.OwnerIdentity = "" }},
		{"port", func(i *SideEffectIntent) { i.Port = "" }},
		{"operation", func(i *SideEffectIntent) { i.Operation = "" }},
		{"targetRef", func(i *SideEffectIntent) { i.TargetRef = "" }},
		{"commandId", func(i *SideEffectIntent) { i.CommandId = "" }},
		{"idempotencyKey", func(i *SideEffectIntent) { i.IdempotencyKey = "" }},
		{"purpose", func(i *SideEffectIntent) { i.Purpose = "" }},
		{"deadline", func(i *SideEffectIntent) { i.Deadline = "" }},
		{"whitespace purpose", func(i *SideEffectIntent) { i.Purpose = "  " }},
	}
	for _, tc := range textCases {
		t.Run(tc.name, func(t *testing.T) {
			intent := validIntent()
			tc.change(&intent)
			if err := intent.Validate(); err == nil {
				t.Fatalf("Validate accepted empty %s", tc.name)
			}
		})
	}
	digestCases := []struct {
		name   string
		change func(*SideEffectIntent)
	}{
		{"targetDigest", func(i *SideEffectIntent) { i.TargetDigest = "" }},
		{"requestDigest", func(i *SideEffectIntent) { i.RequestDigest = "" }},
		{"policyDigest", func(i *SideEffectIntent) { i.PolicyDigest = "" }},
		{"authorizationDigest", func(i *SideEffectIntent) { i.AuthorizationDigest = "" }},
		{"prefix-only targetDigest", func(i *SideEffectIntent) { i.TargetDigest = DigestPrefix }},
	}
	for _, tc := range digestCases {
		t.Run(tc.name, func(t *testing.T) {
			intent := validIntent()
			tc.change(&intent)
			if err := intent.Validate(); err == nil {
				t.Fatalf("Validate accepted empty %s", tc.name)
			}
		})
	}
	t.Run("non RFC 3339 deadline", func(t *testing.T) {
		intent := validIntent()
		intent.Deadline = "2026-12-31"
		if err := intent.Validate(); err == nil {
			t.Fatal("Validate accepted a deadline that is not RFC 3339")
		}
	})
	t.Run("zero-time deadline", func(t *testing.T) {
		intent := validIntent()
		intent.Deadline = "0001-01-01T00:00:00Z"
		if err := intent.Validate(); err == nil {
			t.Fatal("Validate accepted the zero time as deadline")
		}
	})
	if err := validIntent().Validate(); err != nil {
		t.Fatalf("Validate rejected a valid intent: %v", err)
	}
	for _, class := range []DispositionClass{
		DispositionClassSandboxProvision,
		DispositionClassSandboxStage,
		DispositionClassSandboxTerminate,
		DispositionClassLocalCleanup,
	} {
		if err := class.Validate(); err != nil {
			t.Fatalf("Validate rejected legal dispositionClass %q: %v", class, err)
		}
	}
}

func TestSideEffectIntentRejectsUnknownDispositionClass(t *testing.T) {
	for _, value := range []string{"", "rollback", "ephemeral-cleanup", "SANDBOX-PROVISION", "sandbox-provision ", "compensatable"} {
		intent := validIntent()
		intent.DispositionClass = DispositionClass(value)
		if err := intent.Validate(); err == nil {
			t.Fatalf("Validate accepted unknown dispositionClass %q", value)
		}
		if err := DispositionClass(value).Validate(); err == nil {
			t.Fatalf("DispositionClass.Validate accepted unknown value %q", value)
		}
	}
}

func TestSideEffectReceiptRejectsUnknownDisposition(t *testing.T) {
	for _, value := range []string{"", "succeeded", "failed", "APPLIED", "not-applied", "applied "} {
		receipt := validReceipt()
		receipt.Disposition = Disposition(value)
		if err := receipt.Validate(); err == nil {
			t.Fatalf("Validate accepted unknown disposition %q", value)
		}
		if err := Disposition(value).Validate(); err == nil {
			t.Fatalf("Disposition.Validate accepted unknown value %q", value)
		}
	}
	for _, disposition := range []Disposition{
		DispositionApplied,
		DispositionNotApplied,
		DispositionAmbiguous,
		DispositionConflict,
	} {
		if err := disposition.Validate(); err != nil {
			t.Fatalf("Validate rejected legal disposition %q: %v", disposition, err)
		}
	}
}

func TestReconcileRecordRejectsUnknownObservation(t *testing.T) {
	for _, value := range []string{"", "present", "APPLIED", "partially-applied", "unknown "} {
		record := validReconcileRecord()
		record.Observation = Observation(value)
		if err := record.Validate(); err == nil {
			t.Fatalf("Validate accepted unknown observation %q", value)
		}
		if err := Observation(value).Validate(); err == nil {
			t.Fatalf("Observation.Validate accepted unknown value %q", value)
		}
	}
	for _, observation := range []Observation{
		ObservationAbsent,
		ObservationApplied,
		ObservationPartiallyApplied,
		ObservationConflict,
		ObservationUnknown,
	} {
		if err := observation.Validate(); err != nil {
			t.Fatalf("Validate rejected legal observation %q: %v", observation, err)
		}
	}
}

func TestReconcileRecordRejectsUnknownDecision(t *testing.T) {
	for _, value := range []string{"", "approve", "ABORT", "Accept", "block "} {
		record := validReconcileRecord()
		record.Decision = Decision(value)
		if err := record.Validate(); err == nil {
			t.Fatalf("Validate accepted unknown decision %q", value)
		}
		if err := Decision(value).Validate(); err == nil {
			t.Fatalf("Decision.Validate accepted unknown value %q", value)
		}
	}
	for _, decision := range []Decision{
		DecisionAccept,
		DecisionRetry,
		DecisionCleanup,
		DecisionCompensate,
		DecisionBlock,
	} {
		if err := decision.Validate(); err != nil {
			t.Fatalf("Validate rejected legal decision %q: %v", decision, err)
		}
	}
}

func TestSideEffectReceiptRejectsBadIntentDigestPrefix(t *testing.T) {
	for _, value := range []string{
		string(digestBytes([]byte("intent")))[len(DigestPrefix):],
		"sha256",
		"SHA256:" + string(digestBytes([]byte("intent")))[len(DigestPrefix):],
		DigestPrefix,
		"",
	} {
		receipt := validReceipt()
		receipt.IntentDigest = value
		if err := receipt.Validate(); err == nil {
			t.Fatalf("Validate accepted intentDigest %q without the %s prefix", value, DigestPrefix)
		}
		if _, err := receipt.Canonical(); err == nil {
			t.Fatalf("Canonical accepted intentDigest %q without the %s prefix", value, DigestPrefix)
		}
	}
}

func TestAuthorityRecordsCanonicalDigestIsDeterministic(t *testing.T) {
	assertDeterministic := func(t *testing.T, name string, first, second canonicalRecord) {
		t.Helper()
		firstCanonical, err := first.Canonical()
		if err != nil {
			t.Fatalf("%s Canonical failed: %v", name, err)
		}
		secondCanonical, err := second.Canonical()
		if err != nil {
			t.Fatalf("%s Canonical failed: %v", name, err)
		}
		if !bytes.Equal(firstCanonical, secondCanonical) {
			t.Fatalf("%s canonical bytes differ for equal values:\n%s\n%s", name, firstCanonical, secondCanonical)
		}
		if len(firstCanonical) == 0 || bytes.Contains(firstCanonical, []byte("\n")) {
			t.Fatalf("%s canonical bytes contain whitespace or are empty: %q", name, firstCanonical)
		}
		digest, err := digestOf(first)
		if err != nil {
			t.Fatalf("%s Digest failed: %v", name, err)
		}
		digestAgain, err := digestOf(second)
		if err != nil {
			t.Fatalf("%s Digest failed: %v", name, err)
		}
		if digest != digestAgain {
			t.Fatalf("%s digests differ for equal values: %s != %s", name, digest, digestAgain)
		}
		if !strings.HasPrefix(digest, DigestPrefix) || len(digest) != len(DigestPrefix)+64 {
			t.Fatalf("%s digest %q is not a sha256 hex digest", name, digest)
		}
	}

	assertDeterministic(t, "AuthorityNamespaceId", validNamespace(), validNamespace())
	assertDeterministic(t, "SecurityDomainId", validSecurityDomain(), validSecurityDomain())
	assertDeterministic(t, "SideEffectIntent", validIntent(), validIntent())
	assertDeterministic(t, "SideEffectReceipt", validReceipt(), validReceipt())
	assertDeterministic(t, "ReconcileRecord", validReconcileRecord(), validReconcileRecord())

	shuffledIntent := `{"deadline":"2026-12-31T00:00:00Z","purpose":"forward","dispositionClass":"sandbox-provision",` +
		`"authorizationDigest":"` + validIntent().AuthorizationDigest + `","policyDigest":"` + validIntent().PolicyDigest + `",` +
		`"idempotencyKey":"idempotency:1","commandId":"command:1","requestDigest":"` + validIntent().RequestDigest + `",` +
		`"targetDigest":"` + validIntent().TargetDigest + `","targetRef":"sandbox:run-1","operation":"allocate",` +
		`"port":"sandbox-control","ownerIdentity":"core","effectId":"effect:1",` +
		`"authorityNamespaceId":{"authorityScopeId":"marshal-harness","controlPlaneId":"default","tenantNamespace":"default"}}`
	var shuffled SideEffectIntent
	if err := json.Unmarshal([]byte(shuffledIntent), &shuffled); err != nil {
		t.Fatalf("unmarshal shuffled intent JSON: %v", err)
	}
	referenceDigest, err := validIntent().Digest()
	if err != nil {
		t.Fatalf("Digest failed: %v", err)
	}
	shuffledDigest, err := shuffled.Digest()
	if err != nil {
		t.Fatalf("Digest failed for shuffled input: %v", err)
	}
	if shuffledDigest != referenceDigest {
		t.Fatalf("intent digest changed with JSON field order: %s != %s", shuffledDigest, referenceDigest)
	}
	if !shuffled.Equal(validIntent()) {
		t.Fatal("Equal rejected intents decoded from shuffled JSON")
	}

	mutated := validIntent()
	mutated.CommandId = "command:2"
	mutatedDigest, err := mutated.Digest()
	if err != nil {
		t.Fatalf("Digest failed: %v", err)
	}
	if mutatedDigest == referenceDigest {
		t.Fatal("different intents produced the same digest")
	}
	if mutated.Equal(validIntent()) {
		t.Fatal("Equal accepted different intents")
	}
}

func TestAuthoritySchemasCompileAgainstMetaschema(t *testing.T) {
	cases := []struct {
		file   string
		id     string
		record canonicalRecord
	}{
		{
			file:   "authority-namespace.schema.json",
			id:     "https://marshal.dev/schemas/v1alpha1/authority-namespace.schema.json",
			record: validNamespace(),
		},
		{
			file:   "side-effect-intent.schema.json",
			id:     "https://marshal.dev/schemas/v1alpha1/side-effect-intent.schema.json",
			record: validIntent(),
		},
		{
			file:   "side-effect-receipt.schema.json",
			id:     "https://marshal.dev/schemas/v1alpha1/side-effect-receipt.schema.json",
			record: validReceipt(),
		},
		{
			file:   "reconcile-record.schema.json",
			id:     "https://marshal.dev/schemas/v1alpha1/reconcile-record.schema.json",
			record: validReconcileRecord(),
		},
	}

	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.AssertFormat()

	for _, tc := range cases {
		data, err := marshalSchemas.FS.ReadFile(tc.file)
		if err != nil {
			t.Fatalf("read schema %s: %v", tc.file, err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("decode schema %s: %v", tc.file, err)
		}
		if decoded["$schema"] != "https://json-schema.org/draft/2020-12/schema" {
			t.Fatalf("schema %s is not Draft 2020-12", tc.file)
		}
		if decoded["$id"] != tc.id {
			t.Fatalf("schema %s has unexpected $id %v", tc.file, decoded["$id"])
		}
		if decoded["additionalProperties"] != false {
			t.Fatalf("schema %s must set additionalProperties to false", tc.file)
		}

		properties, ok := decoded["properties"].(map[string]any)
		if !ok {
			t.Fatalf("schema %s has no properties object", tc.file)
		}
		propertyNames := make([]string, 0, len(properties))
		for name := range properties {
			propertyNames = append(propertyNames, name)
		}
		slices.Sort(propertyNames)

		requiredRaw, ok := decoded["required"].([]any)
		if !ok {
			t.Fatalf("schema %s has no required array", tc.file)
		}
		requiredNames := make([]string, 0, len(requiredRaw))
		for _, entry := range requiredRaw {
			name, ok := entry.(string)
			if !ok {
				t.Fatalf("schema %s required entry is not a string", tc.file)
			}
			requiredNames = append(requiredNames, name)
		}
		slices.Sort(requiredNames)

		fieldNames := jsonFieldNames(tc.record)
		if !slices.Equal(propertyNames, fieldNames) {
			t.Fatalf("schema %s properties %v do not align with Go json fields %v", tc.file, propertyNames, fieldNames)
		}
		if !slices.Equal(requiredNames, fieldNames) {
			t.Fatalf("schema %s required %v does not cover exactly the Go json fields %v", tc.file, requiredNames, fieldNames)
		}

		document, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("unmarshal schema %s: %v", tc.file, err)
		}
		if err := compiler.AddResource(tc.id, document); err != nil {
			t.Fatalf("register schema %s: %v", tc.file, err)
		}
	}

	for _, tc := range cases {
		schema, err := compiler.Compile(tc.id)
		if err != nil {
			t.Fatalf("compile schema %s: %v", tc.file, err)
		}

		canonical, err := tc.record.Canonical()
		if err != nil {
			t.Fatalf("canonical for %s: %v", tc.file, err)
		}
		document, err := jsonschema.UnmarshalJSON(bytes.NewReader(canonical))
		if err != nil {
			t.Fatalf("unmarshal canonical for %s: %v", tc.file, err)
		}
		if err := schema.Validate(document); err != nil {
			t.Fatalf("schema %s rejected the canonical Go record: %v", tc.file, err)
		}

		var expanded map[string]any
		if err := json.Unmarshal(canonical, &expanded); err != nil {
			t.Fatalf("decode canonical for %s: %v", tc.file, err)
		}
		expanded["unexpectedField"] = "pollution"
		polluted, err := json.Marshal(expanded)
		if err != nil {
			t.Fatalf("marshal polluted record for %s: %v", tc.file, err)
		}
		pollutedDocument, err := jsonschema.UnmarshalJSON(bytes.NewReader(polluted))
		if err != nil {
			t.Fatalf("unmarshal polluted record for %s: %v", tc.file, err)
		}
		if err := schema.Validate(pollutedDocument); err == nil {
			t.Fatalf("schema %s accepted an unexpected extra property", tc.file)
		}
	}
}

type canonicalRecord interface {
	Validate() error
	Canonical() ([]byte, error)
}

func digestOf(record canonicalRecord) (string, error) {
	canonical, err := record.Canonical()
	if err != nil {
		return "", err
	}
	return digestBytes(canonical), nil
}

func jsonFieldNames(record any) []string {
	typ := reflect.TypeOf(record)
	names := make([]string, 0, typ.NumField())
	for index := 0; index < typ.NumField(); index++ {
		name, _, _ := strings.Cut(typ.Field(index).Tag.Get("json"), ",")
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}
