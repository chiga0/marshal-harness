package provider

import (
	"fmt"
	"time"
)

// ValidateSnapshotEligible decides whether a capability snapshot is eligible
// (ADR 0018 §5): the record must first pass the frozen gate-2 structural
// validation, and its snapshotState must be exactly active. Gate-2 Validate
// only checks enumeration membership; expired and superseded snapshots fail
// closed here.
func ValidateSnapshotEligible(snapshot ProviderCapabilitySnapshot) error {
	if err := snapshot.Validate(); err != nil {
		return err
	}
	if snapshot.SnapshotState != SnapshotStateActive {
		return fmt.Errorf("provider: only an %s capability snapshot is eligible, got snapshotState %q", SnapshotStateActive, snapshot.SnapshotState)
	}
	return nil
}

// ValidateEvidenceEligible decides whether a conformance evidence record is
// eligible (ADR 0017 §2, ADR 0018 §11): the record must first pass the
// frozen gate-2 structural validation, which already rejects provider
// self-signed evidence and requires the four closed dimensions; its
// evidenceState must be exactly valid, and when validUntil is non-empty now
// must be strictly before it. An empty validUntil carries no expiry. Revoked
// evidence and evidence past its validUntil fail closed.
func ValidateEvidenceEligible(evidence ConformanceEvidence, now time.Time) error {
	if err := evidence.Validate(); err != nil {
		return err
	}
	if evidence.EvidenceState != EvidenceStateValid {
		return fmt.Errorf("provider: only %s conformance evidence is eligible, got evidenceState %q", EvidenceStateValid, evidence.EvidenceState)
	}
	if evidence.ValidUntil == "" {
		return nil
	}
	validUntil, err := time.Parse(time.RFC3339, evidence.ValidUntil)
	if err != nil {
		return fmt.Errorf("provider: validUntil must be an RFC 3339 timestamp")
	}
	if !now.Before(validUntil) {
		return fmt.Errorf("provider: conformance evidence is past its validUntil %q and no longer eligible", evidence.ValidUntil)
	}
	return nil
}

// IsHardenedEligible reports whether the evidence qualifies for the hardened
// conformance tier (ADR 0017 §2): ValidateEvidenceEligible must pass and all
// four closed dimensions (mount, network, resource, credential) must be
// passed. Any failed or skipped dimension, a non-valid evidenceState, an
// expired validUntil or self-signed evidence yields false. The decision is
// made purely on the evidence content, without any provider-specific special
// casing.
func IsHardenedEligible(evidence ConformanceEvidence, now time.Time) bool {
	if err := ValidateEvidenceEligible(evidence, now); err != nil {
		return false
	}
	for _, dimension := range closedDimensions {
		if evidence.DimensionResults[dimension] != DimensionResultPassed {
			return false
		}
	}
	return true
}

// ValidateEvidenceSetForSnapshot reconciles the closed evidence digest set
// declared by the snapshot with the provided evidence records (ADR 0018 §11):
// every declared digest must be covered by exactly one evidence, no evidence
// may fall outside the declared set or duplicate a digest, and an empty
// digest set requires empty evidences; missing, extra or duplicate coverage
// fails closed. Every evidence must additionally pass ValidateAgainstSnapshot,
// so an attestation substitution (providerInstanceId, configDigest or
// trustRootKeyId) never validates.
func ValidateEvidenceSetForSnapshot(snapshot ProviderCapabilitySnapshot, evidences []ConformanceEvidence) error {
	declared := make(map[string]struct{}, len(snapshot.ConformanceEvidenceDigests))
	for _, digest := range snapshot.ConformanceEvidenceDigests {
		declared[digest] = struct{}{}
	}
	if len(declared) == 0 && len(evidences) != 0 {
		return fmt.Errorf("provider: an empty conformanceEvidenceDigests set requires no evidences, got %d", len(evidences))
	}
	covered := make(map[string]struct{}, len(evidences))
	for index, evidence := range evidences {
		if _, ok := declared[evidence.EvidenceDigest]; !ok {
			return fmt.Errorf("provider: evidences[%d] carries evidenceDigest %s not declared by the snapshot conformanceEvidenceDigests", index, evidence.EvidenceDigest)
		}
		if _, duplicate := covered[evidence.EvidenceDigest]; duplicate {
			return fmt.Errorf("provider: evidenceDigest %s is covered by more than one evidence", evidence.EvidenceDigest)
		}
		covered[evidence.EvidenceDigest] = struct{}{}
	}
	for _, digest := range snapshot.ConformanceEvidenceDigests {
		if _, ok := covered[digest]; !ok {
			return fmt.Errorf("provider: conformanceEvidenceDigests declares %s but no evidence covers it", digest)
		}
	}
	for index, evidence := range evidences {
		if err := evidence.ValidateAgainstSnapshot(snapshot); err != nil {
			return fmt.Errorf("provider: evidences[%d] does not validate against the snapshot: %w", index, err)
		}
	}
	return nil
}

// EvaluateProviderEligibility is the single eligibility adjudication for
// gate-6 DispatchLease matching (ADR 0018 §5 and §11): the registration must
// validate and its lifecycleState must be exactly active, the snapshot must
// validate against the registration and be eligible, the evidence set must
// reconcile exactly with the snapshot's closed digest set, and every evidence
// must be eligible at now and validate against the registration. Any failure
// fails closed and the error names the failing stage.
func EvaluateProviderEligibility(registration ProviderRegistration, snapshot ProviderCapabilitySnapshot, evidences []ConformanceEvidence, now time.Time) error {
	if err := registration.Validate(); err != nil {
		return fmt.Errorf("provider: registration eligibility: %w", err)
	}
	if registration.LifecycleState != LifecycleStateActive {
		return fmt.Errorf("provider: registration eligibility requires lifecycleState %q, got %q", LifecycleStateActive, registration.LifecycleState)
	}
	if err := snapshot.ValidateAgainstRegistration(registration); err != nil {
		return fmt.Errorf("provider: snapshot eligibility against the registration: %w", err)
	}
	if err := ValidateSnapshotEligible(snapshot); err != nil {
		return fmt.Errorf("provider: snapshot eligibility: %w", err)
	}
	if err := ValidateEvidenceSetForSnapshot(snapshot, evidences); err != nil {
		return fmt.Errorf("provider: evidence set eligibility against the snapshot: %w", err)
	}
	for index, evidence := range evidences {
		if err := ValidateEvidenceEligible(evidence, now); err != nil {
			return fmt.Errorf("provider: evidences[%d] eligibility: %w", index, err)
		}
		if err := evidence.ValidateAgainstRegistration(registration); err != nil {
			return fmt.Errorf("provider: evidences[%d] eligibility against the registration: %w", index, err)
		}
	}
	return nil
}
