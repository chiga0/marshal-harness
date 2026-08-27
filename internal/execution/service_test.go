package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/dispatch"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/gitworktree"
	"github.com/chiga0/marshal-harness/internal/lifecycle"
	"github.com/chiga0/marshal-harness/internal/port"
	"github.com/chiga0/marshal-harness/internal/provider"
	marshalrepo "github.com/chiga0/marshal-harness/internal/repository"
	"github.com/chiga0/marshal-harness/internal/runstore"
	"github.com/chiga0/marshal-harness/internal/selfidentity"
)

type fixtureAdapter struct {
	id          string
	capability  []byte
	fail        bool
	failure     error
	breakGit    bool
	badIdentity bool
}

func (a *fixtureAdapter) ID() string {
	if a.id != "" {
		return a.id
	}
	return "fixture"
}
func (a *fixtureAdapter) Probe(context.Context) (domain.Record, error) {
	return domain.Record{Kind: domain.KindCapabilitySnapshot, Data: a.capability}, nil
}
func (a *fixtureAdapter) Run(_ context.Context, request domain.Record) (domain.Record, error) {
	if a.failure != nil {
		return domain.Record{}, a.failure
	}
	if a.fail {
		return domain.Record{}, os.ErrDeadlineExceeded
	}
	var input struct{ TaskID, RunID, AttemptID, WorktreePath string }
	if err := json.Unmarshal(request.Data, &input); err != nil {
		return domain.Record{}, err
	}
	if err := os.WriteFile(filepath.Join(input.WorktreePath, "change.txt"), []byte("worker change\n"), 0o600); err != nil {
		return domain.Record{}, err
	}
	if a.breakGit {
		if err := os.Rename(filepath.Join(input.WorktreePath, ".git"), filepath.Join(input.WorktreePath, ".git.broken")); err != nil {
			return domain.Record{}, err
		}
	}
	result := map[string]any{
		"apiVersion": "marshal.dev/v1alpha1", "kind": "WorkerResult", "taskId": input.TaskID, "runId": input.RunID, "attemptId": input.AttemptID,
		"adapter": map[string]any{"id": a.ID(), "executable": "/fixture", "version": "1"}, "status": "completed", "summary": "done",
		"declaredChangedFiles": []string{"change.txt"}, "declaredArtifacts": []any{}, "declaredCommands": []any{}, "declaredRisks": []string{},
		"startedAt": "2026-08-04T00:00:00Z", "completedAt": "2026-08-04T00:00:01Z",
	}
	if a.badIdentity {
		result["attemptId"] = "attempt-other"
	}
	data, err := json.Marshal(result)
	return domain.Record{Kind: domain.KindWorkerResult, Data: data}, err
}

func TestRunPersistsAttemptAndRequiresIndependentVerification(t *testing.T) {
	fixture := newExecutionFixture(t, false)
	result, err := Run(context.Background(), fixture.input)
	if err != nil {
		t.Fatal(err)
	}
	if result.State.State != domain.StateVerifying || result.State.AttemptsUsed != 1 {
		t.Fatalf("state = %+v", result.State)
	}
	attempt := filepath.Join(fixture.runDir, "attempts", result.AttemptID)
	for _, path := range []string{"worker-request.json", "worker-result.json", "worktree-snapshot.json", "control/input/task-spec.json", "control/input/prompt.md"} {
		if _, err := os.Stat(filepath.Join(attempt, path)); err != nil {
			t.Fatalf("missing %s: %v", path, err)
		}
	}
	legacyRequest, err := os.ReadFile(filepath.Join(attempt, "worker-request.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(legacyRequest, []byte("localSelfIdentityBinding")) {
		t.Fatalf("non-local WorkerRequest gained local lineage: %s", legacyRequest)
	}
	events, _, err := runstore.New(fixture.input.StateRoot).ReadEvents(fixture.input.RunID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events[len(events)-2:] {
		if _, present := event.Payload["dispatchObservationDigest"]; present {
			t.Fatalf("non-local event gained local dispatch digest: %+v", event)
		}
	}
	if _, err := os.Stat(filepath.Join(fixture.repository, "change.txt")); !os.IsNotExist(err) {
		t.Fatalf("worker edit leaked into main checkout: %v", err)
	}
}

func TestLocalSelfIdentityAttemptLineagePositive(t *testing.T) {
	fixture := newExecutionFixture(t, false)
	observations := bindLocalSelfIdentityFixture(t, &fixture, "activation-a")
	result, err := Run(context.Background(), fixture.input)
	if err != nil || result.State.State != domain.StateVerifying {
		t.Fatalf("local Run = %+v err=%v", result, err)
	}
	attemptDir := filepath.Join(fixture.runDir, "attempts", result.AttemptID)
	for _, name := range []string{"local-self-identity-dispatch.json", "local-self-identity-ingress.json"} {
		raw, err := os.ReadFile(filepath.Join(attemptDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := selfidentity.DecodeObservation(raw); err != nil {
			t.Fatalf("%s is not exact JCS: %v", name, err)
		}
	}
	requestRaw, err := os.ReadFile(filepath.Join(attemptDir, "worker-request.json"))
	if err != nil {
		t.Fatal(err)
	}
	var request struct {
		Binding *selfidentity.LocalSelfIdentityBindingV1 `json:"localSelfIdentityBinding"`
	}
	if err := json.Unmarshal(requestRaw, &request); err != nil || request.Binding == nil || request.Binding.DispatchObservationDigest != observations[2].ObservationDigest {
		t.Fatalf("request binding = %+v err=%v", request.Binding, err)
	}
	events, _, err := runstore.New(fixture.input.StateRoot).ReadEvents(fixture.input.RunID)
	if err != nil {
		t.Fatal(err)
	}
	started, completed := events[len(events)-2], events[len(events)-1]
	if payloadString(started.Payload, "dispatchObservationDigest") != observations[2].ObservationDigest ||
		payloadString(completed.Payload, "dispatchObservationDigest") != observations[2].ObservationDigest ||
		payloadString(completed.Payload, "ingressObservationDigest") != observations[3].ObservationDigest {
		t.Fatalf("local event lineage: started=%+v completed=%+v", started.Payload, completed.Payload)
	}
}

func TestLocalSelfIdentityFailsBeforeProbeWithoutFrozenPolicyBinding(t *testing.T) {
	fixture := newExecutionFixture(t, false)
	entry := localTestObservation(t, "activation-a", time.Unix(10, 0).UTC())
	fixture.input.EntryLocalSelfIdentity = &entry
	fixture.input.ObserveLocalSelfIdentity = func() (selfidentity.LocalSelfIdentityObservationV1, error) { return entry, nil }
	requireFailsBeforeProbe(t, fixture, selfidentity.ReasonObjectMismatch)
}

func TestLocalSelfIdentityIngressDriftIsCoreEvidenceFailure(t *testing.T) {
	fixture := newExecutionFixture(t, false)
	bindLocalSelfIdentityFixture(t, &fixture, "activation-a")
	calls := 0
	fixture.input.ObserveLocalSelfIdentity = func() (selfidentity.LocalSelfIdentityObservationV1, error) {
		calls++
		activation := "activation-a"
		if calls == 3 {
			activation = "activation-b"
		}
		return localTestObservation(t, activation, time.Unix(int64(10+calls), 0).UTC()), nil
	}
	result, err := Run(context.Background(), fixture.input)
	if err == nil || result.State.State != domain.StateBlocked {
		t.Fatalf("identity drift = %+v err=%v", result, err)
	}
	events, _, readErr := runstore.New(fixture.input.StateRoot).ReadEvents(fixture.input.RunID)
	if readErr != nil {
		t.Fatal(readErr)
	}
	last := events[len(events)-1]
	if last.Type != "worker.evidence-failed" || last.Payload["failureDomain"] != "marshal-self-identity" || last.Payload["workerFault"] != false || last.Payload["reworkEligible"] != false {
		t.Fatalf("structural event = %+v", last)
	}
	attemptDir := filepath.Join(fixture.runDir, "attempts", result.AttemptID)
	if _, err := os.Stat(filepath.Join(attemptDir, "worker-result.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected result entered evidence: %v", err)
	}
	if _, err := os.Stat(filepath.Join(attemptDir, "diagnostics", "quarantined-local-self-identity-worker-result.json")); err != nil {
		t.Fatalf("rejected result was not quarantined: %v", err)
	}
}

func TestLocalSelfIdentityDispatchCrashLeavesNoAttemptAuthority(t *testing.T) {
	fixture := newExecutionFixture(t, false)
	bindLocalSelfIdentityFixture(t, &fixture, "activation-a")
	adapter := &countingAdapter{delegate: fixture.input.Adapter.(*fixtureAdapter)}
	fixture.input.Adapter = adapter
	injected := errors.New("secret=/Users/private/dispatch-observation")
	fixture.input.AfterLocalDispatchObservation = func(string) error { return injected }
	if _, err := Run(context.Background(), fixture.input); err == nil {
		t.Fatal("dispatch observation crash was accepted")
	} else if err.Error() != selfidentity.ReasonObjectMismatch || !errors.Is(err, injected) {
		t.Fatalf("dispatch observation failure is not typed and closed: %q", err)
	}
	if adapter.probes != 0 || adapter.runs != 0 {
		t.Fatalf("crash crossed Adapter boundary: probes=%d runs=%d", adapter.probes, adapter.runs)
	}
	entries, err := os.ReadDir(filepath.Join(fixture.runDir, "attempts"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("attempt diagnostic dirs = %d err=%v", len(entries), err)
	}
	orphan := filepath.Join(fixture.runDir, "attempts", entries[0].Name())
	if number, profile := attemptIdentity(orphan); number != 0 || profile != "" {
		t.Fatalf("observation-only directory became Attempt authority: number=%d profile=%q", number, profile)
	}
	fixture.input.AfterLocalDispatchObservation = nil
	result, err := Run(context.Background(), fixture.input)
	if err != nil || result.State.State != domain.StateVerifying || adapter.runs != 1 {
		t.Fatalf("fresh attempt after orphan diagnostic = %+v err=%v runs=%d", result, err, adapter.runs)
	}
}

func TestLocalSelfIdentityRejectsPersistedLineageReplayAndSymlink(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{
			name: "dispatch observation replay",
			mutate: func(t *testing.T, attemptDir string) {
				replayed := localTestObservation(t, "activation-replayed", time.Unix(99, 0).UTC())
				raw, err := json.Marshal(replayed)
				if err != nil {
					t.Fatal(err)
				}
				raw, err = canonical.JSON(raw)
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(attemptDir, "local-self-identity-dispatch.json")
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, raw, 0o400); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "WorkerRequest symlink substitution",
			mutate: func(t *testing.T, attemptDir string) {
				path := filepath.Join(attemptDir, "worker-request.json")
				backup := path + ".replayed"
				if err := os.Rename(path, backup); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(backup, path); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newExecutionFixture(t, false)
			bindLocalSelfIdentityFixture(t, &fixture, "activation-a")
			fixture.input.BeforeLocalResultIngress = func(attemptDir string) error {
				test.mutate(t, attemptDir)
				return nil
			}
			result, err := Run(context.Background(), fixture.input)
			if err == nil || result.State.State != domain.StateBlocked {
				t.Fatalf("persisted lineage attack = %+v err=%v", result, err)
			}
			events, _, readErr := runstore.New(fixture.input.StateRoot).ReadEvents(fixture.input.RunID)
			if readErr != nil {
				t.Fatal(readErr)
			}
			last := events[len(events)-1]
			if last.Type != "worker.evidence-failed" || last.Payload["failureDomain"] != "marshal-self-identity" {
				t.Fatalf("attack terminal event = %+v", last)
			}
		})
	}
}

func TestLocalSelfIdentityRejectsPersistedIngressReplacementSymlinkReplayAndABA(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{
			name: "write after install",
			mutate: func(t *testing.T, path string) {
				if err := os.Chmod(path, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(`{}`), 0o400); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "symlink replacement",
			mutate: func(t *testing.T, path string) {
				backup := path + ".original"
				if err := os.Rename(path, backup); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(backup, path); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "replayed observation",
			mutate: func(t *testing.T, path string) {
				replayed := localTestObservation(t, "activation-replayed", time.Unix(99, 0).UTC())
				raw, err := json.Marshal(replayed)
				if err == nil {
					raw, err = canonical.JSON(raw)
				}
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, raw, 0o400); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "same bytes ABA replacement",
			mutate: func(t *testing.T, path string) {
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, raw, 0o400); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newExecutionFixture(t, false)
			bindLocalSelfIdentityFixture(t, &fixture, "activation-a")
			fixture.input.AfterLocalIngressObservation = func(attemptDir string) error {
				test.mutate(t, filepath.Join(attemptDir, "local-self-identity-ingress.json"))
				return nil
			}
			result, err := Run(context.Background(), fixture.input)
			if err == nil || result.State.State != domain.StateBlocked {
				t.Fatalf("ingress object attack = %+v err=%v", result, err)
			}
			assertLocalSelfIdentityTerminal(t, fixture, result.AttemptID)
		})
	}
}

func TestLocalSelfIdentityDriftOutranksRetryableAdapterFailure(t *testing.T) {
	fixture := newTypedTransientFailureFixture(t, executionFixtureOptions{maxAttempts: 3, maxOperationalRetries: 2})
	observations := bindLocalSelfIdentityFixture(t, &fixture, "activation-a")
	calls := 0
	fixture.input.ObserveLocalSelfIdentity = func() (selfidentity.LocalSelfIdentityObservationV1, error) {
		calls++
		if calls == 3 {
			return localTestObservation(t, "activation-drift", time.Unix(13, 0).UTC()), nil
		}
		return observations[calls], nil
	}
	result, err := Run(context.Background(), fixture.input)
	if err == nil || result.State.State != domain.StateBlocked || result.State.OperationalRetriesUsed != 0 {
		t.Fatalf("identity drift did not outrank retryable Adapter error: result=%+v err=%v", result, err)
	}
	assertLocalSelfIdentityTerminal(t, fixture, result.AttemptID)
}

func TestLocalSelfIdentityTerminalTransactionCompensatesCrashesAndQuarantineFailure(t *testing.T) {
	const injectedSecret = "secret=/Users/private/token"
	for _, test := range []struct {
		name   string
		inject func(*testing.T, *executionFixture)
	}{
		{
			name: "journal durable before Outcome commit",
			inject: func(_ *testing.T, fixture *executionFixture) {
				fixture.input.AfterWorkerTerminalAppend = func() error { return errors.New(injectedSecret) }
			},
		},
		{
			name: "Outcome durable before snapshot",
			inject: func(_ *testing.T, fixture *executionFixture) {
				fixture.input.AfterLocalIdentityOutcomeCommit = func() error { return errors.New(injectedSecret) }
			},
		},
		{
			name: "diagnostic quarantine failure",
			inject: func(t *testing.T, fixture *executionFixture) {
				fixture.input.BeforeLocalResultIngress = func(attemptDir string) error {
					if err := os.WriteFile(filepath.Join(attemptDir, "diagnostics"), []byte(injectedSecret), 0o600); err != nil {
						t.Fatal(err)
					}
					return errors.New(injectedSecret)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newExecutionFixture(t, false)
			bindLocalSelfIdentityFixture(t, &fixture, "activation-a")
			if test.name != "diagnostic quarantine failure" {
				fixture.input.AfterLocalIngressObservation = func(string) error { return errors.New(injectedSecret) }
			}
			test.inject(t, &fixture)
			first, err := Run(context.Background(), fixture.input)
			if err == nil || first.State.State != domain.StateBlocked || strings.Contains(err.Error(), injectedSecret) {
				t.Fatalf("first terminal result=%+v err=%v", first, err)
			}
			fixture.input.AfterWorkerTerminalAppend = nil
			fixture.input.AfterLocalIdentityOutcomeCommit = nil
			fixture.input.BeforeLocalResultIngress = nil
			fixture.input.AfterLocalIngressObservation = nil
			second, secondErr := Run(context.Background(), fixture.input)
			if secondErr == nil || second.State.State != domain.StateBlocked {
				t.Fatalf("re-entry did not converge: result=%+v err=%v", second, secondErr)
			}
			assertLocalSelfIdentityTerminal(t, fixture, first.AttemptID)
			for _, name := range []string{"events.jsonl", "outcome.json", "outcome.md"} {
				raw, readErr := os.ReadFile(filepath.Join(fixture.runDir, name))
				if readErr != nil || bytes.Contains(raw, []byte(injectedSecret)) {
					t.Fatalf("%s missing or leaked injected cause: err=%v data=%s", name, readErr, raw)
				}
			}
		})
	}
}

func assertLocalSelfIdentityTerminal(t *testing.T, fixture executionFixture, attemptID string) {
	t.Helper()
	events, _, err := runstore.New(fixture.input.StateRoot).ReadEvents(fixture.input.RunID)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range events {
		if event.Type == "worker.evidence-failed" {
			count++
			if event.AttemptID != attemptID || payloadString(event.Payload, "reasonCode") != selfidentity.ReasonCrossProfileEvidence ||
				event.Payload["workerFault"] != false || event.Payload["reworkEligible"] != false {
				t.Fatalf("invalid local terminal event: %+v", event)
			}
		}
	}
	if count != 1 {
		t.Fatalf("worker.evidence-failed count=%d, want 1", count)
	}
}

func TestRunClassifiesOperationalFailureAndConsumesRetryBudget(t *testing.T) {
	fixture := newTypedTransientFailureFixture(t, executionFixtureOptions{})
	result, err := Run(context.Background(), fixture.input)
	if err == nil {
		t.Fatal("worker failure was accepted")
	}
	if result.State.State != domain.StateRetryPending || result.State.OperationalRetriesUsed != 1 {
		t.Fatalf("state = %+v", result.State)
	}
}

func TestRunUntypedAdapterFailureFailsClosedWithoutRetry(t *testing.T) {
	const rawCause = "provider secret=do-not-persist path=/Users/private/credential"
	for _, test := range []struct {
		name     string
		cause    error
		wantKind port.FailureKind
	}{
		{name: "plain failure is a protocol violation", cause: errors.New(rawCause), wantKind: port.FailureKindProtocolInvalid},
		{name: "permanent compatibility remains terminal", cause: port.Permanent(errors.New(rawCause)), wantKind: port.FailureKindProviderTerminal},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newExecutionFixtureWithOptions(t, false, executionFixtureOptions{
				preferredAdapter: "fake", fallbackAdapters: []string{}, capabilityAdapterID: "fake",
				maxAttempts: 3, maxOperationalRetries: 2,
			})
			delegate := fixture.input.Adapter.(*fixtureAdapter)
			delegate.id = "fake"
			delegate.failure = test.cause
			adapter := &countingAdapter{delegate: delegate}
			fixture.input.Adapter = adapter

			result, err := Run(context.Background(), fixture.input)
			if err == nil || result.State.State != domain.StateBlocked || result.State.AttemptsUsed != 1 || result.State.OperationalRetriesUsed != 0 {
				t.Fatalf("untyped failure result = %+v err=%v", result, err)
			}
			if adapter.probes != 1 || adapter.runs != 1 {
				t.Fatalf("first failure calls = probes:%d runs:%d, want one each", adapter.probes, adapter.runs)
			}
			if strings.Contains(err.Error(), rawCause) {
				t.Fatalf("returned error leaked raw Adapter cause: %q", err)
			}

			events, _, readErr := runstore.New(fixture.input.StateRoot).ReadEvents(fixture.input.RunID)
			if readErr != nil {
				t.Fatal(readErr)
			}
			last := events[len(events)-1]
			wantFailure, failureErr := port.NewAdapterFailure(port.AdapterIDFake, test.wantKind, port.RetryDispositionDoNotRetry, nil, nil, last.Timestamp)
			if failureErr != nil {
				t.Fatal(failureErr)
			}
			if last.Type != "worker.failed" || last.StateTo != domain.StateBlocked || last.Payload["failureKind"] != string(test.wantKind) || last.Payload["retryDisposition"] != string(port.RetryDispositionDoNotRetry) || last.Payload["error"] != wantFailure.Error() {
				t.Fatalf("terminal failure event = %+v", last)
			}
			if signature, _ := last.Payload["failureSignature"].(string); !isCanonicalSHA256(signature) {
				t.Fatalf("failureSignature = %q", signature)
			}

			journal, journalErr := os.ReadFile(filepath.Join(fixture.runDir, "events.jsonl"))
			outcomeJSON, outcomeErr := os.ReadFile(filepath.Join(fixture.runDir, "outcome.json"))
			outcomeMarkdown, markdownErr := os.ReadFile(filepath.Join(fixture.runDir, "outcome.md"))
			if journalErr != nil || outcomeErr != nil || markdownErr != nil {
				t.Fatalf("read terminal evidence: journal=%v outcome=%v markdown=%v", journalErr, outcomeErr, markdownErr)
			}
			for name, evidence := range map[string][]byte{"journal": journal, "outcome.json": outcomeJSON, "outcome.md": outcomeMarkdown} {
				if bytes.Contains(evidence, []byte(rawCause)) {
					t.Fatalf("%s leaked raw Adapter cause", name)
				}
			}

			beforeProbes, beforeRuns := adapter.probes, adapter.runs
			restarted, restartErr := Run(context.Background(), fixture.input)
			if restartErr == nil || restarted.State.State != domain.StateBlocked || restarted.State.TerminalReason != "adapter-"+string(test.wantKind) {
				t.Fatalf("terminal restart = %+v err=%v", restarted, restartErr)
			}
			if adapter.probes != beforeProbes || adapter.runs != beforeRuns {
				t.Fatalf("terminal restart crossed Adapter boundary: probes=%d runs=%d", adapter.probes, adapter.runs)
			}
		})
	}
}

func TestRunConsumesTypedAdapterFailureDisposition(t *testing.T) {
	now := time.Now().UTC()
	tests := []struct {
		name              string
		kind              port.FailureKind
		disposition       port.RetryDisposition
		wantState         domain.State
		wantRetries       uint
		wantTerminal      string
		wantSecondRunCall bool
	}{
		{name: "transient transport", kind: port.FailureKindConnectionFailure, disposition: port.RetryDispositionRetryable, wantState: domain.StateRetryPending, wantRetries: 1, wantSecondRunCall: true},
		{name: "protocol structural", kind: port.FailureKindProtocolInvalid, disposition: port.RetryDispositionDoNotRetry, wantState: domain.StateBlocked, wantTerminal: "adapter-protocol-invalid", wantSecondRunCall: false},
		{name: "quota blocked", kind: port.FailureKindQuotaExhausted, disposition: port.RetryDispositionBlocked, wantState: domain.StateBlocked, wantTerminal: "adapter-quota-exhausted", wantSecondRunCall: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newExecutionFixtureWithOptions(t, false, executionFixtureOptions{
				preferredAdapter: "fake", fallbackAdapters: []string{}, capabilityAdapterID: "fake",
				maxAttempts: 3, maxOperationalRetries: 2,
			})
			fixture.input.Adapter.(*fixtureAdapter).id = "fake"
			failure, err := port.NewAdapterFailure(port.AdapterIDFake, test.kind, test.disposition, nil, nil, now)
			if err != nil {
				t.Fatal(err)
			}
			fixture.input.Adapter.(*fixtureAdapter).failure = failure
			adapter := &countingAdapter{delegate: fixture.input.Adapter.(*fixtureAdapter)}
			fixture.input.Adapter = adapter

			result, err := Run(context.Background(), fixture.input)
			if err == nil {
				t.Fatal("typed adapter failure was accepted")
			}
			if result.State.State != test.wantState || result.State.OperationalRetriesUsed != test.wantRetries {
				t.Fatalf("state = %+v", result.State)
			}
			if adapter.runs != 1 {
				t.Fatalf("worker calls = %d, want one", adapter.runs)
			}
			events, _, readErr := runstore.New(fixture.input.StateRoot).ReadEvents(fixture.input.RunID)
			if readErr != nil {
				t.Fatal(readErr)
			}
			last := events[len(events)-1]
			if last.Type != "worker.failed" || last.Payload["failureKind"] != string(test.kind) || last.Payload["retryDisposition"] != string(test.disposition) {
				t.Fatalf("typed failure event = %+v", last)
			}
			signature, _ := last.Payload["failureSignature"].(string)
			if !strings.HasPrefix(signature, "sha256:") || len(signature) != len("sha256:")+64 {
				t.Fatalf("failureSignature = %q", signature)
			}

			beforeCalls := adapter.runs
			second, secondErr := Run(context.Background(), fixture.input)
			if secondErr == nil {
				t.Fatal("second run unexpectedly succeeded")
			}
			if test.wantSecondRunCall {
				if adapter.runs != beforeCalls+1 {
					t.Fatalf("retryable failure did not start the bounded next attempt: calls=%d", adapter.runs)
				}
			} else {
				if adapter.runs != beforeCalls {
					t.Fatalf("structural failure started another worker: calls=%d", adapter.runs)
				}
				if second.State.State != domain.StateBlocked || second.State.TerminalReason != test.wantTerminal {
					t.Fatalf("terminal replay = %+v err=%v", second, secondErr)
				}
				if _, statErr := os.Stat(filepath.Join(fixture.runDir, "outcome.json")); statErr != nil {
					t.Fatalf("typed terminal Outcome missing: %v", statErr)
				}
			}
		})
	}
}

func TestRetryPendingAdmissionHonorsTypedRetryGate(t *testing.T) {
	t.Run("future retry-after is a zero-side-effect hold", func(t *testing.T) {
		retryAfter := time.Hour
		fixture := setupPersistedTypedRetryFailure(t, &retryAfter, nil)
		requireFailsBeforeProbe(t, fixture, "retry admission", "not ready")
		requireFailsBeforeProbe(t, fixture, "retry admission", "not ready")
	})

	t.Run("future not-before is a zero-side-effect hold", func(t *testing.T) {
		notBefore := time.Now().UTC().Add(time.Hour).Truncate(time.Second).Add(123456789 * time.Nanosecond)
		fixture := setupPersistedTypedRetryFailure(t, nil, &notBefore)
		events, _, err := runstore.New(fixture.input.StateRoot).ReadEvents(fixture.input.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if got := payloadString(events[len(events)-1].Payload, "notBefore"); got != notBefore.Format(time.RFC3339Nano) {
			t.Fatalf("persisted notBefore = %q, want lossless %q", got, notBefore.Format(time.RFC3339Nano))
		}
		requireFailsBeforeProbe(t, fixture, "retry admission", "not ready")
		requireFailsBeforeProbe(t, fixture, "retry admission", "not ready")
	})

	for _, test := range []struct {
		name       string
		retryAfter *time.Duration
	}{
		{name: "no hint retries immediately"},
		{name: "elapsed retry-after retries once", retryAfter: durationPointer(time.Nanosecond)},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := setupPersistedTypedRetryFailure(t, test.retryAfter, nil)
			before, _, err := runstore.New(fixture.input.StateRoot).ReadEvents(fixture.input.RunID)
			if err != nil {
				t.Fatal(err)
			}
			oldFailure := before[len(before)-1]
			adapter := &countingAdapter{delegate: fixture.input.Adapter.(*fixtureAdapter)}
			input := fixture.input
			input.Adapter = adapter

			result, err := Run(context.Background(), input)
			if err != nil || result.State.State != domain.StateVerifying {
				t.Fatalf("retry result = %+v err=%v", result, err)
			}
			if adapter.probes != 1 || adapter.runs != 1 {
				t.Fatalf("retry adapter calls = probes:%d runs:%d, want exactly one each", adapter.probes, adapter.runs)
			}
			if result.State.AttemptsUsed != 2 || result.State.OperationalRetriesUsed != 1 || result.AttemptID == oldFailure.AttemptID {
				t.Fatalf("retry counters/identity = %+v oldAttempt=%s", result.State, oldFailure.AttemptID)
			}
			after, _, err := runstore.New(fixture.input.StateRoot).ReadEvents(fixture.input.RunID)
			if err != nil {
				t.Fatal(err)
			}
			var preserved *domain.RunEvent
			for index := range after {
				if after[index].EventID == oldFailure.EventID {
					preserved = &after[index]
					break
				}
			}
			if preserved == nil || !reflect.DeepEqual(*preserved, oldFailure) {
				t.Fatalf("prior typed failure evidence changed: before=%+v after=%+v", oldFailure, preserved)
			}
		})
	}

	t.Run("elapsed not-before retries once", func(t *testing.T) {
		fixture := setupHistoricalNotBeforeRetryFailure(t)
		oldState := inspectState(t, fixture)
		adapter := &countingAdapter{delegate: fixture.input.Adapter.(*fixtureAdapter)}
		input := fixture.input
		input.Adapter = adapter
		result, err := Run(context.Background(), input)
		if err != nil || result.State.State != domain.StateVerifying {
			t.Fatalf("historical not-before retry = %+v err=%v", result, err)
		}
		if adapter.probes != 1 || adapter.runs != 1 || result.State.AttemptsUsed != oldState.AttemptsUsed+1 || result.State.OperationalRetriesUsed != oldState.OperationalRetriesUsed {
			t.Fatalf("historical not-before retry calls/state = probes:%d runs:%d old=%+v new=%+v", adapter.probes, adapter.runs, oldState, result.State)
		}
	})
}

func TestRetryPendingAdmissionRejectsMalformedOrNonRetryableAuthority(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*domain.RunEvent, domain.RunState)
		fragments []string
	}{
		{
			name: "blocked disposition",
			mutate: func(event *domain.RunEvent, state domain.RunState) {
				setPersistedFailurePayload(t, event, state, port.AdapterIDFake, port.FailureKindQuotaExhausted, port.RetryDispositionBlocked, nil, nil)
			},
			fragments: []string{"retry admission", "not retryable"},
		},
		{
			name: "do-not-retry disposition",
			mutate: func(event *domain.RunEvent, state domain.RunState) {
				setPersistedFailurePayload(t, event, state, port.AdapterIDFake, port.FailureKindProtocolInvalid, port.RetryDispositionDoNotRetry, nil, nil)
			},
			fragments: []string{"retry admission", "not retryable"},
		},
		{
			name: "legacy untyped record",
			mutate: func(event *domain.RunEvent, _ domain.RunState) {
				for _, key := range []string{"adapterId", "failureKind", "retryDisposition", "failureSignature"} {
					delete(event.Payload, key)
				}
				event.Payload["error"] = "legacy failure"
			},
			fragments: []string{"retry admission", "typed failure"},
		},
		{
			name: "unknown kind",
			mutate: func(event *domain.RunEvent, _ domain.RunState) {
				event.Payload["failureKind"] = "provider-secret"
			},
			fragments: []string{"retry admission", "unknown failure kind"},
		},
		{
			name: "unknown disposition",
			mutate: func(event *domain.RunEvent, _ domain.RunState) {
				event.Payload["retryDisposition"] = "guess"
			},
			fragments: []string{"retry admission", "disposition"},
		},
		{
			name: "illegal kind disposition pairing",
			mutate: func(event *domain.RunEvent, _ domain.RunState) {
				event.Payload["retryDisposition"] = string(port.RetryDispositionDoNotRetry)
			},
			fragments: []string{"retry admission", "disposition"},
		},
		{
			name: "adapter mismatch",
			mutate: func(event *domain.RunEvent, state domain.RunState) {
				setPersistedFailurePayload(t, event, state, port.AdapterIDQwen, port.FailureKindConnectionFailure, port.RetryDispositionRetryable, nil, nil)
			},
			fragments: []string{"retry admission", "adapter"},
		},
		{
			name: "signature mismatch",
			mutate: func(event *domain.RunEvent, _ domain.RunState) {
				event.Payload["failureSignature"] = "sha256:" + strings.Repeat("0", 64)
			},
			fragments: []string{"retry admission", "signature"},
		},
		{
			name: "negative retry-after",
			mutate: func(event *domain.RunEvent, _ domain.RunState) {
				event.Payload["retryAfterNanoseconds"] = float64(-1)
			},
			fragments: []string{"retry admission", "retry-after"},
		},
		{
			name: "overbound retry-after",
			mutate: func(event *domain.RunEvent, _ domain.RunState) {
				event.Payload["retryAfterNanoseconds"] = float64((port.MaxRetryHintWindow + time.Second).Nanoseconds())
			},
			fragments: []string{"retry admission", "retry-after"},
		},
		{
			name: "conflicting hints",
			mutate: func(event *domain.RunEvent, _ domain.RunState) {
				event.Payload["retryAfterNanoseconds"] = float64(time.Second.Nanoseconds())
				event.Payload["notBefore"] = event.Timestamp.Add(time.Hour).UTC().Format(time.RFC3339)
			},
			fragments: []string{"retry admission", "conflict"},
		},
		{
			name: "noncanonical not-before",
			mutate: func(event *domain.RunEvent, _ domain.RunState) {
				event.Payload["notBefore"] = event.Timestamp.Add(time.Hour).Format("2006-01-02T15:04:05+00:00")
			},
			fragments: []string{"retry admission", "not-before"},
		},
		{
			name: "noncanonical padded fractional not-before",
			mutate: func(event *domain.RunEvent, _ domain.RunState) {
				// Force a whole-second value so the fixed-width fractional form
				// always contains redundant zero padding. Using the event's live
				// nanoseconds can accidentally produce canonical RFC3339Nano when
				// the final digit is non-zero, making this fixture platform-dependent.
				event.Payload["notBefore"] = event.Timestamp.Truncate(time.Second).Add(time.Hour).UTC().Format("2006-01-02T15:04:05.000000000Z07:00")
			},
			fragments: []string{"retry admission", "not-before"},
		},
		{
			name: "past not-before",
			mutate: func(event *domain.RunEvent, _ domain.RunState) {
				event.Payload["notBefore"] = event.Timestamp.Add(-time.Second).UTC().Format(time.RFC3339)
			},
			fragments: []string{"retry admission", "not-before"},
		},
		{
			name: "overbound not-before",
			mutate: func(event *domain.RunEvent, _ domain.RunState) {
				event.Payload["notBefore"] = event.Timestamp.Add(port.MaxRetryHintWindow + time.Second).UTC().Format(time.RFC3339)
			},
			fragments: []string{"retry admission", "not-before"},
		},
		{
			name:      "wrong attempt identity",
			mutate:    func(event *domain.RunEvent, _ domain.RunState) { event.AttemptID = "attempt-other" },
			fragments: []string{"retry"},
		},
		{
			name:      "wrong run identity",
			mutate:    func(event *domain.RunEvent, _ domain.RunState) { event.RunID = "run-other" },
			fragments: []string{"run"},
		},
		{
			name:      "wrong producer authority",
			mutate:    func(event *domain.RunEvent, _ domain.RunState) { event.Actor = planningActor() },
			fragments: []string{"worker.failed"},
		},
		{
			name:      "wrong transition state",
			mutate:    func(event *domain.RunEvent, _ domain.RunState) { event.StateFrom = domain.StateReady },
			fragments: []string{"journal"},
		},
		{
			name:      "wrong sequence",
			mutate:    func(event *domain.RunEvent, _ domain.RunState) { event.Sequence++ },
			fragments: []string{"sequence"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := setupPersistedTypedRetryFailure(t, nil, nil)
			state := inspectState(t, fixture)
			mutateLastWorkerFailure(t, fixture, func(event *domain.RunEvent) { test.mutate(event, state) })
			requireFailsBeforeProbe(t, fixture, test.fragments...)
		})
	}
}

func TestQwenQoderCodexResultMissingStopsAtCore(t *testing.T) {
	for _, adapterID := range []port.AdapterID{port.AdapterIDQwen, port.AdapterIDQoder, port.AdapterIDCodex} {
		t.Run(string(adapterID), func(t *testing.T) {
			fixture := newExecutionFixtureWithOptions(t, false, executionFixtureOptions{
				preferredAdapter: string(adapterID), fallbackAdapters: []string{}, capabilityAdapterID: string(adapterID),
				maxAttempts: 3, maxOperationalRetries: 2,
			})
			failure, err := port.NewAdapterFailure(adapterID, port.FailureKindResultMissing, port.RetryDispositionDoNotRetry, nil, nil, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			delegate := fixture.input.Adapter.(*fixtureAdapter)
			delegate.id, delegate.failure = string(adapterID), failure
			adapter := &countingAdapter{delegate: delegate}
			fixture.input.Adapter = adapter

			first, err := Run(context.Background(), fixture.input)
			if err == nil || first.State.State != domain.StateBlocked || first.State.TerminalReason != "adapter-result-missing" || first.State.OperationalRetriesUsed != 0 {
				t.Fatalf("result = %+v err=%v", first, err)
			}
			if adapter.runs != 1 {
				t.Fatalf("worker calls = %d, want one", adapter.runs)
			}
			second, secondErr := Run(context.Background(), fixture.input)
			if secondErr == nil || second.State.State != domain.StateBlocked {
				t.Fatalf("terminal replay = %+v err=%v", second, secondErr)
			}
			if adapter.runs != 1 {
				t.Fatalf("result-missing relaunched %s: calls=%d", adapterID, adapter.runs)
			}
		})
	}
}

func TestRunRejectsInvalidTypedFailureWithoutCauseLeakOrRetry(t *testing.T) {
	now := time.Now().UTC()
	negative := -time.Second
	overbound := port.MaxRetryHintWindow + time.Second
	valid, err := port.NewAdapterFailure(port.AdapterIDFake, port.FailureKindProtocolInvalid, port.RetryDispositionDoNotRetry, nil, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := port.NewAdapterFailure(port.AdapterIDFake, port.FailureKindProviderTerminal, port.RetryDispositionDoNotRetry, nil, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		failure error
	}{
		{name: "unknown kind with free text and control", failure: port.AdapterFailure{Adapter: port.AdapterIDFake, Kind: port.FailureKind("secret\npath=/private/tmp/token"), Disposition: port.RetryDispositionDoNotRetry}},
		{name: "invalid pairing", failure: port.AdapterFailure{Adapter: port.AdapterIDFake, Kind: port.FailureKindProtocolInvalid, Disposition: port.RetryDispositionRetryable}},
		{name: "negative retry hint", failure: port.AdapterFailure{Adapter: port.AdapterIDFake, Kind: port.FailureKindConnectionFailure, Disposition: port.RetryDispositionRetryable, RetryAfter: negative}},
		{name: "overbound retry hint", failure: port.AdapterFailure{Adapter: port.AdapterIDFake, Kind: port.FailureKindConnectionFailure, Disposition: port.RetryDispositionRetryable, RetryAfter: overbound}},
		{name: "ambiguous joined authority", failure: errors.Join(valid, second)},
		{name: "adapter identity mismatch", failure: port.AdapterFailure{Adapter: port.AdapterIDQwen, Kind: port.FailureKindProtocolInvalid, Disposition: port.RetryDispositionDoNotRetry}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newExecutionFixtureWithOptions(t, false, executionFixtureOptions{
				preferredAdapter: "fake", fallbackAdapters: []string{}, capabilityAdapterID: "fake", maxAttempts: 3, maxOperationalRetries: 2,
			})
			delegate := fixture.input.Adapter.(*fixtureAdapter)
			delegate.id, delegate.failure = "fake", fmt.Errorf("provider wrapper secret-control=\n\t: %w", test.failure)
			adapter := &countingAdapter{delegate: delegate}
			fixture.input.Adapter = adapter

			result, runErr := Run(context.Background(), fixture.input)
			if runErr == nil || result.State.State != domain.StateBlocked || result.State.TerminalReason != "adapter-protocol-invalid" || result.State.OperationalRetriesUsed != 0 {
				t.Fatalf("result = %+v err=%v", result, runErr)
			}
			if strings.Contains(runErr.Error(), "secret") || strings.Contains(runErr.Error(), "private/tmp") || strings.Contains(runErr.Error(), "\n") || strings.Contains(runErr.Error(), "\t") {
				t.Fatalf("invalid carrier cause leaked: %q", runErr)
			}
			events, _, readErr := runstore.New(fixture.input.StateRoot).ReadEvents(fixture.input.RunID)
			if readErr != nil {
				t.Fatal(readErr)
			}
			last := events[len(events)-1]
			if got := last.Payload["error"]; got != "adapter fake provider failure protocol-invalid/do-not-retry" {
				t.Fatalf("safe summary = %q", got)
			}
			if raw, readErr := os.ReadFile(filepath.Join(fixture.runDir, "outcome.json")); readErr != nil {
				t.Fatal(readErr)
			} else if bytes.Contains(raw, []byte("secret")) || bytes.Contains(raw, []byte("private/tmp")) {
				t.Fatalf("invalid carrier cause leaked into Outcome: %s", raw)
			}
			if _, secondErr := Run(context.Background(), fixture.input); secondErr == nil {
				t.Fatal("terminal invalid carrier replay unexpectedly succeeded")
			}
			if adapter.runs != 1 {
				t.Fatalf("invalid carrier relaunched Worker: calls=%d", adapter.runs)
			}
		})
	}
}

func TestStructuralFailureSignatureIgnoresRunAndAttemptIdentity(t *testing.T) {
	failure, err := port.NewAdapterFailure(port.AdapterIDQoder, port.FailureKindProtocolInvalid, port.RetryDispositionDoNotRetry, nil, nil, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	first := domain.RunState{RunID: "run-one", CurrentAttemptID: "attempt-one", SpecDigest: "sha256:" + strings.Repeat("a", 64), PolicyDigest: "sha256:" + strings.Repeat("b", 64), CapabilityDigest: "sha256:" + strings.Repeat("c", 64), BaseSHA: strings.Repeat("d", 40)}
	second := first
	second.RunID, second.CurrentAttemptID = "run-two", "attempt-nine"
	a, err := classifyWorkerFailure(first, "qoder", failure, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	b, err := classifyWorkerFailure(second, "qoder", failure, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if a.signature == "" || a.signature != b.signature {
		t.Fatalf("structural signature changed across Run/Attempt identity: %q != %q", a.signature, b.signature)
	}
	second.CapabilityDigest = "sha256:" + strings.Repeat("e", 64)
	c, err := classifyWorkerFailure(second, "qoder", failure, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if c.signature == a.signature {
		t.Fatal("adapter/capability repair did not invalidate the structural signature")
	}
	second = first
	second.BaseSHA = strings.Repeat("f", 40)
	d, err := classifyWorkerFailure(second, "qoder", failure, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if d.signature == a.signature {
		t.Fatal("source repair did not invalidate the structural signature")
	}
}

func TestStructuralFailureRestartCompensationDoesNotRelaunchWorker(t *testing.T) {
	fixture := newExecutionFixtureWithOptions(t, false, executionFixtureOptions{
		preferredAdapter: "fake", fallbackAdapters: []string{}, capabilityAdapterID: "fake", maxAttempts: 3, maxOperationalRetries: 2,
	})
	retryAfter := 30 * time.Second
	failure, err := port.NewAdapterFailure(port.AdapterIDFake, port.FailureKindQuotaExhausted, port.RetryDispositionBlocked, &retryAfter, nil, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	delegate := fixture.input.Adapter.(*fixtureAdapter)
	delegate.id, delegate.failure = "fake", failure
	adapter := &countingAdapter{delegate: delegate}
	fixture.input.Adapter = adapter
	fixture.input.AfterWorkerTerminalAppend = func() error { return errors.New("structural terminal crash") }

	first, err := Run(context.Background(), fixture.input)
	if err == nil || !strings.Contains(err.Error(), "post-append failure") || first.State.State != domain.StateBlocked {
		t.Fatalf("crash result = %+v err=%v", first, err)
	}
	if adapter.runs != 1 {
		t.Fatalf("worker calls = %d, want one", adapter.runs)
	}
	events, _, readErr := runstore.New(fixture.input.StateRoot).ReadEvents(fixture.input.RunID)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if got := events[len(events)-1].Payload["retryAfterNanoseconds"]; got != float64(retryAfter.Nanoseconds()) {
		t.Fatalf("retryAfterNanoseconds = %v", got)
	}
	if _, err := os.Stat(filepath.Join(fixture.runDir, "outcome.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Outcome committed before restart compensation: %v", err)
	}
	fixture.input.AfterWorkerTerminalAppend = nil
	recovered, err := Run(context.Background(), fixture.input)
	if err == nil || !strings.Contains(err.Error(), "operator intervention") || recovered.State.State != domain.StateBlocked {
		t.Fatalf("restart result = %+v err=%v", recovered, err)
	}
	if adapter.runs != 1 {
		t.Fatalf("restart relaunched Worker: calls=%d", adapter.runs)
	}
	if _, err := os.Stat(filepath.Join(fixture.runDir, "outcome.json")); err != nil {
		t.Fatalf("restart did not compensate Outcome: %v", err)
	}
}

func TestStructuralFailureRestartRejectsTamperedPersistentFields(t *testing.T) {
	tests := []struct {
		name        string
		mutate      func(map[string]any)
		wantErrPart string
	}{
		{name: "signature", wantErrPart: "signature", mutate: func(payload map[string]any) { payload["failureSignature"] = "sha256:" + strings.Repeat("0", 64) }},
		{name: "summary free text and control", wantErrPart: "summary", mutate: func(payload map[string]any) { payload["error"] = "secret\n/private/tmp/token" }},
		{name: "unknown kind", wantErrPart: "normalized", mutate: func(payload map[string]any) { payload["failureKind"] = "provider-secret" }},
		{name: "invalid pairing", wantErrPart: "normalized", mutate: func(payload map[string]any) { payload["retryDisposition"] = "retryable" }},
		{name: "negative retry hint", wantErrPart: "retry-after", mutate: func(payload map[string]any) { payload["retryAfterNanoseconds"] = float64(-1) }},
		{name: "overbound retry hint", wantErrPart: "retry-after", mutate: func(payload map[string]any) {
			payload["retryAfterNanoseconds"] = float64((port.MaxRetryHintWindow + time.Second).Nanoseconds())
		}},
		{name: "conflicting retry hints", wantErrPart: "normalized", mutate: func(payload map[string]any) {
			payload["retryAfterNanoseconds"] = float64(time.Second.Nanoseconds())
			payload["notBefore"] = time.Now().UTC().Add(time.Hour).Truncate(time.Second).Format(time.RFC3339)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newExecutionFixtureWithOptions(t, false, executionFixtureOptions{
				preferredAdapter: "fake", fallbackAdapters: []string{}, capabilityAdapterID: "fake", maxAttempts: 3, maxOperationalRetries: 2,
			})
			failure, err := port.NewAdapterFailure(port.AdapterIDFake, port.FailureKindProtocolInvalid, port.RetryDispositionDoNotRetry, nil, nil, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			delegate := fixture.input.Adapter.(*fixtureAdapter)
			delegate.id, delegate.failure = "fake", failure
			adapter := &countingAdapter{delegate: delegate}
			fixture.input.Adapter = adapter
			fixture.input.AfterWorkerTerminalAppend = func() error { return errors.New("structural terminal crash") }
			if result, err := Run(context.Background(), fixture.input); err == nil || result.State.State != domain.StateBlocked {
				t.Fatalf("crash result = %+v err=%v", result, err)
			}

			journalPath := filepath.Join(fixture.runDir, "events.jsonl")
			raw, err := os.ReadFile(journalPath)
			if err != nil {
				t.Fatal(err)
			}
			lines := bytes.Split(bytes.TrimSuffix(raw, []byte("\n")), []byte("\n"))
			var terminal domain.RunEvent
			if err := json.Unmarshal(lines[len(lines)-1], &terminal); err != nil {
				t.Fatal(err)
			}
			test.mutate(terminal.Payload)
			lines[len(lines)-1], err = json.Marshal(terminal)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(journalPath, append(bytes.Join(lines, []byte("\n")), '\n'), 0o600); err != nil {
				t.Fatal(err)
			}
			fixture.input.AfterWorkerTerminalAppend = nil
			if _, err := Run(context.Background(), fixture.input); err == nil || !strings.Contains(err.Error(), test.wantErrPart) {
				t.Fatalf("tampered structural fields were accepted: %v", err)
			}
			if adapter.runs != 1 {
				t.Fatalf("tampered restart relaunched Worker: calls=%d", adapter.runs)
			}
			if _, err := os.Stat(filepath.Join(fixture.runDir, "outcome.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("tampered authority received Outcome: %v", err)
			}
		})
	}
}

func TestStructuralFailureRunLeasePreventsConcurrentRelaunch(t *testing.T) {
	fixture := newExecutionFixtureWithOptions(t, false, executionFixtureOptions{
		preferredAdapter: "fake", fallbackAdapters: []string{}, capabilityAdapterID: "fake", maxAttempts: 3, maxOperationalRetries: 2,
	})
	failure, err := port.NewAdapterFailure(port.AdapterIDFake, port.FailureKindProtocolInvalid, port.RetryDispositionDoNotRetry, nil, nil, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	adapter := &blockingFailureAdapter{
		delegate: fixture.input.Adapter.(*fixtureAdapter), failure: failure,
		started: make(chan struct{}), release: make(chan struct{}),
	}
	adapter.delegate.id = "fake"
	fixture.input.Adapter = adapter
	firstResult := make(chan Result, 1)
	firstErr := make(chan error, 1)
	go func() {
		result, runErr := Run(context.Background(), fixture.input)
		firstResult <- result
		firstErr <- runErr
	}()
	<-adapter.started
	if _, err := Run(context.Background(), fixture.input); err == nil || !strings.Contains(err.Error(), "lease") {
		t.Fatalf("concurrent Run was not rejected by the run lease: %v", err)
	}
	if got := adapter.runs.Load(); got != 1 {
		t.Fatalf("concurrent admission launched %d Workers", got)
	}
	close(adapter.release)
	result, err := <-firstResult, <-firstErr
	if err == nil || result.State.State != domain.StateBlocked {
		t.Fatalf("first result = %+v err=%v", result, err)
	}
	if _, err := Run(context.Background(), fixture.input); err == nil {
		t.Fatal("blocked Run unexpectedly relaunched")
	}
	if got := adapter.runs.Load(); got != 1 {
		t.Fatalf("blocked replay launched %d Workers", got)
	}
}

func TestRunBlocksWhenPostWorkerEvidenceCannotBeRecorded(t *testing.T) {
	fixture := newExecutionFixture(t, false)
	delegate := fixture.input.Adapter.(*fixtureAdapter)
	delegate.breakGit = true
	adapter := &countingAdapter{delegate: delegate}
	fixture.input.Adapter = adapter
	result, err := Run(context.Background(), fixture.input)
	if err == nil {
		t.Fatal("observation failure was accepted")
	}
	if result.State.State != domain.StateBlocked || result.State.TerminalReason == "" {
		t.Fatalf("state = %+v", result.State)
	}
	events, _, readErr := runstore.New(fixture.input.StateRoot).ReadEvents(fixture.input.RunID)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if events[len(events)-1].Type != "worker.evidence-failed" {
		t.Fatalf("last event = %+v", events[len(events)-1])
	}
	before := result.State
	restarted, restartErr := Run(context.Background(), fixture.input)
	if restartErr == nil || !strings.Contains(restartErr.Error(), "cannot start a worker attempt") ||
		strings.Contains(restartErr.Error(), selfidentity.ReasonCrossProfileEvidence) {
		t.Fatalf("non-local evidence re-entry changed reason: result=%+v err=%v", restarted, restartErr)
	}
	if adapter.probes != 1 || adapter.runs != 1 {
		t.Fatalf("non-local evidence re-entry relaunched Adapter: probes=%d runs=%d", adapter.probes, adapter.runs)
	}
	after, inspectErr := runstore.New(fixture.input.StateRoot).Inspect(fixture.input.RunID)
	if inspectErr != nil {
		t.Fatal(inspectErr)
	}
	if after.State != before.State || after.Sequence != before.Sequence || after.AttemptsUsed != before.AttemptsUsed ||
		after.OperationalRetriesUsed != before.OperationalRetriesUsed || after.ReviewRound != before.ReviewRound {
		t.Fatalf("non-local evidence re-entry mutated authority: before=%+v after=%+v", before, after)
	}
	afterEvents, _, readErr := runstore.New(fixture.input.StateRoot).ReadEvents(fixture.input.RunID)
	if readErr != nil || len(afterEvents) != len(events) {
		t.Fatalf("non-local evidence re-entry changed journal: before=%d after=%d err=%v", len(events), len(afterEvents), readErr)
	}
}

func TestRunRejectsWorkerResultIdentityAndAttemptBudget(t *testing.T) {
	t.Run("identity", func(t *testing.T) {
		fixture := newExecutionFixtureWithOptions(t, false, executionFixtureOptions{preferredAdapter: "fake", fallbackAdapters: []string{}, capabilityAdapterID: "fake"})
		fixture.input.Adapter.(*fixtureAdapter).id = "fake"
		fixture.input.Adapter.(*fixtureAdapter).badIdentity = true
		result, err := Run(context.Background(), fixture.input)
		if err == nil || result.State.State != domain.StateBlocked || result.State.TerminalReason != "adapter-protocol-invalid" || result.State.OperationalRetriesUsed != 0 {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})
	t.Run("budget", func(t *testing.T) {
		fixture := newExecutionFixture(t, false)
		state, err := runstore.New(fixture.input.StateRoot).Inspect(fixture.input.RunID)
		if err != nil {
			t.Fatal(err)
		}
		state.AttemptsUsed = 2
		writeSnapshotFile(t, fixture, state)
		requireFailsBeforeProbe(t, fixture)
	})
}

func TestLoadReviewFindingsReadsRoundBoundDecision(t *testing.T) {
	fixture := newExecutionFixture(t, false)
	state := inspectState(t, fixture)
	decision := reworkDecisionFixture(state, 1)
	decisionData := writeDecisionFile(t, fixture, decision)
	stray := reworkDecisionFixture(state, 2)
	stray.BlockingFindings = []domain.Finding{{ID: "finding-2", Severity: "P0", Title: "stray", Description: "stray round finding", RequiredOutcome: "must never be projected"}}
	writeDecisionFile(t, fixture, stray)
	appendVerifiedAttempt(t, fixture, "attempt-round-bound")
	appendReviewReworkEvent(t, fixture, decisionData)
	if _, err := os.Stat(filepath.Join(fixture.runDir, "review-decision.json")); !os.IsNotExist(err) {
		t.Fatalf("legacy review-decision.json should not exist, stat error = %v", err)
	}
	state = inspectState(t, fixture)
	if state.State != domain.StateReworkRequested || state.ReviewRound != 1 {
		t.Fatalf("state = %+v", state)
	}
	findings, err := directLoad(t, fixture)
	if err != nil {
		t.Fatalf("loadReviewFindings failed with real journal authority: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("findings = %+v", findings)
	}
	if findings[0]["id"] != "finding-1" || findings[0]["severity"] != "P1" || findings[0]["description"] != "verification gate failed" || findings[0]["requiredOutcome"] != "fix the failing gate" {
		t.Fatalf("finding = %+v", findings[0])
	}

	t.Run("snapshot-only-recovery-rejected", func(t *testing.T) {
		snapshotFixture := newExecutionFixture(t, false)
		tampered := inspectState(t, snapshotFixture)
		tampered.State, tampered.ReviewRound, tampered.AttemptsUsed = domain.StateReworkRequested, 2, 1
		writeSnapshotFile(t, snapshotFixture, tampered)
		requireFailsBeforeProbe(t, snapshotFixture)
	})
}

func TestRunSelectsFallbackAdapterFromFrozenCapability(t *testing.T) {
	fixture := newExecutionFixtureWithOptions(t, false, executionFixtureOptions{preferredAdapter: "missing", fallbackAdapters: []string{"fixture"}, capabilityAdapterID: "fixture"})
	result, err := Run(context.Background(), fixture.input)
	if err != nil {
		t.Fatal(err)
	}
	if result.State.State != domain.StateVerifying {
		t.Fatalf("state = %+v", result.State)
	}
	requestData, err := os.ReadFile(filepath.Join(fixture.runDir, "attempts", result.AttemptID, "worker-request.json"))
	if err != nil {
		t.Fatal(err)
	}
	var request struct {
		AdapterID string `json:"adapterId"`
	}
	if err := json.Unmarshal(requestData, &request); err != nil {
		t.Fatal(err)
	}
	if request.AdapterID != "fixture" {
		t.Fatalf("worker-request adapterId = %q", request.AdapterID)
	}
	promptData, err := os.ReadFile(filepath.Join(fixture.runDir, "attempts", result.AttemptID, "control", "input", "prompt.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(promptData), "adapter.id=fixture") {
		t.Fatalf("prompt does not require the selected adapter:\n%s", promptData)
	}
	if strings.Contains(string(promptData), "adapter.id=missing") {
		t.Fatalf("prompt still requires the preferred adapter:\n%s", promptData)
	}
}

func TestRunFailsClosedWhenFrozenCapabilityAdapterDiffers(t *testing.T) {
	fixture := newExecutionFixtureWithOptions(t, false, executionFixtureOptions{preferredAdapter: "fixture", fallbackAdapters: []string{}, capabilityAdapterID: "other"})
	result, err := Run(context.Background(), fixture.input)
	if err == nil {
		t.Fatalf("mismatched frozen capability was accepted: %+v", result)
	}
	if strings.Contains(err.Error(), "notes") || strings.Contains(err.Error(), "probeErrors") {
		t.Fatalf("error leaks provider free text: %v", err)
	}
	if entries, statErr := os.ReadDir(filepath.Join(fixture.runDir, "attempts")); statErr == nil && len(entries) > 0 {
		t.Fatalf("worker was started despite capability mismatch: %v", entries)
	}
}

func TestRunSupportsReadOnlyExecutionProfile(t *testing.T) {
	fixture := newExecutionFixtureWithOptions(t, false, executionFixtureOptions{preferredAdapter: "fixture", fallbackAdapters: []string{}, capabilityAdapterID: "fixture", executionProfile: "read-only", readOnlyCapability: true})
	result, err := Run(context.Background(), fixture.input)
	if err != nil {
		t.Fatal(err)
	}
	if result.State.State != domain.StateVerifying {
		t.Fatalf("state = %+v", result.State)
	}
	requestData, err := os.ReadFile(filepath.Join(fixture.runDir, "attempts", result.AttemptID, "worker-request.json"))
	if err != nil {
		t.Fatal(err)
	}
	var request struct {
		ExecutionProfile string `json:"executionProfile"`
	}
	if err := json.Unmarshal(requestData, &request); err != nil {
		t.Fatal(err)
	}
	if request.ExecutionProfile != "read-only" {
		t.Fatalf("worker-request executionProfile = %q", request.ExecutionProfile)
	}
}

func TestRunRejectsUnsupportedExecutionProfilesFailClosed(t *testing.T) {
	t.Run("hardened", func(t *testing.T) {
		fixture := newExecutionFixtureWithOptions(t, false, executionFixtureOptions{preferredAdapter: "fixture", fallbackAdapters: []string{}, capabilityAdapterID: "fixture", executionProfile: "hardened"})
		if _, err := Run(context.Background(), fixture.input); err == nil || !strings.Contains(err.Error(), "execution profiles are supported") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("capability-misses-read-only", func(t *testing.T) {
		fixture := newExecutionFixtureWithOptions(t, false, executionFixtureOptions{preferredAdapter: "fixture", fallbackAdapters: []string{}, capabilityAdapterID: "fixture", executionProfile: "read-only"})
		if _, err := Run(context.Background(), fixture.input); err == nil || !strings.Contains(err.Error(), "execution profile not supported") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestReworkKeepsOriginalExecutionProfile(t *testing.T) {
	writePreviousAttempt := func(t *testing.T, fixture executionFixture, profile string) {
		t.Helper()
		attemptDir := filepath.Join(fixture.runDir, "attempts", "attempt:prev")
		if err := os.MkdirAll(attemptDir, 0o700); err != nil {
			t.Fatal(err)
		}
		request := mustJSON(t, map[string]any{"attemptNumber": 1, "executionProfile": profile})
		if err := os.WriteFile(filepath.Join(attemptDir, "worker-request.json"), request, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("profile-change-rejected", func(t *testing.T) {
		fixture := newExecutionFixtureWithOptions(t, false, executionFixtureOptions{preferredAdapter: "fixture", fallbackAdapters: []string{}, capabilityAdapterID: "fixture", executionProfile: "read-only", readOnlyCapability: true})
		writePreviousAttempt(t, fixture, "workspace-write")
		if _, err := Run(context.Background(), fixture.input); err == nil || !strings.Contains(err.Error(), "execution profile") {
			t.Fatalf("escalated rework profile accepted: %v", err)
		}
	})
	t.Run("same-profile-accepted", func(t *testing.T) {
		fixture := newExecutionFixtureWithOptions(t, false, executionFixtureOptions{preferredAdapter: "fixture", fallbackAdapters: []string{}, capabilityAdapterID: "fixture", executionProfile: "read-only", readOnlyCapability: true})
		writePreviousAttempt(t, fixture, "read-only")
		result, err := Run(context.Background(), fixture.input)
		if err != nil || result.State.State != domain.StateVerifying {
			t.Fatalf("state = %+v err = %v", result.State, err)
		}
	})
}

type executionFixture struct {
	input              Input
	repository, runDir string
}

type executionFixtureOptions struct {
	preferredAdapter      string
	fallbackAdapters      []string
	capabilityAdapterID   string
	executionProfile      string
	readOnlyCapability    bool
	maxAttempts           int
	maxOperationalRetries int
	maxReworkRounds       int
}

func localTestObservation(t *testing.T, activation string, observedAt time.Time) selfidentity.LocalSelfIdentityObservationV1 {
	t.Helper()
	observation := selfidentity.LocalSelfIdentityObservationV1{
		SchemaVersion: selfidentity.ObservationSchema, ActivationDigest: digestLiteral(t, activation),
		ProcessID: 42, ProcessExecutablePath: "/stable/marshal",
		RepositoryIdentity: digestLiteral(t, "repository"), CanonicalRepositoryRoot: "/repository",
		CurrentPathObject: selfidentity.CurrentPathObjectV1{
			CanonicalPath: "/stable/marshal", Device: "1", Inode: "2", Size: 3,
			RawSHA256: digestLiteral(t, "executable"), PathRechecked: true, ObservationKind: "darwin-current-path-fd-object",
		},
		SourceHead: strings.Repeat("a", 40), SelfProfile: selfidentity.LocalProfile,
		ObservedAt: observedAt.UTC().Format(time.RFC3339), Status: "pass", ReasonCode: selfidentity.ReasonObserved,
	}
	subjectRaw := mustJSON(t, map[string]any{
		"activationDigest": observation.ActivationDigest, "repositoryIdentity": observation.RepositoryIdentity,
		"canonicalRepositoryRoot": observation.CanonicalRepositoryRoot, "canonicalExecutablePath": observation.CurrentPathObject.CanonicalPath,
		"device": observation.CurrentPathObject.Device, "inode": observation.CurrentPathObject.Inode,
		"size": observation.CurrentPathObject.Size, "rawSHA256": observation.CurrentPathObject.RawSHA256,
		"sourceHead": observation.SourceHead, "selfProfile": observation.SelfProfile,
	})
	observation.IdentitySubjectDigest, _ = canonical.DigestJSON(subjectRaw)
	digestRaw := mustJSON(t, map[string]any{
		"schemaVersion": observation.SchemaVersion, "activationDigest": observation.ActivationDigest,
		"processId": observation.ProcessID, "processExecutablePath": observation.ProcessExecutablePath,
		"repositoryIdentity": observation.RepositoryIdentity, "canonicalRepositoryRoot": observation.CanonicalRepositoryRoot,
		"currentPathObject": observation.CurrentPathObject, "sourceHead": observation.SourceHead,
		"selfProfile": observation.SelfProfile, "observedAt": observation.ObservedAt,
		"status": observation.Status, "reasonCode": observation.ReasonCode,
		"identitySubjectDigest": observation.IdentitySubjectDigest,
	})
	observation.ObservationDigest, _ = canonical.DigestJSON(digestRaw)
	if err := selfidentity.ValidateObservation(observation); err != nil {
		t.Fatal(err)
	}
	return observation
}

func digestLiteral(t *testing.T, value string) string {
	t.Helper()
	digest, err := canonical.DigestJSON(mustJSON(t, map[string]string{"value": value}))
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func bindLocalSelfIdentityFixture(t *testing.T, fixture *executionFixture, activation string) []selfidentity.LocalSelfIdentityObservationV1 {
	t.Helper()
	observations := []selfidentity.LocalSelfIdentityObservationV1{
		localTestObservation(t, activation, time.Unix(10, 0).UTC()),
		localTestObservation(t, activation, time.Unix(11, 0).UTC()),
		localTestObservation(t, activation, time.Unix(12, 0).UTC()),
		localTestObservation(t, activation, time.Unix(13, 0).UTC()),
	}
	policyPath := filepath.Join(fixture.runDir, "policy-snapshot.json")
	policyRaw, err := os.ReadFile(policyPath)
	if err != nil {
		t.Fatal(err)
	}
	var policy map[string]any
	if err := json.Unmarshal(policyRaw, &policy); err != nil {
		t.Fatal(err)
	}
	policy["environmentBinding"] = map[string]any{
		"schemaVersion": "marshal.local-dogfood-environment-binding.v1", "selfProfile": selfidentity.LocalProfile,
		"activationDigest": observations[0].ActivationDigest, "identitySubjectDigest": observations[0].IdentitySubjectDigest,
		"assurance": "ordinary-user", "execution": "workspace-write", "production": false, "publication": "none",
	}
	policyRaw = mustJSON(t, policy)
	if err := fixture.input.Validator.Validate(domain.KindPolicySnapshot, policyRaw); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(policyPath, policyRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	policyDigest, err := canonical.DigestJSON(policyRaw)
	if err != nil {
		t.Fatal(err)
	}
	store := runstore.New(fixture.input.StateRoot)
	state, err := store.Inspect(fixture.input.RunID)
	if err != nil {
		t.Fatal(err)
	}
	state.PolicyDigest = policyDigest
	lease, err := store.Acquire(fixture.input.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WriteSnapshot(lease, state); err != nil {
		_ = lease.Release()
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	mutateRawJournalLines(t, *fixture, func(lines []string) {
		var event domain.RunEvent
		if err := json.Unmarshal([]byte(lines[1]), &event); err != nil {
			t.Fatal(err)
		}
		event.Payload["policyDigest"] = policyDigest
		lines[1] = string(mustJSON(t, event))
	})
	fixture.input.EntryLocalSelfIdentity = &observations[0]
	next := 1
	fixture.input.ObserveLocalSelfIdentity = func() (selfidentity.LocalSelfIdentityObservationV1, error) {
		if next >= len(observations) {
			return observations[len(observations)-1], nil
		}
		value := observations[next]
		next++
		return value, nil
	}
	return observations
}

func newExecutionFixture(t *testing.T, fail bool) executionFixture {
	t.Helper()
	return newExecutionFixtureWithOptions(t, fail, executionFixtureOptions{preferredAdapter: "fixture", fallbackAdapters: []string{}, capabilityAdapterID: "fixture"})
}

func newTypedTransientFailureFixture(t *testing.T, options executionFixtureOptions) executionFixture {
	t.Helper()
	fixture := newFakeExecutionFixture(t, options)
	fixture.input.Adapter.(*fixtureAdapter).failure = newTypedTransientFailure(t)
	return fixture
}

func newFakeExecutionFixture(t *testing.T, options executionFixtureOptions) executionFixture {
	t.Helper()
	options.preferredAdapter = string(port.AdapterIDFake)
	options.fallbackAdapters = []string{}
	options.capabilityAdapterID = string(port.AdapterIDFake)
	fixture := newExecutionFixtureWithOptions(t, false, options)
	fixture.input.Adapter.(*fixtureAdapter).id = string(port.AdapterIDFake)
	return fixture
}

func newTypedTransientFailureFixtureAdapter(t *testing.T, capability []byte) *fixtureAdapter {
	t.Helper()
	return &fixtureAdapter{id: string(port.AdapterIDFake), capability: capability, failure: newTypedTransientFailure(t)}
}

func newTypedTransientFailure(t *testing.T) error {
	t.Helper()
	failure, err := port.NewAdapterFailure(port.AdapterIDFake, port.FailureKindConnectionFailure, port.RetryDispositionRetryable, nil, nil, time.Unix(1, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	return failure
}

func newExecutionFixtureWithOptions(t *testing.T, fail bool, options executionFixtureOptions) executionFixture {
	t.Helper()
	repository := t.TempDir()
	git(t, repository, "init", "-q")
	git(t, repository, "config", "user.name", "Marshal Test")
	git(t, repository, "config", "user.email", "marshal@example.invalid")
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, repository, "add", "README.md")
	git(t, repository, "commit", "-q", "-m", "base")
	base := strings.TrimSpace(git(t, repository, "rev-parse", "HEAD"))
	location, err := marshalrepo.Discover(repository)
	if err != nil {
		t.Fatal(err)
	}
	if err := location.Init(); err != nil {
		t.Fatal(err)
	}
	manager, err := gitworktree.Open(repository)
	if err != nil {
		t.Fatal(err)
	}
	worktree, err := manager.Create(location.StateRoot, "TASK-1", base)
	if err != nil {
		t.Fatal(err)
	}
	if err := worktree.Release(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = exec.Command("git", "-C", repository, "worktree", "remove", "--force", worktree.Path).Run()
		_ = exec.Command("git", "-C", repository, "branch", "-D", worktree.Branch).Run()
	})
	validator, err := contract.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	executionProfile := options.executionProfile
	if executionProfile == "" {
		executionProfile = "workspace-write"
	}
	maxAttempts, maxOperationalRetries, maxReworkRounds := options.maxAttempts, options.maxOperationalRetries, options.maxReworkRounds
	if maxAttempts == 0 {
		maxAttempts = 2
	}
	if maxOperationalRetries == 0 {
		maxOperationalRetries = 1
	}
	profiles := []string{"workspace-write"}
	if options.readOnlyCapability {
		profiles = append(profiles, "read-only")
	}
	capabilityObject := map[string]any{
		"apiVersion": "marshal.dev/v1alpha1", "kind": "CapabilitySnapshot", "adapterId": options.capabilityAdapterID, "adapterVersion": "0.1.0", "executable": "/fixture", "executableDigest": "sha256:" + strings.Repeat("a", 64), "binaryVersion": "1", "probeStatus": "supported",
		"capabilities": map[string]any{"structuredOutput": []string{"jsonl"}, "nonInteractiveEdit": true, "sessionPolicies": []string{"ephemeral"}, "modelSelection": false, "executionProfiles": profiles, "nativeBudgets": []string{}, "processTreeCancellation": true, "notes": []string{}}, "probeErrors": []string{}, "probedAt": "2026-08-04T00:00:00Z",
	}
	if options.capabilityAdapterID == string(port.AdapterIDQoder) || options.capabilityAdapterID == string(port.AdapterIDCodex) {
		capabilityObject["authorityMode"] = "ordinary-user"
	}
	capability := mustJSON(t, capabilityObject)
	policy := mustJSON(t, map[string]any{
		"apiVersion": "marshal.dev/v1alpha1", "kind": "PolicySnapshot", "taskId": "TASK-1", "runId": "run-1",
		"sources":      []any{map[string]any{"scope": "builtin", "digest": "sha256:" + strings.Repeat("b", 64), "required": true}},
		"effective":    map[string]any{"minimumExecutionProfile": "workspace-write", "requireEnforcedNetworkPolicy": false, "networkPolicy": "unenforced", "allowFallbackWorkers": false, "allowWorkerSubagents": false, "allowPublication": false, "allowMerge": false, "allowGateWaivers": false, "allowedAdapters": []string{"fixture"}, "environmentAllowlist": []string{"PATH"}, "retentionDays": 1},
		"policyDigest": "sha256:" + strings.Repeat("c", 64), "generatedAt": "2026-08-04T00:00:00Z",
	})
	task := mustJSON(t, map[string]any{
		"apiVersion": "marshal.dev/v1alpha1", "kind": "Task", "metadata": map[string]any{"id": "TASK-1", "title": "fixture"},
		"repository": map[string]any{"path": repository, "baseRef": "HEAD", "remote": "origin"}, "work": map[string]any{"objective": "write change.txt", "constraints": []string{}, "nonGoals": []string{}},
		"scope":      map[string]any{"allowPaths": []string{"change.txt"}, "denyPaths": []string{}, "allowSubmodules": false, "maxChangedFiles": 2, "maxDiffBytes": 10000},
		"acceptance": map[string]any{"commands": []any{}, "allowNoChange": false}, "deliverables": []any{map[string]any{"id": "code", "kind": "code", "required": true, "pathGlob": "change.txt"}},
		"worker":      map[string]any{"preferredAdapter": options.preferredAdapter, "fallbackAdapters": options.fallbackAdapters, "executionProfile": executionProfile, "sessionPolicy": "ephemeral"},
		"budgets":     map[string]any{"runTimeoutSeconds": 60, "attemptTimeoutSeconds": 10, "maxAttempts": maxAttempts, "maxOperationalRetries": maxOperationalRetries, "maxReworkRounds": maxReworkRounds, "maxOutputBytes": 100000},
		"publication": map[string]any{"required": false, "provider": "none", "mode": "none", "remote": "origin", "baseBranch": "main", "mergePolicy": "never", "requiredChecks": []string{}},
	})
	if err := validator.Validate(domain.KindTask, task); err != nil {
		t.Fatal(err)
	}
	runID := "run-1"
	runDir := filepath.Join(location.StateRoot, "runs", runID)
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "task-spec.json"), task, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "capability-snapshot.json"), capability, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "policy-snapshot.json"), policy, 0o600); err != nil {
		t.Fatal(err)
	}
	specDigest, _ := canonical.DigestJSON(task)
	capDigest, _ := canonical.DigestJSON(capability)
	policyDigest, _ := canonical.DigestJSON(policy)
	store := runstore.New(location.StateRoot)
	lease, err := store.Acquire(runID)
	if err != nil {
		t.Fatal(err)
	}
	// Planning froze the snapshot and both planning events with the same
	// instant; one durable batch keeps fixture setup cheap.
	now := time.Unix(1, 0).UTC()
	state := domain.NewRunState("TASK-1", runID, now)
	state.State, state.Sequence, state.SpecDigest, state.PolicyDigest, state.CapabilityDigest, state.BaseSHA, state.WorktreePath = domain.StateReady, 2, specDigest, policyDigest, capDigest, base, worktree.Path
	// Real planning authority events: admission binds the Run identity and
	// the five frozen fields to exactly these producer records.
	planningEvents := []domain.RunEvent{
		{
			APIVersion: domain.APIVersionV1Alpha1,
			Kind:       domain.KindRunEvent,
			EventID:    "event:1",
			RunID:      runID,
			Sequence:   1,
			Type:       "planning.spec-accepted",
			StateFrom:  domain.StateCreated,
			StateTo:    domain.StatePlanned,
			Timestamp:  now,
			Actor:      planningActor(),
			Payload:    map[string]any{"specDigest": specDigest, "executionProfile": executionProfile, "sessionPolicy": "ephemeral"},
		},
		{
			APIVersion: domain.APIVersionV1Alpha1,
			Kind:       domain.KindRunEvent,
			EventID:    "event:2",
			RunID:      runID,
			Sequence:   2,
			Type:       "planning.inputs-frozen",
			StateFrom:  domain.StatePlanned,
			StateTo:    domain.StateReady,
			Timestamp:  now,
			Actor:      planningActor(),
			Payload:    map[string]any{"adapterId": options.capabilityAdapterID, "baseSha": base, "specDigest": specDigest, "policyDigest": policyDigest, "capabilityDigest": capDigest, "worktreePath": worktree.Path},
		},
	}
	var journal []byte
	for _, event := range planningEvents {
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		journal = append(append(journal, data...), '\n')
	}
	if err := os.WriteFile(filepath.Join(runDir, "events.jsonl"), journal, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteSnapshot(lease, state); err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	return executionFixture{Input{StateRoot: location.StateRoot, RepositoryRoot: manager.Root, RunID: runID, Adapter: &fixtureAdapter{capability: capability, fail: fail}, Validator: validator}, repository, runDir}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
func git(t *testing.T, directory string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", directory}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}

type countingAdapter struct {
	delegate *fixtureAdapter
	probes   int
	runs     int
}

type blockingFailureAdapter struct {
	delegate *fixtureAdapter
	failure  error
	started  chan struct{}
	release  chan struct{}
	runs     atomic.Int32
}

func (a *blockingFailureAdapter) ID() string { return a.delegate.ID() }

func (a *blockingFailureAdapter) Probe(ctx context.Context) (domain.Record, error) {
	return a.delegate.Probe(ctx)
}

func (a *blockingFailureAdapter) Run(context.Context, domain.Record) (domain.Record, error) {
	if a.runs.Add(1) == 1 {
		close(a.started)
	}
	<-a.release
	return domain.Record{}, a.failure
}

func (a *countingAdapter) ID() string { return a.delegate.ID() }

func (a *countingAdapter) Probe(ctx context.Context) (domain.Record, error) {
	a.probes++
	return a.delegate.Probe(ctx)
}

func (a *countingAdapter) Run(ctx context.Context, request domain.Record) (domain.Record, error) {
	a.runs++
	return a.delegate.Run(ctx, request)
}

type journalFingerprint struct {
	records int
	raw     string
}

type persistedPathFingerprint struct {
	info os.FileInfo
	raw  []byte
}

func capturePersistedPath(t *testing.T, path string) persistedPathFingerprint {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if info.Mode().IsRegular() {
		raw, err = os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
	}
	return persistedPathFingerprint{info: info, raw: raw}
}

func capturePersistedTree(t *testing.T, root string) map[string]persistedPathFingerprint {
	t.Helper()
	result := map[string]persistedPathFingerprint{}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		result[relative] = capturePersistedPath(t, path)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return result
}

func requirePersistedPathUnchanged(t *testing.T, label string, before, after persistedPathFingerprint) {
	t.Helper()
	if !os.SameFile(before.info, after.info) || before.info.Mode() != after.info.Mode() || before.info.Size() != after.info.Size() || !before.info.ModTime().Equal(after.info.ModTime()) || !bytes.Equal(before.raw, after.raw) {
		t.Fatalf("%s identity, metadata or bytes changed across fail-closed admission", label)
	}
}

func requirePersistedTreeUnchanged(t *testing.T, root string, before map[string]persistedPathFingerprint) {
	t.Helper()
	after := capturePersistedTree(t, root)
	if len(after) != len(before) {
		t.Fatalf("persisted tree membership changed across fail-closed admission: before=%v after=%v", reflect.ValueOf(before).MapKeys(), reflect.ValueOf(after).MapKeys())
	}
	for relative, beforePath := range before {
		afterPath, ok := after[relative]
		if !ok {
			t.Fatalf("persisted tree path %q disappeared across fail-closed admission", relative)
		}
		requirePersistedPathUnchanged(t, filepath.Join(root, relative), beforePath, afterPath)
	}
}

func captureJournal(t *testing.T, fixture executionFixture) journalFingerprint {
	t.Helper()
	events, _, _ := runstore.New(fixture.input.StateRoot).ReadEvents(fixture.input.RunID)
	raw, err := os.ReadFile(filepath.Join(fixture.runDir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return journalFingerprint{records: len(events), raw: string(raw)}
}

// requireFailsBeforeProbe is the unified fail-closed oracle: every
// negative class enters through Run and must fail before Probe with no side
// effect — no adapter call, no attempt/control directory, and no journal
// sequence or raw byte change.
func requireFailsBeforeProbe(t *testing.T, fixture executionFixture, fragments ...string) {
	t.Helper()
	adapter := &countingAdapter{delegate: fixture.input.Adapter.(*fixtureAdapter)}
	input := fixture.input
	input.Adapter = adapter
	before := captureJournal(t, fixture)
	statePath := filepath.Join(fixture.runDir, "state.json")
	beforeState, stateErr := os.ReadFile(statePath)
	if stateErr != nil {
		t.Fatal(stateErr)
	}
	attempts := attemptDirCount(t, fixture)
	_, err := Run(context.Background(), input)
	if err == nil {
		t.Fatal("expected a fail-closed rejection before the worker starts")
	}
	for _, fragment := range fragments {
		if !strings.Contains(err.Error(), fragment) {
			t.Fatalf("rejection %q does not contain %q", err, fragment)
		}
	}
	if adapter.probes != 0 || adapter.runs != 0 {
		t.Fatalf("adapter was invoked before the fail-closed rejection: probes=%d runs=%d", adapter.probes, adapter.runs)
	}
	if attemptDirCount(t, fixture) != attempts {
		t.Fatal("attempt/control directories advanced despite the fail-closed rejection")
	}
	if after := captureJournal(t, fixture); after != before {
		t.Fatal("journal sequence or raw bytes advanced despite the fail-closed rejection")
	}
	afterState, stateErr := os.ReadFile(statePath)
	if stateErr != nil {
		t.Fatal(stateErr)
	}
	if !bytes.Equal(afterState, beforeState) {
		t.Fatalf("run state or counters advanced despite the fail-closed rejection: before=%s after=%s", beforeState, afterState)
	}
}

func systemActor(id string) *domain.Actor { return &domain.Actor{Type: "system", ID: id} }
func planningActor() *domain.Actor        { return systemActor("marshal-planning") }
func workerRunnerActor() *domain.Actor    { return systemActor("marshal-worker-runner") }
func verifierActor() *domain.Actor        { return systemActor("marshal-verifier") }
func reviewActor() *domain.Actor          { return systemActor("marshal-review") }
func publisherActor() *domain.Actor {
	return &domain.Actor{Type: "publisher", ID: "marshal-github-publisher"}
}
func reconciliationActor() *domain.Actor { return systemActor("marshal-reconciliation") }

func verificationPayload() map[string]any {
	return map[string]any{"snapshotDigest": "sha256:" + strings.Repeat("5", 64), "diffDigest": "sha256:" + strings.Repeat("6", 64)}
}

func reworkBudgetOptions(retries int) executionFixtureOptions {
	return executionFixtureOptions{preferredAdapter: "fixture", fallbackAdapters: []string{}, capabilityAdapterID: "fixture", maxAttempts: 5, maxOperationalRetries: retries, maxReworkRounds: 1}
}

func appendVerifiedAttempt(t *testing.T, fixture executionFixture, attemptID string) {
	t.Helper()
	appendRunEvents(t, fixture,
		step("worker.started", domain.StateRunning, workerRunnerActor(), attemptID, map[string]any{"adapterId": "fixture"}),
		step("worker.completed", domain.StateVerifying, workerRunnerActor(), attemptID, verificationPayload()),
		step("verification.completed", domain.StateReviewPending, verifierActor(), "", nil))
}

func setupReviewPendingFixture(t *testing.T, attemptID string) executionFixture {
	t.Helper()
	fixture := newExecutionFixture(t, false)
	appendVerifiedAttempt(t, fixture, attemptID)
	return fixture
}

func appendRetrySegment(t *testing.T, fixture executionFixture, attemptID string) {
	t.Helper()
	state := inspectState(t, fixture)
	payload := persistedFailurePayload(t, state, port.AdapterID(fixture.input.Adapter.ID()), port.FailureKindConnectionFailure, port.RetryDispositionRetryable, nil, nil)
	appendRunEvents(t, fixture,
		step("worker.started", domain.StateRunning, workerRunnerActor(), attemptID, map[string]any{"adapterId": "fixture"}),
		step("worker.failed", domain.StateRetryPending, workerRunnerActor(), attemptID, payload))
}

func durationPointer(value time.Duration) *time.Duration { return &value }

func persistedFailurePayload(t *testing.T, state domain.RunState, adapterID port.AdapterID, kind port.FailureKind, disposition port.RetryDisposition, retryAfter *time.Duration, notBefore *time.Time) map[string]any {
	t.Helper()
	signature, err := workerFailureSignature(workerFailureSignatureContext{
		baseSHA: state.BaseSHA, specDigest: state.SpecDigest, policyDigest: state.PolicyDigest, capabilityDigest: state.CapabilityDigest,
	}, string(adapterID), kind, disposition)
	if err != nil {
		t.Fatal(err)
	}
	failure := port.AdapterFailure{Adapter: adapterID, Kind: kind, Disposition: disposition}
	payload := map[string]any{
		"adapterId":        string(adapterID),
		"failureKind":      string(kind),
		"retryDisposition": string(disposition),
		"failureSignature": signature,
	}
	if retryAfter != nil {
		failure.RetryAfter = *retryAfter
		payload["retryAfterNanoseconds"] = float64(retryAfter.Nanoseconds())
	}
	if notBefore != nil {
		failure.NotBefore = notBefore.UTC()
		payload["notBefore"] = notBefore.UTC().Format(time.RFC3339Nano)
	}
	payload["error"] = failure.Error()
	return payload
}

func setPersistedFailurePayload(t *testing.T, event *domain.RunEvent, state domain.RunState, adapterID port.AdapterID, kind port.FailureKind, disposition port.RetryDisposition, retryAfter *time.Duration, notBefore *time.Time) {
	t.Helper()
	event.Payload = persistedFailurePayload(t, state, adapterID, kind, disposition, retryAfter, notBefore)
}

func setupPersistedTypedRetryFailure(t *testing.T, retryAfter *time.Duration, notBefore *time.Time) executionFixture {
	t.Helper()
	fixture := newExecutionFixtureWithOptions(t, false, executionFixtureOptions{
		preferredAdapter: "fake", fallbackAdapters: []string{}, capabilityAdapterID: "fake", maxAttempts: 3, maxOperationalRetries: 2,
	})
	delegate := fixture.input.Adapter.(*fixtureAdapter)
	delegate.id = "fake"
	failure, err := port.NewAdapterFailure(port.AdapterIDFake, port.FailureKindConnectionFailure, port.RetryDispositionRetryable, retryAfter, notBefore, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	delegate.failure = failure
	result, err := Run(context.Background(), fixture.input)
	if err == nil || result.State.State != domain.StateRetryPending || result.State.AttemptsUsed != 1 || result.State.OperationalRetriesUsed != 1 {
		t.Fatalf("seed typed retry failure = %+v err=%v", result, err)
	}
	delegate.failure = nil
	return fixture
}

func setupHistoricalNotBeforeRetryFailure(t *testing.T) executionFixture {
	t.Helper()
	fixture := newExecutionFixtureWithOptions(t, false, executionFixtureOptions{
		preferredAdapter: "fake", fallbackAdapters: []string{}, capabilityAdapterID: "fake", maxAttempts: 3, maxOperationalRetries: 2,
	})
	fixture.input.Adapter.(*fixtureAdapter).id = "fake"
	state := inspectState(t, fixture)
	// appendRunEvents deterministically records worker.started at t=102 and
	// worker.failed at t=103 for a fresh fixture. The bounded not-before is
	// valid relative to the failure event but has elapsed relative to now.
	// The failure event below is recorded at exactly t=103. Preserve a
	// same-second fractional not-before so persistence round-trip loss would
	// collapse it to the terminal timestamp and permanently reject this retry.
	notBefore := time.Unix(103, 500000123).UTC()
	payload := persistedFailurePayload(t, state, port.AdapterIDFake, port.FailureKindConnectionFailure, port.RetryDispositionRetryable, nil, &notBefore)
	appendRunEvents(t, fixture,
		step("worker.started", domain.StateRunning, workerRunnerActor(), "attempt-historical-not-before", map[string]any{"adapterId": "fake"}),
		step("worker.failed", domain.StateRetryPending, workerRunnerActor(), "attempt-historical-not-before", payload))
	return fixture
}

func mutateLastWorkerFailure(t *testing.T, fixture executionFixture, mutate func(*domain.RunEvent)) {
	t.Helper()
	mutateRawJournalLines(t, fixture, func(lines []string) {
		for index := len(lines) - 1; index >= 0; index-- {
			var event domain.RunEvent
			if err := json.Unmarshal([]byte(lines[index]), &event); err != nil {
				t.Fatal(err)
			}
			if event.Type != "worker.failed" {
				continue
			}
			mutate(&event)
			data, err := json.Marshal(event)
			if err != nil {
				t.Fatal(err)
			}
			lines[index] = string(data)
			return
		}
		t.Fatal("journal has no worker.failed event")
	})
}

func setupRetryPendingFixture(t *testing.T, attemptID string) executionFixture {
	t.Helper()
	fixture := newExecutionFixture(t, false)
	appendRetrySegment(t, fixture, attemptID)
	return fixture
}

func inspectState(t *testing.T, fixture executionFixture) domain.RunState {
	t.Helper()
	state, err := runstore.New(fixture.input.StateRoot).Inspect(fixture.input.RunID)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func directLoad(t *testing.T, fixture executionFixture) ([]map[string]string, error) {
	t.Helper()
	var capability struct {
		AdapterID string `json:"adapterId"`
	}
	if err := json.Unmarshal(fixture.input.Adapter.(*fixtureAdapter).capability, &capability); err != nil {
		t.Fatal(err)
	}
	return loadReviewFindings(runstore.New(fixture.input.StateRoot), fixture.runDir, inspectState(t, fixture), fixture.input.Validator, capability.AdapterID)
}

func writeSnapshotFile(t *testing.T, fixture executionFixture, state domain.RunState) {
	t.Helper()
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.runDir, "state.json"), append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

type journalStep struct {
	eventType string
	target    domain.State
	actor     *domain.Actor
	attemptID string
	payload   map[string]any
}

func step(eventType string, target domain.State, actor *domain.Actor, attemptID string, payload map[string]any) journalStep {
	return journalStep{eventType: eventType, target: target, actor: actor, attemptID: attemptID, payload: payload}
}

// appendRunEvents advances the fixture journal with one durable batch: events
// are replay-validated in order and appended to events.jsonl in a single
// write, while the snapshot is left behind so Inspect replays the journal
// tail. This keeps fixture setup cheap without weakening journal authority.
func appendRunEvents(t *testing.T, fixture executionFixture, steps ...journalStep) domain.RunState {
	t.Helper()
	state := inspectState(t, fixture)
	var batch []byte
	for _, current := range steps {
		if current.payload == nil {
			current.payload = map[string]any{}
		}
		eventID, err := domain.NewID("event")
		if err != nil {
			t.Fatal(err)
		}
		event := domain.RunEvent{
			APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent, EventID: eventID, RunID: fixture.input.RunID,
			AttemptID: current.attemptID, Sequence: state.Sequence + 1, Type: current.eventType, StateFrom: state.State, StateTo: current.target,
			Timestamp: time.Unix(100+int64(state.Sequence), 0).UTC(), Actor: current.actor, Payload: current.payload,
		}
		next, err := lifecycle.Replay(state, event)
		if err != nil {
			t.Fatalf("append %s: replay: %v", current.eventType, err)
		}
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		batch = append(append(batch, data...), '\n')
		state = next
	}
	appendRawJournalBytes(t, fixture, string(batch))
	return state
}

func appendRunEvent(t *testing.T, fixture executionFixture, eventType string, target domain.State, actor *domain.Actor, attemptID string, payload map[string]any) domain.RunState {
	t.Helper()
	return appendRunEvents(t, fixture, step(eventType, target, actor, attemptID, payload))
}

func reworkDecisionFixture(state domain.RunState, round uint) domain.ReviewDecision {
	return domain.ReviewDecision{
		APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindReviewDecision,
		TaskID: state.TaskID, RunID: state.RunID, ReviewRound: round,
		Reviewer:               domain.Reviewer{Type: "lead-agent", ID: "execution-test"},
		SpecDigest:             state.SpecDigest,
		ReviewPacketDigest:     "sha256:" + strings.Repeat("1", 64),
		VerificationDigest:     "sha256:" + strings.Repeat("2", 64),
		ArtifactManifestDigest: "sha256:" + strings.Repeat("3", 64),
		EvidenceDigest:         "sha256:" + strings.Repeat("4", 64),
		Verdict:                "rework", Summary: "rework required",
		BlockingFindings:          []domain.Finding{{ID: "finding-1", Severity: "P1", Title: "gate failed", Description: "verification gate failed", RequiredOutcome: "fix the failing gate"}},
		NonBlockingFindings:       []domain.Finding{{ID: "note-1", Severity: "P3", Title: "style", Description: "cosmetic naming note"}},
		PublicationRecommendation: "do-not-publish", MergeRecommendation: "do-not-merge",
		DecidedAt: time.Unix(2, 0).UTC(),
	}
}

func writeDecisionFile(t *testing.T, fixture executionFixture, decision domain.ReviewDecision) []byte {
	t.Helper()
	data, err := json.Marshal(decision)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.input.Validator.Validate(domain.KindReviewDecision, data); err != nil {
		t.Fatalf("decision fixture fails contract: %v", err)
	}
	path := filepath.Join(fixture.runDir, "decisions", fmt.Sprintf("decision-%03d.json", decision.ReviewRound))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return data
}

func reviewReworkPayload(decisionData []byte) map[string]any {
	var decision domain.ReviewDecision
	if err := json.Unmarshal(decisionData, &decision); err != nil {
		panic(err)
	}
	digest, err := canonical.DigestJSON(decisionData)
	if err != nil {
		panic(err)
	}
	return map[string]any{"verdict": "rework", "decisionDigest": digest, "evidenceDigest": decision.EvidenceDigest}
}

func appendReviewReworkEvent(t *testing.T, fixture executionFixture, decisionData []byte) {
	t.Helper()
	appendRunEvent(t, fixture, "review.rework", domain.StateReworkRequested, reviewActor(), "", reviewReworkPayload(decisionData))
}

func setupReviewOriginRework(t *testing.T, fixture executionFixture, decisionData []byte, attemptID string) {
	t.Helper()
	appendVerifiedAttempt(t, fixture, attemptID)
	appendReviewReworkEvent(t, fixture, decisionData)
}

// setupRound1ReviewOrigin builds the canonical round-1 review-origin journal.
func setupRound1ReviewOrigin(t *testing.T, fixture executionFixture, attemptID string) {
	t.Helper()
	setupReviewOriginRework(t, fixture, writeDecisionFile(t, fixture, reworkDecisionFixture(inspectState(t, fixture), 1)), attemptID)
}

// attemptReviewFindings reads the reviewFindings of an attempt's worker-request.json.
func attemptReviewFindings(t *testing.T, fixture executionFixture, attemptID string) ([]map[string]string, []byte) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixture.runDir, "attempts", attemptID, "worker-request.json"))
	if err != nil {
		t.Fatal(err)
	}
	var request struct {
		ReviewFindings []map[string]string `json:"reviewFindings"`
	}
	if err := json.Unmarshal(data, &request); err != nil {
		t.Fatal(err)
	}
	return request.ReviewFindings, data
}

func publicationPayload(headSHA string) map[string]any {
	return map[string]any{
		"provider": "github", "repository": "example/repo", "headBranch": "marshal/run-1", "baseBranch": "main",
		"externalId": "pr-1", "uri": "https://github.example/pr/1", "headSha": headSHA,
	}
}

// appendAcceptPublicationChain advances a REVIEW_PENDING fixture through an
// accept decision, publication and remote-check request into CI_PENDING.
func appendAcceptPublicationChain(t *testing.T, fixture executionFixture, headSHA string) {
	t.Helper()
	acceptDecision := reworkDecisionFixture(inspectState(t, fixture), 1)
	acceptDecision.Verdict, acceptDecision.Summary = "accept", "accepted for publication"
	acceptDecision.BlockingFindings = []domain.Finding{}
	acceptDecision.PublicationRecommendation = "publish"
	acceptPayload := reviewReworkPayload(writeDecisionFile(t, fixture, acceptDecision))
	acceptPayload["verdict"] = "accept"
	appendRunEvents(t, fixture,
		step("review.accept", domain.StatePublishing, reviewActor(), "", acceptPayload),
		step("publication.completed", domain.StatePublished, publisherActor(), "", publicationPayload(headSHA)),
		step("publication.checks-requested", domain.StateCIPending, publisherActor(), "", map[string]any{"headSha": headSHA}))
}

func setupCIOriginToCIPending(t *testing.T, fixture executionFixture, attemptID string) string {
	t.Helper()
	const headSHA = "abcdef0123456789abcdef0123456789abcdef01"
	appendVerifiedAttempt(t, fixture, attemptID)
	appendAcceptPublicationChain(t, fixture, headSHA)
	return headSHA
}

func attemptDirCount(t *testing.T, fixture executionFixture) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(fixture.runDir, "attempts"))
	if err != nil {
		return 0
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir() {
			count++
		}
	}
	return count
}

func appendRawJournalBytes(t *testing.T, fixture executionFixture, extra string) {
	t.Helper()
	path := filepath.Join(fixture.runDir, "events.jsonl")
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.WriteString(extra); err != nil {
		t.Fatal(err)
	}
}

func TestRunReviewReworkOperationalRetryPersistsFindingsAfterRestart(t *testing.T) {
	fixture := newFakeExecutionFixture(t, reworkBudgetOptions(1))
	first, err := Run(context.Background(), fixture.input)
	if err != nil || first.State.State != domain.StateVerifying {
		t.Fatalf("first attempt: state=%+v err=%v", first.State, err)
	}
	appendRunEvent(t, fixture, "verification.completed", domain.StateReviewPending, verifierActor(), "", map[string]any{})
	state := inspectState(t, fixture)
	if state.ReviewRound != 1 {
		t.Fatalf("reviewRound = %d", state.ReviewRound)
	}
	decisionData := writeDecisionFile(t, fixture, reworkDecisionFixture(state, 1))
	appendReviewReworkEvent(t, fixture, decisionData)

	capability := fixture.input.Adapter.(*fixtureAdapter).capability
	failInput := fixture.input
	failInput.Adapter = newTypedTransientFailureFixtureAdapter(t, capability)
	second, err := Run(context.Background(), failInput)
	if err == nil {
		t.Fatal("rework attempt operational failure was accepted")
	}
	if second.State.State != domain.StateRetryPending || second.State.OperationalRetriesUsed != 1 || second.State.AttemptsUsed != 2 || second.State.ReviewRound != 1 {
		t.Fatalf("state after rework attempt failure = %+v", second.State)
	}

	// Restart with brand-new caller-side objects; no in-memory state survives.
	freshValidator, err := contract.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	freshState, err := runstore.New(fixture.input.StateRoot).Inspect(fixture.input.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if freshState.State != domain.StateRetryPending || freshState.ReviewRound != 1 || freshState.CurrentAttemptID != second.AttemptID {
		t.Fatalf("fresh inspect after restart = %+v", freshState)
	}
	restartInput := Input{StateRoot: fixture.input.StateRoot, RepositoryRoot: fixture.input.RepositoryRoot, RunID: fixture.input.RunID, Adapter: &fixtureAdapter{id: string(port.AdapterIDFake), capability: capability}, Validator: freshValidator}
	third, err := Run(context.Background(), restartInput)
	if err != nil {
		t.Fatalf("restart after operational retry of a rework attempt: %v", err)
	}
	if third.State.State != domain.StateVerifying || third.State.AttemptsUsed != 3 || third.State.ReviewRound != 1 {
		t.Fatalf("state after restart = %+v", third.State)
	}
	reviewFindings, requestData := attemptReviewFindings(t, fixture, third.AttemptID)
	if len(reviewFindings) != 1 {
		t.Fatalf("reviewFindings after restart = %+v", reviewFindings)
	}
	for key, want := range map[string]string{"id": "finding-1", "severity": "P1", "description": "verification gate failed", "requiredOutcome": "fix the failing gate"} {
		if reviewFindings[0][key] != want {
			t.Fatalf("reviewFindings[0][%s] = %q, want %q", key, reviewFindings[0][key], want)
		}
	}
	if len(reviewFindings[0]) != len(projectionFindingKeys) {
		t.Fatalf("reviewFindings[0] has unexpected fields: %+v", reviewFindings[0])
	}
	promptData, err := os.ReadFile(filepath.Join(fixture.runDir, "attempts", third.AttemptID, "control", "input", "prompt.md"))
	if err != nil {
		t.Fatal(err)
	}
	prompt := string(promptData)
	if !strings.Contains(prompt, `"id":"finding-1"`) || !strings.Contains(prompt, `"requiredOutcome":"fix the failing gate"`) {
		t.Fatalf("prompt after restart does not project the blocking finding:\n%s", prompt)
	}
	for _, leaked := range []string{"note-1", "cosmetic naming note"} {
		if strings.Contains(string(requestData), leaked) || strings.Contains(prompt, leaked) {
			t.Fatalf("non-blocking finding leaked into request or prompt: %q", leaked)
		}
	}
}

func TestRunInvalidReworkAuthorityFailsBeforeWorkerStart(t *testing.T) {
	cases := map[string]func(t *testing.T, fixture executionFixture){
		"decision-digest-mismatch": func(t *testing.T, fixture executionFixture) {
			setupRound1ReviewOrigin(t, fixture, "attempt-authority")
			mutated := reworkDecisionFixture(inspectState(t, fixture), 1)
			mutated.Summary = "tampered after the journal event"
			writeDecisionFile(t, fixture, mutated)
			requireFailsBeforeProbe(t, fixture, "canonical digest")
		},
		"decision-missing": func(t *testing.T, fixture executionFixture) {
			setupRound1ReviewOrigin(t, fixture, "attempt-authority")
			if err := os.Remove(filepath.Join(fixture.runDir, "decisions", "decision-001.json")); err != nil {
				t.Fatal(err)
			}
			requireFailsBeforeProbe(t, fixture, "round-bound ReviewDecision")
		},
		"forged-origin-actor": func(t *testing.T, fixture executionFixture) {
			appendVerifiedAttempt(t, fixture, "attempt-authority")
			decisionData := writeDecisionFile(t, fixture, reworkDecisionFixture(inspectState(t, fixture), 1))
			appendRunEvent(t, fixture, "review.rework", domain.StateReworkRequested, workerRunnerActor(), "", reviewReworkPayload(decisionData))
			requireFailsBeforeProbe(t, fixture, "system/marshal-review")
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			setup(t, newExecutionFixture(t, false))
		})
	}
}

func TestRunCIFailureReworkAndOperationalRetryDoesNotRequireReworkDecision(t *testing.T) {
	fixture := newFakeExecutionFixture(t, reworkBudgetOptions(1))
	first, err := Run(context.Background(), fixture.input)
	if err != nil || first.State.State != domain.StateVerifying {
		t.Fatalf("first attempt: state=%+v err=%v", first.State, err)
	}
	appendRunEvent(t, fixture, "verification.completed", domain.StateReviewPending, verifierActor(), "", map[string]any{})
	const headSHA = "abcdef0123456789abcdef0123456789abcdef01"
	appendAcceptPublicationChain(t, fixture, headSHA)
	appendRunEvent(t, fixture, "publication.checks-failed", domain.StateReworkRequested, publisherActor(), "", map[string]any{"headSha": headSHA})

	capability := fixture.input.Adapter.(*fixtureAdapter).capability
	failInput := fixture.input
	failInput.Adapter = newTypedTransientFailureFixtureAdapter(t, capability)
	second, err := Run(context.Background(), failInput)
	if err == nil {
		t.Fatal("ci-origin rework attempt failure was accepted")
	}
	if second.State.State != domain.StateRetryPending || second.State.OperationalRetriesUsed != 1 {
		t.Fatalf("state = %+v", second.State)
	}
	restartInput := fixture.input
	restartInput.Adapter = &fixtureAdapter{id: string(port.AdapterIDFake), capability: capability}
	third, err := Run(context.Background(), restartInput)
	if err != nil {
		t.Fatalf("operational retry after ci-origin rework: %v", err)
	}
	if third.State.State != domain.StateVerifying || third.State.AttemptsUsed != 3 {
		t.Fatalf("state = %+v", third.State)
	}
	reviewFindings, _ := attemptReviewFindings(t, fixture, third.AttemptID)
	if len(reviewFindings) != 0 {
		t.Fatalf("ci-origin operational retry must not project findings: %+v", reviewFindings)
	}
	acceptRaw, err := os.ReadFile(filepath.Join(fixture.runDir, "decisions", "decision-001.json"))
	if err != nil {
		t.Fatal(err)
	}
	var preserved domain.ReviewDecision
	if err := json.Unmarshal(acceptRaw, &preserved); err != nil || preserved.Verdict != "accept" {
		t.Fatalf("ci-origin retry demanded the accept decision change: verdict=%q err=%v", preserved.Verdict, err)
	}
}

func TestLoadReviewFindingsRejectsDecisionDigestMismatchBeforeWorkerStart(t *testing.T) {
	fixture := newExecutionFixture(t, false)
	state := inspectState(t, fixture)
	decisionData := writeDecisionFile(t, fixture, reworkDecisionFixture(state, 1))
	setupReviewOriginRework(t, fixture, decisionData, "attempt-digest")
	mutated := reworkDecisionFixture(inspectState(t, fixture), 1)
	mutated.Summary = "tampered decision body"
	writeDecisionFile(t, fixture, mutated)
	requireFailsBeforeProbe(t, fixture, "canonical digest")
}

func TestLoadReviewFindingsInitialRetryHasNoDecision(t *testing.T) {
	fixture := newTypedTransientFailureFixture(t, executionFixtureOptions{})
	result, err := Run(context.Background(), fixture.input)
	if err == nil {
		t.Fatal("worker failure was accepted")
	}
	if result.State.State != domain.StateRetryPending || result.State.ReviewRound != 0 {
		t.Fatalf("state = %+v", result.State)
	}
	if _, statErr := os.Stat(filepath.Join(fixture.runDir, "decisions")); !os.IsNotExist(statErr) {
		t.Fatalf("initial retry must not have any review decision, stat = %v", statErr)
	}
	findings, err := directLoad(t, fixture)
	if err != nil {
		t.Fatalf("initial operational retry lineage rejected: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("initial retry must project no findings: %+v", findings)
	}
}

func TestLoadReviewFindingsRejectsUnknownOrConflictingLineage(t *testing.T) {
	validDigest := "sha256:" + strings.Repeat("7", 64)

	for name, forged := range map[string]struct {
		eventType string
		actor     *domain.Actor
		payload   map[string]any
		fragment  string
	}{
		"unknown-origin-type":       {"review.custom", reviewActor(), map[string]any{"verdict": "rework", "decisionDigest": validDigest}, "not a recognized authority event"},
		"conflicting-verdict":       {"review.rework", reviewActor(), map[string]any{"verdict": "accept", "decisionDigest": validDigest}, "verdict must be rework"},
		"ci-type-from-review-state": {"publication.checks-failed", publisherActor(), map[string]any{"headSha": "deadbeef"}, "ci lineage"},
	} {
		t.Run(name, func(t *testing.T) {
			fixture := setupReviewPendingFixture(t, "attempt-lineage")
			appendRunEvent(t, fixture, forged.eventType, domain.StateReworkRequested, forged.actor, "", forged.payload)
			requireFailsBeforeProbe(t, fixture, forged.fragment)
		})
	}
	t.Run("truncated-tail", func(t *testing.T) {
		fixture := newExecutionFixture(t, false)
		setupRound1ReviewOrigin(t, fixture, "attempt-lineage")
		appendRawJournalBytes(t, fixture, `{"apiVersion":"marshal.dev/v1alpha1","ki`)
		requireFailsBeforeProbe(t, fixture, "truncated")
	})
	t.Run("rework-requested-round-zero-lie", func(t *testing.T) {
		fixture := newExecutionFixture(t, false)
		setupRound1ReviewOrigin(t, fixture, "attempt-lineage")
		tampered := inspectState(t, fixture)
		tampered.ReviewRound = 0
		writeSnapshotFile(t, fixture, tampered)
		requireFailsBeforeProbe(t, fixture)
	})
	t.Run("ready-with-review-round-lie", func(t *testing.T) {
		fixture := newExecutionFixture(t, false)
		tampered := inspectState(t, fixture)
		tampered.ReviewRound = 1
		writeSnapshotFile(t, fixture, tampered)
		requireFailsBeforeProbe(t, fixture)
	})
}

func TestLoadReviewFindingsValidatesFullJournalAndSnapshot(t *testing.T) {
	newReviewOrigin := func(t *testing.T) executionFixture {
		fixture := newExecutionFixture(t, false)
		setupRound1ReviewOrigin(t, fixture, "attempt-journal")
		return fixture
	}
	matrix := map[string]func(*domain.RunState){
		"task-id":           func(s *domain.RunState) { s.TaskID = "TASK-OTHER" },
		"review-round":      func(s *domain.RunState) { s.ReviewRound = 9 },
		"attempts-used":     func(s *domain.RunState) { s.AttemptsUsed = 7 },
		"operational-retry": func(s *domain.RunState) { s.OperationalRetriesUsed = 3 },
		"rework-rounds":     func(s *domain.RunState) { s.ReworkRoundsUsed = 4 },
		"current-attempt":   func(s *domain.RunState) { s.CurrentAttemptID = "attempt-other" },
		"terminal-reason":   func(s *domain.RunState) { s.TerminalReason = "forged-terminal" },
		"run-id":            func(s *domain.RunState) { s.RunID = "run-evil" },
		"created-at":        func(s *domain.RunState) { s.CreatedAt = time.Unix(999, 0).UTC() },
		"updated-at":        func(s *domain.RunState) { s.UpdatedAt = time.Unix(999, 0).UTC() },
		"state-kind":        func(s *domain.RunState) { s.Kind = domain.Kind("ForgedKind") },
	}
	for name, mutate := range matrix {
		t.Run("snapshot-"+name, func(t *testing.T) {
			fixture := newReviewOrigin(t)
			tampered := inspectState(t, fixture)
			mutate(&tampered)
			writeSnapshotFile(t, fixture, tampered)
			requireFailsBeforeProbe(t, fixture)
		})
	}
	t.Run("publication-fails-before-probe", func(t *testing.T) {
		fixture := newExecutionFixtureWithOptions(t, false, reworkBudgetOptions(1))
		headSHA := setupCIOriginToCIPending(t, fixture, "attempt-ci")
		appendRunEvent(t, fixture, "publication.checks-failed", domain.StateReworkRequested, publisherActor(), "", map[string]any{"headSha": headSHA})
		tampered := inspectState(t, fixture)
		tampered.Publication.HeadSHA = "forged-head-sha"
		writeSnapshotFile(t, fixture, tampered)
		requireFailsBeforeProbe(t, fixture, "publication")
	})
	t.Run("raw-record-schema-unknown-field", func(t *testing.T) {
		fixture := newReviewOrigin(t)
		mutateRawJournalLine(t, fixture, "review.rework", `"injected":true`)
		requireFailsBeforeProbe(t, fixture, "RunEvent contract")
	})
	t.Run("raw-record-count-mismatch", func(t *testing.T) {
		fixture := newReviewOrigin(t)
		data, err := os.ReadFile(filepath.Join(fixture.runDir, "events.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
		appendRawJournalBytes(t, fixture, lines[len(lines)-1]+"\n")
		requireFailsBeforeProbe(t, fixture)
	})
}

func TestLoadReviewFindingsRejectsInvalidRoundBoundDecision(t *testing.T) {
	buildWithDecision := func(t *testing.T, mutate func(*domain.ReviewDecision)) executionFixture {
		t.Helper()
		fixture := setupReviewPendingFixture(t, "attempt-decision")
		decision := reworkDecisionFixture(inspectState(t, fixture), 1)
		if mutate != nil {
			mutate(&decision)
		}
		data, err := json.Marshal(decision)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(fixture.runDir, "decisions", "decision-001.json")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		appendRunEvent(t, fixture, "review.rework", domain.StateReworkRequested, reviewActor(), "", reviewReworkPayload(data))
		return fixture
	}
	t.Run("schema-invalid", func(t *testing.T) {
		fixture := buildWithDecision(t, func(decision *domain.ReviewDecision) { decision.ReviewPacketDigest = "not-a-digest" })
		requireFailsBeforeProbe(t, fixture, "contract")
	})
	t.Run("identity-mismatch", func(t *testing.T) {
		fixture := buildWithDecision(t, func(decision *domain.ReviewDecision) { decision.TaskID = "TASK-OTHER" })
		requireFailsBeforeProbe(t, fixture, "identity")
	})
	t.Run("spec-mismatch", func(t *testing.T) {
		fixture := buildWithDecision(t, func(decision *domain.ReviewDecision) { decision.SpecDigest = "sha256:" + strings.Repeat("8", 64) })
		requireFailsBeforeProbe(t, fixture, "identity")
	})
	t.Run("round-mismatch", func(t *testing.T) {
		fixture := buildWithDecision(t, func(decision *domain.ReviewDecision) { decision.ReviewRound = 2 })
		requireFailsBeforeProbe(t, fixture, "round")
	})
	t.Run("verdict-mismatch", func(t *testing.T) {
		fixture := buildWithDecision(t, func(decision *domain.ReviewDecision) {
			decision.Verdict = "reject"
			decision.BlockingFindings = []domain.Finding{}
		})
		requireFailsBeforeProbe(t, fixture, "verdict")
	})
	t.Run("only-non-blocking-findings", func(t *testing.T) {
		fixture := buildWithDecision(t, func(decision *domain.ReviewDecision) { decision.BlockingFindings = []domain.Finding{} })
		findings, err := directLoad(t, fixture)
		if err != nil {
			t.Fatalf("decision without blocking findings rejected: %v", err)
		}
		if len(findings) != 0 {
			t.Fatalf("non-blocking findings must never become rework findings: %+v", findings)
		}
	})
}

func TestLoadReviewFindingsRequiresAdjacentAttemptChain(t *testing.T) {
	t.Run("mismatched-attempt-between-started-and-failed", func(t *testing.T) {
		fixture := newExecutionFixture(t, false)
		appendRunEvents(t, fixture,
			step("worker.started", domain.StateRunning, workerRunnerActor(), "attempt-first", map[string]any{"adapterId": "fixture"}),
			step("worker.failed", domain.StateRetryPending, workerRunnerActor(), "attempt-second", map[string]any{"error": "boom"}))
		requireFailsBeforeProbe(t, fixture, "retry lineage")
	})
	t.Run("ready-origin-duplicate-attempt-id", func(t *testing.T) {
		fixture := newExecutionFixture(t, false)
		appendRetrySegment(t, fixture, "attempt-dup")
		appendRetrySegment(t, fixture, "attempt-dup")
		requireFailsBeforeProbe(t, fixture, "reused across retry segments")
	})
	t.Run("review-origin-duplicate-attempt-id", func(t *testing.T) {
		fixture := newExecutionFixtureWithOptions(t, false, reworkBudgetOptions(2))
		setupRound1ReviewOrigin(t, fixture, "attempt-dup")
		appendRetrySegment(t, fixture, "attempt-dup")
		appendRetrySegment(t, fixture, "attempt-dup")
		requireFailsBeforeProbe(t, fixture, "reused across retry segments")
	})
	t.Run("non-retry-tail", func(t *testing.T) {
		fixture := newExecutionFixture(t, false)
		appendRetrySegment(t, fixture, "attempt-tail")
		appendRunEvents(t, fixture,
			step("worker.started", domain.StateRunning, workerRunnerActor(), "attempt-tail-2", map[string]any{"adapterId": "fixture"}),
			step("worker.completed", domain.StateVerifying, workerRunnerActor(), "attempt-tail-2", verificationPayload()))
		if _, err := directLoad(t, fixture); err == nil || !strings.Contains(err.Error(), "expected worker.failed") {
			t.Fatalf("non-retry tail accepted: %v", err)
		}
		requireFailsBeforeProbe(t, fixture)
	})
}

func TestLoadReviewFindingsRejectsForgedOriginActor(t *testing.T) {
	validDigest := "sha256:" + strings.Repeat("7", 64)

	for name, forged := range map[string]struct {
		actor    *domain.Actor
		payload  map[string]any
		fragment string
	}{
		"forged-review-actor":     {workerRunnerActor(), map[string]any{"verdict": "rework", "decisionDigest": validDigest, "evidenceDigest": validDigest}, "system/marshal-review"},
		"missing-review-actor":    {nil, map[string]any{"verdict": "rework", "decisionDigest": validDigest, "evidenceDigest": validDigest}, "system/marshal-review"},
		"missing-decision-digest": {reviewActor(), map[string]any{"verdict": "rework", "evidenceDigest": validDigest}, "decisionDigest"},
		"invalid-decision-digest": {reviewActor(), map[string]any{"verdict": "rework", "decisionDigest": "sha256:nothex", "evidenceDigest": validDigest}, "decisionDigest"},
		"missing-evidence-digest": {reviewActor(), map[string]any{"verdict": "rework", "decisionDigest": validDigest}, "evidenceDigest"},
		"invalid-evidence-digest": {reviewActor(), map[string]any{"verdict": "rework", "decisionDigest": validDigest, "evidenceDigest": "md5:abc"}, "evidenceDigest"},
	} {
		t.Run(name, func(t *testing.T) {
			fixture := setupReviewPendingFixture(t, "attempt-forged")
			appendRunEvent(t, fixture, "review.rework", domain.StateReworkRequested, forged.actor, "", forged.payload)
			requireFailsBeforeProbe(t, fixture, forged.fragment)
		})
	}
	t.Run("evidence-digest-mismatch-with-decision", func(t *testing.T) {
		fixture := setupReviewPendingFixture(t, "attempt-forged")
		decisionData := writeDecisionFile(t, fixture, reworkDecisionFixture(inspectState(t, fixture), 1))
		payload := reviewReworkPayload(decisionData)
		payload["evidenceDigest"] = "sha256:" + strings.Repeat("9", 64)
		appendRunEvent(t, fixture, "review.rework", domain.StateReworkRequested, reviewActor(), "", payload)
		requireFailsBeforeProbe(t, fixture, "evidenceDigest does not match")
	})
	t.Run("forged-ci-actor", func(t *testing.T) {
		fixture := newExecutionFixture(t, false)
		headSHA := setupCIOriginToCIPending(t, fixture, "attempt-ci-forged")
		appendRunEvent(t, fixture, "publication.checks-failed", domain.StateReworkRequested, reviewActor(), "", map[string]any{"headSha": headSHA})
		requireFailsBeforeProbe(t, fixture, "publisher/marshal-github-publisher")
	})
	t.Run("ci-head-sha-mismatch", func(t *testing.T) {
		fixture := newExecutionFixture(t, false)
		setupCIOriginToCIPending(t, fixture, "attempt-ci-sha")
		appendRunEvent(t, fixture, "publication.checks-failed", domain.StateReworkRequested, publisherActor(), "", map[string]any{"headSha": "forged-head-sha"})
		requireFailsBeforeProbe(t, fixture, "frozen publication")
	})
	t.Run("ci-head-sha-empty", func(t *testing.T) {
		fixture := newExecutionFixture(t, false)
		setupCIOriginToCIPending(t, fixture, "attempt-ci-empty")
		appendRunEvent(t, fixture, "publication.checks-failed", domain.StateReworkRequested, publisherActor(), "", map[string]any{"headSha": ""})
		requireFailsBeforeProbe(t, fixture, "headSha")
	})
}

func TestLoadReviewFindingsAcceptsOnlyValidRepairAudit(t *testing.T) {
	appendRepair := func(t *testing.T, fixture executionFixture, actor *domain.Actor, attemptID string, mutate func(map[string]any)) {
		t.Helper()
		state := inspectState(t, fixture)
		payload := map[string]any{"repairKind": "snapshot-rebuild", "sourceJournalSequence": float64(state.Sequence)}
		if mutate != nil {
			mutate(payload)
		}
		appendRunEvent(t, fixture, lifecycle.RepairAuditEventType, state.State, actor, attemptID, payload)
	}

	t.Run("valid-repair-skipped-in-lineage", func(t *testing.T) {
		fixture := setupRetryPendingFixture(t, "attempt-repair")
		appendRepair(t, fixture, reconciliationActor(), "", nil)
		findings, err := directLoad(t, fixture)
		if err != nil || len(findings) != 0 {
			t.Fatalf("valid repair audit broke the ready-origin lineage: findings=%+v err=%v", findings, err)
		}
	})
	t.Run("valid-repair-between-origin-and-retry", func(t *testing.T) {
		fixture := newExecutionFixtureWithOptions(t, false, reworkBudgetOptions(1))
		setupRound1ReviewOrigin(t, fixture, "attempt-repair-2")
		appendRepair(t, fixture, reconciliationActor(), "", nil)
		appendRetrySegment(t, fixture, "attempt-repair-2")
		appendRepair(t, fixture, reconciliationActor(), "", nil)
		findings, err := directLoad(t, fixture)
		if err != nil {
			t.Fatalf("valid repair audits broke the review lineage: %v", err)
		}
		if len(findings) != 1 || findings[0]["id"] != "finding-1" {
			t.Fatalf("findings = %+v", findings)
		}
	})
	t.Run("forged-repair-actor-fails-before-probe", func(t *testing.T) {
		fixture := setupRetryPendingFixture(t, "attempt-repair-3")
		appendRepair(t, fixture, workerRunnerActor(), "", nil)
		requireFailsBeforeProbe(t, fixture, "system/marshal-reconciliation")
	})
	t.Run("forged-repair-attempt-id", func(t *testing.T) {
		fixture := setupRetryPendingFixture(t, "attempt-repair-4")
		appendRepair(t, fixture, reconciliationActor(), "attempt-forged", nil)
		requireFailsBeforeProbe(t, fixture, "attempt id")
	})
	t.Run("forged-repair-kind", func(t *testing.T) {
		fixture := setupRetryPendingFixture(t, "attempt-repair-5")
		appendRepair(t, fixture, reconciliationActor(), "", func(payload map[string]any) { payload["repairKind"] = "manual-edit" })
		requireFailsBeforeProbe(t, fixture, "repairKind")
	})
	t.Run("forged-source-sequence", func(t *testing.T) {
		fixture := setupRetryPendingFixture(t, "attempt-repair-6")
		appendRepair(t, fixture, reconciliationActor(), "", func(payload map[string]any) { payload["sourceJournalSequence"] = float64(0) })
		requireFailsBeforeProbe(t, fixture, "sourceJournalSequence")
	})
	// sourceJournalSequence must be a canonical unsigned decimal integer in
	// the raw journal bytes: non-canonical notations that decode to the same
	// or another number all fail closed before Probe.
	for name, literal := range map[string]string{
		"fraction":            "4.0",
		"exponent":            "4e0",
		"negative":            "-4",
		"leading-zero-string": `"05"`,
		"string":              `"4"`,
		"wrong-value":         "3",
	} {
		t.Run("non-canonical-sequence-"+name, func(t *testing.T) {
			fixture := setupRetryPendingFixture(t, "attempt-repair-notation")
			appendRepair(t, fixture, reconciliationActor(), "", nil)
			mutateJournalLineContaining(t, fixture, `"type":"`+lifecycle.RepairAuditEventType+`"`, `"sourceJournalSequence":4`, `"sourceJournalSequence":`+literal)
			requireFailsBeforeProbe(t, fixture, "sourceJournalSequence")
		})
	}
	t.Run("non-canonical-sequence-leading-zero-number", func(t *testing.T) {
		// The numeric leading-zero literal 04 is invalid JSON under
		// RFC 8259, but the JCS number parser behind canonical admission
		// (strconv.ParseFloat based) accepts it, so the canonical admission
		// gate lets the raw line through. The strict authoritative journal
		// decode inside runstore (encoding/json) therefore fail-closes this
		// input first with a decode journal record error — earlier than both
		// the execution canonical admission rejection and the repair-layer
		// sourceJournalSequence notation check. That earlier rejection layer
		// is the expected fail-closed behavior: the run is refused before
		// any review findings admission, and every valid JSON non-canonical
		// notation still reaches the repair-layer sentinel in the notation
		// table above.
		fixture := setupRetryPendingFixture(t, "attempt-repair-notation")
		appendRepair(t, fixture, reconciliationActor(), "", nil)
		mutateJournalLineContaining(t, fixture, `"type":"`+lifecycle.RepairAuditEventType+`"`, `"sourceJournalSequence":4`, `"sourceJournalSequence":04`)
		requireFailsBeforeProbe(t, fixture, "decode journal record")
	})
}

// mutateRawJournalLines rewrites events.jsonl through a line-level callback,
// keeping the trailing newline so journal shape is preserved.
func mutateRawJournalLines(t *testing.T, fixture executionFixture, mutate func(lines []string)) {
	t.Helper()
	path := filepath.Join(fixture.runDir, "events.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	mutate(lines)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// mutateJournalLineContaining applies one old->new replacement inside the
// first journal line that carries marker, so a single event line can be
// forged without touching the rest of the journal.
func mutateJournalLineContaining(t *testing.T, fixture executionFixture, marker, old, new string) {
	t.Helper()
	mutateRawJournalLines(t, fixture, func(lines []string) {
		for index, line := range lines {
			if strings.Contains(line, marker) {
				if !strings.Contains(line, old) {
					t.Fatalf("journal line with %q does not contain %q", marker, old)
				}
				lines[index] = strings.Replace(line, old, new, 1)
				return
			}
		}
		t.Fatalf("journal has no line containing %q", marker)
	})
}

func TestRunRejectsCrossDirectoryRunIdentityBeforeProbe(t *testing.T) {
	fixture := newExecutionFixture(t, false)
	tampered := inspectState(t, fixture)
	tampered.RunID = "run-forged-directory"
	writeSnapshotFile(t, fixture, tampered)
	requireFailsBeforeProbe(t, fixture, "identity does not match the requested run")
}

func TestRunRejectsForgedPlanningAuthorityBeforeProbe(t *testing.T) {
	const specAcceptedType = `"type":"planning.spec-accepted"`
	const inputsFrozenType = `"type":"planning.inputs-frozen"`
	const planningActorLiteral = `"actor":{"type":"system","id":"marshal-planning"}`
	forgedDigest := "sha256:" + strings.Repeat("f", 64)

	t.Run("first-event-type-not-spec-accepted", func(t *testing.T) {
		fixture := newExecutionFixture(t, false)
		mutateJournalLineContaining(t, fixture, specAcceptedType, specAcceptedType, `"type":"planning.inputs-frozen"`)
		requireFailsBeforeProbe(t, fixture, "planning.spec-accepted")
	})
	t.Run("spec-accepted-actor-omitted", func(t *testing.T) {
		fixture := newExecutionFixture(t, false)
		mutateJournalLineContaining(t, fixture, specAcceptedType, ","+planningActorLiteral, "")
		requireFailsBeforeProbe(t, fixture, "system/marshal-planning")
	})
	t.Run("spec-accepted-actor-id-forged", func(t *testing.T) {
		fixture := newExecutionFixture(t, false)
		mutateJournalLineContaining(t, fixture, specAcceptedType, `"id":"marshal-planning"`, `"id":"marshal-planning-forged"`)
		requireFailsBeforeProbe(t, fixture, "system/marshal-planning")
	})
	t.Run("spec-accepted-spec-digest-mismatch", func(t *testing.T) {
		fixture := newExecutionFixture(t, false)
		state := inspectState(t, fixture)
		mutateJournalLineContaining(t, fixture, specAcceptedType, `"specDigest":"`+state.SpecDigest+`"`, `"specDigest":"`+forgedDigest+`"`)
		requireFailsBeforeProbe(t, fixture, "planning.spec-accepted payload specDigest")
	})
	t.Run("inputs-frozen-actor-omitted", func(t *testing.T) {
		fixture := newExecutionFixture(t, false)
		mutateJournalLineContaining(t, fixture, inputsFrozenType, ","+planningActorLiteral, "")
		requireFailsBeforeProbe(t, fixture, "system/marshal-planning")
	})
	t.Run("inputs-frozen-actor-id-forged", func(t *testing.T) {
		fixture := newExecutionFixture(t, false)
		mutateJournalLineContaining(t, fixture, inputsFrozenType, `"id":"marshal-planning"`, `"id":"marshal-planning-forged"`)
		requireFailsBeforeProbe(t, fixture, "system/marshal-planning")
	})
	t.Run("inputs-frozen-missing", func(t *testing.T) {
		fixture := newExecutionFixture(t, false)
		mutateJournalLineContaining(t, fixture, inputsFrozenType, inputsFrozenType, `"type":"planning.spec-accepted"`)
		requireFailsBeforeProbe(t, fixture, "exactly one planning.inputs-frozen")
	})
	t.Run("inputs-frozen-field-mismatch", func(t *testing.T) {
		for field, forged := range map[string]string{
			"specDigest":       `"specDigest":"` + forgedDigest + `"`,
			"policyDigest":     `"policyDigest":"` + forgedDigest + `"`,
			"capabilityDigest": `"capabilityDigest":"` + forgedDigest + `"`,
			"baseSha":          `"baseSha":"` + strings.Repeat("9", 40) + `"`,
			"worktreePath":     `"worktreePath":"/forged/worktree/path"`,
		} {
			t.Run(field, func(t *testing.T) {
				fixture := newExecutionFixture(t, false)
				state := inspectState(t, fixture)
				original := map[string]string{
					"specDigest":       `"specDigest":"` + state.SpecDigest + `"`,
					"policyDigest":     `"policyDigest":"` + state.PolicyDigest + `"`,
					"capabilityDigest": `"capabilityDigest":"` + state.CapabilityDigest + `"`,
					"baseSha":          `"baseSha":"` + state.BaseSHA + `"`,
					"worktreePath":     `"worktreePath":"` + state.WorktreePath + `"`,
				}[field]
				mutateJournalLineContaining(t, fixture, inputsFrozenType, original, forged)
				requireFailsBeforeProbe(t, fixture, "planning.inputs-frozen")
			})
		}
	})
}

func TestRunRejectsUnsafeRawJournalBytesBeforeProbe(t *testing.T) {
	t.Run("invalid-utf8", func(t *testing.T) {
		fixture := newExecutionFixture(t, false)
		mutateRawJournalLines(t, fixture, func(lines []string) {
			lines[0] = lines[0][:20] + "\xff" + lines[0][20:]
		})
		requireFailsBeforeProbe(t, fixture, "canonical JSON admission")
	})
	t.Run("nested-duplicate-member", func(t *testing.T) {
		fixture := newExecutionFixture(t, false)
		state := inspectState(t, fixture)
		mutateJournalLineContaining(t, fixture, `"type":"planning.inputs-frozen"`,
			`"worktreePath":"`+state.WorktreePath+`"`,
			`"worktreePath":"`+state.WorktreePath+`","nested":{"dup":1,"dup":2}`)
		requireFailsBeforeProbe(t, fixture, "canonical JSON admission")
	})
	t.Run("trailing-second-value", func(t *testing.T) {
		fixture := newExecutionFixture(t, false)
		mutateRawJournalLines(t, fixture, func(lines []string) {
			lines[0] = lines[0] + `{"trailing":"second-value"}`
		})
		requireFailsBeforeProbe(t, fixture, "canonical JSON admission")
	})
}

func TestRunRejectsForgedEvidenceAuthorityBeforeProbe(t *testing.T) {
	t.Run("verification-completed-actor-omitted", func(t *testing.T) {
		fixture := newExecutionFixture(t, false)
		appendRunEvents(t, fixture,
			step("worker.started", domain.StateRunning, workerRunnerActor(), "attempt-vauth", map[string]any{"adapterId": "fixture"}),
			step("worker.completed", domain.StateVerifying, workerRunnerActor(), "attempt-vauth", verificationPayload()),
			step("verification.completed", domain.StateReviewPending, nil, "", map[string]any{}))
		// The run state gate only admits REWORK_REQUESTED runs, so a
		// legitimate round-1 review.rework origin brings the journal back to
		// an admittable state; admission then reaches the journal authority
		// check, which must reject the omitted verifier actor itself instead
		// of the state gate rejecting the fixture first.
		appendReviewReworkEvent(t, fixture, writeDecisionFile(t, fixture, reworkDecisionFixture(inspectState(t, fixture), 1)))
		requireFailsBeforeProbe(t, fixture, "system/marshal-verifier")
	})
	t.Run("verification-completed-actor-forged", func(t *testing.T) {
		fixture := newExecutionFixture(t, false)
		appendRunEvents(t, fixture,
			step("worker.started", domain.StateRunning, workerRunnerActor(), "attempt-vauth-forged", map[string]any{"adapterId": "fixture"}),
			step("worker.completed", domain.StateVerifying, workerRunnerActor(), "attempt-vauth-forged", verificationPayload()),
			step("verification.completed", domain.StateReviewPending, workerRunnerActor(), "", map[string]any{}))
		// Same admission path as the omitted case: the legitimate rework
		// origin keeps the run admittable so the forged worker-runner actor
		// is rejected by the verifier authority check itself.
		appendReviewReworkEvent(t, fixture, writeDecisionFile(t, fixture, reworkDecisionFixture(inspectState(t, fixture), 1)))
		requireFailsBeforeProbe(t, fixture, "system/marshal-verifier")
	})
	t.Run("publication-completed-actor-omitted", func(t *testing.T) {
		fixture := setupReviewPendingFixture(t, "attempt-pauth")
		acceptDecision := reworkDecisionFixture(inspectState(t, fixture), 1)
		acceptDecision.Verdict, acceptDecision.Summary = "accept", "accepted for publication"
		acceptDecision.BlockingFindings = []domain.Finding{}
		acceptDecision.PublicationRecommendation = "publish"
		acceptPayload := reviewReworkPayload(writeDecisionFile(t, fixture, acceptDecision))
		acceptPayload["verdict"] = "accept"
		const headSHA = "abcdef0123456789abcdef0123456789abcdef01"
		appendRunEvents(t, fixture,
			step("review.accept", domain.StatePublishing, reviewActor(), "", acceptPayload),
			step("publication.completed", domain.StatePublished, nil, "", publicationPayload(headSHA)),
			step("publication.checks-requested", domain.StateCIPending, publisherActor(), "", map[string]any{"headSha": headSHA}),
			step("publication.checks-failed", domain.StateReworkRequested, publisherActor(), "", map[string]any{"headSha": headSHA}))
		requireFailsBeforeProbe(t, fixture, "publisher/marshal-github-publisher")
	})
}

func mutateRawJournalLine(t *testing.T, fixture executionFixture, typePrefix, injectedField string) {
	t.Helper()
	path := filepath.Join(fixture.runDir, "events.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	for index, line := range lines {
		if strings.Contains(line, `"type":"`+typePrefix+`"`) {
			lines[index] = strings.TrimSuffix(line, "}") + "," + injectedField + "}"
			if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatalf("journal has no event with type %q", typePrefix)
}

// appendWorkerStartedAt appends one worker.started event with an explicit
// timestamp, letting the orphan recovery tests control the driver-liveness
// signal that admission derives from the journal tail.
func appendWorkerStartedAt(t *testing.T, fixture executionFixture, attemptID string, at time.Time) {
	t.Helper()
	state := inspectState(t, fixture)
	eventID, err := domain.NewID("event")
	if err != nil {
		t.Fatal(err)
	}
	event := domain.RunEvent{
		APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent, EventID: eventID, RunID: fixture.input.RunID,
		AttemptID: attemptID, Sequence: state.Sequence + 1, Type: "worker.started", StateFrom: state.State, StateTo: domain.StateRunning,
		Timestamp: at, Actor: workerRunnerActor(), Payload: map[string]any{"adapterId": "fixture"},
	}
	if _, err := lifecycle.Replay(state, event); err != nil {
		t.Fatalf("append worker.started: replay: %v", err)
	}
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	appendRawJournalBytes(t, fixture, string(data)+"\n")
}

// setupOrphanedRunningFixture starts one attempt whose worker.started event
// carries a stale timestamp (the driver died) and materializes the attempt
// directory the live runner would have created before the journal append.
func setupOrphanedRunningFixture(t *testing.T, fixture executionFixture, attemptID string) {
	t.Helper()
	appendWorkerStartedAt(t, fixture, attemptID, time.Unix(100, 0).UTC())
	attemptDir := filepath.Join(fixture.runDir, "attempts", attemptID)
	if err := os.MkdirAll(filepath.Join(attemptDir, "control", "output"), 0o700); err != nil {
		t.Fatal(err)
	}
	request := mustJSON(t, map[string]any{"attemptNumber": 1, "executionProfile": "workspace-write"})
	if err := os.WriteFile(filepath.Join(attemptDir, "worker-request.json"), request, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestOrphanedRunningAttemptRecoveryIsFencingCapable(t *testing.T) {
	for name, threshold := range map[string]time.Duration{"explicit-short-threshold": time.Second, "default-threshold": 0} {
		t.Run(name, func(t *testing.T) {
			fixture := newExecutionFixture(t, false)
			fixture.input.OrphanStalenessThreshold = threshold
			setupOrphanedRunningFixture(t, fixture, "attempt-orphan")
			result, err := Run(context.Background(), fixture.input)
			if err != nil {
				t.Fatalf("orphan recovery rejected a stale RUNNING attempt: %v", err)
			}
			if result.State.State != domain.StateVerifying || result.State.AttemptsUsed != 2 {
				t.Fatalf("state = %+v", result.State)
			}
			if result.AttemptID == "" || result.AttemptID == "attempt-orphan" {
				t.Fatalf("recovery reused the orphaned attempt id: %q", result.AttemptID)
			}
			events, _, readErr := runstore.New(fixture.input.StateRoot).ReadEvents(fixture.input.RunID)
			if readErr != nil {
				t.Fatal(readErr)
			}
			var orphanFailed, recoveredStarted *domain.RunEvent
			for index := range events {
				event := &events[index]
				if event.Type == "worker.failed" && event.AttemptID == "attempt-orphan" {
					orphanFailed = event
				}
				if event.Type == "worker.started" && event.AttemptID == result.AttemptID {
					recoveredStarted = event
				}
			}
			if orphanFailed == nil || orphanFailed.StateFrom != domain.StateRunning || orphanFailed.StateTo != domain.StateRetryPending {
				t.Fatalf("orphan worker.failed event = %+v", orphanFailed)
			}
			if orphanFailed.Payload["orphaned"] != true || orphanFailed.Payload["fencingGeneration"] != float64(1) {
				t.Fatalf("orphan worker.failed payload = %+v", orphanFailed.Payload)
			}
			if orphanFailed.Payload["adapterId"] != "fixture" || orphanFailed.Payload["failureKind"] != string(port.FailureKindConnectionFailure) || orphanFailed.Payload["retryDisposition"] != string(port.RetryDispositionRetryable) {
				t.Fatalf("orphan retry lacks typed failure authority: %+v", orphanFailed.Payload)
			}
			if signature, _ := orphanFailed.Payload["failureSignature"].(string); !isCanonicalSHA256(signature) {
				t.Fatalf("orphan retry failureSignature = %q", signature)
			}
			if recoveredStarted == nil || recoveredStarted.StateFrom != domain.StateRetryPending {
				t.Fatalf("recovered worker.started event = %+v", recoveredStarted)
			}
			if recoveredStarted.Payload["fencingGeneration"] != float64(2) || recoveredStarted.Payload["supersedesAttempt"] != "attempt-orphan" || recoveredStarted.Payload["orphanRecovery"] != true {
				t.Fatalf("recovered worker.started payload = %+v", recoveredStarted.Payload)
			}
			_, requestData := attemptReviewFindings(t, fixture, result.AttemptID)
			var request struct {
				PreviousAttemptID string `json:"previousAttemptId"`
			}
			if err := json.Unmarshal(requestData, &request); err != nil || request.PreviousAttemptID != "attempt-orphan" {
				t.Fatalf("worker request previousAttemptId = %q err = %v", request.PreviousAttemptID, err)
			}
		})
	}
}

func TestOrphanedRecoveryQuarantinesStaleOutputsAndCompletesTheChain(t *testing.T) {
	fixture := newExecutionFixture(t, false)
	fixture.input.OrphanStalenessThreshold = time.Second
	setupOrphanedRunningFixture(t, fixture, "attempt-orphan-late")
	orphanDir := filepath.Join(fixture.runDir, "attempts", "attempt-orphan-late")
	lateResult := mustJSON(t, map[string]any{
		"apiVersion": "marshal.dev/v1alpha1", "kind": "WorkerResult",
		"taskId": "TASK-1", "runId": fixture.input.RunID, "attemptId": "attempt-orphan-late",
		"adapter":              map[string]any{"id": "fixture", "executable": "/fixture", "version": "1"},
		"status":               "completed",
		"summary":              "late orphaned output",
		"declaredChangedFiles": []string{},
		"declaredArtifacts":    []any{},
		"declaredCommands":     []any{},
		"declaredRisks":        []string{},
		"startedAt":            "2026-08-04T00:00:00Z",
		"completedAt":          "2026-08-04T00:00:01Z",
	})
	for name, data := range map[string][]byte{"worker-result.json": lateResult, "worktree-snapshot.json": []byte("{\"snapshot\":\"stale\"}\n")} {
		if err := os.WriteFile(filepath.Join(orphanDir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	result, err := Run(context.Background(), fixture.input)
	if err != nil {
		t.Fatalf("orphan recovery with stale outputs rejected: %v", err)
	}
	if result.State.State != domain.StateVerifying {
		t.Fatalf("state = %+v", result.State)
	}
	for _, name := range []string{"worker-result.json", "worktree-snapshot.json"} {
		if _, statErr := os.Stat(filepath.Join(orphanDir, name)); !os.IsNotExist(statErr) {
			t.Fatalf("orphaned %s still reachable for evidence collection: %v", name, statErr)
		}
		if _, statErr := os.Stat(filepath.Join(orphanDir, "diagnostics", "quarantined-"+name)); statErr != nil {
			t.Fatalf("quarantined %s missing from diagnostics: %v", name, statErr)
		}
	}
	quarantined, err := os.ReadFile(filepath.Join(orphanDir, "diagnostics", "quarantined-worker-result.json"))
	if err != nil || !strings.Contains(string(quarantined), "attempt-orphan-late") {
		t.Fatalf("quarantined WorkerResult = %q err = %v", quarantined, err)
	}
	diagnosticsRaw, err := os.ReadFile(filepath.Join(orphanDir, "diagnostics", "orphan-diagnostics.json"))
	if err != nil {
		t.Fatal(err)
	}
	var diagnostics struct {
		Reason           string   `json:"reason"`
		AttemptID        string   `json:"attemptId"`
		QuarantinedFiles []string `json:"quarantinedFiles"`
	}
	if err := json.Unmarshal(diagnosticsRaw, &diagnostics); err != nil {
		t.Fatal(err)
	}
	if diagnostics.Reason != "orphaned-attempt-stale-outputs" || diagnostics.AttemptID != "attempt-orphan-late" || len(diagnostics.QuarantinedFiles) != 2 {
		t.Fatalf("orphan diagnostics = %+v", diagnostics)
	}
	// Evidence-collection view: the review packet glob can only ever see the
	// recovered attempt's WorkerResult, never the quarantined orphan output.
	matches, err := filepath.Glob(filepath.Join(fixture.runDir, "attempts", "*", "worker-result.json"))
	if err != nil || len(matches) != 1 || !strings.Contains(matches[0], result.AttemptID) {
		t.Fatalf("evidence glob after quarantine = %+v err = %v", matches, err)
	}
	// The recovered attempt completes the normal chain to REVIEW_PENDING.
	appendRunEvent(t, fixture, "verification.completed", domain.StateReviewPending, verifierActor(), "", map[string]any{})
	final := inspectState(t, fixture)
	if final.State != domain.StateReviewPending || final.ReviewRound != 1 || final.CurrentAttemptID != result.AttemptID {
		t.Fatalf("state after recovered chain = %+v", final)
	}
}

func TestOrphanedRecoveryRejectsLiveRunningAttempt(t *testing.T) {
	for name, setup := range map[string]func(t *testing.T) executionFixture{
		"fresh-timestamp-short-threshold": func(t *testing.T) executionFixture {
			fixture := newExecutionFixture(t, false)
			fixture.input.OrphanStalenessThreshold = time.Second
			appendWorkerStartedAt(t, fixture, "attempt-live", time.Now().UTC())
			return fixture
		},
		"fresh-timestamp-default-threshold": func(t *testing.T) executionFixture {
			fixture := newExecutionFixture(t, false)
			appendWorkerStartedAt(t, fixture, "attempt-live", time.Now().UTC())
			return fixture
		},
	} {
		t.Run(name, func(t *testing.T) {
			requireFailsBeforeProbe(t, setup(t), "cannot start a worker attempt", "live driver evidence")
		})
	}
}

// TestOrphanedRecoveryBlocksWhenBudgetExhausted keeps the R1 intent —
// attempt-budget exhaustion blocks orphan recovery — with fixture budgets
// that satisfy the attempt-budget-overcommitted semantic rule: maxAttempts
// must be at least 1 + maxOperationalRetries + maxReworkRounds. The default
// fixture budgets (maxAttempts=2, maxOperationalRetries=1, maxReworkRounds=0)
// meet exactly that minimum, and the journal drives a second attempt so
// AttemptsUsed reaches maxAttempts before the orphan decision. R-11: the
// recovery layer is reachable here (stale RUNNING tail established first),
// so the budget sentinel is asserted at the recovery layer ahead of Probe.
func TestOrphanedRecoveryBlocksWhenBudgetExhausted(t *testing.T) {
	t.Run("attempt-budget-exhausted", func(t *testing.T) {
		fixture := newExecutionFixture(t, false)
		fixture.input.OrphanStalenessThreshold = time.Second
		appendRetrySegment(t, fixture, "attempt-exhausted-1")
		appendWorkerStartedAt(t, fixture, "attempt-exhausted-2", time.Unix(200, 0).UTC())
		state := inspectState(t, fixture)
		if state.State != domain.StateRunning || state.AttemptsUsed != 2 {
			t.Fatalf("fixture precondition = %+v", state)
		}
		adapter := &countingAdapter{delegate: fixture.input.Adapter.(*fixtureAdapter)}
		fixture.input.Adapter = adapter
		_, err := Run(context.Background(), fixture.input)
		if err == nil || !strings.Contains(err.Error(), "attempt budget exhausted") {
			t.Fatalf("Run error = %v, want attempt budget exhaustion", err)
		}
		if adapter.probes != 0 || adapter.runs != 0 {
			t.Fatalf("adapter invoked while closing exhausted budget: probes=%d runs=%d", adapter.probes, adapter.runs)
		}
		state = inspectState(t, fixture)
		if state.State != domain.StateBlocked || state.AttemptsUsed != 2 || state.OperationalRetriesUsed != 1 {
			t.Fatalf("attempt-budget terminal state = %+v", state)
		}
		outcomeData, err := os.ReadFile(filepath.Join(fixture.runDir, "outcome.json"))
		if err != nil || len(outcomeData) == 0 {
			t.Fatalf("attempt-budget Outcome missing: %v", err)
		}
	})
}

func TestOrphanedRecoveryClosesAtOperationalRetryBudget(t *testing.T) {
	fixture := newExecutionFixtureWithOptions(t, false, executionFixtureOptions{
		preferredAdapter: "fixture", fallbackAdapters: []string{}, capabilityAdapterID: "fixture",
		maxAttempts: 3, maxOperationalRetries: 1,
	})
	fixture.input.OrphanStalenessThreshold = time.Second
	appendRetrySegment(t, fixture, "attempt-retry-used")
	appendWorkerStartedAt(t, fixture, "attempt-orphan-budget", time.Unix(200, 0).UTC())
	before := inspectState(t, fixture)
	if before.OperationalRetriesUsed != 1 || before.AttemptsUsed != 2 {
		t.Fatalf("fixture budget precondition = %+v", before)
	}
	result, err := Run(context.Background(), fixture.input)
	if err == nil || !strings.Contains(err.Error(), "operator intervention") {
		t.Fatalf("Run error = %v, want explicit intervention", err)
	}
	if result.State.State != domain.StateBlocked || result.State.OperationalRetriesUsed != 1 || result.State.AttemptsUsed != 2 {
		t.Fatalf("budget-exhausted recovery state = %+v", result.State)
	}
	events, _, readErr := runstore.New(fixture.input.StateRoot).ReadEvents(fixture.input.RunID)
	if readErr != nil {
		t.Fatal(readErr)
	}
	last := events[len(events)-1]
	if last.Type != "worker.failed" || last.StateFrom != domain.StateRunning || last.StateTo != domain.StateBlocked || last.AttemptID != "attempt-orphan-budget" {
		t.Fatalf("budget terminal event = %+v", last)
	}
	if last.Payload["terminalReason"] != "orphan-operational-retry-budget-exhausted" || last.Payload["operationalRetriesUsed"] != float64(1) || last.Payload["maxOperationalRetries"] != float64(1) {
		t.Fatalf("budget terminal payload = %+v", last.Payload)
	}
	outcomeData, outcomeErr := os.ReadFile(filepath.Join(fixture.runDir, "outcome.json"))
	if outcomeErr != nil {
		t.Fatalf("terminal Outcome missing: %v", outcomeErr)
	}
	var outcome domain.OutcomeBundle
	if json.Unmarshal(outcomeData, &outcome) != nil || outcome.TerminalState != domain.StateBlocked || outcome.Verdict != "blocked" {
		t.Fatalf("terminal Outcome = %+v", outcome)
	}
}

func TestRetryPendingOrphanQuarantineReconcilesAfterEventAppendCrash(t *testing.T) {
	fixture := newExecutionFixtureWithOptions(t, false, executionFixtureOptions{
		preferredAdapter: "fixture", fallbackAdapters: []string{}, capabilityAdapterID: "fixture", maxAttempts: 3, maxOperationalRetries: 2,
	})
	fixture.input.OrphanStalenessThreshold = time.Second
	appendWorkerStartedAt(t, fixture, "attempt-orphan-retry-crash", time.Unix(200, 0).UTC())
	fixture.input.AfterOrphanRetryAppend = func() error { return errors.New("retry append crash") }
	result, err := Run(context.Background(), fixture.input)
	if err == nil || !strings.Contains(err.Error(), "retry post-append failure") || result.State.State != domain.StateRetryPending {
		t.Fatalf("crash result = %+v err=%v", result, err)
	}
	events, _, err := runstore.New(fixture.input.StateRoot).ReadEvents(fixture.input.RunID)
	if err != nil {
		t.Fatal(err)
	}
	last := events[len(events)-1]
	binding, err := quarantineBindingFromPayload(last.Payload)
	if err != nil || binding.Files == nil || len(binding.Files) != 0 {
		t.Fatalf("retry event empty binding = %+v err=%v payload=%+v", binding, err, last.Payload)
	}
	outputs, ok := last.Payload["quarantinedOutputs"].([]any)
	if !ok || outputs == nil || len(outputs) != 0 {
		t.Fatalf("retry event quarantinedOutputs must be strict []: %#v", last.Payload["quarantinedOutputs"])
	}
	manifest := filepath.Join(fixture.runDir, "attempts", last.AttemptID, "diagnostics", "orphan-quarantine-transaction.json")
	var transaction quarantineTransaction
	data, err := os.ReadFile(manifest)
	if err != nil || json.Unmarshal(data, &transaction) != nil || transaction.Files == nil || len(transaction.Files) != 0 {
		t.Fatalf("durable empty transaction = %+v data=%s err=%v", transaction, data, err)
	}
	fixture.input.AfterOrphanRetryAppend = nil
	result, err = Run(context.Background(), fixture.input)
	if err != nil || result.State.State != domain.StateVerifying {
		t.Fatalf("retry restart = %+v err=%v", result, err)
	}
}

func TestRetryPendingOrphanAuthorityRejectsBeforeAnyCompensationOrDispatchSideEffect(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*domain.RunEvent, domain.RunState)
		fragments []string
	}{
		{
			name: "legacy untyped authority",
			mutate: func(event *domain.RunEvent, _ domain.RunState) {
				for _, key := range []string{"adapterId", "failureKind", "retryDisposition", "failureSignature"} {
					delete(event.Payload, key)
				}
				event.Payload["error"] = "legacy failure"
			},
			fragments: []string{"retry admission", "typed failure"},
		},
		{
			name: "signature mismatch",
			mutate: func(event *domain.RunEvent, _ domain.RunState) {
				event.Payload["failureSignature"] = "sha256:" + strings.Repeat("0", 64)
			},
			fragments: []string{"retry admission", "signature"},
		},
		{
			name: "future not-before hold",
			mutate: func(event *domain.RunEvent, _ domain.RunState) {
				notBefore := time.Now().UTC().Add(time.Hour).Truncate(time.Second).Add(987654321 * time.Nanosecond)
				failure := port.AdapterFailure{Adapter: port.AdapterID("fixture"), Kind: port.FailureKindConnectionFailure, Disposition: port.RetryDispositionRetryable, NotBefore: notBefore}
				event.Payload["notBefore"] = notBefore.Format(time.RFC3339Nano)
				event.Payload["error"] = failure.Error()
			},
			fragments: []string{"retry admission", "not ready"},
		},
		{
			name: "nonretryable authority",
			mutate: func(event *domain.RunEvent, state domain.RunState) {
				for key, value := range persistedFailurePayload(t, state, port.AdapterID("fixture"), port.FailureKindProtocolInvalid, port.RetryDispositionDoNotRetry, nil, nil) {
					event.Payload[key] = value
				}
			},
			fragments: []string{"retry admission", "not retryable"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newExecutionFixtureWithOptions(t, false, executionFixtureOptions{
				preferredAdapter: "fixture", fallbackAdapters: []string{}, capabilityAdapterID: "fixture", maxAttempts: 3, maxOperationalRetries: 2,
			})
			fixture.input.OrphanStalenessThreshold = time.Second
			appendWorkerStartedAt(t, fixture, "attempt-orphan-authority-hold", time.Unix(200, 0).UTC())
			fixture.input.AfterOrphanRetryAppend = func() error { return errors.New("retry append crash") }
			result, err := Run(context.Background(), fixture.input)
			if err == nil || !strings.Contains(err.Error(), "retry post-append failure") || result.State.State != domain.StateRetryPending {
				t.Fatalf("orphan crash seed = %+v err=%v", result, err)
			}
			fixture.input.AfterOrphanRetryAppend = nil
			state := inspectState(t, fixture)
			mutateLastWorkerFailure(t, fixture, func(event *domain.RunEvent) { test.mutate(event, state) })

			attemptsRoot := filepath.Join(fixture.runDir, "attempts")
			diagnostics := filepath.Join(attemptsRoot, state.CurrentAttemptID, "diagnostics")
			if err := os.RemoveAll(diagnostics); err != nil {
				t.Fatal(err)
			}
			beforeAttempts := capturePersistedTree(t, attemptsRoot)
			statePath := filepath.Join(fixture.runDir, "state.json")
			journalPath := filepath.Join(fixture.runDir, "events.jsonl")
			beforeState := capturePersistedPath(t, statePath)
			beforeJournal := capturePersistedPath(t, journalPath)

			adapter := &countingAdapter{delegate: fixture.input.Adapter.(*fixtureAdapter)}
			input := fixture.input
			input.Adapter = adapter
			binderCalls := 0
			input.DispatchBinder = dispatchBinderFunc(func(context.Context, string, string, string, domain.SandboxRequirements) (*DispatchBinding, error) {
				binderCalls++
				return nil, errors.New("dispatch binder must not run")
			})
			_, err = Run(context.Background(), input)
			if err == nil {
				t.Fatal("orphan retry authority unexpectedly admitted")
			}
			for _, fragment := range test.fragments {
				if !strings.Contains(err.Error(), fragment) {
					t.Fatalf("rejection %q does not contain %q", err, fragment)
				}
			}
			if binderCalls != 0 || adapter.probes != 0 || adapter.runs != 0 {
				t.Fatalf("rejected authority crossed dispatch/adapter boundary: binder=%d probes=%d runs=%d", binderCalls, adapter.probes, adapter.runs)
			}
			requirePersistedPathUnchanged(t, "state snapshot", beforeState, capturePersistedPath(t, statePath))
			requirePersistedPathUnchanged(t, "journal", beforeJournal, capturePersistedPath(t, journalPath))
			requirePersistedTreeUnchanged(t, attemptsRoot, beforeAttempts)
			if _, err := os.Stat(diagnostics); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("rejected authority recreated quarantine diagnostics: %v", err)
			}
		})
	}
}

func TestOrphanBudgetTerminalTransactionRecoversAfterRestart(t *testing.T) {
	fixture := newExecutionFixtureWithOptions(t, false, executionFixtureOptions{
		preferredAdapter: "fixture", fallbackAdapters: []string{}, capabilityAdapterID: "fixture", maxAttempts: 3, maxOperationalRetries: 1,
	})
	fixture.input.OrphanStalenessThreshold = time.Second
	appendRetrySegment(t, fixture, "attempt-retry-before-crash")
	appendWorkerStartedAt(t, fixture, "attempt-orphan-crash", time.Unix(200, 0).UTC())
	fixture.input.AfterOrphanTerminalAppend = func() error { return errors.New("crash fixture") }
	result, err := Run(context.Background(), fixture.input)
	if err == nil || !strings.Contains(err.Error(), "post-append failure") || result.State.State != domain.StateBlocked {
		t.Fatalf("crash result = %+v err = %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(fixture.runDir, "outcome.json")); !os.IsNotExist(err) {
		t.Fatalf("final Outcome unexpectedly visible before recovery: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.runDir, "outcome.json.pending")); err != nil {
		t.Fatalf("prepared Outcome not durable across crash: %v", err)
	}
	fixture.input.AfterOrphanTerminalAppend = nil
	result, err = Run(context.Background(), fixture.input)
	if err == nil || !strings.Contains(err.Error(), "operator intervention") || result.State.State != domain.StateBlocked {
		t.Fatalf("restart compensation result = %+v err = %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(fixture.runDir, "outcome.json")); err != nil {
		t.Fatalf("restart did not finalize Outcome: %v", err)
	}
	events, _, err := runstore.New(fixture.input.StateRoot).ReadEvents(fixture.input.RunID)
	if err != nil {
		t.Fatal(err)
	}
	terminal := 0
	for _, event := range events {
		if event.Type == "worker.failed" && event.StateTo == domain.StateBlocked {
			terminal++
		}
	}
	if terminal != 1 {
		t.Fatalf("terminal events = %d, want exactly one", terminal)
	}
}

func TestOrphanQuarantineTransactionRecoversBeforeTerminalAppend(t *testing.T) {
	fixture := newExecutionFixtureWithOptions(t, false, executionFixtureOptions{
		preferredAdapter: "fixture", fallbackAdapters: []string{}, capabilityAdapterID: "fixture", maxAttempts: 3, maxOperationalRetries: 1,
	})
	fixture.input.OrphanStalenessThreshold = time.Second
	appendRetrySegment(t, fixture, "attempt-retry-before-quarantine-crash")
	appendWorkerStartedAt(t, fixture, "attempt-orphan-quarantine-crash", time.Unix(200, 0).UTC())
	attemptDir := filepath.Join(fixture.runDir, "attempts", "attempt-orphan-quarantine-crash")
	if err := os.MkdirAll(attemptDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"worker-result.json", "worktree-snapshot.json"} {
		if err := os.WriteFile(filepath.Join(attemptDir, name), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fixture.input.AfterOrphanQuarantine = func() error { return errors.New("crash after quarantine") }
	_, err := Run(context.Background(), fixture.input)
	if err == nil || !strings.Contains(err.Error(), "post-quarantine failure") {
		t.Fatalf("Run error = %v", err)
	}
	if state := inspectState(t, fixture); state.State != domain.StateRunning {
		t.Fatalf("journal advanced before terminal append: %+v", state)
	}
	transaction := filepath.Join(attemptDir, "diagnostics", "orphan-quarantine-transaction.json")
	data, err := os.ReadFile(transaction)
	if err != nil || !strings.Contains(string(data), "worker-result.json") || !strings.Contains(string(data), "worktree-snapshot.json") {
		t.Fatalf("durable quarantine transaction = %q err=%v", data, err)
	}
	fixture.input.AfterOrphanQuarantine = nil
	result, err := Run(context.Background(), fixture.input)
	if err == nil || result.State.State != domain.StateBlocked {
		t.Fatalf("restart result = %+v err=%v", result, err)
	}
	events, _, err := runstore.New(fixture.input.StateRoot).ReadEvents(fixture.input.RunID)
	if err != nil {
		t.Fatal(err)
	}
	last := events[len(events)-1]
	quarantined, _ := last.Payload["quarantinedOutputs"].([]any)
	if len(quarantined) != 2 {
		t.Fatalf("terminal event lost quarantine binding: %+v", last.Payload)
	}
}

func TestCanonicalRunReplacementAfterQuarantineCannotReceiveOutcome(t *testing.T) {
	fixture := newExecutionFixtureWithOptions(t, false, executionFixtureOptions{
		preferredAdapter: "fixture", fallbackAdapters: []string{}, capabilityAdapterID: "fixture", maxAttempts: 3, maxOperationalRetries: 1,
	})
	fixture.input.OrphanStalenessThreshold = time.Second
	appendRetrySegment(t, fixture, "attempt-retry-before-authority-replace")
	appendWorkerStartedAt(t, fixture, "attempt-orphan-authority-replace", time.Unix(200, 0).UTC())
	oldRunDirectory := fixture.runDir + ".old"
	fixture.input.AfterOrphanQuarantine = func() error {
		fixture.input.AfterOrphanQuarantine = nil
		if err := os.Rename(fixture.runDir, oldRunDirectory); err != nil {
			return err
		}
		return os.Mkdir(fixture.runDir, 0o700)
	}
	if _, err := Run(context.Background(), fixture.input); err == nil || !strings.Contains(err.Error(), "authority") {
		t.Fatalf("canonical replacement was not rejected: %v", err)
	}
	for _, directory := range []string{fixture.runDir, oldRunDirectory} {
		for _, name := range []string{"outcome.json", "outcome.md", "outcome.json.pending", "outcome.md.pending"} {
			if _, err := os.Lstat(filepath.Join(directory, name)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("%s received Outcome bytes: %v", directory, err)
			}
		}
	}
}

func TestOrdinaryWorkerFailureBudgetClosureAndRestartCompensation(t *testing.T) {
	for _, tc := range []struct {
		name           string
		maxAttempts    int
		maxOperational int
		wantReason     string
	}{
		{name: "attempt", maxAttempts: 2, maxOperational: 1, wantReason: "attempt-budget-exhausted"},
		{name: "operational", maxAttempts: 3, maxOperational: 1, wantReason: "operational-retry-budget-exhausted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newTypedTransientFailureFixture(t, executionFixtureOptions{
				preferredAdapter: "fixture", fallbackAdapters: []string{}, capabilityAdapterID: "fixture",
				maxAttempts: tc.maxAttempts, maxOperationalRetries: tc.maxOperational,
			})
			first, err := Run(context.Background(), fixture.input)
			if err == nil || first.State.State != domain.StateRetryPending {
				t.Fatalf("first failure = %+v err=%v", first, err)
			}
			fixture.input.AfterWorkerTerminalAppend = func() error { return errors.New("ordinary terminal crash") }
			second, err := Run(context.Background(), fixture.input)
			if err == nil || !strings.Contains(err.Error(), "post-append failure") || second.State.State != domain.StateBlocked {
				t.Fatalf("terminal failure = %+v err=%v", second, err)
			}
			fixture.input.AfterWorkerTerminalAppend = nil
			recovered, err := Run(context.Background(), fixture.input)
			if err == nil || !strings.Contains(err.Error(), "operator intervention") || recovered.State.State != domain.StateBlocked {
				t.Fatalf("restart compensation = %+v err=%v", recovered, err)
			}
			state := inspectState(t, fixture)
			if state.AttemptsUsed != 2 || state.OperationalRetriesUsed != 1 {
				t.Fatalf("budget counters advanced past limits: %+v", state)
			}
			events, _, err := runstore.New(fixture.input.StateRoot).ReadEvents(fixture.input.RunID)
			if err != nil {
				t.Fatal(err)
			}
			last := events[len(events)-1]
			if last.Payload["terminalReason"] != tc.wantReason || last.Payload["budgetTerminal"] != true {
				t.Fatalf("terminal payload = %+v", last.Payload)
			}
			binding, err := quarantineBindingFromPayload(last.Payload)
			if err != nil || binding.AttemptID != last.AttemptID || binding.StaleSince == "" {
				t.Fatalf("terminal quarantine binding = %+v err=%v", binding, err)
			}
			manifest, err := os.ReadFile(filepath.Join(fixture.runDir, "attempts", last.AttemptID, "diagnostics", "orphan-quarantine-transaction.json"))
			if err != nil {
				t.Fatal(err)
			}
			digest, err := canonical.DigestJSON(manifest)
			if err != nil || digest != binding.TransactionDigest {
				t.Fatalf("event transaction digest = %s durable=%s err=%v", binding.TransactionDigest, digest, err)
			}
			if _, err := os.Stat(filepath.Join(fixture.runDir, "outcome.json")); err != nil {
				t.Fatalf("terminal Outcome missing after restart: %v", err)
			}
		})
	}
}

func TestOrdinaryTerminalQuarantineRecoversBeforeEventAppend(t *testing.T) {
	fixture := newTypedTransientFailureFixture(t, executionFixtureOptions{
		preferredAdapter: "fixture", fallbackAdapters: []string{}, capabilityAdapterID: "fixture",
		maxAttempts: 2, maxOperationalRetries: 1,
	})
	first, err := Run(context.Background(), fixture.input)
	if err == nil || first.State.State != domain.StateRetryPending {
		t.Fatalf("first failure = %+v err=%v", first, err)
	}
	fixture.input.AfterWorkerQuarantine = func() error { return errors.New("crash before terminal append") }
	second, err := Run(context.Background(), fixture.input)
	if err == nil || !strings.Contains(err.Error(), "post-quarantine failure") || second.State.State != domain.StateRunning {
		t.Fatalf("pre-append crash = %+v err=%v", second, err)
	}
	fixture.input.AfterWorkerQuarantine = nil
	fixture.input.OrphanStalenessThreshold = time.Nanosecond
	time.Sleep(time.Millisecond)
	recovered, err := Run(context.Background(), fixture.input)
	if err == nil || recovered.State.State != domain.StateBlocked {
		t.Fatalf("restart recovery = %+v err=%v", recovered, err)
	}
	if _, err := os.Stat(filepath.Join(fixture.runDir, "outcome.json")); err != nil {
		t.Fatalf("terminal Outcome missing: %v", err)
	}
}

func TestQuarantineIsImmutableAndRejectsUnsafeSources(t *testing.T) {
	t.Run("empty set is an immutable transaction", func(t *testing.T) {
		fixture := newExecutionFixture(t, false)
		lease := acquireFixtureLease(t, fixture)
		attemptID := "attempt-quarantine-empty"
		first, err := quarantineAttemptOutputs(lease, attemptID, time.Unix(100, 0))
		if err != nil {
			t.Fatal(err)
		}
		if len(first.Files) != 0 || first.TransactionDigest == "" || first.AttemptID != attemptID || first.StaleSince != time.Unix(100, 0).UTC().Format(time.RFC3339) {
			t.Fatalf("empty transaction binding = %+v", first)
		}
		manifest := filepath.Join(fixture.runDir, "attempts", attemptID, "diagnostics", "orphan-quarantine-transaction.json")
		if _, err := os.Stat(manifest); err != nil {
			t.Fatalf("empty transaction was not persisted: %v", err)
		}
		second, err := quarantineAttemptOutputs(lease, attemptID, time.Unix(100, 0))
		if err != nil || !reflect.DeepEqual(first, second) {
			t.Fatalf("empty transaction recovery = %+v err=%v", second, err)
		}
		if err := os.WriteFile(filepath.Join(filepath.Dir(filepath.Dir(manifest)), "worker-result.json"), []byte("late\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := quarantineAttemptOutputs(lease, attemptID, time.Unix(100, 0)); err == nil {
			t.Fatal("late output escaped the immutable empty transaction")
		}
	})
	t.Run("source replacement at install boundary is rejected", func(t *testing.T) {
		fixture := newExecutionFixture(t, false)
		lease := acquireFixtureLease(t, fixture)
		attemptID := "attempt-quarantine-source-race"
		attemptDir := filepath.Join(fixture.runDir, "attempts", attemptID)
		if err := os.MkdirAll(attemptDir, 0o700); err != nil {
			t.Fatal(err)
		}
		source := filepath.Join(attemptDir, "worker-result.json")
		if err := os.WriteFile(source, []byte("trusted\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		quarantineMutationHook = func(stage string) error {
			if stage != "file-install:worker-result.json" {
				return nil
			}
			quarantineMutationHook = nil
			if err := os.Rename(source, source+".original"); err != nil {
				return err
			}
			return os.WriteFile(source, []byte("replacement\n"), 0o600)
		}
		t.Cleanup(func() { quarantineMutationHook = nil })
		if _, err := quarantineAttemptOutputs(lease, attemptID, time.Unix(100, 0)); err == nil {
			t.Fatal("source replacement crossed the install boundary")
		}
		if _, err := os.Stat(filepath.Join(attemptDir, "diagnostics", "quarantined-worker-result.json")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("replacement source was installed: %v", err)
		}
		if data, err := os.ReadFile(source); err != nil || string(data) != "replacement\n" {
			t.Fatalf("replacement source was mutated: %q err=%v", data, err)
		}
	})
	t.Run("diagnostics replacement at manifest boundary receives zero bytes", func(t *testing.T) {
		fixture := newExecutionFixture(t, false)
		lease := acquireFixtureLease(t, fixture)
		attemptID := "attempt-quarantine-directory-race"
		attemptDir := filepath.Join(fixture.runDir, "attempts", attemptID)
		diagnostics := filepath.Join(attemptDir, "diagnostics")
		quarantineMutationHook = func(stage string) error {
			if stage != "manifest-install" {
				return nil
			}
			quarantineMutationHook = nil
			if err := os.Rename(diagnostics, diagnostics+".old"); err != nil {
				return err
			}
			return os.Mkdir(diagnostics, 0o700)
		}
		t.Cleanup(func() { quarantineMutationHook = nil })
		if _, err := quarantineAttemptOutputs(lease, attemptID, time.Unix(100, 0)); err == nil {
			t.Fatal("diagnostics replacement crossed the manifest boundary")
		}
		for _, directory := range []string{diagnostics, diagnostics + ".old"} {
			if entries, err := os.ReadDir(directory); err != nil || len(entries) != 0 {
				t.Fatalf("%s received transaction bytes: entries=%v err=%v", directory, entries, err)
			}
		}
	})
	t.Run("late conflicting bytes cannot overwrite", func(t *testing.T) {
		fixture := newExecutionFixture(t, false)
		lease := acquireFixtureLease(t, fixture)
		attemptID := "attempt-quarantine-immutable"
		attemptDir := filepath.Join(fixture.runDir, "attempts", attemptID)
		if err := os.MkdirAll(attemptDir, 0o700); err != nil {
			t.Fatal(err)
		}
		source := filepath.Join(attemptDir, "worker-result.json")
		if err := os.WriteFile(source, []byte("first\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := quarantineAttemptOutputs(lease, attemptID, time.Unix(100, 0)); err != nil {
			t.Fatal(err)
		}
		destination := filepath.Join(attemptDir, "diagnostics", "quarantined-worker-result.json")
		before, _ := os.ReadFile(destination)
		if err := os.WriteFile(source, []byte("late-conflict\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := quarantineAttemptOutputs(lease, attemptID, time.Unix(100, 0)); err == nil {
			t.Fatal("late conflicting output was accepted")
		}
		after, _ := os.ReadFile(destination)
		if !bytes.Equal(before, after) {
			t.Fatal("immutable quarantine destination was overwritten")
		}
	})
	t.Run("manifest hard-link commit resumes", func(t *testing.T) {
		fixture := newExecutionFixture(t, false)
		lease := acquireFixtureLease(t, fixture)
		attemptID := "attempt-quarantine-manifest-resume"
		attemptDir := filepath.Join(fixture.runDir, "attempts", attemptID)
		if err := os.MkdirAll(attemptDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(attemptDir, "worker-result.json"), []byte("stable\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := quarantineAttemptOutputs(lease, attemptID, time.Unix(100, 0)); err != nil {
			t.Fatal(err)
		}
		manifest := filepath.Join(attemptDir, "diagnostics", "orphan-quarantine-transaction.json")
		pending := filepath.Join(filepath.Dir(manifest), "."+filepath.Base(manifest)+".pending")
		if err := os.Link(manifest, pending); err != nil {
			t.Fatal(err)
		}
		if _, err := quarantineAttemptOutputs(lease, attemptID, time.Unix(100, 0)); err != nil {
			t.Fatalf("resume hard-link commit: %v", err)
		}
		if _, err := os.Lstat(pending); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("pending manifest remains: %v", err)
		}
	})
	t.Run("symlink source rejected", func(t *testing.T) {
		fixture := newExecutionFixture(t, false)
		lease := acquireFixtureLease(t, fixture)
		attemptID := "attempt-quarantine-symlink"
		attemptDir := filepath.Join(fixture.runDir, "attempts", attemptID)
		if err := os.MkdirAll(attemptDir, 0o700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "outside")
		if err := os.WriteFile(target, []byte("outside\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(attemptDir, "worker-result.json")); err != nil {
			t.Fatal(err)
		}
		if _, err := quarantineAttemptOutputs(lease, attemptID, time.Unix(100, 0)); err == nil {
			t.Fatal("symlink quarantine source was accepted")
		}
	})
	t.Run("symlinked attempt directory rejected", func(t *testing.T) {
		fixture := newExecutionFixture(t, false)
		lease := acquireFixtureLease(t, fixture)
		attemptID := "attempt-quarantine-parent-symlink"
		external := t.TempDir()
		if err := os.WriteFile(filepath.Join(external, "worker-result.json"), []byte("outside\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(fixture.runDir, "attempts"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(external, filepath.Join(fixture.runDir, "attempts", attemptID)); err != nil {
			t.Fatal(err)
		}
		if _, err := quarantineAttemptOutputs(lease, attemptID, time.Unix(100, 0)); err == nil {
			t.Fatal("symlinked attempt directory was accepted")
		}
		if _, err := os.Lstat(filepath.Join(external, "diagnostics")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("external diagnostics directory was created: %v", err)
		}
	})
}

func acquireFixtureLease(t *testing.T, fixture executionFixture) *runstore.Lease {
	t.Helper()
	lease, err := runstore.New(fixture.input.StateRoot).Acquire(fixture.input.RunID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Release() })
	return lease
}

type staleFencingAdapter struct {
	*fixtureAdapter
	claimedAttemptID string
}

func (a *staleFencingAdapter) Run(ctx context.Context, request domain.Record) (domain.Record, error) {
	result, err := a.fixtureAdapter.Run(ctx, request)
	if err != nil {
		return result, err
	}
	var data map[string]any
	if err := json.Unmarshal(result.Data, &data); err != nil {
		return result, err
	}
	data["attemptId"] = a.claimedAttemptID
	rewritten, err := json.Marshal(data)
	return domain.Record{Kind: domain.KindWorkerResult, Data: rewritten}, err
}

func TestOrphanedRecoveryIsolatesStaleFencingWorkerResult(t *testing.T) {
	fixture := newExecutionFixtureWithOptions(t, false, executionFixtureOptions{
		preferredAdapter: "fake", fallbackAdapters: []string{}, capabilityAdapterID: "fake",
		maxAttempts: 5, maxOperationalRetries: 2, maxReworkRounds: 1,
	})
	fixture.input.Adapter.(*fixtureAdapter).id = "fake"
	appendRetrySegment(t, fixture, "attempt-superseded")
	attemptDir := filepath.Join(fixture.runDir, "attempts", "attempt-superseded")
	if err := os.MkdirAll(attemptDir, 0o700); err != nil {
		t.Fatal(err)
	}
	request := mustJSON(t, map[string]any{"attemptNumber": 1, "executionProfile": "workspace-write"})
	if err := os.WriteFile(filepath.Join(attemptDir, "worker-request.json"), request, 0o600); err != nil {
		t.Fatal(err)
	}
	input := fixture.input
	input.Adapter = &staleFencingAdapter{fixtureAdapter: input.Adapter.(*fixtureAdapter), claimedAttemptID: "attempt-superseded"}
	result, err := Run(context.Background(), input)
	if err == nil || result.State.State != domain.StateBlocked || result.State.TerminalReason != "adapter-protocol-invalid" || result.State.OperationalRetriesUsed != 1 {
		t.Fatalf("stale fencing WorkerResult accepted: state=%+v err=%v", result.State, err)
	}
	quarantined, readErr := os.ReadFile(filepath.Join(attemptDir, "diagnostics", "quarantined-late-worker-result.json"))
	if readErr != nil || !strings.Contains(string(quarantined), "attempt-superseded") {
		t.Fatalf("stale WorkerResult quarantine = %q err = %v", quarantined, readErr)
	}
	if _, statErr := os.Stat(filepath.Join(fixture.runDir, "attempts", result.AttemptID, "worker-result.json")); !os.IsNotExist(statErr) {
		t.Fatalf("stale WorkerResult leaked into the active attempt directory: %v", statErr)
	}
}

// stubEdgeLeaseResolver is the controllable dispatch-ledger resolver of the
// result acceptance gate fixtures.
type stubEdgeLeaseResolver struct{ active bool }

func (s stubEdgeLeaseResolver) LeaseActive(string, int64, string) (bool, error) { return s.active, nil }

// stubEdgeTargetResolver is the controllable target eligibility resolver of
// the result acceptance gate fixtures.
type stubEdgeTargetResolver struct{ eligible bool }

func (s stubEdgeTargetResolver) TargetEligible(authority.SecurityDomainId) (bool, error) {
	return s.eligible, nil
}

// edgeRecheckFixture issues one valid dispatch result capability under a
// fresh Core edge runtime with controllable resolvers.
func edgeRecheckFixture(t *testing.T) (*authority.EdgeRuntime, authority.DispatchResultCapability, authority.EdgeLeaseBinding) {
	t.Helper()
	return edgeRecheckFixtureWithOptions(t, "2026-12-31T00:00:00Z", true)
}

func edgeRecheckFixtureWithOptions(t *testing.T, expiry string, leaseActive bool) (*authority.EdgeRuntime, authority.DispatchResultCapability, authority.EdgeLeaseBinding) {
	t.Helper()
	runtime, err := authority.NewEdgeRuntime(authority.AuthorityNamespaceId{
		TenantNamespace:  "default",
		ControlPlaneId:   "default",
		AuthorityScopeId: "marshal-harness",
	})
	if err != nil {
		t.Fatalf("NewEdgeRuntime: %v", err)
	}
	runtime.BindLeaseResolver(stubEdgeLeaseResolver{active: leaseActive})
	runtime.BindTargetEligibilityResolver(stubEdgeTargetResolver{eligible: true})
	binding := authority.EdgeLeaseBinding{
		LeaseId:      "lease-edge-1",
		AttemptId:    "attempt-edge-1",
		AllocationId: "allocation-edge-1",
		Generation:   1,
		FencingToken: canonical.DigestBytes([]byte("fencing-token-edge-1")),
	}
	edge, err := runtime.IssueDispatchResultCapability(authority.DispatchResultIssuance{
		SourceActor: authority.SecurityDomainId{
			TenantNamespace:   "default",
			TrustDomainKind:   authority.TrustDomainKindExecution,
			IsolationDomainId: "isolation-execution",
		},
		TargetActor: authority.SecurityDomainId{
			TenantNamespace:   "default",
			TrustDomainKind:   authority.TrustDomainKindDataCapability,
			IsolationDomainId: "isolation-result-ingress",
		},
		Operation:         authority.DispatchResultOperationAccept,
		BoundAttemptId:    binding.AttemptId,
		BoundAllocationId: binding.AllocationId,
		Expiry:            expiry,
		LeaseBinding:      binding,
	}, time.Now().UTC())
	if err != nil {
		t.Fatalf("IssueDispatchResultCapability: %v", err)
	}
	return runtime, edge, binding
}

func containsEdgeAudit(trail []authority.EdgeAuditRecord, action authority.EdgeAuditAction) bool {
	for _, record := range trail {
		if record.Action == action {
			return true
		}
	}
	return false
}

// TestRunAcceptsResultWhenEdgeRecheckPasses freezes the positive gate
// wiring: a WorkerResult whose dispatch result capability rechecks against
// the current authority ledger is accepted and persisted.
func TestRunAcceptsResultWhenEdgeRecheckPasses(t *testing.T) {
	fixture := newExecutionFixture(t, false)
	runtime, edge, binding := edgeRecheckFixture(t)
	fixture.input.ResultEdgeRecheck = &ResultEdgeRecheck{Runtime: runtime, Edge: edge, Lease: binding}
	result, err := Run(context.Background(), fixture.input)
	if err != nil {
		t.Fatalf("the aligned edge recheck rejected the accepted result: %v", err)
	}
	if result.State.State != domain.StateVerifying {
		t.Fatalf("state = %+v", result.State)
	}
	if _, statErr := os.Stat(filepath.Join(fixture.runDir, "attempts", result.AttemptID, "worker-result.json")); statErr != nil {
		t.Fatalf("the accepted result must be persisted: %v", statErr)
	}
	if !containsEdgeAudit(runtime.AuditTrail(), authority.EdgeAuditUseAccepted) {
		t.Fatal("the accepted use must be recorded in the edge audit trail")
	}
}

// TestRunRejectsResultWhenEdgeRevoked freezes the fail-closed gate: after a
// security-critical revocation the result is rejected, never persisted,
// quarantined as diagnostic material and the rejection is audited.
func TestRunRejectsResultWhenEdgeRevoked(t *testing.T) {
	fixture := newExecutionFixture(t, false)
	runtime, edge, binding := edgeRecheckFixture(t)
	if _, err := runtime.RevokeDispatchResultCapability(edge.EdgeDigest, authority.EdgeRevocationSecurityCritical, time.Now().UTC()); err != nil {
		t.Fatalf("RevokeDispatchResultCapability: %v", err)
	}
	fixture.input.ResultEdgeRecheck = &ResultEdgeRecheck{Runtime: runtime, Edge: edge, Lease: binding}
	result, err := Run(context.Background(), fixture.input)
	if err == nil {
		t.Fatal("a revoked dispatch result capability was accepted")
	}
	if !strings.Contains(err.Error(), "typed-edge recheck") {
		t.Fatalf("expected the typed-edge recheck rejection, got: %v", err)
	}
	if result.State.State != domain.StateRetryPending {
		t.Fatalf("state = %+v", result.State)
	}
	attemptDir := filepath.Join(fixture.runDir, "attempts", result.AttemptID)
	if _, statErr := os.Stat(filepath.Join(attemptDir, "worker-result.json")); !os.IsNotExist(statErr) {
		t.Fatalf("the rejected result must not be persisted: %v", statErr)
	}
	quarantined, readErr := os.ReadFile(filepath.Join(attemptDir, "diagnostics", "quarantined-edge-rejected-worker-result.json"))
	if readErr != nil || len(quarantined) == 0 {
		t.Fatalf("the rejected result must be quarantined as diagnostic material: %v", readErr)
	}
	if !containsEdgeAudit(runtime.AuditTrail(), authority.EdgeAuditUseRejected) {
		t.Fatal("the rejection must be recorded in the edge audit trail")
	}
}

// TestRunRejectsResultWhenEdgeExpiredOrLeaseInactive freezes the remaining
// fail-closed gate classes: an expired edge and an inactive bound lease
// reject the result before it is persisted.
func TestRunRejectsResultWhenEdgeExpiredOrLeaseInactive(t *testing.T) {
	t.Run("expired edge", func(t *testing.T) {
		fixture := newExecutionFixture(t, false)
		runtime, edge, binding := edgeRecheckFixtureWithOptions(t, "2020-01-01T00:00:00Z", true)
		fixture.input.ResultEdgeRecheck = &ResultEdgeRecheck{Runtime: runtime, Edge: edge, Lease: binding}
		result, err := Run(context.Background(), fixture.input)
		if err == nil || result.State.State != domain.StateRetryPending {
			t.Fatalf("an expired edge was accepted: state=%+v err=%v", result.State, err)
		}
		if !strings.Contains(err.Error(), "typed-edge recheck") {
			t.Fatalf("expected the typed-edge recheck rejection, got: %v", err)
		}
	})
	t.Run("inactive bound lease", func(t *testing.T) {
		fixture := newExecutionFixture(t, false)
		runtime, edge, binding := edgeRecheckFixtureWithOptions(t, "2026-12-31T00:00:00Z", false)
		fixture.input.ResultEdgeRecheck = &ResultEdgeRecheck{Runtime: runtime, Edge: edge, Lease: binding}
		result, err := Run(context.Background(), fixture.input)
		if err == nil || result.State.State != domain.StateRetryPending {
			t.Fatalf("an inactive bound lease was accepted: state=%+v err=%v", result.State, err)
		}
		if !strings.Contains(err.Error(), "typed-edge recheck") {
			t.Fatalf("expected the typed-edge recheck rejection, got: %v", err)
		}
	})
	t.Run("gate without runtime fails closed", func(t *testing.T) {
		fixture := newExecutionFixture(t, false)
		_, edge, binding := edgeRecheckFixture(t)
		fixture.input.ResultEdgeRecheck = &ResultEdgeRecheck{Edge: edge, Lease: binding}
		if _, err := Run(context.Background(), fixture.input); err == nil {
			t.Fatal("a gate without a bound runtime was accepted")
		}
	})
}

// dispatchBinderFunc adapts a plain function to the DispatchBinder seam so
// fixtures can inject deterministic bindings, including stale ones.
type dispatchBinderFunc func(ctx context.Context, taskID, runID, attemptID string, requirements domain.SandboxRequirements) (*DispatchBinding, error)

func (f dispatchBinderFunc) BindDispatch(ctx context.Context, taskID, runID, attemptID string, requirements domain.SandboxRequirements) (*DispatchBinding, error) {
	return f(ctx, taskID, runID, attemptID, requirements)
}

// sealedDispatchLease builds a canonically sealed lease binding the given
// attempt identity, so dispatch.ValidateLeaseFencing adjudicates exactly the
// presented generation and fencingToken.
func sealedDispatchLease(t *testing.T, taskID, runID, attemptID string) dispatch.DispatchLease {
	t.Helper()
	lease := dispatch.DispatchLease{
		LeaseId:                          "lease-" + attemptID,
		AuthorityNamespaceId:             authority.AuthorityNamespaceId{TenantNamespace: "local", ControlPlaneId: "default", AuthorityScopeId: "execution-test"},
		SecurityDomainId:                 authority.SecurityDomainId{TenantNamespace: "local", TrustDomainKind: authority.TrustDomainKindExecution, IsolationDomainId: "host-process"},
		RegistrationId:                   "local-sandbox-provider",
		ProviderCapabilitySnapshotDigest: "sha256:" + strings.Repeat("a", 64),
		ConformanceEvidenceDigests:       []string{},
		Attestation:                      provider.Attestation{ProviderInstanceId: "local-instance", ConfigDigest: "sha256:" + strings.Repeat("b", 64), TrustRootKeyId: "local-key", TrustRootAlgorithm: "ed25519"},
		TaskId:                           taskID,
		RunId:                            runID,
		AttemptId:                        attemptID,
		AllocationId:                     "allocation-" + attemptID,
		Generation:                       1,
		FencingToken:                     "sha256:" + strings.Repeat("c", 64),
		AckDeadlineAt:                    "2026-08-13T12:30:00Z",
		ExpiresAt:                        "2026-08-14T12:00:00Z",
		LeaseState:                       dispatch.LeaseStateClaimed,
		CreatedAt:                        "2026-08-13T12:00:00Z",
	}
	digest, err := lease.Digest()
	if err != nil {
		t.Fatalf("seal dispatch lease: %v", err)
	}
	lease.LeaseDigest = digest
	return lease
}

func assertDispatchAdmissionQuarantined(t *testing.T, fixture executionFixture) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixture.runDir, "diagnostics", "quarantined-stale-dispatch-admission.json"))
	if err != nil {
		t.Fatalf("the rejected dispatch presentation was not quarantined: %v", err)
	}
	for _, fragment := range []string{"stale-dispatch-admission", "diagnostic material only"} {
		if !strings.Contains(string(data), fragment) {
			t.Fatalf("quarantine record missing %q: %s", fragment, data)
		}
	}
	if state := inspectState(t, fixture); state.State != domain.StateReady {
		t.Fatalf("run state advanced despite the fenced dispatch admission: %s", state.State)
	}
}

// TestRunDispatchAdmissionAcceptsFreshBinding proves the dispatch-bound
// happy path: a fresh, correctly sealed lease binding passes the fencing
// guard and the attempt verifies exactly like the Local MVP path.
func TestRunDispatchAdmissionAcceptsFreshBinding(t *testing.T) {
	fixture := newExecutionFixture(t, false)
	fixture.input.DispatchBinder = dispatchBinderFunc(func(ctx context.Context, taskID, runID, attemptID string, requirements domain.SandboxRequirements) (*DispatchBinding, error) {
		if taskID != "TASK-1" || runID != fixture.input.RunID {
			t.Fatalf("binder identity = %s/%s, want TASK-1/%s", taskID, runID, fixture.input.RunID)
		}
		if requirements.AccessMode != domain.AccessModeWorkspaceWrite || requirements.MinimumAssuranceLevel != domain.AssuranceLevelWorkspaceWrite {
			t.Fatalf("binder requirements = %+v, want the frozen workspace-write mapping", requirements)
		}
		lease := sealedDispatchLease(t, taskID, runID, attemptID)
		return &DispatchBinding{Lease: lease, Generation: lease.Generation, FencingToken: lease.FencingToken}, nil
	})
	result, err := Run(context.Background(), fixture.input)
	if err != nil {
		t.Fatalf("dispatch-bound attempt rejected: %v", err)
	}
	if result.State.State != domain.StateVerifying {
		t.Fatalf("state = %+v", result.State)
	}
}

// TestRunDispatchAdmissionRejectsStalePresentationsBeforeProbe freezes
// negative fixture 3: a stale or misbound dispatch presentation is rejected
// before Probe and isolated as diagnostic material, never entering the
// evidence, review or publication chain.
func TestRunDispatchAdmissionRejectsStalePresentationsBeforeProbe(t *testing.T) {
	newStaleFixture := func(t *testing.T, mutate func(*DispatchBinding)) executionFixture {
		t.Helper()
		fixture := newExecutionFixture(t, false)
		fixture.input.DispatchBinder = dispatchBinderFunc(func(ctx context.Context, taskID, runID, attemptID string, requirements domain.SandboxRequirements) (*DispatchBinding, error) {
			lease := sealedDispatchLease(t, taskID, runID, attemptID)
			binding := &DispatchBinding{Lease: lease, Generation: lease.Generation, FencingToken: lease.FencingToken}
			mutate(binding)
			return binding, nil
		})
		return fixture
	}
	t.Run("stale generation", func(t *testing.T) {
		fixture := newStaleFixture(t, func(binding *DispatchBinding) { binding.Generation = 0 })
		requireFailsBeforeProbe(t, fixture, "fencing guard rejected stale generation")
		assertDispatchAdmissionQuarantined(t, fixture)
	})
	t.Run("future generation", func(t *testing.T) {
		fixture := newStaleFixture(t, func(binding *DispatchBinding) { binding.Generation = 2 })
		requireFailsBeforeProbe(t, fixture, "fencing guard rejected stale generation")
		assertDispatchAdmissionQuarantined(t, fixture)
	})
	t.Run("mismatched fencing token", func(t *testing.T) {
		fixture := newStaleFixture(t, func(binding *DispatchBinding) { binding.FencingToken = "sha256:" + strings.Repeat("d", 64) })
		requireFailsBeforeProbe(t, fixture, "does not match the lease generation")
		assertDispatchAdmissionQuarantined(t, fixture)
	})
	t.Run("misbound attempt identity", func(t *testing.T) {
		fixture := newStaleFixture(t, func(binding *DispatchBinding) { binding.Lease.AttemptId = "attempt-other" })
		requireFailsBeforeProbe(t, fixture, "does not bind the active task, run and attempt")
		assertDispatchAdmissionQuarantined(t, fixture)
	})
	t.Run("terminal lease", func(t *testing.T) {
		fixture := newStaleFixture(t, func(binding *DispatchBinding) { binding.Lease.LeaseState = dispatch.LeaseStateCancelled })
		requireFailsBeforeProbe(t, fixture, "only claimed or active leases")
		assertDispatchAdmissionQuarantined(t, fixture)
	})
	t.Run("nil binding", func(t *testing.T) {
		fixture := newExecutionFixture(t, false)
		fixture.input.DispatchBinder = dispatchBinderFunc(func(ctx context.Context, taskID, runID, attemptID string, requirements domain.SandboxRequirements) (*DispatchBinding, error) {
			return nil, nil
		})
		requireFailsBeforeProbe(t, fixture, "returned no binding")
	})
	t.Run("binder failure", func(t *testing.T) {
		fixture := newExecutionFixture(t, false)
		fixture.input.DispatchBinder = dispatchBinderFunc(func(ctx context.Context, taskID, runID, attemptID string, requirements domain.SandboxRequirements) (*DispatchBinding, error) {
			return nil, errors.New("claim refused: hardened requirements fail closed without downgrade")
		})
		requireFailsBeforeProbe(t, fixture, "dispatch admission")
	})
}

// TestRunLocalMVPPathUnchangedWithoutDispatchBinder freezes negative fixture
// 9 at the execution layer: without a dispatch binder the Local MVP
// admission path runs exactly as before the M8 vertical slice, with no
// dispatch diagnostics side effect.
func TestRunLocalMVPPathUnchangedWithoutDispatchBinder(t *testing.T) {
	fixture := newExecutionFixture(t, false)
	result, err := Run(context.Background(), fixture.input)
	if err != nil || result.State.State != domain.StateVerifying {
		t.Fatalf("state = %+v err = %v", result.State, err)
	}
	if _, statErr := os.Stat(filepath.Join(fixture.runDir, "diagnostics")); !os.IsNotExist(statErr) {
		t.Fatalf("the Local MVP path produced dispatch diagnostics: %v", statErr)
	}
}

// TestLostResponseWorkerResultDurableButJournalIncomplete 验证 lost-response
// 场景的安全恢复行为（R6-TOP2）：worker-result.json 已 durable 但
// worker.completed 事件未追加（journal tail 仍为 worker.started）时，
// 恢复路径不直接消费未经证实的 stale result，而是隔离旧输出并以新
// attempt 重试。这是 fail-closed 安全选择——比盲目消费 unverified result
// 更安全，因为结果可能是崩溃前写入的不完整或损坏数据。
//
// 此测试锁定当前安全行为作为 lost-response 的契约。未来如增加"检测
// pre-existing result 并经 recheck 后直接消费"的 happy-path 优化，
// 必须先新增 ADR 并修改此测试。
func TestLostResponseWorkerResultDurableButJournalIncomplete(t *testing.T) {
	fixture := newExecutionFixture(t, false)
	fixture.input.OrphanStalenessThreshold = time.Second
	setupOrphanedRunningFixture(t, fixture, "attempt-lost-response")

	// 模拟 lost-response：worker 已产出有效 result 并写入磁盘，但
	// worker.completed 事件未追加（crash between atomicWrite and Append）。
	orphanDir := filepath.Join(fixture.runDir, "attempts", "attempt-lost-response")
	lateResult := mustJSON(t, map[string]any{
		"apiVersion": "marshal.dev/v1alpha1", "kind": "WorkerResult",
		"taskId": "TASK-1", "runId": fixture.input.RunID, "attemptId": "attempt-lost-response",
		"adapter":              map[string]any{"id": "fixture", "executable": "/fixture", "version": "1"},
		"status":               "completed",
		"summary":              "lost-response: result durable but journal incomplete",
		"declaredChangedFiles": []string{},
		"declaredArtifacts":    []any{},
		"declaredCommands":     []any{},
		"declaredRisks":        []string{},
		"startedAt":            "2026-08-04T00:00:00Z",
		"completedAt":          "2026-08-04T00:00:01Z",
	})
	if err := os.WriteFile(filepath.Join(orphanDir, "worker-result.json"), lateResult, 0o600); err != nil {
		t.Fatal(err)
	}

	// 恢复路径必须隔离 stale result，不以 attempt-lost-response 的身份消费它。
	result, err := Run(context.Background(), fixture.input)
	if err != nil {
		t.Fatalf("lost-response recovery failed: %v", err)
	}
	if result.State.State != domain.StateVerifying {
		t.Fatalf("state = %+v, want Verifying (recovered via new attempt)", result.State)
	}
	// 新 attempt 的 ID 不等于 lost-response 的 attempt ID。
	if result.AttemptID == "attempt-lost-response" {
		t.Fatalf("recovery must not reuse the lost-response attempt; got attemptId=%s", result.AttemptID)
	}
	// stale result 被隔离到 diagnostics。
	quarantined, readErr := os.ReadFile(filepath.Join(orphanDir, "diagnostics", "quarantined-worker-result.json"))
	if readErr != nil {
		t.Fatalf("quarantined worker-result missing: %v", readErr)
	}
	if !strings.Contains(string(quarantined), "attempt-lost-response") {
		t.Fatalf("quarantined result must reference the lost-response attempt, got: %s", string(quarantined))
	}
	// 新 attempt 的 worker-result 不是 stale 的那份。
	newResult, readErr := os.ReadFile(filepath.Join(fixture.runDir, "attempts", result.AttemptID, "worker-result.json"))
	if readErr != nil {
		t.Fatalf("new attempt worker-result missing: %v", readErr)
	}
	if strings.Contains(string(newResult), "lost-response") {
		t.Fatalf("new attempt must not reuse the stale result")
	}
}
