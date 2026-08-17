package lifecycle

import (
	"errors"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/domain"
)

// mergeGuards returns the full guard set plus the ADR 0032 MergeAuthorized
// flag required to reduce the receipt/intent-bound publication.merged event.
func mergeGuards() Guard {
	guard := allGuards()
	guard.MergeAuthorized = true
	return guard
}

// mergedEvent builds a structurally valid publication.merged event carrying
// the fixed producer actor and the closed nine-field receipt/intent payload.
func mergedEvent(state domain.RunState) domain.RunEvent {
	return domain.RunEvent{
		APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent, EventID: "event:merged-1",
		RunID: state.RunID, Sequence: state.Sequence + 1, Type: PublicationMergedEventType,
		StateFrom: state.State, StateTo: domain.StateAccepted, Timestamp: time.Unix(9, 0),
		Actor: &domain.Actor{Type: MergerActorType, ID: MergerActorID},
		Payload: map[string]any{
			"intentId":                "intent:" + repeatHex("1", 64),
			"intentDigest":            "sha256:" + repeatHex("a", 64),
			"receiptId":               "receipt:" + repeatHex("2", 64),
			"receiptDigest":           "sha256:" + repeatHex("b", 64),
			"headOid":                 repeatHex("c", 40),
			"mergeCommitSha":          repeatHex("d", 40),
			"mergeMethod":             domain.MergeMethodSquash,
			"publicationDigest":       "sha256:" + repeatHex("e", 64),
			"remoteCheckRecordDigest": "sha256:" + repeatHex("f", 64),
		},
	}
}

func mergedState() domain.RunState {
	state := domain.NewRunState("task:1", "run:1", time.Unix(1, 0))
	state.State = domain.StateCIPending
	state.Sequence = 9
	state.ReviewRound = 1
	state.SpecDigest = "sha256:" + repeatHex("a", 64)
	state.PolicyDigest = "sha256:" + repeatHex("b", 64)
	state.CapabilityDigest = "sha256:" + repeatHex("c", 64)
	state.BaseSHA = repeatHex("d", 40)
	state.Publication = &domain.RunPublication{Provider: "github", Repository: "org/repo", HeadBranch: "marshal/branch", BaseBranch: "main", ExternalID: "PR_1", URI: "https://github.com/org/repo/pull/7", HeadSHA: repeatHex("c", 40)}
	return state
}

func TestPublicationMergedValidateTransition(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		mutate  func(*domain.RunEvent)
		wantErr bool
	}{
		{"ci pending to accepted allowed", nil, false},
		{"wrong source state rejected", func(e *domain.RunEvent) { e.StateFrom = domain.StatePublished }, true},
		{"wrong target state rejected", func(e *domain.RunEvent) { e.StateTo = domain.StateBlocked }, true},
		{"omitted actor rejected", func(e *domain.RunEvent) { e.Actor = nil }, true},
		{"worker actor rejected", func(e *domain.RunEvent) { e.Actor = &domain.Actor{Type: "system", ID: "marshal-worker-runner"} }, true},
		{"wrong publisher actor id rejected", func(e *domain.RunEvent) { e.Actor = &domain.Actor{Type: "publisher", ID: "marshal-github-publisher"} }, true},
		{"wrong run id rejected", func(e *domain.RunEvent) { e.RunID = "run:other" }, true},
		{"same sequence rejected", func(e *domain.RunEvent) { e.Sequence-- }, true},
		{"skipped sequence rejected", func(e *domain.RunEvent) { e.Sequence++ }, true},
		{"missing intent id rejected", func(e *domain.RunEvent) { delete(e.Payload, "intentId") }, true},
		{"blank intent digest rejected", func(e *domain.RunEvent) { e.Payload["intentDigest"] = "   " }, true},
		{"extra payload field rejected", func(e *domain.RunEvent) { e.Payload["injected"] = "value" }, true},
		{"non-string field rejected", func(e *domain.RunEvent) { e.Payload["mergeCommitSha"] = 42 }, true},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			state := mergedState()
			event := mergedEvent(state)
			if test.mutate != nil {
				test.mutate(&event)
			}
			err := ValidateTransition(state.State, state.RunID, state.Sequence, event)
			if test.wantErr && !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("want rejection, got err = %v", err)
			}
			if !test.wantErr && err != nil {
				t.Fatalf("merged event rejected: %v", err)
			}
		})
	}
}

func TestPublicationMergedReduceRequiresMergeAuthorization(t *testing.T) {
	t.Parallel()
	fullGuard := mergeGuards()
	tests := []struct {
		name   string
		mutate func(*Guard)
	}{
		{"missing lease", func(g *Guard) { g.LeaseHeld = false }},
		{"missing merge authorization", func(g *Guard) { g.MergeAuthorized = false }},
		{"missing current evidence", func(g *Guard) { g.EvidenceCurrent = false }},
		{"missing current publication", func(g *Guard) { g.PublicationCurrent = false }},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			state := mergedState()
			guard := fullGuard
			test.mutate(&guard)
			if _, err := Reduce(state, mergedEvent(state), guard); !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("unguarded merge reduce error = %v", err)
			}
		})
	}
	state := mergedState()
	next, err := Reduce(state, mergedEvent(state), fullGuard)
	if err != nil {
		t.Fatalf("fully guarded merge reduce rejected: %v", err)
	}
	if next.State != domain.StateAccepted || next.Sequence != state.Sequence+1 {
		t.Fatalf("merge reduce result = %+v", next)
	}
}

func TestPublicationMergedReduceMatchesReplay(t *testing.T) {
	t.Parallel()
	state := mergedState()
	event := mergedEvent(state)
	reduced, err := Reduce(state, event, mergeGuards())
	if err != nil {
		t.Fatalf("merge reduce rejected: %v", err)
	}
	replayed, err := Replay(state, event)
	if err != nil {
		t.Fatalf("merge replay rejected: %v", err)
	}
	if reduced != replayed {
		t.Fatalf("merge reduce and replay diverge: %+v vs %+v", reduced, replayed)
	}
	if replayed.State != domain.StateAccepted || replayed.Sequence != state.Sequence+1 {
		t.Fatalf("merge replay = %+v", replayed)
	}
	// publication.merged never touches budget counters.
	if replayed.AttemptsUsed != state.AttemptsUsed || replayed.OperationalRetriesUsed != state.OperationalRetriesUsed ||
		replayed.ReviewRound != state.ReviewRound || replayed.ReworkRoundsUsed != state.ReworkRoundsUsed {
		t.Fatalf("merge replay mutated budget counters: %+v", replayed)
	}
	if replayed.Publication == nil || replayed.Publication.HeadSHA != state.Publication.HeadSHA {
		t.Fatalf("merge replay lost publication identity: %+v", replayed.Publication)
	}
}
