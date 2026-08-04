package domain

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

type TaskWorker struct {
	PreferredAdapter string   `json:"preferredAdapter"`
	FallbackAdapters []string `json:"fallbackAdapters"`
	ExecutionProfile string   `json:"executionProfile"`
	SessionPolicy    string   `json:"sessionPolicy"`
	Model            string   `json:"model,omitempty"`
	Reasoning        string   `json:"reasoning,omitempty"`
}

type TaskWork struct {
	Objective   string   `json:"objective"`
	Constraints []string `json:"constraints"`
	NonGoals    []string `json:"nonGoals"`
}

type TaskMetadata struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type TaskRepository struct {
	Path    string `json:"path"`
	BaseRef string `json:"baseRef"`
	Remote  string `json:"remote"`
}

type TaskScope struct {
	AllowPaths      []string `json:"allowPaths"`
	DenyPaths       []string `json:"denyPaths"`
	AllowSubmodules bool     `json:"allowSubmodules"`
	MaxChangedFiles int      `json:"maxChangedFiles"`
	MaxDiffBytes    int64    `json:"maxDiffBytes"`
}

type TaskAcceptance struct {
	Commands      []TaskCommand `json:"commands"`
	AllowNoChange bool          `json:"allowNoChange"`
}

type TaskCommand struct {
	ID             string   `json:"id"`
	Argv           []string `json:"argv"`
	CWD            string   `json:"cwd"`
	TimeoutSeconds int64    `json:"timeoutSeconds"`
	Required       bool     `json:"required"`
	BaselinePolicy string   `json:"baselinePolicy"`
	MaxLogBytes    int64    `json:"maxLogBytes"`
}

type TaskDeliverable struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	Required     bool   `json:"required"`
	PathGlob     string `json:"pathGlob"`
	MediaType    string `json:"mediaType"`
	MinimumCount int    `json:"minimumCount"`
	Description  string `json:"description"`
}

type TaskBudgets struct {
	RunTimeoutSeconds     int64 `json:"runTimeoutSeconds"`
	AttemptTimeoutSeconds int64 `json:"attemptTimeoutSeconds"`
	MaxAttempts           int   `json:"maxAttempts"`
	MaxOperationalRetries int   `json:"maxOperationalRetries"`
	MaxReworkRounds       int   `json:"maxReworkRounds"`
	MaxOutputBytes        int64 `json:"maxOutputBytes"`
}

type TaskPublication struct {
	Required       bool     `json:"required"`
	Provider       string   `json:"provider"`
	Mode           string   `json:"mode"`
	Remote         string   `json:"remote"`
	BaseBranch     string   `json:"baseBranch"`
	MergePolicy    string   `json:"mergePolicy"`
	RequiredChecks []string `json:"requiredChecks"`
}
