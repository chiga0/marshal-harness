package cli

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/contract"
	marshalSchemas "github.com/chiga0/marshal-harness/schemas"
)

func TestContractRequiresKnownSubcommand(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{{"contract"}, {"contract", "export"}} {
		var stdout, stderr bytes.Buffer
		if exit := Run(args, strings.NewReader(""), &stdout, &stderr); exit != ExitUsage {
			t.Fatalf("Run(%v) exit = %d, stderr = %s", args, exit, stderr.String())
		}
	}
}

func TestContractSchemaListsNamesAndVersions(t *testing.T) {
	t.Parallel()

	catalog, err := contract.ExportCatalog()
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if exit := Run([]string{"contract", "schema"}, strings.NewReader(""), &stdout, &stderr); exit != ExitOK {
		t.Fatalf("exit = %d, stderr = %s", exit, stderr.String())
	}
	lines := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
	if len(lines) != len(catalog.Schemas) {
		t.Fatalf("lines = %q", lines)
	}
	for index, entry := range catalog.Schemas {
		if lines[index] != entry.Name+" "+entry.Version {
			t.Fatalf("line %d = %q, want %q", index, lines[index], entry.Name+" "+entry.Version)
		}
	}

	stdout.Reset()
	stderr.Reset()
	if exit := Run([]string{"contract", "schema", "--json"}, strings.NewReader(""), &stdout, &stderr); exit != ExitOK {
		t.Fatalf("json exit = %d, stderr = %s", exit, stderr.String())
	}
	var entries []struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(catalog.Schemas) {
		t.Fatalf("json entries = %+v", entries)
	}
	for index, entry := range catalog.Schemas {
		if entries[index].Name != entry.Name || entries[index].Version != entry.Version {
			t.Fatalf("json entry %d = %+v", index, entries[index])
		}
	}
}

func TestContractSchemaAllPrintsSelfDescribingCatalog(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	if exit := Run([]string{"contract", "schema", "--all"}, strings.NewReader(""), &stdout, &stderr); exit != ExitOK {
		t.Fatalf("exit = %d, stderr = %s", exit, stderr.String())
	}
	var catalog contract.Catalog
	if err := json.Unmarshal(stdout.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	descriptors := contract.Descriptors()
	if len(catalog.Schemas) != len(descriptors) || len(catalog.Schemas) != 20 {
		t.Fatalf("catalog schemas = %d, want %d", len(catalog.Schemas), len(descriptors))
	}
	for index, entry := range catalog.Schemas {
		descriptor := descriptors[index]
		schemaData, err := marshalSchemas.FS.ReadFile(descriptor.SchemaPath)
		if err != nil {
			t.Fatal(err)
		}
		if entry.Digest != canonical.DigestBytes(schemaData) {
			t.Fatalf("schema %s digest mismatch: %s", descriptor.Name, entry.Digest)
		}
		expectedPaths := []string{descriptor.HappyPath, descriptor.InvalidPath}
		if len(entry.Examples) != len(expectedPaths) {
			t.Fatalf("entry %d examples = %+v", index, entry.Examples)
		}
		for exampleIndex, example := range entry.Examples {
			if example.Path != expectedPaths[exampleIndex] {
				t.Fatalf("entry %d example %d path = %q", index, exampleIndex, example.Path)
			}
			fixture, err := marshalSchemas.FS.ReadFile(example.Path)
			if err != nil {
				t.Fatal(err)
			}
			if example.Digest != canonical.DigestBytes(fixture) {
				t.Fatalf("example %s digest mismatch: %s", example.Path, example.Digest)
			}
		}
	}
}

func TestContractSchemaAllWritesEmbeddedFileSet(t *testing.T) {
	t.Parallel()

	directory := filepath.Join(t.TempDir(), "nested", "export")
	var stdout, stderr bytes.Buffer
	if exit := Run([]string{"contract", "schema", "--all", "--out", directory}, strings.NewReader(""), &stdout, &stderr); exit != ExitOK {
		t.Fatalf("exit = %d, stderr = %s", exit, stderr.String())
	}

	want := map[string][]byte{}
	for _, descriptor := range contract.Descriptors() {
		for _, embeddedPath := range []string{descriptor.SchemaPath, descriptor.HappyPath, descriptor.InvalidPath} {
			data, err := marshalSchemas.FS.ReadFile(embeddedPath)
			if err != nil {
				t.Fatal(err)
			}
			want[embeddedPath] = data
		}
	}
	want["catalog.json"] = nil

	got := map[string][]byte{}
	err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(directory, path)
		if err != nil {
			return err
		}
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if info.Mode().Perm() != 0o644 {
			t.Errorf("exported file %s mode = %v, want 0644", relative, info.Mode().Perm())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		got[filepath.ToSlash(relative)] = data
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("exported %d files, want %d", len(got), len(want))
	}
	for path, data := range want {
		exported, ok := got[path]
		if !ok {
			t.Fatalf("missing exported file %s", path)
		}
		if path == "catalog.json" {
			var catalog contract.Catalog
			if err := json.Unmarshal(exported, &catalog); err != nil {
				t.Fatalf("decode catalog.json: %v", err)
			}
			if len(catalog.Schemas) != 20 || catalog.Schemas[0].Name != "approval-record" {
				t.Fatalf("catalog.json = %+v", catalog)
			}
			continue
		}
		if !bytes.Equal(exported, data) {
			t.Fatalf("exported file %s differs from embedded bytes", path)
		}
	}
}

func TestContractSchemaSingleDumpsEmbeddedDocument(t *testing.T) {
	t.Parallel()

	expected, err := marshalSchemas.FS.ReadFile("task-spec.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if exit := Run([]string{"contract", "schema", "--schema", "task-spec"}, strings.NewReader(""), &stdout, &stderr); exit != ExitOK {
		t.Fatalf("exit = %d, stderr = %s", exit, stderr.String())
	}
	if !bytes.Equal(stdout.Bytes(), expected) {
		t.Fatalf("single schema dump differs from embedded bytes")
	}

	stdout.Reset()
	stderr.Reset()
	if exit := Run([]string{"contract", "schema", "--schema", "no-such-schema"}, strings.NewReader(""), &stdout, &stderr); exit != ExitUsage {
		t.Fatalf("unknown schema exit = %d, want %d", exit, ExitUsage)
	}
	if stdout.Len() != 0 {
		t.Fatalf("unknown schema wrote stdout: %q", stdout.String())
	}
}

func TestContractSchemaRejectsConflictingFlags(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"contract", "schema", "extra"},
		{"contract", "schema", "--out", "dir"},
		{"contract", "schema", "--all", "--schema", "task-spec"},
		{"contract", "schema", "--schema", "task-spec", "--out", "dir"},
		{"contract", "schema", "--schema", "task-spec", "--json"},
		{"contract", "schema", "--all", "--json"},
	} {
		var stdout, stderr bytes.Buffer
		if exit := Run(args, strings.NewReader(""), &stdout, &stderr); exit != ExitUsage {
			t.Fatalf("Run(%v) exit = %d, want %d, stderr = %s", args, exit, ExitUsage, stderr.String())
		}
	}
}
