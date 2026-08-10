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
	_ "embed"
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

//go:embed webdist/dag.js
var dagJS []byte

//go:embed webdist/dag.css
var dagCSS []byte

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
	mux.HandleFunc("/dag.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		_, _ = w.Write(dagJS)
	})
	mux.HandleFunc("/dag.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css")
		_, _ = w.Write(dagCSS)
	})
	mux.HandleFunc("/", handleIndex)
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

const indexHTML = `<!doctype html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Marshal 控制台</title><link rel="stylesheet" href="/dag.css">
<style>
 :root{--bg:#0d1117;--panel:#161b22;--border:#30363d;--text:#e6edf3;--muted:#8b949e;--accent:#58a6ff}
 body[data-theme=light]{--bg:#ffffff;--panel:#f6f8fa;--border:#d0d7de;--text:#1f2328;--muted:#57606a;--accent:#0969da}
 body[data-theme=light] .ok{background:#dafbe1;color:#1a7f37}body[data-theme=light] .err{background:#ffebe9;color:#cf222e}body[data-theme=light] .warn{background:#fff8c5;color:#9a6700}body[data-theme=light] .info{background:#ddf4ff;color:#0969da}body[data-theme=light] .mut{background:#eaeef2;color:#57606a}body[data-theme=light] .ev{background:#fff}
 *{box-sizing:border-box} body{font-family:-apple-system,"PingFang SC",Segoe UI,sans-serif;background:var(--bg);color:var(--text);margin:0}
 nav{position:sticky;top:0;z-index:10;display:flex;align-items:center;gap:16px;padding:12px 24px;background:var(--panel);border-bottom:1px solid var(--border)}
 nav .brand{font-weight:700;font-size:16px} nav a{color:var(--muted);text-decoration:none;font-size:14px;cursor:pointer} nav a.on{color:var(--text)} nav .sp{flex:1} nav .ro{color:var(--muted);font-size:12px;border:1px solid var(--border);padding:2px 8px;border-radius:10px}
 main{padding:20px 24px}
 .crumb{margin:4px 0 10px;font-size:13px;color:var(--muted)} .crumb a{color:var(--accent);cursor:pointer}
 .legend{display:flex;gap:10px;flex-wrap:wrap;margin:6px 0 16px;font-size:12px;color:var(--muted)} .legend b{display:inline-block;width:10px;height:10px;border-radius:2px;margin-right:4px}
 .sec{margin:16px 0 8px;font-size:13px;color:var(--muted);text-transform:uppercase;letter-spacing:.04em}
 .grid{display:flex;flex-wrap:wrap;gap:14px}
 .card{border:1px solid var(--border);border-radius:10px;padding:14px;width:320px;background:var(--panel);cursor:pointer}
 .card:hover{border-color:var(--accent)} .card h3{margin:0 0 6px;font-size:15px}
 .badge{display:inline-block;padding:2px 8px;border-radius:10px;font-size:12px;font-weight:600;margin:4px 0}
 .meta{color:var(--muted);font-size:12px} .rid{color:var(--muted);font-size:11px;font-family:ui-monospace,Menlo,monospace}
 .ok{background:#12261e;color:#3fb950}.err{background:#2a1215;color:#f85149}.warn{background:#2a2113;color:#d29922}.info{background:#121d2a;color:#58a6ff}.mut{background:#1c2128;color:#8b949e}
 .runrow{border:1px solid var(--border);border-radius:8px;padding:10px 12px;margin:8px 0;cursor:pointer;background:var(--panel)} .runrow:hover{border-color:var(--accent)}
 .cols{display:flex;gap:20px;flex-wrap:wrap} .col-main{flex:3;min-width:320px} .col-side{flex:1;min-width:280px}
 .dagwrap{position:relative;overflow:hidden;border:1px solid var(--border);border-radius:8px;background:var(--bg)} .dagwrap svg{transform-origin:0 0} .dagctl{position:absolute;right:8px;top:8px;z-index:2} .dagctl a{cursor:pointer;margin-left:6px;border:1px solid var(--border);border-radius:4px;padding:2px 7px;color:var(--muted);background:var(--panel)} svg{max-width:none} .node text{fill:var(--text);font-size:11px} .edge{stroke:#444c56;stroke-width:1.5;fill:none;marker-end:url(#ar)}
 .kv{font-size:13px;margin:4px 0} .kv b{color:var(--muted);font-weight:500}
 .ev{border-left:3px solid var(--border);padding:4px 10px;margin:6px 0;font-size:13px;border-radius:3px;background:var(--bg)} .ev small{color:var(--muted);display:block}
</style></head><body>
<nav><span class="brand">Marshal 控制台</span><a id="nav-tasks" onclick="nav('')">任务</a><input id="q" placeholder="检索任务/Run…" style="background:var(--bg);border:1px solid var(--border);color:var(--text);border-radius:6px;padding:4px 8px;font-size:13px"><span class="sp"></span><a id="theme" onclick="toggleTheme()">亮色</a><span class="ro">只读 · 控制在 CLI/Skill</span></nav>
<main><div class="crumb" id="crumb"></div><div class="legend" id="legend"></div><div id="main"></div></main>
<script src="/dag.js"></script>
<script>
var STATE={ACCEPTED:["已接受·成功","ok"],REJECTED:["已拒绝","err"],BLOCKED:["阻塞·需人工","err"],RUNNING:["执行中","info"],VERIFYING:["独立验证中","info"],REVIEW_PENDING:["待审查","warn"],REWORK_REQUESTED:["返工中","warn"],PUBLISHING:["发布中","info"],PUBLISHED:["已发布","info"],CI_PENDING:["等待CI","warn"],READY:["就绪","info"],PLANNED:["规划中","info"],ABORTED:["已中止","mut"],NO_CHANGE:["无变更","mut"]};
var EVENT={"planning.spec-accepted":"任务规格冻结","planning.inputs-frozen":"输入冻结","worker.started":"Worker 启动","worker.completed":"Worker 完成","worker.failed":"Worker 失败","verification.completed":"独立验证完成","review.accept":"审查接受","review.rework":"要求返工","publication.completed":"发布完成","run.aborted":"人工中止"};
var COL={ok:"#3fb950",err:"#f85149",warn:"#d29922",info:"#58a6ff",mut:"#8b949e"};
function st(s){return STATE[s]||[s,"mut"]} function col(s){return COL[st(s)[1]]}
function rel(t){var d=(Date.now()-new Date(t).getTime())/1000;if(d<60)return"刚刚";if(d<3600)return Math.floor(d/60)+"分钟前";if(d<86400)return Math.floor(d/3600)+"小时前";return Math.floor(d/86400)+"天前"}
function esc(x){return String(x).replace(/&/g,"&amp;").replace(/"/g,"&quot;").replace(/</g,"&lt;")}
function legend(){document.getElementById("legend").innerHTML=Object.keys(STATE).map(function(k){return '<span><b style="background:'+COL[STATE[k][1]]+'"></b>'+STATE[k][0]+'</span>'}).join("")}
var TASKS=null;var SEARCH="";
function toggleTheme(){var b=document.body;b.dataset.theme=b.dataset.theme==="light"?"":"light";document.getElementById("theme").textContent=b.dataset.theme==="light"?"暗色":"亮色"}
function nav(h){location.hash=h?"#/"+h:"#/"}
function bindNav(){document.querySelectorAll("[data-nav]").forEach(function(el){el.onclick=function(){nav(el.getAttribute("data-nav"))}})}
var Z={s:1,x:0,y:0};
function applyZ(){var svg=document.querySelector("#dagwrap svg");if(svg)svg.style.transform="translate("+Z.x+"px,"+Z.y+"px) scale("+Z.s+")"}
function mountDAG(d){var el=document.getElementById("dagroot");if(!el)return;var cols={ok:"#3fb950",err:"#f85149",warn:"#d29922",info:"#58a6ff",mut:"#8b949e"};var st=[["Leader/Plan","info"]];(d.attempts||[]).forEach(function(a,i){st.push(["Worker"+(i+1), a.workerStatus==="completed"?"ok":(a.workerStatus?"err":"info"), i])});st.push(["验证",d.verification==="pass"?"ok":(d.verification?"err":"mut")]);st.push(["审查",d.hasReview?"ok":"mut"]);st.push(["发布",d.hasPublication?"ok":"mut"]);st.push(["结局",d.hasOutcome?"ok":"mut"]);var data={stages:st.map(function(x,i){return{id:String(i),label:x[0],color:cols[x[1]],att:(x.length>2?x[2]:undefined)}}),edges:st.slice(1).map(function(_,i){return{from:i,to:i+1}})};window.__marshalAtt=function(att){var el=document.querySelectorAll("#atts .kv")[att];if(el)el.scrollIntoView({block:"center"})};if(window.MarshalDAG&&window.MarshalDAG.mount){window.MarshalDAG.mount(el,data)}else{el.innerHTML=dag(d)}}
function bindDag(){var w=document.getElementById("dagwrap");if(!w)return;Z={s:1,x:0,y:0};applyZ();
 w.querySelectorAll("[data-z]").forEach(function(b){b.onclick=function(e){e.stopPropagation();var m=b.getAttribute("data-z");if(m==="in")Z.s*=1.2;else if(m==="out")Z.s/=1.2;else{Z={s:1,x:0,y:0}}applyZ()}});
 w.onwheel=function(e){e.preventDefault();Z.s*=e.deltaY<0?1.1:0.9;applyZ()};
 var drag=null;w.onmousedown=function(e){drag={x:e.clientX-Z.x,y:e.clientY-Z.y}};w.onmousemove=function(e){if(drag){Z.x=e.clientX-drag.x;Z.y=e.clientY-drag.y;applyZ()}};w.onmouseup=function(){drag=null};
 w.querySelectorAll("[data-att]").forEach(function(n){n.onclick=function(e){e.stopPropagation();var a=n.getAttribute("data-att");var el=document.querySelectorAll("#atts .kv")[a];if(el)el.scrollIntoView({block:"center"})}})}
function route(){
  var h=location.hash.replace(/^#\/?/,"");
  document.getElementById("nav-tasks").className=h?"":"on";
  if(h.indexOf("task/")===0)return showTask(decodeURIComponent(h.slice(5)));
  if(h.indexOf("run/")===0)return showRun(decodeURIComponent(h.slice(4)));
  loadTasks();
}
window.addEventListener("hashchange",route);
async function loadTasks(){TASKS=TASKS||await (await fetch("/api/tasks")).json();renderTasks()}
function logical(){
  var logic={};TASKS.tasks.forEach(function(t){if(SEARCH&&((t.title||"").toLowerCase().indexOf(SEARCH)<0&&t.taskId.toLowerCase().indexOf(SEARCH)<0&&t.runId.toLowerCase().indexOf(SEARCH)<0))return;var ws=t.workspace||"(当前仓库)";var key=ws+"|"+(t.title||t.taskId);
    var g=logic[key]=logic[key]||{ws:ws,title:t.title||t.taskId,taskId:t.taskId,runCount:0,accepted:0,blocked:0,running:0,latestUpdate:t.latestUpdate,latestState:t.latestState};
    g.runCount+=t.runCount;g.accepted+=t.accepted;g.blocked+=t.blocked;g.running+=t.running;
    if(new Date(t.latestUpdate)>new Date(g.latestUpdate)){g.latestUpdate=t.latestUpdate;g.latestState=t.latestState}});
  return logic}
function renderTasks(){
  var logic=logical(),by={};Object.keys(logic).forEach(function(k){var g=logic[k];(by[g.ws]=by[g.ws]||[]).push(g)});
  var html="";Object.keys(by).forEach(function(ws){by[ws].sort(function(a,b){return new Date(b.latestUpdate)-new Date(a.latestUpdate)});
    html+='<div class="sec">Workspace · '+ws+'（'+by[ws].length+' 逻辑任务）</div><div class="grid">'+by[ws].map(function(g){var s=st(g.latestState);
      return '<div class="card" data-nav="task/'+encodeURIComponent(g.title)+'"><h3>'+esc(g.title)+'</h3><span class="badge '+s[1]+'">'+s[0]+'</span>'+
      '<div class="meta">'+rel(g.latestUpdate)+' · '+g.runCount+' Run · 成 '+g.accepted+' / 阻 '+g.blocked+' / 行 '+g.running+'</div>'+
      '<div class="rid">'+esc(g.taskId)+(g.runCount>1?' 等 '+g.runCount+' 次':'')+'</div></div>'}).join("")+'</div>'});
  document.getElementById("crumb").innerHTML="任务列表（"+Object.keys(logic).length+" 逻辑任务）";
  document.getElementById("main").innerHTML=html;bindNav();
}
async function showTask(title){
  var runs=(await (await fetch("/api/runs")).json()).runs.filter(function(r){return r.title===title||r.taskId===title});
  runs.sort(function(a,b){return new Date(b.updatedAt)-new Date(a.updatedAt)});
  document.getElementById("crumb").innerHTML='<a data-nav="">任务</a> / '+esc(title);
  document.getElementById("main").innerHTML='<div class="sec">Run（倒排）</div>'+runs.map(function(r){var s=st(r.state);
    return '<div class="runrow" data-nav="run/'+r.runId+'"><span class="badge '+s[1]+'">'+s[0]+'</span> <b>'+r.runId+'</b> <span class="meta">'+rel(r.updatedAt)+' · 尝试 '+r.attemptsUsed+' · 轮 '+r.reviewRound+'</span></div>'}).join("")+'<div id="runbox"></div>';bindNav();
  if(runs.length)showRun(runs[0].runId,true);
}
async function showRun(run,embedded){
  var d=await (await fetch("/api/runs/"+run)).json();
  var s=st(d.state);
  var h='<div class="cols"><div class="col-main"><div class="sec">流程 DAG（React Flow：缩放/平移/minimap/点节点看 attempt）</div><div id="dagroot" style="height:240px;border:1px solid var(--border);border-radius:8px;overflow:hidden"></div>'+
   '<div class="sec">事件时间线</div>'+(d.events||[]).map(function(e){return '<div class="ev">'+(EVENT[e.type]||e.type)+'<small>'+e.from+' → '+e.to+' · '+(e.at||"")+'</small></div>'}).join("")+'</div>'+
   '<div class="col-side"><div class="sec">Run 详情</div><div class="kv"><b>状态</b> <span class="badge '+s[1]+'">'+s[0]+'</span></div>'+
   '<div class="kv"><b>Workspace</b> '+esc(d.workspace||"")+'</div>'+
   '<div class="kv"><b>验证</b> '+(d.verification||"未运行")+'（'+d.gatesPassed+'/'+d.gatesTotal+(d.gatesFailed&&d.gatesFailed.length?' 失败:'+d.gatesFailed.join(","):'')+'）</div>'+
   '<div class="kv"><b>审查/发布/结局</b> '+(d.hasReview?"已":"未")+' / '+(d.hasPublication?"已":"未")+' / '+(d.hasOutcome?"已":"无")+'</div>'+
   '<div class="kv"><b>重试/返工</b> '+d.operationalRetries+' / '+d.reworkRounds+' · 尝试 '+d.attemptsUsed+'</div>'+
   '<div class="kv"><b>耗时</b> Worker '+(d.workerDurationSec||0)+'s · 全程 '+(d.totalDurationSec||0)+'s</div>'+
   '<div class="kv"><b>Token</b> 入 '+(d.inputTokens||0)+' 出 '+(d.outputTokens||0)+'</div>'+
   '<div class="kv"><b>Artifact</b> '+((d.artifacts&&d.artifacts.length)?d.artifacts.length+' 项':"无")+'</div>'+
   '<div class="sec">Worker 尝试</div><div id="atts">'+((d.attempts||[]).map(function(a){return '<div class="kv">· '+a.id+(a.workerStatus?"（"+a.workerStatus+"）":"")+'</div>'}).join("")||'<div class="kv">无</div>')+'</div></div>';bindDag();
  if(embedded){document.getElementById("runbox").innerHTML=h;bindNav()}else{document.getElementById("crumb").innerHTML='<a data-nav="">任务</a> / <a data-nav="task/'+encodeURIComponent(d.title||d.taskId)+'">'+esc(d.title||d.taskId)+'</a> / '+run;document.getElementById("main").innerHTML=h;bindNav()}
}
function node(x,y,label,color){return '<g class="node"><rect x="'+x+'" y="'+y+'" width="120" height="34" fill="'+color+'22" stroke="'+color+'"/><text x="'+(x+60)+'" y="'+(y+21)+'" text-anchor="middle">'+label+'</text></g>'}
function dag(d){
  var stages=[["Leader/Plan","info"]];
  (d.attempts||[]).forEach(function(a,i){var c=a.workerStatus==="completed"?"ok":(a.workerStatus?"err":"info");stages.push(["Worker"+(i+1),c,i])});
  stages.push(["验证",d.verification==="pass"?"ok":(d.verification?"err":"mut")]);stages.push(["审查",d.hasReview?"ok":"mut"]);stages.push(["发布",d.hasPublication?"ok":"mut"]);stages.push(["结局",d.hasOutcome?"ok":"mut"]);
  var x=10,parts=[],edges=[];stages.forEach(function(sg,i){var c=COL[sg[1]];var att=(sg.length>2&&sg[2]!==undefined)?' data-att="'+sg[2]+'"':'';parts.push('<g class="node"'+att+'><rect x="'+x+'" y="10" width="120" height="34" fill="'+c+'22" stroke="'+c+'"/><text x="'+(x+60)+'" y="31" text-anchor="middle">'+sg[0]+'</text></g>');if(i>0)edges.push('<path class="edge" d="M'+(x-10)+' 27 L'+x+' 27"/>');x+=140});
  return '<svg width="'+x+'" height="60"><defs><marker id="ar" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="6" markerHeight="6" orient="auto"><path d="M0,0L10,5L0,10z" fill="#444c56"/></marker></defs>'+edges.join("")+parts.join("")+'</svg>';
}
legend();route();document.getElementById("q").addEventListener("input",function(e){SEARCH=e.target.value.toLowerCase().trim();if(!location.hash||location.hash==="#/"||location.hash==="#")renderTasks()});
var es=new EventSource("/api/stream");
es.addEventListener("snapshot",function(e){if(!location.hash||location.hash==="#/"||location.hash==="#")loadTasks()});
</script></body></html>`

func handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(indexHTML))
}
