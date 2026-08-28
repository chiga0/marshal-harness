package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
)

// testIdentity builds one valid idempotency identity bound to a fixed
// authority key space and scope.
func testIdentity(key string) Identity {
	return Identity{
		Namespace: authority.AuthorityNamespaceId{
			TenantNamespace:  "local",
			ControlPlaneId:   "default",
			AuthorityScopeId: "repo:/fixture/repository",
		},
		Scope:     "repo:/fixture/repository",
		Operation: "fixture.create",
		Resource:  "fixture-1",
		Key:       key,
	}
}

func testDigest(seed string) string {
	return canonical.DigestBytes([]byte(seed))
}

func countingExecutor(count *int, result string) func() (json.RawMessage, int, error) {
	return func() (json.RawMessage, int, error) {
		*count++
		return json.RawMessage(result), http.StatusCreated, nil
	}
}

// TestSubmitRecordsAndMergesReplay proves the idempotent merge contract: the
// identical (authorityNamespaceId, scope, idempotencyKey) with the identical
// requestDigest returns the stored result verbatim and never executes the
// business operation a second time.
func TestSubmitRecordsAndMergesReplay(t *testing.T) {
	store := NewIdempotencyStore(filepath.Join(t.TempDir(), "idempotency"), nil)
	identity := testIdentity("key-merge")
	digest := testDigest("request-merge")
	count := 0

	first, err := store.Submit(identity, digest, countingExecutor(&count, `{"created":true}`))
	if err != nil {
		t.Fatalf("Submit rejected the first submission: %v", err)
	}
	if first.Replayed || first.Status != http.StatusCreated || string(first.Result) != `{"created":true}` {
		t.Fatalf("first submission = %+v", first)
	}

	second, err := store.Submit(identity, digest, countingExecutor(&count, `{"created":true}`))
	if err != nil {
		t.Fatalf("Submit rejected the identical replay: %v", err)
	}
	if !second.Replayed || second.Status != http.StatusCreated || string(second.Result) != `{"created":true}` {
		t.Fatalf("replay = %+v", second)
	}
	if count != 1 {
		t.Fatalf("the replay executed the business operation again: count=%d", count)
	}

	// The durable authority record carries the complete frozen ADR 0018 §3
	// submission identity quadruple: authorityNamespaceId, scope,
	// idempotencyKey and requestDigest.
	record, found, err := store.Get(identity)
	if err != nil || !found {
		t.Fatalf("the accepted submission must persist an authority record: found=%v err=%v", found, err)
	}
	if record.Kind != idempotencyRecordKind {
		t.Fatalf("record kind = %q, want %q", record.Kind, idempotencyRecordKind)
	}
	if !record.AuthorityNamespaceId.Equal(identity.Namespace) || record.Scope != identity.Scope ||
		record.Operation != identity.Operation || record.Resource != identity.Resource ||
		record.IdempotencyKey != identity.Key || record.RequestDigest != digest || record.Phase != idempotencyPhaseCompleted {
		t.Fatalf("the durable record lost the frozen submission identity quadruple: %+v", record)
	}
}

// TestSubmitConflictsOnDifferentDigest proves the fail-closed conflict rule:
// the identical identity and idempotency key with a different requestDigest
// never merges, never overwrites and never executes.
func TestSubmitConflictsOnDifferentDigest(t *testing.T) {
	store := NewIdempotencyStore(filepath.Join(t.TempDir(), "idempotency"), nil)
	identity := testIdentity("key-conflict")
	count := 0

	if _, err := store.Submit(identity, testDigest("request-original"), countingExecutor(&count, `{"created":true}`)); err != nil {
		t.Fatalf("Submit rejected the original submission: %v", err)
	}

	conflictCount := 0
	_, err := store.Submit(identity, testDigest("request-conflicting"), countingExecutor(&conflictCount, `{"other":true}`))
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("same key with a different digest must conflict fail closed, got %v", err)
	}
	if conflictCount != 0 {
		t.Fatalf("the conflicting submission executed the business operation")
	}

	record, found, err := store.Get(identity)
	if err != nil || !found {
		t.Fatalf("Get after the conflict: found=%v err=%v", found, err)
	}
	if record.RequestDigest != testDigest("request-original") || string(record.Result) != `{"created":true}` {
		t.Fatalf("the conflict modified the existing record: %+v", record)
	}
	if count != 1 {
		t.Fatalf("expected exactly one accepted execution, got %d", count)
	}
}

// TestSubmitSeparateKeys proves distinct idempotency keys are independent
// authority records.
func TestSubmitSeparateKeys(t *testing.T) {
	store := NewIdempotencyStore(filepath.Join(t.TempDir(), "idempotency"), nil)
	count := 0
	if _, err := store.Submit(testIdentity("key-one"), testDigest("request-one"), countingExecutor(&count, `{"id":1}`)); err != nil {
		t.Fatalf("Submit key-one: %v", err)
	}
	if _, err := store.Submit(testIdentity("key-two"), testDigest("request-two"), countingExecutor(&count, `{"id":2}`)); err != nil {
		t.Fatalf("Submit key-two: %v", err)
	}
	if count != 2 {
		t.Fatalf("distinct keys must each execute once, got %d", count)
	}
	first, found, err := store.Get(testIdentity("key-one"))
	if err != nil || !found || string(first.Result) != `{"id":1}` {
		t.Fatalf("key-one record: found=%v err=%v result=%s", found, err, first.Result)
	}
	second, found, err := store.Get(testIdentity("key-two"))
	if err != nil || !found || string(second.Result) != `{"id":2}` {
		t.Fatalf("key-two record: found=%v err=%v result=%s", found, err, second.Result)
	}
}

func TestSubmitSameClientKeySeparatesOperationAndResource(t *testing.T) {
	store := NewIdempotencyStore(filepath.Join(t.TempDir(), "idempotency"), nil)
	digest := testDigest("same-request")
	first := testIdentity("shared-client-key")
	first.Operation, first.Resource = "run.start", "run-one"
	second := first
	second.Resource = "run-two"
	third := first
	third.Operation = "run.approval"
	count := 0
	for _, identity := range []Identity{first, second, third} {
		if _, err := store.Submit(identity, digest, countingExecutor(&count, `{"accepted":true}`)); err != nil {
			t.Fatalf("Submit %+v: %v", identity, err)
		}
	}
	if count != 3 {
		t.Fatalf("operation/resource identities collapsed into %d executions, want 3", count)
	}
}

// TestSubmitRejectsInvalidIdentity fails closed on every malformed identity
// before any executor could run.
func TestSubmitRejectsInvalidIdentity(t *testing.T) {
	store := NewIdempotencyStore(filepath.Join(t.TempDir(), "idempotency"), nil)
	validDigest := testDigest("request-valid")

	blankNamespace := testIdentity("key-blank-namespace")
	blankNamespace.Namespace.TenantNamespace = "  "
	mismatchedScope := testIdentity("key-mismatched-scope")
	mismatchedScope.Scope = "repo:/other/repository"
	blankOperation := testIdentity("key-blank-operation")
	blankOperation.Operation = ""
	blankResource := testIdentity("key-blank-resource")
	blankResource.Resource = ""

	cases := []struct {
		name     string
		identity Identity
		digest   string
	}{
		{"blank namespace member", blankNamespace, validDigest},
		{"empty key", testIdentity(""), validDigest},
		{"scope mismatch", mismatchedScope, validDigest},
		{"blank operation", blankOperation, validDigest},
		{"blank resource", blankResource, validDigest},
		{"digest without prefix", testIdentity("key-digest"), "md5:deadbeef"},
		{"digest too short", testIdentity("key-digest"), "sha256:abc"},
	}
	for _, testCase := range cases {
		count := 0
		_, err := store.Submit(testCase.identity, testCase.digest, countingExecutor(&count, `{"created":true}`))
		if err == nil {
			t.Fatalf("%s: Submit accepted an invalid identity", testCase.name)
		}
		if count != 0 {
			t.Fatalf("%s: the executor ran despite the invalid identity", testCase.name)
		}
	}
}

// TestSubmitFailedExecutorPreservesPendingIntent proves a failed business
// operation keeps one durable, retryable intent without storing sensitive
// error text.
func TestSubmitFailedExecutorPreservesPendingIntent(t *testing.T) {
	store := NewIdempotencyStore(filepath.Join(t.TempDir(), "idempotency"), nil)
	identity := testIdentity("key-retry")
	digest := testDigest("request-retry")

	businessErr := errors.New("business rejected")
	_, err := store.Submit(identity, digest, func() (json.RawMessage, int, error) {
		return nil, 0, businessErr
	})
	if !errors.Is(err, businessErr) {
		t.Fatalf("Submit must surface the executor error, got %v", err)
	}
	record, found, getErr := store.Get(identity)
	if getErr != nil || !found || record.Phase != idempotencyPhasePending || record.LastFailureReason != "executor-failed" {
		t.Fatalf("failed submission intent: found=%v record=%+v err=%v", found, record, getErr)
	}
	if strings.Contains(string(record.Intent), businessErr.Error()) || record.LastFailureCode != "INTERNAL" {
		t.Fatalf("failed intent leaked diagnostics or lost stable code: %+v", record)
	}

	count := 0
	outcome, err := store.Submit(identity, digest, countingExecutor(&count, `{"retried":true}`))
	if err != nil {
		t.Fatalf("the identical request must stay retryable: %v", err)
	}
	if outcome.Replayed || count != 1 || string(outcome.Result) != `{"retried":true}` {
		t.Fatalf("retry outcome = %+v count=%d", outcome, count)
	}
}

// TestStoreReopensDurable proves the records survive a store rebuild: the
// idempotency facts are durable files under the state root, not memory.
func TestStoreReopensDurable(t *testing.T) {
	root := filepath.Join(t.TempDir(), "idempotency")
	identity := testIdentity("key-durable")
	digest := testDigest("request-durable")
	count := 0

	first := NewIdempotencyStore(root, func() time.Time { return time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC) })
	if _, err := first.Submit(identity, digest, countingExecutor(&count, `{"durable":true}`)); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	reopened := NewIdempotencyStore(root, nil)
	outcome, err := reopened.Submit(identity, digest, countingExecutor(&count, `{"durable":true}`))
	if err != nil {
		t.Fatalf("the reopened store rejected the identical replay: %v", err)
	}
	if !outcome.Replayed || string(outcome.Result) != `{"durable":true}` {
		t.Fatalf("reopened replay = %+v", outcome)
	}
	if count != 1 {
		t.Fatalf("the reopened store executed the business operation again: count=%d", count)
	}
	record, found, err := reopened.Get(identity)
	if err != nil || !found {
		t.Fatalf("Get on the reopened store: found=%v err=%v", found, err)
	}
	if record.CreatedAt != time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC) {
		t.Fatalf("the durable record lost its frozen createdAt: %v", record.CreatedAt)
	}
}

// TestSubmitConcurrentSameKeySerializes proves concurrent identical
// submissions serialize: exactly one business execution, every other caller
// merges into the stored result.
func TestSubmitConcurrentSameKeySerializes(t *testing.T) {
	store := NewIdempotencyStore(filepath.Join(t.TempDir(), "idempotency"), nil)
	identity := testIdentity("key-concurrent")
	digest := testDigest("request-concurrent")

	var mu sync.Mutex
	count := 0
	var group sync.WaitGroup
	results := make([]Outcome, 8)
	errs := make([]error, 8)
	for index := range results {
		group.Add(1)
		go func(slot int) {
			defer group.Done()
			outcome, err := store.Submit(identity, digest, func() (json.RawMessage, int, error) {
				mu.Lock()
				count++
				mu.Unlock()
				return json.RawMessage(`{"once":true}`), http.StatusCreated, nil
			})
			results[slot] = outcome
			errs[slot] = err
		}(index)
	}
	group.Wait()

	mu.Lock()
	executions := count
	mu.Unlock()
	if executions != 1 {
		t.Fatalf("concurrent identical submissions executed %d times, want 1", executions)
	}
	replays := 0
	for index := range results {
		if errs[index] != nil {
			t.Fatalf("submission %d failed: %v", index, errs[index])
		}
		if string(results[index].Result) != `{"once":true}` {
			t.Fatalf("submission %d returned a divergent result: %s", index, results[index].Result)
		}
		if results[index].Replayed {
			replays++
		}
	}
	if replays != len(results)-1 {
		t.Fatalf("expected %d merged replays, got %d", len(results)-1, replays)
	}
}

func TestLongCommandDoesNotBlockDifferentIdentity(t *testing.T) {
	store := NewIdempotencyStore(filepath.Join(t.TempDir(), "idempotency"), nil)
	started := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		_, err := store.Submit(testIdentity("key-long"), testDigest("long"), func() (json.RawMessage, int, error) {
			close(started)
			<-release
			return json.RawMessage(`{"long":true}`), http.StatusCreated, nil
		})
		firstDone <- err
	}()
	<-started

	secondDone := make(chan error, 1)
	go func() {
		_, err := store.Submit(testIdentity("key-short"), testDigest("short"), countingExecutor(new(int), `{"short":true}`))
		secondDone <- err
	}()
	select {
	case err := <-secondDone:
		if err != nil {
			t.Fatalf("different identity failed while long command ran: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("different identity was blocked by the long command")
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("long command failed: %v", err)
	}
}

func TestPendingCommandRecoversFromDurableIntent(t *testing.T) {
	root := filepath.Join(t.TempDir(), "idempotency")
	store := NewIdempotencyStore(root, nil)
	identity := testIdentity("key-pending")
	digest := testDigest("pending")
	intent := json.RawMessage(`{"sequence":7}`)
	path, _ := store.recordPaths(identity)
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := store.writeRecord(path, Record{
		APIVersion: domain.APIVersionV1Alpha1, Kind: idempotencyRecordKind,
		AuthorityNamespaceId: identity.Namespace, Scope: identity.Scope,
		Operation: identity.Operation, Resource: identity.Resource,
		IdempotencyKey: identity.Key, RequestDigest: digest,
		Phase: idempotencyPhasePending, Intent: intent, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	prepared := 0
	outcome, err := store.SubmitCommand(identity, digest, func() (json.RawMessage, error) {
		prepared++
		return nil, errors.New("must not replace durable intent")
	}, func(recovery bool, got json.RawMessage) (json.RawMessage, int, error) {
		if !recovery || string(got) != string(intent) {
			t.Fatalf("recovery=%v intent=%s", recovery, got)
		}
		return json.RawMessage(`{"recovered":true}`), http.StatusAccepted, nil
	})
	if err != nil || prepared != 0 || outcome.Replayed || outcome.Status != http.StatusAccepted {
		t.Fatalf("pending recovery outcome=%+v prepared=%d err=%v", outcome, prepared, err)
	}
	replay, err := store.Submit(identity, digest, countingExecutor(new(int), `{"wrong":true}`))
	if err != nil || !replay.Replayed || string(replay.Result) != `{"recovered":true}` {
		t.Fatalf("completed recovery replay=%+v err=%v", replay, err)
	}
}

// TestSubmitRejectsNonSuccessStatus proves executors cannot persist a
// non-success result as an idempotency fact.
func TestSubmitRejectsNonSuccessStatus(t *testing.T) {
	store := NewIdempotencyStore(filepath.Join(t.TempDir(), "idempotency"), nil)
	_, err := store.Submit(testIdentity("key-status"), testDigest("request-status"), func() (json.RawMessage, int, error) {
		return json.RawMessage(`{}`), http.StatusBadRequest, nil
	})
	if err == nil {
		t.Fatal("Submit accepted a non-success executor status")
	}
}

// TestRecordOwnershipExcludesActorKeySpace proves the ADR 0018 §7 negative
// fixture: the idempotency authority record belongs to the authority-side
// key space only. The record carries the complete authorityNamespaceId
// triple and never an actor-side securityDomainId attribution, at any depth.
func TestRecordOwnershipExcludesActorKeySpace(t *testing.T) {
	store := NewIdempotencyStore(filepath.Join(t.TempDir(), "idempotency"), nil)
	identity := testIdentity("key-ownership")
	count := 0
	if _, err := store.Submit(identity, testDigest("request-ownership"), countingExecutor(&count, `{"owned":true}`)); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	record, found, err := store.Get(identity)
	if err != nil || !found {
		t.Fatalf("Get: found=%v err=%v", found, err)
	}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	namespace, ok := decoded["authorityNamespaceId"].(map[string]any)
	if !ok {
		t.Fatalf("the record does not carry an authorityNamespaceId object: %s", data)
	}
	for _, member := range []string{"tenantNamespace", "controlPlaneId", "authorityScopeId"} {
		if value, ok := namespace[member].(string); !ok || value == "" {
			t.Fatalf("authorityNamespaceId lacks the frozen member %s: %v", member, namespace)
		}
	}
	if len(namespace) != 3 {
		t.Fatalf("authorityNamespaceId carries members beyond the frozen triple: %v", namespace)
	}
	for _, actorField := range []string{"securityDomainId", "trustDomainKind", "isolationDomainId"} {
		if containsMember(decoded, actorField) {
			t.Fatalf("the authority record carries the actor-side key space member %s: %s", actorField, data)
		}
	}
}

func containsMember(value any, name string) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, element := range typed {
			if key == name || containsMember(element, name) {
				return true
			}
		}
	case []any:
		for _, element := range typed {
			if containsMember(element, name) {
				return true
			}
		}
	}
	return false
}

// TestSubmitFailsClosedOnTamperedRecord proves a durable record whose
// authorityNamespaceId no longer matches the submission identity never
// merges and never executes: the quadruple binding is rechecked on every
// replay.
func TestSubmitFailsClosedOnTamperedRecord(t *testing.T) {
	store := NewIdempotencyStore(filepath.Join(t.TempDir(), "idempotency"), nil)
	identity := testIdentity("key-tampered")
	digest := testDigest("request-tampered")
	count := 0
	if _, err := store.Submit(identity, digest, countingExecutor(&count, `{"created":true}`)); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	recordPath, _ := store.recordPaths(identity)
	data, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	var record Record
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	record.AuthorityNamespaceId.TenantNamespace = "forged-tenant"
	forged, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recordPath, forged, 0o600); err != nil {
		t.Fatal(err)
	}
	executions := count
	_, err = store.Submit(identity, digest, countingExecutor(&count, `{"created":true}`))
	if err == nil {
		t.Fatal("Submit accepted a record whose authorityNamespaceId does not match the submission identity")
	}
	if count != executions {
		t.Fatal("the tampered record executed the business operation")
	}
}
