package domain

// TaskSpec is the immutable, authoritative description of one Marshal Task:
// what to do, under which scope, budgets and acceptance criteria.
type TaskSpec struct {
	APIVersion   APIVersion        `json:"apiVersion"`
	Kind         Kind              `json:"kind"`
	Metadata     TaskMetadata      `json:"metadata"`
	Repository   TaskRepository    `json:"repository"`
	Work         TaskWork          `json:"work"`
	Scope        TaskScope         `json:"scope"`
	Acceptance   TaskAcceptance    `json:"acceptance"`
	Deliverables []TaskDeliverable `json:"deliverables"`
	Worker       TaskWorker        `json:"worker"`
	Budgets      TaskBudgets       `json:"budgets"`
	Publication  TaskPublication   `json:"publication"`
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
	RunTimeoutSeconds     int64 `json:"runTimeoutSeconds"`
	AttemptTimeoutSeconds int64 `json:"attemptTimeoutSeconds"`
	MaxAttempts           int   `json:"maxAttempts"`
	MaxOperationalRetries int   `json:"maxOperationalRetries"`
	MaxReworkRounds       int   `json:"maxReworkRounds"`
	MaxOutputBytes        int64 `json:"maxOutputBytes"`
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
	RequiredChecks []string `json:"requiredChecks"`
}
