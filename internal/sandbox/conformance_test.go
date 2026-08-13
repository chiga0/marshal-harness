package sandbox

import (
	"context"
	"testing"

	"github.com/chiga0/marshal-harness/internal/domain"
)

func baselineFixture(name string) ConformanceFixture {
	return ConformanceFixture{
		Name:         name,
		Requirements: workspaceWriteRequirements(),
		Payload:      []byte("baseline-payload:" + name),
	}
}

// TestConformancePositiveBaseline freezes the positive baseline: a legal
// identity, legal stage inputs and a single-active allocation pass the full
// conformance scenario against the honest fake provider.
func TestConformancePositiveBaseline(t *testing.T) {
	fake := NewFakeProvider(FakeConfig{})
	verdicts := RunConformance(fake, baselineFixture("baseline-"+"1"))
	if len(verdicts) != 1 {
		t.Fatalf("one fixture must yield one verdict, got %d", len(verdicts))
	}
	verdict := verdicts[0]
	if !verdict.Passed || verdict.ReasonCode != ReasonOK {
		t.Fatalf("the positive baseline must pass, got passed=%v reason=%q trace=%+v", verdict.Passed, verdict.ReasonCode, verdict.Trace)
	}
	if len(verdict.Trace) == 0 {
		t.Fatal("the verdict must carry the normalized business trace")
	}
}

// TestConformanceDigestEchoProviderFails freezes the digest-echo negative
// fixture: a provider that echoes declared stage digests without
// recomputation is caught by the suite's out-of-band recomputation.
func TestConformanceDigestEchoProviderFails(t *testing.T) {
	fake := NewFakeProvider(FakeConfig{}).WithFaults(FaultSpec{Operation: OperationStage, Fault: FaultEchoDeclaredDigest})
	verdicts := RunConformance(fake, baselineFixture("echo-"+"1"))
	verdict := verdicts[0]
	if verdict.Passed {
		t.Fatal("a digest-echoing provider must never pass conformance")
	}
	if verdict.ReasonCode != ReasonStageIntegrityFailure {
		t.Fatalf("the reason must be %q, got %q", ReasonStageIntegrityFailure, verdict.ReasonCode)
	}
}

// TestConformanceSelfSignedPassMustFail freezes the core self-signed
// fixture: a provider that claims conformance pass on its own while the
// out-of-band observations show violations must be judged failed; the
// self-sign never overrides observation.
func TestConformanceSelfSignedPassMustFail(t *testing.T) {
	fake := NewFakeProvider(FakeConfig{}).WithFaults(
		FaultSpec{Operation: OperationProbe, Fault: FaultSelfSignConformance},
		FaultSpec{Operation: OperationExec, Fault: FaultDisableContainment},
	)
	verdicts := RunConformance(fake, baselineFixture("self-signed-"+"1"))
	verdict := verdicts[0]
	if verdict.Passed {
		t.Fatal("a self-signed provider whose out-of-band observations show violations must never pass")
	}
	if verdict.ReasonCode != ReasonSelfSignedConformance {
		t.Fatalf("the reason must be %q, got %q", ReasonSelfSignedConformance, verdict.ReasonCode)
	}
}

// TestConformanceBoundaryViolationObserved freezes that observed violations
// without any self-signed claim fail with the boundary reason.
func TestConformanceBoundaryViolationObserved(t *testing.T) {
	fake := NewFakeProvider(FakeConfig{}).WithFaults(FaultSpec{Operation: OperationExec, Fault: FaultDisableContainment})
	verdicts := RunConformance(fake, baselineFixture("boundary-"+"1"))
	verdict := verdicts[0]
	if verdict.Passed {
		t.Fatal("observed boundary violations must fail conformance")
	}
	if verdict.ReasonCode != ReasonBoundaryViolation {
		t.Fatalf("the reason must be %q, got %q", ReasonBoundaryViolation, verdict.ReasonCode)
	}
}

// TestConformanceHardenedWithoutEvidenceFailsClosed freezes the hardened
// fail-closed fixture: refusing a hardened request without evidence is the
// conformant behavior, and the suite must never coerce a downgrade.
func TestConformanceHardenedWithoutEvidenceFailsClosed(t *testing.T) {
	fake := NewFakeProvider(FakeConfig{})
	fixture := ConformanceFixture{
		Name:         "hardened-" + "1",
		Requirements: hardenedRequirements(),
		Payload:      []byte("hardened-" + "payload"),
	}
	verdicts := RunConformance(fake, fixture)
	verdict := verdicts[0]
	if !verdict.Passed || verdict.ReasonCode != ReasonOK {
		t.Fatalf("a provider that refuses a hardened request without evidence behaves correctly, got %+v", verdict)
	}
}

// TestConformanceHardenedWithEvidencePasses freezes that a hardened request
// served on valid evidence passes the full scenario.
func TestConformanceHardenedWithEvidencePasses(t *testing.T) {
	fake := NewFakeProvider(FakeConfig{ConformanceEvidenceRef: validEvidenceRef()})
	fixture := ConformanceFixture{
		Name:         "hardened-ok-" + "1",
		Requirements: hardenedRequirements(),
		Payload:      []byte("hardened-ok-" + "payload"),
	}
	verdicts := RunConformance(fake, fixture)
	verdict := verdicts[0]
	if !verdict.Passed || verdict.ReasonCode != ReasonOK {
		t.Fatalf("a hardened request with valid evidence must pass, got %+v", verdict)
	}
}

// downgradingProvider violates the assurance gate on purpose: it downgrades
// hardened requests to workspace-write and provisions anyway. The suite must
// judge it failed against any provider, not only the fake.
type downgradingProvider struct {
	inner *FakeProvider
}

func (p *downgradingProvider) Probe(ctx context.Context, request ProbeRequest) (*ProbeReport, error) {
	report, err := p.inner.Probe(ctx, request)
	if err != nil {
		return nil, err
	}
	report.ConformanceEvidenceRef = ""
	return report, nil
}

func (p *downgradingProvider) Provision(ctx context.Context, request ProvisionRequest) (*ProvisionReceipt, error) {
	downgraded := request
	downgraded.Requirements.MinimumAssuranceLevel = domain.AssuranceLevelWorkspaceWrite
	return p.inner.Provision(ctx, downgraded)
}

func (p *downgradingProvider) Stage(ctx context.Context, request StageRequest) (*StageReport, error) {
	return p.inner.Stage(ctx, request)
}

func (p *downgradingProvider) Exec(ctx context.Context, request ExecRequest) (*ExecReceipt, error) {
	return p.inner.Exec(ctx, request)
}

func (p *downgradingProvider) Inspect(ctx context.Context, request InspectRequest) (*InspectReport, error) {
	return p.inner.Inspect(ctx, request)
}

func (p *downgradingProvider) Signal(ctx context.Context, request SignalRequest) (*SignalReceipt, error) {
	return p.inner.Signal(ctx, request)
}

func (p *downgradingProvider) Checkpoint(ctx context.Context, request CheckpointRequest) (*CheckpointReceipt, error) {
	return p.inner.Checkpoint(ctx, request)
}

func (p *downgradingProvider) Restore(ctx context.Context, request RestoreOperationRequest) (*RestoreReceipt, error) {
	return p.inner.Restore(ctx, request)
}

func (p *downgradingProvider) Terminate(ctx context.Context, request TerminateRequest) (*TerminateReceipt, error) {
	return p.inner.Terminate(ctx, request)
}

func (p *downgradingProvider) Reconcile(ctx context.Context, request ReconcileRequest) (*ReconcileReport, error) {
	return p.inner.Reconcile(ctx, request)
}

// TestConformanceHardenedDowngradeNeverPasses freezes that the suite rejects
// a hardened request silently downgraded to workspace-write.
func TestConformanceHardenedDowngradeNeverPasses(t *testing.T) {
	provider := &downgradingProvider{inner: NewFakeProvider(FakeConfig{})}
	fixture := ConformanceFixture{
		Name:         "downgrade-" + "1",
		Requirements: hardenedRequirements(),
		Payload:      []byte("downgrade-" + "payload"),
	}
	verdicts := RunConformance(provider, fixture)
	verdict := verdicts[0]
	if verdict.Passed || verdict.ReasonCode != ReasonAssuranceNotMet {
		t.Fatalf("a silent downgrade must fail closed with %q, got %+v", ReasonAssuranceNotMet, verdict)
	}
}

// TestIssueConformanceEvidenceDeterministicAndNotSelfSigned freezes the
// evidence issuance helper: deterministic records, and ProviderSelfSigned
// always false for suite-issued evidence.
func TestIssueConformanceEvidenceDeterministicAndNotSelfSigned(t *testing.T) {
	verdict := ConformanceVerdict{
		Passed:     true,
		ReasonCode: ReasonOK,
		Trace:      []BusinessEvent{{Kind: EventKindStageIntegrity, Outcome: OutcomePass, Detail: "detail-" + "1"}},
	}
	first, firstDigest, err := IssueConformanceEvidence(DefaultVerifierId, "fixture-"+"1", verdict)
	if err != nil {
		t.Fatalf("issue evidence: %v", err)
	}
	if first.ProviderSelfSigned {
		t.Fatal("suite-issued evidence must never be provider self-signed")
	}
	second, secondDigest, err := IssueConformanceEvidence(DefaultVerifierId, "fixture-"+"1", verdict)
	if err != nil {
		t.Fatalf("issue evidence again: %v", err)
	}
	if firstDigest != secondDigest || first != second {
		t.Fatal("identical inputs must issue the identical evidence record and digest")
	}
	other, otherDigest, err := IssueConformanceEvidence(DefaultVerifierId, "fixture-"+"2", verdict)
	if err != nil {
		t.Fatalf("issue evidence for another fixture: %v", err)
	}
	if otherDigest == firstDigest || other == first {
		t.Fatal("a different fixture must change the evidence record and digest")
	}
	if _, _, err := IssueConformanceEvidence("", "fixture-"+"1", verdict); err == nil {
		t.Fatal("an empty verifier identity must be rejected")
	}
}

// TestConformanceTraceNormalization freezes that the trace comparison is an
// outcome/invariant equivalence: event order, detail text and extra kinds
// never matter, while a differing outcome or a missing kind breaks
// equivalence.
func TestConformanceTraceNormalization(t *testing.T) {
	expected := []BusinessEvent{
		{Kind: EventKindStageIntegrity, Outcome: OutcomePass},
		{Kind: EventKindBoundaryContainment, Outcome: OutcomePass},
	}
	reordered := []BusinessEvent{
		{Kind: EventKindBoundaryContainment, Outcome: OutcomePass, Detail: "detail-" + "b"},
		{Kind: EventKindStageIntegrity, Outcome: OutcomePass, Detail: "detail-" + "a"},
		{Kind: EventKindLifecycle, Outcome: OutcomePass},
	}
	if !compareTraces(expected, reordered) {
		t.Fatal("normalized trace comparison must ignore order, detail and extra kinds")
	}
	failing := append([]BusinessEvent(nil), reordered...)
	failing[0] = BusinessEvent{Kind: EventKindBoundaryContainment, Outcome: OutcomeFail}
	if compareTraces(expected, failing) {
		t.Fatal("a differing outcome must break trace equivalence")
	}
	missing := []BusinessEvent{{Kind: EventKindStageIntegrity, Outcome: OutcomePass}}
	if compareTraces(expected, missing) {
		t.Fatal("a missing expected kind must break trace equivalence")
	}
}
