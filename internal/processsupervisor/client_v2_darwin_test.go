//go:build darwin

package processsupervisor

import (
	"context"
	"os"
	"testing"
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
