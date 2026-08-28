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
	if !admission.AgentOK || !admission.SandboxOK || admission.EvidenceRequired || admission.EvidenceOK || admission.EvidenceReason != "not-required-for-ordinary-user" {
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

// TestStableCapabilityDigestIgnoresVolatileFields 锁定稳定身份 digest 的核心
// 不变量：仅诊断字段（如 probedAt）不同的两次 probe 必须产生相同的稳定
// digest；而身份字段不同则必须产生不同稳定 digest。这保证由稳定 digest 派生
// 的 AgentRegistrationID 跨「注册期 probe」与「冻结期快照」严格一致，无需
// 任何降级匹配。
func TestStableCapabilityDigestIgnoresVolatileFields(t *testing.T) {
	base := `{"apiVersion":"marshal.dev/v1alpha1","kind":"CapabilitySnapshot","adapterId":"pi","adapterVersion":"1.2.3","executable":"/opt/pi","executableDigest":"sha256:1111111111111111111111111111111111111111111111111111111111111111","binaryVersion":"0.84.3","probeStatus":"supported","probedAt":"2026-08-28T00:00:00Z"}`
	onlyProbedAtDiffers := `{"apiVersion":"marshal.dev/v1alpha1","kind":"CapabilitySnapshot","adapterId":"pi","adapterVersion":"1.2.3","executable":"/opt/pi","executableDigest":"sha256:1111111111111111111111111111111111111111111111111111111111111111","binaryVersion":"0.84.3","probeStatus":"supported","probedAt":"2026-08-28T23:59:59Z"}`
	differentBinary := `{"apiVersion":"marshal.dev/v1alpha1","kind":"CapabilitySnapshot","adapterId":"pi","adapterVersion":"1.2.3","executable":"/opt/pi","executableDigest":"sha256:2222222222222222222222222222222222222222222222222222222222222222","binaryVersion":"0.84.4","probeStatus":"supported","probedAt":"2026-08-28T00:00:00Z"}`

	d1, err := StableCapabilityDigest([]byte(base))
	if err != nil {
		t.Fatalf("stable digest of base probe: %v", err)
	}
	d2, err := StableCapabilityDigest([]byte(onlyProbedAtDiffers))
	if err != nil {
		t.Fatalf("stable digest of probedAt-only-differs probe: %v", err)
	}
	if d1 != d2 {
		t.Fatalf("stable digest must be identical when only probedAt differs: %q != %q", d1, d2)
	}
	d3, err := StableCapabilityDigest([]byte(differentBinary))
	if err != nil {
		t.Fatalf("stable digest of different-binary probe: %v", err)
	}
	if d1 == d3 {
		t.Fatalf("stable digest must differ when an identity field (executableDigest/binaryVersion) differs")
	}
}

// TestStableCapabilityDigestRejectsMissingAdapterID 锁定 fail-closed：缺少
// adapterId 的快照不得派生稳定 digest。
func TestStableCapabilityDigestRejectsMissingAdapterID(t *testing.T) {
	if _, err := StableCapabilityDigest([]byte(`{"kind":"CapabilitySnapshot","adapterVersion":"1.0.0"}`)); err == nil {
		t.Fatal("expected error for snapshot missing adapterId")
	}
	if _, err := StableCapabilityDigest([]byte(`not-json`)); err == nil {
		t.Fatal("expected error for non-JSON snapshot")
	}
}

func TestStableCapabilitySnapshotDigestExcludesOnlyProbeTime(t *testing.T) {
	base := `{"apiVersion":"marshal.dev/v1alpha1","kind":"CapabilitySnapshot","adapterId":"pi","adapterVersion":"1.2.3","executable":"/opt/pi","executableDigest":"sha256:1111111111111111111111111111111111111111111111111111111111111111","binaryVersion":"0.84.3","probeStatus":"supported","capabilities":{"executionProfiles":["workspace-write"]},"probedAt":"2026-08-28T00:00:00Z"}`
	later := strings.Replace(base, "2026-08-28T00:00:00Z", "2026-08-28T23:59:59Z", 1)
	changedCapabilities := strings.Replace(base, `["workspace-write"]`, `["read-only"]`, 1)

	d1, err := StableCapabilitySnapshotDigest([]byte(base))
	if err != nil {
		t.Fatal(err)
	}
	d2, err := StableCapabilitySnapshotDigest([]byte(later))
	if err != nil {
		t.Fatal(err)
	}
	if d1 != d2 {
		t.Fatalf("probedAt-only change must keep one authority snapshot: %q != %q", d1, d2)
	}
	d3, err := StableCapabilitySnapshotDigest([]byte(changedCapabilities))
	if err != nil {
		t.Fatal(err)
	}
	if d1 == d3 {
		t.Fatal("capability change must create a new authority snapshot digest")
	}
}

// TestEffectiveAgentRegistrationID 锁定：冻结了 AgentRegistrationID 的 Facts
// 必须精确返回该值（不再从含 probedAt 的完整 CapabilityDigest 派生）；未冻结
// 时回退兼容派生。两种情况都返回唯一确定的 id 供 exact lookup。
func TestEffectiveAgentRegistrationID(t *testing.T) {
	frozen := testFacts(t)
	frozen.AgentRegistrationID = "registration:frozen00000000000000000000000000"
	if got := frozen.EffectiveAgentRegistrationID(); got != "registration:frozen00000000000000000000000000" {
		t.Fatalf("frozen AgentRegistrationID must be returned verbatim, got %q", got)
	}
	unfrozen := testFacts(t)
	unfrozen.AgentRegistrationID = ""
	want := AgentRegistrationID(unfrozen.CapabilityDigest)
	if got := unfrozen.EffectiveAgentRegistrationID(); got != want {
		t.Fatalf("unfrozen facts must fall back to derived id %q, got %q", want, got)
	}
}
