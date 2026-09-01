// Package control implements Marshal's human approval and intervention gates.
package control

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/planning"
	"github.com/chiga0/marshal-harness/internal/runstore"
	"github.com/chiga0/marshal-harness/internal/selfidentity"
)

var (
	ErrApprovalRequired     = errors.New("approval required")
	ErrApprovalStale        = errors.New("approval is stale")
	ErrInvalidApprovalState = errors.New("approval gate is unavailable in the current state")
	ErrApprovalNotRequired  = errors.New("approval is not required by policy")
	ErrInvalidControlInput  = errors.New("invalid approval input")
)

const maxControlInputBytes int64 = 32 << 20

// ApprovalInput identifies one exact Run gate. SourceID is required only when
// creating an ApprovalRecord; Require ignores it.
type ApprovalInput struct {
	StateRoot         string
	RunID             string
	Gate              string
	SourceID          string
	Now               time.Time
	Validator         *contract.Validator
	LocalSelfIdentity *selfidentity.LocalSelfIdentityObservationV2
}

// Approve creates one immutable human ApprovalRecord bound to the current
// frozen inputs and, for publish, the current review evidence.
func Approve(input ApprovalInput) (domain.ApprovalRecord, error) {
	if err := validateInput(input, true); err != nil {
		return domain.ApprovalRecord{}, err
	}
	store := runstore.New(input.StateRoot)
	lease, err := store.Acquire(input.RunID)
	if err != nil {
		return domain.ApprovalRecord{}, err
	}
	defer lease.Release()

	context, err := loadApprovalContext(store, input)
	if err != nil {
		return domain.ApprovalRecord{}, err
	}
	if !slices.Contains(context.policy.RequiredApprovals, input.Gate) {
		return domain.ApprovalRecord{}, ErrApprovalNotRequired
	}
	binding, err := currentBinding(input, context)
	if err != nil {
		return domain.ApprovalRecord{}, err
	}
	records, err := store.ReadControlRecords(input.RunID, input.Validator)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return domain.ApprovalRecord{}, err
	}
	recordID, err := domain.NewID("approval")
	if err != nil {
		return domain.ApprovalRecord{}, err
	}
	record := domain.ApprovalRecord{
		APIVersion:      domain.APIVersionV1Alpha1,
		Kind:            domain.KindApprovalRecord,
		RecordID:        recordID,
		TaskID:          context.state.TaskID,
		RunID:           context.state.RunID,
		ControlSequence: uint64(len(records)) + 1,
		Gate:            input.Gate,
		Source:          domain.ControlSource{Type: domain.ControlSourceTypeHuman, ID: input.SourceID},
		Binding:         binding,
		Outcome:         domain.ApprovalOutcomeApproved,
		CreatedAt:       input.Now.UTC(),
	}
	if err := store.AppendApproval(lease, input.Validator, record); err != nil {
		return domain.ApprovalRecord{}, err
	}
	return record, nil
}

// Require succeeds when policy does not require this gate or when the journal
// contains an ApprovalRecord bound to the exact current state and evidence.
func Require(input ApprovalInput) error {
	if err := validateInput(input, false); err != nil {
		return err
	}
	store := runstore.New(input.StateRoot)
	lease, err := store.Acquire(input.RunID)
	if err != nil {
		return err
	}
	defer lease.Release()

	context, err := loadApprovalContext(store, input)
	if err != nil {
		return err
	}
	if !slices.Contains(context.policy.RequiredApprovals, input.Gate) {
		return nil
	}
	binding, err := currentBinding(input, context)
	if err != nil {
		return err
	}
	records, err := store.ReadControlRecords(input.RunID, input.Validator)
	if errors.Is(err, os.ErrNotExist) {
		return ErrApprovalRequired
	}
	if err != nil {
		return err
	}
	foundGate := false
	for _, entry := range records {
		if entry.Approval == nil || entry.Approval.Gate != input.Gate {
			continue
		}
		foundGate = true
		if approvalMatches(*entry.Approval, context.state, binding) {
			return nil
		}
	}
	if foundGate {
		return ErrApprovalStale
	}
	return ErrApprovalRequired
}

type approvalContext struct {
	state  domain.RunState
	task   domain.TaskSpec
	policy planning.EffectivePolicy
	runDir string
}

func loadApprovalContext(store *runstore.Store, input ApprovalInput) (approvalContext, error) {
	state, err := store.Inspect(input.RunID)
	if err != nil {
		return approvalContext{}, err
	}
	stateData, err := json.Marshal(state)
	if err != nil || input.Validator.Validate(domain.KindRunState, stateData) != nil || state.RunID != input.RunID ||
		state.SpecDigest == "" || state.PolicyDigest == "" || state.CapabilityDigest == "" || state.BaseSHA == "" {
		return approvalContext{}, ErrInvalidControlInput
	}
	runDir := filepath.Join(input.StateRoot, "runs", input.RunID)
	taskData, err := readContract(runDir, "task-spec.json")
	if err != nil || input.Validator.Validate(domain.KindTask, taskData) != nil {
		return approvalContext{}, ErrInvalidControlInput
	}
	policyData, err := readContract(runDir, "policy-snapshot.json")
	if err != nil || input.Validator.Validate(domain.KindPolicySnapshot, policyData) != nil {
		return approvalContext{}, ErrInvalidControlInput
	}
	capabilityData, err := readContract(runDir, "capability-snapshot.json")
	if err != nil || input.Validator.Validate(domain.KindCapabilitySnapshot, capabilityData) != nil {
		return approvalContext{}, ErrInvalidControlInput
	}
	if !digestMatches(taskData, state.SpecDigest) || !digestMatches(policyData, state.PolicyDigest) ||
		!digestMatches(capabilityData, state.CapabilityDigest) {
		return approvalContext{}, ErrInvalidControlInput
	}
	var task domain.TaskSpec
	var policyIdentity struct {
		TaskID string `json:"taskId"`
		RunID  string `json:"runId"`
	}
	if json.Unmarshal(taskData, &task) != nil || json.Unmarshal(policyData, &policyIdentity) != nil ||
		task.Metadata.ID != state.TaskID || policyIdentity.TaskID != state.TaskID || policyIdentity.RunID != state.RunID {
		return approvalContext{}, ErrInvalidControlInput
	}
	effective, err := planning.ValidatePolicy(policyData, task, state.RunID, input.Validator)
	if err != nil {
		return approvalContext{}, ErrInvalidControlInput
	}
	if err := planning.ValidateLocalDogfoodEnvironmentBinding(policyData, input.Validator, input.LocalSelfIdentity); err != nil {
		return approvalContext{}, ErrInvalidControlInput
	}
	return approvalContext{state: state, task: task, policy: effective, runDir: runDir}, nil
}

func currentBinding(input ApprovalInput, context approvalContext) (domain.ApprovalBinding, error) {
	binding := domain.ApprovalBinding{
		StateSequence:    context.state.Sequence,
		SpecDigest:       context.state.SpecDigest,
		PolicyDigest:     context.state.PolicyDigest,
		CapabilityDigest: context.state.CapabilityDigest,
		BaseSHA:          context.state.BaseSHA,
	}
	switch input.Gate {
	case domain.ApprovalGatePlan:
		if context.state.State != domain.StateReady {
			return domain.ApprovalBinding{}, ErrInvalidApprovalState
		}
	case domain.ApprovalGatePublish:
		if context.state.State != domain.StatePublishing {
			return domain.ApprovalBinding{}, ErrInvalidApprovalState
		}
		decisionData, err := readContract(context.runDir,
			filepath.Join("decisions", fmt.Sprintf("decision-%03d.json", context.state.ReviewRound)))
		if err != nil || input.Validator.Validate(domain.KindReviewDecision, decisionData) != nil {
			return domain.ApprovalBinding{}, ErrInvalidControlInput
		}
		var decision domain.ReviewDecision
		if json.Unmarshal(decisionData, &decision) != nil || decision.TaskID != context.state.TaskID ||
			decision.RunID != context.state.RunID || decision.ReviewRound != context.state.ReviewRound ||
			decision.SpecDigest != context.state.SpecDigest || decision.Verdict != "accept" ||
			decision.PublicationRecommendation != "publish" {
			return domain.ApprovalBinding{}, ErrInvalidControlInput
		}
		decisionDigest, err := canonical.DigestJSON(decisionData)
		if err != nil {
			return domain.ApprovalBinding{}, ErrInvalidControlInput
		}
		binding.ReviewRound = decision.ReviewRound
		binding.DecisionDigest = decisionDigest
		binding.EvidenceDigest = decision.EvidenceDigest
	default:
		return domain.ApprovalBinding{}, ErrInvalidControlInput
	}
	return binding, nil
}

func approvalMatches(record domain.ApprovalRecord, state domain.RunState, binding domain.ApprovalBinding) bool {
	return record.TaskID == state.TaskID && record.RunID == state.RunID && record.Outcome == domain.ApprovalOutcomeApproved &&
		record.Source.Type == domain.ControlSourceTypeHuman && record.Binding == binding
}

func validateInput(input ApprovalInput, sourceRequired bool) error {
	if strings.TrimSpace(input.StateRoot) == "" || input.Validator == nil || domain.ValidateID(input.RunID) != nil ||
		(input.Gate != domain.ApprovalGatePlan && input.Gate != domain.ApprovalGatePublish) {
		return ErrInvalidControlInput
	}
	if sourceRequired && (strings.TrimSpace(input.SourceID) == "" || len(input.SourceID) > 512 || input.Now.IsZero()) {
		return ErrInvalidControlInput
	}
	return nil
}

func digestMatches(data []byte, expected string) bool {
	digest, err := canonical.DigestJSON(data)
	return err == nil && digest == expected
}

func readContract(runDir, name string) ([]byte, error) {
	file, err := os.Open(filepath.Join(runDir, name))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxControlInputBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxControlInputBytes {
		return nil, fmt.Errorf("control input exceeds limit")
	}
	return data, nil
}
