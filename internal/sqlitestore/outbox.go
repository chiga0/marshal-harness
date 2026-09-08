package sqlitestore

import (
	"bytes"
	"database/sql"
	"errors"
)

func validCommand(value Command) bool {
	if !validID(value.ID) || !validID(value.TaskID) || value.RunID != "" && !validID(value.RunID) || !value.Source.valid() {
		return false
	}
	switch value.Kind {
	case StartCommand, StopCommand, AnswerCommand, VerifyCommand:
		return true
	}
	return false
}

func (t *ReadTx) Command(id string) (Command, bool, error) {
	if err := t.check(); err != nil {
		return Command{}, false, err
	}
	if !validID(id) {
		return Command{}, false, t.fail(ErrInvalid)
	}
	value := Command{ID: id}
	var journal, streamID, digest sql.NullString
	var sequence sql.NullInt64
	err := t.tx.QueryRowContext(t.ctx, `SELECT task_id,run_id,kind,payload,source_journal,source_id,source_sequence,source_digest,
 revision,status,observation_journal,observation_id,observation_sequence,observation_digest FROM outbox WHERE command_id=?`, id).Scan(
		&value.TaskID, &value.RunID, &value.Kind, &value.Payload, &value.Source.Stream.Journal, &value.Source.Stream.ID,
		&value.Source.Head.Sequence, &value.Source.Head.Digest, &value.Revision, &value.Status, &journal, &streamID, &sequence, &digest)
	if errors.Is(err, sql.ErrNoRows) {
		return Command{}, false, nil
	}
	if err != nil || !validCommand(value) || !validSequence(value.Revision) || canonicalBytes(value.Payload) != nil {
		return Command{}, false, t.fail(ErrUnavailable)
	}
	if value.Status == Pending {
		if value.Revision != 1 || journal.Valid || streamID.Valid || sequence.Valid || digest.Valid {
			return Command{}, false, t.fail(ErrUnavailable)
		}
	} else if value.Status == Unknown || value.Status == Observed {
		if value.Revision < 2 || !journal.Valid || !streamID.Valid || !sequence.Valid || !digest.Valid || sequence.Int64 <= 0 {
			return Command{}, false, t.fail(ErrUnavailable)
		}
		value.Observation = &Reference{Stream: Stream{Journal: Journal(journal.String), ID: streamID.String}, Head: Head{Sequence: uint64(sequence.Int64), Digest: digest.String}}
		if err = t.reference(*value.Observation); err != nil {
			return Command{}, false, err
		}
	} else {
		return Command{}, false, t.fail(ErrUnavailable)
	}
	if err = t.reference(value.Source); err != nil {
		return Command{}, false, err
	}
	if err = t.charge(len(value.Payload)); err != nil {
		return Command{}, false, err
	}
	return value, true, nil
}

// Commands only reads durable state. In particular, Unknown is not converted
// back to Pending by reopening, listing, or exact receipt replay.
func (t *ReadTx) Commands(afterID string, limit int) ([]Command, error) {
	if err := t.check(); err != nil {
		return nil, err
	}
	if afterID != "" && !validID(afterID) || limit < 1 || limit > MaxPage {
		return nil, t.fail(ErrInvalid)
	}
	rows, err := t.tx.QueryContext(t.ctx, "SELECT command_id FROM outbox WHERE command_id>? ORDER BY command_id LIMIT ?", afterID, limit)
	if err != nil {
		return nil, t.fail(ErrUnavailable)
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if rows.Scan(&id) != nil {
			rows.Close()
			return nil, t.fail(ErrUnavailable)
		}
		ids = append(ids, id)
	}
	if rows.Err() != nil {
		rows.Close()
		return nil, t.fail(ErrUnavailable)
	}
	if rows.Close() != nil {
		return nil, t.fail(ErrUnavailable)
	}
	result := []Command{}
	for _, id := range ids {
		command, found, err := t.Command(id)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, t.fail(ErrUnavailable)
		}
		result = append(result, command)
	}
	return result, nil
}

func (t *WriteTx) Enqueue(value Command) error {
	if err := t.check(); err != nil {
		return err
	}
	if !validCommand(value) || value.Revision != 1 || value.Status != Pending || value.Observation != nil {
		return t.fail(ErrInvalid)
	}
	if err := canonicalBytes(value.Payload); err != nil {
		return t.fail(err)
	}
	if err := t.reference(value.Source); err != nil {
		return err
	}
	current, found, err := t.Command(value.ID)
	if err != nil {
		return err
	}
	if found {
		// Re-enqueue is storage-idempotent, including an already observed/unknown
		// command, but it never resets delivery state or authorizes another send.
		if current.TaskID != value.TaskID || current.RunID != value.RunID || current.Kind != value.Kind || current.Source != value.Source || !bytes.Equal(current.Payload, value.Payload) {
			return t.fail(ErrConflict)
		}
		return nil
	}
	if err := t.charge(len(value.Payload)); err != nil {
		return err
	}
	_, err = t.tx.ExecContext(t.ctx, `INSERT INTO outbox(command_id,task_id,run_id,kind,payload,source_journal,source_id,source_sequence,
 source_digest,revision,status) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, value.ID, value.TaskID, value.RunID, value.Kind, value.Payload,
		value.Source.Stream.Journal, value.Source.Stream.ID, value.Source.Head.Sequence, value.Source.Head.Digest, 1, Pending)
	if err != nil {
		return t.fail(ErrConflict)
	}
	return nil
}

// ObserveCommand stores only a Core-provided, fact-bound disposition. Pending
// may become Unknown or Observed; Unknown may only become Observed. There is no
// reset/retry or claim-and-send API. Producer and current execution checks stay
// in the same enclosing Session transaction, outside this storage primitive.
func (t *WriteTx) ObserveCommand(id string, expectedRevision uint64, status CommandStatus, observation Reference) error {
	if err := t.check(); err != nil {
		return err
	}
	if !validID(id) || !validSequence(expectedRevision+1) || status != Unknown && status != Observed {
		return t.fail(ErrInvalid)
	}
	if err := t.reference(observation); err != nil {
		return err
	}
	current, found, err := t.Command(id)
	if err != nil {
		return err
	}
	if !found || current.Revision != expectedRevision || current.Status == Observed || current.Status == Unknown && status != Observed {
		return t.fail(ErrConflict)
	}
	if err := t.charge(0); err != nil {
		return err
	}
	_, err = t.tx.ExecContext(t.ctx, `UPDATE outbox SET revision=?,status=?,observation_journal=?,observation_id=?,observation_sequence=?,observation_digest=?
 WHERE command_id=? AND revision=?`, expectedRevision+1, status, observation.Stream.Journal, observation.Stream.ID, observation.Head.Sequence, observation.Head.Digest, id, expectedRevision)
	if err != nil {
		return t.fail(ErrUnavailable)
	}
	return nil
}
