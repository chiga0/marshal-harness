// Package provider freezes the v1alpha1 provider authority contracts of the
// Marshal Control Plane: ProviderRegistration, the immutable
// ProviderCapabilitySnapshot and ConformanceEvidence (ADR 0018 §5 and §11,
// ADR 0017 §2). It is gate-2 of the M8 hard gate sequence and the
// prerequisite for the legacy capability mapper, durable registration with
// ledger recovery, snapshot/evidence validation and DispatchLease match in
// the later gates. This package only freezes types, canonical digests and
// fail-closed validation; it implements no ledger storage, issuance chain or
// DispatchLease match. Every record is an authority ledger fact owned by
// authority.AuthorityNamespaceId and writable only by Core;
// authority.SecurityDomainId is carried as actor provenance only and never
// owns any of these records.
package provider
