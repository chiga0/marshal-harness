package verification

import (
	"fmt"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

func EvaluateScope(observation Observation, policy ScopePolicy) Gate {
	gate := Gate{ID: "scope:changed-paths", Category: "scope", Required: true, Status: "pass", Summary: "所有变更均满足冻结范围策略", Evidence: []string{"artifact://observed.patch"}}
	var violations []string
	if policy.MaxChangedFiles > 0 && observation.ChangedFileCount > policy.MaxChangedFiles {
		violations = append(violations, fmt.Sprintf("变更文件数 %d 超过上限 %d", observation.ChangedFileCount, policy.MaxChangedFiles))
	}
	if policy.MaxDiffBytes > 0 && observation.DiffBytes > policy.MaxDiffBytes {
		violations = append(violations, fmt.Sprintf("Diff 字节数 %d 超过上限 %d", observation.DiffBytes, policy.MaxDiffBytes))
	}
	for _, change := range observation.Changes {
		if change.Submodule && !policy.AllowSubmodules {
			violations = append(violations, "禁止 Submodule 变更: "+change.Path)
		}
		if change.SymlinkEscapes {
			violations = append(violations, "Symlink 目标逃逸 Worktree: "+change.Path)
		}
		for _, path := range []string{change.OldPath, change.Path} {
			if path == "" {
				continue
			}
			if strings.HasPrefix(path, ".marshal/") || path == ".marshal" {
				violations = append(violations, "Marshal 状态目录不能成为业务变更: "+path)
				continue
			}
			allowed, err := matchesAny(policy.AllowPaths, path)
			if err != nil {
				return Gate{ID: gate.ID, Category: gate.Category, Required: true, Status: "error", Summary: err.Error(), Evidence: gate.Evidence}
			}
			denied, err := matchesAny(policy.DenyPaths, path)
			if err != nil {
				return Gate{ID: gate.ID, Category: gate.Category, Required: true, Status: "error", Summary: err.Error(), Evidence: gate.Evidence}
			}
			if !allowed || denied {
				violations = append(violations, "路径超出冻结范围: "+path)
			}
		}
	}
	if observation.DiffTruncated {
		violations = append(violations, "Observed Patch 超过安全采集上限")
	}
	if len(violations) > 0 {
		gate.Status = "fail"
		gate.Summary = strings.Join(violations, "; ")
	}
	return gate
}

func matchesAny(patterns []string, path string) (bool, error) {
	for _, pattern := range patterns {
		matched, err := doublestar.Match(pattern, path)
		if err != nil {
			return false, fmt.Errorf("invalid path glob %q: %w", pattern, err)
		}
		if matched {
			return true, nil
		}
	}
	return false, nil
}
