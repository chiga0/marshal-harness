package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/port"
)

// Attempt outcomes are fixed, structured codes so selection evidence never
// embeds provider environment details.
const (
	OutcomeSelected         = "selected"
	OutcomePolicyDenied     = "policy-denied"
	OutcomeUnavailable      = "unavailable"
	OutcomeProbeFailed      = "probe-failed"
	OutcomeInvalidSnapshot  = "invalid-snapshot"
	OutcomeIdentityMismatch = "identity-mismatch"
	OutcomeNotLaunchCapable = "not-launch-capable"
	OutcomeUnsupported      = "unsupported"
)

var outcomeOrder = []string{
	OutcomePolicyDenied,
	OutcomeUnavailable,
	OutcomeProbeFailed,
	OutcomeInvalidSnapshot,
	OutcomeIdentityMismatch,
	OutcomeNotLaunchCapable,
	OutcomeUnsupported,
	OutcomeSelected,
}

// SelectionRequest carries the frozen adapter preference and the policy
// allow-list for one selection pass.
type SelectionRequest struct {
	PreferredAdapter string
	FallbackAdapters []string
	AllowedAdapters  []string
}

// SelectionAttempt records the structured outcome of one explicit candidate in
// frozen order.
type SelectionAttempt struct {
	AdapterID string
	Outcome   string
}

// Selection reports the chosen adapter, its capability record, and every
// attempted candidate with its structured reason.
type Selection struct {
	Adapter    port.WorkerAdapter
	Capability domain.Record
	Attempts   []SelectionAttempt
}

// Selector resolves Worker adapters strictly from explicit candidate lists and
// never performs implicit fallback.
type Selector struct {
	registry    *Registry
	eligibility func(port.WorkerAdapter) bool
}

// NewSelector returns a Selector bound to the given exact-ID registry. A nil
// registry is fail-closed: it yields a permanent error instead of a Selector
// that would panic later.
func NewSelector(registry *Registry) (*Selector, error) {
	if registry == nil {
		return nil, port.Permanentf("select adapter: nil registry")
	}
	return &Selector{registry: registry}, nil
}

// NewEligibleSelector returns a selector that rejects registered adapters for
// which eligible returns false before Probe. The predicate is injected by the
// composition root so this provider-neutral package does not depend on a
// concrete runtime capability interface.
func NewEligibleSelector(registry *Registry, eligible func(port.WorkerAdapter) bool) (*Selector, error) {
	if eligible == nil {
		return nil, port.Permanentf("select adapter: nil eligibility predicate")
	}
	selector, err := NewSelector(registry)
	if err != nil {
		return nil, err
	}
	selector.eligibility = eligible
	return selector, nil
}

// Select resolves and probes candidates only in the frozen explicit order:
// preferredAdapter first, then fallbackAdapters in list order. A candidate is
// accepted only when the policy permits its exact ID, the registry resolves
// that exact ID, and the probe returns a CapabilitySnapshot whose adapterId
// matches exactly and whose probeStatus is "supported". Every skipped
// candidate is recorded with a structured reason.
//
// Selection is fail-closed around context cancellation: a probe error that is
// or wraps context.Canceled or context.DeadlineExceeded, and a context found
// done right after a probe, aborts Select with the context error instead of
// continuing to the next fallback candidate.
func (s *Selector) Select(ctx context.Context, request SelectionRequest) (Selection, error) {
	if s == nil || s.registry == nil {
		return Selection{Attempts: []SelectionAttempt{}}, port.Permanentf("select adapter: nil registry")
	}
	candidates, err := validateCandidates(request)
	if err != nil {
		return Selection{Attempts: []SelectionAttempt{}}, err
	}
	allowed := make(map[string]bool, len(request.AllowedAdapters))
	for _, id := range request.AllowedAdapters {
		if trimmed := strings.TrimSpace(id); trimmed != "" {
			allowed[trimmed] = true
		}
	}
	selection := Selection{Attempts: []SelectionAttempt{}}
	for _, id := range candidates {
		if !allowed[id] {
			selection.Attempts = append(selection.Attempts, SelectionAttempt{AdapterID: id, Outcome: OutcomePolicyDenied})
			continue
		}
		worker, err := s.registry.Resolve(id)
		if err != nil {
			selection.Attempts = append(selection.Attempts, SelectionAttempt{AdapterID: id, Outcome: OutcomeUnavailable})
			continue
		}
		if s.eligibility != nil && !s.eligibility(worker) {
			selection.Attempts = append(selection.Attempts, SelectionAttempt{AdapterID: id, Outcome: OutcomeNotLaunchCapable})
			continue
		}
		if err := ctx.Err(); err != nil {
			return selection, err
		}
		snapshot, err := worker.Probe(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return selection, err
			}
			selection.Attempts = append(selection.Attempts, SelectionAttempt{AdapterID: id, Outcome: OutcomeProbeFailed})
			continue
		}
		if err := ctx.Err(); err != nil {
			return selection, err
		}
		adapterID, status, err := decodeCapabilitySnapshot(snapshot)
		if err != nil {
			selection.Attempts = append(selection.Attempts, SelectionAttempt{AdapterID: id, Outcome: OutcomeInvalidSnapshot})
			continue
		}
		if adapterID != id {
			selection.Attempts = append(selection.Attempts, SelectionAttempt{AdapterID: id, Outcome: OutcomeIdentityMismatch})
			continue
		}
		if status != "supported" {
			selection.Attempts = append(selection.Attempts, SelectionAttempt{AdapterID: id, Outcome: OutcomeUnsupported})
			continue
		}
		selection.Adapter = worker
		selection.Capability = snapshot
		selection.Attempts = append(selection.Attempts, SelectionAttempt{AdapterID: id, Outcome: OutcomeSelected})
		return selection, nil
	}
	return selection, selectionError(selection.Attempts)
}

func validateCandidates(request SelectionRequest) ([]string, error) {
	candidates := make([]string, 0, 1+len(request.FallbackAdapters))
	appendCandidate := func(raw, role string) error {
		id := strings.TrimSpace(raw)
		if id == "" {
			return port.Permanentf("select adapter: empty %s", role)
		}
		if slices.Contains(candidates, id) {
			return port.Permanentf("select adapter: duplicate candidate %q", id)
		}
		candidates = append(candidates, id)
		return nil
	}
	if err := appendCandidate(request.PreferredAdapter, "preferredAdapter"); err != nil {
		return nil, err
	}
	for index, raw := range request.FallbackAdapters {
		if err := appendCandidate(raw, fmt.Sprintf("fallbackAdapters[%d]", index)); err != nil {
			return nil, err
		}
	}
	return candidates, nil
}

func decodeCapabilitySnapshot(record domain.Record) (adapterID, status string, err error) {
	if record.Kind != domain.KindCapabilitySnapshot {
		return "", "", fmt.Errorf("probe returned record kind %q", record.Kind)
	}
	var snapshot struct {
		AdapterID   string `json:"adapterId"`
		ProbeStatus string `json:"probeStatus"`
	}
	if err := json.Unmarshal(record.Data, &snapshot); err != nil {
		return "", "", fmt.Errorf("decode capability snapshot: %w", err)
	}
	return snapshot.AdapterID, snapshot.ProbeStatus, nil
}

// selectionError aggregates failures using only fixed outcome codes and counts,
// never provider output or environment content.
func selectionError(attempts []SelectionAttempt) error {
	counts := make(map[string]int, len(outcomeOrder))
	for _, attempt := range attempts {
		counts[attempt.Outcome]++
	}
	parts := make([]string, 0, len(outcomeOrder))
	for _, outcome := range outcomeOrder {
		if counts[outcome] > 0 {
			parts = append(parts, fmt.Sprintf("%s:%d", outcome, counts[outcome]))
		}
	}
	return fmt.Errorf("no explicit adapter candidate produced a supported capability snapshot (%s)", strings.Join(parts, ", "))
}
