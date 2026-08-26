package review

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/chiga0/marshal-harness/internal/domain"
	"golang.org/x/sys/unix"
)

// EnsureOutcome completes or verifies the terminal Outcome transaction for
// callers recovering after the authoritative terminal journal event was
// appended. Existing final records are accepted only when their bytes equal
// the event-derived record; differing bytes fail closed and are never
// replaced. The caller must hold the Run lease.
func EnsureOutcome(runDirectory string, outcome OutcomeData) error {
	jsonData, markdown, err := renderOutcome(outcome)
	if err != nil {
		return err
	}
	return ensureOutcomeRecords(runDirectory, map[string][]byte{"outcome.json": jsonData, "outcome.md": []byte(markdown)})
}

// EnsureOutcomeAt completes a terminal Outcome through a caller-supplied,
// already validated canonical run directory descriptor. No pathname is
// reopened between authority validation and the no-replace transaction.
func EnsureOutcomeAt(runDirectory *os.File, outcome OutcomeData) error {
	if runDirectory == nil {
		return errors.New("terminal outcome requires a run authority descriptor")
	}
	jsonData, markdown, err := renderOutcome(outcome)
	if err != nil {
		return err
	}
	return ensureOutcomeRecordsAt(int(runDirectory.Fd()), map[string][]byte{"outcome.json": jsonData, "outcome.md": []byte(markdown)})
}

type PreparedOutcomeRecords struct {
	directory *os.File
	pairs     [][2]string
}

// PrepareOutcomeAt stages terminal records relative to the exact canonical
// authority descriptor. CommitAt must receive the then-current authority and
// proves it is the same directory before exposing final names.
func PrepareOutcomeAt(runDirectory *os.File, outcome OutcomeData) (*PreparedOutcomeRecords, error) {
	if runDirectory == nil {
		return nil, errors.New("terminal outcome requires a run authority descriptor")
	}
	jsonData, markdown, err := renderOutcome(outcome)
	if err != nil {
		return nil, err
	}
	dupFD, err := unix.Dup(int(runDirectory.Fd()))
	if err != nil {
		return nil, err
	}
	unix.CloseOnExec(dupFD)
	prepared := &PreparedOutcomeRecords{directory: os.NewFile(uintptr(dupFD), "prepared-outcome-authority"), pairs: [][2]string{{"outcome.json.pending", "outcome.json"}, {"outcome.md.pending", "outcome.md"}}}
	records := map[string][]byte{"outcome.json.pending": jsonData, "outcome.md.pending": []byte(markdown)}
	for _, pair := range prepared.pairs {
		final, err := readSecureRecordAt(dupFD, pair[1], 2)
		if err != nil {
			prepared.Abort()
			return nil, err
		}
		if final.exists {
			prepared.Abort()
			return nil, fmt.Errorf("terminal outcome already exists: %s", pair[1])
		}
		pending, err := readSecureRecordAt(dupFD, pair[0], 1)
		if err != nil {
			prepared.Abort()
			return nil, err
		}
		if err := writeSecureRecordAt(dupFD, pair[0], records[pair[0]], pending.exists); err != nil {
			prepared.Abort()
			return nil, err
		}
	}
	if err := unix.Fsync(dupFD); err != nil {
		prepared.Abort()
		return nil, err
	}
	return prepared, nil
}

func (r *PreparedOutcomeRecords) CommitAt(runDirectory *os.File) error {
	if r == nil || r.directory == nil || runDirectory == nil {
		return errors.New("terminal outcome commit lacks authority descriptor")
	}
	defer r.close()
	var staged, current unix.Stat_t
	if err := unix.Fstat(int(r.directory.Fd()), &staged); err != nil {
		return err
	}
	if err := unix.Fstat(int(runDirectory.Fd()), &current); err != nil {
		return err
	}
	if staged.Dev != current.Dev || staged.Ino != current.Ino || current.Mode&unix.S_IFMT != unix.S_IFDIR {
		return errors.New("terminal outcome canonical authority changed before commit")
	}
	for _, pair := range r.pairs {
		if err := unix.Linkat(int(runDirectory.Fd()), pair[0], int(runDirectory.Fd()), pair[1], 0); err != nil {
			return err
		}
		if err := unix.Unlinkat(int(runDirectory.Fd()), pair[0], 0); err != nil {
			return err
		}
	}
	return unix.Fsync(int(runDirectory.Fd()))
}

func (r *PreparedOutcomeRecords) Abort() {
	if r == nil || r.directory == nil {
		return
	}
	for _, pair := range r.pairs {
		_ = unix.Unlinkat(int(r.directory.Fd()), pair[0], 0)
	}
	r.close()
}

func (r *PreparedOutcomeRecords) close() {
	if r.directory != nil {
		_ = r.directory.Close()
		r.directory = nil
	}
}

type PreparedRecords struct {
	pendingDecision string
	finalDecision   string
	pendingPacket   string
	finalPacket     string
	pendingOutcome  string
	finalOutcome    string
	pendingMarkdown string
	finalMarkdown   string
	pendingResult   string
	finalResult     string
}

// PrepareOutcome stages an immutable terminal Outcome without rewriting the
// already committed ReviewDecision and ReviewPacket for the review round.
func PrepareOutcome(runDirectory string, outcome OutcomeData) (*PreparedRecords, error) {
	records := &PreparedRecords{}
	jsonData, markdown, err := renderOutcome(outcome)
	if err != nil {
		return nil, err
	}
	records.pendingOutcome, records.finalOutcome = filepath.Join(runDirectory, "outcome.json.pending"), filepath.Join(runDirectory, "outcome.json")
	records.pendingMarkdown, records.finalMarkdown = filepath.Join(runDirectory, "outcome.md.pending"), filepath.Join(runDirectory, "outcome.md")
	for _, pair := range [][2]string{{records.finalOutcome, records.pendingOutcome}, {records.finalMarkdown, records.pendingMarkdown}} {
		if _, err := os.Lstat(pair[0]); err == nil {
			return nil, fmt.Errorf("terminal outcome already exists: %s", pair[0])
		} else if !os.IsNotExist(err) {
			return nil, err
		}
		if err := os.Remove(pair[1]); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("remove orphan outcome %s: %w", pair[1], err)
		}
	}
	if err := atomicWrite(records.pendingOutcome, jsonData, false); err != nil {
		records.Abort()
		return nil, err
	}
	if err := atomicWrite(records.pendingMarkdown, []byte(markdown), false); err != nil {
		records.Abort()
		return nil, err
	}
	return records, nil
}

// PrepareRecords stages the review round decision and packet in one
// transaction and, when outcome is non-nil, additionally stages the terminal
// outcome.json, outcome.md and result.md within the same transaction.
// result.md carries exactly the bytes of the outcome.md rendered in this
// call. Existing final records are never replaced: any present final fails
// the whole prepare before any deletion or write.
func PrepareRecords(runDirectory string, result DecisionResult, outcome *OutcomeData) (*PreparedRecords, error) {
	return prepareRecordsWithWriter(runDirectory, result, outcome, func(path string, data []byte) error {
		return atomicWrite(path, data, false)
	})
}

func prepareRecordsWithWriter(runDirectory string, result DecisionResult, outcome *OutcomeData, writer func(path string, data []byte) error) (*PreparedRecords, error) {
	records := &PreparedRecords{}
	decisionDir := filepath.Join(runDirectory, "decisions")
	packetDir := filepath.Join(runDirectory, "review-packets")
	records.finalDecision = filepath.Join(decisionDir, fmt.Sprintf("decision-%03d.json", result.Decision.ReviewRound))
	records.pendingDecision = records.finalDecision + ".pending"
	records.finalPacket = filepath.Join(packetDir, fmt.Sprintf("packet-%03d.json", result.Decision.ReviewRound))
	records.pendingPacket = records.finalPacket + ".pending"
	type recordPair struct {
		final, pending string
		recordKind     string
	}
	pairs := []recordPair{
		{records.finalDecision, records.pendingDecision, "review round record"},
		{records.finalPacket, records.pendingPacket, "review round record"},
	}
	var outcomeJSON []byte
	var outcomeMarkdown string
	if outcome != nil {
		var err error
		outcomeJSON, outcomeMarkdown, err = renderOutcome(*outcome)
		if err != nil {
			return nil, err
		}
		records.pendingOutcome, records.finalOutcome = filepath.Join(runDirectory, "outcome.json.pending"), filepath.Join(runDirectory, "outcome.json")
		records.pendingMarkdown, records.finalMarkdown = filepath.Join(runDirectory, "outcome.md.pending"), filepath.Join(runDirectory, "outcome.md")
		records.pendingResult, records.finalResult = filepath.Join(runDirectory, "result.md.pending"), filepath.Join(runDirectory, "result.md")
		pairs = append(pairs, recordPair{records.finalOutcome, records.pendingOutcome, "terminal outcome"})
		pairs = append(pairs, recordPair{records.finalMarkdown, records.pendingMarkdown, "terminal outcome"})
		pairs = append(pairs, recordPair{records.finalResult, records.pendingResult, "terminal outcome"})
	}
	for _, pair := range pairs {
		if _, err := os.Lstat(pair.final); err == nil {
			return nil, fmt.Errorf("%s already exists: %s", pair.recordKind, pair.final)
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}
	for _, pair := range pairs {
		// The caller holds the run lease. A pending file with no final record
		// therefore belongs to a crashed earlier invocation and is safe to replace.
		if err := os.Remove(pair.pending); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("remove orphan %s %s: %w", pair.recordKind, pair.pending, err)
		}
	}
	// Persist exactly the bytes DecisionImporter digested. Re-marshaling the
	// struct here could normalize RFC 3339 decidedAt spellings (fractional
	// seconds in particular) and desynchronize the stored record from the
	// journal decisionDigest that rework and publication lineage verify.
	if err := writer(records.pendingDecision, result.DecisionData); err != nil {
		records.Abort()
		return nil, err
	}
	if err := writer(records.pendingPacket, result.PacketData); err != nil {
		records.Abort()
		return nil, err
	}
	if outcome != nil {
		if err := writer(records.pendingOutcome, outcomeJSON); err != nil {
			records.Abort()
			return nil, err
		}
		if err := writer(records.pendingMarkdown, []byte(outcomeMarkdown)); err != nil {
			records.Abort()
			return nil, err
		}
		if err := writer(records.pendingResult, []byte(outcomeMarkdown)); err != nil {
			records.Abort()
			return nil, err
		}
	}
	return records, nil
}

// Commit finalizes every prepared pending/final pair with a no-replace
// hard link: os.Link fails instead of overwriting a final that appeared
// after prepare, and a failed link never masquerades as success. Each pair
// links its own pending to its own final, so outcome.md and result.md keep
// distinct inodes even when their bytes are identical. Terminal Outcome
// callers pair this with EnsureOutcome restart compensation; review-round
// multi-record compensation remains owned by its lifecycle caller.
func (r *PreparedRecords) Commit() error {
	directories := map[string]bool{}
	for _, pair := range [][2]string{{r.pendingDecision, r.finalDecision}, {r.pendingPacket, r.finalPacket}, {r.pendingOutcome, r.finalOutcome}, {r.pendingMarkdown, r.finalMarkdown}, {r.pendingResult, r.finalResult}} {
		if pair[0] == "" {
			continue
		}
		if err := os.Link(pair[0], pair[1]); err != nil {
			return err
		}
		if err := os.Remove(pair[0]); err != nil {
			return err
		}
		directories[filepath.Dir(pair[1])] = true
	}
	for directory := range directories {
		handle, err := os.Open(directory)
		if err != nil {
			return err
		}
		err = handle.Sync()
		_ = handle.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *PreparedRecords) Abort() {
	for _, path := range []string{r.pendingDecision, r.pendingPacket, r.pendingOutcome, r.pendingMarkdown, r.pendingResult} {
		if path != "" {
			_ = os.Remove(path)
		}
	}
}

func TerminalOutcome(taskID, runID string, state domain.State, result DecisionResult, now time.Time) *OutcomeData {
	if !state.Terminal() {
		return nil
	}
	return &OutcomeData{TaskID: taskID, RunID: runID, TerminalState: state, Verdict: result.Decision.Verdict, FinalReviewRound: result.Decision.ReviewRound, FinalReviewDigest: result.DecisionDigest, FinalEvidenceDigest: result.Decision.EvidenceDigest, Summary: result.Decision.Summary, FindingCount: uint(len(result.Decision.BlockingFindings) + len(result.Decision.NonBlockingFindings)), GeneratedAt: now, LocalSelfIdentityBindingDigest: result.Decision.LocalSelfIdentityBindingDigest}
}
