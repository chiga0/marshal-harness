package command

import (
	"testing"

	"github.com/chiga0/marshal-harness/internal/engine"
)

const validTimerFireAt = "2026-01-01T00:00:00Z"

func validSignalCommand() ApplicationCommand {
	return ApplicationCommand{
		Kind:             ApplicationCommandKindAttemptCancel,
		RequestDigest:    fixedDigest("request-signal"),
		ExpectedSequence: 1,
		SignalReason:     SignalReasonAttemptCancel,
	}
}

func validTimerCommand() ApplicationCommand {
	return ApplicationCommand{
		Kind:             ApplicationCommandKindWatchdogTick,
		RequestDigest:    fixedDigest("request-timer"),
		ExpectedSequence: 1,
		TimerFireAt:      validTimerFireAt,
	}
}

func validSideEffectCommand() ApplicationCommand {
	return ApplicationCommand{
		Kind:                   ApplicationCommandKindPublicationIntent,
		RequestDigest:          fixedDigest("request-side-effect"),
		ExpectedSequence:       1,
		SideEffectIntentDigest: fixedDigest("intent-side-effect"),
	}
}

func TestDeriveDurableCommand_AllKindsPositive(t *testing.T) {
	eng := newTestEngine(t)
	fact := validFact("all-kinds")

	tests := []struct {
		name string
		cmd  ApplicationCommand
	}{
		{"attempt.start", validCommand()},
		{"attempt.cancel", validSignalCommand()},
		{"watchdog.tick", validTimerCommand()},
		{"publication.intent", validSideEffectCommand()},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DeriveDurableCommand(eng, fact, tc.cmd)
			if err != nil {
				t.Fatalf("DeriveDurableCommand(%s): %v", tc.name, err)
			}
			if got.CommandId == "" {
				t.Fatalf("DeriveDurableCommand(%s): empty commandId", tc.name)
			}
		})
	}
}

func TestDeriveDurableCommand_AllKindsIdempotent(t *testing.T) {
	eng := newTestEngine(t)
	fact := validFact("idempotent-kinds")

	tests := []struct {
		name string
		cmd  ApplicationCommand
	}{
		{"attempt.start", validCommand()},
		{"attempt.cancel", validSignalCommand()},
		{"watchdog.tick", validTimerCommand()},
		{"publication.intent", validSideEffectCommand()},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			first, err := DeriveDurableCommand(eng, fact, tc.cmd)
			if err != nil {
				t.Fatalf("first derivation: %v", err)
			}
			second, err := DeriveDurableCommand(eng, fact, tc.cmd)
			if err != nil {
				t.Fatalf("second derivation: %v", err)
			}
			if !first.Equal(second) {
				t.Fatalf("idempotency violated: first.commandId=%s second.commandId=%s", first.CommandId, second.CommandId)
			}
		})
	}
}

func TestDeriveDurableCommand_KindsFailClosed(t *testing.T) {
	tests := []struct {
		name       string
		engine     *engine.DurableExecutionEngine
		fact       engine.LedgerFact
		appCommand ApplicationCommand
	}{
		// nil engine
		{
			name:       "nil engine with signal command",
			engine:     nil,
			fact:       validFact("nil-engine-signal"),
			appCommand: validSignalCommand(),
		},
		// unknown kind
		{
			name:   "unknown kind",
			engine: newTestEngine(t),
			fact:   validFact("unknown-kind"),
			appCommand: ApplicationCommand{
				Kind:             ApplicationCommandKind("unknown.kind"),
				RequestDigest:    fixedDigest("request-unknown"),
				ExpectedSequence: 1,
			},
		},
		// zero-value construction (kindMapping missing branch)
		{
			name:       "zero value command",
			engine:     newTestEngine(t),
			fact:       validFact("zero-value"),
			appCommand: ApplicationCommand{},
		},
		// malformed requestDigest
		{
			name:   "malformed requestDigest for signal",
			engine: newTestEngine(t),
			fact:   validFact("bad-digest-signal"),
			appCommand: ApplicationCommand{
				Kind:             ApplicationCommandKindAttemptCancel,
				RequestDigest:    "not-a-digest",
				ExpectedSequence: 1,
				SignalReason:     SignalReasonAttemptCancel,
			},
		},
		{
			name:   "malformed requestDigest for timer",
			engine: newTestEngine(t),
			fact:   validFact("bad-digest-timer"),
			appCommand: ApplicationCommand{
				Kind:             ApplicationCommandKindWatchdogTick,
				RequestDigest:    "not-a-digest",
				ExpectedSequence: 1,
				TimerFireAt:      validTimerFireAt,
			},
		},
		{
			name:   "malformed requestDigest for side-effect",
			engine: newTestEngine(t),
			fact:   validFact("bad-digest-side-effect"),
			appCommand: ApplicationCommand{
				Kind:                   ApplicationCommandKindPublicationIntent,
				RequestDigest:          "not-a-digest",
				ExpectedSequence:       1,
				SideEffectIntentDigest: fixedDigest("intent-side-effect"),
			},
		},
		// expectedSequence < 1
		{
			name:   "expectedSequence zero for signal",
			engine: newTestEngine(t),
			fact:   validFact("seq-zero-signal"),
			appCommand: ApplicationCommand{
				Kind:             ApplicationCommandKindAttemptCancel,
				RequestDigest:    fixedDigest("request-seq-zero-signal"),
				ExpectedSequence: 0,
				SignalReason:     SignalReasonAttemptCancel,
			},
		},
		{
			name:   "expectedSequence negative for timer",
			engine: newTestEngine(t),
			fact:   validFact("seq-neg-timer"),
			appCommand: ApplicationCommand{
				Kind:             ApplicationCommandKindWatchdogTick,
				RequestDigest:    fixedDigest("request-seq-neg-timer"),
				ExpectedSequence: -1,
				TimerFireAt:      validTimerFireAt,
			},
		},
		// fact.Sequence mismatch
		{
			name:   "sequence mismatch for signal",
			engine: newTestEngine(t),
			fact:   validFact("seq-mismatch-signal"),
			appCommand: ApplicationCommand{
				Kind:             ApplicationCommandKindAttemptCancel,
				RequestDigest:    fixedDigest("request-seq-mismatch-signal"),
				ExpectedSequence: 99,
				SignalReason:     SignalReasonAttemptCancel,
			},
		},
		{
			name:   "sequence mismatch for timer",
			engine: newTestEngine(t),
			fact:   validFact("seq-mismatch-timer"),
			appCommand: ApplicationCommand{
				Kind:             ApplicationCommandKindWatchdogTick,
				RequestDigest:    fixedDigest("request-seq-mismatch-timer"),
				ExpectedSequence: 42,
				TimerFireAt:      validTimerFireAt,
			},
		},
		{
			name:   "sequence mismatch for side-effect",
			engine: newTestEngine(t),
			fact:   validFact("seq-mismatch-side-effect"),
			appCommand: ApplicationCommand{
				Kind:                   ApplicationCommandKindPublicationIntent,
				RequestDigest:          fixedDigest("request-seq-mismatch-side-effect"),
				ExpectedSequence:       7,
				SideEffectIntentDigest: fixedDigest("intent-seq-mismatch"),
			},
		},
		// timer RFC3339 malformed
		{
			name:   "timer fireAt malformed RFC3339",
			engine: newTestEngine(t),
			fact:   validFact("timer-malformed"),
			appCommand: ApplicationCommand{
				Kind:             ApplicationCommandKindWatchdogTick,
				RequestDigest:    fixedDigest("request-timer-malformed"),
				ExpectedSequence: 1,
				TimerFireAt:      "2026-13-45T99:99:99Z",
			},
		},
		{
			name:   "timer fireAt not RFC3339",
			engine: newTestEngine(t),
			fact:   validFact("timer-not-rfc"),
			appCommand: ApplicationCommand{
				Kind:             ApplicationCommandKindWatchdogTick,
				RequestDigest:    fixedDigest("request-timer-not-rfc"),
				ExpectedSequence: 1,
				TimerFireAt:      "yesterday",
			},
		},
		// side-effect digest malformed
		{
			name:   "side-effect digest empty",
			engine: newTestEngine(t),
			fact:   validFact("side-effect-empty"),
			appCommand: ApplicationCommand{
				Kind:                   ApplicationCommandKindPublicationIntent,
				RequestDigest:          fixedDigest("request-side-effect-empty"),
				ExpectedSequence:       1,
				SideEffectIntentDigest: "",
			},
		},
		{
			name:   "side-effect digest wrong prefix",
			engine: newTestEngine(t),
			fact:   validFact("side-effect-prefix"),
			appCommand: ApplicationCommand{
				Kind:                   ApplicationCommandKindPublicationIntent,
				RequestDigest:          fixedDigest("request-side-effect-prefix"),
				ExpectedSequence:       1,
				SideEffectIntentDigest: "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
			},
		},
		{
			name:   "side-effect digest wrong length",
			engine: newTestEngine(t),
			fact:   validFact("side-effect-length"),
			appCommand: ApplicationCommand{
				Kind:                   ApplicationCommandKindPublicationIntent,
				RequestDigest:          fixedDigest("request-side-effect-length"),
				ExpectedSequence:       1,
				SideEffectIntentDigest: "sha256:short",
			},
		},
		// signal reason empty
		{
			name:   "signal reason empty",
			engine: newTestEngine(t),
			fact:   validFact("signal-empty"),
			appCommand: ApplicationCommand{
				Kind:             ApplicationCommandKindAttemptCancel,
				RequestDigest:    fixedDigest("request-signal-empty"),
				ExpectedSequence: 1,
			},
		},
		{
			name:   "signal reason unknown",
			engine: newTestEngine(t),
			fact:   validFact("signal-unknown"),
			appCommand: ApplicationCommand{
				Kind:             ApplicationCommandKindAttemptCancel,
				RequestDigest:    fixedDigest("request-signal-unknown"),
				ExpectedSequence: 1,
				SignalReason:     SignalReason("unknown.reason"),
			},
		},
		// timer fireAt empty
		{
			name:   "timer fireAt empty",
			engine: newTestEngine(t),
			fact:   validFact("timer-empty"),
			appCommand: ApplicationCommand{
				Kind:             ApplicationCommandKindWatchdogTick,
				RequestDigest:    fixedDigest("request-timer-empty"),
				ExpectedSequence: 1,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("DeriveDurableCommand panicked: %v", r)
				}
			}()
			_, err := DeriveDurableCommand(tc.engine, tc.fact, tc.appCommand)
			if err == nil {
				t.Fatalf("expected error but got nil")
			}
		})
	}
}

func TestValidateKindPayload_WrongKindFieldCombos(t *testing.T) {
	tests := []struct {
		name string
		cmd  ApplicationCommand
	}{
		// dispatch with extra fields
		{
			name: "dispatch with signalReason",
			cmd: ApplicationCommand{
				Kind:             ApplicationCommandKindAttemptStart,
				RequestDigest:    fixedDigest("request"),
				ExpectedSequence: 1,
				SignalReason:     SignalReasonAttemptCancel,
			},
		},
		{
			name: "dispatch with timerFireAt",
			cmd: ApplicationCommand{
				Kind:             ApplicationCommandKindAttemptStart,
				RequestDigest:    fixedDigest("request"),
				ExpectedSequence: 1,
				TimerFireAt:      validTimerFireAt,
			},
		},
		{
			name: "dispatch with sideEffectIntentDigest",
			cmd: ApplicationCommand{
				Kind:                   ApplicationCommandKindAttemptStart,
				RequestDigest:          fixedDigest("request"),
				ExpectedSequence:       1,
				SideEffectIntentDigest: fixedDigest("intent"),
			},
		},
		// signal with wrong fields
		{
			name: "signal with timerFireAt",
			cmd: ApplicationCommand{
				Kind:             ApplicationCommandKindAttemptCancel,
				RequestDigest:    fixedDigest("request"),
				ExpectedSequence: 1,
				SignalReason:     SignalReasonAttemptCancel,
				TimerFireAt:      validTimerFireAt,
			},
		},
		{
			name: "signal with sideEffectIntentDigest",
			cmd: ApplicationCommand{
				Kind:                   ApplicationCommandKindAttemptCancel,
				RequestDigest:          fixedDigest("request"),
				ExpectedSequence:       1,
				SignalReason:           SignalReasonAttemptCancel,
				SideEffectIntentDigest: fixedDigest("intent"),
			},
		},
		// timer with wrong fields
		{
			name: "timer with signalReason",
			cmd: ApplicationCommand{
				Kind:             ApplicationCommandKindWatchdogTick,
				RequestDigest:    fixedDigest("request"),
				ExpectedSequence: 1,
				TimerFireAt:      validTimerFireAt,
				SignalReason:     SignalReasonAttemptCancel,
			},
		},
		{
			name: "timer with sideEffectIntentDigest",
			cmd: ApplicationCommand{
				Kind:                   ApplicationCommandKindWatchdogTick,
				RequestDigest:          fixedDigest("request"),
				ExpectedSequence:       1,
				TimerFireAt:            validTimerFireAt,
				SideEffectIntentDigest: fixedDigest("intent"),
			},
		},
		// side-effect with wrong fields
		{
			name: "side-effect with signalReason",
			cmd: ApplicationCommand{
				Kind:                   ApplicationCommandKindPublicationIntent,
				RequestDigest:          fixedDigest("request"),
				ExpectedSequence:       1,
				SignalReason:           SignalReasonAttemptCancel,
				SideEffectIntentDigest: fixedDigest("intent"),
			},
		},
		{
			name: "side-effect with timerFireAt",
			cmd: ApplicationCommand{
				Kind:                   ApplicationCommandKindPublicationIntent,
				RequestDigest:          fixedDigest("request"),
				ExpectedSequence:       1,
				TimerFireAt:            validTimerFireAt,
				SideEffectIntentDigest: fixedDigest("intent"),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cmd.Validate()
			if err == nil {
				t.Fatalf("expected error for %s but got nil", tc.name)
			}
		})
	}
}

func TestValidateKindPayload_ValidCombos(t *testing.T) {
	tests := []struct {
		name string
		cmd  ApplicationCommand
	}{
		{"dispatch zero extra", validCommand()},
		{"signal valid", validSignalCommand()},
		{"timer valid", validTimerCommand()},
		{"side-effect valid", validSideEffectCommand()},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.cmd.Validate(); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestSignalReason_Validate(t *testing.T) {
	tests := []struct {
		reason  SignalReason
		wantErr bool
	}{
		{SignalReasonAttemptCancel, false},
		{SignalReason(""), true},
		{SignalReason("unknown.reason"), true},
	}

	for _, tc := range tests {
		t.Run(string(tc.reason), func(t *testing.T) {
			err := tc.reason.Validate()
			if tc.wantErr && err == nil {
				t.Fatalf("expected error for signal reason %q", string(tc.reason))
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error for signal reason %q: %v", string(tc.reason), err)
			}
		})
	}
}

func TestApplicationCommandKind_NewKindsValidate(t *testing.T) {
	tests := []struct {
		kind    ApplicationCommandKind
		wantErr bool
	}{
		{ApplicationCommandKindAttemptCancel, false},
		{ApplicationCommandKindWatchdogTick, false},
		{ApplicationCommandKindPublicationIntent, false},
	}

	for _, tc := range tests {
		t.Run(string(tc.kind), func(t *testing.T) {
			err := tc.kind.Validate()
			if tc.wantErr && err == nil {
				t.Fatalf("expected error for kind %q", string(tc.kind))
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error for kind %q: %v", string(tc.kind), err)
			}
		})
	}
}
