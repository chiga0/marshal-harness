package domain

import "time"

const (
	PublicationProviderGitHub = "github"
	PublicationModeDraft      = "draft"
	MergePolicyNever          = "never"
	MergePolicyManual         = "manual"
	MergePolicyPolicy         = "policy"
)

const (
	CheckStatusPending  = "pending"
	CheckStatusPass     = "pass"
	CheckStatusFail     = "fail"
	CheckStatusMissing  = "missing"
	CheckStatusSkipping = "skipping"
	CheckStatusCancel   = "cancel"
)

// PublicationIntent is saved atomically before any remote side effect so a
// crashed Publisher can reconcile against Branch, Marker and Provider ID.
// It never carries Tokens, publisher config dirs, or absolute worktree paths.
type PublicationIntent struct {
	APIVersion           APIVersion `json:"apiVersion"`
	Kind                 Kind       `json:"kind"`
	TaskID               string     `json:"taskId"`
	RunID                string     `json:"runId"`
	Provider             string     `json:"provider"`
	Repository           string     `json:"repository"`
	Remote               string     `json:"remote"`
	RemoteURL            string     `json:"remoteUrl"`
	BaseBranch           string     `json:"baseBranch"`
	HeadBranch           string     `json:"headBranch"`
	ReviewRound          uint       `json:"reviewRound"`
	BaseSHA              string     `json:"baseSha"`
	PreviousHeadSHA      string     `json:"previousHeadSha,omitempty"`
	CommitSHA            string     `json:"commitSha"`
	SnapshotDigest       string     `json:"snapshotDigest"`
	DiffDigest           string     `json:"diffDigest"`
	SpecDigest           string     `json:"specDigest"`
	PolicyDigest         string     `json:"policyDigest"`
	EvidenceDigest       string     `json:"evidenceDigest"`
	VerificationDigest   string     `json:"verificationDigest"`
	ReviewDecisionDigest string     `json:"reviewDecisionDigest"`
	Marker               string     `json:"marker"`
	Mode                 string     `json:"mode"`
	MergePolicy          string     `json:"mergePolicy"`
	AttemptID            string     `json:"attemptId,omitempty"`
	Summary              string     `json:"summary,omitempty"`
	CreatedAt            time.Time  `json:"createdAt"`
}

type PublicationRecord struct {
	APIVersion           APIVersion            `json:"apiVersion"`
	Kind                 Kind                  `json:"kind"`
	TaskID               string                `json:"taskId"`
	RunID                string                `json:"runId"`
	Provider             string                `json:"provider"`
	Repository           PublicationRepository `json:"repository"`
	Remote               string                `json:"remote"`
	BaseBranch           string                `json:"baseBranch"`
	HeadBranch           string                `json:"headBranch"`
	ReviewRound          uint                  `json:"reviewRound"`
	BaseSHA              string                `json:"baseSha"`
	PreviousHeadSHA      string                `json:"previousHeadSha,omitempty"`
	HeadSHA              string                `json:"headSha"`
	CommitSHA            string                `json:"commitSha"`
	SnapshotDigest       string                `json:"snapshotDigest"`
	DiffDigest           string                `json:"diffDigest"`
	SpecDigest           string                `json:"specDigest"`
	PolicyDigest         string                `json:"policyDigest"`
	EvidenceDigest       string                `json:"evidenceDigest"`
	VerificationDigest   string                `json:"verificationDigest"`
	ReviewDecisionDigest string                `json:"reviewDecisionDigest"`
	Marker               string                `json:"marker"`
	Mode                 string                `json:"mode"`
	MergePolicy          string                `json:"mergePolicy"`
	Request              PullRequestRecord     `json:"request"`
	Actor                string                `json:"actor"`
	PublishedAt          time.Time             `json:"publishedAt"`
	CIDeadline           *time.Time            `json:"ciDeadline,omitempty"`
	UpdatedAt            time.Time             `json:"updatedAt"`
}

type PublicationRepository struct {
	ID            string `json:"id"`
	NameWithOwner string `json:"nameWithOwner"`
	URL           string `json:"url"`
}

type PullRequestRecord struct {
	ID     string `json:"id"`
	Number int    `json:"number"`
	URL    string `json:"url"`
	Draft  bool   `json:"draft"`
	State  string `json:"state"`
}

// RemoteCheckRecord observes remote checks bound to a Repository ID, a PR ID
// and the exact head SHA; green checks from a stale SHA are not valid.
type RemoteCheckRecord struct {
	APIVersion   APIVersion    `json:"apiVersion"`
	Kind         Kind          `json:"kind"`
	TaskID       string        `json:"taskId"`
	RunID        string        `json:"runId"`
	Provider     string        `json:"provider"`
	RepositoryID string        `json:"repositoryId"`
	RequestID    string        `json:"requestId"`
	HeadSHA      string        `json:"headSha"`
	Status       string        `json:"status"`
	Checks       []RemoteCheck `json:"checks"`
	ObservedAt   time.Time     `json:"observedAt"`
}

type RemoteCheck struct {
	Name        string     `json:"name"`
	Required    bool       `json:"required"`
	Status      string     `json:"status"`
	URL         string     `json:"url,omitempty"`
	CompletedAt *time.Time `json:"completedAt,omitempty"`
}
