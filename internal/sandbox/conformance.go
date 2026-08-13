package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
)

// Outcome values of a normalized business event.
const (
	OutcomePass = "pass"
	OutcomeFail = "fail"
)

// Business event kinds of the normalized conformance trace.
const (
	EventKindAssuranceGate       = "assurance-gate"
	EventKindStageIntegrity      = "stage-integrity"
	EventKindBoundaryContainment = "boundary-containment"
	EventKindSingleActive        = "single-active"
	EventKindLifecycle           = "lifecycle"
	EventKindSelfSignedClaim     = "self-signed-claim"
)

// Reason codes of a conformance verdict.
const (
	ReasonOK                    = "ok"
	ReasonProviderError         = "provider-error"
	ReasonAssuranceNotMet       = "assurance-not-met"
	ReasonStageIntegrityFailure = "stage-integrity-violation"
	ReasonBoundaryViolation     = "boundary-violation"
	ReasonSelfSignedConformance = "self-signed-conformance"
	ReasonSingleActiveViolation = "single-active-violation"
	ReasonLifecycleInconsistent = "lifecycle-inconsistent"
)

// DefaultVerifierId is the deterministic identity of the independent
// conformance verifier used by RunConformance.
const DefaultVerifierId = "conformance-verifier"

// ErrInvalidConformanceEvidence rejects an empty verifier identity.
var ErrInvalidConformanceEvidence = errors.New("sandbox: invalid conformance evidence request")

// BusinessEvent is one normalized business-level observation of a
// conformance scenario: an outcome against one invariant kind, never a
// per-step call detail.
type BusinessEvent struct {
	Kind    string `json:"kind"`
	Outcome string `json:"outcome"`
	Detail  string `json:"detail"`
}

// ConformanceVerdict is the suite's adjudication of one fixture.
type ConformanceVerdict struct {
	Passed     bool            `json:"passed"`
	ReasonCode string          `json:"reasonCode"`
	Trace      []BusinessEvent `json:"trace"`
}

// ConformanceFixture describes one workload the suite drives through the
// provider under test.
type ConformanceFixture struct {
	Name         string
	Requirements domain.SandboxRequirements
	Payload      []byte
}

// ConformanceSuite runs the provider-agnostic conformance scenario under one
// deterministic verifier identity.
type ConformanceSuite struct {
	VerifierId string
}

// RunConformance runs the suite with the default verifier identity. The
// adversarial probes execute inside the target allocation that the provider
// under test provisions itself, and the adjudication input comes only from
// out-of-band observations: Inspect reports, bounded logs and the suite's
// own stage digest recomputations. Provider self-reports and receipts are
// lifecycle-guard inputs only.
func RunConformance(provider SandboxProvider, fixtures ...ConformanceFixture) []ConformanceVerdict {
	return ConformanceSuite{VerifierId: DefaultVerifierId}.Run(context.Background(), provider, fixtures...)
}

// Run executes every fixture against the provider and returns one verdict
// per fixture in order.
func (suite ConformanceSuite) Run(ctx context.Context, provider SandboxProvider, fixtures ...ConformanceFixture) []ConformanceVerdict {
	verdicts := make([]ConformanceVerdict, 0, len(fixtures))
	for _, fixture := range fixtures {
		verdicts = append(verdicts, suite.runFixture(ctx, provider, fixture))
	}
	return verdicts
}

func (suite ConformanceSuite) runFixture(ctx context.Context, provider SandboxProvider, fixture ConformanceFixture) ConformanceVerdict {
	name := fixture.Name
	if strings.TrimSpace(name) == "" {
		name = "fixture"
	}
	payload := fixture.Payload
	if len(payload) == 0 {
		payload = []byte("conformance-payload:" + name)
	}
	trace := make([]BusinessEvent, 0, 8)
	reason := ReasonOK
	passEvent := func(kind, detail string) {
		trace = append(trace, BusinessEvent{Kind: kind, Outcome: OutcomePass, Detail: detail})
	}
	failEvent := func(kind, detail, code string) {
		trace = append(trace, BusinessEvent{Kind: kind, Outcome: OutcomeFail, Detail: detail})
		if reason == ReasonOK {
			reason = code
		}
	}
	failVerdict := func() ConformanceVerdict {
		return ConformanceVerdict{Passed: false, ReasonCode: reason, Trace: trace}
	}

	allocationId := "alloc-" + name
	baseIdentity := func(commandId string) OperationIdentity {
		return OperationIdentity{
			TaskId:       "task-" + name,
			RunId:        "run-" + name,
			AttemptId:    "attempt-" + name,
			WorkloadRole: WorkloadRoleWorker,
			AllocationId: allocationId,
			Generation:   1,
			FencingToken: scenarioFencingToken(name),
			CommandId:    commandId,
		}
	}

	// 1. Probe the provider; any self-signed pass claim is recorded and
	// ignored as evidence.
	probeReport, err := provider.Probe(ctx, ProbeRequest{
		Identity:     baseIdentity("cmd-probe"),
		Requirements: fixture.Requirements,
	})
	if err != nil {
		failEvent(EventKindLifecycle, "probe failed: "+err.Error(), ReasonProviderError)
		return failVerdict()
	}
	selfSignedClaim := probeReport.SelfSignedConformanceClaim
	if selfSignedClaim {
		passEvent(EventKindSelfSignedClaim, "a self-signed conformance claim was observed and ignored by the suite")
	}

	// 2. Assurance gate and provision. A hardened request against a
	// provider without evidence must be refused fail closed; the refusal is
	// the conformant behavior and ends the scenario.
	hardened := fixture.Requirements.MinimumAssuranceLevel == domain.AssuranceLevelHardened
	expectRefusal := hardened && strings.TrimSpace(probeReport.ConformanceEvidenceRef) == ""
	provisionReceipt, provisionErr := provider.Provision(ctx, ProvisionRequest{
		Identity:        baseIdentity("cmd-provision"),
		Requirements:    fixture.Requirements,
		AllowedStoreIds: []string{"store-" + name},
	})
	if expectRefusal {
		if provisionErr != nil {
			passEvent(EventKindAssuranceGate, "the hardened request without conformance evidence was refused fail closed: "+provisionErr.Error())
			return ConformanceVerdict{Passed: true, ReasonCode: ReasonOK, Trace: trace}
		}
		failEvent(EventKindAssuranceGate, "a hardened request without evidence must be refused, but the provider provisioned anyway", ReasonAssuranceNotMet)
		suite.terminate(ctx, provider, baseIdentity("cmd-terminate"), allocationId)
		return failVerdict()
	}
	if provisionErr != nil {
		failEvent(EventKindAssuranceGate, "provision failed: "+provisionErr.Error(), ReasonProviderError)
		return failVerdict()
	}
	if err := ValidateAllocationRequirements(provisionReceipt.Allocation, fixture.Requirements); err != nil {
		failEvent(EventKindAssuranceGate, "the granted allocation violates the requirements: "+err.Error(), ReasonAssuranceNotMet)
		suite.terminate(ctx, provider, baseIdentity("cmd-terminate"), allocationId)
		return failVerdict()
	}
	passEvent(EventKindAssuranceGate, "provision honored the two-dimensional requirements without downgrade")

	// 3. Stage the consistent payload; the suite recomputes the digest
	// out-of-band and compares it with the receipt.
	declaredPayload := RecomputeSHA256(payload)
	stageReport, err := provider.Stage(ctx, StageRequest{
		Identity:     baseIdentity("cmd-stage"),
		AllocationId: allocationId,
		Inputs: []StageInput{{
			InputId:        "payload",
			DeclaredSHA256: declaredPayload,
			Inline:         append([]byte(nil), payload...),
		}},
	})
	if err != nil {
		failEvent(EventKindStageIntegrity, "stage rejected a consistent payload: "+err.Error(), ReasonStageIntegrityFailure)
	} else if stageReport == nil || len(stageReport.Receipts) != 1 || stageReport.Receipts[0].RecomputedSHA256 != declaredPayload {
		failEvent(EventKindStageIntegrity, "the stage receipt digest does not match the suite's out-of-band recomputation", ReasonStageIntegrityFailure)
	} else {
		passEvent(EventKindStageIntegrity, "the consistent payload was staged with matching recomputed digests")
	}

	// 4. Drive the adversarial probe workload through Exec; the receipt
	// status is only the lifecycle guard.
	execOk := true
	for _, probe := range []struct {
		commandId string
		token     string
	}{
		{"cmd-probe-boundary", ProbeCommandBoundaryWrite},
		{"cmd-probe-env", ProbeCommandSensitiveEnvRead},
		{"cmd-probe-spawn", ProbeCommandSpawnFlood},
	} {
		receipt, execErr := provider.Exec(ctx, ExecRequest{
			Identity:     baseIdentity(probe.commandId),
			AllocationId: allocationId,
			Command:      []string{probe.token},
		})
		if execErr != nil || receipt == nil || receipt.Status != ExecutionCompleted {
			failEvent(EventKindLifecycle, "the adversarial probe workload did not complete", ReasonLifecycleInconsistent)
			execOk = false
			break
		}
	}
	if execOk {
		passEvent(EventKindLifecycle, "the adversarial probe workload completed as the lifecycle guard requires")
	}

	// 5. Observe containment out-of-band through Inspect; the adjudication
	// never reads the provider's self-report.
	if execOk {
		inspectReport, inspectErr := provider.Inspect(ctx, InspectRequest{
			Identity:     baseIdentity("cmd-inspect"),
			AllocationId: allocationId,
		})
		switch {
		case inspectErr != nil:
			failEvent(EventKindBoundaryContainment, "inspect failed: "+inspectErr.Error(), ReasonProviderError)
		case inspectReport == nil:
			failEvent(EventKindBoundaryContainment, "inspect returned no report", ReasonProviderError)
		case len(inspectReport.Violations) > 0:
			failEvent(EventKindBoundaryContainment, fmt.Sprintf("%d boundary violations observed out-of-band", len(inspectReport.Violations)), ReasonBoundaryViolation)
		default:
			passEvent(EventKindBoundaryContainment, "the adversarial probe was contained with no violations observed out-of-band")
		}
	} else {
		failEvent(EventKindBoundaryContainment, "containment cannot be observed because the probe workload never completed", ReasonLifecycleInconsistent)
	}

	// 6. Stage integrity probe: a mismatched declared digest must fail
	// closed with the fixed sentinel. A provider that accepts the mismatch
	// is caught by the suite's own out-of-band recomputation.
	probePayload := []byte("conformance-integrity-probe:" + name)
	mismatchedDeclared := RecomputeSHA256(append(append([]byte(nil), probePayload...), []byte("declared-mismatch")...))
	_, stageErr := provider.Stage(ctx, StageRequest{
		Identity:     baseIdentity("cmd-stage-integrity"),
		AllocationId: allocationId,
		Inputs: []StageInput{{
			InputId:        "integrity-probe",
			DeclaredSHA256: mismatchedDeclared,
			Inline:         probePayload,
		}},
	})
	if stageErr == nil {
		if RecomputeSHA256(probePayload) != mismatchedDeclared {
			failEvent(EventKindStageIntegrity, "the provider accepted a mismatched stage input instead of recomputing the digest", ReasonStageIntegrityFailure)
		}
	} else if !errors.Is(stageErr, ErrStageInputMismatch) {
		failEvent(EventKindStageIntegrity, "the mismatched stage input was rejected without the fixed sentinel: "+stageErr.Error(), ReasonStageIntegrityFailure)
	}

	// 7. End the lifecycle and reconcile the scope.
	terminateReceipt, terminateErr := provider.Terminate(ctx, TerminateRequest{
		Identity:     baseIdentity("cmd-terminate"),
		AllocationId: allocationId,
	})
	if terminateErr != nil || terminateReceipt == nil || terminateReceipt.State != AllocationTerminated {
		failEvent(EventKindLifecycle, "terminate did not reach the terminated state", ReasonLifecycleInconsistent)
	}
	reconcileReport, reconcileErr := provider.Reconcile(ctx, ReconcileRequest{
		Identity:  baseIdentity("cmd-reconcile"),
		RunId:     "run-" + name,
		AttemptId: "attempt-" + name,
	})
	if reconcileErr != nil || reconcileReport == nil || reconcileReport.DriftDetected || len(reconcileReport.ActiveAllocationIds) != 0 {
		failEvent(EventKindSingleActive, "reconcile observed drift or a lingering active allocation", ReasonSingleActiveViolation)
	} else {
		passEvent(EventKindSingleActive, "at most one active allocation held the current generation for the whole scenario")
	}

	// Verdict: normalized business-trace equivalence against the expected
	// outcome/invariant trace, never a per-step call comparison.
	expectedTrace := []BusinessEvent{
		{Kind: EventKindAssuranceGate, Outcome: OutcomePass},
		{Kind: EventKindStageIntegrity, Outcome: OutcomePass},
		{Kind: EventKindBoundaryContainment, Outcome: OutcomePass},
		{Kind: EventKindSingleActive, Outcome: OutcomePass},
		{Kind: EventKindLifecycle, Outcome: OutcomePass},
	}
	passed := reason == ReasonOK && compareTraces(expectedTrace, trace)
	if selfSignedClaim && !passed {
		switch reason {
		case ReasonBoundaryViolation, ReasonStageIntegrityFailure, ReasonSingleActiveViolation, ReasonLifecycleInconsistent:
			reason = ReasonSelfSignedConformance
		}
	}
	return ConformanceVerdict{Passed: passed, ReasonCode: reason, Trace: trace}
}

func (suite ConformanceSuite) terminate(ctx context.Context, provider SandboxProvider, identity OperationIdentity, allocationId string) {
	_, _ = provider.Terminate(ctx, TerminateRequest{Identity: identity, AllocationId: allocationId})
}

// compareTraces compares two business traces on normalized outcome and
// invariant equivalence: only the kind-to-outcome mapping matters, never
// the event order, the detail text or extra observed kinds. Every expected
// kind must appear in the observed trace with the identical outcome.
func compareTraces(expected, observed []BusinessEvent) bool {
	observedByKind := make(map[string]string, len(observed))
	for _, event := range observed {
		observedByKind[event.Kind] = event.Outcome
	}
	for _, event := range expected {
		outcome, present := observedByKind[event.Kind]
		if !present || outcome != event.Outcome {
			return false
		}
	}
	return true
}

// scenarioFencingToken derives the deterministic fencing token of one
// conformance scenario; no random source participates.
func scenarioFencingToken(fixtureName string) string {
	return canonical.DigestBytes([]byte("conformance-fencing:" + fixtureName))
}

// ConformanceEvidence is the evidence record issued by the independent
// conformance verifier. ProviderSelfSigned is always false on this path:
// suite-issued evidence is the deliberate opposite of a provider's own pass
// claim, which never becomes evidence.
type ConformanceEvidence struct {
	VerifierId         string `json:"verifierId"`
	Fixture            string `json:"fixture"`
	Passed             bool   `json:"passed"`
	ReasonCode         string `json:"reasonCode"`
	TraceDigest        string `json:"traceDigest"`
	ProviderSelfSigned bool   `json:"providerSelfSigned"`
}

// IssueConformanceEvidence deterministically issues one evidence record for
// a verdict under the verifier identity: identical inputs always issue the
// identical record and digest, with no random source and no clock read.
func IssueConformanceEvidence(verifierId string, fixtureName string, verdict ConformanceVerdict) (ConformanceEvidence, string, error) {
	if strings.TrimSpace(verifierId) == "" {
		return ConformanceEvidence{}, "", fmt.Errorf("%w: verifierId must be a non-empty string", ErrInvalidConformanceEvidence)
	}
	traceRaw, err := json.Marshal(verdict.Trace)
	if err != nil {
		return ConformanceEvidence{}, "", fmt.Errorf("sandbox: evidence trace: %w", err)
	}
	traceDigest, err := canonical.DigestJSON(traceRaw)
	if err != nil {
		return ConformanceEvidence{}, "", fmt.Errorf("sandbox: evidence trace digest: %w", err)
	}
	evidence := ConformanceEvidence{
		VerifierId:         verifierId,
		Fixture:            fixtureName,
		Passed:             verdict.Passed,
		ReasonCode:         verdict.ReasonCode,
		TraceDigest:        traceDigest,
		ProviderSelfSigned: false,
	}
	raw, err := json.Marshal(evidence)
	if err != nil {
		return ConformanceEvidence{}, "", fmt.Errorf("sandbox: evidence record: %w", err)
	}
	digest, err := canonical.DigestJSON(raw)
	if err != nil {
		return ConformanceEvidence{}, "", fmt.Errorf("sandbox: evidence digest: %w", err)
	}
	return evidence, digest, nil
}
