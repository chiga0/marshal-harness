package planning

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/goal"
)

// OrderQuoteOracleDigest pins the independently maintained v1 business oracle,
// not an author report or a configurable command claiming the same template ID.
// Updating the oracle requires an explicit template contract/code change.
const OrderQuoteOracleDigest = "dfa7965c65b896e5d0542fa7edfe5d9699150e4c76f7dbc1a5b1f570ab0db7f2"

const orderQuoteOracleLoader = "import hashlib,pathlib,sys; p=pathlib.Path(sys.argv[1]); data=p.read_bytes() if not p.is_symlink() else b''; hashlib.sha256(data).hexdigest()==sys.argv[2] or sys.exit('oracle-drift'); sys.argv=[str(p)]+sys.argv[3:]; exec(compile(data,str(p),'exec'),{'__name__':'__main__','__file__':str(p)})"

// TaskTemplate is pure frozen configuration. It grants no approval, dispatch,
// persistence, or automatic Decision authority. Those require current facts.
type TaskTemplate struct {
	preview TeamInputsPreview
}

var _ application.TaskTemplatePort = TaskTemplate{}

var ErrTaskTemplate = errors.New("planning: unsupported Task template")

func OrderQuotePaths(nodeID string) []string {
	switch nodeID {
	case "service":
		return []string{"quote_api.py"}
	case "client":
		return []string{"quote_client.py"}
	case "integration":
		return []string{"quote_api.py", "quote_client.py", "quote_delivery.json"}
	default:
		return nil
	}
}

func OrderQuoteOracleCommand(repository, nodeID string) domain.TaskCommand {
	argv := []string{"/usr/bin/python3", "-I", "-B", "-c", orderQuoteOracleLoader, filepath.Join(repository, "scripts", "order-quote-team-oracle.py"), OrderQuoteOracleDigest}
	if nodeID == "service" || nodeID == "integration" {
		argv = append(argv, "--api", "quote_api.py")
	}
	if nodeID == "client" || nodeID == "integration" {
		argv = append(argv, "--client", "quote_client.py")
	}
	if nodeID == "integration" {
		argv = append(argv, "--delivery", "quote_delivery.json")
	}
	return domain.TaskCommand{ID: "quote-team-" + nodeID, Argv: argv, CWD: ".", Required: true, BaselinePolicy: "none", TimeoutSeconds: 30, MaxLogBytes: 4000}
}

// OpenTaskTemplate accepts complete operator-owned inputs only after the
// existing pure preview and the narrower B1 objective-oracle profile match.
// It does not resolve files, probe a Provider, or weaken runtime preflight.
func OpenTaskTemplate(raw []byte, validator *contract.Validator) (TaskTemplate, error) {
	preview, err := PreviewTeamInputs(raw, validator)
	if err != nil {
		return TaskTemplate{}, ErrTaskTemplate
	}
	for _, node := range preview.Inputs.Proposal.Nodes {
		paths := OrderQuotePaths(node.NodeId)
		if len(paths) == 0 || !slices.Equal(paths, node.Paths) || node.Estimate.Runs != 1 || node.Estimate.Attempts != 1 {
			return TaskTemplate{}, ErrTaskTemplate
		}
	}
	limits := preview.Inputs.Limits
	if limits.MaxNodes != 3 || limits.MaxConcurrentNodes != 2 || limits.MaxPlanRevisions != 1 || limits.MaxTotalRuns != 3 || limits.MaxTotalAttempts != 3 {
		return TaskTemplate{}, ErrTaskTemplate
	}
	for _, node := range preview.Inputs.Nodes {
		var task domain.TaskSpec
		if json.Unmarshal(node.Task, &task) != nil || len(task.Acceptance.Commands) != 1 || task.Acceptance.AllowNoChange || task.Budgets.MaxAttempts != 1 || task.Budgets.MaxOperationalRetries != 0 || task.Budgets.MaxReworkRounds != 0 {
			return TaskTemplate{}, ErrTaskTemplate
		}
		paths := OrderQuotePaths(node.NodeID)
		if len(task.Deliverables) != len(paths) || task.Scope.MaxChangedFiles != len(paths) {
			return TaskTemplate{}, ErrTaskTemplate
		}
		for index, path := range paths {
			deliverable := task.Deliverables[index]
			if !deliverable.Required || deliverable.PathGlob != path {
				return TaskTemplate{}, ErrTaskTemplate
			}
		}
		want := OrderQuoteOracleCommand(preview.Inputs.Spec.Repository, node.NodeID)
		got := task.Acceptance.Commands[0]
		if got.ID != want.ID || !slices.Equal(got.Argv, want.Argv) || got.CWD != want.CWD || got.Required != want.Required || got.BaselinePolicy != want.BaselinePolicy || got.TimeoutSeconds != want.TimeoutSeconds || got.MaxLogBytes != want.MaxLogBytes {
			return TaskTemplate{}, ErrTaskTemplate
		}
		wantRole := "implement"
		if node.NodeID == "integration" {
			wantRole = "integrate"
		}
		if node.Role != wantRole {
			return TaskTemplate{}, ErrTaskTemplate
		}
	}
	return TaskTemplate{preview: preview}, nil
}

func (t TaskTemplate) Digest() string { return t.preview.Digest }

// RenderTask implements the neutral application Port. Only composition knows
// this concrete planner; consumers receive canonical bytes, never a planner
// object with persistence or execution methods.
func (t TaskTemplate) RenderTask(goalID string, submission goal.TaskSubmission) ([]byte, error) {
	validator, err := contract.NewValidator()
	if err != nil {
		return nil, err
	}
	preview, err := t.Preview(goalID, submission, validator)
	return preview.Canonical, err
}

// InspectTask validates the exact persisted envelope against its bounded
// oracle profile, not this process's installed template digest. The zero
// value can inspect an old draft even when new submissions are disabled.
func (TaskTemplate) InspectTask(raw []byte) ([]application.TaskPreviewNode, error) {
	validator, err := contract.NewValidator()
	if err != nil {
		return nil, err
	}
	frozen, err := OpenTaskTemplate(raw, validator)
	if err != nil {
		return nil, err
	}
	nodes := make([]application.TaskPreviewNode, 0, len(frozen.preview.Inputs.Nodes))
	for _, node := range frozen.preview.Inputs.Nodes {
		var task domain.TaskSpec
		if err := json.Unmarshal(node.Task, &task); err != nil {
			return nil, ErrTaskTemplate
		}
		nodes = append(nodes, application.TaskPreviewNode{ID: node.NodeID, Role: node.Role, Work: task.Work, Paths: task.Scope.AllowPaths, OracleDigest: "sha256:" + OrderQuoteOracleDigest})
	}
	return nodes, nil
}

// Preview binds new Task/Goal/node identities and public context without
// altering the installed acceptance, permissions, environment, or budget.
// It is deterministic: the caller must keep the same Goal ID on a retry.
func (t TaskTemplate) Preview(goalID string, submission goal.TaskSubmission, validator *contract.Validator) (TeamInputsPreview, error) {
	if t.preview.Digest == "" || domain.ValidateID(goalID) != nil || submission.Validate() != nil {
		return TeamInputsPreview{}, ErrTaskTemplate
	}
	// Deep-copy all raw envelopes; callers cannot mutate the frozen template.
	var inputs TeamInputs
	if json.Unmarshal(t.preview.Canonical, &inputs) != nil {
		return TeamInputsPreview{}, ErrTaskTemplate
	}
	inputs.Spec.GoalId = goalID
	inputs.Proposal.GoalId = goalID
	inputs.Proposal.ProposalId = "task-plan-" + strings.TrimPrefix(canonical.DigestBytes([]byte(goalID)), "sha256:")
	context := "\n\n用户需求上下文（不修改已冻结的 order-quote/v1 交付范围、验收与权限）：\n" + submission.Intent
	if submission.Context.Text != "" {
		context += "\n" + submission.Context.Text
	}
	inputs.Spec.Description += context
	digest, err := inputs.Spec.Digest()
	if err != nil {
		return TeamInputsPreview{}, ErrTaskTemplate
	}
	inputs.Proposal.GoalSpecDigest = digest
	for index := range inputs.Nodes {
		node := &inputs.Nodes[index]
		taskID, runID, err := TeamNodeIDs(inputs.Proposal, node.NodeID)
		if err != nil {
			return TeamInputsPreview{}, ErrTaskTemplate
		}
		var task, policy map[string]any
		if json.Unmarshal(node.Task, &task) != nil || json.Unmarshal(node.Policy, &policy) != nil {
			return TeamInputsPreview{}, ErrTaskTemplate
		}
		metadata, okMetadata := task["metadata"].(map[string]any)
		work, okWork := task["work"].(map[string]any)
		if !okMetadata || !okWork {
			return TeamInputsPreview{}, ErrTaskTemplate
		}
		metadata["id"] = taskID
		var contexts []any
		if previous, exists := work["context"]; exists {
			var ok bool
			contexts, ok = previous.([]any)
			if !ok {
				return TeamInputsPreview{}, ErrTaskTemplate
			}
		}
		contexts = append(contexts, "用户需求上下文（不修改已冻结的 order-quote/v1 交付范围、验收与权限）：", submission.Intent)
		// Schema bounds each array element by characters, while the public
		// submission bounds total bytes. Preserve long text without truncation
		// or splitting a UTF-8 code point; retain the installed context prefix.
		for text := []rune(submission.Context.Text); len(text) > 0; {
			n := min(len(text), 8000)
			contexts = append(contexts, string(text[:n]))
			text = text[n:]
		}
		work["context"] = contexts
		policy["taskId"], policy["runId"], policy["policyDigest"] = taskID, runID, ""
		raw, err := json.Marshal(policy)
		if err != nil {
			return TeamInputsPreview{}, ErrTaskTemplate
		}
		policy["policyDigest"], err = canonical.DigestJSON(raw)
		if err != nil {
			return TeamInputsPreview{}, ErrTaskTemplate
		}
		node.Task, err = json.Marshal(task)
		if err != nil {
			return TeamInputsPreview{}, ErrTaskTemplate
		}
		node.Policy, err = json.Marshal(policy)
		if err != nil {
			return TeamInputsPreview{}, ErrTaskTemplate
		}
	}
	raw, err := json.Marshal(inputs)
	if err != nil {
		return TeamInputsPreview{}, ErrTaskTemplate
	}
	return PreviewTeamInputs(raw, validator)
}
