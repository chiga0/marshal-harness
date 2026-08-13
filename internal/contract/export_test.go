package contract

import (
	"testing"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
)

func TestExportCatalogMatchesEmbeddedCollection(t *testing.T) {
	t.Parallel()

	catalog, err := ExportCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if catalog.APIVersion != domain.APIVersionV1Alpha1 {
		t.Fatalf("apiVersion = %q", catalog.APIVersion)
	}
	descriptors := Descriptors()
	if len(catalog.Schemas) != len(descriptors) || len(catalog.Schemas) != 20 {
		t.Fatalf("catalog has %d schemas, want %d", len(catalog.Schemas), len(descriptors))
	}
	for index, entry := range catalog.Schemas {
		descriptor := descriptors[index]
		if entry.Name != descriptor.Name || entry.Kind != descriptor.Kind || entry.SchemaFile != descriptor.SchemaPath {
			t.Fatalf("entry %d = %+v, descriptor = %+v", index, entry, descriptor)
		}
		if entry.Version != "v1alpha1" || entry.SchemaID != "https://marshal.dev/schemas/v1alpha1/"+descriptor.SchemaPath {
			t.Fatalf("entry %d version/id = %q/%q", index, entry.Version, entry.SchemaID)
		}
		schema, err := SchemaDocument(descriptor.Name)
		if err != nil {
			t.Fatal(err)
		}
		if entry.Digest != canonical.DigestBytes(schema) {
			t.Fatalf("schema %s digest mismatch: %s", descriptor.Name, entry.Digest)
		}
		expectedRoles := []ExampleRole{ExampleRoleHappyPath, ExampleRoleInvalid}
		expectedPaths := []string{descriptor.HappyPath, descriptor.InvalidPath}
		if len(entry.Examples) != len(expectedPaths) {
			t.Fatalf("entry %d examples = %+v", index, entry.Examples)
		}
		for exampleIndex, example := range entry.Examples {
			if example.Role != expectedRoles[exampleIndex] || example.Path != expectedPaths[exampleIndex] {
				t.Fatalf("entry %d example %d = %+v", index, exampleIndex, example)
			}
			fixture, err := ExampleDocument(example.Path)
			if err != nil {
				t.Fatal(err)
			}
			if example.Digest != canonical.DigestBytes(fixture) {
				t.Fatalf("example %s digest mismatch: %s", example.Path, example.Digest)
			}
		}
	}
}

func TestSchemaDocumentRejectsUnknownName(t *testing.T) {
	t.Parallel()

	if _, err := SchemaDocument("no-such-schema"); err == nil {
		t.Fatal("SchemaDocument accepted an unknown name")
	}
}

func TestExampleDocumentRejectsUnknownPath(t *testing.T) {
	t.Parallel()

	if _, err := ExampleDocument("examples/happy-path/no-such.json"); err == nil {
		t.Fatal("ExampleDocument accepted an unknown path")
	}
}

func TestSchemaVersionFromID(t *testing.T) {
	t.Parallel()

	version, err := schemaVersionFromID("https://marshal.dev/schemas/v1alpha1/task-spec.schema.json")
	if err != nil || version != "v1alpha1" {
		t.Fatalf("version = %q, err = %v", version, err)
	}
	for _, invalid := range []string{
		"",
		"https://marshal.dev/schemas/task-spec.schema.json",
		"https://marshal.dev/other/v1alpha1/task-spec.schema.json",
		"https://marshal.dev/schemas/v1alpha1/task-spec.schema.json/extra",
		"ht!tp://%zz",
	} {
		if _, err := schemaVersionFromID(invalid); err == nil {
			t.Fatalf("accepted invalid $id %q", invalid)
		}
	}
}
