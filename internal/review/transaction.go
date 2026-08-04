package review

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/chiga0/marshal-harness/internal/domain"
)

type PreparedRecords struct {
	pendingDecision string
	finalDecision   string
	pendingPacket   string
	finalPacket     string
	pendingOutcome  string
	finalOutcome    string
	pendingMarkdown string
	finalMarkdown   string
}

func PrepareRecords(runDirectory string, result DecisionResult, outcome *OutcomeData) (*PreparedRecords, error) {
	records := &PreparedRecords{}
	decisionDir := filepath.Join(runDirectory, "decisions")
	packetDir := filepath.Join(runDirectory, "review-packets")
	records.finalDecision = filepath.Join(decisionDir, fmt.Sprintf("decision-%03d.json", result.Decision.ReviewRound))
	records.pendingDecision = records.finalDecision + ".pending"
	records.finalPacket = filepath.Join(packetDir, fmt.Sprintf("packet-%03d.json", result.Decision.ReviewRound))
	records.pendingPacket = records.finalPacket + ".pending"
	for _, pair := range [][2]string{{records.finalDecision, records.pendingDecision}, {records.finalPacket, records.pendingPacket}} {
		if _, err := os.Lstat(pair[0]); err == nil {
			return nil, fmt.Errorf("review round record already exists: %s", pair[0])
		} else if !os.IsNotExist(err) {
			return nil, err
		}
		// The caller holds the run lease. A pending file with no final record
		// therefore belongs to a crashed earlier invocation and is safe to replace.
		if err := os.Remove(pair[1]); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("remove orphan review record %s: %w", pair[1], err)
		}
	}
	decisionData, err := json.MarshalIndent(result.Decision, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := atomicWrite(records.pendingDecision, append(decisionData, '\n'), false); err != nil {
		return nil, err
	}
	if err := atomicWrite(records.pendingPacket, result.PacketData, false); err != nil {
		records.Abort()
		return nil, err
	}
	if outcome != nil {
		jsonData, markdown, err := renderOutcome(*outcome)
		if err != nil {
			records.Abort()
			return nil, err
		}
		records.pendingOutcome, records.finalOutcome = filepath.Join(runDirectory, "outcome.json.pending"), filepath.Join(runDirectory, "outcome.json")
		records.pendingMarkdown, records.finalMarkdown = filepath.Join(runDirectory, "outcome.md.pending"), filepath.Join(runDirectory, "outcome.md")
		for _, pair := range [][2]string{{records.finalOutcome, records.pendingOutcome}, {records.finalMarkdown, records.pendingMarkdown}} {
			if _, err := os.Lstat(pair[0]); err == nil {
				records.Abort()
				return nil, fmt.Errorf("terminal outcome already exists: %s", pair[0])
			} else if !os.IsNotExist(err) {
				records.Abort()
				return nil, err
			}
			if err := os.Remove(pair[1]); err != nil && !os.IsNotExist(err) {
				records.Abort()
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
	}
	return records, nil
}

func (r *PreparedRecords) Commit() error {
	directories := map[string]bool{}
	for _, pair := range [][2]string{{r.pendingDecision, r.finalDecision}, {r.pendingPacket, r.finalPacket}, {r.pendingOutcome, r.finalOutcome}, {r.pendingMarkdown, r.finalMarkdown}} {
		if pair[0] == "" {
			continue
		}
		if err := os.Rename(pair[0], pair[1]); err != nil {
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
	for _, path := range []string{r.pendingDecision, r.pendingPacket, r.pendingOutcome, r.pendingMarkdown} {
		if path != "" {
			_ = os.Remove(path)
		}
	}
}

func TerminalOutcome(taskID, runID string, state domain.State, result DecisionResult, now time.Time) *OutcomeData {
	if !state.Terminal() {
		return nil
	}
	return &OutcomeData{TaskID: taskID, RunID: runID, TerminalState: state, Verdict: result.Decision.Verdict, FinalReviewRound: result.Decision.ReviewRound, FinalReviewDigest: result.DecisionDigest, FinalEvidenceDigest: result.Decision.EvidenceDigest, Summary: result.Decision.Summary, FindingCount: uint(len(result.Decision.BlockingFindings) + len(result.Decision.NonBlockingFindings)), GeneratedAt: now}
}
