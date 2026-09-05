package processsupervisor

import (
	"bytes"
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"
)

type countingMechanicsV2 struct {
	calls      int
	before     func()
	fail       error
	legacy     bool
	wrongState bool
}

func (m *countingMechanicsV2) result(command CommandName, state string) (MechanicsResult, error) {
	m.calls++
	if m.before != nil {
		m.before()
	}
	if m.fail != nil {
		return MechanicsResult{}, m.fail
	}
	report := ProcessReport{State: state, ObserverIdentity: observerIdentityV2, ObservedAt: "2026-09-05T00:00:00Z",
		Process: validBootstrap().Core.Process, RuntimeObjectDigest: digest("runtime"), WorkingObjectDigest: digest("cwd"),
		SourceGateRevision: SourceGateRevisionV1, ExactSetDigest: digest("set")}
	if m.legacy {
		report.ObserverIdentity = "darwin-fixed-process-supervisor-v1"
	}
	if m.wrongState {
		report.State = "terminal"
	}
	observed, _ := digestValue(report)
	result := MechanicsResult{Disposition: "ok", ReasonCode: "test-v2-observed", ObservationDigest: observed, Payload: mustCanonical(report)}
	if command == CommandCollect {
		result.TranscriptDigest = observed
	}
	return result, nil
}

func (m *countingMechanicsV2) Spawn(context.Context, SpawnPayload) (MechanicsResult, error) {
	return m.result(CommandSpawn, "exec-stopped")
}
func (m *countingMechanicsV2) Resume(context.Context, ResumePayload) (MechanicsResult, error) {
	return m.result(CommandResume, "running")
}
func (m *countingMechanicsV2) Inspect(context.Context, CleanupPayload) (MechanicsResult, error) {
	return m.result(CommandInspect, "terminal")
}
func (m *countingMechanicsV2) Terminate(context.Context, CleanupPayload) (MechanicsResult, error) {
	return m.result(CommandTerminate, "terminal")
}
func (m *countingMechanicsV2) Collect(context.Context, CollectPayload) (MechanicsResult, error) {
	return m.result(CommandCollect, "terminal")
}
func (m *countingMechanicsV2) Close(context.Context, ClosePayload) (MechanicsResult, error) {
	return m.result(CommandClose, "terminal")
}

func sessionRequestV2(t *testing.T, session *sessionV2, command CommandName, id string, payload any) requestV2 {
	t.Helper()
	request := requestV2{SchemaVersion: requestSchemaV2, ProtocolRevision: protocolRevisionV2,
		LaunchChildProtocolRevision: launchChildProtocolRevisionV2, MechanicsIdentity: mechanicsIdentityV2,
		SessionID: session.core.sessionID, Command: command, CommandID: id, Sequence: session.core.commandSequence + 1,
		PreviousCommandDigest: session.core.commandHead, CurrentAuthorityHead: session.core.authorityHead,
		Deadline: session.core.now().UTC().Add(20 * time.Second).Format(time.RFC3339Nano), Payload: mustCanonical(payload)}
	sealRequestV2(t, &request)
	return request
}

func sealRequestV2(t *testing.T, request *requestV2) {
	t.Helper()
	var err error
	request.RequestDigest, err = digestValue(requestDigestInputV2{
		SchemaVersion: request.SchemaVersion, ProtocolRevision: request.ProtocolRevision, LaunchChildProtocolRevision: request.LaunchChildProtocolRevision,
		MechanicsIdentity: request.MechanicsIdentity, SessionID: request.SessionID, Command: request.Command, CommandID: request.CommandID, Sequence: request.Sequence,
		PreviousCommandDigest: request.PreviousCommandDigest, CurrentAuthorityHead: request.CurrentAuthorityHead, Deadline: request.Deadline, Payload: request.Payload})
	if err != nil {
		t.Fatal(err)
	}
}

func newTestSessionV2(t *testing.T) (*sessionV2, *countingMechanicsV2, string) {
	t.Helper()
	file := newWriterV2File(t, nil)
	journal, err := openJournalWriterV2(file)
	if err != nil {
		t.Fatal(err)
	}
	mechanics := &countingMechanicsV2{}
	now := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	session, err := newSessionV2(validBootstrapV2(), journal, mechanics, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	mechanics.before = func() {
		data, err := os.ReadFile(file.Name())
		if err != nil {
			t.Fatal(err)
		}
		records, _, partial, err := parseJournalV2(data)
		if err != nil || partial || len(records) < 2 || records[len(records)-1].Kind != journalCommandIntent {
			t.Fatal("mechanics ran without a durable v2 intent")
		}
	}
	return session, mechanics, file.Name()
}

func bindTestSessionV2(t *testing.T, session *sessionV2) {
	t.Helper()
	payload := BindAuthorityPayload{SupervisorStartedFactDigest: digest("v2-supervisor-started"), OwnerEpoch: session.core.ownerEpoch,
		PreviousAuthorityHead: session.core.authorityHead, AuthorityHead: digest("bound-v2-head")}
	request := sessionRequestV2(t, session, CommandBindAuthority, "bind-v2", payload)
	response, err := session.handle(mustCanonical(request))
	if err != nil || response.Status != "ok" || session.core.state != sessionBound || validateV2ResponseBinding(response, request) != nil {
		t.Fatalf("bind: %+v %v", response, err)
	}
}

func TestSessionV2LifecycleDurabilityAndConcurrentExactReplay(t *testing.T) {
	session, mechanics, path := newTestSessionV2(t)
	bindTestSessionV2(t, session)
	spawn := validSpawnPayload()
	spawn.LaunchAuthorizedFactDigest, spawn.SupervisorStartedFactDigest = session.core.launchFact, session.core.supervisorStartedFact
	spawn.SourceGateRevision = SourceGateRevisionV1
	request := sessionRequestV2(t, session, CommandSpawn, "spawn-v2", spawn)
	response, err := session.handle(mustCanonical(request))
	if err != nil || response.Status != "ok" || mechanics.calls != 1 || validateV2ResponseBinding(response, request) != nil {
		t.Fatalf("spawn: %+v %v", response, err)
	}
	var workers sync.WaitGroup
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			replay, err := session.handle(mustCanonical(request))
			if err != nil || !bytes.Equal(mustCanonical(replay), mustCanonical(response)) {
				t.Error("exact replay changed receipt")
			}
		}()
	}
	workers.Wait()
	if mechanics.calls != 1 {
		t.Fatal("response loss spawned more than one child")
	}
	resume := sessionRequestV2(t, session, CommandResume, "resume-v2", ResumePayload{ProcessStartedFactDigest: digest("started")})
	if response, err := session.handle(mustCanonical(resume)); err != nil || response.Status != "ok" {
		t.Fatalf("resume: %v", err)
	}
	cleanup := CleanupPayload{ProcessStartedFactDigest: session.core.startedFact, LastObservationDigest: session.core.lastObservation,
		TerminalizationBarrierDigest: digest("barrier"), TerminalizationID: "terminal-v2", TerminalGeneration: 1, CleanupBindingDigest: digest("cleanup")}
	inspect := sessionRequestV2(t, session, CommandInspect, "inspect-v2", cleanup)
	if response, err := session.handle(mustCanonical(inspect)); err != nil || response.Status != "ok" {
		t.Fatalf("inspect: %v", err)
	}
	collect := sessionRequestV2(t, session, CommandCollect, "collect-v2", CollectPayload{
		ProcessStartedFactDigest: session.core.startedFact, LastObservationDigest: session.core.lastObservation})
	if response, err := session.handle(mustCanonical(collect)); err != nil || response.Status != "ok" {
		t.Fatalf("collect: %v", err)
	}
	closeRequest := sessionRequestV2(t, session, CommandClose, "close-v2", ClosePayload{
		ProcessTerminalFactDigest: digest("terminal"), AllocationTerminatedDigest: digest("allocation-terminal"), CleanupBindingDigest: digest("cleanup")})
	if response, err := session.handle(mustCanonical(closeRequest)); err != nil || response.Status != "ok" || session.core.state != sessionClosed {
		t.Fatalf("close: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	observation, err := DecodeSupervisorJournalReadOnly(journalFileNameV2, data)
	if err != nil || observation.Sequence != 13 || observation.PartialTail || mechanics.calls != 5 {
		t.Fatalf("final v2 journal: %+v %v", observation, err)
	}
	if _, err := newSessionV2(validBootstrapV2(), session.journal, mechanics, session.core.now); err == nil {
		t.Fatal("new Supervisor adopted predecessor wait rights")
	}
}

func TestSessionV2RejectsBeforeIntentAndPreservesUncertainEffects(t *testing.T) {
	session, mechanics, path := newTestSessionV2(t)
	bindTestSessionV2(t, session)
	spawn := validSpawnPayload()
	spawn.LaunchAuthorizedFactDigest, spawn.SupervisorStartedFactDigest = session.core.launchFact, session.core.supervisorStartedFact
	spawn.SourceGateRevision = SourceGateRevisionV1
	request := sessionRequestV2(t, session, CommandSpawn, "spawn-v2", spawn)
	before, _ := os.ReadFile(path)
	for _, mutate := range []func(*requestV2){
		func(r *requestV2) { r.ProtocolRevision = ProtocolRevision },
		func(r *requestV2) { r.SessionID = "other" },
		func(r *requestV2) { r.Sequence++ },
		func(r *requestV2) { r.PreviousCommandDigest = CommandGenesisDigest },
		func(r *requestV2) { r.Deadline = "2026-09-04T00:00:00Z" },
	} {
		bad := request
		mutate(&bad)
		sealRequestV2(t, &bad)
		if _, err := session.handle(mustCanonical(bad)); err == nil {
			t.Fatal("invalid command admitted")
		}
		after, _ := os.ReadFile(path)
		if !bytes.Equal(before, after) || mechanics.calls != 0 {
			t.Fatal("pre-admission rejection produced an effect")
		}
	}
	mechanics.legacy = true
	if _, err := session.handle(mustCanonical(request)); !errors.Is(err, ErrIntervention) {
		t.Fatalf("legacy observation promoted: %v", err)
	}
	sequence, _, pending := session.journal.checkpoint()
	if !pending || sequence != 4 || session.core.state != sessionIntervention {
		t.Fatal("uncertain mechanics must retain intent without receipt")
	}
	if _, err := session.handle(mustCanonical(request)); err == nil || mechanics.calls != 1 {
		t.Fatal("uncertain effect blindly retried")
	}
}

func TestSessionV2MechanicsFailureAndWrongState(t *testing.T) {
	for _, wrongState := range []bool{false, true} {
		session, mechanics, _ := newTestSessionV2(t)
		bindTestSessionV2(t, session)
		spawn := validSpawnPayload()
		spawn.LaunchAuthorizedFactDigest, spawn.SupervisorStartedFactDigest = session.core.launchFact, session.core.supervisorStartedFact
		request := sessionRequestV2(t, session, CommandSpawn, "spawn-error-v2", spawn)
		mechanics.wrongState = wrongState
		if !wrongState {
			mechanics.fail = ErrUnavailable
		}
		response, err := session.handle(mustCanonical(request))
		if wrongState {
			if !errors.Is(err, ErrIntervention) || session.core.state != sessionIntervention {
				t.Fatal("terminal observation authorized spawn success")
			}
			continue
		}
		if err != nil || response.Status != "rejected" || response.ReasonCode != ErrUnavailable.ReasonCode || validateV2ResponseBinding(response, request) != nil {
			t.Fatalf("typed error not durably bound: %+v %v", response, err)
		}
		replayed, err := session.handle(mustCanonical(request))
		if err != nil || !bytes.Equal(mustCanonical(response), mustCanonical(replayed)) || mechanics.calls != 1 {
			t.Fatal("rejected receipt retried mechanics")
		}
	}
}
