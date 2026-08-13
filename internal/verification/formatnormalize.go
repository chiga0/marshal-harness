package verification

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// formatNormalizeTimeout bounds a single gofmt invocation; gofmt is a pure
// formatting pass and must never stall verification.
const formatNormalizeTimeout = 2 * time.Minute

// formatNormalizeLogLimit bounds the captured gofmt output, mirroring the
// bounded logging discipline of Runner.
const formatNormalizeLogLimit = 1 << 20

// normalizeFormat deterministically normalizes the changed Go files that lie
// inside the frozen scope allow-list. It selects the .go files among
// changedPaths that match an allowPaths pattern and exist as regular files
// in the worktree, detects formatting drift with gofmt -l and rewrites only
// the drifting files with gofmt -w. The returned slice lists the normalized
// files as repository-relative paths in deterministic (sorted) order; an
// empty slice is a legal outcome meaning nothing needed normalization.
//
// The normalization is deterministic (gofmt output is unique for a given
// input) and does not change program semantics. It waives no quality gate:
// format-check still runs, only now on normalized bytes, so formatting-only
// drift stops burning rework rounds. Every normalized file is recorded
// transparently by the format:normalize gate evidence. gofmt unavailability
// or any execution failure is returned as an error so Verify fails closed;
// nothing is ever skipped silently. Files outside allowPaths are never
// normalized; they are left to the scope:changed-paths gate.
func normalizeFormat(ctx context.Context, worktree string, changedPaths []string, allowPaths []string) (normalized []string, err error) {
	seen := map[string]bool{}
	candidates := make([]string, 0, len(changedPaths))
	for _, path := range changedPaths {
		if err := validateRelativePath(path); err != nil {
			return nil, err
		}
		if seen[path] || !strings.HasSuffix(path, ".go") {
			continue
		}
		seen[path] = true
		allowed, err := matchesAny(allowPaths, path)
		if err != nil {
			return nil, err
		}
		if !allowed {
			continue
		}
		info, err := os.Lstat(filepath.Join(worktree, filepath.FromSlash(path)))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		candidates = append(candidates, path)
	}
	if len(candidates) == 0 {
		return []string{}, nil
	}
	sort.Strings(candidates)
	environment := verifierEnvironment(nil)
	gofmtPath, err := lookPath("gofmt", worktree, worktree, environment)
	if err != nil {
		return nil, fmt.Errorf("resolve gofmt: %w", err)
	}
	candidateSet := make(map[string]bool, len(candidates))
	for _, path := range candidates {
		candidateSet[path] = true
	}
	listing, err := runGofmt(ctx, gofmtPath, environment, worktree, append([]string{"-l"}, candidates...))
	if err != nil {
		return nil, err
	}
	unformatted := parseGofmtList(listing, candidateSet)
	if len(unformatted) == 0 {
		return []string{}, nil
	}
	if _, err := runGofmt(ctx, gofmtPath, environment, worktree, append([]string{"-w"}, unformatted...)); err != nil {
		return nil, err
	}
	sort.Strings(unformatted)
	return unformatted, nil
}

// runGofmt executes gofmt directly (argv only, no shell) with the sanitized
// verifier environment, bounded output capture and a hard timeout, mirroring
// the host-controlled command discipline of Runner.
func runGofmt(ctx context.Context, gofmtPath string, environment []string, worktree string, argv []string) ([]byte, error) {
	runContext, cancel := context.WithTimeout(ctx, formatNormalizeTimeout)
	defer cancel()
	command := exec.CommandContext(runContext, gofmtPath, argv...)
	command.Dir = worktree
	command.Env = environment
	configureProcess(command)
	stdout := newTailCapture(formatNormalizeLogLimit)
	stderr := newTailCapture(formatNormalizeLogLimit)
	command.Stdout, command.Stderr = stdout, stderr
	if err := command.Run(); err != nil {
		message := strings.TrimSpace(string(stderr.Bytes()))
		if len(message) > 2048 {
			message = message[len(message)-2048:]
		}
		return nil, fmt.Errorf("gofmt %s: %w: %s", strings.Join(argv, " "), err, message)
	}
	return stdout.Bytes(), nil
}

// parseGofmtList extracts the unformatted paths reported by gofmt -l,
// keeping only paths that were actually submitted to gofmt.
func parseGofmtList(output []byte, candidates map[string]bool) []string {
	var unformatted []string
	for _, line := range bytes.Split(output, []byte{'\n'}) {
		path := strings.TrimSpace(string(line))
		if path == "" || !candidates[path] {
			continue
		}
		unformatted = append(unformatted, path)
	}
	return unformatted
}

// formatNormalizeGate builds the transparent record of a successful
// normalization pass: an informational (non-required) pass gate whose
// evidence lists every normalized file and, in candidate mode, the head
// Candidate record reference (the normalizer Candidate when normalization
// changed bytes, otherwise the worker Candidate). Failures are instead
// reported by the caller as a required fail gate, keeping Verify fail-closed.
func formatNormalizeGate(normalized []string, candidateDigest string) Gate {
	gate := Gate{ID: "format:normalize", Category: "other", Required: false, Status: "pass", Summary: "无需 gofmt 归一化：所有变更 .go 文件均已合规", Evidence: []string{}}
	if len(normalized) > 0 {
		gate.Summary = fmt.Sprintf("gofmt 归一化 %d 个文件", len(normalized))
		for _, path := range normalized {
			gate.Evidence = append(gate.Evidence, "normalized:"+path)
		}
	}
	if candidateDigest != "" {
		gate.Evidence = append(gate.Evidence, "candidate:"+candidateDigest)
	}
	return gate
}
