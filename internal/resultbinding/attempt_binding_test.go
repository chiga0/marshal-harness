package resultbinding

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/provider"
	"github.com/chiga0/marshal-harness/internal/sandbox"
)

func testBindingFacts() Facts {
	return Facts{
		TaskID:                        "T-BIND",
		RunID:                         "run-bind",
		AttemptID:                     "attempt-bind",
		AgentAdapterID:                "pi",
		AgentExecutable:               "/usr/local/bin/pi",
		AgentProviderVersion:          "0.84.3",
		CapabilityDigest:              "sha256:" + "a1b2c3d4" + "00000000000000000000000000000000000000000000000000000000",
		ExecutionProfile:              "workspace-write",
		SandboxProviderRegistrationID: "registration:local-runner",
		AllocationID:                  "alloc-bind-1",
		AllocationGeneration:          1,
		LiveAllocationState:           sandbox.AllocationActive,
		FencingToken:                  "fence-bind-1",
		LeaseExpiry:                   time.Date(2026, 12, 31, 23, 59, 59, 0, time.UTC),
	}
}

func TestWriteReadAttemptBindingRoundTrip(t *testing.T) {
	dir := t.TempDir()
	facts := testBindingFacts()
	if err := WriteAttemptBinding(dir, facts); err != nil {
		t.Fatalf("WriteAttemptBinding: %v", err)
	}
	binding, err := ReadAttemptBinding(dir)
	if err != nil {
		t.Fatalf("ReadAttemptBinding: %v", err)
	}
	if binding.Facts.AttemptID != facts.AttemptID {
		t.Errorf("AttemptID = %q, want %q", binding.Facts.AttemptID, facts.AttemptID)
	}
	if binding.Facts.LeaseExpiry != facts.LeaseExpiry {
		t.Errorf("LeaseExpiry = %v, want %v", binding.Facts.LeaseExpiry, facts.LeaseExpiry)
	}
	if binding.BindingDigest == "" {
		t.Error("BindingDigest must not be empty")
	}
}

func TestReadAttemptBindingRejectsTampered(t *testing.T) {
	dir := t.TempDir()
	facts := testBindingFacts()
	if err := WriteAttemptBinding(dir, facts); err != nil {
		t.Fatalf("WriteAttemptBinding: %v", err)
	}
	// 篡改 binding 文件：改 AgentProviderVersion 但不改 digest。
	path := filepath.Join(dir, AttemptBindingFileName)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	tampered := string(raw)
	tampered = replaceFirst(tampered, "0.84.3", "9.99.9")
	if err := os.WriteFile(path, []byte(tampered), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = ReadAttemptBinding(dir)
	if err == nil || !errors.Is(err, ErrAdmissionRejected) {
		t.Fatalf("ReadAttemptBinding with tampered file: err = %v, want ErrAdmissionRejected", err)
	}
}

func TestReadAttemptBindingMissingFileFailClosed(t *testing.T) {
	_, err := ReadAttemptBinding(t.TempDir())
	if err == nil {
		t.Fatal("ReadAttemptBinding on missing file must fail closed")
	}
}

// fakeAuthoritySource 是测试用的 DurableAuthoritySource stub。
type fakeAuthoritySource struct {
	registration provider.ProviderRegistration
	active       bool
	getErr       error
}

func (f fakeAuthoritySource) ProviderRegistration() (provider.ProviderRegistration, error) {
	return f.registration, nil
}

func (f fakeAuthoritySource) ProviderRegistrationActive(string) (bool, error) {
	if f.getErr != nil {
		return false, f.getErr
	}
	return f.active, nil
}

func TestAdmitWithDurableAuthorityRejectsRevokedRegistration(t *testing.T) {
	dir := t.TempDir()
	facts := testBindingFacts()
	if err := WriteAttemptBinding(dir, facts); err != nil {
		t.Fatalf("WriteAttemptBinding: %v", err)
	}
	binding, err := ReadAttemptBinding(dir)
	if err != nil {
		t.Fatalf("ReadAttemptBinding: %v", err)
	}
	auth := fakeAuthoritySource{
		registration: provider.ProviderRegistration{RegistrationId: "registration:local-runner"},
		active:       false, // revoked
	}
	_, err = AdmitWithDurableAuthority(context.Background(), binding, []byte(`{"kind":"WorkerResult"}`), auth, sandbox.AllocationActive)
	if err == nil || !errors.Is(err, ErrAdmissionRejected) {
		t.Fatalf("AdmitWithDurableAuthority with revoked registration: err = %v, want ErrAdmissionRejected", err)
	}
}

func TestAdmitWithDurableAuthorityNilBindingFailClosed(t *testing.T) {
	auth := fakeAuthoritySource{active: true}
	_, err := AdmitWithDurableAuthority(context.Background(), nil, []byte(`{}`), auth, sandbox.AllocationActive)
	if err == nil {
		t.Fatal("nil binding must fail closed")
	}
}

func replaceFirst(s, old, new string) string {
	idx := indexOf(s, old)
	if idx < 0 {
		return s
	}
	return s[:idx] + new + s[idx+len(old):]
}

func indexOf(s, sub string) int {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
