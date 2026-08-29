package dispatch

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/provider"
)

func newReservedClaimFixture(t *testing.T, root string) (*Matcher, *LeaseLedger, provider.ProviderRegistration, ReservedClaimRequest) {
	t.Helper()
	store, err := provider.NewRegistrationStore(filepath.Join(root, "registrations"))
	if err != nil {
		t.Fatalf("NewRegistrationStore: %v", err)
	}
	registration := testRegistration("reserved")
	if _, err := store.Put(registration); err != nil {
		t.Fatalf("Put registration: %v", err)
	}
	evidence := testEvidence(registration, nil)
	snapshot := testSnapshot(registration, nil, []string{evidence.EvidenceDigest}, nil)
	ledger, err := NewLeaseLedger(filepath.Join(root, "leases"))
	if err != nil {
		t.Fatalf("NewLeaseLedger: %v", err)
	}
	request := ReservedClaimRequest{
		ReservationFactDigest: fixedDigest("attempt-reserved-fact"),
		RunId:                 "run-reserved",
		ReservedAttemptId:     "attempt-reserved",
		Claim:                 testClaimRequest(registration, snapshot, []provider.ConformanceEvidence{evidence}, hardenedRequirements(), "reserved"),
	}
	request.Claim.RunId = request.RunId
	request.Claim.AttemptId = request.ReservedAttemptId
	matcher := NewMatcherWithReservedClaimLedger(store, newClaimEdgeRuntime(t), ledger)
	return matcher, ledger, registration, request
}

func reopenReservedClaimFixture(t *testing.T, root string) (*Matcher, *LeaseLedger) {
	t.Helper()
	store, err := provider.NewRegistrationStore(filepath.Join(root, "registrations"))
	if err != nil {
		t.Fatalf("reopen registration store: %v", err)
	}
	ledger, err := NewLeaseLedger(filepath.Join(root, "leases"))
	if err != nil {
		t.Fatalf("reopen lease ledger: %v", err)
	}
	return NewMatcherWithReservedClaimLedger(store, newClaimEdgeRuntime(t), ledger), ledger
}

func requireNoLeaseLedgerWrite(t *testing.T, ledger *LeaseLedger) {
	t.Helper()
	raw, err := os.ReadFile(ledger.ledgerPath())
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 0 {
		t.Fatalf("reserved claim failure wrote %d durable lease-ledger bytes", len(raw))
	}
}

func TestClaimReservedResponseLossReplayReturnsExactDurableIdentity(t *testing.T) {
	root := t.TempDir()
	matcher, ledger, _, request := newReservedClaimFixture(t, root)
	first, err := matcher.ClaimReserved(request, dispatchTestNow)
	if err != nil {
		t.Fatalf("first ClaimReserved: %v", err)
	}
	ledgerPath := ledger.ledgerPath()
	before, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := matcher.ClaimReserved(request, dispatchTestNow.Add(17*time.Minute))
	if err != nil {
		t.Fatalf("response-loss replay: %v", err)
	}
	if !reflect.DeepEqual(replayed, first) {
		t.Fatalf("replay changed durable identity\nfirst=%#v\nreplayed=%#v", first, replayed)
	}
	after, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("same-input response-loss replay appended or rewrote the ledger")
	}
	if replayed.Lease.CreatedAt != dispatchTestNow.Format(time.RFC3339) {
		t.Fatalf("replay rewrote CreatedAt: %q", replayed.Lease.CreatedAt)
	}
	if err := matcher.Revalidate(replayed.Lease, request.Claim.Snapshot, request.Claim.Evidences, request.Claim.Requirements, dispatchTestNow.Add(17*time.Minute)); err != nil {
		t.Fatalf("current Revalidate after durable replay: %v", err)
	}
}

func TestClaimReservedRestartRecoveryReturnsExactDurableIdentity(t *testing.T) {
	root := t.TempDir()
	matcher, ledger, _, request := newReservedClaimFixture(t, root)
	first, err := matcher.ClaimReserved(request, dispatchTestNow)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(ledger.ledgerPath())
	if err != nil {
		t.Fatal(err)
	}
	restarted, recoveredLedger := reopenReservedClaimFixture(t, root)
	replayed, err := restarted.ClaimReserved(request, dispatchTestNow.Add(23*time.Minute))
	if err != nil {
		t.Fatalf("restart replay: %v", err)
	}
	if !reflect.DeepEqual(replayed, first) {
		t.Fatal("restart recovery did not return the original lease and result capability")
	}
	after, err := os.ReadFile(recoveredLedger.ledgerPath())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("restart replay appended a sibling fact")
	}
	if edge, ok := restarted.IssuedResultCapability(first.Lease.LeaseId); !ok || edge != first.ResultCapability {
		t.Fatal("restart replay did not restore the exact typed-edge projection")
	}
}

func TestClaimReservedCanonicalInputConflictsFailClosedBeforeMint(t *testing.T) {
	root := t.TempDir()
	matcher, ledger, registration, request := newReservedClaimFixture(t, root)
	if _, err := matcher.ClaimReserved(request, dispatchTestNow); err != nil {
		t.Fatal(err)
	}
	base := reservedClaimInput{
		ReservationFactDigest: request.ReservationFactDigest,
		RunId:                 request.RunId, ReservedAttemptId: request.ReservedAttemptId,
		Registration: registration, Claim: request.Claim,
	}
	cases := []struct {
		name   string
		mutate func(*reservedClaimInput)
	}{
		{name: "registration", mutate: func(input *reservedClaimInput) {
			input.Registration.CreatedAt = "2026-08-12T00:00:09Z"
			setTestRegistrationDigest(&input.Registration)
		}},
		{name: "capability snapshot", mutate: func(input *reservedClaimInput) {
			input.Claim.Snapshot.Capabilities["structuredOutput"] = "json-v2"
			setTestSnapshotDigest(&input.Claim.Snapshot)
		}},
		{name: "evidence", mutate: func(input *reservedClaimInput) {
			input.Claim.Evidences[0].SignedAt = "2026-08-12T00:00:03Z"
			setTestEvidenceDigest(&input.Claim.Evidences[0])
			input.Claim.Snapshot.ConformanceEvidenceDigests = []string{input.Claim.Evidences[0].EvidenceDigest}
			setTestSnapshotDigest(&input.Claim.Snapshot)
		}},
		{name: "requirements", mutate: func(input *reservedClaimInput) {
			input.Claim.Requirements = workspaceWriteRequirements()
		}},
		{name: "frozen input bytes", mutate: func(input *reservedClaimInput) {
			input.Claim.TaskId = "task-conflicting"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var input reservedClaimInput
			raw, _ := json.Marshal(base)
			if err := json.Unmarshal(raw, &input); err != nil {
				t.Fatal(err)
			}
			tc.mutate(&input)
			_, found, err := ledger.lookupReservedClaim(input)
			if !errors.Is(err, ErrLeaseConflict) {
				t.Fatalf("conflict err=%v", err)
			}
			if found {
				t.Fatal("conflicting replay returned an exact durable claim")
			}
		})
	}
}

func TestClaimReservedRejectsSiblingAndLegacyWithoutFallback(t *testing.T) {
	t.Run("sibling reservation", func(t *testing.T) {
		root := t.TempDir()
		matcher, ledger, registration, request := newReservedClaimFixture(t, root)
		if _, err := matcher.ClaimReserved(request, dispatchTestNow); err != nil {
			t.Fatal(err)
		}
		input := reservedClaimInput{
			ReservationFactDigest: fixedDigest("sibling-reservation"),
			RunId:                 request.RunId, ReservedAttemptId: request.ReservedAttemptId,
			Registration: registration, Claim: request.Claim,
		}
		_, found, err := ledger.lookupReservedClaim(input)
		if !errors.Is(err, ErrLeaseConflict) || found {
			t.Fatalf("sibling err=%v found=%v", err, found)
		}
	})

	t.Run("legacy binding", func(t *testing.T) {
		root := t.TempDir()
		matcher, ledger, _, request := newReservedClaimFixture(t, root)
		legacy := ledgerLease("legacy-reserved-collision")
		legacy.RunId, legacy.AttemptId = request.RunId, request.ReservedAttemptId
		if err := sealLease(&legacy); err != nil {
			t.Fatal(err)
		}
		if err := ledger.AppendClaim(legacy); err != nil {
			t.Fatal(err)
		}
		before, _ := os.ReadFile(ledger.ledgerPath())
		if _, err := matcher.ClaimReserved(request, dispatchTestNow); !errors.Is(err, ErrLeaseConflict) {
			t.Fatalf("fresh reserved API adopted legacy claim: %v", err)
		}
		after, _ := os.ReadFile(ledger.ledgerPath())
		if !bytes.Equal(before, after) {
			t.Fatal("legacy collision changed durable ledger")
		}
		recovered, err := NewLeaseLedger(filepath.Join(root, "leases"))
		if err != nil {
			t.Fatalf("legacy history no longer replays: %v", err)
		}
		if _, _, _, err := recovered.Current(legacy.LeaseId); err != nil {
			t.Fatalf("legacy lease missing after replay: %v", err)
		}
	})

	t.Run("legacy resurrection after reserved terminal", func(t *testing.T) {
		root := t.TempDir()
		matcher, ledger, _, request := newReservedClaimFixture(t, root)
		claimed, err := matcher.ClaimReserved(request, dispatchTestNow)
		if err != nil {
			t.Fatal(err)
		}
		if err := ledger.AppendCancel(claimed.Lease.LeaseId, CancelReasonDeadlineExceeded, claimed.Lease.Generation); err != nil {
			t.Fatal(err)
		}
		sibling := rivalLease(claimed.Lease, "legacy-after-reserved-terminal")
		if err := ledger.AppendClaim(sibling); !errors.Is(err, ErrLeaseConflict) {
			t.Fatalf("legacy API resurrected reserved Run/Attempt binding: %v", err)
		}
	})
}

func TestClaimReservedConcurrentReplayAppendsOnce(t *testing.T) {
	root := t.TempDir()
	matcher, ledger, _, request := newReservedClaimFixture(t, root)
	reopenedStore, err := provider.NewRegistrationStore(filepath.Join(root, "registrations"))
	if err != nil {
		t.Fatal(err)
	}
	secondMatcher := NewMatcherWithReservedClaimLedger(reopenedStore, newClaimEdgeRuntime(t), ledger)
	const callers = 32
	results := make(chan ReservedClaimResult, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(offset int) {
			defer wg.Done()
			current := matcher
			if offset%2 == 1 {
				current = secondMatcher
			}
			result, err := current.ClaimReserved(request, dispatchTestNow.Add(time.Duration(offset)*time.Second))
			results <- result
			errs <- err
		}(i)
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent replay: %v", err)
		}
	}
	var first *ReservedClaimResult
	for result := range results {
		if first == nil {
			copy := result
			first = &copy
			continue
		}
		if !reflect.DeepEqual(*first, result) {
			t.Fatal("concurrent callers observed different durable identities")
		}
	}
	raw, err := os.ReadFile(ledger.ledgerPath())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(raw, []byte{'\n'}) != 1 {
		t.Fatalf("concurrent replay appended %d facts, want 1", bytes.Count(raw, []byte{'\n'}))
	}
}

func TestClaimReservedConcurrentExactWinnerReturnsOriginalBytes(t *testing.T) {
	root := t.TempDir()
	firstMatcher, ledger, _, request := newReservedClaimFixture(t, root)
	secondMatcher := NewMatcherWithReservedClaimLedger(firstMatcher.store, firstMatcher.edgeRuntime, ledger)

	arrived := make(chan struct{}, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseClaims := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseClaims()
	hook := func() {
		arrived <- struct{}{}
		<-release
	}
	firstMatcher.beforeReservedEdgePreview = hook
	secondMatcher.beforeReservedEdgePreview = hook

	results := make(chan ReservedClaimResult, 2)
	errs := make(chan error, 2)
	for index, matcher := range []*Matcher{firstMatcher, secondMatcher} {
		go func(offset int, current *Matcher) {
			result, err := current.ClaimReserved(request, dispatchTestNow.Add(time.Duration(offset)*time.Minute))
			results <- result
			errs <- err
		}(index, matcher)
	}
	for index := 0; index < 2; index++ {
		select {
		case <-arrived:
		case <-time.After(2 * time.Second):
			releaseClaims()
			t.Fatal("concurrent exact claimant did not reach lock-external edge preview")
		}
	}
	releaseClaims()

	var observed []ReservedClaimResult
	for index := 0; index < 2; index++ {
		select {
		case err := <-errs:
			if err != nil {
				t.Fatalf("concurrent exact claim: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("concurrent exact claim did not finish")
		}
		select {
		case result := <-results:
			observed = append(observed, result)
		case <-time.After(2 * time.Second):
			t.Fatal("concurrent exact claim did not return its result")
		}
	}
	if !reflect.DeepEqual(observed[0], observed[1]) {
		t.Fatal("concurrent loser did not return the winner's original durable bytes")
	}
	raw, err := os.ReadFile(ledger.ledgerPath())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(raw, []byte{'\n'}) != 1 {
		t.Fatalf("concurrent exact winner appended %d facts, want 1", bytes.Count(raw, []byte{'\n'}))
	}
}

type blockingLedgerLeaseResolver struct {
	ledger  *LeaseLedger
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (resolver *blockingLedgerLeaseResolver) LeaseActive(leaseId string, generation int64, fencingToken string) (bool, error) {
	resolver.once.Do(func() { close(resolver.entered) })
	<-resolver.release
	lease, state, currentGeneration, err := resolver.ledger.Current(leaseId)
	if err != nil {
		return false, err
	}
	return state == LeaseStateClaimed && currentGeneration == generation && lease.FencingToken == fencingToken, nil
}

func TestClaimReservedAndResultRecheckShareLedgerWithoutLockInversion(t *testing.T) {
	root := t.TempDir()
	matcher, ledger, _, request := newReservedClaimFixture(t, root)
	first, err := matcher.ClaimReserved(request, dispatchTestNow)
	if err != nil {
		t.Fatal(err)
	}

	resolver := &blockingLedgerLeaseResolver{
		ledger: ledger, entered: make(chan struct{}), release: make(chan struct{}),
	}
	var releaseOnce sync.Once
	releaseResolver := func() { releaseOnce.Do(func() { close(resolver.release) }) }
	defer releaseResolver()
	matcher.edgeRuntime.BindLeaseResolver(resolver)
	recheckDone := make(chan error, 1)
	go func() {
		recheckDone <- matcher.edgeRuntime.RecheckDispatchResult(
			first.ResultCapability,
			dispatchResultUseRequestFor(first.ResultCapability, first.Lease, "reserved-lock-order-recheck"),
			dispatchTestNow,
		)
	}()
	select {
	case <-resolver.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("result recheck did not enter the lease resolver while holding EdgeRuntime")
	}

	second := request
	second.ReservationFactDigest = fixedDigest("attempt-reserved-lock-order-second")
	second.RunId = "run-reserved-lock-order-second"
	second.ReservedAttemptId = "attempt-reserved-lock-order-second"
	second.Claim.RunId = second.RunId
	second.Claim.AttemptId = second.ReservedAttemptId
	second.Claim.TaskId = "task-reserved-lock-order-second"
	second.Claim.AllocationId = "allocation-reserved-lock-order-second"
	atPreview := make(chan struct{})
	var previewOnce sync.Once
	matcher.beforeReservedEdgePreview = func() { previewOnce.Do(func() { close(atPreview) }) }
	claimDone := make(chan error, 1)
	go func() {
		_, claimErr := matcher.ClaimReserved(second, dispatchTestNow.Add(time.Minute))
		claimDone <- claimErr
	}()
	select {
	case <-atPreview:
	case <-time.After(2 * time.Second):
		releaseResolver()
		t.Fatal("fresh reserved claim did not reach lock-external edge preview")
	}

	// EdgeRuntime remains locked by RecheckDispatchResult. The fresh claim is
	// now blocked immediately before Preview, but must not own LeaseLedger.mu.
	ledgerReadDone := make(chan error, 1)
	go func() {
		_, _, _, currentErr := ledger.Current(first.Lease.LeaseId)
		ledgerReadDone <- currentErr
	}()
	select {
	case currentErr := <-ledgerReadDone:
		if currentErr != nil {
			t.Fatalf("ledger read during edge preview interleave: %v", currentErr)
		}
	case <-time.After(2 * time.Second):
		releaseResolver()
		t.Fatal("fresh reserved claim held LeaseLedger.mu while waiting for EdgeRuntime")
	}
	releaseResolver()

	select {
	case recheckErr := <-recheckDone:
		if recheckErr != nil {
			t.Fatalf("result recheck: %v", recheckErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("result recheck did not finish")
	}
	select {
	case claimErr := <-claimDone:
		if claimErr != nil {
			t.Fatalf("fresh reserved claim: %v", claimErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fresh reserved claim did not finish")
	}
}

func TestClaimReservedRejectsReplayAfterCurrentGenerationChanges(t *testing.T) {
	root := t.TempDir()
	matcher, ledger, _, request := newReservedClaimFixture(t, root)
	first, err := matcher.ClaimReserved(request, dispatchTestNow)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ledger.BumpGeneration(first.Lease.LeaseId, first.Lease.Generation); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(ledger.ledgerPath())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := matcher.ClaimReserved(request, dispatchTestNow.Add(time.Minute)); !errors.Is(err, ErrLeaseConflict) {
		t.Fatalf("stale generation/fencing replay err=%v", err)
	}
	after, err := os.ReadFile(ledger.ledgerPath())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("stale replay appended a new durable fact")
	}
}

func TestClaimReservedRequiresFreshDurablePath(t *testing.T) {
	store, _, registration, snapshot, evidences := eligibleFixture(t)
	request := ReservedClaimRequest{
		ReservationFactDigest: fixedDigest("unbound-reservation"),
		RunId:                 "run-unbound", ReservedAttemptId: "attempt-unbound",
		Claim: testClaimRequest(registration, snapshot, evidences, hardenedRequirements(), "unbound"),
	}
	request.Claim.RunId, request.Claim.AttemptId = request.RunId, request.ReservedAttemptId
	matcher := NewMatcherWithEdgeRuntime(store, newClaimEdgeRuntime(t))
	if _, err := matcher.ClaimReserved(request, dispatchTestNow); err == nil {
		t.Fatal("fresh reserved claim silently fell back without a durable lease ledger")
	}
}

func TestClaimReservedPreflightRejectsIssuerAndKnownBindingBeforeDurableWrite(t *testing.T) {
	t.Run("issuer mismatch", func(t *testing.T) {
		root := t.TempDir()
		matcher, ledger, _, request := newReservedClaimFixture(t, root)
		foreignNamespace := testAuthorityNamespace()
		foreignNamespace.AuthorityScopeId = "repository:foreign"
		foreignRuntime, err := authority.NewEdgeRuntime(foreignNamespace)
		if err != nil {
			t.Fatal(err)
		}
		matcher.edgeRuntime = foreignRuntime
		if _, err := matcher.ClaimReserved(request, dispatchTestNow); !errors.Is(err, ErrLeaseConflict) {
			t.Fatalf("issuer mismatch err=%v", err)
		}
		requireNoLeaseLedgerWrite(t, ledger)
	})

	t.Run("hostile preseed old binding", func(t *testing.T) {
		root := t.TempDir()
		matcher, ledger, registration, request := newReservedClaimFixture(t, root)
		if _, err := matcher.edgeRuntime.IssueDispatchResultCapability(authority.DispatchResultIssuance{
			SourceActor: registration.SecurityDomainId, TargetActor: request.Claim.TargetActor,
			Operation:      authority.DispatchResultOperationAccept,
			BoundAttemptId: request.ReservedAttemptId, BoundAllocationId: request.Claim.AllocationId,
			Expiry: request.Claim.ExpiresAt,
			LeaseBinding: authority.EdgeLeaseBinding{
				LeaseId: "lease-hostile-preseed", AttemptId: request.ReservedAttemptId,
				AllocationId: request.Claim.AllocationId, Generation: 9,
				FencingToken: fixedDigest("hostile-preseed-fencing"),
			},
		}, dispatchTestNow); err != nil {
			t.Fatal(err)
		}
		if _, err := matcher.ClaimReserved(request, dispatchTestNow); !errors.Is(err, authority.ErrEdgeBindingMismatch) {
			t.Fatalf("known edge binding collision err=%v", err)
		}
		requireNoLeaseLedgerWrite(t, ledger)
	})
}

func TestReservedClaimRecoveryRejectsGenerationAndFencingForgery(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*reservedClaimFact)
	}{
		{name: "generation", mutate: func(fact *reservedClaimFact) {
			fact.Lease.Generation = 2
			if err := sealLease(&fact.Lease); err != nil {
				panic(err)
			}
		}},
		{name: "fencing", mutate: func(fact *reservedClaimFact) {
			fact.Lease.FencingToken = fixedDigest("forged-reserved-fencing")
			fact.Lease.LeaseDigest = ""
			digest, err := fact.Lease.Digest()
			if err != nil {
				panic(err)
			}
			fact.Lease.LeaseDigest = digest
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			matcher, ledger, _, request := newReservedClaimFixture(t, root)
			if _, err := matcher.ClaimReserved(request, dispatchTestNow); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(ledger.ledgerPath())
			if err != nil {
				t.Fatal(err)
			}
			var fact reservedClaimFact
			if err := json.Unmarshal(bytes.TrimSpace(raw), &fact); err != nil {
				t.Fatal(err)
			}
			tc.mutate(&fact)
			fact.Digest = ""
			digest, err := leaseFactContentDigest(&fact)
			if err != nil {
				t.Fatal(err)
			}
			fact.Digest = digest
			encoded, err := json.Marshal(fact)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err = canonical.JSON(encoded)
			if err != nil {
				t.Fatal(err)
			}
			encoded = append(encoded, '\n')
			if err := os.WriteFile(ledger.ledgerPath(), encoded, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := NewLeaseLedger(filepath.Join(root, "leases")); err == nil {
				t.Fatal("forged generation/fencing recovered")
			}
		})
	}
}
