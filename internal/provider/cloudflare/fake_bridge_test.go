package cloudflare

import (
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
// httptest implementation of the M10-a Bridge endpoint family (create /
// running / exec SSE / file / persist / hydrate / destroy) that carries the
// deterministic fixtures and fault injections for the provider and client
// tests. It never connects to real Cloudflare infrastructure and depends on
// no Cloudflare SDK.

// fakeBridgeSandbox is the fixture state of one Bridge sandbox.
type fakeBridgeSandbox struct {
	record       SandboxRecord
	files        map[string][]byte
	violations   []ViolationRecord
	spawnCount   int64
	logLines     []string
	exitCode     int
	liveSessions int
	persistCount int
	lost         bool
	destroyed    bool
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
	mu              sync.Mutex
	token           string
	protocolVersion string
	mux             *http.ServeMux
	server          *httptest.Server

	sandboxes   map[string]*fakeBridgeSandbox
	stores      map[string][]byte
	checkpoints map[string][]byte

	idempotencyCache map[string]idempotentResponse
	failTimes        map[string]int
	failStatus       int
	slowTimes        map[string]int
	slowDuration     time.Duration
	dropTimes        map[string]int

	disableContainment bool
	tamperAfterWrite   bool
	capacityErrorNext  bool
	execOutcomes       map[string]fakeExecOutcome
	healthRawBody      []byte

	requestCounts           map[string]int
	authHeaders             []string
	observedIdempotencyKeys []string
}

// newFakeBridge starts one fixture Bridge over httptest and registers its
// cleanup.
func newFakeBridge(t *testing.T, token string) *fakeBridge {
	t.Helper()
	fb := &fakeBridge{
		token:            token,
		protocolVersion:  DefaultProtocolVersion,
		sandboxes:        map[string]*fakeBridgeSandbox{},
		stores:           map[string][]byte{},
		checkpoints:      map[string][]byte{},
		idempotencyCache: map[string]idempotentResponse{},
		failTimes:        map[string]int{},
		failStatus:       http.StatusServiceUnavailable,
		slowTimes:        map[string]int{},
		slowDuration:     200 * time.Millisecond,
		dropTimes:        map[string]int{},
		execOutcomes:     map[string]fakeExecOutcome{},
		requestCounts:    map[string]int{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", fb.handleHealth)
	mux.HandleFunc("POST /v1/sandboxes", fb.handleCreate)
	mux.HandleFunc("GET /v1/sandboxes", fb.handleList)
	mux.HandleFunc("GET /v1/sandboxes/{id}", fb.handleStatus)
	mux.HandleFunc("DELETE /v1/sandboxes/{id}", fb.handleDestroy)
	mux.HandleFunc("POST /v1/sandboxes/{id}/exec", fb.handleExec)
	mux.HandleFunc("POST /v1/sandboxes/{id}/file", fb.handleFileWrite)
	mux.HandleFunc("GET /v1/sandboxes/{id}/file", fb.handleFileRead)
	mux.HandleFunc("POST /v1/sandboxes/{id}/persist", fb.handlePersist)
	mux.HandleFunc("POST /v1/sandboxes/{id}/hydrate", fb.handleHydrate)
	mux.HandleFunc("POST /v1/sandboxes/{id}/signal", fb.handleSignal)
	fb.mux = mux
	fb.server = httptest.NewServer(http.HandlerFunc(fb.serve))
	t.Cleanup(fb.server.Close)
	return fb
}

// serve enforces the Bearer credential gate and records the transport
// surface for the credential-isolation assertions.
func (fb *fakeBridge) serve(w http.ResponseWriter, r *http.Request) {
	fb.mu.Lock()
	fb.requestCounts[r.Method+" "+r.URL.Path]++
	fb.authHeaders = append(fb.authHeaders, r.Header.Get("Authorization"))
	if key := r.Header.Get("Idempotency-Key"); key != "" {
		fb.observedIdempotencyKeys = append(fb.observedIdempotencyKeys, key)
	}
	token := fb.token
	fb.mu.Unlock()
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
	version := fb.protocolVersion
	fb.mu.Unlock()
	if len(raw) > 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(raw)
		return
	}
	writeJSON(w, http.StatusOK, HealthReport{
		Status:          "ok",
		ProtocolVersion: version,
		BridgeVersion:   "m10a-fixture" + "-bridge",
	})
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
	var request CreateSandboxRequest
	if err := decodeRequestBody(r, &request); err != nil {
		writeJSON(w, http.StatusBadRequest, errorPayload("invalid-request", "the create payload was rejected"))
		return
	}
	fb.mu.Lock()
	if fb.capacityErrorNext {
		fb.capacityErrorNext = false
		fb.mu.Unlock()
		fb.respondMutating(w, r, http.StatusInsufficientStorage, errorPayload("capacity-exhausted", "the account container capacity is exhausted"))
		return
	}
	existing, exists := fb.sandboxes[request.SandboxId]
	if exists && !existing.destroyed && !existing.lost {
		fb.mu.Unlock()
		fb.respondMutating(w, r, http.StatusConflict, errorPayload("duplicate-sandbox", "a running sandbox already carries this identifier"))
		return
	}
	box := &fakeBridgeSandbox{
		record: SandboxRecord{
			SandboxId:  request.SandboxId,
			RunId:      request.RunId,
			AttemptId:  request.AttemptId,
			Generation: request.Generation,
			State:      "running",
		},
		files: map[string][]byte{},
	}
	fb.sandboxes[request.SandboxId] = box
	record := box.record
	fb.mu.Unlock()
	fb.respondMutating(w, r, http.StatusCreated, record)
}

func (fb *fakeBridge) handleList(w http.ResponseWriter, r *http.Request) {
	if fb.injectTransportFault(w, r) {
		return
	}
	if fb.injectAPIFault(w, r) {
		return
	}
	runId := r.URL.Query().Get("runId")
	attemptId := r.URL.Query().Get("attemptId")
	fb.mu.Lock()
	records := []SandboxRecord{}
	for _, box := range fb.sandboxes {
		if box.destroyed || box.lost {
			continue
		}
		if runId != "" && box.record.RunId != runId {
			continue
		}
		if attemptId != "" && box.record.AttemptId != attemptId {
			continue
		}
		records = append(records, box.record)
	}
	fb.mu.Unlock()
	sort.Slice(records, func(i, j int) bool { return records[i].SandboxId < records[j].SandboxId })
	writeJSON(w, http.StatusOK, SandboxList{Sandboxes: records})
}

func (fb *fakeBridge) handleStatus(w http.ResponseWriter, r *http.Request) {
	if fb.injectTransportFault(w, r) {
		return
	}
	if fb.injectAPIFault(w, r) {
		return
	}
	id := r.PathValue("id")
	fb.mu.Lock()
	box, ok := fb.resolveBoxLocked(w, id)
	if !ok {
		fb.mu.Unlock()
		return
	}
	status := SandboxStatus{
		SandboxId:    id,
		State:        "running",
		ExitCode:     box.exitCode,
		SpawnCount:   box.spawnCount,
		Violations:   append([]ViolationRecord(nil), box.violations...),
		LogLines:     append([]string(nil), box.logLines...),
		LiveSessions: box.liveSessions,
	}
	fb.mu.Unlock()
	writeJSON(w, http.StatusOK, status)
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
	if len(request.Command) == 0 {
		writeJSON(w, http.StatusBadRequest, errorPayload("invalid-request", "exec requires a non-empty command"))
		return
	}
	fb.mu.Lock()
	box, ok := fb.resolveBoxLocked(w, id)
	if !ok {
		fb.mu.Unlock()
		return
	}
	outcome := fb.execOutcomeLocked(request.Command[0])
	contained := !fb.disableContainment
	for _, token := range request.Command {
		switch token {
		case sandbox.ProbeCommandBoundaryWrite:
			if contained {
				appendLogLocked(box, "probe blocked: out-of-bounds write attempt contained")
			} else {
				box.violations = append(box.violations, ViolationRecord{Kind: sandbox.ViolationOutOfBoundsWrite, Detail: "the probe wrote outside the allocation boundary"})
				appendLogLocked(box, "observed violation: out-of-bounds write escaped containment")
			}
		case sandbox.ProbeCommandSensitiveEnvRead:
			if contained {
				appendLogLocked(box, "probe blocked: sensitive environment read denied")
			} else {
				box.violations = append(box.violations, ViolationRecord{Kind: sandbox.ViolationSensitiveEnvRead, Detail: "the probe read sensitive environment entries"})
				appendLogLocked(box, "observed violation: sensitive environment read escaped containment")
			}
		case sandbox.ProbeCommandSpawnFlood:
			if contained {
				appendLogLocked(box, "probe blocked: spawn flood capped at the limit")
			} else {
				box.spawnCount += 8
				box.violations = append(box.violations, ViolationRecord{Kind: sandbox.ViolationSpawnLimitExceeded, Detail: "the probe exceeded the spawn limit"})
				appendLogLocked(box, "observed violation: spawn flood escaped containment")
			}
		default:
			appendLogLocked(box, "exec: "+token)
		}
	}
	box.exitCode = outcome.exitCode
	stdout := []byte("exec stdout\x00" + strings.Join(request.Command, "\x00"))
	stderr := []byte("exec stderr\x00" + strings.Join(request.Command, "\x00"))
	fb.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	emitSSE(w, flusher, "output", sseOutputEvent{Stream: "stdout", Data: base64.StdEncoding.EncodeToString(stdout)})
	emitSSE(w, flusher, "output", sseOutputEvent{Stream: "stderr", Data: base64.StdEncoding.EncodeToString(stderr)})
	emitSSE(w, flusher, "exit", sseExitEvent{ExitCode: outcome.exitCode, Signaled: outcome.signaled})
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
	var request WriteFileRequest
	if err := decodeRequestBody(r, &request); err != nil {
		writeJSON(w, http.StatusBadRequest, errorPayload("invalid-request", "the file payload was rejected"))
		return
	}
	fb.mu.Lock()
	box, ok := fb.resolveBoxLocked(w, id)
	if !ok {
		fb.mu.Unlock()
		return
	}
	var content []byte
	if request.Locator != nil {
		seeded, resolved := fb.stores[request.Locator.StoreId+"\x00"+request.Locator.SHA256]
		if !resolved {
			fb.mu.Unlock()
			fb.respondMutating(w, r, http.StatusNotFound, errorPayload("locator-unresolved", "the bound store does not hold the referenced object"))
			return
		}
		content = append([]byte(nil), seeded...)
	} else {
		decoded, err := base64.StdEncoding.DecodeString(request.ContentBase64)
		if err != nil {
			fb.mu.Unlock()
			fb.respondMutating(w, r, http.StatusBadRequest, errorPayload("invalid-request", "the inline content was rejected"))
			return
		}
		content = decoded
	}
	// Fail-closed pre-consumption recomputation: a mismatch refuses the
	// write outright, so a declared digest is never consumed unchecked.
	pre := sandbox.RecomputeSHA256(content)
	if pre != request.DeclaredSHA256 {
		fb.mu.Unlock()
		fb.respondMutating(w, r, http.StatusUnprocessableEntity, errorPayload("digest-mismatch", "the digest recomputed before consumption does not match the declared digest"))
		return
	}
	box.files[request.Path] = append([]byte(nil), content...)
	if fb.tamperAfterWrite {
		box.files[request.Path] = append(append([]byte(nil), content...), []byte("|bridge-fixture-tamper")...)
	}
	post := sandbox.RecomputeSHA256(box.files[request.Path])
	if post != pre {
		fb.mu.Unlock()
		fb.respondMutating(w, r, http.StatusUnprocessableEntity, errorPayload("post-write-mismatch", "the read-back digest disagrees with the pre-consumption digest"))
		return
	}
	result := WriteFileResult{PreSHA256: pre, PostSHA256: post, SizeBytes: int64(len(content))}
	fb.mu.Unlock()
	fb.respondMutating(w, r, http.StatusOK, result)
}

func (fb *fakeBridge) handleFileRead(w http.ResponseWriter, r *http.Request) {
	if fb.injectTransportFault(w, r) {
		return
	}
	if fb.injectAPIFault(w, r) {
		return
	}
	id := r.PathValue("id")
	path := r.URL.Query().Get("path")
	fb.mu.Lock()
	box, ok := fb.resolveBoxLocked(w, id)
	if !ok {
		fb.mu.Unlock()
		return
	}
	content, exists := box.files[path]
	if !exists {
		fb.mu.Unlock()
		writeJSON(w, http.StatusNotFound, errorPayload("file-not-found", "the sandbox holds no such staged file"))
		return
	}
	result := ReadFileResult{
		Path:          path,
		ContentBase64: base64.StdEncoding.EncodeToString(content),
		SHA256:        sandbox.RecomputeSHA256(content),
		SizeBytes:     int64(len(content)),
	}
	fb.mu.Unlock()
	writeJSON(w, http.StatusOK, result)
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
	box, ok := fb.resolveBoxLocked(w, id)
	if !ok {
		fb.mu.Unlock()
		return
	}
	box.persistCount++
	paths := make([]string, 0, len(box.files))
	for path := range box.files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	var payload []byte
	for _, path := range paths {
		content := box.files[path]
		payload = append(payload, path...)
		payload = append(payload, 0)
		payload = append(payload, []byte(strconv.Itoa(len(content)))...)
		payload = append(payload, 0)
		payload = append(payload, content...)
	}
	checkpointId := "ckpt:" + id + ":" + strconv.Itoa(box.persistCount)
	fb.checkpoints[checkpointId] = payload
	result := PersistResult{
		CheckpointId: checkpointId,
		SHA256:       sandbox.RecomputeSHA256(payload),
		SizeBytes:    int64(len(payload)),
	}
	fb.mu.Unlock()
	fb.respondMutating(w, r, http.StatusOK, result)
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
	var request HydrateRequest
	if err := decodeRequestBody(r, &request); err != nil {
		writeJSON(w, http.StatusBadRequest, errorPayload("invalid-request", "the hydrate payload was rejected"))
		return
	}
	fb.mu.Lock()
	box, ok := fb.resolveBoxLocked(w, id)
	if !ok {
		fb.mu.Unlock()
		return
	}
	snapshot, exists := fb.checkpoints[request.CheckpointId]
	if !exists {
		fb.mu.Unlock()
		fb.respondMutating(w, r, http.StatusNotFound, errorPayload("checkpoint-not-found", "no checkpoint carries this identifier"))
		return
	}
	files, err := parseCheckpointPayload(snapshot)
	if err != nil {
		fb.mu.Unlock()
		fb.respondMutating(w, r, http.StatusUnprocessableEntity, errorPayload("invalid-checkpoint", "the checkpoint snapshot could not be parsed"))
		return
	}
	box.files = files
	result := HydrateResult{
		SandboxId:    id,
		CheckpointId: request.CheckpointId,
		FileCount:    len(files),
		SHA256:       sandbox.RecomputeSHA256(snapshot),
	}
	fb.mu.Unlock()
	fb.respondMutating(w, r, http.StatusOK, result)
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
	box, exists := fb.sandboxes[id]
	if !exists {
		fb.mu.Unlock()
		fb.respondMutating(w, r, http.StatusNotFound, errorPayload("sandbox-not-found", "no sandbox carries this identifier"))
		return
	}
	box.destroyed = true
	box.lost = false
	box.liveSessions = 0
	box.record.State = "destroyed"
	record := box.record
	fb.mu.Unlock()
	fb.respondMutating(w, r, http.StatusOK, record)
}

func (fb *fakeBridge) handleSignal(w http.ResponseWriter, r *http.Request) {
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
	var request SignalRequest
	if err := decodeRequestBody(r, &request); err != nil {
		writeJSON(w, http.StatusBadRequest, errorPayload("invalid-request", "the signal payload was rejected"))
		return
	}
	fb.mu.Lock()
	box, ok := fb.resolveBoxLocked(w, id)
	if !ok {
		fb.mu.Unlock()
		return
	}
	delivered := false
	if box.liveSessions > 0 {
		box.liveSessions--
		delivered = true
		appendLogLocked(box, "signal delivered: "+request.Signal)
	} else {
		appendLogLocked(box, "signal not delivered: no live workload process for "+request.Signal)
	}
	fb.mu.Unlock()
	fb.respondMutating(w, r, http.StatusOK, SignalResult{Signal: request.Signal, Delivered: delivered})
}

// resolveBoxLocked resolves one live sandbox while the caller holds fb.mu;
// it writes the fixture error response and returns false for unknown,
// destroyed or lost sandboxes.
func (fb *fakeBridge) resolveBoxLocked(w http.ResponseWriter, id string) (*fakeBridgeSandbox, bool) {
	box, ok := fb.sandboxes[id]
	switch {
	case !ok || box.destroyed:
		writeJSON(w, http.StatusNotFound, errorPayload("sandbox-not-found", "no sandbox carries this identifier"))
		return nil, false
	case box.lost:
		writeJSON(w, http.StatusGone, errorPayload("container-lost", "the container state was lost after hibernation"))
		return nil, false
	}
	return box, true
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

// respondMutating writes one mutating outcome and caches it under the
// Idempotency-Key when present.
func (fb *fakeBridge) respondMutating(w http.ResponseWriter, r *http.Request, status int, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		http.Error(w, "fixture encoding failure", http.StatusInternalServerError)
		return
	}
	if key := r.Header.Get("Idempotency-Key"); key != "" {
		fb.mu.Lock()
		fb.idempotencyCache[r.Method+" "+r.URL.Path+"\x00"+key] = idempotentResponse{status: status, body: body}
		fb.mu.Unlock()
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
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
	slow := fb.slowTimes[key] > 0
	if slow {
		fb.slowTimes[key]--
	}
	duration := fb.slowDuration
	fb.mu.Unlock()
	if drop {
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
// digest so staged locators resolve inside the fixture container.
func (fb *fakeBridge) SeedStore(storeId, sha256 string, content []byte) {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	fb.stores[storeId+"\x00"+sha256] = append([]byte(nil), content...)
}

// SetLiveSessions scripts the number of live exec sessions of one sandbox.
func (fb *fakeBridge) SetLiveSessions(t *testing.T, id string, sessions int) {
	t.Helper()
	fb.mu.Lock()
	defer fb.mu.Unlock()
	box, ok := fb.sandboxes[id]
	if !ok {
		t.Fatalf("the fixture bridge holds no sandbox %q", id)
	}
	box.liveSessions = sessions
}

// LoseContainer simulates the platform silently losing the container's
// file and process state after hibernation.
func (fb *fakeBridge) LoseContainer(t *testing.T, id string) {
	t.Helper()
	fb.mu.Lock()
	defer fb.mu.Unlock()
	box, ok := fb.sandboxes[id]
	if !ok {
		t.Fatalf("the fixture bridge holds no sandbox %q", id)
	}
	box.lost = true
	box.files = nil
	box.liveSessions = 0
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

// DisableContainment toggles the containment simulation of the fixture.
func (fb *fakeBridge) DisableContainment(on bool) {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	fb.disableContainment = on
}

// TamperAfterWrite corrupts the staged bytes after the pre-consumption
// check, exercising the post-consumption recomputation path.
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

// SetProtocolVersion overrides the advertised protocol version.
func (fb *fakeBridge) SetProtocolVersion(version string) {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	fb.protocolVersion = version
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

// AuthHeaders returns the Authorization headers observed by the fixture.
func (fb *fakeBridge) AuthHeaders() []string {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	return append([]string(nil), fb.authHeaders...)
}

// CheckpointBytes exposes the raw snapshot bytes the fixture persisted, so
// tests can recompute the digest out-of-band and prove the receipt is not
// an echo.
func (fb *fakeBridge) CheckpointBytes(checkpointId string) ([]byte, bool) {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	content, ok := fb.checkpoints[checkpointId]
	return append([]byte(nil), content...), ok
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

type sseOutputEvent struct {
	Stream string `json:"stream"`
	Data   string `json:"data"`
}

type sseExitEvent struct {
	ExitCode int  `json:"exitCode"`
	Signaled bool `json:"signaled"`
}

func emitSSE(w http.ResponseWriter, flusher http.Flusher, event string, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, body); err != nil {
		return
	}
	if flusher != nil {
		flusher.Flush()
	}
}

func appendLogLocked(box *fakeBridgeSandbox, line string) {
	box.logLines = append(box.logLines, line)
	if len(box.logLines) > maxLogLines {
		box.logLines = box.logLines[len(box.logLines)-maxLogLines:]
	}
}

// parseCheckpointPayload inverts the deterministic snapshot serialization of
// handlePersist.
func parseCheckpointPayload(payload []byte) (map[string][]byte, error) {
	files := map[string][]byte{}
	rest := payload
	for len(rest) > 0 {
		pathEnd := bytes.IndexByte(rest, 0)
		if pathEnd < 0 {
			return nil, errors.New("cloudflare fixture: truncated checkpoint path")
		}
		path := string(rest[:pathEnd])
		rest = rest[pathEnd+1:]
		sizeEnd := bytes.IndexByte(rest, 0)
		if sizeEnd < 0 {
			return nil, errors.New("cloudflare fixture: truncated checkpoint size")
		}
		size, err := strconv.Atoi(string(rest[:sizeEnd]))
		if err != nil || size < 0 {
			return nil, errors.New("cloudflare fixture: malformed checkpoint size")
		}
		rest = rest[sizeEnd+1:]
		if len(rest) < size {
			return nil, errors.New("cloudflare fixture: truncated checkpoint content")
		}
		files[path] = append([]byte(nil), rest[:size]...)
		rest = rest[size:]
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
// with deterministic retry settings and no retry delay.
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
// equivalence: identical Passed/ReasonCode and normalized business-trace
// outcome/invariant equivalence.
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

	fb.LoseContainer(t, alloc)

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
	if staged, ok := fb.SandboxFile(next, "staged/snapshot-source"); !ok || sandbox.RecomputeSHA256(staged) != sandbox.RecomputeSHA256(content) {
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

	fb.DropPathTimes("POST", "/v1/sandboxes/"+next+"/hydrate", 1)
	receipt, err := provider.Restore(ctx, sandbox.RestoreOperationRequest{
		Identity:             scenarioIdentity(name, next, "cmd-restore", 2),
		PreviousAllocationId: alloc,
		NextAllocationId:     next,
	})
	if err != nil {
		t.Fatalf("Restore must recover from one lost hydrate response through the idempotent retry, got %v", err)
	}
	if got := fb.RequestCount("POST", "/v1/sandboxes/"+next+"/hydrate"); got != 2 {
		t.Fatalf("exactly one retry after the lost response was expected, got %d hydrate calls", got)
	}
	if receipt.Allocation.AllocationId != next || receipt.Allocation.State != sandbox.AllocationActive {
		t.Fatalf("the restored allocation must be the active replacement, got %+v", receipt.Allocation)
	}
	if staged, ok := fb.SandboxFile(next, "staged/payload"); !ok || sandbox.RecomputeSHA256(staged) != sandbox.RecomputeSHA256(content) {
		t.Fatal("the idempotent recovery must restore the staged content byte for byte")
	}
}

// TestBridgeLostRestoreResponseReconcilesFailClosed freezes that a restore
// whose hydrate responses are lost beyond the retry budget surfaces as
// fail-closed drift through the independent reconcile path: the orphaned
// Bridge-side sandbox is reported, never silently adopted.
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

	// Every hydrate attempt and every compensating destroy is lost.
	fb.DropPathTimes("POST", "/v1/sandboxes/"+next+"/hydrate", 3)
	fb.DropPathTimes("DELETE", "/v1/sandboxes/"+next, 3)

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
		t.Fatal("reconcile must fail closed when an orphaned sandbox survives a lost restore response")
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
		t.Fatalf("the orphaned replacement sandbox must be reported, got %+v", report)
	}
}
