//go:build darwin

package processsupervisor

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestObservePreparedCommandV2HeldJournal(t *testing.T) {
	h, m, _, request := newAttachV2WireFixture(t)
	a := request.Authority.PreviousSupervisor
	h.session.core.mu.Lock()
	started := h.session.core.supervisorStartedFact
	h.session.core.mu.Unlock()
	prepared, err := PrepareCommandV2(a, clientOptionsV2(a, CommandBindAuthority, "observe-bind"), BindAuthorityPayload{
		SupervisorStartedFactDigest: started, OwnerEpoch: a.Binding.OwnerEpoch,
		PreviousAuthorityHead: a.Binding.CurrentAuthorityHead, AuthorityHead: request.Authority.CurrentOwnerBoundFact.AttemptHead,
	})
	if err != nil {
		t.Fatal(err)
	}
	options := PreparedJournalOptionsV2{ControlDirectory: h.directory, Prepared: prepared}
	check := func(want ReconciliationState) {
		t.Helper()
		before, err := os.ReadFile(filepath.Join(h.root, journalFileNameV2))
		if err != nil {
			t.Fatal(err)
		}
		calls := m.calls
		observed, err := ObservePreparedCommandV2(context.Background(), options)
		if err != nil || observed.Reconciliation != want {
			t.Fatalf("observe %s: %+v %v", want, observed, err)
		}
		after, err := os.ReadFile(filepath.Join(h.root, journalFileNameV2))
		if err != nil || !bytes.Equal(before, after) || m.calls != calls {
			t.Fatal("observation mutated state")
		}
	}
	check(ReconciliationUnchanged)
	if _, err := h.session.handleAttachContinuation(mustCanonical(prepared.request), request.Authority); err != nil {
		t.Fatal(err)
	}
	check(ReconciliationReceiptCommitted)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ObservePreparedCommandV2(canceled, options); err == nil {
		t.Fatal("canceled observation admitted")
	}
	other, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	wrong := options
	wrong.ControlDirectory = other
	if _, err := ObservePreparedCommandV2(context.Background(), wrong); err == nil {
		t.Fatal("different directory admitted")
	}
	file, err := os.OpenFile(filepath.Join(h.root, journalFileNameV2), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("{"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(h.root, journalFileNameV2))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ObservePreparedCommandV2(context.Background(), options); err == nil {
		t.Fatal("partial tail admitted")
	}
	after, err := os.ReadFile(filepath.Join(h.root, journalFileNameV2))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("observer repaired evidence")
	}
}
