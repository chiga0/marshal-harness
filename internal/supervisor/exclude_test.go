package supervisor

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseExcludeListLineFormat(t *testing.T) {
	data := []byte("# full-line comment\n" +
		"run-alpha\n" +
		"\n" +
		"   \n" +
		"  run-beta  \n" +
		"run-gamma\r\n" +
		"#another-comment\n" +
		"   # indented comment\n" +
		"run-alpha\n")
	entries, err := parseExcludeList(data)
	if err != nil {
		t.Fatalf("parseExcludeList: %v", err)
	}
	want := map[string]struct{}{
		"run-alpha": {},
		"run-beta":  {},
		"run-gamma": {},
	}
	if !reflect.DeepEqual(entries, want) {
		t.Fatalf("entries = %v, want %v", entries, want)
	}
}

func TestParseExcludeListEmptyInput(t *testing.T) {
	for _, data := range [][]byte{nil, {}, []byte("# only a comment\n\n")} {
		entries, err := parseExcludeList(data)
		if err != nil {
			t.Fatalf("parseExcludeList(%q): %v", data, err)
		}
		if len(entries) != 0 {
			t.Fatalf("parseExcludeList(%q) = %v, want empty", data, entries)
		}
	}
}

func TestParseExcludeListOverlongLineFailsClosed(t *testing.T) {
	data := make([]byte, maxExcludeListLineBytes+1)
	for index := range data {
		data[index] = 'a'
	}
	_, err := parseExcludeList(data)
	if !errors.Is(err, ErrExcludeListUnreadable) {
		t.Fatalf("parseExcludeList with overlong line: err = %v, want ErrExcludeListUnreadable", err)
	}
}

func TestLoadExcludeListMissingFileIsEmpty(t *testing.T) {
	entries, err := loadExcludeList(t.TempDir())
	if err != nil {
		t.Fatalf("loadExcludeList with missing file: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("entries = %v, want empty list for missing file", entries)
	}
}

func TestLoadExcludeListReadsStateRootRelativePath(t *testing.T) {
	root := t.TempDir()
	listDir := filepath.Join(root, ".marshal")
	if err := os.MkdirAll(listDir, 0o700); err != nil {
		t.Fatalf("mkdir .marshal under state root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(listDir, "supervise-exclude"), []byte("run-one\nrun-two\n"), 0o600); err != nil {
		t.Fatalf("write exclude list: %v", err)
	}
	entries, err := loadExcludeList(root)
	if err != nil {
		t.Fatalf("loadExcludeList: %v", err)
	}
	want := map[string]struct{}{"run-one": {}, "run-two": {}}
	if !reflect.DeepEqual(entries, want) {
		t.Fatalf("entries = %v, want %v", entries, want)
	}
}

// TestLoadExcludeListReadFailureFailsClosed covers the fail-closed rule with
// a failure mode that is deterministic regardless of the test user: a
// directory occupies the exclusion-list path, so reading it must fail.
func TestLoadExcludeListReadFailureFailsClosed(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".marshal", "supervise-exclude"), 0o700); err != nil {
		t.Fatalf("create directory at exclusion list path: %v", err)
	}
	entries, err := loadExcludeList(root)
	if !errors.Is(err, ErrExcludeListUnreadable) {
		t.Fatalf("loadExcludeList with unreadable list: err = %v, want ErrExcludeListUnreadable", err)
	}
	if entries != nil {
		t.Fatalf("entries = %v, want nil on read failure", entries)
	}
}

func TestExcludeEntriesMatchRunIDsExactly(t *testing.T) {
	entries := map[string]struct{}{"run-a": {}}
	if _, ok := entries["run-a"]; !ok {
		t.Fatal("entry run-a must match")
	}
	for _, candidate := range []string{"run-ab", "run-", "run-a ", "RUN-A", "run-a-extra"} {
		if _, ok := entries[candidate]; ok {
			t.Fatalf("entry %q must not match run-a exactly", candidate)
		}
	}
}

func TestWriteDomainEntryConflictShapes(t *testing.T) {
	cases := []struct {
		name string
		a    string
		b    string
		want bool
	}{
		{name: "equal paths conflict", a: "src/app/main.go", b: "src/app/main.go", want: true},
		{name: "directory prefix conflicts", a: "src", b: "src/app/main.go", want: true},
		{name: "reverse directory prefix conflicts", a: "src/app/main.go", b: "src", want: true},
		{name: "wildcard contains concrete path", a: "docs/**", b: "docs/guide.md", want: true},
		{name: "reverse wildcard contains concrete path", a: "docs/guide.md", b: "docs/**", want: true},
		{name: "single-star wildcard conflicts", a: "internal/*/cli.go", b: "internal/cli/cli.go", want: true},
		{name: "glob-all conflicts with anything", a: "**", b: "anywhere/deep/file.txt", want: true},
		{name: "sibling directory names do not conflict", a: "src", b: "src2/main.go", want: false},
		{name: "sibling files do not conflict", a: "src/app.go", b: "src/app_test.go", want: false},
		{name: "disjoint directories do not conflict", a: "docs/**", b: "tools/readme.md", want: false},
		{name: "distinct files in same directory do not conflict", a: "internal/a.go", b: "internal/b.go", want: false},
		{name: "empty entry never conflicts", a: "", b: "src/main.go", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := writeDomainEntryConflict(tc.a, tc.b); got != tc.want {
				t.Fatalf("writeDomainEntryConflict(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
			if got := writeDomainEntryConflict(tc.b, tc.a); got != tc.want {
				t.Fatalf("writeDomainEntryConflict(%q, %q) = %v, want symmetric %v", tc.b, tc.a, got, tc.want)
			}
		})
	}
}

// TestWriteDomainEntryConflictMalformedPatternFailsClosed pins the fail-
// closed rule: a wildcard pattern that cannot be parsed must count as a
// conflict instead of being ignored.
func TestWriteDomainEntryConflictMalformedPatternFailsClosed(t *testing.T) {
	if !writeDomainEntryConflict("[", "src/main.go") {
		t.Fatal("malformed pattern must fail closed and conflict")
	}
}

func TestAllowPathsConflictMatrix(t *testing.T) {
	cases := []struct {
		name      string
		candidate []string
		inflight  []string
		want      bool
	}{
		{name: "overlapping pair in larger domains", candidate: []string{"README.md", "src/app/main.go"}, inflight: []string{"docs/**", "src/app/main.go"}, want: true},
		{name: "disjoint domains", candidate: []string{"src/**", "internal/tool.go"}, inflight: []string{"docs/**", "tools/*"}, want: false},
		{name: "empty candidate domain never conflicts", candidate: nil, inflight: []string{"src/**"}, want: false},
		{name: "empty inflight domain never conflicts", candidate: []string{"src/**"}, inflight: nil, want: false},
		{name: "both empty domains never conflict", candidate: nil, inflight: nil, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := allowPathsConflict(tc.candidate, tc.inflight); got != tc.want {
				t.Fatalf("allowPathsConflict(%v, %v) = %v, want %v", tc.candidate, tc.inflight, got, tc.want)
			}
		})
	}
}

func TestReadSpecAllowPaths(t *testing.T) {
	root := t.TempDir()
	runDir := filepath.Join(root, "runs", "run-spec")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	spec := []byte(`{"apiVersion":"marshal.dev/v1alpha1","kind":"Task","scope":{"allowPaths":["src/**","docs/README.md"]}}`)
	if err := os.WriteFile(filepath.Join(runDir, "task-spec.json"), spec, 0o600); err != nil {
		t.Fatalf("write task-spec.json: %v", err)
	}
	got, err := readSpecAllowPaths(root, "run-spec")
	if err != nil {
		t.Fatalf("readSpecAllowPaths: %v", err)
	}
	want := []string{"src/**", "docs/README.md"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("allowPaths = %v, want %v", got, want)
	}

	t.Run("missing spec is an error", func(t *testing.T) {
		if _, err := readSpecAllowPaths(root, "run-missing"); err == nil {
			t.Fatal("readSpecAllowPaths with missing spec must fail")
		}
	})
	t.Run("broken spec is an error", func(t *testing.T) {
		brokenDir := filepath.Join(root, "runs", "run-broken")
		if err := os.MkdirAll(brokenDir, 0o700); err != nil {
			t.Fatalf("mkdir broken run dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(brokenDir, "task-spec.json"), []byte("{not-json"), 0o600); err != nil {
			t.Fatalf("write broken spec: %v", err)
		}
		if _, err := readSpecAllowPaths(root, "run-broken"); err == nil {
			t.Fatal("readSpecAllowPaths with broken spec must fail")
		}
	})
}
