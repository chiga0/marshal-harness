package productionruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/resultingress"
)

const (
	fixedLifecycleDeliverySchema    = "fixed-lifecycle-delivery-record/v1"
	fixedLifecycleDeliveryProtocol  = "darwin-fixed-lifecycle-delivery/v1"
	FixedLifecycleCollectOperation  = "collect-run-result"
	FixedLifecycleVerifyOperation   = "verify-run"
	FixedLifecycleReviewOperation   = "build-review-packet"
	FixedLifecycleDecisionOperation = "apply-review-decision"
)

type FixedLifecycleDeliveryBinding struct {
	Operation               string
	RequestKeyDigest        string
	RequestDigest           string
	ApplicationIntentDigest string
	Deadline                string
}

type FixedLifecyclePending struct {
	SchemaVersion           string `json:"schemaVersion"`
	ProtocolRevision        string `json:"protocolRevision"`
	RecordType              string `json:"recordType"`
	Operation               string `json:"operation"`
	OwnerAcquisitionDigest  string `json:"ownerAcquisitionDigest"`
	OwnerFactDigest         string `json:"ownerFactDigest"`
	RepositoryDigest        string `json:"repositoryDigest"`
	NamespaceDigest         string `json:"namespaceDigest"`
	AuthorityRootDigest     string `json:"authorityRootDigest"`
	RequestKeyDigest        string `json:"requestKeyDigest"`
	RequestDigest           string `json:"requestDigest"`
	ApplicationIntentDigest string `json:"applicationIntentDigest"`
	Deadline                string `json:"deadline"`
	Digest                  string `json:"digest"`
}

type FixedLifecycleReceipt struct {
	SchemaVersion                string `json:"schemaVersion"`
	ProtocolRevision             string `json:"protocolRevision"`
	RecordType                   string `json:"recordType"`
	Operation                    string `json:"operation"`
	PendingDigest                string `json:"pendingDigest"`
	ApplicationReceiptFactDigest string `json:"applicationReceiptFactDigest"`
	ResultDigest                 string `json:"resultDigest"`
	RunID                        string `json:"runId"`
	AttemptID                    string `json:"attemptId"`
	PostRevision                 uint64 `json:"postRevision"`
	PostAuthorityHead            string `json:"postAuthorityHead"`
	Digest                       string `json:"digest"`
}

// FixedLifecycleResult is the operation-neutral closed tuple written into a
// receipt-ref. ResultDigest binds the complete typed public projection;
// ApplicationReceiptFactDigest names the exact RB1 head, except for the
// state-preserving ReviewPacket operation where it names the packet digest.
type FixedLifecycleResult struct {
	Operation                    string
	Run                          application.RunProjection
	ResultDigest                 string
	ApplicationReceiptFactDigest string
}

type fixedLifecycleIntent struct {
	ProtocolRevision string `json:"protocolRevision"`
	Operation        string `json:"operation"`
	Request          any    `json:"request"`
}

func NewFixedLifecycleDeliveryBinding(idempotencyKey, operation string, request any, deadline time.Time) (FixedLifecycleDeliveryBinding, error) {
	if strings.TrimSpace(idempotencyKey) != idempotencyKey || idempotencyKey == "" || len(idempotencyKey) > 512 {
		return FixedLifecycleDeliveryBinding{}, ErrFixedDeliveryConflict
	}
	return newFixedLifecycleDeliveryBinding(canonical.DigestBytes([]byte(idempotencyKey)), operation, request, deadline)
}

func newFixedLifecycleDeliveryBinding(requestKeyDigest, operation string, request any, deadline time.Time) (FixedLifecycleDeliveryBinding, error) {
	if !fixedDeliveryDigestPattern.MatchString(requestKeyDigest) || !validFixedLifecycleOperation(operation) || deadline.IsZero() || deadline.Location() != time.UTC {
		return FixedLifecycleDeliveryBinding{}, ErrFixedDeliveryConflict
	}
	requestRaw, err := json.Marshal(request)
	if err != nil {
		return FixedLifecycleDeliveryBinding{}, ErrFixedDeliveryConflict
	}
	requestDigest, err := canonical.DigestJSON(requestRaw)
	if err != nil {
		return FixedLifecycleDeliveryBinding{}, ErrFixedDeliveryConflict
	}
	intentRaw, err := json.Marshal(fixedLifecycleIntent{ProtocolRevision: fixedLifecycleDeliveryProtocol, Operation: operation, Request: request})
	if err != nil {
		return FixedLifecycleDeliveryBinding{}, ErrFixedDeliveryConflict
	}
	intentDigest, err := canonical.DigestJSON(intentRaw)
	if err != nil {
		return FixedLifecycleDeliveryBinding{}, ErrFixedDeliveryConflict
	}
	return FixedLifecycleDeliveryBinding{Operation: operation, RequestKeyDigest: requestKeyDigest, RequestDigest: requestDigest, ApplicationIntentDigest: intentDigest, Deadline: deadline.Format(time.RFC3339Nano)}, nil
}

func (store *FixedDeliveryStore) BeginLifecycleBound(ctx context.Context, idempotencyKey, operation string, request any, current application.CurrentRunRequest, deadline time.Time, authenticated FixedLifecycleDeliveryBinding) (result FixedLifecyclePending, replay bool, resultErr error) {
	binding, err := NewFixedLifecycleDeliveryBinding(idempotencyKey, operation, request, deadline)
	if err != nil || binding != authenticated || validateCurrentRunForDelivery(current) != nil || ctx == nil || ctx.Err() != nil || !deadline.After(time.Now().UTC()) {
		return FixedLifecyclePending{}, false, ErrFixedDeliveryConflict
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.closed || store.session == nil || ctx.Err() != nil || !deadline.After(time.Now().UTC()) {
		return FixedLifecyclePending{}, false, ErrFixedDeliveryConflict
	}
	lease, err := store.session.runs.AcquireExisting(current.RunID)
	if err != nil {
		return FixedLifecyclePending{}, false, err
	}
	defer func() {
		if releaseErr := lease.Release(); releaseErr != nil {
			result = FixedLifecyclePending{}
			replay = false
			resultErr = errors.Join(resultErr, ErrFixedDeliveryUnknown, releaseErr)
		}
	}()
	leaf := fixedDeliveryPendingLeaf(binding.RequestKeyDigest)
	observedRaw, observed, err := readFixedDeliveryRecord(store.session.fixedRoot, leaf, fixedDeliveryMaxRecord)
	if err != nil {
		return FixedLifecyclePending{}, false, err
	}
	lineage := FixedLifecyclePending{OwnerAcquisitionDigest: store.ownerDigest, OwnerFactDigest: store.session.ownerState.FactDigest}
	if observed {
		if decodeFixedDeliveryRecord(observedRaw, &lineage) != nil || validateFixedLifecyclePending(lineage) != nil || !store.lifecyclePendingMatches(lineage, binding) {
			return FixedLifecyclePending{}, false, ErrFixedDeliveryConflict
		}
	}
	err = store.withLifecycleOwnerLineage(ctx, lineage, func() error {
		raw, found, readErr := readFixedDeliveryRecord(store.session.fixedRoot, leaf, fixedDeliveryMaxRecord)
		if readErr != nil || found != observed || found && !bytes.Equal(raw, observedRaw) {
			return ErrFixedDeliveryConflict
		}
		if found {
			var existing FixedLifecyclePending
			if decodeFixedDeliveryRecord(raw, &existing) != nil || validateFixedLifecyclePending(existing) != nil || existing != lineage || !store.lifecyclePendingMatches(existing, binding) {
				return ErrFixedDeliveryConflict
			}
			if err := adoptFixedDeliveryRecord(store.session.fixedRoot, leaf, raw, store.publishHook); err != nil {
				return err
			}
			result, replay = existing, true
			return nil
		}
		projection, readErr := store.session.runs.ReadRunStartAuthorityUnderLease(ctx, lease)
		if readErr != nil || !currentMatchesDelivery(projection.Run, current) {
			return ErrFixedDeliveryConflict
		}
		result = FixedLifecyclePending{SchemaVersion: fixedLifecycleDeliverySchema, ProtocolRevision: fixedLifecycleDeliveryProtocol, RecordType: fixedDeliveryPending, Operation: operation, OwnerAcquisitionDigest: store.ownerDigest, OwnerFactDigest: store.session.ownerState.FactDigest, RepositoryDigest: store.session.acquisition.Scope.RepositoryIdentityDigest, NamespaceDigest: store.namespaceDigest, AuthorityRootDigest: store.authorityRootDigest, RequestKeyDigest: binding.RequestKeyDigest, RequestDigest: binding.RequestDigest, ApplicationIntentDigest: binding.ApplicationIntentDigest, Deadline: binding.Deadline}
		sealed, sealErr := sealFixedDeliveryRecord(&result.Digest, &result)
		if sealErr != nil {
			return sealErr
		}
		return publishFixedDeliveryRecord(store.session.fixedRoot, leaf, sealed, store.publishHook)
	})
	if err != nil {
		return FixedLifecyclePending{}, false, err
	}
	return result, replay, nil
}

func (store *FixedDeliveryStore) CommitLifecycleDelivery(ctx context.Context, pending FixedLifecyclePending, operation string, request any, current application.CurrentRunRequest, result FixedLifecycleResult) (receipt FixedLifecycleReceipt, resultErr error) {
	if store == nil || ctx == nil || validateFixedLifecyclePending(pending) != nil || operation != pending.Operation || result.Operation != operation || validateCurrentRunForDelivery(current) != nil {
		return FixedLifecycleReceipt{}, ErrFixedDeliveryConflict
	}
	deadline, err := time.Parse(time.RFC3339Nano, pending.Deadline)
	if err != nil {
		return FixedLifecycleReceipt{}, ErrFixedDeliveryConflict
	}
	binding, err := newFixedLifecycleDeliveryBinding(pending.RequestKeyDigest, operation, request, deadline)
	if err != nil {
		return FixedLifecycleReceipt{}, ErrFixedDeliveryConflict
	}
	if !store.lifecyclePendingMatches(pending, binding) || validateFixedLifecycleResult(current, result) != nil {
		return FixedLifecycleReceipt{}, ErrFixedDeliveryConflict
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.closed || store.session == nil {
		return FixedLifecycleReceipt{}, ErrFixedDeliveryConflict
	}
	lease, err := store.session.runs.AcquireExisting(current.RunID)
	if err != nil {
		return FixedLifecycleReceipt{}, err
	}
	defer func() { resultErr = errors.Join(resultErr, lease.Release()) }()
	err = store.withLifecycleOwnerLineage(ctx, pending, func() error {
		pendingRaw, marshalErr := json.Marshal(pending)
		if marshalErr != nil {
			return ErrFixedDeliveryConflict
		}
		pendingRaw, marshalErr = canonical.JSON(pendingRaw)
		if marshalErr != nil || adoptFixedDeliveryRecord(store.session.fixedRoot, fixedDeliveryPendingLeaf(pending.RequestKeyDigest), pendingRaw, store.publishHook) != nil {
			return ErrFixedDeliveryConflict
		}
		projection, readErr := store.session.runs.ReadRunStartAuthorityUnderLease(ctx, lease)
		if readErr != nil || projection.Run != result.Run {
			return ErrFixedDeliveryConflict
		}
		candidate := FixedLifecycleReceipt{SchemaVersion: fixedLifecycleDeliverySchema, ProtocolRevision: fixedLifecycleDeliveryProtocol, RecordType: fixedDeliveryReceipt, Operation: operation, PendingDigest: pending.Digest, ApplicationReceiptFactDigest: result.ApplicationReceiptFactDigest, ResultDigest: result.ResultDigest, RunID: result.Run.RunID, AttemptID: result.Run.AttemptID, PostRevision: result.Run.Sequence, PostAuthorityHead: result.Run.AuthorityHead}
		sealed, sealErr := sealFixedDeliveryRecord(&candidate.Digest, &candidate)
		if sealErr != nil {
			return sealErr
		}
		leaf := fixedDeliveryReceiptLeaf(pending.Digest)
		raw, found, readErr := readFixedDeliveryRecord(store.session.fixedRoot, leaf, fixedDeliveryMaxRecord)
		if readErr != nil {
			return readErr
		}
		if found {
			var existing FixedLifecycleReceipt
			if decodeFixedDeliveryRecord(raw, &existing) != nil || validateFixedLifecycleReceipt(existing) != nil || existing != candidate {
				return ErrFixedDeliveryConflict
			}
			if err := adoptFixedDeliveryRecord(store.session.fixedRoot, leaf, raw, store.publishHook); err != nil {
				return err
			}
			receipt = existing
			return nil
		}
		if err := publishFixedDeliveryRecord(store.session.fixedRoot, leaf, sealed, store.publishHook); err != nil {
			return err
		}
		receipt = candidate
		return nil
	})
	if err != nil {
		return FixedLifecycleReceipt{}, err
	}
	return receipt, nil
}

func ValidateFixedLifecycleDeliveryResult(result FixedLifecycleResult, receipt FixedLifecycleReceipt) error {
	if validateFixedLifecycleReceipt(receipt) != nil || !validFixedLifecycleOperation(result.Operation) || result.Run.Validate() != nil || !fixedDeliveryDigestPattern.MatchString(result.ResultDigest) || !fixedDeliveryDigestPattern.MatchString(result.ApplicationReceiptFactDigest) || receipt.Operation != result.Operation || receipt.ResultDigest != result.ResultDigest || receipt.ApplicationReceiptFactDigest != result.ApplicationReceiptFactDigest || receipt.RunID != result.Run.RunID || receipt.AttemptID != result.Run.AttemptID || receipt.PostRevision != result.Run.Sequence || receipt.PostAuthorityHead != result.Run.AuthorityHead {
		return ErrFixedDeliveryConflict
	}
	return nil
}

func validateFixedLifecycleResult(current application.CurrentRunRequest, result FixedLifecycleResult) error {
	if !validFixedLifecycleOperation(result.Operation) || result.Run.Validate() != nil || result.Run.RunID != current.RunID || result.Run.AttemptID != current.AttemptID || !fixedDeliveryDigestPattern.MatchString(result.ResultDigest) || !fixedDeliveryDigestPattern.MatchString(result.ApplicationReceiptFactDigest) {
		return ErrFixedDeliveryConflict
	}
	if result.Operation == FixedLifecycleReviewOperation {
		if result.Run.Sequence != current.ExpectedSequence || result.Run.AuthorityHead != current.ExpectedAuthorityHead {
			return ErrFixedDeliveryConflict
		}
		return nil
	}
	if result.Run.Sequence != current.ExpectedSequence+1 || result.Run.AuthorityHead == current.ExpectedAuthorityHead || result.ApplicationReceiptFactDigest != result.Run.AuthorityHead {
		return ErrFixedDeliveryConflict
	}
	return nil
}

func validateFixedLifecyclePending(pending FixedLifecyclePending) error {
	for _, digest := range []string{pending.OwnerAcquisitionDigest, pending.OwnerFactDigest, pending.RepositoryDigest, pending.NamespaceDigest, pending.AuthorityRootDigest, pending.RequestKeyDigest, pending.RequestDigest, pending.ApplicationIntentDigest, pending.Digest} {
		if !fixedDeliveryDigestPattern.MatchString(digest) {
			return ErrFixedDeliveryConflict
		}
	}
	if pending.SchemaVersion != fixedLifecycleDeliverySchema || pending.ProtocolRevision != fixedLifecycleDeliveryProtocol || pending.RecordType != fixedDeliveryPending || !validFixedLifecycleOperation(pending.Operation) {
		return ErrFixedDeliveryConflict
	}
	deadline, err := time.Parse(time.RFC3339Nano, pending.Deadline)
	if err != nil || deadline.Location() != time.UTC || deadline.Format(time.RFC3339Nano) != pending.Deadline {
		return ErrFixedDeliveryConflict
	}
	stored := pending.Digest
	pending.Digest = ""
	raw, err := json.Marshal(pending)
	digest, digestErr := canonical.DigestJSON(raw)
	if err != nil || digestErr != nil || digest != stored {
		return ErrFixedDeliveryConflict
	}
	return nil
}

func validateFixedLifecycleReceipt(receipt FixedLifecycleReceipt) error {
	for _, digest := range []string{receipt.PendingDigest, receipt.ApplicationReceiptFactDigest, receipt.ResultDigest, receipt.PostAuthorityHead, receipt.Digest} {
		if !fixedDeliveryDigestPattern.MatchString(digest) {
			return ErrFixedDeliveryConflict
		}
	}
	if receipt.SchemaVersion != fixedLifecycleDeliverySchema || receipt.ProtocolRevision != fixedLifecycleDeliveryProtocol || receipt.RecordType != fixedDeliveryReceipt || !validFixedLifecycleOperation(receipt.Operation) || domain.ValidateID(receipt.RunID) != nil || domain.ValidateID(receipt.AttemptID) != nil || receipt.PostRevision == 0 {
		return ErrFixedDeliveryConflict
	}
	stored := receipt.Digest
	receipt.Digest = ""
	raw, err := json.Marshal(receipt)
	digest, digestErr := canonical.DigestJSON(raw)
	if err != nil || digestErr != nil || digest != stored {
		return ErrFixedDeliveryConflict
	}
	return nil
}

func validFixedLifecycleOperation(operation string) bool {
	switch operation {
	case FixedLifecycleCollectOperation, FixedLifecycleVerifyOperation, FixedLifecycleReviewOperation, FixedLifecycleDecisionOperation:
		return true
	default:
		return false
	}
}

func (store *FixedDeliveryStore) lifecyclePendingMatches(pending FixedLifecyclePending, binding FixedLifecycleDeliveryBinding) bool {
	return pending.Operation == binding.Operation && pending.RepositoryDigest == store.session.acquisition.Scope.RepositoryIdentityDigest && pending.NamespaceDigest == store.namespaceDigest && pending.AuthorityRootDigest == store.authorityRootDigest && pending.RequestKeyDigest == binding.RequestKeyDigest && pending.RequestDigest == binding.RequestDigest && pending.ApplicationIntentDigest == binding.ApplicationIntentDigest && pending.Deadline == binding.Deadline
}

func (store *FixedDeliveryStore) withLifecycleOwnerLineage(ctx context.Context, pending FixedLifecyclePending, fn func() error) error {
	reference := resultingress.ControlOwnerLineageReference{Scope: store.session.acquisition.Scope, OwnerFactDigest: pending.OwnerFactDigest, OwnerAcquisitionDigest: pending.OwnerAcquisitionDigest}
	return store.session.ingress.WithCurrentOwnerLineage(ctx, store.session.owner, store.session.acquisition, reference, func(current resultingress.ControlOwnerState) error {
		if current.Acquisition != store.session.acquisition || current.FactDigest != store.session.ownerState.FactDigest {
			return ErrFixedDeliveryConflict
		}
		digest, err := controlOwnerDigest(store.session)
		if err != nil || digest != store.ownerDigest || validateFixedServerRoot(store.session.fixedRoot, 5) != nil {
			return ErrFixedDeliveryConflict
		}
		if fn != nil {
			if err := fn(); err != nil {
				return err
			}
		}
		if validateFixedServerRoot(store.session.fixedRoot, 5) != nil {
			return ErrFixedDeliveryConflict
		}
		after, found, err := store.session.ingress.OpenOwner(store.session.acquisition.Scope)
		if err != nil || !found || after != current {
			return ErrFixedDeliveryConflict
		}
		return nil
	})
}

func currentMatchesDelivery(projection application.RunProjection, current application.CurrentRunRequest) bool {
	return projection.Validate() == nil && projection.RunID == current.RunID && projection.AttemptID == current.AttemptID && projection.Sequence == current.ExpectedSequence && projection.AuthorityHead == current.ExpectedAuthorityHead
}

// validateCurrentRunForDelivery is kept here rather than exported from
// application so
// only the closed fixed-delivery adapter can interpret the common request.
func validateCurrentRunForDelivery(request application.CurrentRunRequest) error {
	if domain.ValidateID(request.RunID) != nil || domain.ValidateID(request.AttemptID) != nil || request.ExpectedSequence == 0 || !fixedDeliveryDigestPattern.MatchString(request.ExpectedAuthorityHead) {
		return ErrFixedDeliveryConflict
	}
	return nil
}
