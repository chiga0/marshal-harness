package sandbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/dispatch"
)

var (
	// ErrInvalidWorkloadRole is returned when a workload role falls outside
	// the closed enumeration.
	ErrInvalidWorkloadRole = errors.New("sandbox: invalid workloadRole")
	// ErrInvalidOperationIdentity is returned when any operation identity
	// field is missing or malformed. Every dispatch-bound SPI request fails
	// closed with this sentinel before any provider side effect.
	ErrInvalidOperationIdentity = errors.New("sandbox: invalid operation identity")
)

// WorkloadRole is the closed enumeration of workload roles that may hold a
// dispatch-bound operation identity (ADR 0017 §4). publisher is explicitly
// not a member: publication flows never execute inside a sandbox and Validate
// rejects them.
type WorkloadRole string

// Closed members of WorkloadRole.
const (
	WorkloadRoleWorker   WorkloadRole = "worker"
	WorkloadRoleVerifier WorkloadRole = "verifier"
)

// Validate rejects every value outside the closed enumeration, including
// publisher, the empty string and case-mangled spellings.
func (role WorkloadRole) Validate() error {
	switch role {
	case WorkloadRoleWorker, WorkloadRoleVerifier:
		return nil
	default:
		return fmt.Errorf("%w: %q", ErrInvalidWorkloadRole, string(role))
	}
}

// OperationIdentity is the dispatch-bound identity that every sandbox SPI
// request must carry (ADR 0017 §4/§6). It binds the task/run/attempt/
// allocation quadruple to the lease generation and fencing token, so a
// replayed or stale request is rejected before any provider side effect.
type OperationIdentity struct {
	TaskId       string       `json:"taskId"`
	RunId        string       `json:"runId"`
	AttemptId    string       `json:"attemptId"`
	WorkloadRole WorkloadRole `json:"workloadRole"`
	AllocationId string       `json:"allocationId"`
	Generation   int64        `json:"generation"`
	FencingToken string       `json:"fencingToken"`
	CommandId    string       `json:"commandId"`
}

// Validate fails closed on any missing or malformed field: every string
// field must be non-empty and non-blank, the workload role must be a member
// of the closed enumeration, and the generation must be a positive integer.
func (id OperationIdentity) Validate() error {
	for _, field := range []struct {
		name  string
		value string
	}{
		{"taskId", id.TaskId},
		{"runId", id.RunId},
		{"attemptId", id.AttemptId},
		{"allocationId", id.AllocationId},
		{"fencingToken", id.FencingToken},
		{"commandId", id.CommandId},
	} {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("%w: operationIdentity.%s must be a non-empty string", ErrInvalidOperationIdentity, field.name)
		}
	}
	if err := id.WorkloadRole.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidOperationIdentity, err)
	}
	if id.Generation < 1 {
		return fmt.Errorf("%w: operationIdentity.generation must be a positive integer", ErrInvalidOperationIdentity)
	}
	return nil
}

// ReplayKey returns the replay-fencing key of the identity: the canonical
// JSON digest of all eight fields. Identical identities always derive the
// identical key, and any change to any single field derives a different key.
// The identity is validated first, so an invalid identity never yields a
// replay key.
func (id OperationIdentity) ReplayKey() (string, error) {
	if err := id.Validate(); err != nil {
		return "", fmt.Errorf("sandbox: replayKey: %w", err)
	}
	raw, err := json.Marshal(id)
	if err != nil {
		return "", fmt.Errorf("sandbox: replayKey: %w", err)
	}
	key, err := canonical.DigestJSON(raw)
	if err != nil {
		return "", fmt.Errorf("sandbox: replayKey: %w", err)
	}
	return key, nil
}

// ParseOperationIdentity decodes an operation identity from canonical JSON.
// canonical.JSON is the sole admission gate, so duplicate object members at
// any nesting depth are rejected with canonical.ErrRejected before any field
// is interpreted.
func ParseOperationIdentity(data []byte) (OperationIdentity, error) {
	canonicalized, err := canonical.JSON(data)
	if err != nil {
		return OperationIdentity{}, fmt.Errorf("%w: %w", ErrInvalidOperationIdentity, err)
	}
	var id OperationIdentity
	if err := json.Unmarshal(canonicalized, &id); err != nil {
		return OperationIdentity{}, fmt.Errorf("%w: %v", ErrInvalidOperationIdentity, err)
	}
	if err := id.Validate(); err != nil {
		return OperationIdentity{}, err
	}
	return id, nil
}

// ValidateFencing delegates the fencing adjudication to
// dispatch.ValidateLeaseFencing: the identity's generation and fencingToken
// must equal the lease's current values exactly. A stale generation, a
// mismatched fencingToken, or a lease whose canonical binding no longer
// validates, fails closed with the fencing diagnostics preserved.
func (id OperationIdentity) ValidateFencing(lease dispatch.DispatchLease) error {
	if err := id.Validate(); err != nil {
		return fmt.Errorf("sandbox: fencing guard: %w", err)
	}
	if err := dispatch.ValidateLeaseFencing(lease, id.Generation, id.FencingToken); err != nil {
		return fmt.Errorf("sandbox: fencing guard rejected the operation identity for allocation %q: %w", id.AllocationId, err)
	}
	return nil
}
