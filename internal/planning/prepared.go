package planning

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/chiga0/marshal-harness/internal/adapter"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
)

// PreparedPlan is process-local validated planning state, not approval,
// durable authority or a transport type. All fields are private so neither an
// input adapter nor a caller mutating Inputs can replace the validated bytes.
type PreparedPlan struct {
	input               Input
	task                domain.TaskSpec
	effective           EffectivePolicy
	selection           adapter.Selection
	repositoryRoot      string
	remoteURL           string
	baseSHA             string
	now                 time.Time
	taskCanonical       []byte
	policyCanonical     []byte
	capabilityCanonical []byte
	specDigest          string
	policyDigest        string
	capabilityDigest    string
}

// PreparedInputs is a lossless value for the privileged composition root to
// bind in its creation obligation. It is not itself a receipt and is never
// accepted as approval. RestorePrepared must revalidate it through the same
// preparation pipeline. In particular, TaskSpec's complete JSON
// is preserved rather than re-encoding the deliberately partial domain type.
type PreparedInputs struct {
	RunID             string                     `json:"runId"`
	RepositoryRoot    string                     `json:"repositoryRoot"`
	BaseSHA           string                     `json:"baseSha"`
	PreparedAt        time.Time                  `json:"preparedAt"`
	Task              json.RawMessage            `json:"task"`
	Policy            json.RawMessage            `json:"policy"`
	Capability        json.RawMessage            `json:"capability"`
	SelectionAttempts []adapter.SelectionAttempt `json:"selectionAttempts"`
}

// Inputs returns detached copies. It exposes no filesystem handle, selector,
// executable or mutable reference to the validated preparation.
func (prepared *PreparedPlan) Inputs() PreparedInputs {
	if prepared == nil {
		return PreparedInputs{}
	}
	return PreparedInputs{
		RunID: prepared.input.RunID, RepositoryRoot: prepared.repositoryRoot,
		BaseSHA: prepared.baseSHA, PreparedAt: prepared.now,
		Task:              bytes.Clone(prepared.taskCanonical),
		Policy:            bytes.Clone(prepared.policyCanonical),
		Capability:        bytes.Clone(prepared.capabilityCanonical),
		SelectionAttempts: append([]adapter.SelectionAttempt(nil), prepared.selection.Attempts...),
	}
}

// RestorePrepared revalidates a durable creation input through the same
// planning pipeline, retaining its original timestamp and capability bytes.
// It performs no probe, Run write or worktree creation. Preconditions and
// interpreter preflight are rechecked; a changed environment blocks recovery.
//
// This is an internal composition seam, NOT receipt authentication. The
// controller must obtain frozen from current RB1 authority and recheck that
// authority before Run writes. Raw HTTP/client input must never call this
// function. Create still rejects existing Runs; partial creation recovery is
// a separate responsibility and must not delete/recreate a conflicting Run.
func RestorePrepared(ctx context.Context, input Input, frozen PreparedInputs) (*PreparedPlan, error) {
	invalid := errors.New("planning: invalid or changed frozen preparation")
	if ctx == nil || frozen.PreparedAt.IsZero() || frozen.PreparedAt.Location() != time.UTC ||
		(!input.Now.IsZero() && !input.Now.Equal(frozen.PreparedAt)) ||
		input.RunID != frozen.RunID ||
		len(frozen.Task) > 128<<10 || len(frozen.Policy) > 128<<10 || len(frozen.Capability) > 64<<10 ||
		len(frozen.SelectionAttempts) != 1 {
		return nil, invalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Prepare freezes a canonical repository. Darwin's /var and /private/var
	// aliases must identify the same repository here too, not fail on spelling.
	repositoryRoot, err := canonicalPath(input.RepositoryRoot)
	if err != nil || repositoryRoot != frozen.RepositoryRoot {
		return nil, invalid
	}
	// Caller-provided buffers and the value read from the ledger must describe
	// the same exact canonical input, not merely the same partial domain model.
	task, err := canonical.JSON(input.TaskSpec)
	if err != nil || !bytes.Equal(task, frozen.Task) {
		return nil, invalid
	}
	policy, err := canonical.JSON(input.PolicySnapshot)
	if err != nil || !bytes.Equal(policy, frozen.Policy) {
		return nil, invalid
	}
	capability, err := canonical.JSON(frozen.Capability)
	if err != nil || !bytes.Equal(capability, frozen.Capability) {
		return nil, invalid
	}
	var taskIdentity struct {
		Repository struct {
			BaseRef           string `json:"baseRef"`
			ExpectedRemoteURL string `json:"expectedRemoteUrl"`
		} `json:"repository"`
	}
	if json.Unmarshal(task, &taskIdentity) != nil || taskIdentity.Repository.BaseRef != frozen.BaseSHA || taskIdentity.Repository.ExpectedRemoteURL == "" {
		return nil, invalid
	}
	frozen.Task, frozen.Policy, frozen.Capability = task, policy, capability
	frozen.SelectionAttempts = append([]adapter.SelectionAttempt(nil), frozen.SelectionAttempts...)
	input.TaskSpec, input.PolicySnapshot, input.Now = task, policy, frozen.PreparedAt
	prepared, _, err := prepare(ctx, input, &frozen)
	if err != nil {
		return nil, err
	}
	want, err := json.Marshal(frozen)
	if err != nil {
		return nil, invalid
	}
	actual, err := json.Marshal(prepared.Inputs())
	if err != nil || !bytes.Equal(want, actual) {
		return nil, invalid
	}
	return prepared, nil
}
