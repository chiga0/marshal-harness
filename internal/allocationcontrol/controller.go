package allocationcontrol

import (
	"context"
)

// AuthoritySnapshot is a read-only projection of allocation facts already
// committed by the single Attempt authority. Facts is the complete ordered
// projection source; the journal is forbidden from filling any missing field.
type AuthoritySnapshot struct {
	Facts                       []CommittedAuthorityFact
	ProvisionIntent             *AllocationProvisionIntentV1
	ProvisionIntentFactDigest   string
	ProvisionPrepared           *AllocationStagingPreparedV1
	ProvisionPreparedFactDigest string
	ProvisionReceipt            *AllocationProvisionReceiptV1
	ProvisionReceiptFactDigest  string
	TerminateIntent             *AllocationTerminateIntentV1
	TerminateIntentFactDigest   string
	TerminateReceipt            *AllocationTerminateReceiptV1
	TerminateReceiptFactDigest  string
}

func (snapshot AuthoritySnapshot) Validate() error {
	if err := validateFactSequence(snapshot.Facts); err != nil || snapshot.ProvisionIntent == nil || snapshot.ProvisionIntent.Validate() != nil || !validDigest(snapshot.ProvisionIntentFactDigest) || !snapshot.hasExactFact(RecordProvisionIntent, snapshot.ProvisionIntentFactDigest, snapshot.ProvisionIntent.RequestDigest, *snapshot.ProvisionIntent) {
		return ErrAuthorityConflict
	}
	if snapshot.ProvisionPrepared == nil {
		if len(snapshot.Facts) != 1 || snapshot.ProvisionPreparedFactDigest != "" || snapshot.ProvisionReceipt != nil || snapshot.ProvisionReceiptFactDigest != "" || snapshot.TerminateIntent != nil || snapshot.TerminateReceipt != nil {
			return ErrAuthorityConflict
		}
		return nil
	}
	if snapshot.ProvisionPrepared.Validate(*snapshot.ProvisionIntent) != nil || snapshot.ProvisionPrepared.IntentFactDigest != snapshot.ProvisionIntentFactDigest || !validDigest(snapshot.ProvisionPreparedFactDigest) || !snapshot.hasExactFact(RecordProvisionPrepared, snapshot.ProvisionPreparedFactDigest, snapshot.ProvisionIntent.RequestDigest, *snapshot.ProvisionPrepared) {
		return ErrAuthorityConflict
	}
	if snapshot.ProvisionReceipt == nil {
		if len(snapshot.Facts) != 2 || snapshot.ProvisionReceiptFactDigest != "" || snapshot.TerminateIntent != nil || snapshot.TerminateReceipt != nil {
			return ErrAuthorityConflict
		}
		return nil
	}
	if snapshot.ProvisionReceipt.Validate(*snapshot.ProvisionIntent, *snapshot.ProvisionPrepared) != nil || snapshot.ProvisionReceipt.PreparedFactDigest != snapshot.ProvisionPreparedFactDigest || !validDigest(snapshot.ProvisionReceiptFactDigest) || !snapshot.hasExactFact(RecordProvisionReceipt, snapshot.ProvisionReceiptFactDigest, snapshot.ProvisionIntent.RequestDigest, *snapshot.ProvisionReceipt) {
		return ErrAuthorityConflict
	}
	if snapshot.TerminateIntent == nil {
		if len(snapshot.Facts) != 3 || snapshot.TerminateIntentFactDigest != "" || snapshot.TerminateReceipt != nil || snapshot.TerminateReceiptFactDigest != "" {
			return ErrAuthorityConflict
		}
		return nil
	}
	if snapshot.TerminateIntent.Validate() != nil || !sameAllocationScope(snapshot.ProvisionIntent.Binding, snapshot.TerminateIntent.Binding) || !sameDirectoryObject(snapshot.TerminateIntent.LiveIdentity, snapshot.ProvisionReceipt.LiveIdentity) || snapshot.TerminateIntent.MarkerIdentity != snapshot.ProvisionReceipt.MarkerIdentity || snapshot.TerminateIntent.Marker != snapshot.ProvisionReceipt.Marker || snapshot.TerminateIntent.MarkerDigest != snapshot.ProvisionReceipt.MarkerDigest || !validDigest(snapshot.TerminateIntentFactDigest) || !snapshot.hasExactFact(RecordTerminateIntent, snapshot.TerminateIntentFactDigest, snapshot.TerminateIntent.RequestDigest, *snapshot.TerminateIntent) {
		return ErrAuthorityConflict
	}
	if snapshot.TerminateReceipt == nil {
		if len(snapshot.Facts) != 4 || snapshot.TerminateReceiptFactDigest != "" {
			return ErrAuthorityConflict
		}
		return nil
	}
	if len(snapshot.Facts) != 5 || snapshot.TerminateReceipt.Validate(*snapshot.TerminateIntent) != nil || snapshot.TerminateReceipt.IntentFactDigest != snapshot.TerminateIntentFactDigest || !validDigest(snapshot.TerminateReceiptFactDigest) || !snapshot.hasExactFact(RecordTerminateReceipt, snapshot.TerminateReceiptFactDigest, snapshot.TerminateIntent.RequestDigest, *snapshot.TerminateReceipt) {
		return ErrAuthorityConflict
	}
	return nil
}

func (snapshot AuthoritySnapshot) hasExactFact(kind RecordKind, digest, requestDigest string, value any) bool {
	payload, err := EncodeFactPayload(value)
	if err != nil {
		return false
	}
	for _, fact := range snapshot.Facts {
		if fact.RecordKind == kind && fact.AttemptAuthorityFactDigest == digest && fact.RequestDigest == requestDigest && factMatchesValue(fact, value) && equalCanonical(fact.AuthorityFact, payload) {
			return true
		}
	}
	return false
}

func factMatchesValue(fact CommittedAuthorityFact, value any) bool {
	switch typed := value.(type) {
	case AllocationProvisionIntentV1:
		return fact.Binding == typed.Binding && fact.ExpectedAttemptSequence == typed.ExpectedAttemptSequence
	case AllocationStagingPreparedV1:
		return fact.Binding == typed.Binding
	case AllocationProvisionReceiptV1:
		return fact.Binding == typed.Binding
	case AllocationTerminateIntentV1:
		return fact.Binding == typed.Binding && fact.ExpectedAttemptSequence == typed.ExpectedAttemptSequence && fact.TerminalizationID == typed.TerminalizationID && fact.CleanupBindingDigest == typed.CleanupBindingDigest && fact.ProcessTerminalFactDigest == typed.ProcessTerminalFactDigest
	case AllocationTerminateReceiptV1:
		return fact.Binding == typed.Binding && fact.TerminalizationID == typed.TerminalizationID && fact.CleanupBindingDigest == typed.CleanupBindingDigest && fact.ProcessTerminalFactDigest == typed.ProcessTerminalFactDigest
	default:
		return false
	}
}

func sameAllocationScope(left, right AllocationBindingV1) bool {
	return left.AuthorityNamespaceID == right.AuthorityNamespaceID && left.TaskID == right.TaskID && left.RunID == right.RunID && left.AttemptID == right.AttemptID && left.AllocationID == right.AllocationID && left.LeaseID == right.LeaseID && left.Generation == right.Generation && left.FencingTokenDigest == right.FencingTokenDigest
}

func sameDirectoryObject(left, right ObjectIdentityV1) bool {
	return left.Validate(ObjectTypeDirectory) == nil && right.Validate(ObjectTypeDirectory) == nil && left.Device == right.Device && left.Inode == right.Inode && left.Mode == right.Mode && left.UID == right.UID && left.GID == right.GID && left.Type == right.Type
}

// AuthoritySession is implemented in stage 2 by ResultIngress while it holds
// current Run+Attempt+lease authority and the pending-effect barrier. Every
// append returns a fresh complete snapshot after the authority fsync.
type AuthoritySession interface {
	Snapshot() (AuthoritySnapshot, error)
	AppendProvisionPrepared(context.Context, AllocationStagingPreparedV1) (AuthoritySnapshot, error)
	AppendProvisionReceipt(context.Context, AllocationProvisionReceiptV1) (AuthoritySnapshot, error)
	AppendTerminateReceipt(context.Context, AllocationTerminateReceiptV1) (AuthoritySnapshot, error)
}

// Authority serializes one effect under current business authority. A journal
// or filesystem object can never implement this interface by itself.
type Authority interface {
	WithCurrentAllocation(context.Context, string, func(AuthoritySession) error) error
}

// Controller is the sole stage-1 recovery entry point. Its Store is a durable
// projection/data-plane; Authority remains the only source of permission.
type Controller struct {
	store     *Store
	authority Authority
}

func NewController(store *Store, authority Authority) (*Controller, error) {
	if store == nil || authority == nil {
		return nil, ErrInvalid
	}
	return &Controller{store: store, authority: authority}, nil
}

func (controller *Controller) RecoverProvision(ctx context.Context, effectID string) (AllocationProvisionReceiptV1, error) {
	if controller == nil || controller.store == nil || controller.authority == nil || !validText(effectID) {
		return AllocationProvisionReceiptV1{}, ErrInvalid
	}
	var result AllocationProvisionReceiptV1
	err := controller.authority.WithCurrentAllocation(ctx, effectID, func(session AuthoritySession) error {
		if session == nil {
			return ErrAuthorityConflict
		}
		snapshot, err := session.Snapshot()
		if err != nil || snapshot.Validate() != nil {
			return ErrAuthorityConflict
		}
		if err := controller.store.SyncAuthorityProjection(snapshot.Facts); err != nil {
			return err
		}
		if snapshot.TerminateIntent != nil {
			return ErrAuthorityConflict
		}
		if snapshot.ProvisionReceipt != nil {
			if err := controller.store.verifyProvisionReceipt(*snapshot.ProvisionIntent, *snapshot.ProvisionPrepared, *snapshot.ProvisionReceipt); err != nil {
				return err
			}
			result = *snapshot.ProvisionReceipt
			return nil
		}

		if snapshot.ProvisionPrepared == nil {
			prepared, err := controller.store.prepareProvision(*snapshot.ProvisionIntent, snapshot.ProvisionIntentFactDigest)
			if err != nil {
				return err
			}
			snapshot, err = session.AppendProvisionPrepared(ctx, prepared)
			if err != nil {
				return err
			}
			if snapshot.Validate() != nil || snapshot.ProvisionPrepared == nil || !equalCanonical(*snapshot.ProvisionPrepared, prepared) {
				return ErrAuthorityConflict
			}
			if err := controller.store.SyncAuthorityProjection(snapshot.Facts); err != nil {
				return err
			}
		}

		// This second read is mutation-adjacent. The stage-2 implementation keeps
		// the current authority transaction/barrier held across both reads.
		current, err := session.Snapshot()
		if err != nil || current.Validate() != nil || current.ProvisionPrepared == nil || current.ProvisionReceipt != nil || !equalCanonical(current.Facts, snapshot.Facts) {
			return ErrAuthorityConflict
		}
		receipt, err := controller.store.completeProvision(*current.ProvisionIntent, *current.ProvisionPrepared, current.ProvisionPreparedFactDigest)
		if err != nil {
			return err
		}
		current, err = session.AppendProvisionReceipt(ctx, receipt)
		if err != nil {
			return err
		}
		if current.Validate() != nil || current.ProvisionReceipt == nil || !equalCanonical(*current.ProvisionReceipt, receipt) {
			return ErrAuthorityConflict
		}
		if err := controller.store.SyncAuthorityProjection(current.Facts); err != nil {
			return err
		}
		result = receipt
		return nil
	})
	return result, err
}

func (controller *Controller) RecoverTerminate(ctx context.Context, effectID string) (AllocationTerminateReceiptV1, error) {
	if controller == nil || controller.store == nil || controller.authority == nil || !validText(effectID) {
		return AllocationTerminateReceiptV1{}, ErrInvalid
	}
	var result AllocationTerminateReceiptV1
	err := controller.authority.WithCurrentAllocation(ctx, effectID, func(session AuthoritySession) error {
		if session == nil {
			return ErrAuthorityConflict
		}
		snapshot, err := session.Snapshot()
		if err != nil || snapshot.Validate() != nil || snapshot.TerminateIntent == nil {
			return ErrAuthorityConflict
		}
		if err := controller.store.SyncAuthorityProjection(snapshot.Facts); err != nil {
			return err
		}
		if snapshot.TerminateReceipt != nil {
			if err := controller.store.verifyTerminateReceipt(*snapshot.TerminateIntent, *snapshot.TerminateReceipt); err != nil {
				return err
			}
			result = *snapshot.TerminateReceipt
			return nil
		}

		current, err := session.Snapshot()
		if err != nil || current.Validate() != nil || current.TerminateIntent == nil || current.TerminateReceipt != nil || !equalCanonical(current.Facts, snapshot.Facts) {
			return ErrAuthorityConflict
		}
		receipt, err := controller.store.completeTerminate(*current.TerminateIntent, current.TerminateIntentFactDigest)
		if err != nil {
			return err
		}
		current, err = session.AppendTerminateReceipt(ctx, receipt)
		if err != nil {
			return err
		}
		if current.Validate() != nil || current.TerminateReceipt == nil || !equalCanonical(*current.TerminateReceipt, receipt) {
			return ErrAuthorityConflict
		}
		if err := controller.store.SyncAuthorityProjection(current.Facts); err != nil {
			return err
		}
		result = receipt
		return nil
	})
	return result, err
}

func (controller *Controller) Close() error {
	if controller == nil || controller.store == nil {
		return nil
	}
	return controller.store.Close()
}
