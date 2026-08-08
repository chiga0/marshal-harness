package denials

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIsPermissionErrorUsesFixedKeywordsOnly(t *testing.T) {
	for _, text := range []string{
		"Permission denied",
		"permission DENIED by rule",
		"permission rule prevents this tool",
		"permission prevents bash execution",
	} {
		if !IsPermissionError(text) {
			t.Fatalf("permission marker not detected in %q", text)
		}
	}
	for _, text := range []string{"", "file not found", "permission granted", "rule violation without marker"} {
		if IsPermissionError(text) {
			t.Fatalf("false permission marker in %q", text)
		}
	}
}

func TestToolKindIsDeterministic(t *testing.T) {
	tests := []struct {
		tool string
		kind Kind
	}{
		{"read", KindRead}, {"glob", KindRead}, {"grep", KindRead}, {"list", KindRead},
		{"ls", KindRead}, {"find", KindRead}, {"read_file", KindRead}, {"read_many_files", KindRead},
		{"edit", KindWrite}, {"write", KindWrite}, {"write_file", KindWrite}, {"multiedit", KindWrite},
		{"bash", KindExecute}, {"shell", KindExecute}, {"run_shell_command", KindExecute},
		{"question", KindUnknown}, {"task", KindUnknown}, {"webfetch", KindUnknown}, {"", KindUnknown},
	}
	for _, test := range tests {
		if got := ToolKind(test.tool); got != test.kind {
			t.Fatalf("ToolKind(%q) = %s, want %s", test.tool, got, test.kind)
		}
	}
}

func TestExtractTargetHonorsOnlyFixedKeys(t *testing.T) {
	path := "/tmp/work/file.go"
	for _, test := range []struct {
		tool, input, want string
	}{
		{"read", `{"filePath":"` + path + `"}`, path},
		{"read_file", `{"absolute_path":"` + path + `"}`, path},
		{"grep", `{"path":"` + path + `","pattern":"x"}`, path},
		{"bash", `{"command":"ls -la"}`, "ls -la"},
		{"shell", `{"cmd":"ls"}`, "ls"},
		{"bash", `{"filePath":"` + path + `"}`, ""},
		{"read", `{"command":"not-a-path"}`, ""},
		{"read", ``, ""},
		{"read", `not-json`, ""},
		{"read", `{"filePath":5}`, ""},
		{"read", `{"filePath":"   "}`, ""},
	} {
		got := ExtractTarget(test.tool, json.RawMessage(test.input))
		if got != test.want {
			t.Fatalf("ExtractTarget(%s, %s) = %q, want %q", test.tool, test.input, got, test.want)
		}
	}
}

func TestGradeEventsAssignsSequenceAndGrades(t *testing.T) {
	classifier := Classifier{Provider: "opencode", Worktree: "/worktree", ControlRoot: "/control", TempDir: "/tmpdir"}
	fixed := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	events := []Event{
		{Tool: "read", Target: "/tmpdir/opencode/work-context.txt"},
		{Tool: "bash", Target: "curl http://evil.example"},
	}
	records := GradeEvents(classifier, events, func() time.Time { return fixed })
	if len(records) != 2 {
		t.Fatalf("records = %+v", records)
	}
	if records[0].Seq != 1 || records[0].Grade != string(Benign) || records[0].Kind != string(KindRead) || !records[0].At.Equal(fixed) {
		t.Fatalf("benign record = %+v", records[0])
	}
	if records[1].Seq != 2 || records[1].Grade != string(Fatal) || records[1].Kind != string(KindExecute) || records[1].PathOrCmd != "curl http://evil.example" {
		t.Fatalf("fatal record = %+v", records[1])
	}
	if CountFatal(records) != 1 {
		t.Fatalf("CountFatal = %d", CountFatal(records))
	}
}

func TestAppendLogIs0600AndAppendOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "output", LogFileName)
	classifier := Classifier{Provider: "pi", Worktree: "/worktree", ControlRoot: "/control", TempDir: "/tmpdir"}
	fixed := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	first := GradeEvents(classifier, []Event{{Tool: "read", Target: "/tmpdir/pi/work-context.txt"}}, func() time.Time { return fixed })
	second := GradeEvents(classifier, []Event{{Tool: "bash", Target: "sudo rm -rf /"}}, func() time.Time { return fixed })
	if err := AppendLog(path, first); err != nil {
		t.Fatal(err)
	}
	if err := AppendLog(path, second); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("denial log permissions = %v, want 0600", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	records, err := ParseLog(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].Grade != string(Benign) || records[1].Grade != string(Fatal) {
		t.Fatalf("round-tripped records = %+v", records)
	}
	if err := AppendLog(path, nil); err != nil {
		t.Fatal(err)
	}
}

func TestParseLogRejectsMalformedEvidence(t *testing.T) {
	if _, err := ParseLog([]byte(`{"seq":1`)); err == nil {
		t.Fatal("malformed denial log accepted")
	}
	records, err := ParseLog([]byte("\n{\"seq\":1,\"tool\":\"read\",\"kind\":\"read\",\"path-or-cmd\":\"/x\",\"grade\":\"BENIGN\",\"reason\":\"r\",\"at\":\"2026-08-07T12:00:00Z\"}\n\n"))
	if err != nil || len(records) != 1 {
		t.Fatalf("records = %+v err = %v", records, err)
	}
}

func TestSummarizeCountsEverythingNotBenignAsFatal(t *testing.T) {
	records := []Record{
		{Grade: string(Benign)}, {Grade: string(Fatal)}, {Grade: "WEIRD"},
	}
	summary := Summarize(records)
	if summary.Benign != 1 || summary.Fatal != 2 {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestClassifierReasonsNeverEchoProviderText(t *testing.T) {
	classifier := Classifier{Provider: "opencode", Worktree: "/worktree", ControlRoot: "/control", TempDir: "/tmpdir"}
	decision := classifier.Classify(Event{Tool: "read", Target: "/etc/passwd", Error: "permission denied: sk-super-secret-token"})
	if decision.Grade != Fatal {
		t.Fatalf("grade = %+v", decision)
	}
	if strings.Contains(decision.Reason, "sk-super-secret-token") || strings.Contains(decision.Reason, "permission denied") {
		t.Fatalf("reason echoes provider error text: %s", decision.Reason)
	}
}

func TestExecuteGradingDistinguishesReadOnlyIntrospection(t *testing.T) {
	classifier := Classifier{Provider: "opencode", Worktree: "/worktree", ControlRoot: "/control", TempDir: "/tmpdir"}
	fixed := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		cmd  string
		want Grade
	}{
		{"git tag -l", Benign},
		{"git tag --list", Benign},
		{"git tag v1.0.0", Fatal},
		{"git log --oneline -5", Benign},
		{"git status --short", Benign},
		{"go test -race ./internal/verification/...", Benign},
		{"go vet ./...", Benign},
		{"bash -n scripts/install.sh", Benign},
		{"bash scripts/install.sh", Fatal},
		{"sh -c 'echo hi'", Fatal},
		{"curl http://evil.example", Fatal},
		{"git push origin main", Fatal},
		{"cat README.md", Benign},
	}
	for _, tc := range cases {
		records := GradeEvents(classifier, []Event{{Tool: "bash", Target: tc.cmd}}, func() time.Time { return fixed })
		if len(records) != 1 || records[0].Grade != string(tc.want) {
			t.Fatalf("cmd %q grade = %+v, want %s", tc.cmd, records, tc.want)
		}
	}
}
