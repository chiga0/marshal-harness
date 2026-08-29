package schemas

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const rc1ReceiptSchemaID = "https://marshal.dev/schemas/release/rc1-canary-receipt.schema.json"

func TestRC1CanaryReceiptSchemaAndFixtures(t *testing.T) {
	t.Parallel()

	schemaRaw, err := FS.ReadFile("release/rc1-canary-receipt.schema.json")
	if err != nil {
		t.Fatalf("read RC1 receipt schema: %v", err)
	}
	if !json.Valid(schemaRaw) {
		t.Fatal("RC1 receipt schema is not valid JSON")
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaRaw))
	if err != nil {
		t.Fatalf("decode RC1 receipt schema: %v", err)
	}
	object, ok := document.(map[string]any)
	if !ok {
		t.Fatal("RC1 receipt schema is not an object")
	}
	if object["$schema"] != "https://json-schema.org/draft/2020-12/schema" {
		t.Fatalf("RC1 receipt schema is not Draft 2020-12: %v", object["$schema"])
	}
	if object["$id"] != rc1ReceiptSchemaID {
		t.Fatalf("RC1 receipt schema id mismatch: %v", object["$id"])
	}

	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.AssertFormat()
	if err := compiler.AddResource(rc1ReceiptSchemaID, document); err != nil {
		t.Fatalf("register RC1 receipt schema: %v", err)
	}
	compiled, err := compiler.Compile(rc1ReceiptSchemaID)
	if err != nil {
		t.Fatalf("RC1 receipt schema fails Draft 2020-12 compilation: %v", err)
	}

	valid := readRC1Fixture(t, "release/examples/valid/rc1-canary-receipt.json")
	if err := compiled.Validate(valid); err != nil {
		t.Fatalf("valid RC1 receipt fixture rejected: %v", err)
	}
	invalid := readRC1Fixture(t, "release/examples/invalid/rc1-canary-receipt-missing-authority.json")
	if err := compiled.Validate(invalid); err == nil {
		t.Fatal("invalid RC1 receipt fixture unexpectedly accepted")
	}
}

func readRC1Fixture(t *testing.T, name string) any {
	t.Helper()
	raw, err := FS.ReadFile(name)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("decode fixture %s: %v", name, err)
	}
	return value
}
