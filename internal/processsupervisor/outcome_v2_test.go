package processsupervisor

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

func TestOutcomeV2ColdValidationBindsExactPreparationAndJournal(t *testing.T) {
	session, _, _ := newTestSessionV2(t)
	defer session.journal.close()
	anchor := testAnchorV2(session)
	request := sessionRequestV2(t, session, CommandBindAuthority, "outcome-bind", validBindPayloadForAnchorV2(anchor))
	prepared, err := PrepareCommandV2(anchor, CommandOptions{Command: request.Command, CommandID: request.CommandID, Sequence: request.Sequence,
		PreviousCommandDigest: request.PreviousCommandDigest, CurrentAuthorityHead: request.CurrentAuthorityHead, Deadline: session.core.now().Add(20 * time.Second)}, validBindPayloadForAnchorV2(anchor))
	if err != nil {
		t.Fatal(err)
	}
	response, err := session.handle(mustCanonical(prepared.request))
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := verifiedCommandOutcomeV2(prepared, response)
	if err != nil {
		t.Fatal(err)
	}
	var replay VerifiedCommandOutcomeV2
	if json.Unmarshal(mustCanonical(outcome), &replay) != nil || replay.Validate() != nil {
		t.Fatal("cold outcome rejected")
	}
	for name, mutate := range map[string]func(*VerifiedCommandOutcomeV2){
		"journal-head":     func(o *VerifiedCommandOutcomeV2) { o.PostCommand.Binding.JournalHead = digest("arbitrary-head") },
		"journal-sequence": func(o *VerifiedCommandOutcomeV2) { o.PostCommand.Binding.JournalSequence++ },
		"receipt":          func(o *VerifiedCommandOutcomeV2) { o.ReceiptDigest = digest("forged-receipt") },
		"observation":      func(o *VerifiedCommandOutcomeV2) { o.ObservationDigest = digest("forged-observation") },
		"generation": func(o *VerifiedCommandOutcomeV2) {
			o.PostCommand.Generation.JournalSchema = "marshal.process-supervisor-journal.v1"
		},
		"owner":           func(o *VerifiedCommandOutcomeV2) { o.PostCommand.Binding.OwnerEpoch++ },
		"journal-payload": func(o *VerifiedCommandOutcomeV2) { o.JournalRequest += " " },
		"oversize":        func(o *VerifiedCommandOutcomeV2) { o.JournalRequest = strings.Repeat("x", MaxJournalPayload+1) },
		"extra-report":    func(o *VerifiedCommandOutcomeV2) { o.ProcessReport = &ProcessReport{} },
		"prepared-projection": func(o *VerifiedCommandOutcomeV2) {
			o.Preparation.Projection.AuthorityHead = digest("wrong-bound-head")
			o.Preparation.EvidenceDigest, _ = o.Preparation.integrityDigest()
		},
		"intent-only-is-not-A0": func(o *VerifiedCommandOutcomeV2) {
			o.Preparation.PreCommand.Binding.JournalSequence++
			o.Preparation.EvidenceDigest, _ = o.Preparation.integrityDigest()
		},
	} {
		t.Run(name, func(t *testing.T) {
			bad := outcome
			mutate(&bad)
			if bad.Validate() == nil {
				t.Fatal("forged cold outcome accepted")
			}
		})
	}
	bad := outcome
	var hostile map[string]any
	if json.Unmarshal([]byte(outcome.JournalRequest), &hostile) != nil {
		t.Fatal("invalid positive request")
	}
	hostile["argv"] = []string{"forbidden-raw-field"}
	bad.JournalRequest = string(mustCanonical(hostile))
	bad.Preparation.JournalRequestDigest = canonical.DigestBytes([]byte(bad.JournalRequest))
	bad.Preparation.EvidenceDigest, _ = bad.Preparation.integrityDigest()
	if bad.Preparation.Validate() != nil {
		t.Fatal("hostile fixture fails before the closed journal decoder")
	}
	if bad.Validate() == nil {
		t.Fatal("unknown/raw journal field accepted despite recomputed hashes")
	}
}
