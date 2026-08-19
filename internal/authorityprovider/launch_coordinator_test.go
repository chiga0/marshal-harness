package authorityprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

type coordinatorEffects struct {
	prepareCalls int
	commitCalls  int
	abortCalls   int
	inspectCalls int
	commitErr    error
}

func (effects *coordinatorEffects) PrepareLaunch(context.Context, PrepareLaunchPayload, []FDRef) (PrepareLaunchSuccessPayload, error) {
	effects.prepareCalls++
	return PrepareLaunchSuccessPayload{LaunchTransactionID: "launch-txn-1", APAPLaunchRequestDigest: testDigest("launch-request"), ProfileRequestDigest: testDigest("profile-request"), LaunchReceiptDigest: testDigest("launch-receipt"), LaunchReceipt: json.RawMessage(`{"status":"prepared"}`), ReleaseIdentity: testDigest("release-identity"), Deadline: fixtureNow.Add(time.Minute)}, nil
}

func (effects *coordinatorEffects) CommitLaunch(context.Context, CommitLaunchPayload, LaunchTransaction) (CommitLaunchSuccessPayload, error) {
	effects.commitCalls++
	if effects.commitErr != nil {
		return CommitLaunchSuccessPayload{}, effects.commitErr
	}
	return CommitLaunchSuccessPayload{Status: "released", ReleaseReceiptDigest: testDigest("release-receipt"), ReleaseReceipt: json.RawMessage(`{"status":"released"}`)}, nil
}

func (effects *coordinatorEffects) AbortLaunch(context.Context, AbortLaunchPayload, LaunchTransaction) (AbortLaunchSuccessPayload, error) {
	effects.abortCalls++
	return AbortLaunchSuccessPayload{Status: "aborted", AbortReceiptDigest: testDigest("abort-receipt"), AbortReceipt: json.RawMessage(`{"status":"aborted"}`)}, nil
}

func (effects *coordinatorEffects) InspectLaunch(_ context.Context, _ InspectLaunchPayload, transaction *LaunchTransaction) (InspectLaunchSuccessPayload, error) {
	effects.inspectCalls++
	if transaction == nil {
		return InspectLaunchSuccessPayload{Status: "unknown"}, nil
	}
	result := InspectLaunchSuccessPayload{Status: string(transaction.Status), LaunchTransactionID: transaction.LaunchTransactionID, ChildIdentityDigest: testDigest("child"), LaunchReceiptDigest: transaction.LaunchReceiptDigest}
	switch transaction.Status {
	case LaunchReleased:
		result.ReleaseReceiptDigest = testDigest("release-receipt")
	case LaunchAborted, LaunchExited:
		result.AbortReceiptDigest = testDigest("abort-receipt")
	}
	return result, nil
}

func TestLaunchCoordinatorPrepareCommitReplayAndInspect(t *testing.T) {
	effects := &coordinatorEffects{}
	coordinator, err := NewLaunchCoordinator(effects, 7)
	if err != nil {
		t.Fatal(err)
	}
	peer := defaultPeer(PrincipalConsumer)
	prepare := validRequest(OperationPrepareLaunch, peer)
	prepare.CommandID = "prepare-command"
	prepareRaw := mustSeal(t, prepare)
	prepareResponse, err := coordinator.HandleControl(context.Background(), prepareRaw, peer, fixtureNow, validFDs(OperationPrepareLaunch))
	if err != nil {
		t.Fatal(err)
	}
	decodedPrepare, err := DecodeControlRequest(prepareRaw, peer, fixtureNow, validFDs(OperationPrepareLaunch))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeControlResponse(prepareResponse, decodedPrepare, 7); err != nil {
		t.Fatal(err)
	}
	replay, err := coordinator.HandleControl(context.Background(), prepareRaw, peer, fixtureNow, validFDs(OperationPrepareLaunch))
	if err != nil || !bytes.Equal(prepareResponse, replay) || effects.prepareCalls != 1 {
		t.Fatalf("prepare replay not exact: err=%v calls=%d", err, effects.prepareCalls)
	}
	conflict := prepare
	conflict.Nonce = "nonce-0002"
	conflictRaw := mustSeal(t, conflict)
	conflictResponse, err := coordinator.HandleControl(context.Background(), conflictRaw, peer, fixtureNow, validFDs(OperationPrepareLaunch))
	if err != nil {
		t.Fatal(err)
	}
	decodedConflict, err := DecodeControlRequest(conflictRaw, peer, fixtureNow, validFDs(OperationPrepareLaunch))
	if err != nil {
		t.Fatal(err)
	}
	if response, err := DecodeControlResponse(conflictResponse, decodedConflict, 7); err != nil || response.SafeCode != CodeIdentityMismatch {
		t.Fatalf("command replay conflict = %v, %v", response.SafeCode, err)
	}
	secondReplay, err := coordinator.HandleControl(context.Background(), prepareRaw, peer, fixtureNow, validFDs(OperationPrepareLaunch))
	if err != nil || !bytes.Equal(prepareResponse, secondReplay) {
		t.Fatalf("original replay changed after conflict: err=%v", err)
	}

	commit := validRequest(OperationCommitLaunch, peer)
	commit.CommandID = "commit-command"
	commit.Payload = mustJSON(CommitLaunchPayload{LaunchTransactionID: "launch-txn-1", LaunchReceiptDigest: testDigest("launch-receipt"), ReleaseIdentity: testDigest("release-identity"), DurableAcceptDigest: testDigest("durable-accept")})
	commitRaw := mustSeal(t, commit)
	commitResponse, err := coordinator.HandleControl(context.Background(), commitRaw, peer, fixtureNow, nil)
	if err != nil {
		t.Fatal(err)
	}
	decodedCommit, err := DecodeControlRequest(commitRaw, peer, fixtureNow, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeControlResponse(commitResponse, decodedCommit, 8); err != nil {
		t.Fatal(err)
	}
	if coordinator.ProviderSequence() != 8 || effects.commitCalls != 1 {
		t.Fatalf("commit sequence/calls = %d/%d", coordinator.ProviderSequence(), effects.commitCalls)
	}

	var prepared PrepareLaunchPayload
	if err := json.Unmarshal(prepare.Payload, &prepared); err != nil {
		t.Fatal(err)
	}
	inspect := validRequest(OperationInspectLaunch, peer)
	inspect.CommandID = "inspect-command"
	inspect.Payload = mustJSON(InspectLaunchPayload{AttemptID: prepared.AttemptID, LaunchNonce: prepared.LaunchNonce, APAPLaunchRequestDigest: prepared.APAPLaunchRequestDigest, ProfileRequestDigest: prepared.ProfileRequestDigest})
	inspectRaw := mustSeal(t, inspect)
	inspectResponse, err := coordinator.HandleControl(context.Background(), inspectRaw, peer, fixtureNow, nil)
	if err != nil {
		t.Fatal(err)
	}
	decodedInspect, err := DecodeControlRequest(inspectRaw, peer, fixtureNow, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeControlResponse(inspectResponse, decodedInspect, 8); err != nil {
		t.Fatal(err)
	}
	if effects.inspectCalls != 1 {
		t.Fatalf("inspect calls = %d", effects.inspectCalls)
	}
}

func TestLaunchCoordinatorRejectsStaleAndAmbiguousTransitions(t *testing.T) {
	effects := &coordinatorEffects{commitErr: NewLaunchEffectError(CodeLaunchOutcomeAmbiguous, errors.New("release status lost"))}
	coordinator, err := NewLaunchCoordinator(effects, 7)
	if err != nil {
		t.Fatal(err)
	}
	peer := defaultPeer(PrincipalConsumer)
	prepare := validRequest(OperationPrepareLaunch, peer)
	prepare.CommandID = "prepare-command"
	if _, err := coordinator.HandleControl(context.Background(), mustSeal(t, prepare), peer, fixtureNow, validFDs(OperationPrepareLaunch)); err != nil {
		t.Fatal(err)
	}
	duplicate := prepare
	duplicate.CommandID = "duplicate-command"
	duplicateRaw := mustSeal(t, duplicate)
	duplicateResponse, err := coordinator.HandleControl(context.Background(), duplicateRaw, peer, fixtureNow, validFDs(OperationPrepareLaunch))
	if err != nil {
		t.Fatal(err)
	}
	decodedDuplicate, err := DecodeControlRequest(duplicateRaw, peer, fixtureNow, validFDs(OperationPrepareLaunch))
	if err != nil {
		t.Fatal(err)
	}
	if response, err := DecodeControlResponse(duplicateResponse, decodedDuplicate, 7); err != nil || response.SafeCode != CodeIdentityMismatch {
		t.Fatalf("duplicate prepare response = %v, %v", response.SafeCode, err)
	}

	commit := validRequest(OperationCommitLaunch, peer)
	commit.CommandID = "ambiguous-commit"
	commit.Payload = mustJSON(CommitLaunchPayload{LaunchTransactionID: "launch-txn-1", LaunchReceiptDigest: testDigest("launch-receipt"), ReleaseIdentity: testDigest("release-identity"), DurableAcceptDigest: testDigest("durable-accept")})
	commitRaw := mustSeal(t, commit)
	commitResponse, err := coordinator.HandleControl(context.Background(), commitRaw, peer, fixtureNow, nil)
	if err != nil {
		t.Fatal(err)
	}
	decodedCommit, err := DecodeControlRequest(commitRaw, peer, fixtureNow, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := DecodeControlResponse(commitResponse, decodedCommit, 7)
	if err != nil || response.SafeCode != CodeLaunchOutcomeAmbiguous {
		t.Fatalf("ambiguous commit response = %v, %v", response.SafeCode, err)
	}
	if coordinator.ProviderSequence() != 7 || effects.commitCalls != 1 {
		t.Fatalf("ambiguous commit changed state: sequence=%d calls=%d", coordinator.ProviderSequence(), effects.commitCalls)
	}
}

func TestLaunchCoordinatorInspectUnknownIsNotSuccessWithIdentity(t *testing.T) {
	effects := &coordinatorEffects{}
	coordinator, err := NewLaunchCoordinator(effects, 4)
	if err != nil {
		t.Fatal(err)
	}
	peer := defaultPeer(PrincipalConsumer)
	request := validRequest(OperationInspectLaunch, peer)
	request.CommandID = "unknown-inspect"
	raw := mustSeal(t, request)
	response, err := coordinator.HandleControl(context.Background(), raw, peer, fixtureNow, nil)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeControlRequest(raw, peer, fixtureNow, nil)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Operation != OperationInspectLaunch {
		t.Fatal("wrong operation")
	}
	if _, err := DecodeControlResponse(response, decoded, 4); err != nil {
		t.Fatal(err)
	}
}
