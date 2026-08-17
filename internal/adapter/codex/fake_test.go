package codex

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// runFakeExitCode 直接执行 fake codex 并返回退出码；parser 契约拒绝必须
// 表现为 exit=2，与 0.145.0 真实 parser 一致。
func runFakeExitCode(t *testing.T, executable string, args ...string) int {
	t.Helper()
	command := exec.Command(executable, args...)
	command.Dir = t.TempDir()
	err := command.Run()
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	t.Fatalf("fake execution failed outside the parser contract: %v", err)
	return -1
}

// frozenAdapterArgv 复算 Adapter 冻结的完整 argv，供 parser 契约双向验证。
func frozenAdapterArgv(schemaPath, resultPath string) []string {
	return buildArgs(schemaPath, resultPath, "")
}

func TestFakeParserAcceptsFrozenAdapterArgv(t *testing.T) {
	executable := fakeExecutable(t, supportedVersionOutput, "exit 0")
	for _, test := range []struct {
		name string
		args []string
	}{
		{
			name: "adapter-argv-without-model",
			args: frozenAdapterArgv("/control/codex-output-schema.json", "/control/output/worker-result.json"),
		},
		{
			name: "adapter-argv-with-model",
			args: append(frozenAdapterArgv("/control/codex-output-schema.json", "/control/output/worker-result.json"), "-m", "provider/model"),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if code := runFakeExitCode(t, executable, test.args...); code != 0 {
				t.Fatalf("frozen adapter argv rejected by the 0.145.0 parser contract: exit=%d argv=%#v", code, test.args)
			}
		})
	}
}

func TestFakeParserRejectsWrongOrderingAndValues(t *testing.T) {
	executable := fakeExecutable(t, supportedVersionOutput, "exit 0")
	for _, test := range []struct {
		name string
		args []string
	}{
		{
			// 真实 0.145.0 只接受顶层 --ask-for-approval；放在 exec 之后
			// 必须以 exit=2 失败。
			name: "approval-after-exec-subcommand",
			args: []string{"exec", "--json", "--sandbox", "workspace-write", "--ask-for-approval", "never"},
		},
		{
			// exec --sandbox 只接受枚举值；JSON 策略对象必须被拒绝。
			name: "sandbox-json-policy-object",
			args: []string{"--ask-for-approval", "never", "exec", "--json", "--sandbox", `{"mode":"workspace-write","networkAccess":false,"writableRoots":["/tmp/worktree"]}`},
		},
		{
			name: "sandbox-invalid-enum",
			args: []string{"--ask-for-approval", "never", "exec", "--json", "--sandbox", "danger-full-access-everywhere"},
		},
		{
			name: "approval-invalid-value",
			args: []string{"--ask-for-approval", "always", "exec", "--json", "--sandbox", "workspace-write"},
		},
		{
			name: "color-invalid-value",
			args: []string{"exec", "--color", "sometimes", "--json", "--sandbox", "workspace-write"},
		},
		{
			name: "color-before-exec-subcommand",
			args: []string{"--color", "never", "exec", "--json", "--sandbox", "workspace-write"},
		},
		{
			name: "unknown-global-flag",
			args: []string{"--weird-global", "exec", "--json", "--sandbox", "workspace-write"},
		},
		{
			name: "unknown-exec-flag",
			args: []string{"--ask-for-approval", "never", "exec", "--json", "--sandbox", "workspace-write", "--weird-exec"},
		},
		{
			name: "missing-sandbox-value",
			args: []string{"--ask-for-approval", "never", "exec", "--json", "--sandbox"},
		},
		{
			name: "missing-approval-value",
			args: []string{"--ask-for-approval", "exec", "--json"},
		},
		{
			name: "missing-exec-subcommand",
			args: []string{"--ask-for-approval", "never", "--json", "--sandbox", "workspace-write"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if code := runFakeExitCode(t, executable, test.args...); code != 2 {
				t.Fatalf("parser contract accepted an invalid argv: exit=%d argv=%#v", code, test.args)
			}
		})
	}
}

func TestFakeParserRejectsSandboxEnumDrift(t *testing.T) {
	executable := fakeExecutable(t, supportedVersionOutput, "exit 0")
	for _, mode := range []string{"read-only", "workspace-write", "danger-full-access"} {
		if code := runFakeExitCode(t, executable, "--ask-for-approval", "never", "exec", "--json", "--sandbox", mode); code != 0 {
			t.Fatalf("sandbox enum %q rejected: exit=%d", mode, code)
		}
	}
	for _, mode := range []string{"workspace_write", "Workspace-Write", "full-access", ""} {
		if code := runFakeExitCode(t, executable, "--ask-for-approval", "never", "exec", "--json", "--sandbox", mode); code != 2 {
			t.Fatalf("sandbox enum %q accepted: exit=%d", mode, code)
		}
	}
}

func TestFakeVersionOutputIsFrozen(t *testing.T) {
	executable := fakeExecutable(t, supportedVersionOutput, "exit 0")
	command := exec.Command(executable, "--version")
	command.Dir = t.TempDir()
	output, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(output)) != "codex-cli 0.145.0" {
		t.Fatalf("--version output = %q, want the frozen codex-cli line", output)
	}
	version, err := parseBinaryVersion(string(output))
	if err != nil || version != "0.145.0" {
		t.Fatalf("parseBinaryVersion = %q/%v, want 0.145.0", version, err)
	}
}
