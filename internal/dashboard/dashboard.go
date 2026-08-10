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
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/domain"
)

// RunSummary is the read-only projection of one Run shown in the dashboard.
type RunSummary struct {
	RunID        string    `json:"runId"`
	TaskID       string    `json:"taskId"`
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
	Events         []EventLite   `json:"events"`
	Attempts       []AttemptInfo `json:"attempts"`
	Verification   string        `json:"verification,omitempty"`
	HasReview      bool          `json:"hasReview"`
	HasPublication bool          `json:"hasPublication"`
	HasOutcome     bool          `json:"hasOutcome"`
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
			RunID: state.RunID, TaskID: state.TaskID, State: string(state.State),
			Sequence: state.Sequence, ReviewRound: state.ReviewRound, AttemptsUsed: state.AttemptsUsed,
			CurrentAtt: state.CurrentAttemptID, Terminal: state.TerminalReason, UpdatedAt: state.UpdatedAt,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.Before(out[j].UpdatedAt) })
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
	Addr      string // default 127.0.0.1:7717
}

// NewHandler returns a read-only http.Handler serving the dashboard.
func NewHandler(opts Options) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", handleIndex)
	mux.HandleFunc("/api/runs", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "read-only", http.StatusMethodNotAllowed)
			return
		}
		runs, err := ListRunsCached(opts.StateRoot)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"runs": runs})
	})
	mux.HandleFunc("/api/runs/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "read-only", http.StatusMethodNotAllowed)
			return
		}
		runID := strings.TrimPrefix(r.URL.Path, "/api/runs/")
		detail, err := ReadRunDetail(opts.StateRoot, runID)
		if err != nil {
			http.Error(w, "run not found", http.StatusNotFound)
			return
		}
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
		RunSummary: RunSummary{RunID: state.RunID, TaskID: state.TaskID, State: string(state.State),
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
	return detail, nil
}

func fileExists(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
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

const indexHTML = `<!doctype html>
<html><head><meta charset="utf-8"><title>Marshal Dashboard</title>
<style>
 body{font-family:ui-monospace,Menlo,monospace;background:#0d1117;color:#e6edf3;margin:0;padding:24px}
 h1{font-size:18px} .grid{display:flex;flex-wrap:wrap;gap:12px}
 .task{border:1px solid #30363d;border-radius:8px;padding:12px;min-width:260px}
 .run{border-left:3px solid #444c56;padding:4px 8px;margin:6px 0;border-radius:4px;background:#161b22;cursor:pointer}
 .st{font-weight:bold} .ACCEPTED{color:#3fb950}.BLOCKED,.REJECTED{color:#f85149}
 .RUNNING,.VERIFYING,.REVIEW_PENDING{color:#d29922}.READY,.PLANNED{color:#58a6ff}
 small{color:#8b949e}
</style></head><body>
<h1>Marshal Dashboard <small>(read-only)</small></h1>
<div id="dag" class="grid"></div>
<script>
function color(s){return s}
function render(runs){
  const byTask={};
  runs.forEach(r=>{(byTask[r.taskId]=byTask[r.taskId]||[]).push(r)});
  const dag=document.getElementById('dag');
  dag.innerHTML=Object.keys(byTask).map(t=>{
    const runs=byTask[t].map(r=>
      '<div class="run" data-run="'+r.runId+'"><span class="st '+r.state+'">'+r.state+'</span> '+
      '<small>'+r.runId+'</small><br><small>seq='+r.sequence+' att='+r.attemptsUsed+' round='+r.reviewRound+'</small></div>'
    ).join('');
    return '<div class="task"><b>'+t+'</b>'+runs+'</div>';
  }).join('');
  dag.querySelectorAll('.run').forEach(el=>el.onclick=()=>{location.hash=el.dataset.run;showEvents(el.dataset.run)});
}
async function showEvents(run){
  const d=await (await fetch('/api/runs/'+run)).json();
  alert(run+'\\n'+['review:'+(d.hasReview?'y':'n'),'publish:'+(d.hasPublication?'y':'n'),'outcome:'+(d.hasOutcome?'y':'n'),'verify:'+(d.verification||'-')].join(' ')+'\\n'+(d.attempts||[]).map(a=>' attempt '+a.id+(a.workerStatus?' ['+a.workerStatus+']':'')).join('\\n')+'\\n'+(d.events||[]).map(e=>e.sequence+' '+e.type+' '+e.from+'->'+e.to).join('\\n'));
}
async function load(){const d=await (await fetch('/api/runs')).json();render(d.runs||[]);}
load();
const es=new EventSource('/api/stream');
es.addEventListener('snapshot',e=>{render(JSON.parse(e.data).runs||[])});
</script></body></html>`

func handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(indexHTML))
}
