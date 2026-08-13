package contract

import (
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/chiga0/marshal-harness/internal/adapter/denials"
	"github.com/chiga0/marshal-harness/internal/domain"
)

func validateSemantics(kind domain.Kind, data []byte) error {
	var violations []Violation
	var err error
	switch kind {
	case domain.KindTask:
		violations, err = validateTask(data)
	case domain.KindVerificationReport:
		violations, err = validateVerificationReport(data)
	case domain.KindReviewDecision:
		violations, err = validateReviewDecision(data)
	case domain.KindArtifactManifest:
		violations, err = validateArtifactManifest(data)
	default:
		return nil
	}
	if err != nil {
		return fmt.Errorf("decode %s semantic fields: %w", kind, err)
	}
	if len(violations) == 0 {
		return nil
	}
	slices.SortFunc(violations, func(left, right Violation) int {
		if result := strings.Compare(left.Path, right.Path); result != 0 {
			return result
		}
		return strings.Compare(left.Code, right.Code)
	})
	return &SemanticError{Violations: violations}
}

func validateTask(data []byte) ([]Violation, error) {
	var task struct {
		Repository struct {
			Path string `json:"path"`
		} `json:"repository"`
		Scope struct {
			AllowPaths []string `json:"allowPaths"`
			DenyPaths  []string `json:"denyPaths"`
		} `json:"scope"`
		Acceptance struct {
			Commands []struct {
				ID  string `json:"id"`
				CWD string `json:"cwd"`
			} `json:"commands"`
		} `json:"acceptance"`
		Deliverables []struct {
			ID       string `json:"id"`
			Kind     string `json:"kind"`
			Required bool   `json:"required"`
			PathGlob string `json:"pathGlob"`
		} `json:"deliverables"`
		Worker struct {
			PreferredAdapter string   `json:"preferredAdapter"`
			FallbackAdapters []string `json:"fallbackAdapters"`
			Tools            []string `json:"tools"`
		} `json:"worker"`
		Budgets struct {
			RunTimeoutSeconds     int `json:"runTimeoutSeconds"`
			AttemptTimeoutSeconds int `json:"attemptTimeoutSeconds"`
			MaxAttempts           int `json:"maxAttempts"`
			MaxOperationalRetries int `json:"maxOperationalRetries"`
			MaxReworkRounds       int `json:"maxReworkRounds"`
		} `json:"budgets"`
		Publication struct {
			Required bool `json:"required"`
		} `json:"publication"`
		Admission *struct {
			Status string `json:"status"`
		} `json:"admission"`
		DependsOn []struct {
			Kind          string `json:"kind"`
			RunID         string `json:"runId"`
			TaskID        string `json:"taskId"`
			RequiredState string `json:"requiredState"`
		} `json:"dependsOn"`
		Preconditions []struct {
			ID             string   `json:"id"`
			Argv           []string `json:"argv"`
			CWD            string   `json:"cwd"`
			TimeoutSeconds int64    `json:"timeoutSeconds"`
		} `json:"preconditions"`
	}
	if err := json.Unmarshal(data, &task); err != nil {
		return nil, err
	}

	var violations []Violation
	if !filepath.IsAbs(task.Repository.Path) {
		violations = append(violations, violation("/repository/path", "repository-path-not-absolute", "repository path must be absolute"))
	} else if filepath.Clean(task.Repository.Path) != task.Repository.Path {
		violations = append(violations, violation("/repository/path", "repository-path-not-clean", "repository path must be lexically clean"))
	}

	violations = append(violations, validatePatterns("/scope/allowPaths", task.Scope.AllowPaths)...)
	violations = append(violations, validatePatterns("/scope/denyPaths", task.Scope.DenyPaths)...)
	violations = append(violations, duplicateStringViolations("/acceptance/commands", commandIDs(task.Acceptance.Commands))...)
	for index, command := range task.Acceptance.Commands {
		if !validRelativePath(command.CWD, true) {
			violations = append(violations, violation(fmt.Sprintf("/acceptance/commands/%d/cwd", index), "invalid-relative-path", "command cwd must be a clean repository-relative path"))
		}
	}

	deliverableIDs := make([]string, 0, len(task.Deliverables))
	hasRequiredPublication := false
	for index, deliverable := range task.Deliverables {
		deliverableIDs = append(deliverableIDs, deliverable.ID)
		if deliverable.Kind == "publication" && deliverable.Required {
			hasRequiredPublication = true
		}
		if deliverable.PathGlob != "" && !validPattern(deliverable.PathGlob) {
			violations = append(violations, violation(fmt.Sprintf("/deliverables/%d/pathGlob", index), "invalid-path-pattern", "deliverable pathGlob must be a clean repository-relative pattern"))
		}
	}
	violations = append(violations, duplicateStringViolations("/deliverables", deliverableIDs)...)

	adapters := append([]string{task.Worker.PreferredAdapter}, task.Worker.FallbackAdapters...)
	violations = append(violations, duplicateStringViolations("/worker", adapters)...)
	// The TaskSpec Schema already enforces the closed worker.tools vocabulary
	// and uniqueItems; the semantic layer repeats both checks so direct
	// validateTask callers receive readable violations instead of silently
	// inheriting raw JSON Schema failures.
	for index, tool := range task.Worker.Tools {
		if !denials.IsAllowlistTool(tool) {
			violations = append(violations, violation(fmt.Sprintf("/worker/tools/%d", index), "unknown-worker-tool", fmt.Sprintf("worker.tools entry %q is outside the closed vocabulary read/edit/write/grep/find/ls/bash", tool)))
		}
	}
	violations = append(violations, duplicateStringViolations("/worker/tools", task.Worker.Tools)...)
	if task.Budgets.AttemptTimeoutSeconds > task.Budgets.RunTimeoutSeconds {
		violations = append(violations, violation("/budgets/attemptTimeoutSeconds", "attempt-timeout-exceeds-run", "attempt timeout must not exceed run timeout"))
	}
	minimumAttempts := 1 + task.Budgets.MaxOperationalRetries + task.Budgets.MaxReworkRounds
	if task.Budgets.MaxAttempts < minimumAttempts {
		violations = append(violations, violation("/budgets/maxAttempts", "attempt-budget-overcommitted", fmt.Sprintf("maxAttempts must be at least %d for the configured retry and rework budgets", minimumAttempts)))
	}
	if task.Publication.Required != hasRequiredPublication {
		violations = append(violations, violation("/publication/required", "publication-deliverable-mismatch", "publication.required must match a required publication deliverable"))
	}

	// admission.status is a closed vocabulary; the TaskSpec Schema enforces
	// it, and the semantic layer repeats the check so direct validateTask
	// callers receive readable violations.
	if task.Admission != nil && task.Admission.Status != domain.AdmissionStatusPrepared && task.Admission.Status != domain.AdmissionStatusExecutable {
		violations = append(violations, violation("/admission/status", "unknown-admission-status", fmt.Sprintf("admission.status %q is outside the closed vocabulary prepared/executable", task.Admission.Status)))
	}

	// dependsOn: closed kind vocabulary, per-kind reference presence, closed
	// terminal requiredState vocabulary and (kind, reference) de-duplication.
	dependencyKeys := make([]string, 0, len(task.DependsOn))
	for index, dependency := range task.DependsOn {
		reference := dependency.RunID
		switch dependency.Kind {
		case domain.DependencyKindRun:
			if dependency.RunID == "" {
				violations = append(violations, violation(fmt.Sprintf("/dependsOn/%d/runId", index), "missing-dependency-reference", "run-scoped dependsOn entries require runId"))
			}
		case domain.DependencyKindTask:
			reference = dependency.TaskID
			if dependency.TaskID == "" {
				violations = append(violations, violation(fmt.Sprintf("/dependsOn/%d/taskId", index), "missing-dependency-reference", "task-scoped dependsOn entries require taskId"))
			}
		default:
			violations = append(violations, violation(fmt.Sprintf("/dependsOn/%d/kind", index), "unknown-dependency-kind", fmt.Sprintf("dependsOn kind %q is outside the closed vocabulary run/task", dependency.Kind)))
		}
		if requiredState, err := domain.ParseState(dependency.RequiredState); err != nil {
			violations = append(violations, violation(fmt.Sprintf("/dependsOn/%d/requiredState", index), "unknown-dependency-state", fmt.Sprintf("dependsOn requiredState %q is not a lifecycle state", dependency.RequiredState)))
		} else if !requiredState.Terminal() {
			violations = append(violations, violation(fmt.Sprintf("/dependsOn/%d/requiredState", index), "non-terminal-dependency-state", fmt.Sprintf("dependsOn requiredState %q is not one of the five terminal states", dependency.RequiredState)))
		}
		dependencyKeys = append(dependencyKeys, dependency.Kind+"/"+reference)
	}
	violations = append(violations, duplicateStringViolations("/dependsOn", dependencyKeys)...)

	// preconditions: id de-duplication, non-empty argv, clean relative cwd
	// and a positive timeout when declared; required is always true by shape.
	preconditionIDs := make([]string, 0, len(task.Preconditions))
	for index, precondition := range task.Preconditions {
		preconditionIDs = append(preconditionIDs, precondition.ID)
		if len(precondition.Argv) == 0 {
			violations = append(violations, violation(fmt.Sprintf("/preconditions/%d/argv", index), "empty-argv", "precondition argv must not be empty"))
		}
		if precondition.CWD != "" && !validRelativePath(precondition.CWD, true) {
			violations = append(violations, violation(fmt.Sprintf("/preconditions/%d/cwd", index), "invalid-relative-path", "precondition cwd must be a clean repository-relative path"))
		}
		if precondition.TimeoutSeconds < 0 {
			violations = append(violations, violation(fmt.Sprintf("/preconditions/%d/timeoutSeconds", index), "invalid-timeout", "precondition timeoutSeconds must be positive when declared"))
		}
	}
	violations = append(violations, duplicateStringViolations("/preconditions", preconditionIDs)...)
	return violations, nil
}

func validateVerificationReport(data []byte) ([]Violation, error) {
	var report struct {
		Observed struct {
			ChangedFiles     []string `json:"changedFiles"`
			ChangedFileCount int      `json:"changedFileCount"`
		} `json:"observed"`
		Status string `json:"status"`
		Gates  []struct {
			ID       string `json:"id"`
			Required bool   `json:"required"`
			Status   string `json:"status"`
		} `json:"gates"`
		StartedAt   time.Time `json:"startedAt"`
		CompletedAt time.Time `json:"completedAt"`
	}
	if err := json.Unmarshal(data, &report); err != nil {
		return nil, err
	}

	var violations []Violation
	gateIDs := make([]string, 0, len(report.Gates))
	allRequiredPassed := true
	for _, gate := range report.Gates {
		gateIDs = append(gateIDs, gate.ID)
		if gate.Required && gate.Status != "pass" {
			allRequiredPassed = false
		}
	}
	violations = append(violations, duplicateStringViolations("/gates", gateIDs)...)
	if (report.Status == "pass") != allRequiredPassed {
		violations = append(violations, violation("/status", "verification-status-inconsistent", "status is pass if and only if every required gate passes"))
	}
	if report.Observed.ChangedFileCount != len(report.Observed.ChangedFiles) {
		violations = append(violations, violation("/observed/changedFileCount", "changed-file-count-mismatch", "changedFileCount must equal the number of changedFiles"))
	}
	for index, changedPath := range report.Observed.ChangedFiles {
		if !validRelativePath(changedPath, false) {
			violations = append(violations, violation(fmt.Sprintf("/observed/changedFiles/%d", index), "invalid-relative-path", "changed file must be a clean repository-relative path"))
		}
	}
	if report.CompletedAt.Before(report.StartedAt) {
		violations = append(violations, violation("/completedAt", "completion-before-start", "completedAt must not precede startedAt"))
	}
	return violations, nil
}

func validateReviewDecision(data []byte) ([]Violation, error) {
	type finding struct {
		ID string `json:"id"`
	}
	var decision struct {
		Verdict             string    `json:"verdict"`
		BlockingFindings    []finding `json:"blockingFindings"`
		NonBlockingFindings []finding `json:"nonBlockingFindings"`
		BlockerOwner        string    `json:"blockerOwner"`
	}
	if err := json.Unmarshal(data, &decision); err != nil {
		return nil, err
	}

	findings := make([]string, 0, len(decision.BlockingFindings)+len(decision.NonBlockingFindings))
	for _, finding := range decision.BlockingFindings {
		findings = append(findings, finding.ID)
	}
	for _, finding := range decision.NonBlockingFindings {
		findings = append(findings, finding.ID)
	}
	violations := duplicateStringViolations("/findings", findings)
	if (decision.Verdict == "accept" || decision.Verdict == "no_change") && len(decision.BlockingFindings) > 0 {
		violations = append(violations, violation("/blockingFindings", "accepted-with-blocking-findings", "accept and no_change decisions cannot retain blocking findings"))
	}
	if decision.Verdict == "blocked" && decision.BlockerOwner == "" {
		violations = append(violations, violation("/blockerOwner", "blocked-without-owner", "blocked decisions require blockerOwner"))
	}
	return violations, nil
}

func validateArtifactManifest(data []byte) ([]Violation, error) {
	var manifest struct {
		Artifacts []struct {
			ID           string `json:"id"`
			PathRoot     string `json:"pathRoot"`
			RelativePath string `json:"relativePath"`
		} `json:"artifacts"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, err
	}

	ids := make([]string, 0, len(manifest.Artifacts))
	var violations []Violation
	for index, artifact := range manifest.Artifacts {
		ids = append(ids, artifact.ID)
		if artifact.RelativePath == "" {
			continue
		}
		if !validRelativePath(artifact.RelativePath, false) {
			violations = append(violations, violation(fmt.Sprintf("/artifacts/%d/relativePath", index), "invalid-relative-path", "artifact path must be clean and relative to its declared root"))
		}
		if artifact.PathRoot == "repository" && (artifact.RelativePath == ".marshal" || strings.HasPrefix(artifact.RelativePath, ".marshal/")) {
			violations = append(violations, violation(fmt.Sprintf("/artifacts/%d/relativePath", index), "marshal-state-as-source", ".marshal content cannot be a repository source artifact"))
		}
	}
	violations = append(violations, duplicateStringViolations("/artifacts", ids)...)
	return violations, nil
}

func validatePatterns(base string, patterns []string) []Violation {
	violations := duplicateStringViolations(base, patterns)
	for index, pattern := range patterns {
		if !validPattern(pattern) {
			violations = append(violations, violation(fmt.Sprintf("%s/%d", base, index), "invalid-path-pattern", "path pattern must be clean and repository-relative"))
		}
	}
	return violations
}

func validPattern(pattern string) bool {
	if !validRelativePath(pattern, false) {
		return false
	}
	_, err := doublestar.Match(pattern, "probe")
	return err == nil
}

func validRelativePath(value string, allowDot bool) bool {
	if value == "" || strings.Contains(value, "\\") || strings.HasPrefix(value, "/") {
		return false
	}
	if !allowDot && value == "." {
		return false
	}
	cleaned := path.Clean(value)
	return cleaned == value && cleaned != ".." && !strings.HasPrefix(cleaned, "../")
}

func duplicateStringViolations(base string, values []string) []Violation {
	seen := make(map[string]struct{}, len(values))
	var violations []Violation
	for index, value := range values {
		if _, exists := seen[value]; exists {
			violations = append(violations, violation(fmt.Sprintf("%s/%d", base, index), "duplicate-id", fmt.Sprintf("%q is duplicated", value)))
			continue
		}
		seen[value] = struct{}{}
	}
	return violations
}

func commandIDs(commands []struct {
	ID  string `json:"id"`
	CWD string `json:"cwd"`
}) []string {
	ids := make([]string, 0, len(commands))
	for _, command := range commands {
		ids = append(ids, command.ID)
	}
	return ids
}

func violation(pointer, code, message string) Violation {
	return Violation{Path: pointer, Code: code, Message: message}
}
