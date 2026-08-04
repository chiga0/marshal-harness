package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
)

// TestLiveDraftPR is an explicit, opt-in GitHub E2E. It intentionally leaves
// the Draft PR and remote branch in place as durable acceptance evidence.
func TestLiveDraftPR(t *testing.T) {
	if os.Getenv("MARSHAL_LIVE_GITHUB") != "1" {
		t.Skip("set MARSHAL_LIVE_GITHUB=1 to create a real Draft PR")
	}
	ghPath, configDir := os.Getenv("MARSHAL_GH_PATH"), os.Getenv("MARSHAL_GH_CONFIG_DIR")
	if ghPath == "" || configDir == "" {
		t.Fatal("MARSHAL_GH_PATH and MARSHAL_GH_CONFIG_DIR are required")
	}
	repositoryRoot := liveGit(t, "rev-parse", "--show-toplevel")
	remoteURL := liveGit(t, "remote", "get-url", "origin")
	owner, name, err := parseGitHubRepository(remoteURL)
	if err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().UTC()
	suffix := fmt.Sprintf("%x", stamp.UnixNano())
	if fixed := os.Getenv("MARSHAL_LIVE_GITHUB_FIXED_SUFFIX"); fixed != "" {
		suffix = fixed
	}
	taskID, runID := "m5-live", "run-"+suffix
	headBranch := "marshal/" + taskID + "-" + suffix
	baseSHA := liveGit(t, "rev-parse", "HEAD")
	commitSHA := ""
	if os.Getenv("MARSHAL_LIVE_GITHUB_FIXED_SUFFIX") != "" {
		fields := strings.Fields(liveGit(t, "ls-remote", "--heads", "origin", "refs/heads/"+headBranch))
		if len(fields) != 2 {
			t.Fatalf("fixed live branch %s does not exist", headBranch)
		}
		commitSHA = fields[0]
		baseSHA = liveGit(t, "rev-parse", commitSHA+"^")
	} else {
		index := filepath.Join(t.TempDir(), "index")
		gitEnv := append(os.Environ(), "GIT_INDEX_FILE="+index, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.hooksPath", "GIT_CONFIG_VALUE_0=/dev/null")
		liveGitEnv(t, gitEnv, nil, "read-tree", baseSHA)
		content := []byte("# Marshal M5 GitHub E2E\n\n此文件由显式 live test 生成，用于验证真实 Draft PR 的创建与幂等对账。\n")
		blob := liveGitEnv(t, gitEnv, content, "hash-object", "-w", "--stdin")
		path := "docs/e2e/" + runID + ".md"
		liveGitEnv(t, gitEnv, nil, "update-index", "--add", "--cacheinfo", "100644,"+blob+","+path)
		tree := liveGitEnv(t, gitEnv, nil, "write-tree")
		commitEnv := append(gitEnv,
			"GIT_AUTHOR_NAME=Marshal Publisher", "GIT_AUTHOR_EMAIL=marshal@localhost",
			"GIT_COMMITTER_NAME=Marshal Publisher", "GIT_COMMITTER_EMAIL=marshal@localhost",
		)
		commitSHA = liveGitEnv(t, commitEnv, []byte("test: marshal M5 real Draft PR\n"), "commit-tree", tree, "-p", baseSHA)
	}

	validator, err := contract.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := New(ghPath, configDir, repositoryRoot, validator)
	if err != nil {
		t.Fatal(err)
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	intent := domain.PublicationIntent{
		APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindPublicationIntent,
		TaskID: taskID, RunID: runID, Provider: domain.PublicationProviderGitHub,
		Repository: owner + "/" + name, Remote: "origin", RemoteURL: remoteURL,
		BaseBranch: "main", HeadBranch: headBranch, ReviewRound: 1,
		BaseSHA: baseSHA, CommitSHA: commitSHA, SnapshotDigest: digest, DiffDigest: digest,
		SpecDigest: digest, PolicyDigest: digest, EvidenceDigest: digest,
		VerificationDigest: digest, ReviewDecisionDigest: digest,
		Marker: "<!-- marshal task=" + taskID + " run=" + runID + " -->",
		Mode:   domain.PublicationModeDraft, MergePolicy: domain.MergePolicyNever,
		Summary: "Marshal M5 真实 GitHub Draft PR E2E", CreatedAt: stamp,
	}
	data, err := json.Marshal(intent)
	if err != nil {
		t.Fatal(err)
	}
	input := domain.Record{Kind: domain.KindPublicationIntent, Data: data}
	first, err := publisher.Publish(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := publisher.Publish(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	var firstRecord, secondRecord domain.PublicationRecord
	if err := json.Unmarshal(first.Data, &firstRecord); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(second.Data, &secondRecord); err != nil {
		t.Fatal(err)
	}
	if firstRecord.Request.ID != secondRecord.Request.ID || firstRecord.Request.Number != secondRecord.Request.Number || !secondRecord.Request.Draft {
		t.Fatalf("publisher was not idempotent: first=%+v second=%+v", firstRecord.Request, secondRecord.Request)
	}
	t.Logf("real Draft PR: %s", secondRecord.Request.URL)
}

func liveGit(t *testing.T, args ...string) string {
	t.Helper()
	return liveGitEnv(t, os.Environ(), nil, args...)
}

func liveGitEnv(t *testing.T, environment []string, input []byte, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Env = environment
	command.Stdin = bytes.NewReader(input)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", args[0], err, output)
	}
	return strings.TrimSpace(string(output))
}
