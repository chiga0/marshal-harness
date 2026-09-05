package processsupervisor

import (
	"bytes"
	"os"
	"testing"
	"time"
)

func TestPreparedJournalV2ClassifiesWithoutReplaying(t *testing.T) {
	for _, mode := range []string{"unchanged", "intent", "receipt"} {
		t.Run(mode, func(t *testing.T) {
			session, mechanics, path := newTestSessionV2(t)
			defer session.journal.close()
			session.core.now = time.Now
			bindTestSessionV2(t, session)
			anchor := testAnchorV2(session)
			payload := validSpawnPayload()
			payload.LaunchAuthorizedFactDigest, payload.SupervisorStartedFactDigest = session.core.launchFact, session.core.supervisorStartedFact
			prepared, err := PrepareCommandV2(anchor, clientOptionsV2(anchor, CommandSpawn, "readonly-recovery"), payload)
			if err != nil {
				t.Fatal(err)
			}
			want := ReconciliationUnchanged
			if mode == "intent" {
				projection, _, err := projectRequestV2(prepared.request)
				if err != nil {
					t.Fatal(err)
				}
				record := session.journalBase()
				record.Kind, record.Request = journalCommandIntent, &projection
				if _, err := session.journal.append(record); err != nil {
					t.Fatal(err)
				}
				want = ReconciliationIntentPending
			}
			if mode == "receipt" {
				if _, err := session.handle(mustCanonical(prepared.request)); err != nil {
					t.Fatal(err)
				}
				want = ReconciliationReceiptCommitted
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			calls := mechanics.calls
			state := session.journal.recoverySnapshot(prepared.evidence.CommandID)
			for n := 0; n < 2; n++ {
				observed, err := classifyPreparedJournalV2(state, prepared)
				if err != nil || observed.Reconciliation != want {
					t.Fatalf("classification: %+v %v", observed, err)
				}
				if mode == "receipt" {
					if observed.Outcome == nil || observed.Outcome.Validate() != nil || observed.Outcome.Preparation != prepared.Evidence() {
						t.Fatal("receipt lost exact original preparation")
					}
				} else if observed.Outcome != nil {
					t.Fatal("uncommitted command has outcome")
				}
			}
			for _, mutate := range []func(*journalStateV2){
				func(s *journalStateV2) { s.head = digest("wrong-head") },
				func(s *journalStateV2) { s.ownerEpoch++ },
				func(s *journalStateV2) { s.authorityHead = digest("wrong-owner-head") },
				func(s *journalStateV2) { s.created.SessionNonceDigest = digest("wrong-nonce") },
			} {
				wrong := state
				mutate(&wrong)
				if _, err := classifyPreparedJournalV2(wrong, prepared); err == nil {
					t.Fatal("forged checkpoint accepted")
				}
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) || mechanics.calls != calls {
				t.Fatal("classification changed evidence or replayed mechanics")
			}
		})
	}
}
