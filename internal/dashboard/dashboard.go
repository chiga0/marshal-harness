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
	Title        string    `json:"title,omitempty"`
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

// TaskSummary aggregates a Task's runs for the top-level (level-1) view.
type TaskSummary struct {
	TaskID       string    `json:"taskId"`
	Title        string    `json:"title,omitempty"`
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
	Addr      string // default 127.0.0.1:7717
}

// NewHandler returns a read-only http.Handler serving the dashboard.
func NewHandler(opts Options) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", handleIndex)
	mux.HandleFunc("/api/tasks", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "read-only", http.StatusMethodNotAllowed)
			return
		}
		tasks, err := ListTasks(opts.StateRoot)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"tasks": tasks})
	})
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
	return detail, nil
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
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Marshal 控制台</title>
<style>
 :root{--bg:#0d1117;--panel:#161b22;--border:#30363d;--text:#e6edf3;--muted:#8b949e}
 *{box-sizing:border-box} body{font-family:-apple-system,"PingFang SC",Segoe UI,sans-serif;background:var(--bg);color:var(--text);margin:0;padding:24px}
 header{display:flex;align-items:baseline;gap:12px;flex-wrap:wrap;margin-bottom:6px}
 h1{font-size:20px;margin:0} .sub{color:var(--muted);font-size:13px}
 .crumb{margin:8px 0;font-size:13px;color:var(--muted)} .crumb a{color:#58a6ff;cursor:pointer}
 .legend{display:flex;gap:10px;flex-wrap:wrap;margin:8px 0 16px;font-size:12px;color:var(--muted)}
 .legend b{display:inline-block;width:10px;height:10px;border-radius:2px;margin-right:4px}
 .grid{display:flex;flex-wrap:wrap;gap:14px}
 .card{border:1px solid var(--border);border-radius:10px;padding:14px;width:320px;background:var(--panel);cursor:pointer}
 .card:hover{border-color:#58a6ff} .card h3{margin:0 0 6px;font-size:15px}
 .badge{display:inline-block;padding:2px 8px;border-radius:10px;font-size:12px;font-weight:600;margin:4px 0}
 .meta{color:var(--muted);font-size:12px} .rid{color:var(--muted);font-size:11px;font-family:ui-monospace,Menlo,monospace}
 .ok{background:#12261e;color:#3fb950}.err{background:#2a1215;color:#f85149}.warn{background:#2a2113;color:#d29922}.info{background:#121d2a;color:#58a6ff}.mut{background:#1c2128;color:#8b949e}
 .runrow{border:1px solid var(--border);border-radius:8px;padding:10px 12px;margin:8px 0;cursor:pointer;background:var(--panel)}
 .runrow:hover{border-color:#58a6ff}
 svg{max-width:100%} .node rect{rx:6} .node text{fill:var(--text);font-size:11px} .edge{stroke:#444c56;stroke-width:1.5;fill:none;marker-end:url(#ar)}
 .sec{margin:16px 0 6px;font-size:13px;color:var(--muted);text-transform:uppercase;letter-spacing:.04em}
 .kv{font-size:13px;margin:4px 0} .kv b{color:var(--muted);font-weight:500}
 .ev{border-left:3px solid var(--border);padding:4px 10px;margin:6px 0;font-size:13px;border-radius:3px;background:#0d1117} .ev small{color:var(--muted);display:block}
</style></head><body>
<header><h1>Marshal 控制台</h1><span class="sub">只读 · 任务编排概览（控制请在 CLI/Skill）</span></header>
<div class="crumb" id="crumb"></div>
<div class="legend" id="legend"></div>
<div id="main"></div>
<script>
var STATE={ACCEPTED:["已接受·成功","ok"],REJECTED:["已拒绝","err"],BLOCKED:["阻塞·需人工","err"],RUNNING:["执行中","info"],VERIFYING:["独立验证中","info"],REVIEW_PENDING:["待审查","warn"],REWORK_REQUESTED:["返工中","warn"],PUBLISHING:["发布中","info"],PUBLISHED:["已发布","info"],CI_PENDING:["等待CI","warn"],READY:["就绪","info"],PLANNED:["规划中","info"],ABORTED:["已中止","mut"],NO_CHANGE:["无变更","mut"]};
var EVENT={"planning.spec-accepted":"任务规格冻结","planning.inputs-frozen":"输入冻结","worker.started":"Worker 启动","worker.completed":"Worker 完成","worker.failed":"Worker 失败","verification.completed":"独立验证完成","review.accept":"审查接受","review.rework":"要求返工","publication.completed":"发布完成","run.aborted":"人工中止"};
var COL={ok:"#3fb950",err:"#f85149",warn:"#d29922",info:"#58a6ff",mut:"#8b949e"};
function st(s){return STATE[s]||[s,"mut"]} function col(s){return col2(st(s)[1])} function col2(c){return COL[c]}
function rel(t){var d=(Date.now()-new Date(t).getTime())/1000;if(d<60)return"刚刚";if(d<3600)return Math.floor(d/60)+"分钟前";if(d<86400)return Math.floor(d/3600)+"小时前";return Math.floor(d/86400)+"天前"}
function legend(){document.getElementById("legend").innerHTML=Object.keys(STATE).map(function(k){return '<span><b style="background:'+col(k)+'"></b>'+STATE[k][0]+'</span>'}).join("")}
var TASKS=null;
async function loadTasks(){TASKS=await (await fetch("/api/tasks")).json();renderTasks()}
function renderTasks(){
  document.getElementById("crumb").innerHTML="任务列表（"+TASKS.tasks.length+"）· 按最近活跃倒排";
  document.getElementById("main").innerHTML='<div class="grid">'+TASKS.tasks.map(function(t){var s=st(t.latestState);
    return '<div class="card" data-task="'+t.taskId+'"><h3>'+(t.title||t.taskId)+'</h3><span class="badge '+s[1]+'">'+s[0]+'</span>'+
    '<div class="meta">'+rel(t.latestUpdate)+' · '+t.runCount+' 个 Run · 成功 '+t.accepted+' / 阻塞 '+t.blocked+' / 进行 '+t.running+'</div>'+
    '<div class="rid">'+t.taskId+'</div></div>'}).join("")+'</div>';
  document.querySelectorAll(".card").forEach(function(el){el.onclick=function(){openTask(el.dataset.task)}});
}
async function openTask(taskId){
  var runs=(await (await fetch("/api/runs")).json()).runs.filter(function(r){return r.taskId===taskId});
  runs.sort(function(a,b){return new Date(b.updatedAt)-new Date(a.updatedAt)});
  var t=TASKS.tasks.find(function(x){return x.taskId===taskId});
  document.getElementById("crumb").innerHTML='<a onclick="loadTasks()">任务列表</a> / '+(t&&t.title||taskId)+' · Run 按时间倒排';
  document.getElementById("main").innerHTML=runs.map(function(r){var s=st(r.state);
    return '<div class="runrow" data-run="'+r.runId+'"><span class="badge '+s[1]+'">'+s[0]+'</span> <b>'+r.runId+'</b> <span class="meta">'+rel(r.updatedAt)+' · 尝试 '+r.attemptsUsed+' · 轮 '+r.reviewRound+'</span></div>'}).join("")+
    '<div id="dagbox"></div><div id="detail"></div>';
  document.querySelectorAll(".runrow").forEach(function(el){el.onclick=function(){openRun(el.dataset.run)}});
  if(runs.length)openRun(runs[0].runId);
}
async function openRun(run){
  var d=await (await fetch("/api/runs/"+run)).json();
  document.getElementById("dagbox").innerHTML='<div class="sec">流程 DAG（Leader → Worker → 门禁）</div>'+dag(d);
  var s=st(d.state);
  var h='<div class="sec">Run 详情</div><div class="kv"><b>状态</b> '+s[0]+'</div>'+
   '<div class="kv"><b>验证</b> '+(d.verification||"未运行")+' · <b>审查</b> '+(d.hasReview?"已进行":"未")+' · <b>发布</b> '+(d.hasPublication?"已":"未")+' · <b>结局</b> '+(d.hasOutcome?"已记录":"无")+'</div>';
  if(d.attempts&&d.attempts.length)h+='<div class="sec">Worker 尝试（Attempt）</div>'+d.attempts.map(function(a){return '<div class="kv">· '+a.id+(a.workerStatus?"（"+a.workerStatus+"）":"")+'</div>'}).join("");
  h+='<div class="sec">事件时间线</div>'+(d.events||[]).map(function(e){return '<div class="ev">'+(EVENT[e.type]||e.type)+'<small>'+e.from+' → '+e.to+' · '+(e.at||"")+'</small></div>'}).join("");
  document.getElementById("detail").innerHTML=h;
}
function node(x,y,label,color){return '<g class="node"><rect x="'+x+'" y="'+y+'" width="120" height="34" fill="'+color+'22" stroke="'+color+'"/><text x="'+(x+60)+'" y="'+(y+21)+'" text-anchor="middle">'+label+'</text></g>'}
function dag(d){
  var stages=[["Leader/Plan","info"]];
  (d.attempts||[]).forEach(function(a,i){var c=a.workerStatus==="completed"?"ok":(a.workerStatus?"err":"info");stages.push(["Worker"+(i+1),c]);});
  stages.push(["验证", d.verification==="pass"?"ok":(d.verification?"err":"mut")]);
  stages.push(["审查", d.hasReview?"ok":"mut"]); stages.push(["发布", d.hasPublication?"ok":"mut"]); stages.push(["结局", d.hasOutcome?"ok":"mut"]);
  var x=10,parts=[],edges=[];
  stages.forEach(function(sg,i){var c=col2(sg[1]);parts.push(node(x,10,sg[0],c));if(i>0)edges.push('<path class="edge" d="M'+(x-10)+' 27 L'+x+' 27"/>');x+=140});
  return '<svg width="'+(x)+'" height="60"><defs><marker id="ar" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="6" markerHeight="6" orient="auto"><path d="M0,0L10,5L0,10z" fill="#444c56"/></marker></defs>'+edges.join("")+parts.join("")+'</svg>';
}
legend();loadTasks();
var es=new EventSource("/api/stream");
es.addEventListener("snapshot",function(e){if(document.getElementById("crumb").textContent.indexOf("任务列表")===0)renderFromRuns(JSON.parse(e.data).runs)});
function renderFromRuns(runs){/* refresh task list from snapshot */loadTasks()}
</script></body></html>`

func handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(indexHTML))
}
