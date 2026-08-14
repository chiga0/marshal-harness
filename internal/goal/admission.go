package goal

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
)

// AdmissionStep is the closed enumeration of the six admission steps in the
// frozen order of ADR 0019 §4. The order may never be rearranged or
// short-circuited: the first failing step produces the only rejection.
type AdmissionStep string

// Frozen admission steps, evaluated in declaration order.
const (
	AdmissionStepSchemaDigestCAS   AdmissionStep = "step-1-schema-digest-cas"
	AdmissionStepScope             AdmissionStep = "step-2-goal-scope"
	AdmissionStepNodeEdgeIntegrity AdmissionStep = "step-3-node-edge-integrity"
	AdmissionStepAllowlist         AdmissionStep = "step-4-allowlist"
	AdmissionStepGraphStructure    AdmissionStep = "step-5-graph-structure"
	AdmissionStepBudget            AdmissionStep = "step-6-budget"
)

// Validate rejects every value outside the closed enumeration.
func (step AdmissionStep) Validate() error {
	switch step {
	case AdmissionStepSchemaDigestCAS, AdmissionStepScope, AdmissionStepNodeEdgeIntegrity,
		AdmissionStepAllowlist, AdmissionStepGraphStructure, AdmissionStepBudget:
		return nil
	default:
		return fmt.Errorf("goal: unknown admissionStep %q", string(step))
	}
}

// Rejection reasons form a closed vocabulary; every rejection carries exactly
// one step identifier and one reason classification so the audit trail is
// machine-checkable and deterministic.
const (
	// Step 1: schema, canonical digest and revision CAS.
	ReasonCanonicalRejected    = "canonicalization-rejected"
	ReasonSchemaViolation      = "schema-violation"
	ReasonDigestInstability    = "digest-instability"
	ReasonSpecRevisionConflict = "spec-revision-cas-conflict"
	ReasonPlanRevisionConflict = "plan-revision-cas-conflict"

	// Step 2: goal identity, repository/project/authority scope.
	ReasonAuthorityScopeMismatch = "authority-scope-mismatch"
	ReasonGoalIdentityMismatch   = "goal-identity-mismatch"
	ReasonProjectOutOfScope      = "project-out-of-scope"
	ReasonRepositoryOutOfScope   = "repository-out-of-scope"

	// Step 3: node/edge integrity and node identity conflicts.
	ReasonNodeInvalid           = "node-invalid"
	ReasonEdgeInvalid           = "edge-invalid"
	ReasonDuplicateNode         = "duplicate-node"
	ReasonNodeIdentityConflict  = "node-identity-conflict"
	ReasonProtectedNodeModified = "protected-node-modified"
	ReasonProtectedNodeDeleted  = "protected-node-deleted"
	ReasonPendingNodeDropped    = "pending-node-dropped-without-supersession"
	ReasonSupersessionInvalid   = "supersession-invalid"

	// Step 4: executor kind, repository, path and side-effect class allowlists.
	ReasonExecutorKindNotAllowed    = "executor-kind-not-allowed"
	ReasonRepositoryNotAllowed      = "repository-not-allowed"
	ReasonPathNotAllowed            = "path-not-allowed"
	ReasonSideEffectClassNotAllowed = "side-effect-class-not-allowed"

	// Step 5: edge, cycle, depth, fan-out and concurrency guardrails.
	ReasonDanglingEdge          = "dangling-edge"
	ReasonSelfEdge              = "self-edge"
	ReasonDuplicateEdge         = "duplicate-edge"
	ReasonCycle                 = "cycle"
	ReasonMaxNodesExceeded      = "max-nodes-exceeded"
	ReasonMaxDepthExceeded      = "max-depth-exceeded"
	ReasonMaxFanOutExceeded     = "max-fan-out-exceeded"
	ReasonMaxConcurrentExceeded = "max-concurrent-nodes-exceeded"

	// Step 6: cumulative budget availability and estimate.
	ReasonMaxPlanRevisionsExceeded   = "max-plan-revisions-exceeded"
	ReasonBudgetLimitExceeded        = "budget-limit-exceeded"
	ReasonReservationIdentityInvalid = "reservation-identity-invalid"
)

// rejectionReasons is the closed reason vocabulary used by
// AdmissionRejection.Validate.
var rejectionReasons = map[string]struct{}{
	ReasonCanonicalRejected:          {},
	ReasonSchemaViolation:            {},
	ReasonDigestInstability:          {},
	ReasonSpecRevisionConflict:       {},
	ReasonPlanRevisionConflict:       {},
	ReasonAuthorityScopeMismatch:     {},
	ReasonGoalIdentityMismatch:       {},
	ReasonProjectOutOfScope:          {},
	ReasonRepositoryOutOfScope:       {},
	ReasonNodeInvalid:                {},
	ReasonEdgeInvalid:                {},
	ReasonDuplicateNode:              {},
	ReasonNodeIdentityConflict:       {},
	ReasonProtectedNodeModified:      {},
	ReasonProtectedNodeDeleted:       {},
	ReasonPendingNodeDropped:         {},
	ReasonSupersessionInvalid:        {},
	ReasonExecutorKindNotAllowed:     {},
	ReasonRepositoryNotAllowed:       {},
	ReasonPathNotAllowed:             {},
	ReasonSideEffectClassNotAllowed:  {},
	ReasonDanglingEdge:               {},
	ReasonSelfEdge:                   {},
	ReasonDuplicateEdge:              {},
	ReasonCycle:                      {},
	ReasonMaxNodesExceeded:           {},
	ReasonMaxDepthExceeded:           {},
	ReasonMaxFanOutExceeded:          {},
	ReasonMaxConcurrentExceeded:      {},
	ReasonMaxPlanRevisionsExceeded:   {},
	ReasonBudgetLimitExceeded:        {},
	ReasonReservationIdentityInvalid: {},
}

// maxRejectionSubjectBytes bounds the subject field of a rejection record.
const maxRejectionSubjectBytes = 512

// AdmissionRejection is the auditable record produced when any admission
// step fails. It carries the step identifier and the reason classification;
// admission appends it to the audit ledger and never creates execution
// state. The record is deterministic: identical inputs yield byte-identical
// canonical serializations.
type AdmissionRejection struct {
	AuthorityNamespaceId authority.AuthorityNamespaceId `json:"authorityNamespaceId"`
	GoalId               string                         `json:"goalId"`
	ProposalDigest       string                         `json:"proposalDigest"`
	Step                 AdmissionStep                  `json:"step"`
	Reason               string                         `json:"reason"`
	Subject              string                         `json:"subject"`
}

// Validate fails closed on a missing ownership namespace, a malformed goal
// id, a malformed proposal digest binding, an unknown step or reason, or an
// oversized subject.
func (rejection AdmissionRejection) Validate() error {
	if err := rejection.AuthorityNamespaceId.Validate(); err != nil {
		return err
	}
	if err := domain.ValidateID(rejection.GoalId); err != nil {
		return fmt.Errorf("goal: admissionRejection.goalId: %w", err)
	}
	if err := optionalSHA256Digest("admissionRejection.proposalDigest", rejection.ProposalDigest); err != nil {
		return err
	}
	if err := rejection.Step.Validate(); err != nil {
		return err
	}
	if _, known := rejectionReasons[rejection.Reason]; !known {
		return fmt.Errorf("goal: admissionRejection.reason %q is outside the closed vocabulary", rejection.Reason)
	}
	if len(rejection.Subject) > maxRejectionSubjectBytes {
		return fmt.Errorf("goal: admissionRejection.subject exceeds %d bytes", maxRejectionSubjectBytes)
	}
	return nil
}

// Canonical returns the deterministic serialization of the validated record.
func (rejection AdmissionRejection) Canonical() ([]byte, error) {
	if err := rejection.Validate(); err != nil {
		return nil, err
	}
	return canonicalBytes(rejection)
}

// Digest returns the sha256 digest of the canonical serialization.
func (rejection AdmissionRejection) Digest() (string, error) {
	canonicalized, err := rejection.Canonical()
	if err != nil {
		return "", err
	}
	return canonical.DigestBytes(canonicalized), nil
}

// Equal reports whether both rejections carry identical field values.
func (rejection AdmissionRejection) Equal(other AdmissionRejection) bool {
	return rejection == other
}

// AdmissionPolicy is the frozen allowlist configuration consulted by
// admission step 4. Empty lists are rejected at construction: an
// unconfigured allowlist is a misconfiguration, not a deny-all policy.
type AdmissionPolicy struct {
	ExecutorKinds     []ExecutorKind `json:"executorKinds"`
	Repositories      []string       `json:"repositories"`
	Paths             []string       `json:"paths"`
	SideEffectClasses []string       `json:"sideEffectClasses"`
}

// Validate fails closed on any empty list, any blank entry, an unknown
// executor kind, or a path pattern that is not a valid glob.
func (policy AdmissionPolicy) Validate() error {
	if len(policy.ExecutorKinds) == 0 {
		return fmt.Errorf("goal: admissionPolicy.executorKinds must not be empty")
	}
	for index, kind := range policy.ExecutorKinds {
		if err := kind.Validate(); err != nil {
			return fmt.Errorf("goal: admissionPolicy.executorKinds[%d]: %w", index, err)
		}
	}
	if len(policy.Repositories) == 0 {
		return fmt.Errorf("goal: admissionPolicy.repositories must not be empty")
	}
	for index, repository := range policy.Repositories {
		if err := requireText(fmt.Sprintf("admissionPolicy.repositories[%d]", index), repository); err != nil {
			return err
		}
	}
	if len(policy.Paths) == 0 {
		return fmt.Errorf("goal: admissionPolicy.paths must not be empty")
	}
	for index, pattern := range policy.Paths {
		field := fmt.Sprintf("admissionPolicy.paths[%d]", index)
		if err := requireText(field, pattern); err != nil {
			return err
		}
		if _, err := doublestar.Match(pattern, "probe"); err != nil {
			return fmt.Errorf("goal: %s is not a valid glob: %w", field, err)
		}
	}
	if len(policy.SideEffectClasses) == 0 {
		return fmt.Errorf("goal: admissionPolicy.sideEffectClasses must not be empty")
	}
	for index, class := range policy.SideEffectClasses {
		if err := requireText(fmt.Sprintf("admissionPolicy.sideEffectClasses[%d]", index), class); err != nil {
			return err
		}
	}
	return nil
}

// Digest returns the canonical digest of the validated policy; accepted plan
// revisions bind it so the exact allowlist configuration is auditable.
func (policy AdmissionPolicy) Digest() (string, error) {
	if err := policy.Validate(); err != nil {
		return "", err
	}
	return canonicalDigestOf(policy)
}

func (policy AdmissionPolicy) normalized() AdmissionPolicy {
	policy.ExecutorKinds = nonNilKinds(policy.ExecutorKinds)
	policy.Repositories = nonNilStrings(policy.Repositories)
	policy.Paths = nonNilStrings(policy.Paths)
	policy.SideEffectClasses = nonNilStrings(policy.SideEffectClasses)
	return policy
}

func nonNilKinds(values []ExecutorKind) []ExecutorKind {
	if values == nil {
		return []ExecutorKind{}
	}
	return values
}

// AuthorityState is the current authority snapshot admission evaluates
// against: the owning namespace, the Goal identity and scope, the frozen
// spec revision, the latest accepted plan revision (nil before the first
// plan), the cumulative budget ledger and the execution states of the nodes
// of the current plan. Absent node states resolve to pending.
type AuthorityState struct {
	AuthorityNamespaceId authority.AuthorityNamespaceId
	GoalId               string
	ProjectId            string
	Repository           string
	SpecRevision         *GoalSpecRevision
	PlanRevision         *AcceptedGoalPlanRevision
	Budget               GoalBudgetLedger
	NodeStates           map[string]NodeState
}

func (state AuthorityState) validate() error {
	if err := state.AuthorityNamespaceId.Validate(); err != nil {
		return err
	}
	if err := domain.ValidateID(state.GoalId); err != nil {
		return fmt.Errorf("goal: authorityState.goalId: %w", err)
	}
	if err := requireText("authorityState.projectId", state.ProjectId); err != nil {
		return err
	}
	if err := requireText("authorityState.repository", state.Repository); err != nil {
		return err
	}
	if state.SpecRevision == nil {
		return fmt.Errorf("goal: authorityState must carry the current goalSpecRevision")
	}
	if err := state.SpecRevision.Validate(); err != nil {
		return fmt.Errorf("goal: authorityState.specRevision: %w", err)
	}
	if !state.SpecRevision.AuthorityNamespaceId.Equal(state.AuthorityNamespaceId) || state.SpecRevision.GoalId != state.GoalId {
		return fmt.Errorf("goal: authorityState.specRevision belongs to a different goal or namespace")
	}
	if state.PlanRevision != nil {
		if err := state.PlanRevision.Validate(); err != nil {
			return fmt.Errorf("goal: authorityState.planRevision: %w", err)
		}
		if !state.PlanRevision.AuthorityNamespaceId.Equal(state.AuthorityNamespaceId) || state.PlanRevision.GoalId != state.GoalId {
			return fmt.Errorf("goal: authorityState.planRevision belongs to a different goal or namespace")
		}
	}
	if err := state.Budget.Validate(); err != nil {
		return fmt.Errorf("goal: authorityState.budget: %w", err)
	}
	for nodeID, nodeState := range state.NodeStates {
		if nodeState == "" {
			continue
		}
		if err := nodeState.Validate(); err != nil {
			return fmt.Errorf("goal: authorityState.nodeStates[%s]: %w", nodeID, err)
		}
	}
	return nil
}

func (state AuthorityState) nodeState(nodeID string) NodeState {
	if nodeState, ok := state.NodeStates[nodeID]; ok && nodeState != "" {
		return nodeState
	}
	return NodeStatePending
}

// AdmissionDecision is the deterministic outcome of one evaluation: either
// the accepted revision together with its reservation plan, or exactly one
// auditable rejection. Step 6 computes the reservation plan without
// persisting any live reservation.
type AdmissionDecision struct {
	Accepted        bool
	Rejection       *AdmissionRejection
	Revision        *AcceptedGoalPlanRevision
	ReservationPlan []ReservationRequest
}

// Evaluate runs the six admission steps in the frozen order against the raw
// proposal bytes and returns the deterministic decision. It is a pure
// function: identical inputs always produce the identical decision,
// including the rejection step and reason. It never mutates state, never
// creates execution state and never persists a live reservation.
func Evaluate(rawProposal []byte, state AuthorityState, policy AdmissionPolicy) AdmissionDecision {
	if err := state.validate(); err != nil {
		return rejectedDecision(rejection(state, "", AdmissionStepSchemaDigestCAS, ReasonSchemaViolation, "authority-state"))
	}
	if err := policy.Validate(); err != nil {
		return rejectedDecision(rejection(state, "", AdmissionStepSchemaDigestCAS, ReasonSchemaViolation, "admission-policy"))
	}
	policyDigest, err := policy.Digest()
	if err != nil {
		return rejectedDecision(rejection(state, "", AdmissionStepSchemaDigestCAS, ReasonSchemaViolation, "admission-policy"))
	}
	budgetDigest, err := state.Budget.Digest()
	if err != nil {
		return rejectedDecision(rejection(state, "", AdmissionStepSchemaDigestCAS, ReasonSchemaViolation, "authority-state"))
	}

	// Step 1: schema, canonical digest and revision CAS.
	proposal, proposalDigest, stepRejection := step1SchemaDigestCAS(rawProposal, state)
	if stepRejection != nil {
		return rejectedDecision(stepRejection)
	}

	// Step 2: goal identity, repository/project/authority scope.
	if stepRejection := step2Scope(proposal, state); stepRejection != nil {
		return rejectedDecision(stepRejection)
	}

	// Step 3: node/edge integrity and node identity conflicts; produces the
	// effective graph for a replan overlay.
	effectiveNodes, superseded, stepRejection := step3NodeEdgeIntegrity(proposal, state)
	if stepRejection != nil {
		return rejectedDecision(stepRejection)
	}

	// Step 4: executor kind, repository, path and side-effect class allowlists.
	if stepRejection := step4Allowlist(effectiveNodes, policy, state, proposalDigest); stepRejection != nil {
		return rejectedDecision(stepRejection)
	}

	// Step 5: dangling/self/duplicate edge, cycle, depth, fan-out and
	// concurrency guardrails.
	if stepRejection := step5GraphStructure(effectiveNodes, proposal.Edges, state.Budget.Limits, state, proposalDigest); stepRejection != nil {
		return rejectedDecision(stepRejection)
	}

	// Step 6: cumulative budget availability and estimate; computes the
	// reservation plan without persisting any live reservation.
	return step6Budget(proposal, effectiveNodes, superseded, state, proposalDigest, policyDigest, budgetDigest)
}

func rejection(state AuthorityState, proposalDigest string, step AdmissionStep, reason, subject string) *AdmissionRejection {
	return &AdmissionRejection{
		AuthorityNamespaceId: state.AuthorityNamespaceId,
		GoalId:               state.GoalId,
		ProposalDigest:       proposalDigest,
		Step:                 step,
		Reason:               reason,
		Subject:              subject,
	}
}

func rejectedDecision(rejection *AdmissionRejection) AdmissionDecision {
	return AdmissionDecision{Accepted: false, Rejection: rejection}
}

// step1SchemaDigestCAS canonicalizes the raw submission, decodes it,
// validates the proposal schema, verifies the canonical round-trip digest
// and checks the spec and plan revision CAS bindings against the authority
// state.
func step1SchemaDigestCAS(rawProposal []byte, state AuthorityState) (GoalPlanProposal, string, *AdmissionRejection) {
	canonicalized, err := canonical.JSON(rawProposal)
	if err != nil {
		return GoalPlanProposal{}, "", rejection(state, "", AdmissionStepSchemaDigestCAS, ReasonCanonicalRejected, "")
	}
	proposalDigest := canonical.DigestBytes(canonicalized)

	var proposal GoalPlanProposal
	if err := json.Unmarshal(canonicalized, &proposal); err != nil {
		return GoalPlanProposal{}, "", rejection(state, proposalDigest, AdmissionStepSchemaDigestCAS, ReasonSchemaViolation, "payload")
	}
	if err := proposal.Validate(); err != nil {
		return GoalPlanProposal{}, "", rejection(state, proposalDigest, AdmissionStepSchemaDigestCAS, ReasonSchemaViolation, "payload")
	}
	roundTrip, err := canonicalDigestOf(proposal.normalized())
	if err != nil || roundTrip != proposalDigest {
		return GoalPlanProposal{}, "", rejection(state, proposalDigest, AdmissionStepSchemaDigestCAS, ReasonDigestInstability, "payload")
	}

	// Spec revision CAS: the proposal must pin exactly the current spec
	// revision of the Goal.
	specDigest, err := state.SpecRevision.Digest()
	if err != nil || proposal.GoalSpecRevision != state.SpecRevision.Revision || proposal.GoalSpecDigest != specDigest {
		return GoalPlanProposal{}, "", rejection(state, proposalDigest, AdmissionStepSchemaDigestCAS, ReasonSpecRevisionConflict, "goalSpecRevision")
	}

	// Plan revision CAS: an initial plan must not claim a base revision and
	// a replan must pin exactly the current accepted revision.
	if state.PlanRevision == nil {
		if proposal.BasedOnPlanRevision != 0 || proposal.BasedOnPlanDigest != "" {
			return GoalPlanProposal{}, "", rejection(state, proposalDigest, AdmissionStepSchemaDigestCAS, ReasonPlanRevisionConflict, "basedOnPlanRevision")
		}
	} else {
		baseDigest, err := state.PlanRevision.Digest()
		if err != nil || proposal.BasedOnPlanRevision != state.PlanRevision.PlanRevision || proposal.BasedOnPlanDigest != baseDigest {
			return GoalPlanProposal{}, "", rejection(state, proposalDigest, AdmissionStepSchemaDigestCAS, ReasonPlanRevisionConflict, "basedOnPlanRevision")
		}
	}
	return proposal, proposalDigest, nil
}

// step2Scope verifies the proposal targets exactly the owning authority
// namespace, Goal, project and repository of the authority state.
func step2Scope(proposal GoalPlanProposal, state AuthorityState) *AdmissionRejection {
	proposalDigest := proposalDigestOrEmpty(proposal)
	if !proposal.AuthorityNamespaceId.Equal(state.AuthorityNamespaceId) {
		return rejection(state, proposalDigest, AdmissionStepScope, ReasonAuthorityScopeMismatch, "authorityNamespaceId")
	}
	if proposal.GoalId != state.GoalId {
		return rejection(state, proposalDigest, AdmissionStepScope, ReasonGoalIdentityMismatch, "goalId")
	}
	if proposal.ProjectId != state.ProjectId {
		return rejection(state, proposalDigest, AdmissionStepScope, ReasonProjectOutOfScope, "projectId")
	}
	if proposal.Repository != state.Repository {
		return rejection(state, proposalDigest, AdmissionStepScope, ReasonRepositoryOutOfScope, "repository")
	}
	return nil
}

func proposalDigestOrEmpty(proposal GoalPlanProposal) string {
	digest, err := proposal.Digest()
	if err != nil {
		return ""
	}
	return digest
}

// step3NodeEdgeIntegrity validates every node and edge, rejects duplicate
// node identities, and overlays a replan proposal onto the base revision:
// protected nodes may not be deleted or redefined, pending nodes may only
// change through a valid recorded supersession, and the same node identity
// carrying a different digest fails closed. It returns the effective node
// list and the consumed supersessions in deterministic order.
func step3NodeEdgeIntegrity(proposal GoalPlanProposal, state AuthorityState) ([]GoalNode, []NodeSupersession, *AdmissionRejection) {
	proposalDigest := proposalDigestOrEmpty(proposal)
	reject := func(reason, subject string) ([]GoalNode, []NodeSupersession, *AdmissionRejection) {
		return nil, nil, rejection(state, proposalDigest, AdmissionStepNodeEdgeIntegrity, reason, subject)
	}

	for _, node := range proposal.Nodes {
		if err := node.Validate(); err != nil {
			return reject(ReasonNodeInvalid, node.NodeId)
		}
	}
	for _, edge := range proposal.Edges {
		if err := edge.Validate(); err != nil {
			return reject(ReasonEdgeInvalid, edgeSubject(edge))
		}
	}

	proposedIndex := make(map[string]int, len(proposal.Nodes))
	for index, node := range proposal.Nodes {
		if _, duplicate := proposedIndex[node.NodeId]; duplicate {
			return reject(ReasonDuplicateNode, node.NodeId)
		}
		proposedIndex[node.NodeId] = index
	}

	base := state.PlanRevision
	if base == nil {
		if len(proposal.Supersessions) > 0 {
			return reject(ReasonSupersessionInvalid, proposal.Supersessions[0].NodeId)
		}
		return proposal.Nodes, []NodeSupersession{}, nil
	}

	supersessionByNode := make(map[string]NodeSupersession, len(proposal.Supersessions))
	for _, supersession := range proposal.Supersessions {
		if _, duplicate := supersessionByNode[supersession.NodeId]; duplicate {
			return reject(ReasonSupersessionInvalid, supersession.NodeId)
		}
		supersessionByNode[supersession.NodeId] = supersession
	}

	baseIds := make(map[string]struct{}, len(base.Nodes))
	effective := make([]GoalNode, 0, len(base.Nodes)+len(proposal.Nodes))
	consumed := make(map[string]struct{}, len(proposal.Supersessions))
	var superseded []NodeSupersession

	for _, baseNode := range base.Nodes {
		baseIds[baseNode.NodeId] = struct{}{}
		nodeState := state.nodeState(baseNode.NodeId)
		baseDigest, err := baseNode.Digest()
		if err != nil {
			return reject(ReasonNodeInvalid, baseNode.NodeId)
		}
		proposed, present := proposedIndex[baseNode.NodeId]
		if !present {
			if nodeState.Protected() {
				return reject(ReasonProtectedNodeDeleted, baseNode.NodeId)
			}
			supersession, ok := supersessionByNode[baseNode.NodeId]
			if !ok {
				return reject(ReasonPendingNodeDropped, baseNode.NodeId)
			}
			if supersession.PreviousDigest != baseDigest {
				return reject(ReasonSupersessionInvalid, baseNode.NodeId)
			}
			consumed[baseNode.NodeId] = struct{}{}
			superseded = append(superseded, supersession)
			continue
		}
		proposedNode := proposal.Nodes[proposed]
		proposedDigest, err := proposedNode.Digest()
		if err != nil {
			return reject(ReasonNodeInvalid, baseNode.NodeId)
		}
		if proposedDigest == baseDigest {
			effective = append(effective, baseNode)
			continue
		}
		if nodeState.Protected() {
			return reject(ReasonProtectedNodeModified, baseNode.NodeId)
		}
		supersession, ok := supersessionByNode[baseNode.NodeId]
		if !ok {
			// Same node identity with a different digest and no authorized
			// supersession fails closed.
			return reject(ReasonNodeIdentityConflict, baseNode.NodeId)
		}
		if supersession.PreviousDigest != baseDigest {
			return reject(ReasonSupersessionInvalid, baseNode.NodeId)
		}
		consumed[baseNode.NodeId] = struct{}{}
		superseded = append(superseded, supersession)
		effective = append(effective, proposedNode)
	}

	for _, supersession := range proposal.Supersessions {
		if _, ok := consumed[supersession.NodeId]; !ok {
			return reject(ReasonSupersessionInvalid, supersession.NodeId)
		}
	}

	for _, node := range proposal.Nodes {
		if _, known := baseIds[node.NodeId]; !known {
			effective = append(effective, node)
		}
	}
	if superseded == nil {
		superseded = []NodeSupersession{}
	}
	return effective, superseded, nil
}

func edgeSubject(edge GoalEdge) string {
	subject := edge.From + "->" + edge.To
	if len(subject) > maxRejectionSubjectBytes {
		subject = subject[:maxRejectionSubjectBytes]
	}
	return subject
}

// step4Allowlist enforces the executor kind, repository, path and
// side-effect class allowlists against the effective node list.
func step4Allowlist(nodes []GoalNode, policy AdmissionPolicy, state AuthorityState, proposalDigest string) *AdmissionRejection {
	for _, node := range nodes {
		if !containsKind(policy.ExecutorKinds, node.ExecutorKind) {
			return rejection(state, proposalDigest, AdmissionStepAllowlist, ReasonExecutorKindNotAllowed, node.NodeId)
		}
		if !containsString(policy.Repositories, node.Repository) {
			return rejection(state, proposalDigest, AdmissionStepAllowlist, ReasonRepositoryNotAllowed, node.NodeId)
		}
		for _, path := range node.Paths {
			if !matchesAnyGlob(policy.Paths, path) {
				return rejection(state, proposalDigest, AdmissionStepAllowlist, ReasonPathNotAllowed, node.NodeId)
			}
		}
		for _, class := range node.SideEffectClasses {
			if !containsString(policy.SideEffectClasses, class) {
				return rejection(state, proposalDigest, AdmissionStepAllowlist, ReasonSideEffectClassNotAllowed, node.NodeId)
			}
		}
	}
	return nil
}

func containsKind(kinds []ExecutorKind, kind ExecutorKind) bool {
	for _, entry := range kinds {
		if entry == kind {
			return true
		}
	}
	return false
}

func containsString(values []string, value string) bool {
	for _, entry := range values {
		if entry == value {
			return true
		}
	}
	return false
}

func matchesAnyGlob(patterns []string, path string) bool {
	for _, pattern := range patterns {
		matched, err := doublestar.Match(pattern, path)
		if err == nil && matched {
			return true
		}
	}
	return false
}

// step5GraphStructure enforces the DAG guardrails in the fixed within-step
// order: dangling/self/duplicate edges, cycles, maxNodes, maxDepth,
// maxFanOut and maxConcurrentNodes. Depth is the node count of the longest
// path; concurrency is the maximum level width of the longest-path
// layering, a deterministic conservative bound on simultaneously runnable
// nodes.
func step5GraphStructure(nodes []GoalNode, edges []GoalEdge, limits Guardrails, state AuthorityState, proposalDigest string) *AdmissionRejection {
	reject := func(reason, subject string) *AdmissionRejection {
		return rejection(state, proposalDigest, AdmissionStepGraphStructure, reason, subject)
	}

	nodeIds := make(map[string]struct{}, len(nodes))
	nodeOrder := make([]string, 0, len(nodes))
	for _, node := range nodes {
		nodeIds[node.NodeId] = struct{}{}
		nodeOrder = append(nodeOrder, node.NodeId)
	}

	type edgeKey struct{ from, to string }
	seenEdges := make(map[edgeKey]struct{}, len(edges))
	for _, edge := range edges {
		subject := edgeSubject(edge)
		if _, ok := nodeIds[edge.From]; !ok {
			return reject(ReasonDanglingEdge, subject)
		}
		if _, ok := nodeIds[edge.To]; !ok {
			return reject(ReasonDanglingEdge, subject)
		}
		if edge.From == edge.To {
			return reject(ReasonSelfEdge, subject)
		}
		key := edgeKey{from: edge.From, to: edge.To}
		if _, duplicate := seenEdges[key]; duplicate {
			return reject(ReasonDuplicateEdge, subject)
		}
		seenEdges[key] = struct{}{}
	}

	indegree := make(map[string]int, len(nodes))
	successors := make(map[string][]string, len(edges))
	for _, edge := range edges {
		indegree[edge.To]++
		successors[edge.From] = append(successors[edge.From], edge.To)
	}
	queue := make([]string, 0, len(nodes))
	for _, nodeID := range nodeOrder {
		if indegree[nodeID] == 0 {
			queue = append(queue, nodeID)
		}
	}
	level := make(map[string]int64, len(nodes))
	for _, nodeID := range queue {
		level[nodeID] = 1
	}
	width := make(map[int64]int64)
	var depth int64
	processed := 0
	for head := 0; head < len(queue); head++ {
		nodeID := queue[head]
		processed++
		width[level[nodeID]]++
		if level[nodeID] > depth {
			depth = level[nodeID]
		}
		for _, successor := range successors[nodeID] {
			if level[successor] < level[nodeID]+1 {
				level[successor] = level[nodeID] + 1
			}
			indegree[successor]--
			if indegree[successor] == 0 {
				queue = append(queue, successor)
			}
		}
	}
	if processed < len(nodes) {
		for _, nodeID := range nodeOrder {
			if indegree[nodeID] > 0 {
				return reject(ReasonCycle, nodeID)
			}
		}
		return reject(ReasonCycle, "")
	}

	if int64(len(nodes)) > limits.MaxNodes {
		return reject(ReasonMaxNodesExceeded, "")
	}
	if depth > limits.MaxDepth {
		return reject(ReasonMaxDepthExceeded, "")
	}
	for _, nodeID := range nodeOrder {
		if int64(len(successors[nodeID])) > limits.MaxFanOut {
			return reject(ReasonMaxFanOutExceeded, nodeID)
		}
	}
	var concurrent int64
	for _, levelWidth := range width {
		if levelWidth > concurrent {
			concurrent = levelWidth
		}
	}
	if concurrent > limits.MaxConcurrentNodes {
		return reject(ReasonMaxConcurrentExceeded, "")
	}
	return nil
}

// step6Budget enforces the cumulative Goal budget: the plan revision count
// and the used-plus-estimated totals must all stay within the frozen
// guardrails. On success it builds the accepted revision bound to the
// proposal, policy and budget snapshot digests, and the deterministic
// reservation plan — without persisting any live reservation.
func step6Budget(proposal GoalPlanProposal, nodes []GoalNode, superseded []NodeSupersession, state AuthorityState, proposalDigest, policyDigest, budgetDigest string) AdmissionDecision {
	reject := func(reason, subject string) AdmissionDecision {
		return rejectedDecision(rejection(state, proposalDigest, AdmissionStepBudget, reason, subject))
	}
	limits := state.Budget.Limits
	used := state.Budget.Used

	if used.PlanRevisions+1 > limits.MaxPlanRevisions {
		return reject(ReasonMaxPlanRevisionsExceeded, "maxPlanRevisions")
	}

	total := NodeEstimate{}
	for _, node := range nodes {
		total = total.Add(node.Estimate)
	}
	if used.TotalRuns+total.Runs > limits.MaxTotalRuns {
		return reject(ReasonBudgetLimitExceeded, "maxTotalRuns")
	}
	if used.TotalAttempts+total.Attempts > limits.MaxTotalAttempts {
		return reject(ReasonBudgetLimitExceeded, "maxTotalAttempts")
	}
	if used.WallTimeSeconds+total.WallTimeSeconds > limits.MaxWallTimeSeconds {
		return reject(ReasonBudgetLimitExceeded, "maxWallTimeSeconds")
	}
	if used.ComputeUnits+total.ComputeUnits > limits.MaxComputeUnits {
		return reject(ReasonBudgetLimitExceeded, "maxComputeUnits")
	}
	if used.Tokens+total.Tokens > limits.MaxTokens {
		return reject(ReasonBudgetLimitExceeded, "maxTokens")
	}
	if used.ArtifactBytes+total.ArtifactBytes > limits.MaxArtifactBytes {
		return reject(ReasonBudgetLimitExceeded, "maxArtifactBytes")
	}

	planRevision := int64(1)
	previousPlanDigest := ""
	if state.PlanRevision != nil {
		planRevision = state.PlanRevision.PlanRevision + 1
		baseDigest, err := state.PlanRevision.Digest()
		if err != nil {
			return reject(ReasonSchemaViolation, "authority-state")
		}
		previousPlanDigest = baseDigest
	}

	revision := AcceptedGoalPlanRevision{
		AuthorityNamespaceId: state.AuthorityNamespaceId,
		GoalId:               state.GoalId,
		PlanRevision:         planRevision,
		PreviousPlanDigest:   previousPlanDigest,
		ProposalDigest:       proposalDigest,
		PolicyDigest:         policyDigest,
		BudgetSnapshotDigest: budgetDigest,
		Nodes:                nodes,
		Edges:                proposal.Edges,
		Supersessions:        superseded,
	}

	reservations := make([]ReservationRequest, 0, len(nodes))
	for _, node := range nodes {
		request := reservationForNode(state.GoalId, planRevision, node)
		if err := request.Validate(); err != nil {
			return reject(ReasonReservationIdentityInvalid, node.NodeId)
		}
		reservations = append(reservations, request)
	}

	return AdmissionDecision{
		Accepted:        true,
		Revision:        &revision,
		ReservationPlan: reservations,
	}
}

// reservationForNode derives the deterministic reservation identity for one
// effective node. The readable identity is used when it stays within the
// Marshal ID bounds; otherwise a digest-derived identity of identical
// uniqueness is substituted. No clock or random source participates.
func reservationForNode(goalID string, planRevision int64, node GoalNode) ReservationRequest {
	request := ReservationRequest{
		ReservationId:  fmt.Sprintf("reservation:%s:plan-%d:%s", goalID, planRevision, node.NodeId),
		IdempotencyKey: fmt.Sprintf("reserve:%s:plan-%d:%s", goalID, planRevision, node.NodeId),
		GoalId:         goalID,
		NodeId:         node.NodeId,
		PlanRevision:   planRevision,
		CommandId:      fmt.Sprintf("materialize:%s:plan-%d:%s", goalID, planRevision, node.NodeId),
		Estimate:       node.Estimate,
	}
	if request.Validate() == nil {
		return request
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|plan-%d|%s", goalID, planRevision, node.NodeId)))
	encoded := hex.EncodeToString(sum[:])[:40]
	return ReservationRequest{
		ReservationId:  "reservation:" + encoded,
		IdempotencyKey: "reserve:" + encoded,
		GoalId:         goalID,
		NodeId:         node.NodeId,
		PlanRevision:   planRevision,
		CommandId:      "materialize:" + encoded,
		Estimate:       node.Estimate,
	}
}

// AdmissionEvent is one append-only audit entry recording the outcome of a
// single admission evaluation.
type AdmissionEvent struct {
	Sequence               int64
	Accepted               bool
	ProposalDigest         string
	AcceptedRevisionDigest string
	Rejection              *AdmissionRejection
}

// AdmissionAudit is the in-memory append-only ledger of admission decisions.
// It never mutates an appended entry and never discards one; this lineage
// does not freeze a disk contract in the current phase.
type AdmissionAudit struct {
	mu      sync.Mutex
	entries []AdmissionEvent
}

// NewAdmissionAudit returns an empty audit ledger.
func NewAdmissionAudit() *AdmissionAudit {
	return &AdmissionAudit{}
}

// Entries returns a copy of every appended event in append order.
func (audit *AdmissionAudit) Entries() []AdmissionEvent {
	audit.mu.Lock()
	defer audit.mu.Unlock()
	entries := make([]AdmissionEvent, len(audit.entries))
	copy(entries, audit.entries)
	return entries
}

// Rejections returns every rejection record in append order.
func (audit *AdmissionAudit) Rejections() []AdmissionRejection {
	audit.mu.Lock()
	defer audit.mu.Unlock()
	var rejections []AdmissionRejection
	for _, entry := range audit.entries {
		if entry.Rejection != nil {
			rejections = append(rejections, *entry.Rejection)
		}
	}
	return rejections
}

func (audit *AdmissionAudit) append(decision AdmissionDecision) AdmissionEvent {
	audit.mu.Lock()
	defer audit.mu.Unlock()
	event := AdmissionEvent{
		Sequence: int64(len(audit.entries)) + 1,
		Accepted: decision.Accepted,
	}
	if decision.Accepted {
		event.ProposalDigest = decision.Revision.ProposalDigest
		if digest, err := decision.Revision.Digest(); err == nil {
			event.AcceptedRevisionDigest = digest
		}
	} else {
		event.ProposalDigest = decision.Rejection.ProposalDigest
		event.Rejection = decision.Rejection
	}
	audit.entries = append(audit.entries, event)
	return event
}

// Evaluator binds one frozen admission policy to one audit ledger and admits
// raw proposals against successive authority states.
type Evaluator struct {
	policy AdmissionPolicy
	audit  *AdmissionAudit
}

// NewEvaluator validates the frozen policy and binds it to the audit
// ledger; a nil ledger receives a fresh one.
func NewEvaluator(policy AdmissionPolicy, audit *AdmissionAudit) (*Evaluator, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	if audit == nil {
		audit = NewAdmissionAudit()
	}
	return &Evaluator{policy: policy.normalized(), audit: audit}, nil
}

// Admit evaluates the raw proposal in the frozen step order against state
// and appends the decision to the audit ledger. The returned decision is
// exactly the decision Evaluate produces; the ledger is audit-only and never
// influences the verdict.
func (evaluator *Evaluator) Admit(rawProposal []byte, state AuthorityState) AdmissionDecision {
	decision := Evaluate(rawProposal, state, evaluator.policy)
	evaluator.audit.append(decision)
	return decision
}

// Audit exposes the bound append-only audit ledger.
func (evaluator *Evaluator) Audit() *AdmissionAudit {
	return evaluator.audit
}

// sortReservationRequests returns requests ordered by reservationId for
// deterministic comparison and reporting.
func sortReservationRequests(requests []ReservationRequest) []ReservationRequest {
	sorted := make([]ReservationRequest, len(requests))
	copy(sorted, requests)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].ReservationId < sorted[j].ReservationId
	})
	return sorted
}
