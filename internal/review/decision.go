package review

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/verification"
)

type DecisionImporter struct {
	RunDirectory string
	Validator    *contract.Validator
}

type DecisionInput struct {
	Path             string
	Task             domain.TaskSpec
	TaskID           string
	RunID            string
	SpecDigest       string
	ReviewRound      uint
	AttemptsUsed     uint
	ReworkRoundsUsed uint
	Report           verification.Report
	Manifest         verification.ArtifactManifest
}

type DecisionResult struct {
	Decision        domain.ReviewDecision
	DecisionData    []byte
	DecisionDigest  string
	Packet          domain.ReviewPacket
	PacketData      []byte
	TargetState     domain.State
	BudgetExhausted bool
}

func (d *DecisionImporter) Import(input DecisionInput) (DecisionResult, error) {
	if d.Validator == nil {
		return DecisionResult{}, errors.New("contract validator is required")
	}
	decisionData, err := readBounded(input.Path, packetByteLimit)
	if err != nil {
		return DecisionResult{}, fmt.Errorf("read review decision: %w", err)
	}
	if err := d.Validator.Validate(domain.KindReviewDecision, decisionData); err != nil {
		return DecisionResult{}, fmt.Errorf("validate review decision: %w", err)
	}
	var decision domain.ReviewDecision
	if err := json.Unmarshal(decisionData, &decision); err != nil {
		return DecisionResult{}, err
	}
	packetData, err := readBounded(filepath.Join(d.RunDirectory, "review-packet.json"), packetByteLimit)
	if err != nil {
		return DecisionResult{}, fmt.Errorf("read current review packet: %w", err)
	}
	if err := d.Validator.Validate(domain.KindReviewPacket, packetData); err != nil {
		return DecisionResult{}, fmt.Errorf("validate current review packet: %w", err)
	}
	var packet domain.ReviewPacket
	if err := json.Unmarshal(packetData, &packet); err != nil {
		return DecisionResult{}, err
	}
	packetDigest, err := canonical.DigestJSON(packetData)
	if err != nil {
		return DecisionResult{}, err
	}
	if decision.TaskID != input.TaskID || decision.RunID != input.RunID || decision.ReviewRound != input.ReviewRound || decision.SpecDigest != input.SpecDigest {
		return DecisionResult{}, errors.New("review decision identity does not match current run")
	}
	if packet.TaskID != input.TaskID || packet.RunID != input.RunID || packet.ReviewRound != input.ReviewRound || packet.SpecDigest != input.SpecDigest || packet.BaseSHA != input.Report.BaseSHA {
		return DecisionResult{}, errors.New("review packet identity does not match current run")
	}
	if decision.ReviewPacketDigest != packetDigest || decision.VerificationDigest != packet.VerificationDigest || decision.ArtifactManifestDigest != packet.ArtifactManifestDigest || decision.EvidenceDigest != packet.EvidenceDigest {
		return DecisionResult{}, errors.New("review decision references stale evidence")
	}
	if err := validateVerdict(input, decision, packet); err != nil {
		return DecisionResult{}, err
	}
	budgetExhausted := input.ReworkRoundsUsed >= uint(input.Task.Budgets.MaxReworkRounds) || input.AttemptsUsed >= uint(input.Task.Budgets.MaxAttempts)
	target, err := targetState(input.Task, decision, input.Report.Status == "pass", budgetExhausted)
	if err != nil {
		return DecisionResult{}, err
	}
	decisionDigest, err := canonical.DigestJSON(decisionData)
	if err != nil {
		return DecisionResult{}, err
	}
	return DecisionResult{Decision: decision, DecisionData: decisionData, DecisionDigest: decisionDigest, Packet: packet, PacketData: packetData, TargetState: target, BudgetExhausted: budgetExhausted}, nil
}

func validateVerdict(input DecisionInput, decision domain.ReviewDecision, packet domain.ReviewPacket) error {
	requiredPassed := input.Report.Status == "pass"
	switch decision.Verdict {
	case "accept":
		if !requiredPassed || len(decision.BlockingFindings) > 0 {
			return errors.New("accept requires passing evidence and no blocking findings")
		}
		if input.Task.Publication.Required && decision.PublicationRecommendation != "publish" {
			return errors.New("frozen policy requires publication")
		}
		if !input.Task.Publication.Required && decision.PublicationRecommendation == "publish" {
			return errors.New("decision cannot introduce publication not required by frozen policy")
		}
	case "rework":
		if requiredPassed && len(decision.BlockingFindings) == 0 {
			return errors.New("rework requires a blocking finding or failed required gate")
		}
	case "reject":
	case "blocked":
		if decision.BlockerOwner == "" {
			return errors.New("blocked decision requires blocker owner")
		}
	case "no_change":
		if !input.Task.Acceptance.AllowNoChange || input.Report.Observed.ChangedFileCount != 0 || !hasValidatedDiagnostic(input.Manifest) {
			return errors.New("no_change requires an allowed empty diff and a validated diagnostic artifact")
		}
	default:
		return errors.New("unsupported review verdict")
	}
	if input.Task.Publication.MergePolicy == "never" && decision.MergeRecommendation != "do-not-merge" && decision.MergeRecommendation != "not-applicable" {
		return errors.New("merge recommendation conflicts with frozen never-merge policy")
	}
	if decision.Verdict != "accept" && decision.PublicationRecommendation == "publish" {
		return errors.New("only accept may recommend publication")
	}
	currentIDs := make(map[string]bool, len(decision.BlockingFindings))
	for _, finding := range decision.BlockingFindings {
		currentIDs[finding.ID] = true
	}
	for _, previous := range packet.PreviousBlockingFindings {
		if !currentIDs[previous.ID] && packet.SnapshotDigest == previous.SnapshotDigest && packet.VerificationDigest == previous.VerificationDigest {
			return fmt.Errorf("blocking finding %s cannot close without new evidence", previous.ID)
		}
	}
	return nil
}

func targetState(task domain.TaskSpec, decision domain.ReviewDecision, requiredPassed, budgetExhausted bool) (domain.State, error) {
	switch decision.Verdict {
	case "accept":
		if !requiredPassed {
			return "", errors.New("accept requires passing evidence")
		}
		if task.Publication.Required {
			return domain.StatePublishing, nil
		}
		return domain.StateAccepted, nil
	case "rework":
		if budgetExhausted {
			return domain.StateRejected, nil
		}
		return domain.StateReworkRequested, nil
	case "reject":
		return domain.StateRejected, nil
	case "blocked":
		return domain.StateBlocked, nil
	case "no_change":
		return domain.StateNoChange, nil
	default:
		return "", errors.New("unsupported verdict")
	}
}

func hasValidatedDiagnostic(manifest verification.ArtifactManifest) bool {
	for _, artifact := range manifest.Artifacts {
		if artifact.Kind == "diagnostic" && artifact.Status == "validated" {
			return true
		}
	}
	return false
}
