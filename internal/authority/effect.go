package authority

import (
	"fmt"
	"time"
)

// DispositionClass is the closed enumeration of first-batch M8 side-effect
// disposition classes (ADR 0019).
type DispositionClass string

const (
	DispositionClassSandboxProvision DispositionClass = "sandbox-provision"
	DispositionClassSandboxStage     DispositionClass = "sandbox-stage"
	DispositionClassSandboxTerminate DispositionClass = "sandbox-terminate"
	DispositionClassLocalCleanup     DispositionClass = "local-cleanup"
)

// Validate rejects every value outside the closed enumeration.
func (class DispositionClass) Validate() error {
	switch class {
	case DispositionClassSandboxProvision, DispositionClassSandboxStage,
		DispositionClassSandboxTerminate, DispositionClassLocalCleanup:
		return nil
	default:
		return fmt.Errorf("authority: unknown dispositionClass %q", string(class))
	}
}

// Disposition is the closed enumeration of SideEffectReceipt dispositions
// (ADR 0019 §124).
type Disposition string

const (
	DispositionApplied    Disposition = "applied"
	DispositionNotApplied Disposition = "not_applied"
	DispositionAmbiguous  Disposition = "ambiguous"
	DispositionConflict   Disposition = "conflict"
)

// Validate rejects every value outside the closed enumeration.
func (disposition Disposition) Validate() error {
	switch disposition {
	case DispositionApplied, DispositionNotApplied, DispositionAmbiguous, DispositionConflict:
		return nil
	default:
		return fmt.Errorf("authority: unknown disposition %q", string(disposition))
	}
}

// Observation is the closed enumeration of ReconcileRecord observations
// (ADR 0019 §125).
type Observation string

const (
	ObservationAbsent           Observation = "absent"
	ObservationApplied          Observation = "applied"
	ObservationPartiallyApplied Observation = "partially_applied"
	ObservationConflict         Observation = "conflict"
	ObservationUnknown          Observation = "unknown"
)

// Validate rejects every value outside the closed enumeration.
func (observation Observation) Validate() error {
	switch observation {
	case ObservationAbsent, ObservationApplied, ObservationPartiallyApplied,
		ObservationConflict, ObservationUnknown:
		return nil
	default:
		return fmt.Errorf("authority: unknown observation %q", string(observation))
	}
}

// Decision is the closed enumeration of ReconcileRecord decisions
// (ADR 0019 §125).
type Decision string

const (
	DecisionAccept     Decision = "accept"
	DecisionRetry      Decision = "retry"
	DecisionCleanup    Decision = "cleanup"
	DecisionCompensate Decision = "compensate"
	DecisionBlock      Decision = "block"
)

// Validate rejects every value outside the closed enumeration.
func (decision Decision) Validate() error {
	switch decision {
	case DecisionAccept, DecisionRetry, DecisionCleanup, DecisionCompensate, DecisionBlock:
		return nil
	default:
		return fmt.Errorf("authority: unknown decision %q", string(decision))
	}
}

// ActorProvenance carries the provider actor identity attached to an observed
// side effect. SecurityDomainId is provenance here and never an authority
// owner.
type ActorProvenance struct {
	SecurityDomainId SecurityDomainId `json:"securityDomainId"`
}

// Validate fails closed unless the carrier security domain is valid.
func (provenance ActorProvenance) Validate() error {
	return provenance.SecurityDomainId.Validate()
}

// SideEffectIntent is the Core-internal normalized authority record describing
// one side effect before execution (ADR 0019 §123). It is owned by an
// AuthorityNamespaceId and is not a provider wire schema.
type SideEffectIntent struct {
	AuthorityNamespaceId AuthorityNamespaceId `json:"authorityNamespaceId"`
	EffectId             string               `json:"effectId"`
	OwnerIdentity        string               `json:"ownerIdentity"`
	Port                 string               `json:"port"`
	Operation            string               `json:"operation"`
	TargetRef            string               `json:"targetRef"`
	TargetDigest         string               `json:"targetDigest"`
	RequestDigest        string               `json:"requestDigest"`
	CommandId            string               `json:"commandId"`
	IdempotencyKey       string               `json:"idempotencyKey"`
	PolicyDigest         string               `json:"policyDigest"`
	AuthorizationDigest  string               `json:"authorizationDigest"`
	Purpose              string               `json:"purpose"`
	DispositionClass     DispositionClass     `json:"dispositionClass"`
	Deadline             string               `json:"deadline"`
}

// Validate fails closed on a missing ownership namespace, any empty required
// field, any malformed digest reference, an unknown dispositionClass or an
// invalid deadline.
func (intent SideEffectIntent) Validate() error {
	if err := intent.AuthorityNamespaceId.Validate(); err != nil {
		return err
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"sideEffectIntent.effectId", intent.EffectId},
		{"sideEffectIntent.ownerIdentity", intent.OwnerIdentity},
		{"sideEffectIntent.port", intent.Port},
		{"sideEffectIntent.operation", intent.Operation},
		{"sideEffectIntent.targetRef", intent.TargetRef},
		{"sideEffectIntent.commandId", intent.CommandId},
		{"sideEffectIntent.idempotencyKey", intent.IdempotencyKey},
		{"sideEffectIntent.purpose", intent.Purpose},
	} {
		if err := requireText(field.name, field.value); err != nil {
			return err
		}
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"sideEffectIntent.targetDigest", intent.TargetDigest},
		{"sideEffectIntent.requestDigest", intent.RequestDigest},
		{"sideEffectIntent.policyDigest", intent.PolicyDigest},
		{"sideEffectIntent.authorizationDigest", intent.AuthorizationDigest},
	} {
		if err := requireDigest(field.name, field.value); err != nil {
			return err
		}
	}
	if err := intent.DispositionClass.Validate(); err != nil {
		return err
	}
	return validateDeadline("sideEffectIntent.deadline", intent.Deadline)
}

// Canonical returns the deterministic serialization of the validated record.
func (intent SideEffectIntent) Canonical() ([]byte, error) {
	if err := intent.Validate(); err != nil {
		return nil, err
	}
	return canonicalJSON(intent)
}

// Digest returns the sha256 digest of the canonical serialization.
func (intent SideEffectIntent) Digest() (string, error) {
	canonical, err := intent.Canonical()
	if err != nil {
		return "", err
	}
	return digestBytes(canonical), nil
}

// Equal reports whether both records carry identical field values.
func (intent SideEffectIntent) Equal(other SideEffectIntent) bool {
	return intent == other
}

// SideEffectReceipt is the Core-internal authority record describing the
// observed outcome of a SideEffectIntent (ADR 0019 §124).
type SideEffectReceipt struct {
	AuthorityNamespaceId     AuthorityNamespaceId `json:"authorityNamespaceId"`
	IntentDigest             string               `json:"intentDigest"`
	Disposition              Disposition          `json:"disposition"`
	ProviderResourceIdentity string               `json:"providerResourceIdentity"`
	ObservedDigest           string               `json:"observedDigest"`
	ActorProvenance          ActorProvenance      `json:"actorProvenance"`
	ReconcileIdentity        string               `json:"reconcileIdentity"`
}

// Validate fails closed on a missing ownership namespace, a malformed intent
// digest binding, an unknown disposition, any empty required field or an
// invalid actor provenance.
func (receipt SideEffectReceipt) Validate() error {
	if err := receipt.AuthorityNamespaceId.Validate(); err != nil {
		return err
	}
	if err := requireDigest("sideEffectReceipt.intentDigest", receipt.IntentDigest); err != nil {
		return err
	}
	if err := receipt.Disposition.Validate(); err != nil {
		return err
	}
	if err := requireText("sideEffectReceipt.providerResourceIdentity", receipt.ProviderResourceIdentity); err != nil {
		return err
	}
	if err := requireDigest("sideEffectReceipt.observedDigest", receipt.ObservedDigest); err != nil {
		return err
	}
	if err := receipt.ActorProvenance.Validate(); err != nil {
		return err
	}
	return requireText("sideEffectReceipt.reconcileIdentity", receipt.ReconcileIdentity)
}

// Canonical returns the deterministic serialization of the validated record.
func (receipt SideEffectReceipt) Canonical() ([]byte, error) {
	if err := receipt.Validate(); err != nil {
		return nil, err
	}
	return canonicalJSON(receipt)
}

// Digest returns the sha256 digest of the canonical serialization.
func (receipt SideEffectReceipt) Digest() (string, error) {
	canonical, err := receipt.Canonical()
	if err != nil {
		return "", err
	}
	return digestBytes(canonical), nil
}

// Equal reports whether both records carry identical field values.
func (receipt SideEffectReceipt) Equal(other SideEffectReceipt) bool {
	return receipt == other
}

// ReconcileRecord is the Core-internal authority record reconciling a
// SideEffectIntent with its SideEffectReceipt (ADR 0019 §125).
type ReconcileRecord struct {
	AuthorityNamespaceId AuthorityNamespaceId `json:"authorityNamespaceId"`
	Observation          Observation          `json:"observation"`
	Decision             Decision             `json:"decision"`
	IntentDigest         string               `json:"intentDigest"`
	ReceiptDigest        string               `json:"receiptDigest"`
}

// Validate fails closed on a missing ownership namespace, an unknown
// observation or decision, or a malformed intent/receipt digest reference.
func (record ReconcileRecord) Validate() error {
	if err := record.AuthorityNamespaceId.Validate(); err != nil {
		return err
	}
	if err := record.Observation.Validate(); err != nil {
		return err
	}
	if err := record.Decision.Validate(); err != nil {
		return err
	}
	if err := requireDigest("reconcileRecord.intentDigest", record.IntentDigest); err != nil {
		return err
	}
	return requireDigest("reconcileRecord.receiptDigest", record.ReceiptDigest)
}

// Canonical returns the deterministic serialization of the validated record.
func (record ReconcileRecord) Canonical() ([]byte, error) {
	if err := record.Validate(); err != nil {
		return nil, err
	}
	return canonicalJSON(record)
}

// Digest returns the sha256 digest of the canonical serialization.
func (record ReconcileRecord) Digest() (string, error) {
	canonical, err := record.Canonical()
	if err != nil {
		return "", err
	}
	return digestBytes(canonical), nil
}

// Equal reports whether both records carry identical field values.
func (record ReconcileRecord) Equal(other ReconcileRecord) bool {
	return record == other
}

func validateDeadline(field, value string) error {
	if err := requireText(field, value); err != nil {
		return err
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return fmt.Errorf("authority: %s must be an RFC 3339 timestamp", field)
	}
	if parsed.IsZero() {
		return fmt.Errorf("authority: %s must not be the zero time", field)
	}
	return nil
}
