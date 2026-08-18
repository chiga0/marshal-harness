package provider

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func td(b byte) string {
	value := make([]byte, 71)
	copy(value, "sha256:")
	for i := 7; i < len(value); i++ {
		value[i] = "0123456789abcdef"[b&15]
	}
	return string(value)
}

func authorityDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func authorityIdentity(t *testing.T, dir string) AuthorityRootIdentity {
	t.Helper()
	identity, err := MeasureAuthorityRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

type fakeAnchor struct {
	mu       sync.Mutex
	current  AnchorSnapshot
	advances int
	fail     bool
}

func (a *fakeAnchor) Snapshot() (AnchorSnapshot, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.fail {
		return AnchorSnapshot{}, ErrUnavailable
	}
	return a.current, nil
}
func (a *fakeAnchor) CompareAndAdvance(v AnchorAdvance) (AnchorSnapshot, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.fail {
		return AnchorSnapshot{}, ErrUnavailable
	}
	if a.current.Sequence != v.OriginalExpected || v.Next != v.OriginalExpected+1 {
		return AnchorSnapshot{}, ErrConflict
	}
	a.advances++
	a.current = AnchorSnapshot{v.Next, v.BundleDigest, td(byte(v.Next + 3))}
	return a.current, nil
}

type fakeChildState struct {
	status         LaunchStatus
	release, abort string
}
type fakeChildren struct {
	mu                         sync.Mutex
	states                     map[string]*fakeChildState
	prepares, releases, aborts int
	releaseResponseLost        bool
	unknown                    bool
	abortFail                  bool
}

func newFakeChildren() *fakeChildren { return &fakeChildren{states: map[string]*fakeChildState{}} }
func (c *fakeChildren) Prepare(v LaunchRequest) (StoppedChild, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.prepares++
	id := td(byte(c.prepares + 6))
	c.states[id] = &fakeChildState{status: LaunchPending}
	return StoppedChild{id, td(byte(c.prepares + 7)), v.ReleaseIdentity}, nil
}
func (c *fakeChildren) Release(id, release, accept string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.states[id]
	if s == nil || s.status == LaunchAborted {
		return "", ErrConflict
	}
	if s.status == LaunchReleased {
		return s.release, nil
	}
	c.releases++
	s.status = LaunchReleased
	s.release = td(10)
	if c.releaseResponseLost {
		return "", ErrUnavailable
	}
	return s.release, nil
}
func (c *fakeChildren) AbortWait(id, reason string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.states[id]
	if c.abortFail {
		return "", ErrUnavailable
	}
	if s == nil {
		return "", ErrUnavailable
	}
	if s.status == LaunchReleased {
		return "", ErrConflict
	}
	c.aborts++
	s.status = LaunchAborted
	s.abort = td(11)
	return s.abort, nil
}
func (c *fakeChildren) Inspect(id string) (ChildObservation, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.unknown {
		return ChildObservation{Status: LaunchUnknown}, nil
	}
	s := c.states[id]
	if s == nil {
		return ChildObservation{Status: LaunchUnknown}, nil
	}
	receipt := s.release
	if s.status == LaunchAborted {
		receipt = s.abort
	}
	return ChildObservation{s.status, receipt}, nil
}

type fixture struct {
	dir      string
	root     AuthorityRootIdentity
	journal  *Journal
	anchor   *fakeAnchor
	child    *fakeChildren
	provider *Provider
	initial  Current
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	dir := authorityDir(t)
	root := authorityIdentity(t, dir)
	journal, err := OpenJournal(dir, root)
	if err != nil {
		t.Fatal(err)
	}
	anchor := &fakeAnchor{current: AnchorSnapshot{1, td(1), td(2)}}
	child := newFakeChildren()
	initial := Current{1, td(1)}
	provider, err := Open(journal, anchor, child, initial)
	if err != nil {
		t.Fatal(err)
	}
	return fixture{dir: dir, root: root, journal: journal, anchor: anchor, child: child, provider: provider, initial: initial}
}
func bundleRequest() BundlePrepare {
	return BundlePrepare{TransactionID: "bundle-1", UpdateKind: "evidence-update", PreviousBundleDigest: td(1), BundleDigest: td(3), OriginalExpected: 1, AnchoredNext: 2, AuthorizationDigest: td(4), StagedLeafSetDigest: td(5)}
}
func launchRequest() LaunchRequest {
	return LaunchRequest{TransactionID: "launch-1", AttemptID: "attempt-1", LaunchNonce: "nonce-1", RequestDigest: td(6), BundleDigest: td(1), ExpectedSequence: 1, ProfileReceiptDigest: td(7), ReleaseIdentity: td(8), Deadline: time.Now().UTC().Add(time.Hour)}
}

func TestJournalAppendCRCChainLockAndPartialTailRecovery(t *testing.T) {
	dir := authorityDir(t)
	root := authorityIdentity(t, dir)
	j, err := OpenJournal(dir, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenJournal(dir, root); !errors.Is(err, ErrAlreadyOpen) {
		t.Fatalf("second writer: %v", err)
	}
	if _, err := j.appendRecord("one", "tx-1", struct{ Digest string }{td(1)}); err != nil {
		t.Fatal(err)
	}
	if _, err := j.appendRecord("two", "tx-2", struct{ Digest string }{td(2)}); err != nil {
		t.Fatal(err)
	}
	if len(j.snapshotRecords()) != 2 || j.snapshotRecords()[1].PreviousRecordDigest != j.snapshotRecords()[0].RecordDigest {
		t.Fatal("journal chain missing")
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, journalName)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.Write([]byte{0, 0}); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	reopened, err := OpenJournal(dir, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(reopened.snapshotRecords()) != 2 {
		t.Fatal("partial tail became a fact")
	}
	_ = reopened.Close()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)-1] ^= 1
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = OpenJournal(dir, root); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("corrupt CRC/digest accepted: %v", err)
	}
}

func TestBundlePreparedCommitCASIdempotencyAndResponseReplay(t *testing.T) {
	f := newFixture(t)
	defer f.journal.Close()
	request := bundleRequest()
	prepared, err := f.provider.PrepareBundle(request)
	if err != nil || prepared.Status != BundlePreparedNoAnchor {
		t.Fatalf("prepare: %+v %v", prepared, err)
	}
	replay, err := f.provider.PrepareBundle(request)
	if err != nil || replay.PreparedReceiptDigest != prepared.PreparedReceiptDigest {
		t.Fatal("prepare replay changed")
	}
	changed := request
	changed.BundleDigest = td(9)
	if _, err = f.provider.PrepareBundle(changed); !errors.Is(err, ErrConflict) {
		t.Fatal("transaction identity conflict accepted")
	}
	committed, err := f.provider.CommitBundle(request.TransactionID, request.BundleDigest, request.OriginalExpected, prepared.PreparedReceiptDigest)
	if err != nil || committed.Status != BundleCommitted || f.anchor.advances != 1 {
		t.Fatalf("commit: %+v %v", committed, err)
	}
	replayed, err := f.provider.CommitBundle(request.TransactionID, request.BundleDigest, request.OriginalExpected, prepared.PreparedReceiptDigest)
	if err != nil || replayed.CommitReceiptDigest != committed.CommitReceiptDigest || f.anchor.advances != 1 {
		t.Fatal("lost response caused second advance")
	}
}

func TestBundleRecoveryPreparedAndAnchorAdvanced(t *testing.T) {
	t.Run("prepared-no-anchor", func(t *testing.T) {
		f := newFixture(t)
		defer f.journal.Close()
		r := bundleRequest()
		prepared, _ := f.provider.PrepareBundle(r)
		result, err := f.provider.RecoverBundle(r.TransactionID, r.BundleDigest, 1, 1, 2, prepared.PreparedReceiptDigest, "")
		if err != nil || result.Status != BundleCommitted || f.anchor.advances != 1 {
			t.Fatalf("recover: %+v %v", result, err)
		}
	})
	t.Run("anchor-advanced-not-committed", func(t *testing.T) {
		f := newFixture(t)
		r := bundleRequest()
		prepared, _ := f.provider.PrepareBundle(r)
		f.journal.fail = func(kind string) error {
			if kind == "bundle-committed" {
				return ErrUnavailable
			}
			return nil
		}
		result, err := f.provider.CommitBundle(r.TransactionID, r.BundleDigest, 1, prepared.PreparedReceiptDigest)
		if !errors.Is(err, ErrReconcileRequired) || result.Status != BundleAnchorAdvanced {
			t.Fatalf("ambiguous: %+v %v", result, err)
		}
		f.journal.fail = nil
		inspect, err := f.provider.InspectBundle(r.TransactionID, r.BundleDigest)
		if err != nil || inspect.Status != BundleAnchorAdvanced {
			t.Fatalf("inspect: %+v %v", inspect, err)
		}
		_ = f.journal.Close()
		journal, err := OpenJournal(f.dir, f.root)
		if err != nil {
			t.Fatal(err)
		}
		defer journal.Close()
		restarted, err := Open(journal, f.anchor, f.child, f.initial)
		if err != nil {
			t.Fatal(err)
		}
		recovered, err := restarted.RecoverBundle(r.TransactionID, r.BundleDigest, 1, 2, 2, prepared.PreparedReceiptDigest, inspect.AnchorReceiptDigest)
		if err != nil || recovered.Status != BundleCommitted || f.anchor.advances != 1 {
			t.Fatalf("recovery: %+v %v", recovered, err)
		}
	})
}

func TestHigherInvalidAnchorConsumesHighWaterAndNeverFallsBack(t *testing.T) {
	f := newFixture(t)
	_ = f.journal.Close()
	f.anchor.current = AnchorSnapshot{2, td(9), td(8)}
	journal, err := OpenJournal(f.dir, f.root)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := Open(journal, f.anchor, f.child, f.initial)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = provider.Current(); !errors.Is(err, ErrUnavailable) {
		t.Fatal("invalid higher anchor remained available")
	}
	_ = journal.Close()
	f.anchor.current = AnchorSnapshot{1, td(1), td(2)}
	journal, err = OpenJournal(f.dir, f.root)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	if _, err = Open(journal, f.anchor, f.child, f.initial); !errors.Is(err, ErrUnavailable) {
		t.Fatal("durable high-water allowed fallback")
	}
}

func TestSameSequenceAnchorForkIsDurablyFailClosed(t *testing.T) {
	f := newFixture(t)
	_ = f.journal.Close()
	f.anchor.current = AnchorSnapshot{1, td(9), td(8)}
	journal, err := OpenJournal(f.dir, f.root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Open(journal, f.anchor, f.child, f.initial); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("fork accepted: %v", err)
	}
	_ = journal.Close()
	f.anchor.current = AnchorSnapshot{1, td(1), td(2)}
	journal, err = OpenJournal(f.dir, f.root)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	if _, err = Open(journal, f.anchor, f.child, f.initial); !errors.Is(err, ErrUnavailable) {
		t.Fatal("restored projection erased durable fork")
	}
}

func TestLaunchBarrierIdempotencyResponseLostUnknownAndRestart(t *testing.T) {
	t.Run("release-response-lost", func(t *testing.T) {
		f := newFixture(t)
		defer f.journal.Close()
		request := launchRequest()
		pending, err := f.provider.PrepareLaunch(request)
		if err != nil || pending.Status != LaunchPending {
			t.Fatal(err)
		}
		again, _ := f.provider.PrepareLaunch(request)
		if again.ChildIdentityDigest != pending.ChildIdentityDigest || f.child.prepares != 1 {
			t.Fatal("second child spawned")
		}
		f.child.releaseResponseLost = true
		released, err := f.provider.CommitLaunch(request.TransactionID, pending.LaunchReceiptDigest, pending.ReleaseIdentity, td(9))
		if err != nil || released.Status != LaunchReleased {
			t.Fatalf("lost release: %+v %v", released, err)
		}
		replay, err := f.provider.CommitLaunch(request.TransactionID, pending.LaunchReceiptDigest, pending.ReleaseIdentity, td(9))
		if err != nil || replay.ReleaseReceiptDigest != released.ReleaseReceiptDigest || f.child.releases != 1 {
			t.Fatal("release replay changed")
		}
		if unknown, err := f.provider.InspectLaunch("missing", "nonce", td(1)); !errors.Is(err, ErrReconcileRequired) || unknown.Status != LaunchUnknown {
			t.Fatal("unknown was retry permission")
		}
	})
	t.Run("restart-pending-fail-closed", func(t *testing.T) {
		f := newFixture(t)
		request := launchRequest()
		pending, err := f.provider.PrepareLaunch(request)
		if err != nil {
			t.Fatal(err)
		}
		_ = f.journal.Close()
		journal, err := OpenJournal(f.dir, f.root)
		if err != nil {
			t.Fatal(err)
		}
		defer journal.Close()
		restarted, err := Open(journal, f.anchor, f.child, f.initial)
		if err != nil {
			t.Fatal(err)
		}
		result, err := restarted.InspectLaunch(request.AttemptID, request.LaunchNonce, request.RequestDigest)
		if err != nil || result.Status != LaunchAborted || f.child.aborts != 1 || f.child.releases != 0 || result.ChildIdentityDigest != pending.ChildIdentityDigest {
			t.Fatalf("restart: %+v %v", result, err)
		}
	})
}

func TestRevokeBeforeReleaseAbortsStoppedChild(t *testing.T) {
	f := newFixture(t)
	defer f.journal.Close()
	launch := launchRequest()
	pending, err := f.provider.PrepareLaunch(launch)
	if err != nil {
		t.Fatal(err)
	}
	bundle := bundleRequest()
	bundle.UpdateKind = "security-revocation"
	prepared, err := f.provider.PrepareBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.provider.CommitBundle(bundle.TransactionID, bundle.BundleDigest, 1, prepared.PreparedReceiptDigest); err != nil {
		t.Fatal(err)
	}
	result, err := f.provider.CommitLaunch(launch.TransactionID, pending.LaunchReceiptDigest, pending.ReleaseIdentity, td(9))
	if !errors.Is(err, ErrConflict) || result.Status != LaunchAborted || f.child.aborts != 1 || f.child.releases != 0 {
		t.Fatalf("revoke race: %+v %v", result, err)
	}
}

func TestRevokeAnchorLinearizationBlocksReleaseBeforeCommitProjection(t *testing.T) {
	f := newFixture(t)
	defer f.journal.Close()
	launch := launchRequest()
	pending, err := f.provider.PrepareLaunch(launch)
	if err != nil {
		t.Fatal(err)
	}
	bundle := bundleRequest()
	bundle.UpdateKind = "security-revocation"
	prepared, err := f.provider.PrepareBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	f.journal.fail = func(kind string) error {
		if kind == "bundle-committed" {
			return ErrUnavailable
		}
		return nil
	}
	if _, err = f.provider.CommitBundle(bundle.TransactionID, bundle.BundleDigest, 1, prepared.PreparedReceiptDigest); !errors.Is(err, ErrReconcileRequired) {
		t.Fatalf("expected ambiguous revoke commit: %v", err)
	}
	f.journal.fail = nil
	result, err := f.provider.CommitLaunch(launch.TransactionID, pending.LaunchReceiptDigest, pending.ReleaseIdentity, td(9))
	if !errors.Is(err, ErrConflict) || result.Status != LaunchAborted || f.child.releases != 0 {
		t.Fatalf("anchor-advanced revoke released child: %+v %v", result, err)
	}
}

func TestPrepareLaunchRejectsTransactionIdentityReuseBeforeSpawn(t *testing.T) {
	f := newFixture(t)
	defer f.journal.Close()
	request := launchRequest()
	if _, err := f.provider.PrepareLaunch(request); err != nil {
		t.Fatal(err)
	}
	changed := request
	changed.AttemptID = "attempt-2"
	changed.LaunchNonce = "nonce-2"
	changed.RequestDigest = td(12)
	if _, err := f.provider.PrepareLaunch(changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("transaction reuse: %v", err)
	}
	if f.child.prepares != 1 {
		t.Fatal("transaction reuse spawned another child")
	}
}

func TestRecoverRejectsRollbackAndForkedProvider(t *testing.T) {
	f := newFixture(t)
	defer f.journal.Close()
	r := bundleRequest()
	prepared, err := f.provider.PrepareBundle(r)
	if err != nil {
		t.Fatal(err)
	}
	f.provider.highWater = 2
	if _, err = f.provider.RecoverBundle(r.TransactionID, r.BundleDigest, 1, 1, 2, prepared.PreparedReceiptDigest, ""); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("rollback recovery: %v", err)
	}
	f.provider.highWater = 1
	f.provider.forked = true
	if _, err = f.provider.RecoverBundle(r.TransactionID, r.BundleDigest, 1, 1, 2, prepared.PreparedReceiptDigest, ""); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("fork recovery: %v", err)
	}
}

func TestForkAppendFailureCreatesStickyFailClosedMarker(t *testing.T) {
	f := newFixture(t)
	_ = f.journal.Close()
	journal, err := OpenJournal(f.dir, f.root)
	if err != nil {
		t.Fatal(err)
	}
	journal.fail = func(kind string) error {
		if kind == "anchor-fork" {
			return ErrUnavailable
		}
		return nil
	}
	f.anchor.current = AnchorSnapshot{1, td(9), td(8)}
	if _, err = Open(journal, f.anchor, f.child, f.initial); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("fork: %v", err)
	}
	journal.fail = nil
	_ = journal.Close()
	f.anchor.current = AnchorSnapshot{1, td(1), td(2)}
	journal, err = OpenJournal(f.dir, f.root)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	if !journal.failClosed {
		t.Fatal("fork append failure did not persist fail-closed marker")
	}
	if _, err = Open(journal, f.anchor, f.child, f.initial); !errors.Is(err, ErrUnavailable) {
		t.Fatal("restored anchor bypassed fail-closed marker")
	}
}

func TestAbortFailuresRemainAddressableAndNeverWriteEmptyAbortedFacts(t *testing.T) {
	t.Run("prepare", func(t *testing.T) {
		f := newFixture(t)
		request := launchRequest()
		f.journal.fail = func(kind string) error {
			if kind == "launch-prepared" {
				return ErrUnavailable
			}
			return nil
		}
		f.child.abortFail = true
		result, err := f.provider.PrepareLaunch(request)
		if !errors.Is(err, ErrReconcileRequired) || result.Status != LaunchUnknown {
			t.Fatalf("prepare: %+v %v", result, err)
		}
		found := false
		for _, record := range f.journal.snapshotRecords() {
			if record.Kind == "launch-orphaned" {
				found = true
			}
			if record.Kind == "launch-aborted" {
				t.Fatal("empty/false aborted fact written")
			}
		}
		if !found {
			t.Fatal("orphan child was not durably addressable")
		}
		f.journal.fail = nil
		f.child.abortFail = false
		_ = f.journal.Close()
		journal, err := OpenJournal(f.dir, f.root)
		if err != nil {
			t.Fatal(err)
		}
		defer journal.Close()
		restarted, err := Open(journal, f.anchor, f.child, f.initial)
		if err != nil {
			t.Fatal(err)
		}
		inspected, err := restarted.InspectLaunch(request.AttemptID, request.LaunchNonce, request.RequestDigest)
		if err != nil || inspected.Status != LaunchAborted {
			t.Fatalf("orphan recovery: %+v %v", inspected, err)
		}
	})
	t.Run("deadline", func(t *testing.T) {
		f := newFixture(t)
		defer f.journal.Close()
		request := launchRequest()
		pending, err := f.provider.PrepareLaunch(request)
		if err != nil {
			t.Fatal(err)
		}
		f.provider.launches[request.TransactionID].Request.Deadline = time.Now().Add(-time.Second)
		f.child.abortFail = true
		result, err := f.provider.CommitLaunch(request.TransactionID, pending.LaunchReceiptDigest, pending.ReleaseIdentity, td(9))
		if !errors.Is(err, ErrReconcileRequired) || result.Status != LaunchUnknown || f.provider.launches[request.TransactionID].Aborted != "" {
			t.Fatalf("deadline: %+v %v", result, err)
		}
		for _, record := range f.journal.snapshotRecords() {
			if record.Kind == "launch-aborted" {
				t.Fatal("abort failure became aborted fact")
			}
		}
	})
	t.Run("abort-ambiguity-journal", func(t *testing.T) {
		f := newFixture(t)
		defer f.journal.Close()
		request := launchRequest()
		pending, err := f.provider.PrepareLaunch(request)
		if err != nil {
			t.Fatal(err)
		}
		f.provider.launches[request.TransactionID].Request.Deadline = time.Now().Add(-time.Second)
		f.child.abortFail = true
		f.journal.fail = func(kind string) error {
			if kind == "launch-abort-ambiguous" {
				return ErrUnavailable
			}
			return nil
		}
		result, err := f.provider.CommitLaunch(request.TransactionID, pending.LaunchReceiptDigest, pending.ReleaseIdentity, td(9))
		if !errors.Is(err, ErrReconcileRequired) || result.Status != LaunchUnknown || !f.journal.failClosed {
			t.Fatalf("abort ambiguity journal: %+v %v", result, err)
		}
	})
	t.Run("revoke-journal", func(t *testing.T) {
		f := newFixture(t)
		defer f.journal.Close()
		launch := launchRequest()
		pending, err := f.provider.PrepareLaunch(launch)
		if err != nil {
			t.Fatal(err)
		}
		bundle := bundleRequest()
		bundle.UpdateKind = "security-revocation"
		prepared, _ := f.provider.PrepareBundle(bundle)
		if _, err = f.provider.CommitBundle(bundle.TransactionID, bundle.BundleDigest, 1, prepared.PreparedReceiptDigest); err != nil {
			t.Fatal(err)
		}
		f.journal.fail = func(kind string) error {
			if kind == "launch-aborted" {
				return ErrUnavailable
			}
			return nil
		}
		result, err := f.provider.CommitLaunch(launch.TransactionID, pending.LaunchReceiptDigest, pending.ReleaseIdentity, td(9))
		if !errors.Is(err, ErrReconcileRequired) || result.Status != LaunchUnknown || !f.journal.failClosed {
			t.Fatalf("revoke abort journal: %+v %v", result, err)
		}
	})
}

func TestOpenJournalRejectsSymlinkParentAndWritableAuthorityRoot(t *testing.T) {
	base := authorityDir(t)
	realParent := filepath.Join(base, "real-parent")
	if err := os.Mkdir(realParent, 0700); err != nil {
		t.Fatal(err)
	}
	realRoot := filepath.Join(realParent, "root")
	if err := os.Mkdir(realRoot, 0700); err != nil {
		t.Fatal(err)
	}
	root := authorityIdentity(t, realRoot)
	link := filepath.Join(base, "link")
	if err := os.Symlink(realParent, link); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenJournal(filepath.Join(link, "root"), root); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("symlink authority root: %v", err)
	}
	if err := os.Chmod(realRoot, 0777); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenJournal(realRoot, root); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("writable authority root: %v", err)
	}
	if err := os.Chmod(realRoot, 0700); err != nil {
		t.Fatal(err)
	}
	movedRoot := filepath.Join(realParent, "moved-root")
	if err := os.Rename(realRoot, movedRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(realRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenJournal(realRoot, root); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("replaced authority root: %v", err)
	}
}
