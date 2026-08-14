package supervisor

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// excludeListRelativePath is the stateRoot-relative location of the
// supervise exclusion list introduced by issue #100. Runs listed here are
// never re-dispatched by supervise.
const excludeListRelativePath = ".marshal/supervise-exclude"

// maxExcludeListLineBytes bounds one exclusion-list line; longer input fails
// closed instead of being silently truncated into a wrong runId.
const maxExcludeListLineBytes = 4096

// ErrExcludeListUnreadable is the fixed sentinel returned when the exclusion
// list exists but cannot be read or parsed. The affected supervise round
// fails closed: no Run is re-dispatched at all and the error is reported to
// the caller. A missing list file is legal and empty; only an existing,
// unreadable list triggers this sentinel.
var ErrExcludeListUnreadable = errors.New("supervisor: supervise-exclude list unreadable")

// loadExcludeList reads the exclusion list at
// stateRoot/.marshal/supervise-exclude and returns the set of excluded
// runIds. A missing file is a legal empty list. An existing file that
// cannot be read fails closed with ErrExcludeListUnreadable: the round must
// never degrade to "no list" semantics.
//
// Entries are exact runId matches — wildcards are deliberately not
// supported, keeping the exclusion semantics minimal. The line format is one
// runId per line; lines whose first non-blank character is '#' are
// comments, blank lines are ignored, and surrounding whitespace (including
// a trailing carriage return) is stripped.
func loadExcludeList(stateRoot string) (map[string]struct{}, error) {
	data, err := os.ReadFile(filepath.Join(stateRoot, excludeListRelativePath))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]struct{}{}, nil
		}
		return nil, fmt.Errorf("%w: %v", ErrExcludeListUnreadable, err)
	}
	return parseExcludeList(data)
}

// parseExcludeList decodes the exclusion-list line format. Any structural
// read error (for example an overlong line) fails closed with
// ErrExcludeListUnreadable instead of yielding a partial list.
func parseExcludeList(data []byte) (map[string]struct{}, error) {
	entries := make(map[string]struct{})
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, maxExcludeListLineBytes), maxExcludeListLineBytes)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		entries[line] = struct{}{}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrExcludeListUnreadable, err)
	}
	return entries, nil
}

// readSpecAllowPaths reads scope.allowPaths from the frozen task-spec.json
// of one Run under stateRoot/runs. It is strictly read-only and never
// modifies the spec; any read or decode failure is returned so callers can
// fail closed.
func readSpecAllowPaths(stateRoot, runID string) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(stateRoot, "runs", runID, "task-spec.json"))
	if err != nil {
		return nil, fmt.Errorf("read task-spec.json: %w", err)
	}
	var spec struct {
		Scope struct {
			AllowPaths []string `json:"allowPaths"`
		} `json:"scope"`
	}
	if err := json.Unmarshal(data, &spec); err != nil {
		return nil, fmt.Errorf("decode task-spec.json: %w", err)
	}
	return spec.Scope.AllowPaths, nil
}

// allowPathsConflict reports whether two frozen TaskSpec write domains
// overlap (issue #100): any path pair that is equal, where one side is a
// directory prefix of the other, or where either side's wildcard pattern
// matches the other path is a conflict.
func allowPathsConflict(candidate, inflight []string) bool {
	for _, left := range candidate {
		for _, right := range inflight {
			if writeDomainEntryConflict(left, right) {
				return true
			}
		}
	}
	return false
}

// writeDomainEntryConflict decides one path pair of two write domains. All
// undecidable input fails closed: a malformed wildcard pattern counts as a
// conflict rather than being ignored.
func writeDomainEntryConflict(a, b string) bool {
	left := normalizeWriteDomainPath(a)
	right := normalizeWriteDomainPath(b)
	if left == "" || right == "" {
		return false
	}
	if left == right {
		return true
	}
	if sameOrDirectoryPrefix(left, right) || sameOrDirectoryPrefix(right, left) {
		return true
	}
	return wildcardMatches(left, right) || wildcardMatches(right, left)
}

// normalizeWriteDomainPath trims and lexically cleans one repository-
// relative write-domain entry. Frozen TaskSpec entries are already clean
// (contract semantic checks enforce it); the normalization is defensive and
// maps degenerate entries to the empty string, which never conflicts.
func normalizeWriteDomainPath(value string) string {
	cleaned := path.Clean(strings.TrimSpace(value))
	if cleaned == "." {
		return ""
	}
	return cleaned
}

// sameOrDirectoryPrefix reports whether dir equals target or is a directory
// prefix of it: "src" contains "src/app/main.go" but not "src2/main.go".
func sameOrDirectoryPrefix(dir, target string) bool {
	return dir == target || strings.HasPrefix(target, dir+"/")
}

// wildcardMatches treats pattern as a repository-relative glob (doublestar
// semantics, matching the frozen-scope evaluation in verification) and
// target as a concrete path. A malformed pattern fails closed and matches
// everything.
func wildcardMatches(pattern, target string) bool {
	matched, err := doublestar.Match(pattern, target)
	if err != nil {
		return true
	}
	return matched
}
