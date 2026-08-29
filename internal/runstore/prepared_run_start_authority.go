package runstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/lifecycle"
	"github.com/chiga0/marshal-harness/internal/resultingress"
)

const (
	runStartOutcomeEventType        = "run.start-outcome"
	runStartOutcomeProtocolRevision = "run-start-outcome/v1"
)

var (
	runStartDigestPattern    = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	runStartGitObjectPattern = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
)

type runStartOutcomePayload struct {
	ProtocolRevision         string `json:"protocolRevision"`
	TaskID                   string `json:"taskId"`
	PreparationDigest        string `json:"preparationDigest"`
	ProcessStartedFactDigest string `json:"processStartedFactDigest"`
	ResumeOutcomeFactDigest  string `json:"resumeOutcomeFactDigest"`
}

func (payload runStartOutcomePayload) validate() error {
	if payload.ProtocolRevision != runStartOutcomeProtocolRevision || domain.ValidateID(payload.TaskID) != nil {
		return ErrConflict
	}
	for _, digest := range []string{payload.PreparationDigest, payload.ProcessStartedFactDigest, payload.ResumeOutcomeFactDigest} {
		if !runStartDigestPattern.MatchString(digest) {
			return ErrConflict
		}
	}
	return nil
}

type strictRunRecord struct {
	event  domain.RunEvent
	digest string
}

// RunStartAuthorityProjection is the narrow, read-only projection consumed by
// production composition while it already holds the exact Run Lease. It does
// not expose journal records, lease-owner material, or ResultIngress facts.
// PreparationDigest is present only when the current RUNNING successor is the
// sealed run.start-outcome at the journal head. The five frozen launch inputs
// are returned only after exact snapshot-to-READY-event equality and closed
// digest/SHA/path validation.
type RunStartAuthorityProjection struct {
	Run               application.RunProjection
	PreparationDigest string
	SpecDigest        string
	PolicyDigest      string
	CapabilityDigest  string
	BaseSHA           string
	WorktreePath      string
}

// ReadRunStartAuthorityUnderLease strictly replays the descriptor-bound Run
// journal without mutation or callbacks. READY is deliberately projected with
// an empty PreparationDigest; RUNNING is accepted only when its exact head is
// the sealed Run-start successor.
func (s *Store) ReadRunStartAuthorityUnderLease(ctx context.Context, lease *Lease) (RunStartAuthorityProjection, error) {
	if s == nil || ctx == nil || !leaseHeldBySelf(lease) || lease.root != s.root {
		return RunStartAuthorityProjection{}, ErrConflict
	}
	if err := ctx.Err(); err != nil {
		return RunStartAuthorityProjection{}, err
	}
	if lease.guard.preparedBorrowed.Load() {
		return RunStartAuthorityProjection{}, fmt.Errorf("%w: prepared Run-start authority is borrowed", ErrConflict)
	}
	lease.guard.mu.RLock()
	defer lease.guard.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return RunStartAuthorityProjection{}, err
	}
	records, err := strictRunJournalAt(int(lease.runDir.Fd()))
	if err != nil {
		return RunStartAuthorityProjection{}, err
	}
	state, err := inspectAt(int(lease.runDir.Fd()))
	if err != nil || state.RunID != lease.runID || state.Sequence == 0 || state.Sequence != uint64(len(records)) {
		return RunStartAuthorityProjection{}, ErrConflict
	}
	head := records[len(records)-1]
	projection := RunStartAuthorityProjection{Run: application.RunProjection{
		TaskID: state.TaskID, RunID: state.RunID, AttemptID: state.CurrentAttemptID,
		State: state.State, Sequence: state.Sequence, AuthorityHead: head.digest,
	}}
	if projection.Run.Validate() != nil {
		return RunStartAuthorityProjection{}, ErrConflict
	}
	if state.State == domain.StateReady || state.State == domain.StateRunning {
		frozen, err := runStartFrozenInputs(records)
		if err != nil || frozen.SpecDigest != state.SpecDigest || frozen.PolicyDigest != state.PolicyDigest || frozen.CapabilityDigest != state.CapabilityDigest || frozen.BaseSHA != state.BaseSHA || frozen.WorktreePath != state.WorktreePath {
			return RunStartAuthorityProjection{}, ErrConflict
		}
		projection.SpecDigest = frozen.SpecDigest
		projection.PolicyDigest = frozen.PolicyDigest
		projection.CapabilityDigest = frozen.CapabilityDigest
		projection.BaseSHA = frozen.BaseSHA
		projection.WorktreePath = frozen.WorktreePath
	}
	if state.State == domain.StateRunning {
		if head.event.Type != runStartOutcomeEventType || head.event.StateFrom != domain.StateReady || head.event.StateTo != domain.StateRunning || head.event.AttemptID != state.CurrentAttemptID {
			return RunStartAuthorityProjection{}, ErrConflict
		}
		payload, err := runStartPayload(head.event)
		if err != nil || payload.TaskID != state.TaskID {
			return RunStartAuthorityProjection{}, ErrConflict
		}
		projection.PreparationDigest = payload.PreparationDigest
	}
	return projection, nil
}

type runStartFrozenProjection struct {
	SpecDigest       string
	PolicyDigest     string
	CapabilityDigest string
	BaseSHA          string
	WorktreePath     string
}

func runStartFrozenInputs(records []strictRunRecord) (runStartFrozenProjection, error) {
	var result runStartFrozenProjection
	found := false
	for _, record := range records {
		if record.event.StateTo != domain.StateReady {
			continue
		}
		if found || record.event.StateFrom != domain.StatePlanned {
			return runStartFrozenProjection{}, ErrConflict
		}
		found = true
		var ok bool
		result.SpecDigest, ok = record.event.Payload["specDigest"].(string)
		if !ok {
			return runStartFrozenProjection{}, ErrConflict
		}
		result.PolicyDigest, ok = record.event.Payload["policyDigest"].(string)
		if !ok {
			return runStartFrozenProjection{}, ErrConflict
		}
		result.CapabilityDigest, ok = record.event.Payload["capabilityDigest"].(string)
		if !ok {
			return runStartFrozenProjection{}, ErrConflict
		}
		result.BaseSHA, ok = record.event.Payload["baseSha"].(string)
		if !ok {
			return runStartFrozenProjection{}, ErrConflict
		}
		result.WorktreePath, ok = record.event.Payload["worktreePath"].(string)
		if !ok {
			return runStartFrozenProjection{}, ErrConflict
		}
	}
	if !found || !runStartDigestPattern.MatchString(result.SpecDigest) || !runStartDigestPattern.MatchString(result.PolicyDigest) || !runStartDigestPattern.MatchString(result.CapabilityDigest) || !runStartGitObjectPattern.MatchString(result.BaseSHA) || !filepath.IsAbs(result.WorktreePath) || filepath.Clean(result.WorktreePath) != result.WorktreePath {
		return runStartFrozenProjection{}, ErrConflict
	}
	return result, nil
}

func strictRunJournalAt(runFD int) ([]strictRunRecord, error) {
	raw, err := readRegularAt(runFD, "events.jsonl", 0)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || raw[len(raw)-1] != '\n' {
		return nil, ErrTruncatedTail
	}
	lines := bytes.Split(raw[:len(raw)-1], []byte{'\n'})
	records := make([]strictRunRecord, 0, len(lines))
	seen := make(map[string]struct{}, len(lines))
	currentState := domain.StateCreated
	currentRunID := ""
	for index, line := range lines {
		canonicalLine, err := canonical.JSON(line)
		if err != nil || !bytes.Equal(canonicalLine, line) {
			return nil, fmt.Errorf("%w: non-canonical Run journal record %d", ErrConflict, index+1)
		}
		var event domain.RunEvent
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&event); err != nil {
			return nil, fmt.Errorf("%w: decode Run journal record %d", ErrConflict, index+1)
		}
		var trailing json.RawMessage
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) || event.Sequence != uint64(index+1) || event.EventID == "" || event.APIVersion != domain.APIVersionV1Alpha1 || event.Kind != domain.KindRunEvent {
			return nil, fmt.Errorf("%w: malformed Run journal record %d", ErrConflict, index+1)
		}
		if currentRunID == "" {
			currentRunID = event.RunID
		}
		if event.RunID != currentRunID || lifecycle.ValidateTransition(currentState, currentRunID, uint64(index), event) != nil {
			return nil, fmt.Errorf("%w: invalid Run transition at record %d", ErrConflict, index+1)
		}
		currentState = event.StateTo
		if _, duplicate := seen[event.EventID]; duplicate {
			return nil, fmt.Errorf("%w: duplicate Run event ID", ErrConflict)
		}
		seen[event.EventID] = struct{}{}
		records = append(records, strictRunRecord{event: event, digest: canonical.DigestBytes(line)})
	}
	return records, nil
}

func runStartPayload(event domain.RunEvent) (runStartOutcomePayload, error) {
	raw, err := json.Marshal(event.Payload)
	if err != nil {
		return runStartOutcomePayload{}, ErrConflict
	}
	raw, err = canonical.JSON(raw)
	if err != nil {
		return runStartOutcomePayload{}, ErrConflict
	}
	var payload runStartOutcomePayload
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil || payload.validate() != nil {
		return runStartOutcomePayload{}, ErrConflict
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return runStartOutcomePayload{}, ErrConflict
	}
	return payload, nil
}

type borrowedRunStartGuard struct {
	mu       sync.Mutex
	cond     *sync.Cond
	active   bool
	called   bool
	inFlight int
	escaped  bool
	violated bool
	runFD    int
	prepared application.PreparedRunStart
	result   application.RunProjection
}

type borrowedRunStartProjector struct{ guard *borrowedRunStartGuard }

var _ resultingress.RunStartProjector = (*borrowedRunStartProjector)(nil)

func newBorrowedRunStartProjector(runFD int, prepared application.PreparedRunStart) *borrowedRunStartProjector {
	guard := &borrowedRunStartGuard{active: true, runFD: runFD, prepared: prepared}
	guard.cond = sync.NewCond(&guard.mu)
	return &borrowedRunStartProjector{guard: guard}
}

func (projector *borrowedRunStartProjector) ProjectCommittedRunStart(ctx context.Context, proof resultingress.CommittedRunStartProof) (err error) {
	if projector == nil || projector.guard == nil || ctx == nil {
		return ErrConflict
	}
	guard := projector.guard
	guard.mu.Lock()
	if !guard.active || guard.called || ctx.Err() != nil {
		guard.violated = true
		guard.mu.Unlock()
		return ErrConflict
	}
	guard.called = true
	guard.inFlight++
	guard.mu.Unlock()
	defer func() {
		if recover() != nil {
			err = fmt.Errorf("%w: Run-start projector panic", ErrConflict)
		}
		guard.mu.Lock()
		guard.inFlight--
		if err != nil {
			guard.violated = true
		}
		guard.cond.Broadcast()
		guard.mu.Unlock()
	}()
	return proof.WithClaim(func(claim resultingress.CommittedRunStartClaim) error {
		projection, err := appendPreparedRunStartClaim(guard, claim)
		if err == nil {
			guard.mu.Lock()
			guard.result = projection
			guard.mu.Unlock()
		}
		return err
	})
}

func (guard *borrowedRunStartGuard) deactivateAndWait() (application.RunProjection, error) {
	guard.mu.Lock()
	if guard.inFlight != 0 {
		guard.escaped = true
	}
	guard.active = false
	for guard.inFlight != 0 {
		guard.cond.Wait()
	}
	result := guard.result
	valid := guard.called && !guard.escaped && !guard.violated && result.Validate() == nil
	guard.runFD = -1
	guard.prepared = application.PreparedRunStart{}
	guard.result = application.RunProjection{}
	guard.mu.Unlock()
	if !valid {
		return application.RunProjection{}, ErrConflict
	}
	return result, nil
}

// WithPreparedRunStartAuthority is the only exported Run-start mutation seam.
// It borrows the exact lease descriptor and holds its mutation guard through
// the ResultIngress continuation, append/fsync and post-CAS projection.
func (s *Store) WithPreparedRunStartAuthority(ctx context.Context, lease *Lease, prepared application.PreparedRunStart, fn func(resultingress.RunStartProjector) error) (application.RunProjection, error) {
	if s == nil || ctx == nil || fn == nil || prepared.Validate() != nil || !leaseHeldBySelf(lease) || lease.root != s.root || lease.runID != prepared.RunID {
		return application.RunProjection{}, ErrConflict
	}
	if err := ctx.Err(); err != nil {
		return application.RunProjection{}, err
	}
	lease.guard.mu.Lock()
	defer lease.guard.mu.Unlock()
	// Acquire already bound and retained this exact Run directory descriptor.
	// The sealed outer borrow must never reopen the Run by pathname.
	runFD := int(lease.runDir.Fd())
	records, err := strictRunJournalAt(runFD)
	if err != nil {
		return application.RunProjection{}, err
	}
	if replay, _, found, err := findPreparedRunStartOutcome(prepared, records); err != nil {
		return application.RunProjection{}, err
	} else if found {
		return replay, nil
	}
	if err := validatePreparedRunStartCurrentAt(runFD, prepared, records); err != nil {
		return application.RunProjection{}, err
	}
	if !lease.guard.preparedBorrowed.CompareAndSwap(false, true) {
		return application.RunProjection{}, ErrConflict
	}
	defer lease.guard.preparedBorrowed.Store(false)
	projector := newBorrowedRunStartProjector(runFD, prepared)
	callErr := callPreparedRunStartBorrower(fn, projector)
	projection, guardErr := projector.guard.deactivateAndWait()
	if callErr != nil {
		return application.RunProjection{}, callErr
	}
	if guardErr != nil {
		return application.RunProjection{}, guardErr
	}
	return projection, nil
}

func callPreparedRunStartBorrower(fn func(resultingress.RunStartProjector) error, projector resultingress.RunStartProjector) (err error) {
	defer func() {
		if recover() != nil {
			err = fmt.Errorf("%w: Run-start borrower panic", ErrConflict)
		}
	}()
	return fn(projector)
}

func validatePreparedRunStartCurrentAt(runFD int, prepared application.PreparedRunStart, records []strictRunRecord) error {
	if prepared.Sequence == 0 || prepared.Sequence > uint64(len(records)) || records[prepared.Sequence-1].digest != prepared.AuthorityHead {
		return fmt.Errorf("%w: prepared Run head is unavailable", ErrConflict)
	}
	state, err := inspectAt(runFD)
	if err != nil {
		return err
	}
	if state.TaskID != prepared.TaskID || state.RunID != prepared.RunID || state.CurrentAttemptID != prepared.AttemptID || state.State != domain.StateReady || state.Sequence != prepared.Sequence {
		return fmt.Errorf("%w: prepared Run no longer binds current READY Attempt", ErrConflict)
	}
	return nil
}

func appendPreparedRunStartClaim(guard *borrowedRunStartGuard, claim resultingress.CommittedRunStartClaim) (application.RunProjection, error) {
	if guard == nil || guard.runFD < 0 || claim.TaskID != guard.prepared.TaskID || claim.RunID != guard.prepared.RunID || claim.AttemptID != guard.prepared.AttemptID || claim.PreparationDigest != guard.prepared.PreparationDigest || !runStartDigestPattern.MatchString(claim.ProcessStartedFactDigest) || !runStartDigestPattern.MatchString(claim.ResumeOutcomeFactDigest) {
		return application.RunProjection{}, fmt.Errorf("%w: committed Run-start claim mismatch", ErrConflict)
	}
	records, err := strictRunJournalAt(guard.runFD)
	if err != nil {
		return application.RunProjection{}, err
	}
	if replay, payload, found, err := findPreparedRunStartOutcome(guard.prepared, records); err != nil {
		return application.RunProjection{}, err
	} else if found {
		if payload.ProcessStartedFactDigest != claim.ProcessStartedFactDigest || payload.ResumeOutcomeFactDigest != claim.ResumeOutcomeFactDigest {
			return application.RunProjection{}, fmt.Errorf("%w: preparation binds different provenance", ErrConflict)
		}
		return replay, nil
	}
	if err := validatePreparedRunStartCurrentAt(guard.runFD, guard.prepared, records); err != nil {
		return application.RunProjection{}, err
	}
	payload := runStartOutcomePayload{ProtocolRevision: runStartOutcomeProtocolRevision, TaskID: claim.TaskID, PreparationDigest: claim.PreparationDigest, ProcessStartedFactDigest: claim.ProcessStartedFactDigest, ResumeOutcomeFactDigest: claim.ResumeOutcomeFactDigest}
	eventID, err := domain.NewID("event")
	if err != nil {
		return application.RunProjection{}, err
	}
	event := domain.RunEvent{
		APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent,
		EventID: eventID, RunID: claim.RunID, AttemptID: claim.AttemptID,
		Sequence: guard.prepared.Sequence + 1, Type: runStartOutcomeEventType,
		StateFrom: domain.StateReady, StateTo: domain.StateRunning, Timestamp: time.Now().UTC(),
		Actor: &domain.Actor{Type: "system", ID: "marshal-run-start-projector"},
		Payload: map[string]any{
			"protocolRevision": payload.ProtocolRevision, "taskId": payload.TaskID,
			"preparationDigest":        payload.PreparationDigest,
			"processStartedFactDigest": payload.ProcessStartedFactDigest,
			"resumeOutcomeFactDigest":  payload.ResumeOutcomeFactDigest,
		},
	}
	if err := lifecycle.ValidateTransition(domain.StateReady, event.RunID, guard.prepared.Sequence, event); err != nil {
		return application.RunProjection{}, err
	}
	raw, err := json.Marshal(event)
	if err != nil {
		return application.RunProjection{}, err
	}
	raw, err = canonical.JSON(raw)
	if err != nil {
		return application.RunProjection{}, err
	}
	if err := appendRegularAt(guard.runFD, "events.jsonl", append(raw, '\n')); err != nil {
		return application.RunProjection{}, fmt.Errorf("sync Run-start outcome: %w", err)
	}
	after, err := strictRunJournalAt(guard.runFD)
	if err != nil {
		return application.RunProjection{}, err
	}
	projection, stored, found, err := findPreparedRunStartOutcome(guard.prepared, after)
	if err != nil || !found || stored != payload {
		return application.RunProjection{}, fmt.Errorf("%w: Run-start post-CAS replay mismatch", ErrConflict)
	}
	return projection, nil
}

func findPreparedRunStartOutcome(prepared application.PreparedRunStart, records []strictRunRecord) (application.RunProjection, runStartOutcomePayload, bool, error) {
	target := prepared.Sequence + 1
	if target == 0 || target > uint64(len(records)) {
		return application.RunProjection{}, runStartOutcomePayload{}, false, nil
	}
	record := records[target-1]
	if record.event.Type != runStartOutcomeEventType {
		return application.RunProjection{}, runStartOutcomePayload{}, false, fmt.Errorf("%w: prepared successor has another producer", ErrConflict)
	}
	payload, err := runStartPayload(record.event)
	if err != nil || payload.PreparationDigest != prepared.PreparationDigest || payload.TaskID != prepared.TaskID || record.event.RunID != prepared.RunID || record.event.AttemptID != prepared.AttemptID || record.event.StateFrom != domain.StateReady || record.event.StateTo != domain.StateRunning || records[prepared.Sequence-1].digest != prepared.AuthorityHead {
		return application.RunProjection{}, runStartOutcomePayload{}, false, fmt.Errorf("%w: conflicting Run-start outcome", ErrConflict)
	}
	projection := application.RunProjection{TaskID: prepared.TaskID, RunID: prepared.RunID, AttemptID: prepared.AttemptID, State: domain.StateRunning, Sequence: target, AuthorityHead: record.digest}
	if projection.Validate() != nil {
		return application.RunProjection{}, runStartOutcomePayload{}, false, ErrConflict
	}
	for index := target; index < uint64(len(records)); index++ {
		candidate := records[index].event
		if candidate.Type != runStartOutcomeEventType {
			continue
		}
		candidatePayload, decodeErr := runStartPayload(candidate)
		if decodeErr != nil || candidate.AttemptID == prepared.AttemptID || candidatePayload.PreparationDigest == prepared.PreparationDigest {
			return application.RunProjection{}, runStartOutcomePayload{}, false, fmt.Errorf("%w: duplicate Run-start identity", ErrConflict)
		}
	}
	return projection, payload, true, nil
}
