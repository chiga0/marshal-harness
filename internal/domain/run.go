package domain

import (
	"time"
)

type RunState struct {
	APIVersion             APIVersion `json:"apiVersion"`
	Kind                   Kind       `json:"kind"`
	TaskID                 string     `json:"taskId"`
	RunID                  string     `json:"runId"`
	State                  State      `json:"state"`
	Sequence               uint64     `json:"sequence"`
	SpecDigest             string     `json:"specDigest,omitempty"`
	PolicyDigest           string     `json:"policyDigest,omitempty"`
	CapabilityDigest       string     `json:"capabilityDigest,omitempty"`
	BaseSHA                string     `json:"baseSha,omitempty"`
	WorktreePath           string     `json:"worktreePath,omitempty"`
	CurrentAttemptID       string     `json:"currentAttemptId,omitempty"`
	ReviewRound            uint       `json:"reviewRound"`
	AttemptsUsed           uint       `json:"attemptsUsed"`
	OperationalRetriesUsed uint       `json:"operationalRetriesUsed"`
	ReworkRoundsUsed       uint       `json:"reworkRoundsUsed"`
	TerminalReason         string     `json:"terminalReason,omitempty"`
	CreatedAt              time.Time  `json:"createdAt"`
	UpdatedAt              time.Time  `json:"updatedAt"`
}

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

type Actor struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

func NewRunState(taskID, runID string, now time.Time) RunState {
	return RunState{APIVersion: APIVersionV1Alpha1, Kind: KindRunState, TaskID: taskID, RunID: runID, State: StateCreated, CreatedAt: now.UTC(), UpdatedAt: now.UTC()}
}
