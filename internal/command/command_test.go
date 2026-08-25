package command

import (
	"context"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/engine"
)

func fixedDigest(seed string) string {
	return canonical.DigestBytes([]byte(seed))
}

func testNamespace() authority.AuthorityNamespaceId {
	return authority.AuthorityNamespaceId{
		TenantNamespace:  "default",
		ControlPlaneId:   "default",
		AuthorityScopeId: "marshal-harness",
	}
}

type stubBackend struct{}

func (stubBackend) Deliver(_ context.Context, cmd engine.Command) (engine.Receipt, error) {
	return engine.Receipt{
		CommandId:   cmd.CommandId,
		DeliveredAt: time.Now().UTC().Format(time.RFC3339),
		AttemptSeq:  1,
	}, nil
}

func (stubBackend) Recover(_ context.Context) error { return nil }
func (stubBackend) Close() error                    { return nil }

func newTestEngine(t *testing.T) *engine.DurableExecutionEngine {
	t.Helper()
	eng, err := engine.New(testNamespace(), stubBackend{})
	if err != nil {
		t.Fatalf("engine.New: %v", err)
	}
	return eng
}

func validFact(seed string) engine.LedgerFact {
	return engine.LedgerFact{
		Sequence:      1,
		FactDigest:    fixedDigest("fact-" + seed),
		PayloadDigest: fixedDigest("payload-" + seed),
	}
}

func validCommand() ApplicationCommand {
	return ApplicationCommand{
		Kind:             ApplicationCommandKindAttemptStart,
		RequestDigest:    fixedDigest("request-alpha"),
		ExpectedSequence: 1,
	}
}

func TestDeriveDurableCommand_Idempotent(t *testing.T) {
	eng := newTestEngine(t)
	fact := validFact("idempotent")
	cmd := validCommand()

	first, err := DeriveDurableCommand(eng, fact, cmd)
	if err != nil {
		t.Fatalf("first derivation: %v", err)
	}

	second, err := DeriveDurableCommand(eng, fact, cmd)
	if err != nil {
		t.Fatalf("second derivation: %v", err)
	}

	if !first.Equal(second) {
		t.Fatalf("idempotency violated: first.commandId=%s second.commandId=%s", first.CommandId, second.CommandId)
	}
}

func TestDeriveDurableCommand_DifferentDigestDifferentCommandId(t *testing.T) {
	eng := newTestEngine(t)

	cmdA := ApplicationCommand{
		Kind:             ApplicationCommandKindAttemptStart,
		RequestDigest:    fixedDigest("request-alpha"),
		ExpectedSequence: 1,
	}
	cmdB := ApplicationCommand{
		Kind:             ApplicationCommandKindAttemptStart,
		RequestDigest:    fixedDigest("request-beta"),
		ExpectedSequence: 1,
	}

	factA := engine.LedgerFact{
		Sequence:      1,
		FactDigest:    fixedDigest("fact-alpha"),
		PayloadDigest: fixedDigest("payload-alpha"),
	}
	factB := engine.LedgerFact{
		Sequence:      1,
		FactDigest:    fixedDigest("fact-beta"),
		PayloadDigest: fixedDigest("payload-beta"),
	}

	resultA, err := DeriveDurableCommand(eng, factA, cmdA)
	if err != nil {
		t.Fatalf("derive A: %v", err)
	}

	resultB, err := DeriveDurableCommand(eng, factB, cmdB)
	if err != nil {
		t.Fatalf("derive B: %v", err)
	}

	if resultA.CommandId == resultB.CommandId {
		t.Fatalf("different facts produced identical commandId: %s", resultA.CommandId)
	}
}

func TestDeriveDurableCommand_FailClosed(t *testing.T) {
	tests := []struct {
		name       string
		engine     *engine.DurableExecutionEngine
		fact       engine.LedgerFact
		appCommand ApplicationCommand
	}{
		{
			name:       "nil engine",
			engine:     nil,
			fact:       validFact("nil-engine"),
			appCommand: validCommand(),
		},
		{
			name:   "malformed fact: zero sequence",
			engine: newTestEngine(t),
			fact: engine.LedgerFact{
				Sequence:      0,
				FactDigest:    fixedDigest("bad-seq"),
				PayloadDigest: fixedDigest("payload-bad-seq"),
			},
			appCommand: ApplicationCommand{
				Kind:             ApplicationCommandKindAttemptStart,
				RequestDigest:    fixedDigest("request-bad-seq"),
				ExpectedSequence: 0,
			},
		},
		{
			name:   "malformed fact: empty factDigest",
			engine: newTestEngine(t),
			fact: engine.LedgerFact{
				Sequence:      1,
				FactDigest:    "",
				PayloadDigest: fixedDigest("payload-empty-fact"),
			},
			appCommand: validCommand(),
		},
		{
			name:   "malformed fact: bad payloadDigest",
			engine: newTestEngine(t),
			fact: engine.LedgerFact{
				Sequence:      1,
				FactDigest:    fixedDigest("fact-bad-payload"),
				PayloadDigest: "not-a-digest",
			},
			appCommand: validCommand(),
		},
		{
			name:   "empty requestDigest",
			engine: newTestEngine(t),
			fact:   validFact("empty-digest"),
			appCommand: ApplicationCommand{
				Kind:             ApplicationCommandKindAttemptStart,
				RequestDigest:    "",
				ExpectedSequence: 1,
			},
		},
		{
			name:   "requestDigest missing sha256 prefix",
			engine: newTestEngine(t),
			fact:   validFact("no-prefix"),
			appCommand: ApplicationCommand{
				Kind:             ApplicationCommandKindAttemptStart,
				RequestDigest:    "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
				ExpectedSequence: 1,
			},
		},
		{
			name:   "requestDigest wrong length",
			engine: newTestEngine(t),
			fact:   validFact("short-digest"),
			appCommand: ApplicationCommand{
				Kind:             ApplicationCommandKindAttemptStart,
				RequestDigest:    "sha256:abcdef",
				ExpectedSequence: 1,
			},
		},
		{
			name:   "requestDigest non-hex characters",
			engine: newTestEngine(t),
			fact:   validFact("non-hex"),
			appCommand: ApplicationCommand{
				Kind:             ApplicationCommandKindAttemptStart,
				RequestDigest:    "sha256:ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ",
				ExpectedSequence: 1,
			},
		},
		{
			name:   "expectedSequence zero",
			engine: newTestEngine(t),
			fact:   validFact("seq-zero"),
			appCommand: ApplicationCommand{
				Kind:             ApplicationCommandKindAttemptStart,
				RequestDigest:    fixedDigest("request-seq-zero"),
				ExpectedSequence: 0,
			},
		},
		{
			name:   "expectedSequence negative",
			engine: newTestEngine(t),
			fact:   validFact("seq-neg"),
			appCommand: ApplicationCommand{
				Kind:             ApplicationCommandKindAttemptStart,
				RequestDigest:    fixedDigest("request-seq-neg"),
				ExpectedSequence: -1,
			},
		},
		{
			name:   "unknown application command kind",
			engine: newTestEngine(t),
			fact:   validFact("unknown-kind"),
			appCommand: ApplicationCommand{
				Kind:             ApplicationCommandKind("unknown.kind"),
				RequestDigest:    fixedDigest("request-unknown"),
				ExpectedSequence: 1,
			},
		},
		{
			name:   "empty application command kind",
			engine: newTestEngine(t),
			fact:   validFact("empty-kind"),
			appCommand: ApplicationCommand{
				Kind:             ApplicationCommandKind(""),
				RequestDigest:    fixedDigest("request-empty-kind"),
				ExpectedSequence: 1,
			},
		},
		{
			name:   "expectedSequence mismatch with fact sequence",
			engine: newTestEngine(t),
			fact:   validFact("seq-mismatch"),
			appCommand: ApplicationCommand{
				Kind:             ApplicationCommandKindAttemptStart,
				RequestDigest:    fixedDigest("request-seq-mismatch"),
				ExpectedSequence: 99,
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

func TestApplicationCommandKind_Validate(t *testing.T) {
	tests := []struct {
		kind    ApplicationCommandKind
		wantErr bool
	}{
		{ApplicationCommandKindAttemptStart, false},
		{ApplicationCommandKind("unknown"), true},
		{ApplicationCommandKind(""), true},
		{ApplicationCommandKind("ATTEMPT.START"), true},
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

func TestApplicationCommand_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cmd     ApplicationCommand
		wantErr bool
	}{
		{
			name:    "valid",
			cmd:     validCommand(),
			wantErr: false,
		},
		{
			name: "unknown kind",
			cmd: ApplicationCommand{
				Kind:             ApplicationCommandKind("nope"),
				RequestDigest:    fixedDigest("request"),
				ExpectedSequence: 1,
			},
			wantErr: true,
		},
		{
			name: "empty digest",
			cmd: ApplicationCommand{
				Kind:             ApplicationCommandKindAttemptStart,
				RequestDigest:    "",
				ExpectedSequence: 1,
			},
			wantErr: true,
		},
		{
			name: "zero sequence",
			cmd: ApplicationCommand{
				Kind:             ApplicationCommandKindAttemptStart,
				RequestDigest:    fixedDigest("request"),
				ExpectedSequence: 0,
			},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cmd.Validate()
			if tc.wantErr && err == nil {
				t.Fatal("expected error but got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
