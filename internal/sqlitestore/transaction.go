package sqlitestore

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
)

// ReadTx is an owner-checked, consistent SQLite read view. No handle is exposed
// to input adapters or Providers. Composition must discard results on error.
type ReadTx struct {
	ctx          context.Context
	tx           *sql.Tx
	done         bool
	err          error
	count, bytes int
}
type WriteTx struct{ *ReadTx }

func (t *ReadTx) check() error {
	if t == nil || t.done {
		return ErrClosed
	}
	if t.err != nil {
		return t.err
	}
	if err := t.ctx.Err(); err != nil {
		return t.fail(err)
	}
	return nil
}
func (t *ReadTx) fail(err error) error {
	if err != nil && t != nil && t.err == nil {
		t.err = err
	}
	return err
}
func (t *ReadTx) charge(size int) error {
	t.count++
	t.bytes += size
	if t.count > MaxTransactionRecords || t.bytes > MaxTransactionBytes {
		return t.fail(ErrLimit)
	}
	return nil
}
func (t *ReadTx) Head(stream Stream) (Head, error) {
	if err := t.check(); err != nil {
		return Head{}, err
	}
	if !stream.valid() {
		return Head{}, t.fail(ErrInvalid)
	}
	var head Head
	err := t.tx.QueryRowContext(t.ctx, "SELECT sequence,digest FROM heads WHERE journal=? AND stream_id=?", stream.Journal, stream.ID).Scan(&head.Sequence, &head.Digest)
	if errors.Is(err, sql.ErrNoRows) {
		return Head{}, nil
	}
	if err != nil || !head.valid() || head.Sequence == 0 {
		return Head{}, t.fail(ErrUnavailable)
	}
	if err = t.reference(Reference{Stream: stream, Head: head}); err != nil {
		return Head{}, err
	}
	return head, nil
}

func (t *ReadTx) Records(stream Stream, after uint64, limit int) ([]Record, error) {
	if err := t.check(); err != nil {
		return nil, err
	}
	if !stream.valid() || after > uint64(^uint64(0)>>1) || limit < 1 || limit > MaxPage {
		return nil, t.fail(ErrInvalid)
	}
	rows, err := t.tx.QueryContext(t.ctx, "SELECT sequence,digest,data FROM records WHERE journal=? AND stream_id=? AND sequence>? ORDER BY sequence LIMIT ?", stream.Journal, stream.ID, after, limit)
	if err != nil {
		return nil, t.fail(ErrUnavailable)
	}
	defer rows.Close()
	result := []Record{}
	for rows.Next() {
		var record Record
		if rows.Scan(&record.Sequence, &record.Digest, &record.Bytes) != nil || record.Sequence != after+uint64(len(result))+1 || validateRecord(stream, record) != nil {
			return nil, t.fail(ErrUnavailable)
		}
		if err := t.charge(len(record.Bytes)); err != nil {
			return nil, err
		}
		result = append(result, record)
	}
	if rows.Err() != nil {
		return nil, t.fail(ErrUnavailable)
	}
	return result, nil
}

func (t *ReadTx) reference(ref Reference) error {
	if !ref.valid() {
		return t.fail(ErrInvalid)
	}
	var record Record
	err := t.tx.QueryRowContext(t.ctx, "SELECT sequence,digest,data FROM records WHERE journal=? AND stream_id=? AND sequence=?", ref.Stream.Journal, ref.Stream.ID, ref.Head.Sequence).Scan(&record.Sequence, &record.Digest, &record.Bytes)
	if errors.Is(err, sql.ErrNoRows) || err == nil && record.Digest != ref.Head.Digest {
		return t.fail(ErrConflict)
	}
	if err != nil {
		return t.fail(ErrUnavailable)
	}
	if validateRecord(ref.Stream, record) != nil {
		return t.fail(ErrUnavailable)
	}
	return t.charge(len(record.Bytes))
}

// CompareAppend preserves original record bytes and their existing digest
// algorithm. expected compares both sequence and digest; there is no last-write
// wins, automatic retry, reducer, producer admission or lifecycle side effect.
func (t *WriteTx) CompareAppend(stream Stream, expected Head, records []Record) (Head, error) {
	if err := t.check(); err != nil {
		return Head{}, err
	}
	if !stream.valid() || !expected.valid() || len(records) == 0 || len(records) > MaxTransactionRecords {
		return Head{}, t.fail(ErrInvalid)
	}
	head, err := t.Head(stream)
	if err != nil {
		return Head{}, err
	}
	if head != expected {
		return Head{}, t.fail(ErrConflict)
	}
	for _, record := range records {
		if !validSequence(head.Sequence+1) || record.Sequence != head.Sequence+1 {
			return Head{}, t.fail(ErrConflict)
		}
		if err := validateRecord(stream, record); err != nil {
			return Head{}, t.fail(err)
		}
		if err := t.charge(len(record.Bytes)); err != nil {
			return Head{}, err
		}
		if _, err := t.tx.ExecContext(t.ctx, "INSERT INTO records(journal,stream_id,sequence,digest,data) VALUES(?,?,?,?,?)", stream.Journal, stream.ID, record.Sequence, record.Digest, record.Bytes); err != nil {
			return Head{}, t.fail(ErrConflict)
		}
		head = Head{Sequence: record.Sequence, Digest: record.Digest}
	}
	if _, err := t.tx.ExecContext(t.ctx, `INSERT INTO heads(journal,stream_id,sequence,digest) VALUES(?,?,?,?)
 ON CONFLICT(journal,stream_id) DO UPDATE SET sequence=excluded.sequence,digest=excluded.digest`, stream.Journal, stream.ID, head.Sequence, head.Digest); err != nil {
		return Head{}, t.fail(ErrUnavailable)
	}
	return head, nil
}

func (t *ReadTx) Projection(key ProjectionKey) (Projection, bool, error) {
	if err := t.check(); err != nil {
		return Projection{}, false, err
	}
	if !key.valid() {
		return Projection{}, false, t.fail(ErrInvalid)
	}
	value := Projection{Key: key}
	err := t.tx.QueryRowContext(t.ctx, `SELECT revision,source_journal,source_id,source_sequence,source_digest,data
 FROM projections WHERE kind=? AND object_id=?`, key.Kind, key.ID).Scan(&value.Revision, &value.Source.Stream.Journal, &value.Source.Stream.ID, &value.Source.Head.Sequence, &value.Source.Head.Digest, &value.Bytes)
	if errors.Is(err, sql.ErrNoRows) {
		return Projection{}, false, nil
	}
	if err != nil || !validSequence(value.Revision) || !value.Source.valid() || canonicalBytes(value.Bytes) != nil {
		return Projection{}, false, t.fail(ErrUnavailable)
	}
	if err = t.reference(value.Source); err != nil {
		return Projection{}, false, err
	}
	if err = t.charge(len(value.Bytes)); err != nil {
		return Projection{}, false, err
	}
	return value, true, nil
}

func (t *WriteTx) PutProjection(expectedRevision uint64, value Projection) error {
	if err := t.check(); err != nil {
		return err
	}
	if !value.Key.valid() || !validSequence(value.Revision) || value.Revision != expectedRevision+1 {
		return t.fail(ErrInvalid)
	}
	if err := canonicalBytes(value.Bytes); err != nil {
		return t.fail(err)
	}
	if err := t.reference(value.Source); err != nil {
		return err
	}
	current, found, err := t.Projection(value.Key)
	if err != nil {
		return err
	}
	if found && current.Revision != expectedRevision || !found && expectedRevision != 0 {
		return t.fail(ErrConflict)
	}
	if err := t.charge(len(value.Bytes)); err != nil {
		return err
	}
	_, err = t.tx.ExecContext(t.ctx, `INSERT INTO projections(kind,object_id,revision,source_journal,source_id,source_sequence,source_digest,data)
 VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(kind,object_id) DO UPDATE SET revision=excluded.revision,source_journal=excluded.source_journal,
 source_id=excluded.source_id,source_sequence=excluded.source_sequence,source_digest=excluded.source_digest,data=excluded.data`,
		value.Key.Kind, value.Key.ID, value.Revision, value.Source.Stream.Journal, value.Source.Stream.ID, value.Source.Head.Sequence, value.Source.Head.Digest, value.Bytes)
	if err != nil {
		return t.fail(ErrUnavailable)
	}
	return nil
}

// Receipt is intentionally callable before any new-command revision CAS, after
// the enclosing Session has authenticated the caller. Cross-scope keys do not
// alias; exact replay returns the original canonical response bytes.
func (t *ReadTx) Receipt(key ReceiptKey, requestDigest string) (Receipt, bool, error) {
	if err := t.check(); err != nil {
		return Receipt{}, false, err
	}
	if !key.valid() || !validDigest(requestDigest) {
		return Receipt{}, false, t.fail(ErrInvalid)
	}
	value := Receipt{Key: key}
	err := t.tx.QueryRowContext(t.ctx, `SELECT request_digest,source_journal,source_id,source_sequence,source_digest,response
 FROM receipts WHERE scope=? AND operation=? AND key_digest=?`, key.Scope, key.Operation, key.KeyDigest).Scan(&value.RequestDigest, &value.Source.Stream.Journal, &value.Source.Stream.ID, &value.Source.Head.Sequence, &value.Source.Head.Digest, &value.Response)
	if errors.Is(err, sql.ErrNoRows) {
		return Receipt{}, false, nil
	}
	if err != nil || !validDigest(value.RequestDigest) || !value.Source.valid() || canonicalBytes(value.Response) != nil {
		return Receipt{}, false, t.fail(ErrUnavailable)
	}
	if value.RequestDigest != requestDigest {
		return Receipt{}, false, t.fail(ErrConflict)
	}
	if err = t.reference(value.Source); err != nil {
		return Receipt{}, false, err
	}
	if err = t.charge(len(value.Response)); err != nil {
		return Receipt{}, false, err
	}
	return value, true, nil
}

func (t *WriteTx) PutReceipt(value Receipt) error {
	if err := t.check(); err != nil {
		return err
	}
	if !value.Key.valid() || !validDigest(value.RequestDigest) {
		return t.fail(ErrInvalid)
	}
	if err := canonicalBytes(value.Response); err != nil {
		return t.fail(err)
	}
	if err := t.reference(value.Source); err != nil {
		return err
	}
	current, found, err := t.Receipt(value.Key, value.RequestDigest)
	if err != nil {
		return err
	}
	if found {
		if current.Source != value.Source || !bytes.Equal(current.Response, value.Response) {
			return t.fail(ErrConflict)
		}
		return nil
	}
	if err := t.charge(len(value.Response)); err != nil {
		return err
	}
	_, err = t.tx.ExecContext(t.ctx, "INSERT INTO receipts VALUES(?,?,?,?,?,?,?,?,?)", value.Key.Scope, value.Key.Operation, value.Key.KeyDigest, value.RequestDigest,
		value.Source.Stream.Journal, value.Source.Stream.ID, value.Source.Head.Sequence, value.Source.Head.Digest, value.Response)
	if err != nil {
		return t.fail(ErrConflict)
	}
	return nil
}
