// Package dashboard is a READ-ONLY observability surface for Marshal runs.
//
// POC（实验分支 exp/web-dashboard，不进主干）。它只读取 .marshal 状态目录
// （state.json + events.jsonl），以 HTTP+ SSE 呈现实时任务概览（DAG/状态），
// 不提供任何控制端点（approve/publish 等仍留在 CLI/Skill），因此不成为第二个
// 权威，不改变信任边界（见 docs/research/marshal-future-directions.md §2.1）。
//
// 生产化前置（本 POC 之外）：认证、TLS/反向代理、多用户、远程状态源。
package dashboard

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/domain"
)

//go:embed all:webdist
var webFS embed.FS

// RunSummary is the read-only projection of one Run shown in the dashboard.
type RunSummary struct {
	RunID        string    `json:"runId"`
	TaskID       string    `json:"taskId"`
	Title        string    `json:"title,omitempty"`
	Workspace    string    `json:"workspace,omitempty"`
	State        string    `json:"state"`
	Sequence     uint64    `json:"sequence"`
	ReviewRound  uint      `json:"reviewRound"`
	AttemptsUsed uint      `json:"attemptsUsed"`
	CurrentAtt   string    `json:"currentAttemptId,omitempty"`
	Terminal     string    `json:"terminalReason,omitempty"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// EventLite is a lightweight event row for the live feed.
type EventLite struct {
	Sequence uint64 `json:"sequence"`
	Type     string `json:"type"`
	From     string `json:"from"`
	To       string `json:"to"`
	At       string `json:"at"`
}

// AttemptInfo is one Worker Attempt's projection for the DAG detail view.
type AttemptInfo struct {
	ID           string `json:"id"`
	WorkerStatus string `json:"workerStatus,omitempty"`
}

// RunDetail is the enriched per-run projection (Attempt/Review/Publish/Outcome).
type RunDetail struct {
	RunSummary
	Events             []EventLite   `json:"events"`
	Attempts           []AttemptInfo `json:"attempts"`
	Verification       string        `json:"verification,omitempty"`
	GatesPassed        int           `json:"gatesPassed"`
	GatesTotal         int           `json:"gatesTotal"`
	GatesFailed        []string      `json:"gatesFailed,omitempty"`
	Artifacts          []string      `json:"artifacts,omitempty"`
	OperationalRetries uint          `json:"operationalRetries"`
	ReworkRounds       uint          `json:"reworkRounds"`
	WorkerDurationSec  int64         `json:"workerDurationSec"`
	TotalDurationSec   int64         `json:"totalDurationSec"`
	InputTokens        int64         `json:"inputTokens"`
	OutputTokens       int64         `json:"outputTokens"`
	HasReview          bool          `json:"hasReview"`
	HasPublication     bool          `json:"hasPublication"`
	HasOutcome         bool          `json:"hasOutcome"`
}

// TaskSummary aggregates a Task's runs for the top-level (level-1) view.
type TaskSummary struct {
	TaskID       string    `json:"taskId"`
	Title        string    `json:"title,omitempty"`
	Workspace    string    `json:"workspace,omitempty"`
	LatestState  string    `json:"latestState"`
	RunCount     int       `json:"runCount"`
	Accepted     int       `json:"accepted"`
	Blocked      int       `json:"blocked"`
	Running      int       `json:"running"`
	LatestUpdate time.Time `json:"latestUpdate"`
}

// ListTasks groups runs by task and aggregates, sorted by most recent activity.
func ListTasks(stateRoot string) ([]TaskSummary, error) {
	runs, err := ListRuns(stateRoot)
	if err != nil {
		return nil, err
	}
	by := map[string]*TaskSummary{}
	for _, r := range runs {
		t, ok := by[r.TaskID]
		if !ok {
			t = &TaskSummary{TaskID: r.TaskID, Title: r.Title}
			by[r.TaskID] = t
		}
		t.RunCount++
		switch r.State {
		case "ACCEPTED":
			t.Accepted++
		case "BLOCKED", "REJECTED":
			t.Blocked++
		case "RUNNING", "VERIFYING", "REVIEW_PENDING", "PUBLISHING", "CI_PENDING":
			t.Running++
		}
		if r.UpdatedAt.After(t.LatestUpdate) {
			t.LatestUpdate = r.UpdatedAt
			t.LatestState = r.State
		}
		if t.Title == "" && r.Title != "" {
			t.Title = r.Title
		}
	}
	out := make([]TaskSummary, 0, len(by))
	for _, t := range by {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LatestUpdate.After(out[j].LatestUpdate) })
	return out, nil
}

// ListRuns reads every run's state.json under stateRoot/runs (read-only).
func ListRuns(stateRoot string) ([]RunSummary, error) {
	runsDir := filepath.Join(stateRoot, "runs")
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []RunSummary{}, nil
		}
		return nil, err
	}
	out := make([]RunSummary, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		state, err := readState(filepath.Join(runsDir, entry.Name(), "state.json"))
		if err != nil {
			continue
		}
		out = append(out, RunSummary{
			RunID: state.RunID, TaskID: state.TaskID, Title: readTitle(filepath.Join(runsDir, entry.Name())), State: string(state.State),
			Sequence: state.Sequence, ReviewRound: state.ReviewRound, AttemptsUsed: state.AttemptsUsed,
			CurrentAtt: state.CurrentAttemptID, Terminal: state.TerminalReason, UpdatedAt: state.UpdatedAt,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

// ReadEvents reads a run's events.jsonl into lightweight rows (read-only).
func ReadEvents(stateRoot, runID string) ([]EventLite, error) {
	path := filepath.Join(stateRoot, "runs", runID, "events.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []EventLite{}, nil
		}
		return nil, err
	}
	var out []EventLite
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var ev struct {
			Sequence  uint64    `json:"sequence"`
			Type      string    `json:"type"`
			StateFrom string    `json:"stateFrom"`
			StateTo   string    `json:"stateTo"`
			Timestamp time.Time `json:"timestamp"`
		}
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		out = append(out, EventLite{Sequence: ev.Sequence, Type: ev.Type, From: ev.StateFrom, To: ev.StateTo, At: ev.Timestamp.UTC().Format(time.RFC3339)})
	}
	return out, nil
}

func readState(path string) (domain.RunState, error) {
	var state domain.RunState
	data, err := os.ReadFile(path)
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, err
	}
	return state, nil
}

// Options configures the read-only server.
type Options struct {
	StateRoot string
	Addr      string   // default 127.0.0.1:7717
	Roots     []string // additional repository state roots to aggregate (workspace grouping)
}

func (o Options) roots() []string {
	roots := []string{o.StateRoot}
	for _, r := range o.Roots {
		if r != o.StateRoot && r != "" {
			roots = append(roots, r)
		}
	}
	return roots
}

func workspaceLabel(root string) string {
	return filepath.Base(filepath.Dir(root)) + "/" + filepath.Base(root)
}

func (o Options) findRunRoot(runID string) string {
	for _, root := range o.roots() {
		if _, err := os.Stat(filepath.Join(root, "runs", runID, "state.json")); err == nil {
			return root
		}
	}
	return o.StateRoot
}

// NewHandler returns a read-only http.Handler serving the dashboard.
func NewHandler(opts Options) http.Handler {
	mux := http.NewServeMux()
	sub, err := fs.Sub(webFS, "webdist")
	if err == nil {
		mux.Handle("/", http.FileServer(http.FS(sub)))
	}
	mux.HandleFunc("/api/tasks", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "read-only", http.StatusMethodNotAllowed)
			return
		}
		var all []TaskSummary
		for _, root := range opts.roots() {
			rs, err := ListTasks(root)
			if err != nil {
				continue
			}
			for i := range rs {
				rs[i].Workspace = workspaceLabel(root)
			}
			all = append(all, rs...)
		}
		sort.Slice(all, func(i, j int) bool { return all[i].LatestUpdate.After(all[j].LatestUpdate) })
		writeJSON(w, map[string]any{"tasks": all})
	})
	mux.HandleFunc("/api/runs", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "read-only", http.StatusMethodNotAllowed)
			return
		}
		var all []RunSummary
		for _, root := range opts.roots() {
			rs, err := ListRunsCached(root)
			if err != nil {
				continue
			}
			for i := range rs {
				rs[i].Workspace = workspaceLabel(root)
			}
			all = append(all, rs...)
		}
		sort.Slice(all, func(i, j int) bool { return all[i].UpdatedAt.After(all[j].UpdatedAt) })
		writeJSON(w, map[string]any{"runs": all})
	})
	mux.HandleFunc("/api/runs/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "read-only", http.StatusMethodNotAllowed)
			return
		}
		runID := strings.TrimPrefix(r.URL.Path, "/api/runs/")
		root := opts.findRunRoot(runID)
		detail, err := ReadRunDetail(root, runID)
		if err != nil {
			http.Error(w, "run not found", http.StatusNotFound)
			return
		}
		detail.Workspace = workspaceLabel(root)
		writeJSON(w, detail)
	})
	mux.HandleFunc("/api/stream", func(w http.ResponseWriter, r *http.Request) {
		stream(w, r, opts.StateRoot)
	})
	return mux
}

// Serve runs the read-only dashboard until ctx is done or the listener fails.
func Serve(opts Options) error {
	addr := opts.Addr
	if addr == "" {
		addr = "127.0.0.1:7717"
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: NewHandler(opts), ReadHeaderTimeout: 5 * time.Second}
	return server.Serve(listener)
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

// stream pushes a runs snapshot over SSE every second (server-side poll).
func stream(w http.ResponseWriter, r *http.Request, stateRoot string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	last := ""
	for {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(time.Second):
		}
		runs, err := ListRunsCached(stateRoot)
		if err != nil {
			return
		}
		data, _ := json.Marshal(map[string]any{"runs": runs})
		if string(data) != last {
			last = string(data)
			fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", data)
			flusher.Flush()
		}
	}
}

// ReadRunDetail returns the enriched per-run projection for the DAG detail view.
func ReadRunDetail(stateRoot, runID string) (RunDetail, error) {
	runDir := filepath.Join(stateRoot, "runs", runID)
	state, err := readState(filepath.Join(runDir, "state.json"))
	if err != nil {
		return RunDetail{}, err
	}
	events, _ := ReadEvents(stateRoot, runID)
	detail := RunDetail{
		RunSummary: RunSummary{RunID: state.RunID, TaskID: state.TaskID, Title: readTitle(runDir), State: string(state.State),
			Sequence: state.Sequence, ReviewRound: state.ReviewRound, AttemptsUsed: state.AttemptsUsed,
			CurrentAtt: state.CurrentAttemptID, Terminal: state.TerminalReason, UpdatedAt: state.UpdatedAt},
		Events:         events,
		HasReview:      fileExists(filepath.Join(runDir, "review-packet.json")) || dirHasEntries(filepath.Join(runDir, "decisions")),
		HasPublication: fileExists(filepath.Join(runDir, "publication-record.json")),
		HasOutcome:     fileExists(filepath.Join(runDir, "outcome.json")),
	}
	if v, err := readVerification(filepath.Join(runDir, "verification-report.json")); err == nil {
		detail.Verification = v
	}
	if entries, err := os.ReadDir(filepath.Join(runDir, "attempts")); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			info := AttemptInfo{ID: entry.Name()}
			if wr, err := os.ReadFile(filepath.Join(runDir, "attempts", entry.Name(), "worker-result.json")); err == nil {
				var parsed struct {
					Status string `json:"status"`
				}
				if json.Unmarshal(wr, &parsed) == nil {
					info.WorkerStatus = parsed.Status
				}
			}
			detail.Attempts = append(detail.Attempts, info)
		}
	}
	detail.OperationalRetries = state.OperationalRetriesUsed
	detail.ReworkRounds = state.ReworkRoundsUsed
	detail.Artifacts = readArtifacts(filepath.Join(runDir, "artifact-manifest.json"))
	detail.GatesPassed, detail.GatesTotal, detail.GatesFailed = readGates(filepath.Join(runDir, "verification-report.json"))
	detail.WorkerDurationSec, detail.TotalDurationSec = durations(state, events)
	detail.InputTokens, detail.OutputTokens = readTokens(runDir)
	return detail, nil
}

func readArtifacts(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var parsed struct {
		Artifacts []struct {
			ID string `json:"id"`
		} `json:"artifacts"`
	}
	if json.Unmarshal(data, &parsed) != nil {
		return nil
	}
	var out []string
	for _, a := range parsed.Artifacts {
		out = append(out, a.ID)
	}
	return out
}

func readGates(path string) (passed, total int, failed []string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, nil
	}
	var parsed struct {
		Gates []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"gates"`
	}
	if json.Unmarshal(data, &parsed) != nil {
		return 0, 0, nil
	}
	for _, g := range parsed.Gates {
		total++
		if g.Status == "pass" {
			passed++
		} else {
			failed = append(failed, g.ID)
		}
	}
	return passed, total, failed
}

// durations returns total worker seconds (sum of attempts) and total run seconds.
func durations(state domain.RunState, events []EventLite) (workerSec, totalSec int64) {
	if !state.CreatedAt.IsZero() && !state.UpdatedAt.IsZero() {
		totalSec = int64(state.UpdatedAt.Sub(state.CreatedAt).Seconds())
	}
	var started, completed time.Time
	for _, e := range events {
		t, err := time.Parse(time.RFC3339, e.At)
		if err != nil {
			continue
		}
		if e.Type == "worker.started" && started.IsZero() {
			started = t
		}
		if e.Type == "worker.completed" {
			completed = t
		}
	}
	if !started.IsZero() && !completed.IsZero() {
		workerSec = int64(completed.Sub(started).Seconds())
	}
	return workerSec, totalSec
}

// readTokens sums input/output tokens across attempts' transcript metadata.
func readTokens(runDir string) (in, out int64) {
	entries, err := os.ReadDir(filepath.Join(runDir, "attempts"))
	if err != nil {
		return 0, 0
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		outDir := filepath.Join(runDir, "attempts", entry.Name(), "control", "output")
		// PTY best-effort: worker-result may carry usage when the agent reports it.
		if wr, err := os.ReadFile(filepath.Join(outDir, "worker-result.json")); err == nil {
			var parsed struct {
				Usage struct {
					Input  int64 `json:"input"`
					Output int64 `json:"output"`
				} `json:"usage"`
			}
			if json.Unmarshal(wr, &parsed) == nil && (parsed.Usage.Input > 0 || parsed.Usage.Output > 0) {
				in += parsed.Usage.Input
				out += parsed.Usage.Output
				continue
			}
		}
		metas, err := os.ReadDir(outDir)
		if err != nil {
			continue
		}
		for _, m := range metas {
			if !strings.HasSuffix(m.Name(), "transcript-meta.json") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(outDir, m.Name()))
			if err != nil {
				continue
			}
			var parsed struct {
				InputTokens  int64 `json:"inputTokens"`
				OutputTokens int64 `json:"outputTokens"`
			}
			if json.Unmarshal(data, &parsed) == nil {
				in += parsed.InputTokens
				out += parsed.OutputTokens
			}
		}
	}
	return in, out
}

func fileExists(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}

// readTitle returns the human task title from task-spec.json, else "".
func readTitle(runDir string) string {
	data, err := os.ReadFile(filepath.Join(runDir, "task-spec.json"))
	if err != nil {
		return ""
	}
	var parsed struct {
		Metadata struct {
			Title string `json:"title"`
		} `json:"metadata"`
	}
	if json.Unmarshal(data, &parsed) != nil {
		return ""
	}
	return parsed.Metadata.Title
}

func dirHasEntries(path string) bool {
	entries, err := os.ReadDir(path)
	return err == nil && len(entries) > 0
}

func readVerification(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var parsed struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return "", err
	}
	return parsed.Status, nil
}

// runsCache memoizes ListRuns keyed by the runs dir mtime, so the SSE loop does
// not re-read every state.json each tick (production scale path: SQLite index).
type runsCache struct {
	modTime time.Time
	runs    []RunSummary
}

var cache runsCache

// ListRunsCached returns ListRuns with mtime-based invalidation.
func ListRunsCached(stateRoot string) ([]RunSummary, error) {
	runsDir := filepath.Join(stateRoot, "runs")
	info, err := os.Stat(runsDir)
	if err != nil {
		return ListRuns(stateRoot)
	}
	if info.ModTime() == cache.modTime && cache.runs != nil {
		return cache.runs, nil
	}
	runs, err := ListRuns(stateRoot)
	if err != nil {
		return nil, err
	}
	cache = runsCache{modTime: info.ModTime(), runs: runs}
	return runs, nil
}
