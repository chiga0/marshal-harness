package contract

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/chiga0/marshal-harness/internal/domain"
	marshalSchemas "github.com/chiga0/marshal-harness/schemas"
	"github.com/dlclark/regexp2"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

type ecmaRegexp regexp2.Regexp

func (re *ecmaRegexp) MatchString(value string) bool {
	matched, err := (*regexp2.Regexp)(re).MatchString(value)
	return err == nil && matched
}

func (re *ecmaRegexp) String() string {
	return (*regexp2.Regexp)(re).String()
}

func compileECMARegexp(pattern string) (jsonschema.Regexp, error) {
	re, err := regexp2.Compile(pattern, regexp2.ECMAScript)
	if err != nil {
		return nil, err
	}
	return (*ecmaRegexp)(re), nil
}

// Validator owns compiled JSON Schemas and the semantic validation layer.
// It is safe for concurrent validation after construction.
type Validator struct {
	compiled map[domain.Kind]*jsonschema.Schema
}

// NewValidator compiles every embedded Draft 2020-12 schema with format
// assertions enabled. Construction fails if the catalog or a schema drifts.
func NewValidator() (*Validator, error) {
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.AssertFormat()
	compiler.UseRegexpEngine(compileECMARegexp)

	identifiers := make(map[domain.Kind]string, len(descriptors))
	for _, descriptor := range descriptors {
		data, err := marshalSchemas.FS.ReadFile(descriptor.SchemaPath)
		if err != nil {
			return nil, fmt.Errorf("read schema %s: %w", descriptor.Name, err)
		}
		document, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("decode schema %s: %w", descriptor.Name, err)
		}
		object, ok := document.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("schema %s is not an object", descriptor.Name)
		}
		identifier, ok := object["$id"].(string)
		if !ok || identifier == "" {
			return nil, fmt.Errorf("schema %s has no $id", descriptor.Name)
		}
		if err := compiler.AddResource(identifier, document); err != nil {
			return nil, fmt.Errorf("register schema %s: %w", descriptor.Name, err)
		}
		identifiers[descriptor.Kind] = identifier
	}

	compiled := make(map[domain.Kind]*jsonschema.Schema, len(descriptors))
	for _, descriptor := range descriptors {
		schema, err := compiler.Compile(identifiers[descriptor.Kind])
		if err != nil {
			return nil, fmt.Errorf("compile schema %s: %w", descriptor.Name, err)
		}
		compiled[descriptor.Kind] = schema
	}
	return &Validator{compiled: compiled}, nil
}

// ValidateDocument detects the kind, applies its JSON Schema, and then runs
// kind-specific semantic validation.
func (v *Validator) ValidateDocument(data []byte) (domain.Record, error) {
	var envelope struct {
		APIVersion domain.APIVersion `json:"apiVersion"`
		Kind       string            `json:"kind"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return domain.Record{}, fmt.Errorf("decode record envelope: %w", err)
	}
	kind, err := domain.ParseKind(envelope.Kind)
	if err != nil {
		return domain.Record{}, err
	}
	if envelope.APIVersion != domain.APIVersionV1Alpha1 {
		return domain.Record{}, fmt.Errorf("unsupported apiVersion %q", envelope.APIVersion)
	}
	if err := v.Validate(kind, data); err != nil {
		return domain.Record{}, err
	}
	return domain.Record{Kind: kind, Data: append([]byte(nil), data...)}, nil
}

// Validate applies the schema registered for kind and semantic rules. It also
// rejects a valid document presented under the wrong kind.
func (v *Validator) Validate(kind domain.Kind, data []byte) error {
	schema, ok := v.compiled[kind]
	if !ok {
		return fmt.Errorf("no compiled schema for kind %q", kind)
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("decode %s: %w", kind, err)
	}
	if err := schema.Validate(document); err != nil {
		return fmt.Errorf("validate %s schema: %w", kind, err)
	}
	var envelope struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return fmt.Errorf("decode %s envelope: %w", kind, err)
	}
	if envelope.Kind != string(kind) {
		return fmt.Errorf("record kind %q does not match requested kind %q", envelope.Kind, kind)
	}
	if err := validateSemantics(kind, data); err != nil {
		return err
	}
	return nil
}
