package verification

import (
	"time"

	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/evidencebinding"
)

type Change struct {
	Status         string `json:"status"`
	OldPath        string `json:"oldPath,omitempty"`
	Path           string `json:"path"`
	Mode           uint32 `json:"mode,omitempty"`
	ByteSize       int64  `json:"byteSize,omitempty"`
	Digest         string `json:"digest,omitempty"`
	Symlink        bool   `json:"symlink,omitempty"`
	SymlinkEscapes bool   `json:"symlinkEscapes,omitempty"`
	Submodule      bool   `json:"submodule,omitempty"`
}

type Observation struct {
	SnapshotDigest    string   `json:"snapshotDigest"`
	DiffDigest        string   `json:"diffDigest"`
	ChangedFiles      []string `json:"changedFiles"`
	ChangedFileCount  int      `json:"changedFileCount"`
	DiffBytes         int64    `json:"diffBytes"`
	HasUntrackedFiles bool     `json:"hasUntrackedFiles"`
	Changes           []Change `json:"-"`
	Patch             []byte   `json:"-"`
	DiffTruncated     bool     `json:"-"`
}

type Gate struct {
	ID       string         `json:"id"`
	Category string         `json:"category"`
	Required bool           `json:"required"`
	Status   string         `json:"status"`
	Summary  string         `json:"summary"`
	Evidence []string       `json:"evidence"`
	Command  *CommandRecord `json:"command,omitempty"`
}

type CommandRecord struct {
	Argv                 []string  `json:"argv"`
	CWD                  string    `json:"cwd"`
	Executable           string    `json:"executable"`
	StartedAt            time.Time `json:"startedAt"`
	CompletedAt          time.Time `json:"completedAt"`
	ExitCode             *int      `json:"exitCode"`
	Signal               *string   `json:"signal"`
	DurationMilliseconds int64     `json:"durationMilliseconds"`
	Truncated            bool      `json:"truncated"`
	BaselineStatus       string    `json:"baselineStatus,omitempty"`
}

type Report struct {
	APIVersion domain.APIVersion `json:"apiVersion"`
	Kind       domain.Kind       `json:"kind"`
	TaskID     string            `json:"taskId"`
	RunID      string            `json:"runId"`
	SpecDigest string            `json:"specDigest"`
	BaseSHA    string            `json:"baseSha"`
	Observed   Observation       `json:"observed"`
	// WorkerCandidateDigest is the ADR 0027 worker Candidate record identity
	// (chain root of this verification); optional, present only in candidate
	// mode. Legacy reports omit it byte-for-byte.
	WorkerCandidateDigest string `json:"workerCandidateDigest,omitempty"`
	// CandidateDigest is the head Candidate record identity: the normalizer
	// Candidate when normalization changed bytes, otherwise the worker
	// Candidate. Optional; observed/report/manifest bindings all point at it.
	CandidateDigest string `json:"candidateDigest,omitempty"`
	// LocalSelfIdentityBinding is Core-owned ADR 0051 applicability lineage.
	// Legacy/non-local reports omit it byte-for-byte.
	LocalSelfIdentityBinding *evidencebinding.VerificationIdentityBindingV1 `json:"localSelfIdentityBinding,omitempty"`
	Status                   string                                         `json:"status"`
	Gates                    []Gate                                         `json:"gates"`
	Summary                  string                                         `json:"summary,omitempty"`
	StartedAt                time.Time                                      `json:"startedAt"`
	CompletedAt              time.Time                                      `json:"completedAt"`
}

type ArtifactManifest struct {
	APIVersion               domain.APIVersion                              `json:"apiVersion"`
	Kind                     domain.Kind                                    `json:"kind"`
	TaskID                   string                                         `json:"taskId"`
	RunID                    string                                         `json:"runId"`
	LocalSelfIdentityBinding *evidencebinding.VerificationIdentityBindingV1 `json:"localSelfIdentityBinding,omitempty"`
	Artifacts                []Artifact                                     `json:"artifacts"`
	GeneratedAt              time.Time                                      `json:"generatedAt"`
}

type Artifact struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	MediaType    string `json:"mediaType,omitempty"`
	Producer     string `json:"producer"`
	Required     bool   `json:"required"`
	Status       string `json:"status"`
	PathRoot     string `json:"pathRoot,omitempty"`
	RelativePath string `json:"relativePath,omitempty"`
	ByteSize     int64  `json:"byteSize"`
	Digest       string `json:"digest,omitempty"`
	// CandidateDigest binds the artifact to the ADR 0027 Candidate record
	// whose content it carries; optional, present only in candidate mode.
	CandidateDigest string    `json:"candidateDigest,omitempty"`
	CreatedAt       time.Time `json:"createdAt"`
	Redacted        bool      `json:"redacted"`
	Truncated       bool      `json:"truncated"`
	RelatedGates    []string  `json:"relatedGates"`
	Description     string    `json:"description,omitempty"`
}

type ScopePolicy struct {
	AllowPaths      []string
	DenyPaths       []string
	AllowSubmodules bool
	MaxChangedFiles int
	MaxDiffBytes    int64
}

type Deliverable struct {
	ID           string
	Kind         string
	Required     bool
	PathGlob     string
	MediaType    string
	MinimumCount int
	Description  string
}

type CommandSpec struct {
	ID             string
	Argv           []string
	CWD            string
	Timeout        time.Duration
	Required       bool
	MaxLogBytes    int64
	BaselinePolicy string
}

type CommandResult struct {
	Record          CommandRecord
	Status          string
	Stdout          []byte
	Stderr          []byte
	StdoutTruncated bool
	StderrTruncated bool
	StartedAt       time.Time
	EndedAt         time.Time
}
