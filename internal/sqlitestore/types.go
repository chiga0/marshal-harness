// Package sqlitestore is the internal B2 storage seam, not an Application Port
// or a second business authority. One transaction can span RB1, Run, dispatch
// and provider records, projections, receipts and command outbox. The trusted
// Session must still perform the existing producer/current-ledger checks and
// reducers; this package never authorizes a launch, accepts a result, sends an
// outbox command, or reconstructs execution ownership from a database lease.
//
// Create only initializes an empty, private directory. Open never initializes
// missing or incompatible state. This component does not import old roots,
// enable a production profile, or implement migration/recovery of live Workers.
package sqlitestore

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"time"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
)

var (
	ErrInvalid     = errors.New("sqlite store: invalid input")
	ErrConflict    = errors.New("sqlite store: compare-and-append conflict")
	ErrOwner       = errors.New("sqlite store: owner is stale or expired")
	ErrClosed      = errors.New("sqlite store: closed session")
	ErrUnavailable = errors.New("sqlite store: unavailable or incompatible state")
	ErrBusy        = errors.New("sqlite store: writer already held")
	ErrLimit       = errors.New("sqlite store: bounded operation exceeded")
)

const (
	SchemaVersion         = 1
	MaxRecordBytes        = 1 << 20
	MaxTransactionBytes   = 8 << 20
	MaxTransactionRecords = 128
	MaxPage               = 100
	transactionTimeout    = 5 * time.Second
)

var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

type Journal string

const (
	RB1      Journal = "rb1"
	Run      Journal = "run"
	Dispatch Journal = "dispatch"
	Provider Journal = "provider"
)

// RB1/Dispatch retain their detached digest and embedded sequence. Run retains
// its canonical-event digest and sequence. Provider's existing ledger has no
// embedded sequence/detached digest; its storage head hashes the original bytes.
// The digest algorithm is fixed by Journal, never selected per record.
type Stream struct {
	Journal Journal
	ID      string
}
type Head struct {
	Sequence uint64
	Digest   string
}
type Record struct {
	Sequence uint64
	Digest   string
	Bytes    []byte
}
type Reference struct {
	Stream Stream
	Head   Head
}

type Info struct {
	StoreID    string
	Namespace  authority.AuthorityNamespaceId
	Generation uint64
}

// Owner is a database writer/session fence, NOT a Worker lease or permission to
// reuse a directory. Composition supplies the original acquisition digest and
// must reconcile outstanding execution before enabling new commands.
type Owner struct {
	StoreID        string
	Generation     uint64
	IdentityDigest string
	ExpiresAt      time.Time
}

type ProjectionKind string

const (
	TaskProjection        ProjectionKind = "task"
	RunProjection         ProjectionKind = "run"
	AttemptProjection     ProjectionKind = "attempt"
	BudgetProjection      ProjectionKind = "budget"
	LeaseProjection       ProjectionKind = "lease"
	ProviderProjection    ProjectionKind = "provider"
	InteractionProjection ProjectionKind = "interaction"
	ArtifactProjection    ProjectionKind = "artifact"
)

type ProjectionKey struct {
	Kind ProjectionKind
	ID   string
}

// Bytes are the existing reducer's canonical output, not a caller-writable
// public state. Source must name an original record in this same database.
type Projection struct {
	Key      ProjectionKey
	Revision uint64
	Source   Reference
	Bytes    []byte
}

type ReceiptKey struct {
	Scope     string
	Operation string
	KeyDigest string
}
type Receipt struct {
	Key           ReceiptKey
	RequestDigest string
	Source        Reference
	Response      []byte
}

type CommandKind string

const (
	StartCommand  CommandKind = "start"
	StopCommand   CommandKind = "stop"
	AnswerCommand CommandKind = "answer"
	VerifyCommand CommandKind = "verify"
)

type CommandStatus string

const (
	Pending  CommandStatus = "pending"
	Unknown  CommandStatus = "unknown"
	Observed CommandStatus = "observed"
)

type Command struct {
	ID          string
	TaskID      string
	RunID       string
	Kind        CommandKind
	Payload     []byte
	Source      Reference
	Revision    uint64
	Status      CommandStatus
	Observation *Reference
}

func validID(id string) bool      { return len(id) <= 256 && domain.ValidateID(id) == nil }
func validSequence(n uint64) bool { return n > 0 && n <= math.MaxInt64 }
func validDigest(d string) bool   { return digestPattern.MatchString(d) }
func (s Stream) valid() bool {
	if !validID(s.ID) {
		return false
	}
	switch s.Journal {
	case RB1:
		return s.ID == "rb1"
	case Dispatch:
		return s.ID == "dispatch"
	case Provider:
		return s.ID == "provider"
	case Run:
		return true
	}
	return false
}
func (h Head) valid() bool {
	return h.Sequence == 0 && h.Digest == "" || validSequence(h.Sequence) && validDigest(h.Digest)
}
func (r Reference) valid() bool {
	return r.Stream.valid() && validSequence(r.Head.Sequence) && validDigest(r.Head.Digest)
}
func (k ProjectionKey) valid() bool {
	if !validID(k.ID) {
		return false
	}
	switch k.Kind {
	case TaskProjection, RunProjection, AttemptProjection, BudgetProjection, LeaseProjection, ProviderProjection, InteractionProjection, ArtifactProjection:
		return true
	}
	return false
}
func (k ReceiptKey) valid() bool {
	return validID(k.Scope) && validID(k.Operation) && validDigest(k.KeyDigest)
}
func canonicalBytes(raw []byte) error {
	if len(raw) == 0 || len(raw) > MaxRecordBytes {
		return ErrLimit
	}
	v, err := canonical.JSON(raw)
	if err != nil || !bytes.Equal(v, raw) {
		return ErrInvalid
	}
	return nil
}

func validateRecord(stream Stream, record Record) error {
	if !stream.valid() || !validSequence(record.Sequence) || !validDigest(record.Digest) {
		return ErrInvalid
	}
	if err := canonicalBytes(record.Bytes); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(record.Bytes, &fields) != nil || fields == nil {
		return ErrInvalid
	}
	if stream.Journal != Provider {
		var sequence uint64
		if json.Unmarshal(fields["sequence"], &sequence) != nil || sequence != record.Sequence {
			return ErrInvalid
		}
	}
	if stream.Journal == Run {
		var runID string
		if json.Unmarshal(fields["runId"], &runID) != nil || runID != stream.ID {
			return ErrInvalid
		}
	}
	digest := canonical.DigestBytes(record.Bytes)
	if stream.Journal == RB1 || stream.Journal == Dispatch {
		var embedded string
		if json.Unmarshal(fields["digest"], &embedded) != nil || embedded != record.Digest {
			return ErrInvalid
		}
		fields["digest"] = json.RawMessage(`""`)
		detached, err := json.Marshal(fields)
		if err != nil {
			return ErrInvalid
		}
		digest, err = canonical.DigestJSON(detached)
		if err != nil {
			return ErrInvalid
		}
	}
	if digest != record.Digest {
		return ErrInvalid
	}
	return nil
}
