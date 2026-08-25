package agentregistry

import (
	"strings"
	"testing"
	"time"
)

// ── test fixtures ─────────────────────────────────────────────────────────────

const (
	validDigest  = "sha256:0000000000000000000000000000000000000000000000000000000000000001"
	validDigest2 = "sha256:0000000000000000000000000000000000000000000000000000000000000002"
	validDigest3 = "sha256:0000000000000000000000000000000000000000000000000000000000000003"
)

var baseTime = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func validReg() AgentRegistration {
	return AgentRegistration{
		RegistrationID:       "registration:0001",
		AuthorityNamespaceID: "ns-1",
		SecurityDomainID:     "sd-1",
		Principal:            "principal-1",
		ProviderType:         ProviderTypeAgent,
		ProviderName:         "test-agent",
		ProviderVersion:      "1.0.0",
		ProtocolVersion:      "v1",
		Scope:                "task",
		IdempotencyKey:       "key-1",
		RequestDigest:        validDigest,
		LifecycleState:       LifecycleStateActive,
		CreatedAt:            baseTime,
		UpdatedAt:            baseTime,
	}
}

func validSnap(regID, snapDigest string) AgentCapabilitySnapshot {
	return AgentCapabilitySnapshot{
		SnapshotDigest:  snapDigest,
		RegistrationID:  regID,
		ProtocolVersion: "v1",
		ProviderName:    "test-agent",
		ProviderVersion: "1.0.0",
		Capabilities: []Capability{
			CapabilityExecutionProfileWorkspaceWrite,
			CapabilitySessionPolicyEphemeral,
		},
		ConformanceEvidenceDigests: []string{},
		SnapshotState:              SnapshotStateActive,
	}
}

// ── AgentRegistration.Validate ────────────────────────────────────────────────

func TestRegistrationValidate_AllFieldsRequired(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*AgentRegistration)
	}{
		{"empty RegistrationID", func(r *AgentRegistration) { r.RegistrationID = "" }},
		{"empty AuthorityNamespaceID", func(r *AgentRegistration) { r.AuthorityNamespaceID = "" }},
		{"empty SecurityDomainID", func(r *AgentRegistration) { r.SecurityDomainID = "" }},
		{"empty Principal", func(r *AgentRegistration) { r.Principal = "" }},
		{"unknown ProviderType", func(r *AgentRegistration) { r.ProviderType = "sandbox" }},
		{"empty ProviderName", func(r *AgentRegistration) { r.ProviderName = "" }},
		{"empty ProviderVersion", func(r *AgentRegistration) { r.ProviderVersion = "" }},
		{"empty ProtocolVersion", func(r *AgentRegistration) { r.ProtocolVersion = "" }},
		{"empty Scope", func(r *AgentRegistration) { r.Scope = "" }},
		{"empty IdempotencyKey", func(r *AgentRegistration) { r.IdempotencyKey = "" }},
		{"empty RequestDigest", func(r *AgentRegistration) { r.RequestDigest = "" }},
		{"malformed RequestDigest no prefix", func(r *AgentRegistration) { r.RequestDigest = "deadbeef" }},
		{"malformed RequestDigest short hex", func(r *AgentRegistration) { r.RequestDigest = "sha256:0000" }},
		{"malformed RequestDigest uppercase", func(r *AgentRegistration) {
			r.RequestDigest = "sha256:0000000000000000000000000000000000000000000000000000000000000G0F"
		}},
		{"unknown LifecycleState", func(r *AgentRegistration) { r.LifecycleState = "deleted" }},
		{"zero CreatedAt", func(r *AgentRegistration) { r.CreatedAt = time.Time{} }},
		{"zero UpdatedAt", func(r *AgentRegistration) { r.UpdatedAt = time.Time{} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg := validReg()
			tc.mutate(&reg)
			err := reg.Validate()
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.HasPrefix(err.Error(), "agentregistry:") {
				t.Errorf("error missing agentregistry: prefix: %v", err)
			}
		})
	}
}

func TestRegistrationValidate_Valid(t *testing.T) {
	if err := validReg().Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ── AgentRegistration.Digest ──────────────────────────────────────────────────

func TestRegistrationDigest_Stability(t *testing.T) {
	reg := validReg()
	d1, err := reg.Digest()
	if err != nil {
		t.Fatal(err)
	}
	d2, err := reg.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if d1 != d2 {
		t.Errorf("digest not stable: %q != %q", d1, d2)
	}
	if !strings.HasPrefix(d1, "sha256:") {
		t.Errorf("digest missing sha256: prefix: %q", d1)
	}
}

func TestRegistrationDigest_ChangeDetected(t *testing.T) {
	reg := validReg()
	d1, _ := reg.Digest()

	reg2 := reg
	reg2.ProviderName = "other-agent"
	d2, _ := reg2.Digest()

	if d1 == d2 {
		t.Error("digest should differ when ProviderName changes")
	}
}

// ── AgentCapabilitySnapshot.Validate ─────────────────────────────────────────

func TestSnapshotValidate_AllFieldsRequired(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*AgentCapabilitySnapshot)
	}{
		{"empty SnapshotDigest", func(s *AgentCapabilitySnapshot) { s.SnapshotDigest = "" }},
		{"malformed SnapshotDigest", func(s *AgentCapabilitySnapshot) { s.SnapshotDigest = "bad" }},
		{"empty RegistrationID", func(s *AgentCapabilitySnapshot) { s.RegistrationID = "" }},
		{"empty ProtocolVersion", func(s *AgentCapabilitySnapshot) { s.ProtocolVersion = "" }},
		{"empty ProviderName", func(s *AgentCapabilitySnapshot) { s.ProviderName = "" }},
		{"empty ProviderVersion", func(s *AgentCapabilitySnapshot) { s.ProviderVersion = "" }},
		{"empty Capabilities", func(s *AgentCapabilitySnapshot) { s.Capabilities = nil }},
		{"unknown Capability", func(s *AgentCapabilitySnapshot) {
			s.Capabilities = []Capability{"execution-profile:workspace-write", "unknown-cap:x"}
		}},
		{"malformed ConformanceEvidenceDigest", func(s *AgentCapabilitySnapshot) {
			s.ConformanceEvidenceDigests = []string{"not-a-digest"}
		}},
		{"unknown SnapshotState", func(s *AgentCapabilitySnapshot) { s.SnapshotState = "archived" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snap := validSnap("registration:0001", validDigest)
			tc.mutate(&snap)
			err := snap.Validate()
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.HasPrefix(err.Error(), "agentregistry:") {
				t.Errorf("error missing agentregistry: prefix: %v", err)
			}
		})
	}
}

func TestSnapshotValidate_Valid(t *testing.T) {
	snap := validSnap("registration:0001", validDigest)
	if err := snap.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ── AgentCapabilitySnapshot.Digest ───────────────────────────────────────────

func TestSnapshotDigest_Stability(t *testing.T) {
	snap := validSnap("registration:0001", validDigest)
	d1, err := snap.Digest()
	if err != nil {
		t.Fatal(err)
	}
	d2, _ := snap.Digest()
	if d1 != d2 {
		t.Errorf("digest not stable: %q != %q", d1, d2)
	}
}

func TestSnapshotDigest_ChangeDetected(t *testing.T) {
	snap := validSnap("registration:0001", validDigest)
	d1, _ := snap.Digest()

	snap2 := snap
	snap2.ProtocolVersion = "v2"
	d2, _ := snap2.Digest()

	if d1 == d2 {
		t.Error("digest should differ when ProtocolVersion changes")
	}
}

// ── EvidenceRecord.Validate ───────────────────────────────────────────────────

func TestEvidenceValidate_AllFieldsRequired(t *testing.T) {
	validEvidence := EvidenceRecord{
		EvidenceDigest: validDigest,
		EvidenceKind:   EvidenceKindAttestation,
		ProviderType:   ProviderTypeAgent,
		RegistrationID: "registration:0001",
	}
	cases := []struct {
		name   string
		mutate func(*EvidenceRecord)
	}{
		{"empty EvidenceDigest", func(e *EvidenceRecord) { e.EvidenceDigest = "" }},
		{"malformed EvidenceDigest", func(e *EvidenceRecord) { e.EvidenceDigest = "sha256:short" }},
		{"unknown EvidenceKind", func(e *EvidenceRecord) { e.EvidenceKind = "unknown-kind" }},
		{"unknown ProviderType", func(e *EvidenceRecord) { e.ProviderType = "sandbox" }},
		{"empty RegistrationID", func(e *EvidenceRecord) { e.RegistrationID = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev := validEvidence
			tc.mutate(&ev)
			err := ev.Validate()
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.HasPrefix(err.Error(), "agentregistry:") {
				t.Errorf("error missing agentregistry: prefix: %v", err)
			}
		})
	}
}

func TestEvidenceBindingToAgent(t *testing.T) {
	ev := EvidenceRecord{
		EvidenceDigest: validDigest,
		EvidenceKind:   EvidenceKindAttestation,
		ProviderType:   ProviderTypeAgent, // must be agent
		RegistrationID: "registration:0001",
	}
	if err := ev.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ── Registry: Register (idempotent) ──────────────────────────────────────────

func TestRegistry_Register_Idempotent(t *testing.T) {
	r := NewRegistry()
	reg := validReg()

	first, err := r.Register(reg)
	if err != nil {
		t.Fatalf("first Register: %v", err)
	}
	second, err := r.Register(reg)
	if err != nil {
		t.Fatalf("second Register: %v", err)
	}
	if first.RegistrationID != second.RegistrationID {
		t.Error("idempotent replay should return same registration")
	}
}

func TestRegistry_Register_ConflictDifferentDigest(t *testing.T) {
	r := NewRegistry()
	reg1 := validReg()

	if _, err := r.Register(reg1); err != nil {
		t.Fatalf("first Register: %v", err)
	}

	reg2 := validReg()
	reg2.RequestDigest = validDigest2
	reg2.RegistrationID = "registration:0002" // different ID but same key

	_, err := r.Register(reg2)
	if err == nil {
		t.Fatal("expected conflict error, got nil")
	}
	if !strings.HasPrefix(err.Error(), "agentregistry:") {
		t.Errorf("error missing agentregistry: prefix: %v", err)
	}
}

func TestRegistry_Register_InvalidInputFailClosed(t *testing.T) {
	r := NewRegistry()
	reg := validReg()
	reg.RegistrationID = ""
	_, err := r.Register(reg)
	if err == nil {
		t.Fatal("expected error for empty RegistrationID")
	}
}

// ── Registry: Lifecycle transitions ──────────────────────────────────────────

func TestRegistry_LifecycleTransitions_LegalMatrix(t *testing.T) {
	cases := []struct {
		name       string
		setup      func(*Registry, string)
		transition func(*Registry, string) (*AgentRegistration, error)
		wantState  LifecycleState
	}{
		{
			name:  "active→suspended",
			setup: func(r *Registry, id string) {},
			transition: func(r *Registry, id string) (*AgentRegistration, error) {
				return r.Suspend(id)
			},
			wantState: LifecycleStateSuspended,
		},
		{
			name: "suspended→active",
			setup: func(r *Registry, id string) {
				r.Suspend(id) //nolint:errcheck
			},
			transition: func(r *Registry, id string) (*AgentRegistration, error) {
				return r.Reactivate(id)
			},
			wantState: LifecycleStateActive,
		},
		{
			name:  "active→revoked",
			setup: func(r *Registry, id string) {},
			transition: func(r *Registry, id string) (*AgentRegistration, error) {
				return r.Revoke(id)
			},
			wantState: LifecycleStateRevoked,
		},
		{
			name:  "active→replaced",
			setup: func(r *Registry, id string) {},
			transition: func(r *Registry, id string) (*AgentRegistration, error) {
				return r.Replace(id)
			},
			wantState: LifecycleStateReplaced,
		},
		{
			name:  "active→expired",
			setup: func(r *Registry, id string) {},
			transition: func(r *Registry, id string) (*AgentRegistration, error) {
				return r.Expire(id)
			},
			wantState: LifecycleStateExpired,
		},
		{
			name: "suspended→revoked",
			setup: func(r *Registry, id string) {
				r.Suspend(id) //nolint:errcheck
			},
			transition: func(r *Registry, id string) (*AgentRegistration, error) {
				return r.Revoke(id)
			},
			wantState: LifecycleStateRevoked,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRegistry()
			reg := validReg()
			if _, err := r.Register(reg); err != nil {
				t.Fatalf("Register: %v", err)
			}
			tc.setup(r, reg.RegistrationID)

			result, err := tc.transition(r, reg.RegistrationID)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.LifecycleState != tc.wantState {
				t.Errorf("got state %q, want %q", result.LifecycleState, tc.wantState)
			}
		})
	}
}

func TestRegistry_LifecycleTransitions_IllegalMatrix(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(*Registry, string)
		attempt func(*Registry, string) (*AgentRegistration, error)
	}{
		{
			name: "revoked→active (terminal)",
			setup: func(r *Registry, id string) {
				r.Revoke(id) //nolint:errcheck
			},
			attempt: func(r *Registry, id string) (*AgentRegistration, error) {
				return r.Reactivate(id)
			},
		},
		{
			name: "revoked→suspended (terminal)",
			setup: func(r *Registry, id string) {
				r.Revoke(id) //nolint:errcheck
			},
			attempt: func(r *Registry, id string) (*AgentRegistration, error) {
				return r.Suspend(id)
			},
		},
		{
			name: "replaced→active (terminal)",
			setup: func(r *Registry, id string) {
				r.Replace(id) //nolint:errcheck
			},
			attempt: func(r *Registry, id string) (*AgentRegistration, error) {
				return r.Reactivate(id)
			},
		},
		{
			name: "expired→active (terminal)",
			setup: func(r *Registry, id string) {
				r.Expire(id) //nolint:errcheck
			},
			attempt: func(r *Registry, id string) (*AgentRegistration, error) {
				return r.Reactivate(id)
			},
		},
		{
			name: "suspended→replaced (illegal)",
			setup: func(r *Registry, id string) {
				r.Suspend(id) //nolint:errcheck
			},
			attempt: func(r *Registry, id string) (*AgentRegistration, error) {
				return r.Replace(id)
			},
		},
		{
			name:  "not found",
			setup: func(r *Registry, id string) {},
			attempt: func(r *Registry, id string) (*AgentRegistration, error) {
				return r.Revoke("nonexistent")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRegistry()
			reg := validReg()
			if _, err := r.Register(reg); err != nil {
				t.Fatalf("Register: %v", err)
			}
			tc.setup(r, reg.RegistrationID)

			_, err := tc.attempt(r, reg.RegistrationID)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.HasPrefix(err.Error(), "agentregistry:") {
				t.Errorf("error missing agentregistry: prefix: %v", err)
			}
		})
	}
}

// ── Registry: Lookup ──────────────────────────────────────────────────────────

func TestRegistry_Lookup(t *testing.T) {
	r := NewRegistry()
	reg := validReg()
	if _, err := r.Register(reg); err != nil {
		t.Fatalf("Register: %v", err)
	}
	got, err := r.Lookup(reg.RegistrationID)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got.RegistrationID != reg.RegistrationID {
		t.Errorf("got %q, want %q", got.RegistrationID, reg.RegistrationID)
	}
}

func TestRegistry_Lookup_NotFound(t *testing.T) {
	r := NewRegistry()
	_, err := r.Lookup("registration:missing")
	if err == nil {
		t.Fatal("expected error for missing registration")
	}
	if !strings.HasPrefix(err.Error(), "agentregistry:") {
		t.Errorf("error missing agentregistry: prefix: %v", err)
	}
}

// ── Registry: Snapshot binding ────────────────────────────────────────────────

func TestRegistry_ActiveSnapshot(t *testing.T) {
	r := NewRegistry()
	reg := validReg()
	if _, err := r.Register(reg); err != nil {
		t.Fatalf("Register: %v", err)
	}
	snap := validSnap(reg.RegistrationID, validDigest)
	if _, err := r.AddSnapshot(snap); err != nil {
		t.Fatalf("AddSnapshot: %v", err)
	}
	got, err := r.ActiveSnapshot(reg.RegistrationID)
	if err != nil {
		t.Fatalf("ActiveSnapshot: %v", err)
	}
	if got.SnapshotDigest != snap.SnapshotDigest {
		t.Errorf("got %q, want %q", got.SnapshotDigest, snap.SnapshotDigest)
	}
}

func TestRegistry_ActiveSnapshot_BindingMismatch(t *testing.T) {
	r := NewRegistry()
	reg := validReg()
	if _, err := r.Register(reg); err != nil {
		t.Fatalf("Register: %v", err)
	}
	// Snapshot references a different RegistrationID.
	snap := validSnap("registration:OTHER", validDigest)
	_, err := r.AddSnapshot(snap)
	if err == nil {
		t.Fatal("expected error for unknown RegistrationID in snapshot")
	}
	if !strings.HasPrefix(err.Error(), "agentregistry:") {
		t.Errorf("error missing agentregistry: prefix: %v", err)
	}
}

func TestRegistry_ActiveSnapshot_NoSnapshot(t *testing.T) {
	r := NewRegistry()
	reg := validReg()
	if _, err := r.Register(reg); err != nil {
		t.Fatalf("Register: %v", err)
	}
	_, err := r.ActiveSnapshot(reg.RegistrationID)
	if err == nil {
		t.Fatal("expected error when no snapshot registered")
	}
	if !strings.HasPrefix(err.Error(), "agentregistry:") {
		t.Errorf("error missing agentregistry: prefix: %v", err)
	}
}

// ── Matcher ───────────────────────────────────────────────────────────────────

func TestMatch_HappyPath(t *testing.T) {
	reg := validReg()
	snap := validSnap(reg.RegistrationID, validDigest)
	snap.ConformanceEvidenceDigests = []string{validDigest2}

	req := Requirement{
		ProtocolVersion:         "v1",
		RequiredCapabilities:    []Capability{CapabilityExecutionProfileWorkspaceWrite},
		MinConformanceEvidences: 1,
	}
	result, err := Match(req, reg, snap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Matched {
		t.Errorf("expected match, got reject reason: %q", result.Reason)
	}
}

func TestMatch_RejectReasons(t *testing.T) {
	makeReg := func(state LifecycleState) AgentRegistration {
		r := validReg()
		r.LifecycleState = state
		return r
	}
	makeSnap := func(state SnapshotState, regID string) AgentCapabilitySnapshot {
		s := validSnap(regID, validDigest)
		s.SnapshotState = state
		return s
	}

	cases := []struct {
		name       string
		reg        AgentRegistration
		snap       AgentCapabilitySnapshot
		req        Requirement
		wantReason RejectReason
	}{
		{
			name:       "inactive-registration (suspended)",
			reg:        makeReg(LifecycleStateSuspended),
			snap:       makeSnap(SnapshotStateActive, "registration:0001"),
			req:        Requirement{ProtocolVersion: "v1"},
			wantReason: RejectReasonInactiveRegistration,
		},
		{
			name:       "inactive-registration (revoked)",
			reg:        makeReg(LifecycleStateRevoked),
			snap:       makeSnap(SnapshotStateActive, "registration:0001"),
			req:        Requirement{ProtocolVersion: "v1"},
			wantReason: RejectReasonInactiveRegistration,
		},
		{
			name:       "inactive-snapshot (revoked)",
			reg:        makeReg(LifecycleStateActive),
			snap:       makeSnap(SnapshotStateRevoked, "registration:0001"),
			req:        Requirement{ProtocolVersion: "v1"},
			wantReason: RejectReasonInactiveSnapshot,
		},
		{
			name:       "binding-mismatch",
			reg:        makeReg(LifecycleStateActive),
			snap:       makeSnap(SnapshotStateActive, "registration:OTHER"),
			req:        Requirement{ProtocolVersion: "v1"},
			wantReason: RejectReasonBindingMismatch,
		},
		{
			name:       "protocol-mismatch",
			reg:        makeReg(LifecycleStateActive),
			snap:       makeSnap(SnapshotStateActive, "registration:0001"),
			req:        Requirement{ProtocolVersion: "v2"},
			wantReason: RejectReasonProtocolMismatch,
		},
		{
			name: "capability-missing",
			reg:  makeReg(LifecycleStateActive),
			snap: makeSnap(SnapshotStateActive, "registration:0001"),
			req: Requirement{
				ProtocolVersion:      "v1",
				RequiredCapabilities: []Capability{CapabilityNetworkPolicyEnforced},
			},
			wantReason: RejectReasonCapabilityMissing,
		},
		{
			name: "evidence-insufficient",
			reg:  makeReg(LifecycleStateActive),
			snap: makeSnap(SnapshotStateActive, "registration:0001"),
			req: Requirement{
				ProtocolVersion:         "v1",
				MinConformanceEvidences: 1,
			},
			wantReason: RejectReasonEvidenceInsufficient,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := Match(tc.req, tc.reg, tc.snap)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.Matched {
				t.Fatal("expected no match")
			}
			if result.Reason != tc.wantReason {
				t.Errorf("got reason %q, want %q", result.Reason, tc.wantReason)
			}
		})
	}
}

func TestMatch_InvalidInputsFailClosed(t *testing.T) {
	t.Run("invalid registration", func(t *testing.T) {
		reg := validReg()
		reg.RegistrationID = "" // triggers Validate failure
		snap := validSnap("registration:0001", validDigest)
		_, err := Match(Requirement{ProtocolVersion: "v1"}, reg, snap)
		if err == nil {
			t.Fatal("expected error for invalid registration")
		}
	})
	t.Run("invalid snapshot", func(t *testing.T) {
		reg := validReg()
		snap := validSnap(reg.RegistrationID, validDigest)
		snap.SnapshotDigest = "" // triggers Validate failure
		_, err := Match(Requirement{ProtocolVersion: "v1"}, reg, snap)
		if err == nil {
			t.Fatal("expected error for invalid snapshot")
		}
	})
}

// ── Digest stability: any field change alters the digest ─────────────────────

func TestRegistrationDigest_EachFieldAltersDigest(t *testing.T) {
	base := validReg()
	baseDigest, err := base.Digest()
	if err != nil {
		t.Fatal(err)
	}

	mutations := []struct {
		name   string
		mutate func(*AgentRegistration)
	}{
		{"RegistrationID", func(r *AgentRegistration) { r.RegistrationID = "registration:9999" }},
		{"AuthorityNamespaceID", func(r *AgentRegistration) { r.AuthorityNamespaceID = "ns-changed" }},
		{"SecurityDomainID", func(r *AgentRegistration) { r.SecurityDomainID = "sd-changed" }},
		{"Principal", func(r *AgentRegistration) { r.Principal = "principal-changed" }},
		{"ProviderName", func(r *AgentRegistration) { r.ProviderName = "other-agent" }},
		{"ProviderVersion", func(r *AgentRegistration) { r.ProviderVersion = "2.0.0" }},
		{"ProtocolVersion", func(r *AgentRegistration) { r.ProtocolVersion = "v2" }},
		{"Scope", func(r *AgentRegistration) { r.Scope = "other-scope" }},
		{"IdempotencyKey", func(r *AgentRegistration) { r.IdempotencyKey = "key-changed" }},
		{"RequestDigest", func(r *AgentRegistration) { r.RequestDigest = validDigest2 }},
		{"LifecycleState", func(r *AgentRegistration) { r.LifecycleState = LifecycleStateSuspended }},
		{"CreatedAt", func(r *AgentRegistration) { r.CreatedAt = baseTime.Add(time.Second) }},
		{"UpdatedAt", func(r *AgentRegistration) { r.UpdatedAt = baseTime.Add(time.Second) }},
	}
	for _, m := range mutations {
		t.Run(m.name, func(t *testing.T) {
			r := base
			m.mutate(&r)
			d, err := r.Digest()
			if err != nil {
				t.Fatal(err)
			}
			if d == baseDigest {
				t.Errorf("digest unchanged after mutating %s", m.name)
			}
		})
	}
}

func TestSnapshotDigest_EachFieldAltersDigest(t *testing.T) {
	base := validSnap("registration:0001", validDigest)
	base.ConformanceEvidenceDigests = []string{validDigest2}
	baseDigest, err := base.Digest()
	if err != nil {
		t.Fatal(err)
	}

	mutations := []struct {
		name   string
		mutate func(*AgentCapabilitySnapshot)
	}{
		{"SnapshotDigest", func(s *AgentCapabilitySnapshot) { s.SnapshotDigest = validDigest3 }},
		{"RegistrationID", func(s *AgentCapabilitySnapshot) { s.RegistrationID = "registration:changed" }},
		{"ProtocolVersion", func(s *AgentCapabilitySnapshot) { s.ProtocolVersion = "v2" }},
		{"ProviderName", func(s *AgentCapabilitySnapshot) { s.ProviderName = "changed" }},
		{"ProviderVersion", func(s *AgentCapabilitySnapshot) { s.ProviderVersion = "2.0.0" }},
		{"Capabilities", func(s *AgentCapabilitySnapshot) {
			s.Capabilities = []Capability{CapabilitySessionPolicyEphemeral}
		}},
		{"ConformanceEvidenceDigests", func(s *AgentCapabilitySnapshot) {
			s.ConformanceEvidenceDigests = []string{validDigest3}
		}},
		{"SnapshotState", func(s *AgentCapabilitySnapshot) { s.SnapshotState = SnapshotStateRevoked }},
	}
	for _, m := range mutations {
		t.Run(m.name, func(t *testing.T) {
			s := base
			m.mutate(&s)
			d, err := s.Digest()
			if err != nil {
				t.Fatal(err)
			}
			if d == baseDigest {
				t.Errorf("digest unchanged after mutating %s", m.name)
			}
		})
	}
}

// ── Nil / zero-value inputs fail closed ──────────────────────────────────────

func TestNilZeroInputsFailClosed(t *testing.T) {
	t.Run("zero AgentRegistration Validate", func(t *testing.T) {
		var r AgentRegistration
		if err := r.Validate(); err == nil {
			t.Fatal("expected error for zero AgentRegistration")
		}
	})
	t.Run("zero AgentCapabilitySnapshot Validate", func(t *testing.T) {
		var s AgentCapabilitySnapshot
		if err := s.Validate(); err == nil {
			t.Fatal("expected error for zero AgentCapabilitySnapshot")
		}
	})
	t.Run("zero EvidenceRecord Validate", func(t *testing.T) {
		var e EvidenceRecord
		if err := e.Validate(); err == nil {
			t.Fatal("expected error for zero EvidenceRecord")
		}
	})
	t.Run("Register nil-like registration", func(t *testing.T) {
		r := NewRegistry()
		var reg AgentRegistration
		if _, err := r.Register(reg); err == nil {
			t.Fatal("expected error for zero AgentRegistration")
		}
	})
}
