package domain

import "time"

const (
	ApprovalGatePlan    = "plan"
	ApprovalGatePublish = "publish"
)

const (
	ApprovalOutcomeApproved = "approved"
)

const (
	ControlSourceTypeHuman        = "human"
	ControlSourceTypeLeadAgent    = "lead-agent"
	ControlSourceTypeTerminalHook = "terminal-hook"
)

const (
	InterventionCategoryClarification            = "clarification"
	InterventionCategoryImplementationCorrection = "implementation-correction"
	InterventionCategoryScopeChange              = "scope-change"
	InterventionCategoryManualPTY                = "manual-pty"
	InterventionCategoryPause                    = "pause"
	InterventionCategoryResume                   = "resume"
	InterventionCategoryAbort                    = "abort"
)

const (
	InterventionEffectContinue               = "continue"
	InterventionEffectNewRunRequired         = "new-run-required"
	InterventionEffectRequiredReverification = "required-reverification"
	InterventionEffectPaused                 = "paused"
	InterventionEffectResumed                = "resumed"
	InterventionEffectAbortRequested         = "abort-requested"
)

// ApprovalRecord is an append-only authorization for one Gate, bound to the
// exact digests, Base SHA and State Sequence that were in effect when the
// human approved. Workers cannot create ApprovalRecords.
type ApprovalRecord struct {
	APIVersion      APIVersion      `json:"apiVersion"`
	Kind            Kind            `json:"kind"`
	RecordID        string          `json:"recordId"`
	TaskID          string          `json:"taskId"`
	RunID           string          `json:"runId"`
	ControlSequence uint64          `json:"controlSequence"`
	Gate            string          `json:"gate"`
	Source          ControlSource   `json:"source"`
	Binding         ApprovalBinding `json:"binding"`
	Outcome         string          `json:"outcome"`
	CreatedAt       time.Time       `json:"createdAt"`
}

// ControlSource identifies who issued a control record.
type ControlSource struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// ApprovalBinding pins an ApprovalRecord to the frozen inputs and evidence
// generation it authorizes.
type ApprovalBinding struct {
	StateSequence    uint64 `json:"stateSequence"`
	SpecDigest       string `json:"specDigest"`
	PolicyDigest     string `json:"policyDigest"`
	CapabilityDigest string `json:"capabilityDigest"`
	BaseSHA          string `json:"baseSha"`
	ReviewRound      uint   `json:"reviewRound,omitempty"`
	DecisionDigest   string `json:"decisionDigest,omitempty"`
	EvidenceDigest   string `json:"evidenceDigest,omitempty"`
}

// InterventionRecord is an append-only account of one human or Lead control
// action: its category, the Attempt it touched, its content digest and its
// required effect on the Run.
type InterventionRecord struct {
	APIVersion        APIVersion    `json:"apiVersion"`
	Kind              Kind          `json:"kind"`
	RecordID          string        `json:"recordId"`
	TaskID            string        `json:"taskId"`
	RunID             string        `json:"runId"`
	ControlSequence   uint64        `json:"controlSequence"`
	StateSequence     uint64        `json:"stateSequence"`
	AttemptID         string        `json:"attemptId,omitempty"`
	Category          string        `json:"category"`
	Source            ControlSource `json:"source"`
	Effect            string        `json:"effect"`
	Instruction       string        `json:"instruction,omitempty"`
	InstructionDigest string        `json:"instructionDigest,omitempty"`
	SteeringRound     uint          `json:"steeringRound,omitempty"`
	CreatedAt         time.Time     `json:"createdAt"`
}
