package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

// workerVisibleOracle is the explicit, hand-maintained oracle of every
// worker-visible catalog path. It is bidirectionally checked against the
// catalog to detect silent additions or omissions.
var workerVisibleOracle = []string{
	"/budgets/attemptTimeoutSeconds",
	"/budgets/maxAttempts",
	"/budgets/maxOperationalRetries",
	"/budgets/maxOutputBytes",
	"/budgets/maxReworkRounds",
	"/budgets/runTimeoutSeconds",
	"/deliverables/*/description",
	"/deliverables/*/id",
	"/deliverables/*/kind",
	"/deliverables/*/mediaType",
	"/deliverables/*/minimumCount",
	"/deliverables/*/pathGlob",
	"/deliverables/*/required",
	"/metadata/id",
	"/scope/allowPaths/*",
	"/scope/allowSubmodules",
	"/scope/denyPaths/*",
	"/scope/maxChangedFiles",
	"/scope/maxDiffBytes",
	"/work/context/*",
	"/work/constraints/*",
	"/work/nonGoals/*",
	"/work/objective",
	"/worker/executionProfile",
	"/worker/readRoots/*",
	"/worker/sessionPolicy",
}

// nonLeakOracle is the explicit, hand-maintained oracle of every
// verifier-only and hidden catalog path. It is bidirectionally checked
// against the catalog to detect silent additions or omissions.
var nonLeakOracle = []string{
	"/acceptance/allowNoChange",
	"/acceptance/commands/*/argv/*",
	"/acceptance/commands/*/baselinePolicy",
	"/acceptance/commands/*/cwd",
	"/acceptance/commands/*/id",
	"/acceptance/commands/*/maxLogBytes",
	"/acceptance/commands/*/required",
	"/acceptance/commands/*/timeoutSeconds",
	"/admission/status",
	"/apiVersion",
	"/dependsOn/*/baseSha",
	"/dependsOn/*/kind",
	"/dependsOn/*/requiredState",
	"/dependsOn/*/runId",
	"/dependsOn/*/specDigest",
	"/dependsOn/*/taskId",
	"/extensions/*",
	"/kind",
	"/metadata/description",
	"/metadata/labels/*",
	"/metadata/title",
	"/preconditions/*/argv/*",
	"/preconditions/*/cwd",
	"/preconditions/*/id",
	"/preconditions/*/timeoutSeconds",
	"/publication/baseBranch",
	"/publication/mergeMethod",
	"/publication/mergePolicy",
	"/publication/mode",
	"/publication/provider",
	"/publication/remote",
	"/publication/required",
	"/publication/requiredChecks/*",
	"/repository/baseRef",
	"/repository/expectedRemoteUrl",
	"/repository/path",
	"/repository/remote",
	"/worker/fallbackAdapters/*",
	"/worker/model",
	"/worker/preferredAdapter",
	"/worker/reasoning",
	"/worker/tools/*",
}

func stringSet(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, v := range values {
		set[v] = true
	}
	return set
}

// TestTaskSpecPromptProjectionV1CatalogCoversSchema reads the embedded
// TaskSpec Schema via contract.SchemaDocument, traverses every leaf/prefix
// path, and asserts that the catalog classifies each one exactly. This is
// the fail-closed coverage gate: adding a new Schema field without
// classifying it here causes this test to fail.
func TestTaskSpecPromptProjectionV1CatalogCoversSchema(t *testing.T) {
	rawSchema, err := contract.SchemaDocument("task-spec")
	if err != nil {
		t.Fatalf("contract.SchemaDocument failed: %v", err)
	}
	schemaPaths, err := schemaLeafPaths(rawSchema)
	if err != nil {
		t.Fatalf("schemaLeafPaths failed: %v", err)
	}
	catalogPaths := catalogSortedPaths()
	schemaSet := stringSet(schemaPaths)
	catalogSet := stringSet(catalogPaths)

	// Every schema path must be classified in the catalog.
	for _, p := range schemaPaths {
		if !catalogSet[p] {
			t.Errorf("schema path %q is not classified in the catalog (add it or the coverage gate stays closed)", p)
		}
	}
	// Every catalog path must exist in the schema.
	for _, p := range catalogPaths {
		if !schemaSet[p] {
			t.Errorf("catalog path %q does not exist in the schema (catalog-only path)", p)
		}
	}
}

// TestTaskSpecPromptProjectionV1RejectsUnclassifiedSchemaLeaf injects a
// synthetic future leaf into an in-memory copy of the TaskSpec Schema and
// proves the coverage helper explicitly reports the unclassified path. The
// on-disk Schema is not modified and the test does not depend on ordering.
func TestTaskSpecPromptProjectionV1RejectsUnclassifiedSchemaLeaf(t *testing.T) {
	rawSchema, err := contract.SchemaDocument("task-spec")
	if err != nil {
		t.Fatalf("contract.SchemaDocument failed: %v", err)
	}
	var schema map[string]any
	if err := json.Unmarshal(rawSchema, &schema); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	// Inject a synthetic future leaf into the work definition.
	defs, ok := schema["$defs"].(map[string]any)
	if !ok {
		t.Fatal("schema has no $defs")
	}
	workDef, ok := defs["work"].(map[string]any)
	if !ok {
		t.Fatal("schema has no $defs/work")
	}
	workProps, ok := workDef["properties"].(map[string]any)
	if !ok {
		t.Fatal("$defs/work has no properties")
	}
	workProps["futureField"] = map[string]any{"type": "string"}

	modifiedSchema, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("marshal modified schema: %v", err)
	}
	schemaPaths, err := schemaLeafPaths(modifiedSchema)
	if err != nil {
		t.Fatalf("schemaLeafPaths failed: %v", err)
	}

	// The synthetic path must be discovered by the traversal.
	found := false
	for _, p := range schemaPaths {
		if p == "/work/futureField" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("synthetic leaf /work/futureField was not discovered by schemaLeafPaths")
	}

	// The synthetic path must not be in the catalog.
	if catalogPathSet(taskSpecPromptProjectionCatalog)["/work/futureField"] {
		t.Fatalf("synthetic leaf /work/futureField should not be in the catalog")
	}

	// The coverage diff must report it as missing from the catalog.
	catalogSet := stringSet(catalogSortedPaths())
	missing := false
	for _, p := range schemaPaths {
		if !catalogSet[p] {
			missing = true
			if p != "/work/futureField" {
				t.Errorf("unexpected unclassified path %q (only /work/futureField should be unclassified)", p)
			}
		}
	}
	if !missing {
		t.Fatalf("coverage diff did not report /work/futureField as missing from catalog")
	}
}

// TestTaskSpecPromptProjectionV1CatalogHasSingleDecisionPerField verifies
// that the catalog is well-formed through a single injectable validation
// seam. It proves that the production catalog passes the same closed
// validation used by tests, and that malformed catalogs are rejected for
// each of the three failure modes: unknown classification, duplicate path,
// and catalog-only path. The seam accepts an injectable slice of entries so
// a duplicate path survives and is detected instead of being silently
// swallowed by Go map semantics.
func TestTaskSpecPromptProjectionV1CatalogHasSingleDecisionPerField(t *testing.T) {
	rawSchema, err := contract.SchemaDocument("task-spec")
	if err != nil {
		t.Fatalf("contract.SchemaDocument failed: %v", err)
	}
	schemaPaths, err := schemaLeafPaths(rawSchema)
	if err != nil {
		t.Fatalf("schemaLeafPaths failed: %v", err)
	}
	schemaPathSet := stringSet(schemaPaths)

	// The production catalog must pass the same closed validation.
	if err := validateCatalog(taskSpecPromptProjectionCatalog, schemaPathSet); err != nil {
		t.Fatalf("production catalog failed closed validation: %v", err)
	}

	// A duplicate path must be rejected. The path is real so the only
	// failure mode is the duplicate; it survives because the catalog is a
	// slice, not a map.
	duplicate := []catalogEntry{{"/metadata/id", workerVisible}, {"/metadata/id", workerVisible}}
	if err := validateCatalog(duplicate, schemaPathSet); err == nil {
		t.Error("validateCatalog must reject a duplicate path, got nil")
	} else if !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("validateCatalog must report a duplicate, got: %v", err)
	}

	// An unknown classification must be rejected. The path is real so the
	// only failure mode is the unknown classification.
	unknown := []catalogEntry{{"/metadata/id", "bogus"}}
	if err := validateCatalog(unknown, schemaPathSet); err == nil {
		t.Error("validateCatalog must reject an unknown classification, got nil")
	} else if !strings.Contains(err.Error(), "unknown classification") {
		t.Errorf("validateCatalog must report an unknown classification, got: %v", err)
	}

	// A catalog-only path (absent from the schema) must be rejected. The
	// classification is valid so the only failure mode is the catalog-only
	// path.
	catalogOnly := []catalogEntry{{"/work/syntheticFutureField", workerVisible}}
	if err := validateCatalog(catalogOnly, schemaPathSet); err == nil {
		t.Error("validateCatalog must reject a catalog-only path, got nil")
	} else if !strings.Contains(err.Error(), "catalog-only") {
		t.Errorf("validateCatalog must report a catalog-only path, got: %v", err)
	}
}

// TestTaskSpecPromptProjectionV1WorkerVisibleFieldsHaveExactOracle
// bidirectionally checks the worker-visible oracle against the catalog.
func TestTaskSpecPromptProjectionV1WorkerVisibleFieldsHaveExactOracle(t *testing.T) {
	catalogVisible := catalogPathsByClassification(workerVisible)
	catalogSet := stringSet(catalogVisible)
	oracleSet := stringSet(workerVisibleOracle)

	for _, p := range workerVisibleOracle {
		if !catalogSet[p] {
			t.Errorf("oracle path %q is not in the catalog as worker-visible", p)
		}
	}
	for _, p := range catalogVisible {
		if !oracleSet[p] {
			t.Errorf("catalog worker-visible path %q is not in the oracle", p)
		}
	}
}

// TestRenderPromptProjectionV1WorkerVisibleValuesAreExact injects unique
// sentinels, numbers, and booleans into every worker-visible field and
// proves the prompt presents them in original order and original value.
// metadata.id is projected via state.TaskID (alternate identity source);
// the existing equality guard in Run (task.Metadata.ID == state.TaskID)
// ensures they match before renderPrompt is reached.
func TestRenderPromptProjectionV1WorkerVisibleValuesAreExact(t *testing.T) {
	spec := promptFixtureSpec()

	// Inject unique sentinels for worker-visible fields.
	spec["metadata"].(map[string]any)["id"] = "wv-meta-id-4242"
	spec["work"].(map[string]any)["objective"] = "wv-objective-sentinel"
	spec["work"].(map[string]any)["context"] = []string{"wv-ctx-alpha", "wv-ctx-beta"}
	spec["work"].(map[string]any)["constraints"] = []string{"wv-cstr-alpha"}
	spec["work"].(map[string]any)["nonGoals"] = []string{"wv-ng-alpha"}
	spec["scope"].(map[string]any)["allowPaths"] = []string{"wv-allow-alpha"}
	spec["scope"].(map[string]any)["denyPaths"] = []string{"wv-deny-alpha"}
	spec["scope"].(map[string]any)["allowSubmodules"] = true
	spec["scope"].(map[string]any)["maxChangedFiles"] = 777
	spec["scope"].(map[string]any)["maxDiffBytes"] = 8888

	deliverables := spec["deliverables"].([]any)
	deliverables[0].(map[string]any)["id"] = "wv-deliv-id"
	deliverables[0].(map[string]any)["kind"] = "code"
	deliverables[0].(map[string]any)["required"] = true
	deliverables[0].(map[string]any)["pathGlob"] = "wv-deliv-path"
	deliverables[0].(map[string]any)["mediaType"] = "wv-deliv-media"
	deliverables[0].(map[string]any)["minimumCount"] = 9
	deliverables[0].(map[string]any)["description"] = "wv-deliv-desc"

	spec["worker"].(map[string]any)["executionProfile"] = "workspace-write"
	spec["worker"].(map[string]any)["sessionPolicy"] = "ephemeral"

	spec["budgets"].(map[string]any)["runTimeoutSeconds"] = 111
	spec["budgets"].(map[string]any)["attemptTimeoutSeconds"] = 22
	spec["budgets"].(map[string]any)["maxAttempts"] = 33
	spec["budgets"].(map[string]any)["maxOperationalRetries"] = 4
	spec["budgets"].(map[string]any)["maxReworkRounds"] = 5
	spec["budgets"].(map[string]any)["maxOutputBytes"] = 6666

	taskData, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	var task domain.TaskSpec
	if err := json.Unmarshal(taskData, &task); err != nil {
		t.Fatal(err)
	}

	// metadata.id is projected via state.TaskID (alternate identity source).
	// The equality guard in Run ensures task.Metadata.ID == state.TaskID.
	state := promptFixtureState()
	state.TaskID = "wv-meta-id-4242"

	prompt, err := renderPrompt(taskData, task, state, "attempt-wv", promptFixtureControlRoot, "wv-adapter", nil)
	if err != nil {
		t.Fatalf("renderPrompt failed: %v", err)
	}

	// Check string sentinels appear verbatim in the prompt.
	stringSentinels := []string{
		"wv-meta-id-4242",
		"wv-objective-sentinel",
		"wv-ctx-alpha",
		"wv-ctx-beta",
		"wv-cstr-alpha",
		"wv-ng-alpha",
		"wv-allow-alpha",
		"wv-deny-alpha",
		"wv-deliv-id",
		"wv-deliv-path",
		"wv-deliv-media",
		"wv-deliv-desc",
		"workspace-write",
		"ephemeral",
	}
	for _, s := range stringSentinels {
		if !strings.Contains(prompt, s) {
			t.Errorf("prompt missing worker-visible sentinel %q", s)
		}
	}

	// Check formatted numeric/boolean sentinels appear verbatim.
	formatSentinels := []string{
		"- allowSubmodules：true",
		"- maxChangedFiles：777 个文件",
		"- maxDiffBytes：8888 字节",
		"- runTimeoutSeconds：111 秒",
		"- attemptTimeoutSeconds：22 秒",
		"- maxAttempts：33 次尝试",
		"- maxOperationalRetries：4 次运维重试",
		"- maxReworkRounds：5 轮 rework",
		"- maxOutputBytes：6666 字节",
		`"minimumCount":9`,
		`"required":true`,
	}
	for _, s := range formatSentinels {
		if !strings.Contains(prompt, s) {
			t.Errorf("prompt missing worker-visible formatted sentinel %q", s)
		}
	}

	// Check array order is preserved: context[0] before context[1].
	idx0 := strings.Index(prompt, "wv-ctx-alpha")
	idx1 := strings.Index(prompt, "wv-ctx-beta")
	if idx0 < 0 || idx1 < 0 || idx0 >= idx1 {
		t.Errorf("context items not in original order: idx0=%d idx1=%d", idx0, idx1)
	}

	// metadata.id is projected via taskId (alternate identity source), not
	// directly. Verify the taskId line carries the sentinel value.
	if !strings.Contains(prompt, "taskId=wv-meta-id-4242") {
		t.Errorf("metadata.id alternate identity source (taskId) not projected with sentinel value")
	}

	// Verify the version identifier appears exactly once.
	if count := strings.Count(prompt, taskSpecPromptProjectionVersionV1); count != 1 {
		t.Errorf("version identifier must appear exactly once, got %d", count)
	}

	t.Run("readRoots projected under read-only profile", func(t *testing.T) {
		readSpec := promptFixtureSpec()
		readSpec["worker"].(map[string]any)["executionProfile"] = "read-only"
		readSpec["worker"].(map[string]any)["sessionPolicy"] = "ephemeral"
		readSpec["worker"].(map[string]any)["readRoots"] = []string{"wv-readroot-alpha", "wv-readroot-beta"}
		readPrompt := renderFixturePrompt(t, readSpec, nil)
		for _, s := range []string{"wv-readroot-alpha", "wv-readroot-beta"} {
			if !strings.Contains(readPrompt, s) {
				t.Errorf("readRoots sentinel %q not projected", s)
			}
		}
		idx0 := strings.Index(readPrompt, "wv-readroot-alpha")
		idx1 := strings.Index(readPrompt, "wv-readroot-beta")
		if idx0 < 0 || idx1 < 0 || idx0 >= idx1 {
			t.Errorf("readRoots not in original order: idx0=%d idx1=%d", idx0, idx1)
		}
	})

	// The equality guard in Run ensures task.Metadata.ID == state.TaskID.
	// Test that mismatched metadata.id is rejected before renderPrompt.
	t.Run("metadata.id equality guard rejects mismatched identity", func(t *testing.T) {
		fixture := newExecutionFixture(t, false)
		taskPath := filepath.Join(fixture.runDir, "task-spec.json")
		taskData, err := os.ReadFile(taskPath)
		if err != nil {
			t.Fatal(err)
		}
		var spec map[string]any
		if err := json.Unmarshal(taskData, &spec); err != nil {
			t.Fatal(err)
		}
		spec["metadata"].(map[string]any)["id"] = "mismatched-task-id"
		newTask := mustJSON(t, spec)
		digest, err := canonical.DigestJSON(newTask)
		if err != nil {
			t.Fatal(err)
		}
		store := runstore.New(fixture.input.StateRoot)
		lease, err := store.Acquire(fixture.input.RunID)
		if err != nil {
			t.Fatal(err)
		}
		state, err := store.Inspect(fixture.input.RunID)
		if err != nil {
			t.Fatal(err)
		}
		state.SpecDigest = digest
		if err := store.WriteSnapshot(lease, state); err != nil {
			t.Fatal(err)
		}
		if err := lease.Release(); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(taskPath, newTask, 0o600); err != nil {
			t.Fatal(err)
		}
		_, err = Run(context.Background(), fixture.input)
		if err == nil || !strings.Contains(err.Error(), "identity do not match") {
			t.Fatalf("expected identity mismatch error, got: %v", err)
		}
	})
}

// TestTaskSpecPromptProjectionV1NonVisibleFieldsHaveNonLeakOracle
// bidirectionally checks the non-leak oracle (verifier-only + hidden)
// against the catalog.
func TestTaskSpecPromptProjectionV1NonVisibleFieldsHaveNonLeakOracle(t *testing.T) {
	catalogNonVisible := catalogPathsByClassification(verifierOnly)
	catalogNonVisible = append(catalogNonVisible, catalogPathsByClassification(hidden)...)
	sort.Strings(catalogNonVisible)
	catalogSet := stringSet(catalogNonVisible)
	oracleSet := stringSet(nonLeakOracle)

	for _, p := range nonLeakOracle {
		if !catalogSet[p] {
			t.Errorf("non-leak oracle path %q is not in the catalog as non-visible", p)
		}
	}
	for _, p := range catalogNonVisible {
		if !oracleSet[p] {
			t.Errorf("catalog non-visible path %q is not in the non-leak oracle", p)
		}
	}
}

// TestRenderPromptProjectionV1VerifierOnlyAndHiddenValuesDoNotLeak injects
// unique sentinels into every verifier-only and hidden field and proves
// none appear in the rendered prompt.
func TestRenderPromptProjectionV1VerifierOnlyAndHiddenValuesDoNotLeak(t *testing.T) {
	spec := promptFixtureSpec()

	// Verifier-only sentinels.
	spec["acceptance"].(map[string]any)["commands"] = []any{
		map[string]any{
			"id": "nl-accept-id", "argv": []string{"nl-accept-argv-A", "nl-accept-argv-B"},
			"cwd": "nl-accept-cwd", "timeoutSeconds": 99, "required": true,
			"baselinePolicy": "nl-baseline", "maxLogBytes": 8888,
		},
	}
	spec["acceptance"].(map[string]any)["allowNoChange"] = true

	// Hidden sentinels.
	spec["apiVersion"] = "nl-api-version"
	spec["kind"] = "nl-kind"
	spec["metadata"].(map[string]any)["title"] = "nl-meta-title"
	spec["metadata"].(map[string]any)["description"] = "nl-meta-desc"
	spec["metadata"].(map[string]any)["labels"] = map[string]any{"nl-label-key": "nl-label-value"}
	spec["repository"].(map[string]any)["path"] = "nl-repo-path"
	spec["repository"].(map[string]any)["baseRef"] = "nl-base-ref"
	spec["repository"].(map[string]any)["remote"] = "nl-remote"
	spec["repository"].(map[string]any)["expectedRemoteUrl"] = "nl-expected-url"
	spec["worker"].(map[string]any)["preferredAdapter"] = "nl-preferred"
	spec["worker"].(map[string]any)["fallbackAdapters"] = []string{"nl-fallback"}
	spec["worker"].(map[string]any)["model"] = "nl-model"
	spec["worker"].(map[string]any)["reasoning"] = "nl-reasoning"
	// worker.tools is hidden: it is consumed by the adapter enforcement
	// layer and the Verification tool-allowlist gate, never rendered. The
	// sentinel is deliberately not a closed vocabulary word; renderPrompt
	// never schema-validates the fixture, so a leak would surface verbatim.
	spec["worker"].(map[string]any)["tools"] = []string{"nl-tools-sentinel"}
	spec["publication"].(map[string]any)["required"] = true
	spec["publication"].(map[string]any)["provider"] = "nl-provider"
	spec["publication"].(map[string]any)["mode"] = "nl-mode"
	spec["publication"].(map[string]any)["remote"] = "nl-pub-remote"
	spec["publication"].(map[string]any)["baseBranch"] = "nl-base-branch"
	spec["publication"].(map[string]any)["mergePolicy"] = "nl-merge-policy"
	spec["publication"].(map[string]any)["mergeMethod"] = "nl-merge-method"
	spec["publication"].(map[string]any)["requiredChecks"] = []string{"nl-check"}
	spec["extensions"] = map[string]any{"nl.ext": "nl-ext-value"}

	// Issue #23 admission scheduling metadata is hidden: it is consumed by
	// the planning admission gate and never rendered into the Worker prompt.
	spec["admission"] = map[string]any{"status": "nl-admission-status"}
	spec["dependsOn"] = []any{map[string]any{
		"kind": "nl-dep-kind", "runId": "nl-dep-run", "taskId": "nl-dep-task",
		"requiredState": "nl-dep-state", "baseSha": "nl-dep-base", "specDigest": "nl-dep-digest",
	}}
	spec["preconditions"] = []any{map[string]any{
		"id": "nl-pre-id", "argv": []string{"nl-pre-argv"}, "cwd": "nl-pre-cwd", "timeoutSeconds": 42,
	}}

	prompt := renderFixturePrompt(t, spec, nil)

	sentinels := []string{
		"nl-accept-id", "nl-accept-argv-A", "nl-accept-argv-B", "nl-accept-cwd",
		"nl-baseline", "99", "8888",
		"allowNoChange",
		"nl-api-version", "nl-kind", "nl-meta-title", "nl-meta-desc",
		"nl-label-key", "nl-label-value", "nl-repo-path", "nl-base-ref", "nl-remote",
		"nl-expected-url", "nl-preferred", "nl-fallback", "nl-model",
		"nl-reasoning", "nl-tools-sentinel", "nl-provider", "nl-mode", "nl-pub-remote",
		"nl-base-branch", "nl-merge-policy", "nl-merge-method", "nl-check", "nl.ext", "nl-ext-value",
		"nl-admission-status", "nl-dep-kind", "nl-dep-run", "nl-dep-task",
		"nl-dep-state", "nl-dep-base", "nl-dep-digest",
		"nl-pre-id", "nl-pre-argv", "nl-pre-cwd",
	}
	for _, s := range sentinels {
		if strings.Contains(prompt, s) {
			t.Errorf("prompt leaks non-visible sentinel %q", s)
		}
	}
}

// TestRenderPromptProjectionV1AcceptanceArgvAndOperatorSecretDoNotLeak
// injects unique acceptance id, argv, cwd and an operator-secret sentinel
// into hidden metadata.labels and extensions, then proves none appear in
// the prompt. No real secret is written to any fixture, log, or error.
func TestRenderPromptProjectionV1AcceptanceArgvAndOperatorSecretDoNotLeak(t *testing.T) {
	spec := promptFixtureSpec()

	// Unique acceptance material.
	spec["acceptance"].(map[string]any)["commands"] = []any{
		map[string]any{
			"id": "secret-accept-id-XYZ123", "argv": []string{"secret-accept-argv-A", "secret-accept-argv-B"},
			"cwd": "secret-accept-cwd", "timeoutSeconds": 60, "required": true,
			"baselinePolicy": "none", "maxLogBytes": 4096,
		},
	}

	// Operator-secret sentinel in hidden metadata.labels.
	spec["metadata"].(map[string]any)["labels"] = map[string]any{
		"operator.secret": "secret-operator-token-ABC789",
	}

	// Operator-secret sentinel in hidden extensions.
	spec["extensions"] = map[string]any{
		"operator.ext": "secret-extension-token-DEF012",
	}

	prompt := renderFixturePrompt(t, spec, nil)

	secrets := []string{
		"secret-accept-id-XYZ123",
		"secret-accept-argv-A",
		"secret-accept-argv-B",
		"secret-accept-cwd",
		"operator.secret",
		"secret-operator-token-ABC789",
		"operator.ext",
		"secret-extension-token-DEF012",
	}
	for _, s := range secrets {
		if strings.Contains(prompt, s) {
			t.Errorf("prompt leaks secret sentinel %q", s)
		}
	}
}

// TestPromptProjectionV1LegacyFrozenTaskSpecRemainsReadCompatible uses the
// current v1alpha1 fixture (no projectionVersion/catalog fields) and proves
// renderPrompt and Run still read it, the version identifier appears exactly
// once, and the task-spec bytes are not rewritten.
func TestPromptProjectionV1LegacyFrozenTaskSpecRemainsReadCompatible(t *testing.T) {
	// The fixture is v1alpha1 with no projectionVersion or catalog fields.
	spec := promptFixtureSpec()
	if _, has := spec["projectionVersion"]; has {
		t.Fatal("fixture spec must not have a projectionVersion field")
	}
	if _, has := spec["promptProjectionCatalog"]; has {
		t.Fatal("fixture spec must not have a promptProjectionCatalog field")
	}

	// renderPrompt still reads the legacy fixture.
	taskData, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	originalBytes := make([]byte, len(taskData))
	copy(originalBytes, taskData)

	var task domain.TaskSpec
	if err := json.Unmarshal(taskData, &task); err != nil {
		t.Fatal(err)
	}
	prompt, err := renderPrompt(taskData, task, promptFixtureState(), "attempt-legacy", promptFixtureControlRoot, "opencode", nil)
	if err != nil {
		t.Fatalf("renderPrompt failed for legacy fixture: %v", err)
	}

	// The version identifier appears exactly once.
	if count := strings.Count(prompt, taskSpecPromptProjectionVersionV1); count != 1 {
		t.Errorf("version identifier must appear exactly once in legacy prompt, got %d", count)
	}

	// The task-spec bytes are not modified by rendering.
	if !bytes.Equal(originalBytes, taskData) {
		t.Errorf("task-spec bytes were modified by rendering")
	}

	// Run still works with a legacy v1alpha1 fixture (no projectionVersion/catalog).
	t.Run("Run still works with legacy v1alpha1 fixture", func(t *testing.T) {
		fixture := newExecutionFixture(t, false)

		// Verify the on-disk task-spec has no projectionVersion or catalog fields.
		taskPath := filepath.Join(fixture.runDir, "task-spec.json")
		diskData, err := os.ReadFile(taskPath)
		if err != nil {
			t.Fatal(err)
		}
		var diskSpec map[string]any
		if err := json.Unmarshal(diskData, &diskSpec); err != nil {
			t.Fatal(err)
		}
		if _, has := diskSpec["projectionVersion"]; has {
			t.Fatal("on-disk task-spec must not have projectionVersion")
		}
		if _, has := diskSpec["promptProjectionCatalog"]; has {
			t.Fatal("on-disk task-spec must not have promptProjectionCatalog")
		}

		originalDisk := make([]byte, len(diskData))
		copy(originalDisk, diskData)

		result, err := Run(context.Background(), fixture.input)
		if err != nil {
			t.Fatalf("Run failed with legacy fixture: %v", err)
		}
		if result.State.State != domain.StateVerifying {
			t.Fatalf("unexpected state: %s", result.State.State)
		}

		// The on-disk task-spec bytes are not rewritten by Run.
		afterDisk, err := os.ReadFile(taskPath)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(originalDisk, afterDisk) {
			t.Errorf("on-disk task-spec bytes were modified by Run")
		}
	})
}
