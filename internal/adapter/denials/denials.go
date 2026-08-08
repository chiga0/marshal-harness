// Package denials implements ADR 0013: deterministic grading of worker
// permission denials. Every denial is either BENIGN (expected read-only
// probes; recorded and the attempt continues) or FATAL (fail-closed
// immediately). Grading uses fixed rules only; no model judgement is ever
// applied, and any denial that cannot be proven benign is FATAL.
package denials

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Grade is the binary verdict for one denial event.
type Grade string

const (
	// Benign marks expected read-only probes. The attempt continues and the
	// event is persisted to the denial log as evidence.
	Benign Grade = "BENIGN"
	// Fatal marks a security-relevant denial. The adapter must terminate the
	// attempt fail-closed immediately.
	Fatal Grade = "FATAL"
)

// Kind is the operation class of the denied tool call, derived from a fixed
// tool table. Unknown tools stay unknown and always grade FATAL.
type Kind string

const (
	KindRead    Kind = "read"
	KindWrite   Kind = "write"
	KindExecute Kind = "execute"
	KindUnknown Kind = "unknown"
)

// LogFileName is the shared denial evidence file name inside each attempt's
// control output directory.
const LogFileName = "denials.jsonl"

// readTools, writeTools and executeTools are the frozen tool tables shared
// by every adapter. Changes to these tables are security-semantics changes
// and require audit, per ADR 0013.
var readTools = map[string]bool{
	"find": true, "glob": true, "grep": true, "list": true, "list_directory": true,
	"list_folder": true, "ls": true, "lsp": true, "read": true, "read_file": true,
	"read_many_files": true, "search_file": true, "search_file_content": true,
}

var writeTools = map[string]bool{
	"apply_patch": true, "edit": true, "insert": true, "multiedit": true,
	"notebook_edit": true, "patch": true, "replace": true, "save_file": true,
	"save_memory": true, "write": true, "write_file": true, "write_todos": true,
}

var executeTools = map[string]bool{
	"bash": true, "command": true, "exec": true, "run_shell_command": true, "shell": true,
}

// ToolKind maps a tool name to its operation class deterministically.
func ToolKind(tool string) Kind {
	switch {
	case readTools[tool]:
		return KindRead
	case writeTools[tool]:
		return KindWrite
	case executeTools[tool]:
		return KindExecute
	default:
		return KindUnknown
	}
}

// Event is one normalized denial observation extracted from an adapter
// transcript. Target is the path or command the provider denied, exactly as
// the transcript reported it.
type Event struct {
	Tool   string
	Target string
	Error  string
}

// Decision is the deterministic grading outcome for one denial event.
type Decision struct {
	Grade  Grade
	Kind   Kind
	Reason string
}

// Classifier applies the ADR 0013 rules to denial events. All paths must be
// absolute and already resolved by the adapter (realpath where required).
type Classifier struct {
	Provider    string
	Worktree    string
	ControlRoot string
	TempDir     string
}

// Classify grades one denial event. The rules are fixed and fail-closed:
// only read-class denials can ever be benign, and only when the target
// matches an explicit benign candidate list.
func (c Classifier) Classify(event Event) Decision {
	kind := ToolKind(event.Tool)
	if kind == KindWrite {
		return Decision{Grade: Fatal, Kind: kind, Reason: "写类拒绝一律 FATAL"}
	}
	if kind == KindExecute {
		if readOnlyExecute(event.Target) {
			return Decision{Grade: Benign, Kind: kind, Reason: "只读内省/自验证命令，ADR-0013 修订"}
		}
		return Decision{Grade: Fatal, Kind: kind, Reason: "执行类拒绝一律 FATAL"}
	}
	if kind == KindUnknown {
		return Decision{Grade: Fatal, Kind: kind, Reason: "未知拒绝默认 FATAL"}
	}
	target := filepath.Clean(event.Target)
	if event.Target == "" || !filepath.IsAbs(target) {
		return Decision{Grade: Fatal, Kind: kind, Reason: "拒绝目标缺失或非绝对路径，无法可靠分级"}
	}
	if contained(filepath.Join(c.TempDir, c.Provider), target) {
		return Decision{Grade: Benign, Kind: kind, Reason: "Provider 自引导只读产物"}
	}
	if contained(c.ControlRoot, target) && !contained(filepath.Join(c.ControlRoot, "input"), target) {
		return Decision{Grade: Benign, Kind: kind, Reason: "control 目录下非 control/input 只读路径"}
	}
	if real, err := filepath.EvalSymlinks(target); err == nil && within(resolveOrLexical(c.Worktree), real) {
		return Decision{Grade: Benign, Kind: kind, Reason: "读操作 realpath 落在受管 worktree 内"}
	}
	return Decision{Grade: Fatal, Kind: kind, Reason: "读拒绝不落入 BENIGN 候选清单"}
}

// readOnlyExecute reports whether a denied execute-class command is a known
// read-only introspection or self-verification form that must not terminate
// the attempt (ADR-0013 amendment). Matching is token-precise, never prefix
// matching, so `git tag -l` is benign while `git tag foo` stays FATAL and
// `bash -n x` is benign while `bash x` / `sh -c` stay FATAL.
func readOnlyExecute(command string) bool {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return false
	}
	switch fields[0] {
	case "ls", "cat", "head", "tail", "wc", "find", "grep", "rg", "file", "stat", "which", "pwd", "echo", "date":
		return true
	case "git":
		if len(fields) < 2 {
			return false
		}
		switch fields[1] {
		case "log", "status", "diff", "show", "ls-files", "remote", "branch":
			return true
		case "tag":
			return len(fields) >= 3 && (fields[2] == "-l" || fields[2] == "--list")
		}
		return false
	case "go":
		if len(fields) < 2 {
			return false
		}
		switch fields[1] {
		case "test", "build", "vet", "fmt", "run", "mod", "list":
			return true
		}
		return false
	case "bash", "sh":
		return len(fields) >= 2 && fields[1] == "-n"
	case "gofmt":
		return true
	}
	return false
}

// resolveOrLexical returns the realpath of path when it resolves, otherwise
// the already-cleaned lexical path. A dangling symlink therefore never
// masquerades as an escape.
func resolveOrLexical(path string) string {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	return path
}

// contained reports whether target sits inside root. Symlinked parents such
// as /var -> /private/var must not break containment, so both sides are
// compared as realpaths whenever the target resolves. A target that does not
// resolve (nonexistent or dangling symlink) can only be judged by its lexical
// spelling; it cannot read anything outside because nothing backs it.
func contained(root, target string) bool {
	if real, err := filepath.EvalSymlinks(target); err == nil {
		return within(resolveOrLexical(root), real)
	}
	return within(root, target)
}

// within reports whether path equals root or sits beneath it. Both arguments
// must be absolute cleaned paths.
func within(root, path string) bool {
	if root == "" || path == "" {
		return false
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

// IsPermissionError reports whether provider error text marks a permission
// denial. The keyword set is fixed and never interprets provider content.
func IsPermissionError(text string) bool {
	lower := strings.ToLower(text)
	if !strings.Contains(lower, "permission") {
		return false
	}
	return strings.Contains(lower, "denied") || strings.Contains(lower, "prevents") || strings.Contains(lower, "rule")
}

var pathKeys = []string{"filePath", "absolute_path", "file_path", "path", "directory", "dir"}
var commandKeys = []string{"command", "cmd"}

// ExtractTarget returns the denied path or command from the provider's tool
// input arguments. Only the fixed candidate keys of the tool's own operation
// class are honored; anything else yields an empty target, which grades
// FATAL fail-closed. Unknown tools try both key classes so evidence is not
// lost, although their grade stays FATAL.
func ExtractTarget(tool string, input json.RawMessage) string {
	if len(bytes.TrimSpace(input)) == 0 {
		return ""
	}
	var args map[string]any
	if err := json.Unmarshal(input, &args); err != nil || args == nil {
		return ""
	}
	var keys []string
	switch ToolKind(tool) {
	case KindExecute:
		keys = commandKeys
	case KindRead, KindWrite:
		keys = pathKeys
	default:
		keys = append(append([]string{}, pathKeys...), commandKeys...)
	}
	return firstString(args, keys)
}

func firstString(args map[string]any, keys []string) string {
	for _, key := range keys {
		if value, ok := args[key].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// RawDenial is a captured, not yet graded denial, exactly as an adapter
// extracts it from the provider stream. Target extraction from provider
// input arguments is shared here so every adapter applies identical rules.
type RawDenial struct {
	Tool  string
	Input json.RawMessage
}

// GradeRaw extracts targets from captured denials and grades them in
// transcript order.
func GradeRaw(classifier Classifier, captured []RawDenial, now func() time.Time) []Record {
	events := make([]Event, 0, len(captured))
	for _, denial := range captured {
		events = append(events, Event{Tool: denial.Tool, Target: ExtractTarget(denial.Tool, denial.Input)})
	}
	return GradeEvents(classifier, events, now)
}

// Record is one line of the denial log evidence file.
type Record struct {
	Seq       int       `json:"seq"`
	Tool      string    `json:"tool"`
	Kind      string    `json:"kind"`
	PathOrCmd string    `json:"path-or-cmd"`
	Grade     string    `json:"grade"`
	Reason    string    `json:"reason"`
	At        time.Time `json:"at"`
}

// GradeEvents grades normalized denial events in transcript order and assigns
// monotonically increasing sequence numbers and timestamps.
func GradeEvents(classifier Classifier, events []Event, now func() time.Time) []Record {
	records := make([]Record, 0, len(events))
	for index, event := range events {
		decision := classifier.Classify(event)
		records = append(records, Record{
			Seq:       index + 1,
			Tool:      event.Tool,
			Kind:      string(decision.Kind),
			PathOrCmd: event.Target,
			Grade:     string(decision.Grade),
			Reason:    decision.Reason,
			At:        now().UTC(),
		})
	}
	return records
}

// CountFatal returns how many records grade FATAL.
func CountFatal(records []Record) int {
	count := 0
	for _, record := range records {
		if record.Grade == string(Fatal) {
			count++
		}
	}
	return count
}

// AppendLog appends records to the denial log at path with 0600 permissions,
// creating the file and parent directory when needed.
func AppendLog(path string, records []Record) error {
	if len(records) == 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	for _, record := range records {
		data, err := json.Marshal(record)
		if err != nil {
			return err
		}
		if _, err := file.Write(append(data, '\n')); err != nil {
			return err
		}
	}
	return file.Sync()
}

// ParseLog decodes a denial log. Any malformed line is an error so denial
// evidence can never be silently discarded.
func ParseLog(data []byte) ([]Record, error) {
	records := []Record{}
	lineNumber := 0
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		lineNumber++
		var record Record
		if err := json.Unmarshal(line, &record); err != nil {
			return nil, fmt.Errorf("denial log line %d: %w", lineNumber, err)
		}
		records = append(records, record)
	}
	return records, nil
}

// Summary counts graded denial records.
type Summary struct {
	Benign int
	Fatal  int
}

// Summarize counts records; anything that is not BENIGN counts as FATAL so
// accounting stays fail-closed.
func Summarize(records []Record) Summary {
	summary := Summary{}
	for _, record := range records {
		if record.Grade == string(Benign) {
			summary.Benign++
		} else {
			summary.Fatal++
		}
	}
	return summary
}
