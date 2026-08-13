// SSE read-only event projection of the public-api Port (ADR 0018 §4/§14).
//
// The append-only Run journals under the state root are the only authority;
// this file projects them as a rebuildable, never-authoritative event stream:
//
//   - cursor identity is (authorityNamespaceId, scope, ledgerSequence); the
//     cursor is the exclusive lower bound of delivered sequences, absent or
//     zero cursor replays the full backlog from ledgerSequence 1;
//   - delivery is at-least-once; clients deduplicate by eventId/sequence and
//     the server never assumes exactly-once consumption;
//   - an expired, gap or unservable cursor yields the deterministic
//     EventResync directive (resume point plus snapshot digest), never a
//     silent continuation and never an incomplete replay;
//   - every SSE data frame carries id=eventId and the field-identical
//     EventProjection JSON the polling fallback /events/poll returns for the
//     same sequence: one constructor feeds both channels, and no other frame
//     kind is ever emitted;
//   - periodic re-Authorization plus immediate re-Authorization on sensitive
//     change; any failed check closes the connection fail closed, never
//     degraded to full visibility;
//   - per-subscriber buffers are bounded: a slow subscriber is disconnected
//     and guided to resync, and the event source (the runstore notification
//     path) is never blocked by subscribers.
//
// The event source is the read-only adaptation of runstore's existing
// MARSHAL_NOTIFY_CMD notification hook: HandleNotifyHook accepts the hook's
// JSON payload and wakes the projection, while a bounded background scan
// keeps the projection eventually consistent even when no notification is
// wired. The runstore package itself is never modified.
//
// The stream is strictly read-only: it carries no business ACK, no lease
// heartbeat and no command frames.
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

// Frozen document kinds of the SSE projection family. KindEventProjection is
// the only data frame kind ever emitted on the stream; the retired
// kind:EventCursor emission path does not exist in this implementation.
const (
	KindEventProjection = "EventProjection"
	KindEventPage       = "EventPage"
	KindEventResync     = "EventResync"
)

// Frozen parameter defaults of the projection (ADR 0018 §14 leaves the
// concrete values to this schema revision).
const (
	defaultEventWatchInterval   = 25 * time.Millisecond
	defaultSSEBufferLimit       = 256
	defaultSSEHeartbeatInterval = 15 * time.Second
	defaultSSEReauthzInterval   = 30 * time.Second
	defaultPollLimit            = 100
	maxPollLimit                = 1000
)

// Frozen resync reasons of the EventResync directive.
const (
	resyncReasonCursorExpired     = "cursor-expired"
	resyncReasonProjectionRebuilt = "projection-rebuilt"
	resyncReasonBackpressureFull  = "backpressure-buffer-full"
	resyncReasonBacklogOverflow   = "cursor-backlog-overflow"
)

// NotifyHookEnv names the runstore state-transition notification hook this
// projection adapts to read-only (issue #78). The runstore package itself is
// never modified.
const NotifyHookEnv = "MARSHAL_NOTIFY_CMD"

// EventProjection is the frozen read-only projection of one Run ledger
// event. One constructor feeds both the SSE data frames and the polling
// fallback, so the JSON is field-identical across the two channels.
type EventProjection struct {
	APIVersion           domain.APIVersion              `json:"apiVersion"`
	Kind                 string                         `json:"kind"`
	AuthorityNamespaceId authority.AuthorityNamespaceId `json:"authorityNamespaceId"`
	Scope                string                         `json:"scope"`
	EventID              string                         `json:"eventId"`
	LedgerSequence       uint64                         `json:"ledgerSequence"`
	RunID                string                         `json:"runId"`
	RunSequence          uint64                         `json:"runSequence"`
	TaskID               string                         `json:"taskId,omitempty"`
	AttemptID            string                         `json:"attemptId,omitempty"`
	Type                 string                         `json:"type"`
	StateFrom            domain.State                   `json:"stateFrom,omitempty"`
	StateTo              domain.State                   `json:"stateTo,omitempty"`
	Timestamp            time.Time                      `json:"timestamp"`
	PayloadDigest        string                         `json:"payloadDigest"`
}

// EventPage is one polling-fallback page of the projection: the identical
// EventProjection records the SSE stream delivers, plus the cursor and
// snapshot binding of the page.
type EventPage struct {
	APIVersion           domain.APIVersion              `json:"apiVersion"`
	Kind                 string                         `json:"kind"`
	AuthorityNamespaceId authority.AuthorityNamespaceId `json:"authorityNamespaceId"`
	Scope                string                         `json:"scope"`
	Events               []EventProjection              `json:"events"`
	NextCursor           uint64                         `json:"nextCursor"`
	SnapshotDigest       string                         `json:"snapshotDigest"`
}

// EventResync is the deterministic resync directive (ADR 0018 §4/§14): the
// cursor is expired, gap or unservable, and the subscription must be rebuilt
// from StartSequence against the SnapshotDigest. It is never a silent
// continuation and never an incomplete replay, and it is deliberately not
// the Error envelope.
type EventResync struct {
	APIVersion           domain.APIVersion              `json:"apiVersion"`
	Kind                 string                         `json:"kind"`
	AuthorityNamespaceId authority.AuthorityNamespaceId `json:"authorityNamespaceId"`
	Scope                string                         `json:"scope"`
	Reason               string                         `json:"reason"`
	StartSequence        uint64                         `json:"startSequence"`
	SnapshotDigest       string                         `json:"snapshotDigest"`
}

// NotifyHookPayload mirrors the JSON argument runstore passes to the command
// named by MARSHAL_NOTIFY_CMD after a journaled state transition.
type NotifyHookPayload struct {
	RunID         string       `json:"runId"`
	TaskID        string       `json:"taskId"`
	StateFrom     domain.State `json:"stateFrom"`
	StateTo       domain.State `json:"stateTo"`
	EventSequence uint64       `json:"eventSequence"`
	Timestamp     time.Time    `json:"timestamp"`
}

// ingestKey identifies one journaled event inside the projection.
type ingestKey struct {
	runID       string
	runSequence uint64
}

// ingestedEvent remembers how one journaled event was projected, so journal
// compaction or rewrite of already projected events is detected.
type ingestedEvent struct {
	eventID        string
	ledgerSequence uint64
}

// Projection is the rebuildable read-only projection of the Run journals
// into one scope-monotonic ledger. It is safe for concurrent use; the
// journal store is only ever read, never written.
type Projection struct {
	stateRoot     string
	namespace     authority.AuthorityNamespaceId
	store         *runstore.Store
	watchInterval time.Duration

	mu        sync.Mutex
	epoch     uint64
	ledger    []EventProjection
	ingested  map[ingestKey]ingestedEvent
	byEventID map[string]uint64
	hub       *sseHub

	stop      chan struct{}
	wakeup    chan struct{}
	closeOnce sync.Once
	closed    bool
}

func newProjection(stateRoot string, namespace authority.AuthorityNamespaceId, store *runstore.Store, watchInterval time.Duration) *Projection {
	if watchInterval <= 0 {
		watchInterval = defaultEventWatchInterval
	}
	return &Projection{
		stateRoot:     stateRoot,
		namespace:     namespace,
		store:         store,
		watchInterval: watchInterval,
		epoch:         1,
		ingested:      map[ingestKey]ingestedEvent{},
		byEventID:     map[string]uint64{},
		hub:           newSSEHub(),
		stop:          make(chan struct{}),
		wakeup:        make(chan struct{}, 1),
	}
}

// Close stops the background watcher and releases every active subscription
// with a deterministic resync directive. It is idempotent.
func (p *Projection) Close() error {
	p.closeOnce.Do(func() {
		p.mu.Lock()
		p.closed = true
		directive := p.resyncLocked(resyncReasonProjectionRebuilt, 1)
		p.mu.Unlock()
		close(p.stop)
		p.hub.broadcastResync(directive)
	})
	return nil
}

// wake requests one immediate out-of-band scan. It is the read-only
// adaptation point of the MARSHAL_NOTIFY_CMD hook and never blocks.
func (p *Projection) wake() {
	select {
	case p.wakeup <- struct{}{}:
	default:
	}
}

// watch reconciles the projection against the journals until Close: one
// bounded scan per interval plus immediate scans on notify wakes.
func (p *Projection) watch() {
	ticker := time.NewTicker(p.watchInterval)
	defer ticker.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-p.wakeup:
			p.ScanNow()
		case <-ticker.C:
			p.ScanNow()
		}
	}
}

// ScanNow reconciles the projection against the current journals once. It is
// the single ingest path: freshly journaled events receive the next
// ledgerSequence values, already projected events keep theirs, and a journal
// that lost or rewrote projected events rebuilds the projection and issues
// resync directives instead of silently continuing.
func (p *Projection) ScanNow() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.scanLocked()
}

// journalEvent is one read-only view of one Run journal.
type journalEvent struct {
	runID  string
	events []domain.RunEvent
}

func (p *Projection) readJournalsLocked() []journalEvent {
	runsDirectory := filepath.Join(p.stateRoot, "runs")
	entries, err := os.ReadDir(runsDirectory)
	if err != nil {
		// Absent or unreadable run space: keep the current projection.
		// The projection fails closed by projecting nothing new rather
		// than fabricating events.
		return nil
	}
	journals := make([]journalEvent, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		events, _, readErr := p.store.ReadEvents(entry.Name())
		if readErr != nil && !errors.Is(readErr, runstore.ErrTruncatedTail) {
			// Unreadable (or not yet existing) journal: never project
			// what cannot be read fail closed.
			continue
		}
		journals = append(journals, journalEvent{runID: entry.Name(), events: events})
	}
	return journals
}

func (p *Projection) scanLocked() {
	journals := p.readJournalsLocked()
	present := make(map[ingestKey]string)
	for _, journal := range journals {
		for _, event := range journal.events {
			present[ingestKey{journal.runID, event.Sequence}] = event.EventID
		}
	}
	for key, record := range p.ingested {
		eventID, ok := present[key]
		if !ok || eventID != record.eventID {
			// A projected event was compacted or rewritten: the ledger
			// can no longer continue silently from existing cursors.
			p.rebuildLocked(journals)
			return
		}
	}
	var fresh []EventProjection
	for _, journal := range journals {
		for _, event := range journal.events {
			key := ingestKey{journal.runID, event.Sequence}
			if _, already := p.ingested[key]; already {
				continue
			}
			projected := p.projectEventLocked(journal.runID, event)
			p.ledger = append(p.ledger, projected)
			p.ingested[key] = ingestedEvent{eventID: event.EventID, ledgerSequence: projected.LedgerSequence}
			p.byEventID[event.EventID] = projected.LedgerSequence
			fresh = append(fresh, projected)
		}
	}
	if len(fresh) > 0 {
		p.hub.publish(fresh, p.resyncLocked(resyncReasonBackpressureFull, 1))
	}
}

func (p *Projection) rebuildLocked(journals []journalEvent) {
	p.epoch++
	p.ledger = nil
	p.ingested = map[ingestKey]ingestedEvent{}
	p.byEventID = map[string]uint64{}
	for _, journal := range journals {
		for _, event := range journal.events {
			key := ingestKey{journal.runID, event.Sequence}
			if _, already := p.ingested[key]; already {
				continue
			}
			projected := p.projectEventLocked(journal.runID, event)
			p.ledger = append(p.ledger, projected)
			p.ingested[key] = ingestedEvent{eventID: event.EventID, ledgerSequence: projected.LedgerSequence}
			p.byEventID[event.EventID] = projected.LedgerSequence
		}
	}
	p.hub.broadcastResync(p.resyncLocked(resyncReasonProjectionRebuilt, 1))
}

// projectEventLocked is the single constructor of the projection shared by
// the initial ingest and every rebuild, so both channels always observe the
// identical field set.
func (p *Projection) projectEventLocked(runID string, event domain.RunEvent) EventProjection {
	payloadDigest := ""
	if event.Payload != nil {
		if data, err := json.Marshal(event.Payload); err == nil {
			if digest, err := canonical.DigestJSON(data); err == nil {
				payloadDigest = digest
			}
		}
	}
	taskID, _ := event.Payload["taskId"].(string)
	return EventProjection{
		APIVersion:           domain.APIVersionV1Alpha1,
		Kind:                 KindEventProjection,
		AuthorityNamespaceId: p.namespace,
		Scope:                p.namespace.AuthorityScopeId,
		EventID:              event.EventID,
		LedgerSequence:       uint64(len(p.ledger)) + 1,
		RunID:                runID,
		RunSequence:          event.Sequence,
		TaskID:               taskID,
		AttemptID:            event.AttemptID,
		Type:                 event.Type,
		StateFrom:            event.StateFrom,
		StateTo:              event.StateTo,
		Timestamp:            event.Timestamp.UTC(),
		PayloadDigest:        payloadDigest,
	}
}

// snapshotDigestLocked digests the complete current projection content and
// epoch, so identical projection states yield identical digests and any
// rebuild changes the digest deterministically.
func (p *Projection) snapshotDigestLocked() string {
	eventIDs := make([]string, len(p.ledger))
	for index, projected := range p.ledger {
		eventIDs[index] = projected.EventID
	}
	summary := struct {
		Epoch        uint64   `json:"epoch"`
		Scope        string   `json:"scope"`
		LastSequence uint64   `json:"lastSequence"`
		EventIDs     []string `json:"eventIds"`
	}{p.epoch, p.namespace.AuthorityScopeId, uint64(len(p.ledger)), eventIDs}
	data, err := json.Marshal(summary)
	if err != nil {
		return ""
	}
	digest, err := canonical.DigestJSON(data)
	if err != nil {
		return canonical.DigestBytes(data)
	}
	return digest
}

func (p *Projection) resyncLocked(reason string, startSequence uint64) EventResync {
	return EventResync{
		APIVersion:           domain.APIVersionV1Alpha1,
		Kind:                 KindEventResync,
		AuthorityNamespaceId: p.namespace,
		Scope:                p.namespace.AuthorityScopeId,
		Reason:               reason,
		StartSequence:        startSequence,
		SnapshotDigest:       p.snapshotDigestLocked(),
	}
}

// Poll reads one page of the projection after the exclusive cursor: the
// identical data the SSE stream delivers. A cursor beyond the ledger returns
// the deterministic resync directive instead of events.
func (p *Projection) Poll(cursor uint64, limit int) (EventPage, *EventResync) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		directive := p.resyncLocked(resyncReasonProjectionRebuilt, 1)
		return EventPage{}, &directive
	}
	maxSequence := uint64(len(p.ledger))
	if cursor > maxSequence {
		directive := p.resyncLocked(resyncReasonCursorExpired, 1)
		return EventPage{}, &directive
	}
	window := p.ledger[cursor:]
	if limit >= 0 && len(window) > limit {
		window = window[:limit]
	}
	page := EventPage{
		APIVersion:           domain.APIVersionV1Alpha1,
		Kind:                 KindEventPage,
		AuthorityNamespaceId: p.namespace,
		Scope:                p.namespace.AuthorityScopeId,
		Events:               make([]EventProjection, 0, len(window)),
		NextCursor:           cursor,
		SnapshotDigest:       p.snapshotDigestLocked(),
	}
	page.Events = append(page.Events, window...)
	if len(window) > 0 {
		page.NextCursor = window[len(window)-1].LedgerSequence
	}
	return page, nil
}

// SubscriptionOutcome reports one established or rejected subscription.
type SubscriptionOutcome struct {
	subscriber *sseSubscriber
	resync     *EventResync
	apiErr     *APIError
}

// Subscriber exposes the established subscription of one SSE stream.
func (o SubscriptionOutcome) Subscriber() *sseSubscriber { return o.subscriber }

// Resync reports the deterministic resync directive of a rejected
// subscription, if any.
func (o SubscriptionOutcome) Resync() *EventResync { return o.resync }

// APIError reports the fail-closed validation rejection of a subscription,
// if any.
func (o SubscriptionOutcome) APIError() *APIError { return o.apiErr }

// Subscribe establishes one subscription atomically against the current
// ledger: the cursor (or the Last-Event-ID eventId) resolves the exclusive
// resume point, the backlog is captured and the subscriber is registered in
// one step, so no event can be lost or reordered between resolution and
// registration.
func (p *Projection) Subscribe(cursor, lastEventID string, bufferLimit int) SubscriptionOutcome {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		directive := p.resyncLocked(resyncReasonProjectionRebuilt, 1)
		return SubscriptionOutcome{resync: &directive}
	}
	start, directive, apiErr := p.resolveStartLocked(cursor, lastEventID)
	if apiErr != nil {
		return SubscriptionOutcome{apiErr: apiErr}
	}
	if directive != nil {
		return SubscriptionOutcome{resync: directive}
	}
	maxSequence := uint64(len(p.ledger))
	var backlog []EventProjection
	if start <= maxSequence {
		backlog = p.ledger[start-1:]
	}
	if len(backlog) > bufferLimit {
		overflow := p.resyncLocked(resyncReasonBacklogOverflow, start)
		return SubscriptionOutcome{resync: &overflow}
	}
	subscriber := newSSESubscriber(bufferLimit)
	for _, frame := range backlog {
		subscriber.out <- frame
	}
	p.hub.add(subscriber)
	return SubscriptionOutcome{subscriber: subscriber}
}

// resolveStartLocked resolves the exclusive resume point of one
// subscription. The cursor is the last consumed ledgerSequence; absent or
// zero cursor replays from sequence 1, and the Last-Event-ID eventId is the
// reconnection spelling of the identical boundary.
func (p *Projection) resolveStartLocked(cursor, lastEventID string) (uint64, *EventResync, *APIError) {
	maxSequence := uint64(len(p.ledger))
	if strings.TrimSpace(cursor) != "" {
		value, err := strconv.ParseUint(strings.TrimSpace(cursor), 10, 64)
		if err != nil {
			return 0, nil, apiError(CodeInvalidRequest, "cursor-invalid",
				"the cursor must be a decimal ledgerSequence")
		}
		if value == 0 {
			return 1, nil, nil
		}
		if value > maxSequence {
			directive := p.resyncLocked(resyncReasonCursorExpired, 1)
			return 0, &directive, nil
		}
		return value + 1, nil, nil
	}
	if strings.TrimSpace(lastEventID) != "" {
		sequence, known := p.byEventID[strings.TrimSpace(lastEventID)]
		if !known {
			directive := p.resyncLocked(resyncReasonCursorExpired, 1)
			return 0, &directive, nil
		}
		return sequence + 1, nil, nil
	}
	return 1, nil, nil
}

// sseHub tracks every active SSE subscriber of one projection.
type sseHub struct {
	mu          sync.Mutex
	subscribers map[*sseSubscriber]struct{}
}

func newSSEHub() *sseHub {
	return &sseHub{subscribers: map[*sseSubscriber]struct{}{}}
}

func (h *sseHub) add(subscriber *sseSubscriber) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.subscribers[subscriber] = struct{}{}
}

func (h *sseHub) remove(subscriber *sseSubscriber) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.subscribers, subscriber)
}

// publish delivers freshly projected events to every subscriber without ever
// blocking the event source: a subscriber whose bounded buffer is full is
// disconnected with the deterministic resync directive instead of stalling
// the ledger write path (ADR 0018 §14 backpressure).
func (h *sseHub) publish(events []EventProjection, overflowDirective EventResync) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for subscriber := range h.subscribers {
		h.deliver(subscriber, events, overflowDirective)
	}
}

func (h *sseHub) deliver(subscriber *sseSubscriber, events []EventProjection, overflowDirective EventResync) {
	if subscriber.disconnected() {
		return
	}
	for index := range events {
		select {
		case subscriber.out <- events[index]:
		default:
			subscriber.disconnect(overflowDirective)
			return
		}
	}
}

// broadcastResync disconnects every subscriber with the identical directive.
func (h *sseHub) broadcastResync(directive EventResync) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for subscriber := range h.subscribers {
		subscriber.disconnect(directive)
	}
}

// reauthAll requests the immediate re-Authorization of every subscriber
// (sensitive-change path of ADR 0018 §14). It never blocks.
func (h *sseHub) reauthAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for subscriber := range h.subscribers {
		subscriber.requestReauth()
	}
}

// sseSubscriber is one bounded SSE subscription.
type sseSubscriber struct {
	out       chan EventProjection
	done      chan struct{}
	reauth    chan struct{}
	closeOnce sync.Once
	directive EventResync
}

func newSSESubscriber(bufferLimit int) *sseSubscriber {
	if bufferLimit < 1 {
		bufferLimit = 1
	}
	return &sseSubscriber{
		out:    make(chan EventProjection, bufferLimit),
		done:   make(chan struct{}),
		reauth: make(chan struct{}, 1),
	}
}

func (s *sseSubscriber) disconnected() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

// disconnect terminates the subscription with the deterministic resync
// directive. The directive write happens before the channel close inside the
// Once, so observers of done observe a fully initialized directive.
func (s *sseSubscriber) disconnect(directive EventResync) {
	s.closeOnce.Do(func() {
		s.directive = directive
		close(s.done)
	})
}

func (s *sseSubscriber) requestReauth() {
	select {
	case s.reauth <- struct{}{}:
	default:
	}
}

// defaultAuthorizer is the loopback MVP re-Authorization check: the
// principal must remain present and the bound scope must still equal the
// serving authority namespace's scope. Any mismatch fails closed.
func defaultAuthorizer(principal string, namespace authority.AuthorityNamespaceId, scope string) error {
	if strings.TrimSpace(principal) == "" {
		return errors.New("server: SSE authorization requires a principal")
	}
	if err := namespace.Validate(); err != nil {
		return fmt.Errorf("server: SSE authorization: %w", err)
	}
	if scope != namespace.AuthorityScopeId {
		return errors.New("server: SSE authorization: scope no longer matches the authority namespace")
	}
	return nil
}

// Close stops the SSE projection watcher and releases every active
// subscription of the server.
func (s *Server) Close() error {
	return s.events.Close()
}

// NotifySensitiveChange forces the immediate re-Authorization of every
// active SSE subscription (ADR 0018 §14 sensitive-change path): revocation,
// scope change or permission withdrawal must revalidate at once, and any
// failed check closes the affected connection fail closed.
func (s *Server) NotifySensitiveChange() {
	s.events.hub.reauthAll()
}

// HandleNotifyHook is the read-only adaptation of runstore's
// MARSHAL_NOTIFY_CMD notification hook: it accepts the hook's JSON payload
// and wakes the projection for an immediate out-of-band scan. Invalid
// payloads are dropped; the hook never writes and never blocks the caller.
func (s *Server) HandleNotifyHook(payload []byte) {
	var decoded NotifyHookPayload
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return
	}
	s.events.wake()
}

// expectQueryParams fails closed on any query parameter outside the frozen
// set of one endpoint.
func expectQueryParams(request *http.Request, allowed ...string) *APIError {
	for key := range request.URL.Query() {
		known := false
		for _, name := range allowed {
			if key == name {
				known = true
				break
			}
		}
		if !known {
			return apiError(CodeInvalidRequest, "unknown-query:"+key,
				"the endpoint does not accept this query parameter")
		}
	}
	return nil
}

// handleEventsStream serves the versioned SSE endpoint /events: the
// read-only projection stream with cursor resume, resync, heartbeat,
// re-Authorization and bounded backpressure.
func (s *Server) handleEventsStream(writer http.ResponseWriter, request *http.Request, identity requestIdentity) {
	if apiErr := readGetBody(request); apiErr != nil {
		writeError(writer, identity.RequestID, apiErr)
		return
	}
	if apiErr := expectQueryParams(request, "cursor"); apiErr != nil {
		writeError(writer, identity.RequestID, apiErr)
		return
	}
	projection := s.events
	projection.ScanNow()
	outcome := projection.Subscribe(request.URL.Query().Get("cursor"), request.Header.Get("Last-Event-ID"), s.sseBufferLimit)
	if apiErr := outcome.APIError(); apiErr != nil {
		writeError(writer, identity.RequestID, apiErr)
		return
	}
	if directive := outcome.Resync(); directive != nil {
		writeResync(writer, directive)
		return
	}
	subscriber := outcome.Subscriber()
	defer projection.hub.remove(subscriber)
	flusher, ok := writer.(http.Flusher)
	if !ok {
		writeError(writer, identity.RequestID, apiError(CodeInternal, "internal",
			"the transport does not support server-sent events"))
		return
	}
	header := writer.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache")
	header.Set("Connection", "keep-alive")
	header.Set("X-Accel-Buffering", "no")
	writer.WriteHeader(http.StatusOK)
	flusher.Flush()
	s.streamEvents(writer, flusher, request, identity, subscriber)
}

// streamEvents pumps one established subscription. It exits — closing the
// connection — on client disconnect, on any write failure, on a failed
// re-Authorization (fail closed, never degraded), or on the terminal resync
// directive of a forced disconnect.
func (s *Server) streamEvents(writer http.ResponseWriter, flusher http.Flusher, request *http.Request, identity requestIdentity, subscriber *sseSubscriber) {
	controller := http.NewResponseController(writer)
	writeDeadline := s.sseHeartbeatInterval * 2
	refreshDeadline := func() {
		_ = controller.SetWriteDeadline(time.Now().Add(writeDeadline))
	}
	heartbeat := time.NewTicker(s.sseHeartbeatInterval)
	defer heartbeat.Stop()
	reauthz := time.NewTicker(s.sseReauthzInterval)
	defer reauthz.Stop()
	reauthorized := func() bool {
		return s.authorizer(identity.Principal, s.namespace, s.namespace.AuthorityScopeId) == nil
	}
	for {
		if subscriber.disconnected() {
			refreshDeadline()
			writeResyncFrame(writer, flusher, subscriber.directive)
			return
		}
		select {
		case <-request.Context().Done():
			return
		case <-subscriber.done:
			refreshDeadline()
			writeResyncFrame(writer, flusher, subscriber.directive)
			return
		case <-reauthz.C:
			if !reauthorized() {
				return
			}
		case <-subscriber.reauth:
			if !reauthorized() {
				return
			}
		case <-heartbeat.C:
			refreshDeadline()
			if _, err := fmt.Fprint(writer, ": keep-alive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case frame := <-subscriber.out:
			refreshDeadline()
			if !writeEventFrame(writer, flusher, frame) {
				return
			}
		}
	}
}

// writeEventFrame emits one SSE data frame. The frame id is the eventId
// (Last-Event-ID resume depends on it) and the data payload is the exact
// JSON the polling fallback returns for the same sequence: both channels
// marshal the identical EventProjection value.
func writeEventFrame(writer http.ResponseWriter, flusher http.Flusher, frame EventProjection) bool {
	data, err := json.Marshal(frame)
	if err != nil {
		return false
	}
	if _, err := fmt.Fprintf(writer, "id: %s\ndata: %s\n\n", frame.EventID, data); err != nil {
		return false
	}
	flusher.Flush()
	return true
}

// writeResyncFrame emits the terminal resync directive of a forced
// disconnect on best effort: a stalled client may never observe it, in which
// case the closed connection alone guides it back to resync.
func writeResyncFrame(writer http.ResponseWriter, flusher http.Flusher, directive EventResync) {
	data, err := json.Marshal(directive)
	if err != nil {
		return
	}
	if _, err := fmt.Fprintf(writer, "event: resync\ndata: %s\n\n", data); err != nil {
		return
	}
	flusher.Flush()
}

// writeResync answers a subscription or poll whose cursor cannot be served
// with the deterministic HTTP 409 EventResync directive.
func writeResync(writer http.ResponseWriter, directive *EventResync) {
	data, err := json.Marshal(directive)
	if err != nil {
		writer.WriteHeader(http.StatusInternalServerError)
		return
	}
	writer.WriteHeader(http.StatusConflict)
	_, _ = writer.Write(append(data, '\n'))
}

// handleEventsPoll serves the polling fallback /events/poll: the identical
// projection data as the SSE stream, read over plain HTTP/JSON with the
// identical exclusive cursor boundary.
func (s *Server) handleEventsPoll(writer http.ResponseWriter, request *http.Request, identity requestIdentity) {
	if apiErr := readGetBody(request); apiErr != nil {
		writeError(writer, identity.RequestID, apiErr)
		return
	}
	if apiErr := expectQueryParams(request, "cursor", "limit"); apiErr != nil {
		writeError(writer, identity.RequestID, apiErr)
		return
	}
	query := request.URL.Query()
	var cursor uint64
	if raw := strings.TrimSpace(query.Get("cursor")); raw != "" {
		value, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			writeError(writer, identity.RequestID, apiError(CodeInvalidRequest, "cursor-invalid",
				"the cursor must be a decimal ledgerSequence"))
			return
		}
		cursor = value
	}
	limit := defaultPollLimit
	if raw := strings.TrimSpace(query.Get("limit")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > maxPollLimit {
			writeError(writer, identity.RequestID, apiError(CodeInvalidRequest, "limit-invalid",
				"limit must be an integer between 1 and 1000"))
			return
		}
		limit = value
	}
	projection := s.events
	projection.ScanNow()
	page, directive := projection.Poll(cursor, limit)
	if directive != nil {
		writeResync(writer, directive)
		return
	}
	data, err := json.Marshal(page)
	if err != nil {
		writeError(writer, identity.RequestID, apiError(CodeInternal, "internal", "encode event page"))
		return
	}
	writeJSON(writer, http.StatusOK, data)
}
