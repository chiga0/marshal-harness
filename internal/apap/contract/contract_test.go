package contract

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const d = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestDraft202012SchemaAndExamples(t *testing.T) {
	root := repositoryRoot(t)
	schemaPath := filepath.Join(root, "schemas/apap/apap-v1.schema.json")
	raw, err := os.ReadFile(schemaPath)
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
			t.Fatalf("%s: %v", filepath.Base(path), err)
		}
		if err := schema.Validate(value); err != nil {
			t.Fatalf("%s: %v", filepath.Base(path), err)
		}
		packet := bytes.TrimSuffix(data, []byte("\n"))
		switch filepath.Base(path) {
		case "describe-request.json":
			if _, err := ValidateControlRequest(packet, nil); err != nil {
				t.Fatalf("%s strict example admission: %v", filepath.Base(path), err)
			}
		case "begin-probe-request.json":
			fds := []FDDescriptor{{"candidateExecutable", 0, d}, {"scratchRoot", 0, d}, {"businessDenyRoot", 0, d}}
			if _, err := ValidateControlRequest(packet, fds); err != nil {
				t.Fatalf("%s strict example admission: %v", filepath.Base(path), err)
			}
		case "stage-bundle-request.json":
			if _, err := ValidateControlRequest(packet, []FDDescriptor{{"bundleLeaf", 0, d}}); err != nil {
				t.Fatalf("%s strict example admission: %v", filepath.Base(path), err)
			}
		case "describe-response.json":
			if _, err := ValidateControlResponse(packet); err != nil {
				t.Fatalf("%s strict example admission: %v", filepath.Base(path), err)
			}
		case "fd-table.json":
			if _, _, err := ValidateFDTable(packet); err != nil {
				t.Fatalf("%s strict example admission: %v", filepath.Base(path), err)
			}
		case "signed-object-envelope.json":
			if err := ValidateSignedObjectEnvelope(packet, "marshal-bundle-prepared-receipt-v1\x00"); err != nil {
				t.Fatalf("%s strict example admission: %v", filepath.Base(path), err)
			}
		}
	}
}

func TestRegisteredOperationsAcceptExactVectors(t *testing.T) {
	now := time.Date(2026, 8, 18, 8, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		op      Operation
		seq     any
		payload any
		fds     []FDDescriptor
	}{
		{"describe", Describe, nil, struct{}{}, nil},
		{"begin-probe", BeginProbe, uint64(7), map[string]any{"candidateIdentityDigest": d, "suiteDigest": d, "probeArtifactDigest": d, "policyDigest": d, "challengeDigest": d, "deadline": now.Add(time.Minute).Format(time.RFC3339Nano)}, []FDDescriptor{{"candidateExecutable", 0, d}, {"scratchRoot", 0, d}, {"businessDenyRoot", 0, d}}},
		{"stage", StageBundleLeafBatch, uint64(7), map[string]any{"bundleTransactionId": "tx-123456", "updateKind": "evidence-update", "orderedLeafDescriptors": []any{map[string]any{"leafKind": "config", "digest": d, "size": 2, "mediaType": "application/json"}}}, []FDDescriptor{{"bundleLeaf", 0, d}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := requestVector(t, test.op, test.seq, test.payload, now)
			got, err := ValidateControlRequest(raw, test.fds)
			if err != nil || got.Operation != test.op {
				t.Fatalf("ValidateControlRequest() = %v, %v", got, err)
			}
		})
	}
}

func TestStrictRequestNegatives(t *testing.T) {
	now := time.Date(2026, 8, 18, 8, 0, 0, 0, time.UTC)
	valid := requestVector(t, BeginProbe, uint64(1), map[string]any{"candidateIdentityDigest": d, "suiteDigest": d, "probeArtifactDigest": d, "policyDigest": d, "challengeDigest": d, "deadline": now.Add(time.Minute).Format(time.RFC3339Nano)}, now)
	fds := []FDDescriptor{{"candidateExecutable", 0, d}, {"scratchRoot", 0, d}, {"businessDenyRoot", 0, d}}
	mutations := map[string][]byte{
		"unknown":            bytes.Replace(valid, []byte(`"audience":`), []byte(`"extra":true,"audience":`), 1),
		"duplicate":          bytes.Replace(valid, []byte(`"audience":`), []byte(`"audience":"marshal.agent-production-authority.local","audience":`), 1),
		"nested-duplicate":   bytes.Replace(valid, []byte(`"candidateIdentityDigest":`), []byte(`"candidateIdentityDigest":"`+d+`","candidateIdentityDigest":`), 1),
		"wrong-type":         bytes.Replace(valid, []byte(`"protocolVersion":1`), []byte(`"protocolVersion":"1"`), 1),
		"null":               bytes.Replace(valid, []byte(`"payload":{`), []byte(`"payload":null,"ignored":{`), 1),
		"noncanonical-order": append([]byte(" "), valid...),
		"substitution":       bytes.Replace(valid, []byte(`"operation":"BeginProbe"`), []byte(`"operation":"RunProbeVariant"`), 1),
		"bad-digest":         bytes.Replace(valid, []byte(d), []byte("sha256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"), 1),
		"bad-time":           bytes.Replace(valid, []byte("2026-08-18T08:01:00Z"), []byte("2026-08-18T08:01:00+00:00"), 1),
	}
	for name, raw := range mutations {
		t.Run(name, func(t *testing.T) {
			if _, err := ValidateControlRequest(raw, fds); err == nil {
				t.Fatal("mutation accepted")
			}
		})
	}
}

func TestCardinalityOrderAndFDBindingNegatives(t *testing.T) {
	now := time.Date(2026, 8, 18, 8, 0, 0, 0, time.UTC)
	leaves := []any{map[string]any{"leafKind": "z", "digest": d, "size": 2, "mediaType": "application/json"}, map[string]any{"leafKind": "a", "digest": "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "size": 2, "mediaType": "application/json"}}
	raw := requestVector(t, StageBundleLeafBatch, uint64(1), map[string]any{"bundleTransactionId": "tx-123456", "updateKind": "evidence-update", "orderedLeafDescriptors": leaves}, now)
	if _, err := ValidateControlRequest(raw, []FDDescriptor{{"bundleLeaf", 0, d}, {"bundleLeaf", 1, "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}); err == nil {
		t.Fatal("out-of-order descriptors accepted")
	}
	empty := requestVector(t, StageBundleLeafBatch, uint64(1), map[string]any{"bundleTransactionId": "tx-123456", "updateKind": "evidence-update", "orderedLeafDescriptors": []any{}}, now)
	if _, err := ValidateControlRequest(empty, nil); err == nil {
		t.Fatal("empty batch accepted")
	}
	begin := requestVector(t, BeginProbe, uint64(1), map[string]any{"candidateIdentityDigest": d, "suiteDigest": d, "probeArtifactDigest": d, "policyDigest": d, "challengeDigest": d, "deadline": now.Add(time.Minute).Format(time.RFC3339Nano)}, now)
	if _, err := ValidateControlRequest(begin, []FDDescriptor{{"scratchRoot", 0, d}, {"candidateExecutable", 0, d}, {"businessDenyRoot", 0, d}}); err == nil {
		t.Fatal("fd substitution accepted")
	}
}

func TestResponseSuccessAndFailureAreDisjoint(t *testing.T) {
	success := responseVector(t, Describe, "ok", "", map[string]any{"providerBuildDigest": d, "platform": "linux", "profiles": []any{map[string]any{"authorityProfile": "qoder-cli-adr0034-v1", "status": "unsupported"}}})
	if _, err := ValidateControlResponse(success); err != nil {
		t.Fatal(err)
	}
	failure := responseVector(t, Describe, "platform-unsupported", "unsupported", nil)
	if _, err := ValidateControlResponse(failure); err != nil {
		t.Fatal(err)
	}
	smuggled := responseVector(t, Describe, "platform-unsupported", "unsupported", map[string]any{})
	if _, err := ValidateControlResponse(smuggled); err == nil {
		t.Fatal("failure success-payload smuggling accepted")
	}
}

func TestSignedEnvelopeStrictBase64URL(t *testing.T) {
	signature := base64.RawURLEncoding.EncodeToString(make([]byte, 64))
	domain := "marshal-bundle-prepared-receipt-v1\x00"
	raw := canonicalValue(t, map[string]any{"objectDigest": d, "signatureAlgorithm": "Ed25519", "signatureEncoding": "base64url-unpadded", "keyId": "key-123456", "keyEpoch": 1, "signatureDomain": domain, "signature": signature})
	if err := ValidateSignedObjectEnvelope(raw, domain); err != nil {
		t.Fatal(err)
	}
	for name, mutation := range map[string][]byte{
		"padding": bytes.Replace(raw, []byte(signature), []byte(signature+"=="), 1),
		"short":   bytes.Replace(raw, []byte(signature), []byte(signature[:84]), 1),
		"domain":  bytes.Replace(raw, []byte("marshal-bundle-prepared"), []byte("marshal-bundle-commit"), 1),
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateSignedObjectEnvelope(mutation, domain); err == nil {
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
	now := time.Date(2026, 8, 18, 8, 0, 0, 0, time.UTC)
	f.Add(requestVectorForFuzz(Describe, nil, struct{}{}, now))
	f.Add([]byte(`{"x":1,"x":2}`))
	f.Add([]byte(`null`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		_, _ = ValidateControlRequest(raw, nil)
	})
}

func requestVector(t *testing.T, op Operation, sequence any, payload any, now time.Time) []byte {
	t.Helper()
	return requestVectorForFuzz(op, sequence, payload, now)
}

func requestVectorForFuzz(op Operation, sequence any, payload any, now time.Time) []byte {
	object := map[string]any{"schemaVersion": RequestSchema, "protocolFamily": ControlFamily, "protocolVersion": 1, "audience": ControlAudience, "requestId": "request-123456", "commandId": "command-123456", "callerPrincipalDigest": d, "providerInstanceId": "provider-123456", "authorityProfile": "qoder-cli-adr0034-v1", "operation": op, "issuedAt": now.Format(time.RFC3339Nano), "expiresAt": now.Add(2 * time.Minute).Format(time.RFC3339Nano), "nonce": "nonce-123456", "expectedProviderSequence": sequence, "payload": payload, "requestEnvelopeDigest": d}
	raw, _ := json.Marshal(object)
	var members map[string]json.RawMessage
	_ = json.Unmarshal(raw, &members)
	object["requestEnvelopeDigest"] = digestDetached(members, "requestEnvelopeDigest")
	raw, _ = json.Marshal(object)
	canonicalRaw, _ := canonical.JSON(raw)
	return canonicalRaw
}

func responseVector(t *testing.T, op Operation, code, message string, payload any) []byte {
	t.Helper()
	object := map[string]any{"schemaVersion": ResponseSchema, "protocolFamily": ControlFamily, "protocolVersion": 1, "audience": ControlAudience, "requestId": "request-123456", "commandId": "command-123456", "providerInstanceId": "provider-123456", "authorityProfile": "qoder-cli-adr0034-v1", "operation": op, "observedProviderSequence": 7, "safeCode": code, "safeMessage": message, "payload": payload, "responseEnvelopeDigest": d}
	raw, _ := json.Marshal(object)
	var members map[string]json.RawMessage
	_ = json.Unmarshal(raw, &members)
	object["responseEnvelopeDigest"] = digestDetached(members, "responseEnvelopeDigest")
	return canonicalValue(t, object)
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

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../../.."))
}
