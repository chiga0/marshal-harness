//go:build darwin && arm64

package productionruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/goal"
	"github.com/chiga0/marshal-harness/internal/planning"
	"github.com/chiga0/marshal-harness/internal/taskhttp"
)

func repositoryTaskJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = canonical.JSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

type repositoryTaskFailingProducer struct {
	application.TaskTemplatePort
	raw []byte
	err error
}

func (p repositoryTaskFailingProducer) RenderTask(string, goal.TaskSubmission) ([]byte, error) {
	return p.raw, p.err
}

// Complete Task/Policy examples are used as pure input fixtures, not Agent
// credentials. The session/held owner/RB1 and HTTP methods below are real;
// actual provider/environment probing and dispatch are deliberately not run.
func repositoryTaskTemplate(t *testing.T, fixture publicFixedDeliveryInputs) []byte {
	t.Helper()
	var inputs goal.TeamInputs
	if json.Unmarshal(repositoryTeamRequest(t, fixture).Inputs, &inputs) != nil {
		t.Fatal("team fixture")
	}
	inputs.Limits = goal.Guardrails{MaxNodes: 3, MaxDepth: 3, MaxFanOut: 2, MaxConcurrentNodes: 2, MaxPlanRevisions: 1, MaxTotalRuns: 3, MaxTotalAttempts: 3, MaxWallTimeSeconds: 60000, MaxComputeUnits: 100, MaxTokens: 1000000, MaxArtifactBytes: 6 << 30}
	inputs.AdmissionPolicy.Paths = planning.OrderQuotePaths("integration")
	load := func(name string) map[string]any {
		raw, err := os.ReadFile(filepath.Join("..", "..", "schemas", "examples", "happy-path", name))
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if json.Unmarshal(raw, &doc) != nil {
			t.Fatal("fixture JSON")
		}
		return doc
	}
	for index := range inputs.Proposal.Nodes {
		node := &inputs.Proposal.Nodes[index]
		node.Paths = planning.OrderQuotePaths(node.NodeId)
		node.Estimate = goal.NodeEstimate{Runs: 1, Attempts: 1, WallTimeSeconds: 10000, ArtifactBytes: 1 << 30}
		taskID, runID, err := goal.TeamNodeIDs(inputs.Proposal, node.NodeId)
		if err != nil {
			t.Fatal(err)
		}
		task := load("task-spec.json")
		task["metadata"].(map[string]any)["id"] = taskID
		task["repository"] = map[string]any{"path": fixture.repository, "baseRef": inputs.BaseSHA, "remote": "origin", "expectedRemoteUrl": "https://example.invalid/team.git"}
		task["admission"] = map[string]any{"status": "executable"}
		worker := task["worker"].(map[string]any)
		worker["preferredAdapter"] = "pi"
		worker["fallbackAdapters"] = []string{}
		worker["sessionPolicy"] = "ephemeral"
		worker["model"] = "openai/fixture"
		scope := task["scope"].(map[string]any)
		scope["allowPaths"] = node.Paths
		scope["denyPaths"] = []string{}
		scope["maxChangedFiles"] = len(node.Paths)
		task["work"].(map[string]any)["context"] = []string{"固定契约，用户上下文不得替换验收"}
		deliverables := []domain.TaskDeliverable{}
		for _, path := range node.Paths {
			deliverables = append(deliverables, domain.TaskDeliverable{ID: strings.ReplaceAll(path, ".", "-"), Kind: "code", Required: true, PathGlob: path, MinimumCount: 1, MediaType: "text/plain"})
		}
		task["deliverables"] = deliverables
		task["acceptance"] = domain.TaskAcceptance{Commands: []domain.TaskCommand{planning.OrderQuoteOracleCommand(fixture.repository, node.NodeId)}}
		budgets := task["budgets"].(map[string]any)
		budgets["maxAttempts"] = 1
		budgets["maxOperationalRetries"] = 0
		budgets["maxReworkRounds"] = 0
		publication := task["publication"].(map[string]any)
		publication["required"] = false
		publication["provider"] = "none"
		publication["mode"] = "none"
		publication["requiredChecks"] = []string{}
		policy := load("policy-snapshot.json")
		policy["taskId"], policy["runId"] = taskID, runID
		effective := policy["effective"].(map[string]any)
		effective["allowFallbackWorkers"] = false
		effective["allowPublication"] = false
		effective["allowMerge"] = false
		effective["allowedAdapters"] = []string{"pi"}
		policy["policyDigest"] = ""
		policy["policyDigest"] = canonical.DigestBytes(repositoryTaskJSON(t, policy))
		inputs.Nodes[index].Task = repositoryTaskJSON(t, task)
		inputs.Nodes[index].Policy = repositoryTaskJSON(t, policy)
	}
	raw := repositoryTaskJSON(t, inputs)
	validator, err := contract.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := planning.OpenTaskTemplate(raw, validator); err != nil {
		t.Fatalf("complete template fixture: %v", err)
	}
	return raw
}

func TestRepositoryTaskHTTPHeldOwnerColdReplay(t *testing.T) {
	ctx := context.Background()
	fixture := newPublicFixedDeliveryInputs(t)
	ns := authority.AuthorityNamespaceId{TenantNamespace: "local", ControlPlaneId: "default", AuthorityScopeId: fixture.repository}
	digest, err := ns.Digest()
	if err != nil {
		t.Fatal(err)
	}
	fixture.inputs.Acquisition.Scope.AuthorityNamespaceID = ns
	fixture.inputs.Acquisition.Scope.RepositoryIdentityDigest = digest
	templateInputs := repositoryTaskTemplate(t, fixture)
	validator, err := contract.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	fixture.inputs.TaskTemplate, err = planning.OpenTaskTemplate(templateInputs, validator)
	if err != nil {
		t.Fatal(err)
	}
	preflights := 0
	deny := false
	fixture.inputs.TeamInputPreflight = func(raw []byte) error {
		preflights++
		if deny {
			return errors.New("fixture preflight denied")
		}
		_, err := planning.PreviewTeamInputs(raw, validator)
		return err
	}
	session, err := OpenRepositorySession(ctx, fixture.inputs)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	makeHandler := func(s *RepositorySession) (*taskhttp.Handler, *FixedEndpointAuthority) {
		t.Helper()
		a, err := s.OpenFixedEndpointAuthority(ctx)
		if err != nil {
			t.Fatal(err)
		}
		h, err := taskhttp.NewHandler(taskhttp.HandlerConfig{Application: s, Host: "127.0.0.1:1234", Token: strings.Repeat("1", 64), Recheck: a.Recheck, Mutation: func(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) }})
		if err != nil {
			t.Fatal(err)
		}
		return h, a
	}
	h, endpoint := makeHandler(session)
	t.Cleanup(func() { _ = endpoint.Close() })
	call := func(handler *taskhttp.Handler, method, path, key string, body any) *httptest.ResponseRecorder {
		t.Helper()
		var raw []byte
		if body != nil {
			raw = repositoryTaskJSON(t, body)
		}
		r := httptest.NewRequest(method, path, bytes.NewReader(raw))
		r.Host = "127.0.0.1:1234"
		r.Header.Set("Authorization", "Bearer "+strings.Repeat("1", 64))
		r.Header.Set("Content-Type", "application/json")
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	submission := goal.TaskSubmission{Template: goal.TaskTemplateOrderQuote, Intent: "构建订单报价 API 和客户端", Context: goal.TaskContext{Text: "保持固定接口与独立验收"}}
	installed := session.taskTemplate
	for _, tc := range []struct {
		name string
		port application.TaskTemplatePort
	}{
		{"missing", nil},
		{"failed", repositoryTaskFailingProducer{TaskTemplatePort: installed, err: errors.New("producer failed")}},
		{"malformed", repositoryTaskFailingProducer{TaskTemplatePort: installed, raw: []byte(`{"not":"team inputs"}`)}},
	} {
		t.Run("producer-"+tc.name, func(t *testing.T) {
			session.taskTemplate = tc.port
			if _, err := session.CreateTask(ctx, application.CreateTaskRequest{IdempotencyKey: "bad-" + tc.name, Submission: submission}); err == nil {
				t.Fatal("invalid producer created a draft")
			}
			ids, err := session.ingress.ListTaskDraftIDs(session.acquisition.Scope, "", 20)
			if err != nil || len(ids) != 0 {
				t.Fatal("failed producer mutated draft authority")
			}
		})
	}
	session.taskTemplate = installed
	preflights = 0
	w := call(h, http.MethodPost, "/v1/tasks", "create-order", submission)
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var draft application.TaskProjection
	if json.Unmarshal(w.Body.Bytes(), &draft) != nil || draft.ID == "" || draft.PreviewDigest == "" || draft.Status != "awaiting-confirmation" || len(draft.Workers) != 3 || len(draft.Edges) != 2 {
		t.Fatal("incomplete public preview")
	}
	if _, found, err := session.ingress.ReadTeamPlan(session.acquisition.Scope, draft.ID); err != nil || found {
		t.Fatal("draft created approved authority")
	}
	if obligations, err := session.ingress.ListTeamCreationObligations(session.acquisition.Scope); err != nil || len(obligations) != 0 {
		t.Fatal("draft materialized Runs")
	}
	if preflights != 1 {
		t.Fatal("missing initial preflight")
	}
	// Even an environment failure after response loss may not alter a draft.
	deny = true
	w = call(h, http.MethodPost, "/v1/tasks", "create-order", submission)
	if w.Code != 201 || preflights != 1 {
		t.Fatal("same creation retried environment")
	}
	deny = false
	changed := submission
	changed.Intent = "改变已冻结请求"
	if w := call(h, http.MethodPost, "/v1/tasks", "create-order", changed); w.Code != 409 {
		t.Fatalf("same key conflict: %d", w.Code)
	}
	approve := application.ApproveTaskRequest{ExpectedRevision: draft.Revision, PreviewDigest: draft.PreviewDigest}
	bad := approve
	bad.PreviewDigest = canonical.DigestBytes([]byte("stale"))
	if w := call(h, http.MethodPost, "/v1/tasks/"+draft.ID+"/approve", "approve-order", bad); w.Code != 409 {
		t.Fatalf("stale preview: %d", w.Code)
	}
	bad = approve
	bad.ExpectedRevision++
	if w := call(h, http.MethodPost, "/v1/tasks/"+draft.ID+"/approve", "approve-order", bad); w.Code != 409 {
		t.Fatalf("wrong revision: %d", w.Code)
	}
	w = call(h, http.MethodPost, "/v1/tasks/"+draft.ID+"/approve", "approve-order", approve)
	if w.Code != 202 {
		t.Fatalf("confirm: %d %s", w.Code, w.Body.String())
	}
	plan, found, err := session.ingress.ReadTeamPlan(session.acquisition.Scope, draft.ID)
	if err != nil || !found || len(plan.Materializations) != 3 || plan.Approval.TaskDraftDigest != draft.PreviewDigest {
		t.Fatal("HTTP did not reach existing accepted plan")
	}
	if preflights != 2 {
		t.Fatal("exact confirmation did not preflight once")
	}
	deny = true
	w = call(h, http.MethodPost, "/v1/tasks/"+draft.ID+"/approve", "approve-order", approve)
	if w.Code != 202 || preflights != 2 {
		t.Fatal("accepted replay reprobed or reset")
	}
	if w := call(h, http.MethodPost, "/v1/tasks/"+draft.ID+"/approve", "different-key", approve); w.Code != 409 {
		t.Fatalf("changed confirmation key: %d", w.Code)
	}
	for _, path := range []string{"/v1/tasks", "/v1/tasks/" + draft.ID, "/v1/tasks/" + draft.ID + "/graph", "/v1/tasks/" + draft.ID + "/workers"} {
		if w := call(h, http.MethodGet, path, "", nil); w.Code != 200 {
			t.Fatalf("query %s: %d", path, w.Code)
		}
	}
	if err := endpoint.Close(); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if w := call(h, http.MethodGet, "/v1/tasks/"+draft.ID, "", nil); w.Code != 503 {
		t.Fatal("closed owner remained usable")
	}
	// A restart may disable the submission template without preventing exact
	// existing Task/approval replay or replacing its original preview.
	fixture.inputs.TaskTemplate = planning.TaskTemplate{}
	session, err = OpenRepositorySession(ctx, fixture.inputs)
	if err != nil {
		t.Fatal(err)
	}
	h, endpoint = makeHandler(session)
	w = call(h, http.MethodPost, "/v1/tasks/"+draft.ID+"/approve", "approve-order", approve)
	if w.Code != 202 || preflights != 2 {
		t.Fatalf("cold confirm replay: %d", w.Code)
	}
	again, found, err := session.ingress.ReadTeamPlan(session.acquisition.Scope, draft.ID)
	if err != nil || !found || again.FactDigest != plan.FactDigest {
		t.Fatal("cold replay created another authority")
	}
	var projection application.TaskProjection
	if json.Unmarshal(w.Body.Bytes(), &projection) != nil || projection.Status != "approved" || projection.Outcome != nil || projection.Delivery != nil {
		t.Fatal("approval falsely claimed delivery")
	}
}
