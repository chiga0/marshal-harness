package domain

// ADR 0032 closed remote PR-state enumeration observed by the merge target
// observer. These are the only states the pre-merge observation and the
// ObserveReady recovery state machine ever admit; anything else fails closed
// at the observer boundary.
const (
	MergeTargetStateOpen   = "OPEN"
	MergeTargetStateMerged = "MERGED"
	MergeTargetStateClosed = "CLOSED"
)

// SCMMergeTarget is the fresh pre-merge observation of a published PR node
// (ADR 0032 §2, §5). It carries only typed remote facts; the merge service
// compares every field against the frozen intent and the RunState anchors
// (head, frozen baseSha, marker) before any side effect. It never carries
// credentials, tokens or Provider raw output.
type SCMMergeTarget struct {
	Repository    string `json:"repository"`
	PRNumber      int    `json:"prNumber"`
	HeadOid       string `json:"headOid"`
	BaseBranch    string `json:"baseBranch"`
	BaseOid       string `json:"baseOid"`
	Draft         bool   `json:"draft"`
	State         string `json:"state"`
	MarkerPresent bool   `json:"markerPresent"`
}

// ObserveReady classification derived by the ADR 0032 §5 recovery state
// machine from a fresh SCMMergeTarget. The closed set distinguishes the four
// recovery outcomes; the merge service maps each to its frozen continuation.
type MergeReadyState string

const (
	// MergeReadyStillDraft means ready has not taken effect and may be
	// retried idempotently with the same intent.
	MergeReadyStillDraft MergeReadyState = "still-draft"
	// MergeReadyReady means ready has taken effect (including a lost
	// response) and Merge may continue under recovery binding.
	MergeReadyReady MergeReadyState = "ready"
	// MergeReadyMerged means the PR is already merged; recovery continues
	// through ObserveMergeReceipt instead.
	MergeReadyMerged MergeReadyState = "merged"
	// MergeReadyDrifted means the PR is closed or any identity field drifted;
	// recovery must fail closed to BLOCKED.
	MergeReadyDrifted MergeReadyState = "drifted"
)
