// This file defines the non-durable prompt projection v1 version identifier,
// the closed field-classification catalog, and Schema-to-projection coverage
// helpers. It does not modify the durable TaskSpec/WorkerRequest/Run Schema,
// does not change any field's existing visibility, and does not introduce a
// new durable record or digest. The catalog only describes the visibility
// boundary already implemented by renderPrompt; it does not drive
// authorization or replace the TaskSpec Schema.

package execution

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// taskSpecPromptProjectionVersionV1 is the non-sensitive version identifier
// for the Worker prompt projection established by Issue #22. It is emitted
// exactly once in the rendered prompt near the fixed rules and does not enter
// WorkerRequest, RunState, or the TaskSpec Schema. It does not trigger legacy
// frozen Run byte-rewriting.
const taskSpecPromptProjectionVersionV1 = "task-spec-worker-prompt/v1"

// promptProjectionClassification is the closed set of field visibility
// decisions for TaskSpec Schema paths.
type promptProjectionClassification string

const (
	workerVisible promptProjectionClassification = "worker-visible"
	verifierOnly  promptProjectionClassification = "verifier-only"
	hidden        promptProjectionClassification = "hidden"
)

// catalogEntry pairs a JSON Pointer style TaskSpec Schema path with its single
// projection classification decision. The catalog is a slice, not a map, so a
// duplicate path survives and is mechanically rejected by validateCatalog
// instead of being silently swallowed by Go map semantics.
type catalogEntry struct {
	schemaPath string
	visibility promptProjectionClassification
}

// taskSpecPromptProjectionCatalog is the unique, authoritative field
// classification catalog for the TaskSpec Schema. Each JSON Pointer style
// path is classified exactly once. Array elements are written as /* and open
// maps end with /*. The catalog only describes the existing visibility
// boundary established by renderPrompt; it does not drive authorization or
// replace the TaskSpec Schema. When a new TaskSpec field is added, it must be
// classified here in the same change or the coverage gate will fail. It is
// built with append statements so duplicate paths remain detectable by
// validateCatalog and the declaration does not rely on composite-literal
// alignment.
var taskSpecPromptProjectionCatalog = buildTaskSpecPromptProjectionCatalog()

func buildTaskSpecPromptProjectionCatalog() []catalogEntry {
	var c []catalogEntry
	// Worker-visible: projected verbatim into the Worker prompt.
	c = append(c, catalogEntry{"/metadata/id", workerVisible})
	c = append(c, catalogEntry{"/work/objective", workerVisible})
	c = append(c, catalogEntry{"/work/context/*", workerVisible})
	c = append(c, catalogEntry{"/work/constraints/*", workerVisible})
	c = append(c, catalogEntry{"/work/nonGoals/*", workerVisible})
	c = append(c, catalogEntry{"/scope/allowPaths/*", workerVisible})
	c = append(c, catalogEntry{"/scope/denyPaths/*", workerVisible})
	c = append(c, catalogEntry{"/scope/allowSubmodules", workerVisible})
	c = append(c, catalogEntry{"/scope/maxChangedFiles", workerVisible})
	c = append(c, catalogEntry{"/scope/maxDiffBytes", workerVisible})
	c = append(c, catalogEntry{"/deliverables/*/id", workerVisible})
	c = append(c, catalogEntry{"/deliverables/*/kind", workerVisible})
	c = append(c, catalogEntry{"/deliverables/*/required", workerVisible})
	c = append(c, catalogEntry{"/deliverables/*/pathGlob", workerVisible})
	c = append(c, catalogEntry{"/deliverables/*/mediaType", workerVisible})
	c = append(c, catalogEntry{"/deliverables/*/minimumCount", workerVisible})
	c = append(c, catalogEntry{"/deliverables/*/description", workerVisible})
	c = append(c, catalogEntry{"/worker/executionProfile", workerVisible})
	c = append(c, catalogEntry{"/worker/readRoots/*", workerVisible})
	c = append(c, catalogEntry{"/worker/sessionPolicy", workerVisible})
	c = append(c, catalogEntry{"/budgets/runTimeoutSeconds", workerVisible})
	c = append(c, catalogEntry{"/budgets/attemptTimeoutSeconds", workerVisible})
	c = append(c, catalogEntry{"/budgets/maxAttempts", workerVisible})
	c = append(c, catalogEntry{"/budgets/maxOperationalRetries", workerVisible})
	c = append(c, catalogEntry{"/budgets/maxReworkRounds", workerVisible})
	c = append(c, catalogEntry{"/budgets/maxOutputBytes", workerVisible})
	// Verifier-only: never enter the Worker prompt.
	c = append(c, catalogEntry{"/acceptance/commands/*/id", verifierOnly})
	c = append(c, catalogEntry{"/acceptance/commands/*/argv/*", verifierOnly})
	c = append(c, catalogEntry{"/acceptance/commands/*/cwd", verifierOnly})
	c = append(c, catalogEntry{"/acceptance/commands/*/timeoutSeconds", verifierOnly})
	c = append(c, catalogEntry{"/acceptance/commands/*/required", verifierOnly})
	c = append(c, catalogEntry{"/acceptance/commands/*/baselinePolicy", verifierOnly})
	c = append(c, catalogEntry{"/acceptance/commands/*/maxLogBytes", verifierOnly})
	c = append(c, catalogEntry{"/acceptance/allowNoChange", verifierOnly})
	// Hidden: never enter the Worker prompt.
	c = append(c, catalogEntry{"/apiVersion", hidden})
	c = append(c, catalogEntry{"/kind", hidden})
	c = append(c, catalogEntry{"/metadata/title", hidden})
	c = append(c, catalogEntry{"/metadata/description", hidden})
	c = append(c, catalogEntry{"/metadata/labels/*", hidden})
	c = append(c, catalogEntry{"/repository/path", hidden})
	c = append(c, catalogEntry{"/repository/baseRef", hidden})
	c = append(c, catalogEntry{"/repository/remote", hidden})
	c = append(c, catalogEntry{"/repository/expectedRemoteUrl", hidden})
	c = append(c, catalogEntry{"/worker/preferredAdapter", hidden})
	c = append(c, catalogEntry{"/worker/fallbackAdapters/*", hidden})
	c = append(c, catalogEntry{"/worker/model", hidden})
	c = append(c, catalogEntry{"/worker/reasoning", hidden})
	// ciObserveTimeoutSeconds controls Publisher-side remote check observation;
	// it grants no capability and is irrelevant to Worker execution.
	c = append(c, catalogEntry{"/budgets/ciObserveTimeoutSeconds", hidden})
	// worker.tools is consumed by the adapter enforcement layer (provider
	// call-layer allowlists) and the Verification tool-allowlist gate; the
	// prompt already carries constraints free text, so the declaration is
	// never rendered into the Worker prompt.
	c = append(c, catalogEntry{"/worker/tools/*", hidden})
	c = append(c, catalogEntry{"/publication/required", hidden})
	c = append(c, catalogEntry{"/publication/provider", hidden})
	c = append(c, catalogEntry{"/publication/mode", hidden})
	c = append(c, catalogEntry{"/publication/remote", hidden})
	c = append(c, catalogEntry{"/publication/baseBranch", hidden})
	c = append(c, catalogEntry{"/publication/mergePolicy", hidden})
	// mergeMethod belongs to Publisher/Merger permission control: the Worker
	// must never derive merge authority from it, so it stays hidden under
	// the existing security model.
	c = append(c, catalogEntry{"/publication/mergeMethod", hidden})
	c = append(c, catalogEntry{"/publication/requiredChecks/*", hidden})
	// admission, dependsOn and preconditions are scheduling metadata consumed
	// by the planning admission gate; they never enter the Worker prompt.
	c = append(c, catalogEntry{"/admission/status", hidden})
	c = append(c, catalogEntry{"/dependsOn/*/kind", hidden})
	c = append(c, catalogEntry{"/dependsOn/*/runId", hidden})
	c = append(c, catalogEntry{"/dependsOn/*/taskId", hidden})
	c = append(c, catalogEntry{"/dependsOn/*/requiredState", hidden})
	c = append(c, catalogEntry{"/dependsOn/*/baseSha", hidden})
	c = append(c, catalogEntry{"/dependsOn/*/specDigest", hidden})
	c = append(c, catalogEntry{"/preconditions/*/id", hidden})
	c = append(c, catalogEntry{"/preconditions/*/argv/*", hidden})
	c = append(c, catalogEntry{"/preconditions/*/cwd", hidden})
	c = append(c, catalogEntry{"/preconditions/*/timeoutSeconds", hidden})
	c = append(c, catalogEntry{"/extensions/*", hidden})
	return c
}

// validateCatalog is the single closed-validation seam for the projection
// catalog. It accepts an injectable slice of entries together with the set of
// paths that the embedded TaskSpec Schema actually defines, and returns a
// descriptive error as soon as it finds an entry whose classification is not
// one of workerVisible, verifierOnly or hidden (unknown classification), a
// path that appears more than once (duplicate decision), or a path that is
// absent from schemaPathSet (catalog-only path). It detects duplicates by
// scanning the slice directly rather than relying on a Go map to
// deduplicate, so the rejection is mechanically provable. It is the same
// validation applied to the production catalog and to malformed catalogs
// injected by tests. It does not check Schema-to-catalog coverage (every
// schema path classified); that is the job of the coverage gate.
func validateCatalog(entries []catalogEntry, schemaPathSet map[string]bool) error {
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		switch entry.visibility {
		case workerVisible, verifierOnly, hidden:
		default:
			return fmt.Errorf("catalog entry %q has unknown classification %q", entry.schemaPath, entry.visibility)
		}
		if seen[entry.schemaPath] {
			return fmt.Errorf("catalog has duplicate path %q", entry.schemaPath)
		}
		seen[entry.schemaPath] = true
		if !schemaPathSet[entry.schemaPath] {
			return fmt.Errorf("catalog path %q does not exist in the schema (catalog-only path)", entry.schemaPath)
		}
	}
	return nil
}

// catalogSortedPaths returns the de-duplicated, sorted set of catalog paths.
// De-duplication is safe here because duplicate detection is performed by
// validateCatalog; this helper is only used for set membership and coverage
// comparison.
func catalogSortedPaths() []string {
	set := catalogPathSet(taskSpecPromptProjectionCatalog)
	result := make([]string, 0, len(set))
	for path := range set {
		result = append(result, path)
	}
	sort.Strings(result)
	return result
}

// catalogPathsByClassification returns the sorted catalog paths that carry the
// given classification.
func catalogPathsByClassification(classification promptProjectionClassification) []string {
	paths := make([]string, 0)
	for _, entry := range taskSpecPromptProjectionCatalog {
		if entry.visibility == classification {
			paths = append(paths, entry.schemaPath)
		}
	}
	sort.Strings(paths)
	return paths
}

// catalogPathSet returns the set of paths in entries. Duplicate paths are
// collapsed; duplicate detection is the responsibility of validateCatalog.
func catalogPathSet(entries []catalogEntry) map[string]bool {
	set := make(map[string]bool, len(entries))
	for _, entry := range entries {
		set[entry.schemaPath] = true
	}
	return set
}

// schemaLeafPaths traverses a raw JSON Schema document and returns every
// leaf/prefix path using JSON Pointer style notation. It resolves local $ref,
// recurses into object properties, array items, and additionalProperties map
// prefixes. Array elements are written as /* and open maps end with /*.
// Intermediate object containers (e.g. /metadata, /work) are not emitted as
// separate entries; only terminal leaves and /* element/map prefixes are
// produced. The result is sorted and de-duplicated.
func schemaLeafPaths(rawSchema []byte) ([]string, error) {
	var root any
	if err := json.Unmarshal(rawSchema, &root); err != nil {
		return nil, fmt.Errorf("parse schema: %w", err)
	}
	rootMap, ok := root.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("schema root is not an object")
	}
	var paths []string
	visited := make(map[string]bool)
	var visit func(node any, path string, depth int) error
	visit = func(node any, path string, depth int) error {
		if depth > 64 {
			return fmt.Errorf("schema traversal exceeded depth limit at %q", path)
		}
		nodeMap, ok := node.(map[string]any)
		if !ok {
			// Non-map node (boolean true, etc.): treat as leaf.
			if path != "" {
				paths = append(paths, path)
			}
			return nil
		}
		// Resolve $ref first (JSON Schema: $ref overrides other keywords).
		if ref, hasRef := nodeMap["$ref"]; hasRef {
			refStr, ok := ref.(string)
			if !ok {
				return fmt.Errorf("$ref at %q is not a string", path)
			}
			visitedKey := refStr + "\x00" + path
			if visited[visitedKey] {
				return nil
			}
			visited[visitedKey] = true
			resolved, err := resolveSchemaRef(refStr, rootMap)
			if err != nil {
				return err
			}
			return visit(resolved, path, depth+1)
		}
		// Object with properties: recurse into each property (sorted for
		// deterministic output). The container itself is not emitted.
		if props, hasProps := nodeMap["properties"].(map[string]any); hasProps {
			keys := make([]string, 0, len(props))
			for k := range props {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if err := visit(props[k], path+"/"+k, depth+1); err != nil {
					return err
				}
			}
			// If additionalProperties is not false, the open-map part
			// produces /* for the additional keys.
			if ap, hasAP := nodeMap["additionalProperties"]; hasAP && ap != false {
				if path != "" {
					paths = append(paths, path+"/*")
				}
			}
			return nil
		}
		// Array with items: the item schema is visited at path+/*.
		if items, hasItems := nodeMap["items"]; hasItems {
			return visit(items, path+"/*", depth+1)
		}
		// Object with additionalProperties but no properties (open map).
		if ap, hasAP := nodeMap["additionalProperties"]; hasAP && ap != false {
			if apMap, ok := ap.(map[string]any); ok {
				// If the value schema has sub-structure, recurse.
				if _, hasProps := apMap["properties"]; hasProps {
					return visit(apMap, path+"/*", depth+1)
				}
				if _, hasItems := apMap["items"]; hasItems {
					return visit(apMap, path+"/*", depth+1)
				}
				if _, hasRef := apMap["$ref"]; hasRef {
					return visit(apMap, path+"/*", depth+1)
				}
			}
			// additionalProperties is true, {}, or a leaf schema.
			if path != "" {
				paths = append(paths, path+"/*")
			}
			return nil
		}
		// Leaf node (string, integer, boolean, const, etc.).
		if path != "" {
			paths = append(paths, path)
		}
		return nil
	}
	if err := visit(rootMap, "", 0); err != nil {
		return nil, err
	}
	sort.Strings(paths)
	result := make([]string, 0, len(paths))
	for i, p := range paths {
		if i == 0 || p != paths[i-1] {
			result = append(result, p)
		}
	}
	return result, nil
}

// resolveSchemaRef resolves a local JSON Pointer $ref like "#/$defs/metadata"
// against the root schema document.
func resolveSchemaRef(ref string, root map[string]any) (map[string]any, error) {
	if !strings.HasPrefix(ref, "#/") && ref != "#" {
		return nil, fmt.Errorf("unsupported $ref (only local refs starting with #/ are supported): %s", ref)
	}
	if ref == "#" {
		return root, nil
	}
	segments := strings.Split(strings.TrimPrefix(ref, "#/"), "/")
	var current any = root
	for _, seg := range segments {
		seg = strings.ReplaceAll(seg, "~1", "/")
		seg = strings.ReplaceAll(seg, "~0", "~")
		m, ok := current.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("$ref segment %q in %s is not an object", seg, ref)
		}
		current, ok = m[seg]
		if !ok {
			return nil, fmt.Errorf("$ref segment %q not found in %s", seg, ref)
		}
	}
	result, ok := current.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("$ref %s does not resolve to a schema object", ref)
	}
	return result, nil
}
