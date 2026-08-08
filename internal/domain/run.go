package domain

import (
	"time"
)

// RunState is the atomic snapshot of one Run: its lifecycle state, sequence
// number, budget usage and associated resources.
type RunState struct {
	APIVersion             APIVersion      `json:"apiVersion"`
	Kind                   Kind            `json:"kind"`
	TaskID                 string          `json:"taskId"`
	RunID                  string          `json:"runId"`
	State                  State           `json:"state"`
	Sequence               uint64          `json:"sequence"`
	SpecDigest             string          `json:"specDigest,omitempty"`
	PolicyDigest           string          `json:"policyDigest,omitempty"`
	CapabilityDigest       string          `json:"capabilityDigest,omitempty"`
	BaseSHA                string          `json:"baseSha,omitempty"`
	WorktreePath           string          `json:"worktreePath,omitempty"`
	Publication            *RunPublication `json:"publication,omitempty"`
	CurrentAttemptID       string          `json:"currentAttemptId,omitempty"`
	ReviewRound            uint            `json:"reviewRound"`
	AttemptsUsed           uint            `json:"attemptsUsed"`
	OperationalRetriesUsed uint            `json:"operationalRetriesUsed"`
	ReworkRoundsUsed       uint            `json:"reworkRoundsUsed"`
	TerminalReason         string          `json:"terminalReason,omitempty"`
	CreatedAt              time.Time       `json:"createdAt"`
	UpdatedAt              time.Time       `json:"updatedAt"`
}

// RunPublication records where and how a Run was published, kept idempotent
// across retries.
type RunPublication struct {
	Provider   string `json:"provider"`
	Repository string `json:"repository"`
	HeadBranch string `json:"headBranch"`
	BaseBranch string `json:"baseBranch"`
	ExternalID string `json:"externalId,omitempty"`
	URI        string `json:"uri,omitempty"`
	HeadSHA    string `json:"headSha,omitempty"`
}

// RunEvent is one append-only entry in a Run's Journal, recording a state
// transition or other observable fact.
type RunEvent struct {
	APIVersion APIVersion     `json:"apiVersion"`
	Kind       Kind           `json:"kind"`
	EventID    string         `json:"eventId"`
	RunID      string         `json:"runId"`
	AttemptID  string         `json:"attemptId,omitempty"`
	Sequence   uint64         `json:"sequence"`
	Type       string         `json:"type"`
	StateFrom  State          `json:"stateFrom,omitempty"`
	StateTo    State          `json:"stateTo,omitempty"`
	Timestamp  time.Time      `json:"timestamp"`
	Actor      *Actor         `json:"actor,omitempty"`
	Payload    map[string]any `json:"payload"`
}

// Actor identifies the principal that caused a RunEvent.
type Actor struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// NewRunState returns the initial RunState snapshot for a freshly created Run.
func NewRunState(taskID, runID string, now time.Time) RunState {
	return RunState{APIVersion: APIVersionV1Alpha1, Kind: KindRunState, TaskID: taskID, RunID: runID, State: StateCreated, CreatedAt: now.UTC(), UpdatedAt: now.UTC()}
}
