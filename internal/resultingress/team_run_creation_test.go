package resultingress

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/goal"
)

// Store-only template/probe fixtures, not actual Task schema or Pi evidence.
func teamCreationFixture(t *testing.T, store *DurableStore, owner ControlOwnerAcquisition) (TeamPlanState, TeamPlanApproval, []byte) {
	t.Helper()
	inputs, _, _ := teamTestInputs(t, owner)
	for i := range inputs.Nodes {
		node := &inputs.Nodes[i]
		taskID, runID, err := goal.TeamNodeIDs(inputs.Proposal, node.NodeID)
		if err != nil {
			t.Fatal(err)
		}
		node.Task = teamTestBytes(t, map[string]any{
			"metadata":   map[string]any{"id": taskID},
			"repository": map[string]any{"path": inputs.Spec.Repository, "baseRef": inputs.BaseSHA},
			"work":       map[string]any{"context": "preserve the complete approved template"},
		})
		node.Policy = teamTestBytes(t, map[string]any{"taskId": taskID, "runId": runID})
	}
	raw := teamTestBytes(t, inputs)
	approval := TeamPlanApproval{InputsDigest: canonical.DigestBytes(raw), RequestDigest: attemptTestDigest("approved-creation-fixture")}
	plan, err := store.AcceptInitialTeamPlan(context.Background(), teamTestApproval{owner, approval, false}, owner, approval, raw)
	if err != nil {
		t.Fatal(err)
	}
	_, runID, err := goal.TeamNodeIDs(inputs.Proposal, inputs.Nodes[0].NodeID)
	if err != nil {
		t.Fatal(err)
	}
	prepared := map[string]any{
		"runId": runID, "repositoryRoot": inputs.Spec.Repository, "baseSha": inputs.BaseSHA,
		"preparedAt": time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC),
		"task":       inputs.Nodes[0].Task, "policy": inputs.Nodes[0].Policy,
		"capability":        map[string]any{"adapterId": "pi", "probeStatus": "supported"},
		"selectionAttempts": []any{map[string]any{"AdapterID": "pi", "Outcome": "selected"}},
	}
	return plan, approval, teamTestBytes(t, prepared)
}

func TestTeamRunCreationColdReplayDoesNotRefreshOrReserveAgain(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenResultIngressStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	owner, _ := supervisorTestAcquireOwner(t, store, attemptTestIdentity())
	plan, approval, raw := teamCreationFixture(t, store, owner.Acquisition)
	before := reservationLedgerBytes(t, store)
	created, err := store.FreezeInitialTeamRun(context.Background(), teamTestApproval{owner.Acquisition, approval, false}, owner.Acquisition, approval, "team-1", "service", plan.FactDigest, raw)
	if err != nil {
		t.Fatal(err)
	}
	if created.InputsDigest != canonical.DigestBytes(raw) || created.RunID == "" || created.FactDigest == "" ||
		bytes.Count(reservationLedgerBytes(t, store), []byte{'\n'}) != bytes.Count(before, []byte{'\n'})+1 {
		t.Fatal("freeze did not append exactly one complete creation fact")
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
	before = reservationLedgerBytes(t, store)
	found, exists, err := store.ReadTeamRunCreation(owner2.Acquisition.Scope, "team-1", "service")
	if err != nil || !exists || !bytes.Equal(teamTestBytes(t, found), teamTestBytes(t, created)) {
		t.Fatalf("cold lookup lost frozen creation: %v", err)
	}
	replayed, err := store.FreezeInitialTeamRun(context.Background(), teamTestApproval{owner2.Acquisition, approval, false}, owner2.Acquisition, approval, "team-1", "service", plan.FactDigest, raw)
	if err != nil || replayed.FactDigest != created.FactDigest || !bytes.Equal(before, reservationLedgerBytes(t, store)) {
		t.Fatalf("replay appended or changed identity: %v", err)
	}
	if _, err := store.FreezeInitialTeamRun(context.Background(), teamTestApproval{owner.Acquisition, approval, false}, owner.Acquisition, approval, "team-1", "service", plan.FactDigest, raw); !errors.Is(err, ErrControlOwnerNotCurrent) {
		t.Fatalf("stale owner: %v", err)
	}
	var changed map[string]any
	if json.Unmarshal(raw, &changed) != nil {
		t.Fatal("decode fixture")
	}
	changed["preparedAt"] = "2026-09-07T00:00:01Z"
	if _, err := store.FreezeInitialTeamRun(context.Background(), teamTestApproval{owner2.Acquisition, approval, false}, owner2.Acquisition, approval, "team-1", "service", plan.FactDigest, teamTestBytes(t, changed)); !errors.Is(err, ErrTeamRunCreationConflict) {
		t.Fatalf("retry refreshed frozen probe time: %v", err)
	}
	if !bytes.Equal(before, reservationLedgerBytes(t, store)) {
		t.Fatal("rejection changed ledger")
	}
}

func TestTeamCreationObligationsAreScopedReadOnlyAndColdReplayable(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenResultIngressStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	owner, _ := supervisorTestAcquireOwner(t, store, attemptTestIdentity())
	plan, approval, raw := teamCreationFixture(t, store, owner.Acquisition)
	if got, err := store.ListTeamCreationObligations(owner.Acquisition.Scope); err != nil || len(got) != 0 {
		t.Fatalf("approval alone is not a frozen creation: %v", err)
	}
	var prepared teamPreparedInputs
	if json.Unmarshal(raw, &prepared) != nil {
		t.Fatal("decode prepared fixture")
	}
	if _, member, err := store.ReadTeamRunObligation(owner.Acquisition.Scope, prepared.RunID); err == nil || !member {
		t.Fatal("approved but unfrozen node allowed standalone fallback")
	}
	creation, err := store.FreezeInitialTeamRun(context.Background(), teamTestApproval{owner.Acquisition, approval, false}, owner.Acquisition, approval, "team-1", "service", plan.FactDigest, raw)
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
	before := reservationLedgerBytes(t, store)
	got, err := store.ListTeamCreationObligations(owner.Acquisition.Scope)
	if err != nil || len(got) != 1 || got[0].Plan.FactDigest != plan.FactDigest || got[0].Creation.FactDigest != creation.FactDigest || !bytes.Equal(got[0].Creation.Inputs, raw) {
		t.Fatalf("cold enumeration lost exact facts: %v", err)
	}
	if bound, member, err := store.ReadTeamRunObligation(owner.Acquisition.Scope, creation.RunID); err != nil || !member || bound.Creation.FactDigest != creation.FactDigest {
		t.Fatalf("cold membership lost creation: %v", err)
	}
	other := owner.Acquisition.Scope
	other.RepositoryIdentityDigest = attemptTestDigest("other repository")
	if _, member, err := store.ReadTeamRunObligation(other, creation.RunID); err != nil || member {
		t.Fatalf("membership crossed repository scope: %v", err)
	}
	if got, err := store.ListTeamCreationObligations(other); err != nil || len(got) != 0 {
		t.Fatalf("enumeration crossed repository scope: %v", err)
	}
	if _, err := store.ListTeamCreationObligations(ControlOwnerScope{}); err == nil {
		t.Fatal("invalid scope accepted")
	}
	if !bytes.Equal(before, reservationLedgerBytes(t, store)) {
		t.Fatal("enumeration mutated ledger")
	}
}

func TestTeamRunCreationRejectsChangedPlanInputAndPrematureIntegration(t *testing.T) {
	store, err := OpenResultIngressStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	owner, _ := supervisorTestAcquireOwner(t, store, attemptTestIdentity())
	plan, approval, raw := teamCreationFixture(t, store, owner.Acquisition)
	before := reservationLedgerBytes(t, store)
	for _, mode := range []string{"missing-plan", "wrong-plan", "wrong-approval", "integration", "node", "task", "policy", "base", "run", "repository", "capability", "fallback", "time", "unknown", "oversize", "denied", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			var prepared map[string]any
			if json.Unmarshal(raw, &prepared) != nil {
				t.Fatal("decode fixture")
			}
			goalID, nodeID, factDigest, requestedApproval := "team-1", "service", plan.FactDigest, approval
			verifier := teamTestApproval{owner.Acquisition, approval, false}
			ctx := context.Background()
			switch mode {
			case "missing-plan":
				goalID = "other-goal"
			case "wrong-plan":
				factDigest = attemptTestDigest("wrong-plan")
			case "wrong-approval":
				requestedApproval.RequestDigest = attemptTestDigest("wrong-approval")
				verifier.approval = requestedApproval
			case "integration":
				nodeID = "integration"
			case "node":
				nodeID = "missing"
			case "task":
				prepared["task"].(map[string]any)["work"] = map[string]any{"context": "changed"}
			case "policy":
				prepared["policy"].(map[string]any)["extra"] = true
			case "base":
				prepared["baseSha"] = strings.Repeat("b", 40)
			case "run":
				prepared["runId"] = "another-run"
			case "repository":
				prepared["repositoryRoot"] = "/other"
			case "capability":
				prepared["capability"].(map[string]any)["adapterId"] = "other"
			case "fallback":
				prepared["selectionAttempts"] = []any{}
			case "time":
				prepared["preparedAt"] = "2026-09-07T08:00:00+08:00"
			case "unknown":
				prepared["extra"] = true
			case "oversize":
				prepared["capability"].(map[string]any)["oversize"] = strings.Repeat("x", 64<<10)
			case "denied":
				verifier.deny = true
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if _, err := store.FreezeInitialTeamRun(ctx, verifier, owner.Acquisition, requestedApproval, goalID, nodeID, factDigest, teamTestBytes(t, prepared)); err == nil {
				t.Fatal("invalid creation accepted")
			}
			if !bytes.Equal(before, reservationLedgerBytes(t, store)) {
				t.Fatal("rejection appended")
			}
		})
	}
}

func TestTeamRunCreationConcurrentFreezeHasOneCommit(t *testing.T) {
	store, err := OpenResultIngressStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	owner, _ := supervisorTestAcquireOwner(t, store, attemptTestIdentity())
	plan, approval, raw := teamCreationFixture(t, store, owner.Acquisition)
	before := reservationLedgerBytes(t, store)
	results := make(chan TeamRunCreationState, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := store.FreezeInitialTeamRun(context.Background(), teamTestApproval{owner.Acquisition, approval, false}, owner.Acquisition, approval, "team-1", "service", plan.FactDigest, raw)
			if err != nil {
				t.Error(err)
				return
			}
			results <- result
		}()
	}
	wg.Wait()
	close(results)
	if len(results) != 2 {
		t.Fatal("both exact requests must resolve")
	}
	first, second := <-results, <-results
	if first.FactDigest != second.FactDigest || bytes.Count(reservationLedgerBytes(t, store), []byte{'\n'}) != bytes.Count(before, []byte{'\n'})+1 {
		t.Fatal("concurrent freeze created two facts")
	}
}

func TestTeamRunCreationReplayRejectsForgedAndDuplicateFact(t *testing.T) {
	for _, mode := range []string{"run", "template", "duplicate"} {
		t.Run(mode, func(t *testing.T) {
			store, err := OpenResultIngressStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			owner, _ := supervisorTestAcquireOwner(t, store, attemptTestIdentity())
			plan, approval, raw := teamCreationFixture(t, store, owner.Acquisition)
			if _, err := store.FreezeInitialTeamRun(context.Background(), teamTestApproval{owner.Acquisition, approval, false}, owner.Acquisition, approval, "team-1", "service", plan.FactDigest, raw); err != nil {
				t.Fatal(err)
			}
			lines := bytes.Split(bytes.TrimSpace(reservationLedgerBytes(t, store)), []byte{'\n'})
			var fact teamRunCreationFact
			if json.Unmarshal(lines[len(lines)-1], &fact) != nil {
				t.Fatal("decode fixture")
			}
			switch mode {
			case "run":
				fact.Creation.RunID = "forged"
			case "template":
				var prepared map[string]any
				if json.Unmarshal(fact.Creation.Inputs, &prepared) != nil {
					t.Fatal("decode fixture")
				}
				prepared["task"].(map[string]any)["work"] = map[string]any{"context": "forged"}
				fact.Creation.Inputs = teamTestBytes(t, prepared)
				fact.Creation.InputsDigest = canonical.DigestBytes(fact.Creation.Inputs)
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
			if _, _, err := store.ReadTeamRunCreation(owner.Acquisition.Scope, "team-1", "service"); err == nil {
				t.Fatal("forged replay accepted")
			}
		})
	}
}
