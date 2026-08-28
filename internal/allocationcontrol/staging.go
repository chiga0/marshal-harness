package allocationcontrol

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/chiga0/marshal-harness/internal/sandbox"
)

// CurrentLiveAllocationV1 is a non-bearer observation returned by the
// production-only durable local facade. Every Stage or ReadArtifact call
// reopens current Stage2 authority; copying this value can never authorize a
// later filesystem operation.
type CurrentLiveAllocationV1 struct {
	Binding                    AllocationBindingV1
	ProvisionIntentFactDigest  string
	ProvisionRequestDigest     string
	ProvisionReceiptFactDigest string
	ProvisionReceiptDigest     string
	MarkerNonceDigest          string
	HeldObjectsRootIdentity    ObjectIdentityV1
	LiveIdentity               ObjectIdentityV1
	MarkerIdentity             ObjectIdentityV1
	MarkerDigest               string
	Requirements               SandboxRequirementsV1
	AllowedStoreIDs            []string
	WorkDirAllowlist           []string
	EnvironmentAllowlist       []string
}

func (current CurrentLiveAllocationV1) Validate() error {
	if current.Binding.Validate() != nil || !validDigest(current.ProvisionIntentFactDigest) || !validDigest(current.ProvisionRequestDigest) ||
		!validDigest(current.ProvisionReceiptFactDigest) || !validDigest(current.ProvisionReceiptDigest) || !validDigest(current.MarkerNonceDigest) ||
		current.HeldObjectsRootIdentity.Validate(ObjectTypeDirectory) != nil ||
		current.LiveIdentity.Validate(ObjectTypeDirectory) != nil || current.MarkerIdentity.Validate(ObjectTypeRegular) != nil || !validDigest(current.MarkerDigest) ||
		current.Requirements.Validate() != nil ||
		!sortedUniqueText(current.AllowedStoreIDs) || !sortedUniquePaths(current.WorkDirAllowlist) ||
		!sortedUniqueEnvironmentKeys(current.EnvironmentAllowlist) {
		return ErrInvalid
	}
	return nil
}

// BoundTo proves composition identity without exporting a bearer. The future
// ProductionRuntime supplies the exact ResultIngress AllocationAuthority and
// canonical effect key it already obtained from its DurableProcessAuthority;
// copied facade/current values cannot satisfy this pointer binding.
func (facade *DurableLocalFacade) BoundTo(authority Authority, canonicalEffectKey string) bool {
	if facade == nil || facade.authority == nil || authority == nil || canonicalEffectKey == "" || facade.canonicalEffectKey != canonicalEffectKey {
		return false
	}
	left, right := reflect.TypeOf(facade.authority), reflect.TypeOf(authority)
	return left == right && left.Comparable() && facade.authority == authority
}

// DurableLocalFacade is the sole production staging/readback projection over
// an already-provisioned Stage2 allocation. It owns neither Provision nor
// Terminate: both remain exclusive to Controller under current authority.
// canonicalEffectKey is only a lookup coordinate; Authority must still hold
// the exact current Run/Attempt/lease verification around every operation.
type DurableLocalFacade struct {
	store              *Store
	authority          Authority
	canonicalEffectKey string
	binding            AllocationBindingV1
	objectsRoot        ObjectIdentityV1
}

// NewDurableLocalFacade binds one future ProductionRuntime attempt to its
// canonical Stage2 provision effect and the already-held Store root. It does
// not inspect or create a live allocation; Current performs that check under
// authority. No path is retained or reopened.
func NewDurableLocalFacade(store *Store, authority Authority, canonicalEffectKey string, binding AllocationBindingV1) (*DurableLocalFacade, error) {
	if store == nil || authority == nil || strings.TrimSpace(canonicalEffectKey) == "" || binding.Validate() != nil {
		return nil, ErrInvalid
	}
	root, err := store.heldObjectsRootIdentity()
	if err != nil || root.Validate(ObjectTypeDirectory) != nil {
		return nil, errors.Join(ErrAllocationIntervention, err)
	}
	return &DurableLocalFacade{
		store: store, authority: authority, canonicalEffectKey: canonicalEffectKey,
		binding: binding, objectsRoot: root,
	}, nil
}

// Current proves one complete, live Stage2 provision receipt and the exact
// held objects root. Termination intent/receipt, incomplete authority,
// cross-attempt binding or filesystem drift fail closed.
func (facade *DurableLocalFacade) Current(ctx context.Context) (CurrentLiveAllocationV1, error) {
	var current CurrentLiveAllocationV1
	err := facade.withCurrent(ctx, func(snapshot AuthoritySnapshot) error {
		identity, err := facade.store.currentLiveIdentity(snapshot, facade.objectsRoot)
		if err != nil {
			return err
		}
		intent, receipt := snapshot.ProvisionIntent, snapshot.ProvisionReceipt
		current = CurrentLiveAllocationV1{
			Binding:                    facade.binding,
			ProvisionIntentFactDigest:  snapshot.ProvisionIntentFactDigest,
			ProvisionRequestDigest:     intent.RequestDigest,
			ProvisionReceiptFactDigest: snapshot.ProvisionReceiptFactDigest,
			ProvisionReceiptDigest:     receipt.ReceiptDigest, MarkerNonceDigest: intent.MarkerNonceDigest,
			HeldObjectsRootIdentity: facade.objectsRoot, LiveIdentity: identity,
			MarkerIdentity: receipt.MarkerIdentity, MarkerDigest: receipt.MarkerDigest,
			Requirements:         intent.Requirements,
			AllowedStoreIDs:      append([]string(nil), intent.AllowedStoreIDs...),
			WorkDirAllowlist:     append([]string(nil), intent.WorkDirAllowlist...),
			EnvironmentAllowlist: append([]string(nil), intent.EnvironmentAllowlist...),
		}
		return current.Validate()
	})
	return current, err
}

// Stage writes content-addressed inputs descriptor-relative to the current
// Stage2 live receipt. It never calls SandboxProvider.Provision/Stage and it
// never adopts a directory merely because a matching path exists.
func (facade *DurableLocalFacade) Stage(ctx context.Context, inputs []sandbox.StageInput) (*sandbox.StageReport, error) {
	var report *sandbox.StageReport
	err := facade.withCurrent(ctx, func(snapshot AuthoritySnapshot) error {
		var err error
		report, err = facade.store.stageCurrentLive(ctx, snapshot, facade.objectsRoot, inputs)
		return err
	})
	return report, err
}

// ReadArtifact reads one bounded regular file descriptor-relative to the
// current Stage2 live receipt. It is the only production TranscriptSource
// shape; path-based AllocationDirectory callbacks remain non-production.
func (facade *DurableLocalFacade) ReadArtifact(ctx context.Context, artifactID string, maxBytes int64) ([]byte, error) {
	var raw []byte
	err := facade.withCurrent(ctx, func(snapshot AuthoritySnapshot) error {
		var err error
		raw, err = facade.store.readCurrentLiveArtifact(ctx, snapshot, facade.objectsRoot, artifactID, maxBytes)
		return err
	})
	return raw, err
}

func (facade *DurableLocalFacade) withCurrent(ctx context.Context, operation func(AuthoritySnapshot) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if facade == nil || facade.store == nil || facade.authority == nil || operation == nil || facade.binding.Validate() != nil || facade.objectsRoot.Validate(ObjectTypeDirectory) != nil {
		return ErrAllocationUnavailable
	}
	called := false
	err := facade.authority.WithCurrentAllocation(ctx, facade.canonicalEffectKey, func(session AuthoritySession) error {
		if called || session == nil {
			return ErrAllocationIntervention
		}
		called = true
		snapshot, err := session.Snapshot()
		if err != nil {
			return fmt.Errorf("%w: reload Stage2 allocation snapshot", ErrAllocationUnavailable)
		}
		if err := validateCurrentLiveSnapshot(snapshot, facade.binding); err != nil {
			return err
		}
		return operation(snapshot)
	})
	if err != nil {
		if errors.Is(err, ErrAllocationIntervention) || errors.Is(err, ErrFilesystemConflict) || errors.Is(err, ErrFilesystemUnknown) || errors.Is(err, ErrAuthorityConflict) {
			return errors.Join(ErrAllocationIntervention, err)
		}
		if errors.Is(err, ErrInvalid) {
			return errors.Join(ErrAllocationIntervention, err)
		}
		return errors.Join(ErrAllocationUnavailable, err)
	}
	if !called {
		return ErrAllocationUnavailable
	}
	return nil
}

func validateCurrentLiveSnapshot(snapshot AuthoritySnapshot, binding AllocationBindingV1) error {
	if snapshot.Validate() != nil || snapshot.ProvisionIntent == nil || snapshot.ProvisionPrepared == nil || snapshot.ProvisionReceipt == nil {
		return ErrAllocationIntervention
	}
	if snapshot.ProvisionIntent.Binding != binding || snapshot.ProvisionPrepared.Binding != binding || snapshot.ProvisionReceipt.Binding != binding {
		return ErrAllocationIntervention
	}
	// Once terminalization is intended, the provision receipt is no longer a
	// live-use authority even if the tombstone rename has not happened yet.
	if snapshot.TerminateIntent != nil || snapshot.TerminateReceipt != nil {
		return ErrAllocationIntervention
	}
	return nil
}
