//go:build darwin && arm64

package productionruntime

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/chiga0/marshal-harness/internal/allocationcontrol"
	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/launchidentity"
	"github.com/chiga0/marshal-harness/internal/resultingress"
)

// testLaunchArgvBuilder is the inspectable, deterministic production argv
// builder used by the productionruntime tests. It mirrors the Pi 0.84.4
// production argv shape (canonical node, canonical entrypoint, --mode json
// --print, the frozen hardening flags, optional --model, single trailing
// prompt) without importing adapter/pi. The captured calls let tests assert
// the precise reserved identity was handed to the builder; err injects a
// builder failure for fail-closed coverage.
type testLaunchArgvBuilder struct {
	node       string
	entrypoint string
	profile    string
	model      string
	calls      []AttemptLaunchIdentity
	err        error
}

func (b *testLaunchArgvBuilder) build() AttemptLaunchArgvBuilder {
	return func(identity AttemptLaunchIdentity) (AttemptLaunchArgv, error) {
		b.calls = append(b.calls, identity)
		if b.err != nil {
			return AttemptLaunchArgv{}, b.err
		}
		prompt := testProductionPrompt(identity.TaskID, identity.RunID, identity.AttemptID)
		return AttemptLaunchArgv{Argv: testProductionArgv(b.node, b.entrypoint, b.profile, b.model, prompt), Prompt: prompt}, nil
	}
}

// testProductionArgv mirrors the exact frozen flag order emitted by
// adapter/pi.BuildProductionLaunch so the productionruntime closure-sealing
// assertions exercise the real production argv shape.
func testProductionArgv(node, entrypoint, profile, model, prompt string) []string {
	tools := "read,grep,find,ls,write,edit"
	if profile == "read-only" {
		tools = "read,grep,find,ls,edit"
	}
	argv := []string{node, entrypoint,
		"--mode", "json", "--print", "--no-approve",
		"--no-extensions", "--no-skills", "--no-prompt-templates", "--no-themes", "--no-context-files",
		"--tools", tools, "--no-session",
	}
	if model != "" {
		argv = append(argv, "--model", model)
	}
	return append(argv, prompt)
}

func testProductionPrompt(taskID, runID, attemptID string) string {
	return "The final assistant output must be exactly one WorkerResult JSON object.\n\n" +
		"taskId: " + taskID + "\n" +
		"runId: " + runID + "\n" +
		"attemptId: " + attemptID + "\n" +
		"\nObjective:\nTest objective\n" +
		"\nWorkerResult contract:\n" +
		"- Keep apiVersion, kind, taskId, runId, attemptId, and adapter.id exactly as shown.\n" +
		"- Do not add a result wrapper or any key not shown in the object, except blocker as described below.\n" +
		"- Set status truthfully to completed, blocked, failed, or cancelled. Use completed only when the objective and every constraint are fully satisfied.\n" +
		"- For any non-completed status, add a top-level blocker string explaining why.\n" +
		"- Replace summary truthfully and set outputTruncated truthfully.\n" +
		"- declaredChangedFiles is a unique array of relative-path strings for files actually changed.\n" +
		"- declaredArtifacts is [] unless the objective explicitly requests a named artifact; each artifact must be an object with id, kind, and exactly one relative path or URI. An ordinary changed file is not automatically an artifact.\n" +
		"- declaredCommands is [] or an array of objects with commandId, status (passed, failed, not-run, or unknown), and optional summary.\n" +
		"- declaredRisks is [] or an array of non-empty strings.\n" +
		"- Keep the placeholder adapter executable/version and timestamps; Marshal replaces them with observed authority.\n" +
		`{"apiVersion":"marshal.dev/v1alpha1","kind":"WorkerResult","taskId":"` + taskID + `","runId":"` + runID + `","attemptId":"` + attemptID + `","adapter":{"id":"pi","executable":"marshal-observed","version":"marshal-observed"},"status":"completed","summary":"Describe the outcome","declaredChangedFiles":[],"declaredArtifacts":[],"declaredCommands":[],"declaredRisks":[],"outputTruncated":false,"startedAt":"1970-01-01T00:00:00Z","completedAt":"1970-01-01T00:00:01Z"}` + "\n"
}

// authorizedClosure reads the launch-authorized closure sealed for one attempt
// so tests can assert its argv byte-for-byte.
func authorizedClosure(t *testing.T, ledger *CompositionLedger, identity resultingress.AttemptIdentity) launchidentity.ClosureV1 {
	t.Helper()
	state, found, err := ledger.ingress.AttemptState(identity)
	if err != nil || !found {
		t.Fatalf("attempt state: found=%t err=%v", found, err)
	}
	closure, err := state.LaunchClosure.Closure()
	if err != nil {
		t.Fatalf("decode authorized closure: %v", err)
	}
	return closure
}

// TestPathBLaunchArgvBuilderReceivesPreciseReservedIdentity proves the
// injected builder is called with the exact reserved TaskID/RunID/AttemptID
// after ReserveAttempt and ensureAttemptLease, and that the reserved
// attempt/task/run identity reaches the prepared execution.
func TestPathBLaunchArgvBuilderReceivesPreciseReservedIdentity(t *testing.T) {
	inputs, runID, _, _, builder := pathBCompositionInputsForLaunch(t)
	ledger, err := NewCompositionLedger(context.Background(), inputs)
	if err != nil {
		t.Fatalf("composition ledger: %v", err)
	}
	if err := ledger.owner.claimRuntime(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ledger.Close() }()
	projection := pathBProjection(t, ledger)
	prepared, err := ledger.PrepareRunStart(context.Background(), ledger.owner, inputs.Acquisition, application.PrepareRunStartRequest{RunID: runID, ExpectedSequence: projection.Run.Sequence, ExpectedAuthorityHead: projection.Run.AuthorityHead})
	if err != nil {
		t.Fatalf("prepare run start: %v", err)
	}
	if len(builder.calls) != 1 {
		t.Fatalf("builder calls=%d, want 1", len(builder.calls))
	}
	resolved, err := ledger.ingress.ResolvePreparedExecution(context.Background(), ledger.owner, inputs.Acquisition, prepared.PreparationDigest)
	if err != nil {
		t.Fatalf("resolve prepared: %v", err)
	}
	// The builder received the precise reserved identity that reaches the
	// prepared execution: the exact task/run from the READY projection and
	// the creation-once reserved attempt id.
	want := AttemptLaunchIdentity{TaskID: resolved.AttemptIdentity.TaskID, RunID: resolved.AttemptIdentity.RunID, AttemptID: resolved.AttemptIdentity.AttemptID}
	if builder.calls[0] != want {
		t.Fatalf("builder identity=%+v, want %+v", builder.calls[0], want)
	}
	if builder.calls[0].TaskID != projection.Run.TaskID || builder.calls[0].RunID != projection.Run.RunID {
		t.Fatalf("builder task/run drifted from READY projection: got task=%s run=%s, want task=%s run=%s", builder.calls[0].TaskID, builder.calls[0].RunID, projection.Run.TaskID, projection.Run.RunID)
	}
}

// TestPathBLaunchArgvBuilderSealedIntoAuthorizedClosure proves the authorized
// closure's argv is exactly the builder's output, contains --mode json
// --print, and ends with the single trailing prompt.
func TestPathBLaunchArgvBuilderSealedIntoAuthorizedClosure(t *testing.T) {
	inputs, runID, _, _, builder := pathBCompositionInputsForLaunch(t)
	ledger, err := NewCompositionLedger(context.Background(), inputs)
	if err != nil {
		t.Fatalf("composition ledger: %v", err)
	}
	if err := ledger.owner.claimRuntime(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ledger.Close() }()
	projection := pathBProjection(t, ledger)
	prepared, err := ledger.PrepareRunStart(context.Background(), ledger.owner, inputs.Acquisition, application.PrepareRunStartRequest{RunID: runID, ExpectedSequence: projection.Run.Sequence, ExpectedAuthorityHead: projection.Run.AuthorityHead})
	if err != nil {
		t.Fatalf("prepare run start: %v", err)
	}
	resolved, err := ledger.ingress.ResolvePreparedExecution(context.Background(), ledger.owner, inputs.Acquisition, prepared.PreparationDigest)
	if err != nil {
		t.Fatalf("resolve prepared: %v", err)
	}
	closure := authorizedClosure(t, ledger, resolved.AttemptIdentity)
	wantArgv := testProductionArgv(builder.node, builder.entrypoint, builder.profile, builder.model, testProductionPrompt(resolved.AttemptIdentity.TaskID, resolved.AttemptIdentity.RunID, resolved.AttemptIdentity.AttemptID))
	if !slices.Equal(closure.Arguments, wantArgv) {
		t.Fatalf("authorized closure argv =\n%q\nwant builder output\n%q", closure.Arguments, wantArgv)
	}
	if !slices.Contains(closure.Arguments, "--mode") || !slices.Contains(closure.Arguments, "json") || !slices.Contains(closure.Arguments, "--print") {
		t.Fatalf("authorized closure argv missing --mode json --print: %q", closure.Arguments)
	}
	if closure.Arguments[len(closure.Arguments)-1] != testProductionPrompt(resolved.AttemptIdentity.TaskID, resolved.AttemptIdentity.RunID, resolved.AttemptIdentity.AttemptID) {
		t.Fatalf("authorized closure argv does not end with the prompt: %q", closure.Arguments[len(closure.Arguments)-1])
	}
}

// TestPathBLaunchArgvBuilderErrorFailsClosedBeforeAuthorization proves a
// builder error fails closed before any launch-authorized or
// prepared-execution-created fact is appended.
func TestPathBLaunchArgvBuilderErrorFailsClosedBeforeAuthorization(t *testing.T) {
	inputs, runID, _, base, builder := pathBCompositionInputsForLaunch(t)
	builder.err = errors.New("test builder unavailable")
	ledger, err := NewCompositionLedger(context.Background(), inputs)
	if err != nil {
		t.Fatalf("composition ledger: %v", err)
	}
	if err := ledger.owner.claimRuntime(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ledger.Close() }()
	projection := pathBProjection(t, ledger)
	_, err = ledger.PrepareRunStart(context.Background(), ledger.owner, inputs.Acquisition, application.PrepareRunStartRequest{RunID: runID, ExpectedSequence: projection.Run.Sequence, ExpectedAuthorityHead: projection.Run.AuthorityHead})
	if err == nil {
		t.Fatal("builder error was accepted")
	}
	ledgerBytes := pathBLedgerBytes(t, base)
	if strings.Contains(string(ledgerBytes), "launch-authorized") {
		t.Fatal("builder error appended a launch-authorized fact")
	}
	if strings.Contains(string(ledgerBytes), "prepared-execution-created") {
		t.Fatal("builder error appended a prepared-execution-created fact")
	}
}

// TestPathBReplaySealsSameClosureArgv proves a replay does not append sibling
// facts and the authorized closure's argv stays byte-identical to the fresh
// seal (the builder is deterministic).
func TestPathBReplaySealsSameClosureArgv(t *testing.T) {
	inputs, runID, _, base, _ := pathBCompositionInputsForLaunch(t)
	ledger, err := NewCompositionLedger(context.Background(), inputs)
	if err != nil {
		t.Fatalf("composition ledger: %v", err)
	}
	if err := ledger.owner.claimRuntime(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ledger.Close() }()
	projection := pathBProjection(t, ledger)
	request := application.PrepareRunStartRequest{RunID: runID, ExpectedSequence: projection.Run.Sequence, ExpectedAuthorityHead: projection.Run.AuthorityHead}
	first, err := ledger.PrepareRunStart(context.Background(), ledger.owner, inputs.Acquisition, request)
	if err != nil {
		t.Fatalf("first prepare: %v", err)
	}
	resolved, err := ledger.ingress.ResolvePreparedExecution(context.Background(), ledger.owner, inputs.Acquisition, first.PreparationDigest)
	if err != nil {
		t.Fatalf("resolve first: %v", err)
	}
	freshClosure := authorizedClosure(t, ledger, resolved.AttemptIdentity)
	before := pathBLedgerBytes(t, base)
	second, err := ledger.PrepareRunStart(context.Background(), ledger.owner, inputs.Acquisition, request)
	if err != nil {
		t.Fatalf("replay prepare: %v", err)
	}
	if second != first {
		t.Fatalf("replay drifted: first=%+v second=%+v", first, second)
	}
	if after := pathBLedgerBytes(t, base); string(before) != string(after) {
		t.Fatalf("replay appended new RB1 facts: before=%d bytes after=%d bytes", len(before), len(after))
	}
	replayClosure := authorizedClosure(t, ledger, resolved.AttemptIdentity)
	if !slices.Equal(replayClosure.Arguments, freshClosure.Arguments) {
		t.Fatalf("replay closure argv drifted from fresh seal")
	}
}

// TestPathBNilLaunchArgvBuilderFailsClosed proves path B rejects a nil argv
// builder at composition time so it can never launch with the bare
// composition-time kernel argv.
func TestPathBNilLaunchArgvBuilderFailsClosed(t *testing.T) {
	inputs, _, _, _ := pathBCompositionInputs(t)
	inputs.LaunchArgvBuilder = nil
	if _, err := NewCompositionLedger(context.Background(), inputs); err == nil {
		t.Fatal("path B accepted a nil LaunchArgvBuilder")
	}
}

// TestPathALaunchArgvBuilderSealedIntoAuthorizedClosure proves path A with an
// injected builder also re-seals the production argv into the authorized
// closure (fresh and replay are byte-identical).
func TestPathALaunchArgvBuilderSealedIntoAuthorizedClosure(t *testing.T) {
	inputs, runID, _, _, builder := pathBCompositionInputsForLaunch(t)
	// Clear path B inputs so the ledger falls back to staging provision
	// (path A) while keeping the injected builder.
	inputs.ExistingWorktreeDescriptorGraph = allocationcontrol.ExistingWorktreeDescriptorGraphV1{}
	inputs.ExistingWorktreeTargetWorktree = nil
	pathALease, err := inputs.Runs.AcquireExisting(runID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pathALease.Release() })
	inputs.RunLease = pathALease
	ledger, err := NewCompositionLedger(context.Background(), inputs)
	if err != nil {
		t.Fatalf("composition ledger: %v", err)
	}
	if err := ledger.owner.claimRuntime(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ledger.Close() }()
	projection := pathBProjection(t, ledger)
	prepared, err := ledger.PrepareRunStart(context.Background(), ledger.owner, inputs.Acquisition, application.PrepareRunStartRequest{RunID: runID, ExpectedSequence: projection.Run.Sequence, ExpectedAuthorityHead: projection.Run.AuthorityHead})
	if err != nil {
		t.Fatalf("path A prepare: %v", err)
	}
	resolved, err := ledger.ingress.ResolvePreparedExecution(context.Background(), ledger.owner, inputs.Acquisition, prepared.PreparationDigest)
	if err != nil {
		t.Fatalf("path A resolve: %v", err)
	}
	closure := authorizedClosure(t, ledger, resolved.AttemptIdentity)
	wantArgv := testProductionArgv(builder.node, builder.entrypoint, builder.profile, builder.model, testProductionPrompt(resolved.AttemptIdentity.TaskID, resolved.AttemptIdentity.RunID, resolved.AttemptIdentity.AttemptID))
	if !slices.Equal(closure.Arguments, wantArgv) {
		t.Fatalf("path A authorized closure argv =\n%q\nwant builder output\n%q", closure.Arguments, wantArgv)
	}
	// Replay must seal the same closure argv.
	replay, err := ledger.PrepareRunStart(context.Background(), ledger.owner, inputs.Acquisition, application.PrepareRunStartRequest{RunID: runID, ExpectedSequence: projection.Run.Sequence, ExpectedAuthorityHead: projection.Run.AuthorityHead})
	if err != nil {
		t.Fatalf("path A replay: %v", err)
	}
	if replay != prepared {
		t.Fatalf("path A replay drifted")
	}
	replayClosure := authorizedClosure(t, ledger, resolved.AttemptIdentity)
	if !slices.Equal(replayClosure.Arguments, closure.Arguments) {
		t.Fatalf("path A replay closure argv drifted from fresh seal")
	}
}

// TestPathANilLaunchArgvBuilderKeepsCompositionArgv proves path A tolerates a
// nil builder: the authorized closure keeps the composition-time kernel argv
// (only the working directory is re-sealed), preserving the legacy behavior.
func TestPathANilLaunchArgvBuilderKeepsCompositionArgv(t *testing.T) {
	inputs, runID, _, _ := pathBCompositionInputs(t)
	inputs.ExistingWorktreeDescriptorGraph = allocationcontrol.ExistingWorktreeDescriptorGraphV1{}
	inputs.ExistingWorktreeTargetWorktree = nil
	inputs.LaunchArgvBuilder = nil
	pathALease, err := inputs.Runs.AcquireExisting(runID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pathALease.Release() })
	inputs.RunLease = pathALease
	compositionArgv := append([]string(nil), inputs.LaunchClosure.Arguments...)
	ledger, err := NewCompositionLedger(context.Background(), inputs)
	if err != nil {
		t.Fatalf("composition ledger: %v", err)
	}
	if err := ledger.owner.claimRuntime(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ledger.Close() }()
	projection := pathBProjection(t, ledger)
	prepared, err := ledger.PrepareRunStart(context.Background(), ledger.owner, inputs.Acquisition, application.PrepareRunStartRequest{RunID: runID, ExpectedSequence: projection.Run.Sequence, ExpectedAuthorityHead: projection.Run.AuthorityHead})
	if err != nil {
		t.Fatalf("path A nil-builder prepare: %v", err)
	}
	resolved, err := ledger.ingress.ResolvePreparedExecution(context.Background(), ledger.owner, inputs.Acquisition, prepared.PreparationDigest)
	if err != nil {
		t.Fatalf("path A nil-builder resolve: %v", err)
	}
	closure := authorizedClosure(t, ledger, resolved.AttemptIdentity)
	if !slices.Equal(closure.Arguments, compositionArgv) {
		t.Fatalf("path A nil-builder argv drifted from composition-time argv: got %q want %q", closure.Arguments, compositionArgv)
	}
}
