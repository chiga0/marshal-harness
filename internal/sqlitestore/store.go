package sqlitestore

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"path/filepath"
	"sync"
	"time"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
	_ "modernc.org/sqlite"
)

const databaseName = "authority.sqlite"
const applicationID = 1297305932

const schema = `
CREATE TABLE metadata (singleton INTEGER PRIMARY KEY CHECK(singleton=1), schema_version INTEGER NOT NULL,
 store_id TEXT NOT NULL, namespace BLOB NOT NULL, generation INTEGER NOT NULL CHECK(generation>=0),
 owner_identity TEXT NOT NULL, owner_expires INTEGER NOT NULL);
CREATE TABLE owner_history (id INTEGER PRIMARY KEY, generation INTEGER NOT NULL, identity_digest TEXT NOT NULL,
 expires INTEGER NOT NULL, kind TEXT NOT NULL CHECK(kind IN ('claim','renew')));
CREATE TABLE records (journal TEXT NOT NULL, stream_id TEXT NOT NULL, sequence INTEGER NOT NULL CHECK(sequence>0),
 digest TEXT NOT NULL, data BLOB NOT NULL, PRIMARY KEY(journal,stream_id,sequence),
 UNIQUE(journal,stream_id,sequence,digest));
CREATE TRIGGER records_no_update BEFORE UPDATE ON records BEGIN SELECT RAISE(ABORT,'immutable'); END;
CREATE TRIGGER records_no_delete BEFORE DELETE ON records BEGIN SELECT RAISE(ABORT,'immutable'); END;
CREATE TABLE heads (journal TEXT NOT NULL, stream_id TEXT NOT NULL, sequence INTEGER NOT NULL, digest TEXT NOT NULL,
 PRIMARY KEY(journal,stream_id), FOREIGN KEY(journal,stream_id,sequence,digest) REFERENCES records(journal,stream_id,sequence,digest));
CREATE TABLE projections (kind TEXT NOT NULL, object_id TEXT NOT NULL, revision INTEGER NOT NULL CHECK(revision>0),
 source_journal TEXT NOT NULL, source_id TEXT NOT NULL, source_sequence INTEGER NOT NULL, source_digest TEXT NOT NULL,
 data BLOB NOT NULL, PRIMARY KEY(kind,object_id),
 FOREIGN KEY(source_journal,source_id,source_sequence,source_digest) REFERENCES records(journal,stream_id,sequence,digest));
CREATE TABLE receipts (scope TEXT NOT NULL, operation TEXT NOT NULL, key_digest TEXT NOT NULL, request_digest TEXT NOT NULL,
 source_journal TEXT NOT NULL, source_id TEXT NOT NULL, source_sequence INTEGER NOT NULL, source_digest TEXT NOT NULL,
 response BLOB NOT NULL, PRIMARY KEY(scope,operation,key_digest),
 FOREIGN KEY(source_journal,source_id,source_sequence,source_digest) REFERENCES records(journal,stream_id,sequence,digest));
CREATE TRIGGER receipts_no_update BEFORE UPDATE ON receipts BEGIN SELECT RAISE(ABORT,'immutable'); END;
CREATE TRIGGER receipts_no_delete BEFORE DELETE ON receipts BEGIN SELECT RAISE(ABORT,'immutable'); END;
CREATE TABLE outbox (command_id TEXT PRIMARY KEY, task_id TEXT NOT NULL, run_id TEXT NOT NULL, kind TEXT NOT NULL,
 payload BLOB NOT NULL, source_journal TEXT NOT NULL, source_id TEXT NOT NULL, source_sequence INTEGER NOT NULL,
 source_digest TEXT NOT NULL, revision INTEGER NOT NULL CHECK(revision>0),
 status TEXT NOT NULL CHECK(status IN ('pending','unknown','observed')),
 observation_journal TEXT, observation_id TEXT, observation_sequence INTEGER, observation_digest TEXT,
 FOREIGN KEY(source_journal,source_id,source_sequence,source_digest) REFERENCES records(journal,stream_id,sequence,digest),
 FOREIGN KEY(observation_journal,observation_id,observation_sequence,observation_digest) REFERENCES records(journal,stream_id,sequence,digest));
PRAGMA user_version=1;
PRAGMA application_id=1297305932;
`

type Store struct {
	life        sync.RWMutex
	ownerChange sync.RWMutex
	closed      bool
	writer      *sql.DB
	reader      *sql.DB
	files       *privateFiles
	info        Info
	clock       func() time.Time
	// Guarded by ownerChange: an Open never inherits the previous writer's
	// usable owner tuple, even when its persisted lease has not yet expired.
	activeGeneration uint64
}

// Create never accepts a populated directory, including one left by an
// interrupted initialization. Bootstrap recovery belongs to the composition.
func Create(ctx context.Context, directory string, namespace authority.AuthorityNamespaceId) (*Store, error) {
	return open(ctx, directory, namespace, true, (*privateFiles).sync)
}

// Open requires the existing schema and namespace. No create mode or schema
// upgrade is passed to SQLite; missing/corrupt/partial state remains untouched.
func Open(ctx context.Context, directory string, namespace authority.AuthorityNamespaceId) (*Store, error) {
	return open(ctx, directory, namespace, false, (*privateFiles).sync)
}

func open(ctx context.Context, directory string, namespace authority.AuthorityNamespaceId, create bool, syncFiles func(*privateFiles) error) (_ *Store, resultErr error) {
	if ctx == nil || namespace.Validate() != nil || syncFiles == nil {
		return nil, ErrInvalid
	}
	namespaceJSON, err := json.Marshal(namespace)
	if err != nil {
		return nil, ErrInvalid
	}
	namespaceJSON, err = canonical.JSON(namespaceJSON)
	if err != nil || len(namespaceJSON) > 4096 {
		return nil, ErrInvalid
	}
	files, err := openPrivateFiles(directory, create)
	if err != nil {
		return nil, err
	}
	s := &Store{files: files, clock: time.Now}
	defer func() {
		if resultErr != nil {
			_ = s.Close()
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, transactionTimeout)
	defer cancel()
	uri := url.URL{Scheme: "file", Path: filepath.Join(directory, databaseName)}
	params := url.Values{"mode": {"rw"}, "_txlock": {"immediate"},
		"_pragma": {"foreign_keys(ON)", "busy_timeout(250)", "synchronous(FULL)"}}
	uri.RawQuery = params.Encode()
	s.writer, err = sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, ErrUnavailable
	}
	s.writer.SetMaxOpenConns(1)
	s.writer.SetMaxIdleConns(1)
	if err = s.writer.PingContext(ctx); err != nil {
		return nil, ErrUnavailable
	}
	if err = files.check(); err != nil {
		return nil, err
	}
	if create {
		var mode string
		if err = s.writer.QueryRowContext(ctx, "PRAGMA journal_mode=WAL").Scan(&mode); err != nil || mode != "wal" {
			return nil, ErrUnavailable
		}
		tx, err := s.writer.BeginTx(ctx, nil)
		if err != nil {
			return nil, ErrUnavailable
		}
		defer tx.Rollback()
		if _, err = tx.ExecContext(ctx, schema); err != nil {
			return nil, ErrUnavailable
		}
		var id [16]byte
		if _, err = rand.Read(id[:]); err != nil {
			return nil, ErrUnavailable
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO metadata VALUES(1,?,?,?,?,?,?)", SchemaVersion, "store-"+hex.EncodeToString(id[:]), namespaceJSON, 0, "", 0); err != nil {
			return nil, ErrUnavailable
		}
		if err = tx.Commit(); err != nil {
			return nil, ErrUnavailable
		}
	}
	var version, appID, metadataVersion int
	var namespaceBytes []byte
	var mode, integrity string
	if s.writer.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version) != nil || version != SchemaVersion ||
		s.writer.QueryRowContext(ctx, "PRAGMA application_id").Scan(&appID) != nil || appID != applicationID ||
		s.writer.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode) != nil || mode != "wal" ||
		s.writer.QueryRowContext(ctx, "PRAGMA quick_check(1)").Scan(&integrity) != nil || integrity != "ok" ||
		s.writer.QueryRowContext(ctx, "SELECT schema_version,store_id,namespace,generation FROM metadata WHERE singleton=1").Scan(&metadataVersion, &s.info.StoreID, &namespaceBytes, &s.info.Generation) != nil ||
		metadataVersion != SchemaVersion || !validID(s.info.StoreID) || !bytes.Equal(namespaceBytes, namespaceJSON) {
		return nil, ErrUnavailable
	}
	s.info.Namespace = namespace
	// Check every required table before an owner may be claimed. No lazy repair.
	for _, table := range []string{"owner_history", "records", "heads", "projections", "receipts", "outbox"} {
		rows, err := s.writer.QueryContext(ctx, "SELECT * FROM "+table+" LIMIT 0")
		if err != nil {
			return nil, ErrUnavailable
		}
		if rows.Close() != nil {
			return nil, ErrUnavailable
		}
	}
	foreignKeys, err := s.writer.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return nil, ErrUnavailable
	}
	invalidReferences := foreignKeys.Next() || foreignKeys.Err() != nil
	if foreignKeys.Close() != nil || invalidReferences {
		return nil, ErrUnavailable
	}
	params.Set("_txlock", "deferred")
	params["_pragma"] = []string{"foreign_keys(ON)", "busy_timeout(250)", "query_only(ON)"}
	uri.RawQuery = params.Encode()
	s.reader, err = sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, ErrUnavailable
	}
	s.reader.SetMaxOpenConns(4)
	s.reader.SetMaxIdleConns(4)
	if s.reader.PingContext(ctx) != nil || files.check() != nil || syncFiles(files) != nil {
		return nil, ErrUnavailable
	}
	return s, nil
}

func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.life.Lock()
	defer s.life.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	var err error
	if s.reader != nil {
		err = errors.Join(err, s.reader.Close())
	}
	if s.writer != nil {
		err = errors.Join(err, s.writer.Close())
	}
	if s.files != nil {
		err = errors.Join(err, s.files.close())
	}
	if err != nil {
		return ErrUnavailable
	}
	return nil
}

func (s *Store) enter() error {
	if s == nil {
		return ErrClosed
	}
	s.life.RLock()
	if s.closed {
		s.life.RUnlock()
		return ErrClosed
	}
	if err := s.files.check(); err != nil {
		s.life.RUnlock()
		return err
	}
	return nil
}

// Info exposes only bootstrap identity, never business state without an owner.
func (s *Store) Info(ctx context.Context) (Info, error) {
	if ctx == nil {
		return Info{}, ErrInvalid
	}
	if err := s.enter(); err != nil {
		return Info{}, err
	}
	defer s.life.RUnlock()
	ctx, cancel := context.WithTimeout(ctx, transactionTimeout)
	defer cancel()
	info := s.info
	if err := s.reader.QueryRowContext(ctx, "SELECT generation FROM metadata WHERE singleton=1").Scan(&info.Generation); err != nil {
		return Info{}, ErrUnavailable
	}
	return info, nil
}

// ClaimOwner is an explicit new database-writer epoch under the held physical
// directory lock. It does not release/rebind an Attempt or replay an outbox.
func (s *Store) ClaimOwner(ctx context.Context, expectedGeneration uint64, identityDigest string, expires time.Time) (Owner, error) {
	return s.changeOwner(ctx, Owner{Generation: expectedGeneration}, identityDigest, expires, false)
}

func (s *Store) RenewOwner(ctx context.Context, current Owner, expires time.Time) (Owner, error) {
	return s.changeOwner(ctx, current, current.IdentityDigest, expires, true)
}

func (s *Store) changeOwner(ctx context.Context, previous Owner, identityDigest string, expires time.Time, renew bool) (Owner, error) {
	if ctx == nil || !validDigest(identityDigest) || previous.Generation >= uint64(^uint64(0)>>1) {
		return Owner{}, ErrInvalid
	}
	if err := s.enter(); err != nil {
		return Owner{}, err
	}
	defer s.life.RUnlock()
	s.ownerChange.Lock()
	defer s.ownerChange.Unlock()
	if !expires.After(s.clock()) || expires.Year() > 2200 {
		return Owner{}, ErrOwner
	}
	ctx, cancel := context.WithTimeout(ctx, transactionTimeout)
	defer cancel()
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return Owner{}, ErrUnavailable
	}
	defer tx.Rollback()
	var generation uint64
	if tx.QueryRowContext(ctx, "SELECT generation FROM metadata WHERE singleton=1").Scan(&generation) != nil {
		return Owner{}, ErrUnavailable
	}
	if generation != previous.Generation {
		return Owner{}, ErrOwner
	}
	kind := "claim"
	if renew {
		if err = s.checkOwner(ctx, tx, previous); err != nil {
			return Owner{}, err
		}
		if !expires.After(previous.ExpiresAt) {
			return Owner{}, ErrInvalid
		}
		kind = "renew"
	} else {
		generation++
	}
	owner := Owner{StoreID: s.info.StoreID, Generation: generation, IdentityDigest: identityDigest, ExpiresAt: expires.UTC()}
	if _, err = tx.ExecContext(ctx, "UPDATE metadata SET generation=?,owner_identity=?,owner_expires=? WHERE singleton=1", generation, identityDigest, expires.UnixNano()); err != nil {
		return Owner{}, ErrUnavailable
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO owner_history(generation,identity_digest,expires,kind) VALUES(?,?,?,?)", generation, identityDigest, expires.UnixNano(), kind); err != nil {
		return Owner{}, ErrUnavailable
	}
	if s.files.check() != nil || ctx.Err() != nil || !expires.After(s.clock()) {
		return Owner{}, ErrUnavailable
	}
	if tx.Commit() != nil {
		return Owner{}, ErrUnavailable
	}
	s.activeGeneration = generation
	return owner, nil
}

func (s *Store) checkOwner(ctx context.Context, tx *sql.Tx, owner Owner) error {
	if owner.StoreID != s.info.StoreID || !validSequence(owner.Generation) || owner.Generation != s.activeGeneration || !validDigest(owner.IdentityDigest) || !owner.ExpiresAt.After(s.clock()) {
		return ErrOwner
	}
	var generation uint64
	var identity string
	var expires int64
	if tx.QueryRowContext(ctx, "SELECT generation,owner_identity,owner_expires FROM metadata WHERE singleton=1").Scan(&generation, &identity, &expires) != nil {
		return ErrUnavailable
	}
	if generation != owner.Generation || identity != owner.IdentityDigest || expires != owner.ExpiresAt.UnixNano() || expires <= s.clock().UnixNano() {
		return ErrOwner
	}
	return nil
}

// View/Update callbacks are synchronous, short, storage-only operations. They
// must not run execution/network work or recursively open another transaction.
// Retaining a transaction after the callback fails closed. Every method error
// poisons Update, even if the callback accidentally discards that error.
func (s *Store) View(ctx context.Context, owner Owner, fn func(*ReadTx) error) error {
	if fn == nil {
		return ErrInvalid
	}
	return s.transaction(ctx, owner, false, func(tx *ReadTx) error { return fn(tx) })
}
func (s *Store) Update(ctx context.Context, owner Owner, fn func(*WriteTx) error) error {
	if fn == nil {
		return ErrInvalid
	}
	return s.transaction(ctx, owner, true, func(tx *ReadTx) error { return fn(&WriteTx{ReadTx: tx}) })
}

func (s *Store) transaction(ctx context.Context, owner Owner, write bool, fn func(*ReadTx) error) error {
	if ctx == nil {
		return ErrInvalid
	}
	if err := s.enter(); err != nil {
		return err
	}
	defer s.life.RUnlock()
	s.ownerChange.RLock()
	defer s.ownerChange.RUnlock()
	ctx, cancel := context.WithTimeout(ctx, transactionTimeout)
	defer cancel()
	db := s.reader
	if write {
		db = s.writer
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: !write})
	if err != nil {
		return ErrUnavailable
	}
	defer tx.Rollback()
	read := &ReadTx{tx: tx, ctx: ctx}
	defer func() { read.done = true }()
	if err = s.checkOwner(ctx, tx, owner); err != nil {
		return err
	}
	if err = fn(read); err != nil {
		return err
	}
	if read.err != nil {
		return read.err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err = s.files.check(); err != nil {
		return err
	}
	if err = s.checkOwner(ctx, tx, owner); err != nil {
		return err
	}
	if tx.Commit() != nil {
		return ErrUnavailable
	}
	return nil
}
