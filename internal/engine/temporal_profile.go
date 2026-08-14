package engine

import (
	"fmt"
)

// TemporalBackendName is the frozen identifier of the Temporal backend
// profile.
const TemporalBackendName = "temporal"

// SeamChoice is the closed enumeration of the single authority seam
// selections a backend profile may declare (ADR 0018 §15).
type SeamChoice string

// Closed seam selections. The M9-e frozen selection is the ledger-derived
// Core command journal; the same-transaction outbox alternative was
// rejected at spec time and is not implemented in this repository.
const (
	SeamLedgerDerivedJournal  SeamChoice = "ledger-derived-journal"
	SeamSameTransactionOutbox SeamChoice = "same-transaction-outbox"
)

// Validate rejects every value outside the closed enumeration.
func (choice SeamChoice) Validate() error {
	switch choice {
	case SeamLedgerDerivedJournal, SeamSameTransactionOutbox:
		return nil
	default:
		return fmt.Errorf("engine: unknown seam choice %q", string(choice))
	}
}

// WorkflowVersioningStrategy is the closed enumeration of workflow
// versioning strategies the Temporal profile may declare.
type WorkflowVersioningStrategy string

// VersioningStrategyBuildIdTaskQueue routes activity and workflow tasks by
// worker build ID on the task queue: in-flight workflows keep replaying on
// the build that owns their history, and command identity never depends on
// the build (ADR 0018 §8/§15).
const VersioningStrategyBuildIdTaskQueue WorkflowVersioningStrategy = "build-id-task-queue"

// Validate rejects every value outside the closed enumeration.
func (strategy WorkflowVersioningStrategy) Validate() error {
	switch strategy {
	case VersioningStrategyBuildIdTaskQueue:
		return nil
	default:
		return fmt.Errorf("engine: unknown workflow versioning strategy %q", string(strategy))
	}
}

// ContinueAsNewCarryOver is the closed enumeration of what a Temporal
// workflow may carry across a Continue-As-New boundary.
type ContinueAsNewCarryOver string

// ContinueAsNewCarryOverJournal is the only permitted carry-over: the new
// workflow run re-derives everything from the ledger-derived command
// journal. Workflow memory never carries business state across the
// Continue-As-New boundary.
const ContinueAsNewCarryOverJournal ContinueAsNewCarryOver = "re-derive-from-journal"

// Validate rejects every value outside the closed enumeration.
func (carryOver ContinueAsNewCarryOver) Validate() error {
	switch carryOver {
	case ContinueAsNewCarryOverJournal:
		return nil
	default:
		return fmt.Errorf("engine: unknown continue-as-new carry-over %q", string(carryOver))
	}
}

// PayloadPlacement is the closed enumeration of payload placement modes.
type PayloadPlacement string

// PayloadPlacementExternalReference carries only the payloadRef digest in
// the command; payload bytes live in the external payload store and never
// inline in workflow history (ADR 0018 §15).
const PayloadPlacementExternalReference PayloadPlacement = "external-reference"

// Validate rejects every value outside the closed enumeration.
func (placement PayloadPlacement) Validate() error {
	switch placement {
	case PayloadPlacementExternalReference:
		return nil
	default:
		return fmt.Errorf("engine: unknown payload placement %q", string(placement))
	}
}

// ActivityCancelSemantics is the closed enumeration of activity cancel
// semantics a backend profile may declare.
type ActivityCancelSemantics string

// ActivityCancelReportOnly: a cancelled activity reports the cancellation
// transport observation only and never a business terminal claim.
const ActivityCancelReportOnly ActivityCancelSemantics = "report-no-business-claim"

// Validate rejects every value outside the closed enumeration.
func (semantics ActivityCancelSemantics) Validate() error {
	switch semantics {
	case ActivityCancelReportOnly:
		return nil
	default:
		return fmt.Errorf("engine: unknown activity cancel semantics %q", string(semantics))
	}
}

// DeliveryRetryBudget is the closed enumeration of delivery retry budget
// semantics a backend profile may declare.
type DeliveryRetryBudget string

// DeliveryRetryBudgetBackendOnly: delivery and activity retry is backend
// transport retry. It never creates a business Attempt and never consumes
// a business retry or rework budget (ADR 0019 §7).
const DeliveryRetryBudgetBackendOnly DeliveryRetryBudget = "backend-only"

// Validate rejects every value outside the closed enumeration.
func (budget DeliveryRetryBudget) Validate() error {
	switch budget {
	case DeliveryRetryBudgetBackendOnly:
		return nil
	default:
		return fmt.Errorf("engine: unknown delivery retry budget %q", string(budget))
	}
}

// WorkflowVersioningDeclaration declares the workflow versioning/build ID
// policy (ADR 0018 §8/§15).
type WorkflowVersioningDeclaration struct {
	Strategy  WorkflowVersioningStrategy `json:"strategy"`
	TaskQueue string                     `json:"taskQueue"`
	BuildId   string                     `json:"buildId"`
}

// Validate fails closed on any unknown strategy or missing identity.
func (declaration WorkflowVersioningDeclaration) Validate() error {
	if err := declaration.Strategy.Validate(); err != nil {
		return err
	}
	if err := requireText("temporalProfile.versioning.taskQueue", declaration.TaskQueue); err != nil {
		return err
	}
	return requireText("temporalProfile.versioning.buildId", declaration.BuildId)
}

// ContinueAsNewDeclaration declares the Continue-As-New boundary (ADR 0018
// §8/§15).
type ContinueAsNewDeclaration struct {
	MaxHistoryEvents int64                  `json:"maxHistoryEvents"`
	MaxHistoryBytes  int64                  `json:"maxHistoryBytes"`
	CarryOver        ContinueAsNewCarryOver `json:"carryOver"`
}

// Validate fails closed on non-positive bounds or an unknown carry-over.
func (declaration ContinueAsNewDeclaration) Validate() error {
	if declaration.MaxHistoryEvents < 1 {
		return fmt.Errorf("engine: temporalProfile.continueAsNew.maxHistoryEvents must be a positive integer")
	}
	if declaration.MaxHistoryBytes < 1 {
		return fmt.Errorf("engine: temporalProfile.continueAsNew.maxHistoryBytes must be a positive integer")
	}
	return declaration.CarryOver.Validate()
}

// PayloadDeclaration declares payload externalization and the payload size
// limit (ADR 0018 §15).
type PayloadDeclaration struct {
	Placement       PayloadPlacement `json:"placement"`
	MaxPayloadBytes int64            `json:"maxPayloadBytes"`
}

// Validate fails closed on an unknown placement or a non-positive limit.
func (declaration PayloadDeclaration) Validate() error {
	if err := declaration.Placement.Validate(); err != nil {
		return err
	}
	if declaration.MaxPayloadBytes < 1 {
		return fmt.Errorf("engine: temporalProfile.payload.maxPayloadBytes must be a positive integer")
	}
	return nil
}

// ActivityDeclaration declares activity heartbeat, cancel and delivery
// retry semantics (ADR 0016 §5, ADR 0018 §8/§15, ADR 0019 §7).
type ActivityDeclaration struct {
	HeartbeatTimeoutSeconds int64                   `json:"heartbeatTimeoutSeconds"`
	InitialIntervalSeconds  int64                   `json:"initialIntervalSeconds"`
	MaxIntervalSeconds      int64                   `json:"maxIntervalSeconds"`
	MaxAttempts             int64                   `json:"maxAttempts"`
	CancelSemantics         ActivityCancelSemantics `json:"cancelSemantics"`
	RetryBudget             DeliveryRetryBudget     `json:"retryBudget"`
}

// Validate fails closed on non-positive bounds, an inconsistent interval
// order or unknown semantics.
func (declaration ActivityDeclaration) Validate() error {
	if declaration.HeartbeatTimeoutSeconds < 1 {
		return fmt.Errorf("engine: temporalProfile.activity.heartbeatTimeoutSeconds must be a positive integer")
	}
	if declaration.InitialIntervalSeconds < 1 {
		return fmt.Errorf("engine: temporalProfile.activity.initialIntervalSeconds must be a positive integer")
	}
	if declaration.MaxIntervalSeconds < declaration.InitialIntervalSeconds {
		return fmt.Errorf("engine: temporalProfile.activity.maxIntervalSeconds must not be below initialIntervalSeconds")
	}
	if declaration.MaxAttempts < 1 {
		return fmt.Errorf("engine: temporalProfile.activity.maxAttempts must be a positive integer")
	}
	if err := declaration.CancelSemantics.Validate(); err != nil {
		return err
	}
	return declaration.RetryBudget.Validate()
}

// TemporalProfile declares the Temporal backend profile (ADR 0016 §5/§7,
// ADR 0018 §8/§15): the selected single authority seam, the workflow
// versioning/build ID policy, the Continue-As-New boundary, payload
// externalization and limit, and activity heartbeat/cancel/retry
// semantics. The profile is a declaration only: this package carries no
// Temporal SDK dependency, never connects to a Temporal service and never
// introduces production storage dependencies (PostgreSQL/S3 belong to M11).
// Replacing or removing the Temporal backend never changes Core lifecycle
// semantics.
type TemporalProfile struct {
	BackendName   string                        `json:"backendName"`
	Seam          SeamChoice                    `json:"seam"`
	Versioning    WorkflowVersioningDeclaration `json:"versioning"`
	ContinueAsNew ContinueAsNewDeclaration      `json:"continueAsNew"`
	Payload       PayloadDeclaration            `json:"payload"`
	Activity      ActivityDeclaration           `json:"activity"`
}

// Validate fails closed on any missing or malformed declaration member.
func (profile TemporalProfile) Validate() error {
	if profile.BackendName != TemporalBackendName {
		return fmt.Errorf("engine: temporalProfile.backendName must be %q", TemporalBackendName)
	}
	if err := profile.Seam.Validate(); err != nil {
		return err
	}
	if err := profile.Versioning.Validate(); err != nil {
		return err
	}
	if err := profile.ContinueAsNew.Validate(); err != nil {
		return err
	}
	if err := profile.Payload.Validate(); err != nil {
		return err
	}
	return profile.Activity.Validate()
}

// AssertSeamChoice guards the frozen M9-e seam selection: the Temporal
// profile must declare the ledger-derived Core command journal. The
// same-transaction outbox alternative was rejected at spec time — the M9-a
// lease ledger is the single atomic sink with a frozen write path, the
// journal derivation by construction excludes the "command delivered but
// ledger not committed" state, and recovery re-derives undelivered
// commands from the ledger without backend internal state — and no outbox
// implementation exists in this repository.
func (profile TemporalProfile) AssertSeamChoice() error {
	if err := profile.Seam.Validate(); err != nil {
		return err
	}
	if profile.Seam != SeamLedgerDerivedJournal {
		return fmt.Errorf("engine: the frozen M9-e seam selection is the ledger-derived Core command journal; seam %q has no implementation in this repository", string(profile.Seam))
	}
	return nil
}

// WithBuildId returns the profile after rolling the worker build identity
// to buildId (the upgrade path): every other declaration stays identical,
// and command identity never depends on the build.
func (profile TemporalProfile) WithBuildId(buildId string) (TemporalProfile, error) {
	if err := requireText("temporalProfile.versioning.buildId", buildId); err != nil {
		return TemporalProfile{}, err
	}
	upgraded := profile
	upgraded.Versioning.BuildId = buildId
	if err := upgraded.Validate(); err != nil {
		return TemporalProfile{}, err
	}
	return upgraded, nil
}

// DefaultTemporalProfile returns the frozen baseline Temporal backend
// profile declaration.
func DefaultTemporalProfile() TemporalProfile {
	return TemporalProfile{
		BackendName: TemporalBackendName,
		Seam:        SeamLedgerDerivedJournal,
		Versioning: WorkflowVersioningDeclaration{
			Strategy:  VersioningStrategyBuildIdTaskQueue,
			TaskQueue: "marshal-durable-engine",
			BuildId:   "temporal-build-1",
		},
		ContinueAsNew: ContinueAsNewDeclaration{
			MaxHistoryEvents: 10000,
			MaxHistoryBytes:  10485760,
			CarryOver:        ContinueAsNewCarryOverJournal,
		},
		Payload: PayloadDeclaration{
			Placement:       PayloadPlacementExternalReference,
			MaxPayloadBytes: 2097152,
		},
		Activity: ActivityDeclaration{
			HeartbeatTimeoutSeconds: 60,
			InitialIntervalSeconds:  1,
			MaxIntervalSeconds:      60,
			MaxAttempts:             10,
			CancelSemantics:         ActivityCancelReportOnly,
			RetryBudget:             DeliveryRetryBudgetBackendOnly,
		},
	}
}

// ValidatePayloadPolicy enforces the declared payload externalization
// policy fail closed: only the external-reference placement is admitted and
// payload bytes above the declared limit are rejected. Payload bytes never
// travel inline through the seam.
func ValidatePayloadPolicy(payloadSize int64, policy PayloadDeclaration) error {
	if err := policy.Validate(); err != nil {
		return err
	}
	if payloadSize < 0 {
		return fmt.Errorf("engine: payload size must not be negative")
	}
	if payloadSize > policy.MaxPayloadBytes {
		return fmt.Errorf("engine: payload of %d bytes exceeds the declared temporalProfile.payload.maxPayloadBytes limit %d", payloadSize, policy.MaxPayloadBytes)
	}
	return nil
}
