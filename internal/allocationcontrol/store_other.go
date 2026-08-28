//go:build !darwin

package allocationcontrol

// Store is deliberately inert off Darwin. ADR 0057 does not permit the
// production allocation profile to degrade to path-based or memory behavior.
type Store struct{}

func OpenStore(string, AllocationStoreScopeV1) (*Store, error) { return nil, ErrPlatformUnavailable }

func (store *Store) Close() error { return nil }

func (store *Store) SyncAuthorityProjection([]CommittedAuthorityFact) error {
	return ErrPlatformUnavailable
}

func (store *Store) JournalRecords() []JournalRecord { return nil }

func (store *Store) prepareProvision(AllocationProvisionIntentV1, string) (AllocationStagingPreparedV1, error) {
	return AllocationStagingPreparedV1{}, ErrPlatformUnavailable
}

func (store *Store) provisionNeedsPreparationMutation(AllocationProvisionIntentV1) (bool, error) {
	return false, ErrPlatformUnavailable
}

func (store *Store) completeProvision(AllocationProvisionIntentV1, AllocationStagingPreparedV1, string) (AllocationProvisionReceiptV1, error) {
	return AllocationProvisionReceiptV1{}, ErrPlatformUnavailable
}

func (store *Store) verifyProvisionReceipt(AllocationProvisionIntentV1, AllocationStagingPreparedV1, AllocationProvisionReceiptV1) error {
	return ErrPlatformUnavailable
}

func (store *Store) completeTerminate(AllocationTerminateIntentV1, string) (AllocationTerminateReceiptV1, error) {
	return AllocationTerminateReceiptV1{}, ErrPlatformUnavailable
}

func (store *Store) terminateNeedsMutation(AllocationTerminateIntentV1) (bool, error) {
	return false, ErrPlatformUnavailable
}

func (store *Store) prepareTerminateIntent(TerminateRequestV1) (AllocationTerminateIntentV1, error) {
	return AllocationTerminateIntentV1{}, ErrPlatformUnavailable
}

func (store *Store) verifyTerminateReceipt(AllocationTerminateIntentV1, AllocationTerminateReceiptV1) error {
	return ErrPlatformUnavailable
}
