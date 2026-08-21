// Package planpremortem contains the deterministic, read-only plan pre-mortem
// used by both the fixed Marshal internal command and its operator tests.
// It never starts a Worker and never writes Core state.
package planpremortem

import (
	"bytes"
	"context"
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
	"github.com/chiga0/marshal-harness/internal/buildinfo"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/planning"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	maxInputBytes  = 2 << 20
	commandVersion = "plan-premortem-check/v1"
)

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
	Status               string           `json:"status"`
	ReasonCode           string           `json:"reasonCode"`
	TaskSpecDigest       string           `json:"taskSpecDigest,omitempty"`
	PolicySnapshotDigest string           `json:"policySnapshotDigest,omitempty"`
	SourceHead           string           `json:"sourceHead,omitempty"`
	SelectedAdapter      string           `json:"selectedAdapter,omitempty"`
	AuthorityMode        string           `json:"authorityMode,omitempty"`
	CapabilityDigest     string           `json:"capabilityDigest,omitempty"`
	Marshal              *marshalIdentity `json:"marshal,omitempty"`
}

type marshalIdentity struct {
	Version                string `json:"version"`
	Commit                 string `json:"commit"`
	InternalCommandVersion string `json:"internalCommandVersion"`
	InputDigest            string `json:"inputDigest"`
}

// Run executes the fixed internal command. Failures are deliberately emitted
// to stderr with a closed reason code and no input-derived text.
func Run(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("plan-premortem-check", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var manifestPath, taskPath, policyPath, schemaPath string
	flags.StringVar(&manifestPath, "manifest", "", "operator manifest")
	flags.StringVar(&taskPath, "task-spec", "", "held TaskSpec copy")
	flags.StringVar(&policyPath, "policy-snapshot", "", "held PolicySnapshot copy")
	flags.StringVar(&schemaPath, "schema", "", "operator manifest schema")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || manifestPath == "" || taskPath == "" || policyPath == "" || schemaPath == "" {
		return writeFailure(stderr, "core-probe-usage-invalid", 2)
	}
	manifestRaw, err := readBounded(manifestPath)
	if err != nil || validateOperatorSchema(schemaPath, manifestRaw) != nil {
		return writeFailure(stderr, "manifest-schema-invalid", 1)
	}
	var input manifest
	if json.Unmarshal(manifestRaw, &input) != nil {
		return writeFailure(stderr, "manifest-schema-invalid", 1)
	}
	taskRaw, err := readBounded(taskPath)
	if err != nil {
		return writeFailure(stderr, "task-spec-contract-invalid", 1)
	}
	policyRaw, err := readBounded(policyPath)
	if err != nil {
		return writeFailure(stderr, "policy-snapshot-contract-invalid", 1)
	}
	if digestBytes(taskRaw) != input.TaskSpec.Digest || digestBytes(policyRaw) != input.PolicySnapshot.Digest {
		return writeFailure(stderr, "input-digest-mismatch", 1)
	}
	if !sourceHeadPattern.MatchString(input.SourceHead) {
		return writeFailure(stderr, "source-head-invalid", 1)
	}
	validator, err := contract.NewValidator()
	if err != nil {
		return writeFailure(stderr, "core-contract-unavailable", 1)
	}
	if validator.Validate(domain.KindTask, taskRaw) != nil {
		return writeFailure(stderr, "task-spec-contract-invalid", 1)
	}
	var task domain.TaskSpec
	if json.Unmarshal(taskRaw, &task) != nil {
		return writeFailure(stderr, "task-spec-contract-invalid", 1)
	}
	if task.Repository.BaseRef != input.SourceHead {
		return writeFailure(stderr, "source-head-mismatch", 1)
	}
	if err := contract.ValidateTaskSpecAcceptanceFloor(task); err != nil {
		return writeFailure(stderr, "acceptance-required-command-missing", 1)
	}
	if validator.Validate(domain.KindPolicySnapshot, policyRaw) != nil {
		return writeFailure(stderr, "policy-snapshot-contract-invalid", 1)
	}
	effective, err := planning.ValidatePolicy(policyRaw, task, input.RunID, validator)
	if err != nil {
		return writeFailure(stderr, policyReason(err), 1)
	}
	if !eligibleSelectedAdapter(input.SelectedAdapter, effective) {
		return writeFailure(stderr, "selected-adapter-policy-mismatch", 1)
	}
	if input.SelectedAdapter == "qoder" || input.SelectedAdapter == "codex" {
		if _, err := denials.ProjectDeclaredWorkerTools(taskRaw, false); err != nil {
			if errors.Is(err, denials.ErrNamedWorkerToolsUnsupported) {
				return writeFailure(stderr, "adapter-named-worker-tools-unsupported", 1)
			}
			return writeFailure(stderr, "task-spec-contract-invalid", 1)
		}
	}
	if input.SelectedAdapter == "qoder" {
		if reason := qoderDeliverableParents(task); reason != "" {
			return writeFailure(stderr, reason, 1)
		}
	}
	runtime, err := app.NewWorkerRuntime(getenv)
	if err != nil {
		return writeFailure(stderr, "adapter-runtime-unavailable", 1)
	}
	if reason := configurationReason(input.SelectedAdapter, runtime.Configurations()); reason != "" {
		return writeFailure(stderr, reason, 1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	selection, err := runtime.Selector().Select(ctx, effective.SelectionRequest())
	if err != nil {
		return writeFailure(stderr, "adapter-selection-failed", 1)
	}
	if selection.Adapter == nil || selection.Adapter.ID() != input.SelectedAdapter {
		return writeFailure(stderr, "selected-adapter-mismatch", 1)
	}
	if validator.Validate(domain.KindCapabilitySnapshot, selection.Capability.Data) != nil {
		return writeFailure(stderr, "adapter-capability-contract-invalid", 1)
	}
	if _, err := adapter.ValidateCapability(selection.Capability, task); err != nil {
		if strings.Contains(err.Error(), adapter.ErrCapabilityExecutionProfile) && ordinaryUser(selection.Capability.Data) {
			return writeFailure(stderr, "adapter-ordinary-user-execution-profile-unsupported", 1)
		}
		if strings.Contains(err.Error(), adapter.ErrCapabilityExecutionProfile) {
			return writeFailure(stderr, "adapter-execution-profile-unsupported", 1)
		}
		return writeFailure(stderr, "adapter-capability-incompatible", 1)
	}
	capabilityDigest, err := canonical.DigestJSON(selection.Capability.Data)
	if err != nil {
		return writeFailure(stderr, "adapter-capability-contract-invalid", 1)
	}
	schemaRaw, err := readBounded(schemaPath)
	if err != nil {
		return writeFailure(stderr, "manifest-schema-invalid", 1)
	}
	build := buildinfo.Current()
	output := result{
		Status: "pass", ReasonCode: "plan-premortem-pass",
		TaskSpecDigest: input.TaskSpec.Digest, PolicySnapshotDigest: input.PolicySnapshot.Digest,
		SourceHead: input.SourceHead, SelectedAdapter: input.SelectedAdapter,
		AuthorityMode: capabilityAuthorityMode(selection.Capability.Data), CapabilityDigest: capabilityDigest,
		Marshal: &marshalIdentity{
			Version: build.Version, Commit: build.Commit, InternalCommandVersion: commandVersion,
			InputDigest: canonical.DigestBytes(bytes.Join([][]byte{manifestRaw, taskRaw, policyRaw, schemaRaw}, nil)),
		},
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(output); err != nil {
		return 1
	}
	return 0
}

func writeFailure(stderr io.Writer, reason string, code int) int {
	_, _ = fmt.Fprintf(stderr, "{\"status\":\"fail\",\"reasonCode\":%q}\n", reason)
	return code
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
	if strings.Contains(message, planning.ErrPolicyAcceptanceFloorEmpty) || strings.Contains(message, planning.ErrPolicyAcceptanceFloorNoRequired) || strings.Contains(message, planning.ErrPolicyAcceptanceFloorArgv) {
		return "acceptance-required-command-missing"
	}
	if strings.Contains(message, planning.ErrPolicyControlGates) {
		return "policy-approval-gates-conflict"
	}
	for _, sentinel := range []string{planning.ErrPolicyPublication, planning.ErrPolicyMerge, planning.ErrPolicyMergeNotAllowed, planning.ErrPolicyMergeProvider, planning.ErrPolicyMergeMethod, planning.ErrPolicyMergeChecks} {
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

func ordinaryUser(data []byte) bool { return capabilityAuthorityMode(data) == "ordinary-user" }

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
	output, err := command.Output()
	return string(output), err
}

func digestBytes(data []byte) string { return canonical.DigestBytes(data) }
