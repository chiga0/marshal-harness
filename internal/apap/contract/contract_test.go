package contract

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const d = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const d2 = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func fixedNow() time.Time { return time.Date(2026, 8, 18, 8, 0, 0, 0, time.UTC) }

func TestDraft202012SchemaAndExamples(t *testing.T) {
	root := repositoryRoot()
	schema := compileAPAPSchema(t)
	examples, err := filepath.Glob(filepath.Join(root, "schemas/apap/examples/*.json"))
	if err != nil || len(examples) == 0 {
		t.Fatalf("examples: %v", err)
	}
	for _, path := range examples {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		value, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("decode schema example %s: %v", filepath.Base(path), err)
		}
		if err := schema.Validate(value); err != nil {
			t.Fatalf("schema example %s rejected: %v", filepath.Base(path), err)
		}
		packet := bytes.TrimSuffix(data, []byte("\n"))
		switch filepath.Base(path) {
		case "describe-request.json":
			if _, err := ValidateControlRequest(packet, nil, fixedNow()); err != nil {
				t.Fatalf("strict example admission: %v", err)
			}
		case "begin-probe-request.json":
			if _, err := ValidateControlRequest(packet, beginFDs(d), fixedNow()); err != nil {
				t.Fatalf("strict example admission: %v", err)
			}
		case "describe-response.json":
			if _, err := ValidateControlResponse(packet, describeRequest(), 7); err != nil {
				t.Fatalf("strict example admission: %v", err)
			}
		case "fd-table.json":
			if _, _, err := ValidateFDTable(packet); err != nil {
				t.Fatalf("strict example admission: %v", err)
			}
		case "signed-object-envelope.json":
			if err := ValidateSignedObjectEnvelope(packet, "marshal-bundle-prepared-receipt-v1\x00"); err != nil {
				t.Fatalf("strict example admission: %v", err)
			}
		default:
			t.Fatalf("unrouted schema example %s", filepath.Base(path))
		}
	}
}

func TestSchemaAndSemanticAdmissionRejectOrderingViolations(t *testing.T) {
	schema := compileAPAPSchema(t)
	describe := responseVector(t, Describe, "ok", "", map[string]any{
		"providerBuildDigest": d,
		"platform":            "linux",
		"profiles":            []string{"qoder-cli-adr0034-v1", "codex-cli-adr0037-v1"},
	})
	fdWrongRoleOrder := canonicalValue(t, map[string]any{
		"schemaVersion": FDTableSchema,
		"operation":     BeginProbe,
		"descriptors": []any{
			map[string]any{"role": "scratchRoot", "index": 0, "identityDigest": d},
			map[string]any{"role": "candidateExecutable", "index": 0, "identityDigest": d},
			map[string]any{"role": "businessDenyRoot", "index": 0, "identityDigest": d},
		},
	})
	fdWrongIndexOrder := canonicalValue(t, map[string]any{
		"schemaVersion": FDTableSchema,
		"operation":     BeginProbe,
		"descriptors": []any{
			map[string]any{"role": "candidateExecutable", "index": 0, "identityDigest": d},
			map[string]any{"role": "scratchRoot", "index": 0, "identityDigest": d},
			map[string]any{"role": "businessDenyRoot", "index": 1, "identityDigest": d},
		},
	})
	for name, raw := range map[string][]byte{
		"unsorted-profiles": describe,
		"wrong-role-order":  fdWrongRoleOrder,
		"wrong-index-order": fdWrongIndexOrder,
	} {
		t.Run(name, func(t *testing.T) {
			value, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			if err := schema.Validate(value); err == nil {
				t.Fatal("Draft 2020-12 schema accepted ordering violation")
			}
			switch name {
			case "unsorted-profiles":
				if _, err := ValidateControlResponse(raw, describeRequest(), 7); err == nil {
					t.Fatal("semantic admission accepted unsorted profiles")
				}
			default:
				if _, _, err := ValidateFDTable(raw); err == nil {
					t.Fatal("semantic admission accepted fd ordering violation")
				}
			}
		})
	}
}

func TestUintAdmissionRejectsNullAndNonCanonicalTokens(t *testing.T) {
	for name, raw := range map[string][]byte{
		"null": []byte("null"), "string": []byte(`"0"`), "bool": []byte("true"),
		"object": []byte("{}"), "array": []byte("[]"), "fraction": []byte("1.0"),
		"exponent": []byte("1e0"), "positive-sign": []byte("+1"), "negative": []byte("-1"),
		"leading-zero": []byte("01"), "overflow": []byte("18446744073709551616"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, ok := rawUint(raw); ok {
				t.Fatal("invalid uint64 token accepted")
			}
		})
	}
	for _, raw := range [][]byte{[]byte("0"), []byte("18446744073709551615")} {
		if _, ok := rawUint(raw); !ok {
			t.Fatalf("valid uint64 token %s rejected", raw)
		}
	}

	// Recompute the detached response digest after substituting null so this
	// vector proves numeric admission, not stale-digest rejection.
	response := responseVector(t, Describe, "ok", "", map[string]any{"providerBuildDigest": d, "platform": "linux", "profiles": []string{"qoder-cli-adr0034-v1"}})
	response = resignResponse(t, response, "observedProviderSequence", nil)
	if _, err := ValidateControlResponse(response, describeRequest(), 0); err == nil {
		t.Fatal("re-digested null observedProviderSequence accepted as zero")
	}
	safeMessage := responseVector(t, Describe, "ok", "", map[string]any{"providerBuildDigest": d, "platform": "linux", "profiles": []string{"qoder-cli-adr0034-v1"}})
	safeMessage = resignResponse(t, safeMessage, "safeMessage", nil)
	if _, err := ValidateControlResponse(safeMessage, describeRequest(), 7); err == nil {
		t.Fatal("re-digested null safeMessage accepted as empty string")
	}

	domain := "marshal-bundle-prepared-receipt-v1\x00"
	signature := base64.RawURLEncoding.EncodeToString(make([]byte, 64))
	envelope := canonicalValue(t, map[string]any{"objectDigest": d, "signatureAlgorithm": "Ed25519", "signatureEncoding": "base64url-unpadded", "keyId": "key", "keyEpoch": nil, "signatureDomain": domain, "signature": signature})
	if err := ValidateSignedObjectEnvelope(envelope, domain); err == nil {
		t.Fatal("null keyEpoch accepted as valid epoch zero")
	}

	fdTable := canonicalValue(t, map[string]any{
		"schemaVersion": FDTableSchema,
		"operation":     BeginProbe,
		"descriptors": []any{
			map[string]any{"role": "candidateExecutable", "index": nil, "identityDigest": d},
			map[string]any{"role": "scratchRoot", "index": 0, "identityDigest": d},
			map[string]any{"role": "businessDenyRoot", "index": 0, "identityDigest": d},
		},
	})
	if _, _, err := ValidateFDTable(fdTable); err == nil {
		t.Fatal("null fd index accepted as valid index zero")
	}

	for name, raw := range map[string][]byte{"response-null": response, "safe-message-null": safeMessage, "epoch-null": envelope, "fd-index-null": fdTable} {
		t.Run("schema-"+name, func(t *testing.T) {
			value, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			if err := compileAPAPSchema(t).Validate(value); err == nil {
				t.Fatal("schema accepted null numeric field")
			}
		})
	}
}

func TestOnlyDescribeAndBeginProbeRegistered(t *testing.T) {
	now := fixedNow()
	describeRaw := requestVector(t, Describe, nil, struct{}{}, now)
	if _, err := ValidateControlRequest(describeRaw, nil, now); err != nil {
		t.Fatal(err)
	}
	beginRaw := requestVector(t, BeginProbe, uint64(7), beginPayload(now, d), now)
	if _, err := ValidateControlRequest(beginRaw, beginFDs(d), now); err != nil {
		t.Fatal(err)
	}
	for _, op := range []Operation{"RunProbeVariant", "PrepareLaunch"} {
		raw := requestVector(t, op, uint64(7), map[string]any{}, now)
		if _, err := ValidateControlRequest(raw, nil, now); err == nil {
			t.Fatalf("unregistered operation %q accepted", op)
		}
	}
}

func TestStrictRequestNegatives(t *testing.T) {
	now := fixedNow()
	valid := requestVector(t, BeginProbe, uint64(1), beginPayload(now, d), now)
	fds := beginFDs(d)
	mutations := map[string][]byte{
		"unknown":            bytes.Replace(valid, []byte(`"audience":`), []byte(`"extra":true,"audience":`), 1),
		"duplicate":          bytes.Replace(valid, []byte(`"audience":`), []byte(`"audience":"marshal.agent-production-authority.local","audience":`), 1),
		"nested-duplicate":   bytes.Replace(valid, []byte(`"candidateIdentityDigest":`), []byte(`"candidateIdentityDigest":"`+d+`","candidateIdentityDigest":`), 1),
		"wrong-type":         bytes.Replace(valid, []byte(`"protocolVersion":1`), []byte(`"protocolVersion":"1"`), 1),
		"null":               bytes.Replace(valid, []byte(`"payload":{`), []byte(`"payload":null,"ignored":{`), 1),
		"noncanonical-order": append([]byte(" "), valid...),
		"substitution":       bytes.Replace(valid, []byte(`"operation":"BeginProbe"`), []byte(`"operation":"RunProbeVariant"`), 1),
		"bad-digest":         bytes.Replace(valid, []byte(d), []byte("sha256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"), 1),
		"bad-time":           bytes.Replace(valid, []byte("2026-08-18T08:02:00Z"), []byte("2026-08-18T08:02:00+00:00"), 1),
	}
	for name, raw := range mutations {
		t.Run(name, func(t *testing.T) {
			if _, err := ValidateControlRequest(raw, fds, now); err == nil {
				t.Fatal("mutation accepted")
			}
		})
	}
	for name, validationNow := range map[string]time.Time{"zero-now": {}, "issued-in-future": now.Add(-time.Second), "expired": now.Add(3 * time.Minute)} {
		t.Run(name, func(t *testing.T) {
			if _, err := ValidateControlRequest(valid, fds, validationNow); err == nil {
				t.Fatal("freshness violation accepted")
			}
		})
	}
}

func TestBeginProbeExactDeadlineAndCandidateFDBinding(t *testing.T) {
	now := fixedNow()
	mismatchedDeadline := requestVector(t, BeginProbe, uint64(1), map[string]any{"candidateIdentityDigest": d, "suiteDigest": d, "probeArtifactDigest": d, "policyDigest": d, "challengeDigest": d, "deadline": now.Add(time.Minute).Format(time.RFC3339Nano)}, now)
	if _, err := ValidateControlRequest(mismatchedDeadline, beginFDs(d), now); err == nil {
		t.Fatal("deadline different from envelope expiresAt accepted")
	}
	// requestVector recomputes requestEnvelopeDigest, proving a correctly
	// re-signed payload cannot substitute a different held candidate identity.
	substituted := requestVector(t, BeginProbe, uint64(1), beginPayload(now, d2), now)
	if _, err := ValidateControlRequest(substituted, beginFDs(d), now); err == nil {
		t.Fatal("re-signed candidate fd substitution accepted")
	}
	valid := requestVector(t, BeginProbe, uint64(1), beginPayload(now, d), now)
	for name, fds := range map[string][]FDDescriptor{
		"cardinality": beginFDs(d)[:2],
		"order":       {{"scratchRoot", 0, d}, {"candidateExecutable", 0, d}, {"businessDenyRoot", 0, d}},
		"deny-index":  {{"candidateExecutable", 0, d}, {"scratchRoot", 0, d}, {"businessDenyRoot", 1, d}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ValidateControlRequest(valid, fds, now); err == nil {
				t.Fatal("invalid fd table accepted")
			}
		})
	}
}

func TestDescribeProfilesAreExactSortedUniqueStrings(t *testing.T) {
	request := describeRequest()
	for name, profiles := range map[string]any{
		"objects":   []any{map[string]any{"authorityProfile": "qoder-cli-adr0034-v1", "status": "unsupported"}},
		"unsorted":  []string{"qoder-cli-adr0034-v1", "codex-cli-adr0037-v1"},
		"duplicate": []string{"qoder-cli-adr0034-v1", "qoder-cli-adr0034-v1"},
		"unknown":   []string{"other"},
		"empty":     []string{},
		"too-many":  []string{"codex-cli-adr0037-v1", "qoder-cli-adr0034-v1", "qoder-cli-adr0034-v1"},
	} {
		t.Run(name, func(t *testing.T) {
			raw := responseVector(t, Describe, "ok", "", map[string]any{"providerBuildDigest": d, "platform": "linux", "profiles": profiles})
			if _, err := ValidateControlResponse(raw, request, 7); err == nil {
				t.Fatal("invalid Describe profiles accepted")
			}
		})
	}
	valid := responseVector(t, Describe, "ok", "", map[string]any{"providerBuildDigest": d, "platform": "linux", "profiles": []string{"codex-cli-adr0037-v1", "qoder-cli-adr0034-v1"}})
	if _, err := ValidateControlResponse(valid, request, 7); err != nil {
		t.Fatal(err)
	}
}

func TestResponseBindsRequestSequenceMessageAndExpiry(t *testing.T) {
	now := fixedNow()
	describe := describeRequest()
	success := responseVector(t, Describe, "ok", "", map[string]any{"providerBuildDigest": d, "platform": "linux", "profiles": []string{"qoder-cli-adr0034-v1"}})
	if _, err := ValidateControlResponse(success, describe, 7); err != nil {
		t.Fatal(err)
	}
	failure := responseVector(t, Describe, "platform-unsupported", "request rejected: platform-unsupported", nil)
	if _, err := ValidateControlResponse(failure, describe, 7); err != nil {
		t.Fatal(err)
	}
	for name, fieldValue := range map[string]any{"requestId": "other-request", "commandId": "other-command", "providerInstanceId": "other-provider", "authorityProfile": "codex-cli-adr0037-v1", "operation": "BeginProbe", "observedProviderSequence": uint64(8)} {
		t.Run(name, func(t *testing.T) {
			mutated := resignResponse(t, success, name, fieldValue)
			if _, err := ValidateControlResponse(mutated, describe, 7); err == nil {
				t.Fatal("re-signed response identity substitution accepted")
			}
		})
	}
	wrongMessage := responseVector(t, Describe, "platform-unsupported", "unsupported", nil)
	if _, err := ValidateControlResponse(wrongMessage, describe, 7); err == nil {
		t.Fatal("non-canonical safe message accepted")
	}
	smuggled := responseVector(t, Describe, "platform-unsupported", "request rejected: platform-unsupported", map[string]any{})
	if _, err := ValidateControlResponse(smuggled, describe, 7); err == nil {
		t.Fatal("failure success payload accepted")
	}
	beginRaw := requestVector(t, BeginProbe, uint64(7), beginPayload(now, d), now)
	begin, err := ValidateControlRequest(beginRaw, beginFDs(d), now)
	if err != nil {
		t.Fatal(err)
	}
	beginResponse := responseVector(t, BeginProbe, "ok", "", map[string]any{"probeSessionId": "probe-123456", "targetIsolationIdentityDigest": d, "credentialIngressEndpointIdentityDigest": d, "expiresAt": now.Add(time.Minute).Format(time.RFC3339Nano)})
	if _, err := ValidateControlResponse(beginResponse, begin, 7); err == nil {
		t.Fatal("response expiry substitution accepted")
	}
	exactBeginResponse := responseVector(t, BeginProbe, "ok", "", map[string]any{"probeSessionId": "probe-123456", "targetIsolationIdentityDigest": d, "credentialIngressEndpointIdentityDigest": d, "expiresAt": begin.ExpiresAt.Format(time.RFC3339Nano)})
	if _, err := ValidateControlResponse(exactBeginResponse, begin, 7); err != nil {
		t.Fatalf("exact BeginProbe response rejected: %v", err)
	}
}

func TestSignedEnvelopeMatchesMainBoundsAndStrictBase64URL(t *testing.T) {
	signature := base64.RawURLEncoding.EncodeToString(make([]byte, 64))
	domain := "marshal-bundle-prepared-receipt-v1\x00"
	for _, epoch := range []uint64{0, 7} {
		raw := signedVector(t, "printable key !~", epoch, domain, signature)
		if err := ValidateSignedObjectEnvelope(raw, domain); err != nil {
			t.Fatalf("epoch %d rejected: %v", epoch, err)
		}
	}
	for name, raw := range map[string][]byte{
		"epoch-overflow": signedVector(t, "key", uint64(math.MaxInt64)+1, domain, signature),
		"control-key":    signedVector(t, "bad\nkey", 0, domain, signature),
		"long-key":       signedVector(t, string(bytes.Repeat([]byte{'x'}, 257)), 0, domain, signature),
		"padding":        signedVector(t, "key", 0, domain, signature+"=="),
		"short":          signedVector(t, "key", 0, domain, signature[:84]),
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateSignedObjectEnvelope(raw, domain); err == nil {
				t.Fatal("invalid signed envelope accepted")
			}
		})
	}
}

func TestJCSGoldenVector(t *testing.T) {
	input := []byte(`{"z":1.0,"nested":{"β":2,"a":1},"a":"x"}`)
	want := `{"a":"x","nested":{"a":1,"β":2},"z":1}`
	got, err := canonical.JSON(input)
	if err != nil || string(got) != want {
		t.Fatalf("JCS = %s, %v", got, err)
	}
	if canonical.DigestBytes(got) != "sha256:2a6bf4e3f4883dd9301845db8d6115986bda5099c55d1660363ae0f3c37d6083" {
		t.Fatalf("golden digest drifted: %s", canonical.DigestBytes(got))
	}
}

func FuzzControlRequestAdmission(f *testing.F) {
	f.Add(requestVectorBytes(Describe, nil, struct{}{}, fixedNow()))
	f.Add([]byte(`{"x":1,"x":2}`))
	f.Add([]byte(`null`))
	f.Fuzz(func(t *testing.T, raw []byte) { _, _ = ValidateControlRequest(raw, nil, fixedNow()) })
}

func beginPayload(now time.Time, candidate string) map[string]any {
	return map[string]any{"candidateIdentityDigest": candidate, "suiteDigest": d, "probeArtifactDigest": d, "policyDigest": d, "challengeDigest": d, "deadline": now.Add(2 * time.Minute).Format(time.RFC3339Nano)}
}
func beginFDs(candidate string) []FDDescriptor {
	return []FDDescriptor{{"candidateExecutable", 0, candidate}, {"scratchRoot", 0, d}, {"businessDenyRoot", 0, d}}
}
func describeRequest() ValidatedRequest {
	return ValidatedRequest{Operation: Describe, AuthorityProfile: "qoder-cli-adr0034-v1", RequestID: "request-123456", CommandID: "command-123456", ProviderInstanceID: "provider-123456", IssuedAt: fixedNow(), ExpiresAt: fixedNow().Add(2 * time.Minute)}
}
func requestVector(t *testing.T, op Operation, sequence any, payload any, now time.Time) []byte {
	t.Helper()
	return requestVectorBytes(op, sequence, payload, now)
}
func requestVectorBytes(op Operation, sequence any, payload any, now time.Time) []byte {
	object := map[string]any{"schemaVersion": RequestSchema, "protocolFamily": ControlFamily, "protocolVersion": 1, "audience": ControlAudience, "requestId": "request-123456", "commandId": "command-123456", "callerPrincipalDigest": d, "providerInstanceId": "provider-123456", "authorityProfile": "qoder-cli-adr0034-v1", "operation": op, "issuedAt": now.Format(time.RFC3339Nano), "expiresAt": now.Add(2 * time.Minute).Format(time.RFC3339Nano), "nonce": "nonce-123456", "expectedProviderSequence": sequence, "payload": payload, "requestEnvelopeDigest": d}
	return sealMap(object, "requestEnvelopeDigest")
}
func responseVector(t *testing.T, op Operation, code, message string, payload any) []byte {
	t.Helper()
	object := map[string]any{"schemaVersion": ResponseSchema, "protocolFamily": ControlFamily, "protocolVersion": 1, "audience": ControlAudience, "requestId": "request-123456", "commandId": "command-123456", "providerInstanceId": "provider-123456", "authorityProfile": "qoder-cli-adr0034-v1", "operation": op, "observedProviderSequence": 7, "safeCode": code, "safeMessage": message, "payload": payload, "responseEnvelopeDigest": d}
	return sealMap(object, "responseEnvelopeDigest")
}
func resignResponse(t *testing.T, raw []byte, field string, value any) []byte {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	object[field] = value
	return sealMap(object, "responseEnvelopeDigest")
}
func signedVector(t *testing.T, key string, epoch uint64, domain, signature string) []byte {
	t.Helper()
	return canonicalValue(t, map[string]any{"objectDigest": d, "signatureAlgorithm": "Ed25519", "signatureEncoding": "base64url-unpadded", "keyId": key, "keyEpoch": epoch, "signatureDomain": domain, "signature": signature})
}
func sealMap(object map[string]any, field string) []byte {
	raw, _ := json.Marshal(object)
	var members map[string]json.RawMessage
	_ = json.Unmarshal(raw, &members)
	object[field] = digestDetached(members, field)
	raw, _ = json.Marshal(object)
	canonicalRaw, _ := canonical.JSON(raw)
	return canonicalRaw
}
func canonicalValue(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	got, err := canonical.JSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	return got
}
func compileAPAPSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repositoryRoot(), "schemas/apap/apap-v1.schema.json"))
	if err != nil || !json.Valid(raw) {
		t.Fatalf("read schema: %v", err)
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	object := document.(map[string]any)
	if object["$schema"] != "https://json-schema.org/draft/2020-12/schema" {
		t.Fatal("schema does not declare Draft 2020-12")
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.AssertFormat()
	if err := compiler.AddResource(object["$id"].(string), document); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile(object["$id"].(string))
	if err != nil {
		t.Fatalf("metaschema/compile: %v", err)
	}
	return schema
}
func repositoryRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../../.."))
}
