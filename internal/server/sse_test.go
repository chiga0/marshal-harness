package server

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

// sseTestNamespace is the authority key space of the projection unit tests.
func sseTestNamespace() authority.AuthorityNamespaceId {
	return authority.AuthorityNamespaceId{
		TenantNamespace:  "local",
		ControlPlaneId:   "default",
		AuthorityScopeId: "repo:/sse-fixture",
	}
}

// newTestProjection assembles one manually driven projection: the watcher
// goroutine only runs where a test starts it explicitly.
func newTestProjection(t *testing.T, stateRoot string, watchInterval time.Duration) *Projection {
	t.Helper()
	projection := newProjection(stateRoot, sseTestNamespace(), runstore.New(stateRoot), watchInterval)
	t.Cleanup(func() { _ = projection.Close() })
	return projection
}

// appendJournalEvents appends `count` schema-valid RunEvents with sequences
// startSequence+1..startSequence+count directly to one Run journal and
// returns them in journal order.
func appendJournalEvents(t *testing.T, stateRoot, runID string, startSequence uint64, count, payloadBytes int) []domain.RunEvent {
	t.Helper()
	directory := filepath.Join(stateRoot, "runs", runID)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatalf("create run directory: %v", err)
	}
	file, err := os.OpenFile(filepath.Join(directory, "events.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open journal: %v", err)
	}
	defer func() { _ = file.Close() }()
	events := make([]domain.RunEvent, 0, count)
	for index := 0; index < count; index++ {
		eventID, err := domain.NewID("event")
		if err != nil {
			t.Fatalf("generate event ID: %v", err)
		}
		sequence := startSequence + uint64(index) + 1
		event := domain.RunEvent{
			APIVersion: domain.APIVersionV1Alpha1,
			Kind:       domain.KindRunEvent,
			EventID:    eventID,
			RunID:      runID,
			Sequence:   sequence,
			Type:       "worker.progress",
			StateFrom:  domain.StateRunning,
			StateTo:    domain.StateRunning,
			Timestamp:  fixtureClock.Add(time.Duration(sequence) * time.Second),
			Actor:      &domain.Actor{Type: "system", ID: "sse-fixture"},
			Payload: map[string]any{
				"taskId":  "task-sse-fixture",
				"counter": sequence,
				"padding": strings.Repeat("s", payloadBytes),
			},
		}
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("encode event: %v", err)
		}
		if _, err := file.Write(append(data, '\n')); err != nil {
			t.Fatalf("append event: %v", err)
		}
		events = append(events, event)
	}
	return events
}

// rewriteJournal replaces one Run journal with exactly the given events,
// simulating journal compaction or rewrite below the projection.
func rewriteJournal(t *testing.T, stateRoot, runID string, events []domain.RunEvent) {
	t.Helper()
	var buffer bytes.Buffer
	for _, event := range events {
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("encode event: %v", err)
		}
		buffer.Write(append(data, '\n'))
	}
	path := filepath.Join(stateRoot, "runs", runID, "events.jsonl")
	if err := os.WriteFile(path, buffer.Bytes(), 0o600); err != nil {
		t.Fatalf("rewrite journal: %v", err)
	}
}

// waitFor polls one condition until it holds or the timeout fails the test.
func waitFor(t *testing.T, timeout time.Duration, condition func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", message)
}

// drainFrames reads `count` projections from one subscription buffer,
// failing closed on a stalled delivery.
func drainFrames(t *testing.T, subscriber *sseSubscriber, count int) []EventProjection {
	t.Helper()
	frames := make([]EventProjection, 0, count)
	for index := 0; index < count; index++ {
		select {
		case frame := <-subscriber.out:
			frames = append(frames, frame)
		case <-time.After(2 * time.Second):
			t.Fatalf("the subscription stalled after %d of %d frames", len(frames), count)
		}
	}
	return frames
}

// skipWhenParentFailed guards the sequentially dependent subtests: when an
// earlier stage of the identical chain already failed, the dependent stages
// skip instead of asserting against a broken prerequisite state.
func skipWhenParentFailed(parent, sub *testing.T) {
	sub.Helper()
	if parent.Failed() {
		sub.Skip("a prerequisite stage of this test already failed")
	}
}

// TestProjectionBacklogAndPollBoundaries freezes the cursor semantics of the
// projection: backlog replay from ledgerSequence 1, the exclusive cursor
// boundary shared with the SSE channel, the drained cursor, and the
// deterministic resync beyond the ledger.
func TestProjectionBacklogAndPollBoundaries(t *testing.T) {
	stateRoot := t.TempDir()
	projection := newTestProjection(t, stateRoot, time.Hour)
	journal := appendJournalEvents(t, stateRoot, "run-sse-a", 0, 9, 0)
	projection.ScanNow()

	t.Run("full-backlog-poll-replays-9-of-9", func(t *testing.T) {
		page, resync := projection.Poll(0, 100)
		if resync != nil {
			t.Fatalf("the full backlog poll resynced: %+v", resync)
		}
		if page.Kind != KindEventPage || len(page.Events) != 9 {
			t.Fatalf("EventPage = kind %q with %d events, want %q with 9", page.Kind, len(page.Events), KindEventPage)
		}
		if !page.AuthorityNamespaceId.Equal(sseTestNamespace()) || page.Scope != sseTestNamespace().AuthorityScopeId {
			t.Fatalf("EventPage lost the cursor identity: %+v scope=%q", page.AuthorityNamespaceId, page.Scope)
		}
		for index, frame := range page.Events {
			if frame.Kind != KindEventProjection {
				t.Fatalf("projection kind = %q, want %q", frame.Kind, KindEventProjection)
			}
			if frame.LedgerSequence != uint64(index+1) {
				t.Fatalf("ledgerSequence = %d, want %d", frame.LedgerSequence, index+1)
			}
			if frame.EventID != journal[index].EventID {
				t.Fatalf("eventId %q does not match the journal order %q", frame.EventID, journal[index].EventID)
			}
			if !strings.HasPrefix(frame.PayloadDigest, "sha256:") {
				t.Fatalf("payloadDigest %q is not a sha256 digest", frame.PayloadDigest)
			}
		}
		if page.NextCursor != 9 {
			t.Fatalf("nextCursor = %d, want 9", page.NextCursor)
		}
	})

	t.Run("snapshot-digest-is-deterministic", func(t *testing.T) {
		// The snapshot digest is deterministic for identical projection states.
		page, resync := projection.Poll(0, 100)
		again, againResync := projection.Poll(0, 100)
		if resync != nil || againResync != nil || again.SnapshotDigest == "" || again.SnapshotDigest != page.SnapshotDigest {
			t.Fatalf("snapshot digest is not deterministic: %q vs %q", again.SnapshotDigest, page.SnapshotDigest)
		}
	})

	t.Run("exclusive-cursor-boundary", func(t *testing.T) {
		// Exclusive cursor boundary: cursor 3 delivers 4 and 5 only.
		page, resync := projection.Poll(3, 2)
		if resync != nil || len(page.Events) != 2 {
			t.Fatalf("cursor window = %d events resync=%v, want 2", len(page.Events), resync)
		}
		if page.Events[0].LedgerSequence != 4 || page.Events[1].LedgerSequence != 5 || page.NextCursor != 5 {
			t.Fatalf("cursor window = sequences %d..%d next %d, want 4..5 next 5",
				page.Events[0].LedgerSequence, page.Events[1].LedgerSequence, page.NextCursor)
		}
	})

	t.Run("drained-cursor-returns-empty-page", func(t *testing.T) {
		// Drained cursor: an empty page, never a resync.
		page, resync := projection.Poll(9, 10)
		if resync != nil || len(page.Events) != 0 || page.NextCursor != 9 {
			t.Fatalf("drained cursor = %d events next %d resync=%v, want empty page at cursor 9",
				len(page.Events), page.NextCursor, resync)
		}
	})

	t.Run("beyond-ledger-cursor-resyncs", func(t *testing.T) {
		// Beyond the ledger: deterministic resync, never silent continuation.
		_, resync := projection.Poll(10, 10)
		if resync == nil {
			t.Fatal("polling beyond the ledger did not resync")
		}
		if resync.Kind != KindEventResync || resync.Reason != resyncReasonCursorExpired || resync.StartSequence != 1 {
			t.Fatalf("EventResync = %+v, want kind %q reason %q startSequence 1",
				resync, KindEventResync, resyncReasonCursorExpired)
		}
		if !strings.HasPrefix(resync.SnapshotDigest, "sha256:") {
			t.Fatalf("EventResync lacks the snapshot digest: %+v", resync)
		}
	})
}

// TestSubscriptionCursorResolution freezes subscription positioning: no
// cursor replays the full backlog from sequence 1, the cursor and
// Last-Event-ID resume with the identical exclusive boundary as the polling
// channel, and expired or malformed cursors fail closed or resync.
func TestSubscriptionCursorResolution(t *testing.T) {
	stateRoot := t.TempDir()
	projection := newTestProjection(t, stateRoot, time.Hour)
	journal := appendJournalEvents(t, stateRoot, "run-sse-a", 0, 9, 0)
	projection.ScanNow()

	t.Run("no-cursor-replays-full-backlog", func(t *testing.T) {
		// No cursor: the complete backlog, 9/9, from ledgerSequence 1.
		outcome := projection.Subscribe("", "", 64)
		if outcome.APIError() != nil || outcome.Resync() != nil {
			t.Fatalf("fresh subscription rejected: err=%v resync=%+v", outcome.APIError(), outcome.Resync())
		}
		frames := drainFrames(t, outcome.Subscriber(), 9)
		for index, frame := range frames {
			if frame.LedgerSequence != uint64(index+1) || frame.EventID != journal[index].EventID {
				t.Fatalf("backlog frame %d = sequence %d eventId %q, want sequence %d eventId %q",
					index, frame.LedgerSequence, frame.EventID, index+1, journal[index].EventID)
			}
		}
	})

	t.Run("cursor-resumes-with-exclusive-boundary", func(t *testing.T) {
		// Cursor: exclusive resume from ledgerSequence 4.
		outcome := projection.Subscribe("3", "", 64)
		if outcome.Resync() != nil || outcome.APIError() != nil {
			t.Fatalf("cursor subscription rejected: %+v", outcome)
		}
		frames := drainFrames(t, outcome.Subscriber(), 6)
		if frames[0].LedgerSequence != 4 || frames[5].LedgerSequence != 9 {
			t.Fatalf("cursor resume delivered sequences %d..%d, want 4..9",
				frames[0].LedgerSequence, frames[5].LedgerSequence)
		}
	})

	t.Run("last-event-id-resumes-with-identical-boundary", func(t *testing.T) {
		// Last-Event-ID: the identical boundary spelled as an eventId.
		outcome := projection.Subscribe("", journal[4].EventID, 64)
		if outcome.Resync() != nil || outcome.APIError() != nil {
			t.Fatalf("Last-Event-ID subscription rejected: %+v", outcome)
		}
		frames := drainFrames(t, outcome.Subscriber(), 4)
		if frames[0].LedgerSequence != 6 || frames[3].LedgerSequence != 9 {
			t.Fatalf("Last-Event-ID resume delivered sequences %d..%d, want 6..9",
				frames[0].LedgerSequence, frames[3].LedgerSequence)
		}
	})

	t.Run("drained-cursor-waits-live", func(t *testing.T) {
		// Drained cursor: the subscription waits live with an empty backlog.
		outcome := projection.Subscribe("9", "", 64)
		if outcome.Resync() != nil || outcome.APIError() != nil || outcome.Subscriber() == nil {
			t.Fatalf("drained cursor subscription rejected: %+v", outcome)
		}
		select {
		case frame := <-outcome.Subscriber().out:
			t.Fatalf("the drained cursor replayed %+v", frame)
		case <-time.After(50 * time.Millisecond):
		}
	})

	t.Run("expired-cursor-resyncs", func(t *testing.T) {
		outcome := projection.Subscribe("10", "", 64)
		if outcome.Subscriber() != nil || outcome.Resync() == nil {
			t.Fatal("the expired cursor did not resync")
		}
		if outcome.Resync().Reason != resyncReasonCursorExpired || outcome.Resync().StartSequence != 1 {
			t.Fatalf("expired cursor resync = %+v", outcome.Resync())
		}
	})

	t.Run("unknown-last-event-id-resyncs", func(t *testing.T) {
		outcome := projection.Subscribe("", "event-does-not-exist", 64)
		if outcome.Resync() == nil || outcome.Resync().Reason != resyncReasonCursorExpired {
			t.Fatalf("unknown Last-Event-ID resync = %+v", outcome.Resync())
		}
	})

	t.Run("malformed-cursor-fails-closed", func(t *testing.T) {
		outcome := projection.Subscribe("not-a-number", "", 64)
		if outcome.APIError() == nil || outcome.APIError().Code != CodeInvalidRequest || outcome.APIError().Reason != "cursor-invalid" {
			t.Fatalf("malformed cursor error = %+v, want INVALID_REQUEST cursor-invalid", outcome.APIError())
		}
	})
}

// TestProjectionRebuildIssuesDeterministicResync freezes the gap/compaction
// rule: a journal that lost projected events rebuilds the projection,
// disconnects active subscribers with a deterministic resync directive, and
// never continues silently from stale cursors. The subtests form one
// sequential chain over the identical rebuilt state.
func TestProjectionRebuildIssuesDeterministicResync(t *testing.T) {
	stateRoot := t.TempDir()
	projection := newTestProjection(t, stateRoot, time.Hour)
	journal := appendJournalEvents(t, stateRoot, "run-sse-a", 0, 5, 0)
	projection.ScanNow()

	outcome := projection.Subscribe("", "", 64)
	subscriber := outcome.Subscriber()
	drainFrames(t, subscriber, 5)

	parent := t

	t.Run("compaction-disconnects-with-rebuild-resync", func(t *testing.T) {
		// Compaction: the journal keeps only the first two projected events.
		rewriteJournal(t, stateRoot, "run-sse-a", journal[:2])
		projection.ScanNow()

		select {
		case <-subscriber.done:
		case <-time.After(2 * time.Second):
			t.Fatal("the rebuilt projection kept serving a stale subscription")
		}
		if subscriber.directive.Reason != resyncReasonProjectionRebuilt || subscriber.directive.StartSequence != 1 {
			t.Fatalf("rebuild resync directive = %+v", subscriber.directive)
		}
	})

	t.Run("rebuild-resync-digest-matches-projection", func(t *testing.T) {
		skipWhenParentFailed(parent, t)
		page, resync := projection.Poll(0, 100)
		if resync != nil || len(page.Events) != 2 {
			t.Fatalf("rebuilt projection = %d events resync=%v, want 2", len(page.Events), resync)
		}
		if subscriber.directive.SnapshotDigest != page.SnapshotDigest {
			t.Fatalf("rebuild resync digest %q diverges from the projection digest %q",
				subscriber.directive.SnapshotDigest, page.SnapshotDigest)
		}
	})

	t.Run("stale-cursor-resync-is-deterministic", func(t *testing.T) {
		skipWhenParentFailed(parent, t)
		// The directive is deterministic across repeated observations.
		_, first := projection.Poll(99, 10)
		_, second := projection.Poll(99, 10)
		if first == nil || second == nil {
			t.Fatal("the rebuilt projection did not resync a stale cursor")
		}
		if !reflect.DeepEqual(first, second) {
			t.Fatalf("the resync directive is not deterministic: %+v vs %+v", first, second)
		}
	})

	t.Run("fresh-subscription-replays-rebuilt-ledger", func(t *testing.T) {
		skipWhenParentFailed(parent, t)
		outcome := projection.Subscribe("0", "", 64)
		if outcome.Resync() != nil || outcome.APIError() != nil {
			t.Fatalf("fresh subscription after rebuild rejected: %+v", outcome)
		}
		frames := drainFrames(t, outcome.Subscriber(), 2)
		if frames[0].LedgerSequence != 1 || frames[0].EventID != journal[0].EventID {
			t.Fatalf("rebuilt backlog = %+v, want the first surviving event at sequence 1", frames[0])
		}
	})
}

// TestBackpressureDisconnectsSlowSubscriberWithoutBlockingSource freezes the
// backpressure rule: a bounded buffer overflow disconnects the slow
// subscriber with a resync directive, and the event source never blocks nor
// drops ledger content because of it. The subtests form one sequential chain
// over the identical stalled subscription.
func TestBackpressureDisconnectsSlowSubscriberWithoutBlockingSource(t *testing.T) {
	stateRoot := t.TempDir()
	projection := newTestProjection(t, stateRoot, time.Hour)
	appendJournalEvents(t, stateRoot, "run-sse-a", 0, 1, 0)
	projection.ScanNow()

	// The subscriber never drains: the backlog occupies one of two slots.
	outcome := projection.Subscribe("", "", 2)
	subscriber := outcome.Subscriber()
	if outcome.Resync() != nil || outcome.APIError() != nil {
		t.Fatalf("slow subscriber setup rejected: %+v", outcome)
	}

	parent := t

	t.Run("event-source-never-blocks", func(t *testing.T) {
		appendJournalEvents(t, stateRoot, "run-sse-a", 1, 10, 0)
		started := time.Now()
		projection.ScanNow()
		if elapsed := time.Since(started); elapsed > time.Second {
			t.Fatalf("the event source blocked on the slow subscriber for %v", elapsed)
		}
	})

	t.Run("slow-subscriber-disconnects-with-resync", func(t *testing.T) {
		skipWhenParentFailed(parent, t)
		select {
		case <-subscriber.done:
		case <-time.After(2 * time.Second):
			t.Fatal("the slow subscriber was not disconnected")
		}
		if subscriber.directive.Reason != resyncReasonBackpressureFull || subscriber.directive.StartSequence != 1 {
			t.Fatalf("backpressure resync directive = %+v", subscriber.directive)
		}
	})

	t.Run("ledger-retains-every-event", func(t *testing.T) {
		skipWhenParentFailed(parent, t)
		// The ledger kept every event: the source never loses content to a
		// slow consumer, and recovery stays possible through the projection.
		page, resync := projection.Poll(0, 100)
		if resync != nil || len(page.Events) != 11 {
			t.Fatalf("ledger after backpressure = %d events resync=%v, want 11", len(page.Events), resync)
		}
	})
}

// TestProjectionLiveDeliveryViaNotifyWake freezes the MARSHAL_NOTIFY_CMD
// adaptation: one hook wake delivers freshly journaled events to an open
// subscription without waiting for the watch interval.
func TestProjectionLiveDeliveryViaNotifyWake(t *testing.T) {
	stateRoot := t.TempDir()
	projection := newTestProjection(t, stateRoot, time.Hour)
	go projection.watch()

	parent := t

	t.Run("notify-wake-ingests-initial-journal", func(t *testing.T) {
		appendJournalEvents(t, stateRoot, "run-sse-a", 0, 1, 0)
		projection.wake()
		waitFor(t, 2*time.Second, func() bool {
			page, resync := projection.Poll(0, 10)
			return resync == nil && len(page.Events) == 1
		}, "the initial scan to ingest the journal")
	})

	t.Run("notify-wake-delivers-live-event", func(t *testing.T) {
		skipWhenParentFailed(parent, t)
		outcome := projection.Subscribe("", "", 64)
		subscriber := outcome.Subscriber()
		drainFrames(t, subscriber, 1)

		appendJournalEvents(t, stateRoot, "run-sse-a", 1, 1, 0)
		projection.wake()
		select {
		case frame := <-subscriber.out:
			if frame.LedgerSequence != 2 {
				t.Fatalf("live delivery sequence = %d, want 2", frame.LedgerSequence)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("the notify wake did not deliver the live event")
		}
	})
}

// TestSSEFrameBytesMatchPollBytes freezes the single-constructor rule: the
// SSE data frame bytes and the polling fallback bytes of the same sequence
// are identical JSON, and the retired EventCursor frame cannot appear.
func TestSSEFrameBytesMatchPollBytes(t *testing.T) {
	stateRoot := t.TempDir()
	projection := newTestProjection(t, stateRoot, time.Hour)
	appendJournalEvents(t, stateRoot, "run-sse-a", 0, 3, 0)
	projection.ScanNow()

	page, resync := projection.Poll(0, 100)
	if resync != nil {
		t.Fatalf("poll resynced: %+v", resync)
	}
	pageData, err := json.Marshal(page)
	if err != nil {
		t.Fatalf("encode EventPage: %v", err)
	}
	var wire struct {
		Events []json.RawMessage `json:"events"`
	}
	if err := json.Unmarshal(pageData, &wire); err != nil {
		t.Fatalf("decode EventPage: %v", err)
	}

	t.Run("sse-frame-bytes-match-poll-bytes", func(t *testing.T) {
		for index, frame := range page.Events {
			data, err := json.Marshal(frame)
			if err != nil {
				t.Fatalf("encode EventProjection: %v", err)
			}
			if !bytes.Equal(data, []byte(wire.Events[index])) {
				t.Fatalf("the SSE frame bytes diverge from the poll projection at sequence %d:\n SSE: %s\npoll: %s",
					frame.LedgerSequence, data, wire.Events[index])
			}
		}
	})

	t.Run("retired-event-cursor-frame-absent", func(t *testing.T) {
		for _, frame := range page.Events {
			data, err := json.Marshal(frame)
			if err != nil {
				t.Fatalf("encode EventProjection: %v", err)
			}
			if strings.Contains(string(data), "EventCursor") {
				t.Fatalf("the projection carries the retired EventCursor frame: %s", data)
			}
		}
	})
}
