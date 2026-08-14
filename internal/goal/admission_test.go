package goal

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/chiga0/marshal-harness/internal/authority"
)

func testPolicy() AdmissionPolicy {
	return AdmissionPolicy{
		ExecutorKinds:     []ExecutorKind{ExecutorKindImplement, ExecutorKindVerify, ExecutorKindReview, ExecutorKindPublish},
		Repositories:      []string{"repo-a"},
		Paths:             []string{"internal/**", "docs/**"},
		SideEffectClasses: []string{"local-cleanup"},
	}
}

func testAuthorityState(t *testing.T) AuthorityState {
	t.Helper()
	spec := validSpecRevision()
	return AuthorityState{
		AuthorityNamespaceId: testNamespace(),
		GoalId:               "goal-1",
		ProjectId:            "project-1",
		Repository:           "repo-a",
		SpecRevision:         &spec,
		Budget:               validBudgetRecord(),
	}
}

func proposalBytes(t *testing.T, proposal GoalPlanProposal) []byte {
	t.Helper()
	raw, err := json.Marshal(proposal.normalized())
	if err != nil {
		t.Fatalf("marshal proposal: %v", err)
	}
	return raw
}

func expectRejection(t *testing.T, decision AdmissionDecision, step AdmissionStep, reason string) *AdmissionRejection {
	t.Helper()
	if decision.Accepted {
		t.Fatalf("expected rejection %s/%s, got acceptance", step, reason)
	}
	if decision.Rejection == nil {
		t.Fatal("rejected decision carries no rejection record")
	}
	if decision.Rejection.Step != step {
		t.Fatalf("rejection step = %s, want %s", decision.Rejection.Step, step)
	}
	if decision.Rejection.Reason != reason {
		t.Fatalf("rejection reason = %s, want %s", decision.Rejection.Reason, reason)
	}
	if err := decision.Rejection.Validate(); err != nil {
		t.Fatalf("rejection record does not validate: %v", err)
	}
	if decision.Revision != nil || len(decision.ReservationPlan) != 0 {
		t.Fatal("rejected decision must not carry execution state or reservations")
	}
	return decision.Rejection
}

func TestAdmissionAcceptsInitialPlanDeterministically(t *testing.T) {
	state := testAuthorityState(t)
	policy := testPolicy()
	proposal := validProposal()
	proposal.Nodes = []GoalNode{validNode("n1"), validNode("n2")}
	proposal.Edges = []GoalEdge{{From: "n1", To: "n2", Kind: EdgeKindDependsOn}}
	raw := proposalBytes(t, proposal)

	first := Evaluate(raw, state, policy)
	if !first.Accepted {
		t.Fatalf("expected acceptance, got rejection %+v", first.Rejection)
	}
	second := Evaluate(raw, state, policy)

	// Double-run determinism: identical input yields the identical decision.
	if !reflect.DeepEqual(first, second) {
		t.Fatal("Evaluate is not deterministic across a double run")
	}
	firstDigest, err := first.Revision.Digest()
	if err != nil {
		t.Fatalf("Digest of accepted revision: %v", err)
	}
	secondDigest, err := second.Revision.Digest()
	if err != nil || firstDigest != secondDigest {
		t.Fatalf("accepted revision digests diverge: %q vs %q (err=%v)", firstDigest, secondDigest, err)
	}

	revision := *first.Revision
	if revision.PlanRevision != 1 || revision.PreviousPlanDigest != "" {
		t.Fatalf("first accepted revision must be planRevision 1 with an empty predecessor, got %+v", revision)
	}
	proposalDigest, err := proposal.Digest()
	if err != nil {
		t.Fatalf("proposal digest: %v", err)
	}
	if revision.ProposalDigest != proposalDigest {
		t.Fatalf("accepted revision binds proposal digest %q, want %q", revision.ProposalDigest, proposalDigest)
	}
	policyDigest, err := policy.Digest()
	if err != nil || revision.PolicyDigest != policyDigest {
		t.Fatalf("accepted revision binds policy digest %q, want %q (err=%v)", revision.PolicyDigest, policyDigest, err)
	}
	budgetDigest, err := state.Budget.Digest()
	if err != nil || revision.BudgetSnapshotDigest != budgetDigest {
		t.Fatalf("accepted revision binds budget digest %q, want %q (err=%v)", revision.BudgetSnapshotDigest, budgetDigest, err)
	}
	if len(revision.Nodes) != 2 || len(revision.Edges) != 1 {
		t.Fatalf("accepted revision carries %d nodes and %d edges", len(revision.Nodes), len(revision.Edges))
	}
	if len(first.ReservationPlan) != 2 {
		t.Fatalf("reservation plan carries %d entries, want 2", len(first.ReservationPlan))
	}
	for index, request := range first.ReservationPlan {
		if err := request.Validate(); err != nil {
			t.Fatalf("reservation request %d invalid: %v", index, err)
		}
		if request.PlanRevision != 1 || request.NodeId != revision.Nodes[index].NodeId {
			t.Fatalf("reservation request %d binds planRevision %d node %s", index, request.PlanRevision, request.NodeId)
		}
		if request.Estimate != revision.Nodes[index].Estimate {
			t.Fatalf("reservation request %d estimate diverges from the node estimate", index)
		}
	}
}

func TestAdmissionRejectsRejectedInputWithoutExecutionState(t *testing.T) {
	state := testAuthorityState(t)
	proposal := validProposal()
	proposal.GoalId = "other-goal"
	raw := proposalBytes(t, proposal)

	evaluator, err := NewEvaluator(testPolicy(), nil)
	if err != nil {
		t.Fatalf("NewEvaluator: %v", err)
	}
	decision := evaluator.Admit(raw, state)
	rejection := expectRejection(t, decision, AdmissionStepScope, ReasonGoalIdentityMismatch)
	if rejection.ProposalDigest == "" {
		t.Fatal("rejection must bind the proposal digest when canonicalization succeeded")
	}
	entries := evaluator.Audit().Entries()
	if len(entries) != 1 {
		t.Fatalf("audit ledger carries %d entries, want 1", len(entries))
	}
	if entries[0].Accepted || entries[0].Rejection == nil || entries[0].Rejection.Reason != ReasonGoalIdentityMismatch {
		t.Fatalf("audit entry does not record the rejection: %+v", entries[0])
	}
	records := evaluator.Audit().Rejections()
	if len(records) != 1 || !records[0].Equal(*rejection) {
		t.Fatalf("audit rejections diverge: %+v", records)
	}
}

func TestAdmissionStepOrderIsFrozen(t *testing.T) {
	t.Run("step 2 precedes step 3", func(t *testing.T) {
		state := testAuthorityState(t)
		proposal := validProposal()
		// Step 2 violation: wrong goal identity.
		proposal.GoalId = "other-goal"
		// Step 3 violation: duplicate node.
		proposal.Nodes = append(proposal.Nodes, validNode("n1"))
		decision := Evaluate(proposalBytes(t, proposal), state, testPolicy())
		expectRejection(t, decision, AdmissionStepScope, ReasonGoalIdentityMismatch)
	})
	t.Run("step 3 precedes step 5", func(t *testing.T) {
		state := testAuthorityState(t)
		proposal := validProposal()
		// Step 3 violation: duplicate node; step 5 violation: dangling edge.
		proposal.Nodes = []GoalNode{validNode("n1"), validNode("n1")}
		proposal.Edges = []GoalEdge{{From: "n1", To: "ghost", Kind: EdgeKindDependsOn}}
		decision := Evaluate(proposalBytes(t, proposal), state, testPolicy())
		expectRejection(t, decision, AdmissionStepNodeEdgeIntegrity, ReasonDuplicateNode)
	})
	t.Run("step 4 precedes step 5", func(t *testing.T) {
		state := testAuthorityState(t)
		proposal := validProposal()
		// Step 4 violation: disallowed side-effect class; step 5: self edge.
		proposal.Nodes[0].SideEffectClasses = []string{"external-notify"}
		proposal.Edges = []GoalEdge{{From: "n1", To: "n1", Kind: EdgeKindDependsOn}}
		decision := Evaluate(proposalBytes(t, proposal), state, testPolicy())
		expectRejection(t, decision, AdmissionStepAllowlist, ReasonSideEffectClassNotAllowed)
	})
	t.Run("step 5 precedes step 6", func(t *testing.T) {
		state := testAuthorityState(t)
		// Step 6 violation: the run budget is already exhausted.
		state.Budget.Used.TotalRuns = state.Budget.Limits.MaxTotalRuns
		proposal := validProposal()
		// Step 5 violation: self edge.
		proposal.Edges = []GoalEdge{{From: "n1", To: "n1", Kind: EdgeKindDependsOn}}
		decision := Evaluate(proposalBytes(t, proposal), state, testPolicy())
		expectRejection(t, decision, AdmissionStepGraphStructure, ReasonSelfEdge)
	})
}

func TestAdmissionStep1SchemaDigestCAS(t *testing.T) {
	state := testAuthorityState(t)
	policy := testPolicy()

	t.Run("malformed JSON", func(t *testing.T) {
		decision := Evaluate([]byte("{"), state, policy)
		rejection := expectRejection(t, decision, AdmissionStepSchemaDigestCAS, ReasonCanonicalRejected)
		if rejection.ProposalDigest != "" {
			t.Fatal("canonicalization failure must not carry a proposal digest")
		}
	})
	t.Run("duplicate members rejected by JCS", func(t *testing.T) {
		decision := Evaluate([]byte(`{"a":1,"a":2}`), state, policy)
		expectRejection(t, decision, AdmissionStepSchemaDigestCAS, ReasonCanonicalRejected)
	})
	t.Run("schema violation", func(t *testing.T) {
		proposal := validProposal()
		proposal.GoalId = ""
		decision := Evaluate(proposalBytes(t, proposal), state, policy)
		expectRejection(t, decision, AdmissionStepSchemaDigestCAS, ReasonSchemaViolation)
	})
	t.Run("unknown field breaks the canonical round trip", func(t *testing.T) {
		var document map[string]json.RawMessage
		if err := json.Unmarshal(proposalBytes(t, validProposal()), &document); err != nil {
			t.Fatalf("unmarshal proposal: %v", err)
		}
		document["exfiltrated"] = []byte(`"surprise"`)
		raw, err := json.Marshal(document)
		if err != nil {
			t.Fatalf("marshal extended proposal: %v", err)
		}
		decision := Evaluate(raw, state, policy)
		expectRejection(t, decision, AdmissionStepSchemaDigestCAS, ReasonDigestInstability)
	})
	t.Run("spec revision CAS conflict", func(t *testing.T) {
		proposal := validProposal()
		proposal.GoalSpecDigest = digestOfLiteral("forged-spec")
		decision := Evaluate(proposalBytes(t, proposal), state, policy)
		expectRejection(t, decision, AdmissionStepSchemaDigestCAS, ReasonSpecRevisionConflict)
	})
	t.Run("spec revision number CAS conflict", func(t *testing.T) {
		proposal := validProposal()
		proposal.GoalSpecRevision = 2
		decision := Evaluate(proposalBytes(t, proposal), state, policy)
		expectRejection(t, decision, AdmissionStepSchemaDigestCAS, ReasonSpecRevisionConflict)
	})
	t.Run("initial plan claiming a base revision", func(t *testing.T) {
		proposal := validProposal()
		proposal.BasedOnPlanRevision = 1
		proposal.BasedOnPlanDigest = digestOfLiteral("base")
		decision := Evaluate(proposalBytes(t, proposal), state, policy)
		expectRejection(t, decision, AdmissionStepSchemaDigestCAS, ReasonPlanRevisionConflict)
	})
	t.Run("stale base revision digest", func(t *testing.T) {
		accepted := acceptInitialPlan(t, state, policy)
		withPlan := state
		withPlan.PlanRevision = &accepted
		proposal := replanProposal(t, accepted)
		proposal.BasedOnPlanDigest = digestOfLiteral("forged-base")
		decision := Evaluate(proposalBytes(t, proposal), withPlan, policy)
		expectRejection(t, decision, AdmissionStepSchemaDigestCAS, ReasonPlanRevisionConflict)
	})
	t.Run("replan required when a plan exists", func(t *testing.T) {
		accepted := acceptInitialPlan(t, state, policy)
		withPlan := state
		withPlan.PlanRevision = &accepted
		decision := Evaluate(proposalBytes(t, validProposal()), withPlan, policy)
		expectRejection(t, decision, AdmissionStepSchemaDigestCAS, ReasonPlanRevisionConflict)
	})
	t.Run("invalid authority state fails closed at step 1", func(t *testing.T) {
		broken := testAuthorityState(t)
		broken.SpecRevision = nil
		decision := Evaluate(proposalBytes(t, validProposal()), broken, policy)
		expectRejection(t, decision, AdmissionStepSchemaDigestCAS, ReasonSchemaViolation)
	})
	t.Run("invalid policy fails closed at step 1", func(t *testing.T) {
		brokenPolicy := testPolicy()
		brokenPolicy.Paths = nil
		decision := Evaluate(proposalBytes(t, validProposal()), state, brokenPolicy)
		expectRejection(t, decision, AdmissionStepSchemaDigestCAS, ReasonSchemaViolation)
	})
}

func TestAdmissionStep2Scope(t *testing.T) {
	policy := testPolicy()
	t.Run("authority scope mismatch", func(t *testing.T) {
		state := testAuthorityState(t)
		proposal := validProposal()
		proposal.AuthorityNamespaceId.AuthorityScopeId = "other-scope"
		decision := Evaluate(proposalBytes(t, proposal), state, policy)
		expectRejection(t, decision, AdmissionStepScope, ReasonAuthorityScopeMismatch)
	})
	t.Run("goal identity mismatch", func(t *testing.T) {
		state := testAuthorityState(t)
		proposal := validProposal()
		proposal.GoalId = "goal-2"
		decision := Evaluate(proposalBytes(t, proposal), state, policy)
		expectRejection(t, decision, AdmissionStepScope, ReasonGoalIdentityMismatch)
	})
	t.Run("project out of scope", func(t *testing.T) {
		state := testAuthorityState(t)
		proposal := validProposal()
		proposal.ProjectId = "project-2"
		decision := Evaluate(proposalBytes(t, proposal), state, policy)
		expectRejection(t, decision, AdmissionStepScope, ReasonProjectOutOfScope)
	})
	t.Run("repository out of scope", func(t *testing.T) {
		state := testAuthorityState(t)
		proposal := validProposal()
		proposal.Repository = "repo-b"
		decision := Evaluate(proposalBytes(t, proposal), state, policy)
		expectRejection(t, decision, AdmissionStepScope, ReasonRepositoryOutOfScope)
	})
}

func TestAdmissionStep3NodeEdgeIntegrity(t *testing.T) {
	policy := testPolicy()

	t.Run("invalid node rejected", func(t *testing.T) {
		state := testAuthorityState(t)
		proposal := validProposal()
		proposal.Nodes[0].ExecutorKind = ExecutorKind("plan")
		decision := Evaluate(proposalBytes(t, proposal), state, policy)
		expectRejection(t, decision, AdmissionStepNodeEdgeIntegrity, ReasonNodeInvalid)
	})
	t.Run("invalid edge rejected", func(t *testing.T) {
		state := testAuthorityState(t)
		proposal := validProposal()
		proposal.Nodes = []GoalNode{validNode("n1"), validNode("n2")}
		proposal.Edges = []GoalEdge{{From: "n1", To: "n2", Kind: "blocks"}}
		decision := Evaluate(proposalBytes(t, proposal), state, policy)
		expectRejection(t, decision, AdmissionStepNodeEdgeIntegrity, ReasonEdgeInvalid)
	})
	t.Run("duplicate node identity rejected", func(t *testing.T) {
		state := testAuthorityState(t)
		proposal := validProposal()
		proposal.Nodes = []GoalNode{validNode("n1"), validNode("n1")}
		decision := Evaluate(proposalBytes(t, proposal), state, policy)
		expectRejection(t, decision, AdmissionStepNodeEdgeIntegrity, ReasonDuplicateNode)
	})
	t.Run("supersession on an initial plan rejected", func(t *testing.T) {
		state := testAuthorityState(t)
		proposal := validProposal()
		proposal.Supersessions = []NodeSupersession{{NodeId: "n1", PreviousDigest: digestOfLiteral("old"), Reason: "r", Lineage: "l"}}
		decision := Evaluate(proposalBytes(t, proposal), state, policy)
		expectRejection(t, decision, AdmissionStepNodeEdgeIntegrity, ReasonSupersessionInvalid)
	})
}

// acceptInitialPlan admits a two-node chain plan n1 → n2 and returns the
// accepted revision for replan fixtures.
func acceptInitialPlan(t *testing.T, state AuthorityState, policy AdmissionPolicy) AcceptedGoalPlanRevision {
	t.Helper()
	proposal := validProposal()
	proposal.Nodes = []GoalNode{validNode("n1"), validNode("n2")}
	proposal.Edges = []GoalEdge{{From: "n1", To: "n2", Kind: EdgeKindDependsOn}}
	decision := Evaluate(proposalBytes(t, proposal), state, policy)
	if !decision.Accepted {
		t.Fatalf("expected acceptance of the initial plan, got %+v", decision.Rejection)
	}
	return *decision.Revision
}

func replanProposal(t *testing.T, base AcceptedGoalPlanRevision) GoalPlanProposal {
	t.Helper()
	spec := validSpecRevision()
	specDigest, err := spec.Digest()
	if err != nil {
		t.Fatalf("spec digest: %v", err)
	}
	baseDigest, err := base.Digest()
	if err != nil {
		t.Fatalf("base digest: %v", err)
	}
	nodes := make([]GoalNode, len(base.Nodes))
	copy(nodes, base.Nodes)
	edges := make([]GoalEdge, len(base.Edges))
	copy(edges, base.Edges)
	return GoalPlanProposal{
		AuthorityNamespaceId: testNamespace(),
		ProposalId:           "proposal-2",
		GoalId:               "goal-1",
		ProjectId:            "project-1",
		Repository:           "repo-a",
		GoalSpecRevision:     spec.Revision,
		GoalSpecDigest:       specDigest,
		BasedOnPlanRevision:  base.PlanRevision,
		BasedOnPlanDigest:    baseDigest,
		PlannerIdentity:      "planner-1",
		Nodes:                nodes,
		Edges:                edges,
		Supersessions:        []NodeSupersession{},
	}
}

func replanState(state AuthorityState, accepted AcceptedGoalPlanRevision, nodeStates map[string]NodeState) AuthorityState {
	next := state
	next.PlanRevision = &accepted
	next.NodeStates = nodeStates
	return next
}

func TestAdmissionReplanSupersedesPendingNode(t *testing.T) {
	state := testAuthorityState(t)
	policy := testPolicy()
	base := acceptInitialPlan(t, state, policy)

	proposal := replanProposal(t, base)
	replacement := base.Nodes[1]
	replacement.Title = "node n2 reworked"
	previousDigest, err := base.Nodes[1].Digest()
	if err != nil {
		t.Fatalf("base node digest: %v", err)
	}
	proposal.Nodes[1] = replacement
	proposal.Supersessions = []NodeSupersession{{
		NodeId:         "n2",
		PreviousDigest: previousDigest,
		Reason:         "verification scope rework",
		Lineage:        "plan-1:n2",
	}}

	decision := Evaluate(proposalBytes(t, proposal), replanState(state, base, nil), policy)
	if !decision.Accepted {
		t.Fatalf("expected acceptance of the replan, got %+v", decision.Rejection)
	}
	revision := *decision.Revision
	if revision.PlanRevision != 2 {
		t.Fatalf("replan revision = %d, want 2", revision.PlanRevision)
	}
	baseDigest, err := base.Digest()
	if err != nil || revision.PreviousPlanDigest != baseDigest {
		t.Fatalf("replan previousPlanDigest = %q, want %q (err=%v)", revision.PreviousPlanDigest, baseDigest, err)
	}
	if len(revision.Supersessions) != 1 || revision.Supersessions[0].NodeId != "n2" ||
		revision.Supersessions[0].Reason != "verification scope rework" || revision.Supersessions[0].Lineage != "plan-1:n2" {
		t.Fatalf("replan must preserve the supersession reason and lineage, got %+v", revision.Supersessions)
	}
	if len(revision.Nodes) != 2 || !revision.Nodes[1].Equal(replacement) {
		t.Fatalf("replan effective nodes diverge: %+v", revision.Nodes)
	}

	// Double-run determinism of the replan decision.
	second := Evaluate(proposalBytes(t, proposal), replanState(state, base, nil), policy)
	if !reflect.DeepEqual(decision, second) {
		t.Fatal("replan admission is not deterministic across a double run")
	}
}

func TestAdmissionStep3ReplanNegativeFixtures(t *testing.T) {
	state := testAuthorityState(t)
	policy := testPolicy()
	base := acceptInitialPlan(t, state, policy)
	baseNodeDigest := func(index int) string {
		digest, err := base.Nodes[index].Digest()
		if err != nil {
			t.Fatalf("base node digest: %v", err)
		}
		return digest
	}

	t.Run("running node may not be redefined", func(t *testing.T) {
		proposal := replanProposal(t, base)
		replacement := base.Nodes[0]
		replacement.Title = "node n1 tampered"
		proposal.Nodes[0] = replacement
		proposal.Supersessions = []NodeSupersession{{NodeId: "n1", PreviousDigest: baseNodeDigest(0), Reason: "r", Lineage: "l"}}
		decision := Evaluate(proposalBytes(t, proposal), replanState(state, base, map[string]NodeState{"n1": NodeStateRunning}), policy)
		expectRejection(t, decision, AdmissionStepNodeEdgeIntegrity, ReasonProtectedNodeModified)
	})
	t.Run("completed node may not be deleted", func(t *testing.T) {
		proposal := replanProposal(t, base)
		proposal.Nodes = []GoalNode{base.Nodes[1]}
		proposal.Edges = []GoalEdge{}
		proposal.Supersessions = []NodeSupersession{{NodeId: "n1", PreviousDigest: baseNodeDigest(0), Reason: "r", Lineage: "l"}}
		decision := Evaluate(proposalBytes(t, proposal), replanState(state, base, map[string]NodeState{"n1": NodeStateCompleted}), policy)
		expectRejection(t, decision, AdmissionStepNodeEdgeIntegrity, ReasonProtectedNodeDeleted)
	})
	t.Run("pending node dropped without supersession", func(t *testing.T) {
		proposal := replanProposal(t, base)
		proposal.Nodes = []GoalNode{base.Nodes[0]}
		proposal.Edges = []GoalEdge{}
		decision := Evaluate(proposalBytes(t, proposal), replanState(state, base, nil), policy)
		expectRejection(t, decision, AdmissionStepNodeEdgeIntegrity, ReasonPendingNodeDropped)
	})
	t.Run("same node identity with different digest fails closed", func(t *testing.T) {
		proposal := replanProposal(t, base)
		replacement := base.Nodes[1]
		replacement.Title = "node n2 silently changed"
		proposal.Nodes[1] = replacement
		decision := Evaluate(proposalBytes(t, proposal), replanState(state, base, nil), policy)
		rejection := expectRejection(t, decision, AdmissionStepNodeEdgeIntegrity, ReasonNodeIdentityConflict)
		if rejection.Subject != "n2" {
			t.Fatalf("rejection subject = %q, want n2", rejection.Subject)
		}
	})
	t.Run("supersession binding a wrong digest", func(t *testing.T) {
		proposal := replanProposal(t, base)
		replacement := base.Nodes[1]
		replacement.Title = "node n2 reworked"
		proposal.Nodes[1] = replacement
		proposal.Supersessions = []NodeSupersession{{NodeId: "n2", PreviousDigest: digestOfLiteral("forged"), Reason: "r", Lineage: "l"}}
		decision := Evaluate(proposalBytes(t, proposal), replanState(state, base, nil), policy)
		expectRejection(t, decision, AdmissionStepNodeEdgeIntegrity, ReasonSupersessionInvalid)
	})
	t.Run("supersession for an unchanged node", func(t *testing.T) {
		proposal := replanProposal(t, base)
		proposal.Supersessions = []NodeSupersession{{NodeId: "n1", PreviousDigest: baseNodeDigest(0), Reason: "r", Lineage: "l"}}
		decision := Evaluate(proposalBytes(t, proposal), replanState(state, base, nil), policy)
		expectRejection(t, decision, AdmissionStepNodeEdgeIntegrity, ReasonSupersessionInvalid)
	})
	t.Run("supersession for an unknown node", func(t *testing.T) {
		proposal := replanProposal(t, base)
		proposal.Supersessions = []NodeSupersession{{NodeId: "ghost", PreviousDigest: baseNodeDigest(0), Reason: "r", Lineage: "l"}}
		decision := Evaluate(proposalBytes(t, proposal), replanState(state, base, nil), policy)
		expectRejection(t, decision, AdmissionStepNodeEdgeIntegrity, ReasonSupersessionInvalid)
	})
}

func TestAdmissionStep4Allowlist(t *testing.T) {
	policy := testPolicy()
	policy.ExecutorKinds = []ExecutorKind{ExecutorKindImplement}
	policy.Repositories = []string{"repo-a"}

	t.Run("executor kind not allowed", func(t *testing.T) {
		state := testAuthorityState(t)
		proposal := validProposal()
		proposal.Nodes[0].ExecutorKind = ExecutorKindPublish
		decision := Evaluate(proposalBytes(t, proposal), state, policy)
		expectRejection(t, decision, AdmissionStepAllowlist, ReasonExecutorKindNotAllowed)
	})
	t.Run("repository not allowed", func(t *testing.T) {
		state := testAuthorityState(t)
		state.Repository = "repo-a"
		proposal := validProposal()
		proposal.Nodes[0].Repository = "repo-b"
		decision := Evaluate(proposalBytes(t, proposal), state, policy)
		expectRejection(t, decision, AdmissionStepAllowlist, ReasonRepositoryNotAllowed)
	})
	t.Run("path not allowed", func(t *testing.T) {
		state := testAuthorityState(t)
		proposal := validProposal()
		proposal.Nodes[0].Paths = []string{"secrets/keys.pem"}
		decision := Evaluate(proposalBytes(t, proposal), state, policy)
		expectRejection(t, decision, AdmissionStepAllowlist, ReasonPathNotAllowed)
	})
	t.Run("side effect class not allowed", func(t *testing.T) {
		state := testAuthorityState(t)
		proposal := validProposal()
		proposal.Nodes[0].SideEffectClasses = []string{"external-notify"}
		decision := Evaluate(proposalBytes(t, proposal), state, policy)
		expectRejection(t, decision, AdmissionStepAllowlist, ReasonSideEffectClassNotAllowed)
	})
}

func TestAdmissionStep5GraphGuardrails(t *testing.T) {
	policy := testPolicy()

	chainNodes := func(count int) []GoalNode {
		nodes := make([]GoalNode, 0, count)
		for index := 0; index < count; index++ {
			nodes = append(nodes, validNode(fmtNodeID(index)))
		}
		return nodes
	}
	chainEdges := func(count int) []GoalEdge {
		edges := make([]GoalEdge, 0, count-1)
		for index := 0; index < count-1; index++ {
			edges = append(edges, GoalEdge{From: fmtNodeID(index), To: fmtNodeID(index + 1), Kind: EdgeKindDependsOn})
		}
		return edges
	}

	t.Run("dangling edge", func(t *testing.T) {
		state := testAuthorityState(t)
		proposal := validProposal()
		proposal.Edges = []GoalEdge{{From: "n1", To: "ghost", Kind: EdgeKindDependsOn}}
		decision := Evaluate(proposalBytes(t, proposal), state, policy)
		expectRejection(t, decision, AdmissionStepGraphStructure, ReasonDanglingEdge)
	})
	t.Run("self edge", func(t *testing.T) {
		state := testAuthorityState(t)
		proposal := validProposal()
		proposal.Edges = []GoalEdge{{From: "n1", To: "n1", Kind: EdgeKindDependsOn}}
		decision := Evaluate(proposalBytes(t, proposal), state, policy)
		expectRejection(t, decision, AdmissionStepGraphStructure, ReasonSelfEdge)
	})
	t.Run("duplicate edge", func(t *testing.T) {
		state := testAuthorityState(t)
		proposal := validProposal()
		proposal.Nodes = []GoalNode{validNode("n1"), validNode("n2")}
		edge := GoalEdge{From: "n1", To: "n2", Kind: EdgeKindDependsOn}
		proposal.Edges = []GoalEdge{edge, edge}
		decision := Evaluate(proposalBytes(t, proposal), state, policy)
		expectRejection(t, decision, AdmissionStepGraphStructure, ReasonDuplicateEdge)
	})
	t.Run("cycle", func(t *testing.T) {
		state := testAuthorityState(t)
		proposal := validProposal()
		proposal.Nodes = []GoalNode{validNode("n1"), validNode("n2"), validNode("n3")}
		proposal.Edges = []GoalEdge{
			{From: "n1", To: "n2", Kind: EdgeKindDependsOn},
			{From: "n2", To: "n3", Kind: EdgeKindDependsOn},
			{From: "n3", To: "n1", Kind: EdgeKindDependsOn},
		}
		decision := Evaluate(proposalBytes(t, proposal), state, policy)
		rejection := expectRejection(t, decision, AdmissionStepGraphStructure, ReasonCycle)
		if rejection.Subject != "n1" {
			t.Fatalf("cycle subject = %q, want n1", rejection.Subject)
		}
	})
	t.Run("maxNodes exceeded", func(t *testing.T) {
		state := testAuthorityState(t)
		state.Budget.Limits.MaxNodes = 4
		proposal := validProposal()
		proposal.Nodes = chainNodes(5)
		proposal.Edges = chainEdges(5)
		decision := Evaluate(proposalBytes(t, proposal), state, policy)
		expectRejection(t, decision, AdmissionStepGraphStructure, ReasonMaxNodesExceeded)
	})
	t.Run("maxDepth exceeded", func(t *testing.T) {
		state := testAuthorityState(t)
		state.Budget.Limits.MaxDepth = 4
		proposal := validProposal()
		proposal.Nodes = chainNodes(5)
		proposal.Edges = chainEdges(5)
		decision := Evaluate(proposalBytes(t, proposal), state, policy)
		expectRejection(t, decision, AdmissionStepGraphStructure, ReasonMaxDepthExceeded)
	})
	t.Run("maxFanOut exceeded", func(t *testing.T) {
		state := testAuthorityState(t)
		state.Budget.Limits.MaxFanOut = 4
		proposal := validProposal()
		proposal.Nodes = chainNodes(6)
		edges := make([]GoalEdge, 0, 5)
		for index := 1; index <= 5; index++ {
			edges = append(edges, GoalEdge{From: "n0", To: fmtNodeID(index), Kind: EdgeKindDependsOn})
		}
		proposal.Edges = edges
		decision := Evaluate(proposalBytes(t, proposal), state, policy)
		rejection := expectRejection(t, decision, AdmissionStepGraphStructure, ReasonMaxFanOutExceeded)
		if rejection.Subject != "n0" {
			t.Fatalf("fan-out subject = %q, want n0", rejection.Subject)
		}
	})
	t.Run("maxConcurrentNodes exceeded", func(t *testing.T) {
		state := testAuthorityState(t)
		state.Budget.Limits.MaxConcurrentNodes = 4
		proposal := validProposal()
		// Five independent nodes share one level.
		proposal.Nodes = chainNodes(5)
		proposal.Edges = []GoalEdge{}
		decision := Evaluate(proposalBytes(t, proposal), state, policy)
		expectRejection(t, decision, AdmissionStepGraphStructure, ReasonMaxConcurrentExceeded)
	})
	t.Run("graph within every guardrail accepted", func(t *testing.T) {
		state := testAuthorityState(t)
		proposal := validProposal()
		proposal.Nodes = chainNodes(4)
		proposal.Edges = chainEdges(4)
		decision := Evaluate(proposalBytes(t, proposal), state, policy)
		if !decision.Accepted {
			t.Fatalf("expected acceptance, got %+v", decision.Rejection)
		}
	})
}

func fmtNodeID(index int) string {
	return "n" + string(rune('0'+index))
}

func TestAdmissionStep6Budget(t *testing.T) {
	policy := testPolicy()

	t.Run("maxPlanRevisions exceeded", func(t *testing.T) {
		state := testAuthorityState(t)
		state.Budget.Used.PlanRevisions = state.Budget.Limits.MaxPlanRevisions
		decision := Evaluate(proposalBytes(t, validProposal()), state, policy)
		expectRejection(t, decision, AdmissionStepBudget, ReasonMaxPlanRevisionsExceeded)
	})

	dimensions := []struct {
		name    string
		exhaust func(state *AuthorityState)
	}{
		{"maxTotalRuns", func(state *AuthorityState) { state.Budget.Used.TotalRuns = state.Budget.Limits.MaxTotalRuns }},
		{"maxTotalAttempts", func(state *AuthorityState) { state.Budget.Used.TotalAttempts = state.Budget.Limits.MaxTotalAttempts }},
		{"maxWallTimeSeconds", func(state *AuthorityState) {
			state.Budget.Used.WallTimeSeconds = state.Budget.Limits.MaxWallTimeSeconds
		}},
		{"maxComputeUnits", func(state *AuthorityState) { state.Budget.Used.ComputeUnits = state.Budget.Limits.MaxComputeUnits }},
		{"maxTokens", func(state *AuthorityState) { state.Budget.Used.Tokens = state.Budget.Limits.MaxTokens }},
		{"maxArtifactBytes", func(state *AuthorityState) { state.Budget.Used.ArtifactBytes = state.Budget.Limits.MaxArtifactBytes }},
	}
	for _, dimension := range dimensions {
		t.Run(dimension.name+" exceeded", func(t *testing.T) {
			state := testAuthorityState(t)
			dimension.exhaust(&state)
			decision := Evaluate(proposalBytes(t, validProposal()), state, policy)
			rejection := expectRejection(t, decision, AdmissionStepBudget, ReasonBudgetLimitExceeded)
			if rejection.Subject != dimension.name {
				t.Fatalf("budget rejection subject = %q, want %s", rejection.Subject, dimension.name)
			}
		})
	}

	t.Run("estimate fitting the budget accepted", func(t *testing.T) {
		state := testAuthorityState(t)
		proposal := validProposal()
		proposal.Nodes[0].Estimate = NodeEstimate{
			Runs:            state.Budget.Limits.MaxTotalRuns,
			Attempts:        state.Budget.Limits.MaxTotalAttempts,
			WallTimeSeconds: state.Budget.Limits.MaxWallTimeSeconds,
			ComputeUnits:    state.Budget.Limits.MaxComputeUnits,
			Tokens:          state.Budget.Limits.MaxTokens,
			ArtifactBytes:   state.Budget.Limits.MaxArtifactBytes,
		}
		decision := Evaluate(proposalBytes(t, proposal), state, policy)
		if !decision.Accepted {
			t.Fatalf("expected acceptance at the exact budget boundary, got %+v", decision.Rejection)
		}
	})
}

func TestEvaluatorAuditAppendsAcceptedAndRejectedEntries(t *testing.T) {
	state := testAuthorityState(t)
	evaluator, err := NewEvaluator(testPolicy(), nil)
	if err != nil {
		t.Fatalf("NewEvaluator: %v", err)
	}

	accepted := evaluator.Admit(proposalBytes(t, validProposal()), state)
	if !accepted.Accepted {
		t.Fatalf("expected acceptance, got %+v", accepted.Rejection)
	}
	rejected := evaluator.Admit([]byte("{"), state)
	if rejected.Accepted {
		t.Fatal("expected rejection of malformed input")
	}

	entries := evaluator.Audit().Entries()
	if len(entries) != 2 {
		t.Fatalf("audit ledger carries %d entries, want 2", len(entries))
	}
	if entries[0].Sequence != 1 || !entries[0].Accepted || entries[0].AcceptedRevisionDigest == "" {
		t.Fatalf("first audit entry malformed: %+v", entries[0])
	}
	if entries[1].Sequence != 2 || entries[1].Accepted || entries[1].Rejection == nil {
		t.Fatalf("second audit entry malformed: %+v", entries[1])
	}
	revisionDigest, err := accepted.Revision.Digest()
	if err != nil || entries[0].AcceptedRevisionDigest != revisionDigest {
		t.Fatalf("audit entry binds revision digest %q, want %q (err=%v)", entries[0].AcceptedRevisionDigest, revisionDigest, err)
	}
}

func TestNewEvaluatorRejectsInvalidPolicy(t *testing.T) {
	broken := testPolicy()
	broken.ExecutorKinds = nil
	if _, err := NewEvaluator(broken, nil); err == nil {
		t.Fatal("NewEvaluator accepted a policy without executor kinds")
	}
	invalidGlob := testPolicy()
	invalidGlob.Paths = []string{"["}
	if _, err := NewEvaluator(invalidGlob, nil); err == nil {
		t.Fatal("NewEvaluator accepted a policy with an invalid glob")
	}
}

func TestAdmissionRejectionValidateClosedVocabulary(t *testing.T) {
	rejection := AdmissionRejection{
		AuthorityNamespaceId: testNamespace(),
		GoalId:               "goal-1",
		ProposalDigest:       digestOfLiteral("proposal"),
		Step:                 AdmissionStepBudget,
		Reason:               ReasonBudgetLimitExceeded,
		Subject:              "maxTokens",
	}
	if err := rejection.Validate(); err != nil {
		t.Fatalf("Validate rejected a valid rejection record: %v", err)
	}
	if _, err := rejection.Digest(); err != nil {
		t.Fatalf("Digest rejected a valid rejection record: %v", err)
	}
	for _, mutate := range []func(*AdmissionRejection){
		func(rejection *AdmissionRejection) { rejection.AuthorityNamespaceId = authority.AuthorityNamespaceId{} },
		func(rejection *AdmissionRejection) { rejection.GoalId = "bad id" },
		func(rejection *AdmissionRejection) { rejection.ProposalDigest = "sha256:zz" },
		func(rejection *AdmissionRejection) { rejection.Step = AdmissionStep("step-7-unknown") },
		func(rejection *AdmissionRejection) { rejection.Reason = "because" },
	} {
		broken := rejection
		mutate(&broken)
		if err := broken.Validate(); err == nil {
			t.Fatalf("Validate accepted rejection %+v", broken)
		}
	}
}
