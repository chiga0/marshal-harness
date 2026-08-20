// Command plan_premortem_core_probe exposes the existing Marshal planning,
// adapter selection and capability contracts to the operator-local plan
// pre-mortem. It probes adapter capability only; it never calls Worker.Run or
// writes Core state.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/adapter"
	"github.com/chiga0/marshal-harness/internal/adapter/denials"
	"github.com/chiga0/marshal-harness/internal/app"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/planning"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

const maxInputBytes = 2 << 20

var sourceHeadPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

type fileBinding struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
}

type manifest struct {
	APIVersion      string      `json:"apiVersion"`
	Kind            string      `json:"kind"`
	RunID           string      `json:"runId"`
	SelectedAdapter string      `json:"selectedAdapter"`
	SourceHead      string      `json:"sourceHead"`
	TaskSpec        fileBinding `json:"taskSpec"`
	PolicySnapshot  fileBinding `json:"policySnapshot"`
}

type result struct {
	Status               string `json:"status"`
	ReasonCode           string `json:"reasonCode"`
	TaskSpecDigest       string `json:"taskSpecDigest,omitempty"`
	PolicySnapshotDigest string `json:"policySnapshotDigest,omitempty"`
	SourceHead           string `json:"sourceHead,omitempty"`
	SelectedAdapter      string `json:"selectedAdapter,omitempty"`
	AuthorityMode        string `json:"authorityMode,omitempty"`
	CapabilityDigest     string `json:"capabilityDigest,omitempty"`
}

func main() {
	var manifestPath, taskPath, policyPath, schemaPath string
	flag.StringVar(&manifestPath, "manifest", "", "operator manifest")
	flag.StringVar(&taskPath, "task-spec", "", "held TaskSpec copy")
	flag.StringVar(&policyPath, "policy-snapshot", "", "held PolicySnapshot copy")
	flag.StringVar(&schemaPath, "schema", "", "operator manifest schema")
	flag.Parse()
	if flag.NArg() != 0 || manifestPath == "" || taskPath == "" || policyPath == "" || schemaPath == "" {
		emitFail("core-probe-usage-invalid")
	}

	manifestRaw, err := readBounded(manifestPath)
	if err != nil || validateOperatorSchema(schemaPath, manifestRaw) != nil {
		emitFail("manifest-schema-invalid")
	}
	var input manifest
	if json.Unmarshal(manifestRaw, &input) != nil {
		emitFail("manifest-schema-invalid")
	}
	taskRaw, err := readBounded(taskPath)
	if err != nil {
		emitFail("task-spec-contract-invalid")
	}
	policyRaw, err := readBounded(policyPath)
	if err != nil {
		emitFail("policy-snapshot-contract-invalid")
	}
	if digestBytes(taskRaw) != input.TaskSpec.Digest || digestBytes(policyRaw) != input.PolicySnapshot.Digest {
		emitFail("input-digest-mismatch")
	}
	if !sourceHeadPattern.MatchString(input.SourceHead) {
		emitFail("source-head-invalid")
	}

	validator, err := contract.NewValidator()
	if err != nil {
		emitFail("core-contract-unavailable")
	}
	if validator.Validate(domain.KindTask, taskRaw) != nil {
		emitFail("task-spec-contract-invalid")
	}
	var task domain.TaskSpec
	if json.Unmarshal(taskRaw, &task) != nil {
		emitFail("task-spec-contract-invalid")
	}
	if task.Repository.BaseRef != input.SourceHead {
		emitFail("source-head-mismatch")
	}
	if err := contract.ValidateTaskSpecAcceptanceFloor(task); err != nil {
		emitFail("acceptance-required-command-missing")
	}
	if validator.Validate(domain.KindPolicySnapshot, policyRaw) != nil {
		emitFail("policy-snapshot-contract-invalid")
	}

	effective, err := planning.ValidatePolicy(policyRaw, task, input.RunID, validator)
	if err != nil {
		emitFail(policyReason(err))
	}
	if !eligibleSelectedAdapter(input.SelectedAdapter, effective) {
		emitFail("selected-adapter-policy-mismatch")
	}
	if input.SelectedAdapter == "qoder" || input.SelectedAdapter == "codex" {
		if _, err := denials.ProjectDeclaredWorkerTools(taskRaw, false); err != nil {
			if errors.Is(err, denials.ErrNamedWorkerToolsUnsupported) {
				emitFail("adapter-named-worker-tools-unsupported")
			}
			emitFail("task-spec-contract-invalid")
		}
	}
	if input.SelectedAdapter == "qoder" {
		if reason := qoderDeliverableParents(task); reason != "" {
			emitFail(reason)
		}
	}

	runtime, err := app.NewWorkerRuntime(os.Getenv)
	if err != nil {
		emitFail("adapter-runtime-unavailable")
	}
	if reason := configurationReason(input.SelectedAdapter, runtime.Configurations()); reason != "" {
		emitFail(reason)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	selection, err := runtime.Selector().Select(ctx, effective.SelectionRequest())
	if err != nil {
		emitFail("adapter-selection-failed")
	}
	if selection.Adapter == nil || selection.Adapter.ID() != input.SelectedAdapter {
		emitFail("selected-adapter-mismatch")
	}
	if validator.Validate(domain.KindCapabilitySnapshot, selection.Capability.Data) != nil {
		emitFail("adapter-capability-contract-invalid")
	}
	if _, err := adapter.ValidateCapability(selection.Capability, task); err != nil {
		if strings.Contains(err.Error(), adapter.ErrCapabilityExecutionProfile) && ordinaryUser(selection.Capability.Data) {
			emitFail("adapter-ordinary-user-execution-profile-unsupported")
		}
		if strings.Contains(err.Error(), adapter.ErrCapabilityExecutionProfile) {
			emitFail("adapter-execution-profile-unsupported")
		}
		emitFail("adapter-capability-incompatible")
	}
	capabilityDigest, err := canonical.DigestJSON(selection.Capability.Data)
	if err != nil {
		emitFail("adapter-capability-contract-invalid")
	}
	authorityMode := capabilityAuthorityMode(selection.Capability.Data)
	emit(result{
		Status: "pass", ReasonCode: "plan-premortem-pass",
		TaskSpecDigest: input.TaskSpec.Digest, PolicySnapshotDigest: input.PolicySnapshot.Digest,
		SourceHead: input.SourceHead, SelectedAdapter: input.SelectedAdapter,
		AuthorityMode: authorityMode, CapabilityDigest: capabilityDigest,
	}, 0)
}

func readBounded(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > maxInputBytes {
		return nil, errors.New("invalid input file")
	}
	data := make([]byte, info.Size())
	if _, err := io.ReadFull(file, data); err != nil {
		return nil, err
	}
	return data, nil
}

func validateOperatorSchema(schemaPath string, instance []byte) error {
	absolute, err := filepath.Abs(schemaPath)
	if err != nil {
		return err
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.AssertFormat()
	schema, err := compiler.Compile("file://" + filepath.ToSlash(absolute))
	if err != nil {
		return err
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(instance))
	if err != nil {
		return err
	}
	return schema.Validate(document)
}

func policyReason(err error) string {
	message := err.Error()
	if strings.Contains(message, planning.ErrPolicyAcceptanceFloorEmpty) ||
		strings.Contains(message, planning.ErrPolicyAcceptanceFloorNoRequired) ||
		strings.Contains(message, planning.ErrPolicyAcceptanceFloorArgv) {
		return "acceptance-required-command-missing"
	}
	if strings.Contains(message, planning.ErrPolicyControlGates) {
		return "policy-approval-gates-conflict"
	}
	for _, sentinel := range []string{
		planning.ErrPolicyPublication, planning.ErrPolicyMerge, planning.ErrPolicyMergeNotAllowed,
		planning.ErrPolicyMergeProvider, planning.ErrPolicyMergeMethod, planning.ErrPolicyMergeChecks,
	} {
		if strings.Contains(message, sentinel) {
			return "policy-publication-merge-conflict"
		}
	}
	return "policy-task-conflict"
}

func eligibleSelectedAdapter(selected string, policy planning.EffectivePolicy) bool {
	candidates := append([]string{policy.PreferredAdapter}, policy.FallbackAdapters...)
	return slices.Contains(candidates, selected) && slices.Contains(policy.AllowedAdapters, selected)
}

func configurationReason(selected string, configurations []app.WorkerConfiguration) string {
	for _, configuration := range configurations {
		if configuration.AdapterID != selected {
			continue
		}
		if !configuration.Configured {
			return "adapter-not-configured"
		}
		if !configuration.Registered {
			return "adapter-configuration-invalid"
		}
		return ""
	}
	return "adapter-not-registered"
}

func ordinaryUser(data []byte) bool {
	return capabilityAuthorityMode(data) == "ordinary-user"
}

func capabilityAuthorityMode(data []byte) string {
	var value struct {
		AuthorityMode string `json:"authorityMode"`
	}
	_ = json.Unmarshal(data, &value)
	return value.AuthorityMode
}

func qoderDeliverableParents(task domain.TaskSpec) string {
	if !sourceHeadPattern.MatchString(task.Repository.BaseRef) {
		return "source-head-invalid"
	}
	repository, err := filepath.EvalSymlinks(task.Repository.Path)
	if err != nil || !filepath.IsAbs(repository) || filepath.Clean(repository) != repository {
		return "repository-identity-invalid"
	}
	if output, err := git(repository, "rev-parse", "--show-toplevel"); err != nil || strings.TrimSpace(output) != repository {
		return "repository-identity-invalid"
	}
	if _, err := git(repository, "cat-file", "-e", task.Repository.BaseRef+"^{commit}"); err != nil {
		return "source-head-unavailable"
	}
	for _, deliverable := range task.Deliverables {
		if !deliverable.Required || deliverable.PathGlob == "" {
			continue
		}
		parent := filepath.ToSlash(filepath.Dir(deliverable.PathGlob))
		if parent == "." {
			continue
		}
		if strings.ContainsAny(parent, "*?[") {
			return "qoder-deliverable-parent-indeterminate"
		}
		kind, err := git(repository, "cat-file", "-t", task.Repository.BaseRef+":"+parent)
		if err != nil || strings.TrimSpace(kind) != "tree" {
			return "qoder-deliverable-parent-missing"
		}
	}
	return ""
}

func git(repository string, arguments ...string) (string, error) {
	command := exec.Command("/usr/bin/git", append([]string{"-c", "core.fsmonitor=false", "-c", "gc.auto=0"}, arguments...)...)
	command.Dir = repository
	command.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C", "GIT_OPTIONAL_LOCKS=0"}
	command.Stdin = nil
	output, err := command.Output()
	return string(output), err
}

func digestBytes(data []byte) string {
	return canonicalDigestPrefix + fmt.Sprintf("%x", sha256Sum(data))
}

const canonicalDigestPrefix = "sha256:"

func sha256Sum(data []byte) [32]byte {
	// Kept behind this helper so every raw input binding uses the same exact
	// representation while Core-owned records continue to use canonical.JSON.
	return sha256.Sum256(data)
}

func emitFail(reason string) {
	emit(result{Status: "fail", ReasonCode: reason}, 1)
}

func emit(value result, code int) {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	if encoder.Encode(value) != nil {
		os.Exit(2)
	}
	os.Exit(code)
}
