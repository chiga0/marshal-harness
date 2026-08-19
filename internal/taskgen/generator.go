// Package taskgen prepares complete TaskSpec documents before schema
// admission. It owns only scaffolding defaults: planning continues to consume
// the generated TaskSpec's explicit preferred/fallback order without runtime
// insertion or reordering.
package taskgen

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
)

var defaultWorkerOrder = []string{"qoder", "codex", "qwen", "pi"}

var (
	ErrInvalidDraft       = errors.New("task scaffolding: invalid draft")
	ErrIncompleteOrder    = errors.New("task scaffolding: preferredAdapter and fallbackAdapters must be declared together")
	ErrOpenCodeIneligible = errors.New("task scaffolding: OpenCode is not eligible for new tasks")
)

// Selection is an optional operator-supplied Worker order. Preferred is
// followed by Fallback exactly as supplied.
type Selection struct {
	Preferred string
	Fallback  []string
}

// Generate turns a TaskSpec draft into a schema-valid TaskSpec. A draft that
// omits both Worker selection fields receives the production default. A draft
// that declares both fields preserves their order exactly. An override, when
// present, replaces both fields explicitly. OpenCode is rejected at this
// pre-admission generation boundary and can never enter a newly generated
// TaskSpec.
func Generate(draft []byte, override *Selection, validator *contract.Validator) ([]byte, error) {
	if validator == nil {
		return nil, ErrInvalidDraft
	}
	clean, err := canonical.JSON(draft)
	if err != nil {
		return nil, ErrInvalidDraft
	}
	var document map[string]json.RawMessage
	if json.Unmarshal(clean, &document) != nil {
		return nil, ErrInvalidDraft
	}
	workerRaw, ok := document["worker"]
	if !ok {
		return nil, ErrInvalidDraft
	}
	var worker map[string]json.RawMessage
	if json.Unmarshal(workerRaw, &worker) != nil {
		return nil, ErrInvalidDraft
	}

	draftSelection, err := resolveSelection(worker)
	if err != nil {
		return nil, err
	}
	if containsOpenCode(draftSelection) {
		return nil, ErrOpenCodeIneligible
	}
	selection := draftSelection
	if override != nil {
		selection = cloneSelection(*override)
	}
	if containsOpenCode(selection) {
		return nil, ErrOpenCodeIneligible
	}
	preferred, err := json.Marshal(selection.Preferred)
	if err != nil {
		return nil, ErrInvalidDraft
	}
	fallback, err := json.Marshal(selection.Fallback)
	if err != nil {
		return nil, ErrInvalidDraft
	}
	worker["preferredAdapter"] = preferred
	worker["fallbackAdapters"] = fallback
	workerData, err := json.Marshal(worker)
	if err != nil {
		return nil, ErrInvalidDraft
	}
	document["worker"] = workerData
	generated, err := json.Marshal(document)
	if err != nil {
		return nil, ErrInvalidDraft
	}
	if err := validator.Validate(domain.KindTask, generated); err != nil {
		return nil, ErrInvalidDraft
	}
	return generated, nil
}

func resolveSelection(worker map[string]json.RawMessage) (Selection, error) {
	preferredRaw, hasPreferred := worker["preferredAdapter"]
	fallbackRaw, hasFallback := worker["fallbackAdapters"]
	if hasPreferred != hasFallback {
		return Selection{}, ErrIncompleteOrder
	}
	if !hasPreferred {
		return Selection{Preferred: defaultWorkerOrder[0], Fallback: slices.Clone(defaultWorkerOrder[1:])}, nil
	}
	var selection Selection
	if json.Unmarshal(preferredRaw, &selection.Preferred) != nil || json.Unmarshal(fallbackRaw, &selection.Fallback) != nil {
		return Selection{}, ErrInvalidDraft
	}
	// A JSON null is not a valid fallbackAdapters value. Keep invalid draft
	// input fail-closed while allowing a typed operator override with a nil
	// slice to mean the explicit empty fallback order.
	if selection.Fallback == nil {
		return Selection{}, ErrInvalidDraft
	}
	return cloneSelection(selection), nil
}

func cloneSelection(selection Selection) Selection {
	fallback := make([]string, len(selection.Fallback))
	copy(fallback, selection.Fallback)
	return Selection{Preferred: selection.Preferred, Fallback: fallback}
}

func containsOpenCode(selection Selection) bool {
	if strings.EqualFold(strings.TrimSpace(selection.Preferred), "opencode") {
		return true
	}
	for _, candidate := range selection.Fallback {
		if strings.EqualFold(strings.TrimSpace(candidate), "opencode") {
			return true
		}
	}
	return false
}
