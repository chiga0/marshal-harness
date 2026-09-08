//go:build darwin || linux

package sqlitestore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/lifecycle"
)

// These storage fixtures are deliberately not producer-authority/Worker proof.
var testNamespace = authority.AuthorityNamespaceId{TenantNamespace: "local", ControlPlaneId: "test", AuthorityScopeId: "fixture"}
var testStreams = []Stream{{RB1, "rb1"}, {Run, "run-test"}, {Dispatch, "dispatch"}, {Provider, "provider"}}
var testTime = time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)

func canon(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = canonical.JSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func fact(t *testing.T, stream Stream, sequence uint64, label string) Record {
	t.Helper()
	v := map[string]any{"factType": "storage-fixture", "value": label}
	if stream.Journal != Provider {
		v["sequence"] = sequence
	}
	if stream.Journal == Run {
		v["runId"] = stream.ID
	}
	if stream.Journal == RB1 || stream.Journal == Dispatch {
		v["digest"] = ""
		digest := canonical.DigestBytes(canon(t, v))
		v["digest"] = digest
		return Record{Sequence: sequence, Digest: digest, Bytes: canon(t, v)}
	}
	raw := canon(t, v)
	return Record{Sequence: sequence, Digest: canonical.DigestBytes(raw), Bytes: raw}
}
func ref(stream Stream, record Record) Reference {
	return Reference{Stream: stream, Head: Head{Sequence: record.Sequence, Digest: record.Digest}}
}
func fixture(t *testing.T) (*Store, Owner, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "sqlite")
	s, err := Create(context.Background(), dir, testNamespace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	s.clock = func() time.Time { return testTime }
	owner, err := s.ClaimOwner(context.Background(), 0, canonical.DigestBytes([]byte("original-owner-acquisition")), testTime.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return s, owner, dir
}
func reopen(t *testing.T, s *Store, owner Owner, dir string) (*Store, Owner) {
	t.Helper()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := Open(context.Background(), dir, testNamespace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = next.Close() })
	next.clock = func() time.Time { return testTime }
	newOwner, err := next.ClaimOwner(context.Background(), owner.Generation, canonical.DigestBytes([]byte("next-owner")), testTime.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return next, newOwner
}

func transactionFixture(t *testing.T, tx *WriteTx, failAfter int) error {
	t.Helper()
	step := 0
	check := func(err error) error {
		if err != nil {
			return err
		}
		step++
		if step == failAfter {
			return errors.New("injected transaction interruption")
		}
		return nil
	}
	var source Reference
	for _, stream := range testStreams {
		record := fact(t, stream, 1, "original canonical record")
		_, err := tx.CompareAppend(stream, Head{}, []Record{record})
		if err = check(err); err != nil {
			return err
		}
		if stream.Journal == RB1 {
			source = ref(stream, record)
		}
	}
	if err := check(tx.PutProjection(0, Projection{Key: ProjectionKey{TaskProjection, "task-test"}, Revision: 1, Source: source, Bytes: []byte(`{"revision":1}`)})); err != nil {
		return err
	}
	if err := check(tx.PutReceipt(Receipt{Key: ReceiptKey{"task-test", "approve", canonical.DigestBytes([]byte("key"))}, RequestDigest: canonical.DigestBytes([]byte("request")), Source: source, Response: []byte(`{"accepted":true}`)})); err != nil {
		return err
	}
	return check(tx.Enqueue(Command{ID: "command-test", TaskID: "task-test", RunID: "run-test", Kind: StartCommand, Payload: []byte(`{"inputDigest":"fixture"}`), Source: source, Revision: 1, Status: Pending}))
}

func assertEmpty(t *testing.T, s *Store, owner Owner) {
	t.Helper()
	err := s.View(context.Background(), owner, func(tx *ReadTx) error {
		for _, stream := range testStreams {
			head, err := tx.Head(stream)
			if err != nil || head != (Head{}) {
				t.Fatalf("partial head: %+v, %v", head, err)
			}
			records, err := tx.Records(stream, 0, 10)
			if err != nil || len(records) != 0 {
				t.Fatalf("partial records: %d, %v", len(records), err)
			}
		}
		if _, found, err := tx.Projection(ProjectionKey{TaskProjection, "task-test"}); err != nil || found {
			t.Fatalf("partial projection: %v %v", found, err)
		}
		if _, found, err := tx.Receipt(ReceiptKey{"task-test", "approve", canonical.DigestBytes([]byte("key"))}, canonical.DigestBytes([]byte("request"))); err != nil || found {
			t.Fatalf("partial receipt: %v %v", found, err)
		}
		if commands, err := tx.Commands("", 10); err != nil || len(commands) != 0 {
			t.Fatalf("partial outbox: %d %v", len(commands), err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAllFourStreamsProjectionReceiptOutboxOneCommitAndColdOpen(t *testing.T) {
	s, owner, dir := fixture(t)
	if err := s.Update(context.Background(), owner, func(tx *WriteTx) error { return transactionFixture(t, tx, 0) }); err != nil {
		t.Fatal(err)
	}
	next, nextOwner := reopen(t, s, owner, dir)
	if nextOwner.StoreID != owner.StoreID || nextOwner.Generation != owner.Generation+1 {
		t.Fatal("identity did not survive reopen")
	}
	err := next.View(context.Background(), nextOwner, func(tx *ReadTx) error {
		for _, stream := range testStreams {
			records, err := tx.Records(stream, 0, 10)
			if err != nil || len(records) != 1 || !reflect.DeepEqual(records[0], fact(t, stream, 1, "original canonical record")) {
				t.Fatalf("original bytes changed: %v", err)
			}
		}
		projection, found, err := tx.Projection(ProjectionKey{TaskProjection, "task-test"})
		if err != nil || !found || projection.Revision != 1 {
			t.Fatalf("projection: %+v %v", projection, err)
		}
		receipt, found, err := tx.Receipt(ReceiptKey{"task-test", "approve", canonical.DigestBytes([]byte("key"))}, canonical.DigestBytes([]byte("request")))
		if err != nil || !found || string(receipt.Response) != `{"accepted":true}` {
			t.Fatalf("receipt: %v", err)
		}
		commands, err := tx.Commands("", 10)
		if err != nil || len(commands) != 1 || commands[0].Status != Pending {
			t.Fatalf("outbox: %+v %v", commands, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestEveryCallbackFailureRollsBackAllFacetsAcrossReopen(t *testing.T) {
	for point := 1; point <= 7; point++ {
		t.Run(string(rune('0'+point)), func(t *testing.T) {
			s, owner, dir := fixture(t)
			if err := s.Update(context.Background(), owner, func(tx *WriteTx) error { return transactionFixture(t, tx, point) }); err == nil {
				t.Fatal("fault committed")
			}
			assertEmpty(t, s, owner)
			s, owner = reopen(t, s, owner, dir)
			assertEmpty(t, s, owner)
		})
	}
}

// The child is this fixed test executable, never a Provider/Worker. Exiting
// without Close exercises SQLite's WAL recovery, not orderly connection flush.
func TestProcessInterruptionDoesNotExposePartialTransaction(t *testing.T) {
	for _, phase := range []string{"before-commit", "after-commit", "second-writer"} {
		t.Run(phase, func(t *testing.T) {
			s, owner, dir := fixture(t)
			if phase != "second-writer" {
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
			}
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			child := exec.CommandContext(ctx, executable, "-test.run=^TestStorageCrashHelper$")
			child.Env = append(os.Environ(), "MARSHAL_SQLITE_TEST_PHASE="+phase, "MARSHAL_SQLITE_TEST_DIRECTORY="+dir)
			output, err := child.CombinedOutput()
			var exited *exec.ExitError
			if !errors.As(err, &exited) || exited.ExitCode() != 23 {
				t.Fatalf("storage child did not reach interruption: %v %s", err, output)
			}
			if phase == "second-writer" {
				assertEmpty(t, s, owner)
				return
			}
			owner.Generation++ // Child's acquisition committed before its interruption.
			s, owner = reopen(t, s, owner, dir)
			if phase == "before-commit" {
				assertEmpty(t, s, owner)
				return
			}
			if err := s.View(context.Background(), owner, func(tx *ReadTx) error {
				for _, stream := range testStreams {
					head, err := tx.Head(stream)
					if err != nil || head != ref(stream, fact(t, stream, 1, "original canonical record")).Head {
						t.Fatalf("committed stream missing: %v", err)
					}
				}
				if _, found, err := tx.Projection(ProjectionKey{TaskProjection, "task-test"}); err != nil || !found {
					t.Fatalf("committed projection missing: %v", err)
				}
				if _, found, err := tx.Receipt(ReceiptKey{"task-test", "approve", canonical.DigestBytes([]byte("key"))}, canonical.DigestBytes([]byte("request"))); err != nil || !found {
					t.Fatalf("committed receipt missing: %v", err)
				}
				commands, err := tx.Commands("", 10)
				if err != nil || len(commands) != 1 || commands[0].Status != Pending {
					t.Fatalf("committed outbox missing: %v", err)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestStorageCrashHelper(t *testing.T) {
	phase := os.Getenv("MARSHAL_SQLITE_TEST_PHASE")
	if phase == "" {
		t.Skip("only invoked by bounded process interruption test")
	}
	s, err := Open(context.Background(), os.Getenv("MARSHAL_SQLITE_TEST_DIRECTORY"), testNamespace)
	if phase == "second-writer" {
		if !errors.Is(err, ErrBusy) {
			t.Fatalf("cross-process lock: %v", err)
		}
		os.Exit(23)
	}
	if err != nil {
		t.Fatal(err)
	}
	s.clock = func() time.Time { return testTime }
	owner, err := s.ClaimOwner(context.Background(), 1, canonical.DigestBytes([]byte("child-owner")), testTime.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	err = s.Update(context.Background(), owner, func(tx *WriteTx) error {
		if err := transactionFixture(t, tx, 0); err != nil {
			return err
		}
		if phase == "before-commit" {
			os.Exit(23)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if phase != "after-commit" {
		t.Fatal("unknown storage test phase")
	}
	os.Exit(23)
}

func TestIgnoredMethodErrorAndCancelledContextCannotCommitPrefix(t *testing.T) {
	s, owner, _ := fixture(t)
	for _, cancelDuring := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		err := s.Update(ctx, owner, func(tx *WriteTx) error {
			_, err := tx.CompareAppend(testStreams[0], Head{}, []Record{fact(t, testStreams[0], 1, "first")})
			if err != nil {
				return err
			}
			if cancelDuring {
				cancel()
			} else {
				_, _ = tx.CompareAppend(testStreams[1], Head{Sequence: 9, Digest: canonical.DigestBytes(nil)}, []Record{fact(t, testStreams[1], 10, "wrong CAS")})
			}
			return nil
		})
		cancel()
		if err == nil {
			t.Fatal("poisoned transaction committed")
		}
		assertEmpty(t, s, owner)
	}
}

func TestConcurrentSameHeadHasExactlyOneSuccessor(t *testing.T) {
	s, owner, _ := fixture(t)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, value := range []string{"one", "two"} {
		record := fact(t, testStreams[0], 1, value)
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- s.Update(context.Background(), owner, func(tx *WriteTx) error {
				_, err := tx.CompareAppend(testStreams[0], Head{}, []Record{record})
				return err
			})
		}()
	}
	wg.Wait()
	close(results)
	success, conflicts := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatalf("success=%d conflict=%d", success, conflicts)
	}
}

func TestSequenceDigestAlgorithmAndCanonicalBytesAreClosed(t *testing.T) {
	for _, mutation := range []string{"raw-for-detached", "body-sequence", "metadata-sequence", "wrong-head-digest", "noncanonical", "duplicate-member", "cross-run"} {
		t.Run(mutation, func(t *testing.T) {
			s, owner, _ := fixture(t)
			stream := testStreams[0]
			record := fact(t, stream, 1, "private fixture")
			head := Head{}
			switch mutation {
			case "raw-for-detached":
				record.Digest = canonical.DigestBytes(record.Bytes)
			case "body-sequence":
				record.Bytes = bytes.Replace(record.Bytes, []byte(`"sequence":1`), []byte(`"sequence":2`), 1)
			case "metadata-sequence":
				record.Sequence = 2
			case "wrong-head-digest":
				head = Head{Sequence: 1, Digest: canonical.DigestBytes(nil)}
			case "noncanonical":
				record.Bytes = append(record.Bytes, '\n')
			case "duplicate-member":
				record.Bytes = []byte(`{"PRIVATE_SECRET":1,"PRIVATE_SECRET":2,"sequence":1}`)
			case "cross-run":
				stream = Stream{Run, "run-other"}
				record = fact(t, testStreams[1], 1, "private fixture")
			}
			err := s.Update(context.Background(), owner, func(tx *WriteTx) error { _, err := tx.CompareAppend(stream, head, []Record{record}); return err })
			if err == nil || strings.Contains(err.Error(), "PRIVATE_SECRET") {
				t.Fatalf("unsafe error: %v", err)
			}
			assertEmpty(t, s, owner)
		})
	}
}

func TestExpiredOrStaleOwnerCannotReadOrWriteAndMidTransactionExpiryRollsBack(t *testing.T) {
	s, owner, _ := fixture(t)
	now := testTime
	s.clock = func() time.Time { return now }
	err := s.Update(context.Background(), owner, func(tx *WriteTx) error {
		_, err := tx.CompareAppend(testStreams[0], Head{}, []Record{fact(t, testStreams[0], 1, "never commit")})
		now = owner.ExpiresAt
		return err
	})
	if !errors.Is(err, ErrOwner) {
		t.Fatalf("expiry: %v", err)
	}
	called := false
	if err = s.View(context.Background(), owner, func(*ReadTx) error { called = true; return nil }); !errors.Is(err, ErrOwner) || called {
		t.Fatalf("expired read: %v", err)
	}
	if err = s.Update(context.Background(), owner, func(*WriteTx) error { called = true; return nil }); !errors.Is(err, ErrOwner) || called {
		t.Fatalf("expired write: %v", err)
	}
	now = testTime
	assertEmpty(t, s, owner)
	newOwner, err := s.ClaimOwner(context.Background(), owner.Generation, canonical.DigestBytes([]byte("next")), testTime.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for _, old := range []Owner{owner, {StoreID: "store-other", Generation: newOwner.Generation, IdentityDigest: newOwner.IdentityDigest, ExpiresAt: newOwner.ExpiresAt}} {
		if err := s.View(context.Background(), old, func(*ReadTx) error { t.Fatal("stale callback called"); return nil }); !errors.Is(err, ErrOwner) {
			t.Fatal(err)
		}
	}
	assertEmpty(t, s, newOwner)
}

func TestOwnerGenerationCASAndRenewalInvalidateOldTuple(t *testing.T) {
	s, owner, _ := fixture(t)
	if _, err := s.ClaimOwner(context.Background(), 0, owner.IdentityDigest, owner.ExpiresAt); !errors.Is(err, ErrOwner) {
		t.Fatal(err)
	}
	next, err := s.RenewOwner(context.Background(), owner, owner.ExpiresAt.Add(time.Minute))
	if err != nil || next.Generation != owner.Generation {
		t.Fatal(err)
	}
	if err = s.View(context.Background(), owner, func(*ReadTx) error { return nil }); !errors.Is(err, ErrOwner) {
		t.Fatalf("old expiry tuple: %v", err)
	}
	assertEmpty(t, s, next)
}

func TestReopenRequiresNewAcquisitionEvenWhenPreviousOwnerUnexpired(t *testing.T) {
	s, owner, dir := fixture(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := Open(context.Background(), dir, testNamespace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = next.Close() })
	next.clock = func() time.Time { return testTime }
	if err := next.View(context.Background(), owner, func(*ReadTx) error {
		t.Fatal("inherited owner read callback called")
		return nil
	}); !errors.Is(err, ErrOwner) {
		t.Fatal(err)
	}
	if err := next.Update(context.Background(), owner, func(*WriteTx) error {
		t.Fatal("inherited owner write callback called")
		return nil
	}); !errors.Is(err, ErrOwner) {
		t.Fatal(err)
	}
	if _, err := next.RenewOwner(context.Background(), owner, owner.ExpiresAt.Add(time.Minute)); !errors.Is(err, ErrOwner) {
		t.Fatal(err)
	}
	current, err := next.ClaimOwner(context.Background(), owner.Generation, canonical.DigestBytes([]byte("new-acquisition")), owner.ExpiresAt)
	if err != nil || current.Generation != owner.Generation+1 {
		t.Fatalf("new acquisition: %v", err)
	}
	assertEmpty(t, next, current)
}

func TestReceiptLookupPrecedesNewCommandCASAndScopeDoesNotAlias(t *testing.T) {
	s, owner, _ := fixture(t)
	if err := s.Update(context.Background(), owner, func(tx *WriteTx) error { return transactionFixture(t, tx, 0) }); err != nil {
		t.Fatal(err)
	}
	key := ReceiptKey{"task-test", "approve", canonical.DigestBytes([]byte("key"))}
	digest := canonical.DigestBytes([]byte("request"))
	if err := s.Update(context.Background(), owner, func(tx *WriteTx) error {
		receipt, found, err := tx.Receipt(key, digest)
		if err != nil || !found {
			return ErrConflict
		}
		if err = tx.PutReceipt(receipt); err != nil {
			return err
		}
		// No new revision/Attempt check on an exact already-accepted request.
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.View(context.Background(), owner, func(tx *ReadTx) error {
		other := key
		other.Scope = "task-other"
		if _, found, err := tx.Receipt(other, digest); err != nil || found {
			t.Fatalf("cross scope: %v", err)
		}
		_, _, err := tx.Receipt(key, canonical.DigestBytes([]byte("different request")))
		return err
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("different content: %v", err)
	}
}

func TestProjectionCASAndMissingReferenceRollbackNewFact(t *testing.T) {
	s, owner, _ := fixture(t)
	record := fact(t, testStreams[0], 1, "first")
	for _, missing := range []bool{true, false} {
		err := s.Update(context.Background(), owner, func(tx *WriteTx) error {
			if _, err := tx.CompareAppend(testStreams[0], Head{}, []Record{record}); err != nil {
				return err
			}
			source := ref(testStreams[0], record)
			expected := uint64(4)
			if missing {
				source.Head.Sequence = 9
				expected = 0
			}
			return tx.PutProjection(expected, Projection{Key: ProjectionKey{TaskProjection, "task-test"}, Revision: expected + 1, Source: source, Bytes: []byte(`{}`)})
		})
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("projection conflict: %v", err)
		}
		assertEmpty(t, s, owner)
	}
}

func TestUnknownOutboxSurvivesRestartWithoutResetOrSend(t *testing.T) {
	s, owner, dir := fixture(t)
	if err := s.Update(context.Background(), owner, func(tx *WriteTx) error { return transactionFixture(t, tx, 0) }); err != nil {
		t.Fatal(err)
	}
	first := fact(t, testStreams[0], 1, "original canonical record")
	unknown := fact(t, testStreams[0], 2, "ack unavailable")
	if err := s.Update(context.Background(), owner, func(tx *WriteTx) error {
		if _, err := tx.CompareAppend(testStreams[0], ref(testStreams[0], first).Head, []Record{unknown}); err != nil {
			return err
		}
		return tx.ObserveCommand("command-test", 1, Unknown, ref(testStreams[0], unknown))
	}); err != nil {
		t.Fatal(err)
	}
	s, owner = reopen(t, s, owner, dir)
	var command Command
	if err := s.Update(context.Background(), owner, func(tx *WriteTx) error {
		value, found, err := tx.Command("command-test")
		if err != nil || !found || value.Status != Unknown || value.Revision != 2 {
			t.Fatalf("unknown lost: %+v %v", value, err)
		}
		command = value
		value.Status, value.Revision, value.Observation = Pending, 1, nil
		return tx.Enqueue(value)
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.View(context.Background(), owner, func(tx *ReadTx) error {
		value, _, err := tx.Command(command.ID)
		if !reflect.DeepEqual(value, command) {
			t.Fatal("exact enqueue reset unknown")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(context.Background(), owner, func(tx *WriteTx) error { return tx.ObserveCommand(command.ID, 2, Pending, *command.Observation) }); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	observed := fact(t, testStreams[0], 3, "known receipt")
	if err := s.Update(context.Background(), owner, func(tx *WriteTx) error {
		if _, err := tx.CompareAppend(testStreams[0], ref(testStreams[0], unknown).Head, []Record{observed}); err != nil {
			return err
		}
		return tx.ObserveCommand(command.ID, 2, Observed, ref(testStreams[0], observed))
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(context.Background(), owner, func(tx *WriteTx) error {
		return tx.ObserveCommand(command.ID, 3, Unknown, ref(testStreams[0], observed))
	}); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
}

func TestExistingLifecycleReducerBytesRoundTrip(t *testing.T) {
	s, owner, dir := fixture(t)
	initial := domain.NewRunState("task-test", "run-test", testTime)
	event := domain.RunEvent{RunID: initial.RunID, EventID: "event-test", Sequence: 1, Type: "plan.completed", StateFrom: domain.StateCreated, StateTo: domain.StatePlanned, Timestamp: testTime.Add(time.Second), Payload: map[string]any{}}
	reduced, err := lifecycle.Reduce(initial, event, lifecycle.Guard{LeaseHeld: true, DraftValid: true})
	if err != nil {
		t.Fatal(err)
	}
	raw := canon(t, event)
	record := Record{Sequence: 1, Digest: canonical.DigestBytes(raw), Bytes: raw}
	if err := s.Update(context.Background(), owner, func(tx *WriteTx) error {
		if _, err := tx.CompareAppend(testStreams[1], Head{}, []Record{record}); err != nil {
			return err
		}
		return tx.PutProjection(0, Projection{Key: ProjectionKey{RunProjection, event.RunID}, Revision: 1, Source: ref(testStreams[1], record), Bytes: canon(t, reduced)})
	}); err != nil {
		t.Fatal(err)
	}
	s, owner = reopen(t, s, owner, dir)
	if err := s.View(context.Background(), owner, func(tx *ReadTx) error {
		records, err := tx.Records(testStreams[1], 0, 1)
		if err != nil {
			return err
		}
		var original domain.RunEvent
		if err = json.Unmarshal(records[0].Bytes, &original); err != nil {
			return err
		}
		replayed, err := lifecycle.Replay(initial, original)
		if err != nil {
			return err
		}
		stored, found, err := tx.Projection(ProjectionKey{RunProjection, event.RunID})
		if err != nil || !found || !bytes.Equal(stored.Bytes, canon(t, replayed)) {
			t.Fatalf("new reducer semantics: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestReadSnapshotDoesNotBlockUnrelatedWriter(t *testing.T) {
	s, owner, _ := fixture(t)
	done := make(chan error, 1)
	err := s.View(context.Background(), owner, func(tx *ReadTx) error {
		before, err := tx.Head(testStreams[0])
		if err != nil {
			return err
		}
		record := fact(t, testStreams[0], 1, "concurrent")
		go func() {
			done <- s.Update(context.Background(), owner, func(write *WriteTx) error {
				_, err := write.CompareAppend(testStreams[0], Head{}, []Record{record})
				return err
			})
		}()
		select {
		case err := <-done:
			if err != nil {
				return err
			}
		case <-time.After(3 * time.Second):
			return errors.New("reader blocked writer")
		}
		after, err := tx.Head(testStreams[0])
		if before != after {
			t.Fatal("read transaction mixed snapshots")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRetainedTransactionsAndClosedStoreFail(t *testing.T) {
	s, owner, _ := fixture(t)
	var retained *WriteTx
	if err := s.Update(context.Background(), owner, func(tx *WriteTx) error { retained = tx; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := retained.Head(testStreams[0]); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	if _, err := retained.CompareAppend(testStreams[0], Head{}, []Record{fact(t, testStreams[0], 1, "late")}); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.View(context.Background(), owner, func(*ReadTx) error { return nil }); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}

func TestRecordTransactionAndPageBounds(t *testing.T) {
	s, owner, _ := fixture(t)
	oversize := fact(t, testStreams[0], 1, strings.Repeat("x", MaxRecordBytes))
	if err := s.Update(context.Background(), owner, func(tx *WriteTx) error {
		_, err := tx.CompareAppend(testStreams[0], Head{}, []Record{oversize})
		return err
	}); !errors.Is(err, ErrLimit) {
		t.Fatal(err)
	}
	if err := s.Update(context.Background(), owner, func(tx *WriteTx) error {
		head := Head{}
		for n := uint64(1); n <= 10; n++ {
			var err error
			head, err = tx.CompareAppend(testStreams[0], head, []Record{fact(t, testStreams[0], n, strings.Repeat("x", 900<<10))})
			if err != nil {
				return err
			}
		}
		return nil
	}); !errors.Is(err, ErrLimit) {
		t.Fatalf("transaction bound: %v", err)
	}
	assertEmpty(t, s, owner)
	if err := s.View(context.Background(), owner, func(tx *ReadTx) error { _, err := tx.Records(testStreams[0], 0, MaxPage+1); return err }); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}

func TestSecondWriterAndUnsafePathsFail(t *testing.T) {
	s, owner, dir := fixture(t)
	if _, err := Open(context.Background(), dir, testNamespace); !errors.Is(err, ErrBusy) {
		t.Fatalf("second writer: %v", err)
	}
	link := filepath.Join(filepath.Dir(dir), "alias")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), link, testNamespace); err == nil {
		t.Fatal("symlink accepted")
	}
	if err := os.Chmod(filepath.Join(dir, databaseName), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.View(context.Background(), owner, func(*ReadTx) error { return nil }); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
}

func TestOpenMissingCorruptWrongNamespaceAndVersionNeverInitializes(t *testing.T) {
	for _, kind := range []string{"missing-db", "wrong-namespace", "new-version", "missing-table", "corrupt"} {
		t.Run(kind, func(t *testing.T) {
			s, _, dir := fixture(t)
			if kind == "new-version" {
				if _, err := s.writer.Exec("PRAGMA user_version=2"); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "missing-table" {
				if _, err := s.writer.Exec("DROP TABLE outbox"); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			ns := testNamespace
			if kind == "missing-db" {
				if err := os.Remove(filepath.Join(dir, databaseName)); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "corrupt" {
				if err := os.WriteFile(filepath.Join(dir, databaseName), []byte("PRIVATE_SECRET_NOT_A_DATABASE"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "wrong-namespace" {
				ns.AuthorityScopeId = "other"
			}
			before, beforeErr := os.ReadFile(filepath.Join(dir, databaseName))
			if _, err := Open(context.Background(), dir, ns); err == nil || strings.Contains(err.Error(), "PRIVATE_SECRET") {
				t.Fatalf("invalid root accepted/leaked: %v", err)
			}
			after, afterErr := os.ReadFile(filepath.Join(dir, databaseName))
			if !bytes.Equal(before, after) || errors.Is(beforeErr, os.ErrNotExist) != errors.Is(afterErr, os.ErrNotExist) {
				t.Fatal("Open repaired or created invalid state")
			}
		})
	}
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(parent, "absent")
	if _, err := Open(context.Background(), missing, testNamespace); err == nil {
		t.Fatal("missing root accepted")
	}
	if _, err := os.Lstat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("Open created a root")
	}
}

func TestPrivateFilesAndCreateRefusesAnyPriorState(t *testing.T) {
	s, _, dir := fixture(t)
	for _, name := range []string{databaseName, "owner.lock", databaseName + "-wal", databaseName + "-shm"} {
		info, err := os.Lstat(filepath.Join(dir, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("not private %s: %v", name, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(context.Background(), dir, testNamespace); err == nil {
		t.Fatal("Create accepted prior database")
	}
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "old-ledger"), []byte("retain"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(context.Background(), parent, testNamespace); err == nil {
		t.Fatal("Create accepted old root")
	}
	if _, err := os.Lstat(filepath.Join(parent, "owner.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("Create modified old root")
	}
}

func TestCreateSyncsChildThenHeldParentAndPreservesSyncFailure(t *testing.T) {
	for _, failAt := range []string{"", "child", "parent"} {
		t.Run("fail-at-"+failAt, func(t *testing.T) {
			parent, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(parent, "sqlite")
			var order []string
			store, err := open(context.Background(), dir, testNamespace, true, func(files *privateFiles) error {
				if files.parent == nil {
					t.Fatal("new root did not retain its parent")
				}
				return files.syncDirectories(func(file *os.File) error {
					name := "child"
					if file == files.parent {
						name = "parent"
					} else if file != files.root {
						t.Fatal("sync used an unheld directory")
					}
					order = append(order, name)
					if name == failAt {
						return errors.New("injected directory sync failure")
					}
					return file.Sync()
				})
			})
			want := []string{"child", "parent"}
			if failAt == "child" {
				want = []string{"child"}
			}
			if !reflect.DeepEqual(order, want) {
				t.Fatalf("sync order: got %v want %v", order, want)
			}
			if failAt == "" {
				if err != nil || store == nil {
					t.Fatalf("successful sync unavailable: %v", err)
				}
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
				return
			}
			if !errors.Is(err, ErrUnavailable) || store != nil {
				t.Fatalf("failed sync returned usable store: %v", err)
			}
			before, err := os.ReadFile(filepath.Join(dir, databaseName))
			if err != nil || len(before) == 0 {
				t.Fatalf("uncertain database removed: %v", err)
			}
			if _, err := os.Stat(filepath.Join(dir, "owner.lock")); err != nil {
				t.Fatalf("uncertain lock removed: %v", err)
			}
			if retried, err := Create(context.Background(), dir, testNamespace); err == nil || retried != nil {
				t.Fatal("Create silently reset uncertain state")
			}
			after, err := os.ReadFile(filepath.Join(dir, databaseName))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("retry changed uncertain database")
			}
		})
	}
}

func TestCreateRejectsParentReplacementDuringSync(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(base, "parent")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := open(context.Background(), filepath.Join(parent, "sqlite"), testNamespace, true, func(files *privateFiles) error {
		return files.syncDirectories(func(file *os.File) error {
			if file != files.root {
				t.Fatal("replacement parent reached sync")
			}
			if err := file.Sync(); err != nil {
				return err
			}
			if err := os.Rename(parent, parent+"-moved"); err != nil {
				return err
			}
			return os.Mkdir(parent, 0o700)
		})
	})
	if !errors.Is(err, ErrUnavailable) || store != nil {
		t.Fatalf("renamed parent returned usable store: %v", err)
	}
	if _, err := os.Stat(filepath.Join(parent+"-moved", "sqlite", databaseName)); err != nil {
		t.Fatalf("uncertain original state removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(parent, "sqlite")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("replacement parent was initialized")
	}
}

func TestFailedCreateRequiresParentSyncBeforeOpenClaimAndRestart(t *testing.T) {
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(parent, "sqlite")
	openObserved := func(create, failParent bool) (*Store, error) {
		var order []string
		store, err := open(context.Background(), dir, testNamespace, create, func(files *privateFiles) error {
			if files.parent == nil {
				t.Fatal("recovery did not retain parent")
			}
			return files.syncDirectories(func(file *os.File) error {
				switch file {
				case files.root:
					order = append(order, "child")
				case files.parent:
					order = append(order, "parent")
					if failParent {
						return errors.New("parent sync still unavailable")
					}
				default:
					t.Fatal("sync without held capability")
				}
				return file.Sync()
			})
		})
		if !reflect.DeepEqual(order, []string{"child", "parent"}) {
			t.Fatalf("recovery skipped directory durability: %v", order)
		}
		if failParent && (store != nil || !errors.Is(err, ErrUnavailable)) {
			t.Fatalf("failed parent sync exposed a claimable store: %v", err)
		}
		return store, err
	}
	_, _ = openObserved(true, true)
	original, err := os.ReadFile(filepath.Join(dir, databaseName))
	if err != nil || len(original) == 0 {
		t.Fatalf("failed Create lost committed schema: %v", err)
	}
	_, _ = openObserved(false, true)
	unchanged := func() {
		t.Helper()
		current, err := os.ReadFile(filepath.Join(dir, databaseName))
		if err != nil || !bytes.Equal(original, current) {
			t.Fatal("Open rewrote initialized database before acquisition")
		}
	}
	unchanged()
	s, err := openObserved(false, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	unchanged()
	s.clock = func() time.Time { return testTime }
	info, err := s.Info(context.Background())
	if err != nil || info.Generation != 0 {
		t.Fatalf("recovery fabricated an owner: %v", err)
	}
	owner, err := s.ClaimOwner(context.Background(), 0, canonical.DigestBytes([]byte("after-parent-sync")), testTime.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	record := fact(t, testStreams[0], 1, "preserve after recovered initialization")
	if err := s.Update(context.Background(), owner, func(tx *WriteTx) error {
		_, err := tx.CompareAppend(testStreams[0], Head{}, []Record{record})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := openObserved(false, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = next.Close() })
	next.clock = func() time.Time { return testTime }
	if err := next.View(context.Background(), owner, func(*ReadTx) error {
		t.Fatal("restart inherited prior claim")
		return nil
	}); !errors.Is(err, ErrOwner) {
		t.Fatal(err)
	}
	nextOwner, err := next.ClaimOwner(context.Background(), owner.Generation, canonical.DigestBytes([]byte("after-restart")), testTime.Add(time.Hour))
	if err != nil || nextOwner.StoreID != info.StoreID || nextOwner.Generation != owner.Generation+1 {
		t.Fatalf("restart replaced original store or generation: %v", err)
	}
	if err := next.View(context.Background(), nextOwner, func(tx *ReadTx) error {
		records, err := tx.Records(testStreams[0], 0, 1)
		if err != nil || len(records) != 1 || !reflect.DeepEqual(records[0], record) {
			t.Fatalf("restart changed original record: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
