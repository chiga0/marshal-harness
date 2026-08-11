package planning

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/domain"
)

// Sentinel errors for the acceptance syntax preflight. They are fixed strings
// that never carry script text, environment content, absolute paths, or
// interpreter stderr, so callers can compare and log them deterministically.
var (
	errPreflightInterpreterUnavailable = errors.New("inline Python script syntax preflight failed closed: interpreter unavailable")
	errPreflightTimeout                = errors.New("inline Python script syntax preflight failed closed: helper timeout")
	errPreflightUnclassified           = errors.New("inline Python script syntax preflight failed closed: unclassifiable helper result")
)

const (
	// preflightOutputPrefix is the fixed protocol prefix every helper line emits.
	preflightOutputPrefix = "marshal-syntax-check "
	// preflightOutputOK is the only helper output accepted for valid syntax.
	preflightOutputOK = "marshal-syntax-check ok"
	// preflightExitSyntaxInvalid is the fixed helper exit code for a script
	// that fails ast.parse with a classifiable SyntaxError category.
	preflightExitSyntaxInvalid = 3
	// maxPreflightOutputBytes bounds helper stdout; anything larger is
	// discarded and treated as unclassifiable.
	maxPreflightOutputBytes = 4096
)

// preflightTimeout bounds one helper invocation. It is a variable so tests can
// exercise the timeout path without waiting for a real interpreter.
var preflightTimeout = 10 * time.Second

// preflightSyntaxKinds is the closed set of helper-reported categories the
// service accepts as a classified syntax finding. Anything else fails closed.
var preflightSyntaxKinds = map[string]bool{
	"SyntaxError":      true,
	"IndentationError": true,
	"TabError":         true,
	"ValueError":       true,
}

// preflightHelperSource is Marshal's fixed, trusted compile/ast.parse helper.
// It reads exactly one untrusted source string from its argv data, parses it
// with ast.parse only, and reports a fixed protocol line. It never executes
// the source, never imports or executes TaskSpec code, never touches files,
// and never opens the network. It is handed to the interpreter via -c with the
// untrusted script following as one separate argv data element.
const preflightHelperSource = `import ast
import sys

try:
    source = sys.argv[1]
except IndexError:
    source = None
if source is None:
    print("marshal-syntax-check protocol-violation")
    sys.exit(4)
try:
    ast.parse(source)
except SyntaxError as err:
    kind = type(err).__name__
    line = err.lineno if isinstance(err.lineno, int) else 0
    column = err.offset if isinstance(err.offset, int) else 0
    print("marshal-syntax-check kind=%s line=%d column=%d" % (kind, line, column))
    sys.exit(3)
except ValueError:
    print("marshal-syntax-check kind=ValueError line=0 column=0")
    sys.exit(3)
except Exception:
    print("marshal-syntax-check protocol-violation")
    sys.exit(4)
print("marshal-syntax-check ok")
sys.exit(0)
`

// SyntaxFinding is one Python syntax error reported by the fixed helper. It
// carries only the SyntaxError category and a usable line/column location,
// never any script text.
type SyntaxFinding struct {
	Kind   string
	Line   int
	Column int
}

// PythonSyntaxChecker syntax-checks one inline script with a fixed trusted
// Python helper. A nil finding and a nil error mean the source parses.
// Implementations must never execute the source and must never echo it in
// errors. The optional seam on Input lets tests inject deterministic behavior
// without a real Python installation.
type PythonSyntaxChecker interface {
	CheckPythonSyntax(ctx context.Context, interpreter, source string) (*SyntaxFinding, error)
}

// PreflightSyntaxError rejects one inline acceptance command whose Python
// source failed the syntax preflight. It names the TaskSpec command ID and the
// script's argv index and carries only the SyntaxError category and location:
// never the script text, the environment, absolute paths, or interpreter
// stderr. Preflight is not a Gate, Evidence, or ReviewFinding; the error only
// stops planning from creating side effects for a run Validation would later
// have to reject.
type PreflightSyntaxError struct {
	CommandID string
	ArgvIndex int
	Finding   SyntaxFinding
}

func (e *PreflightSyntaxError) Error() string {
	return fmt.Sprintf("planning: acceptance command %q argv[%d] inline script failed Python syntax preflight: %s at line %d, column %d",
		e.CommandID, e.ArgvIndex, e.Finding.Kind, e.Finding.Line, e.Finding.Column)
}

// preflightAcceptanceCommands syntax-checks the acceptance commands that are
// safely classifiable as Python inline scripts. Plan runs it only after full
// PolicySnapshot validation and before any repository resolution, adapter
// probe, worktree, run lease, journal, or frozen file side effect. Unknown
// interpreters — including every path-form interpreter token — file mode, -m,
// and unsupported argument arrangements are skipped unchanged for independent
// Verification and never guessed or executed. A supported command that cannot
// be checked fails closed.
func preflightAcceptanceCommands(ctx context.Context, commands []domain.TaskCommand, checker PythonSyntaxChecker) error {
	for _, command := range commands {
		interpreter, source, sourceIndex, supported := classifyInlinePythonCommand(command.Argv)
		if !supported {
			continue
		}
		finding, err := checker.CheckPythonSyntax(ctx, interpreter, source)
		if err != nil {
			return fmt.Errorf("planning: acceptance command %q argv[%d]: %w", command.ID, sourceIndex, err)
		}
		if finding != nil {
			return &PreflightSyntaxError{CommandID: command.ID, ArgvIndex: sourceIndex, Finding: *finding}
		}
	}
	return nil
}

// classifyInlinePythonCommand recognizes the single acceptance command form v1
// may syntax-preflight: a bare python-family interpreter token executing an
// inline -c script, where argv[1] is exactly -c and the source is the argv
// element immediately after it, so the script argv index is unambiguous. Empty
// source is classified too and judged by Python syntax rules. Everything else
// returns supported=false and is left to Verification.
func classifyInlinePythonCommand(argv []string) (interpreter, source string, sourceIndex int, supported bool) {
	if len(argv) < 3 {
		return "", "", 0, false
	}
	if !supportedPythonInterpreter(argv[0]) {
		return "", "", 0, false
	}
	if argv[1] != "-c" {
		return "", "", 0, false
	}
	return argv[0], argv[2], 2, true
}

// supportedPythonInterpreter reports whether the command token is one of the
// bare interpreter names v1 may syntax-preflight: exactly python, python3, or
// python3.<digits>. Any token containing a slash or backslash is a path form
// that would name a TaskSpec-supplied executable — it is an unknown
// interpreter and is skipped without execution, as are .exe-suffixed names
// and every other spelling.
func supportedPythonInterpreter(token string) bool {
	if strings.ContainsAny(token, "/\\") {
		return false
	}
	if token == "python" || token == "python3" {
		return true
	}
	version, ok := strings.CutPrefix(token, "python3.")
	if !ok || version == "" {
		return false
	}
	for _, r := range version {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// execPythonSyntaxChecker is the production PythonSyntaxChecker. It resolves a
// bare interpreter token through the Marshal process's trusted PATH and runs
// Marshal's fixed argv with os/exec: no shell, no temp files, no repository
// file access. It never executes a TaskSpec-supplied interpreter path.
type execPythonSyntaxChecker struct{}

func (execPythonSyntaxChecker) CheckPythonSyntax(ctx context.Context, interpreter, source string) (*SyntaxFinding, error) {
	// Only bare interpreter tokens may be executed, resolved through the
	// Marshal process's trusted PATH. A path form would spawn a TaskSpec-
	// supplied executable and is rejected fail-closed.
	if strings.ContainsAny(interpreter, "/\\") {
		return nil, errPreflightUnclassified
	}
	// A NUL byte can never cross an exec argv boundary, so such a command
	// cannot reliably be classified or handed to the helper.
	if strings.ContainsRune(interpreter, 0) || strings.ContainsRune(source, 0) {
		return nil, errPreflightUnclassified
	}
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, preflightTimeout)
	defer cancel()

	stdout := &limitedWriter{limit: maxPreflightOutputBytes}
	runErr := runDirectCommand(ctx, pythonSyntaxCheckArgv(interpreter, source), preflightPythonEnvironment(), stdout)
	if parentErr := parent.Err(); parentErr != nil {
		return nil, parentErr
	}
	if ctx.Err() != nil {
		return nil, errPreflightTimeout
	}
	exitCode := 0
	if runErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(runErr, &exitErr) {
			return nil, errPreflightInterpreterUnavailable
		}
		exitCode = exitErr.ExitCode()
	}
	if stdout.overflow {
		return nil, errPreflightUnclassified
	}
	return classifyPreflightOutput(string(stdout.buf), exitCode)
}

// pythonSyntaxCheckArgv builds Marshal's fixed helper invocation for a bare
// interpreter token that os/exec resolves through the Marshal process's
// trusted PATH. The trusted helper alone is executed via -c, and the untrusted
// source follows as one separate argv data element, which the interpreter
// passes to the helper as sys.argv data and never executes.
func pythonSyntaxCheckArgv(interpreter, source string) []string {
	return []string{interpreter, "-I", "-c", preflightHelperSource, source}
}

// classifyPreflightOutput maps the bounded helper output and exit code to a
// finding. Helper stdout must be exactly one line terminated by a single LF or
// CRLF; unterminated output, extra lines, or surrounding whitespace are
// protocol violations that fail closed. Only exit 0 with the exact OK line
// means valid syntax and only exit 3 with a parseable finding line means
// classified invalid syntax; every other combination fails closed.
func classifyPreflightOutput(output string, exitCode int) (*SyntaxFinding, error) {
	line, ok := singlePreflightOutputLine(output)
	if !ok {
		return nil, errPreflightUnclassified
	}
	if exitCode == 0 {
		if line == preflightOutputOK {
			return nil, nil
		}
		return nil, errPreflightUnclassified
	}
	if exitCode != preflightExitSyntaxInvalid {
		return nil, errPreflightUnclassified
	}
	finding, parsed := parsePreflightFinding(line)
	if !parsed {
		return nil, errPreflightUnclassified
	}
	return finding, nil
}

// singlePreflightOutputLine returns the content of output only when output is
// exactly one line terminated by a single LF or a single CRLF, with no other
// line break anywhere. Unterminated output, extra lines, and every other shape
// are rejected without trimming or relaxing whitespace.
func singlePreflightOutputLine(output string) (string, bool) {
	switch {
	case strings.HasSuffix(output, "\r\n"):
		output = output[:len(output)-2]
	case strings.HasSuffix(output, "\n"):
		output = output[:len(output)-1]
	default:
		return "", false
	}
	if strings.ContainsAny(output, "\r\n") {
		return "", false
	}
	return output, true
}

// parsePreflightFinding strictly parses one helper finding line. It rejects
// unknown categories and malformed numbers so interpreter output can never
// smuggle content into the planning error.
func parsePreflightFinding(line string) (*SyntaxFinding, bool) {
	rest, ok := strings.CutPrefix(line, preflightOutputPrefix)
	if !ok {
		return nil, false
	}
	fields := strings.Split(rest, " ")
	if len(fields) != 3 {
		return nil, false
	}
	kind, kindOK := strings.CutPrefix(fields[0], "kind=")
	lineValue, lineOK := strings.CutPrefix(fields[1], "line=")
	columnValue, columnOK := strings.CutPrefix(fields[2], "column=")
	if !kindOK || !lineOK || !columnOK || !preflightSyntaxKinds[kind] {
		return nil, false
	}
	lineNumber, err := strconv.Atoi(lineValue)
	if err != nil || lineNumber < 0 {
		return nil, false
	}
	columnNumber, err := strconv.Atoi(columnValue)
	if err != nil || columnNumber < 0 {
		return nil, false
	}
	return &SyntaxFinding{Kind: kind, Line: lineNumber, Column: columnNumber}, true
}

// preflightPythonEnvironment mirrors the restricted git invocations: a stable
// locale and only PATH, HOME, and TMPDIR pass through, so no credentials or
// harness variables reach the interpreter. The Python-specific variables deny
// user site packages, bytecode writes, and PYTHONPATH imports; the -I flag on
// the invocation additionally ignores them at the interpreter level.
func preflightPythonEnvironment() []string {
	environment := []string{
		"LC_ALL=C",
		"LANG=C",
		"PYTHONDONTWRITEBYTECODE=1",
		"PYTHONNOUSERSITE=1",
		"PYTHONPATH=",
	}
	for _, name := range []string{"PATH", "HOME", "TMPDIR"} {
		if value := os.Getenv(name); value != "" {
			environment = append(environment, name+"="+value)
		}
	}
	return environment
}
