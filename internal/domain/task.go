package domain

type TaskSpec struct {
	APIVersion   APIVersion        `json:"apiVersion"`
	Kind         Kind              `json:"kind"`
	Metadata     TaskMetadata      `json:"metadata"`
	Repository   TaskRepository    `json:"repository"`
	Scope        TaskScope         `json:"scope"`
	Acceptance   TaskAcceptance    `json:"acceptance"`
	Deliverables []TaskDeliverable `json:"deliverables"`
	Budgets      TaskBudgets       `json:"budgets"`
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
	RunTimeoutSeconds int64 `json:"runTimeoutSeconds"`
	MaxOutputBytes    int64 `json:"maxOutputBytes"`
}
