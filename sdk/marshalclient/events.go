package marshalclient

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Frozen defaults of the event stream consumption.
const (
	// DefaultPollInterval bounds how long an empty polling page may stay
	// stale before the next poll.
	DefaultPollInterval = time.Second
	// DefaultPollLimit is the page size of the polling fallback.
	DefaultPollLimit = 100
	// DefaultReconnectAttempts bounds how many consecutive transport-level
	// SSE failures the stream tolerates before falling back to polling.
	DefaultReconnectAttempts = 3
	// DefaultReconnectDelay is the delay between SSE reconnect attempts.
	DefaultReconnectDelay = 250 * time.Millisecond
)

// EventsOptions configures one EventStream.
type EventsOptions struct {
	// Cursor is the last consumed ledgerSequence (exclusive lower bound) of
	// a previous subscription; zero replays the full backlog from
	// ledgerSequence 1. Cursor takes precedence over LastEventID.
	Cursor uint64
	// LastEventID resumes from one eventId instead of a numeric cursor
	// (the Last-Event-ID reconnection spelling). It is only used when
	// Cursor is zero.
	LastEventID string
	// PollingOnly skips the SSE stream entirely and consumes the identical
	// projection through the polling fallback.
	PollingOnly bool
	// PollInterval is the delay after an empty polling page; zero or
	// negative selects DefaultPollInterval.
	PollInterval time.Duration
	// PollLimit is the page size of the polling fallback; zero selects
	// DefaultPollLimit, values above 1000 are clamped to 1000.
	PollLimit int
	// ReconnectAttempts bounds the consecutive transport-level SSE failures
	// before the stream falls back to polling; zero or negative selects
	// DefaultReconnectAttempts.
	ReconnectAttempts int
	// ReconnectDelay is the delay between SSE reconnect attempts; zero or
	// negative selects DefaultReconnectDelay.
	ReconnectDelay time.Duration
}

// StreamItem is one item delivered by an EventStream: either one
// EventProjection of the read-only ledger projection, or one deterministic
// EventResync directive exposed to the caller for an explicit resume
// decision. The SDK never silently swallows a resync directive.
type StreamItem struct {
	// Event is set for projection data frames.
	Event *EventProjection
	// Resync is set for deterministic resync directives.
	Resync *EventResync
}

type streamMode int

const (
	modeSSE streamMode = iota
	modePolling
)

// EventStream consumes the read-only event projection of the public-api
// Port. It is an explicit iterator: Next advances to the next item, Item
// exposes it, and Err reports the terminal state.
//
// Consumption semantics:
//
//   - events are consumed by monotonically increasing ledgerSequence; a gap
//     fails closed with a SequenceGapError instead of silently continuing;
//   - delivery is at-least-once; duplicates are deduplicated by eventId;
//   - a lost SSE connection is resumed with the last consumed eventId
//     (Last-Event-ID) or cursor;
//   - one deterministic EventResync directive (HTTP 409 or the terminal
//     event:resync frame) is exposed as a typed item and pauses the stream
//     until the caller resumes from the directive's startSequence;
//   - transport-level SSE unavailability degrades to the polling fallback
//     over the identical projection data, preserving deduplication;
//   - API-level rejections (identity, scope, protocol) fail closed and are
//     never masked by the fallback.
//
// An EventStream is not safe for concurrent use; drive it from a single
// goroutine.
type EventStream struct {
	client *Client
	ctx    context.Context

	pollInterval      time.Duration
	pollLimit         int
	reconnectAttempts int
	reconnectDelay    time.Duration

	mode streamMode

	cursor      uint64
	lastEventID string
	seen        map[string]struct{}

	response *http.Response
	reader   *bufio.Reader

	pending []EventProjection

	item StreamItem

	pendingResync  *EventResync
	activeResync   *EventResync
	awaitingResume bool

	failuresSinceProgress int

	terminated bool
	closed     bool
	err        error
}

// Events subscribes to the read-only event projection (GET /v1alpha1/events)
// and returns the established stream. The SSE subscription is established
// eagerly: an API-level rejection fails closed and is returned, while a
// transport-level failure degrades the stream to the polling fallback. A
// deterministic resync directive observed at establishment is not an error:
// it is exposed as the first stream item.
func (c *Client) Events(ctx context.Context, options EventsOptions) (*EventStream, error) {
	if ctx == nil {
		return nil, errors.New("marshalclient: Events requires a non-nil context")
	}
	stream := &EventStream{
		client:            c,
		ctx:               ctx,
		cursor:            options.Cursor,
		pollInterval:      options.PollInterval,
		pollLimit:         options.PollLimit,
		reconnectAttempts: options.ReconnectAttempts,
		reconnectDelay:    options.ReconnectDelay,
		seen:              map[string]struct{}{},
		mode:              modeSSE,
	}
	if stream.pollInterval <= 0 {
		stream.pollInterval = DefaultPollInterval
	}
	if stream.pollLimit <= 0 {
		stream.pollLimit = DefaultPollLimit
	}
	if stream.pollLimit > 1000 {
		stream.pollLimit = 1000
	}
	if stream.reconnectAttempts <= 0 {
		stream.reconnectAttempts = DefaultReconnectAttempts
	}
	if stream.reconnectDelay <= 0 {
		stream.reconnectDelay = DefaultReconnectDelay
	}
	if options.Cursor == 0 {
		stream.lastEventID = strings.TrimSpace(options.LastEventID)
	}
	if options.PollingOnly {
		stream.mode = modePolling
		return stream, nil
	}
	err := stream.connectSSE()
	if err == nil {
		return stream, nil
	}
	var resync *ResyncRequiredError
	if errors.As(err, &resync) {
		directive := resync.Directive
		stream.pendingResync = &directive
		return stream, nil
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		// Fail closed: an identity-level rejection applies to the polling
		// fallback identically and must never be masked by it.
		return nil, err
	}
	// Transport-level: the SSE endpoint is unavailable, degrade to the
	// polling fallback over the identical projection data.
	stream.mode = modePolling
	return stream, nil
}

// Next advances the stream to the next item. It returns true when a new
// item (one event or one resync directive) is available via Item, and false
// when the stream is paused awaiting a resume decision, terminated or
// closed; Err reports why.
func (s *EventStream) Next() bool {
	for {
		if s.closed || s.terminated {
			return false
		}
		if s.pendingResync != nil {
			directive := *s.pendingResync
			s.pendingResync = nil
			s.activeResync = &directive
			s.awaitingResume = true
			s.item = StreamItem{Resync: &directive}
			return true
		}
		if s.awaitingResume {
			return false
		}
		if err := s.ctx.Err(); err != nil {
			s.finish(err)
			return false
		}
		switch s.mode {
		case modeSSE:
			if s.reader == nil {
				if !s.redialSSE() {
					// Either a resync item is pending, the stream
					// terminated, or the stream degraded to polling; the
					// loop top re-evaluates each state.
					continue
				}
			}
			frame, err := s.readFrame()
			if err != nil {
				s.closeConnection()
				if ctxErr := s.ctx.Err(); ctxErr != nil {
					s.finish(ctxErr)
					return false
				}
				var violation *ProtocolViolationError
				if errors.As(err, &violation) {
					s.finish(err)
					return false
				}
				s.noteTransportFailure()
				continue
			}
			if frame.Event != nil {
				if !s.accept(*frame.Event) {
					if s.terminated {
						return false
					}
					continue
				}
				s.failuresSinceProgress = 0
				return true
			}
			if frame.Resync != nil {
				s.pendingResync = frame.Resync
				s.closeConnection()
				continue
			}
		case modePolling:
			if len(s.pending) == 0 {
				if !s.fetchPollPage() {
					// A resync item is pending or the stream terminated.
					continue
				}
				if len(s.pending) == 0 {
					if !s.sleep(s.pollInterval) {
						s.finish(s.ctx.Err())
						return false
					}
					continue
				}
			}
			event := s.pending[0]
			s.pending = s.pending[1:]
			if !s.accept(event) {
				if s.terminated {
					return false
				}
				continue
			}
			s.failuresSinceProgress = 0
			return true
		}
	}
}

// Item returns the current stream item; it is valid until the next Next
// call.
func (s *EventStream) Item() StreamItem { return s.item }

// Err reports the terminal error of the stream. While the stream awaits a
// resume decision it returns the typed ResyncRequiredError carrying the
// directive. It returns nil while the stream is healthy.
func (s *EventStream) Err() error {
	if s.awaitingResume && s.activeResync != nil {
		return &ResyncRequiredError{Directive: *s.activeResync}
	}
	return s.err
}

// Resume rebuilds the subscription from the pending deterministic resync
// directive: the resume point becomes the directive's startSequence, the
// eventId deduplication state is reset for the new projection epoch, and
// the next Next call reconnects or repolls from that point. It is an error
// to call Resume without a pending resync directive.
func (s *EventStream) Resume() error {
	if !s.awaitingResume || s.activeResync == nil {
		return errors.New("marshalclient: no pending resync directive to resume from")
	}
	start := s.activeResync.StartSequence
	if start == 0 {
		start = 1
	}
	s.cursor = start - 1
	s.lastEventID = ""
	s.seen = map[string]struct{}{}
	s.pending = nil
	s.awaitingResume = false
	s.activeResync = nil
	s.closeConnection()
	return nil
}

// Cursor returns the last consumed ledgerSequence (the exclusive lower
// bound of the next delivered event); zero means nothing was consumed yet.
func (s *EventStream) Cursor() uint64 { return s.cursor }

// LastEventID returns the eventId of the last consumed event.
func (s *EventStream) LastEventID() string { return s.lastEventID }

// PollingFallback reports whether the stream currently consumes the polling
// fallback instead of the SSE stream.
func (s *EventStream) PollingFallback() bool { return s.mode == modePolling }

// Close releases the stream's connection. It is idempotent.
func (s *EventStream) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	s.closeConnection()
	return nil
}

// accept applies the at-least-once consumption semantics to one projected
// event: duplicates are deduplicated by eventId, already consumed sequences
// are skipped, and a gap fails closed. It returns true when the event
// became the current item.
func (s *EventStream) accept(event EventProjection) bool {
	if event.EventID == "" || event.LedgerSequence == 0 {
		s.finish(&ProtocolViolationError{Detail: "event projection without eventId or ledgerSequence"})
		return false
	}
	if _, duplicate := s.seen[event.EventID]; duplicate {
		return false
	}
	if event.LedgerSequence <= s.cursor {
		return false
	}
	if event.LedgerSequence != s.cursor+1 {
		s.finish(&SequenceGapError{Expected: s.cursor + 1, Got: event.LedgerSequence})
		return false
	}
	s.seen[event.EventID] = struct{}{}
	s.cursor = event.LedgerSequence
	s.lastEventID = event.EventID
	s.item = StreamItem{Event: &event}
	return true
}

// connectSSE establishes one SSE subscription bound to the current resume
// state: the lastEventID header spelling takes precedence over the cursor
// query parameter, and an absent or zero cursor replays from sequence 1.
func (s *EventStream) connectSSE() error {
	query := url.Values{}
	var extraHeaders map[string]string
	if s.lastEventID != "" {
		extraHeaders = map[string]string{"Last-Event-ID": s.lastEventID}
	} else if s.cursor > 0 {
		query.Set("cursor", strconv.FormatUint(s.cursor, 10))
	}
	response, err := s.client.execute(s.ctx, http.MethodGet, APIPrefix+"/events", query, nil, "text/event-stream", extraHeaders)
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		data, readErr := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
		response.Body.Close()
		if readErr != nil {
			return &TransportError{Op: "read response", Err: readErr}
		}
		return mapHTTPError(response.StatusCode, data)
	}
	if contentType := response.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "text/event-stream") {
		response.Body.Close()
		return &TransportError{Op: "subscribe events", Err: errors.New("the response is not text/event-stream")}
	}
	s.response = response
	s.reader = bufio.NewReaderSize(response.Body, 64*1024)
	return nil
}

// redialSSE attempts one SSE reconnect after a transport-level failure. It
// returns true when the subscription is re-established, sets a pending
// resync item, terminates the stream on API-level rejection, or degrades
// the stream to the polling fallback after too many consecutive failures.
func (s *EventStream) redialSSE() bool {
	if s.failuresSinceProgress > 0 {
		if !s.sleep(s.reconnectDelay) {
			s.finish(s.ctx.Err())
			return false
		}
	}
	err := s.connectSSE()
	if err == nil {
		return true
	}
	var resync *ResyncRequiredError
	if errors.As(err, &resync) {
		directive := resync.Directive
		s.pendingResync = &directive
		return false
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		s.finish(err)
		return false
	}
	if ctxErr := s.ctx.Err(); ctxErr != nil {
		s.finish(ctxErr)
		return false
	}
	s.noteTransportFailure()
	return false
}

// noteTransportFailure counts one transport-level failure without progress
// and degrades the stream to the polling fallback once the bounded budget
// is exhausted.
func (s *EventStream) noteTransportFailure() {
	s.failuresSinceProgress++
	if s.failuresSinceProgress > s.reconnectAttempts {
		s.mode = modePolling
		s.failuresSinceProgress = 0
	}
}

// fetchPollPage reads the next page of the polling fallback at the current
// cursor. It returns true when a page was fetched (possibly empty), sets a
// pending resync item on the deterministic 409 directive, and terminates
// the stream on any other error.
func (s *EventStream) fetchPollPage() bool {
	page, err := s.client.PollEvents(s.ctx, s.cursor, s.pollLimit)
	if err != nil {
		var resync *ResyncRequiredError
		if errors.As(err, &resync) {
			directive := resync.Directive
			s.pendingResync = &directive
			return false
		}
		s.finish(err)
		return false
	}
	s.pending = append(s.pending, page.Events...)
	return true
}

// readFrame reads one complete SSE frame of the current connection. It
// returns frames carrying data only: projection data frames become
// StreamItem.Event, the terminal event:resync frame becomes
// StreamItem.Resync. Comment frames (heartbeats) are consumed silently.
// Transport failures return the underlying error; malformed frames fail
// closed with a ProtocolViolationError.
func (s *EventStream) readFrame() (StreamItem, error) {
	var eventType string
	var dataLines []string
	for {
		line, readErr := s.reader.ReadString('\n')
		if len(line) > 0 {
			line = strings.TrimSuffix(line, "\n")
			line = strings.TrimSuffix(line, "\r")
			switch {
			case line == "":
				data := strings.Join(dataLines, "\n")
				frameType := eventType
				eventType = ""
				dataLines = nil
				if data != "" {
					if frameType == "resync" {
						var directive EventResync
						if err := json.Unmarshal([]byte(data), &directive); err != nil {
							return StreamItem{}, &ProtocolViolationError{Detail: "invalid resync frame"}
						}
						return StreamItem{Resync: &directive}, nil
					}
					var event EventProjection
					if err := json.Unmarshal([]byte(data), &event); err != nil {
						return StreamItem{}, &ProtocolViolationError{Detail: "invalid event frame"}
					}
					return StreamItem{Event: &event}, nil
				}
			case strings.HasPrefix(line, ":"):
				// Comment frame (keep-alive heartbeat): ignore.
			default:
				field, value, _ := strings.Cut(line, ":")
				value = strings.TrimPrefix(value, " ")
				switch field {
				case "event":
					eventType = value
				case "data":
					dataLines = append(dataLines, value)
				case "id", "retry":
					// The frame id is the eventId carried field-identically
					// inside the data payload; retry is not part of the
					// frozen protocol. Both are consumed and ignored.
				}
			}
		}
		if readErr != nil {
			return StreamItem{}, readErr
		}
	}
}

// sleep waits one delay bounded by the stream context. It returns false
// when the context ended first.
func (s *EventStream) sleep(delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-s.ctx.Done():
		return false
	}
}

// closeConnection releases the current SSE connection, when any.
func (s *EventStream) closeConnection() {
	if s.response != nil {
		s.response.Body.Close()
		s.response = nil
	}
	s.reader = nil
}

// finish terminates the stream with one error and releases its connection.
func (s *EventStream) finish(err error) {
	if s.err == nil {
		s.err = err
	}
	s.terminated = true
	s.closeConnection()
}
