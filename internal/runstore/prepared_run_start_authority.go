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
	"strings"
	"sync"
	"time"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/lifecycle"
	"github.com/chiga0/marshal-harness/internal/resultingress"
)

const (
	runStartOutcomeEventType  = "run.start-outcome"
	runStartOutcomeProtocolV1 = "run-start-outcome/v1"
	runStartOutcomeProtocolV2 = "run-start-outcome/v2"
)

var (
	runStartDigestPattern    = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	runStartGitObjectPattern = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
)

type runStartOutcomePayload struct {
	ProtocolRevision          string `json:"protocolRevision"`
	TaskID                    string `json:"taskId"`
	PreparationDigest         string `json:"preparationDigest"`
	ProcessStartedFactDigest  string `json:"processStartedFactDigest"`
	ResumeOutcomeFactDigest   string `json:"resumeOutcomeFactDigest"`
	ReservationFactDigest     string `json:"reservationFactDigest,omitempty"`
	AttemptOpenedFactDigest   string `json:"attemptOpenedFactDigest,omitempty"`
	AttemptOrdinal            uint64 `json:"attemptOrdinal,omitempty"`
	AttemptsUsedBefore        uint64 `json:"attemptsUsedBefore,omitempty"`
	MaxAttempts               uint64 `json:"maxAttempts,omitempty"`
	ReadySequence             uint64 `json:"readySequence,omitempty"`
	ReadyAuthorityHead        string `json:"readyAuthorityHead,omitempty"`
	DispatchObservationDigest string `json:"dispatchObservationDigest,omitempty"`
}

func (payload runStartOutcomePayload) validate() error {
	if payload.ProtocolRevision != runStartOutcomeProtocolV1 && payload.ProtocolRevision != runStartOutcomeProtocolV2 || domain.ValidateID(payload.TaskID) != nil {
		return ErrConflict
	}
	for _, digest := range []string{payload.PreparationDigest, payload.ProcessStartedFactDigest, payload.ResumeOutcomeFactDigest} {
		if !runStartDigestPattern.MatchString(digest) {
			return ErrConflict
		}
	}
	if payload.ProtocolRevision == runStartOutcomeProtocolV1 {
		if payload.ReservationFactDigest != "" || payload.AttemptOpenedFactDigest != "" || payload.AttemptOrdinal != 0 || payload.AttemptsUsedBefore != 0 || payload.MaxAttempts != 0 || payload.ReadySequence != 0 || payload.ReadyAuthorityHead != "" || payload.DispatchObservationDigest != "" {
			return ErrConflict
		}
	} else {
		for _, digest := range []string{payload.ReservationFactDigest, payload.AttemptOpenedFactDigest, payload.ReadyAuthorityHead} {
			if !runStartDigestPattern.MatchString(digest) {
				return ErrConflict
			}
		}
		if payload.AttemptOrdinal != payload.AttemptsUsedBefore+1 || payload.MaxAttempts == 0 || payload.AttemptOrdinal > payload.MaxAttempts || payload.ReadySequence == 0 {
			return ErrConflict
		}
		if payload.DispatchObservationDigest != "" && !runStartDigestPattern.MatchString(payload.DispatchObservationDigest) {
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
	AttemptsUsed      uint64
	MaxAttempts       uint64
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
	if s == nil || ctx == nil || !leaseOwnerMatches(lease) {
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
	if !leaseHeldBySelfLocked(lease) || lease.root != s.root || lease.guard.preparedBorrowed.Load() {
		return RunStartAuthorityProjection{}, ErrConflict
	}
	if err := ctx.Err(); err != nil {
		return RunStartAuthorityProjection{}, err
	}
	return s.readRunStartAuthorityLocked(lease)
}

// readRunStartAuthorityLocked requires guard.mu's read or write lock.
func (s *Store) readRunStartAuthorityLocked(lease *Lease) (RunStartAuthorityProjection, error) {
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
	projection.AttemptsUsed = uint64(state.AttemptsUsed)
	if projection.Run.Validate() != nil {
		return RunStartAuthorityProjection{}, ErrConflict
	}
	if state.State == domain.StateReady || state.State == domain.StateRunning {
		requireBudget := true
		if state.State == domain.StateRunning && head.event.Type == runStartOutcomeEventType {
			legacyPayload, payloadErr := runStartPayload(head.event)
			if payloadErr != nil {
				return RunStartAuthorityProjection{}, ErrConflict
			}
			requireBudget = legacyPayload.ProtocolRevision != runStartOutcomeProtocolV1
		}
		frozen, err := runStartFrozenInputs(records, requireBudget)
		if err != nil || frozen.SpecDigest != state.SpecDigest || frozen.PolicyDigest != state.PolicyDigest || frozen.CapabilityDigest != state.CapabilityDigest || frozen.BaseSHA != state.BaseSHA || frozen.WorktreePath != state.WorktreePath {
			return RunStartAuthorityProjection{}, ErrConflict
		}
		projection.SpecDigest = frozen.SpecDigest
		projection.PolicyDigest = frozen.PolicyDigest
		projection.CapabilityDigest = frozen.CapabilityDigest
		projection.BaseSHA = frozen.BaseSHA
		projection.WorktreePath = frozen.WorktreePath
		projection.MaxAttempts = frozen.MaxAttempts
		if requireBudget && (projection.AttemptsUsed > projection.MaxAttempts || state.State == domain.StateReady && (state.CurrentAttemptID != "" || projection.AttemptsUsed >= projection.MaxAttempts)) {
			return RunStartAuthorityProjection{}, ErrConflict
		}
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

// AttemptRunAuthorityVerifier holds the exact Run Lease across the RB1
// transaction. It is constructed only from an already-acquired Lease and
// carries repository-scope identity without exposing the Lease descriptor.
type AttemptRunAuthorityVerifier struct {
	store          *Store
	lease          *Lease
	namespace      authority.AuthorityNamespaceId
	orchestratorID string
}

func NewAttemptRunAuthorityVerifier(store *Store, lease *Lease, namespace authority.AuthorityNamespaceId, orchestratorID string) (*AttemptRunAuthorityVerifier, error) {
	if store == nil || !leaseOwnerMatches(lease) || namespace.Validate() != nil || strings.TrimSpace(orchestratorID) == "" {
		return nil, ErrConflict
	}
	return &AttemptRunAuthorityVerifier{store: store, lease: lease, namespace: namespace, orchestratorID: orchestratorID}, nil
}

func (verifier *AttemptRunAuthorityVerifier) WithCurrentReadyRunAuthority(ctx context.Context, want resultingress.ReadyRunAuthority, fn func() error) error {
	if verifier == nil || ctx == nil || fn == nil || want.Validate() != nil || !leaseOwnerMatches(verifier.lease) {
		return ErrConflict
	}
	verifier.lease.guard.mu.Lock()
	defer verifier.lease.guard.mu.Unlock()
	if !leaseHeldBySelfLocked(verifier.lease) || verifier.lease.guard.preparedBorrowed.Load() || verifier.lease.root != verifier.store.root || ctx.Err() != nil {
		return ErrConflict
	}
	projection, err := verifier.store.readRunStartAuthorityLocked(verifier.lease)
	if err != nil || projection.Run.State != domain.StateReady || projection.Run.AttemptID != "" {
		return ErrConflict
	}
	got := resultingress.ReadyRunAuthority{AuthorityNamespaceID: verifier.namespace, TaskID: projection.Run.TaskID, RunID: projection.Run.RunID, OrchestratorID: verifier.orchestratorID, ReadySequence: projection.Run.Sequence, ReadyAuthorityHead: projection.Run.AuthorityHead, AttemptsUsed: projection.AttemptsUsed, MaxAttempts: projection.MaxAttempts, SpecDigest: projection.SpecDigest, PolicyDigest: projection.PolicyDigest, CapabilityDigest: projection.CapabilityDigest, BaseSHA: projection.BaseSHA, WorktreePath: projection.WorktreePath}
	if got != want {
		return ErrConflict
	}
	return fn()
}

func (verifier *AttemptRunAuthorityVerifier) WithCurrentSealedRunSuccessor(ctx context.Context, want resultingress.SealedRunSuccessorAuthority, fn func() error) error {
	if verifier == nil || ctx == nil || fn == nil || want.Validate() != nil || !leaseOwnerMatches(verifier.lease) {
		return ErrConflict
	}
	verifier.lease.guard.mu.Lock()
	defer verifier.lease.guard.mu.Unlock()
	if !leaseHeldBySelfLocked(verifier.lease) || verifier.lease.guard.preparedBorrowed.Load() || verifier.lease.root != verifier.store.root || ctx.Err() != nil {
		return ErrConflict
	}
	projection, err := verifier.store.readRunStartAuthorityLocked(verifier.lease)
	if err != nil || projection.Run.State != domain.StateRunning || projection.Run.AttemptID != want.AttemptID || projection.Run.Sequence != want.RunSuccessorSequence || projection.Run.AuthorityHead != want.RunSuccessorHead || projection.AttemptsUsed != want.AttemptsUsedAfter || projection.MaxAttempts != want.Ready.MaxAttempts {
		return ErrConflict
	}
	records, err := strictRunJournalAt(int(verifier.lease.runDir.Fd()))
	if err != nil || want.Ready.ReadySequence == 0 || want.Ready.ReadySequence >= uint64(len(records)) {
		return ErrConflict
	}
	payload, err := runStartPayload(records[want.Ready.ReadySequence].event)
	if err != nil || payload.ReservationFactDigest != want.ReservationFactDigest || payload.AttemptOpenedFactDigest != want.AttemptOpenedFactDigest || payload.AttemptOrdinal != want.AttemptOrdinal || payload.AttemptsUsedBefore != want.Ready.AttemptsUsed || payload.MaxAttempts != want.Ready.MaxAttempts || payload.ReadySequence != want.Ready.ReadySequence || payload.ReadyAuthorityHead != want.Ready.ReadyAuthorityHead {
		return ErrConflict
	}
	frozen, err := runStartFrozenInputs(records, true)
	if err != nil || records[want.Ready.ReadySequence-1].digest != want.Ready.ReadyAuthorityHead {
		return ErrConflict
	}
	gotReady := resultingress.ReadyRunAuthority{AuthorityNamespaceID: verifier.namespace, TaskID: projection.Run.TaskID, RunID: projection.Run.RunID, OrchestratorID: verifier.orchestratorID, ReadySequence: want.Ready.ReadySequence, ReadyAuthorityHead: records[want.Ready.ReadySequence-1].digest, AttemptsUsed: payload.AttemptsUsedBefore, MaxAttempts: frozen.MaxAttempts, SpecDigest: frozen.SpecDigest, PolicyDigest: frozen.PolicyDigest, CapabilityDigest: frozen.CapabilityDigest, BaseSHA: frozen.BaseSHA, WorktreePath: frozen.WorktreePath}
	if gotReady != want.Ready {
		return ErrConflict
	}
	return fn()
}

// WithCurrentRunningRunAuthority verifies the durable READY authority carried
// by an Attempt against the exact sealed RUNNING successor while keeping the
// Run lease guard held across fn. Cleanup and result ingress use the READY
// digest as their immutable RunAuthorityBinding; the current Run journal head
// is the later run.start-outcome and must therefore be joined, not compared as
// if both digests named the same fact.
func (verifier *AttemptRunAuthorityVerifier) WithCurrentRunningRunAuthority(ctx context.Context, want resultingress.RunAuthorityBinding, attemptID string, fn func() error) error {
	if verifier == nil || ctx == nil || fn == nil || want.AuthorityNamespaceID != verifier.namespace || want.OrchestratorID != verifier.orchestratorID || strings.TrimSpace(attemptID) == "" || !leaseOwnerMatches(verifier.lease) {
		return ErrConflict
	}
	verifier.lease.guard.mu.Lock()
	defer verifier.lease.guard.mu.Unlock()
	if !leaseHeldBySelfLocked(verifier.lease) || verifier.lease.guard.preparedBorrowed.Load() || verifier.lease.root != verifier.store.root || ctx.Err() != nil {
		return ErrConflict
	}
	records, err := strictRunJournalAt(int(verifier.lease.runDir.Fd()))
	if err != nil || len(records) < 2 {
		return ErrConflict
	}
	state, err := inspectAt(int(verifier.lease.runDir.Fd()))
	if err != nil || state.State != domain.StateRunning || state.RunID != want.RunID || state.CurrentAttemptID != attemptID || state.Sequence != uint64(len(records)) {
		return ErrConflict
	}
	head := records[len(records)-1]
	payload, err := runStartPayload(head.event)
	if err != nil || head.event.Type != runStartOutcomeEventType || head.event.AttemptID != attemptID || payload.ReadyAuthorityHead != want.RunAuthorityDigest || records[len(records)-2].digest != want.RunAuthorityDigest {
		return ErrConflict
	}
	return fn()
}

// WithCurrentRunAuthority verifies the immutable READY authority carried by
// an Attempt against either the exact current READY head or its unique sealed
// RUNNING successor. The lease guard remains held across fn, so callers use
// one verifier for both pre-start mutations and post-start terminalization
// without a check-then-append window.
func (verifier *AttemptRunAuthorityVerifier) WithCurrentRunAuthority(ctx context.Context, want resultingress.RunAuthorityBinding, fn func() error) error {
	if verifier == nil || ctx == nil || fn == nil || want.AuthorityNamespaceID != verifier.namespace || want.OrchestratorID != verifier.orchestratorID || !leaseOwnerMatches(verifier.lease) {
		return ErrConflict
	}
	verifier.lease.guard.mu.Lock()
	defer verifier.lease.guard.mu.Unlock()
	if !leaseHeldBySelfLocked(verifier.lease) || verifier.lease.guard.preparedBorrowed.Load() || verifier.lease.root != verifier.store.root || ctx.Err() != nil {
		return ErrConflict
	}
	records, err := strictRunJournalAt(int(verifier.lease.runDir.Fd()))
	if err != nil || len(records) == 0 {
		return ErrConflict
	}
	state, err := inspectAt(int(verifier.lease.runDir.Fd()))
	if err != nil || state.RunID != want.RunID || state.Sequence != uint64(len(records)) {
		return ErrConflict
	}
	switch state.State {
	case domain.StateReady:
		if state.CurrentAttemptID != "" || records[len(records)-1].digest != want.RunAuthorityDigest {
			return ErrConflict
		}
	case domain.StateRunning:
		if state.CurrentAttemptID == "" || len(records) < 2 {
			return ErrConflict
		}
		head := records[len(records)-1]
		payload, err := runStartPayload(head.event)
		if err != nil || head.event.Type != runStartOutcomeEventType || head.event.AttemptID != state.CurrentAttemptID || payload.ReadyAuthorityHead != want.RunAuthorityDigest || records[len(records)-2].digest != want.RunAuthorityDigest {
			return ErrConflict
		}
	default:
		return ErrConflict
	}
	return fn()
}

type runStartFrozenProjection struct {
	SpecDigest       string
	PolicyDigest     string
	CapabilityDigest string
	BaseSHA          string
	WorktreePath     string
	MaxAttempts      uint64
}

func runStartFrozenInputs(records []strictRunRecord, requireBudget bool) (runStartFrozenProjection, error) {
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
		maxAttempts, ok := record.event.Payload["maxAttempts"].(float64)
		if requireBudget || ok {
			if !ok || maxAttempts < 1 || maxAttempts > float64(1<<53-1) || maxAttempts != float64(uint64(maxAttempts)) {
				return runStartFrozenProjection{}, ErrConflict
			}
			result.MaxAttempts = uint64(maxAttempts)
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
	mu                        sync.Mutex
	cond                      *sync.Cond
	active                    bool
	called                    bool
	inFlight                  int
	escaped                   bool
	violated                  bool
	runFD                     int
	prepared                  application.PreparedRunStart
	dispatchObservationDigest string
	result                    application.RunProjection
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
// dispatchObservationDigest is empty for generic/legacy execution and the
// exact descriptor-bound digest for local-dogfood. A non-empty value is
// re-read under the exclusive guard to prevent evidence-removal downgrade.
func (s *Store) WithPreparedRunStartAuthority(ctx context.Context, lease *Lease, prepared application.PreparedRunStart, dispatchObservationDigest string, fn func(resultingress.RunStartProjector) error) (application.RunProjection, error) {
	if dispatchObservationDigest != "" && !runStartDigestPattern.MatchString(dispatchObservationDigest) {
		return application.RunProjection{}, ErrConflict
	}
	if s == nil || ctx == nil || fn == nil || prepared.Validate() != nil || !leaseOwnerMatches(lease) {
		return application.RunProjection{}, ErrConflict
	}
	if err := ctx.Err(); err != nil {
		return application.RunProjection{}, err
	}
	lease.guard.mu.Lock()
	defer lease.guard.mu.Unlock()
	if !leaseHeldBySelfLocked(lease) || lease.root != s.root || lease.runID != prepared.RunID || lease.guard.preparedBorrowed.Load() {
		return application.RunProjection{}, ErrConflict
	}
	// Acquire already bound and retained this exact Run directory descriptor.
	// The sealed outer borrow must never reopen the Run by pathname.
	runFD := int(lease.runDir.Fd())
	records, err := strictRunJournalAt(runFD)
	if err != nil {
		return application.RunProjection{}, err
	}
	actualDispatchDigest, foundDispatchObservation, err := localDispatchObservationDigestAt(runFD, prepared.AttemptID)
	if err != nil || foundDispatchObservation != (dispatchObservationDigest != "") || actualDispatchDigest != dispatchObservationDigest {
		return application.RunProjection{}, fmt.Errorf("%w: local dispatch observation mismatch", ErrConflict)
	}
	if replay, payload, found, err := findPreparedRunStartOutcome(prepared, records); err != nil {
		return application.RunProjection{}, err
	} else if found {
		if payload.DispatchObservationDigest != actualDispatchDigest {
			return application.RunProjection{}, fmt.Errorf("%w: Run-start local dispatch binding mismatch", ErrConflict)
		}
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
	projector.guard.dispatchObservationDigest = dispatchObservationDigest
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
	frozen, frozenErr := runStartFrozenInputs(records, true)
	if frozenErr != nil || state.TaskID != prepared.TaskID || state.RunID != prepared.RunID || state.CurrentAttemptID != "" || state.State != domain.StateReady || state.Sequence != prepared.Sequence || uint64(state.AttemptsUsed) != prepared.AttemptsUsedBefore || frozen.MaxAttempts != prepared.MaxAttempts || prepared.AttemptOrdinal != uint64(state.AttemptsUsed)+1 {
		return fmt.Errorf("%w: prepared Run no longer binds current READY reservation", ErrConflict)
	}
	return nil
}

func appendPreparedRunStartClaim(guard *borrowedRunStartGuard, claim resultingress.CommittedRunStartClaim) (application.RunProjection, error) {
	if guard == nil || guard.runFD < 0 || claim.TaskID != guard.prepared.TaskID || claim.RunID != guard.prepared.RunID || claim.AttemptID != guard.prepared.AttemptID || claim.ReservationFactDigest != guard.prepared.ReservationFactDigest || claim.AttemptOpenedFactDigest != guard.prepared.AttemptOpenedFactDigest || claim.AttemptOrdinal != guard.prepared.AttemptOrdinal || claim.AttemptsUsedBefore != guard.prepared.AttemptsUsedBefore || claim.MaxAttempts != guard.prepared.MaxAttempts || claim.ReadySequence != guard.prepared.Sequence || claim.ReadyAuthorityHead != guard.prepared.AuthorityHead || claim.PreparationDigest != guard.prepared.PreparationDigest || !runStartDigestPattern.MatchString(claim.ProcessStartedFactDigest) || !runStartDigestPattern.MatchString(claim.ResumeOutcomeFactDigest) {
		return application.RunProjection{}, fmt.Errorf("%w: committed Run-start claim mismatch", ErrConflict)
	}
	records, err := strictRunJournalAt(guard.runFD)
	if err != nil {
		return application.RunProjection{}, err
	}
	dispatchObservationDigest, foundDispatchObservation, err := localDispatchObservationDigestAt(guard.runFD, claim.AttemptID)
	if err != nil || foundDispatchObservation != (guard.dispatchObservationDigest != "") || dispatchObservationDigest != guard.dispatchObservationDigest {
		return application.RunProjection{}, fmt.Errorf("%w: local dispatch observation mismatch", ErrConflict)
	}
	if replay, payload, found, err := findPreparedRunStartOutcome(guard.prepared, records); err != nil {
		return application.RunProjection{}, err
	} else if found {
		if payload.ProcessStartedFactDigest != claim.ProcessStartedFactDigest || payload.ResumeOutcomeFactDigest != claim.ResumeOutcomeFactDigest || payload.DispatchObservationDigest != dispatchObservationDigest {
			return application.RunProjection{}, fmt.Errorf("%w: preparation binds different provenance", ErrConflict)
		}
		return replay, nil
	}
	if err := validatePreparedRunStartCurrentAt(guard.runFD, guard.prepared, records); err != nil {
		return application.RunProjection{}, err
	}
	payload := runStartOutcomePayload{ProtocolRevision: runStartOutcomeProtocolV2, TaskID: claim.TaskID, PreparationDigest: claim.PreparationDigest, ProcessStartedFactDigest: claim.ProcessStartedFactDigest, ResumeOutcomeFactDigest: claim.ResumeOutcomeFactDigest, ReservationFactDigest: claim.ReservationFactDigest, AttemptOpenedFactDigest: claim.AttemptOpenedFactDigest, AttemptOrdinal: claim.AttemptOrdinal, AttemptsUsedBefore: claim.AttemptsUsedBefore, MaxAttempts: claim.MaxAttempts, ReadySequence: claim.ReadySequence, ReadyAuthorityHead: claim.ReadyAuthorityHead, DispatchObservationDigest: dispatchObservationDigest}
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
			"reservationFactDigest":    payload.ReservationFactDigest,
			"attemptOpenedFactDigest":  payload.AttemptOpenedFactDigest,
			"attemptOrdinal":           payload.AttemptOrdinal,
			"attemptsUsedBefore":       payload.AttemptsUsedBefore,
			"maxAttempts":              payload.MaxAttempts,
			"readySequence":            payload.ReadySequence,
			"readyAuthorityHead":       payload.ReadyAuthorityHead,
		},
	}
	if dispatchObservationDigest != "" {
		event.Payload["dispatchObservationDigest"] = dispatchObservationDigest
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
	state, err := inspectAt(guard.runFD)
	if err != nil || state.State != domain.StateRunning || state.CurrentAttemptID != claim.AttemptID || uint64(state.AttemptsUsed) != claim.AttemptOrdinal || state.Sequence != guard.prepared.Sequence+1 {
		return application.RunProjection{}, fmt.Errorf("%w: Run-start budget projection mismatch", ErrConflict)
	}
	notifyStateTransition(false, []domain.RunEvent{after[guard.prepared.Sequence].event})
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
	if err != nil || payload.PreparationDigest != prepared.PreparationDigest || payload.TaskID != prepared.TaskID || payload.ReservationFactDigest != prepared.ReservationFactDigest || payload.AttemptOpenedFactDigest != prepared.AttemptOpenedFactDigest || payload.AttemptOrdinal != prepared.AttemptOrdinal || payload.AttemptsUsedBefore != prepared.AttemptsUsedBefore || payload.MaxAttempts != prepared.MaxAttempts || payload.ReadySequence != prepared.Sequence || payload.ReadyAuthorityHead != prepared.AuthorityHead || record.event.RunID != prepared.RunID || record.event.AttemptID != prepared.AttemptID || record.event.StateFrom != domain.StateReady || record.event.StateTo != domain.StateRunning || records[prepared.Sequence-1].digest != prepared.AuthorityHead {
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
