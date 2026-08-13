package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/port"
)

func TestNewRequiresExactExecutableAndValidator(t *testing.T) {
	validator := newValidator(t)
	if _, err := New("opencode", validator); err == nil {
		t.Fatal("relative executable accepted")
	}
	executable := fakeExecutable(t, supportedBinary, "exit 0")
	if _, err := New(executable, nil); err == nil {
		t.Fatal("nil validator accepted")
	}
	nonExecutable := filepath.Join(t.TempDir(), "opencode")
	if err := os.WriteFile(nonExecutable, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(nonExecutable, validator); err == nil {
		t.Fatal("non-executable file accepted")
	}
}

func TestPrepareTerminalFreezesNativeTUIAndResolvedPermissions(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "publisher-secret")
	fixture := newRunFixture(t, supportedBinary, "exit 0")
	spec, err := fixture.adapter.PrepareTerminal(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	expectedWorktree, err := filepath.EvalSymlinks(fixture.worktree)
	if err != nil {
		t.Fatal(err)
	}
	joinedArgs := strings.Join(spec.Arguments, "\x00")
	joinedEnv := strings.Join(spec.Environment, "\n")
	if spec.AdapterID != adapterID || spec.BinaryVersion != supportedBinary || spec.Executable != fixture.executable || !strings.HasPrefix(spec.ExecutableDigest, "sha256:") || spec.WorkingDirectory != expectedWorktree {
		t.Fatalf("identity = %+v", spec)
	}
	if spec.InitialPrompt != "完成 fixture" || spec.CompletionGate != port.TerminalCompletionSupervisedConfirmation {
		t.Fatalf("prompt/gate = %q %q", spec.InitialPrompt, spec.CompletionGate)
	}
	for _, forbidden := range []string{"run", "--format", "json", "--title", "完成 fixture", "--auto"} {
		if containsArgument(spec.Arguments, forbidden) {
			t.Fatalf("native argv contains forbidden argument %q: %#v", forbidden, spec.Arguments)
		}
	}
	if !strings.Contains(joinedArgs, "--pure") || !strings.Contains(joinedArgs, "--model") || !strings.Contains(joinedEnv, "OPENCODE_CONFIG_CONTENT=") || !strings.Contains(joinedEnv, `"task":"deny"`) {
		t.Fatalf("native policy is incomplete: args=%#v env=%s", spec.Arguments, joinedEnv)
	}
	if strings.Contains(joinedEnv, "GITHUB_TOKEN") || strings.Contains(joinedEnv, "publisher-secret") {
		t.Fatalf("publisher credential leaked: %s", joinedEnv)
	}
	if strings.Contains(joinedEnv, "CI=1") || !strings.Contains(joinedEnv, "TERM=xterm-256color") || !strings.Contains(joinedEnv, "COLORTERM=truecolor") {
		t.Fatalf("native TUI environment is not interactive: %s", joinedEnv)
	}
}

func containsArgument(arguments []string, target string) bool {
	for _, argument := range arguments {
		if argument == target {
			return true
		}
	}
	return false
}

func TestProbeFreezesSupportedAndUnsupportedBinary(t *testing.T) {
	for _, test := range []struct{ version, status string }{{supportedBinary, "supported"}, {"1.19.0", "unsupported"}} {
		t.Run(test.version, func(t *testing.T) {
			adapter, err := New(fakeExecutable(t, test.version, "exit 0"), newValidator(t))
			if err != nil {
				t.Fatal(err)
			}
			record, err := adapter.Probe(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			var snapshot struct {
				Status, Version, Digest, Executable string
			}
			var raw map[string]any
			if err := json.Unmarshal(record.Data, &raw); err != nil {
				t.Fatal(err)
			}
			snapshot.Status, _ = raw["probeStatus"].(string)
			snapshot.Version, _ = raw["binaryVersion"].(string)
			snapshot.Digest, _ = raw["executableDigest"].(string)
			snapshot.Executable, _ = raw["executable"].(string)
			if snapshot.Status != test.status || snapshot.Version != test.version || !strings.HasPrefix(snapshot.Digest, "sha256:") || !filepath.IsAbs(snapshot.Executable) {
				t.Fatalf("snapshot = %+v", snapshot)
			}
		})
	}
}

func TestPermissionConfigAndEnvironmentFailClosed(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "publisher-secret")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "cloud-secret")
	t.Setenv("OPENAI_API_KEY", "model-secret")
	controlRoot := t.TempDir()
	config, err := permissionConfig(controlRoot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(config, `"permissions"`) || !strings.Contains(config, `"permission"`) || !strings.Contains(config, `"git push *":"deny"`) || !strings.Contains(config, `"external_directory":{"*":"deny"`) || !strings.Contains(config, filepath.ToSlash(filepath.Join(controlRoot, "input"))+`/**":"deny"`) {
		t.Fatalf("unsafe config: %s", config)
	}
	environment := workerEnvironment(t.TempDir(), config)
	joined := strings.Join(environment, "\n")
	for _, secret := range []string{"GITHUB_TOKEN", "AWS_SECRET_ACCESS_KEY", "OPENAI_API_KEY", "publisher-secret", "cloud-secret", "model-secret"} {
		if strings.Contains(joined, secret) {
			t.Fatalf("worker environment leaked %s", secret)
		}
	}
	if !strings.Contains(joined, "OPENCODE_CONFIG_CONTENT=") || !strings.Contains(joined, "GIT_CONFIG_GLOBAL=/dev/null") {
		t.Fatalf("missing isolation environment: %s", joined)
	}
}

func TestResolvedPermissionValidationRejectsWildcardAndMissingIndirectDenies(t *testing.T) {
	root := t.TempDir()
	config, err := permissionConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Permission map[string]any `json:"permission"`
	}
	if err := json.Unmarshal([]byte(config), &raw); err != nil {
		t.Fatal(err)
	}
	if err := validatePermissionMap(raw.Permission, root); err != nil {
		t.Fatal(err)
	}
	raw.Permission["*"] = "allow"
	if err := validatePermissionMap(raw.Permission, root); err == nil {
		t.Fatal("allowed global wildcard accepted")
	}
}

func TestReadOnlyPermissionConfigLocksEditBashAndReadRoots(t *testing.T) {
	worktree := t.TempDir()
	controlRoot := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(outside, "repo"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(worktree, "sources")); err != nil {
		t.Fatal(err)
	}
	scope := readOnlyScope{allowPaths: []string{"reports/**", "findings.md"}, readRoots: []string{"sources/repo/"}}
	config, err := readOnlyPermissionConfig(worktree, controlRoot, scope)
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Permission map[string]any `json:"permission"`
	}
	if err := json.Unmarshal([]byte(config), &raw); err != nil {
		t.Fatal(err)
	}
	if err := validateReadOnlyPermissionMap(raw.Permission, controlRoot, worktree, scope); err != nil {
		t.Fatalf("fresh read-only config rejected: %v", err)
	}
	edit := raw.Permission["edit"].(map[string]any)
	if edit[filepath.ToSlash(filepath.Join(worktree, "reports/**"))] != "allow" || edit[filepath.ToSlash(filepath.Join(worktree, "findings.md"))] != "allow" || edit["*"] != "deny" {
		t.Fatalf("edit is not locked to allowPaths: %v", edit)
	}
	if edit[filepath.ToSlash(filepath.Join(worktree, "internal/x.go"))] != nil && edit[filepath.ToSlash(filepath.Join(worktree, "internal/x.go"))] != "deny" {
		t.Fatalf("source edit granted: %v", edit)
	}
	bash := raw.Permission["bash"].(map[string]any)
	if bash["*"] != "deny" || bash["cat *"] != "allow" || bash["sed -n *"] != "allow" || bash["git push *"] != nil {
		t.Fatalf("bash is not the read-only whitelist: %v", bash)
	}
	for _, tool := range []string{"task", "webfetch", "websearch", "question", "skill"} {
		if raw.Permission[tool] != "deny" {
			t.Fatalf("%s is not denied in read-only config", tool)
		}
	}
	outsideReal, err := filepath.EvalSymlinks(filepath.Join(outside, "repo"))
	if err != nil {
		t.Fatal(err)
	}
	external := raw.Permission["external_directory"].(map[string]any)
	if external[filepath.ToSlash(outsideReal)+"/**"] != "allow" {
		t.Fatalf("readRoot symlink target is not read-permitted: %v", external)
	}
	// Mutations must fail the resolved-config validation.
	merged, err := json.Marshal(map[string]any{"autoupdate": false, "share": "disabled", "permission": raw.Permission, "agent": map[string]any{"build": map[string]any{"permission": raw.Permission}}})
	if err != nil {
		t.Fatal(err)
	}
	fake := fakeExecutable(t, supportedBinary, "printf '%s\\n' "+shellQuote(string(merged)))
	environment := workerEnvironment(worktree, string(merged))
	if err := validateResolvedConfig(context.Background(), fake, environment, controlRoot, worktree, "read-only", scope, nil); err != nil {
		t.Fatalf("valid resolved config rejected: %v", err)
	}
	bash["*"] = "allow"
	mergedUnsafe, err := json.Marshal(map[string]any{"autoupdate": false, "share": "disabled", "permission": raw.Permission, "agent": map[string]any{"build": map[string]any{"permission": raw.Permission}}})
	if err != nil {
		t.Fatal(err)
	}
	fakeUnsafe := fakeExecutable(t, supportedBinary, "printf '%s\\n' "+shellQuote(string(mergedUnsafe)))
	if err := validateResolvedConfig(context.Background(), fakeUnsafe, workerEnvironment(worktree, string(mergedUnsafe)), controlRoot, worktree, "read-only", scope, nil); err == nil {
		t.Fatal("bash wildcard grant accepted in read-only validation")
	}
}

func TestDeclaredPermissionConfigGrantsOnlyDeclaredTools(t *testing.T) {
	controlRoot := t.TempDir()
	config, err := declaredPermissionConfig(controlRoot, []string{"read", "edit", "write"})
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Permission map[string]any `json:"permission"`
	}
	if err := json.Unmarshal([]byte(config), &raw); err != nil {
		t.Fatal(err)
	}
	for _, granted := range []string{"read", "edit", "write"} {
		if granted == "edit" {
			edit, ok := raw.Permission["edit"].(map[string]any)
			if !ok || edit["*"] != "allow" || edit[filepath.ToSlash(filepath.Join(controlRoot, "input"))+"/**"] != "deny" {
				t.Fatalf("declared edit is not granted with control input locked: %v", raw.Permission["edit"])
			}
			continue
		}
		if raw.Permission[granted] != "allow" {
			t.Fatalf("declared tool %s is not allowed: %v", granted, raw.Permission[granted])
		}
	}
	for _, denied := range []string{"grep", "glob", "list", "lsp", "bash", "question", "skill", "task", "webfetch", "websearch"} {
		if raw.Permission[denied] != "deny" {
			t.Fatalf("undeclared tool %s is not denied: %v", denied, raw.Permission[denied])
		}
	}
	if raw.Permission["*"] != "deny" {
		t.Fatal("global wildcard is not denied")
	}
	if err := validateDeclaredPermissionMap(raw.Permission, controlRoot, []string{"read", "edit", "write"}); err != nil {
		t.Fatalf("fresh declared config rejected: %v", err)
	}
	// Mutations must fail the declared read-back validation.
	raw.Permission["grep"] = "allow"
	if err := validateDeclaredPermissionMap(raw.Permission, controlRoot, []string{"read", "edit", "write"}); err == nil {
		t.Fatal("undeclared grep grant accepted by declared validation")
	}
	raw.Permission["grep"] = "deny"
	raw.Permission["lsp"] = "allow"
	if err := validateDeclaredPermissionMap(raw.Permission, controlRoot, []string{"read", "edit", "write"}); err == nil {
		t.Fatal("lsp grant accepted by declared validation")
	}
}

func TestDeclaredBashGrantKeepsDangerousCommandDenies(t *testing.T) {
	controlRoot := t.TempDir()
	config, err := declaredPermissionConfig(controlRoot, []string{"read", "bash"})
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Permission map[string]any `json:"permission"`
	}
	if err := json.Unmarshal([]byte(config), &raw); err != nil {
		t.Fatal(err)
	}
	bash, ok := raw.Permission["bash"].(map[string]any)
	if !ok || bash["*"] != "allow" {
		t.Fatalf("declared bash is not granted: %v", raw.Permission["bash"])
	}
	for _, pattern := range []string{"git push *", "curl *", "sudo *", "sh *"} {
		if bash[pattern] != "deny" {
			t.Fatalf("declared bash lost deny pattern %q: %v", pattern, bash)
		}
	}
	if err := validateDeclaredPermissionMap(raw.Permission, controlRoot, []string{"read", "bash"}); err != nil {
		t.Fatalf("declared bash config rejected: %v", err)
	}
}

func TestDeclaredReadOnlyPermissionConfigConverges(t *testing.T) {
	worktree := t.TempDir()
	controlRoot := t.TempDir()
	scope := readOnlyScope{allowPaths: []string{"reports/**"}, readRoots: nil}
	config, err := declaredReadOnlyPermissionConfig(worktree, controlRoot, scope, []string{"read", "grep"})
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Permission map[string]any `json:"permission"`
	}
	if err := json.Unmarshal([]byte(config), &raw); err != nil {
		t.Fatal(err)
	}
	if raw.Permission["read"] != "allow" || raw.Permission["grep"] != "allow" {
		t.Fatalf("declared read/grep not granted: %v", raw.Permission)
	}
	for _, denied := range []string{"glob", "list", "lsp", "edit", "write"} {
		if raw.Permission[denied] != "deny" {
			t.Fatalf("undeclared %s not denied in declared read-only config: %v", denied, raw.Permission[denied])
		}
	}
	bash := raw.Permission["bash"].(map[string]any)
	if bash["*"] != "deny" || bash["cat *"] != "deny" || bash["grep *"] != "deny" {
		t.Fatalf("read-only bash whitelist must be empty without a bash declaration: %v", bash)
	}
	if err := validateDeclaredReadOnlyPermissionMap(raw.Permission, controlRoot, worktree, scope, []string{"read", "grep"}); err != nil {
		t.Fatalf("fresh declared read-only config rejected: %v", err)
	}
	// Declaring bash restores the read-only whitelist and nothing else.
	bashConfig, err := declaredReadOnlyPermissionConfig(worktree, controlRoot, scope, []string{"read", "bash"})
	if err != nil {
		t.Fatal(err)
	}
	var bashRaw struct {
		Permission map[string]any `json:"permission"`
	}
	if err := json.Unmarshal([]byte(bashConfig), &bashRaw); err != nil {
		t.Fatal(err)
	}
	bashRules := bashRaw.Permission["bash"].(map[string]any)
	if bashRules["cat *"] != "allow" || bashRules["sed -n *"] != "allow" || bashRules["*"] != "deny" {
		t.Fatalf("declared bash must restore the read-only whitelist: %v", bashRules)
	}
	if err := validateDeclaredReadOnlyPermissionMap(bashRaw.Permission, controlRoot, worktree, scope, []string{"read", "bash"}); err != nil {
		t.Fatalf("declared read-only bash config rejected: %v", err)
	}
}

func TestRunDeclaredToolsEnforcedEndToEndAndReconciled(t *testing.T) {
	events := `printf '%s\n'` +
		` '{"type":"tool","sessionID":"session-1","part":{"type":"tool","tool":"read","state":{"status":"completed"}}}'` +
		` '{"type":"tool","sessionID":"session-1","part":{"type":"tool","tool":"edit","state":{"status":"completed"}}}'` +
		` '{"type":"tool","sessionID":"session-1","part":{"type":"tool","tool":"write","state":{"status":"completed"}}}'` +
		` '{"type":"error","sessionID":"session-1","part":{"tool":"read","state":{"status":"error","error":"permission denied","input":{"filePath":"'"$PWD"'/source.go"}}}}'` +
		` '{"type":"text","sessionID":"session-1","part":{"type":"text","text":"done"}}'`
	fixture := newRunFixture(t, supportedBinary, events)
	if err := os.WriteFile(filepath.Join(fixture.worktree, "source.go"), []byte("package x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(fixture.controlRoot, "input", "task-spec.json"), map[string]any{
		"worker": map[string]any{"model": "provider/model", "tools": []string{"read", "edit", "write"}},
	})
	record, err := fixture.adapter.Run(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("declared tools attempt rejected: %v", err)
	}
	if err := fixture.validator.Validate(domain.KindWorkerResult, record.Data); err != nil {
		t.Fatal(err)
	}
	metadata, err := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "opencode-transcript-meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	var meta struct {
		ToolNames []string `json:"toolNames"`
	}
	if err := json.Unmarshal(metadata, &meta); err != nil {
		t.Fatal(err)
	}
	if strings.Join(meta.ToolNames, ",") != "edit,read,write" {
		t.Fatalf("toolNames = %v, want exactly [edit read write]; denied calls must not be collected", meta.ToolNames)
	}
	// The resolved-config read-back loop already ran inside Run through the
	// fake executable echoing $OPENCODE_CONFIG_CONTENT; assert the generated
	// config itself denies every undeclared tool.
	config, err := declaredPermissionConfig(fixture.controlRoot, []string{"read", "edit", "write"})
	if err != nil {
		t.Fatal(err)
	}
	for _, denied := range []string{`"grep":"deny"`, `"glob":"deny"`, `"list":"deny"`, `"lsp":"deny"`, `"bash":"deny"`} {
		if !strings.Contains(config, denied) {
			t.Fatalf("generated declared config misses %s: %s", denied, config)
		}
	}
}

func TestRunReadOnlyDeclaredToolsEndToEnd(t *testing.T) {
	fixture := newRunFixture(t, supportedBinary, `printf '%s\n' '{"type":"step_start","sessionID":"session-1","part":{"type":"step-start"}}' '{"type":"text","sessionID":"session-1","part":{"type":"text","text":"done"}}'`)
	writeJSON(t, filepath.Join(fixture.controlRoot, "input", "task-spec.json"), map[string]any{
		"worker": map[string]any{"model": "provider/model", "readRoots": []string{"docs/**"}, "tools": []string{"read", "grep"}},
		"scope":  map[string]any{"allowPaths": []string{"reports/**"}},
	})
	if _, err := fixture.adapter.Run(context.Background(), fixture.requestWith(map[string]any{"executionProfile": "read-only"})); err != nil {
		t.Fatalf("declared read-only attempt rejected: %v", err)
	}
}

func TestRunRejectsMalformedToolsDeclarationBeforeLaunch(t *testing.T) {
	for _, test := range []struct {
		name  string
		tools any
	}{
		{name: "outside-vocabulary", tools: []string{"read", "shell"}},
		{name: "duplicated", tools: []string{"read", "read"}},
		{name: "wrong-type", tools: "read,edit"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRunFixture(t, supportedBinary, "exit 0")
			writeJSON(t, filepath.Join(fixture.controlRoot, "input", "task-spec.json"), map[string]any{"worker": map[string]any{"model": "provider/model", "tools": test.tools}})
			if _, err := fixture.adapter.Run(context.Background(), fixture.request); err == nil || !strings.Contains(err.Error(), "worker tools") {
				t.Fatalf("err = %v, want fail-closed worker tools rejection", err)
			}
		})
	}
}

func TestRunReadOnlyProfileAppliesScopedConfigAndRejectsUnsafeScope(t *testing.T) {
	t.Run("happy", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, `printf '%s\n' '{"type":"step_start","sessionID":"session-1","part":{"type":"step-start"}}' '{"type":"text","sessionID":"session-1","part":{"type":"text","text":"done"}}'`)
		writeJSON(t, filepath.Join(fixture.controlRoot, "input", "task-spec.json"), map[string]any{
			"worker": map[string]any{"model": "provider/model", "readRoots": []string{"docs/**"}},
			"scope":  map[string]any{"allowPaths": []string{"reports/**"}},
		})
		if _, err := fixture.adapter.Run(context.Background(), fixture.requestWith(map[string]any{"executionProfile": "read-only"})); err != nil {
			t.Fatalf("read-only attempt rejected: %v", err)
		}
	})
	t.Run("missing-allowPaths", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, "exit 0")
		writeJSON(t, filepath.Join(fixture.controlRoot, "input", "task-spec.json"), map[string]any{
			"worker": map[string]any{"model": "provider/model"},
			"scope":  map[string]any{"allowPaths": []string{}},
		})
		if _, err := fixture.adapter.Run(context.Background(), fixture.requestWith(map[string]any{"executionProfile": "read-only"})); err == nil || !strings.Contains(err.Error(), "allowPaths") {
			t.Fatalf("err = %v, want missing allowPaths", err)
		}
	})
	t.Run("unsafe-pattern", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, "exit 0")
		writeJSON(t, filepath.Join(fixture.controlRoot, "input", "task-spec.json"), map[string]any{
			"worker": map[string]any{"model": "provider/model", "readRoots": []string{"../outside"}},
			"scope":  map[string]any{"allowPaths": []string{"reports/**"}},
		})
		if _, err := fixture.adapter.Run(context.Background(), fixture.requestWith(map[string]any{"executionProfile": "read-only"})); err == nil || !strings.Contains(err.Error(), "unsafe path pattern") {
			t.Fatalf("err = %v, want unsafe path pattern", err)
		}
	})
	t.Run("hardened-profile-rejected", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, "exit 0")
		if _, err := fixture.adapter.Run(context.Background(), fixture.requestWith(map[string]any{"executionProfile": "hardened"})); err == nil || !strings.Contains(err.Error(), "does not match") {
			t.Fatalf("err = %v, want profile mismatch", err)
		}
	})
}

func TestBuildArgsNeverUsesShellAndBindsSessionModelPrompt(t *testing.T) {
	args := buildArgs("resume", "session-1", "provider/model", "完成任务")
	want := []string{"run", "--pure", "--format", "json", "--title", "Marshal Worker", "--session", "session-1", "--model", "provider/model", "完成任务"}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args = %#v", args)
	}
}

func TestRunNormalizesResultAndPersistsBoundedTranscript(t *testing.T) {
	fixture := newRunFixture(t, supportedBinary, `printf '%s\n' '{"type":"step_start","sessionID":"session-1","part":{"type":"step-start"}}' '{"type":"text","sessionID":"session-1","part":{"type":"text","text":"done"}}'`)
	record, err := fixture.adapter.Run(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.validator.Validate(domain.KindWorkerResult, record.Data); err != nil {
		t.Fatal(err)
	}
	var result declaredResult
	if err := json.Unmarshal(record.Data, &result); err != nil {
		t.Fatal(err)
	}
	if result.TaskID != "TASK-1" || result.Adapter.ID != adapterID || result.Adapter.Version != supportedBinary || result.Adapter.Executable != fixture.executable || result.Session == nil || result.Session.ID != "session-1" || result.Adapter.Model != "provider/model" {
		t.Fatalf("normalized result = %+v", result)
	}
	if !result.StartedAt.Before(result.CompletedAt) && !result.StartedAt.Equal(result.CompletedAt) {
		t.Fatalf("invalid times: %s %s", result.StartedAt, result.CompletedAt)
	}
	transcript, err := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "opencode-transcript.jsonl"))
	if err != nil || !strings.Contains(string(transcript), `"sessionID":"session-1"`) {
		t.Fatalf("transcript = %s err=%v", transcript, err)
	}
	metadata, err := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "opencode-transcript-meta.json"))
	if err != nil || !strings.Contains(string(metadata), `"eventCount": 2`) {
		t.Fatalf("metadata = %s err=%v", metadata, err)
	}
}

func TestRunRejectsUnsupportedVersionBeforeWorkerLaunch(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "launched")
	fixture := newRunFixture(t, "1.19.0", "touch "+shellQuote(marker))
	if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("error = %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("unsupported worker process was launched")
	}
}

func TestRunRejectsMalformedJSONLAndIdentityMismatch(t *testing.T) {
	t.Run("malformed", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, `printf '%s\n' 'not-json'`)
		if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrProtocol) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("identity", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, `printf '%s\n' '{"type":"step_start","sessionID":"session-1","part":{}}'`)
		data := validDeclaredResult(fixture.executable)
		data["taskId"] = "OTHER"
		writeJSON(t, filepath.Join(fixture.controlRoot, "output", "worker-result.json"), data)
		if _, err := fixture.adapter.Run(context.Background(), fixture.request); err == nil || !strings.Contains(err.Error(), "identity") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestRunEnforcesOutputCapPermissionAndCancellation(t *testing.T) {
	t.Run("output-cap", func(t *testing.T) {
		large := strings.Repeat("x", 1800)
		fixture := newRunFixture(t, supportedBinary, `printf '%s\n' '{"type":"text","sessionID":"session-1","part":{"type":"text","text":"`+large+`"}}'`)
		if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrOutputLimit) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("unterminated-output-cap", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, `yes x | tr -d '\n'`)
		started := time.Now()
		if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrOutputLimit) {
			t.Fatalf("error = %v", err)
		}
		if time.Since(started) > 3*time.Second {
			t.Fatal("unterminated line was not cancelled at the byte limit")
		}
	})
	t.Run("permission", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, `printf '%s\n' '{"type":"error","sessionID":"session-1","part":{"state":{"status":"error","error":"permission denied"}}}'`)
		if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrPermissionDenied) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("permission-words-in-text", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, `printf '%s\n' '{"type":"text","sessionID":"session-1","part":{"type":"text","text":"permission denied is documentation text"}}'`)
		if _, err := fixture.adapter.Run(context.Background(), fixture.request); err != nil {
			t.Fatalf("text caused false permission denial: %v", err)
		}
	})
	t.Run("cancel-process-group", func(t *testing.T) {
		handshake := t.TempDir()
		pidFile := filepath.Join(handshake, "child.pid")
		readyFile := filepath.Join(handshake, "ready")
		body := "sleep 60 &\nchild=$!\nprintf '%s' \"$child\" > " + shellQuote(pidFile+".tmp") + " && mv " + shellQuote(pidFile+".tmp") + " " + shellQuote(pidFile) + "\n: > " + shellQuote(readyFile+".tmp") + " && mv " + shellQuote(readyFile+".tmp") + " " + shellQuote(readyFile) + "\nwait"
		fixture := newRunFixture(t, supportedBinary, body)
		var raw map[string]any
		if err := json.Unmarshal(fixture.request.Data, &raw); err != nil {
			t.Fatal(err)
		}
		raw["attemptTimeoutSeconds"] = 15
		requestData, err := json.Marshal(raw)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		errCh := make(chan error, 1)
		go func() {
			_, runErr := fixture.adapter.Run(ctx, domain.Record{Kind: domain.KindWorkerRequest, Data: requestData})
			errCh <- runErr
		}()
		waitForFile(t, readyFile, 5*time.Second)
		pidData, err := os.ReadFile(pidFile)
		if err != nil {
			t.Fatal(err)
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(pidData)))
		if err != nil {
			t.Fatalf("pid file = %q: %v", pidData, err)
		}
		cancel()
		select {
		case runErr := <-errCh:
			if !errors.Is(runErr, context.Canceled) {
				t.Fatalf("error = %v", runErr)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Run did not return promptly after cancellation")
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("background child %d survived process-group cancellation", pid)
			}
			time.Sleep(5 * time.Millisecond)
		}
	})
}

func TestRunGradesBenignDenialRecordsEvidenceAndContinues(t *testing.T) {
	events := `printf '%s\n'` +
		` '{"type":"error","sessionID":"session-1","part":{"tool":"read","state":{"status":"error","error":"permission denied","input":{"filePath":"'"$PWD"'/source.go"}}}}'` +
		` '{"type":"error","sessionID":"session-1","part":{"tool":"read","state":{"status":"error","error":"permission prevents reading bootstrap","input":{"filePath":"` + os.TempDir() + `/opencode/work-context.txt"}}}}'` +
		` '{"type":"text","sessionID":"session-1","part":{"type":"text","text":"done"}}'`
	fixture := newRunFixture(t, supportedBinary, events)
	if err := os.WriteFile(filepath.Join(fixture.worktree, "source.go"), []byte("package x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	record, err := fixture.adapter.Run(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("benign denials must not terminate the attempt: %v", err)
	}
	if err := fixture.validator.Validate(domain.KindWorkerResult, record.Data); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(fixture.controlRoot, "output", "denials.jsonl")
	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("denial log missing: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("denial log permissions = %v, want 0600", info.Mode().Perm())
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("denial log line invalid: %v: %s", err, line)
		}
		records = append(records, record)
	}
	if len(records) != 2 {
		t.Fatalf("denial records = %+v", records)
	}
	for _, key := range []string{"seq", "tool", "kind", "path-or-cmd", "grade", "reason", "at"} {
		if _, present := records[0][key]; !present {
			t.Fatalf("denial record misses key %q: %+v", key, records[0])
		}
	}
	worktreeReal, err := filepath.EvalSymlinks(fixture.worktree)
	if err != nil {
		t.Fatal(err)
	}
	if records[0]["tool"] != "read" || records[0]["kind"] != "read" || records[0]["grade"] != "BENIGN" || records[0]["path-or-cmd"] != filepath.Join(worktreeReal, "source.go") {
		t.Fatalf("worktree benign record = %+v", records[0])
	}
	if records[1]["grade"] != "BENIGN" || !strings.HasSuffix(records[1]["path-or-cmd"].(string), filepath.Join("opencode", "work-context.txt")) {
		t.Fatalf("bootstrap benign record = %+v", records[1])
	}
	metadata, err := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "opencode-transcript-meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(metadata), `"permissionDenied": false`) || !strings.Contains(string(metadata), `"denialsBenign": 2`) || !strings.Contains(string(metadata), `"denialsFatal": 0`) {
		t.Fatalf("metadata lost denial grading: %s", metadata)
	}
}

func TestRunGradesFatalDenialsFailClosedAndPersistEvidence(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Run("execute-denial", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, `printf '%s\n' '{"type":"error","sessionID":"session-1","part":{"tool":"bash","state":{"status":"error","error":"permission denied by rule","input":{"command":"curl http://evil.example"}}}}'`)
		if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrPermissionDenied) {
			t.Fatalf("error = %v", err)
		}
		assertFatalDenialLog(t, fixture.controlRoot, "bash", "execute", "curl http://evil.example")
		metadata, err := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "opencode-transcript-meta.json"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(metadata), `"permissionDenied": true`) || !strings.Contains(string(metadata), `"denialsFatal": 1`) {
			t.Fatalf("metadata lost fatal denial state: %s", metadata)
		}
	})
	t.Run("write-denial", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, `printf '%s\n' '{"type":"error","sessionID":"session-1","part":{"tool":"edit","state":{"status":"error","error":"permission denied","input":{"filePath":"'"$PWD"'/source.go"}}}}'`)
		worktreeReal, err := filepath.EvalSymlinks(fixture.worktree)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrPermissionDenied) {
			t.Fatalf("error = %v", err)
		}
		assertFatalDenialLog(t, fixture.controlRoot, "edit", "write", filepath.Join(worktreeReal, "source.go"))
	})
	t.Run("symlink-escape-read", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, `printf '%s\n' '{"type":"error","sessionID":"session-1","part":{"tool":"read","state":{"status":"error","error":"permission denied","input":{"filePath":"'"$PWD"'/escape.txt"}}}}'`)
		if err := os.Symlink(outside, filepath.Join(fixture.worktree, "escape.txt")); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrPermissionDenied) {
			t.Fatalf("symlink escape read must grade FATAL: %v", err)
		}
	})
	t.Run("outside-read", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, `printf '%s\n' '{"type":"error","sessionID":"session-1","part":{"tool":"read","state":{"status":"error","error":"permission denied","input":{"filePath":"/etc/hosts"}}}}'`)
		if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrPermissionDenied) {
			t.Fatalf("outside read must grade FATAL: %v", err)
		}
	})
	t.Run("input-read-denial", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, "exit 0")
		body := `printf '%s\n' '{"type":"error","sessionID":"session-1","part":{"tool":"read","state":{"status":"error","error":"permission denied","input":{"filePath":"` + filepath.Join(fixture.controlRoot, "input", "task-spec.json") + `"}}}}'`
		if err := os.WriteFile(fixture.executable, []byte(fakeScript(supportedBinary, body)), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrPermissionDenied) {
			t.Fatalf("control/input read denial must grade FATAL: %v", err)
		}
	})
}

func assertFatalDenialLog(t *testing.T, controlRoot, tool, kind, target string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(controlRoot, "output", "denials.jsonl"))
	if err != nil {
		t.Fatalf("denial log missing: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("denial log = %s", data)
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &record); err != nil {
		t.Fatal(err)
	}
	if record["tool"] != tool || record["kind"] != kind || record["grade"] != "FATAL" || record["path-or-cmd"] != target || record["seq"] != float64(1) {
		t.Fatalf("fatal denial record = %+v", record)
	}
}

func TestRunProcessFailureNeverLeaksStderrIntoError(t *testing.T) {
	secrets := []string{"sk-ant-api03-super-secret-token", "Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.secret-payload", "user private content: password=hunter2"}
	body := `printf '%s\n' '{"type":"step_start","sessionID":"session-1","part":{"type":"step-start"}}'`
	for _, secret := range secrets {
		body += "\nprintf '%s\\n' " + shellQuote(secret) + " >&2"
	}
	body += "\nexit 7"
	fixture := newRunFixture(t, supportedBinary, body)
	_, err := fixture.adapter.Run(context.Background(), fixture.request)
	if !errors.Is(err, ErrProcessFailed) {
		t.Fatalf("error = %v", err)
	}
	if !strings.Contains(err.Error(), "exit=7") {
		t.Fatalf("error must carry the exit code: %v", err)
	}
	if strings.Contains(err.Error(), "stderr") {
		t.Fatalf("error references stderr contents: %v", err)
	}
	for _, secret := range secrets {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("provider stderr leaked into error: %v", err)
		}
	}
	evidence, readErr := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "opencode-stderr.log"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	metadata, readErr := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "opencode-transcript-meta.json"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, secret := range secrets {
		if !strings.Contains(string(evidence), secret) {
			t.Fatalf("bounded stderr evidence file lost %q", secret)
		}
		if strings.Contains(string(metadata), secret) {
			t.Fatalf("metadata leaked stderr content %q", secret)
		}
	}
	if !strings.Contains(string(metadata), `"stderrBytes"`) || !strings.Contains(string(metadata), `"exitCode": 7`) {
		t.Fatalf("metadata lost bounded stderr/process accounting: %s", metadata)
	}
}

func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("file %s was not produced within %s", path, timeout)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type runFixture struct {
	adapter                           *Adapter
	validator                         *contract.Validator
	executable, worktree, controlRoot string
	request                           domain.Record
}

func newRunFixture(t *testing.T, version, body string) runFixture {
	t.Helper()
	executable := fakeExecutable(t, version, body)
	validator := newValidator(t)
	adapter, err := New(executable, validator)
	if err != nil {
		t.Fatal(err)
	}
	worktree := t.TempDir()
	controlRoot := t.TempDir()
	writeJSON(t, filepath.Join(controlRoot, "input", "task-spec.json"), map[string]any{"worker": map[string]any{"model": "provider/model"}})
	promptPath := filepath.Join(controlRoot, "input", "prompt.md")
	if err := os.MkdirAll(filepath.Dir(promptPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(promptPath, []byte("完成 fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(controlRoot, "output", "worker-result.json"), validDeclaredResult(executable))
	requestData := map[string]any{
		"apiVersion": "marshal.dev/v1alpha1", "kind": "WorkerRequest", "taskId": "TASK-1", "runId": "run-1", "attemptId": "attempt-1", "attemptNumber": 1,
		"specDigest": digest("a"), "policyDigest": digest("b"), "capabilityDigest": digest("c"), "baseSha": strings.Repeat("1", 40),
		"worktreePath": worktree, "controlRoot": controlRoot, "taskSpecPath": "input/task-spec.json", "promptPath": "input/prompt.md", "resultPath": "output/worker-result.json",
		"adapterId": "opencode", "executionProfile": "workspace-write", "sessionPolicy": "ephemeral", "attemptTimeoutSeconds": 5, "maxOutputBytes": 1024, "reviewFindings": []any{},
	}
	requestBytes, _ := json.Marshal(requestData)
	return runFixture{adapter, validator, adapter.executable, worktree, controlRoot, domain.Record{Kind: domain.KindWorkerRequest, Data: requestBytes}}
}

func (f runFixture) requestWith(overrides map[string]any) domain.Record {
	data := map[string]any{}
	var source map[string]any
	if err := json.Unmarshal(f.request.Data, &source); err != nil {
		panic(err)
	}
	for key, value := range source {
		data[key] = value
	}
	for key, value := range overrides {
		data[key] = value
	}
	requestBytes, err := json.Marshal(data)
	if err != nil {
		panic(err)
	}
	return domain.Record{Kind: domain.KindWorkerRequest, Data: requestBytes}
}

func validDeclaredResult(executable string) map[string]any {
	return map[string]any{
		"apiVersion": "marshal.dev/v1alpha1", "kind": "WorkerResult", "taskId": "TASK-1", "runId": "run-1", "attemptId": "attempt-1",
		"adapter": map[string]any{"id": "opencode", "executable": executable, "version": "worker-claim"},
		"session": map[string]any{"id": "session-1", "resumable": false}, "status": "completed", "summary": "fixture completed",
		"declaredChangedFiles": []string{"file.txt"}, "declaredArtifacts": []any{}, "declaredCommands": []any{}, "declaredRisks": []string{}, "outputTruncated": false,
		"startedAt": "2026-08-04T00:00:00Z", "completedAt": "2026-08-04T00:00:01Z",
	}
}

func fakeExecutable(t *testing.T, version, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "opencode")
	if err := os.WriteFile(path, []byte(fakeScript(version, body)), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func fakeScript(version, body string) string {
	return "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then printf '%s\\n' '" + version + "'; exit 0; fi\nif [ \"$1\" = \"debug\" ] && [ \"$2\" = \"config\" ]; then printf '%s\\n' \"$OPENCODE_CONFIG_CONTENT\"; exit 0; fi\n" + body + "\n"
}

func newValidator(t *testing.T) *contract.Validator {
	t.Helper()
	validator, err := contract.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	return validator
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func digest(character string) string { return "sha256:" + strings.Repeat(character, 64) }

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
