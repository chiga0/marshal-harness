package allocationcontrol

import (
	"context"
)

// ExistingWorktreeAuthority owns the repository owner lock and opens one RB1
// transaction while the supplied AcquireExisting Run descriptor remains
// borrowed. Implementations must preserve owner -> Run lease -> RB1 ordering.
type ExistingWorktreeAuthority interface {
	WithCurrentExistingWorktreeBind(context.Context, DescriptorBoundRunV1, ExistingWorktreeBindRequestV1, func(ExistingWorktreeAuthoritySession) error) error
	WithCurrentExistingWorktreeRelease(context.Context, DescriptorBoundRunV1, ExistingWorktreeReleaseRequestV1, func(ExistingWorktreeAuthoritySession) error) error
}

// ExistingWorktreeAuthoritySession is the sole source of RB1 truth. Every
// append must fsync authority before returning the complete fresh snapshot.
type ExistingWorktreeAuthoritySession interface {
	CurrentAuthority() ExistingWorktreeCurrentAuthorityV1
	// WithExistingWorktreeTarget holds the complete target/admin/common-dir and
	// ancestor descriptor graph across current-ledger admission, append+fsync
	// and the final current-name recheck. expected is nil for bind admission and
	// the durable bind observation for release.
	WithExistingWorktreeTarget(context.Context, ExistingWorktreeBindRequestV1, *ExistingWorktreeObservationV1, func(ExistingWorktreeTargetSession) error) error
	SyncExistingWorktreeProjection(context.Context, ExistingWorktreeAuthoritySnapshotV1) error
	Snapshot() (ExistingWorktreeAuthoritySnapshotV1, error)
	AppendBindIntent(context.Context, ExistingWorktreeBindIntentV1) (ExistingWorktreeAuthoritySnapshotV1, error)
	AppendBindReceipt(context.Context, ExistingWorktreeBindReceiptV1) (ExistingWorktreeAuthoritySnapshotV1, error)
	AppendReleaseIntent(context.Context, ExistingWorktreeReleaseIntentV1) (ExistingWorktreeAuthoritySnapshotV1, error)
	AppendReleaseReceipt(context.Context, ExistingWorktreeReleaseReceiptV1) (ExistingWorktreeAuthoritySnapshotV1, error)
}

type ExistingWorktreeTargetSession interface {
	Observation() (ExistingWorktreeObservationV1, error)
	Revalidate() error
}

type ExistingWorktreeAuthoritySnapshotV1 struct {
	CurrentAttemptRevision   uint64
	CurrentAttemptHeadDigest string
	Facts                    []ExistingWorktreeAttemptFactV1
}

func (snapshot ExistingWorktreeAuthoritySnapshotV1) HeadDigest() string {
	return snapshot.CurrentAttemptHeadDigest
}

type existingWorktreeChain struct {
	BindIntent               *ExistingWorktreeBindIntentV1
	BindIntentFactDigest     string
	BindReceipt              *ExistingWorktreeBindReceiptV1
	BindReceiptFactDigest    string
	ReleaseIntent            *ExistingWorktreeReleaseIntentV1
	ReleaseIntentFactDigest  string
	ReleaseReceipt           *ExistingWorktreeReleaseReceiptV1
	ReleaseReceiptFactDigest string
}

func (snapshot ExistingWorktreeAuthoritySnapshotV1) Validate() error {
	_, err := snapshot.chains()
	return err
}

func (snapshot ExistingWorktreeAuthoritySnapshotV1) chains() (map[string]existingWorktreeChain, error) {
	chains := make(map[string]existingWorktreeChain)
	activeTargets := make(map[string]string)
	activeObjects := make(map[string]string)
	boundAttempts := make(map[string]string)
	boundAttemptFacts := make(map[string]string)
	boundReservations := make(map[string]string)
	if snapshot.CurrentAttemptRevision == 0 || !validDigest(snapshot.CurrentAttemptHeadDigest) {
		return nil, ErrAuthorityConflict
	}
	for _, fact := range snapshot.Facts {
		if fact.Validate() != nil {
			return nil, ErrAuthorityConflict
		}
		var bindingDigest, targetDigest, predecessor string
		chain := existingWorktreeChain{}
		switch fact.Kind {
		case ExistingWorktreeFactBindIntent:
			var value ExistingWorktreeBindIntentV1
			if strictCanonicalDecode(fact.Payload, &value) != nil || value.Validate() != nil {
				return nil, ErrAuthorityConflict
			}
			bindingDigest, targetDigest, predecessor = value.BindingDigest, value.Observation.TargetIdentityDigest, value.PredecessorRB1HeadDigest
			if _, exists := chains[bindingDigest]; exists {
				return nil, ErrAuthorityConflict
			}
			attemptKey := value.Request.Binding.RunID + "\x00" + value.Request.Binding.AttemptID
			attemptFactKey := value.Request.Binding.AttemptOpenedFactDigest
			reservationKey := value.Request.Binding.ReservationFactDigest
			objectKey := value.Observation.TargetCurrentName.ObjectIdentity.Device + "\x00" + value.Observation.TargetCurrentName.ObjectIdentity.Inode
			if other, exists := activeTargets[targetDigest]; exists && other != bindingDigest {
				return nil, ErrAuthorityConflict
			}
			if other, exists := activeObjects[objectKey]; exists && other != bindingDigest {
				return nil, ErrAuthorityConflict
			}
			if _, exists := boundAttempts[attemptKey]; exists {
				return nil, ErrAuthorityConflict
			}
			if _, exists := boundAttemptFacts[attemptFactKey]; exists {
				return nil, ErrAuthorityConflict
			}
			if _, exists := boundReservations[reservationKey]; exists {
				return nil, ErrAuthorityConflict
			}
			chain.BindIntent = &value
			chain.BindIntentFactDigest = fact.AttemptFactDigest
			chains[bindingDigest] = chain
			activeTargets[targetDigest] = bindingDigest
			activeObjects[objectKey] = bindingDigest
			boundAttempts[attemptKey] = bindingDigest
			boundAttemptFacts[attemptFactKey] = bindingDigest
			boundReservations[reservationKey] = bindingDigest
		case ExistingWorktreeFactBindReceipt:
			var value ExistingWorktreeBindReceiptV1
			if strictCanonicalDecode(fact.Payload, &value) != nil {
				return nil, ErrAuthorityConflict
			}
			bindingDigest, predecessor = value.BindingDigest, value.PredecessorRB1HeadDigest
			chain, ok := chains[bindingDigest]
			if !ok || chain.BindIntent == nil || chain.BindReceipt != nil || value.IntentFactDigest != chain.BindIntentFactDigest || value.Validate(*chain.BindIntent) != nil {
				return nil, ErrAuthorityConflict
			}
			chain.BindReceipt = &value
			chain.BindReceiptFactDigest = fact.AttemptFactDigest
			chains[bindingDigest] = chain
		case ExistingWorktreeFactReleaseIntent:
			var value ExistingWorktreeReleaseIntentV1
			if strictCanonicalDecode(fact.Payload, &value) != nil || value.Validate() != nil {
				return nil, ErrAuthorityConflict
			}
			bindingDigest, targetDigest, predecessor = value.BindingDigest, value.TargetIdentityDigest, value.PredecessorRB1HeadDigest
			chain, ok := chains[bindingDigest]
			if !ok || chain.BindReceipt == nil || chain.ReleaseIntent != nil || value.Request.BindingReceiptDigest != chain.BindReceipt.ReceiptDigest || targetDigest != chain.BindReceipt.Observation.TargetIdentityDigest {
				return nil, ErrAuthorityConflict
			}
			chain.ReleaseIntent = &value
			chain.ReleaseIntentFactDigest = fact.AttemptFactDigest
			chains[bindingDigest] = chain
		case ExistingWorktreeFactReleaseReceipt:
			var value ExistingWorktreeReleaseReceiptV1
			if strictCanonicalDecode(fact.Payload, &value) != nil {
				return nil, ErrAuthorityConflict
			}
			bindingDigest, targetDigest, predecessor = mustExistingWorktreeBindingDigest(value.Binding), value.TargetIdentityDigest, value.PredecessorRB1HeadDigest
			chain, ok := chains[bindingDigest]
			if !ok || chain.ReleaseIntent == nil || chain.ReleaseReceipt != nil || value.ReleaseIntentFactDigest != chain.ReleaseIntentFactDigest || value.Validate(*chain.ReleaseIntent) != nil {
				return nil, ErrAuthorityConflict
			}
			chain.ReleaseReceipt = &value
			chain.ReleaseReceiptFactDigest = fact.AttemptFactDigest
			chains[bindingDigest] = chain
			delete(activeTargets, targetDigest)
			object := chain.BindReceipt.Observation.TargetCurrentName.ObjectIdentity
			delete(activeObjects, object.Device+"\x00"+object.Inode)
		default:
			return nil, ErrAuthorityConflict
		}
		if predecessor != fact.PreviousAttemptHeadDigest {
			return nil, ErrAuthorityConflict
		}
	}
	return chains, nil
}

func mustExistingWorktreeBindingDigest(binding ExistingWorktreeBindingV1) string {
	digest, _ := binding.Digest()
	return digest
}

func (snapshot ExistingWorktreeAuthoritySnapshotV1) chainFor(binding ExistingWorktreeBindingV1) (existingWorktreeChain, bool, error) {
	chains, err := snapshot.chains()
	if err != nil {
		return existingWorktreeChain{}, false, err
	}
	digest, err := binding.Digest()
	if err != nil {
		return existingWorktreeChain{}, false, err
	}
	chain, ok := chains[digest]
	return chain, ok, nil
}

// ExistingWorktreeController implements S2'-RB1 logical binding. It never
// creates, mutates, cleans, resets, moves, prunes or deletes a Git worktree.
type ExistingWorktreeController struct {
	authority ExistingWorktreeAuthority
}

func NewExistingWorktreeController(authority ExistingWorktreeAuthority) (*ExistingWorktreeController, error) {
	if authority == nil {
		return nil, ErrInvalid
	}
	return &ExistingWorktreeController{authority: authority}, nil
}

func (controller *ExistingWorktreeController) Bind(ctx context.Context, run DescriptorBoundRunV1, request ExistingWorktreeBindRequestV1) (ExistingWorktreeBindReceiptV1, error) {
	if controller == nil || controller.authority == nil || request.Validate() != nil || run.validate(request.Binding) != nil || request.RunDirectoryIdentity != run.DirectoryIdentity || request.RunAuthorityHeadDigest != run.AuthorityHeadDigest {
		return ExistingWorktreeBindReceiptV1{}, ErrInvalid
	}
	if err := validateDescriptorBoundRun(run); err != nil {
		return ExistingWorktreeBindReceiptV1{}, err
	}
	var result ExistingWorktreeBindReceiptV1
	err := controller.authority.WithCurrentExistingWorktreeBind(ctx, run, request, func(session ExistingWorktreeAuthoritySession) error {
		if session == nil || session.CurrentAuthority().validateBind(request, run) != nil {
			return ErrAuthorityConflict
		}
		return session.WithExistingWorktreeTarget(ctx, request, nil, func(target ExistingWorktreeTargetSession) error {
			observation, err := target.Observation()
			if err != nil {
				return err
			}
			snapshot, err := session.Snapshot()
			if err != nil || snapshot.Validate() != nil {
				return ErrAuthorityConflict
			}
			// Reconcile the complete current prefix before adding a new RB1
			// fact. This closes intent/receipt response-loss windows without
			// allowing a later corrupt entry to be discovered after a write.
			if err := session.SyncExistingWorktreeProjection(ctx, snapshot); err != nil || target.Revalidate() != nil {
				return ErrFilesystemConflict
			}
			chain, exists, err := snapshot.chainFor(request.Binding)
			if err != nil {
				return err
			}
			if exists {
				if chain.BindIntent == nil || chain.BindIntent.Request.RequestDigest != request.RequestDigest || !equalCanonical(chain.BindIntent.Request, request) || chain.ReleaseIntent != nil {
					return ErrAuthorityConflict
				}
				if chain.BindReceipt != nil {
					if !equalCanonical(observation, chain.BindReceipt.Observation) || target.Revalidate() != nil {
						return ErrFilesystemConflict
					}
					if err := session.SyncExistingWorktreeProjection(ctx, snapshot); err != nil || target.Revalidate() != nil {
						return ErrFilesystemConflict
					}
					result = *chain.BindReceipt
					return nil
				}
			}
			if !exists {
				bindingDigest, _ := request.Binding.Digest()
				intent := ExistingWorktreeBindIntentV1{Request: request, Observation: observation, BindingDigest: bindingDigest, PredecessorRB1HeadDigest: snapshot.HeadDigest()}
				if err := intent.Seal(); err != nil || target.Revalidate() != nil {
					return ErrFilesystemConflict
				}
				snapshot, err = session.AppendBindIntent(ctx, intent)
				if err != nil || snapshot.Validate() != nil || target.Revalidate() != nil {
					return ErrAuthorityConflict
				}
				if err := session.SyncExistingWorktreeProjection(ctx, snapshot); err != nil || target.Revalidate() != nil {
					return ErrFilesystemConflict
				}
				chain, exists, err = snapshot.chainFor(request.Binding)
				if err != nil || !exists || chain.BindIntent == nil || !equalCanonical(*chain.BindIntent, intent) {
					return ErrAuthorityConflict
				}
			}
			if chain.BindIntent == nil || chain.BindReceipt != nil || !equalCanonical(observation, chain.BindIntent.Observation) {
				return ErrAuthorityConflict
			}
			receipt := ExistingWorktreeBindReceiptV1{
				Binding: request.Binding, RequestDigest: request.RequestDigest, IntentFactDigest: chain.BindIntentFactDigest,
				Observation: observation, BindingDigest: chain.BindIntent.BindingDigest,
				PredecessorRB1HeadDigest: snapshot.HeadDigest(), Disposition: DispositionApplied,
			}
			if err := receipt.Seal(); err != nil || receipt.Validate(*chain.BindIntent) != nil || target.Revalidate() != nil {
				return ErrInvalid
			}
			snapshot, err = session.AppendBindReceipt(ctx, receipt)
			if err != nil || snapshot.Validate() != nil || target.Revalidate() != nil {
				return ErrAuthorityConflict
			}
			chain, exists, err = snapshot.chainFor(request.Binding)
			if err != nil || !exists || chain.BindReceipt == nil || !equalCanonical(*chain.BindReceipt, receipt) {
				return ErrAuthorityConflict
			}
			if err := session.SyncExistingWorktreeProjection(ctx, snapshot); err != nil || target.Revalidate() != nil {
				return ErrFilesystemConflict
			}
			result = *chain.BindReceipt
			return nil
		})
	})
	if err != nil {
		return ExistingWorktreeBindReceiptV1{}, err
	}
	return result, nil
}

func (controller *ExistingWorktreeController) Release(ctx context.Context, run DescriptorBoundRunV1, request ExistingWorktreeReleaseRequestV1) (ExistingWorktreeReleaseReceiptV1, error) {
	if controller == nil || controller.authority == nil || request.Validate() != nil || run.validate(request.Binding) != nil || request.RunAuthorityHeadDigest != run.AuthorityHeadDigest {
		return ExistingWorktreeReleaseReceiptV1{}, ErrInvalid
	}
	if err := validateDescriptorBoundRun(run); err != nil {
		return ExistingWorktreeReleaseReceiptV1{}, err
	}
	var result ExistingWorktreeReleaseReceiptV1
	err := controller.authority.WithCurrentExistingWorktreeRelease(ctx, run, request, func(session ExistingWorktreeAuthoritySession) error {
		if session == nil || session.CurrentAuthority().validateRelease(request, run) != nil {
			return ErrAuthorityConflict
		}
		snapshot, err := session.Snapshot()
		if err != nil || snapshot.Validate() != nil {
			return ErrAuthorityConflict
		}
		if err := session.SyncExistingWorktreeProjection(ctx, snapshot); err != nil {
			return ErrFilesystemConflict
		}
		chain, exists, err := snapshot.chainFor(request.Binding)
		if err != nil || !exists || chain.BindIntent == nil || chain.BindReceipt == nil || chain.BindReceipt.ReceiptDigest != request.BindingReceiptDigest {
			return ErrAuthorityConflict
		}
		return session.WithExistingWorktreeTarget(ctx, chain.BindIntent.Request, &chain.BindReceipt.Observation, func(target ExistingWorktreeTargetSession) error {
			if chain.ReleaseIntent != nil {
				if chain.ReleaseIntent.Request.RequestDigest != request.RequestDigest || !equalCanonical(chain.ReleaseIntent.Request, request) {
					return ErrAuthorityConflict
				}
				if chain.ReleaseReceipt != nil {
					if err := session.SyncExistingWorktreeProjection(ctx, snapshot); err != nil {
						return err
					}
					if target.Revalidate() != nil {
						return ErrFilesystemConflict
					}
					result = *chain.ReleaseReceipt
					return nil
				}
			} else {
				if verifyErr := target.Revalidate(); verifyErr != nil {
					return ErrFilesystemConflict
				}
				intent := ExistingWorktreeReleaseIntentV1{Request: request, TargetIdentityDigest: chain.BindReceipt.Observation.TargetIdentityDigest, BindingDigest: chain.BindReceipt.BindingDigest, PredecessorRB1HeadDigest: snapshot.HeadDigest()}
				if err := intent.Seal(); err != nil {
					return err
				}
				snapshot, err = session.AppendReleaseIntent(ctx, intent)
				if err != nil || snapshot.Validate() != nil || target.Revalidate() != nil {
					return ErrAuthorityConflict
				}
				if err := session.SyncExistingWorktreeProjection(ctx, snapshot); err != nil || target.Revalidate() != nil {
					return ErrFilesystemConflict
				}
				chain, exists, err = snapshot.chainFor(request.Binding)
				if err != nil || !exists || chain.ReleaseIntent == nil || !equalCanonical(*chain.ReleaseIntent, intent) {
					return ErrAuthorityConflict
				}
			}
			if chain.ReleaseIntent == nil || chain.ReleaseReceipt != nil {
				return ErrAuthorityConflict
			}
			if verifyErr := target.Revalidate(); verifyErr != nil {
				return ErrFilesystemConflict
			}
			receipt := ExistingWorktreeReleaseReceiptV1{
				Binding: request.Binding, RequestDigest: request.RequestDigest, ReleaseIntentFactDigest: chain.ReleaseIntentFactDigest,
				BindingReceiptDigest: request.BindingReceiptDigest, TargetIdentityDigest: chain.BindReceipt.Observation.TargetIdentityDigest,
				PredecessorRB1HeadDigest: snapshot.HeadDigest(), Disposition: "released",
			}
			if err := receipt.Seal(); err != nil || receipt.Validate(*chain.ReleaseIntent) != nil {
				return ErrInvalid
			}
			snapshot, err = session.AppendReleaseReceipt(ctx, receipt)
			if err != nil || snapshot.Validate() != nil || target.Revalidate() != nil {
				return ErrAuthorityConflict
			}
			chain, exists, err = snapshot.chainFor(request.Binding)
			if err != nil || !exists || chain.ReleaseReceipt == nil || !equalCanonical(*chain.ReleaseReceipt, receipt) {
				return ErrAuthorityConflict
			}
			if err := session.SyncExistingWorktreeProjection(ctx, snapshot); err != nil {
				return err
			}
			if target.Revalidate() != nil {
				return ErrFilesystemConflict
			}
			result = *chain.ReleaseReceipt
			return nil
		})
	})
	if err != nil {
		return ExistingWorktreeReleaseReceiptV1{}, err
	}
	return result, nil
}
