package github

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/port"
)

const (
	testRepository   = "example-org/example-repo"
	testRemoteURL    = "https://github.com/example-org/example-repo.git"
	testBaseSHA      = "0123456789abcdef0123456789abcdef01234567"
	testCommitSHA    = "2222222222222222222222222222222222222222"
	testForeignSHA   = "9999999999999999999999999999999999999999"
	testHeadBranch   = "marshal/ENG-123-a1b2c3d4e5f6"
	testMarker       = "<!-- marshal task=ENG-123 run=run-01 -->"
	testPRID         = "PR_kw0000000001"
	testPRURL        = "https://github.com/example-org/example-repo/pull/7"
	testRepositoryID = "R_repository0001"
	testActor        = "marshal-bot"
	testMergeSHA     = "4444444444444444444444444444444444444444"
	testHeadTree     = "5555555555555555555555555555555555555555"
	testOtherTree    = "6666666666666666666666666666666666666666"
)

var fixedTime = time.Date(2026, 8, 4, 12, 30, 0, 0, time.UTC)

// fakeGitScript emulates the two git commands the publisher runs. Remote
// branch state lives in the harness state directory so tests can observe and
// mutate it between invocations.
const fakeGitScript = `#!/bin/sh
set -u
STATE='@STATE@'
printf '%s\n' "$*" >> "$STATE/git.log"
{ printf -- '--- invocation\n'; env; } >> "$STATE/git.env"
if [ -n "${GIT_ASKPASS:-}" ] && [ -f "$GIT_ASKPASS" ]; then
	cp "$GIT_ASKPASS" "$STATE/askpass-copy"
fi
case "$1" in
ls-remote)
	if [ -s "$STATE/remote-head" ]; then
		printf '%s\t%s\n' "$(cat "$STATE/remote-head")" "$4"
	fi
	exit 0
	;;
push)
	refspec=''
	for arg in "$@"; do refspec="$arg"; done
	sha="${refspec%%:*}"
	mode='ok'
	if [ -s "$STATE/push-mode" ]; then mode="$(cat "$STATE/push-mode")"; fi
	case "$mode" in
	ok)
		printf '%s' "$sha" > "$STATE/remote-head"
		exit 0
		;;
	fail-delivered)
		printf '%s' "$sha" > "$STATE/remote-head"
		printf 'error: RPC failed; fake transport interrupted\n' >&2
		exit 1
		;;
	fail)
		if [ -s "$STATE/error-output" ]; then cat "$STATE/error-output" >&2; else printf 'error: fake push rejected\n' >&2; fi
		exit 1
		;;
	esac
	;;
esac
printf 'unexpected git invocation\n' >&2
exit 1
`

// fakeGHScript emulates the gh CLI subset the publisher uses. It logs every
// invocation and environment so tests can assert side effects and credential
// isolation.
const fakeGHScript = `#!/bin/sh
set -u
STATE='@STATE@'
printf '%s\n' "$*" >> "$STATE/gh.log"
{ printf -- '--- invocation\n'; env; } >> "$STATE/gh.env"
case "$1" in
api)
	case "$2" in
	repos/*/commits/*)
		sha="${2##*/}"
		if [ -f "$STATE/commit-$sha.json" ]; then
			cat "$STATE/commit-$sha.json"
			exit 0
		fi
		exit 1
		;;
	repos/*) cat "$STATE/repo.json" ;;
	user) cat "$STATE/user.json" ;;
	*) exit 1 ;;
	esac
	exit 0
	;;
auth)
	printf 'fake-gh-token-000000\n'
	exit 0
	;;
pr)
	case "$2" in
	list)
		if [ -f "$STATE/created" ]; then
			printf '['
			cat "$STATE/pr.json"
			printf ']\n'
		else
			cat "$STATE/pr-list.json"
		fi
		exit 0
		;;
	view)
		count=0
		if [ -f "$STATE/view-count" ]; then count="$(cat "$STATE/view-count")"; fi
		count=$((count + 1))
		printf '%s' "$count" > "$STATE/view-count"
		if [ "$count" -ge 2 ] && [ -f "$STATE/pr-changed.json" ]; then
			cat "$STATE/pr-changed.json"
		else
			cat "$STATE/pr.json"
		fi
		exit 0
		;;
	edit)
		exit 0
		;;
	create)
		if [ -f "$STATE/create-fail" ]; then
			printf 'error: fake pr create failure\n' >&2
			exit 1
		fi
		touch "$STATE/created"
		if [ -f "$STATE/create-error" ]; then
			printf 'error: fake transient create failure\n' >&2
			exit 1
		fi
		cat "$STATE/pr.json"
		exit 0
		;;
	checks)
		cat "$STATE/checks.json"
		if [ -s "$STATE/checks-exit" ]; then
			exit "$(cat "$STATE/checks-exit")"
		fi
		exit 0
		;;
	esac
	;;
esac
printf 'unexpected gh invocation\n' >&2
exit 1
`

type harness struct {
	t         *testing.T
	stateDir  string
	configDir string
	repoRoot  string
	publisher *Publisher
	validator *contract.Validator
	secrets   []string
}

var sharedValidator = sync.OnceValues(func() (*contract.Validator, error) { return contract.NewValidator() })

func newHarness(t *testing.T) *harness {
	t.Helper()
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	binDir := filepath.Join(root, "bin")
	configDir := filepath.Join(root, "gh-config")
	repoRoot := filepath.Join(root, "repository")
	for _, dir := range []string{stateDir, binDir, configDir, repoRoot} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Publisher.New normalizes repositoryRoot through filepath.EvalSymlinks.
	// Normalize the harness root identically so every derivation site (and
	// the expectations below) sees one single repository identity even on
	// platforms where the temp directory traverses symlinks (macOS /var ->
	// /private/var); otherwise the authority namespace digest would differ
	// between harness expectation and implementation on the same machine.
	evaluatedRepoRoot, err := filepath.EvalSymlinks(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	repoRoot = evaluatedRepoRoot
	writeScript := func(name, script string) string {
		path := filepath.Join(binDir, name)
		if err := os.WriteFile(path, []byte(strings.ReplaceAll(script, "@STATE@", stateDir)), 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}
	writeScript("git", fakeGitScript)
	ghScript := writeScript("gh", fakeGHScript)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	validator, err := sharedValidator()
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := New(ghScript, configDir, repoRoot, validator)
	if err != nil {
		t.Fatal(err)
	}
	publisher.now = func() time.Time { return fixedTime }
	publisher.reconcileDelay = 0
	h := &harness{t: t, stateDir: stateDir, configDir: configDir, repoRoot: repoRoot, publisher: publisher, validator: validator}
	h.writeState("repo.json", `{"node_id":"`+testRepositoryID+`","full_name":"`+testRepository+`","html_url":"https://github.com/`+testRepository+`"}`)
	h.writeState("user.json", `{"login":"`+testActor+`"}`)
	h.writeState("pr-list.json", "[]")
	return h
}

func (h *harness) writeState(name, content string) {
	h.t.Helper()
	if err := os.WriteFile(filepath.Join(h.stateDir, name), []byte(content), 0o600); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) readState(name string) string {
	h.t.Helper()
	data, err := os.ReadFile(filepath.Join(h.stateDir, name))
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		h.t.Fatal(err)
	}
	return string(data)
}

func (h *harness) commandLines(binary string) []string {
	content := h.readState(binary + ".log")
	if content == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(content, "\n"), "\n")
}

func (h *harness) countCommands(binary, prefix string) int {
	count := 0
	for _, line := range h.commandLines(binary) {
		if strings.HasPrefix(line, prefix) {
			count++
		}
	}
	return count
}

func (h *harness) envDump(binary string) string {
	return h.readState(binary + ".env")
}

func (h *harness) setPublisherSecrets() {
	h.t.Helper()
	secrets := map[string]string{
		"GITHUB_TOKEN":          "test-secret-github-token",
		"GH_TOKEN":              "test-secret-gh-token",
		"GITHUB_APP_TOKEN":      "test-secret-app-token",
		"GH_ENTERPRISE_TOKEN":   "test-secret-enterprise-token",
		"AWS_SECRET_ACCESS_KEY": "test-secret-aws-key",
		"GITLAB_TOKEN":          "test-secret-gitlab-token",
	}
	for key, value := range secrets {
		h.t.Setenv(key, value)
		h.secrets = append(h.secrets, key, value)
	}
	h.t.Setenv("GIT_ASKPASS", "/attacker-controlled/askpass")
	h.t.Setenv("GIT_CONFIG_GLOBAL", "/attacker-controlled/gitconfig")
	h.t.Setenv("GH_CONFIG_DIR", "/attacker-controlled/gh-config")
	h.secrets = append(h.secrets, "test-secret-gitlab-token", "/attacker-controlled")
}

func (h *harness) assertNoSecretTokens(label, value string) {
	h.t.Helper()
	for _, secret := range h.secrets {
		if strings.Contains(value, secret) {
			h.t.Fatalf("%s leaked sensitive value %q", label, secret)
		}
	}
}

func (h *harness) assertNoSecrets(label, value string) {
	h.t.Helper()
	h.assertNoSecretTokens(label, value)
	if strings.Contains(value, h.publisher.configDir) || strings.Contains(value, h.configDir) {
		h.t.Fatalf("%s leaked GitHub config dir", label)
	}
	if strings.Contains(value, h.publisher.repositoryRoot) || strings.Contains(value, h.repoRoot) {
		h.t.Fatalf("%s leaked repository root", label)
	}
}

func (h *harness) seedPR(intent domain.PublicationIntent, mutate func(map[string]any)) {
	h.t.Helper()
	pr := map[string]any{
		"id":                  testPRID,
		"number":              7,
		"url":                 testPRURL,
		"isDraft":             true,
		"state":               "OPEN",
		"headRefName":         intent.HeadBranch,
		"headRefOid":          intent.CommitSHA,
		"headRepositoryOwner": map[string]any{"login": "example-org"},
		"isCrossRepository":   false,
		"baseRefName":         intent.BaseBranch,
		"body":                renderBody(intent),
	}
	if mutate != nil {
		mutate(pr)
	}
	data, err := json.Marshal(pr)
	if err != nil {
		h.t.Fatal(err)
	}
	h.writeState("pr.json", string(data))
}

func (h *harness) publish(intent domain.PublicationIntent) (domain.Record, error) {
	h.t.Helper()
	data, err := json.Marshal(intent)
	if err != nil {
		h.t.Fatal(err)
	}
	if err := h.validator.Validate(domain.KindPublicationIntent, data); err != nil {
		h.t.Fatalf("test intent failed schema validation: %v", err)
	}
	return h.publisher.Publish(context.Background(), domain.Record{Kind: domain.KindPublicationIntent, Data: data})
}

func testIntent() domain.PublicationIntent {
	digest := func(fill string) string { return "sha256:" + strings.Repeat(fill, 64) }
	return domain.PublicationIntent{
		APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindPublicationIntent,
		TaskID: "ENG-123", RunID: "run-01",
		Provider: domain.PublicationProviderGitHub, Repository: testRepository, Remote: "origin", RemoteURL: testRemoteURL,
		BaseBranch: "main", HeadBranch: testHeadBranch, BaseSHA: testBaseSHA, CommitSHA: testCommitSHA,
		ReviewRound:    1,
		SnapshotDigest: digest("4"), DiffDigest: digest("5"), SpecDigest: digest("d"), PolicyDigest: digest("c"),
		EvidenceDigest: digest("3"), VerificationDigest: digest("1"), ReviewDecisionDigest: digest("6"),
		Marker: testMarker, Mode: domain.PublicationModeDraft, MergePolicy: domain.MergePolicyNever,
		Summary: "Add marshal demo", CreatedAt: fixedTime,
	}
}

func decodePublication(t *testing.T, record domain.Record) domain.PublicationRecord {
	t.Helper()
	if record.Kind != domain.KindPublicationRecord {
		t.Fatalf("record kind = %s", record.Kind)
	}
	var published domain.PublicationRecord
	if err := json.Unmarshal(record.Data, &published); err != nil {
		t.Fatal(err)
	}
	return published
}

func TestPublishCreatesDraftPRAndIsIdempotent(t *testing.T) {
	h := newHarness(t)
	intent := testIntent()
	h.seedPR(intent, nil)

	record, err := h.publish(intent)
	if err != nil {
		t.Fatalf("first publish: %v", err)
	}
	published := decodePublication(t, record)
	if published.Repository.ID != testRepositoryID || published.Repository.NameWithOwner != testRepository {
		t.Fatalf("repository binding = %+v", published.Repository)
	}
	if published.Request.ID != testPRID || published.Request.Number != 7 || published.Request.URL != testPRURL || !published.Request.Draft || published.Request.State != "OPEN" {
		t.Fatalf("PR record = %+v", published.Request)
	}
	if published.HeadSHA != testCommitSHA || published.CommitSHA != testCommitSHA || published.HeadBranch != testHeadBranch || published.BaseBranch != "main" {
		t.Fatalf("head/base binding = %+v", published)
	}
	if published.Marker != testMarker || published.Mode != domain.PublicationModeDraft || published.MergePolicy != domain.MergePolicyNever {
		t.Fatalf("marker/mode binding = %+v", published)
	}
	if published.PolicyDigest != intent.PolicyDigest || published.VerificationDigest != intent.VerificationDigest || published.ReviewDecisionDigest != intent.ReviewDecisionDigest {
		t.Fatalf("publication evidence binding = %+v", published)
	}
	if published.Actor != testActor || !published.PublishedAt.Equal(fixedTime) || !published.UpdatedAt.Equal(fixedTime) {
		t.Fatalf("actor/time binding = %+v", published)
	}
	if h.countCommands("git", "ls-remote ") != 1 || h.countCommands("git", "push ") != 1 {
		t.Fatalf("git commands = %v", h.commandLines("git"))
	}
	if h.countCommands("gh", "pr create ") != 1 || h.countCommands("gh", "pr view ") != 1 || h.countCommands("gh", "pr edit ") != 0 {
		t.Fatalf("gh commands = %v", h.commandLines("gh"))
	}
	if head := h.readState("remote-head"); head != testCommitSHA {
		t.Fatalf("remote head = %q", head)
	}
	if pushLine := h.commandLines("git")[1]; !strings.Contains(pushLine, "--no-force") || !strings.Contains(pushLine, testCommitSHA+":refs/heads/"+testHeadBranch) {
		t.Fatalf("push command = %q", pushLine)
	}
	h.assertNoSecrets("PublicationRecord", string(record.Data))

	record2, err := h.publish(intent)
	if err != nil {
		t.Fatalf("second publish: %v", err)
	}
	again := decodePublication(t, record2)
	if again.Request.ID != testPRID || again.Request.Number != 7 {
		t.Fatalf("second publish created a different PR: %+v", again.Request)
	}
	if h.countCommands("git", "push ") != 1 {
		t.Fatalf("idempotent rerun pushed again: %v", h.commandLines("git"))
	}
	if h.countCommands("git", "ls-remote ") != 2 {
		t.Fatalf("expected a ls-remote observation per publish: %v", h.commandLines("git"))
	}
	if h.countCommands("gh", "pr create ") != 1 || h.countCommands("gh", "pr edit ") != 1 || h.countCommands("gh", "pr view ") != 2 {
		t.Fatalf("idempotent rerun gh commands = %v", h.commandLines("gh"))
	}
}

func TestPublishRejectsUnexpectedRemoteHead(t *testing.T) {
	h := newHarness(t)
	intent := testIntent()
	h.seedPR(intent, nil)
	h.writeState("remote-head", testForeignSHA)

	_, err := h.publish(intent)
	if err == nil {
		t.Fatal("expected permanent failure for unexpected remote head")
	}
	if !port.IsPermanent(err) {
		t.Fatalf("error must be permanent, got: %v", err)
	}
	if !strings.Contains(err.Error(), testForeignSHA) {
		t.Fatalf("error should name the foreign head: %v", err)
	}
	if h.countCommands("git", "push ") != 0 {
		t.Fatalf("publisher must not push over a foreign branch: %v", h.commandLines("git"))
	}
	for _, line := range h.commandLines("gh") {
		if strings.HasPrefix(line, "pr ") {
			t.Fatalf("publisher must not touch PRs after foreign head: %q", line)
		}
	}
}

func TestPublishResolvesAmbiguousPushByObservingRemote(t *testing.T) {
	t.Run("delivered despite command failure", func(t *testing.T) {
		h := newHarness(t)
		intent := testIntent()
		h.seedPR(intent, nil)
		h.writeState("push-mode", "fail-delivered")

		record, err := h.publish(intent)
		if err != nil {
			t.Fatalf("reconciled publish should succeed: %v", err)
		}
		published := decodePublication(t, record)
		if published.Request.ID != testPRID || published.HeadSHA != testCommitSHA {
			t.Fatalf("publication = %+v", published)
		}
		if h.countCommands("git", "push ") != 1 {
			t.Fatalf("push must not be retried blindly: %v", h.commandLines("git"))
		}
		if h.countCommands("git", "ls-remote ") != 2 {
			t.Fatalf("expected reconcile observation: %v", h.commandLines("git"))
		}
		if h.countCommands("gh", "pr create ") != 1 {
			t.Fatalf("gh commands = %v", h.commandLines("gh"))
		}
	})

	t.Run("not delivered stays ambiguous", func(t *testing.T) {
		h := newHarness(t)
		intent := testIntent()
		h.seedPR(intent, nil)
		h.writeState("push-mode", "fail")

		_, err := h.publish(intent)
		if err == nil || !strings.Contains(err.Error(), "ambiguous push") {
			t.Fatalf("expected ambiguous push error, got: %v", err)
		}
		if port.IsPermanent(err) {
			t.Fatalf("ambiguous push must remain retryable: %v", err)
		}
		if h.readState("remote-head") != "" {
			t.Fatal("remote observation must disagree with delivered push")
		}
		if h.countCommands("git", "push ") != 1 || h.countCommands("git", "ls-remote ") != 2 {
			t.Fatalf("git commands = %v", h.commandLines("git"))
		}
		if h.countCommands("gh", "pr create ") != 0 {
			t.Fatalf("no PR may be created after ambiguous push: %v", h.commandLines("gh"))
		}
	})
}

func TestPublishFastForwardsExistingMarshalGeneration(t *testing.T) {
	h := newHarness(t)
	intent := testIntent()
	intent.ReviewRound = 2
	intent.PreviousHeadSHA = testCommitSHA
	intent.CommitSHA = strings.Repeat("3", 40)
	h.writeState("remote-head", intent.PreviousHeadSHA)
	h.writeState("created", "")
	h.seedPR(intent, nil)

	record, err := h.publish(intent)
	if err != nil {
		t.Fatal(err)
	}
	published := decodePublication(t, record)
	if published.PreviousHeadSHA != testCommitSHA || published.HeadSHA != intent.CommitSHA || published.ReviewRound != 2 {
		t.Fatalf("publication generation = %+v", published)
	}
	if h.countCommands("git", "push ") != 1 || h.countCommands("gh", "pr edit ") != 1 || h.countCommands("gh", "pr create ") != 0 {
		t.Fatalf("commands: git=%v gh=%v", h.commandLines("git"), h.commandLines("gh"))
	}
	if h.readState("remote-head") != intent.CommitSHA {
		t.Fatal("remote branch was not fast-forwarded to the new generation")
	}
}

func TestPublishReconcilesExistingPRs(t *testing.T) {
	t.Run("create command failed but PR exists", func(t *testing.T) {
		h := newHarness(t)
		intent := testIntent()
		h.seedPR(intent, nil)
		h.writeState("create-error", "")

		record, err := h.publish(intent)
		if err != nil {
			t.Fatalf("creation must reconcile against the remote PR list: %v", err)
		}
		published := decodePublication(t, record)
		if published.Request.ID != testPRID {
			t.Fatalf("publication = %+v", published.Request)
		}
		if h.countCommands("gh", "pr create ") != 1 {
			t.Fatalf("gh commands = %v", h.commandLines("gh"))
		}

		if _, err := h.publish(intent); err != nil {
			t.Fatalf("rerun must reuse the reconciled PR: %v", err)
		}
		if h.countCommands("gh", "pr create ") != 1 || h.countCommands("gh", "pr edit ") != 1 {
			t.Fatalf("rerun duplicated the PR: %v", h.commandLines("gh"))
		}
	})

	t.Run("create failure without PR stays ambiguous", func(t *testing.T) {
		h := newHarness(t)
		intent := testIntent()
		h.seedPR(intent, nil)
		h.writeState("create-fail", "")

		_, err := h.publish(intent)
		if err == nil || !strings.Contains(err.Error(), "ambiguous PR creation") {
			t.Fatalf("expected ambiguous creation error, got: %v", err)
		}
		if h.countCommands("gh", "pr create ") != 1 || h.countCommands("gh", "pr view ") != 0 {
			t.Fatalf("gh commands = %v", h.commandLines("gh"))
		}
	})

	t.Run("multiple matching PRs block", func(t *testing.T) {
		h := newHarness(t)
		intent := testIntent()
		base := map[string]any{"id": testPRID, "number": 7, "url": testPRURL, "isDraft": true, "state": "OPEN", "headRefName": intent.HeadBranch, "headRefOid": intent.CommitSHA, "headRepositoryOwner": map[string]any{"login": "example-org"}, "baseRefName": intent.BaseBranch, "body": renderBody(intent)}
		second := map[string]any{"id": "PR_kw0000000002", "number": 8, "url": testPRURL, "isDraft": true, "state": "OPEN", "headRefName": intent.HeadBranch, "headRefOid": intent.CommitSHA, "headRepositoryOwner": map[string]any{"login": "example-org"}, "baseRefName": intent.BaseBranch, "body": renderBody(intent)}
		data, err := json.Marshal([]map[string]any{base, second})
		if err != nil {
			t.Fatal(err)
		}
		h.writeState("pr-list.json", string(data))

		_, err = h.publish(intent)
		if err == nil || !port.IsPermanent(err) || !strings.Contains(err.Error(), "multiple PRs") {
			t.Fatalf("expected permanent block for duplicate PRs, got: %v", err)
		}
		if h.countCommands("gh", "pr create ") != 0 {
			t.Fatalf("publisher must not create a third PR: %v", h.commandLines("gh"))
		}
	})

	t.Run("branch owned by someone else blocks", func(t *testing.T) {
		h := newHarness(t)
		intent := testIntent()
		foreign := map[string]any{"id": testPRID, "number": 7, "url": testPRURL, "isDraft": true, "state": "OPEN", "headRefName": intent.HeadBranch, "headRefOid": intent.CommitSHA, "headRepositoryOwner": map[string]any{"login": "example-org"}, "baseRefName": intent.BaseBranch, "body": "hand-crafted PR without a Marshal marker"}
		data, err := json.Marshal([]map[string]any{foreign})
		if err != nil {
			t.Fatal(err)
		}
		h.writeState("pr-list.json", string(data))

		_, err = h.publish(intent)
		if err == nil || !port.IsPermanent(err) || !strings.Contains(err.Error(), "not owned") {
			t.Fatalf("expected marker ownership block, got: %v", err)
		}
		if h.countCommands("gh", "pr create ") != 0 || h.countCommands("gh", "pr edit ") != 0 {
			t.Fatalf("publisher must not modify foreign PRs: %v", h.commandLines("gh"))
		}
	})

	t.Run("existing PR not an open draft blocks", func(t *testing.T) {
		h := newHarness(t)
		intent := testIntent()
		h.seedPR(intent, func(pr map[string]any) { pr["state"] = "CLOSED" })
		data, err := os.ReadFile(filepath.Join(h.stateDir, "pr.json"))
		if err != nil {
			t.Fatal(err)
		}
		h.writeState("pr-list.json", "["+string(data)+"]")

		_, err = h.publish(intent)
		if err == nil || !port.IsPermanent(err) || !strings.Contains(err.Error(), "not an open draft") {
			t.Fatalf("expected closed-draft block, got: %v", err)
		}
		if h.countCommands("gh", "pr create ") != 0 || h.countCommands("gh", "pr edit ") != 0 {
			t.Fatalf("gh commands = %v", h.commandLines("gh"))
		}
	})

	t.Run("matching branch from a fork blocks before edit", func(t *testing.T) {
		h := newHarness(t)
		intent := testIntent()
		h.seedPR(intent, func(pr map[string]any) {
			pr["isCrossRepository"] = true
			pr["headRepositoryOwner"] = map[string]any{"login": "attacker"}
		})
		data, err := os.ReadFile(filepath.Join(h.stateDir, "pr.json"))
		if err != nil {
			t.Fatal(err)
		}
		h.writeState("pr-list.json", "["+string(data)+"]")

		_, err = h.publish(intent)
		if err == nil || !port.IsPermanent(err) || !strings.Contains(err.Error(), "head repository") {
			t.Fatalf("expected foreign head repository block, got: %v", err)
		}
		if h.countCommands("gh", "pr edit ") != 0 || h.countCommands("gh", "pr create ") != 0 {
			t.Fatalf("publisher modified a foreign PR: %v", h.commandLines("gh"))
		}
	})
}

func TestParseRepositoryURLStrict(t *testing.T) {
	valid := map[string]string{
		"https://github.com/example-org/example-repo":     "example-org/example-repo",
		"https://github.com/example-org/example-repo.git": "example-org/example-repo",
		"https://github.com/OWNER/Repo.Name-1":            "OWNER/Repo.Name-1",
	}
	for remote, expected := range valid {
		repository, err := ParseRepositoryURL(remote)
		if err != nil || repository != expected {
			t.Fatalf("ParseRepositoryURL(%q) = %q, %v", remote, repository, err)
		}
	}
	for _, remote := range []string{
		"",
		"git@github.com:example-org/example-repo.git",
		"ssh://git@github.com/example-org/example-repo.git",
		"http://github.com/example-org/example-repo",
		"https://gitlab.com/example-org/example-repo",
		"https://example.com/example-org/example-repo",
		"https://github.com/example-org/example-repo?ref=main",
		"https://github.com/example-org/example-repo#readme",
		"https://github.com/example-org",
		"https://github.com/example-org/",
		"https://github.com/example-org/example-repo/extra",
		"https://github.com//example-repo",
		"https://github.com/exa mple/repo",
	} {
		if repository, err := ParseRepositoryURL(remote); err == nil {
			t.Fatalf("ParseRepositoryURL(%q) = %q, expected rejection", remote, repository)
		}
	}
}

func TestPullRequestTitleIsBoundedByRunes(t *testing.T) {
	intent := testIntent()
	intent.Summary = strings.Repeat("发布", 200)
	value := title(intent)
	if len([]rune(value)) != 240 || !strings.HasPrefix(value, "发布") {
		t.Fatalf("bounded title has %d runes", len([]rune(value)))
	}
}

func TestPublishRejectsRepositoryIdentityMismatch(t *testing.T) {
	t.Run("remote URL differs from intent repository", func(t *testing.T) {
		h := newHarness(t)
		intent := testIntent()
		intent.RemoteURL = "https://github.com/other-owner/other-repo.git"
		h.seedPR(intent, nil)

		_, err := h.publish(intent)
		if err == nil || !strings.Contains(err.Error(), "does not match HTTPS GitHub remote") {
			t.Fatalf("expected remote mismatch rejection, got: %v", err)
		}
		if h.commandLines("gh") != nil || h.commandLines("git") != nil {
			t.Fatalf("no subprocess may run before identity validation: gh=%v git=%v", h.commandLines("gh"), h.commandLines("git"))
		}
	})

	t.Run("authenticated repository identity mismatch", func(t *testing.T) {
		h := newHarness(t)
		intent := testIntent()
		h.seedPR(intent, nil)
		h.writeState("repo.json", `{"node_id":"`+testRepositoryID+`","full_name":"example-org/surprise-repo","html_url":"https://github.com/example-org/surprise-repo"}`)

		_, err := h.publish(intent)
		if err == nil || !port.IsPermanent(err) || !strings.Contains(err.Error(), "authenticated repository identity mismatch") {
			t.Fatalf("expected permanent identity mismatch, got: %v", err)
		}
		if h.countCommands("git", "ls-remote ") != 0 || h.countCommands("gh", "pr ") != 0 {
			t.Fatalf("no remote side effects allowed: gh=%v git=%v", h.commandLines("gh"), h.commandLines("git"))
		}
	})
}

func TestPublishInputValidation(t *testing.T) {
	h := newHarness(t)
	intent := testIntent()

	if _, err := h.publisher.Publish(context.Background(), domain.Record{Kind: domain.KindPublicationRecord, Data: []byte("{}")}); err == nil {
		t.Fatal("expected kind rejection")
	}

	for name, mutate := range map[string]func(*domain.PublicationIntent){
		"mode=ready is schema-invalid":        func(i *domain.PublicationIntent) { i.Mode = "ready" },
		"mergePolicy=sometimes is rejected":   func(i *domain.PublicationIntent) { i.MergePolicy = "sometimes" },
		"non-github provider is rejected":     func(i *domain.PublicationIntent) { i.Provider = "gitlab" },
		"caller-supplied branch is rejected":  func(i *domain.PublicationIntent) { i.HeadBranch = "feature/anything-goes" },
		"marker without run binding rejected": func(i *domain.PublicationIntent) { i.Marker = "<!-- marshal task=ENG-123 -->" },
	} {
		mutated := intent
		mutate(&mutated)
		data, err := json.Marshal(mutated)
		if err != nil {
			t.Fatal(err)
		}
		if err := h.validator.Validate(domain.KindPublicationIntent, data); err == nil {
			t.Fatalf("%s: expected schema rejection", name)
		}
		if _, err := h.publisher.Publish(context.Background(), domain.Record{Kind: domain.KindPublicationIntent, Data: data}); err == nil {
			t.Fatalf("%s: expected publish rejection", name)
		}
	}
}

func TestObserveChecksBindsPRHeadAndClassifies(t *testing.T) {
	newPublished := func(t *testing.T, h *harness) domain.PublicationRecord {
		t.Helper()
		intent := testIntent()
		h.seedPR(intent, nil)
		record, err := h.publish(intent)
		if err != nil {
			t.Fatal(err)
		}
		return decodePublication(t, record)
	}
	required := []string{"build", "lint"}

	t.Run("all required checks pass", func(t *testing.T) {
		h := newHarness(t)
		published := newPublished(t, h)
		h.writeState("checks.json", checkRowsJSON(t, checkRow("build", "pass"), checkRow("lint", "pass"), checkRow("optional", "fail")))

		record, err := h.publisher.ObserveChecks(context.Background(), domain.Record{Kind: domain.KindPublicationRecord, Data: publicationData(t, h, published)}, required)
		if err != nil {
			t.Fatal(err)
		}
		var observation domain.RemoteCheckRecord
		if err := json.Unmarshal(record.Data, &observation); err != nil {
			t.Fatal(err)
		}
		if observation.Status != "pass" {
			t.Fatalf("overall status = %q", observation.Status)
		}
		if observation.RepositoryID != testRepositoryID || observation.RequestID != testPRID || observation.HeadSHA != testCommitSHA || observation.TaskID != "ENG-123" || observation.RunID != "run-01" || observation.Provider != "github" {
			t.Fatalf("observation binding = %+v", observation)
		}
		if !observation.ObservedAt.Equal(fixedTime) {
			t.Fatalf("observedAt = %v", observation.ObservedAt)
		}
		if len(observation.Checks) != 2 || observation.Checks[0].Name != "build" || observation.Checks[0].Status != "pass" || !observation.Checks[0].Required || observation.Checks[1].Status != "pass" {
			t.Fatalf("checks = %+v", observation.Checks)
		}
	})

	t.Run("pending check keeps pending and tolerates gh exit 8", func(t *testing.T) {
		h := newHarness(t)
		published := newPublished(t, h)
		h.writeState("checks.json", checkRowsJSON(t, checkRow("build", "pass"), checkRow("lint", "pending")))
		h.writeState("checks-exit", "8")

		record, err := h.publisher.ObserveChecks(context.Background(), domain.Record{Kind: domain.KindPublicationRecord, Data: publicationData(t, h, published)}, required)
		if err != nil {
			t.Fatal(err)
		}
		var observation domain.RemoteCheckRecord
		if err := json.Unmarshal(record.Data, &observation); err != nil {
			t.Fatal(err)
		}
		if observation.Status != "pending" || observation.Checks[0].Status != "pass" || observation.Checks[1].Status != "pending" {
			t.Fatalf("observation = %+v", observation)
		}
	})

	t.Run("failed check maps to fail", func(t *testing.T) {
		h := newHarness(t)
		published := newPublished(t, h)
		h.writeState("checks.json", checkRowsJSON(t, checkRow("build", "fail"), checkRow("lint", "pass")))

		record, err := h.publisher.ObserveChecks(context.Background(), domain.Record{Kind: domain.KindPublicationRecord, Data: publicationData(t, h, published)}, required)
		if err != nil {
			t.Fatal(err)
		}
		var observation domain.RemoteCheckRecord
		if err := json.Unmarshal(record.Data, &observation); err != nil {
			t.Fatal(err)
		}
		if observation.Status != "fail" || observation.Checks[0].Status != "fail" || observation.Checks[1].Status != "pass" {
			t.Fatalf("observation = %+v", observation)
		}
	})

	t.Run("cancelled and unknown buckets stay strict", func(t *testing.T) {
		h := newHarness(t)
		published := newPublished(t, h)
		h.writeState("checks.json", checkRowsJSON(t, checkRow("build", "cancel"), checkRow("lint", "unexpected-bucket")))

		record, err := h.publisher.ObserveChecks(context.Background(), domain.Record{Kind: domain.KindPublicationRecord, Data: publicationData(t, h, published)}, required)
		if err != nil {
			t.Fatal(err)
		}
		var observation domain.RemoteCheckRecord
		if err := json.Unmarshal(record.Data, &observation); err != nil {
			t.Fatal(err)
		}
		if observation.Status != "fail" || observation.Checks[0].Status != "cancel" || observation.Checks[1].Status != "pending" {
			t.Fatalf("observation = %+v", observation)
		}
	})

	t.Run("skipping required check stays pending", func(t *testing.T) {
		h := newHarness(t)
		published := newPublished(t, h)
		h.writeState("checks.json", checkRowsJSON(t, checkRow("build", "skipping"), checkRow("lint", "pass")))

		record, err := h.publisher.ObserveChecks(context.Background(), domain.Record{Kind: domain.KindPublicationRecord, Data: publicationData(t, h, published)}, required)
		if err != nil {
			t.Fatal(err)
		}
		var observation domain.RemoteCheckRecord
		if err := json.Unmarshal(record.Data, &observation); err != nil {
			t.Fatal(err)
		}
		if observation.Status != "pending" || observation.Checks[0].Status != "skipping" {
			t.Fatalf("observation = %+v", observation)
		}
	})

	t.Run("missing required check stays pending", func(t *testing.T) {
		h := newHarness(t)
		published := newPublished(t, h)
		h.writeState("checks.json", "[]")

		record, err := h.publisher.ObserveChecks(context.Background(), domain.Record{Kind: domain.KindPublicationRecord, Data: publicationData(t, h, published)}, required)
		if err != nil {
			t.Fatal(err)
		}
		var observation domain.RemoteCheckRecord
		if err := json.Unmarshal(record.Data, &observation); err != nil {
			t.Fatal(err)
		}
		if observation.Status != "pending" || observation.Checks[0].Status != "missing" || observation.Checks[1].Status != "missing" {
			t.Fatalf("observation = %+v", observation)
		}
	})

	t.Run("duplicate check identity blocks", func(t *testing.T) {
		h := newHarness(t)
		published := newPublished(t, h)
		h.writeState("checks.json", checkRowsJSON(t, checkRow("build", "pass"), checkRow("build", "pending"), checkRow("lint", "pass")))

		_, err := h.publisher.ObserveChecks(context.Background(), domain.Record{Kind: domain.KindPublicationRecord, Data: publicationData(t, h, published)}, required)
		if err == nil || !port.IsPermanent(err) || !strings.Contains(err.Error(), "multiple GitHub checks") {
			t.Fatalf("expected duplicate identity block, got: %v", err)
		}
	})

	t.Run("stale head is rejected permanently", func(t *testing.T) {
		h := newHarness(t)
		published := newPublished(t, h)
		intent := testIntent()
		h.seedPR(intent, func(pr map[string]any) { pr["headRefOid"] = testForeignSHA })
		h.writeState("checks.json", checkRowsJSON(t, checkRow("build", "pass"), checkRow("lint", "pass")))

		_, err := h.publisher.ObserveChecks(context.Background(), domain.Record{Kind: domain.KindPublicationRecord, Data: publicationData(t, h, published)}, required)
		if err == nil || !port.IsPermanent(err) || !strings.Contains(err.Error(), "head") {
			t.Fatalf("expected permanent stale-head rejection, got: %v", err)
		}
		if h.countCommands("gh", "pr checks ") != 0 {
			t.Fatalf("checks must not be read for a stale head: %v", h.commandLines("gh"))
		}
	})

	t.Run("PR identity change is rejected permanently", func(t *testing.T) {
		h := newHarness(t)
		published := newPublished(t, h)
		intent := testIntent()
		h.seedPR(intent, func(pr map[string]any) { pr["id"] = "PR_kw9999999999" })
		h.writeState("checks.json", checkRowsJSON(t, checkRow("build", "pass"), checkRow("lint", "pass")))

		_, err := h.publisher.ObserveChecks(context.Background(), domain.Record{Kind: domain.KindPublicationRecord, Data: publicationData(t, h, published)}, required)
		if err == nil || !port.IsPermanent(err) {
			t.Fatalf("expected permanent identity rejection, got: %v", err)
		}
	})

	t.Run("check command failure is not permanent", func(t *testing.T) {
		h := newHarness(t)
		published := newPublished(t, h)
		h.writeState("checks.json", checkRowsJSON(t, checkRow("build", "pass"), checkRow("lint", "pass")))
		h.writeState("checks-exit", "1")

		_, err := h.publisher.ObserveChecks(context.Background(), domain.Record{Kind: domain.KindPublicationRecord, Data: publicationData(t, h, published)}, required)
		if err == nil {
			t.Fatal("expected error for non-pending gh failure")
		}
		if port.IsPermanent(err) {
			t.Fatalf("transient check failure must stay retryable: %v", err)
		}
	})

	t.Run("wrong record kind is rejected", func(t *testing.T) {
		h := newHarness(t)
		newPublished(t, h)
		intent := testIntent()
		data, err := json.Marshal(intent)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.publisher.ObserveChecks(context.Background(), domain.Record{Kind: domain.KindPublicationIntent, Data: data}, required); err == nil {
			t.Fatal("expected kind rejection")
		}
	})
}

func checkRow(name, bucket string) map[string]string {
	return map[string]string{"name": name, "bucket": bucket, "link": "https://github.com/example-org/example-repo/runs/1", "state": "COMPLETED"}
}

func checkRowsJSON(t *testing.T, rows ...map[string]string) string {
	t.Helper()
	data, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func publicationData(t *testing.T, h *harness, published domain.PublicationRecord) []byte {
	t.Helper()
	data, err := json.Marshal(published)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.validator.Validate(domain.KindPublicationRecord, data); err != nil {
		t.Fatal(err)
	}
	return data
}

func (h *harness) seedMergedPR(intent domain.PublicationIntent, mutate func(map[string]any)) {
	h.t.Helper()
	h.seedPR(intent, func(pr map[string]any) {
		pr["state"] = "MERGED"
		pr["isDraft"] = false
		pr["mergedAt"] = "2026-08-12T10:00:00Z"
		pr["mergedBy"] = map[string]any{"login": "maintainer"}
		pr["mergeCommit"] = map[string]any{"oid": testMergeSHA}
		pr["baseRefOid"] = testBaseSHA
		if mutate != nil {
			mutate(pr)
		}
	})
}

func commitNodeJSON(t *testing.T, tree string, parents ...string) string {
	t.Helper()
	type parentRef struct {
		SHA string `json:"sha"`
	}
	type node struct {
		Parents []parentRef `json:"parents"`
		Tree    struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	value := node{}
	value.Tree.SHA = tree
	for _, parent := range parents {
		value.Parents = append(value.Parents, parentRef{SHA: parent})
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func mergedPublication(t *testing.T, h *harness, intent domain.PublicationIntent) []byte {
	t.Helper()
	publication := domain.PublicationRecord{
		APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindPublicationRecord,
		TaskID: intent.TaskID, RunID: intent.RunID, Provider: intent.Provider,
		Repository: domain.PublicationRepository{ID: testRepositoryID, NameWithOwner: intent.Repository, URL: "https://github.com/" + intent.Repository},
		Remote:     intent.Remote, BaseBranch: intent.BaseBranch, HeadBranch: intent.HeadBranch, ReviewRound: intent.ReviewRound,
		BaseSHA: intent.BaseSHA, PreviousHeadSHA: intent.PreviousHeadSHA, HeadSHA: intent.CommitSHA, CommitSHA: intent.CommitSHA,
		SnapshotDigest: intent.SnapshotDigest, DiffDigest: intent.DiffDigest,
		SpecDigest: intent.SpecDigest, PolicyDigest: intent.PolicyDigest,
		EvidenceDigest: intent.EvidenceDigest, VerificationDigest: intent.VerificationDigest,
		ReviewDecisionDigest: intent.ReviewDecisionDigest,
		Marker:               intent.Marker, Mode: intent.Mode, MergePolicy: intent.MergePolicy,
		Request: domain.PullRequestRecord{ID: testPRID, Number: 7, URL: testPRURL, Draft: true, State: "OPEN"},
		Actor:   testActor, PublishedAt: fixedTime, UpdatedAt: fixedTime,
	}
	data, err := json.Marshal(publication)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.validator.Validate(domain.KindPublicationRecord, data); err != nil {
		t.Fatalf("test publication record failed schema validation: %v", err)
	}
	return data
}

func TestObserveMergeReceiptCapturesImmutableMergeFact(t *testing.T) {
	h := newHarness(t)
	intent := testIntent()
	h.seedMergedPR(intent, nil)
	h.writeState("commit-"+testMergeSHA+".json", commitNodeJSON(t, testOtherTree, testBaseSHA, testCommitSHA))
	publicationData := mergedPublication(t, h, intent)

	record, err := h.publisher.ObserveMergeReceipt(context.Background(), domain.Record{Kind: domain.KindPublicationRecord, Data: publicationData})
	if err != nil {
		t.Fatalf("ObserveMergeReceipt: %v", err)
	}
	if record.Kind != domain.KindSCMMergeReceipt {
		t.Fatalf("record kind = %s", record.Kind)
	}
	var receipt domain.SCMMergeReceipt
	if err := json.Unmarshal(record.Data, &receipt); err != nil {
		t.Fatal(err)
	}
	publicationDigest, err := canonical.DigestJSON(publicationData)
	if err != nil {
		t.Fatal(err)
	}
	// The authority namespace expectation uses the identical frozen local
	// derivation as the implementation (tenantNamespace=local,
	// controlPlaneId=default, authorityScopeId=repository identity). The
	// scope is the symlink-normalized repository root: that is the exact
	// repository identity Publisher.New resolved and derives from, so the
	// expectation stays deterministic for a given repository instead of
	// drifting with unresolved temp-directory spellings.
	if h.publisher.repositoryRoot != h.repoRoot {
		t.Fatalf("harness repository root %q differs from publisher identity %q", h.repoRoot, h.publisher.repositoryRoot)
	}
	expectedNamespace, err := authority.AuthorityNamespaceId{TenantNamespace: "local", ControlPlaneId: "default", AuthorityScopeId: h.repoRoot}.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if receipt.AuthorityNamespaceID != expectedNamespace {
		t.Fatalf("authorityNamespaceId = %q, want %q", receipt.AuthorityNamespaceID, expectedNamespace)
	}
	if receipt.PublicationRecordID != publicationDigest {
		t.Fatalf("publicationRecordId = %q, want frozen publication digest %q", receipt.PublicationRecordID, publicationDigest)
	}
	if receipt.ReceiptID != MergeReceiptID("run-01", publicationDigest, testMergeSHA) || receipt.ReceiptID == "" {
		t.Fatalf("receiptId = %q", receipt.ReceiptID)
	}
	if receipt.RunID != "run-01" || receipt.RepositoryRef != testRepository || receipt.PRNumber != 7 {
		t.Fatalf("receipt binding = %+v", receipt)
	}
	if receipt.HeadOid != testCommitSHA || receipt.BaseOid != testBaseSHA || receipt.MergeCommitSha != testMergeSHA {
		t.Fatalf("receipt OIDs = %+v", receipt)
	}
	if receipt.MergedBy != "maintainer" || receipt.MergeMethod != domain.MergeMethodMerge {
		t.Fatalf("receipt merge facts = %+v", receipt)
	}
	if !receipt.MergedAt.Equal(time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)) || !receipt.CapturedAt.Equal(fixedTime) {
		t.Fatalf("receipt timestamps = %+v", receipt)
	}
	recomputed, err := receipt.Digest()
	if err != nil || receipt.ReceiptDigest != recomputed {
		t.Fatalf("receiptDigest = %q, recomputed %q (err=%v)", receipt.ReceiptDigest, recomputed, err)
	}
	if err := receipt.Validate(); err != nil {
		t.Fatalf("receipt semantic validation failed: %v", err)
	}
	if h.countCommands("gh", "pr view ") != 2 {
		t.Fatalf("expected the dual-cut identity recheck: %v", h.commandLines("gh"))
	}
	if h.countCommands("gh", "api repos/"+testRepository+"/commits/"+testMergeSHA) != 1 {
		t.Fatalf("expected merge commit observation: %v", h.commandLines("gh"))
	}
	if h.commandLines("git") != nil {
		t.Fatalf("merge receipt capture must not run git: %v", h.commandLines("git"))
	}
	h.assertNoSecrets("SCMMergeReceipt", string(record.Data))
}

func TestObserveMergeReceiptClassifiesMergeMethod(t *testing.T) {
	run := func(t *testing.T, mergeCommit, headCommit string) (string, error) {
		t.Helper()
		h := newHarness(t)
		intent := testIntent()
		h.seedMergedPR(intent, nil)
		if mergeCommit != "" {
			h.writeState("commit-"+testMergeSHA+".json", mergeCommit)
		}
		if headCommit != "" {
			h.writeState("commit-"+testCommitSHA+".json", headCommit)
		}
		record, err := h.publisher.ObserveMergeReceipt(context.Background(), domain.Record{Kind: domain.KindPublicationRecord, Data: mergedPublication(t, h, intent)})
		if err != nil {
			return "", err
		}
		var receipt domain.SCMMergeReceipt
		if err := json.Unmarshal(record.Data, &receipt); err != nil {
			t.Fatal(err)
		}
		// Classification must have been derived exclusively from the injected
		// fake gh api responses (hermetic; no real network or credentials).
		if h.countCommands("gh", "api repos/"+testRepository+"/commits/") == 0 {
			t.Fatalf("classification did not use the injected gh api path: %v", h.commandLines("gh"))
		}
		return receipt.MergeMethod, nil
	}

	t.Run("two parents classify as merge", func(t *testing.T) {
		method, err := run(t, commitNodeJSON(t, testOtherTree, testBaseSHA, testCommitSHA), "")
		if err != nil || method != domain.MergeMethodMerge {
			t.Fatalf("method = %q, err = %v", method, err)
		}
	})
	t.Run("single parent with head tree classifies as squash", func(t *testing.T) {
		method, err := run(t, commitNodeJSON(t, testHeadTree, testBaseSHA), commitNodeJSON(t, testHeadTree, testCommitSHA))
		if err != nil || method != domain.MergeMethodSquash {
			t.Fatalf("method = %q, err = %v", method, err)
		}
	})
	t.Run("single parent with different tree classifies as rebase", func(t *testing.T) {
		method, err := run(t, commitNodeJSON(t, testOtherTree, testBaseSHA), commitNodeJSON(t, testHeadTree, testCommitSHA))
		if err != nil || method != domain.MergeMethodRebase {
			t.Fatalf("method = %q, err = %v", method, err)
		}
	})
	t.Run("zero parents fail closed", func(t *testing.T) {
		_, err := run(t, commitNodeJSON(t, testOtherTree), "")
		if err == nil || !port.IsPermanent(err) || !strings.Contains(err.Error(), "cannot be determined") {
			t.Fatalf("expected fail-closed classification error, got %v", err)
		}
	})
	t.Run("missing merge commit fact fails retryably", func(t *testing.T) {
		_, err := run(t, "", "")
		if err == nil || port.IsPermanent(err) {
			t.Fatalf("expected retryable failure for absent merge commit observation, got %v", err)
		}
	})
}

func TestObserveMergeReceiptRejectsUnsafeFacts(t *testing.T) {
	t.Run("unmerged PR returns the not-merged sentinel", func(t *testing.T) {
		h := newHarness(t)
		intent := testIntent()
		h.seedPR(intent, nil)
		_, err := h.publisher.ObserveMergeReceipt(context.Background(), domain.Record{Kind: domain.KindPublicationRecord, Data: mergedPublication(t, h, intent)})
		if !errors.Is(err, port.ErrPRNotMerged) {
			t.Fatalf("err = %v, want port.ErrPRNotMerged", err)
		}
	})
	t.Run("foreign head is rejected permanently", func(t *testing.T) {
		h := newHarness(t)
		intent := testIntent()
		h.seedMergedPR(intent, func(pr map[string]any) { pr["headRefOid"] = testForeignSHA })
		_, err := h.publisher.ObserveMergeReceipt(context.Background(), domain.Record{Kind: domain.KindPublicationRecord, Data: mergedPublication(t, h, intent)})
		if err == nil || !port.IsPermanent(err) || !strings.Contains(err.Error(), "identity") {
			t.Fatalf("expected permanent identity rejection, got %v", err)
		}
	})
	t.Run("merged node without merge facts fails closed", func(t *testing.T) {
		h := newHarness(t)
		intent := testIntent()
		h.seedMergedPR(intent, func(pr map[string]any) { pr["mergeCommit"] = nil })
		_, err := h.publisher.ObserveMergeReceipt(context.Background(), domain.Record{Kind: domain.KindPublicationRecord, Data: mergedPublication(t, h, intent)})
		if err == nil || !port.IsPermanent(err) || !strings.Contains(err.Error(), "immutable merge facts") {
			t.Fatalf("expected fail-closed missing facts, got %v", err)
		}
	})
	t.Run("identity change during the observation window conflicts", func(t *testing.T) {
		h := newHarness(t)
		intent := testIntent()
		h.seedMergedPR(intent, nil)
		h.writeState("commit-"+testMergeSHA+".json", commitNodeJSON(t, testOtherTree, testBaseSHA, testCommitSHA))
		changed := map[string]any{
			"id": "PR_kw9999999999", "number": 7, "url": testPRURL, "isDraft": false, "state": "MERGED",
			"headRefName": intent.HeadBranch, "headRefOid": intent.CommitSHA,
			"headRepositoryOwner": map[string]any{"login": "example-org"}, "isCrossRepository": false,
			"baseRefName": intent.BaseBranch, "baseRefOid": testBaseSHA,
			"mergedAt": "2026-08-12T10:00:00Z", "mergedBy": map[string]any{"login": "maintainer"},
			"mergeCommit": map[string]any{"oid": testMergeSHA}, "body": renderBody(intent),
		}
		data, err := json.Marshal(changed)
		if err != nil {
			t.Fatal(err)
		}
		h.writeState("pr-changed.json", string(data))
		_, err = h.publisher.ObserveMergeReceipt(context.Background(), domain.Record{Kind: domain.KindPublicationRecord, Data: mergedPublication(t, h, intent)})
		if err == nil || !port.IsPermanent(err) || !strings.Contains(err.Error(), "changed while merge receipt was observed") {
			t.Fatalf("expected permanent observation-window conflict, got %v", err)
		}
	})
	t.Run("wrong record kind is rejected", func(t *testing.T) {
		h := newHarness(t)
		if _, err := h.publisher.ObserveMergeReceipt(context.Background(), domain.Record{Kind: domain.KindPublicationIntent, Data: []byte("{}")}); err == nil {
			t.Fatal("expected kind rejection")
		}
	})
}

func TestObserveChecksAcceptsMergedPRHead(t *testing.T) {
	h := newHarness(t)
	intent := testIntent()
	h.seedMergedPR(intent, nil)
	h.writeState("checks.json", checkRowsJSON(t, checkRow("build", "pass"), checkRow("lint", "pass")))
	publicationData := mergedPublication(t, h, intent)

	record, err := h.publisher.ObserveChecks(context.Background(), domain.Record{Kind: domain.KindPublicationRecord, Data: publicationData}, []string{"build", "lint"})
	if err != nil {
		t.Fatalf("ObserveChecks on a merged PR must not fail closed on identity: %v", err)
	}
	var observation domain.RemoteCheckRecord
	if err := json.Unmarshal(record.Data, &observation); err != nil {
		t.Fatal(err)
	}
	if observation.Status != "pass" || observation.HeadSHA != testCommitSHA {
		t.Fatalf("observation = %+v", observation)
	}
}

func TestObserveMergeReceiptErrorsDoNotLeakSecrets(t *testing.T) {
	h := newHarness(t)
	h.setPublisherSecrets()
	intent := testIntent()
	h.seedMergedPR(intent, nil)
	// No commit fixture: the classification call fails and the error surface
	// must stay free of credentials, config dirs and local paths.
	_, err := h.publisher.ObserveMergeReceipt(context.Background(), domain.Record{Kind: domain.KindPublicationRecord, Data: mergedPublication(t, h, intent)})
	if err == nil {
		t.Fatal("expected classification failure")
	}
	h.assertNoSecrets("merge receipt error", err.Error())
}

func TestPublisherChildEnvironmentIsControlled(t *testing.T) {
	h := newHarness(t)
	h.setPublisherSecrets()
	intent := testIntent()
	h.seedPR(intent, nil)

	record, err := h.publish(intent)
	if err != nil {
		t.Fatal(err)
	}

	ghEnv, gitEnv := h.envDump("gh"), h.envDump("git")
	if ghEnv == "" || gitEnv == "" {
		t.Fatalf("expected environment captures, gh=%d git=%d bytes", len(ghEnv), len(gitEnv))
	}
	for _, dump := range []string{ghEnv, gitEnv} {
		h.assertNoSecretTokens("child environment", dump)
	}
	for _, entry := range []string{
		"GH_CONFIG_DIR=" + h.publisher.configDir,
		"GH_PROMPT_DISABLED=1",
		"NO_COLOR=1",
	} {
		if !strings.Contains(ghEnv, entry+"\n") {
			t.Fatalf("gh child environment missing %q", entry)
		}
	}
	if strings.Contains(ghEnv, "GIT_ASKPASS") {
		t.Fatal("gh children must not receive GIT_ASKPASS")
	}

	gitLines := strings.Split(gitEnv, "\n")
	var askpassValue string
	for _, line := range gitLines {
		if strings.HasPrefix(line, "GIT_ASKPASS=") {
			askpassValue = strings.TrimPrefix(line, "GIT_ASKPASS=")
		}
	}
	requiredGitEntries := []string{
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_KEY_0=credential.helper",
		"GIT_CONFIG_VALUE_0=",
		"GIT_CONFIG_KEY_1=core.hooksPath",
		"GIT_CONFIG_VALUE_1=/dev/null",
	}
	for _, entry := range requiredGitEntries {
		found := false
		for _, line := range gitLines {
			if line == entry {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("git child environment missing controlled entry %q", entry)
		}
	}
	if askpassValue == "" || askpassValue == "/attacker-controlled/askpass" || !strings.Contains(askpassValue, "marshal-publisher-") {
		t.Fatalf("git child GIT_ASKPASS = %q", askpassValue)
	}

	script := h.readState("askpass-copy")
	if script == "" {
		t.Fatal("git child never received the askpass wrapper")
	}
	if !strings.Contains(script, h.publisher.ghPath+`" auth token`) || !strings.Contains(script, "x-access-token") {
		t.Fatalf("askpass wrapper does not delegate to gh auth token: %s", script)
	}
	h.assertNoSecretTokens("askpass wrapper", script)

	h.assertNoSecrets("PublicationRecord", string(record.Data))
}

func TestPublisherErrorsDoNotLeakSecrets(t *testing.T) {
	h := newHarness(t)
	h.setPublisherSecrets()
	intent := testIntent()
	h.seedPR(intent, nil)
	h.writeState("push-mode", "fail")
	h.writeState("error-output", "test-secret-github-token "+h.configDir+" "+h.repoRoot)

	_, err := h.publish(intent)
	if err == nil {
		t.Fatal("expected failure")
	}
	h.assertNoSecrets("publish error", err.Error())
}
