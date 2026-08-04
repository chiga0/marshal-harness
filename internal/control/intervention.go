package control

import (
	"errors"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/planning"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

var (
	ErrInterventionUnavailable = errors.New("intervention is unavailable in the current state")
	ErrSteeringDenied          = errors.New("mediated steering is denied by policy")
	ErrSteeringBudget          = errors.New("mediated steering round budget exhausted")
	ErrDirectPTYDenied         = errors.New("direct PTY intervention is denied by policy")
)

// InterventionInput describes one already-classified control action. Delivery
// to a TerminalSession is intentionally separate; callers must not persist a
// clarification or correction until the instruction was accepted for delivery.
type InterventionInput struct {
	StateRoot   string
	RunID       string
	AttemptID   string
	Category    string
	SourceType  string
	SourceID    string
	Instruction string
	// DeliveryAccepted is required for clarification/correction and must be
	// set only after the active TerminalSession accepted the instruction.
	DeliveryAccepted bool
	Now              time.Time
	Validator        *contract.Validator
}

// RecordIntervention appends the classified action after checking current Run,
// Attempt, Policy and steering-budget invariants. It never mutates frozen input
// files or the lifecycle journal.
func RecordIntervention(input InterventionInput) (domain.InterventionRecord, error) {
	if err := validateInterventionInput(input); err != nil {
		return domain.InterventionRecord{}, err
	}
	store := runstore.New(input.StateRoot)
	lease, err := store.Acquire(input.RunID)
	if err != nil {
		return domain.InterventionRecord{}, err
	}
	defer lease.Release()
	record, err := prepareIntervention(store, input)
	if err != nil {
		return domain.InterventionRecord{}, err
	}
	if err := store.AppendIntervention(lease, input.Validator, record); err != nil {
		return domain.InterventionRecord{}, err
	}
	return record, nil
}

// prepareIntervention validates the current Run, Attempt, Policy and control
// journal while the caller holds the Run lease. It does not perform delivery
// or persistence, allowing the coordinator to bind both to the same snapshot.
func prepareIntervention(store *runstore.Store, input InterventionInput) (domain.InterventionRecord, error) {
	context, err := loadApprovalContext(store, ApprovalInput{StateRoot: input.StateRoot, RunID: input.RunID, Validator: input.Validator})
	if err != nil {
		return domain.InterventionRecord{}, err
	}
	records, err := store.ReadControlRecords(input.RunID, input.Validator)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return domain.InterventionRecord{}, err
	}
	record := domain.InterventionRecord{
		APIVersion:      domain.APIVersionV1Alpha1,
		Kind:            domain.KindInterventionRecord,
		TaskID:          context.state.TaskID,
		RunID:           context.state.RunID,
		ControlSequence: uint64(len(records)) + 1,
		StateSequence:   context.state.Sequence,
		Category:        input.Category,
		Source:          domain.ControlSource{Type: input.SourceType, ID: input.SourceID},
		CreatedAt:       input.Now.UTC(),
	}
	switch input.Category {
	case domain.InterventionCategoryClarification, domain.InterventionCategoryImplementationCorrection:
		if context.state.State != domain.StateRunning || input.AttemptID == "" || input.AttemptID != context.state.CurrentAttemptID {
			return domain.InterventionRecord{}, ErrInterventionUnavailable
		}
		if !context.policy.AllowMediatedSteering {
			return domain.InterventionRecord{}, ErrSteeringDenied
		}
		round := nextSteeringRound(records)
		if round > context.policy.MaxSteeringRounds {
			return domain.InterventionRecord{}, ErrSteeringBudget
		}
		record.AttemptID = input.AttemptID
		record.Effect = domain.InterventionEffectContinue
		record.Instruction = input.Instruction
		record.InstructionDigest = canonical.DigestBytes([]byte(input.Instruction))
		record.SteeringRound = round
	case domain.InterventionCategoryScopeChange:
		if context.state.State.Terminal() || input.SourceType != domain.ControlSourceTypeHuman {
			return domain.InterventionRecord{}, ErrInterventionUnavailable
		}
		if context.state.State == domain.StateRunning {
			if input.AttemptID == "" || input.AttemptID != context.state.CurrentAttemptID {
				return domain.InterventionRecord{}, ErrInterventionUnavailable
			}
			record.AttemptID = input.AttemptID
		}
		record.Effect = domain.InterventionEffectNewRunRequired
		record.Instruction = input.Instruction
		record.InstructionDigest = canonical.DigestBytes([]byte(input.Instruction))
	case domain.InterventionCategoryManualPTY:
		if context.state.State != domain.StateRunning || input.AttemptID == "" || input.AttemptID != context.state.CurrentAttemptID {
			return domain.InterventionRecord{}, ErrInterventionUnavailable
		}
		if context.policy.DirectPTYPolicy != planning.DirectPTYRecordAndReverify {
			return domain.InterventionRecord{}, ErrDirectPTYDenied
		}
		record.AttemptID = input.AttemptID
		record.Effect = domain.InterventionEffectRequiredReverification
	default:
		return domain.InterventionRecord{}, ErrInvalidControlInput
	}
	recordID, err := domain.NewID("intervention")
	if err != nil {
		return domain.InterventionRecord{}, err
	}
	record.RecordID = recordID
	return record, nil
}

func validateInterventionInput(input InterventionInput) error {
	if strings.TrimSpace(input.StateRoot) == "" || input.Validator == nil || input.Now.IsZero() ||
		domain.ValidateID(input.RunID) != nil || strings.TrimSpace(input.SourceID) == "" || len(input.SourceID) > 512 ||
		!slices.Contains([]string{domain.ControlSourceTypeHuman, domain.ControlSourceTypeLeadAgent, domain.ControlSourceTypeTerminalHook}, input.SourceType) {
		return ErrInvalidControlInput
	}
	if input.AttemptID != "" && domain.ValidateID(input.AttemptID) != nil {
		return ErrInvalidControlInput
	}
	hasInstruction := strings.TrimSpace(input.Instruction) != ""
	switch input.Category {
	case domain.InterventionCategoryClarification, domain.InterventionCategoryImplementationCorrection:
		if !hasInstruction || len(input.Instruction) > 32768 || input.SourceType == domain.ControlSourceTypeTerminalHook || !input.DeliveryAccepted {
			return ErrInvalidControlInput
		}
	case domain.InterventionCategoryScopeChange:
		if !hasInstruction || len(input.Instruction) > 32768 || input.SourceType != domain.ControlSourceTypeHuman {
			return ErrInvalidControlInput
		}
	case domain.InterventionCategoryManualPTY:
		if hasInstruction || input.SourceType == domain.ControlSourceTypeLeadAgent {
			return ErrInvalidControlInput
		}
	default:
		return ErrInvalidControlInput
	}
	return nil
}

func nextSteeringRound(records []runstore.ControlRecord) uint {
	var round uint
	for _, entry := range records {
		if entry.Intervention != nil && (entry.Intervention.Category == domain.InterventionCategoryClarification ||
			entry.Intervention.Category == domain.InterventionCategoryImplementationCorrection) && entry.Intervention.SteeringRound > round {
			round = entry.Intervention.SteeringRound
		}
	}
	return round + 1
}
