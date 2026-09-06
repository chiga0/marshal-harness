package planning

import (
	"bytes"
	"encoding/json"
	"time"

	"github.com/chiga0/marshal-harness/internal/adapter"
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
// accepted as a substitute for Prepare. In particular, TaskSpec's complete JSON
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
