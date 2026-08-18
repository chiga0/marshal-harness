package provider

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

type BundleStatus string

const (
	BundlePreparedNoAnchor BundleStatus = "prepared-no-anchor"
	BundleAnchorAdvanced   BundleStatus = "anchor-advanced-not-committed"
	BundleCommitted        BundleStatus = "committed"
	BundleUnknown          BundleStatus = "unknown"
)

type LaunchStatus string

const (
	LaunchPending  LaunchStatus = "pending"
	LaunchReleased LaunchStatus = "released"
	LaunchAborted  LaunchStatus = "aborted"
	LaunchExited   LaunchStatus = "exited"
	LaunchUnknown  LaunchStatus = "unknown"
)

type Current struct {
	Sequence     uint64
	BundleDigest string
}
type AnchorSnapshot struct {
	Sequence                    uint64
	BundleDigest, ReceiptDigest string
}
type AnchorAdvance struct {
	TransactionID, BundleDigest string
	OriginalExpected, Next      uint64
	PreparedReceiptDigest       string
}
type Anchor interface {
	Snapshot() (AnchorSnapshot, error)
	CompareAndAdvance(AnchorAdvance) (AnchorSnapshot, error)
}

type BundlePrepare struct {
	TransactionID, UpdateKind, PreviousBundleDigest, BundleDigest string
	OriginalExpected, AnchoredNext                                uint64
	AuthorizationDigest, StagedLeafSetDigest                      string
}
type BundleResult struct {
	Status                                                          BundleStatus
	PreparedReceiptDigest, AnchorReceiptDigest, CommitReceiptDigest string
	OriginalExpected, AnchoredNext, ObservedCurrent                 uint64
}

type LaunchRequest struct {
	TransactionID, AttemptID, LaunchNonce, RequestDigest string
	BundleDigest                                         string
	ExpectedSequence                                     uint64
	ProfileReceiptDigest, ReleaseIdentity                string
	Deadline                                             time.Time
}
type StoppedChild struct{ IdentityDigest, LaunchReceiptDigest, ReleaseIdentity string }
type ChildObservation struct {
	Status        LaunchStatus
	ReceiptDigest string
}
type ChildBarrier interface {
	Prepare(LaunchRequest) (StoppedChild, error)
	Release(childIdentity, releaseIdentity, durableAcceptDigest string) (string, error)
	AbortWait(childIdentity, reason string) (string, error)
	Inspect(childIdentity string) (ChildObservation, error)
}
type LaunchResult struct {
	Status                                                                                                             LaunchStatus
	TransactionID, ChildIdentityDigest, LaunchReceiptDigest, ReleaseIdentity, ReleaseReceiptDigest, AbortReceiptDigest string
}

type bundleState struct {
	Request                  BundlePrepare
	Prepared, Anchor, Commit string
}
type launchState struct {
	Request           LaunchRequest
	Child             StoppedChild
	Accepted          string
	Released, Aborted string
}

type Provider struct {
	mu                  sync.Mutex
	journal             *Journal
	anchor              Anchor
	children            ChildBarrier
	current             Current
	highWater           uint64
	unavailable         bool
	bundles             map[string]*bundleState
	launches            map[string]*launchState
	launchKeys          map[string]string
	unknownAnchorBundle string
	forked              bool
}

func Open(journal *Journal, anchor Anchor, children ChildBarrier, initial Current) (*Provider, error) {
	if journal == nil || anchor == nil || children == nil || initial.Sequence == 0 || !validDigest(initial.BundleDigest) {
		return nil, ErrInvalid
	}
	p := &Provider{journal: journal, anchor: anchor, children: children, current: initial, highWater: initial.Sequence, bundles: map[string]*bundleState{}, launches: map[string]*launchState{}, launchKeys: map[string]string{}}
	if err := p.rebuild(); err != nil {
		return nil, err
	}
	reconcileErr := p.reconcileAnchorOnOpen()
	abortErr := p.abortRestartPending()
	if abortErr != nil {
		return nil, abortErr
	}
	if reconcileErr != nil {
		return nil, reconcileErr
	}
	return p, nil
}

func (p *Provider) Current() (Current, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.unavailable {
		return Current{}, ErrUnavailable
	}
	return p.current, nil
}

func (p *Provider) PrepareBundle(request BundlePrepare) (BundleResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.unavailable {
		return BundleResult{}, ErrUnavailable
	}
	if !validBundlePrepare(request) {
		return BundleResult{}, ErrInvalid
	}
	if prior := p.bundles[request.TransactionID]; prior != nil {
		if digestObject(prior.Request) != digestObject(request) {
			return BundleResult{}, ErrConflict
		}
		return p.bundleResult(prior), nil
	}
	if request.OriginalExpected != p.current.Sequence || request.AnchoredNext != request.OriginalExpected+1 || request.PreviousBundleDigest != p.current.BundleDigest {
		return BundleResult{}, ErrConflict
	}
	payload := struct {
		Request               BundlePrepare `json:"request"`
		PreparedReceiptDigest string        `json:"preparedReceiptDigest"`
	}{Request: request}
	payload.PreparedReceiptDigest = digestObject(struct {
		Kind    string        `json:"kind"`
		Request BundlePrepare `json:"request"`
	}{"prepared", request})
	if _, err := p.journal.appendRecord("bundle-prepared", request.TransactionID, payload); err != nil {
		return BundleResult{}, err
	}
	state := &bundleState{Request: request, Prepared: payload.PreparedReceiptDigest}
	p.bundles[request.TransactionID] = state
	return p.bundleResult(state), nil
}

func (p *Provider) CommitBundle(transactionID, bundleDigest string, expected uint64, preparedReceipt string) (BundleResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	state := p.bundles[transactionID]
	if state == nil {
		return BundleResult{Status: BundleUnknown}, ErrReconcileRequired
	}
	if state.Request.BundleDigest != bundleDigest || state.Request.OriginalExpected != expected || state.Prepared != preparedReceipt {
		return BundleResult{}, ErrConflict
	}
	if state.Commit != "" {
		return p.bundleResult(state), nil
	}
	snapshot, err := p.anchor.Snapshot()
	if err != nil {
		return BundleResult{}, ErrUnavailable
	}
	if snapshot.Sequence == state.Request.OriginalExpected && snapshot.BundleDigest == state.Request.PreviousBundleDigest {
		snapshot, err = p.anchor.CompareAndAdvance(AnchorAdvance{TransactionID: transactionID, BundleDigest: bundleDigest, OriginalExpected: expected, Next: state.Request.AnchoredNext, PreparedReceiptDigest: state.Prepared})
		if err != nil {
			return BundleResult{}, ErrUnavailable
		}
	}
	if snapshot.Sequence != state.Request.AnchoredNext || snapshot.BundleDigest != bundleDigest || !validDigest(snapshot.ReceiptDigest) {
		p.consumeHighWater(snapshot.Sequence)
		p.unavailable = true
		return BundleResult{Status: BundleUnknown, ObservedCurrent: snapshot.Sequence}, ErrReconcileRequired
	}
	p.consumeHighWater(snapshot.Sequence)
	p.unavailable = true
	if state.Anchor == "" {
		payload := struct {
			ReceiptDigest string `json:"receiptDigest"`
			Sequence      uint64 `json:"sequence"`
		}{snapshot.ReceiptDigest, snapshot.Sequence}
		if _, err := p.journal.appendRecord("bundle-anchor-advanced", transactionID, payload); err != nil {
			return BundleResult{Status: BundleAnchorAdvanced, AnchorReceiptDigest: snapshot.ReceiptDigest}, ErrReconcileRequired
		}
		state.Anchor = snapshot.ReceiptDigest
	}
	commit := digestObject(struct {
		Kind, TransactionID, BundleDigest, Anchor string
		Sequence                                  uint64
	}{"committed", transactionID, bundleDigest, state.Anchor, snapshot.Sequence})
	payload := struct {
		CommitReceiptDigest, BundleDigest string
		Sequence                          uint64
	}{commit, bundleDigest, snapshot.Sequence}
	if _, err := p.journal.appendRecord("bundle-committed", transactionID, payload); err != nil {
		return BundleResult{Status: BundleAnchorAdvanced, AnchorReceiptDigest: state.Anchor}, ErrReconcileRequired
	}
	state.Commit = commit
	p.current = Current{Sequence: snapshot.Sequence, BundleDigest: bundleDigest}
	p.unavailable = false
	return p.bundleResult(state), nil
}

func (p *Provider) InspectBundle(transactionID, bundleDigest string) (BundleResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	state := p.bundles[transactionID]
	if state == nil || state.Request.BundleDigest != bundleDigest {
		return BundleResult{Status: BundleUnknown}, ErrReconcileRequired
	}
	if state.Commit != "" {
		return p.bundleResult(state), nil
	}
	snapshot, err := p.anchor.Snapshot()
	if err != nil {
		return BundleResult{}, ErrUnavailable
	}
	if snapshot.Sequence == state.Request.OriginalExpected && snapshot.BundleDigest == state.Request.PreviousBundleDigest {
		return p.bundleResult(state), nil
	}
	if snapshot.Sequence == state.Request.AnchoredNext && snapshot.BundleDigest == state.Request.BundleDigest && validDigest(snapshot.ReceiptDigest) {
		p.consumeHighWater(snapshot.Sequence)
		p.unavailable = true
		result := p.bundleResult(state)
		result.Status = BundleAnchorAdvanced
		result.AnchorReceiptDigest = snapshot.ReceiptDigest
		result.ObservedCurrent = snapshot.Sequence
		return result, nil
	}
	p.consumeHighWater(snapshot.Sequence)
	p.unavailable = true
	return BundleResult{Status: BundleUnknown, ObservedCurrent: snapshot.Sequence}, ErrReconcileRequired
}

func (p *Provider) RecoverBundle(transactionID, bundleDigest string, originalExpected, observedCurrent, next uint64, prepared, anchorReceipt string) (BundleResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	state := p.bundles[transactionID]
	if state == nil || state.Request.BundleDigest != bundleDigest || state.Request.OriginalExpected != originalExpected || state.Request.AnchoredNext != next || state.Prepared != prepared {
		return BundleResult{Status: BundleUnknown}, ErrReconcileRequired
	}
	snapshot, err := p.anchor.Snapshot()
	if err != nil {
		return BundleResult{}, ErrUnavailable
	}
	if snapshot.Sequence != observedCurrent {
		return BundleResult{}, ErrConflict
	}
	if anchorReceipt != "" && state.Anchor != "" && anchorReceipt != state.Anchor {
		return BundleResult{}, ErrConflict
	}
	if state.Commit != "" {
		return p.bundleResult(state), nil
	}
	if snapshot.Sequence == originalExpected {
		if anchorReceipt != "" {
			return BundleResult{}, ErrConflict
		}
		snapshot, err = p.anchor.CompareAndAdvance(AnchorAdvance{TransactionID: transactionID, BundleDigest: bundleDigest, OriginalExpected: originalExpected, Next: next, PreparedReceiptDigest: prepared})
		if err != nil {
			return BundleResult{}, ErrUnavailable
		}
	} else if snapshot.Sequence != next || snapshot.BundleDigest != bundleDigest || anchorReceipt != "" && anchorReceipt != snapshot.ReceiptDigest {
		return BundleResult{Status: BundleUnknown}, ErrReconcileRequired
	}
	// The remaining transition is identical and deterministic; perform it while
	// retaining the provider serialization lock.
	return p.finishRecoveredBundle(state, snapshot)
}

func (p *Provider) finishRecoveredBundle(state *bundleState, snapshot AnchorSnapshot) (BundleResult, error) {
	if snapshot.Sequence != state.Request.AnchoredNext || snapshot.BundleDigest != state.Request.BundleDigest || !validDigest(snapshot.ReceiptDigest) {
		return BundleResult{Status: BundleUnknown}, ErrReconcileRequired
	}
	p.consumeHighWater(snapshot.Sequence)
	p.unavailable = true
	if state.Anchor == "" {
		if _, err := p.journal.appendRecord("bundle-anchor-advanced", state.Request.TransactionID, struct {
			ReceiptDigest string
			Sequence      uint64
		}{snapshot.ReceiptDigest, snapshot.Sequence}); err != nil {
			return BundleResult{Status: BundleAnchorAdvanced}, ErrReconcileRequired
		}
		state.Anchor = snapshot.ReceiptDigest
	}
	commit := digestObject(struct {
		Kind, TransactionID, BundleDigest, Anchor string
		Sequence                                  uint64
	}{"committed", state.Request.TransactionID, state.Request.BundleDigest, state.Anchor, snapshot.Sequence})
	if _, err := p.journal.appendRecord("bundle-committed", state.Request.TransactionID, struct {
		CommitReceiptDigest, BundleDigest string
		Sequence                          uint64
	}{commit, state.Request.BundleDigest, snapshot.Sequence}); err != nil {
		return BundleResult{Status: BundleAnchorAdvanced}, ErrReconcileRequired
	}
	state.Commit = commit
	p.current = Current{snapshot.Sequence, state.Request.BundleDigest}
	p.unavailable = false
	return p.bundleResult(state), nil
}

func (p *Provider) PrepareLaunch(request LaunchRequest) (LaunchResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.unavailable {
		return LaunchResult{}, ErrUnavailable
	}
	if !validLaunchRequest(request) || !time.Now().UTC().Before(request.Deadline) || request.ExpectedSequence != p.current.Sequence || request.BundleDigest != p.current.BundleDigest {
		return LaunchResult{}, ErrConflict
	}
	key := request.AttemptID + "\x00" + request.LaunchNonce
	if id := p.launchKeys[key]; id != "" {
		state := p.launches[id]
		if state == nil || digestObject(state.Request) != digestObject(request) {
			return LaunchResult{}, ErrConflict
		}
		return launchResult(state), nil
	}
	child, err := p.children.Prepare(request)
	if err != nil || !validDigest(child.IdentityDigest) || !validDigest(child.LaunchReceiptDigest) || child.ReleaseIdentity != request.ReleaseIdentity {
		return LaunchResult{}, ErrUnavailable
	}
	payload := struct {
		Request LaunchRequest `json:"request"`
		Child   StoppedChild  `json:"child"`
	}{request, child}
	if _, err := p.journal.appendRecord("launch-prepared", request.TransactionID, payload); err != nil {
		_, _ = p.children.AbortWait(child.IdentityDigest, "prepare-not-durable")
		return LaunchResult{}, ErrUnavailable
	}
	state := &launchState{Request: request, Child: child}
	p.launches[request.TransactionID] = state
	p.launchKeys[key] = request.TransactionID
	return launchResult(state), nil
}

func (p *Provider) CommitLaunch(transactionID, launchReceipt, releaseIdentity, durableAcceptDigest string) (LaunchResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	state := p.launches[transactionID]
	if state == nil {
		return LaunchResult{Status: LaunchUnknown}, ErrReconcileRequired
	}
	if state.Child.LaunchReceiptDigest != launchReceipt || state.Child.ReleaseIdentity != releaseIdentity || !validDigest(durableAcceptDigest) {
		return LaunchResult{}, ErrConflict
	}
	if state.Released != "" || state.Aborted != "" {
		return launchResult(state), nil
	}
	if state.Accepted != "" && state.Accepted != durableAcceptDigest {
		return LaunchResult{}, ErrConflict
	}
	if !time.Now().UTC().Before(state.Request.Deadline) {
		abort, _ := p.children.AbortWait(state.Child.IdentityDigest, "launch-deadline-expired")
		state.Aborted = abort
		_, _ = p.journal.appendRecord("launch-aborted", transactionID, struct{ ReceiptDigest, Reason string }{abort, "launch-deadline-expired"})
		return launchResult(state), ErrConflict
	}
	if p.unavailable || p.current.Sequence != state.Request.ExpectedSequence || p.current.BundleDigest != state.Request.BundleDigest {
		abort, _ := p.children.AbortWait(state.Child.IdentityDigest, "authority-changed-before-release")
		state.Aborted = abort
		_, _ = p.journal.appendRecord("launch-aborted", transactionID, struct{ ReceiptDigest, Reason string }{abort, "authority-changed-before-release"})
		return launchResult(state), ErrConflict
	}
	if state.Accepted == "" {
		if _, err := p.journal.appendRecord("launch-accepted", transactionID, struct{ DurableAcceptDigest string }{durableAcceptDigest}); err != nil {
			return LaunchResult{}, ErrUnavailable
		}
		state.Accepted = durableAcceptDigest
	}
	release, err := p.children.Release(state.Child.IdentityDigest, releaseIdentity, durableAcceptDigest)
	if err != nil {
		observed, inspectErr := p.children.Inspect(state.Child.IdentityDigest)
		if inspectErr != nil || observed.Status != LaunchReleased || !validDigest(observed.ReceiptDigest) {
			return LaunchResult{Status: LaunchUnknown}, ErrReconcileRequired
		}
		release = observed.ReceiptDigest
	}
	if !validDigest(release) {
		return LaunchResult{Status: LaunchUnknown}, ErrReconcileRequired
	}
	if _, err := p.journal.appendRecord("launch-released", transactionID, struct{ ReceiptDigest string }{release}); err != nil {
		return LaunchResult{Status: LaunchUnknown}, ErrReconcileRequired
	}
	state.Released = release
	return launchResult(state), nil
}

func (p *Provider) InspectLaunch(attemptID, nonce, requestDigest string) (LaunchResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	id := p.launchKeys[attemptID+"\x00"+nonce]
	state := p.launches[id]
	if state == nil || state.Request.RequestDigest != requestDigest {
		return LaunchResult{Status: LaunchUnknown}, ErrReconcileRequired
	}
	if state.Released != "" || state.Aborted != "" {
		return launchResult(state), nil
	}
	observed, err := p.children.Inspect(state.Child.IdentityDigest)
	if err != nil || observed.Status == LaunchUnknown || observed.Status == LaunchReleased || observed.Status == LaunchAborted {
		return LaunchResult{Status: LaunchUnknown}, ErrReconcileRequired
	}
	if observed.Status == LaunchExited {
		result := launchResult(state)
		result.Status = LaunchExited
		return result, nil
	}
	return launchResult(state), nil
}

func (p *Provider) rebuild() error {
	for _, record := range p.journal.snapshotRecords() {
		switch record.Kind {
		case "bundle-prepared":
			var v struct {
				Request               BundlePrepare `json:"request"`
				PreparedReceiptDigest string        `json:"preparedReceiptDigest"`
			}
			if json.Unmarshal(record.Payload, &v) != nil || !validBundlePrepare(v.Request) {
				return ErrUnavailable
			}
			p.bundles[record.TransactionID] = &bundleState{Request: v.Request, Prepared: v.PreparedReceiptDigest}
		case "bundle-anchor-advanced":
			state := p.bundles[record.TransactionID]
			var v struct {
				ReceiptDigest string
				Sequence      uint64
			}
			if state == nil || json.Unmarshal(record.Payload, &v) != nil {
				return ErrUnavailable
			}
			state.Anchor = v.ReceiptDigest
			p.consumeHighWater(v.Sequence)
		case "bundle-committed":
			state := p.bundles[record.TransactionID]
			var v struct {
				CommitReceiptDigest, BundleDigest string
				Sequence                          uint64
			}
			if state == nil || json.Unmarshal(record.Payload, &v) != nil || v.BundleDigest != state.Request.BundleDigest {
				return ErrUnavailable
			}
			state.Commit = v.CommitReceiptDigest
			p.current = Current{v.Sequence, v.BundleDigest}
			p.consumeHighWater(v.Sequence)
		case "launch-prepared":
			var v struct {
				Request LaunchRequest `json:"request"`
				Child   StoppedChild  `json:"child"`
			}
			if json.Unmarshal(record.Payload, &v) != nil {
				return ErrUnavailable
			}
			state := &launchState{Request: v.Request, Child: v.Child}
			p.launches[record.TransactionID] = state
			p.launchKeys[v.Request.AttemptID+"\x00"+v.Request.LaunchNonce] = record.TransactionID
		case "launch-accepted":
			state := p.launches[record.TransactionID]
			var v struct{ DurableAcceptDigest string }
			if state == nil || json.Unmarshal(record.Payload, &v) != nil {
				return ErrUnavailable
			}
			state.Accepted = v.DurableAcceptDigest
		case "launch-released":
			state := p.launches[record.TransactionID]
			var v struct{ ReceiptDigest string }
			if state == nil || json.Unmarshal(record.Payload, &v) != nil {
				return ErrUnavailable
			}
			state.Released = v.ReceiptDigest
		case "launch-aborted":
			state := p.launches[record.TransactionID]
			var v struct{ ReceiptDigest, Reason string }
			if state == nil || json.Unmarshal(record.Payload, &v) != nil {
				return ErrUnavailable
			}
			state.Aborted = v.ReceiptDigest
		case "anchor-high-water":
			var v struct {
				Sequence     uint64
				BundleDigest string
			}
			if json.Unmarshal(record.Payload, &v) != nil || !validDigest(v.BundleDigest) {
				return ErrUnavailable
			}
			p.consumeHighWater(v.Sequence)
			p.unknownAnchorBundle = v.BundleDigest
			p.unavailable = true
		case "anchor-fork":
			p.forked = true
			p.unavailable = true
		default:
			return ErrUnavailable
		}
	}
	return nil
}

func (p *Provider) reconcileAnchorOnOpen() error {
	snapshot, err := p.anchor.Snapshot()
	if err != nil {
		return ErrUnavailable
	}
	if p.forked {
		p.unavailable = true
		return ErrUnavailable
	}
	if snapshot.Sequence < p.highWater {
		p.unavailable = true
		return ErrUnavailable
	}
	if snapshot.Sequence > p.highWater {
		for id, state := range p.bundles {
			if state.Commit == "" && state.Request.AnchoredNext == snapshot.Sequence && state.Request.BundleDigest == snapshot.BundleDigest && validDigest(snapshot.ReceiptDigest) {
				if _, appendErr := p.journal.appendRecord("bundle-anchor-advanced", id, struct {
					ReceiptDigest string
					Sequence      uint64
				}{snapshot.ReceiptDigest, snapshot.Sequence}); appendErr != nil {
					return appendErr
				}
				state.Anchor = snapshot.ReceiptDigest
				p.consumeHighWater(snapshot.Sequence)
				p.unavailable = true
				return nil
			}
		}
		p.consumeHighWater(snapshot.Sequence)
		p.unavailable = true
		if _, appendErr := p.journal.appendRecord("anchor-high-water", "anchor-observation", struct {
			Sequence     uint64
			BundleDigest string
		}{snapshot.Sequence, snapshot.BundleDigest}); appendErr != nil {
			return appendErr
		}
		return nil
	}
	for _, state := range p.bundles {
		if state.Commit == "" && state.Anchor != "" && state.Request.AnchoredNext == snapshot.Sequence && state.Request.BundleDigest == snapshot.BundleDigest {
			p.unavailable = true
			return nil
		}
	}
	if snapshot.Sequence != p.current.Sequence || snapshot.BundleDigest != p.current.BundleDigest {
		if snapshot.Sequence == p.highWater && snapshot.BundleDigest == p.unknownAnchorBundle {
			p.unavailable = true
			return nil
		}
		if snapshot.Sequence == p.highWater {
			p.forked = true
			_, _ = p.journal.appendRecord("anchor-fork", "anchor-observation", struct {
				Sequence     uint64
				BundleDigest string
			}{snapshot.Sequence, snapshot.BundleDigest})
		}
		p.unavailable = true
		return ErrUnavailable
	}
	return nil
}

func (p *Provider) abortRestartPending() error {
	for id, state := range p.launches {
		if state.Released == "" && state.Aborted == "" {
			receipt, err := p.children.AbortWait(state.Child.IdentityDigest, "provider-restart-fail-closed")
			if err != nil || !validDigest(receipt) {
				p.unavailable = true
				return ErrUnavailable
			}
			if _, err = p.journal.appendRecord("launch-aborted", id, struct{ ReceiptDigest, Reason string }{receipt, "provider-restart-fail-closed"}); err != nil {
				return err
			}
			state.Aborted = receipt
		}
	}
	return nil
}

func (p *Provider) consumeHighWater(sequence uint64) {
	if sequence > p.highWater {
		p.highWater = sequence
	}
}
func (p *Provider) bundleResult(s *bundleState) BundleResult {
	status := BundlePreparedNoAnchor
	if s.Anchor != "" {
		status = BundleAnchorAdvanced
	}
	if s.Commit != "" {
		status = BundleCommitted
	}
	return BundleResult{status, s.Prepared, s.Anchor, s.Commit, s.Request.OriginalExpected, s.Request.AnchoredNext, p.highWater}
}
func launchResult(s *launchState) LaunchResult {
	status := LaunchPending
	if s.Released != "" {
		status = LaunchReleased
	} else if s.Aborted != "" {
		status = LaunchAborted
	}
	return LaunchResult{status, s.Request.TransactionID, s.Child.IdentityDigest, s.Child.LaunchReceiptDigest, s.Child.ReleaseIdentity, s.Released, s.Aborted}
}
func validBundlePrepare(v BundlePrepare) bool {
	return validID(v.TransactionID) && (v.UpdateKind == "evidence-update" || v.UpdateKind == "planned-rotation" || v.UpdateKind == "security-revocation") && validDigest(v.PreviousBundleDigest) && validDigest(v.BundleDigest) && v.BundleDigest != v.PreviousBundleDigest && v.OriginalExpected > 0 && v.AnchoredNext == v.OriginalExpected+1 && validDigest(v.AuthorizationDigest) && validDigest(v.StagedLeafSetDigest)
}
func validLaunchRequest(v LaunchRequest) bool {
	return validID(v.TransactionID) && validID(v.AttemptID) && validID(v.LaunchNonce) && validDigest(v.RequestDigest) && validDigest(v.BundleDigest) && v.ExpectedSequence > 0 && validDigest(v.ProfileReceiptDigest) && validDigest(v.ReleaseIdentity) && !v.Deadline.IsZero()
}
func validID(v string) bool {
	if len(v) < 1 || len(v) > 128 {
		return false
	}
	for _, b := range []byte(v) {
		if b < 0x21 || b > 0x7e {
			return false
		}
	}
	return true
}
func validDigest(v string) bool {
	if len(v) != 71 || v[:7] != "sha256:" {
		return false
	}
	for _, b := range []byte(v[7:]) {
		if b < '0' || b > '9' && b < 'a' || b > 'f' {
			return false
		}
	}
	return true
}
func digestObject(v any) string {
	encoded, err := canonicalValue(v)
	if err != nil {
		return ""
	}
	return canonical.DigestBytes(encoded)
}
