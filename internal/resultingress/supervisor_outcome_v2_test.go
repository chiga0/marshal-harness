package resultingress

import (
	"encoding/json"
	"testing"

	"github.com/chiga0/marshal-harness/internal/processsupervisor"
)

// Independent ADR 0079 bind fixture: no private response constructor is used
// by ResultIngress, and every record/head is checked by the protocol owner.
func testBindOutcomeV2(t *testing.T, intent SupervisorCommandIntent) SupervisorCommandEvidence {
	t.Helper()
	return testCommandOutcomeV2(t, intent, nil, nil)
}

func testCommandOutcomeV2(t *testing.T, intent SupervisorCommandIntent, report *processsupervisor.ProcessReport, environmentKeys []string) SupervisorCommandEvidence {
	t.Helper()
	p, err := SupervisorPreparedCommandEvidenceV2(intent)
	if err != nil {
		t.Fatal(err)
	}
	g, b := p.PreCommand.Generation, p.PreCommand.Binding
	digest := func(value any) string {
		t.Helper()
		d, err := canonicalDigest(value)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	raw := func(value any) []byte {
		t.Helper()
		b, err := processsupervisor.CanonicalProtocolMessage(value)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	request := map[string]any{"command": p.Command, "commandId": p.CommandID, "sequence": p.Sequence, "requestDigest": p.RequestDigest,
		"previousCommandDigest": p.PreviousCommandDigest, "currentAuthorityHead": p.CurrentAuthorityHead, "deadline": p.Deadline}
	source, reason, resultPayload := p.Projection.SupervisorStartedFactDigest, "authority-bound", json.RawMessage("{}")
	switch p.Command {
	case processsupervisor.CommandBindAuthority:
		request["nextAuthorityHead"], request["supervisorStartedFactDigest"] = p.Projection.AuthorityHead, p.Projection.SupervisorStartedFactDigest
	case processsupervisor.CommandSpawn:
		v := p.Projection
		request["launchMaterialsDigest"], request["agentLaunchSpecDigest"] = v.LaunchMaterialsDigest, v.AgentLaunchSpecDigest
		request["sourceGateRevision"], request["closureProfileId"] = v.SourceGateRevision, v.ClosureProfileID
		request["argvDigest"], request["environmentDigest"], request["stdinDigest"] = v.ArgvDigest, v.EnvironmentDigest, v.StdinDigest
		if len(environmentKeys) != 0 {
			request["environmentKeys"] = environmentKeys
		}
		reason = "process-exec-stopped"
	case processsupervisor.CommandResume:
		request["processStartedFactDigest"] = p.Projection.ProcessStartedFactDigest
		reason = "process-resumed"
	case processsupervisor.CommandCollect:
		request["processStartedFactDigest"], request["lastObservationDigest"] = p.Projection.ProcessStartedFactDigest, p.Projection.LastObservationDigest
		reason = "transcript-collected"
	case processsupervisor.CommandInspect:
		v := p.Projection
		request["processStartedFactDigest"], request["lastObservationDigest"] = v.ProcessStartedFactDigest, v.LastObservationDigest
		request["terminalizationBarrierDigest"], request["terminalizationId"], request["terminalGeneration"], request["cleanupBindingDigest"] = v.TerminalizationBarrierDigest, v.TerminalizationID, v.TerminalGeneration, v.CleanupBindingDigest
		reason = "process-inspected"
	case processsupervisor.CommandClose:
		v := p.Projection
		request["processTerminalFactDigest"], request["allocationTerminatedFactDigest"], request["cleanupBindingDigest"] = v.ProcessTerminalFactDigest, v.AllocationTerminatedFactDigest, v.CleanupBindingDigest
		reason = "mechanics-closed"
	default:
		t.Fatalf("unsupported fixture command %s", p.Command)
	}
	if report != nil {
		source, resultPayload = digest(*report), raw(*report)
	}
	observation := digest(map[string]any{"schemaVersion": g.ResponseSchema, "protocolRevision": g.ProtocolRevision, "launchChildProtocolRevision": g.LaunchChildProtocolRevision,
		"mechanicsIdentity": g.MechanicsIdentity, "observerIdentity": g.ObserverIdentity, "command": p.Command, "sourceDigest": source})
	result := processsupervisor.MechanicsResult{Disposition: "ok", ReasonCode: reason, ObservationDigest: observation, Payload: resultPayload}
	if p.Command == processsupervisor.CommandCollect && report != nil {
		result.TranscriptDigest, result.StdoutBytes, result.StderrBytes, result.Truncated = digest(*report), report.StdoutBytes, report.StderrBytes, report.TranscriptTruncated
	}
	receipt := digest(map[string]any{"schemaVersion": g.ResponseSchema, "protocolRevision": g.ProtocolRevision, "launchChildProtocolRevision": g.LaunchChildProtocolRevision, "mechanicsIdentity": g.MechanicsIdentity, "result": result})
	commandHead := digest(map[string]any{"previousCommandDigest": p.PreviousCommandDigest, "requestDigest": p.RequestDigest, "receiptDigest": receipt})
	response := map[string]any{"schemaVersion": g.ResponseSchema, "protocolRevision": g.ProtocolRevision, "launchChildProtocolRevision": g.LaunchChildProtocolRevision,
		"mechanicsIdentity": g.MechanicsIdentity, "sessionId": b.SessionID, "command": p.Command, "commandId": p.CommandID, "sequence": p.Sequence,
		"requestDigest": p.RequestDigest, "status": "ok", "reasonCode": result.ReasonCode, "receiptDigest": receipt, "observationDigest": observation, "commandHead": commandHead, "payload": result}
	record := map[string]any{"schemaVersion": g.JournalSchema, "protocolRevision": g.ProtocolRevision, "launchChildProtocolRevision": g.LaunchChildProtocolRevision,
		"mechanicsIdentity": g.MechanicsIdentity, "journalSequence": b.JournalSequence + 1, "kind": "command-intent", "sessionId": b.SessionID,
		"sessionNonceDigest": b.SessionNonceDigest, "authority": b.Authority, "ownerEpoch": b.OwnerEpoch, "currentAuthorityHead": b.CurrentAuthorityHead,
		"request": request, "previousRecordDigest": b.JournalHead}
	intentHead := digest(record)
	record["journalSequence"], record["kind"], record["previousRecordDigest"], record["response"] = b.JournalSequence+2, "command-receipt", intentHead, response
	post := p.PreCommand
	post.Binding.CommandSequence, post.Binding.CommandHead = p.Sequence, commandHead
	post.Binding.JournalSequence, post.Binding.JournalHead = b.JournalSequence+2, digest(record)
	post.Binding.CurrentAuthorityHead = p.CurrentAuthorityHead
	if p.Command == processsupervisor.CommandBindAuthority {
		post.Binding.CurrentAuthorityHead = p.Projection.AuthorityHead
	}
	evidence, err := NewSupervisorCommandEvidenceV2(processsupervisor.VerifiedCommandOutcomeV2{Preparation: p, JournalRequest: string(raw(request)), PostCommand: post,
		Status: "ok", ReasonCode: result.ReasonCode, ReceiptDigest: receipt, ObservationDigest: observation, CommandHead: commandHead, ProcessReport: report,
		TranscriptDigest: result.TranscriptDigest, StdoutBytes: result.StdoutBytes, StderrBytes: result.StderrBytes, Truncated: result.Truncated})
	if err != nil {
		t.Fatal(err)
	}
	return evidence
}

func TestSupervisorOutcomeV2ExactProjectionAndTamperRejection(t *testing.T) {
	owner, request := testBootstrapV2Input()
	bootstrap, err := NewSupervisorBootstrapPreparedV2(owner, request)
	if err != nil {
		t.Fatal(err)
	}
	started := testInitialStartedV2(t, bootstrap, attemptTestDigest("bootstrap"))
	intent, _, _ := testBindIntentV2(t, started.V2.Anchor, attemptTestDigest("started"))
	e := testBindOutcomeV2(t, intent)
	if !supervisorOutcomeMatchesStartedV2(started, e) {
		t.Fatal("original session outcome lost its started binding")
	}
	for name, mutate := range map[string]func(*processsupervisor.SessionAnchorV2){
		"collect-directory-growth": func(a *processsupervisor.SessionAnchorV2) { a.ControlDirectory.LinkCount += 3 },
		"session-substitution":     func(a *processsupervisor.SessionAnchorV2) { a.Binding.SessionID = "other-session" },
		"directory-substitution":   func(a *processsupervisor.SessionAnchorV2) { a.ControlDirectory.Inode++ },
	} {
		anchor := started.V2.Anchor
		mutate(&anchor)
		otherIntent, _, _ := testBindIntentV2(t, anchor, attemptTestDigest("started"))
		other := testBindOutcomeV2(t, otherIntent)
		if supervisorOutcomeMatchesStartedV2(started, other) != (name == "collect-directory-growth") {
			t.Fatalf("%s: stable session identity not enforced", name)
		}
	}
	var replay SupervisorCommandEvidence
	raw, err := json.Marshal(e)
	if err != nil || json.Unmarshal(raw, &replay) != nil || replay != e || replay.Validate() != nil {
		t.Fatal("outcome cold projection changed")
	}
	for name, mutate := range map[string]func(*SupervisorCommandEvidence){
		"legacy-header":       func(e *SupervisorCommandEvidence) { e.ProtocolRevision = processsupervisor.ProtocolRevision },
		"missing-preparation": func(e *SupervisorCommandEvidence) { e.V2Preparation = processsupervisor.PreparedCommandEvidenceV2{} },
		"wrong-journal":       func(e *SupervisorCommandEvidence) { e.PostCommand.JournalHead = attemptTestDigest("wrong-journal") },
		"wrong-head":          func(e *SupervisorCommandEvidence) { e.BoundAuthorityHead = attemptTestDigest("wrong-head") },
		"wrong-id":            func(e *SupervisorCommandEvidence) { e.CommandID = "wrong-command" },
		"business-forgery":    func(e *SupervisorCommandEvidence) { e.Outcome.State = SupervisorProcessRunning },
	} {
		t.Run(name, func(t *testing.T) {
			bad := e
			mutate(&bad)
			if bad.Validate() == nil {
				t.Fatal("changed outcome accepted")
			}
		})
	}
	state := AttemptAuthorityState{SupervisorPendingIntent: intent}
	if validateSupervisorCommandOutcomeAgainstIntent(state, e) != nil {
		t.Fatal("valid v2 bind failed intent binding")
	}
	state.SupervisorPendingIntent.JournalRequestDigest = attemptTestDigest("other-prepared-journal")
	if validateSupervisorCommandOutcomeAgainstIntent(state, e) == nil {
		t.Fatal("outcome substituted an unrelated durable preparation")
	}
}
