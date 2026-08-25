package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/port"
)

func providerSchemaFixturePath(name string) string {
	return filepath.Join("..", "..", "..", ".agents", "skills", "marshal", "references", "fixtures", "codex-provider-schema", name)
}

func providerProfileFixturePath() string {
	return filepath.Join("..", "..", "..", ".agents", "skills", "marshal", "references", "codex-0.145-provider-schema-profile.json")
}

func providerProfile0149FixturePath() string {
	return filepath.Join("..", "..", "..", ".agents", "skills", "marshal", "references", "codex-0.149-provider-schema-profile.json")
}

func readProviderFixture(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestProviderSchemaCompatibilityMatchesCurrentProjectionAndR16Fixture(t *testing.T) {
	durable, err := contract.SchemaDocument("worker-result")
	if err != nil {
		t.Fatal(err)
	}
	projected, err := providerSchemaDocument(durable)
	if err != nil {
		t.Fatal(err)
	}
	fixture := readProviderFixture(t, providerSchemaFixturePath("valid-r16-provider-schema.json"))
	if bytes.HasSuffix(fixture, []byte("\n")) {
		t.Fatal("R16 raw provider fixture must not have a trailing LF")
	}
	if !bytes.Equal(projected, fixture) {
		t.Fatal("R16 fixture is not byte-identical to current providerSchemaDocument output")
	}
	profile := readProviderFixture(t, providerProfileFixturePath())
	result, err := CheckProviderSchemaCompatibility(projected, profile)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "pass" || result.ReasonCode != providerSchemaCompatible || result.IssueCount != 0 || len(result.Issues) != 0 {
		t.Fatalf("compatibility = %+v", result)
	}
}

func TestProviderSchemaCompatibilityBindsCodex0149Profile(t *testing.T) {
	durable, err := contract.SchemaDocument("worker-result")
	if err != nil {
		t.Fatal(err)
	}
	projected, err := providerSchemaDocument(durable)
	if err != nil {
		t.Fatal(err)
	}
	profile := readProviderFixture(t, providerProfile0149FixturePath())
	result, err := CheckProviderSchemaCompatibility(projected, profile)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "pass" || result.ReasonCode != providerSchemaCompatible || result.CLICompatibilityLine != ordinaryUserCompatibilityLine0149 || result.IssueCount != 0 {
		t.Fatalf("0.149 compatibility = %+v", result)
	}
	generated, err := frozenProviderSchemaProfileDocumentForLine(ordinaryUserCompatibilityLine0149)
	if err != nil {
		t.Fatal(err)
	}
	var generatedProfile, fixtureProfile providerSchemaProfile
	if json.Unmarshal(generated, &generatedProfile) != nil || json.Unmarshal(profile, &fixtureProfile) != nil || !reflect.DeepEqual(generatedProfile, fixtureProfile) {
		t.Fatal("0.149 operator profile differs from the production profile")
	}
}

func TestProviderSchemaCompatibilityAggregatesExactHistoricalIssues(t *testing.T) {
	schema := readProviderFixture(t, providerSchemaFixturePath("invalid-aggregate-r1-r10.json"))
	profile := readProviderFixture(t, providerProfileFixturePath())
	result, err := CheckProviderSchemaCompatibility(schema, profile)
	if err != nil {
		t.Fatal(err)
	}
	want := []ProviderSchemaIssue{
		{Code: "unsupported-keyword", JSONPointer: "/not", Keyword: "not"},
		{Code: "missing-type", JSONPointer: "/not", Keyword: "type"},
		{Code: "missing-type", JSONPointer: "/not/anyOf/0", Keyword: "type"},
		{Code: "unsupported-keyword", JSONPointer: "/not/anyOf/0/pattern", Keyword: "pattern"},
		{Code: "required-properties-mismatch", JSONPointer: "/properties/adapter/required", Keyword: "required"},
		{Code: "missing-type", JSONPointer: "/properties/apiVersion", Keyword: "type"},
		{Code: "unsupported-keyword", JSONPointer: "/properties/apiVersion/const", Keyword: "const"},
		{Code: "unsupported-keyword", JSONPointer: "/properties/declaredArtifacts/items/oneOf", Keyword: "oneOf"},
		{Code: "unsupported-keyword", JSONPointer: "/properties/declaredArtifacts/items/properties/uri/format", Keyword: "format"},
		{Code: "unsupported-keyword", JSONPointer: "/properties/declaredChangedFiles/uniqueItems", Keyword: "uniqueItems"},
	}
	// The checker sorts by pointer, then code, then keyword.
	sortProviderIssues(want)
	if result.Status != "fail" || result.ReasonCode != providerSchemaIncompatible || result.IssueCount != len(want) || !reflect.DeepEqual(result.Issues, want) {
		t.Fatalf("compatibility issues = %#v, want %#v", result.Issues, want)
	}
}

func sortProviderIssues(issues []ProviderSchemaIssue) {
	for i := 0; i < len(issues); i++ {
		for j := i + 1; j < len(issues); j++ {
			left, right := issues[i], issues[j]
			if right.JSONPointer < left.JSONPointer || (right.JSONPointer == left.JSONPointer && (right.Code < left.Code || (right.Code == left.Code && right.Keyword < left.Keyword))) {
				issues[i], issues[j] = issues[j], issues[i]
			}
		}
	}
}

func TestProviderSchemaStrictJSONRejectsAmbiguousOrNonFiniteInputs(t *testing.T) {
	profile := readProviderFixture(t, providerProfileFixturePath())
	for _, raw := range []string{
		`{"type":"string","type":"string"}`,
		`{"type":"number","default":NaN}`,
		`{"type":"number","default":Infinity}`,
		`{"type":"number","default":-Infinity}`,
		`{"type":"number","default":1e9999}`,
	} {
		t.Run(raw, func(t *testing.T) {
			_, err := CheckProviderSchemaCompatibility([]byte(raw), profile)
			var checkErr *ProviderSchemaCheckError
			if !errors.As(err, &checkErr) || checkErr.ReasonCode != providerSchemaJSONInvalid {
				t.Fatalf("err = %v, want %s", err, providerSchemaJSONInvalid)
			}
		})
	}
	if _, err := providerSchemaDocument([]byte(`{"type":"string","type":"string"}`)); err == nil {
		t.Fatal("providerSchemaDocument accepted a duplicate JSON key")
	}
}

func TestProviderSchemaProfileDriftFailsClosed(t *testing.T) {
	profileRaw := readProviderFixture(t, providerProfileFixturePath())
	var profile map[string]any
	if err := json.Unmarshal(profileRaw, &profile); err != nil {
		t.Fatal(err)
	}
	profile["allowedKeywords"] = append(profile["allowedKeywords"].([]any), "description")
	drifted, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	_, err = CheckProviderSchemaCompatibility([]byte(`{"type":"string"}`), drifted)
	var checkErr *ProviderSchemaCheckError
	if !errors.As(err, &checkErr) || checkErr.ReasonCode != providerProfileInvalid {
		t.Fatalf("err = %v, want %s", err, providerProfileInvalid)
	}
}

func TestProviderSchemaUnknownCompatibilityLineFailsClosed(t *testing.T) {
	profileRaw := readProviderFixture(t, providerProfile0149FixturePath())
	var profile map[string]any
	if err := json.Unmarshal(profileRaw, &profile); err != nil {
		t.Fatal(err)
	}
	profile["cliCompatibilityLine"] = "0.150.x"
	drifted, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	_, err = CheckProviderSchemaCompatibility([]byte(`{"type":"string"}`), drifted)
	var checkErr *ProviderSchemaCheckError
	if !errors.As(err, &checkErr) || checkErr.ReasonCode != providerProfileInvalid {
		t.Fatalf("err = %v, want %s", err, providerProfileInvalid)
	}
}

func TestProviderSchemaProfileStrictJSONFailsClosed(t *testing.T) {
	for _, raw := range []string{
		`{"profileVersion":"x","profileVersion":"x"}`,
		`{"maxSchemaBytes":NaN}`,
		`{"maxSchemaBytes":Infinity}`,
		`{"maxSchemaBytes":1e9999}`,
	} {
		_, err := CheckProviderSchemaCompatibility([]byte(`{"type":"string"}`), []byte(raw))
		var checkErr *ProviderSchemaCheckError
		if !errors.As(err, &checkErr) || checkErr.ReasonCode != providerProfileInvalid {
			t.Fatalf("raw = %s, err = %v, want %s", raw, err, providerProfileInvalid)
		}
	}
}

func TestProviderSchemaAllowedKeywordValuesAreClosedRegardlessOfApplicability(t *testing.T) {
	profile := readProviderFixture(t, providerProfileFixturePath())
	tests := []struct{ name, schema, code, pointer string }{
		{"type", `{"type":[]}`, "type-invalid", "/type"},
		{"properties", `{"type":"string","properties":[]}`, "object-properties-invalid", "/properties"},
		{"required", `{"type":"string","required":["x","x"]}`, "keyword-value-invalid", "/required"},
		{"additionalProperties", `{"type":"string","additionalProperties":"false"}`, "keyword-value-invalid", "/additionalProperties"},
		{"items", `{"type":"string","items":[]}`, "keyword-value-invalid", "/items"},
		{"anyOf", `{"type":"string","anyOf":[]}`, "anyof-shape-invalid", "/anyOf"},
		{"default", `{"type":"string","default":1}`, "keyword-value-invalid", "/default"},
		{"enum", `{"type":"string","enum":[]}`, "enum-shape-invalid", "/enum"},
		{"minimum", `{"type":"string","minimum":0}`, "keyword-value-invalid", "/minimum"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := CheckProviderSchemaCompatibility([]byte(test.schema), profile)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, issue := range result.Issues {
				if issue.Code == test.code && issue.JSONPointer == test.pointer {
					found = true
				}
			}
			if !found {
				t.Fatalf("issues = %#v, want %s at %s", result.Issues, test.code, test.pointer)
			}
		})
	}
}

func TestProviderSchemaEnumRejectsNumericallyEquivalentValues(t *testing.T) {
	profile := readProviderFixture(t, providerProfileFixturePath())
	for _, schema := range []string{
		`{"type":"number","enum":[1,1.0,1e0]}`,
		`{"type":"array","items":{"type":"number"},"enum":[[1,{"a":2.0,"b":[3]}],[1.0,{"b":[3e0],"a":2e0}]]}`,
	} {
		result, err := CheckProviderSchemaCompatibility([]byte(schema), profile)
		if err != nil {
			t.Fatal(err)
		}
		if result.Status != "fail" || result.ReasonCode != providerSchemaIncompatible {
			t.Fatalf("result = %#v, want incompatible", result)
		}
		if len(result.Issues) != 1 || result.Issues[0] != (ProviderSchemaIssue{
			Code: "enum-shape-invalid", JSONPointer: "/enum", Keyword: "enum",
		}) {
			t.Fatalf("issues = %#v, want enum-shape-invalid at /enum", result.Issues)
		}
	}
}

func TestRunRejectsIncompatibleProviderSchemaBeforeClaimsOrLaunch(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "launched")
	fixture := newRunFixture(t, supportedVersionOutput, "touch "+shellQuote(marker))
	fixture.adapter.providerSchemaMutationForTest = func([]byte) []byte {
		return []byte(`{"type":"object","properties":{},"required":[],"additionalProperties":false,"not":{}}`)
	}
	_, err := fixture.adapter.Run(context.Background(), fixture.request)
	if err == nil || !strings.Contains(err.Error(), providerSchemaIncompatible) {
		t.Fatalf("err = %v, want fixed compatibility reason", err)
	}
	assertCodexFailure(t, err, port.FailureKindProtocolInvalid, port.RetryDispositionDoNotRetry)
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatal("provider launched after schema compatibility failure")
	}
	output := filepath.Join(fixture.controlRoot, "output")
	entries, readErr := os.ReadDir(output)
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("schema compatibility failure claimed evidence leaves: %v", entries)
	}
}
