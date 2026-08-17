package runstore

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"time"

	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/lifecycle"
)

var (
	ErrConflict      = errors.New("run store conflict")
	ErrLeaseHeld     = errors.New("run lease already held")
	ErrTruncatedTail = errors.New("journal has a truncated final record")
)

type Store struct{ root string }

type Lease struct {
	file  *os.File
	held  bool
	path  string
	runID string
}

type leaseOwnerRecord struct {
	Token            string    `json:"token"`
	PID              int       `json:"pid"`
	ProcessStartedAt time.Time `json:"processStartedAt"`
	AcquiredAt       time.Time `json:"acquiredAt"`
	HeartbeatAt      time.Time `json:"heartbeatAt"`
	Device           uint64    `json:"device"`
	Inode            uint64    `json:"inode"`
}

var processStartedAt = time.Now().UTC()
var eventTypePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*(?:\.[a-z][a-z0-9-]*)+$`)

// notifyCommandEnv names the environment variable holding the command
// invoked to report Run state transitions to external observers (issue #78).
const notifyCommandEnv = "MARSHAL_NOTIFY_CMD"

func New(root string) *Store { return &Store{root: root} }

func (s *Store) runDir(runID string) (string, error) {
	if err := domain.ValidateID(runID); err != nil {
		return "", err
	}
	return filepath.Join(s.root, "runs", runID), nil
}

func (s *Store) Acquire(runID string) (*Lease, error) {
	directory, err := s.runDir(runID)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create run directory: %w", err)
	}
	path := filepath.Join(directory, "lease.lock")
	leaseFile, device, inode, locked, err := acquireLeaseFile(s.root, runID)
	if err != nil {
		return nil, fmt.Errorf("acquire run lease: %w", err)
	}
	if !locked {
		return nil, ErrLeaseHeld
	}
	var tokenBytes [16]byte
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		_ = releaseLeaseFile(leaseFile)
		return nil, fmt.Errorf("generate lease token: %w", err)
	}
	now := time.Now().UTC()
	record, err := json.MarshalIndent(leaseOwnerRecord{
		Token: hex.EncodeToString(tokenBytes[:]), PID: os.Getpid(), ProcessStartedAt: processStartedAt,
		AcquiredAt: now, HeartbeatAt: now, Device: device, Inode: inode,
	}, "", "  ")
	if err != nil {
		_ = releaseLeaseFile(leaseFile)
		return nil, err
	}
	record = append(record, '\n')
	if err := writeLeaseOwner(s.root, runID, record); err != nil {
		_ = releaseLeaseFile(leaseFile)
		return nil, fmt.Errorf("write lease owner: %w", err)
	}
	return &Lease{file: leaseFile, held: true, path: path, runID: runID}, nil
}

// LeaseHeld reports whether the operating system lock for runID is
// currently held by a live owner. It is a read-only observation used by
// supervisor: unlike Acquire it never rewrites lease.lock.owner and never
// creates a missing lock file. A missing, linked or non-regular lock fails
// closed because process ownership then cannot be proven.
func (s *Store) LeaseHeld(runID string) (bool, error) {
	if _, err := s.runDir(runID); err != nil {
		return false, err
	}
	return probeLeaseHeld(s.root, runID)
}

func (l *Lease) Release() error {
	if l == nil || l.file == nil || !l.held {
		return nil
	}
	err := releaseLeaseFile(l.file)
	l.file = nil
	l.held = false
	return err
}

func (s *Store) Append(lease *Lease, event domain.RunEvent, expectedSequence uint64) error {
	if lease == nil || lease.file == nil || !lease.held {
		return errors.New("append requires held run lease")
	}
	if lease.runID != event.RunID {
		return fmt.Errorf("%w: lease belongs to run %s", ErrConflict, lease.runID)
	}
	directory, err := s.runDir(event.RunID)
	if err != nil {
		return err
	}
	events, _, readErr := s.ReadEvents(event.RunID)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	actual := uint64(len(events))
	if actual != expectedSequence || event.Sequence != expectedSequence+1 {
		return fmt.Errorf("%w: expected sequence %d, journal is %d, event is %d", ErrConflict, expectedSequence, actual, event.Sequence)
	}
	for _, existing := range events {
		if existing.EventID == event.EventID {
			return fmt.Errorf("%w: duplicate event ID %s", ErrConflict, event.EventID)
		}
	}
	currentState := domain.StateCreated
	if len(events) > 0 {
		currentState = events[len(events)-1].StateTo
	}
	if err := lifecycle.ValidateTransition(currentState, event.RunID, expectedSequence, event); err != nil {
		return err
	}
	if event.EventID == "" || event.Payload == nil || event.Kind != domain.KindRunEvent || event.APIVersion != domain.APIVersionV1Alpha1 {
		return fmt.Errorf("%w: incomplete run event", ErrConflict)
	}
	if err := domain.ValidateID(event.EventID); err != nil {
		return fmt.Errorf("%w: %v", ErrConflict, err)
	}
	if !eventTypePattern.MatchString(event.Type) {
		return fmt.Errorf("%w: invalid event type %q", ErrConflict, event.Type)
	}
	if event.AttemptID != "" {
		if err := domain.ValidateID(event.AttemptID); err != nil {
			return fmt.Errorf("%w: %v", ErrConflict, err)
		}
	}
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode event: %w", err)
	}
	file, err := os.OpenFile(filepath.Join(directory, "events.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open event journal: %w", err)
	}
	defer file.Close()
	if _, err := file.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("append event: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync event journal: %w", err)
	}
	notifyStateTransition(len(events) == 0, []domain.RunEvent{event})
	return nil
}

// notifyStateTransition is a pure observation side-channel off Append: after
// the journal write succeeds it reports the last state transition among
// appended events by starting the command named by MARSHAL_NOTIFY_CMD with a
// JSON payload as its single argument. The call is fire-and-forget by
// design: the process is started without waiting for it or reading its
// output, and marshal or start failures are silently dropped (no retry,
// queue or persistence), so Append's validation order, error semantics,
// journaled content, lease semantics and return value are never affected.
// When MARSHAL_NOTIFY_CMD is unset or empty there are no side effects at
// all: no payload is built and no process is started. firstEvent reports
// whether appended[0] is the first journal event of the run; the first event
// of a run counts as a transition even when the state does not change. When
// several events are appended in one call, only the last state transition
// is reported (deterministic). The taskId field is taken from the
// triggering event's payload when present.
func notifyStateTransition(firstEvent bool, appended []domain.RunEvent) {
	notifyCommand := os.Getenv(notifyCommandEnv)
	if notifyCommand == "" {
		return
	}
	var trigger *domain.RunEvent
	for index := range appended {
		candidate := &appended[index]
		if candidate.StateTo != candidate.StateFrom || (firstEvent && index == 0) {
			trigger = candidate
		}
	}
	if trigger == nil {
		return
	}
	taskID, _ := trigger.Payload["taskId"].(string)
	payload, err := json.Marshal(map[string]any{
		"runId":         trigger.RunID,
		"taskId":        taskID,
		"stateFrom":     trigger.StateFrom,
		"stateTo":       trigger.StateTo,
		"eventSequence": trigger.Sequence,
		"timestamp":     trigger.Timestamp,
	})
	if err != nil {
		return
	}
	_ = exec.Command(notifyCommand, string(payload)).Start()
}

func (s *Store) ReadEvents(runID string) ([]domain.RunEvent, bool, error) {
	directory, err := s.runDir(runID)
	if err != nil {
		return nil, false, err
	}
	data, err := os.ReadFile(filepath.Join(directory, "events.jsonl"))
	if err != nil {
		return nil, false, err
	}
	truncated := len(data) > 0 && data[len(data)-1] != '\n'
	reader := bufio.NewReader(bytes.NewReader(data))
	var events []domain.RunEvent
	seen := map[string]bool{}
	for {
		line, readErr := reader.ReadBytes('\n')
		complete := len(line) > 0 && line[len(line)-1] == '\n'
		if len(bytes.TrimSpace(line)) > 0 && complete {
			var event domain.RunEvent
			if err := json.Unmarshal(bytes.TrimSpace(line), &event); err != nil {
				return nil, truncated, fmt.Errorf("decode journal record %d: %w", len(events)+1, err)
			}
			if event.Sequence != uint64(len(events)+1) || seen[event.EventID] {
				return nil, truncated, fmt.Errorf("%w: invalid sequence or duplicate event at record %d", ErrConflict, len(events)+1)
			}
			seen[event.EventID] = true
			events = append(events, event)
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, truncated, readErr
		}
	}
	if truncated {
		return events, true, ErrTruncatedTail
	}
	return events, false, nil
}

func (s *Store) WriteSnapshot(lease *Lease, state domain.RunState) error {
	if lease == nil || lease.file == nil || !lease.held {
		return errors.New("snapshot write requires held run lease")
	}
	if lease.runID != state.RunID {
		return fmt.Errorf("%w: lease belongs to run %s", ErrConflict, lease.runID)
	}
	if state.Kind != domain.KindRunState || state.APIVersion != domain.APIVersionV1Alpha1 {
		return fmt.Errorf("%w: incomplete run state", ErrConflict)
	}
	if err := domain.ValidateID(state.TaskID); err != nil {
		return err
	}
	directory, err := s.runDir(state.RunID)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".state-*.tmp")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(append(data, '\n')); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, filepath.Join(directory, "state.json")); err != nil {
		return err
	}
	dir, err := os.Open(directory)
	if err == nil {
		err = dir.Sync()
		_ = dir.Close()
	}
	return err
}

func (s *Store) ReadSnapshot(runID string) (domain.RunState, error) {
	directory, err := s.runDir(runID)
	if err != nil {
		return domain.RunState{}, err
	}
	data, err := os.ReadFile(filepath.Join(directory, "state.json"))
	if err != nil {
		return domain.RunState{}, err
	}
	var state domain.RunState
	if err := json.Unmarshal(data, &state); err != nil {
		return state, err
	}
	return state, nil
}

func (s *Store) Inspect(runID string) (domain.RunState, error) {
	state, err := s.ReadSnapshot(runID)
	if err != nil {
		return state, err
	}
	events, _, journalErr := s.ReadEvents(runID)
	if errors.Is(journalErr, os.ErrNotExist) && state.Sequence == 0 {
		return state, nil
	}
	if journalErr != nil && !errors.Is(journalErr, ErrTruncatedTail) {
		return state, journalErr
	}
	if uint64(len(events)) < state.Sequence {
		return state, fmt.Errorf("%w: snapshot sequence %d is ahead of journal sequence %d", ErrConflict, state.Sequence, len(events))
	}
	for index := state.Sequence; index < uint64(len(events)); index++ {
		state, err = lifecycle.Replay(state, events[index])
		if err != nil {
			return state, fmt.Errorf("%w: replay journal tail: %v", ErrConflict, err)
		}
	}
	if len(events) > 0 && events[len(events)-1].StateTo != state.State {
		return state, fmt.Errorf("%w: snapshot state %s differs from journal state %s", ErrConflict, state.State, events[len(events)-1].StateTo)
	}
	return state, nil
}

func (s *Store) Rebuild(initial domain.RunState) (domain.RunState, error) {
	events, _, err := s.ReadEvents(initial.RunID)
	if err != nil && !errors.Is(err, ErrTruncatedTail) {
		return initial, err
	}
	state := initial
	for _, event := range events {
		state, err = lifecycle.Replay(state, event)
		if err != nil {
			return initial, err
		}
	}
	return state, nil
}
