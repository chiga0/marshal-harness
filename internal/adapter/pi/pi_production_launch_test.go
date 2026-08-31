package pi

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
)

// validProductionInput is the canonical valid input for the Pi 0.84.4
// production launch builder. Every field is path-free caller authority.
func validProductionInput() ProductionLaunchInput {
	return ProductionLaunchInput{
		NodeRuntime: "/opt/pi/bin/node",
		Entrypoint:  "/opt/pi/bundle/cli.js",
		Profile:     "workspace-write",
		TaskID:      "TASK-1",
		RunID:       "run-1",
		AttemptID:   "attempt-1",
		Objective:   "Fix the concurrency bug",
		Constraints: []string{"Do not add dependencies", "Keep tests green"},
	}
}

// expectedProductionPrompt mirrors the deterministic prompt format locked by
// buildProductionPrompt. Hardcoding it here makes the exact-argv table a true
// byte-for-byte lock, independent of the builder's own assembly.
func expectedProductionPrompt(taskID, runID, attemptID, objective string, constraints []string) string {
	var b strings.Builder
	b.WriteString("The final assistant output must be exactly one WorkerResult JSON object.\n\n")
	b.WriteString("taskId: ")
	b.WriteString(taskID)
	b.WriteByte('\n')
	b.WriteString("runId: ")
	b.WriteString(runID)
	b.WriteByte('\n')
	b.WriteString("attemptId: ")
	b.WriteString(attemptID)
	b.WriteByte('\n')
	b.WriteString("\nObjective:\n")
	b.WriteString(objective)
	b.WriteByte('\n')
	if len(constraints) > 0 {
		b.WriteString("\nConstraints:\n")
		for _, c := range constraints {
			b.WriteString("- ")
			b.WriteString(c)
			b.WriteByte('\n')
		}
	}
	b.WriteString("\nWorkerResult contract:\n")
	b.WriteString("- Keep apiVersion, kind, taskId, runId, attemptId, and adapter.id exactly as shown.\n")
	b.WriteString("- Do not add a result wrapper or any key not shown in the object, except blocker as described below.\n")
	b.WriteString("- Set status truthfully to completed, blocked, failed, or cancelled. Use completed only when the objective and every constraint are fully satisfied.\n")
	b.WriteString("- For any non-completed status, add a top-level blocker string explaining why.\n")
	b.WriteString("- Replace summary and the declared arrays with truthful values; paths must be relative. Use [] when an array is empty, and set outputTruncated truthfully.\n")
	b.WriteString("- Keep the placeholder adapter executable/version and timestamps; Marshal replaces them with observed authority.\n")
	b.WriteString(`{"apiVersion":"marshal.dev/v1alpha1","kind":"WorkerResult","taskId":"`)
	b.WriteString(taskID)
	b.WriteString(`","runId":"`)
	b.WriteString(runID)
	b.WriteString(`","attemptId":"`)
	b.WriteString(attemptID)
	b.WriteString(`","adapter":{"id":"pi","executable":"marshal-observed","version":"marshal-observed"},"status":"completed","summary":"Describe the outcome","declaredChangedFiles":[],"declaredArtifacts":[],"declaredCommands":[],"declaredRisks":[],"outputTruncated":false,"startedAt":"1970-01-01T00:00:00Z","completedAt":"1970-01-01T00:00:01Z"}`)
	b.WriteByte('\n')
	return b.String()
}

func TestBuildProductionLaunchExactArgv(t *testing.T) {
	const node = "/opt/pi/bin/node"
	const entry = "/opt/pi/bundle/cli.js"
	// The deterministic flag prefix shared by every production launch argv,
	// in the exact frozen order, including --no-approve.
	wantFlags := []string{
		"--mode", "json", "--print", "--no-approve",
		"--no-extensions", "--no-skills", "--no-prompt-templates", "--no-themes", "--no-context-files",
		"--tools", workerTools, "--no-session",
	}
	tests := []struct {
		name        string
		profile     string
		model       string
		taskID      string
		objective   string
		constraints []string
	}{
		{name: "workspace-write-no-model", profile: "workspace-write", taskID: "TASK-1", objective: "Fix the concurrency bug", constraints: []string{"Do not add dependencies", "Keep tests green"}},
		{name: "read-only-no-model", profile: "read-only", taskID: "TASK-2", objective: "Review the read-only diff", constraints: nil},
		{name: "workspace-write-with-model", profile: "workspace-write", model: "anthropic/claude-sonnet-4", taskID: "TASK-3", objective: "Add the launch builder", constraints: []string{"No new ADR"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			in := ProductionLaunchInput{
				NodeRuntime: node, Entrypoint: entry, Profile: test.profile, Model: test.model,
				TaskID: test.taskID, RunID: "run-1", AttemptID: "attempt-1",
				Objective: test.objective, Constraints: test.constraints,
			}
			out, err := BuildProductionLaunch(in)
			if err != nil {
				t.Fatalf("BuildProductionLaunch: %v", err)
			}
			wantTools := toolsForProfile(test.profile)
			wantPrompt := expectedProductionPrompt(test.taskID, "run-1", "attempt-1", test.objective, test.constraints)
			wantArgv := []string{node, entry}
			wantArgv = append(wantArgv, wantFlags...)
			wantArgv[slices.Index(wantArgv, "--tools")+1] = wantTools
			if test.model != "" {
				wantArgv = append(wantArgv, "--model", test.model)
			}
			wantArgv = append(wantArgv, wantPrompt)
			if !slices.Equal(out.Argv, wantArgv) {
				t.Fatalf("argv =\n%q\nwant\n%q", out.Argv, wantArgv)
			}
			if out.Prompt != wantPrompt {
				t.Fatalf("prompt = %q, want %q", out.Prompt, wantPrompt)
			}
			// The prompt is always exactly one trailing positional argument.
			if out.Argv[len(out.Argv)-1] != out.Prompt {
				t.Fatalf("last argv element is not the prompt: %q != %q", out.Argv[len(out.Argv)-1], out.Prompt)
			}
			if !slices.Contains(out.Argv, "--no-approve") {
				t.Fatalf("argv must contain --no-approve: %q", out.Argv)
			}
			// bash is never granted by either profile's closed allowlist.
			if slices.Contains(strings.Split(out.Argv[flagsToolsIndex(t, out.Argv)], ","), "bash") {
				t.Fatalf("tools allowlist grants bash: %q", out.Argv)
			}
		})
	}
}

func TestProductionPromptEmbedsValidWorkerResultContract(t *testing.T) {
	prompt := buildProductionPrompt(validProductionInput())
	for _, required := range []string{"Use completed only when the objective and every constraint are fully satisfied", "For any non-completed status, add a top-level blocker", "set outputTruncated truthfully", "Do not add a result wrapper"} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("production prompt lacks terminal-result rule %q", required)
		}
	}
	lines := strings.Split(strings.TrimSpace(prompt), "\n")
	template := []byte(lines[len(lines)-1])
	validator, err := contract.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	if err := validator.Validate(domain.KindWorkerResult, template); err != nil {
		t.Fatalf("embedded WorkerResult contract is invalid: %v\n%s", err, template)
	}
}

func TestBuildProductionLaunchRejectsWorkerResultInvalidIDs(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ProductionLaunchInput)
	}{
		{name: "quote", mutate: func(in *ProductionLaunchInput) { in.TaskID = `TASK"1` }},
		{name: "slash", mutate: func(in *ProductionLaunchInput) { in.RunID = "run/1" }},
		{name: "backslash", mutate: func(in *ProductionLaunchInput) { in.AttemptID = `attempt\1` }},
		{name: "unicode", mutate: func(in *ProductionLaunchInput) { in.TaskID = "任务-1" }},
		{name: "leading-punctuation", mutate: func(in *ProductionLaunchInput) { in.RunID = "-run-1" }},
		{name: "too-long", mutate: func(in *ProductionLaunchInput) { in.AttemptID = strings.Repeat("a", 129) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := validProductionInput()
			test.mutate(&input)
			if _, err := BuildProductionLaunch(input); err == nil || !strings.Contains(err.Error(), "WorkerResult ID schema") {
				t.Fatalf("error = %v, want WorkerResult ID schema rejection", err)
			}
		})
	}
}

// flagsToolsIndex returns the index of the --tools value in argv for the
// bash-presence assertion. It fails the test if the flag is not where the
// frozen order places it.
func flagsToolsIndex(t *testing.T, argv []string) int {
	t.Helper()
	for i, element := range argv {
		if element == "--tools" {
			if i+1 >= len(argv) {
				t.Fatalf("argv has --tools without a value: %q", argv)
			}
			return i + 1
		}
	}
	t.Fatalf("argv lacks --tools: %q", argv)
	return -1
}

func TestBuildProductionLaunchToolAllowlistFollowsProfile(t *testing.T) {
	tests := []struct {
		profile string
		want    string
	}{
		{profile: "workspace-write", want: workerTools},
		{profile: "read-only", want: readOnlyTools},
	}
	for _, test := range tests {
		t.Run(test.profile, func(t *testing.T) {
			in := validProductionInput()
			in.Profile = test.profile
			out, err := BuildProductionLaunch(in)
			if err != nil {
				t.Fatalf("BuildProductionLaunch: %v", err)
			}
			idx := flagsToolsIndex(t, out.Argv)
			if out.Argv[idx] != test.want {
				t.Fatalf("tools = %q, want %q", out.Argv[idx], test.want)
			}
			if strings.Contains(out.Argv[idx], "bash") {
				t.Fatalf("tools allowlist grants bash: %q", out.Argv[idx])
			}
		})
	}
}

func TestBuildProductionLaunchOptionalModel(t *testing.T) {
	in := validProductionInput()
	in.Model = "anthropic/claude-sonnet-4"
	out, err := BuildProductionLaunch(in)
	if err != nil {
		t.Fatalf("BuildProductionLaunch: %v", err)
	}
	modelIdx := slices.Index(out.Argv, "--model")
	if modelIdx < 0 {
		t.Fatalf("argv lacks --model: %q", out.Argv)
	}
	if modelIdx+1 >= len(out.Argv) || out.Argv[modelIdx+1] != in.Model {
		t.Fatalf("model value = %q, want %q", out.Argv[modelIdx+1:], in.Model)
	}
	// --model sits after the tools allowlist and before the single prompt arg.
	toolsIdx := slices.Index(out.Argv, "--tools")
	if !(modelIdx > toolsIdx && modelIdx < len(out.Argv)-1) {
		t.Fatalf("--model position %d is not between --tools %d and the prompt: %q", modelIdx, toolsIdx, out.Argv)
	}
	// An empty model is unset: no --model flag is emitted.
	in.Model = ""
	out, err = BuildProductionLaunch(in)
	if err != nil {
		t.Fatalf("BuildProductionLaunch: %v", err)
	}
	if slices.Contains(out.Argv, "--model") {
		t.Fatalf("empty model emitted --model: %q", out.Argv)
	}
}

func TestBuildProductionLaunchAbsolutePOSIXPathRejection(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ProductionLaunchInput)
	}{
		{name: "objective-absolute-path", mutate: func(in *ProductionLaunchInput) { in.Objective = "read /etc/passwd and report" }},
		{name: "objective-root-token", mutate: func(in *ProductionLaunchInput) { in.Objective = "inspect the root directory /" }},
		{name: "constraint-absolute-path", mutate: func(in *ProductionLaunchInput) { in.Constraints = []string{"do not touch /private/tmp"} }},
		{name: "constraint-private-path", mutate: func(in *ProductionLaunchInput) { in.Constraints = []string{"ignore /.marshal/state"} }},
		{name: "constraint-tmp-path", mutate: func(in *ProductionLaunchInput) { in.Constraints = []string{"never write /tmp/worker-result.json"} }},
		{name: "quoted-absolute-path", mutate: func(in *ProductionLaunchInput) { in.Objective = `inspect "/private/tmp/work"` }},
		{name: "parenthesized-absolute-path", mutate: func(in *ProductionLaunchInput) { in.Objective = "inspect (/tmp/work)" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			in := validProductionInput()
			test.mutate(&in)
			out, err := BuildProductionLaunch(in)
			if err == nil {
				t.Fatalf("BuildProductionLaunch accepted an absolute POSIX path token: argv=%q prompt=%q", out.Argv, out.Prompt)
			}
			if !strings.Contains(err.Error(), "absolute POSIX path") {
				t.Fatalf("error = %v, want an absolute POSIX path rejection", err)
			}
		})
	}
	// A relative path token (no leading '/') is not an absolute POSIX path
	// and must not be rejected by the path-token check.
	in := validProductionInput()
	in.Objective = "edit the relative/path/file module"
	in.Constraints = []string{"keep docs/ignore intact"}
	if out, err := BuildProductionLaunch(in); err != nil {
		t.Fatalf("BuildProductionLaunch rejected a relative path token: %v (prompt=%q)", err, out.Prompt)
	}
}

func TestBuildProductionLaunchReservedControlPathAndModelRejection(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ProductionLaunchInput)
	}{
		{name: "relative-marshal-control", mutate: func(in *ProductionLaunchInput) { in.Objective = "inspect .marshal/runtime-v1" }},
		{name: "result-filename", mutate: func(in *ProductionLaunchInput) { in.Constraints = []string{"emit worker-result.json"} }},
		{name: "model-control", mutate: func(in *ProductionLaunchInput) { in.Model = "provider/model\n--tools bash" }},
		{name: "objective-nul", mutate: func(in *ProductionLaunchInput) { in.Objective = "fix\x00thing" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			in := validProductionInput()
			test.mutate(&in)
			if _, err := BuildProductionLaunch(in); err == nil {
				t.Fatal("BuildProductionLaunch accepted reserved/control input")
			}
		})
	}
}

func TestBuildProductionLaunchControlCharIDRejection(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ProductionLaunchInput)
	}{
		{name: "taskid-newline", mutate: func(in *ProductionLaunchInput) { in.TaskID = "TASK\n1" }},
		{name: "runid-carriage-return", mutate: func(in *ProductionLaunchInput) { in.RunID = "run\r1" }},
		{name: "attemptid-tab", mutate: func(in *ProductionLaunchInput) { in.AttemptID = "attempt\t1" }},
		{name: "taskid-del", mutate: func(in *ProductionLaunchInput) { in.TaskID = "TASK\x7f" }},
		{name: "runid-null", mutate: func(in *ProductionLaunchInput) { in.RunID = "run\x00" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			in := validProductionInput()
			test.mutate(&in)
			out, err := BuildProductionLaunch(in)
			if err == nil {
				t.Fatalf("BuildProductionLaunch accepted a control-character ID: argv=%q prompt=%q", out.Argv, out.Prompt)
			}
			if !strings.Contains(err.Error(), "control characters") {
				t.Fatalf("error = %v, want a control-character ID rejection", err)
			}
		})
	}
}

func TestBuildProductionLaunchInvalidFieldsFailClosed(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ProductionLaunchInput)
	}{
		{name: "empty-node-runtime", mutate: func(in *ProductionLaunchInput) { in.NodeRuntime = "" }},
		{name: "relative-node-runtime", mutate: func(in *ProductionLaunchInput) { in.NodeRuntime = "node" }},
		{name: "unclean-node-runtime", mutate: func(in *ProductionLaunchInput) { in.NodeRuntime = "/opt/pi/./bin/node" }},
		{name: "node-runtime-trailing-slash", mutate: func(in *ProductionLaunchInput) { in.NodeRuntime = "/opt/pi/bin/" }},
		{name: "empty-entrypoint", mutate: func(in *ProductionLaunchInput) { in.Entrypoint = "" }},
		{name: "relative-entrypoint", mutate: func(in *ProductionLaunchInput) { in.Entrypoint = "cli.js" }},
		{name: "unclean-entrypoint", mutate: func(in *ProductionLaunchInput) { in.Entrypoint = "/opt/pi/bundle//cli.js" }},
		{name: "unknown-profile", mutate: func(in *ProductionLaunchInput) { in.Profile = "admin" }},
		{name: "empty-profile", mutate: func(in *ProductionLaunchInput) { in.Profile = "" }},
		{name: "empty-task-id", mutate: func(in *ProductionLaunchInput) { in.TaskID = "" }},
		{name: "empty-run-id", mutate: func(in *ProductionLaunchInput) { in.RunID = "" }},
		{name: "empty-attempt-id", mutate: func(in *ProductionLaunchInput) { in.AttemptID = "" }},
		{name: "empty-objective", mutate: func(in *ProductionLaunchInput) { in.Objective = "" }},
		{name: "empty-constraint", mutate: func(in *ProductionLaunchInput) { in.Constraints = []string{"valid", ""} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			in := validProductionInput()
			test.mutate(&in)
			out, err := BuildProductionLaunch(in)
			if err == nil {
				t.Fatalf("BuildProductionLaunch accepted an invalid field: argv=%q prompt=%q", out.Argv, out.Prompt)
			}
		})
	}
}

func TestBuildProductionLaunchArgvElementLimit(t *testing.T) {
	in := validProductionInput()
	// A 17KiB objective produces a prompt argv element well over the 16KiB
	// per-element ceiling while staying under the 48KiB aggregate ceiling.
	in.Objective = strings.Repeat("o", 17<<10)
	out, err := BuildProductionLaunch(in)
	if !errors.Is(err, ErrProductionArgvElementTooLarge) {
		t.Fatalf("error = %v, want ErrProductionArgvElementTooLarge (out=%+v)", err, out)
	}
	// Exactly at the 16KiB element ceiling is permitted for a clean argv. Derive
	// the fixed prompt overhead so this remains true when the deterministic
	// WorkerResult contract evolves.
	in.Objective = "o"
	fixedPromptBytes := len(buildProductionPrompt(in)) - len(in.Objective)
	in.Objective = strings.Repeat("o", productionArgvElementLimit-fixedPromptBytes)
	atLimit, err := BuildProductionLaunch(in)
	if err != nil {
		t.Fatalf("BuildProductionLaunch rejected an at-limit element: %v", err)
	}
	if len(atLimit.Prompt) != productionArgvElementLimit {
		t.Fatalf("at-limit prompt bytes = %d, want %d", len(atLimit.Prompt), productionArgvElementLimit)
	}
}

func TestBuildProductionLaunchArgvAggregateLimit(t *testing.T) {
	in := validProductionInput()
	// Three 16KiB argv elements (node runtime, entrypoint, model) plus the
	// short flags and prompt push the aggregate over 48KiB while every
	// individual element stays at or under the 16KiB per-element ceiling, so
	// only the aggregate bound fires.
	in.NodeRuntime = "/" + strings.Repeat("n", productionArgvElementLimit-1)
	in.Entrypoint = "/" + strings.Repeat("e", productionArgvElementLimit-1)
	in.Model = strings.Repeat("m", productionArgvElementLimit)
	out, err := BuildProductionLaunch(in)
	if !errors.Is(err, ErrProductionArgvAggregateTooLarge) {
		t.Fatalf("error = %v, want ErrProductionArgvAggregateTooLarge (out=%+v)", err, out)
	}
}

func TestBuildProductionLaunchDeterministicRepeatedOutput(t *testing.T) {
	in := validProductionInput()
	first, err := BuildProductionLaunch(in)
	if err != nil {
		t.Fatalf("first BuildProductionLaunch: %v", err)
	}
	for i := 0; i < 3; i++ {
		next, err := BuildProductionLaunch(in)
		if err != nil {
			t.Fatalf("repeat %d BuildProductionLaunch: %v", i, err)
		}
		if !slices.Equal(next.Argv, first.Argv) {
			t.Fatalf("repeat %d argv = %q, want %q", i, next.Argv, first.Argv)
		}
		if next.Prompt != first.Prompt {
			t.Fatalf("repeat %d prompt = %q, want %q", i, next.Prompt, first.Prompt)
		}
	}
}

func TestBuildProductionLaunchPromptHasNoStateOrResultPaths(t *testing.T) {
	in := validProductionInput()
	out, err := BuildProductionLaunch(in)
	if err != nil {
		t.Fatalf("BuildProductionLaunch: %v", err)
	}
	for _, token := range []string{"/private/", "/tmp/", ".marshal", "worker-result.json"} {
		if strings.Contains(out.Prompt, token) {
			t.Fatalf("prompt leaked %q: %q", token, out.Prompt)
		}
		if token != "worker-result.json" {
			for _, element := range out.Argv {
				if element != out.Prompt && strings.Contains(element, token) {
					t.Fatalf("argv element leaked %q: %q", token, element)
				}
			}
		}
	}
	// The provided IDs/objective/constraints are the only variable content.
	if !strings.Contains(out.Prompt, in.TaskID) || !strings.Contains(out.Prompt, in.RunID) || !strings.Contains(out.Prompt, in.AttemptID) {
		t.Fatalf("prompt dropped an ID: %q", out.Prompt)
	}
	if !strings.Contains(out.Prompt, in.Objective) {
		t.Fatalf("prompt dropped the objective: %q", out.Prompt)
	}
	for _, c := range in.Constraints {
		if !strings.Contains(out.Prompt, c) {
			t.Fatalf("prompt dropped a constraint: %q", out.Prompt)
		}
	}
}
