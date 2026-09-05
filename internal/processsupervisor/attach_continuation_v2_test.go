package processsupervisor

import (
	"bytes"
	"os"
	"testing"
)

func testSessionAttachAuthorityV2(t *testing.T, s *sessionV2) AttachAuthorityV2 {
	t.Helper()
	a := testAttachRequestV2(t).Authority
	a.PreviousSupervisor = testAnchorV2(s)
	a.CurrentAcquisition.AuthorityNamespaceID = s.core.authority.AuthorityNamespaceID
	a.CurrentOwnerBoundFact.Authority = s.core.authority
	a.ChildObservationDigest = s.core.lastObservation
	if a.Validate() != nil {
		t.Fatal("invalid session Attach fixture")
	}
	return a
}

func TestAttachContinuationV2BindsOnlyFrozenSuccessor(t *testing.T) {
	s, m, path := newTestSessionV2(t)
	defer s.journal.close()
	bindTestSessionV2(t, s)
	spawn := spawnRequestForSessionV2(t, s)
	if _, err := s.handle(mustCanonical(spawn)); err != nil {
		t.Fatal(err)
	}
	a := testSessionAttachAuthorityV2(t, s)
	payload := BindAuthorityPayload{SupervisorStartedFactDigest: s.core.supervisorStartedFact, OwnerEpoch: s.core.ownerEpoch,
		PreviousAuthorityHead: s.core.authorityHead, AuthorityHead: a.CurrentOwnerBoundFact.AttemptHead}
	request := sessionRequestV2(t, s, CommandBindAuthority, "attach-bind-v2", payload)
	before, _ := os.ReadFile(path)
	for name, mutate := range map[string]func(*requestV2){
		"wrong-successor": func(r *requestV2) {
			p := payload
			p.AuthorityHead = digest("not-owner-bound")
			r.Payload = mustCanonical(p)
		},
		"new-mechanics-owner": func(r *requestV2) { p := payload; p.OwnerEpoch++; r.Payload = mustCanonical(p) },
		"wrong-started": func(r *requestV2) {
			p := payload
			p.SupervisorStartedFactDigest = digest("wrong-started")
			r.Payload = mustCanonical(p)
		},
		"old-head":     func(r *requestV2) { r.CurrentAuthorityHead = digest("wrong-head") },
		"deadline":     func(r *requestV2) { r.Deadline = "2026-09-04T00:00:00Z" },
		"sequence":     func(r *requestV2) { r.Sequence++ },
		"spawn-replay": func(r *requestV2) { *r = spawn },
		"resume": func(r *requestV2) {
			r.Command = CommandResume
			r.Payload = mustCanonical(ResumePayload{ProcessStartedFactDigest: digest("started")})
		},
		"mixed-generation": func(r *requestV2) { r.ProtocolRevision = ProtocolRevision },
	} {
		t.Run(name, func(t *testing.T) {
			bad := request
			mutate(&bad)
			sealRequestV2(t, &bad)
			if _, err := s.handleAttachContinuation(mustCanonical(bad), a); err == nil {
				t.Fatal("invalid Attach continuation admitted")
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) || m.calls != 1 {
				t.Fatal("rejection wrote journal or executed child")
			}
		})
	}
	// The ordinary command loop cannot acquire this already-bound capability.
	if _, err := s.handle(mustCanonical(request)); err == nil {
		t.Fatal("generic loop performed Attach rebind")
	}
	response, err := s.handleAttachContinuation(mustCanonical(request), a)
	if err != nil || response.Status != "ok" || validateV2ResponseBinding(response, request) != nil ||
		s.core.ownerEpoch != a.PreviousSupervisor.Binding.OwnerEpoch || s.core.authorityHead != payload.AuthorityHead || m.calls != 1 {
		t.Fatalf("exact rebind failed: %+v %v", response, err)
	}
	committed, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := DecodeSupervisorJournalReadOnly(journalFileNameV2, committed)
	if err != nil || journal.Sequence != a.PreviousSupervisor.Binding.JournalSequence+2 {
		t.Fatal("rebind did not persist one pair")
	}
	if _, err := s.handleAttachContinuation(mustCanonical(request), a); err == nil {
		t.Fatal("stale Attach retained authority")
	}
	// A lost reply may recover the exact committed receipt without repeating
	// the effect through the existing command recovery path.
	replayed, err := s.handle(mustCanonical(request))
	if err != nil || !bytes.Equal(mustCanonical(replayed), mustCanonical(response)) {
		t.Fatal("committed receipt replay changed")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(committed, after) || m.calls != 1 {
		t.Fatal("recovery repeated effect")
	}
}

func TestAttachContinuationV2TerminalCommandsKeepLifecycleAdmission(t *testing.T) {
	s, m, _ := newTestSessionV2(t)
	defer s.journal.close()
	bindTestSessionV2(t, s)
	if _, err := s.handle(mustCanonical(spawnRequestForSessionV2(t, s))); err != nil {
		t.Fatal(err)
	}
	resume := sessionRequestV2(t, s, CommandResume, "terminal-resume-v2", ResumePayload{ProcessStartedFactDigest: digest("started")})
	if _, err := s.handle(mustCanonical(resume)); err != nil {
		t.Fatal(err)
	}
	cleanup := CleanupPayload{ProcessStartedFactDigest: s.core.startedFact, LastObservationDigest: s.core.lastObservation,
		TerminalizationBarrierDigest: digest("barrier"), TerminalizationID: "terminal-v2", TerminalGeneration: 1, CleanupBindingDigest: digest("cleanup")}
	steps := []struct {
		command CommandName
		payload func() any
	}{
		{CommandInspect, func() any { return cleanup }},
		{CommandCollect, func() any {
			return CollectPayload{ProcessStartedFactDigest: s.core.startedFact, LastObservationDigest: s.core.lastObservation}
		}},
		{CommandClose, func() any {
			return ClosePayload{ProcessTerminalFactDigest: digest("terminal"), AllocationTerminatedDigest: digest("allocation-terminal"), CleanupBindingDigest: digest("cleanup")}
		}},
	}
	for _, step := range steps {
		a := testSessionAttachAuthorityV2(t, s)
		request := sessionRequestV2(t, s, step.command, "attach-"+string(step.command), step.payload())
		response, err := s.handleAttachContinuation(mustCanonical(request), a)
		if err != nil || response.Status != "ok" || validateV2ResponseBinding(response, request) != nil {
			t.Fatalf("%s continuation: %+v %v", step.command, response, err)
		}
		if _, err := s.handleAttachContinuation(mustCanonical(request), a); err == nil {
			t.Fatal("consumed Attach checkpoint reused")
		}
	}
	if s.core.state != sessionClosed || m.calls != 5 {
		t.Fatal("terminal continuation did not close exact child")
	}
}
