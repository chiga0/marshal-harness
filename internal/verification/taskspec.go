package verification

import (
	"time"

	"github.com/chiga0/marshal-harness/internal/domain"
)

// ToolAllowlistFromTask extracts the declared worker.tools allowlist from
// the frozen TaskSpec. An empty result means the Run keeps its execution
// profile defaults and the tool-allowlist gate stays skipped. It is the
// allowlist input the tool-allowlist gate reconciles against; the returned
// slice never aliases the TaskSpec storage.
func ToolAllowlistFromTask(task domain.TaskSpec) []string {
	return append([]string(nil), task.Worker.Tools...)
}

func PolicyFromTask(task domain.TaskSpec) (ScopePolicy, []Deliverable, []CommandSpec) {
	scope := ScopePolicy{AllowPaths: append([]string(nil), task.Scope.AllowPaths...), DenyPaths: append([]string(nil), task.Scope.DenyPaths...), AllowSubmodules: task.Scope.AllowSubmodules, MaxChangedFiles: task.Scope.MaxChangedFiles, MaxDiffBytes: task.Scope.MaxDiffBytes}
	deliverables := make([]Deliverable, 0, len(task.Deliverables))
	for _, item := range task.Deliverables {
		deliverables = append(deliverables, Deliverable{ID: item.ID, Kind: item.Kind, Required: item.Required, PathGlob: item.PathGlob, MediaType: item.MediaType, MinimumCount: item.MinimumCount, Description: item.Description})
	}
	commands := make([]CommandSpec, 0, len(task.Acceptance.Commands))
	for _, item := range task.Acceptance.Commands {
		commands = append(commands, CommandSpec{ID: item.ID, Argv: append([]string(nil), item.Argv...), CWD: item.CWD, Timeout: time.Duration(item.TimeoutSeconds) * time.Second, Required: item.Required, MaxLogBytes: item.MaxLogBytes, BaselinePolicy: item.BaselinePolicy})
	}
	return scope, deliverables, commands
}
