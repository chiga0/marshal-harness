package resultbinding

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/sandbox"
)

func testFacts(t *testing.T) Facts {
	t.Helper()
	return Facts{
		TaskID:                        "T1",
		RunID:                         "R1",
		AttemptID:                     "A1",
		AgentAdapterID:                "pi",
		AgentExecutable:               "/opt/pi/cli.js",
		AgentProviderVersion:          "0.84.3",
		CapabilityDigest:              canonical.DigestBytes([]byte("capability-snapshot")),
		ExecutionProfile:              "workspace-write",
		SandboxProviderRegistrationID: "registration:local-runner",
		AllocationID:                  "sha256:" + strings.Repeat("a", 64),
		AllocationGeneration:          1,
		LiveAllocationState:           sandbox.AllocationActive,
		FencingToken:                  canonical.DigestBytes([]byte("fencing")),
		LeaseExpiry:                   time.Now().UTC().Add(24 * time.Hour),
	}
}

func TestAdmitWorkerResultPositive(t *testing.T) {
	admission, err := AdmitWorkerResult(context.Background(), testFacts(t), []byte(`{"kind":"WorkerResult","status":"completed"}`))
	if err != nil {
		t.Fatalf("positive admission: %v", err)
	}
	if !admission.Accepted {
		t.Fatalf("admission must be accepted: %+v", admission)
	}
	if admission.AdmissionFact == "" || admission.DrcDigest == "" || admission.ProfileDigest == "" {
		t.Errorf("anchor fields incomplete: %+v", admission)
	}
	if !admission.AgentOK || !admission.SandboxOK || !admission.EvidenceOK {
		t.Errorf("side flags = %+v", admission)
	}
	if admission.RegistrationID == "" || !strings.HasPrefix(admission.RegistrationID, "registration:") {
		t.Errorf("registration id malformed: %q", admission.RegistrationID)
	}
}

func TestAdmitWorkerResultLiveTerminatedRejected(t *testing.T) {
	facts := testFacts(t)
	facts.LiveAllocationState = sandbox.AllocationTerminated
	admission, err := AdmitWorkerResult(context.Background(), facts, []byte(`{"kind":"WorkerResult"}`))
	if err == nil {
		t.Fatal("terminated allocation must not admit results")
	}
	if !errors.Is(err, ErrAdmissionRejected) {
		t.Errorf("expected ErrAdmissionRejected, got %v", err)
	}
	if admission == nil || admission.Accepted {
		t.Fatalf("admission must be recorded-rejected, got %+v", admission)
	}
	if admission.SandboxOK {
		t.Errorf("sandbox side must fail on live-terminated allocation")
	}
	found := false
	for _, reason := range admission.SandboxReasons {
		if strings.Contains(reason, "inactive") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected allocation-inactive reason, got %v", admission.SandboxReasons)
	}
	if !admission.AgentOK {
		t.Errorf("sandbox-only failure must not drag the agent side: %+v", admission)
	}
}

func TestAdmitWorkerResultReplacedGeneration(t *testing.T) {
	facts := testFacts(t)
	facts.LiveAllocationState = sandbox.AllocationReplaced
	admission, err := AdmitWorkerResult(context.Background(), facts, []byte(`{"kind":"WorkerResult"}`))
	if err == nil {
		t.Fatal("replaced allocation must not admit stale-generation results")
	}
	if !errors.Is(err, ErrAdmissionRejected) {
		t.Errorf("expected ErrAdmissionRejected, got %v", err)
	}
	found := false
	for _, reason := range admission.SandboxReasons {
		if strings.Contains(reason, "generation") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected generation-mismatch reason, got %v", admission.SandboxReasons)
	}
}

func TestAdmitWorkerResultMalformedFacts(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Facts)
	}{
		{"empty attempt", func(f *Facts) { f.AttemptID = "" }},
		{"bad capability digest", func(f *Facts) { f.CapabilityDigest = "sha256:xyz" }},
		{"zero generation", func(f *Facts) { f.AllocationGeneration = 0 }},
		{"zero expiry", func(f *Facts) { f.LeaseExpiry = time.Time{} }},
		{"unknown live state", func(f *Facts) { f.LiveAllocationState = sandbox.AllocationState("ghost") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			facts := testFacts(t)
			tc.mutate(&facts)
			if _, err := AdmitWorkerResult(context.Background(), facts, []byte(`{}`)); !errors.Is(err, ErrMalformedFacts) {
				t.Errorf("expected ErrMalformedFacts, got %v", err)
			} else if !strings.HasPrefix(err.Error(), "resultbinding: ") {
				t.Errorf("error %q must carry resultbinding: prefix", err)
			}
		})
	}
}
