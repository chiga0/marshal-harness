package resultingress

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"testing"
)

func TestTeamHaltProgressStagesSurviveColdReplay(t *testing.T) {
	for _, stage := range []string{"collect", "verify"} {
		t.Run(stage, func(t *testing.T) {
			dir := t.TempDir()
			store, err := OpenResultIngressStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			owner, _ := supervisorTestAcquireOwner(t, store, attemptTestIdentity())
			plan, approval, _ := teamCreationFixture(t, store, owner.Acquisition)
			halt := TeamPlanHalt{GoalID: "team-1", NodeID: "service", PlanFactDigest: plan.FactDigest, Stage: stage}
			first, err := store.HaltTeamPlan(context.Background(), teamTestApproval{owner.Acquisition, approval, false}, owner.Acquisition, approval, halt)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err = OpenResultIngressStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			got, found, err := store.ReadTeamPlanHalt(owner.Acquisition.Scope, "team-1")
			if err != nil || !found || got != first {
				t.Fatalf("cold stage lost: %v", err)
			}
		})
	}
}

func TestTeamHaltReplayRejectsRehashedForgeryAndDuplicate(t *testing.T) {
	for _, mode := range []string{"stage", "node", "plan", "owner", "duplicate"} {
		t.Run(mode, func(t *testing.T) {
			store, err := OpenResultIngressStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			owner, _ := supervisorTestAcquireOwner(t, store, attemptTestIdentity())
			plan, approval, _ := teamCreationFixture(t, store, owner.Acquisition)
			halt := TeamPlanHalt{GoalID: "team-1", NodeID: "service", PlanFactDigest: plan.FactDigest, Stage: "start"}
			if _, err := store.HaltTeamPlan(context.Background(), teamTestApproval{owner.Acquisition, approval, false}, owner.Acquisition, approval, halt); err != nil {
				t.Fatal(err)
			}
			lines := bytes.Split(bytes.TrimSpace(reservationLedgerBytes(t, store)), []byte{'\n'})
			var fact teamHaltFact
			if err := json.Unmarshal(lines[len(lines)-1], &fact); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "stage":
				fact.Halt.Stage = "worker-claimed-success"
			case "node":
				fact.Halt.NodeID = "foreign-node"
			case "plan":
				fact.Halt.PlanFactDigest = attemptTestDigest("foreign-plan")
			case "owner":
				fact.OwnerFactDigest = attemptTestDigest("foreign-owner")
			case "duplicate":
				fact.Sequence++
			}
			fact.Digest = ""
			fact.Digest = canonicalDigestOrEmpty(fact)
			if mode == "duplicate" {
				lines = append(lines, teamTestBytes(t, fact))
			} else {
				lines[len(lines)-1] = teamTestBytes(t, fact)
			}
			if err := os.WriteFile(store.ledgerPath(), append(bytes.Join(lines, []byte{'\n'}), '\n'), 0600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := store.ReadTeamPlanHalt(owner.Acquisition.Scope, "team-1"); err == nil {
				t.Fatal("rehashed invalid halt was replayed")
			}
		})
	}
}

func TestTeamHaltColdReplayPreventsNewCreationWithoutBudgetRefund(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenResultIngressStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	owner, _ := supervisorTestAcquireOwner(t, store, attemptTestIdentity())
	plan, approval, prepared := teamCreationFixture(t, store, owner.Acquisition)
	halt := TeamPlanHalt{GoalID: "team-1", NodeID: "service", PlanFactDigest: plan.FactDigest, Stage: "prepare"}
	verifier := teamTestApproval{owner.Acquisition, approval, false}
	before := reservationLedgerBytes(t, store)
	first, err := store.HaltTeamPlan(context.Background(), verifier, owner.Acquisition, approval, halt)
	if err != nil || first.FactDigest == "" || bytes.Count(reservationLedgerBytes(t, store), []byte{'\n'}) != bytes.Count(before, []byte{'\n'})+1 {
		t.Fatalf("halt commit: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenResultIngressStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	owner2, _ := supervisorTestAcquireOwner(t, store, attemptTestIdentity())
	verifier = teamTestApproval{owner2.Acquisition, approval, false}
	before = reservationLedgerBytes(t, store)
	replay, err := store.HaltTeamPlan(context.Background(), verifier, owner2.Acquisition, approval, halt)
	if err != nil || replay != first || !bytes.Equal(before, reservationLedgerBytes(t, store)) {
		t.Fatalf("halt replay: %v", err)
	}
	found, exists, err := store.ReadTeamPlanHalt(owner2.Acquisition.Scope, "team-1")
	if err != nil || !exists || found != first {
		t.Fatal("cold halt lost")
	}
	if _, err := store.FreezeInitialTeamRun(context.Background(), verifier, owner2.Acquisition, approval, "team-1", "service", plan.FactDigest, prepared); err == nil {
		t.Fatal("halted plan created inputs")
	}
	if !bytes.Equal(before, reservationLedgerBytes(t, store)) {
		t.Fatal("blocked dispatch mutated ledger")
	}
	current, exists, err := store.ReadTeamPlan(owner2.Acquisition.Scope, "team-1")
	if err != nil || !exists || !bytes.Equal(teamTestBytes(t, current), teamTestBytes(t, plan)) {
		t.Fatal("halt rewrote plan or budget")
	}
	plans, err := store.ListTeamPlans(owner2.Acquisition.Scope)
	if err != nil || len(plans) != 1 || plans[0].FactDigest != plan.FactDigest || !bytes.Equal(before, reservationLedgerBytes(t, store)) {
		t.Fatal("cold enumeration changed or omitted halted plan")
	}
	changed := halt
	changed.Stage = "start"
	if _, err := store.HaltTeamPlan(context.Background(), verifier, owner2.Acquisition, approval, changed); err == nil {
		t.Fatal("later reason overwrote first failure")
	}
}

func TestTeamHaltRejectsForgedOrUnknownInputs(t *testing.T) {
	store, err := OpenResultIngressStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	owner, _ := supervisorTestAcquireOwner(t, store, attemptTestIdentity())
	plan, approval, _ := teamCreationFixture(t, store, owner.Acquisition)
	valid := TeamPlanHalt{GoalID: "team-1", NodeID: "service", PlanFactDigest: plan.FactDigest, Stage: "start"}
	for _, mode := range []string{"unknown-stage", "unknown-node", "unknown-goal", "stale-plan", "claimed-digest", "cancel", "verifier"} {
		t.Run(mode, func(t *testing.T) {
			input := valid
			verifier := teamTestApproval{owner.Acquisition, approval, false}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch mode {
			case "unknown-stage":
				input.Stage = "private worker content"
			case "unknown-node":
				input.NodeID = "unknown"
			case "unknown-goal":
				input.GoalID = "unknown"
			case "stale-plan":
				input.PlanFactDigest = attemptTestDigest("stale")
			case "claimed-digest":
				input.FactDigest = attemptTestDigest("claimed")
			case "cancel":
				cancel()
			case "verifier":
				verifier = teamTestApproval{owner.Acquisition, approval, true}
			}
			before := reservationLedgerBytes(t, store)
			if _, err := store.HaltTeamPlan(ctx, verifier, owner.Acquisition, approval, input); err == nil {
				t.Fatal("invalid halt accepted")
			}
			if !bytes.Equal(before, reservationLedgerBytes(t, store)) {
				t.Fatal("invalid halt appended")
			}
		})
	}
}
