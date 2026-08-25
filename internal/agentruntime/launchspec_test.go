package agentruntime

import (
	"context"
	"testing"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
)

// ── helpers ──────────────────────────────────────────────────────────────────

func fixedDigest(seed string) string {
	return canonical.DigestBytes([]byte(seed))
}

func validSpec(t *testing.T) AgentLaunchSpec {
	t.Helper()
	s, err := NewAgentLaunchSpec(
		"adapter-id", "1.0.0",
		"run-1", "attempt-1",
		"/usr/bin/agent", fixedDigest("exe"),
		"/workdir",
		[]string{"--flag"},
		[]string{"HOME=/home/agent"},
		fixedDigest("profile"),
		"",
	)
	if err != nil {
		t.Fatalf("validSpec: %v", err)
	}
	return s
}

// ── AgentLaunchSpec immutability & digest stability ──────────────────────────

func TestAgentLaunchSpec_Validate_Valid(t *testing.T) {
	if err := validSpec(t).Validate(); err != nil {
		t.Fatalf("expected no error: %v", err)
	}
}

func TestAgentLaunchSpec_Digest_Stable(t *testing.T) {
	s := validSpec(t)
	d1, err := s.Digest()
	if err != nil {
		t.Fatalf("digest 1: %v", err)
	}
	d2, err := s.Digest()
	if err != nil {
		t.Fatalf("digest 2: %v", err)
	}
	if d1 != d2 {
		t.Fatalf("digest not stable: %s != %s", d1, d2)
	}
}

func TestAgentLaunchSpec_Digest_DifferentForDifferentSpecs(t *testing.T) {
	s1, _ := NewAgentLaunchSpec(
		"adapter-a", "1.0.0", "run-1", "attempt-1",
		"/bin/a", fixedDigest("exe-a"), "/wd",
		nil, nil, fixedDigest("profile-a"), "",
	)
	s2, _ := NewAgentLaunchSpec(
		"adapter-b", "1.0.0", "run-1", "attempt-1",
		"/bin/b", fixedDigest("exe-b"), "/wd",
		nil, nil, fixedDigest("profile-b"), "",
	)
	d1, _ := s1.Digest()
	d2, _ := s2.Digest()
	if d1 == d2 {
		t.Fatalf("different specs produced identical digest: %s", d1)
	}
}

// slices returned by NewAgentLaunchSpec must be independent copies.
func TestAgentLaunchSpec_Immutable_SlicesIndependent(t *testing.T) {
	args := []string{"--flag"}
	env := []string{"K=V"}
	s, _ := NewAgentLaunchSpec(
		"id", "1.0", "r", "a",
		"/exe", fixedDigest("x"), "/wd",
		args, env, fixedDigest("p"), "",
	)
	args[0] = "MUTATED"
	env[0] = "MUTATED"
	if s.Arguments[0] == "MUTATED" {
		t.Fatal("Arguments slice was not copied; mutation visible in spec")
	}
	if s.Environment[0] == "MUTATED" {
		t.Fatal("Environment slice was not copied; mutation visible in spec")
	}
}

// ── Validate fail-closed cases ────────────────────────────────────────────────

func TestAgentLaunchSpec_Validate_FailClosed(t *testing.T) {
	base := func() AgentLaunchSpec {
		return AgentLaunchSpec{
			AdapterID:        "id",
			AdapterVersion:   "1.0",
			RunID:            "run",
			AttemptID:        "attempt",
			Executable:       "/bin/agent",
			ExecutableDigest: fixedDigest("exe"),
			WorkingDirectory: "/wd",
			ProfileDigest:    fixedDigest("profile"),
		}
	}

	tests := []struct {
		name   string
		mutate func(*AgentLaunchSpec)
	}{
		{"empty AdapterID", func(s *AgentLaunchSpec) { s.AdapterID = "" }},
		{"blank AdapterVersion", func(s *AgentLaunchSpec) { s.AdapterVersion = "  " }},
		{"empty RunID", func(s *AgentLaunchSpec) { s.RunID = "" }},
		{"empty AttemptID", func(s *AgentLaunchSpec) { s.AttemptID = "" }},
		{"empty Executable", func(s *AgentLaunchSpec) { s.Executable = "" }},
		{"empty WorkingDirectory", func(s *AgentLaunchSpec) { s.WorkingDirectory = "" }},
		{"ExecutableDigest no prefix", func(s *AgentLaunchSpec) { s.ExecutableDigest = "abcd1234" }},
		{"ExecutableDigest wrong length", func(s *AgentLaunchSpec) { s.ExecutableDigest = "sha256:abc" }},
		{"ExecutableDigest uppercase", func(s *AgentLaunchSpec) {
			s.ExecutableDigest = "sha256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
		}},
		{"ProfileDigest empty", func(s *AgentLaunchSpec) { s.ProfileDigest = "" }},
		{"ProfileDigest no prefix", func(s *AgentLaunchSpec) { s.ProfileDigest = "badhex" }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := base()
			tc.mutate(&s)
			if err := s.Validate(); err == nil {
				t.Fatalf("expected error for case %q but got nil", tc.name)
			}
		})
	}
}

// ── Compat mapping: provenance & ProbeCompat ─────────────────────────────────

type stubAdapter struct {
	id string
}

func (s *stubAdapter) ID() string { return s.id }
func (s *stubAdapter) Probe(_ context.Context) (domain.Record, error) {
	return domain.Record{Kind: domain.KindWorkerRequest}, nil
}
func (s *stubAdapter) Run(_ context.Context, _ domain.Record) (domain.Record, error) {
	return domain.Record{Kind: domain.KindWorkerResult}, nil
}

func TestCompatRuntime_PrepareLaunch_ProvenanceMark(t *testing.T) {
	rt := NewCompatRuntime(&stubAdapter{id: "stub"})
	cr := rt.(*compatRuntime)
	spec, err := cr.PrepareLaunch(
		"run-1", "attempt-1",
		"/bin/stub", fixedDigest("exe"),
		"/wd", nil, nil, fixedDigest("profile"),
	)
	if err != nil {
		t.Fatalf("PrepareLaunch: %v", err)
	}
	if spec.MigrationProvenance != MigrationProvenance {
		t.Fatalf("expected MigrationProvenance=%q got %q", MigrationProvenance, spec.MigrationProvenance)
	}
	if spec.AdapterID != "stub" {
		t.Fatalf("expected AdapterID=stub got %s", spec.AdapterID)
	}
}

func TestCompatRuntime_PrepareLaunch_FailClosed_BadProfileDigest(t *testing.T) {
	rt := NewCompatRuntime(&stubAdapter{id: "stub"})
	cr := rt.(*compatRuntime)
	_, err := cr.PrepareLaunch(
		"run-1", "attempt-1",
		"/bin/stub", fixedDigest("exe"),
		"/wd", nil, nil, "not-a-digest",
	)
	if err == nil {
		t.Fatal("expected error for bad profileDigest but got nil")
	}
}

func TestProbeCompat_Delegates(t *testing.T) {
	rt := NewCompatRuntime(&stubAdapter{id: "probe-test"})
	rec, err := ProbeCompat(context.Background(), rt)
	if err != nil {
		t.Fatalf("ProbeCompat: %v", err)
	}
	if rec.Kind != domain.KindWorkerRequest {
		t.Fatalf("unexpected kind: %v", rec.Kind)
	}
}

func TestProbeCompat_FailClosed_NonCompat(t *testing.T) {
	_, err := ProbeCompat(context.Background(), &FakeAgent{})
	if err == nil {
		t.Fatal("expected error for non-compat runtime")
	}
}

// ── Compat DecodeEvent / FinalizeResult ───────────────────────────────────────

func TestCompatRuntime_DecodeEvent_Valid(t *testing.T) {
	rt := NewCompatRuntime(&stubAdapter{id: "id"})
	ev, err := rt.DecodeEvent([]byte(`{"key":"value"}`))
	if err != nil {
		t.Fatalf("DecodeEvent: %v", err)
	}
	if ev.Sequence != 1 {
		t.Fatalf("expected sequence=1 got %d", ev.Sequence)
	}
}

func TestCompatRuntime_DecodeEvent_FailClosed(t *testing.T) {
	rt := NewCompatRuntime(&stubAdapter{id: "id"})
	tests := []struct {
		name string
		raw  []byte
	}{
		{"empty", []byte{}},
		{"malformed JSON", []byte(`{bad json}`)},
		{"nil", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := rt.DecodeEvent(tc.raw)
			if err == nil {
				t.Fatal("expected error but got nil")
			}
		})
	}
}

func TestCompatRuntime_FinalizeResult_FailClosed_EmptyEvents(t *testing.T) {
	rt := NewCompatRuntime(&stubAdapter{id: "id"})
	_, err := rt.FinalizeResult(nil, ExecEvidence{})
	if err == nil {
		t.Fatal("expected error for empty events")
	}
}

func TestCompatRuntime_FinalizeResult_UntrustedResult(t *testing.T) {
	rt := NewCompatRuntime(&stubAdapter{id: "id"})
	ev, _ := rt.DecodeEvent([]byte(`{"k":"v"}`))
	res, err := rt.FinalizeResult([]AgentEvent{ev}, ExecEvidence{ExitCode: 0})
	if err != nil {
		t.Fatalf("FinalizeResult: %v", err)
	}
	if res.Trusted {
		t.Fatal("WorkloadResult.Trusted must be false")
	}
	if res.ProviderHint != MigrationProvenance {
		t.Fatalf("expected ProviderHint=%q got %q", MigrationProvenance, res.ProviderHint)
	}
}

// ── FakeAgent determinism ─────────────────────────────────────────────────────

func TestFakeAgent_DecodeEvent_Deterministic(t *testing.T) {
	fa := &FakeAgent{}
	input := []byte(`{"x":1}`)
	ev1, err := fa.DecodeEvent(input)
	if err != nil {
		t.Fatalf("first DecodeEvent: %v", err)
	}
	ev2, err := fa.DecodeEvent(input)
	if err != nil {
		t.Fatalf("second DecodeEvent: %v", err)
	}
	if string(ev1.Raw) != string(ev2.Raw) {
		t.Fatalf("non-deterministic: %s != %s", ev1.Raw, ev2.Raw)
	}
}

func TestFakeAgent_FinalizeResult_Deterministic(t *testing.T) {
	fa := &FakeAgent{FixedHint: "h"}
	ev := AgentEvent{Sequence: 1, Raw: []byte(`{"fake":true}`)}
	evidence := ExecEvidence{ExitCode: 0}

	r1, err := fa.FinalizeResult([]AgentEvent{ev}, evidence)
	if err != nil {
		t.Fatalf("first FinalizeResult: %v", err)
	}
	r2, err := fa.FinalizeResult([]AgentEvent{ev}, evidence)
	if err != nil {
		t.Fatalf("second FinalizeResult: %v", err)
	}
	if r1 != r2 {
		t.Fatalf("non-deterministic results: %+v != %+v", r1, r2)
	}
	if r1.Trusted {
		t.Fatal("WorkloadResult.Trusted must be false")
	}
}

func TestFakeAgent_DecodeEvent_FailClosed(t *testing.T) {
	fa := &FakeAgent{}
	tests := []struct {
		name string
		raw  []byte
	}{
		{"empty", []byte{}},
		{"malformed", []byte(`not json`)},
		{"nil", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := fa.DecodeEvent(tc.raw)
			if err == nil {
				t.Fatalf("expected error for %q", tc.name)
			}
		})
	}
}

func TestFakeAgent_FinalizeResult_FailClosed_EmptyEvents(t *testing.T) {
	fa := &FakeAgent{}
	_, err := fa.FinalizeResult([]AgentEvent{}, ExecEvidence{})
	if err == nil {
		t.Fatal("expected error for empty events")
	}
}

func TestFakeAgent_FinalizeResult_UntrustedResult(t *testing.T) {
	fa := &FakeAgent{}
	ev, _ := fa.DecodeEvent([]byte(`{"a":1}`))
	res, err := fa.FinalizeResult([]AgentEvent{ev}, ExecEvidence{ExitCode: 42})
	if err != nil {
		t.Fatalf("FinalizeResult: %v", err)
	}
	if res.Trusted {
		t.Fatal("WorkloadResult.Trusted must be false")
	}
	if res.ExitCode != 42 {
		t.Fatalf("ExitCode not propagated: got %d", res.ExitCode)
	}
}
