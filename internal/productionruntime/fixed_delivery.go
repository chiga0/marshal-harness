package productionruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/resultingress"
)

const (
	fixedDeliverySchema    = "fixed-delivery-record/v1"
	fixedDeliveryProtocol  = "darwin-fixed-delivery/v1"
	fixedDeliveryPending   = "pending"
	fixedDeliveryStartRun  = "start-run"
	fixedDeliveryMaxRecord = 64 << 10
)

var fixedDeliveryDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

var (
	ErrFixedDeliveryConflict = errors.New("productionruntime: fixed delivery conflict")
	ErrFixedDeliveryUnknown  = errors.New("productionruntime: fixed delivery outcome unknown")
)

// FixedDeliveryPending is a secret-free immutable recovery index. It contains
// only canonical digests and the original frozen deadline.
type FixedDeliveryPending struct {
	SchemaVersion          string `json:"schemaVersion"`
	ProtocolRevision       string `json:"protocolRevision"`
	RecordType             string `json:"recordType"`
	Operation              string `json:"operation"`
	OwnerAcquisitionDigest string `json:"ownerAcquisitionDigest"`
	OwnerFactDigest        string `json:"ownerFactDigest"`
	RepositoryDigest       string `json:"repositoryDigest"`
	NamespaceDigest        string `json:"namespaceDigest"`
	// AuthorityRootDigest is an S1.1 current-session mutation observation. It
	// must not be reused as the stable S1.3 identity across owner lifetimes.
	AuthorityRootDigest     string `json:"authorityRootDigest"`
	RequestKeyDigest        string `json:"requestKeyDigest"`
	RequestDigest           string `json:"requestDigest"`
	ApplicationIntentDigest string `json:"applicationIntentDigest"`
	Deadline                string `json:"deadline"`
	Digest                  string `json:"digest"`
}

// FixedDeliveryStore is minted only by a live RepositorySession. It retains a
// session borrow and does not expose the RunStore, held root, or owner lock.
type FixedDeliveryStore struct {
	mu                  sync.Mutex
	session             *RepositorySession
	borrow              *repositorySessionBorrow
	ownerDigest         string
	namespaceDigest     string
	authorityRootDigest string
	closed              bool
	publishHook         func(fixedDeliveryPublishPhase) error
}

type fixedStartRunIntent struct {
	ProtocolRevision string                      `json:"protocolRevision"`
	Operation        string                      `json:"operation"`
	Request          application.StartRunRequest `json:"request"`
}

// OpenFixedDeliveryStore binds delivery to the complete live Session tuple.
func (session *RepositorySession) OpenFixedDeliveryStore(ctx context.Context) (*FixedDeliveryStore, error) {
	borrow, err := session.borrow()
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*FixedDeliveryStore, error) {
		_ = borrow.Close()
		return nil, err
	}
	if session.runs == nil || session.fixedRoot.deliveryRoot() == nil {
		return fail(ErrFixedDeliveryConflict)
	}
	ownerDigest, err := controlOwnerDigest(session)
	if err != nil {
		return fail(err)
	}
	namespaceRaw, err := json.Marshal(session.acquisition.Scope.AuthorityNamespaceID)
	if err != nil {
		return fail(err)
	}
	namespaceDigest, err := canonical.DigestJSON(namespaceRaw)
	if err != nil {
		return fail(err)
	}
	rootDigest, err := session.fixedRoot.digest()
	if err != nil {
		return fail(err)
	}
	store := &FixedDeliveryStore{session: session, borrow: borrow, ownerDigest: ownerDigest, namespaceDigest: namespaceDigest, authorityRootDigest: rootDigest}
	if err := store.withCurrentOwner(ctx, func() error { return validateFixedServerRoot(session.fixedRoot, 5) }); err != nil {
		return fail(err)
	}
	return store, nil
}

func controlOwnerDigest(session *RepositorySession) (string, error) {
	if session == nil {
		return "", ErrFixedDeliveryConflict
	}
	return resultingress.ControlOwnerAcquisitionDigest(session.acquisition)
}

// BeginStartRun follows the only S1.1 lock order: existing Run lease, current
// owner lock, exact READY recheck, then immutable pending publish.
func (store *FixedDeliveryStore) BeginStartRun(ctx context.Context, idempotencyKey string, request application.StartRunRequest, deadline time.Time) (result FixedDeliveryPending, replay bool, resultErr error) {
	if store == nil {
		return FixedDeliveryPending{}, false, ErrFixedDeliveryConflict
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.closed || store.session == nil || ctx == nil || request.Validate() != nil || strings.TrimSpace(idempotencyKey) != idempotencyKey || idempotencyKey == "" || len(idempotencyKey) > 512 || deadline.IsZero() || deadline.Location() != time.UTC || deadline.Format(time.RFC3339Nano) == "" {
		return FixedDeliveryPending{}, false, ErrFixedDeliveryConflict
	}
	lease, err := store.session.runs.AcquireExisting(request.RunID)
	if err != nil {
		return FixedDeliveryPending{}, false, err
	}
	defer func() {
		if releaseErr := lease.Release(); releaseErr != nil {
			result = FixedDeliveryPending{}
			replay = false
			if resultErr == nil {
				resultErr = errors.Join(ErrFixedDeliveryUnknown, releaseErr)
			} else {
				resultErr = errors.Join(resultErr, releaseErr)
			}
		}
	}()
	keyDigest := canonical.DigestBytes([]byte(idempotencyKey))
	requestDigest, intentDigest, err := fixedStartRunDigests(request)
	if err != nil {
		return FixedDeliveryPending{}, false, err
	}
	err = store.withCurrentOwner(ctx, func() error {
		projection, readErr := store.session.runs.ReadRunStartAuthorityUnderLease(ctx, lease)
		if readErr != nil || projection.Run.State != domain.StateReady || projection.Run.AttemptID != "" || projection.Run.RunID != request.RunID || projection.Run.Sequence != request.ExpectedSequence || projection.Run.AuthorityHead != request.ExpectedAuthorityHead {
			return ErrFixedDeliveryConflict
		}
		leaf := fixedDeliveryPendingLeaf(keyDigest)
		raw, found, readErr := readFixedDeliveryRecord(store.session.fixedRoot, leaf, fixedDeliveryMaxRecord)
		if readErr != nil {
			return readErr
		}
		if found {
			var existing FixedDeliveryPending
			if decodeFixedDeliveryRecord(raw, &existing) != nil || validateFixedDeliveryPending(existing) != nil || existing.RequestKeyDigest != keyDigest || existing.RequestDigest != requestDigest || existing.ApplicationIntentDigest != intentDigest || existing.Deadline != deadline.Format(time.RFC3339Nano) || existing.OwnerAcquisitionDigest != store.ownerDigest || existing.OwnerFactDigest != store.session.ownerState.FactDigest || existing.RepositoryDigest != store.session.acquisition.Scope.RepositoryIdentityDigest || existing.NamespaceDigest != store.namespaceDigest || existing.AuthorityRootDigest != store.authorityRootDigest {
				return ErrFixedDeliveryConflict
			}
			if adoptErr := adoptFixedDeliveryRecord(store.session.fixedRoot, leaf, raw, store.publishHook); adoptErr != nil {
				return adoptErr
			}
			result, replay = existing, true
			return nil
		}
		result = FixedDeliveryPending{SchemaVersion: fixedDeliverySchema, ProtocolRevision: fixedDeliveryProtocol, RecordType: fixedDeliveryPending, Operation: fixedDeliveryStartRun, OwnerAcquisitionDigest: store.ownerDigest, OwnerFactDigest: store.session.ownerState.FactDigest, RepositoryDigest: store.session.acquisition.Scope.RepositoryIdentityDigest, NamespaceDigest: store.namespaceDigest, AuthorityRootDigest: store.authorityRootDigest, RequestKeyDigest: keyDigest, RequestDigest: requestDigest, ApplicationIntentDigest: intentDigest, Deadline: deadline.Format(time.RFC3339Nano)}
		sealed, sealErr := sealFixedDeliveryRecord(&result.Digest, &result)
		if sealErr != nil {
			return sealErr
		}
		return publishFixedDeliveryRecord(store.session.fixedRoot, leaf, sealed, store.publishHook)
	})
	if err != nil {
		return FixedDeliveryPending{}, false, err
	}
	return result, replay, nil
}

func (store *FixedDeliveryStore) withCurrentOwner(ctx context.Context, fn func() error) error {
	if store == nil || store.session == nil || ctx == nil {
		return ErrFixedDeliveryConflict
	}
	return store.session.owner.WithCurrentOwnerLock(ctx, store.session.acquisition, func() error {
		current, found, err := store.session.ingress.OpenOwner(store.session.acquisition.Scope)
		if err != nil || !found || current.Acquisition != store.session.acquisition || current.FactDigest != store.session.ownerState.FactDigest {
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
		current, found, err = store.session.ingress.OpenOwner(store.session.acquisition.Scope)
		if err != nil || !found || current.Acquisition != store.session.acquisition || current.FactDigest != store.session.ownerState.FactDigest {
			return ErrFixedDeliveryConflict
		}
		return nil
	})
}

func (store *FixedDeliveryStore) Close() error {
	if store == nil {
		return nil
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.closed {
		return nil
	}
	store.closed = true
	if store.borrow != nil {
		return store.borrow.Close()
	}
	return nil
}

func fixedStartRunDigests(request application.StartRunRequest) (string, string, error) {
	raw, err := json.Marshal(request)
	if err != nil {
		return "", "", err
	}
	requestDigest, err := canonical.DigestJSON(raw)
	if err != nil {
		return "", "", err
	}
	intentRaw, err := json.Marshal(fixedStartRunIntent{ProtocolRevision: fixedDeliveryProtocol, Operation: fixedDeliveryStartRun, Request: request})
	if err != nil {
		return "", "", err
	}
	intentDigest, err := canonical.DigestJSON(intentRaw)
	return requestDigest, intentDigest, err
}

func sealFixedDeliveryRecord(digest *string, value any) ([]byte, error) {
	if digest == nil || *digest != "" {
		return nil, ErrFixedDeliveryConflict
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	sealedDigest, err := canonical.DigestJSON(raw)
	if err != nil {
		return nil, err
	}
	*digest = sealedDigest
	raw, err = json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return canonical.JSON(raw)
}

func decodeFixedDeliveryRecord(raw []byte, target any) error {
	canonicalRaw, err := canonical.JSON(raw)
	if err != nil || !bytes.Equal(canonicalRaw, raw) {
		return ErrFixedDeliveryConflict
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return ErrFixedDeliveryConflict
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ErrFixedDeliveryConflict
	}
	return nil
}

func validateFixedDeliveryPending(pending FixedDeliveryPending) error {
	for _, digest := range []string{pending.OwnerAcquisitionDigest, pending.OwnerFactDigest, pending.RepositoryDigest, pending.NamespaceDigest, pending.AuthorityRootDigest, pending.RequestKeyDigest, pending.RequestDigest, pending.ApplicationIntentDigest, pending.Digest} {
		if !fixedDeliveryDigestPattern.MatchString(digest) {
			return ErrFixedDeliveryConflict
		}
	}
	if pending.SchemaVersion != fixedDeliverySchema || pending.ProtocolRevision != fixedDeliveryProtocol || pending.RecordType != fixedDeliveryPending || pending.Operation != fixedDeliveryStartRun {
		return ErrFixedDeliveryConflict
	}
	deadline, err := time.Parse(time.RFC3339Nano, pending.Deadline)
	if err != nil || deadline.Location() != time.UTC || deadline.Format(time.RFC3339Nano) != pending.Deadline {
		return ErrFixedDeliveryConflict
	}
	stored := pending.Digest
	pending.Digest = ""
	raw, err := json.Marshal(pending)
	if err != nil {
		return ErrFixedDeliveryConflict
	}
	digest, err := canonical.DigestJSON(raw)
	if err != nil || digest != stored {
		return ErrFixedDeliveryConflict
	}
	return nil
}

func fixedDeliveryPendingLeaf(keyDigest string) string {
	return "p-" + strings.TrimPrefix(keyDigest, "sha256:") + ".json"
}

var _ io.Closer = (*FixedDeliveryStore)(nil)
