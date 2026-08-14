package goal

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/chiga0/marshal-harness/internal/authority"
)

func testNamespace() authority.AuthorityNamespaceId {
	return authority.AuthorityNamespaceId{
		TenantNamespace:  "tenant-default",
		ControlPlaneId:   "control-plane-1",
		AuthorityScopeId: "authority-scope-1",
	}
}

func digestOfLiteral(literal string) string {
	digest, err := canonicalDigestOf(map[string]string{"value": literal})
	if err != nil {
		panic(err)
	}
	return digest
}

func validNodeEstimate() NodeEstimate {
	return NodeEstimate{Runs: 1, Attempts: 2, WallTimeSeconds: 600, ComputeUnits: 1, Tokens: 1000, ArtifactBytes: 1024}
}

func validNode(id string) GoalNode {
	return GoalNode{
		NodeId:            id,
		ExecutorKind:      ExecutorKindImplement,
		Title:             "node " + id,
		Repository:        "repo-a",
		Paths:             []string{"internal/**"},
		SideEffectClasses: []string{"local-cleanup"},
		Estimate:          validNodeEstimate(),
	}
}

func validSpecRevision() GoalSpecRevision {
	return GoalSpecRevision{
		AuthorityNamespaceId: testNamespace(),
		GoalId:               "goal-1",
		Revision:             1,
		PreviousDigest:       "",
		ProjectId:            "project-1",
		Repository:           "repo-a",
		Title:                "Goal 1",
		Description:          "First frozen goal spec",
	}
}

func validProposal() GoalPlanProposal {
	spec := validSpecRevision()
	specDigest, err := spec.Digest()
	if err != nil {
		panic(err)
	}
	return GoalPlanProposal{
		AuthorityNamespaceId: testNamespace(),
		ProposalId:           "proposal-1",
		GoalId:               "goal-1",
		ProjectId:            "project-1",
		Repository:           "repo-a",
		GoalSpecRevision:     1,
		GoalSpecDigest:       specDigest,
		BasedOnPlanRevision:  0,
		BasedOnPlanDigest:    "",
		PlannerIdentity:      "planner-1",
		Nodes:                []GoalNode{validNode("n1")},
		Edges:                []GoalEdge{},
		Supersessions:        []NodeSupersession{},
	}
}

func validBudgetRecord() GoalBudgetLedger {
	return GoalBudgetLedger{
		AuthorityNamespaceId: testNamespace(),
		GoalId:               "goal-1",
		Limits: Guardrails{
			MaxNodes:           16,
			MaxDepth:           4,
			MaxFanOut:          4,
			MaxConcurrentNodes: 4,
			MaxPlanRevisions:   4,
			MaxTotalRuns:       16,
			MaxTotalAttempts:   32,
			MaxWallTimeSeconds: 36000,
			MaxComputeUnits:    64,
			MaxTokens:          1000000,
			MaxArtifactBytes:   1048576,
		},
		Used: BudgetUsage{},
	}
}

func TestNodeDigestStableAcrossKeyOrderAndSliceNormalization(t *testing.T) {
	node := validNode("n1")
	first, err := node.Digest()
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	if !strings.HasPrefix(first, authority.DigestPrefix) {
		t.Fatalf("digest %q does not carry the sha256 prefix", first)
	}
	reordered, err := node.Digest()
	if err != nil || reordered != first {
		t.Fatalf("Digest is not stable: %q vs %q (err=%v)", first, reordered, err)
	}

	raw := `{"estimate":{"artifactBytes":1024,"attempts":2,"computeUnits":1,"runs":1,"tokens":1000,"wallTimeSeconds":600},
	"executorKind":"implement","nodeId":"n1","paths":["internal/**"],"repository":"repo-a",
	"sideEffectClasses":["local-cleanup"],"title":"node n1"}`
	var decoded GoalNode
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("unmarshal reordered node: %v", err)
	}
	decodedDigest, err := decoded.Digest()
	if err != nil {
		t.Fatalf("Digest of reordered node: %v", err)
	}
	if decodedDigest != first {
		t.Fatalf("member order changed the digest: %q vs %q", decodedDigest, first)
	}

	normalized := validNode("n1")
	normalized.Paths = nil
	normalized.SideEffectClasses = nil
	rawEmpty := `{"estimate":{"artifactBytes":1024,"attempts":2,"computeUnits":1,"runs":1,"tokens":1000,"wallTimeSeconds":600},
	"executorKind":"implement","nodeId":"n1","paths":[],"repository":"repo-a",
	"sideEffectClasses":[],"title":"node n1"}`
	var decodedEmpty GoalNode
	if err := json.Unmarshal([]byte(rawEmpty), &decodedEmpty); err != nil {
		t.Fatalf("unmarshal empty-slice node: %v", err)
	}
	normalizedDigest, err := normalized.Digest()
	if err != nil {
		t.Fatalf("Digest of nil-slice node: %v", err)
	}
	emptyDigest, err := decodedEmpty.Digest()
	if err != nil {
		t.Fatalf("Digest of empty-slice node: %v", err)
	}
	if normalizedDigest != emptyDigest {
		t.Fatalf("nil and empty slices produced different digests: %q vs %q", normalizedDigest, emptyDigest)
	}
	if !normalized.Equal(decodedEmpty) {
		t.Fatal("Equal rejected normalized nil and empty slice content")
	}
}

func TestNodeValidateRejectsMalformedContent(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*GoalNode)
	}{
		{"empty nodeId", func(node *GoalNode) { node.NodeId = "" }},
		{"invalid nodeId", func(node *GoalNode) { node.NodeId = "bad node id" }},
		{"unknown executorKind", func(node *GoalNode) { node.ExecutorKind = ExecutorKind("plan") }},
		{"empty title", func(node *GoalNode) { node.Title = "  " }},
		{"empty repository", func(node *GoalNode) { node.Repository = "" }},
		{"absolute path", func(node *GoalNode) { node.Paths = []string{"/etc/passwd"} }},
		{"parent traversal path", func(node *GoalNode) { node.Paths = []string{"../secrets"} }},
		{"backslash path", func(node *GoalNode) { node.Paths = []string{`internal\file`} }},
		{"empty side effect class", func(node *GoalNode) { node.SideEffectClasses = []string{" "} }},
		{"zero runs estimate", func(node *GoalNode) { node.Estimate.Runs = 0 }},
		{"zero attempts estimate", func(node *GoalNode) { node.Estimate.Attempts = 0 }},
		{"negative tokens estimate", func(node *GoalNode) { node.Estimate.Tokens = -1 }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			node := validNode("n1")
			testCase.mutate(&node)
			if err := node.Validate(); err == nil {
				t.Fatalf("Validate accepted a node with %s", testCase.name)
			}
			if _, err := node.Digest(); err == nil {
				t.Fatalf("Digest accepted a node with %s", testCase.name)
			}
		})
	}
}

func TestEdgeValidateClosedVocabulary(t *testing.T) {
	edge := GoalEdge{From: "n1", To: "n2", Kind: EdgeKindDependsOn}
	if err := edge.Validate(); err != nil {
		t.Fatalf("Validate rejected a valid edge: %v", err)
	}
	if _, err := edge.Digest(); err != nil {
		t.Fatalf("Digest rejected a valid edge: %v", err)
	}
	for _, mutate := range []func(*GoalEdge){
		func(edge *GoalEdge) { edge.From = "" },
		func(edge *GoalEdge) { edge.To = "bad id" },
		func(edge *GoalEdge) { edge.Kind = "blocks" },
		func(edge *GoalEdge) { edge.Kind = "" },
	} {
		broken := edge
		mutate(&broken)
		if err := broken.Validate(); err == nil {
			t.Fatalf("Validate accepted edge %+v", broken)
		}
	}
}

func TestSpecRevisionCASChain(t *testing.T) {
	first := validSpecRevision()
	if err := ValidateSpecRevisionCAS(nil, first); err != nil {
		t.Fatalf("CAS rejected the first revision: %v", err)
	}

	second := first
	second.Revision = 2
	second.Title = "Goal 1 revised"
	firstDigest, err := first.Digest()
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	second.PreviousDigest = firstDigest
	if err := ValidateSpecRevisionCAS(&first, second); err != nil {
		t.Fatalf("CAS rejected a valid second revision: %v", err)
	}

	t.Run("first revision must be 1", func(t *testing.T) {
		broken := first
		broken.Revision = 2
		if err := ValidateSpecRevisionCAS(nil, broken); err == nil {
			t.Fatal("CAS accepted a first revision numbered 2")
		}
	})
	t.Run("first revision must not bind a previous digest", func(t *testing.T) {
		broken := first
		broken.PreviousDigest = firstDigest
		if err := ValidateSpecRevisionCAS(nil, broken); err == nil {
			t.Fatal("CAS accepted a first revision with a previousDigest")
		}
	})
	t.Run("revision must advance by exactly one", func(t *testing.T) {
		broken := second
		broken.Revision = 3
		if err := ValidateSpecRevisionCAS(&first, broken); err == nil {
			t.Fatal("CAS accepted a skipped revision number")
		}
	})
	t.Run("previous digest must bind the current revision", func(t *testing.T) {
		broken := second
		broken.PreviousDigest = digestOfLiteral("forged")
		if err := ValidateSpecRevisionCAS(&first, broken); err == nil {
			t.Fatal("CAS accepted a forged previousDigest")
		}
	})
	t.Run("goal identity may not change along the chain", func(t *testing.T) {
		broken := second
		broken.GoalId = "goal-2"
		if err := ValidateSpecRevisionCAS(&first, broken); err == nil {
			t.Fatal("CAS accepted a goalId change")
		}
	})
	t.Run("ownership may not change along the chain", func(t *testing.T) {
		broken := second
		broken.AuthorityNamespaceId.AuthorityScopeId = "other-scope"
		if err := ValidateSpecRevisionCAS(&first, broken); err == nil {
			t.Fatal("CAS accepted an ownership change")
		}
	})
}

func TestProposalValidateRejectsSchemaViolations(t *testing.T) {
	specDigest := digestOfLiteral("spec")
	cases := []struct {
		name   string
		mutate func(*GoalPlanProposal)
	}{
		{"zero namespace", func(proposal *GoalPlanProposal) { proposal.AuthorityNamespaceId = authority.AuthorityNamespaceId{} }},
		{"empty proposalId", func(proposal *GoalPlanProposal) { proposal.ProposalId = "" }},
		{"empty goalId", func(proposal *GoalPlanProposal) { proposal.GoalId = "" }},
		{"empty projectId", func(proposal *GoalPlanProposal) { proposal.ProjectId = " " }},
		{"empty repository", func(proposal *GoalPlanProposal) { proposal.Repository = "" }},
		{"zero goalSpecRevision", func(proposal *GoalPlanProposal) { proposal.GoalSpecRevision = 0 }},
		{"malformed goalSpecDigest", func(proposal *GoalPlanProposal) { proposal.GoalSpecDigest = "sha256:zz" }},
		{"negative basedOnPlanRevision", func(proposal *GoalPlanProposal) { proposal.BasedOnPlanRevision = -1 }},
		{"initial plan with basedOnPlanDigest", func(proposal *GoalPlanProposal) { proposal.BasedOnPlanDigest = specDigest }},
		{"replan without basedOnPlanDigest", func(proposal *GoalPlanProposal) { proposal.BasedOnPlanRevision = 2 }},
		{"empty plannerIdentity", func(proposal *GoalPlanProposal) { proposal.PlannerIdentity = "" }},
		{"empty node list", func(proposal *GoalPlanProposal) { proposal.Nodes = nil }},
		{"malformed supersession", func(proposal *GoalPlanProposal) {
			proposal.BasedOnPlanRevision = 1
			proposal.BasedOnPlanDigest = specDigest
			proposal.Supersessions = []NodeSupersession{{NodeId: "n1", PreviousDigest: "sha256:bad", Reason: "r", Lineage: "l"}}
		}},
		{"supersession missing reason", func(proposal *GoalPlanProposal) {
			proposal.BasedOnPlanRevision = 1
			proposal.BasedOnPlanDigest = specDigest
			proposal.Supersessions = []NodeSupersession{{NodeId: "n1", PreviousDigest: specDigest, Reason: "", Lineage: "l"}}
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			proposal := validProposal()
			testCase.mutate(&proposal)
			if err := proposal.Validate(); err == nil {
				t.Fatalf("Validate accepted a proposal with %s", testCase.name)
			}
		})
	}

	proposal := validProposal()
	if err := proposal.Validate(); err != nil {
		t.Fatalf("Validate rejected a valid proposal: %v", err)
	}
	digest, err := proposal.Digest()
	if err != nil || !strings.HasPrefix(digest, authority.DigestPrefix) {
		t.Fatalf("Digest failed for a valid proposal: %q err=%v", digest, err)
	}
}

func TestAcceptedPlanRevisionCASChain(t *testing.T) {
	first := AcceptedGoalPlanRevision{
		AuthorityNamespaceId: testNamespace(),
		GoalId:               "goal-1",
		PlanRevision:         1,
		PreviousPlanDigest:   "",
		ProposalDigest:       digestOfLiteral("proposal"),
		PolicyDigest:         digestOfLiteral("policy"),
		BudgetSnapshotDigest: digestOfLiteral("budget"),
		Nodes:                []GoalNode{validNode("n1")},
		Edges:                []GoalEdge{},
		Supersessions:        []NodeSupersession{},
	}
	if err := first.Validate(); err != nil {
		t.Fatalf("Validate rejected a valid accepted revision: %v", err)
	}
	if err := ValidatePlanRevisionCAS(nil, first); err != nil {
		t.Fatalf("CAS rejected the first accepted revision: %v", err)
	}

	firstDigest, err := first.Digest()
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	second := first
	second.PlanRevision = 2
	second.PreviousPlanDigest = firstDigest
	second.ProposalDigest = digestOfLiteral("proposal-2")
	if err := ValidatePlanRevisionCAS(&first, second); err != nil {
		t.Fatalf("CAS rejected a valid second accepted revision: %v", err)
	}

	t.Run("first revision must not bind a predecessor", func(t *testing.T) {
		broken := first
		broken.PreviousPlanDigest = firstDigest
		if err := ValidatePlanRevisionCAS(nil, broken); err == nil {
			t.Fatal("CAS accepted a first plan revision with a previousPlanDigest")
		}
	})
	t.Run("revision must advance by exactly one", func(t *testing.T) {
		broken := second
		broken.PlanRevision = 4
		if err := ValidatePlanRevisionCAS(&first, broken); err == nil {
			t.Fatal("CAS accepted a skipped plan revision number")
		}
	})
	t.Run("previous digest must bind the current revision", func(t *testing.T) {
		broken := second
		broken.PreviousPlanDigest = digestOfLiteral("forged")
		if err := ValidatePlanRevisionCAS(&first, broken); err == nil {
			t.Fatal("CAS accepted a forged previousPlanDigest")
		}
	})
	t.Run("node list may not be empty", func(t *testing.T) {
		broken := first
		broken.Nodes = nil
		if err := broken.Validate(); err == nil {
			t.Fatal("Validate accepted an accepted revision without nodes")
		}
	})
	t.Run("invalid node content rejected", func(t *testing.T) {
		broken := first
		broken.Nodes = []GoalNode{{NodeId: "n1"}}
		if err := broken.Validate(); err == nil {
			t.Fatal("Validate accepted an accepted revision with an invalid node")
		}
	})
}

func TestBudgetRecordValidate(t *testing.T) {
	record := validBudgetRecord()
	if err := record.Validate(); err != nil {
		t.Fatalf("Validate rejected a valid budget record: %v", err)
	}
	digest, err := record.Digest()
	if err != nil || !strings.HasPrefix(digest, authority.DigestPrefix) {
		t.Fatalf("Digest failed: %q err=%v", digest, err)
	}

	t.Run("zero limit rejected", func(t *testing.T) {
		broken := record
		broken.Limits.MaxNodes = 0
		if err := broken.Validate(); err == nil {
			t.Fatal("Validate accepted a zero maxNodes limit")
		}
	})
	t.Run("negative usage rejected", func(t *testing.T) {
		broken := record
		broken.Used.Tokens = -5
		if err := broken.Validate(); err == nil {
			t.Fatal("Validate accepted negative used tokens")
		}
	})
	t.Run("missing namespace rejected", func(t *testing.T) {
		broken := record
		broken.AuthorityNamespaceId = authority.AuthorityNamespaceId{}
		if err := broken.Validate(); err == nil {
			t.Fatal("Validate accepted a zero ownership namespace")
		}
	})
}

func TestInterventionValidate(t *testing.T) {
	intervention := GoalIntervention{
		AuthorityNamespaceId: testNamespace(),
		InterventionId:       "intervention-1",
		GoalId:               "goal-1",
		Kind:                 InterventionKindPause,
		PauseReason:          PauseReasonOperator,
		PauseMode:            PauseModeDrainActive,
		Actor:                "operator-1",
		Reason:               "manual hold",
		Sequence:             1,
		ExpectedSequence:     0,
		CreatedAt:            "2026-08-14T00:00:00Z",
	}
	if err := intervention.Validate(); err != nil {
		t.Fatalf("Validate rejected a valid pause intervention: %v", err)
	}
	if _, err := intervention.Digest(); err != nil {
		t.Fatalf("Digest rejected a valid pause intervention: %v", err)
	}

	resume := intervention
	resume.Kind = InterventionKindResume
	resume.PauseReason = ""
	resume.PauseMode = ""
	resume.Sequence = 2
	resume.ExpectedSequence = 1
	if err := resume.Validate(); err != nil {
		t.Fatalf("Validate rejected a valid resume intervention: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*GoalIntervention)
	}{
		{"unknown kind", func(in *GoalIntervention) { in.Kind = InterventionKind("restart") }},
		{"pause without reason", func(in *GoalIntervention) { in.PauseReason = "" }},
		{"pause without mode", func(in *GoalIntervention) { in.PauseMode = "" }},
		{"unknown pause reason", func(in *GoalIntervention) { in.PauseReason = PauseReason("coffee") }},
		{"unknown pause mode", func(in *GoalIntervention) { in.PauseMode = PauseMode("stop-all") }},
		{"resume carrying pause reason", func(in *GoalIntervention) { in.Kind = InterventionKindResume }},
		{"empty actor", func(in *GoalIntervention) { in.Actor = "" }},
		{"empty reason", func(in *GoalIntervention) { in.Reason = "" }},
		{"zero sequence", func(in *GoalIntervention) { in.Sequence = 0 }},
		{"broken sequence CAS", func(in *GoalIntervention) { in.ExpectedSequence = 5 }},
		{"malformed timestamp", func(in *GoalIntervention) { in.CreatedAt = "yesterday" }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			broken := intervention
			testCase.mutate(&broken)
			if err := broken.Validate(); err == nil {
				t.Fatalf("Validate accepted an intervention with %s", testCase.name)
			}
		})
	}
}

func TestOutcomeValidate(t *testing.T) {
	outcome := GoalOutcome{
		AuthorityNamespaceId: testNamespace(),
		GoalId:               "goal-1",
		State:                OutcomeStateBlocked,
		Reason:               "budget exhausted",
		FinalPlanDigest:      digestOfLiteral("plan"),
		BudgetDigest:         digestOfLiteral("budget"),
		FinalizedAt:          "2026-08-14T00:00:00Z",
	}
	if err := outcome.Validate(); err != nil {
		t.Fatalf("Validate rejected a valid outcome: %v", err)
	}
	for _, state := range []OutcomeState{OutcomeStateCompleted, OutcomeStateFailed, OutcomeStateBlocked, OutcomeStateAborted} {
		accepted := outcome
		accepted.State = state
		if err := accepted.Validate(); err != nil {
			t.Fatalf("Validate rejected outcome state %s: %v", state, err)
		}
	}
	for _, mutate := range []func(*GoalOutcome){
		func(outcome *GoalOutcome) { outcome.State = OutcomeState("paused") },
		func(outcome *GoalOutcome) { outcome.Reason = "" },
		func(outcome *GoalOutcome) { outcome.FinalPlanDigest = "sha256:bad" },
		func(outcome *GoalOutcome) { outcome.BudgetDigest = "" },
		func(outcome *GoalOutcome) { outcome.FinalizedAt = "not-a-time" },
	} {
		broken := outcome
		mutate(&broken)
		if err := broken.Validate(); err == nil {
			t.Fatalf("Validate accepted outcome %+v", broken)
		}
	}
}

func TestEvidenceDependencySetValidate(t *testing.T) {
	set := EvidenceDependencySet{
		AuthorityNamespaceId:     testNamespace(),
		EvidenceId:               "evidence-1",
		SubjectDigest:            digestOfLiteral("subject"),
		BaseSha:                  "7ba98ad5ed5691012be09017ee488bfb248dfcd9",
		EnvironmentDigest:        digestOfLiteral("environment"),
		PolicyDigest:             digestOfLiteral("policy"),
		VerifierCapabilityDigest: digestOfLiteral("capability"),
		UpstreamArtifactDigests:  []string{digestOfLiteral("artifact-1")},
		ValidUntil:               "2026-12-31T00:00:00Z",
	}
	if err := set.Validate(); err != nil {
		t.Fatalf("Validate rejected a valid dependency set: %v", err)
	}
	digest, err := set.Digest()
	if err != nil || !strings.HasPrefix(digest, authority.DigestPrefix) {
		t.Fatalf("Digest failed: %q err=%v", digest, err)
	}

	withoutOptional := set
	withoutOptional.ValidUntil = ""
	if err := withoutOptional.Validate(); err != nil {
		t.Fatalf("Validate rejected a set without optional validUntil: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*EvidenceDependencySet)
	}{
		{"malformed evidence id", func(set *EvidenceDependencySet) { set.EvidenceId = "bad id" }},
		{"malformed subject digest", func(set *EvidenceDependencySet) { set.SubjectDigest = "md5:abc" }},
		{"empty baseSha", func(set *EvidenceDependencySet) { set.BaseSha = "" }},
		{"malformed capability digest", func(set *EvidenceDependencySet) { set.VerifierCapabilityDigest = "sha256:zz" }},
		{"duplicate upstream digest", func(set *EvidenceDependencySet) {
			digest := digestOfLiteral("artifact-1")
			set.UpstreamArtifactDigests = []string{digest, digest}
		}},
		{"malformed upstream digest", func(set *EvidenceDependencySet) { set.UpstreamArtifactDigests = []string{"sha256:nope"} }},
		{"malformed validUntil", func(set *EvidenceDependencySet) { set.ValidUntil = "soon" }},
		{"missing namespace", func(set *EvidenceDependencySet) { set.AuthorityNamespaceId = authority.AuthorityNamespaceId{} }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			broken := set
			testCase.mutate(&broken)
			if err := broken.Validate(); err == nil {
				t.Fatalf("Validate accepted a dependency set with %s", testCase.name)
			}
		})
	}
}

func TestEstimateArithmetic(t *testing.T) {
	base := NodeEstimate{Runs: 1, Attempts: 1, WallTimeSeconds: 10, ComputeUnits: 2, Tokens: 3, ArtifactBytes: 4}
	sum := base.Add(NodeEstimate{Runs: 2, Attempts: 3, WallTimeSeconds: 4, ComputeUnits: 5, Tokens: 6, ArtifactBytes: 7})
	want := NodeEstimate{Runs: 3, Attempts: 4, WallTimeSeconds: 14, ComputeUnits: 7, Tokens: 9, ArtifactBytes: 11}
	if sum != want {
		t.Fatalf("Add returned %+v, want %+v", sum, want)
	}

	limit := NodeEstimate{Runs: 3, Attempts: 4, WallTimeSeconds: 13, ComputeUnits: 7, Tokens: 9, ArtifactBytes: 11}
	exceeded := sum.Exceeds(limit)
	if len(exceeded) != 1 || exceeded[0] != "maxWallTimeSeconds" {
		t.Fatalf("Exceeds returned %v, want [maxWallTimeSeconds]", exceeded)
	}
	// An estimate equal to the limit on every dimension does not exceed it:
	// the budget comparison is strictly greater. sum equals want (asserted
	// above), so it serves as the exactly-equal limit here.
	if got := want.Exceeds(sum); len(got) != 0 {
		t.Fatalf("Exceeds reported %v for an equal estimate", got)
	}

	usage := BudgetUsage{TotalRuns: 1, Tokens: 5}
	added := usage.AddEstimate(base)
	if added.TotalRuns != 2 || added.Tokens != 8 || added.PlanRevisions != 0 {
		t.Fatalf("AddEstimate returned %+v", added)
	}
	if err := usage.Validate(); err != nil {
		t.Fatalf("Validate rejected valid usage: %v", err)
	}
	negative := usage
	negative.ComputeUnits = -1
	if err := negative.Validate(); err == nil {
		t.Fatal("Validate accepted negative usage")
	}
}
