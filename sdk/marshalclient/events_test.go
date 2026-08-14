package marshalclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// fixtureEvent renders one EventProjection of the fake ledger.
func fixtureEvent(sequence uint64, id string) EventProjection {
	return EventProjection{
		APIVersion:           "marshal.dev/v1alpha1",
		Kind:                 "EventProjection",
		AuthorityNamespaceId: fixtureNamespace,
		Scope:                fixtureScope,
		EventID:              id,
		LedgerSequence:       sequence,
		RunID:                "run-1",
		RunSequence:          sequence,
		TaskID:               "task-1",
		Type:                 "run.state",
		StateFrom:            "CREATED",
		StateTo:              "PLANNED",
		Timestamp:            time.Unix(1700000000+int64(sequence), 0).UTC(),
		PayloadDigest:        "sha256:fixture-payload",
	}
}

func fixtureResyncDirective(reason string, startSequence uint64, snapshotDigest string) EventResync {
	return EventResync{
		APIVersion:           "marshal.dev/v1alpha1",
		Kind:                 "EventResync",
		AuthorityNamespaceId: fixtureNamespace,
		Scope:                fixtureScope,
		Reason:               reason,
		StartSequence:        startSequence,
		SnapshotDigest:       snapshotDigest,
	}
}

// startSSE switches the response to the SSE stream, mirroring the real
// server's frame layout.
func startSSE(w http.ResponseWriter) http.Flusher {
	flusher, ok := w.(http.Flusher)
	if !ok {
		panic("the test transport does not support flushing")
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	return flusher
}

// writeEventFrame emits id=eventId plus the field-identical EventProjection
// data, exactly like the frozen server.
func writeEventFrame(w http.ResponseWriter, flusher http.Flusher, event EventProjection) {
	data, err := json.Marshal(event)
	if err != nil {
		panic(err)
	}
	fmt.Fprintf(w, "id: %s\ndata: %s\n\n", event.EventID, data)
	flusher.Flush()
}

// writeResyncFrame emits the terminal event:resync frame of the frozen
// server.
func writeResyncFrame(w http.ResponseWriter, flusher http.Flusher, directive EventResync) {
	data, err := json.Marshal(directive)
	if err != nil {
		panic(err)
	}
	fmt.Fprintf(w, "event: resync\ndata: %s\n\n", data)
	flusher.Flush()
}

func collectStreamIDs(t *testing.T, stream *EventStream, count int) []string {
	t.Helper()
	ids := make([]string, 0, count)
	for len(ids) < count {
		if !stream.Next() {
			t.Fatalf("stream ended after %d events: %v", len(ids), stream.Err())
		}
		item := stream.Item()
		if item.Event == nil {
			t.Fatalf("item %d is not an event: %+v", len(ids)+1, item)
		}
		ids = append(ids, item.Event.EventID)
	}
	return ids
}

func assertIDs(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("event ids = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("event ids = %v, want %v", got, want)
		}
	}
}

func TestEventStreamHappyPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != APIPrefix+"/events" {
			t.Errorf("path = %q", r.URL.Path)
			return
		}
		verifyIdentity(t, w, r)
		flusher := startSSE(w)
		for sequence := uint64(1); sequence <= 3; sequence++ {
			writeEventFrame(w, flusher, fixtureEvent(sequence, fmt.Sprintf("evt-%d", sequence)))
		}
		fmt.Fprint(w, ": keep-alive\n\n")
		flusher.Flush()
		<-r.Context().Done()
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := client.Events(ctx, EventsOptions{})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	defer stream.Close()
	if stream.PollingFallback() {
		t.Errorf("healthy SSE stream reported as polling fallback")
	}
	ids := collectStreamIDs(t, stream, 3)
	assertIDs(t, ids, "evt-1", "evt-2", "evt-3")
	if stream.Cursor() != 3 || stream.LastEventID() != "evt-3" {
		t.Errorf("resume state = (%d, %q), want (3, evt-3)", stream.Cursor(), stream.LastEventID())
	}
	cancel()
	if stream.Next() {
		t.Fatalf("stream delivered an item after context cancellation")
	}
	if !errors.Is(stream.Err(), context.Canceled) {
		t.Errorf("stream error = %v, want context.Canceled", stream.Err())
	}
}

func TestEventStreamReconnectsWithLastEventID(t *testing.T) {
	var connections int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != APIPrefix+"/events" {
			t.Errorf("path = %q", r.URL.Path)
			return
		}
		verifyIdentity(t, w, r)
		connection := atomic.AddInt32(&connections, 1)
		flusher := startSSE(w)
		switch connection {
		case 1:
			writeEventFrame(w, flusher, fixtureEvent(1, "evt-1"))
			writeEventFrame(w, flusher, fixtureEvent(2, "evt-2"))
			return // the connection drops mid-stream
		default:
			if got := r.Header.Get("Last-Event-ID"); got != "evt-2" {
				t.Errorf("reconnect Last-Event-ID = %q, want evt-2", got)
			}
			writeEventFrame(w, flusher, fixtureEvent(3, "evt-3"))
			<-r.Context().Done()
		}
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := client.Events(ctx, EventsOptions{
		ReconnectDelay:    time.Millisecond,
		ReconnectAttempts: 5,
	})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	defer stream.Close()
	ids := collectStreamIDs(t, stream, 3)
	assertIDs(t, ids, "evt-1", "evt-2", "evt-3")
	if stream.Cursor() != 3 {
		t.Errorf("cursor = %d, want 3", stream.Cursor())
	}
	cancel()
}

func TestEventStreamSubscribesWithCursor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		verifyIdentity(t, w, r)
		if got := r.URL.Query().Get("cursor"); got != "2" {
			t.Errorf("cursor query = %q, want 2", got)
		}
		if got := r.Header.Get("Last-Event-ID"); got != "" {
			t.Errorf("Last-Event-ID = %q, want absent", got)
		}
		flusher := startSSE(w)
		writeEventFrame(w, flusher, fixtureEvent(3, "evt-3"))
		<-r.Context().Done()
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := client.Events(ctx, EventsOptions{Cursor: 2})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	defer stream.Close()
	ids := collectStreamIDs(t, stream, 1)
	assertIDs(t, ids, "evt-3")
	if stream.Cursor() != 3 {
		t.Errorf("cursor = %d, want 3", stream.Cursor())
	}
	cancel()
}

func TestEventStreamResyncDirectiveAtSubscribe(t *testing.T) {
	directive := fixtureResyncDirective("cursor-expired", 1, "sha256:snap-resync")
	var requests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		verifyIdentity(t, w, r)
		request := atomic.AddInt32(&requests, 1)
		if request == 1 {
			if got := r.URL.Query().Get("cursor"); got != "57" {
				t.Errorf("initial cursor = %q, want 57", got)
			}
			writeFakeJSON(w, http.StatusConflict, directive)
			return
		}
		if got := r.URL.Query().Get("cursor"); got != "" {
			t.Errorf("cursor after resume = %q, want absent", got)
		}
		flusher := startSSE(w)
		writeEventFrame(w, flusher, fixtureEvent(1, "evt-1"))
		<-r.Context().Done()
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := client.Events(ctx, EventsOptions{Cursor: 57})
	if err != nil {
		t.Fatalf("Events: %v, want the resync directive as a stream item", err)
	}
	defer stream.Close()

	if !stream.Next() {
		t.Fatalf("stream ended before the resync item: %v", stream.Err())
	}
	item := stream.Item()
	if item.Resync == nil || item.Event != nil {
		t.Fatalf("item = %+v, want resync directive", item)
	}
	if item.Resync.Reason != "cursor-expired" || item.Resync.StartSequence != 1 {
		t.Errorf("directive = %+v", item.Resync)
	}
	if item.Resync.SnapshotDigest != "sha256:snap-resync" {
		t.Errorf("snapshot digest = %q", item.Resync.SnapshotDigest)
	}
	if stream.Next() {
		t.Fatalf("stream continued while awaiting the resume decision")
	}
	resyncErr, ok := AsResyncRequired(stream.Err())
	if !ok {
		t.Fatalf("stream error = %v, want ResyncRequiredError", stream.Err())
	}
	if resyncErr.Directive.StartSequence != 1 {
		t.Errorf("awaiting directive = %+v", resyncErr.Directive)
	}
	if err := stream.Resume(); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	ids := collectStreamIDs(t, stream, 1)
	assertIDs(t, ids, "evt-1")
	if stream.Cursor() != 1 {
		t.Errorf("cursor after resume = %d, want 1", stream.Cursor())
	}
	cancel()
}

func TestEventStreamInStreamResyncFrame(t *testing.T) {
	directive := fixtureResyncDirective("projection-rebuilt", 1, "sha256:snap-2")
	var connections int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		verifyIdentity(t, w, r)
		connection := atomic.AddInt32(&connections, 1)
		flusher := startSSE(w)
		switch connection {
		case 1:
			writeEventFrame(w, flusher, fixtureEvent(1, "evt-1"))
			writeResyncFrame(w, flusher, directive)
			return
		default:
			if got := r.URL.Query().Get("cursor"); got != "" {
				t.Errorf("cursor after rebuild = %q, want absent", got)
			}
			// the rebuilt epoch re-projects the identical event identity
			writeEventFrame(w, flusher, fixtureEvent(1, "evt-1"))
			<-r.Context().Done()
		}
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := client.Events(ctx, EventsOptions{ReconnectDelay: time.Millisecond})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	defer stream.Close()

	ids := collectStreamIDs(t, stream, 1)
	assertIDs(t, ids, "evt-1")

	if !stream.Next() {
		t.Fatalf("stream ended before the terminal resync frame: %v", stream.Err())
	}
	item := stream.Item()
	if item.Resync == nil {
		t.Fatalf("item = %+v, want resync directive", item)
	}
	if item.Resync.Reason != "projection-rebuilt" || item.Resync.SnapshotDigest != "sha256:snap-2" {
		t.Errorf("directive = %+v", item.Resync)
	}
	if stream.Next() {
		t.Fatalf("stream continued while awaiting the resume decision")
	}
	if !IsResyncRequired(stream.Err()) {
		t.Fatalf("stream error = %v, want resync required", stream.Err())
	}
	if err := stream.Resume(); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	// the rebuilt epoch delivers the identical eventId again: the dedupe
	// state was reset by the resume decision
	ids = collectStreamIDs(t, stream, 1)
	assertIDs(t, ids, "evt-1")
	if stream.Cursor() != 1 {
		t.Errorf("cursor after rebuild = %d, want 1", stream.Cursor())
	}
	cancel()
}

func TestEventStreamFallsBackToPolling(t *testing.T) {
	var pollRequests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case APIPrefix + "/events":
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("hijack: %v", err)
				return
			}
			_ = conn.Close() // the SSE transport is unavailable
		case APIPrefix + "/events/poll":
			verifyIdentity(t, w, r)
			atomic.AddInt32(&pollRequests, 1)
			cursor, _ := strconv.ParseUint(r.URL.Query().Get("cursor"), 10, 64)
			page := EventPage{
				APIVersion:           "marshal.dev/v1alpha1",
				Kind:                 "EventPage",
				AuthorityNamespaceId: fixtureNamespace,
				Scope:                fixtureScope,
				SnapshotDigest:       "sha256:snap-poll",
			}
			switch cursor {
			case 0:
				page.Events = []EventProjection{fixtureEvent(1, "evt-1"), fixtureEvent(2, "evt-2")}
				page.NextCursor = 2
			case 2:
				// at-least-once overlap: evt-2 is redelivered
				page.Events = []EventProjection{fixtureEvent(2, "evt-2"), fixtureEvent(3, "evt-3")}
				page.NextCursor = 3
			default:
				page.Events = []EventProjection{}
				page.NextCursor = cursor
			}
			writeFakeJSON(w, http.StatusOK, page)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := client.Events(ctx, EventsOptions{PollInterval: time.Millisecond})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	defer stream.Close()
	if !stream.PollingFallback() {
		t.Errorf("transport-level SSE failure did not degrade to the polling fallback")
	}
	ids := collectStreamIDs(t, stream, 3)
	assertIDs(t, ids, "evt-1", "evt-2", "evt-3")
	if stream.Cursor() != 3 || stream.LastEventID() != "evt-3" {
		t.Errorf("resume state = (%d, %q), want (3, evt-3)", stream.Cursor(), stream.LastEventID())
	}
	if atomic.LoadInt32(&pollRequests) == 0 {
		t.Errorf("the polling fallback never polled")
	}
	cancel()
	if stream.Next() {
		t.Fatalf("stream delivered an item after context cancellation")
	}
	if !errors.Is(stream.Err(), context.Canceled) {
		t.Errorf("stream error = %v, want context.Canceled", stream.Err())
	}
}

func TestEventStreamPollingOnlyResync(t *testing.T) {
	directive := fixtureResyncDirective("cursor-backlog-overflow", 3, "sha256:snap-3")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != APIPrefix+"/events/poll" {
			t.Errorf("path = %q", r.URL.Path)
			return
		}
		verifyIdentity(t, w, r)
		cursor, _ := strconv.ParseUint(r.URL.Query().Get("cursor"), 10, 64)
		switch cursor {
		case 0:
			writeFakeJSON(w, http.StatusConflict, directive)
		case 2:
			page := EventPage{
				APIVersion:           "marshal.dev/v1alpha1",
				Kind:                 "EventPage",
				AuthorityNamespaceId: fixtureNamespace,
				Scope:                fixtureScope,
				Events:               []EventProjection{fixtureEvent(3, "evt-3"), fixtureEvent(4, "evt-4")},
				NextCursor:           4,
				SnapshotDigest:       "sha256:snap-3",
			}
			writeFakeJSON(w, http.StatusOK, page)
		default:
			page := EventPage{
				APIVersion:           "marshal.dev/v1alpha1",
				Kind:                 "EventPage",
				AuthorityNamespaceId: fixtureNamespace,
				Scope:                fixtureScope,
				Events:               []EventProjection{},
				NextCursor:           cursor,
				SnapshotDigest:       "sha256:snap-3",
			}
			writeFakeJSON(w, http.StatusOK, page)
		}
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := client.Events(ctx, EventsOptions{PollingOnly: true, PollInterval: time.Millisecond})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	defer stream.Close()
	if !stream.PollingFallback() {
		t.Errorf("PollingOnly stream not in polling mode")
	}
	if !stream.Next() {
		t.Fatalf("stream ended before the resync item: %v", stream.Err())
	}
	item := stream.Item()
	if item.Resync == nil {
		t.Fatalf("item = %+v, want resync directive", item)
	}
	if item.Resync.Reason != "cursor-backlog-overflow" || item.Resync.StartSequence != 3 {
		t.Errorf("directive = %+v", item.Resync)
	}
	if err := stream.Resume(); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	ids := collectStreamIDs(t, stream, 2)
	assertIDs(t, ids, "evt-3", "evt-4")
	if stream.Cursor() != 4 {
		t.Errorf("cursor = %d, want 4", stream.Cursor())
	}
	cancel()
}

func TestEventStreamGapFailsClosed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		verifyIdentity(t, w, r)
		flusher := startSSE(w)
		writeEventFrame(w, flusher, fixtureEvent(1, "evt-1"))
		writeEventFrame(w, flusher, fixtureEvent(3, "evt-3")) // gap: sequence 2 is missing
		<-r.Context().Done()
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := client.Events(ctx, EventsOptions{})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	defer stream.Close()
	ids := collectStreamIDs(t, stream, 1)
	assertIDs(t, ids, "evt-1")
	if stream.Next() {
		t.Fatalf("stream continued over a ledger gap")
	}
	var gap *SequenceGapError
	if !errors.As(stream.Err(), &gap) {
		t.Fatalf("stream error = %v, want SequenceGapError", stream.Err())
	}
	if gap.Expected != 2 || gap.Got != 3 {
		t.Errorf("gap = %+v, want expected 2 got 3", gap)
	}
}

func TestEventStreamAPIErrorFailsClosedWithoutFallback(t *testing.T) {
	var pollHits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case APIPrefix + "/events":
			writeFakeJSON(w, http.StatusForbidden, fakeError(CodeForbiddenIdentity,
				"forbidden-header:Marshal-Workload-Role",
				"public-api requests must not carry dispatch-bound identity"))
		case APIPrefix + "/events/poll":
			atomic.AddInt32(&pollHits, 1)
			writeFakeJSON(w, http.StatusOK, EventPage{})
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := client.Events(ctx, EventsOptions{})
	if err == nil {
		stream.Close()
		t.Fatalf("Events succeeded, want the API rejection")
	}
	if !errors.Is(err, ErrForbiddenIdentity) {
		t.Errorf("error = %v, want ErrForbiddenIdentity", err)
	}
	if got := atomic.LoadInt32(&pollHits); got != 0 {
		t.Errorf("the polling fallback masked the API rejection (%d polls)", got)
	}
}
