package goal

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
)

// ExecutorKind is the closed enumeration of Goal node executor kinds, the
// typed-execution workload classes a plan node may materialize (ADR 0019 §2).
// Plan work itself is the Planner proposal that precedes admission and is
// never a node executor. Matching is case sensitive.
type ExecutorKind string

// Closed executor kinds of a GoalNode.
const (
	ExecutorKindImplement ExecutorKind = "implement"
	ExecutorKindVerify    ExecutorKind = "verify"
	ExecutorKindReview    ExecutorKind = "review"
	ExecutorKindPublish   ExecutorKind = "publish"
)

// Validate rejects every value outside the closed enumeration.
func (kind ExecutorKind) Validate() error {
	switch kind {
	case ExecutorKindImplement, ExecutorKindVerify, ExecutorKindReview, ExecutorKindPublish:
		return nil
	default:
		return fmt.Errorf("goal: unknown executorKind %q", string(kind))
	}
}

// NodeState is the closed enumeration of execution states a node of the
// current accepted plan revision may carry. The zero value is not a state:
// callers resolve absent entries to NodeStatePending before admission.
type NodeState string

// Closed execution states of a Goal node.
const (
	NodeStatePending   NodeState = "pending"
	NodeStateRunning   NodeState = "running"
	NodeStateCompleted NodeState = "completed"
)

// Validate rejects every value outside the closed enumeration.
func (state NodeState) Validate() error {
	switch state {
	case NodeStatePending, NodeStateRunning, NodeStateCompleted:
		return nil
	default:
		return fmt.Errorf("goal: unknown nodeState %q", string(state))
	}
}

// Protected reports whether replanning may delete or redefine the node:
// completed and running nodes are immutable, pending nodes may be superseded
// with recorded reason and lineage (ADR 0019 §5).
func (state NodeState) Protected() bool {
	return state == NodeStateRunning || state == NodeStateCompleted
}

// EdgeKindDependsOn is the single frozen GoalEdge kind: the edge points from
// the producer node to the dependent consumer node, which may start only
// after the producer completes.
const EdgeKindDependsOn = "depends-on"

// ValidateEdgeKind rejects every edge kind outside the closed vocabulary.
func ValidateEdgeKind(kind string) error {
	if kind != EdgeKindDependsOn {
		return fmt.Errorf("goal: unknown edge kind %q", kind)
	}
	return nil
}

// NodeEstimate is the planner-declared per-node budget estimate. Every field
// is cumulative material the admission budget step adds to the Goal-wide
// usage; runs and attempts must be positive because every node materializes
// at least one bounded Run.
type NodeEstimate struct {
	Runs            int64 `json:"runs"`
	Attempts        int64 `json:"attempts"`
	WallTimeSeconds int64 `json:"wallTimeSeconds"`
	ComputeUnits    int64 `json:"computeUnits"`
	Tokens          int64 `json:"tokens"`
	ArtifactBytes   int64 `json:"artifactBytes"`
}

// Validate fails closed on any negative field and on non-positive runs or
// attempts.
func (estimate NodeEstimate) Validate() error {
	if estimate.Runs < 1 {
		return fmt.Errorf("goal: estimate.runs must be at least 1")
	}
	if estimate.Attempts < 1 {
		return fmt.Errorf("goal: estimate.attempts must be at least 1")
	}
	for _, field := range []struct {
		name  string
		value int64
	}{
		{"estimate.wallTimeSeconds", estimate.WallTimeSeconds},
		{"estimate.computeUnits", estimate.ComputeUnits},
		{"estimate.tokens", estimate.Tokens},
		{"estimate.artifactBytes", estimate.ArtifactBytes},
	} {
		if field.value < 0 {
			return fmt.Errorf("goal: %s must not be negative", field.name)
		}
	}
	return nil
}

// Add returns the component-wise sum of both estimates.
func (estimate NodeEstimate) Add(other NodeEstimate) NodeEstimate {
	return NodeEstimate{
		Runs:            estimate.Runs + other.Runs,
		Attempts:        estimate.Attempts + other.Attempts,
		WallTimeSeconds: estimate.WallTimeSeconds + other.WallTimeSeconds,
		ComputeUnits:    estimate.ComputeUnits + other.ComputeUnits,
		Tokens:          estimate.Tokens + other.Tokens,
		ArtifactBytes:   estimate.ArtifactBytes + other.ArtifactBytes,
	}
}

// Exceeds reports the fixed-order list of budget dimension field names on
// which the estimate strictly exceeds limit.
func (estimate NodeEstimate) Exceeds(limit NodeEstimate) []string {
	var exceeded []string
	for _, dimension := range []struct {
		name  string
		value int64
		bound int64
	}{
		{"maxTotalRuns", estimate.Runs, limit.Runs},
		{"maxTotalAttempts", estimate.Attempts, limit.Attempts},
		{"maxWallTimeSeconds", estimate.WallTimeSeconds, limit.WallTimeSeconds},
		{"maxComputeUnits", estimate.ComputeUnits, limit.ComputeUnits},
		{"maxTokens", estimate.Tokens, limit.Tokens},
		{"maxArtifactBytes", estimate.ArtifactBytes, limit.ArtifactBytes},
	} {
		if dimension.value > dimension.bound {
			exceeded = append(exceeded, dimension.name)
		}
	}
	return exceeded
}

// BudgetUsage is the cumulative Goal-wide consumption recorded by the budget
// ledger. Every field is non-negative; usage only ever grows through settled
// actuals and accepted plan revisions, never through negative bookkeeping.
type BudgetUsage struct {
	PlanRevisions   int64 `json:"planRevisions"`
	TotalRuns       int64 `json:"totalRuns"`
	TotalAttempts   int64 `json:"totalAttempts"`
	WallTimeSeconds int64 `json:"wallTimeSeconds"`
	ComputeUnits    int64 `json:"computeUnits"`
	Tokens          int64 `json:"tokens"`
	ArtifactBytes   int64 `json:"artifactBytes"`
}

// Validate fails closed on any negative field.
func (usage BudgetUsage) Validate() error {
	for _, field := range []struct {
		name  string
		value int64
	}{
		{"used.planRevisions", usage.PlanRevisions},
		{"used.totalRuns", usage.TotalRuns},
		{"used.totalAttempts", usage.TotalAttempts},
		{"used.wallTimeSeconds", usage.WallTimeSeconds},
		{"used.computeUnits", usage.ComputeUnits},
		{"used.tokens", usage.Tokens},
		{"used.artifactBytes", usage.ArtifactBytes},
	} {
		if field.value < 0 {
			return fmt.Errorf("goal: %s must not be negative", field.name)
		}
	}
	return nil
}

// AddEstimate returns the usage with one node estimate added.
func (usage BudgetUsage) AddEstimate(estimate NodeEstimate) BudgetUsage {
	return BudgetUsage{
		PlanRevisions:   usage.PlanRevisions,
		TotalRuns:       usage.TotalRuns + estimate.Runs,
		TotalAttempts:   usage.TotalAttempts + estimate.Attempts,
		WallTimeSeconds: usage.WallTimeSeconds + estimate.WallTimeSeconds,
		ComputeUnits:    usage.ComputeUnits + estimate.ComputeUnits,
		Tokens:          usage.Tokens + estimate.Tokens,
		ArtifactBytes:   usage.ArtifactBytes + estimate.ArtifactBytes,
	}
}

// Guardrails is the frozen DAG guardrail limit list of ADR 0019 §4. Every
// limit is constructed once, never relaxed at admission time, and each one
// rejects fail closed when exceeded; not caring about token cost is not an
// exemption from any limit.
type Guardrails struct {
	MaxNodes           int64 `json:"maxNodes"`
	MaxDepth           int64 `json:"maxDepth"`
	MaxFanOut          int64 `json:"maxFanOut"`
	MaxConcurrentNodes int64 `json:"maxConcurrentNodes"`
	MaxPlanRevisions   int64 `json:"maxPlanRevisions"`
	MaxTotalRuns       int64 `json:"maxTotalRuns"`
	MaxTotalAttempts   int64 `json:"maxTotalAttempts"`
	MaxWallTimeSeconds int64 `json:"maxWallTimeSeconds"`
	MaxComputeUnits    int64 `json:"maxComputeUnits"`
	MaxTokens          int64 `json:"maxTokens"`
	MaxArtifactBytes   int64 `json:"maxArtifactBytes"`
}

// Validate fails closed unless every limit is a positive integer.
func (guardrails Guardrails) Validate() error {
	for _, field := range []struct {
		name  string
		value int64
	}{
		{"maxNodes", guardrails.MaxNodes},
		{"maxDepth", guardrails.MaxDepth},
		{"maxFanOut", guardrails.MaxFanOut},
		{"maxConcurrentNodes", guardrails.MaxConcurrentNodes},
		{"maxPlanRevisions", guardrails.MaxPlanRevisions},
		{"maxTotalRuns", guardrails.MaxTotalRuns},
		{"maxTotalAttempts", guardrails.MaxTotalAttempts},
		{"maxWallTimeSeconds", guardrails.MaxWallTimeSeconds},
		{"maxComputeUnits", guardrails.MaxComputeUnits},
		{"maxTokens", guardrails.MaxTokens},
		{"maxArtifactBytes", guardrails.MaxArtifactBytes},
	} {
		if field.value < 1 {
			return fmt.Errorf("goal: %s must be a positive integer", field.name)
		}
	}
	return nil
}

// GoalNode is one node of a Goal plan: a bounded unit of typed work owned by
// the authority namespace. The canonical digest of the node is its identity
// binding: the same nodeId carrying a different digest is a conflict that
// admission resolves only through a recorded supersession of a pending node.
type GoalNode struct {
	NodeId            string       `json:"nodeId"`
	ExecutorKind      ExecutorKind `json:"executorKind"`
	Title             string       `json:"title"`
	Repository        string       `json:"repository"`
	Paths             []string     `json:"paths"`
	SideEffectClasses []string     `json:"sideEffectClasses"`
	Estimate          NodeEstimate `json:"estimate"`
}

// Validate fails closed on a malformed node id, an unknown executor kind,
// any empty required field, a non-relative path, or an invalid estimate.
func (node GoalNode) Validate() error {
	if err := domain.ValidateID(node.NodeId); err != nil {
		return fmt.Errorf("goal: goalNode.nodeId: %w", err)
	}
	if err := node.ExecutorKind.Validate(); err != nil {
		return err
	}
	if err := requireText("goalNode.title", node.Title); err != nil {
		return err
	}
	if err := requireText("goalNode.repository", node.Repository); err != nil {
		return err
	}
	for index, path := range node.Paths {
		field := fmt.Sprintf("goalNode.paths[%d]", index)
		if err := requireText(field, path); err != nil {
			return err
		}
		if err := requireRelativePath(field, path); err != nil {
			return err
		}
	}
	for index, class := range node.SideEffectClasses {
		if err := requireText(fmt.Sprintf("goalNode.sideEffectClasses[%d]", index), class); err != nil {
			return err
		}
	}
	return node.Estimate.Validate()
}

// Canonical returns the deterministic serialization of the validated node
// with nil slice fields normalized to empty slices, so constructed and
// decoded records with equivalent content yield identical bytes.
func (node GoalNode) Canonical() ([]byte, error) {
	if err := node.Validate(); err != nil {
		return nil, err
	}
	return canonicalBytes(node.normalized())
}

// Digest returns the sha256 digest of the canonical serialization.
func (node GoalNode) Digest() (string, error) {
	canonicalized, err := node.Canonical()
	if err != nil {
		return "", err
	}
	return canonical.DigestBytes(canonicalized), nil
}

// Equal reports whether both nodes carry identical content after slice
// normalization.
func (node GoalNode) Equal(other GoalNode) bool {
	return reflect.DeepEqual(node.normalized(), other.normalized())
}

func (node GoalNode) normalized() GoalNode {
	node.Paths = nonNilStrings(node.Paths)
	node.SideEffectClasses = nonNilStrings(node.SideEffectClasses)
	return node
}

// GoalEdge is one dependency edge of a Goal plan pointing from the producer
// node to the dependent consumer node.
type GoalEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"`
}

// Validate fails closed on malformed endpoint ids or an unknown edge kind.
func (edge GoalEdge) Validate() error {
	if err := domain.ValidateID(edge.From); err != nil {
		return fmt.Errorf("goal: goalEdge.from: %w", err)
	}
	if err := domain.ValidateID(edge.To); err != nil {
		return fmt.Errorf("goal: goalEdge.to: %w", err)
	}
	return ValidateEdgeKind(edge.Kind)
}

// Canonical returns the deterministic serialization of the validated edge.
func (edge GoalEdge) Canonical() ([]byte, error) {
	if err := edge.Validate(); err != nil {
		return nil, err
	}
	return canonicalBytes(edge)
}

// Digest returns the sha256 digest of the canonical serialization.
func (edge GoalEdge) Digest() (string, error) {
	canonicalized, err := edge.Canonical()
	if err != nil {
		return "", err
	}
	return canonical.DigestBytes(canonicalized), nil
}

// Equal reports whether both edges carry identical field values.
func (edge GoalEdge) Equal(other GoalEdge) bool {
	return edge == other
}

// NodeSupersession records the authorized replacement or removal of one
// pending node during replanning, preserving the reason and lineage required
// by ADR 0019 §5. PreviousDigest binds the exact base revision node being
// superseded.
type NodeSupersession struct {
	NodeId         string `json:"nodeId"`
	PreviousDigest string `json:"previousDigest"`
	Reason         string `json:"reason"`
	Lineage        string `json:"lineage"`
}

// Validate fails closed on a malformed node id or previous digest and on any
// empty reason or lineage.
func (supersession NodeSupersession) Validate() error {
	if err := domain.ValidateID(supersession.NodeId); err != nil {
		return fmt.Errorf("goal: nodeSupersession.nodeId: %w", err)
	}
	if err := requireSHA256Digest("nodeSupersession.previousDigest", supersession.PreviousDigest); err != nil {
		return err
	}
	if err := requireText("nodeSupersession.reason", supersession.Reason); err != nil {
		return err
	}
	return requireText("nodeSupersession.lineage", supersession.Lineage)
}

// GoalSpecRevision is one immutable, append-only revision of the Goal
// specification. Revision numbers start at 1 and every later revision binds
// the canonical digest of its predecessor, forming a hash chain verified by
// ValidateSpecRevisionCAS.
type GoalSpecRevision struct {
	AuthorityNamespaceId authority.AuthorityNamespaceId `json:"authorityNamespaceId"`
	GoalId               string                         `json:"goalId"`
	Revision             int64                          `json:"revision"`
	PreviousDigest       string                         `json:"previousDigest"`
	ProjectId            string                         `json:"projectId"`
	Repository           string                         `json:"repository"`
	Title                string                         `json:"title"`
	Description          string                         `json:"description"`
}

// Validate fails closed on a missing ownership namespace, malformed ids, a
// non-positive revision, a malformed previous digest binding, or any empty
// required field.
func (revision GoalSpecRevision) Validate() error {
	if err := revision.AuthorityNamespaceId.Validate(); err != nil {
		return err
	}
	if err := domain.ValidateID(revision.GoalId); err != nil {
		return fmt.Errorf("goal: goalSpecRevision.goalId: %w", err)
	}
	if revision.Revision < 1 {
		return fmt.Errorf("goal: goalSpecRevision.revision must be at least 1")
	}
	if err := optionalSHA256Digest("goalSpecRevision.previousDigest", revision.PreviousDigest); err != nil {
		return err
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"goalSpecRevision.projectId", revision.ProjectId},
		{"goalSpecRevision.repository", revision.Repository},
		{"goalSpecRevision.title", revision.Title},
		{"goalSpecRevision.description", revision.Description},
	} {
		if err := requireText(field.name, field.value); err != nil {
			return err
		}
	}
	return nil
}

// Canonical returns the deterministic serialization of the validated record.
func (revision GoalSpecRevision) Canonical() ([]byte, error) {
	if err := revision.Validate(); err != nil {
		return nil, err
	}
	return canonicalBytes(revision)
}

// Digest returns the sha256 digest of the canonical serialization.
func (revision GoalSpecRevision) Digest() (string, error) {
	canonicalized, err := revision.Canonical()
	if err != nil {
		return "", err
	}
	return canonical.DigestBytes(canonicalized), nil
}

// Equal reports whether both revisions carry identical field values.
func (revision GoalSpecRevision) Equal(other GoalSpecRevision) bool {
	return revision == other
}

// ValidateSpecRevisionCAS verifies that next extends current by exactly one
// append-only revision: the revision number advances by one, the previous
// digest binds the exact canonical digest of current, and ownership and goal
// identity never change along the chain. A nil current admits only revision
// 1 with an empty previous digest.
func ValidateSpecRevisionCAS(current *GoalSpecRevision, next GoalSpecRevision) error {
	if err := next.Validate(); err != nil {
		return err
	}
	if current == nil {
		if next.Revision != 1 {
			return fmt.Errorf("goal: first goalSpecRevision must be revision 1, got %d", next.Revision)
		}
		if next.PreviousDigest != "" {
			return fmt.Errorf("goal: first goalSpecRevision must carry an empty previousDigest")
		}
		return nil
	}
	if !current.AuthorityNamespaceId.Equal(next.AuthorityNamespaceId) {
		return fmt.Errorf("goal: goalSpecRevision CAS rejects an ownership change")
	}
	if current.GoalId != next.GoalId {
		return fmt.Errorf("goal: goalSpecRevision CAS rejects a goalId change")
	}
	if next.Revision != current.Revision+1 {
		return fmt.Errorf("goal: goalSpecRevision CAS expects revision %d, got %d", current.Revision+1, next.Revision)
	}
	currentDigest, err := current.Digest()
	if err != nil {
		return fmt.Errorf("goal: goalSpecRevision CAS cannot digest the current revision: %w", err)
	}
	if next.PreviousDigest != currentDigest {
		return fmt.Errorf("goal: goalSpecRevision CAS rejects a previousDigest mismatch")
	}
	return nil
}

// GoalPlanProposal is the untrusted plan submitted by a Planner, a human or
// an LLM — all three pass through the identical admission path. It pins the
// Goal spec revision it plans against and, for replanning, the accepted plan
// revision it overlays. Nodes and edges are validated by the admission steps,
// not by the proposal schema, so their violations are classified at the
// correct step.
type GoalPlanProposal struct {
	AuthorityNamespaceId authority.AuthorityNamespaceId `json:"authorityNamespaceId"`
	ProposalId           string                         `json:"proposalId"`
	GoalId               string                         `json:"goalId"`
	ProjectId            string                         `json:"projectId"`
	Repository           string                         `json:"repository"`
	GoalSpecRevision     int64                          `json:"goalSpecRevision"`
	GoalSpecDigest       string                         `json:"goalSpecDigest"`
	BasedOnPlanRevision  int64                          `json:"basedOnPlanRevision"`
	BasedOnPlanDigest    string                         `json:"basedOnPlanDigest"`
	PlannerIdentity      string                         `json:"plannerIdentity"`
	Nodes                []GoalNode                     `json:"nodes"`
	Edges                []GoalEdge                     `json:"edges"`
	Supersessions        []NodeSupersession             `json:"supersessions"`
}

// Validate fails closed on proposal-level schema violations: a missing
// ownership namespace, malformed ids, non-positive or inconsistent revision
// bindings, an empty node list, or a malformed supersession entry. Node and
// edge content validity is intentionally not checked here; admission
// classifies those failures at steps 3-5.
func (proposal GoalPlanProposal) Validate() error {
	if err := proposal.AuthorityNamespaceId.Validate(); err != nil {
		return err
	}
	if err := domain.ValidateID(proposal.ProposalId); err != nil {
		return fmt.Errorf("goal: goalPlanProposal.proposalId: %w", err)
	}
	if err := domain.ValidateID(proposal.GoalId); err != nil {
		return fmt.Errorf("goal: goalPlanProposal.goalId: %w", err)
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"goalPlanProposal.projectId", proposal.ProjectId},
		{"goalPlanProposal.repository", proposal.Repository},
	} {
		if err := requireText(field.name, field.value); err != nil {
			return err
		}
	}
	if proposal.GoalSpecRevision < 1 {
		return fmt.Errorf("goal: goalPlanProposal.goalSpecRevision must be at least 1")
	}
	if err := requireSHA256Digest("goalPlanProposal.goalSpecDigest", proposal.GoalSpecDigest); err != nil {
		return err
	}
	if proposal.BasedOnPlanRevision < 0 {
		return fmt.Errorf("goal: goalPlanProposal.basedOnPlanRevision must not be negative")
	}
	if proposal.BasedOnPlanRevision == 0 {
		if proposal.BasedOnPlanDigest != "" {
			return fmt.Errorf("goal: an initial goalPlanProposal must carry an empty basedOnPlanDigest")
		}
	} else if err := requireSHA256Digest("goalPlanProposal.basedOnPlanDigest", proposal.BasedOnPlanDigest); err != nil {
		return err
	}
	if err := requireText("goalPlanProposal.plannerIdentity", proposal.PlannerIdentity); err != nil {
		return err
	}
	if len(proposal.Nodes) == 0 {
		return fmt.Errorf("goal: goalPlanProposal must declare at least one node")
	}
	for index, supersession := range proposal.Supersessions {
		if err := supersession.Validate(); err != nil {
			return fmt.Errorf("goal: goalPlanProposal.supersessions[%d]: %w", index, err)
		}
	}
	return nil
}

// Canonical returns the deterministic serialization of the validated
// proposal with nil slice fields normalized to empty slices.
func (proposal GoalPlanProposal) Canonical() ([]byte, error) {
	if err := proposal.Validate(); err != nil {
		return nil, err
	}
	return canonicalBytes(proposal.normalized())
}

// Digest returns the sha256 digest of the canonical serialization.
func (proposal GoalPlanProposal) Digest() (string, error) {
	canonicalized, err := proposal.Canonical()
	if err != nil {
		return "", err
	}
	return canonical.DigestBytes(canonicalized), nil
}

// Equal reports whether both proposals carry identical content after slice
// normalization.
func (proposal GoalPlanProposal) Equal(other GoalPlanProposal) bool {
	return reflect.DeepEqual(proposal.normalized(), other.normalized())
}

func (proposal GoalPlanProposal) normalized() GoalPlanProposal {
	proposal.Nodes = nonNilNodes(proposal.Nodes)
	proposal.Edges = nonNilEdges(proposal.Edges)
	proposal.Supersessions = nonNilSupersessions(proposal.Supersessions)
	return proposal
}

// AcceptedGoalPlanRevision is the immutable, append-only authority record
// Core creates when a proposal passes admission. It binds the proposal,
// Policy and budget snapshot digests and carries the complete effective
// graph together with the consumed supersessions (ADR 0019 §5).
type AcceptedGoalPlanRevision struct {
	AuthorityNamespaceId authority.AuthorityNamespaceId `json:"authorityNamespaceId"`
	GoalId               string                         `json:"goalId"`
	PlanRevision         int64                          `json:"planRevision"`
	PreviousPlanDigest   string                         `json:"previousPlanDigest"`
	ProposalDigest       string                         `json:"proposalDigest"`
	PolicyDigest         string                         `json:"policyDigest"`
	BudgetSnapshotDigest string                         `json:"budgetSnapshotDigest"`
	Nodes                []GoalNode                     `json:"nodes"`
	Edges                []GoalEdge                     `json:"edges"`
	Supersessions        []NodeSupersession             `json:"supersessions"`
}

// Validate fails closed on a missing ownership namespace, malformed ids or
// revision bindings, any invalid node, edge or supersession, or an empty
// effective node list. Accepted revisions are Core-written and therefore
// validated deeply, unlike untrusted proposals.
func (revision AcceptedGoalPlanRevision) Validate() error {
	if err := revision.AuthorityNamespaceId.Validate(); err != nil {
		return err
	}
	if err := domain.ValidateID(revision.GoalId); err != nil {
		return fmt.Errorf("goal: acceptedGoalPlanRevision.goalId: %w", err)
	}
	if revision.PlanRevision < 1 {
		return fmt.Errorf("goal: acceptedGoalPlanRevision.planRevision must be at least 1")
	}
	if err := optionalSHA256Digest("acceptedGoalPlanRevision.previousPlanDigest", revision.PreviousPlanDigest); err != nil {
		return err
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"acceptedGoalPlanRevision.proposalDigest", revision.ProposalDigest},
		{"acceptedGoalPlanRevision.policyDigest", revision.PolicyDigest},
		{"acceptedGoalPlanRevision.budgetSnapshotDigest", revision.BudgetSnapshotDigest},
	} {
		if err := requireSHA256Digest(field.name, field.value); err != nil {
			return err
		}
	}
	if len(revision.Nodes) == 0 {
		return fmt.Errorf("goal: acceptedGoalPlanRevision must carry at least one node")
	}
	for index, node := range revision.Nodes {
		if err := node.Validate(); err != nil {
			return fmt.Errorf("goal: acceptedGoalPlanRevision.nodes[%d]: %w", index, err)
		}
	}
	for index, edge := range revision.Edges {
		if err := edge.Validate(); err != nil {
			return fmt.Errorf("goal: acceptedGoalPlanRevision.edges[%d]: %w", index, err)
		}
	}
	for index, supersession := range revision.Supersessions {
		if err := supersession.Validate(); err != nil {
			return fmt.Errorf("goal: acceptedGoalPlanRevision.supersessions[%d]: %w", index, err)
		}
	}
	return nil
}

// Canonical returns the deterministic serialization of the validated record
// with nil slice fields normalized to empty slices.
func (revision AcceptedGoalPlanRevision) Canonical() ([]byte, error) {
	if err := revision.Validate(); err != nil {
		return nil, err
	}
	return canonicalBytes(revision.normalized())
}

// Digest returns the sha256 digest of the canonical serialization.
func (revision AcceptedGoalPlanRevision) Digest() (string, error) {
	canonicalized, err := revision.Canonical()
	if err != nil {
		return "", err
	}
	return canonical.DigestBytes(canonicalized), nil
}

// Equal reports whether both revisions carry identical content after slice
// normalization.
func (revision AcceptedGoalPlanRevision) Equal(other AcceptedGoalPlanRevision) bool {
	return reflect.DeepEqual(revision.normalized(), other.normalized())
}

func (revision AcceptedGoalPlanRevision) normalized() AcceptedGoalPlanRevision {
	revision.Nodes = nonNilNodes(revision.Nodes)
	revision.Edges = nonNilEdges(revision.Edges)
	revision.Supersessions = nonNilSupersessions(revision.Supersessions)
	return revision
}

// ValidatePlanRevisionCAS verifies that next extends current by exactly one
// append-only revision: the plan revision number advances by one, the
// previous plan digest binds the exact canonical digest of current, and
// ownership and goal identity never change along the chain. A nil current
// admits only plan revision 1 with an empty previous plan digest.
func ValidatePlanRevisionCAS(current *AcceptedGoalPlanRevision, next AcceptedGoalPlanRevision) error {
	if err := next.Validate(); err != nil {
		return err
	}
	if current == nil {
		if next.PlanRevision != 1 {
			return fmt.Errorf("goal: first acceptedGoalPlanRevision must be planRevision 1, got %d", next.PlanRevision)
		}
		if next.PreviousPlanDigest != "" {
			return fmt.Errorf("goal: first acceptedGoalPlanRevision must carry an empty previousPlanDigest")
		}
		return nil
	}
	if !current.AuthorityNamespaceId.Equal(next.AuthorityNamespaceId) {
		return fmt.Errorf("goal: acceptedGoalPlanRevision CAS rejects an ownership change")
	}
	if current.GoalId != next.GoalId {
		return fmt.Errorf("goal: acceptedGoalPlanRevision CAS rejects a goalId change")
	}
	if next.PlanRevision != current.PlanRevision+1 {
		return fmt.Errorf("goal: acceptedGoalPlanRevision CAS expects planRevision %d, got %d", current.PlanRevision+1, next.PlanRevision)
	}
	currentDigest, err := current.Digest()
	if err != nil {
		return fmt.Errorf("goal: acceptedGoalPlanRevision CAS cannot digest the current revision: %w", err)
	}
	if next.PreviousPlanDigest != currentDigest {
		return fmt.Errorf("goal: acceptedGoalPlanRevision CAS rejects a previousPlanDigest mismatch")
	}
	return nil
}

// GoalBudgetLedger is the frozen snapshot record of the Goal-wide cumulative
// budget: the constructed guardrail limits and the consumed usage. Admission
// binds the snapshot digest into every accepted plan revision, and the
// in-memory BudgetLedger rechecks it before appending a revision.
type GoalBudgetLedger struct {
	AuthorityNamespaceId authority.AuthorityNamespaceId `json:"authorityNamespaceId"`
	GoalId               string                         `json:"goalId"`
	Limits               Guardrails                     `json:"limits"`
	Used                 BudgetUsage                    `json:"used"`
}

// Validate fails closed on a missing ownership namespace, a malformed goal
// id, any non-positive limit or any negative usage counter.
func (ledger GoalBudgetLedger) Validate() error {
	if err := ledger.AuthorityNamespaceId.Validate(); err != nil {
		return err
	}
	if err := domain.ValidateID(ledger.GoalId); err != nil {
		return fmt.Errorf("goal: goalBudgetLedger.goalId: %w", err)
	}
	if err := ledger.Limits.Validate(); err != nil {
		return err
	}
	return ledger.Used.Validate()
}

// Canonical returns the deterministic serialization of the validated record.
func (ledger GoalBudgetLedger) Canonical() ([]byte, error) {
	if err := ledger.Validate(); err != nil {
		return nil, err
	}
	return canonicalBytes(ledger)
}

// Digest returns the sha256 digest of the canonical serialization.
func (ledger GoalBudgetLedger) Digest() (string, error) {
	canonicalized, err := ledger.Canonical()
	if err != nil {
		return "", err
	}
	return canonical.DigestBytes(canonicalized), nil
}

// Equal reports whether both snapshots carry identical field values.
func (ledger GoalBudgetLedger) Equal(other GoalBudgetLedger) bool {
	return ledger == other
}

// InterventionKind is the closed enumeration of Goal intervention kinds
// (ADR 0019 §8).
type InterventionKind string

// Closed intervention kinds of a Goal.
const (
	InterventionKindPause  InterventionKind = "pause"
	InterventionKindResume InterventionKind = "resume"
	InterventionKindAbort  InterventionKind = "abort"
)

// Validate rejects every value outside the closed enumeration.
func (kind InterventionKind) Validate() error {
	switch kind {
	case InterventionKindPause, InterventionKindResume, InterventionKindAbort:
		return nil
	default:
		return fmt.Errorf("goal: unknown interventionKind %q", string(kind))
	}
}

// PauseReason is the closed enumeration of typed Goal pause reasons
// (ADR 0019 §8).
type PauseReason string

// Closed pause reasons of a Goal.
const (
	PauseReasonAwaitingInput  PauseReason = "awaiting-input"
	PauseReasonOperator       PauseReason = "operator"
	PauseReasonPolicy         PauseReason = "policy"
	PauseReasonBudgetApproval PauseReason = "budget-approval"
)

// Validate rejects every value outside the closed enumeration.
func (reason PauseReason) Validate() error {
	switch reason {
	case PauseReasonAwaitingInput, PauseReasonOperator, PauseReasonPolicy, PauseReasonBudgetApproval:
		return nil
	default:
		return fmt.Errorf("goal: unknown pauseReason %q", string(reason))
	}
}

// PauseMode is the closed enumeration of frozen Goal pause modes
// (ADR 0019 §8).
type PauseMode string

// Closed pause modes of a Goal.
const (
	PauseModeDrainActive  PauseMode = "drain-active"
	PauseModeCancelActive PauseMode = "cancel-active"
)

// Validate rejects every value outside the closed enumeration.
func (mode PauseMode) Validate() error {
	switch mode {
	case PauseModeDrainActive, PauseModeCancelActive:
		return nil
	default:
		return fmt.Errorf("goal: unknown pauseMode %q", string(mode))
	}
}

// GoalIntervention is one append-only human or Policy intervention on a Goal
// (ADR 0019 §8). Pause interventions carry the typed pauseReason and one of
// the two frozen pause modes; resume and abort carry neither. Sequence forms
// a CAS chain: expectedSequence must equal sequence-1.
type GoalIntervention struct {
	AuthorityNamespaceId authority.AuthorityNamespaceId `json:"authorityNamespaceId"`
	InterventionId       string                         `json:"interventionId"`
	GoalId               string                         `json:"goalId"`
	Kind                 InterventionKind               `json:"kind"`
	PauseReason          PauseReason                    `json:"pauseReason"`
	PauseMode            PauseMode                      `json:"pauseMode"`
	Actor                string                         `json:"actor"`
	Reason               string                         `json:"reason"`
	Sequence             int64                          `json:"sequence"`
	ExpectedSequence     int64                          `json:"expectedSequence"`
	CreatedAt            string                         `json:"createdAt"`
}

// Validate fails closed on a missing ownership namespace, malformed ids, an
// unknown kind, pause fields present outside a pause or missing on one, any
// empty actor or reason, a broken sequence CAS, or a malformed timestamp.
func (intervention GoalIntervention) Validate() error {
	if err := intervention.AuthorityNamespaceId.Validate(); err != nil {
		return err
	}
	if err := domain.ValidateID(intervention.InterventionId); err != nil {
		return fmt.Errorf("goal: goalIntervention.interventionId: %w", err)
	}
	if err := domain.ValidateID(intervention.GoalId); err != nil {
		return fmt.Errorf("goal: goalIntervention.goalId: %w", err)
	}
	if err := intervention.Kind.Validate(); err != nil {
		return err
	}
	if intervention.Kind == InterventionKindPause {
		if err := intervention.PauseReason.Validate(); err != nil {
			return err
		}
		if err := intervention.PauseMode.Validate(); err != nil {
			return err
		}
	} else {
		if intervention.PauseReason != "" {
			return fmt.Errorf("goal: pauseReason must stay empty outside a pause intervention")
		}
		if intervention.PauseMode != "" {
			return fmt.Errorf("goal: pauseMode must stay empty outside a pause intervention")
		}
	}
	if err := requireText("goalIntervention.actor", intervention.Actor); err != nil {
		return err
	}
	if err := requireText("goalIntervention.reason", intervention.Reason); err != nil {
		return err
	}
	if intervention.Sequence < 1 {
		return fmt.Errorf("goal: goalIntervention.sequence must be at least 1")
	}
	if intervention.ExpectedSequence != intervention.Sequence-1 {
		return fmt.Errorf("goal: goalIntervention.expectedSequence must equal sequence-1")
	}
	return requireRFC3339("goalIntervention.createdAt", intervention.CreatedAt)
}

// Canonical returns the deterministic serialization of the validated record.
func (intervention GoalIntervention) Canonical() ([]byte, error) {
	if err := intervention.Validate(); err != nil {
		return nil, err
	}
	return canonicalBytes(intervention)
}

// Digest returns the sha256 digest of the canonical serialization.
func (intervention GoalIntervention) Digest() (string, error) {
	canonicalized, err := intervention.Canonical()
	if err != nil {
		return "", err
	}
	return canonical.DigestBytes(canonicalized), nil
}

// Equal reports whether both interventions carry identical field values.
func (intervention GoalIntervention) Equal(other GoalIntervention) bool {
	return intervention == other
}

// OutcomeState is the closed enumeration of terminal Goal outcome states.
type OutcomeState string

// Closed terminal states of a GoalOutcome.
const (
	OutcomeStateCompleted OutcomeState = "completed"
	OutcomeStateFailed    OutcomeState = "failed"
	OutcomeStateBlocked   OutcomeState = "blocked"
	OutcomeStateAborted   OutcomeState = "aborted"
)

// Validate rejects every value outside the closed enumeration.
func (state OutcomeState) Validate() error {
	switch state {
	case OutcomeStateCompleted, OutcomeStateFailed, OutcomeStateBlocked, OutcomeStateAborted:
		return nil
	default:
		return fmt.Errorf("goal: unknown outcomeState %q", string(state))
	}
}

// GoalOutcome is the immutable terminal record of a Goal: budget exhausted,
// terminated or non-converging Goals all preserve an outcome, and failure or
// blockage never ends silently (ADR 0019 §9 exit gates).
type GoalOutcome struct {
	AuthorityNamespaceId authority.AuthorityNamespaceId `json:"authorityNamespaceId"`
	GoalId               string                         `json:"goalId"`
	State                OutcomeState                   `json:"state"`
	Reason               string                         `json:"reason"`
	FinalPlanDigest      string                         `json:"finalPlanDigest"`
	BudgetDigest         string                         `json:"budgetDigest"`
	FinalizedAt          string                         `json:"finalizedAt"`
}

// Validate fails closed on a missing ownership namespace, a malformed goal
// id, an unknown outcome state, any empty reason, malformed digest bindings
// or a malformed timestamp.
func (outcome GoalOutcome) Validate() error {
	if err := outcome.AuthorityNamespaceId.Validate(); err != nil {
		return err
	}
	if err := domain.ValidateID(outcome.GoalId); err != nil {
		return fmt.Errorf("goal: goalOutcome.goalId: %w", err)
	}
	if err := outcome.State.Validate(); err != nil {
		return err
	}
	if err := requireText("goalOutcome.reason", outcome.Reason); err != nil {
		return err
	}
	if err := requireSHA256Digest("goalOutcome.finalPlanDigest", outcome.FinalPlanDigest); err != nil {
		return err
	}
	if err := requireSHA256Digest("goalOutcome.budgetDigest", outcome.BudgetDigest); err != nil {
		return err
	}
	return requireRFC3339("goalOutcome.finalizedAt", outcome.FinalizedAt)
}

// Canonical returns the deterministic serialization of the validated record.
func (outcome GoalOutcome) Canonical() ([]byte, error) {
	if err := outcome.Validate(); err != nil {
		return nil, err
	}
	return canonicalBytes(outcome)
}

// Digest returns the sha256 digest of the canonical serialization.
func (outcome GoalOutcome) Digest() (string, error) {
	canonicalized, err := outcome.Canonical()
	if err != nil {
		return "", err
	}
	return canonical.DigestBytes(canonicalized), nil
}

// Equal reports whether both outcomes carry identical field values.
func (outcome GoalOutcome) Equal(other GoalOutcome) bool {
	return outcome == other
}

// EvidenceDependencySet binds the dependency identity of one immutable
// Evidence record (ADR 0019 §6): subject, base, environment, Policy and
// verifier capability digests together with the upstream artifact digest
// set. Core derives current eligibility by appending supersession and
// ineligibility events against this binding; the set itself is never
// rewritten. ValidUntil is optional; when present it must be RFC 3339.
type EvidenceDependencySet struct {
	AuthorityNamespaceId     authority.AuthorityNamespaceId `json:"authorityNamespaceId"`
	EvidenceId               string                         `json:"evidenceId"`
	SubjectDigest            string                         `json:"subjectDigest"`
	BaseSha                  string                         `json:"baseSha"`
	EnvironmentDigest        string                         `json:"environmentDigest"`
	PolicyDigest             string                         `json:"policyDigest"`
	VerifierCapabilityDigest string                         `json:"verifierCapabilityDigest"`
	UpstreamArtifactDigests  []string                       `json:"upstreamArtifactDigests"`
	ValidUntil               string                         `json:"validUntil,omitempty"`
}

// Validate fails closed on a missing ownership namespace, a malformed
// evidence id, any malformed required digest or baseSha, a duplicate or
// malformed upstream artifact digest, or a malformed optional validUntil.
func (set EvidenceDependencySet) Validate() error {
	if err := set.AuthorityNamespaceId.Validate(); err != nil {
		return err
	}
	if err := domain.ValidateID(set.EvidenceId); err != nil {
		return fmt.Errorf("goal: evidenceDependencySet.evidenceId: %w", err)
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"evidenceDependencySet.subjectDigest", set.SubjectDigest},
		{"evidenceDependencySet.environmentDigest", set.EnvironmentDigest},
		{"evidenceDependencySet.policyDigest", set.PolicyDigest},
		{"evidenceDependencySet.verifierCapabilityDigest", set.VerifierCapabilityDigest},
	} {
		if err := requireSHA256Digest(field.name, field.value); err != nil {
			return err
		}
	}
	if err := requireText("evidenceDependencySet.baseSha", set.BaseSha); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(set.UpstreamArtifactDigests))
	for index, digest := range set.UpstreamArtifactDigests {
		field := fmt.Sprintf("evidenceDependencySet.upstreamArtifactDigests[%d]", index)
		if err := requireSHA256Digest(field, digest); err != nil {
			return err
		}
		if _, duplicate := seen[digest]; duplicate {
			return fmt.Errorf("goal: evidenceDependencySet.upstreamArtifactDigests must be a set without duplicates")
		}
		seen[digest] = struct{}{}
	}
	if set.ValidUntil == "" {
		return nil
	}
	return requireRFC3339("evidenceDependencySet.validUntil", set.ValidUntil)
}

// Canonical returns the deterministic serialization of the validated record
// with a nil upstream digest list normalized to an empty list.
func (set EvidenceDependencySet) Canonical() ([]byte, error) {
	if err := set.Validate(); err != nil {
		return nil, err
	}
	set.UpstreamArtifactDigests = nonNilStrings(set.UpstreamArtifactDigests)
	return canonicalBytes(set)
}

// Digest returns the sha256 digest of the canonical serialization.
func (set EvidenceDependencySet) Digest() (string, error) {
	canonicalized, err := set.Canonical()
	if err != nil {
		return "", err
	}
	return canonical.DigestBytes(canonicalized), nil
}

// Equal reports whether both sets carry identical content after slice
// normalization.
func (set EvidenceDependencySet) Equal(other EvidenceDependencySet) bool {
	set.UpstreamArtifactDigests = nonNilStrings(set.UpstreamArtifactDigests)
	other.UpstreamArtifactDigests = nonNilStrings(other.UpstreamArtifactDigests)
	return reflect.DeepEqual(set, other)
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func nonNilNodes(values []GoalNode) []GoalNode {
	if values == nil {
		return []GoalNode{}
	}
	return values
}

func nonNilEdges(values []GoalEdge) []GoalEdge {
	if values == nil {
		return []GoalEdge{}
	}
	return values
}

func nonNilSupersessions(values []NodeSupersession) []NodeSupersession {
	if values == nil {
		return []NodeSupersession{}
	}
	return values
}

func canonicalBytes(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("goal: canonical marshal: %w", err)
	}
	canonicalized, err := canonical.JSON(raw)
	if err != nil {
		return nil, fmt.Errorf("goal: canonical digest: %w", err)
	}
	return canonicalized, nil
}

func canonicalDigestOf(value any) (string, error) {
	canonicalized, err := canonicalBytes(value)
	if err != nil {
		return "", err
	}
	return canonical.DigestBytes(canonicalized), nil
}

func requireText(field, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("goal: %s must be a non-empty string", field)
	}
	return nil
}

func requireSHA256Digest(field, value string) error {
	if err := requireText(field, value); err != nil {
		return err
	}
	if !strings.HasPrefix(value, authority.DigestPrefix) {
		return fmt.Errorf("goal: %s must carry the %s digest prefix", field, authority.DigestPrefix)
	}
	hexPart := strings.TrimPrefix(value, authority.DigestPrefix)
	if len(hexPart) != 64 {
		return fmt.Errorf("goal: %s must be a 64 character sha256 hex digest", field)
	}
	for _, r := range hexPart {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return fmt.Errorf("goal: %s must be lowercase hex", field)
		}
	}
	return nil
}

func optionalSHA256Digest(field, value string) error {
	if value == "" {
		return nil
	}
	return requireSHA256Digest(field, value)
}

func requireRFC3339(field, value string) error {
	if _, err := time.Parse(time.RFC3339, value); err != nil {
		return fmt.Errorf("goal: %s must be an RFC 3339 timestamp", field)
	}
	return nil
}

func requireRelativePath(field, value string) error {
	if strings.HasPrefix(value, "/") {
		return fmt.Errorf("goal: %s must be a repository-relative path", field)
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == ".." {
			return fmt.Errorf("goal: %s must not traverse parent directories", field)
		}
	}
	if strings.ContainsRune(value, '\\') {
		return fmt.Errorf("goal: %s must use forward slashes", field)
	}
	return nil
}
