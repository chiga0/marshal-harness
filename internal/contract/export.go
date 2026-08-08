package contract

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
	marshalSchemas "github.com/chiga0/marshal-harness/schemas"
)

// ExampleRole distinguishes the bundled contract fixture families.
type ExampleRole string

const (
	ExampleRoleHappyPath ExampleRole = "happy-path"
	ExampleRoleInvalid   ExampleRole = "invalid"
)

// CatalogExample points at one embedded fixture belonging to a schema.
type CatalogExample struct {
	Role   ExampleRole `json:"role"`
	Path   string      `json:"path"`
	Digest string      `json:"digest"`
}

// CatalogSchema is the self-describing entry for one embedded schema.
type CatalogSchema struct {
	Name       string           `json:"name"`
	Kind       domain.Kind      `json:"kind"`
	Version    string           `json:"version"`
	SchemaID   string           `json:"schemaId"`
	SchemaFile string           `json:"schemaFile"`
	Digest     string           `json:"digest"`
	Examples   []CatalogExample `json:"examples"`
}

// Catalog lists every embedded schema together with its example inventory so
// agents and external tools can consume the contract surface without prior
// knowledge.
type Catalog struct {
	APIVersion domain.APIVersion `json:"apiVersion"`
	Schemas    []CatalogSchema   `json:"schemas"`
}

// SchemaDocument returns the embedded schema document registered under name.
func SchemaDocument(name string) ([]byte, error) {
	descriptor, err := DescriptorByName(name)
	if err != nil {
		return nil, err
	}
	data, err := marshalSchemas.FS.ReadFile(descriptor.SchemaPath)
	if err != nil {
		return nil, fmt.Errorf("read schema %s: %w", descriptor.Name, err)
	}
	return data, nil
}

// ExampleDocument returns the embedded fixture stored at a catalog path such
// as "examples/happy-path/task-spec.json".
func ExampleDocument(path string) ([]byte, error) {
	data, err := marshalSchemas.FS.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read example %s: %w", path, err)
	}
	return data, nil
}

// ExportCatalog compiles the self-describing catalog from the embedded
// schemas. It fails closed when a schema or fixture is missing, or a schema
// $id does not encode its contract version.
func ExportCatalog() (Catalog, error) {
	catalog := Catalog{APIVersion: domain.APIVersionV1Alpha1}
	for _, descriptor := range descriptors {
		data, err := marshalSchemas.FS.ReadFile(descriptor.SchemaPath)
		if err != nil {
			return Catalog{}, fmt.Errorf("read schema %s: %w", descriptor.Name, err)
		}
		var header struct {
			ID string `json:"$id"`
		}
		if err := json.Unmarshal(data, &header); err != nil {
			return Catalog{}, fmt.Errorf("decode schema %s: %w", descriptor.Name, err)
		}
		version, err := schemaVersionFromID(header.ID)
		if err != nil {
			return Catalog{}, fmt.Errorf("schema %s: %w", descriptor.Name, err)
		}
		entry := CatalogSchema{
			Name:       descriptor.Name,
			Kind:       descriptor.Kind,
			Version:    version,
			SchemaID:   header.ID,
			SchemaFile: descriptor.SchemaPath,
			Digest:     canonical.DigestBytes(data),
			Examples: []CatalogExample{
				{Role: ExampleRoleHappyPath, Path: descriptor.HappyPath},
				{Role: ExampleRoleInvalid, Path: descriptor.InvalidPath},
			},
		}
		for index, example := range entry.Examples {
			fixture, err := marshalSchemas.FS.ReadFile(example.Path)
			if err != nil {
				return Catalog{}, fmt.Errorf("read example %s: %w", example.Path, err)
			}
			entry.Examples[index].Digest = canonical.DigestBytes(fixture)
		}
		catalog.Schemas = append(catalog.Schemas, entry)
	}
	return catalog, nil
}

func schemaVersionFromID(id string) (string, error) {
	if id == "" {
		return "", fmt.Errorf("missing $id")
	}
	parsed, err := url.Parse(id)
	if err != nil {
		return "", fmt.Errorf("invalid $id %q: %w", id, err)
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(segments) != 3 || segments[0] != "schemas" || segments[1] == "" {
		return "", fmt.Errorf("$id %q does not encode a schema version", id)
	}
	return segments[1], nil
}
