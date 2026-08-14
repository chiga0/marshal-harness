package contract

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"slices"
	"strings"
	"testing"

	"github.com/chiga0/marshal-harness/internal/domain"
	marshalSchemas "github.com/chiga0/marshal-harness/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// issue65BaselineNames lists the eighteen descriptors already registered in
// the durable catalog when Issue #65 was filed: the fifteen M0-M5 durable
// records plus the three ADR 0017 control records.
var issue65BaselineNames = []string{
	"approval-record",
	"artifact-manifest",
	"capability-snapshot",
	"intervention-record",
	"outcome",
	"policy-snapshot",
	"publication-intent",
	"publication-record",
	"remote-check-record",
	"review-decision",
	"review-packet",
	"run-event",
	"run-state",
	"sandbox-requirements",
	"task-spec",
	"verification-report",
	"worker-request",
	"worker-result",
}

// issue65ExceptionKinds maps each Issue #65 exception schema name to the
// reserved domain.Kind constant of the internal Go type it freezes.
var issue65ExceptionKinds = map[string]domain.Kind{
	"authority-namespace":          domain.KindAuthorityNamespace,
	"conformance-evidence":         domain.KindConformanceEvidence,
	"provider-capability-snapshot": domain.KindProviderCapabilitySnapshot,
	"provider-registration":        domain.KindProviderRegistration,
	"reconcile-record":             domain.KindReconcileRecord,
	"side-effect-intent":           domain.KindSideEffectIntent,
	"side-effect-receipt":          domain.KindSideEffectReceipt,
}

// TestCatalogCountAndKindConsistency pins the Issue #65 consistency gate:
// the durable catalog and domain.Kinds carry the identical inventory. The
// Issue #65 baseline had eighteen descriptors; the only growth since is
// scm-merge-receipt and publication-reconcile-record (the two Issue #65
// schemas that carry the durable envelope) and candidate-record, while the
// seven envelope-less M8 schemas remain documented CatalogExceptions.
func TestCatalogCountAndKindConsistency(t *testing.T) {
	t.Parallel()

	descriptors := Descriptors()
	kinds := domain.Kinds()
	if len(descriptors) != len(kinds) {
		t.Fatalf("catalog has %d descriptors but domain declares %d kinds", len(descriptors), len(kinds))
	}
	if len(descriptors) != 21 {
		t.Fatalf("catalog has %d descriptors, want 21: the eighteen Issue #65 baseline entries plus scm-merge-receipt, publication-reconcile-record and candidate-record", len(descriptors))
	}

	seen := make(map[string]bool, len(descriptors))
	for _, descriptor := range descriptors {
		if seen[descriptor.Name] {
			t.Fatalf("descriptor %s is registered twice", descriptor.Name)
		}
		seen[descriptor.Name] = true
	}

	for _, name := range issue65BaselineNames {
		if _, err := DescriptorByName(name); err != nil {
			t.Fatalf("Issue #65 baseline schema %s regressed: %v", name, err)
		}
	}
}

// TestIssue65EnvelopeSchemasAreRegistered pins that the two Issue #65 M8
// schemas carrying the durable apiVersion/kind envelope are registered with
// catalog entries isomorphic to the existing M0-M6 descriptors.
func TestIssue65EnvelopeSchemasAreRegistered(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		kind domain.Kind
	}{
		{name: "scm-merge-receipt", kind: domain.KindSCMMergeReceipt},
		{name: "publication-reconcile-record", kind: domain.KindPublicationReconcileRecord},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			descriptor, err := DescriptorByName(tc.name)
			if err != nil {
				t.Fatalf("DescriptorByName(%q) error = %v", tc.name, err)
			}
			if descriptor.Kind != tc.kind {
				t.Fatalf("descriptor kind = %q, want %q", descriptor.Kind, tc.kind)
			}
			if descriptor.SchemaPath != tc.name+".schema.json" {
				t.Fatalf("schema path = %q", descriptor.SchemaPath)
			}
			if descriptor.HappyPath != "examples/happy-path/"+tc.name+".json" {
				t.Fatalf("happy path = %q", descriptor.HappyPath)
			}
			if descriptor.InvalidPath != "examples/invalid/"+tc.name+".json" {
				t.Fatalf("invalid path = %q", descriptor.InvalidPath)
			}
			if resolved, err := DescriptorByKind(tc.kind); err != nil || resolved.Name != tc.name {
				t.Fatalf("DescriptorByKind(%q) = %+v, %v", tc.kind, resolved, err)
			}
		})
	}
}

// TestIssue65ExceptionSchemasStayOutOfDurableCatalog pins the documented
// exception side: the seven envelope-less M8 schemas stay out of the
// durable catalog and their reserved kinds stay unresolvable until their
// frozen schema documents gain the apiVersion/kind envelope.
func TestIssue65ExceptionSchemasStayOutOfDurableCatalog(t *testing.T) {
	t.Parallel()

	exceptions := CatalogExceptions()
	wantNames := []string{
		"authority-namespace",
		"conformance-evidence",
		"provider-capability-snapshot",
		"provider-registration",
		"reconcile-record",
		"side-effect-intent",
		"side-effect-receipt",
	}
	names := make([]string, 0, len(exceptions))
	for _, exception := range exceptions {
		names = append(names, exception.Name)
		if exception.Reason == "" {
			t.Fatalf("exception %s carries no documented reason", exception.Name)
		}
	}
	slices.Sort(names)
	if !slices.Equal(names, wantNames) {
		t.Fatalf("catalog exceptions = %v, want %v", names, wantNames)
	}

	for _, exception := range exceptions {
		if _, err := DescriptorByName(exception.Name); err == nil {
			t.Fatalf("exception schema %s is unexpectedly registered as a durable descriptor", exception.Name)
		}
		kind, ok := issue65ExceptionKinds[exception.Name]
		if !ok {
			t.Fatalf("exception schema %s has no reserved kind mapping", exception.Name)
		}
		if _, err := DescriptorByKind(kind); err == nil {
			t.Fatalf("reserved kind %q unexpectedly resolves to a durable descriptor", kind)
		}
	}
}

// TestEmbeddedSchemaInventoryCoveredByCatalogOrExceptions pins that every
// embedded schema document is exactly one of a registered Descriptor or a
// documented CatalogException: nothing silently unregistered, nothing
// double-counted.
func TestEmbeddedSchemaInventoryCoveredByCatalogOrExceptions(t *testing.T) {
	t.Parallel()

	entries, err := fs.ReadDir(marshalSchemas.FS, ".")
	if err != nil {
		t.Fatalf("read embedded schema root: %v", err)
	}
	var inventory []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".schema.json") {
			inventory = append(inventory, strings.TrimSuffix(entry.Name(), ".schema.json"))
		}
	}
	slices.Sort(inventory)
	if len(inventory) != 28 {
		t.Fatalf("embedded schema inventory = %v, want the 21 catalog schemas plus the 7 Issue #65 exceptions", inventory)
	}

	registered := make(map[string]bool)
	for _, descriptor := range Descriptors() {
		registered[descriptor.Name] = true
	}
	excepted := make(map[string]bool)
	for _, exception := range CatalogExceptions() {
		excepted[exception.Name] = true
	}
	for _, name := range inventory {
		if registered[name] == excepted[name] {
			t.Fatalf("schema %s registered=%v excepted=%v; every embedded schema must be exactly one of a durable descriptor or a documented exception", name, registered[name], excepted[name])
		}
	}
}

// compileIssue65Schema compiles one embedded schema with the exact
// production validator configuration: Draft 2020-12, format assertions, and
// the ECMA regexp engine.
func compileIssue65Schema(t *testing.T, name string) *jsonschema.Schema {
	t.Helper()

	data := readFixture(t, name+".schema.json")
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode schema %s: %v", name, err)
	}
	object, ok := document.(map[string]any)
	if !ok {
		t.Fatalf("schema %s is not a JSON object", name)
	}
	identifier, ok := object["$id"].(string)
	if !ok || identifier == "" {
		t.Fatalf("schema %s has no $id", name)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.AssertFormat()
	compiler.UseRegexpEngine(compileECMARegexp)
	if err := compiler.AddResource(identifier, document); err != nil {
		t.Fatalf("register schema %s: %v", name, err)
	}
	schema, err := compiler.Compile(identifier)
	if err != nil {
		t.Fatalf("compile schema %s: %v", name, err)
	}
	return schema
}

// TestIssue65ExceptionFixturesPassSchemaGate is the fixture gate for the
// documented exceptions: every exception schema's happy-path fixture passes
// the frozen schema and its invalid fixture is rejected.
func TestIssue65ExceptionFixturesPassSchemaGate(t *testing.T) {
	t.Parallel()

	for _, exception := range CatalogExceptions() {
		exception := exception
		t.Run(exception.Name, func(t *testing.T) {
			t.Parallel()
			schema := compileIssue65Schema(t, exception.Name)

			happy := readFixture(t, "examples/happy-path/"+exception.Name+".json")
			happyDocument, err := jsonschema.UnmarshalJSON(bytes.NewReader(happy))
			if err != nil {
				t.Fatalf("decode happy fixture: %v", err)
			}
			if err := schema.Validate(happyDocument); err != nil {
				t.Fatalf("happy fixture failed the frozen schema: %v", err)
			}

			invalid := readFixture(t, "examples/invalid/"+exception.Name+".json")
			invalidDocument, err := jsonschema.UnmarshalJSON(bytes.NewReader(invalid))
			if err != nil {
				t.Fatalf("decode invalid fixture: %v", err)
			}
			if err := schema.Validate(invalidDocument); err == nil {
				t.Fatal("invalid fixture unexpectedly passed the frozen schema")
			}
		})
	}
}

// TestIssue65ExceptionSchemasRejectDurableEnvelope proves the infeasibility
// required for every catalog exception: a durable catalog fixture must carry
// the apiVersion/kind envelope matched by Validator.Validate, but the frozen
// exception schemas forbid additional properties, so no document can satisfy
// both gates at once. If this test ever fails, the affected schema must be
// promoted to a durable Descriptor instead of staying an exception.
func TestIssue65ExceptionSchemasRejectDurableEnvelope(t *testing.T) {
	t.Parallel()

	for _, exception := range CatalogExceptions() {
		exception := exception
		t.Run(exception.Name, func(t *testing.T) {
			t.Parallel()
			schema := compileIssue65Schema(t, exception.Name)

			var document map[string]any
			happy := readFixture(t, "examples/happy-path/"+exception.Name+".json")
			if err := json.Unmarshal(happy, &document); err != nil {
				t.Fatalf("decode happy fixture: %v", err)
			}
			document["apiVersion"] = string(domain.APIVersionV1Alpha1)
			document["kind"] = string(issue65ExceptionKinds[exception.Name])
			enveloped, err := json.Marshal(document)
			if err != nil {
				t.Fatalf("encode enveloped fixture: %v", err)
			}
			envelopeDocument, err := jsonschema.UnmarshalJSON(bytes.NewReader(enveloped))
			if err != nil {
				t.Fatalf("decode enveloped fixture: %v", err)
			}
			if err := schema.Validate(envelopeDocument); err == nil {
				t.Fatalf("frozen schema %s accepted a durable envelope document; the catalog exception is no longer proven infeasible and the schema must be registered as a Descriptor", exception.Name)
			}
		})
	}
}
