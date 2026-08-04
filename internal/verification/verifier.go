package verification

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/gitworktree"
)

type Input struct {
	TaskID              string
	RunID               string
	SpecDigest          string
	BaseSHA             string
	Worktree            string
	ExpectedCommonDir   string
	RunDirectory        string
	Scope               ScopePolicy
	Deliverables        []Deliverable
	Commands            []CommandSpec
	BaselinePath        string
	Environment         []string
	PatchCaptureBytes   int64
	WorkerDeclaredPaths []string // diagnostic only; never authorizes scope
}

type Result struct {
	Report   Report
	Manifest ArtifactManifest
}

type Verifier struct {
	now func() time.Time
}

func New() *Verifier { return &Verifier{now: time.Now} }

func (v *Verifier) Verify(ctx context.Context, input Input) (Result, error) {
	started := v.now().UTC()
	result := Result{Report: Report{APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindVerificationReport, TaskID: input.TaskID, RunID: input.RunID, SpecDigest: input.SpecDigest, BaseSHA: input.BaseSHA, Observed: emptyObservation(), StartedAt: started}, Manifest: ArtifactManifest{APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindArtifactManifest, TaskID: input.TaskID, RunID: input.RunID, GeneratedAt: started}}
	if err := validateInput(input); err != nil {
		return result, err
	}
	if err := os.MkdirAll(input.RunDirectory, 0o700); err != nil {
		return result, err
	}
	repositoryGate := verifyRepository(ctx, input)
	result.Report.Gates = append(result.Report.Gates, repositoryGate)
	if repositoryGate.Status == "error" || repositoryGate.Status == "fail" {
		return v.finish(input, result)
	}
	observation, err := ObserveContext(ctx, input.Worktree, input.BaseSHA, input.PatchCaptureBytes)
	if err != nil {
		result.Report.Gates = append(result.Report.Gates, Gate{ID: "diff:observe", Category: "diff", Required: true, Status: "error", Summary: err.Error(), Evidence: []string{}})
		return v.finish(input, result)
	}
	result.Report.Observed = observation
	patchPath := filepath.Join(input.RunDirectory, "observed.patch")
	if err := atomicWrite(patchPath, observation.Patch); err != nil {
		return result, err
	}
	result.Manifest.Artifacts = append(result.Manifest.Artifacts, Artifact{ID: "evidence:observed-patch", Kind: "patch", MediaType: "text/x-diff", Producer: "verifier", Required: true, Status: "validated", PathRoot: "run", RelativePath: "observed.patch", ByteSize: int64(len(observation.Patch)), Digest: canonical.DigestBytes(observation.Patch), CreatedAt: started, Truncated: observation.DiffTruncated, RelatedGates: []string{"diff:observe", "scope:changed-paths"}})
	result.Report.Gates = append(result.Report.Gates, Gate{ID: "diff:observe", Category: "diff", Required: true, Status: "pass", Summary: fmt.Sprintf("观察到 %d 个变更，%d 字节", observation.ChangedFileCount, observation.DiffBytes), Evidence: []string{"artifact://evidence:observed-patch"}})
	result.Report.Gates = append(result.Report.Gates, EvaluateScope(observation, input.Scope))
	artifacts, artifactGates := CollectArtifacts(input.Worktree, input.Deliverables, started)
	result.Manifest.Artifacts = append(result.Manifest.Artifacts, artifacts...)
	result.Report.Gates = append(result.Report.Gates, artifactGates...)
	runner := Runner{Environment: input.Environment}
	for _, spec := range input.Commands {
		if ctx.Err() != nil {
			result.Report.Gates = append(result.Report.Gates, Gate{ID: "command:" + spec.ID, Category: "command", Required: spec.Required, Status: "cancelled", Summary: "验证已取消", Evidence: []string{}})
			break
		}
		commandResult := runner.Run(ctx, input.Worktree, spec)
		baselineRequested := spec.BaselinePolicy == "always" || (spec.BaselinePolicy == "on-failure" && commandResult.Status != "pass")
		baselineMissing := baselineRequested && input.BaselinePath == ""
		if input.BaselinePath != "" && baselineRequested {
			baseline := runner.Run(ctx, input.BaselinePath, spec)
			if baseline.Status == "cancelled" {
				commandResult.Record.BaselineStatus = "error"
			} else {
				commandResult.Record.BaselineStatus = baseline.Status
			}
		}
		stdoutID, stderrID, persistErr := persistCommandLogs(input.RunDirectory, spec.ID, commandResult, &result.Manifest, started)
		if persistErr != nil {
			return result, persistErr
		}
		gate := Gate{ID: "command:" + spec.ID, Category: "command", Required: spec.Required, Status: commandResult.Status, Summary: commandSummary(commandResult), Evidence: []string{"artifact://" + stdoutID, "artifact://" + stderrID}, Command: &commandResult.Record}
		if baselineMissing {
			gate.Status, gate.Summary = "error", "命令请求 Baseline Diagnostic，但没有提供干净 Base Worktree"
			commandResult.Record.BaselineStatus = "error"
		}
		after, observeErr := ObserveContext(ctx, input.Worktree, input.BaseSHA, input.PatchCaptureBytes)
		if observeErr != nil {
			gate.Status, gate.Summary = "error", "命令后无法重新观察 worktree: "+observeErr.Error()
		} else if after.SnapshotDigest != observation.SnapshotDigest || after.DiffDigest != observation.DiffDigest {
			gate.Status = "fail"
			gate.Summary = "Verifier 命令产生了未声明的 worktree 变更"
		}
		result.Report.Gates = append(result.Report.Gates, gate)
	}
	return v.finish(input, result)
}

func emptyObservation() Observation {
	emptyDigest := canonical.DigestBytes(nil)
	return Observation{SnapshotDigest: emptyDigest, DiffDigest: emptyDigest, ChangedFiles: []string{}, Changes: []Change{}}
}

func (v *Verifier) finish(input Input, result Result) (Result, error) {
	result.Report.CompletedAt = v.now().UTC()
	result.Manifest.GeneratedAt = result.Report.CompletedAt
	result.Report.Status = overallStatus(result.Report.Gates)
	result.Report.Summary = fmt.Sprintf("Verification %s：%d 个 Gate", result.Report.Status, len(result.Report.Gates))
	reportData, err := json.MarshalIndent(result.Report, "", "  ")
	if err != nil {
		return result, err
	}
	manifestData, err := json.MarshalIndent(result.Manifest, "", "  ")
	if err != nil {
		return result, err
	}
	validator, err := contract.NewValidator()
	if err != nil {
		return result, err
	}
	if err := validator.Validate(domain.KindVerificationReport, reportData); err != nil {
		return result, fmt.Errorf("generated verification report violates contract: %w", err)
	}
	if err := validator.Validate(domain.KindArtifactManifest, manifestData); err != nil {
		return result, fmt.Errorf("generated artifact manifest violates contract: %w", err)
	}
	if err := atomicWrite(filepath.Join(input.RunDirectory, "verification-report.json"), append(reportData, '\n')); err != nil {
		return result, err
	}
	if err := atomicWrite(filepath.Join(input.RunDirectory, "artifact-manifest.json"), append(manifestData, '\n')); err != nil {
		return result, err
	}
	return result, nil
}

func validateInput(input Input) error {
	for _, value := range []string{input.TaskID, input.RunID} {
		if err := domain.ValidateID(value); err != nil {
			return err
		}
	}
	if !strings.HasPrefix(input.SpecDigest, "sha256:") || len(input.SpecDigest) != 71 {
		return errors.New("invalid spec digest")
	}
	if input.RunDirectory == "" || input.Worktree == "" {
		return errors.New("worktree and run directory are required")
	}
	return nil
}

func verifyRepository(ctx context.Context, input Input) Gate {
	gate := Gate{ID: "repository:integrity", Category: "repository", Required: true, Status: "pass", Summary: "Worktree 与锁定基线完整", Evidence: []string{}}
	repository, err := gitworktree.OpenContext(ctx, input.Worktree)
	if err != nil {
		gate.Status, gate.Summary = "error", err.Error()
		return gate
	}
	if input.ExpectedCommonDir != "" && repository.CommonDir != input.ExpectedCommonDir {
		gate.Status, gate.Summary = "fail", "Worktree 不属于预期 Git Common Directory"
		return gate
	}
	if _, err := repository.ResolveBaseContext(ctx, input.BaseSHA); err != nil {
		gate.Status, gate.Summary = "fail", err.Error()
		return gate
	}
	if _, err := gitBytesContext(ctx, input.Worktree, "merge-base", "--is-ancestor", input.BaseSHA, "HEAD"); err != nil {
		gate.Status, gate.Summary = "fail", "锁定 Base 不是当前 HEAD 的祖先"
		return gate
	}
	conflicts, err := gitBytesContext(ctx, input.Worktree, "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		gate.Status, gate.Summary = "error", err.Error()
		return gate
	}
	if len(conflicts) > 0 {
		gate.Status, gate.Summary = "fail", "Worktree 存在未解决冲突"
		return gate
	}
	for _, marker := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-merge", "rebase-apply"} {
		path, pathErr := gitBytesContext(ctx, input.Worktree, "rev-parse", "--git-path", marker)
		if pathErr != nil {
			gate.Status, gate.Summary = "error", pathErr.Error()
			return gate
		}
		if !filepath.IsAbs(string(path)) {
			path = []byte(filepath.Join(input.Worktree, string(path)))
		}
		if _, statErr := os.Stat(string(path)); statErr == nil {
			gate.Status, gate.Summary = "fail", "Worktree 存在未完成的 Git 操作: "+marker
			return gate
		} else if !errors.Is(statErr, os.ErrNotExist) {
			gate.Status, gate.Summary = "error", statErr.Error()
			return gate
		}
	}
	return gate
}

func persistCommandLogs(runDirectory, id string, result CommandResult, manifest *ArtifactManifest, now time.Time) (string, string, error) {
	if err := domain.ValidateID(id); err != nil {
		return "", "", err
	}
	directory := filepath.Join(runDirectory, "logs")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", "", err
	}
	stdoutPath, stderrPath := filepath.Join(directory, id+".stdout.log"), filepath.Join(directory, id+".stderr.log")
	if err := atomicWrite(stdoutPath, result.Stdout); err != nil {
		return "", "", err
	}
	if err := atomicWrite(stderrPath, result.Stderr); err != nil {
		return "", "", err
	}
	stdoutID, stderrID := "log:"+id+":stdout", "log:"+id+":stderr"
	manifest.Artifacts = append(manifest.Artifacts,
		Artifact{ID: stdoutID, Kind: "command-log", MediaType: "text/plain", Producer: "verifier", Status: "validated", PathRoot: "run", RelativePath: filepath.ToSlash(filepath.Join("logs", id+".stdout.log")), ByteSize: int64(len(result.Stdout)), Digest: canonical.DigestBytes(result.Stdout), CreatedAt: now, Truncated: result.StdoutTruncated, RelatedGates: []string{"command:" + id}},
		Artifact{ID: stderrID, Kind: "command-log", MediaType: "text/plain", Producer: "verifier", Status: "validated", PathRoot: "run", RelativePath: filepath.ToSlash(filepath.Join("logs", id+".stderr.log")), ByteSize: int64(len(result.Stderr)), Digest: canonical.DigestBytes(result.Stderr), CreatedAt: now, Truncated: result.StderrTruncated, RelatedGates: []string{"command:" + id}},
	)
	return stdoutID, stderrID, nil
}

func commandSummary(result CommandResult) string {
	if result.Status == "pass" {
		return fmt.Sprintf("命令通过，耗时 %dms", result.Record.DurationMilliseconds)
	}
	return fmt.Sprintf("命令状态 %s，退出码 %v", result.Status, result.Record.ExitCode)
}

func overallStatus(gates []Gate) string {
	status := "pass"
	for _, gate := range gates {
		if !gate.Required {
			continue
		}
		switch gate.Status {
		case "cancelled":
			return "cancelled"
		case "error":
			status = "error"
		case "fail", "skipped":
			if status == "pass" {
				status = "fail"
			}
		}
	}
	return status
}

func atomicWrite(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".marshal-*.tmp")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
