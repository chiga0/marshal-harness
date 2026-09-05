package resultingress

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/processsupervisor"
)

func testBindIntentV2(t *testing.T, anchor processsupervisor.SessionAnchorV2, startedFact string) (SupervisorCommandIntent, processsupervisor.PreparedCommandV2, processsupervisor.BindAuthorityPayload) {
	t.Helper()
	b := anchor.Binding
	payload := processsupervisor.BindAuthorityPayload{SupervisorStartedFactDigest: startedFact, OwnerEpoch: b.OwnerEpoch, PreviousAuthorityHead: b.CurrentAuthorityHead, AuthorityHead: startedFact}
	prepared, err := processsupervisor.PrepareCommandV2(anchor, processsupervisor.CommandOptions{Command: processsupervisor.CommandBindAuthority, CommandID: "durable-v2-bind", Sequence: b.CommandSequence + 1,
		PreviousCommandDigest: b.CommandHead, CurrentAuthorityHead: b.CurrentAuthorityHead, Deadline: time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)}, payload)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := NewSupervisorCommandIntentV2(prepared.Evidence())
	if err != nil {
		t.Fatal(err)
	}
	return intent, prepared, payload
}

func TestSupervisorIntentV2ExactRoundTripAndMixedGenerationRejection(t *testing.T) {
	owner, request := testBootstrapV2Input()
	bootstrap, err := NewSupervisorBootstrapPreparedV2(owner, request)
	if err != nil {
		t.Fatal(err)
	}
	started := testInitialStartedV2(t, bootstrap, attemptTestDigest("bootstrap"))
	intent, prepared, payload := testBindIntentV2(t, started.V2.Anchor, attemptTestDigest("started"))
	encoded, err := json.Marshal(intent)
	if err != nil {
		t.Fatal(err)
	}
	var replay SupervisorCommandIntent
	if json.Unmarshal(encoded, &replay) != nil || replay != intent {
		t.Fatal("durable intent changed on JSON round trip")
	}
	e, err := SupervisorPreparedCommandEvidenceV2(replay)
	if err != nil || e != prepared.Evidence() {
		t.Fatal("producer evidence changed on replay")
	}
	rebuilt, err := processsupervisor.RebuildPreparedCommandV2(e, payload)
	if err != nil || rebuilt.Evidence() != e {
		t.Fatal("exact payload did not rebuild")
	}
	payload.AuthorityHead = attemptTestDigest("drifted-rebuild-head")
	if _, err := processsupervisor.RebuildPreparedCommandV2(e, payload); err == nil {
		t.Fatal("rebuild accepted changed payload")
	}
	for name, change := range map[string]func(*SupervisorCommandIntent){
		"legacy-revision":       func(i *SupervisorCommandIntent) { i.ProtocolRevision = processsupervisor.ProtocolRevision },
		"no-producer-digest":    func(i *SupervisorCommandIntent) { i.PreparedEvidenceDigest = "" },
		"wrong-session":         func(i *SupervisorCommandIntent) { i.SessionID = "another-session" },
		"changed-payload":       func(i *SupervisorCommandIntent) { i.PayloadDigest = attemptTestDigest("another-payload") },
		"changed-rebuild":       func(i *SupervisorCommandIntent) { i.Rebuild.AuthorityHead = attemptTestDigest("another-head") },
		"changed-current-owner": func(i *SupervisorCommandIntent) { i.PreCommand.OwnerEpoch++ },
		"changed-directory":     func(i *SupervisorCommandIntent) { i.PreCommand.ControlDirectory.Inode++ },
	} {
		t.Run(name, func(t *testing.T) {
			bad := intent
			change(&bad)
			if bad.Validate() == nil {
				t.Fatal("changed producer projection accepted")
			}
		})
	}
	for index := 0; index < reflect.TypeOf(intent.PreCommand.Generation).NumField(); index++ {
		bad := intent
		reflect.ValueOf(&bad.PreCommand.Generation).Elem().Field(index).SetString("")
		if bad.Validate() == nil {
			t.Fatalf("generation field %d ignored", index)
		}
	}
	fact := supervisorCommandFact{FactType: supervisorCommandIntentFactType, Intent: intent, ProtocolRevision: supervisorIntentRecoveryRevision(intent)}
	if !validSupervisorRecoveryFactGeneration(fact) {
		t.Fatal("valid v2 recovery header rejected")
	}
	fact.ProtocolRevision = supervisorCommandProtocolRevision
	if validSupervisorRecoveryFactGeneration(fact) {
		t.Fatal("v2 body accepted under legacy recovery header")
	}
}
