package domain

import "testing"

func TestTerminalStates(t *testing.T) {
	t.Parallel()

	for _, state := range States() {
		want := state == StateAccepted || state == StateRejected || state == StateBlocked || state == StateAborted || state == StateNoChange
		if got := state.Terminal(); got != want {
			t.Errorf("%s.Terminal() = %v, want %v", state, got, want)
		}
	}
}

func TestParsersRejectUnknownValues(t *testing.T) {
	t.Parallel()

	if _, err := ParseKind("Unknown"); err == nil {
		t.Fatal("ParseKind accepted an unknown kind")
	}
	if _, err := ParseState("UNKNOWN"); err == nil {
		t.Fatal("ParseState accepted an unknown state")
	}
}
