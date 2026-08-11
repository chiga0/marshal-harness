package planning

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/domain"
)

type preflightCheckCall struct {
	interpreter string
	source      string
}

// preflightFakeChecker is the deterministic dependency-injection seam used by
// the unit tests, so no real Python installation is required.
type preflightFakeChecker struct {
	calls   []preflightCheckCall
	finding *SyntaxFinding
	err     error
}

func (c *preflightFakeChecker) CheckPythonSyntax(_ context.Context, interpreter, source string) (*SyntaxFinding, error) {
	c.calls = append(c.calls, preflightCheckCall{interpreter: interpreter, source: source})
	return c.finding, c.err
}

func TestPreflightAcceptanceCommandsAcceptsValidPython(t *testing.T) {
	checker := &preflightFakeChecker{}
	commands := []domain.TaskCommand{
		{ID: "valid-basic", Argv: []string{"python3", "-c", "print('ok')"}},
		{ID: "valid-bare-python", Argv: []string{"python", "-c", "x = 1"}},
		{ID: "valid-versioned-with-data", Argv: []string{"python3.13", "-c", "x = 1", "data-only"}},
		{ID: "valid-versioned-empty", Argv: []string{"python3.9", "-c", ""}},
	}
	if err := preflightAcceptanceCommands(context.Background(), commands, checker); err != nil {
		t.Fatalf("preflightAcceptanceCommands(): %v", err)
	}
	want := []preflightCheckCall{
		{interpreter: "python3", source: "print('ok')"},
		{interpreter: "python", source: "x = 1"},
		{interpreter: "python3.13", source: "x = 1"},
		{interpreter: "python3.9", source: ""},
	}
	if !reflect.DeepEqual(checker.calls, want) {
		t.Fatalf("checker calls = %#v, want %#v", checker.calls, want)
	}
}

func TestPreflightAcceptanceCommandsRejectsInvalidPythonWithLocation(t *testing.T) {
	const script = "def broken(:"
	checker := &preflightFakeChecker{finding: &SyntaxFinding{Kind: "SyntaxError", Line: 3, Column: 9}}
	commands := []domain.TaskCommand{{ID: "broken-syntax", Argv: []string{"python3", "-c", script}}}
	err := preflightAcceptanceCommands(context.Background(), commands, checker)
	var syntaxErr *PreflightSyntaxError
	if !errors.As(err, &syntaxErr) {
		t.Fatalf("error = %v, want *PreflightSyntaxError", err)
	}
	if syntaxErr.CommandID != "broken-syntax" || syntaxErr.ArgvIndex != 2 {
		t.Fatalf("syntax error = %#v, want command broken-syntax argv[2]", syntaxErr)
	}
	if syntaxErr.Finding.Kind != "SyntaxError" || syntaxErr.Finding.Line != 3 || syntaxErr.Finding.Column != 9 {
		t.Fatalf("finding = %#v, want SyntaxError at line 3, column 9", syntaxErr.Finding)
	}
	message := err.Error()
	for _, token := range []string{`"broken-syntax"`, "argv[2]", "SyntaxError", "line 3", "column 9"} {
		if !strings.Contains(message, token) {
			t.Fatalf("error %q missing %q", message, token)
		}
	}
	if strings.Contains(message, script) {
		t.Fatalf("error echoes the script text: %q", message)
	}
}

func TestPreflightAcceptanceCommandsSkipsUnknownInterpreter(t *testing.T) {
	cases := [][]string{
		{},
		{"python3"},
		{"python3", "-c"},
		{"bash", "-c", "echo run"},
		{"sh", "-c", "echo run"},
		{"node", "-e", "process.exit(0)"},
		{"python3", "script.py"},
		{"python3", "-m", "module"},
		{"python3", "-B", "-c", "x = 1"},
		{"python", "--version"},
		{"python3.13.1", "-c", "x = 1"},
		{"mypython3", "-c", "x = 1"},
		{"Python3", "-c", "x = 1"},
		{"./python3", "-c", "x = 1"},
		{"../python3", "-c", "x = 1"},
		{"/usr/bin/python", "-c", "x = 1"},
		{"/usr/bin/python3", "-c", "x = 1"},
		{"/tmp/python3", "-c", "x = 1"},
		{"bin/python", "-c", "x = 1"},
		{"sub\\python3", "-c", "x = 1"},
		{"python.exe", "-c", "x = 1"},
		{"python3.exe", "-c", "x = 1"},
		{"python3.12.exe", "-c", "x = 1"},
	}
	for i, argv := range cases {
		checker := &preflightFakeChecker{finding: &SyntaxFinding{Kind: "SyntaxError", Line: 1, Column: 1}}
		err := preflightAcceptanceCommands(context.Background(), []domain.TaskCommand{{ID: "skipped", Argv: argv}}, checker)
		if err != nil || len(checker.calls) != 0 {
			t.Fatalf("case %d argv=%q: err=%v calls=%d, want skip without any check", i, argv, err, len(checker.calls))
		}
	}
}

func TestPreflightAcceptanceCommandsNeverExecutesTaskScript(t *testing.T) {
	const script = "import os; os.remove('/fixture/sentinel')"
	checker := &preflightFakeChecker{}
	commands := []domain.TaskCommand{{ID: "inline-script", Argv: []string{"python3", "-c", script, "data-only"}}}
	if err := preflightAcceptanceCommands(context.Background(), commands, checker); err != nil {
		t.Fatalf("preflightAcceptanceCommands(): %v", err)
	}
	if len(checker.calls) != 1 {
		t.Fatalf("checker calls = %d, want 1", len(checker.calls))
	}
	if call := checker.calls[0]; call.interpreter != "python3" || call.source != script {
		t.Fatalf("checker call = %#v, want the TaskSpec script handed over only as data", call)
	}

	argv := pythonSyntaxCheckArgv("python3", script)
	wantArgv := []string{"python3", "-I", "-c", preflightHelperSource, script}
	if !reflect.DeepEqual(argv, wantArgv) {
		t.Fatalf("helper argv = %#v, want %#v", argv, wantArgv)
	}
	if argv[3] == script {
		t.Fatal("TaskSpec script must never become the executed -c code")
	}
	if !strings.Contains(preflightHelperSource, "ast.parse") {
		t.Fatal("fixed helper must parse with ast.parse")
	}
	for _, forbidden := range []string{"exec(", "eval(", "open(", "subprocess", "__import__"} {
		if strings.Contains(preflightHelperSource, forbidden) {
			t.Fatalf("fixed helper source contains %q", forbidden)
		}
	}
}

// TestPreflightAcceptanceCommandsNeverSpawnsPathFormInterpreter places a real
// executable named python3 on disk whose only behavior would create a sentinel
// file, then proves path-form argv[0] tokens are skipped without ever being
// spawned: the production checker is used end to end and the sentinel must
// never appear.
func TestPreflightAcceptanceCommandsNeverSpawnsPathFormInterpreter(t *testing.T) {
	directory := t.TempDir()
	sentinel := filepath.Join(directory, "spawn-sentinel")
	executable := filepath.Join(directory, "python3")
	script := "#!/bin/sh\ntouch '" + sentinel + "'\n"
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	commands := []domain.TaskCommand{
		{ID: "spoofed-absolute", Argv: []string{executable, "-c", "x = 1"}},
		{ID: "spoofed-relative", Argv: []string{"./python3", "-c", "x = 1"}},
		{ID: "spoofed-dir", Argv: []string{"bin/python", "-c", "x = 1"}},
		{ID: "spoofed-backslash", Argv: []string{"sub\\python3", "-c", "x = 1"}},
		{ID: "spoofed-exe", Argv: []string{"python3.exe", "-c", "x = 1"}},
	}
	if err := preflightAcceptanceCommands(context.Background(), commands, execPythonSyntaxChecker{}); err != nil {
		t.Fatalf("preflightAcceptanceCommands(): %v, want path-form tokens skipped", err)
	}
	if _, err := os.Lstat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("path-form interpreter token spawned an executable: %v", err)
	}
}

func TestPythonSyntaxCheckRejectsPathFormInterpreter(t *testing.T) {
	directory := t.TempDir()
	executable := filepath.Join(directory, "python3")
	sentinel := filepath.Join(directory, "spawn-sentinel")
	script := "#!/bin/sh\ntouch '" + sentinel + "'\nexit 0\n"
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	checker := execPythonSyntaxChecker{}
	for _, interpreter := range []string{executable, "./python3", "bin/python", "sub\\python3"} {
		finding, err := checker.CheckPythonSyntax(context.Background(), interpreter, "x = 1")
		if finding != nil || !errors.Is(err, errPreflightUnclassified) {
			t.Fatalf("interpreter %q: finding=%#v err=%v, want fail-closed unclassifiable", interpreter, finding, err)
		}
	}
	if _, err := os.Lstat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("path-form interpreter token spawned an executable: %v", err)
	}
}

func TestPreflightAcceptanceCommandsFailsClosedWhenCheckUnavailable(t *testing.T) {
	checker := &preflightFakeChecker{err: errPreflightInterpreterUnavailable}
	commands := []domain.TaskCommand{{ID: "inline-script", Argv: []string{"python3", "-c", "x = 1"}}}
	err := preflightAcceptanceCommands(context.Background(), commands, checker)
	if err == nil || !errors.Is(err, errPreflightInterpreterUnavailable) {
		t.Fatalf("error = %v, want fail-closed interpreter unavailable", err)
	}
	for _, token := range []string{`"inline-script"`, "argv[2]"} {
		if !strings.Contains(err.Error(), token) {
			t.Fatalf("error %q missing %q", err.Error(), token)
		}
	}
}

func TestPythonSyntaxCheckInterpreterMissing(t *testing.T) {
	missing := "marshal-python-missing-for-test"
	if _, err := (execPythonSyntaxChecker{}).CheckPythonSyntax(context.Background(), missing, "x = 1"); !errors.Is(err, errPreflightInterpreterUnavailable) {
		t.Fatalf("error = %v, want interpreter unavailable", err)
	}
}

// fakePython installs a stub executable named python3 as the first PATH entry
// so the production checker resolves the bare token onto it, mirroring the
// fakeGit seam. The script is run via /bin/sh.
func fakePython(t *testing.T, script string) {
	t.Helper()
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "python3"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestPythonSyntaxCheckUnclassifiableOutput(t *testing.T) {
	fakePython(t, "#!/bin/sh\nprintf 'unexpected output\\n'\nexit 0\n")
	if _, err := (execPythonSyntaxChecker{}).CheckPythonSyntax(context.Background(), "python3", "x = 1"); !errors.Is(err, errPreflightUnclassified) {
		t.Fatalf("error = %v, want unclassifiable helper result", err)
	}
}

func TestPythonSyntaxCheckTimeoutFailsClosed(t *testing.T) {
	fakePython(t, "#!/bin/sh\nsleep 30\n")
	previous := preflightTimeout
	preflightTimeout = 100 * time.Millisecond
	defer func() { preflightTimeout = previous }()
	if _, err := (execPythonSyntaxChecker{}).CheckPythonSyntax(context.Background(), "python3", "x = 1"); !errors.Is(err, errPreflightTimeout) {
		t.Fatalf("error = %v, want fail-closed helper timeout", err)
	}
}

func TestPythonSyntaxCheckRejectsNulSource(t *testing.T) {
	checker := execPythonSyntaxChecker{}
	if finding, err := checker.CheckPythonSyntax(context.Background(), "python3", "x\x00 = 1"); finding != nil || !errors.Is(err, errPreflightUnclassified) {
		t.Fatalf("finding=%#v err=%v, want unclassifiable fail-closed", finding, err)
	}
	if finding, err := checker.CheckPythonSyntax(context.Background(), "python3\x00", "x = 1"); finding != nil || !errors.Is(err, errPreflightUnclassified) {
		t.Fatalf("finding=%#v err=%v, want unclassifiable fail-closed", finding, err)
	}
}

func TestPythonSyntaxCheckOutputClassification(t *testing.T) {
	cases := []struct {
		name     string
		output   string
		exitCode int
		want     *SyntaxFinding
		wantErr  error
	}{
		{name: "ok lf", output: "marshal-syntax-check ok\n", exitCode: 0, want: nil},
		{name: "ok crlf", output: "marshal-syntax-check ok\r\n", exitCode: 0, want: nil},
		{name: "finding lf", output: "marshal-syntax-check kind=SyntaxError line=2 column=7\n", exitCode: 3, want: &SyntaxFinding{Kind: "SyntaxError", Line: 2, Column: 7}},
		{name: "finding crlf", output: "marshal-syntax-check kind=IndentationError line=4 column=1\r\n", exitCode: 3, want: &SyntaxFinding{Kind: "IndentationError", Line: 4, Column: 1}},
		{name: "ok unterminated", output: "marshal-syntax-check ok", exitCode: 0, wantErr: errPreflightUnclassified},
		{name: "finding unterminated", output: "marshal-syntax-check kind=SyntaxError line=2 column=7", exitCode: 3, wantErr: errPreflightUnclassified},
		{name: "ok extra line", output: "marshal-syntax-check ok\nextra\n", exitCode: 0, wantErr: errPreflightUnclassified},
		{name: "ok extra line crlf", output: "marshal-syntax-check ok\r\nextra\r\n", exitCode: 0, wantErr: errPreflightUnclassified},
		{name: "ok blank second line", output: "marshal-syntax-check ok\n\n", exitCode: 0, wantErr: errPreflightUnclassified},
		{name: "ok trailing space", output: "marshal-syntax-check ok \n", exitCode: 0, wantErr: errPreflightUnclassified},
		{name: "ok leading space", output: " marshal-syntax-check ok\n", exitCode: 0, wantErr: errPreflightUnclassified},
		{name: "ok lone carriage return", output: "marshal-syntax-check ok\r", exitCode: 0, wantErr: errPreflightUnclassified},
		{name: "ok wrong exit", output: "marshal-syntax-check ok\n", exitCode: 1, wantErr: errPreflightUnclassified},
		{name: "garbage ok line", output: "marshal-syntax-check ok extra\n", exitCode: 0, wantErr: errPreflightUnclassified},
		{name: "unknown kind", output: "marshal-syntax-check kind=WeirdError line=1 column=1\n", exitCode: 3, wantErr: errPreflightUnclassified},
		{name: "negative line", output: "marshal-syntax-check kind=SyntaxError line=-1 column=1\n", exitCode: 3, wantErr: errPreflightUnclassified},
		{name: "protocol violation", output: "marshal-syntax-check protocol-violation\n", exitCode: 4, wantErr: errPreflightUnclassified},
		{name: "empty output", output: "", exitCode: 0, wantErr: errPreflightUnclassified},
	}
	for _, tc := range cases {
		finding, err := classifyPreflightOutput(tc.output, tc.exitCode)
		if tc.wantErr != nil {
			if !errors.Is(err, tc.wantErr) || finding != nil {
				t.Fatalf("%s: finding=%#v err=%v, want %v", tc.name, finding, err, tc.wantErr)
			}
			continue
		}
		if err != nil || !reflect.DeepEqual(finding, tc.want) {
			t.Fatalf("%s: finding=%#v err=%v, want %#v", tc.name, finding, err, tc.want)
		}
	}
}

func TestSupportedPythonInterpreter(t *testing.T) {
	supported := []string{"python", "python3", "python3.9", "python3.13", "python3.0", "python3.99"}
	for _, token := range supported {
		if !supportedPythonInterpreter(token) {
			t.Fatalf("token %q must be supported", token)
		}
	}
	unsupported := []string{
		"", "Python3", "python2", "python3.", "python3.x", "python3.13.1", "mypython3", "pythonista",
		"/usr/bin/python", "/usr/bin/python2.7", "/usr/bin/python3", "/tmp/python3",
		"bin/python", "./python3", "../python3", "python3/",
		"sub\\python3", "python3\\",
		"python.exe", "python3.exe", "python3.12.exe", "/usr/bin/python3.exe",
		"python3 ", " python3", "python3\t",
	}
	for _, token := range unsupported {
		if supportedPythonInterpreter(token) {
			t.Fatalf("token %q must be unsupported", token)
		}
	}
}

// TestPreflightPythonSyntaxCheckHelperIntegration is the minimal integration
// proof with the host's current python3: the fixed helper accepts valid
// source, rejects invalid source with a usable location, and never executes
// the untrusted script (the side-effect sentinel must never appear). The bare
// token python3 is resolved through the trusted PATH exactly as production
// does; no TaskSpec path is ever executed.
func TestPreflightPythonSyntaxCheckHelperIntegration(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is not available on this host")
	}
	const python = "python3"
	checker := execPythonSyntaxChecker{}
	ctx := context.Background()

	if finding, err := checker.CheckPythonSyntax(ctx, python, "print('ok')\n"); err != nil || finding != nil {
		t.Fatalf("valid source: finding=%#v err=%v", finding, err)
	}
	if finding, err := checker.CheckPythonSyntax(ctx, python, ""); err != nil || finding != nil {
		t.Fatalf("empty source: finding=%#v err=%v", finding, err)
	}

	finding, err := checker.CheckPythonSyntax(ctx, python, "def broken(:\n")
	if err != nil || finding == nil {
		t.Fatalf("invalid source: finding=%#v err=%v", finding, err)
	}
	if finding.Kind != "SyntaxError" || finding.Line != 1 || finding.Column < 1 {
		t.Fatalf("finding = %#v, want SyntaxError at line 1 with a usable column", finding)
	}

	sentinel := filepath.Join(t.TempDir(), "sentinel")
	sideEffect := "open(" + strconv.Quote(sentinel) + ", \"w\").write(\"x\")"
	if finding, err := checker.CheckPythonSyntax(ctx, python, sideEffect); err != nil || finding != nil {
		t.Fatalf("side-effect source: finding=%#v err=%v", finding, err)
	}
	assertSentinelAbsent(t, sentinel)

	brokenFinding, err := checker.CheckPythonSyntax(ctx, python, sideEffect+"\n    bad")
	if err != nil || brokenFinding == nil {
		t.Fatalf("broken side-effect source: finding=%#v err=%v", brokenFinding, err)
	}
	assertSentinelAbsent(t, sentinel)
}

func assertSentinelAbsent(t *testing.T, sentinel string) {
	t.Helper()
	if _, err := os.Lstat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("syntax preflight executed the TaskSpec script or wrote %s: %v", sentinel, err)
	}
}
