package domain

import "encoding/json"

// Closed admission.status vocabulary and dependsOn kind vocabulary declared
// by the TaskSpec Schema (issue #23). The zero values carry the backward-
// compatible semantics: an absent admission declaration is executable and an
// empty dependsOn or preconditions list imposes no restriction.
const (
	// AdmissionStatusPrepared marks a TaskSpec that is declared but not yet
	// executable: planning rejects it fail-closed with a fixed sentinel.
	AdmissionStatusPrepared = "prepared"
	// AdmissionStatusExecutable marks a TaskSpec that planning may execute.
	AdmissionStatusExecutable = "executable"

	// DependencyKindRun binds a dependsOn entry to one named Run.
	DependencyKindRun = "run"
	// DependencyKindTask binds a dependsOn entry to the latest Run of a Task.
	DependencyKindTask = "task"
)

// TaskSpec is the immutable, authoritative description of one Marshal Task:
// what to do, under which scope, budgets and acceptance criteria.
type TaskSpec struct {
	APIVersion    APIVersion         `json:"apiVersion"`
	Kind          Kind               `json:"kind"`
	Metadata      TaskMetadata       `json:"metadata"`
	Repository    TaskRepository     `json:"repository"`
	Work          TaskWork           `json:"work"`
	Scope         TaskScope          `json:"scope"`
	Acceptance    TaskAcceptance     `json:"acceptance"`
	Deliverables  []TaskDeliverable  `json:"deliverables"`
	Worker        TaskWorker         `json:"worker"`
	Budgets       TaskBudgets        `json:"budgets"`
	Publication   TaskPublication    `json:"publication"`
	Admission     TaskAdmission      `json:"admission"`
	DependsOn     []TaskDependency   `json:"dependsOn,omitempty"`
	Preconditions []TaskPrecondition `json:"preconditions,omitempty"`
}

// TaskWorker selects the Worker Adapter configuration for executing the Task.
type TaskWorker struct {
	PreferredAdapter string   `json:"preferredAdapter"`
	FallbackAdapters []string `json:"fallbackAdapters"`
	ExecutionProfile string   `json:"executionProfile"`
	SessionPolicy    string   `json:"sessionPolicy"`
	Model            string   `json:"model,omitempty"`
	Reasoning        string   `json:"reasoning,omitempty"`
	// Tools is the optional declarative tool allowlist (closed vocabulary
	// read/edit/write/grep/find/ls/bash). Empty means the Adapter keeps its
	// frozen execution-profile tool surface; when declared, the Adapter
	// enforces it mechanically at the Provider call layer.
	Tools []string `json:"tools,omitempty"`
}

// TaskWork describes the actual work content: objective, constraints and
// explicit non-goals.
type TaskWork struct {
	Objective   string   `json:"objective"`
	Constraints []string `json:"constraints"`
	NonGoals    []string `json:"nonGoals"`
}

// TaskMetadata carries the Task's identity and human-readable title.
type TaskMetadata struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// TaskRepository identifies the target repository, its locked base ref and
// the expected remote used to verify repository identity.
type TaskRepository struct {
	Path              string `json:"path"`
	BaseRef           string `json:"baseRef"`
	Remote            string `json:"remote"`
	ExpectedRemoteURL string `json:"expectedRemoteUrl,omitempty"`
}

// TaskScope bounds where and how much the Worker may change: allowed and
// denied paths, and limits on changed files and diff size.
type TaskScope struct {
	AllowPaths      []string `json:"allowPaths"`
	DenyPaths       []string `json:"denyPaths"`
	AllowSubmodules bool     `json:"allowSubmodules"`
	MaxChangedFiles int      `json:"maxChangedFiles"`
	MaxDiffBytes    int64    `json:"maxDiffBytes"`
}

// TaskAcceptance defines the acceptance commands executed by the Verifier
// and whether a no-change Run is allowed.
type TaskAcceptance struct {
	Commands      []TaskCommand `json:"commands"`
	AllowNoChange bool          `json:"allowNoChange"`
}

// TaskCommand is one acceptance command with timeout, baseline policy and
// log capture limits.
type TaskCommand struct {
	ID             string   `json:"id"`
	Argv           []string `json:"argv"`
	CWD            string   `json:"cwd"`
	TimeoutSeconds int64    `json:"timeoutSeconds"`
	Required       bool     `json:"required"`
	BaselinePolicy string   `json:"baselinePolicy"`
	MaxLogBytes    int64    `json:"maxLogBytes"`
}

// TaskDeliverable declares one expected output produced by the Task.
type TaskDeliverable struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	Required     bool   `json:"required"`
	PathGlob     string `json:"pathGlob"`
	MediaType    string `json:"mediaType"`
	MinimumCount int    `json:"minimumCount"`
	Description  string `json:"description"`
}

// TaskBudgets caps the time, attempts, retries and output a Task may consume.
type TaskBudgets struct {
	RunTimeoutSeconds       int64 `json:"runTimeoutSeconds"`
	CIObserveTimeoutSeconds int64 `json:"ciObserveTimeoutSeconds,omitempty"`
	AttemptTimeoutSeconds   int64 `json:"attemptTimeoutSeconds"`
	MaxAttempts             int   `json:"maxAttempts"`
	MaxOperationalRetries   int   `json:"maxOperationalRetries"`
	MaxReworkRounds         int   `json:"maxReworkRounds"`
	MaxOutputBytes          int64 `json:"maxOutputBytes"`
}

// TaskAdmission declares the machine-executable admission status of the
// TaskSpec. The zero value carries backward-compatible semantics: a TaskSpec
// frozen without an admission object decodes to the zero TaskAdmission and
// planning treats it as executable.
type TaskAdmission struct {
	Status string `json:"status"`
}

// MarshalJSON normalizes freshly constructed Tasks: an unset admission status
// defaults to AdmissionStatusPrepared so every marshaled TaskSpec carries a
// legal closed-enum status and existing fixtures and legacy Tasks keep
// passing the TaskSpec schema. Planning semantics key off the frozen JSON: a
// spec written without an admission object still decodes to the zero value
// and stays executable.
func (admission TaskAdmission) MarshalJSON() ([]byte, error) {
	if admission.Status == "" {
		admission.Status = AdmissionStatusPrepared
	}
	type normalized TaskAdmission
	return json.Marshal(normalized(admission))
}

// TaskDependency declares one dependsOn entry: a run-scoped dependency names
// the depended-on Run directly, a task-scoped dependency resolves the
// depended-on Task's latest Run at planning time. RequiredState is one of the
// five terminal states; BaseSHA and SpecDigest optionally pin the depended-on
// Run's frozen values, an empty value disables the corresponding check.
type TaskDependency struct {
	Kind          string `json:"kind"`
	RunID         string `json:"runId,omitempty"`
	TaskID        string `json:"taskId,omitempty"`
	RequiredState string `json:"requiredState"`
	BaseSHA       string `json:"baseSha,omitempty"`
	SpecDigest    string `json:"specDigest,omitempty"`
}

// TaskPrecondition declares one planning-time precondition: a controlled
// command executed at the repository root before planning creates any side
// effect. Every precondition is required; any non-zero exit rejects the plan
// fail-closed.
type TaskPrecondition struct {
	ID             string   `json:"id"`
	Argv           []string `json:"argv"`
	CWD            string   `json:"cwd,omitempty"`
	TimeoutSeconds int64    `json:"timeoutSeconds,omitempty"`
}

// TaskPublication describes how and where the Run's result may be
// published; mergePolicy "never" keeps merge disabled by default.
type TaskPublication struct {
	Required       bool     `json:"required"`
	Provider       string   `json:"provider"`
	Mode           string   `json:"mode"`
	Remote         string   `json:"remote"`
	BaseBranch     string   `json:"baseBranch"`
	MergePolicy    string   `json:"mergePolicy"`
	MergeMethod    string   `json:"mergeMethod,omitempty"`
	RequiredChecks []string `json:"requiredChecks"`
}
