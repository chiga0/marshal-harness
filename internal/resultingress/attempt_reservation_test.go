package resultingress

import (
	"bytes"
	"context"
	"errors"
	"os"
	"reflect"
	"testing"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

func reservationReady(id AttemptIdentity) ReadyRunAuthority {
	return ReadyRunAuthority{
		AuthorityNamespaceID: id.AuthorityNamespaceID,
		TaskID:               id.TaskID,
		RunID:                id.RunID,
		OrchestratorID:       id.OrchestratorID,
		ReadySequence:        2,
		ReadyAuthorityHead:   id.RunAuthorityDigest,
		AttemptsUsed:         0,
		MaxAttempts:          3,
		SpecDigest:           attemptTestDigest("reservation-spec"),
		PolicyDigest:         attemptTestDigest("reservation-policy"),
		CapabilityDigest:     attemptTestDigest("reservation-capability"),
		BaseSHA:              "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		WorktreePath:         "/tmp/marshal-reservation-worktree",
	}
}

func reservationLedgerBytes(t *testing.T, store *DurableStore) []byte {
	t.Helper()
	raw, err := os.ReadFile(store.ledgerPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

type sealedSuccessorVerifier struct{ want SealedRunSuccessorAuthority }

func (verifier sealedSuccessorVerifier) WithCurrentSealedRunSuccessor(_ context.Context, got SealedRunSuccessorAuthority, fn func() error) error {
	if got != verifier.want || fn == nil {
		return ErrAttemptReservationConflict
	}
	return fn()
}

type zeroSideEffectVerifier struct {
	want  AttemptReservationState
	proof ZeroSideEffectProof
}

type readyCallbackErrorVerifier struct {
	callbackErr error
	verifierErr error
	double      bool
}

func (verifier readyCallbackErrorVerifier) WithCurrentReadyRunAuthority(_ context.Context, _ ReadyRunAuthority, fn func() error) error {
	first := fn()
	if verifier.double {
		_ = fn()
	}
	if verifier.verifierErr != nil {
		return verifier.verifierErr
	}
	if verifier.callbackErr != nil {
		return verifier.callbackErr
	}
	return first
}

type sealedResponseLossVerifier struct {
	want SealedRunSuccessorAuthority
	err  error
}

func (verifier sealedResponseLossVerifier) WithCurrentSealedRunSuccessor(_ context.Context, got SealedRunSuccessorAuthority, fn func() error) error {
	if got != verifier.want || fn == nil {
		return ErrAttemptReservationConflict
	}
	if err := fn(); err != nil {
		return err
	}
	return verifier.err
}

func (verifier zeroSideEffectVerifier) WithZeroAttemptSideEffects(_ context.Context, got AttemptReservationState, fn func(ZeroSideEffectProof) error) error {
	if got != verifier.want || fn == nil {
		return ErrAttemptReservationConflict
	}
	return fn(verifier.proof)
}

func reserveAndOpen(t *testing.T, store *DurableStore) (AttemptReservationState, AttemptAuthorityState, ReadyRunAuthority) {
	t.Helper()
	id := attemptTestIdentity()
	ready := reservationReady(id)
	reservation, err := store.ReserveAttempt(context.Background(), attemptReadyVerifier{want: ready}, ready)
	if err != nil || reservation.Status != AttemptReservationActive {
		t.Fatalf("reserve=%+v err=%v", reservation, err)
	}
	id.AttemptID = reservation.Reservation.AttemptID
	opened, err := store.OpenReservedAttempt(context.Background(), attemptReadyVerifier{want: ready}, reservation.ReservationFactDigest, id)
	if err != nil || !opened.Appended || opened.State.ProtocolRevision != attemptAuthorityProtocolV2 || opened.State.OpenedSchemaRevision != attemptOpenedSchemaV2 {
		t.Fatalf("open=%+v err=%v", opened, err)
	}
	return reservation, opened.State, ready
}

func TestAttemptReservationLookupBeforeMintAndBudgetFailClosed(t *testing.T) {
	store, err := OpenResultIngressStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := attemptTestIdentity()
	ready := reservationReady(id)
	first, err := store.ReserveAttempt(context.Background(), attemptReadyVerifier{want: ready}, ready)
	if err != nil || first.Status != AttemptReservationActive || first.Reservation.AttemptOrdinal != 1 {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	before := reservationLedgerBytes(t, store)
	replay, err := store.ReserveAttempt(context.Background(), attemptReadyVerifier{want: ready}, ready)
	if err != nil || replay != first || !bytes.Equal(before, reservationLedgerBytes(t, store)) {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	drifted := ready
	drifted.SpecDigest = attemptTestDigest("drifted-spec")
	if _, err := store.ReserveAttempt(context.Background(), attemptReadyVerifier{want: ready}, drifted); !errors.Is(err, ErrRunAuthorityUnauthorized) {
		t.Fatalf("drifted READY err=%v", err)
	}
	if !bytes.Equal(before, reservationLedgerBytes(t, store)) {
		t.Fatal("rejected READY drift changed ledger")
	}
	exhausted := ready
	exhausted.AttemptsUsed = exhausted.MaxAttempts
	if _, err := store.ReserveAttempt(context.Background(), attemptReadyVerifier{want: exhausted}, exhausted); !errors.Is(err, ErrAttemptReservationExhausted) {
		t.Fatalf("exhausted budget err=%v", err)
	}
	if !bytes.Equal(before, reservationLedgerBytes(t, store)) {
		t.Fatal("exhausted reservation changed ledger")
	}
}

func TestAttemptOpenedV2BindsExactReservationAndReplays(t *testing.T) {
	store, err := OpenResultIngressStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reservation, opened, ready := reserveAndOpen(t, store)
	before := reservationLedgerBytes(t, store)
	replay, err := store.OpenReservedAttempt(context.Background(), attemptReadyVerifier{want: ready}, reservation.ReservationFactDigest, opened.Identity)
	if err != nil || replay.Appended || !reflect.DeepEqual(replay.State, opened) || !bytes.Equal(before, reservationLedgerBytes(t, store)) {
		t.Fatalf("open replay=%+v err=%v", replay, err)
	}
	forged := opened.Identity
	forged.TaskID = "task-forged"
	if _, err := store.OpenReservedAttempt(context.Background(), attemptReadyVerifier{want: ready}, reservation.ReservationFactDigest, forged); !errors.Is(err, ErrAttemptReservationConflict) {
		t.Fatalf("forged full identity err=%v", err)
	}
	if !bytes.Equal(before, reservationLedgerBytes(t, store)) {
		t.Fatal("forged full identity changed ledger")
	}
}

func TestAttemptReservationConsumesExactSealedSuccessorOnce(t *testing.T) {
	store, err := OpenResultIngressStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reservation, opened, ready := reserveAndOpen(t, store)
	sealed := SealedRunSuccessorAuthority{
		ReservationFactDigest:   reservation.ReservationFactDigest,
		Ready:                   ready,
		AttemptID:               opened.Identity.AttemptID,
		AttemptOpenedFactDigest: opened.OpenedDigest,
		AttemptOrdinal:          1,
		AttemptsUsedAfter:       1,
		RunSuccessorSequence:    ready.ReadySequence + 1,
		RunSuccessorHead:        attemptTestDigest("run-successor"),
	}
	consumed, err := store.ConsumeAttemptReservation(context.Background(), sealedSuccessorVerifier{want: sealed}, sealed)
	if err != nil || consumed.Status != AttemptReservationConsumed || consumed.RunSuccessorHead != sealed.RunSuccessorHead {
		t.Fatalf("consume=%+v err=%v", consumed, err)
	}
	before := reservationLedgerBytes(t, store)
	replay, err := store.ConsumeAttemptReservation(context.Background(), sealedSuccessorVerifier{want: sealed}, sealed)
	if err != nil || replay != consumed || !bytes.Equal(before, reservationLedgerBytes(t, store)) {
		t.Fatalf("consume replay=%+v err=%v", replay, err)
	}
}

func TestAttemptReservationCallbackErrorPrecedesVerifierClassification(t *testing.T) {
	ready := reservationReady(attemptTestIdentity())
	callbackErr := errors.New("injected durable outcome unknown")
	verifierErr := errors.New("verifier returned after callback")
	err := withCurrentReadyRunAuthority(context.Background(), readyCallbackErrorVerifier{callbackErr: callbackErr, verifierErr: verifierErr}, ready, func() error { return callbackErr })
	if err != callbackErr {
		t.Fatalf("callback error=%v, want exact %v", err, callbackErr)
	}
	err = withCurrentReadyRunAuthority(context.Background(), readyCallbackErrorVerifier{double: true}, ready, func() error { return callbackErr })
	if !errors.Is(err, ErrRunAuthorityUnauthorized) || err == callbackErr {
		t.Fatalf("duplicate callback precedence=%v", err)
	}
}

func TestAttemptReservationResponseLossReplaysExactResolution(t *testing.T) {
	store, err := OpenResultIngressStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reservation, opened, ready := reserveAndOpen(t, store)
	sealed := SealedRunSuccessorAuthority{
		ReservationFactDigest: reservation.ReservationFactDigest, Ready: ready,
		AttemptID: opened.Identity.AttemptID, AttemptOpenedFactDigest: opened.OpenedDigest,
		AttemptOrdinal: 1, AttemptsUsedAfter: 1, RunSuccessorSequence: ready.ReadySequence + 1,
		RunSuccessorHead: attemptTestDigest("response-loss-successor"),
	}
	lost := errors.New("injected verifier response loss")
	if _, err := store.ConsumeAttemptReservation(context.Background(), sealedResponseLossVerifier{want: sealed, err: lost}, sealed); !errors.Is(err, ErrAttemptReservationConflict) {
		t.Fatalf("response-loss classification=%v", err)
	}
	afterLoss := reservationLedgerBytes(t, store)
	replay, err := store.ConsumeAttemptReservation(context.Background(), sealedSuccessorVerifier{want: sealed}, sealed)
	if err != nil || replay.Status != AttemptReservationConsumed || replay.RunSuccessorHead != sealed.RunSuccessorHead || !bytes.Equal(afterLoss, reservationLedgerBytes(t, store)) {
		t.Fatalf("response-loss replay=%+v err=%v", replay, err)
	}
}

func TestAttemptReservationRejectsForgedSealedReadyWithoutMutation(t *testing.T) {
	store, err := OpenResultIngressStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reservation, opened, ready := reserveAndOpen(t, store)
	forged := ready
	forged.PolicyDigest = attemptTestDigest("forged-policy")
	sealed := SealedRunSuccessorAuthority{
		ReservationFactDigest: reservation.ReservationFactDigest, Ready: forged,
		AttemptID: opened.Identity.AttemptID, AttemptOpenedFactDigest: opened.OpenedDigest,
		AttemptOrdinal: 1, AttemptsUsedAfter: 1, RunSuccessorSequence: ready.ReadySequence + 1,
		RunSuccessorHead: attemptTestDigest("forged-successor"),
	}
	before := reservationLedgerBytes(t, store)
	if _, err := store.ConsumeAttemptReservation(context.Background(), sealedSuccessorVerifier{want: sealed}, sealed); !errors.Is(err, ErrAttemptReservationConflict) {
		t.Fatalf("forged sealed READY err=%v", err)
	}
	if !bytes.Equal(before, reservationLedgerBytes(t, store)) {
		t.Fatal("forged sealed READY changed ledger")
	}
}

func TestAttemptReservationCancellationUsesVerifierProofAndExactReplay(t *testing.T) {
	store, err := OpenResultIngressStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ready := reservationReady(attemptTestIdentity())
	reservation, err := store.ReserveAttempt(context.Background(), attemptReadyVerifier{want: ready}, ready)
	if err != nil {
		t.Fatal(err)
	}
	proof := ZeroSideEffectProof{SchemaRevision: "attempt-zero-side-effect-proof/v1", ReservationFactDigest: reservation.ReservationFactDigest, ReadyAuthorityHead: ready.ReadyAuthorityHead, ObservationDigest: canonical.DigestBytes([]byte("all-zero"))}
	verifier := zeroSideEffectVerifier{want: reservation, proof: proof}
	cancelled, err := store.CancelAttemptReservation(context.Background(), verifier, reservation.ReservationFactDigest)
	if err != nil || cancelled.Status != AttemptReservationCancelled {
		t.Fatalf("cancel=%+v err=%v", cancelled, err)
	}
	before := reservationLedgerBytes(t, store)
	replayVerifier := zeroSideEffectVerifier{want: cancelled, proof: proof}
	replay, err := store.CancelAttemptReservation(context.Background(), replayVerifier, reservation.ReservationFactDigest)
	if err != nil || replay != cancelled || !bytes.Equal(before, reservationLedgerBytes(t, store)) {
		t.Fatalf("cancel replay=%+v err=%v", replay, err)
	}
	different := proof
	different.ObservationDigest = canonical.DigestBytes([]byte("different-zero-proof"))
	if _, err := store.CancelAttemptReservation(context.Background(), zeroSideEffectVerifier{want: cancelled, proof: different}, reservation.ReservationFactDigest); !errors.Is(err, ErrAttemptReservationResolved) {
		t.Fatalf("different cancellation proof replay err=%v", err)
	}
	if !bytes.Equal(before, reservationLedgerBytes(t, store)) {
		t.Fatal("different cancellation proof changed ledger")
	}
}

func TestAttemptReservationCannotCancelAfterOpened(t *testing.T) {
	store, err := OpenResultIngressStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reservation, _, ready := reserveAndOpen(t, store)
	proof := ZeroSideEffectProof{SchemaRevision: "attempt-zero-side-effect-proof/v1", ReservationFactDigest: reservation.ReservationFactDigest, ReadyAuthorityHead: ready.ReadyAuthorityHead, ObservationDigest: canonical.DigestBytes([]byte("claimed-zero"))}
	before := reservationLedgerBytes(t, store)
	if _, err := store.CancelAttemptReservation(context.Background(), zeroSideEffectVerifier{want: reservation, proof: proof}, reservation.ReservationFactDigest); !errors.Is(err, ErrAttemptReservationConflict) {
		t.Fatalf("cancel after opened err=%v", err)
	}
	if !bytes.Equal(before, reservationLedgerBytes(t, store)) {
		t.Fatal("cancel after opened changed ledger")
	}
}

type asynchronousReadyVerifier struct {
	release <-chan struct{}
	done    chan error
}

func (verifier asynchronousReadyVerifier) WithCurrentReadyRunAuthority(_ context.Context, _ ReadyRunAuthority, fn func() error) error {
	go func() {
		<-verifier.release
		verifier.done <- fn()
	}()
	return nil
}

func TestAttemptReservationRejectsEscapedVerifierCallback(t *testing.T) {
	store, err := OpenResultIngressStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ready := reservationReady(attemptTestIdentity())
	release, done := make(chan struct{}), make(chan error, 1)
	if _, err := store.ReserveAttempt(context.Background(), asynchronousReadyVerifier{release: release, done: done}, ready); !errors.Is(err, ErrRunAuthorityUnauthorized) {
		t.Fatalf("escaped verifier err=%v", err)
	}
	close(release)
	if callbackErr := <-done; !errors.Is(callbackErr, ErrRunAuthorityUnauthorized) {
		t.Fatalf("escaped callback err=%v", callbackErr)
	}
	if len(reservationLedgerBytes(t, store)) != 0 {
		t.Fatal("escaped verifier appended after return")
	}
}
