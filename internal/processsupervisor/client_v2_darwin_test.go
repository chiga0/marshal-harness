//go:build darwin

package processsupervisor

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// This crosses the actual Unix codec and inherited server loop using the
// public prepared-command API. Fake mechanics are explicit; it does not
// replace a fixed-image/real-Pi production integration receipt.
func TestClientV2ToInheritedServerCompletePreparedLifecycle(t *testing.T) {
	m := &transcriptMechanicsV2{}
	h := newSupervisorV2Harness(t, func(_ *sessionV2, d *os.File) { m.directory = d }, m)
	directory, err := ObserveHeldControlDirectory(h.directory)
	if err != nil {
		t.Fatal(err)
	}
	h.session.core.mu.Lock()
	anchor := testAnchorV2(h.session)
	h.session.core.mu.Unlock()
	anchor.ControlDirectory = directory
	anchor.Binding.ControlSocket, anchor.Binding.ControlFiles = h.handshake.ControlSocket, h.handshake.ControlFiles
	client, err := newClientV2(h.connection, h.handshake, anchor, h.bootstrap.Core)
	if err != nil {
		t.Fatal(err)
	}
	client.codec = h.codec
	defer client.Disconnect()
	do := func(command CommandName, id string, payload any) VerifiedCommandOutcomeV2 {
		t.Helper()
		prepared, err := client.Prepare(clientOptionsV2(client.Anchor(), command, id), payload)
		if err != nil {
			t.Fatalf("prepare %s: %v", command, err)
		}
		// Fake Core retains an exact intent here; production ResultIngress
		// persistence is a separate S3 integration gate, not simulated evidence.
		intent := prepared.Evidence()
		outcome, err := client.DoPrepared(context.Background(), prepared)
		if err != nil || outcome.Status != "ok" || outcome.Preparation != intent {
			t.Fatalf("execute %s: %v %+v", command, err, outcome)
		}
		state, err := readHeldJournalStateV2(h.session.journal.file)
		if err != nil || state.head != outcome.PostCommand.Binding.JournalHead || state.commandHead != outcome.CommandHead {
			t.Fatalf("post-command journal %s: %v", command, err)
		}
		return outcome
	}
	do(CommandBindAuthority, "prepared-wire-bind", validBindPayloadForAnchorV2(anchor))
	payload := validSpawnPayload()
	payload.LaunchAuthorizedFactDigest = h.bootstrap.LaunchAuthorizedFact
	payload.SupervisorStartedFactDigest = digest("client-v2-started")
	do(CommandSpawn, "prepared-wire-spawn", payload)
	started := digest("prepared-wire-process")
	resumed := do(CommandResume, "prepared-wire-resume", ResumePayload{ProcessStartedFactDigest: started})
	cleanup := CleanupPayload{ProcessStartedFactDigest: started, LastObservationDigest: resumed.ObservationDigest, TerminalizationBarrierDigest: digest("prepared-barrier"), TerminalizationID: "prepared-terminal", TerminalGeneration: 1, CleanupBindingDigest: digest("prepared-cleanup")}
	observed := do(CommandInspect, "prepared-wire-inspect", cleanup)
	collected := do(CommandCollect, "prepared-wire-collect", CollectPayload{ProcessStartedFactDigest: started, LastObservationDigest: observed.ObservationDigest})
	if collected.ProcessReport == nil || collected.TranscriptDigest == collected.ObservationDigest {
		t.Fatal("v2 transcript and wrapped observation conflated")
	}
	// Close may finish and close the writer before the client receives its
	// response. Inspect the held file via a separately opened read descriptor.
	file, err := openControlFileAt(h.directory, journalFileNameV2)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	prepared, err := client.Prepare(clientOptionsV2(client.Anchor(), CommandClose, "prepared-wire-close"), ClosePayload{CleanupBindingDigest: cleanup.CleanupBindingDigest, ProcessTerminalFactDigest: digest("prepared-terminal-fact"), AllocationTerminatedDigest: digest("prepared-allocation-terminal")})
	if err != nil {
		t.Fatal(err)
	}
	closed, err := client.DoPrepared(context.Background(), prepared)
	if err != nil || closed.Status != "ok" {
		t.Fatalf("close: %v", err)
	}
	if err := h.wait(t); err != nil {
		t.Fatal(err)
	}
	state, err := readHeldJournalStateV2(file)
	if err != nil || state.sequence != 13 || state.head != closed.PostCommand.Binding.JournalHead || state.pending != nil || m.calls != 5 {
		t.Fatalf("terminal journal: %v seq=%d calls=%d", err, state.sequence, m.calls)
	}
}

func TestClientV2ReconnectWireThenSuccessorCommand(t *testing.T) {
	m := &countingMechanicsV2{}
	h := newSupervisorV2Harness(t, nil, m)
	h.bind(t)
	h.session.core.mu.Lock()
	anchor := testAnchorV2(h.session)
	payload := validSpawnPayload()
	payload.LaunchAuthorizedFactDigest, payload.SupervisorStartedFactDigest = h.session.core.launchFact, h.session.core.supervisorStartedFact
	h.session.core.mu.Unlock()
	directory, err := ObserveHeldControlDirectory(h.directory)
	if err != nil {
		t.Fatal(err)
	}
	anchor.ControlDirectory = directory
	anchor.Binding.ControlSocket, anchor.Binding.ControlFiles = h.handshake.ControlSocket, h.handshake.ControlFiles
	prepared, err := PrepareCommandV2(anchor, clientOptionsV2(anchor, CommandSpawn, "wire-recovery-spawn"), payload)
	if err != nil {
		t.Fatal(err)
	}
	// The peer sends an exact durable request but Core loses the response
	// before recording it. Discard it without advancing Core's saved anchor.
	lost := h.do(t, prepared.request)
	_ = h.connection.Close()
	select {
	case <-h.reconnectReady:
	case <-time.After(5 * time.Second):
		t.Fatal("reconnect not ready")
	}
	held, err := openHeldSessionControlFilesForLeaf(h.directory, anchor.Binding.ControlFiles, journalFileNameV2)
	if err != nil {
		t.Fatal(err)
	}
	defer held.close()
	state, err := readHeldJournalStateV2(held.journal)
	if err != nil || validateReconnectJournalV2(state, anchor, &prepared) != nil {
		t.Fatal("journal preflight")
	}
	nonce, err := readSessionNonce(held, anchor.Binding.SessionNonceDigest)
	if err != nil {
		t.Fatal(err)
	}
	plan := reconnectPlanForAnchorV2(anchor)
	request, err := prepareReconnectRequestV2(anchor, plan, &prepared, nonce, h.bootstrap.Core)
	if err != nil {
		t.Fatal(err)
	}
	// This Fake harness owns a short /private/tmp socket outside repository
	// cwd. Production ReconnectV2 deliberately requires a repository-relative
	// address; do not relax that boundary or change process-wide cwd for a test.
	address := filepath.Join(h.root, controlSocket)
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: address, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	codec, _ := NewProtocolCodec(conn)
	var handshake HandshakeResponseV2
	err = runBoundedTransport(context.Background(), conn, time.Now().Add(5*time.Second), func() error {
		if err := codec.Write(request); err != nil {
			return err
		}
		return codec.Read(&handshake)
	})
	if err != nil {
		t.Fatal(err)
	}
	client, err := newReconnectedClientV2(conn, codec, handshake, anchor, plan, &prepared, h.bootstrap.Core)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Disconnect()
	recovery, ok := client.Recovery()
	if !ok || recovery.Reconciliation != ReconciliationReceiptCommitted || recovery.ReplayedOutcome == nil || recovery.ReplayedOutcome.ReceiptDigest != lost.ReceiptDigest {
		t.Fatal("lost receipt not recovered")
	}
	resume, err := client.Prepare(clientOptionsV2(client.Anchor(), CommandResume, "successor-resume"), ResumePayload{ProcessStartedFactDigest: digest("successor-started")})
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := client.DoPrepared(context.Background(), resume)
	if err != nil || outcome.Status != "ok" {
		t.Fatalf("successor command: %v", err)
	}
	state, err = readHeldJournalStateV2(held.journal)
	boundary := sessionControlBoundary{directory: h.directory, directoryIdentity: directory, socket: anchor.Binding.ControlSocket, controlFiles: anchor.Binding.ControlFiles, heldFiles: held}
	if err != nil || boundary.revalidateV2(state) != nil || state.head != outcome.PostCommand.Binding.JournalHead || state.ownerEpoch != plan.OwnerEpoch {
		t.Fatal("successor journal mismatch")
	}
	h.session.core.mu.Lock()
	calls := m.calls
	h.session.core.mu.Unlock()
	if calls != 2 {
		t.Fatalf("spawn/resume calls=%d", calls)
	}
}
