//go:build darwin

package allocationcontrol

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"golang.org/x/sys/unix"
)

const (
	storeDirectoryName   = "allocation-recovery-v1"
	objectsDirectoryName = "objects"
	markerReadLimit      = 64 << 10
)

func (scope AllocationStoreScopeV1) directoryName() (string, error) {
	if scope.Validate() != nil {
		return "", ErrInvalid
	}
	digest, err := digestValue(scope)
	if err != nil {
		return "", err
	}
	return "scope-" + strings.TrimPrefix(digest, "sha256:"), nil
}

func validRelativeName(value string) bool {
	return validPrintableASCII(value, 255) && value != "." && value != ".." && !strings.ContainsAny(value, `/\\`)
}

func requireRelativeName(value string) error {
	if !validRelativeName(value) {
		return fmt.Errorf("%w: relative name", ErrInvalid)
	}
	return nil
}

// Store owns held descriptors for the complete Darwin allocation namespace.
// After OpenStore, all mutation and observation is descriptor-relative; root
// is retained only for diagnostics by the caller and is never reopened.
type Store struct {
	mu      sync.Mutex
	base    *os.File
	objects *os.File
	journal *RecoveryJournal
	scope   AllocationStoreScopeV1
	uid     uint32
	gid     uint32
}

type allocationObservation struct {
	present        bool
	objectIdentity ObjectIdentityV1
	markerIdentity ObjectIdentityV1
	marker         AllocationIdentityMarkerV1
	markerDigest   string
}

func OpenStore(root string, scope AllocationStoreScopeV1) (*Store, error) {
	if scope.Validate() != nil {
		return nil, ErrInvalid
	}
	rootFD, err := openExistingDirectoryPath(root)
	if err != nil {
		return nil, err
	}
	rootFile := os.NewFile(uintptr(rootFD), "allocation-root")
	defer rootFile.Close()

	recoveryRoot, err := openOrCreatePrivateDirectory(rootFile, storeDirectoryName)
	if err != nil {
		return nil, err
	}
	defer recoveryRoot.Close()
	scopeName, err := scope.directoryName()
	if err != nil {
		return nil, err
	}
	base, err := openOrCreatePrivateDirectory(recoveryRoot, scopeName)
	if err != nil {
		return nil, err
	}
	objects, err := openOrCreatePrivateDirectory(base, objectsDirectoryName)
	if err != nil {
		base.Close()
		return nil, err
	}
	journalFile, err := openOrCreateJournal(base)
	if err != nil {
		objects.Close()
		base.Close()
		return nil, err
	}
	journal, err := newRecoveryJournal(journalFile)
	if err != nil {
		journalFile.Close()
		objects.Close()
		base.Close()
		return nil, err
	}
	return &Store{base: base, objects: objects, journal: journal, scope: scope, uid: uint32(unix.Geteuid()), gid: uint32(unix.Getegid())}, nil
}

func (store *Store) Close() error {
	if store == nil {
		return nil
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	var result error
	if store.journal != nil {
		result = errors.Join(result, store.journal.Close())
		store.journal = nil
	}
	if store.objects != nil {
		result = errors.Join(result, store.objects.Close())
		store.objects = nil
	}
	if store.base != nil {
		result = errors.Join(result, store.base.Close())
		store.base = nil
	}
	return result
}

func (store *Store) SyncAuthorityProjection(facts []CommittedAuthorityFact) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.journal == nil {
		return ErrInvalid
	}
	for _, fact := range facts {
		if !store.scope.Matches(fact.Binding) {
			return ErrAuthorityConflict
		}
	}
	return store.journal.SyncAuthorityProjection(facts)
}

func (store *Store) JournalRecords() []JournalRecord {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.journal == nil {
		return nil
	}
	return store.journal.Records()
}

func (store *Store) prepareProvision(intent AllocationProvisionIntentV1, intentFactDigest string) (AllocationStagingPreparedV1, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.objects == nil || intent.Validate() != nil || intent.ExpectedOwnerUID != store.uid || !store.scope.Matches(intent.Binding) || !validDigest(intentFactDigest) {
		return AllocationStagingPreparedV1{}, ErrInvalid
	}
	staging, err := store.inspectPreparedStaging(intent.StagingRelativeName, intent.MarkerRelativeName)
	if err != nil {
		return AllocationStagingPreparedV1{}, err
	}
	live, err := store.inspectAllocation(intent.LiveRelativeName, intent.MarkerRelativeName)
	if err != nil {
		return AllocationStagingPreparedV1{}, err
	}
	_, _, tombstoneName, _, _ := DeriveRelativeNames(intent.Binding.AllocationID)
	tombstone, err := store.inspectAllocation(tombstoneName, intent.MarkerRelativeName)
	if err != nil {
		return AllocationStagingPreparedV1{}, err
	}
	if live.present || tombstone.present {
		return AllocationStagingPreparedV1{}, ErrFilesystemConflict
	}
	marker := intent.Marker()
	if staging.present {
		if staging.marker != marker {
			return AllocationStagingPreparedV1{}, ErrFilesystemConflict
		}
		staging, err = store.syncPreparedStaging(intent.StagingRelativeName, intent.MarkerRelativeName, staging)
		if err != nil {
			return AllocationStagingPreparedV1{}, err
		}
		return buildPrepared(intent, intentFactDigest, staging)
	}

	if !staging.present {
		if err := unix.Mkdirat(int(store.objects.Fd()), intent.StagingRelativeName, 0o700); err != nil {
			if errors.Is(err, unix.EEXIST) {
				return AllocationStagingPreparedV1{}, ErrFilesystemConflict
			}
			return AllocationStagingPreparedV1{}, err
		}
	}
	stagingFD, err := unix.Openat(int(store.objects.Fd()), intent.StagingRelativeName, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return AllocationStagingPreparedV1{}, err
	}
	stagingFile := os.NewFile(uintptr(stagingFD), intent.StagingRelativeName)
	defer stagingFile.Close()
	if err := verifyPrivateDirectory(stagingFD, store.uid); err != nil {
		return AllocationStagingPreparedV1{}, err
	}
	markerBytes, err := marker.Canonical()
	if err != nil {
		return AllocationStagingPreparedV1{}, err
	}
	markerFD, err := unix.Openat(stagingFD, intent.MarkerRelativeName, unix.O_WRONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_CREAT|unix.O_EXCL, 0o600)
	if err != nil {
		return AllocationStagingPreparedV1{}, err
	}
	markerFile := os.NewFile(uintptr(markerFD), intent.MarkerRelativeName)
	writeErr := writeAll(markerFile, markerBytes)
	if writeErr == nil {
		writeErr = markerFile.Sync()
	}
	closeErr := markerFile.Close()
	if writeErr != nil {
		return AllocationStagingPreparedV1{}, writeErr
	}
	if closeErr != nil {
		return AllocationStagingPreparedV1{}, closeErr
	}
	if err := stagingFile.Sync(); err != nil {
		return AllocationStagingPreparedV1{}, err
	}
	if err := store.objects.Sync(); err != nil {
		return AllocationStagingPreparedV1{}, err
	}
	staging, err = store.inspectPreparedStaging(intent.StagingRelativeName, intent.MarkerRelativeName)
	if err != nil || !staging.present || staging.marker != marker {
		if err != nil {
			return AllocationStagingPreparedV1{}, err
		}
		return AllocationStagingPreparedV1{}, ErrFilesystemConflict
	}
	return buildPrepared(intent, intentFactDigest, staging)
}

func (store *Store) syncPreparedStaging(name, markerName string, expected allocationObservation) (allocationObservation, error) {
	directoryFD, err := unix.Openat(int(store.objects.Fd()), name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return allocationObservation{}, ErrFilesystemConflict
	}
	directory := os.NewFile(uintptr(directoryFD), name)
	defer directory.Close()
	markerFD, err := unix.Openat(directoryFD, markerName, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return allocationObservation{}, ErrFilesystemConflict
	}
	marker := os.NewFile(uintptr(markerFD), markerName)
	if err := marker.Sync(); err != nil {
		marker.Close()
		return allocationObservation{}, err
	}
	if err := marker.Close(); err != nil {
		return allocationObservation{}, err
	}
	if err := directory.Sync(); err != nil {
		return allocationObservation{}, err
	}
	if err := store.objects.Sync(); err != nil {
		return allocationObservation{}, err
	}
	current, err := store.inspectPreparedStaging(name, markerName)
	if err != nil {
		return allocationObservation{}, err
	}
	if !matchesObservation(current, expected) {
		return allocationObservation{}, ErrFilesystemConflict
	}
	return current, nil
}

func matchesObservation(left, right allocationObservation) bool {
	return left.present && right.present && sameDirectoryObject(left.objectIdentity, right.objectIdentity) && left.markerIdentity == right.markerIdentity && left.marker == right.marker && left.markerDigest == right.markerDigest
}

func buildPrepared(intent AllocationProvisionIntentV1, intentFactDigest string, observation allocationObservation) (AllocationStagingPreparedV1, error) {
	prepared := AllocationStagingPreparedV1{
		SchemaVersion: PreparedSchema, ProtocolRevision: ProtocolRevision, Binding: intent.Binding,
		IntentFactDigest: intentFactDigest, RequestDigest: intent.RequestDigest,
		StagingRelativeName: intent.StagingRelativeName, LiveRelativeName: intent.LiveRelativeName,
		MarkerRelativeName: intent.MarkerRelativeName, StagingIdentity: observation.objectIdentity,
		MarkerIdentity: observation.markerIdentity, Marker: observation.marker, MarkerDigest: observation.markerDigest,
	}
	if err := prepared.Seal(); err != nil {
		return AllocationStagingPreparedV1{}, err
	}
	if err := prepared.Validate(intent); err != nil {
		return AllocationStagingPreparedV1{}, err
	}
	return prepared, nil
}

func (store *Store) completeProvision(intent AllocationProvisionIntentV1, prepared AllocationStagingPreparedV1, preparedFactDigest string) (AllocationProvisionReceiptV1, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.objects == nil || prepared.Validate(intent) != nil || !store.scope.Matches(intent.Binding) || !validDigest(preparedFactDigest) {
		return AllocationProvisionReceiptV1{}, ErrInvalid
	}
	staging, err := store.inspectPreparedStaging(intent.StagingRelativeName, intent.MarkerRelativeName)
	if err != nil {
		return AllocationProvisionReceiptV1{}, err
	}
	live, err := store.inspectAllocation(intent.LiveRelativeName, intent.MarkerRelativeName)
	if err != nil {
		return AllocationProvisionReceiptV1{}, err
	}
	_, _, tombstoneName, _, _ := DeriveRelativeNames(intent.Binding.AllocationID)
	tombstone, err := store.inspectAllocation(tombstoneName, intent.MarkerRelativeName)
	if err != nil {
		return AllocationProvisionReceiptV1{}, err
	}
	if tombstone.present || staging.present && live.present {
		return AllocationProvisionReceiptV1{}, ErrFilesystemConflict
	}
	if staging.present {
		if !matchesPrepared(staging, prepared) || live.present {
			return AllocationProvisionReceiptV1{}, ErrFilesystemConflict
		}
		if err := unix.RenameatxNp(int(store.objects.Fd()), intent.StagingRelativeName, int(store.objects.Fd()), intent.LiveRelativeName, unix.RENAME_EXCL); err != nil {
			if errors.Is(err, unix.EEXIST) {
				return AllocationProvisionReceiptV1{}, ErrFilesystemConflict
			}
			return AllocationProvisionReceiptV1{}, err
		}
		if err := store.objects.Sync(); err != nil {
			return AllocationProvisionReceiptV1{}, err
		}
	} else if !live.present {
		return AllocationProvisionReceiptV1{}, ErrFilesystemUnknown
	} else if !matchesPrepared(live, prepared) {
		return AllocationProvisionReceiptV1{}, ErrFilesystemConflict
	} else if err := store.objects.Sync(); err != nil {
		return AllocationProvisionReceiptV1{}, err
	}

	staging, err = store.inspectPreparedStaging(intent.StagingRelativeName, intent.MarkerRelativeName)
	if err != nil {
		return AllocationProvisionReceiptV1{}, err
	}
	live, err = store.inspectAllocation(intent.LiveRelativeName, intent.MarkerRelativeName)
	if err != nil {
		return AllocationProvisionReceiptV1{}, err
	}
	if staging.present || !live.present || !matchesPrepared(live, prepared) {
		return AllocationProvisionReceiptV1{}, ErrFilesystemConflict
	}
	receipt := AllocationProvisionReceiptV1{
		SchemaVersion: ProvisionReceiptSchema, ProtocolRevision: ProtocolRevision, Binding: intent.Binding,
		IntentFactDigest: prepared.IntentFactDigest, PreparedFactDigest: preparedFactDigest,
		RequestDigest: intent.RequestDigest, LiveRelativeName: intent.LiveRelativeName, LiveIdentity: prepared.StagingIdentity,
		MarkerRelativeName: intent.MarkerRelativeName, MarkerIdentity: live.markerIdentity,
		Marker: live.marker, MarkerDigest: live.markerDigest, Disposition: DispositionApplied,
	}
	if err := receipt.Seal(); err != nil {
		return AllocationProvisionReceiptV1{}, err
	}
	if err := receipt.Validate(intent, prepared); err != nil {
		return AllocationProvisionReceiptV1{}, err
	}
	return receipt, nil
}

func (store *Store) verifyProvisionReceipt(intent AllocationProvisionIntentV1, prepared AllocationStagingPreparedV1, receipt AllocationProvisionReceiptV1) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if receipt.Validate(intent, prepared) != nil || !store.scope.Matches(intent.Binding) {
		return ErrInvalid
	}
	live, err := store.inspectAllocation(receipt.LiveRelativeName, receipt.MarkerRelativeName)
	if err != nil {
		return err
	}
	if !live.present || !sameDirectoryObject(live.objectIdentity, receipt.LiveIdentity) || live.markerIdentity != receipt.MarkerIdentity || live.marker != receipt.Marker || live.markerDigest != receipt.MarkerDigest {
		return ErrFilesystemConflict
	}
	return nil
}

func (store *Store) completeTerminate(intent AllocationTerminateIntentV1, intentFactDigest string) (AllocationTerminateReceiptV1, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.objects == nil || intent.Validate() != nil || !store.scope.Matches(intent.Binding) || !validDigest(intentFactDigest) {
		return AllocationTerminateReceiptV1{}, ErrInvalid
	}
	live, err := store.inspectAllocation(intent.LiveRelativeName, intent.MarkerRelativeName)
	if err != nil {
		return AllocationTerminateReceiptV1{}, err
	}
	tombstone, err := store.inspectAllocation(intent.TombstoneRelativeName, intent.MarkerRelativeName)
	if err != nil {
		return AllocationTerminateReceiptV1{}, err
	}
	if live.present && tombstone.present {
		return AllocationTerminateReceiptV1{}, ErrFilesystemConflict
	}
	if live.present {
		if !matchesTerminate(live, intent) {
			return AllocationTerminateReceiptV1{}, ErrFilesystemConflict
		}
		if err := unix.RenameatxNp(int(store.objects.Fd()), intent.LiveRelativeName, int(store.objects.Fd()), intent.TombstoneRelativeName, unix.RENAME_EXCL); err != nil {
			if errors.Is(err, unix.EEXIST) {
				return AllocationTerminateReceiptV1{}, ErrFilesystemConflict
			}
			return AllocationTerminateReceiptV1{}, err
		}
		if err := store.objects.Sync(); err != nil {
			return AllocationTerminateReceiptV1{}, err
		}
	} else if !tombstone.present {
		return AllocationTerminateReceiptV1{}, ErrFilesystemUnknown
	} else if !matchesTerminate(tombstone, intent) {
		return AllocationTerminateReceiptV1{}, ErrFilesystemConflict
	} else if err := store.objects.Sync(); err != nil {
		return AllocationTerminateReceiptV1{}, err
	}

	live, err = store.inspectAllocation(intent.LiveRelativeName, intent.MarkerRelativeName)
	if err != nil {
		return AllocationTerminateReceiptV1{}, err
	}
	tombstone, err = store.inspectAllocation(intent.TombstoneRelativeName, intent.MarkerRelativeName)
	if err != nil {
		return AllocationTerminateReceiptV1{}, err
	}
	if live.present || !tombstone.present || !matchesTerminate(tombstone, intent) {
		return AllocationTerminateReceiptV1{}, ErrFilesystemConflict
	}
	receipt := AllocationTerminateReceiptV1{
		SchemaVersion: TerminateReceiptSchema, ProtocolRevision: ProtocolRevision, Binding: intent.Binding,
		TerminalizationID: intent.TerminalizationID, CleanupBindingDigest: intent.CleanupBindingDigest,
		ProcessTerminalFactDigest: intent.ProcessTerminalFactDigest, OrchestratorID: intent.OrchestratorID,
		ExpectedAttemptSequence: intent.ExpectedAttemptSequence, AttemptAuthorityFactDigest: intent.AttemptAuthorityFactDigest,
		IntentFactDigest: intentFactDigest, RequestDigest: intent.RequestDigest,
		LiveRelativeName: intent.LiveRelativeName, TombstoneRelativeName: intent.TombstoneRelativeName,
		TombstoneIdentity: intent.LiveIdentity, MarkerRelativeName: intent.MarkerRelativeName,
		MarkerIdentity: tombstone.markerIdentity, Marker: tombstone.marker, MarkerDigest: tombstone.markerDigest,
		LiveAbsent: true, TombstonePresent: true, Disposition: DispositionApplied,
	}
	if err := receipt.Seal(); err != nil {
		return AllocationTerminateReceiptV1{}, err
	}
	if err := receipt.Validate(intent); err != nil {
		return AllocationTerminateReceiptV1{}, err
	}
	return receipt, nil
}

// prepareTerminateIntent obtains the filesystem observation that is eligible
// to be appended as a terminate intent. It is deliberately read-only: the
// caller must still append the returned value while holding current authority
// before RecoverTerminate may mutate the allocation.
func (store *Store) prepareTerminateIntent(request TerminateRequestV1) (AllocationTerminateIntentV1, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.objects == nil || request.Validate() != nil || !store.scope.Matches(request.Binding) {
		return AllocationTerminateIntentV1{}, ErrInvalid
	}
	stagingName, _, _, markerName, err := DeriveRelativeNames(request.Binding.AllocationID)
	if err != nil {
		return AllocationTerminateIntentV1{}, err
	}
	staging, err := store.inspectPreparedStaging(stagingName, markerName)
	if err != nil {
		return AllocationTerminateIntentV1{}, err
	}
	live, err := store.inspectAllocation(request.LiveRelativeName, markerName)
	if err != nil {
		return AllocationTerminateIntentV1{}, err
	}
	tombstone, err := store.inspectAllocation(request.TombstoneRelativeName, markerName)
	if err != nil {
		return AllocationTerminateIntentV1{}, err
	}
	if staging.present || tombstone.present {
		return AllocationTerminateIntentV1{}, ErrFilesystemConflict
	}
	if !live.present {
		return AllocationTerminateIntentV1{}, ErrFilesystemUnknown
	}
	if !sameAllocationScope(live.marker.Binding, request.Binding) {
		return AllocationTerminateIntentV1{}, ErrFilesystemConflict
	}
	return bindTerminateIntent(request, live.objectIdentity, live.markerIdentity, live.marker, live.markerDigest)
}

func (store *Store) verifyTerminateReceipt(intent AllocationTerminateIntentV1, receipt AllocationTerminateReceiptV1) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if receipt.Validate(intent) != nil || !store.scope.Matches(intent.Binding) {
		return ErrInvalid
	}
	live, err := store.inspectAllocation(intent.LiveRelativeName, intent.MarkerRelativeName)
	if err != nil {
		return err
	}
	tombstone, err := store.inspectAllocation(intent.TombstoneRelativeName, intent.MarkerRelativeName)
	if err != nil {
		return err
	}
	if live.present || !tombstone.present || !sameDirectoryObject(tombstone.objectIdentity, receipt.TombstoneIdentity) || tombstone.markerIdentity != receipt.MarkerIdentity || tombstone.marker != receipt.Marker || tombstone.markerDigest != receipt.MarkerDigest {
		return ErrFilesystemConflict
	}
	return nil
}

func (store *Store) inspectAllocation(name, markerName string) (allocationObservation, error) {
	return store.inspectAllocationState(name, markerName, false)
}

// inspectPreparedStaging additionally requires the marker to be the only
// directory entry. A pre-existing directory without a marker is never adopted:
// ADR 0057's O_EXCL/no-clobber rule makes that state an intervention conflict.
func (store *Store) inspectPreparedStaging(name, markerName string) (allocationObservation, error) {
	return store.inspectAllocationState(name, markerName, true)
}

func (store *Store) inspectAllocationState(name, markerName string, requireOnlyMarker bool) (allocationObservation, error) {
	if requireRelativeName(name) != nil || requireRelativeName(markerName) != nil {
		return allocationObservation{}, ErrInvalid
	}
	var named unix.Stat_t
	if err := unix.Fstatat(int(store.objects.Fd()), name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return allocationObservation{}, nil
		}
		return allocationObservation{}, err
	}
	if named.Mode&unix.S_IFMT != unix.S_IFDIR {
		return allocationObservation{}, ErrFilesystemConflict
	}
	directoryFD, err := unix.Openat(int(store.objects.Fd()), name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return allocationObservation{}, ErrFilesystemConflict
	}
	directory := os.NewFile(uintptr(directoryFD), name)
	defer directory.Close()
	var held unix.Stat_t
	if err := unix.Fstat(directoryFD, &held); err != nil || !sameStat(named, held) || verifyPrivateDirectory(directoryFD, store.uid) != nil {
		return allocationObservation{}, ErrFilesystemConflict
	}
	var parentStat unix.Stat_t
	if err := unix.Fstat(int(store.objects.Fd()), &parentStat); err != nil || held.Dev != parentStat.Dev {
		return allocationObservation{}, ErrFilesystemConflict
	}

	markerFD, err := unix.Openat(directoryFD, markerName, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return allocationObservation{}, ErrFilesystemConflict
	}
	markerFile := os.NewFile(uintptr(markerFD), markerName)
	defer markerFile.Close()
	var markerBefore unix.Stat_t
	if err := unix.Fstat(markerFD, &markerBefore); err != nil || verifyPrivateRegular(markerBefore, store.uid) != nil {
		return allocationObservation{}, ErrFilesystemConflict
	}
	markerBytes, err := io.ReadAll(io.LimitReader(markerFile, markerReadLimit+1))
	if err != nil || len(markerBytes) == 0 || len(markerBytes) > markerReadLimit {
		return allocationObservation{}, ErrFilesystemConflict
	}
	var markerAfter, markerNamed, directoryAfter, directoryNamed unix.Stat_t
	if err := unix.Fstat(markerFD, &markerAfter); err != nil || !sameStat(markerBefore, markerAfter) {
		return allocationObservation{}, ErrFilesystemConflict
	}
	if err := unix.Fstatat(directoryFD, markerName, &markerNamed, unix.AT_SYMLINK_NOFOLLOW); err != nil || !sameStat(markerBefore, markerNamed) {
		return allocationObservation{}, ErrFilesystemConflict
	}
	if err := unix.Fstat(directoryFD, &directoryAfter); err != nil || !sameStat(held, directoryAfter) {
		return allocationObservation{}, ErrFilesystemConflict
	}
	if err := unix.Fstatat(int(store.objects.Fd()), name, &directoryNamed, unix.AT_SYMLINK_NOFOLLOW); err != nil || !sameStat(held, directoryNamed) {
		return allocationObservation{}, ErrFilesystemConflict
	}
	if requireOnlyMarker {
		names, readErr := directory.Readdirnames(2)
		if len(names) != 1 || names[0] != markerName || readErr != nil && !errors.Is(readErr, io.EOF) {
			return allocationObservation{}, ErrFilesystemConflict
		}
		// Readdirnames only promises io.EOF for an empty result. Darwin returns
		// one final name with a nil error, so an explicit second read is required
		// to prove that the marker was the sole directory entry.
		trailing, trailingErr := directory.Readdirnames(1)
		if len(trailing) != 0 || !errors.Is(trailingErr, io.EOF) {
			return allocationObservation{}, ErrFilesystemConflict
		}
	}
	var marker AllocationIdentityMarkerV1
	if err := strictCanonicalDecode(markerBytes, &marker); err != nil || marker.Validate() != nil {
		return allocationObservation{}, ErrFilesystemConflict
	}
	return allocationObservation{
		present: true, objectIdentity: objectIdentity(held), markerIdentity: objectIdentity(markerBefore),
		marker: marker, markerDigest: canonical.DigestBytes(markerBytes),
	}, nil
}

func matchesPrepared(observation allocationObservation, prepared AllocationStagingPreparedV1) bool {
	return observation.present && sameDirectoryObject(observation.objectIdentity, prepared.StagingIdentity) && observation.markerIdentity == prepared.MarkerIdentity && observation.marker == prepared.Marker && observation.markerDigest == prepared.MarkerDigest
}

func matchesTerminate(observation allocationObservation, intent AllocationTerminateIntentV1) bool {
	return observation.present && sameDirectoryObject(observation.objectIdentity, intent.LiveIdentity) && observation.markerIdentity == intent.MarkerIdentity && observation.marker == intent.Marker && observation.markerDigest == intent.MarkerDigest
}

func objectIdentity(stat unix.Stat_t) ObjectIdentityV1 {
	typeName := "unknown"
	switch stat.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
		typeName = ObjectTypeDirectory
	case unix.S_IFREG:
		typeName = ObjectTypeRegular
	}
	return ObjectIdentityV1{
		Device: strconv.FormatUint(uint64(stat.Dev), 10), Inode: strconv.FormatUint(stat.Ino, 10), Mode: uint32(stat.Mode), UID: stat.Uid, GID: stat.Gid,
		Size: stat.Size, Nlink: uint64(stat.Nlink), Type: typeName,
	}
}

func sameStat(left, right unix.Stat_t) bool {
	if left.Dev != right.Dev || left.Ino != right.Ino || left.Mode != right.Mode || left.Uid != right.Uid || left.Gid != right.Gid {
		return false
	}
	// APFS does not expose directory size/link count as stable authority
	// attributes: fsync and entry mutations can change them without replacing
	// the directory object. Device+inode+type/mode+owner remain the binding.
	// Regular files keep exact size/link checks because their content and
	// single-link identity are part of the authority observation.
	if left.Mode&unix.S_IFMT == unix.S_IFDIR {
		return true
	}
	return left.Size == right.Size && left.Nlink == right.Nlink
}

func verifyPrivateDirectory(fd int, uid uint32) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Mode&0o777 != 0o700 || stat.Uid != uid {
		return ErrFilesystemConflict
	}
	return nil
}

func verifyPrivateRegular(stat unix.Stat_t, uid uint32) error {
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0o777 != 0o600 || stat.Uid != uid || stat.Nlink != 1 {
		return ErrFilesystemConflict
	}
	return nil
}

func openOrCreatePrivateDirectory(parent *os.File, name string) (*os.File, error) {
	if parent == nil || requireRelativeName(name) != nil {
		return nil, ErrInvalid
	}
	created := false
	if err := unix.Mkdirat(int(parent.Fd()), name, 0o700); err == nil {
		created = true
	} else if !errors.Is(err, unix.EEXIST) {
		return nil, err
	}
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	var held, named, parentStat unix.Stat_t
	if err := unix.Fstat(fd, &held); err != nil || verifyPrivateDirectory(fd, uint32(unix.Geteuid())) != nil {
		unix.Close(fd)
		return nil, ErrFilesystemConflict
	}
	if err := unix.Fstat(int(parent.Fd()), &parentStat); err != nil || held.Dev != parentStat.Dev {
		unix.Close(fd)
		return nil, ErrFilesystemConflict
	}
	if err := unix.Fstatat(int(parent.Fd()), name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil || !sameStat(held, named) {
		unix.Close(fd)
		return nil, ErrFilesystemConflict
	}
	file := os.NewFile(uintptr(fd), name)
	if created {
		if err := file.Sync(); err != nil {
			file.Close()
			return nil, err
		}
		if err := parent.Sync(); err != nil {
			file.Close()
			return nil, err
		}
	}
	return file, nil
}

func openOrCreateJournal(parent *os.File) (*os.File, error) {
	created := false
	fd, err := unix.Openat(int(parent.Fd()), JournalFileName, unix.O_RDWR|unix.O_APPEND|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_CREAT|unix.O_EXCL, 0o600)
	if err == nil {
		created = true
	} else if errors.Is(err, unix.EEXIST) {
		fd, err = unix.Openat(int(parent.Fd()), JournalFileName, unix.O_RDWR|unix.O_APPEND|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	}
	if err != nil {
		return nil, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || verifyPrivateRegular(stat, uint32(unix.Geteuid())) != nil {
		unix.Close(fd)
		return nil, ErrFilesystemConflict
	}
	var named, parentStat unix.Stat_t
	if err := unix.Fstat(int(parent.Fd()), &parentStat); err != nil || stat.Dev != parentStat.Dev {
		unix.Close(fd)
		return nil, ErrFilesystemConflict
	}
	if err := unix.Fstatat(int(parent.Fd()), JournalFileName, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil || !sameStat(stat, named) {
		unix.Close(fd)
		return nil, ErrFilesystemConflict
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		unix.Close(fd)
		return nil, ErrFilesystemConflict
	}
	file := os.NewFile(uintptr(fd), JournalFileName)
	if created {
		if err := file.Sync(); err != nil {
			file.Close()
			return nil, err
		}
		if err := parent.Sync(); err != nil {
			file.Close()
			return nil, err
		}
	}
	return file, nil
}

func openExistingDirectoryPath(path string) (int, error) {
	clean := filepath.Clean(path)
	if path != clean || clean == "." || clean == string(filepath.Separator) || strings.IndexByte(clean, 0) >= 0 {
		return -1, ErrInvalid
	}
	start := "."
	if filepath.IsAbs(clean) {
		start = string(filepath.Separator)
		clean = strings.TrimPrefix(clean, string(filepath.Separator))
	}
	fd, err := unix.Open(start, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, err
	}
	for _, component := range strings.Split(clean, string(filepath.Separator)) {
		if requireRelativeName(component) != nil {
			unix.Close(fd)
			return -1, ErrInvalid
		}
		next, err := unix.Openat(fd, component, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
		unix.Close(fd)
		if err != nil {
			return -1, err
		}
		fd = next
	}
	return fd, nil
}
