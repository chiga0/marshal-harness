package allocationcontrol

import (
	"context"
)

// ExistingWorktreeAuthority owns the repository owner lock and opens one RB1
// transaction while the supplied AcquireExisting Run descriptor remains
// borrowed. Implementations must preserve owner -> Run lease -> RB1 ordering.
type ExistingWorktreeAuthority interface {
	WithExistingWorktreeSession(context.Context, DescriptorBoundRunV1, func(ExistingWorktreeAuthoritySession) error) error
}

// ExistingWorktreeAuthoritySession is the sole source of RB1 truth. Every
// append must fsync authority before returning the complete fresh snapshot.
type ExistingWorktreeAuthoritySession interface {
	// ObserveExistingWorktree and SyncExistingWorktreeProjection run inside the
	// same held descriptor graph owned by this session. Controller never opens
	// a repository, target or projection from a pathname.
	ObserveExistingWorktree(context.Context, ExistingWorktreeBindRequestV1) (ExistingWorktreeObservationV1, error)
	// VerifyExistingWorktreeTarget rechecks only the immutable held binding
	// anchors. It must not require the task's HEAD/index/worktree contents to
	// equal their bind-time values: successful work is expected to change them.
	VerifyExistingWorktreeTarget(context.Context, ExistingWorktreeBindRequestV1, ExistingWorktreeObservationV1) error
	SyncExistingWorktreeProjection(context.Context, ExistingWorktreeAuthoritySnapshotV1) error
	// VerifyCurrentBind must derive its result from the current owner, active
	// reservation, attempt-opened/v2 and DispatchLease authority. Caller bytes
	// are never sufficient admission evidence.
	VerifyCurrentBind(context.Context, ExistingWorktreeBindRequestV1, DescriptorBoundRunV1) (ExistingWorktreeCurrentAuthorityV1, error)
	// VerifyCurrentRelease must re-read current terminalization/cleanup/process
	// authority. It is called before both release facts while this session keeps
	// the complete lock order held.
	VerifyCurrentRelease(context.Context, ExistingWorktreeReleaseRequestV1, DescriptorBoundRunV1) (ExistingWorktreeCurrentAuthorityV1, error)
	Snapshot() (ExistingWorktreeAuthoritySnapshotV1, error)
	AppendBindIntent(context.Context, ExistingWorktreeBindIntentV1) (ExistingWorktreeAuthoritySnapshotV1, error)
	AppendBindReceipt(context.Context, ExistingWorktreeBindReceiptV1) (ExistingWorktreeAuthoritySnapshotV1, error)
	AppendReleaseIntent(context.Context, ExistingWorktreeReleaseIntentV1) (ExistingWorktreeAuthoritySnapshotV1, error)
	AppendReleaseReceipt(context.Context, ExistingWorktreeReleaseReceiptV1) (ExistingWorktreeAuthoritySnapshotV1, error)
}

type ExistingWorktreeAuthoritySnapshotV1 struct {
	Facts []ExistingWorktreeAuthorityFactV1
}

func (snapshot ExistingWorktreeAuthoritySnapshotV1) HeadDigest() string {
	if len(snapshot.Facts) == 0 {
		return ExistingWorktreeProjectionGenesis
	}
	return snapshot.Facts[len(snapshot.Facts)-1].FactDigest
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
	previous := ExistingWorktreeProjectionGenesis
	for index, fact := range snapshot.Facts {
		if fact.Validate() != nil || fact.Sequence != uint64(index+1) || fact.PreviousFactDigest != previous {
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
			chain.BindIntentFactDigest = fact.FactDigest
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
			chain.BindReceiptFactDigest = fact.FactDigest
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
			chain.ReleaseIntentFactDigest = fact.FactDigest
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
			chain.ReleaseReceiptFactDigest = fact.FactDigest
			chains[bindingDigest] = chain
			delete(activeTargets, targetDigest)
			object := chain.BindReceipt.Observation.TargetCurrentName.ObjectIdentity
			delete(activeObjects, object.Device+"\x00"+object.Inode)
		default:
			return nil, ErrAuthorityConflict
		}
		if predecessor != fact.PreviousFactDigest {
			return nil, ErrAuthorityConflict
		}
		previous = fact.FactDigest
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
	err := controller.authority.WithExistingWorktreeSession(ctx, run, func(session ExistingWorktreeAuthoritySession) error {
		current, currentErr := session.VerifyCurrentBind(ctx, request, run)
		if currentErr != nil || current.validateBind(request, run) != nil {
			return ErrAuthorityConflict
		}
		snapshot, err := session.Snapshot()
		if err != nil || snapshot.Validate() != nil {
			return ErrAuthorityConflict
		}
		chain, exists, err := snapshot.chainFor(request.Binding)
		if err != nil {
			return err
		}
		if exists {
			if chain.BindIntent == nil || chain.BindIntent.Request.RequestDigest != request.RequestDigest || !equalCanonical(chain.BindIntent.Request, request) {
				return ErrAuthorityConflict
			}
			if chain.ReleaseIntent != nil {
				return ErrAuthorityConflict
			}
			if chain.BindReceipt != nil {
				observation, observeErr := session.ObserveExistingWorktree(ctx, request)
				if observeErr != nil || !equalCanonical(observation, chain.BindReceipt.Observation) {
					return ErrFilesystemConflict
				}
				if err := session.SyncExistingWorktreeProjection(ctx, snapshot); err != nil {
					return err
				}
				result = *chain.BindReceipt
				return nil
			}
		}

		if !exists {
			observation, err := session.ObserveExistingWorktree(ctx, request)
			if err != nil {
				return err
			}
			bindingDigest, _ := request.Binding.Digest()
			intent := ExistingWorktreeBindIntentV1{Request: request, Observation: observation, BindingDigest: bindingDigest, PredecessorRB1HeadDigest: snapshot.HeadDigest()}
			if err := intent.Seal(); err != nil {
				return err
			}
			snapshot, err = session.AppendBindIntent(ctx, intent)
			if err != nil || snapshot.Validate() != nil {
				return ErrAuthorityConflict
			}
			chain, exists, err = snapshot.chainFor(request.Binding)
			if err != nil || !exists || chain.BindIntent == nil || !equalCanonical(*chain.BindIntent, intent) {
				return ErrAuthorityConflict
			}
		}
		if chain.BindIntent == nil || chain.BindReceipt != nil {
			return ErrAuthorityConflict
		}
		observation, err := session.ObserveExistingWorktree(ctx, request)
		if err != nil || !equalCanonical(observation, chain.BindIntent.Observation) {
			return ErrFilesystemConflict
		}
		receipt := ExistingWorktreeBindReceiptV1{
			Binding: request.Binding, RequestDigest: request.RequestDigest, IntentFactDigest: chain.BindIntentFactDigest,
			Observation: observation, BindingDigest: chain.BindIntent.BindingDigest,
			PredecessorRB1HeadDigest: snapshot.HeadDigest(), Disposition: DispositionApplied,
		}
		if err := receipt.Seal(); err != nil || receipt.Validate(*chain.BindIntent) != nil {
			return ErrInvalid
		}
		current, currentErr = session.VerifyCurrentBind(ctx, request, run)
		if currentErr != nil || current.validateBind(request, run) != nil {
			return ErrAuthorityConflict
		}
		snapshot, err = session.AppendBindReceipt(ctx, receipt)
		if err != nil || snapshot.Validate() != nil {
			return ErrAuthorityConflict
		}
		chain, exists, err = snapshot.chainFor(request.Binding)
		if err != nil || !exists || chain.BindReceipt == nil || !equalCanonical(*chain.BindReceipt, receipt) {
			return ErrAuthorityConflict
		}
		if err := session.SyncExistingWorktreeProjection(ctx, snapshot); err != nil {
			return err
		}
		result = *chain.BindReceipt
		return nil
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
	err := controller.authority.WithExistingWorktreeSession(ctx, run, func(session ExistingWorktreeAuthoritySession) error {
		current, currentErr := session.VerifyCurrentRelease(ctx, request, run)
		if currentErr != nil || current.validateRelease(request, run) != nil {
			return ErrAuthorityConflict
		}
		snapshot, err := session.Snapshot()
		if err != nil || snapshot.Validate() != nil {
			return ErrAuthorityConflict
		}
		chain, exists, err := snapshot.chainFor(request.Binding)
		if err != nil || !exists || chain.BindIntent == nil || chain.BindReceipt == nil || chain.BindReceipt.ReceiptDigest != request.BindingReceiptDigest {
			return ErrAuthorityConflict
		}
		if chain.ReleaseIntent != nil {
			if chain.ReleaseIntent.Request.RequestDigest != request.RequestDigest || !equalCanonical(chain.ReleaseIntent.Request, request) {
				return ErrAuthorityConflict
			}
			if chain.ReleaseReceipt != nil {
				if err := session.SyncExistingWorktreeProjection(ctx, snapshot); err != nil {
					return err
				}
				result = *chain.ReleaseReceipt
				return nil
			}
		} else {
			if verifyErr := session.VerifyExistingWorktreeTarget(ctx, chain.BindIntent.Request, chain.BindReceipt.Observation); verifyErr != nil {
				return ErrFilesystemConflict
			}
			intent := ExistingWorktreeReleaseIntentV1{Request: request, TargetIdentityDigest: chain.BindReceipt.Observation.TargetIdentityDigest, BindingDigest: chain.BindReceipt.BindingDigest, PredecessorRB1HeadDigest: snapshot.HeadDigest()}
			if err := intent.Seal(); err != nil {
				return err
			}
			snapshot, err = session.AppendReleaseIntent(ctx, intent)
			if err != nil || snapshot.Validate() != nil {
				return ErrAuthorityConflict
			}
			chain, exists, err = snapshot.chainFor(request.Binding)
			if err != nil || !exists || chain.ReleaseIntent == nil || !equalCanonical(*chain.ReleaseIntent, intent) {
				return ErrAuthorityConflict
			}
		}
		if chain.ReleaseIntent == nil || chain.ReleaseReceipt != nil {
			return ErrAuthorityConflict
		}
		if verifyErr := session.VerifyExistingWorktreeTarget(ctx, chain.BindIntent.Request, chain.BindReceipt.Observation); verifyErr != nil {
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
		current, currentErr = session.VerifyCurrentRelease(ctx, request, run)
		if currentErr != nil || current.validateRelease(request, run) != nil {
			return ErrAuthorityConflict
		}
		snapshot, err = session.AppendReleaseReceipt(ctx, receipt)
		if err != nil || snapshot.Validate() != nil {
			return ErrAuthorityConflict
		}
		chain, exists, err = snapshot.chainFor(request.Binding)
		if err != nil || !exists || chain.ReleaseReceipt == nil || !equalCanonical(*chain.ReleaseReceipt, receipt) {
			return ErrAuthorityConflict
		}
		if err := session.SyncExistingWorktreeProjection(ctx, snapshot); err != nil {
			return err
		}
		result = *chain.ReleaseReceipt
		return nil
	})
	if err != nil {
		return ExistingWorktreeReleaseReceiptV1{}, err
	}
	return result, nil
}

func decodeExistingWorktreePayload[T any](fact ExistingWorktreeAuthorityFactV1) (T, error) {
	var value T
	if fact.Validate() != nil || strictCanonicalDecode(fact.Payload, &value) != nil {
		return value, ErrAuthorityConflict
	}
	return value, nil
}
