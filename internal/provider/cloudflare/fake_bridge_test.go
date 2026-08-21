package cloudflare

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/sandbox"
)

// This file hosts the fake Bridge conformance fixture: an in-process
// httptest implementation of the official Cloudflare Sandbox Bridge HTTP
// API (health / create / running / exec SSE / file / persist / hydrate /
// destroy / session) with deterministic fixtures and fault injections for
// the provider and client tests. It never connects to real Cloudflare
// infrastructure and depends on no Cloudflare SDK.

// fakeBridgeSandbox is the fixture state of one Bridge sandbox, keyed by the
// Bridge locator the create endpoint assigns.
type fakeBridgeSandbox struct {
	bridgeId  string
	files     map[string][]byte
	sessions  map[string]bool
	lost      bool
	destroyed bool
}

type fakeExecOutcome struct {
	exitCode int
	signaled bool
}

type idempotentResponse struct {
	status int
	body   []byte
}

// fakeBridge is the deterministic in-process Bridge fixture.
type fakeBridge struct {
	mu      sync.Mutex
	token   string
	mux     *http.ServeMux
	server  *httptest.Server
	nextId  int
	nextSid int

	sandboxes map[string]*fakeBridgeSandbox
	stores    map[string][]byte

	idempotencyCache map[string]idempotentResponse
	failTimes        map[string]int
	failStatus       int
	slowTimes        map[string]int
	slowDuration     time.Duration
	dropTimes        map[string]int
	dropSuffixes     map[string]int

	tamperAfterWrite  bool
	capacityErrorNext bool
	execOutcomes      map[string]fakeExecOutcome
	healthRawBody     []byte

	requestCounts           map[string]int
	authHeaders             []string
	observedIdempotencyKeys []string
	destroyCount            int
}

// newFakeBridge starts one fixture Bridge over httptest and registers its
// cleanup.
func newFakeBridge(t *testing.T, token string) *fakeBridge {
	t.Helper()
	fb := &fakeBridge{
		token:            token,
		sandboxes:        map[string]*fakeBridgeSandbox{},
		stores:           map[string][]byte{},
		idempotencyCache: map[string]idempotentResponse{},
		failTimes:        map[string]int{},
		failStatus:       http.StatusServiceUnavailable,
		slowTimes:        map[string]int{},
		slowDuration:     200 * time.Millisecond,
		dropTimes:        map[string]int{},
		dropSuffixes:     map[string]int{},
		execOutcomes:     map[string]fakeExecOutcome{},
		requestCounts:    map[string]int{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", fb.handleHealth)
	mux.HandleFunc("POST /v1/sandbox", fb.handleCreate)
	mux.HandleFunc("GET /v1/sandbox/{id}/running", fb.handleRunning)
	mux.HandleFunc("DELETE /v1/sandbox/{id}", fb.handleDestroy)
	mux.HandleFunc("POST /v1/sandbox/{id}/exec", fb.handleExec)
	mux.HandleFunc("GET /v1/sandbox/{id}/file/{path...}", fb.handleFileRead)
	mux.HandleFunc("PUT /v1/sandbox/{id}/file/{path...}", fb.handleFileWrite)
	mux.HandleFunc("POST /v1/sandbox/{id}/persist", fb.handlePersist)
	mux.HandleFunc("POST /v1/sandbox/{id}/hydrate", fb.handleHydrate)
	mux.HandleFunc("POST /v1/sandbox/{id}/session", fb.handleCreateSession)
	mux.HandleFunc("DELETE /v1/sandbox/{id}/session/{sessionId}", fb.handleDeleteSession)
	fb.mux = mux
	fb.server = httptest.NewServer(http.HandlerFunc(fb.serve))
	t.Cleanup(fb.server.Close)
	return fb
}

// serve enforces the Bearer credential gate for every endpoint except the
// unauthenticated health read, and records the transport surface for the
// credential-isolation assertions.
func (fb *fakeBridge) serve(w http.ResponseWriter, r *http.Request) {
	fb.mu.Lock()
	fb.requestCounts[r.Method+" "+r.URL.Path]++
	fb.authHeaders = append(fb.authHeaders, r.Header.Get("Authorization"))
	if key := r.Header.Get("Idempotency-Key"); key != "" {
		fb.observedIdempotencyKeys = append(fb.observedIdempotencyKeys, key)
	}
	token := fb.token
	fb.mu.Unlock()
	if r.Method == http.MethodGet && r.URL.Path == healthPath {
		fb.mux.ServeHTTP(w, r)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+token {
		writeJSON(w, http.StatusUnauthorized, errorPayload("credential-rejected", "the bridge credential was rejected"))
		return
	}
	fb.mux.ServeHTTP(w, r)
}

func (fb *fakeBridge) handleHealth(w http.ResponseWriter, r *http.Request) {
	if fb.injectTransportFault(w, r) {
		return
	}
	if fb.injectAPIFault(w, r) {
		return
	}
	fb.mu.Lock()
	raw := fb.healthRawBody
	fb.mu.Unlock()
	if len(raw) > 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(raw)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (fb *fakeBridge) handleCreate(w http.ResponseWriter, r *http.Request) {
	if fb.replayCached(w, r) {
		return
	}
	if fb.injectTransportFault(w, r) {
		return
	}
	if fb.injectAPIFault(w, r) {
		return
	}
	fb.mu.Lock()
	if fb.capacityErrorNext {
		fb.capacityErrorNext = false
		fb.mu.Unlock()
		fb.respondJSON(w, r, http.StatusInsufficientStorage, errorPayload("capacity-exhausted", "the account container capacity is exhausted"))
		return
	}
	fb.nextId++
	bridgeId := "br-" + strconv.Itoa(fb.nextId)
	fb.sandboxes[bridgeId] = &fakeBridgeSandbox{
		bridgeId: bridgeId,
		files:    map[string][]byte{},
		sessions: map[string]bool{},
	}
	fb.mu.Unlock()
	fb.respondJSON(w, r, http.StatusOK, map[string]string{"id": bridgeId})
}

func (fb *fakeBridge) handleRunning(w http.ResponseWriter, r *http.Request) {
	if fb.injectTransportFault(w, r) {
		return
	}
	if fb.injectAPIFault(w, r) {
		return
	}
	id := r.PathValue("id")
	fb.mu.Lock()
	box, ok := fb.sandboxes[id]
	switch {
	case !ok || box.destroyed:
		fb.mu.Unlock()
		writeJSON(w, http.StatusNotFound, errorPayload("sandbox-not-found", "no sandbox carries this identifier"))
		return
	case box.lost:
		fb.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]bool{"running": false})
		return
	}
	fb.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]bool{"running": true})
}

func (fb *fakeBridge) handleDestroy(w http.ResponseWriter, r *http.Request) {
	if fb.replayCached(w, r) {
		return
	}
	if fb.injectTransportFault(w, r) {
		return
	}
	if fb.injectAPIFault(w, r) {
		return
	}
	id := r.PathValue("id")
	fb.mu.Lock()
	box, ok := fb.sandboxes[id]
	if !ok || box.destroyed {
		fb.mu.Unlock()
		fb.respondJSON(w, r, http.StatusNotFound, errorPayload("sandbox-not-found", "no sandbox carries this identifier"))
		return
	}
	box.destroyed = true
	box.lost = false
	box.sessions = map[string]bool{}
	fb.destroyCount++
	fb.mu.Unlock()
	fb.respondRaw(w, r, http.StatusNoContent, "application/octet-stream", nil)
}

func (fb *fakeBridge) handleExec(w http.ResponseWriter, r *http.Request) {
	if fb.injectTransportFault(w, r) {
		return
	}
	if fb.injectAPIFault(w, r) {
		return
	}
	id := r.PathValue("id")
	var request ExecStreamRequest
	if err := decodeRequestBody(r, &request); err != nil {
		writeJSON(w, http.StatusBadRequest, errorPayload("invalid-request", "the exec payload was rejected"))
		return
	}
	if len(request.Argv) == 0 {
		writeJSON(w, http.StatusBadRequest, errorPayload("invalid-request", "exec requires a non-empty argv"))
		return
	}
	fb.mu.Lock()
	box, ok := fb.sandboxes[id]
	if !ok || box.destroyed {
		fb.mu.Unlock()
		writeJSON(w, http.StatusNotFound, errorPayload("sandbox-not-found", "no sandbox carries this identifier"))
		return
	}
	if box.lost {
		fb.mu.Unlock()
		writeJSON(w, http.StatusGone, errorPayload("container-lost", "the container state was lost after hibernation"))
		return
	}
	outcome := fb.execOutcomeLocked(request.Argv[0])
	fb.mu.Unlock()
	stdout := []byte("exec stdout\x00" + strings.Join(request.Argv, "\x00"))
	stderr := []byte("exec stderr\x00" + strings.Join(request.Argv, "\x00"))
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	emitSSE(w, flusher, "stdout", base64.StdEncoding.EncodeToString(stdout))
	emitSSE(w, flusher, "stderr", base64.StdEncoding.EncodeToString(stderr))
	if outcome.signaled {
		emitSSE(w, flusher, "error", sseJSON(map[string]string{"message": "process killed"}))
	} else {
		emitSSE(w, flusher, "exit", sseJSON(map[string]int{"exit_code": outcome.exitCode}))
	}
}

func (fb *fakeBridge) handleFileRead(w http.ResponseWriter, r *http.Request) {
	if fb.injectTransportFault(w, r) {
		return
	}
	if fb.injectAPIFault(w, r) {
		return
	}
	id := r.PathValue("id")
	path := r.PathValue("path")
	fb.mu.Lock()
	box, ok := fb.sandboxes[id]
	if !ok || box.destroyed {
		fb.mu.Unlock()
		writeJSON(w, http.StatusNotFound, errorPayload("sandbox-not-found", "no sandbox carries this identifier"))
		return
	}
	if box.lost {
		fb.mu.Unlock()
		writeJSON(w, http.StatusGone, errorPayload("container-lost", "the container state was lost after hibernation"))
		return
	}
	content, exists := box.files[path]
	if !exists {
		fb.mu.Unlock()
		writeJSON(w, http.StatusNotFound, errorPayload("file-not-found", "the sandbox holds no such staged file"))
		return
	}
	out := append([]byte(nil), content...)
	fb.mu.Unlock()
	fb.respondRaw(w, r, http.StatusOK, "application/octet-stream", out)
}

func (fb *fakeBridge) handleFileWrite(w http.ResponseWriter, r *http.Request) {
	if fb.replayCached(w, r) {
		return
	}
	if fb.injectTransportFault(w, r) {
		return
	}
	if fb.injectAPIFault(w, r) {
		return
	}
	id := r.PathValue("id")
	path := r.PathValue("path")
	content, err := io.ReadAll(io.LimitReader(r.Body, maxRawBytes+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorPayload("invalid-request", "the file payload could not be read"))
		return
	}
	if int64(len(content)) > maxRawBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, errorPayload("payload-too-large", "the file payload exceeds the budget"))
		return
	}
	fb.mu.Lock()
	box, ok := fb.sandboxes[id]
	if !ok || box.destroyed {
		fb.mu.Unlock()
		fb.respondJSON(w, r, http.StatusNotFound, errorPayload("sandbox-not-found", "no sandbox carries this identifier"))
		return
	}
	if box.lost {
		fb.mu.Unlock()
		fb.respondJSON(w, r, http.StatusGone, errorPayload("container-lost", "the container state was lost after hibernation"))
		return
	}
	box.files[path] = append([]byte(nil), content...)
	if fb.tamperAfterWrite {
		box.files[path] = append(append([]byte(nil), content...), []byte("|bridge-fixture-tamper")...)
	}
	fb.mu.Unlock()
	fb.respondRaw(w, r, http.StatusNoContent, "application/octet-stream", nil)
}

func (fb *fakeBridge) handlePersist(w http.ResponseWriter, r *http.Request) {
	if fb.replayCached(w, r) {
		return
	}
	if fb.injectTransportFault(w, r) {
		return
	}
	if fb.injectAPIFault(w, r) {
		return
	}
	id := r.PathValue("id")
	fb.mu.Lock()
	box, ok := fb.sandboxes[id]
	if !ok || box.destroyed {
		fb.mu.Unlock()
		fb.respondJSON(w, r, http.StatusNotFound, errorPayload("sandbox-not-found", "no sandbox carries this identifier"))
		return
	}
	if box.lost {
		fb.mu.Unlock()
		fb.respondJSON(w, r, http.StatusGone, errorPayload("container-lost", "the container state was lost after hibernation"))
		return
	}
	tar := buildTar(box.files)
	fb.mu.Unlock()
	fb.respondRaw(w, r, http.StatusOK, "application/octet-stream", tar)
}

func (fb *fakeBridge) handleHydrate(w http.ResponseWriter, r *http.Request) {
	if fb.replayCached(w, r) {
		return
	}
	if fb.injectTransportFault(w, r) {
		return
	}
	if fb.injectAPIFault(w, r) {
		return
	}
	id := r.PathValue("id")
	tarBytes, err := io.ReadAll(io.LimitReader(r.Body, maxRawBytes+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorPayload("invalid-request", "the hydrate payload could not be read"))
		return
	}
	if int64(len(tarBytes)) > maxRawBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, errorPayload("payload-too-large", "the hydrate payload exceeds the budget"))
		return
	}
	files, err := parseTar(tarBytes)
	if err != nil {
		fb.respondJSON(w, r, http.StatusUnprocessableEntity, errorPayload("invalid-checkpoint", "the checkpoint snapshot could not be parsed"))
		return
	}
	fb.mu.Lock()
	box, ok := fb.sandboxes[id]
	if !ok || box.destroyed {
		fb.mu.Unlock()
		fb.respondJSON(w, r, http.StatusNotFound, errorPayload("sandbox-not-found", "no sandbox carries this identifier"))
		return
	}
	if box.lost {
		fb.mu.Unlock()
		fb.respondJSON(w, r, http.StatusGone, errorPayload("container-lost", "the container state was lost after hibernation"))
		return
	}
	box.files = files
	fb.mu.Unlock()
	fb.respondRaw(w, r, http.StatusNoContent, "application/octet-stream", nil)
}

func (fb *fakeBridge) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	if fb.replayCached(w, r) {
		return
	}
	if fb.injectTransportFault(w, r) {
		return
	}
	if fb.injectAPIFault(w, r) {
		return
	}
	id := r.PathValue("id")
	fb.mu.Lock()
	box, ok := fb.sandboxes[id]
	if !ok || box.destroyed {
		fb.mu.Unlock()
		fb.respondJSON(w, r, http.StatusNotFound, errorPayload("sandbox-not-found", "no sandbox carries this identifier"))
		return
	}
	if box.lost {
		fb.mu.Unlock()
		fb.respondJSON(w, r, http.StatusGone, errorPayload("container-lost", "the container state was lost after hibernation"))
		return
	}
	fb.nextSid++
	sessionId := "sess-" + strconv.Itoa(fb.nextSid)
	box.sessions[sessionId] = true
	fb.mu.Unlock()
	fb.respondJSON(w, r, http.StatusOK, map[string]string{"sessionId": sessionId})
}

func (fb *fakeBridge) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	if fb.injectTransportFault(w, r) {
		return
	}
	if fb.injectAPIFault(w, r) {
		return
	}
	id := r.PathValue("id")
	sessionId := r.PathValue("sessionId")
	fb.mu.Lock()
	box, ok := fb.sandboxes[id]
	if !ok || box.destroyed {
		fb.mu.Unlock()
		writeJSON(w, http.StatusNotFound, errorPayload("sandbox-not-found", "no sandbox carries this identifier"))
		return
	}
	if box.lost {
		fb.mu.Unlock()
		writeJSON(w, http.StatusGone, errorPayload("container-lost", "the container state was lost after hibernation"))
		return
	}
	if !box.sessions[sessionId] {
		fb.mu.Unlock()
		writeJSON(w, http.StatusNotFound, errorPayload("session-not-found", "no session carries this identifier"))
		return
	}
	delete(box.sessions, sessionId)
	fb.mu.Unlock()
	fb.respondRaw(w, r, http.StatusNoContent, "application/octet-stream", nil)
}

func (fb *fakeBridge) execOutcomeLocked(command0 string) fakeExecOutcome {
	if outcome, ok := fb.execOutcomes[command0]; ok {
		return outcome
	}
	return fakeExecOutcome{}
}

// replayCached serves the cached outcome of a mutating call when the same
// Idempotency-Key replays, which is what makes the client's bounded retry
// budget safe after a lost response.
func (fb *fakeBridge) replayCached(w http.ResponseWriter, r *http.Request) bool {
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		return false
	}
	fb.mu.Lock()
	cached, ok := fb.idempotencyCache[r.Method+" "+r.URL.Path+"\x00"+key]
	fb.mu.Unlock()
	if !ok {
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(cached.status)
	_, _ = w.Write(cached.body)
	return true
}

// respondJSON writes one JSON outcome and caches it under the
// Idempotency-Key when present.
func (fb *fakeBridge) respondJSON(w http.ResponseWriter, r *http.Request, status int, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		http.Error(w, "fixture encoding failure", http.StatusInternalServerError)
		return
	}
	fb.cacheResponse(r, status, body)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// respondRaw writes one raw byte outcome and caches it under the
// Idempotency-Key when present.
func (fb *fakeBridge) respondRaw(w http.ResponseWriter, r *http.Request, status int, contentType string, body []byte) {
	fb.cacheResponse(r, status, body)
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(status)
	if len(body) > 0 {
		_, _ = w.Write(body)
	}
}

func (fb *fakeBridge) cacheResponse(r *http.Request, status int, body []byte) {
	// Only a successfully-applied side effect is idempotent. A refusal
	// (capacity, conflict, not-found, lost, semantic 4xx) produced no remote
	// side effect, so it must never be replayed as a successful or terminal
	// outcome: replaying it would block a legitimate retry issued under the
	// identical allocation-derived Idempotency-Key.
	if status < 200 || status >= 300 {
		return
	}
	if key := r.Header.Get("Idempotency-Key"); key != "" {
		fb.mu.Lock()
		fb.idempotencyCache[r.Method+" "+r.URL.Path+"\x00"+key] = idempotentResponse{status: status, body: body}
		fb.mu.Unlock()
	}
}

// injectTransportFault applies the drop and slow transport faults; it
// returns true when the handler must stop.
func (fb *fakeBridge) injectTransportFault(w http.ResponseWriter, r *http.Request) bool {
	key := r.Method + " " + r.URL.Path
	fb.mu.Lock()
	drop := fb.dropTimes[key] > 0
	if drop {
		fb.dropTimes[key]--
	}
	suffixDrop := false
	for suffixKey, remaining := range fb.dropSuffixes {
		method, suffix, ok := strings.Cut(suffixKey, " ")
		if ok && method == r.Method && strings.HasSuffix(r.URL.Path, suffix) && remaining > 0 {
			fb.dropSuffixes[suffixKey] = remaining - 1
			suffixDrop = true
		}
	}
	slow := fb.slowTimes[key] > 0
	if slow {
		fb.slowTimes[key]--
	}
	duration := fb.slowDuration
	fb.mu.Unlock()
	if drop || suffixDrop {
		if hijacker, ok := w.(http.Hijacker); ok {
			if conn, _, err := hijacker.Hijack(); err == nil {
				_ = conn.Close()
				return true
			}
		}
		return true
	}
	if slow {
		time.Sleep(duration)
	}
	return false
}

// injectAPIFault applies the injected 5xx-class failures; it returns true
// when the handler must stop.
func (fb *fakeBridge) injectAPIFault(w http.ResponseWriter, r *http.Request) bool {
	key := r.Method + " " + r.URL.Path
	fb.mu.Lock()
	fail := fb.failTimes[key] > 0
	status := fb.failStatus
	if fail {
		fb.failTimes[key]--
	}
	fb.mu.Unlock()
	if fail {
		writeJSON(w, status, errorPayload("transient-failure", "the fixture bridge injected a transient failure"))
		return true
	}
	return false
}

// Test-control helpers.

// SeedStore places deterministic content behind one bound store alias and
// digest so staged locators resolve through the resolver.
func (fb *fakeBridge) SeedStore(storeId, sha256 string, content []byte) {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	fb.stores[storeId+"\x00"+sha256] = append([]byte(nil), content...)
}

// Resolver returns a locator resolver reading from the fixture store, to be
// wired into the provider under test.
func (fb *fakeBridge) Resolver() func(sandbox.Locator) ([]byte, error) {
	return func(locator sandbox.Locator) ([]byte, error) {
		fb.mu.Lock()
		defer fb.mu.Unlock()
		content, ok := fb.stores[locator.StoreId+"\x00"+locator.SHA256]
		if !ok {
			return nil, errors.New("cloudflare fixture: locator content is not resolvable")
		}
		return append([]byte(nil), content...), nil
	}
}

// LoseContainer simulates the platform silently losing the container's file
// and process state after hibernation.
func (fb *fakeBridge) LoseContainer(t *testing.T, id string) {
	t.Helper()
	fb.mu.Lock()
	defer fb.mu.Unlock()
	box, ok := fb.sandboxes[id]
	if !ok {
		t.Fatalf("the fixture bridge holds no sandbox %q", id)
	}
	box.lost = true
	box.files = map[string][]byte{}
	box.sessions = map[string]bool{}
}

// ForgetSandbox removes one sandbox from the fixture entirely, simulating a
// silent platform reclaim the bookkeeping does not know about.
func (fb *fakeBridge) ForgetSandbox(t *testing.T, id string) {
	t.Helper()
	fb.mu.Lock()
	defer fb.mu.Unlock()
	if _, ok := fb.sandboxes[id]; !ok {
		t.Fatalf("the fixture bridge holds no sandbox %q", id)
	}
	delete(fb.sandboxes, id)
}

// TamperAfterWrite corrupts the staged bytes after the raw write, exercising
// the Marshal-side post-consumption recomputation path.
func (fb *fakeBridge) TamperAfterWrite(on bool) {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	fb.tamperAfterWrite = on
}

// SetExecOutcome scripts the exit observation of commands starting with
// command0.
func (fb *fakeBridge) SetExecOutcome(command0 string, exitCode int, signaled bool) {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	fb.execOutcomes[command0] = fakeExecOutcome{exitCode: exitCode, signaled: signaled}
}

// FailPathWithStatusTimes fails the next n calls of one endpoint with one
// status.
func (fb *fakeBridge) FailPathWithStatusTimes(method, path string, status, times int) {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	fb.failTimes[method+" "+path] = times
	fb.failStatus = status
}

// FailPathTimes fails the next n calls of one endpoint with 503.
func (fb *fakeBridge) FailPathTimes(method, path string, times int) {
	fb.FailPathWithStatusTimes(method, path, http.StatusServiceUnavailable, times)
}

// SlowPathTimes delays the next n calls of one endpoint beyond any small
// client timeout.
func (fb *fakeBridge) SlowPathTimes(method, path string, times int) {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	fb.slowTimes[method+" "+path] = times
}

// SetSlowDuration overrides the injected slow-call duration.
func (fb *fakeBridge) SetSlowDuration(duration time.Duration) {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	fb.slowDuration = duration
}

// DropPathTimes hijacks and closes the connection of the next n calls of
// one endpoint, simulating transport response loss.
func (fb *fakeBridge) DropPathTimes(method, path string, times int) {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	fb.dropTimes[method+" "+path] = times
}

// DropPathSuffixTimes hijacks the next n calls of any endpoint whose path
// ends with suffix, for endpoints whose full path is not known in advance
// (a Bridge-assigned sandbox id).
func (fb *fakeBridge) DropPathSuffixTimes(method, suffix string, times int) {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	fb.dropSuffixes[method+" "+suffix] = times
}

// SetHealthRawBody injects a raw health response body (malformed JSON or
// duplicate members exercise the canonical admission gate).
func (fb *fakeBridge) SetHealthRawBody(raw []byte) {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	fb.healthRawBody = raw
}

// CapacityExhaustNext makes the next create call fail with 507.
func (fb *fakeBridge) CapacityExhaustNext() {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	fb.capacityErrorNext = true
}

// RequestCount returns the number of requests observed for one endpoint.
func (fb *fakeBridge) RequestCount(method, path string) int {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	return fb.requestCounts[method+" "+path]
}

// TotalRequests returns the number of requests observed overall.
func (fb *fakeBridge) TotalRequests() int {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	total := 0
	for _, count := range fb.requestCounts {
		total += count
	}
	return total
}

// DestroyCount returns how many times the fixture actually applied a destroy
// side effect (never counting idempotency-cache replays or not-found reads),
// so the crash matrix can freeze the destroy-exactly-once invariant.
func (fb *fakeBridge) DestroyCount() int {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	return fb.destroyCount
}

// AuthHeaders returns the Authorization headers observed by the fixture.
func (fb *fakeBridge) AuthHeaders() []string {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	return append([]string(nil), fb.authHeaders...)
}

// ObservedIdempotencyKeys returns the Idempotency-Key header values the
// fixture observed on mutating calls, for HTTP-safe key layering assertions.
func (fb *fakeBridge) ObservedIdempotencyKeys() []string {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	return append([]string(nil), fb.observedIdempotencyKeys...)
}

// SandboxFile exposes one staged file of the fixture for out-of-band
// assertions.
func (fb *fakeBridge) SandboxFile(id, path string) ([]byte, bool) {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	box, ok := fb.sandboxes[id]
	if !ok || box.files == nil {
		return nil, false
	}
	content, exists := box.files[path]
	return append([]byte(nil), content...), exists
}

// sandboxCount returns the number of sandboxes the fixture holds, including
// destroyed ones, for idempotency convergence assertions.
func (fb *fakeBridge) sandboxCount() int {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	return len(fb.sandboxes)
}

// activeSandboxCount returns the number of sandboxes the fixture still holds
// that have not been destroyed, so the terminate crash matrix can freeze the
// destroy-leaves-no-leak invariant: a converged destroy leaves zero
// undestroyed remote sandboxes behind.
func (fb *fakeBridge) activeSandboxCount() int {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	count := 0
	for _, box := range fb.sandboxes {
		if !box.destroyed {
			count++
		}
	}
	return count
}

// Wire helpers.

func writeJSON(w http.ResponseWriter, status int, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		http.Error(w, "fixture encoding failure", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func errorPayload(code, message string) map[string]string {
	return map[string]string{"code": code, "message": message}
}

func decodeRequestBody(r *http.Request, out any) error {
	data, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return err
	}
	canonicalized, err := canonical.JSON(data)
	if err != nil {
		return err
	}
	return json.Unmarshal(canonicalized, out)
}

func emitSSE(w http.ResponseWriter, flusher http.Flusher, event, data string) {
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); err != nil {
		return
	}
	if flusher != nil {
		flusher.Flush()
	}
}

func sseJSON(payload any) string {
	body, err := json.Marshal(payload)
	if err != nil {
		return "{}"
	}
	return string(body)
}

// buildTar serializes the fixture files into a real ustar tar, so the
// persist/hydrate path exercises the official raw tar contract.
func buildTar(files map[string][]byte) []byte {
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	var buf bytes.Buffer
	writer := tar.NewWriter(&buf)
	for _, path := range paths {
		content := files[path]
		_ = writer.WriteHeader(&tar.Header{Name: path, Mode: 0644, Size: int64(len(content))})
		_, _ = writer.Write(content)
	}
	_ = writer.Close()
	return buf.Bytes()
}

func parseTar(data []byte) (map[string][]byte, error) {
	files := map[string][]byte{}
	reader := tar.NewReader(bytes.NewReader(data))
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		content, err := io.ReadAll(reader)
		if err != nil {
			return nil, err
		}
		files[header.Name] = content
	}
	return files, nil
}

// Shared test builders.

// fixtureDigest derives a well-formed sha256 digest from seed material, so
// no Digest-family, Token-family or Key-family fixture field is ever
// assigned one complete string literal (gitleaks publication gate).
func fixtureDigest(seed string) string {
	return canonical.DigestBytes([]byte(seed))
}

// testBridgeToken derives a gitleaks-safe fixture token out of two parts.
func testBridgeToken(name string) string {
	return "cf-bridge-fixture" + "-token-" + name
}

func workspaceRequirements(t *testing.T) domain.SandboxRequirements {
	t.Helper()
	requirements, err := domain.NewSandboxRequirements(domain.AccessModeWorkspaceWrite, domain.AssuranceLevelWorkspaceWrite)
	if err != nil {
		t.Fatalf("workspace requirements: %v", err)
	}
	return requirements
}

func hardenedRequirements(t *testing.T) domain.SandboxRequirements {
	t.Helper()
	requirements, err := domain.NewSandboxRequirements(domain.AccessModeWorkspaceWrite, domain.AssuranceLevelHardened)
	if err != nil {
		t.Fatalf("hardened requirements: %v", err)
	}
	return requirements
}

func readOnlyRequirements(t *testing.T) domain.SandboxRequirements {
	t.Helper()
	requirements, err := domain.NewSandboxRequirements(domain.AccessModeReadOnly, domain.AssuranceLevelWorkspaceWrite)
	if err != nil {
		t.Fatalf("read-only requirements: %v", err)
	}
	return requirements
}

// scenarioIdentity builds one valid dispatch-bound identity with a
// deterministic fencing token derived from the scenario name.
func scenarioIdentity(name, allocationId, commandId string, generation int64) sandbox.OperationIdentity {
	return sandbox.OperationIdentity{
		TaskId:       "task-" + name,
		RunId:        "run-" + name,
		AttemptId:    "attempt-" + name,
		WorkloadRole: sandbox.WorkloadRoleWorker,
		AllocationId: allocationId,
		Generation:   generation,
		FencingToken: fixtureDigest("fencing" + "-" + name),
		CommandId:    commandId,
	}
}

// newTestProvider constructs one Bridge provider against one fixture Bridge
// with deterministic retry settings, an ephemeral in-memory store and no
// retry delay.
func newTestProvider(t *testing.T, fb *fakeBridge, evidenceRef string) *Provider {
	t.Helper()
	provider, err := NewProvider(ProviderConfig{
		BridgeBaseURL:          fb.server.URL,
		BridgeToken:            fb.token,
		ConformanceEvidenceRef: evidenceRef,
		MaxRetries:             2,
		RetryDelay:             -1,
		RequestTimeout:         5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	return provider
}

// providerBridgeLocator returns the Bridge locator the provider recorded for
// one allocation.
func providerBridgeLocator(t *testing.T, provider *Provider, allocationId string) string {
	t.Helper()
	provider.mu.Lock()
	defer provider.mu.Unlock()
	entry, ok := provider.allocations[allocationId]
	if !ok {
		t.Fatalf("the provider holds no allocation %q", allocationId)
	}
	return entry.bridgeLocator
}

// assertNoCredential freezes the credential discipline: no observable
// provider output may contain the transport credential.
func assertNoCredential(t *testing.T, token string, values ...string) {
	t.Helper()
	for _, value := range values {
		if strings.Contains(value, token) {
			t.Fatalf("the transport credential surfaced in a provider observable: %q", value)
		}
	}
}

func traceOutcomes(trace []sandbox.BusinessEvent) map[string]string {
	outcomes := make(map[string]string, len(trace))
	for _, event := range trace {
		outcomes[event.Kind] = event.Outcome
	}
	return outcomes
}

// assertVerdictsEquivalent freezes the verdict equivalence between two
// providers: identical Passed/ReasonCode and normalized business-trace
// outcome/invariant equivalence, never a per-step call comparison.
func assertVerdictsEquivalent(t *testing.T, fixtureName string, expected, observed sandbox.ConformanceVerdict) {
	t.Helper()
	if expected.Passed != observed.Passed {
		t.Fatalf("fixture %s: verdict Passed diverges: fake=%t bridge=%t", fixtureName, expected.Passed, observed.Passed)
	}
	if expected.ReasonCode != observed.ReasonCode {
		t.Fatalf("fixture %s: verdict ReasonCode diverges: fake=%q bridge=%q", fixtureName, expected.ReasonCode, observed.ReasonCode)
	}
	expectedOutcomes := traceOutcomes(expected.Trace)
	observedOutcomes := traceOutcomes(observed.Trace)
	for kind, outcome := range expectedOutcomes {
		if observedOutcomes[kind] != outcome {
			t.Fatalf("fixture %s: invariant %q outcome diverges: fake=%q bridge=%q", fixtureName, kind, outcome, observedOutcomes[kind])
		}
	}
	for kind, outcome := range observedOutcomes {
		if expectedOutcomes[kind] != outcome {
			t.Fatalf("fixture %s: bridge emitted invariant %q outcome %q absent from the fake trace", fixtureName, kind, outcome)
		}
	}
}

// TestConformanceEquivalenceFakeBridge drives the identical probe set of
// sandbox.RunConformance through the scripted fake provider and the Bridge
// provider backed by the fake Bridge, and freezes their verdict
// equivalence.
func TestConformanceEquivalenceFakeBridge(t *testing.T) {
	fixtures := []sandbox.ConformanceFixture{
		{Name: "workspace-write-happy", Requirements: workspaceRequirements(t), Payload: []byte("payload" + "-workspace-write")},
		{Name: "hardened-refusal", Requirements: hardenedRequirements(t)},
		{Name: "read-only-happy", Requirements: readOnlyRequirements(t), Payload: []byte("payload" + "-read-only")},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			fake := sandbox.NewFakeProvider(sandbox.FakeConfig{})
			fb := newFakeBridge(t, testBridgeToken(fixture.Name))
			provider := newTestProvider(t, fb, "")
			fakeVerdicts := sandbox.RunConformance(fake, fixture)
			bridgeVerdicts := sandbox.RunConformance(provider, fixture)
			if len(fakeVerdicts) != 1 || len(bridgeVerdicts) != 1 {
				t.Fatalf("expected exactly one verdict per provider, got fake=%d bridge=%d", len(fakeVerdicts), len(bridgeVerdicts))
			}
			assertVerdictsEquivalent(t, fixture.Name, fakeVerdicts[0], bridgeVerdicts[0])
			if fixture.Requirements.MinimumAssuranceLevel != domain.AssuranceLevelHardened && !bridgeVerdicts[0].Passed {
				t.Fatalf("the bridge provider must pass the conformance suite for %s, got reason %q", fixture.Name, bridgeVerdicts[0].ReasonCode)
			}
		})
	}
}

// TestConformanceHardenedWithEvidencePasses freezes that a hardened fixture
// served on valid suite-issued evidence passes the full scenario through the
// Bridge provider.
func TestConformanceHardenedWithEvidencePasses(t *testing.T) {
	evidence := fixtureDigest("conformance-evidence" + "-bridge")
	fb := newFakeBridge(t, testBridgeToken("hardened-ok"))
	provider := newTestProvider(t, fb, evidence)
	fixture := sandbox.ConformanceFixture{
		Name:         "hardened-ok-" + "1",
		Requirements: hardenedRequirements(t),
		Payload:      []byte("hardened-ok-" + "payload"),
	}
	verdicts := sandbox.RunConformance(provider, fixture)
	if len(verdicts) != 1 {
		t.Fatalf("one fixture must yield one verdict, got %d", len(verdicts))
	}
	if !verdicts[0].Passed || verdicts[0].ReasonCode != sandbox.ReasonOK {
		t.Fatalf("a hardened request with valid evidence must pass, got %+v", verdicts[0])
	}
}

// TestBridgeContainerLossCheckpointRestoreSemantics freezes the hibernation
// loss semantics: after the container state is lost, Checkpoint fails
// closed, Inspect observes the failure, and Restore recovers the staged
// content through create + hydrate on a replacement allocation whose
// checkpoint digest equals the pre-loss checkpoint.
func TestBridgeContainerLossCheckpointRestoreSemantics(t *testing.T) {
	name := "loss"
	alloc := "alloc-" + name
	next := alloc + "-next"
	fb := newFakeBridge(t, testBridgeToken(name))
	provider := newTestProvider(t, fb, "")
	ctx := context.Background()
	identity := func(commandId string, generation int64) sandbox.OperationIdentity {
		return scenarioIdentity(name, alloc, commandId, generation)
	}

	if _, err := provider.Provision(ctx, sandbox.ProvisionRequest{
		Identity:        identity("cmd-provision", 1),
		Requirements:    workspaceRequirements(t),
		AllowedStoreIds: []string{"store-" + name},
	}); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	content := []byte("staged" + "-content-" + name)
	if _, err := provider.Stage(ctx, sandbox.StageRequest{
		Identity:     identity("cmd-stage", 1),
		AllocationId: alloc,
		Inputs: []sandbox.StageInput{{
			InputId:        "snapshot-source",
			DeclaredSHA256: sandbox.RecomputeSHA256(content),
			Inline:         append([]byte(nil), content...),
		}},
	}); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	checkpoint, err := provider.Checkpoint(ctx, sandbox.CheckpointRequest{
		Identity:     identity("cmd-checkpoint", 1),
		AllocationId: alloc,
	})
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}

	fb.LoseContainer(t, providerBridgeLocator(t, provider, alloc))

	if _, err := provider.Checkpoint(ctx, sandbox.CheckpointRequest{
		Identity:     identity("cmd-checkpoint-lost", 1),
		AllocationId: alloc,
	}); !errors.Is(err, sandbox.ErrAllocationNotActive) {
		t.Fatalf("Checkpoint after container loss must fail closed with ErrAllocationNotActive, got %v", err)
	}
	lostObservation, err := provider.Inspect(ctx, sandbox.InspectRequest{
		Identity:     identity("cmd-inspect-lost", 1),
		AllocationId: alloc,
	})
	if err != nil {
		t.Fatalf("Inspect after container loss: %v", err)
	}
	if lostObservation.State != sandbox.AllocationFailed {
		t.Fatalf("the lost container must be observed as failed, got %q", string(lostObservation.State))
	}

	restoreIdentity := scenarioIdentity(name, next, "cmd-restore", 2)
	receipt, err := provider.Restore(ctx, sandbox.RestoreOperationRequest{
		Identity:             restoreIdentity,
		PreviousAllocationId: alloc,
		NextAllocationId:     next,
	})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if receipt.Allocation.AllocationId != next || receipt.Allocation.Generation != 2 || receipt.Allocation.State != sandbox.AllocationActive {
		t.Fatalf("the restored allocation must be the active replacement at generation 2, got %+v", receipt.Allocation)
	}

	nextIdentity := func(commandId string) sandbox.OperationIdentity {
		return scenarioIdentity(name, next, commandId, 2)
	}
	restored, err := provider.Inspect(ctx, sandbox.InspectRequest{
		Identity:     nextIdentity("cmd-inspect-next"),
		AllocationId: next,
	})
	if err != nil {
		t.Fatalf("Inspect of the restored allocation: %v", err)
	}
	if restored.State != sandbox.AllocationActive {
		t.Fatalf("the restored allocation must be observed active, got %q", string(restored.State))
	}
	nextBridgeId := providerBridgeLocator(t, provider, next)
	if staged, ok := fb.SandboxFile(nextBridgeId, "staged/snapshot-source"); !ok || sandbox.RecomputeSHA256(staged) != sandbox.RecomputeSHA256(content) {
		t.Fatal("the hydrate must restore the staged content byte for byte")
	}
	restoredCheckpoint, err := provider.Checkpoint(ctx, sandbox.CheckpointRequest{
		Identity:     nextIdentity("cmd-checkpoint-next"),
		AllocationId: next,
	})
	if err != nil {
		t.Fatalf("Checkpoint of the restored allocation: %v", err)
	}
	if restoredCheckpoint.SHA256 != checkpoint.SHA256 {
		t.Fatalf("the restored checkpoint digest must equal the pre-loss checkpoint digest: %q != %q", restoredCheckpoint.SHA256, checkpoint.SHA256)
	}

	report, err := provider.Reconcile(ctx, sandbox.ReconcileRequest{
		Identity:  nextIdentity("cmd-reconcile"),
		RunId:     "run-" + name,
		AttemptId: "attempt-" + name,
	})
	if err != nil || report.DriftDetected || len(report.ActiveAllocationIds) != 1 || report.ActiveAllocationIds[0] != next {
		t.Fatalf("reconcile after restore must observe exactly the restored allocation, got %+v err=%v", report, err)
	}
	if _, err := provider.Terminate(ctx, sandbox.TerminateRequest{
		Identity:     nextIdentity("cmd-terminate"),
		AllocationId: next,
	}); err != nil {
		t.Fatalf("Terminate: %v", err)
	}
	final, err := provider.Reconcile(ctx, sandbox.ReconcileRequest{
		Identity:  nextIdentity("cmd-reconcile-final"),
		RunId:     "run-" + name,
		AttemptId: "attempt-" + name,
	})
	if err != nil || final.DriftDetected || len(final.ActiveAllocationIds) != 0 {
		t.Fatalf("the final reconcile must be clean, got %+v err=%v", final, err)
	}
}

// TestBridgeReconcileIdempotent freezes that repeated reconcile calls of
// one scope observe the identical report, both while an allocation is
// active and after it terminated.
func TestBridgeReconcileIdempotent(t *testing.T) {
	name := "recon"
	alloc := "alloc-" + name
	fb := newFakeBridge(t, testBridgeToken(name))
	provider := newTestProvider(t, fb, "")
	ctx := context.Background()
	identity := func(commandId string) sandbox.OperationIdentity {
		return scenarioIdentity(name, alloc, commandId, 1)
	}
	if _, err := provider.Provision(ctx, sandbox.ProvisionRequest{
		Identity:     identity("cmd-provision"),
		Requirements: workspaceRequirements(t),
	}); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	first, err := provider.Reconcile(ctx, sandbox.ReconcileRequest{
		Identity:  identity("cmd-reconcile-1"),
		RunId:     "run-" + name,
		AttemptId: "attempt-" + name,
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	second, err := provider.Reconcile(ctx, sandbox.ReconcileRequest{
		Identity:  identity("cmd-reconcile-2"),
		RunId:     "run-" + name,
		AttemptId: "attempt-" + name,
	})
	if err != nil {
		t.Fatalf("Reconcile again: %v", err)
	}
	assertReportsEqual(t, first, second)
	if len(first.ActiveAllocationIds) != 1 || first.ActiveAllocationIds[0] != alloc || first.DriftDetected {
		t.Fatalf("the active reconcile must observe exactly the provisioned allocation, got %+v", first)
	}

	if _, err := provider.Terminate(ctx, sandbox.TerminateRequest{
		Identity:     identity("cmd-terminate"),
		AllocationId: alloc,
	}); err != nil {
		t.Fatalf("Terminate: %v", err)
	}
	third, err := provider.Reconcile(ctx, sandbox.ReconcileRequest{
		Identity:  identity("cmd-reconcile-3"),
		RunId:     "run-" + name,
		AttemptId: "attempt-" + name,
	})
	if err != nil {
		t.Fatalf("Reconcile after terminate: %v", err)
	}
	fourth, err := provider.Reconcile(ctx, sandbox.ReconcileRequest{
		Identity:  identity("cmd-reconcile-4"),
		RunId:     "run-" + name,
		AttemptId: "attempt-" + name,
	})
	if err != nil {
		t.Fatalf("Reconcile after terminate again: %v", err)
	}
	assertReportsEqual(t, third, fourth)
	if len(third.ActiveAllocationIds) != 0 || third.DriftDetected {
		t.Fatalf("the post-terminate reconcile must be clean, got %+v", third)
	}
}

func assertReportsEqual(t *testing.T, left, right *sandbox.ReconcileReport) {
	t.Helper()
	if left.DriftDetected != right.DriftDetected ||
		strings.Join(left.ActiveAllocationIds, ",") != strings.Join(right.ActiveAllocationIds, ",") ||
		strings.Join(left.OrphanAllocationIds, ",") != strings.Join(right.OrphanAllocationIds, ",") {
		t.Fatalf("reconcile reports diverge: %+v != %+v", left, right)
	}
}

// TestBridgeLostRestoreResponseRecoversThroughIdempotency freezes that a
// restore whose hydrate response is lost recovers through the bounded retry
// budget and the Idempotency-Key replay, without corrupting the restored
// state.
func TestBridgeLostRestoreResponseRecoversThroughIdempotency(t *testing.T) {
	name := "lost-response"
	alloc := "alloc-" + name
	next := alloc + "-next"
	fb := newFakeBridge(t, testBridgeToken(name))
	provider := newTestProvider(t, fb, "")
	ctx := context.Background()
	identity := func(commandId string) sandbox.OperationIdentity {
		return scenarioIdentity(name, alloc, commandId, 1)
	}
	if _, err := provider.Provision(ctx, sandbox.ProvisionRequest{
		Identity:     identity("cmd-provision"),
		Requirements: workspaceRequirements(t),
	}); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	content := []byte("lost-response" + "-content")
	if _, err := provider.Stage(ctx, sandbox.StageRequest{
		Identity:     identity("cmd-stage"),
		AllocationId: alloc,
		Inputs: []sandbox.StageInput{{
			InputId:        "payload",
			DeclaredSHA256: sandbox.RecomputeSHA256(content),
			Inline:         append([]byte(nil), content...),
		}},
	}); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if _, err := provider.Checkpoint(ctx, sandbox.CheckpointRequest{
		Identity:     identity("cmd-checkpoint"),
		AllocationId: alloc,
	}); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}

	fb.DropPathSuffixTimes("POST", "/hydrate", 1)
	receipt, err := provider.Restore(ctx, sandbox.RestoreOperationRequest{
		Identity:             scenarioIdentity(name, next, "cmd-restore", 2),
		PreviousAllocationId: alloc,
		NextAllocationId:     next,
	})
	if err != nil {
		t.Fatalf("Restore must recover from one lost hydrate response through the idempotent retry, got %v", err)
	}
	if got := fb.RequestCount("POST", "/v1/sandbox/"+providerBridgeLocator(t, provider, next)+"/hydrate"); got != 2 {
		t.Fatalf("exactly one retry after the lost response was expected, got %d hydrate calls", got)
	}
	if receipt.Allocation.AllocationId != next || receipt.Allocation.State != sandbox.AllocationActive {
		t.Fatalf("the restored allocation must be the active replacement, got %+v", receipt.Allocation)
	}
	if staged, ok := fb.SandboxFile(providerBridgeLocator(t, provider, next), "staged/payload"); !ok || sandbox.RecomputeSHA256(staged) != sandbox.RecomputeSHA256(content) {
		t.Fatal("the idempotent recovery must restore the staged content byte for byte")
	}
}

// TestBridgeLostRestoreResponseReconcilesFailClosed freezes that a restore
// whose hydrate responses are lost beyond the retry budget surfaces as
// fail-closed drift through the independent reconcile path.
func TestBridgeLostRestoreResponseReconcilesFailClosed(t *testing.T) {
	name := "lost-beyond-budget"
	alloc := "alloc-" + name
	next := alloc + "-next"
	fb := newFakeBridge(t, testBridgeToken(name))
	provider := newTestProvider(t, fb, "")
	ctx := context.Background()
	identity := func(commandId string) sandbox.OperationIdentity {
		return scenarioIdentity(name, alloc, commandId, 1)
	}
	if _, err := provider.Provision(ctx, sandbox.ProvisionRequest{
		Identity:     identity("cmd-provision"),
		Requirements: workspaceRequirements(t),
	}); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	content := []byte("lost-beyond-budget" + "-content")
	if _, err := provider.Stage(ctx, sandbox.StageRequest{
		Identity:     identity("cmd-stage"),
		AllocationId: alloc,
		Inputs: []sandbox.StageInput{{
			InputId:        "payload",
			DeclaredSHA256: sandbox.RecomputeSHA256(content),
			Inline:         append([]byte(nil), content...),
		}},
	}); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if _, err := provider.Checkpoint(ctx, sandbox.CheckpointRequest{
		Identity:     identity("cmd-checkpoint"),
		AllocationId: alloc,
	}); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}

	fb.DropPathSuffixTimes("POST", "/hydrate", 3)

	_, err := provider.Restore(ctx, sandbox.RestoreOperationRequest{
		Identity:             scenarioIdentity(name, next, "cmd-restore", 2),
		PreviousAllocationId: alloc,
		NextAllocationId:     next,
	})
	if !errors.Is(err, ErrBridgeUnavailable) {
		t.Fatalf("Restore must fail closed with ErrBridgeUnavailable beyond the retry budget, got %v", err)
	}

	report, reconcileErr := provider.Reconcile(ctx, sandbox.ReconcileRequest{
		Identity:  identity("cmd-reconcile"),
		RunId:     "run-" + name,
		AttemptId: "attempt-" + name,
	})
	if reconcileErr == nil {
		t.Fatal("reconcile must fail closed when a create intent survives a lost restore response")
	}
	if report == nil || !report.DriftDetected {
		t.Fatalf("reconcile must report drift, got %+v", report)
	}
	orphaned := false
	for _, orphan := range report.OrphanAllocationIds {
		if orphan == next {
			orphaned = true
		}
	}
	if !orphaned {
		t.Fatalf("the ambiguous replacement allocation must be reported, got %+v", report)
	}
}

// TestFakeBridgeRefusalNotReplayedUnderIdempotencyKey freezes the fixture
// idempotency semantics: a refusal that produced no remote side effect is
// never cached under the Idempotency-Key, so a retry issued under the
// identical allocation-derived key reaches the create handler and succeeds
// once the refusal clears.
func TestFakeBridgeRefusalNotReplayedUnderIdempotencyKey(t *testing.T) {
	fb := newFakeBridge(t, testBridgeToken("refusal-not-replayed"))
	key := httpIdempotencyKey("alloc-refusal", "create")
	doCreate := func() (int, string) {
		req, err := http.NewRequest(http.MethodPost, fb.server.URL+sandboxPath, strings.NewReader("{}"))
		if err != nil {
			t.Fatalf("create request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+fb.token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", key)
		resp, err := fb.server.Client().Do(req)
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}

	fb.CapacityExhaustNext()
	if status, _ := doCreate(); status != http.StatusInsufficientStorage {
		t.Fatalf("the capacity refusal must surface as 507, got %d", status)
	}
	if status, body := doCreate(); status != http.StatusOK || !strings.Contains(body, `"id":"br-1"`) {
		t.Fatalf("the retry under the identical key must re-execute and create a sandbox, got %d %s", status, body)
	}
	if got := fb.sandboxCount(); got != 1 {
		t.Fatalf("exactly one remote sandbox must exist after the re-executed create, got %d", got)
	}
}
