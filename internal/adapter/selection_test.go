package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/port"
)

type probeSpy struct {
	id     string
	record domain.Record
	err    error
	calls  int
}

func (s *probeSpy) ID() string { return s.id }

func (s *probeSpy) Probe(context.Context) (domain.Record, error) {
	s.calls++
	return s.record, s.err
}

func (s *probeSpy) Run(context.Context, domain.Record) (domain.Record, error) {
	return domain.Record{}, nil
}

var _ port.WorkerAdapter = (*probeSpy)(nil)

func capabilityRecord(adapterID, status string) domain.Record {
	data, err := json.Marshal(map[string]any{
		"apiVersion":     string(domain.APIVersionV1Alpha1),
		"kind":           string(domain.KindCapabilitySnapshot),
		"adapterId":      adapterID,
		"adapterVersion": "1.0.0",
		"executable":     "/opt/agents/" + adapterID + "/bin/" + adapterID,
		"binaryVersion":  "1.0.0",
		"probeStatus":    status,
		"capabilities": map[string]any{
			"structuredOutput":   []string{"jsonl"},
			"nonInteractiveEdit": true,
			"sessionPolicies":    []string{"ephemeral"},
			"modelSelection":     false,
			"executionProfiles":  []string{"workspace-write"},
			"nativeBudgets":      []string{"wall-time"},
		},
		"probedAt": "2026-08-04T00:00:00Z",
	})
	if err != nil {
		panic(err)
	}
	return domain.Record{Kind: domain.KindCapabilitySnapshot, Data: data}
}

func registryWith(t *testing.T, workers ...port.WorkerAdapter) *Registry {
	t.Helper()
	registry := NewRegistry()
	for _, worker := range workers {
		if err := registry.Register(worker); err != nil {
			t.Fatalf("Register(%q) error = %v", worker.ID(), err)
		}
	}
	return registry
}

func TestSelectExplicitAdapterOrder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		build          func(t *testing.T) (*Registry, map[string]*probeSpy)
		preferred      string
		fallbacks      []string
		allowed        []string
		wantAdapter    string
		wantAttempts   []SelectionAttempt
		wantProbeCalls map[string]int
		wantErr        string
	}{
		{
			name: "preferred success stops before fallback",
			build: func(t *testing.T) (*Registry, map[string]*probeSpy) {
				opencode := &probeSpy{id: "opencode", record: capabilityRecord("opencode", "supported")}
				qwen := &probeSpy{id: "qwen", record: capabilityRecord("qwen", "supported")}
				return registryWith(t, opencode, qwen), map[string]*probeSpy{"opencode": opencode, "qwen": qwen}
			},
			preferred:   "opencode",
			fallbacks:   []string{"qwen"},
			allowed:     []string{"opencode", "qwen"},
			wantAdapter: "opencode",
			wantAttempts: []SelectionAttempt{
				{AdapterID: "opencode", Outcome: OutcomeSelected},
			},
			wantProbeCalls: map[string]int{"opencode": 1, "qwen": 0},
		},
		{
			name: "explicit fallback after unavailable preferred",
			build: func(t *testing.T) (*Registry, map[string]*probeSpy) {
				pi := &probeSpy{id: "pi", record: capabilityRecord("pi", "supported")}
				return registryWith(t, pi), map[string]*probeSpy{"pi": pi}
			},
			preferred:   "opencode",
			fallbacks:   []string{"qwen", "pi"},
			allowed:     []string{"opencode", "qwen", "pi"},
			wantAdapter: "pi",
			wantAttempts: []SelectionAttempt{
				{AdapterID: "opencode", Outcome: OutcomeUnavailable},
				{AdapterID: "qwen", Outcome: OutcomeUnavailable},
				{AdapterID: "pi", Outcome: OutcomeSelected},
			},
			wantProbeCalls: map[string]int{"pi": 1},
		},
		{
			name: "policy deny skips probe and records reason",
			build: func(t *testing.T) (*Registry, map[string]*probeSpy) {
				opencode := &probeSpy{id: "opencode", record: capabilityRecord("opencode", "supported")}
				qwen := &probeSpy{id: "qwen", record: capabilityRecord("qwen", "supported")}
				return registryWith(t, opencode, qwen), map[string]*probeSpy{"opencode": opencode, "qwen": qwen}
			},
			preferred:   "opencode",
			fallbacks:   []string{"qwen"},
			allowed:     []string{"qwen"},
			wantAdapter: "qwen",
			wantAttempts: []SelectionAttempt{
				{AdapterID: "opencode", Outcome: OutcomePolicyDenied},
				{AdapterID: "qwen", Outcome: OutcomeSelected},
			},
			wantProbeCalls: map[string]int{"opencode": 0, "qwen": 1},
		},
		{
			name:      "duplicate preferred in fallback rejected",
			preferred: "qwen",
			fallbacks: []string{"qwen"},
			allowed:   []string{"qwen"},
			wantErr:   `duplicate candidate "qwen"`,
		},
		{
			name:      "duplicate fallback entries rejected",
			preferred: "opencode",
			fallbacks: []string{"qwen", "pi", "qwen"},
			allowed:   []string{"opencode", "qwen", "pi"},
			wantErr:   `duplicate candidate "qwen"`,
		},
		{
			name:      "empty preferred rejected",
			preferred: "   ",
			allowed:   []string{"qwen"},
			wantErr:   "empty preferredAdapter",
		},
		{
			name:      "empty fallback rejected",
			preferred: "opencode",
			fallbacks: []string{"qwen", ""},
			allowed:   []string{"opencode", "qwen"},
			wantErr:   "empty fallbackAdapters[1]",
		},
		{
			name: "identity mismatch rejected",
			build: func(t *testing.T) (*Registry, map[string]*probeSpy) {
				qwen := &probeSpy{id: "qwen", record: capabilityRecord("opencode", "supported")}
				return registryWith(t, qwen), map[string]*probeSpy{"qwen": qwen}
			},
			preferred: "qwen",
			allowed:   []string{"qwen"},
			wantAttempts: []SelectionAttempt{
				{AdapterID: "qwen", Outcome: OutcomeIdentityMismatch},
			},
			wantProbeCalls: map[string]int{"qwen": 1},
			wantErr:        "identity-mismatch:1",
		},
		{
			name: "unsupported probe status rejected",
			build: func(t *testing.T) (*Registry, map[string]*probeSpy) {
				qwen := &probeSpy{id: "qwen", record: capabilityRecord("qwen", "unsupported")}
				pi := &probeSpy{id: "pi", record: capabilityRecord("pi", "experimental")}
				return registryWith(t, qwen, pi), map[string]*probeSpy{"qwen": qwen, "pi": pi}
			},
			preferred: "qwen",
			fallbacks: []string{"pi"},
			allowed:   []string{"qwen", "pi"},
			wantAttempts: []SelectionAttempt{
				{AdapterID: "qwen", Outcome: OutcomeUnsupported},
				{AdapterID: "pi", Outcome: OutcomeUnsupported},
			},
			wantProbeCalls: map[string]int{"qwen": 1, "pi": 1},
			wantErr:        "unsupported:2",
		},
		{
			name: "probe error recorded without leaking detail",
			build: func(t *testing.T) (*Registry, map[string]*probeSpy) {
				leak := errors.New("spawn /Users/operator/.opencode/auth/token: permission denied")
				qwen := &probeSpy{id: "qwen", err: leak}
				return registryWith(t, qwen), map[string]*probeSpy{"qwen": qwen}
			},
			preferred: "qwen",
			allowed:   []string{"qwen"},
			wantAttempts: []SelectionAttempt{
				{AdapterID: "qwen", Outcome: OutcomeProbeFailed},
			},
			wantProbeCalls: map[string]int{"qwen": 1},
			wantErr:        "probe-failed:1",
		},
		{
			name: "invalid snapshot rejected",
			build: func(t *testing.T) (*Registry, map[string]*probeSpy) {
				qwen := &probeSpy{id: "qwen", record: domain.Record{Kind: domain.KindCapabilitySnapshot, Data: json.RawMessage(`{"adapterId":`)}}
				return registryWith(t, qwen), map[string]*probeSpy{"qwen": qwen}
			},
			preferred: "qwen",
			allowed:   []string{"qwen"},
			wantAttempts: []SelectionAttempt{
				{AdapterID: "qwen", Outcome: OutcomeInvalidSnapshot},
			},
			wantErr: "invalid-snapshot:1",
		},
		{
			name: "wrong record kind rejected",
			build: func(t *testing.T) (*Registry, map[string]*probeSpy) {
				qwen := &probeSpy{id: "qwen", record: domain.Record{Kind: domain.KindWorkerResult, Data: json.RawMessage(`{}`)}}
				return registryWith(t, qwen), map[string]*probeSpy{"qwen": qwen}
			},
			preferred: "qwen",
			allowed:   []string{"qwen"},
			wantAttempts: []SelectionAttempt{
				{AdapterID: "qwen", Outcome: OutcomeInvalidSnapshot},
			},
			wantErr: "invalid-snapshot:1",
		},
		{
			name: "none available aggregates every reason",
			build: func(t *testing.T) (*Registry, map[string]*probeSpy) {
				opencode := &probeSpy{id: "opencode", record: capabilityRecord("opencode", "supported")}
				qwen := &probeSpy{id: "qwen", err: errors.New("boom")}
				pi := &probeSpy{id: "pi", record: capabilityRecord("pi", "unsupported")}
				extra := &probeSpy{id: "extra", record: capabilityRecord("extra", "supported")}
				return registryWith(t, opencode, qwen, pi, extra), map[string]*probeSpy{"opencode": opencode, "qwen": qwen, "pi": pi, "extra": extra}
			},
			preferred: "opencode",
			fallbacks: []string{"qwen", "pi", "ghost"},
			allowed:   []string{"qwen", "pi", "ghost"},
			wantAttempts: []SelectionAttempt{
				{AdapterID: "opencode", Outcome: OutcomePolicyDenied},
				{AdapterID: "qwen", Outcome: OutcomeProbeFailed},
				{AdapterID: "pi", Outcome: OutcomeUnsupported},
				{AdapterID: "ghost", Outcome: OutcomeUnavailable},
			},
			wantProbeCalls: map[string]int{"opencode": 0, "qwen": 1, "pi": 1, "extra": 0},
			wantErr:        "no explicit adapter candidate produced a supported capability snapshot (policy-denied:1, unavailable:1, probe-failed:1, unsupported:1)",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var registry *Registry
			var spies map[string]*probeSpy
			if test.build != nil {
				registry, spies = test.build(t)
			} else {
				registry, spies = NewRegistry(), map[string]*probeSpy{}
			}
			selector, err := NewSelector(registry)
			if err != nil {
				t.Fatalf("NewSelector() error = %v", err)
			}
			selection, err := selector.Select(context.Background(), SelectionRequest{
				PreferredAdapter: test.preferred,
				FallbackAdapters: test.fallbacks,
				AllowedAdapters:  test.allowed,
			})
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("Select() error = %v, want error containing %q", err, test.wantErr)
				}
				if selection.Adapter != nil {
					t.Fatalf("Select() returned adapter %q despite error", selection.Adapter.ID())
				}
				if strings.Contains(err.Error(), "/Users/") || strings.Contains(err.Error(), "token") {
					t.Fatalf("Select() error leaks environment content: %v", err)
				}
			} else if err != nil {
				t.Fatalf("Select() error = %v", err)
			}
			if test.wantAdapter != "" {
				if selection.Adapter == nil || selection.Adapter.ID() != test.wantAdapter {
					t.Fatalf("Select() adapter = %v, want %q", selection.Adapter, test.wantAdapter)
				}
				if selection.Capability.Kind != domain.KindCapabilitySnapshot {
					t.Fatalf("Select() capability kind = %q, want %q", selection.Capability.Kind, domain.KindCapabilitySnapshot)
				}
				if !bytes.Contains(selection.Capability.Data, []byte(`"adapterId":"`+test.wantAdapter+`"`)) {
					t.Fatalf("Select() capability does not carry selected adapter identity: %s", selection.Capability.Data)
				}
			}
			if len(selection.Attempts) != len(test.wantAttempts) {
				t.Fatalf("Select() attempts = %+v, want %+v", selection.Attempts, test.wantAttempts)
			}
			for index, want := range test.wantAttempts {
				if selection.Attempts[index] != want {
					t.Fatalf("Select() attempts[%d] = %+v, want %+v", index, selection.Attempts[index], want)
				}
			}
			for id, wantCalls := range test.wantProbeCalls {
				if spies[id].calls != wantCalls {
					t.Fatalf("Probe(%q) called %d times, want %d", id, spies[id].calls, wantCalls)
				}
			}
		})
	}
}

func TestSelectNeverLeavesExplicitCandidateList(t *testing.T) {
	t.Parallel()

	supported := &probeSpy{id: "pi", record: capabilityRecord("pi", "supported")}
	selector, err := NewSelector(registryWith(t, supported))
	if err != nil {
		t.Fatalf("NewSelector() error = %v", err)
	}
	if _, err := selector.Select(context.Background(), SelectionRequest{
		PreferredAdapter: "opencode",
		FallbackAdapters: []string{"qwen"},
		AllowedAdapters:  []string{"opencode", "qwen", "pi"},
	}); err == nil {
		t.Fatal("Select() implicitly fell back to an unlisted adapter")
	}
	if supported.calls != 0 {
		t.Fatalf("Probe(pi) called %d times, want 0", supported.calls)
	}
}

func TestEligibleSelectorRejectsBeforeProbeAndKeepsCompatibilitySelector(t *testing.T) {
	t.Parallel()

	ordinary := &probeSpy{id: "qwen", record: capabilityRecord("qwen", "supported")}
	launch := &probeSpy{id: "pi", record: capabilityRecord("pi", "supported")}
	registry := registryWith(t, ordinary, launch)
	production, err := NewEligibleSelector(registry, func(worker port.WorkerAdapter) bool {
		return worker.ID() == "pi"
	})
	if err != nil {
		t.Fatal(err)
	}
	selection, err := production.Select(context.Background(), SelectionRequest{
		PreferredAdapter: "qwen",
		FallbackAdapters: []string{"pi"},
		AllowedAdapters:  []string{"qwen", "pi"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if selection.Adapter != launch || ordinary.calls != 0 || launch.calls != 1 {
		t.Fatalf("production selection=%+v ordinaryCalls=%d launchCalls=%d", selection, ordinary.calls, launch.calls)
	}
	want := []SelectionAttempt{{AdapterID: "qwen", Outcome: OutcomeNotLaunchCapable}, {AdapterID: "pi", Outcome: OutcomeSelected}}
	if !reflect.DeepEqual(selection.Attempts, want) {
		t.Fatalf("production attempts=%+v, want %+v", selection.Attempts, want)
	}

	compatibility, err := NewSelector(registry)
	if err != nil {
		t.Fatal(err)
	}
	selection, err = compatibility.Select(context.Background(), SelectionRequest{
		PreferredAdapter: "qwen", AllowedAdapters: []string{"qwen"},
	})
	if err != nil || selection.Adapter != ordinary || ordinary.calls != 1 {
		t.Fatalf("compatibility selection=%+v err=%v calls=%d", selection, err, ordinary.calls)
	}
}

func TestNewEligibleSelectorRejectsNilPredicate(t *testing.T) {
	t.Parallel()
	selector, err := NewEligibleSelector(NewRegistry(), nil)
	if selector != nil || err == nil || !port.IsPermanent(err) {
		t.Fatalf("selector=%+v err=%v, want permanent fail-closed error", selector, err)
	}
}

func TestNewSelectorNilRegistryIsPermanentError(t *testing.T) {
	t.Parallel()

	selector, err := NewSelector(nil)
	if err == nil {
		t.Fatal("NewSelector(nil) returned nil error")
	}
	if selector != nil {
		t.Fatalf("NewSelector(nil) returned selector %+v, want nil", selector)
	}
	if !port.IsPermanent(err) {
		t.Fatalf("NewSelector(nil) error = %v, want permanent error", err)
	}
	if !strings.Contains(err.Error(), "nil registry") {
		t.Fatalf("NewSelector(nil) error = %v, want mention of nil registry", err)
	}
}

func TestSelectNilRegistryFailsClosed(t *testing.T) {
	t.Parallel()

	request := SelectionRequest{
		PreferredAdapter: "qwen",
		AllowedAdapters:  []string{"qwen"},
	}
	assertPermanent := func(t *testing.T, err error, what string) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s returned nil error", what)
		}
		if !port.IsPermanent(err) {
			t.Fatalf("%s error = %v, want permanent error", what, err)
		}
		if strings.Contains(err.Error(), "panic") {
			t.Fatalf("%s error mentions panic: %v", what, err)
		}
	}

	var zeroValue Selector
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				t.Fatalf("zero-value Selector.Select() panicked: %v", recovered)
			}
		}()
		selection, err := zeroValue.Select(context.Background(), request)
		assertPermanent(t, err, "zero-value Selector.Select()")
		if selection.Adapter != nil || len(selection.Attempts) != 0 {
			t.Fatalf("zero-value Selector.Select() selection = %+v, want empty", selection)
		}
	}()

	func() {
		var nilSelector *Selector
		defer func() {
			if recovered := recover(); recovered != nil {
				t.Fatalf("nil Selector.Select() panicked: %v", recovered)
			}
		}()
		selection, err := nilSelector.Select(context.Background(), request)
		assertPermanent(t, err, "nil Selector.Select()")
		if selection.Adapter != nil || len(selection.Attempts) != 0 {
			t.Fatalf("nil Selector.Select() selection = %+v, want empty", selection)
		}
	}()
}

func TestSelectAbortsOnProbeContextError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		probeErr  error
		wantError error
	}{
		{name: "probe canceled", probeErr: context.Canceled, wantError: context.Canceled},
		{name: "probe deadline exceeded", probeErr: context.DeadlineExceeded, wantError: context.DeadlineExceeded},
		{name: "wrapped canceled", probeErr: fmt.Errorf("probe qwen: %w", context.Canceled), wantError: context.Canceled},
		{name: "wrapped deadline", probeErr: fmt.Errorf("probe qwen: %w", context.DeadlineExceeded), wantError: context.DeadlineExceeded},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			qwen := &probeSpy{id: "qwen", err: test.probeErr}
			pi := &probeSpy{id: "pi", record: capabilityRecord("pi", "supported")}
			selector, err := NewSelector(registryWith(t, qwen, pi))
			if err != nil {
				t.Fatalf("NewSelector() error = %v", err)
			}
			selection, err := selector.Select(context.Background(), SelectionRequest{
				PreferredAdapter: "qwen",
				FallbackAdapters: []string{"pi"},
				AllowedAdapters:  []string{"qwen", "pi"},
			})
			if !errors.Is(err, test.wantError) {
				t.Fatalf("Select() error = %v, want %v", err, test.wantError)
			}
			if selection.Adapter != nil {
				t.Fatalf("Select() adapter = %q, want none", selection.Adapter.ID())
			}
			for _, attempt := range selection.Attempts {
				if attempt.AdapterID == "pi" {
					t.Fatalf("Select() continued to fallback after context error: %+v", selection.Attempts)
				}
			}
			if qwen.calls != 1 {
				t.Fatalf("Probe(qwen) called %d times, want 1", qwen.calls)
			}
			if pi.calls != 0 {
				t.Fatalf("Probe(pi) called %d times, want 0", pi.calls)
			}
		})
	}
}

type cancelDuringProbeSpy struct {
	id     string
	record domain.Record
	cancel context.CancelFunc
	calls  int
}

func (s *cancelDuringProbeSpy) ID() string { return s.id }

func (s *cancelDuringProbeSpy) Probe(context.Context) (domain.Record, error) {
	s.calls++
	s.cancel()
	return s.record, nil
}

func (s *cancelDuringProbeSpy) Run(context.Context, domain.Record) (domain.Record, error) {
	return domain.Record{}, nil
}

var _ port.WorkerAdapter = (*cancelDuringProbeSpy)(nil)

func TestSelectAbortsWhenContextDoneAfterProbe(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	qwen := &cancelDuringProbeSpy{id: "qwen", record: capabilityRecord("qwen", "supported"), cancel: cancel}
	pi := &probeSpy{id: "pi", record: capabilityRecord("pi", "supported")}
	selector, err := NewSelector(registryWith(t, qwen, pi))
	if err != nil {
		t.Fatalf("NewSelector() error = %v", err)
	}
	selection, err := selector.Select(ctx, SelectionRequest{
		PreferredAdapter: "qwen",
		FallbackAdapters: []string{"pi"},
		AllowedAdapters:  []string{"qwen", "pi"},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Select() error = %v, want context.Canceled", err)
	}
	if selection.Adapter != nil {
		t.Fatalf("Select() adapter = %q, want none", selection.Adapter.ID())
	}
	for _, attempt := range selection.Attempts {
		if attempt.AdapterID == "pi" {
			t.Fatalf("Select() continued to fallback after canceled context: %+v", selection.Attempts)
		}
	}
	if qwen.calls != 1 {
		t.Fatalf("Probe(qwen) called %d times, want 1", qwen.calls)
	}
	if pi.calls != 0 {
		t.Fatalf("Probe(pi) called %d times, want 0", pi.calls)
	}
}
