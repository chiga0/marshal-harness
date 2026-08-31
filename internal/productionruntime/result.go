package productionruntime

import (
	"context"
	"errors"
	"time"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/domain"
)

var ErrAttemptStillRunning = errors.New("productionruntime: attempt is still running")

// AttemptResultInput is the path-free parser boundary between production
// runtime and the selected AgentProvider adapter.
type AttemptResultInput struct {
	Transcript     []byte
	Worktree       string
	TaskID         string
	RunID          string
	AttemptID      string
	Executable     string
	Version        string
	StartedAt      time.Time
	CompletedAt    time.Time
	MaxOutputBytes int64
}

type AttemptResultParser func(context.Context, AttemptResultInput) (domain.Record, error)

type CollectedRunResult struct {
	Run                 application.RunProjection
	WorkerResult        domain.Record
	AdmissionFactDigest string
	DRCDigest           string
	EnvelopeDigest      string
}
