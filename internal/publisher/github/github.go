// Package github implements the credentialed GitHub Draft PR publisher. It
// never exposes merge, ready-for-review, release, or deployment operations.
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/port"
)

const commandTimeout = 45 * time.Second

type Publisher struct {
	ghPath, gitPath, configDir, repositoryRoot string
	validator                                  *contract.Validator
	now                                        func() time.Time
	reconcileDelay                             time.Duration
}

func New(ghPath, configDir, repositoryRoot string, validator *contract.Validator) (*Publisher, error) {
	if validator == nil {
		return nil, errors.New("contract validator is required")
	}
	for name, path := range map[string]string{"GitHub CLI": ghPath, "GitHub config dir": configDir, "repository": repositoryRoot} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return nil, fmt.Errorf("%s must be an absolute clean path", name)
		}
	}
	realGH, err := filepath.EvalSymlinks(ghPath)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(realGH)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return nil, errors.New("GitHub CLI must be an executable regular file")
	}
	realConfig, err := filepath.EvalSymlinks(configDir)
	if err != nil {
		return nil, fmt.Errorf("resolve GitHub config dir: %w", err)
	}
	if info, err = os.Stat(realConfig); err != nil || !info.IsDir() {
		return nil, errors.New("GitHub config dir must exist")
	}
	realRepository, err := filepath.EvalSymlinks(repositoryRoot)
	if err != nil {
		return nil, err
	}
	if info, err = os.Stat(realRepository); err != nil || !info.IsDir() {
		return nil, errors.New("repository must be an existing directory")
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return nil, err
	}
	realGit, err := filepath.EvalSymlinks(gitPath)
	if err != nil {
		return nil, err
	}
	if info, err = os.Stat(realGit); err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return nil, errors.New("git must be an executable regular file")
	}
	return &Publisher{ghPath: realGH, gitPath: realGit, configDir: realConfig, repositoryRoot: realRepository, validator: validator, now: time.Now, reconcileDelay: 300 * time.Millisecond}, nil
}

func (p *Publisher) Publish(ctx context.Context, record domain.Record) (domain.Record, error) {
	if record.Kind != domain.KindPublicationIntent {
		return domain.Record{}, fmt.Errorf("expected PublicationIntent, got %s", record.Kind)
	}
	if err := p.validator.Validate(domain.KindPublicationIntent, record.Data); err != nil {
		return domain.Record{}, err
	}
	var intent domain.PublicationIntent
	if err := json.Unmarshal(record.Data, &intent); err != nil {
		return domain.Record{}, err
	}
	if intent.Provider != domain.PublicationProviderGitHub || intent.Mode != domain.PublicationModeDraft || intent.MergePolicy != domain.MergePolicyNever {
		return domain.Record{}, errors.New("GitHub publisher only supports draft PRs with mergePolicy=never")
	}
	owner, name, err := parseGitHubRepository(intent.RemoteURL)
	if err != nil || owner+"/"+name != intent.Repository {
		return domain.Record{}, errors.New("intent repository does not match HTTPS GitHub remote")
	}
	repository, err := p.repository(ctx, intent.Repository)
	if err != nil {
		return domain.Record{}, err
	}
	if repository.NameWithOwner != intent.Repository {
		return domain.Record{}, port.Permanent(errors.New("authenticated repository identity mismatch"))
	}
	actor, err := p.actor(ctx)
	if err != nil {
		return domain.Record{}, err
	}
	askDir, err := os.MkdirTemp("", "marshal-publisher-*")
	if err != nil {
		return domain.Record{}, err
	}
	defer os.RemoveAll(askDir)
	askpass, err := p.writeAskPass(askDir)
	if err != nil {
		return domain.Record{}, err
	}
	remoteHead, err := p.remoteHead(ctx, askpass, intent.RemoteURL, intent.HeadBranch)
	if err != nil {
		return domain.Record{}, err
	}
	allowedPrevious := intent.PreviousHeadSHA != "" && remoteHead == intent.PreviousHeadSHA
	if remoteHead != "" && remoteHead != intent.CommitSHA && !allowedPrevious {
		return domain.Record{}, port.Permanentf("remote branch contains unexpected head %s", remoteHead)
	}
	if remoteHead != intent.CommitSHA {
		if pushErr := p.push(ctx, askpass, intent); pushErr != nil {
			reconciled, reconcileErr := p.remoteHead(ctx, askpass, intent.RemoteURL, intent.HeadBranch)
			if reconcileErr != nil || reconciled != intent.CommitSHA {
				return domain.Record{}, fmt.Errorf("ambiguous push: %w", errors.Join(pushErr, reconcileErr))
			}
		}
	}
	body := renderBody(intent)
	pr, err := p.reconcilePR(ctx, intent, body)
	if err != nil {
		return domain.Record{}, err
	}
	if !pr.IsDraft || pr.State != "OPEN" || !prBelongsToIntent(pr, intent) || !strings.Contains(pr.Body, intent.Marker) {
		return domain.Record{}, port.Permanent(errors.New("GitHub PR identity, draft state, marker, or head SHA mismatch"))
	}
	now := p.now().UTC()
	publication := domain.PublicationRecord{
		APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindPublicationRecord, TaskID: intent.TaskID, RunID: intent.RunID,
		Provider: intent.Provider, Repository: domain.PublicationRepository{ID: repository.ID, NameWithOwner: repository.NameWithOwner, URL: repository.URL},
		Remote: intent.Remote, BaseBranch: intent.BaseBranch, HeadBranch: intent.HeadBranch, ReviewRound: intent.ReviewRound,
		BaseSHA: intent.BaseSHA, PreviousHeadSHA: intent.PreviousHeadSHA, HeadSHA: intent.CommitSHA, CommitSHA: intent.CommitSHA,
		SnapshotDigest: intent.SnapshotDigest, DiffDigest: intent.DiffDigest, SpecDigest: intent.SpecDigest, PolicyDigest: intent.PolicyDigest,
		EvidenceDigest: intent.EvidenceDigest, VerificationDigest: intent.VerificationDigest, ReviewDecisionDigest: intent.ReviewDecisionDigest,
		Marker: intent.Marker, Mode: intent.Mode, MergePolicy: intent.MergePolicy,
		Request: domain.PullRequestRecord{ID: pr.ID, Number: pr.Number, URL: pr.URL, Draft: pr.IsDraft, State: pr.State}, Actor: actor, PublishedAt: now, UpdatedAt: now,
	}
	data, err := json.Marshal(publication)
	if err != nil {
		return domain.Record{}, err
	}
	if err := p.validator.Validate(domain.KindPublicationRecord, data); err != nil {
		return domain.Record{}, err
	}
	return domain.Record{Kind: domain.KindPublicationRecord, Data: data}, nil
}

func (p *Publisher) ObserveChecks(ctx context.Context, record domain.Record, required []string) (domain.Record, error) {
	if record.Kind != domain.KindPublicationRecord {
		return domain.Record{}, errors.New("expected PublicationRecord")
	}
	if err := p.validator.Validate(domain.KindPublicationRecord, record.Data); err != nil {
		return domain.Record{}, err
	}
	var publication domain.PublicationRecord
	if err := json.Unmarshal(record.Data, &publication); err != nil {
		return domain.Record{}, err
	}
	pr, err := p.viewPR(ctx, publication.Repository.NameWithOwner, strconv.Itoa(publication.Request.Number))
	if err != nil {
		return domain.Record{}, err
	}
	if !prMatchesPublication(pr, publication) {
		return domain.Record{}, port.Permanent(errors.New("remote PR head or identity changed"))
	}
	var rows []struct{ Name, Bucket, Link, State string }
	output, checkErr := p.ghAllowPending(ctx, "pr", "checks", strconv.Itoa(publication.Request.Number), "--repo", publication.Repository.NameWithOwner, "--json", "name,bucket,link,state")
	if checkErr != nil {
		return domain.Record{}, checkErr
	}
	if err := json.Unmarshal(output, &rows); err != nil {
		return domain.Record{}, err
	}
	pr, err = p.viewPR(ctx, publication.Repository.NameWithOwner, strconv.Itoa(publication.Request.Number))
	if err != nil {
		return domain.Record{}, err
	}
	if !prMatchesPublication(pr, publication) {
		return domain.Record{}, port.Permanent(errors.New("remote PR changed while checks were observed"))
	}
	requiredSet := make(map[string]bool, len(required))
	for _, name := range required {
		if requiredSet[name] {
			return domain.Record{}, port.Permanentf("required GitHub check identity %q is duplicated", name)
		}
		requiredSet[name] = true
	}
	byName := map[string]struct{ Bucket, Link string }{}
	for _, row := range rows {
		if !requiredSet[row.Name] {
			continue
		}
		if _, duplicate := byName[row.Name]; duplicate {
			return domain.Record{}, port.Permanentf("multiple GitHub checks share required identity %q", row.Name)
		}
		byName[row.Name] = struct{ Bucket, Link string }{strings.ToLower(row.Bucket), row.Link}
	}
	checks := make([]domain.RemoteCheck, 0, len(required))
	overall := "pass"
	for _, name := range required {
		row, ok := byName[name]
		status := "missing"
		link := ""
		if ok {
			link = row.Link
			switch row.Bucket {
			case "pass":
				status = "pass"
			case "fail":
				status = "fail"
			case "pending":
				status = "pending"
			case "skipping":
				status = "skipping"
			case "cancel":
				status = "cancel"
			default:
				status = "pending"
			}
		}
		if status == "fail" || status == "cancel" {
			overall = "fail"
		} else if overall != "fail" && status != "pass" {
			overall = "pending"
		}
		checks = append(checks, domain.RemoteCheck{Name: name, Required: true, Status: status, URL: link})
	}
	observation := domain.RemoteCheckRecord{APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRemoteCheckRecord, TaskID: publication.TaskID, RunID: publication.RunID, Provider: "github", RepositoryID: publication.Repository.ID, RequestID: publication.Request.ID, HeadSHA: publication.HeadSHA, Status: overall, Checks: checks, ObservedAt: p.now().UTC()}
	data, err := json.Marshal(observation)
	if err != nil {
		return domain.Record{}, err
	}
	if err := p.validator.Validate(domain.KindRemoteCheckRecord, data); err != nil {
		return domain.Record{}, err
	}
	return domain.Record{Kind: domain.KindRemoteCheckRecord, Data: data}, nil
}

// ActorLogin observes the authenticated maintainer login. It is a read-only
// identity observation used to attribute compensating reconciliation.
func (p *Publisher) ActorLogin(ctx context.Context) (string, error) {
	return p.actor(ctx)
}

var commitObjectIDPattern = regexp.MustCompile(`^[0-9a-f]{40,64}$`)

// ObserveMergeReceipt captures the ADR 0026 immutable merge fact of a merged
// publication. It is strictly observational (merge-never): it never merges,
// edits, closes or otherwise mutates the remote PR. When the PR node is not
// merged it returns port.ErrPRNotMerged so callers fall back to the ordinary
// check observation flow. Errors and the returned record never carry GH
// config dirs, tokens or absolute local paths.
func (p *Publisher) ObserveMergeReceipt(ctx context.Context, record domain.Record) (domain.Record, error) {
	if record.Kind != domain.KindPublicationRecord {
		return domain.Record{}, errors.New("expected PublicationRecord")
	}
	if err := p.validator.Validate(domain.KindPublicationRecord, record.Data); err != nil {
		return domain.Record{}, err
	}
	var publication domain.PublicationRecord
	if err := json.Unmarshal(record.Data, &publication); err != nil {
		return domain.Record{}, err
	}
	number := strconv.Itoa(publication.Request.Number)
	pr, err := p.viewPR(ctx, publication.Repository.NameWithOwner, number)
	if err != nil {
		return domain.Record{}, err
	}
	if !prMatchesPublication(pr, publication) {
		return domain.Record{}, port.Permanent(errors.New("remote PR head or identity changed"))
	}
	if pr.State != "MERGED" {
		return domain.Record{}, port.ErrPRNotMerged
	}
	if pr.MergeCommit.OID == "" || pr.MergedAt == "" || pr.MergedBy.Login == "" || pr.BaseRefOID == "" {
		return domain.Record{}, port.Permanent(errors.New("merged PR node lacks immutable merge facts"))
	}
	if !commitObjectIDPattern.MatchString(pr.MergeCommit.OID) || !commitObjectIDPattern.MatchString(pr.HeadRefOID) {
		return domain.Record{}, port.Permanent(errors.New("merged PR node reports malformed object ids"))
	}
	mergedAt, err := time.Parse(time.RFC3339, pr.MergedAt)
	if err != nil {
		return domain.Record{}, port.Permanent(errors.New("merged PR node reports malformed mergedAt"))
	}
	method, err := p.classifyMergeMethod(ctx, publication.Repository.NameWithOwner, pr.MergeCommit.OID, publication.HeadSHA)
	if err != nil {
		return domain.Record{}, err
	}
	// Collection discipline: identity recheck after the merge-method
	// observation mirrors the dual-cut pattern used around check observation.
	recheck, err := p.viewPR(ctx, publication.Repository.NameWithOwner, number)
	if err != nil {
		return domain.Record{}, err
	}
	if !prMatchesPublication(recheck, publication) || recheck.State != "MERGED" ||
		recheck.MergeCommit.OID != pr.MergeCommit.OID || recheck.HeadRefOID != pr.HeadRefOID ||
		recheck.BaseRefOID != pr.BaseRefOID || recheck.MergedAt != pr.MergedAt {
		return domain.Record{}, port.Permanent(errors.New("remote PR changed while merge receipt was observed"))
	}
	publicationDigest, err := canonical.DigestJSON(record.Data)
	if err != nil {
		return domain.Record{}, err
	}
	authorityNamespaceID, err := localAuthorityNamespaceID(p.repositoryRoot)
	if err != nil {
		return domain.Record{}, err
	}
	// prNumber is taken from the frozen PublicationRecord, never from the
	// remote echo; repositoryRef is the frozen publication repository identity.
	receipt := domain.SCMMergeReceipt{
		APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindSCMMergeReceipt,
		ReceiptID:            MergeReceiptID(publication.RunID, publicationDigest, pr.MergeCommit.OID),
		AuthorityNamespaceID: authorityNamespaceID,
		RunID:                publication.RunID,
		PublicationRecordID:  publicationDigest,
		RepositoryRef:        publication.Repository.NameWithOwner,
		PRNumber:             publication.Request.Number,
		HeadOid:              pr.HeadRefOID,
		BaseOid:              pr.BaseRefOID,
		MergeCommitSha:       pr.MergeCommit.OID,
		MergedAt:             mergedAt.UTC(),
		MergedBy:             pr.MergedBy.Login,
		MergeMethod:          method,
		CapturedAt:           p.now().UTC(),
	}
	digest, err := receipt.Digest()
	if err != nil {
		return domain.Record{}, err
	}
	receipt.ReceiptDigest = digest
	data, err := json.Marshal(receipt)
	if err != nil {
		return domain.Record{}, err
	}
	if err := p.validator.Validate(domain.KindSCMMergeReceipt, data); err != nil {
		return domain.Record{}, err
	}
	return domain.Record{Kind: domain.KindSCMMergeReceipt, Data: data}, nil
}

// MergeReceiptID derives the deterministic content-bound receipt identity:
// "receipt-" + sha256 over the canonical form of (runId,
// publicationRecordId, mergeCommitSha). Repeated capture of the same merge
// fact therefore merges idempotently.
func MergeReceiptID(runID, publicationRecordID, mergeCommitSHA string) string {
	document, err := json.Marshal(map[string]string{
		"mergeCommitSha":      mergeCommitSHA,
		"publicationRecordId": publicationRecordID,
		"runId":               runID,
	})
	if err != nil {
		return ""
	}
	digest, err := canonical.DigestJSON(document)
	if err != nil {
		return ""
	}
	return "receipt-" + strings.TrimPrefix(digest, "sha256:")
}

// localAuthorityNamespaceID derives the frozen local authority namespace
// (ADR 0026): tenantNamespace=local, controlPlaneId=default,
// authorityScopeId=repository identity. repositoryRoot equals the
// RepositoryIdentity recorded in repo.json for this repository (the CLI
// enforces the binding), so both capture sites derive the identical digest.
func localAuthorityNamespaceID(repositoryRoot string) (string, error) {
	namespace := authority.AuthorityNamespaceId{TenantNamespace: "local", ControlPlaneId: "default", AuthorityScopeId: repositoryRoot}
	return namespace.Digest()
}

// classifyMergeMethod deterministically derives the merge method from the
// merge commit's parents and tree (the GitHub API does not expose it
// directly). Two parents means a merge commit; a single parent whose tree
// equals the PR head tree means squash; a single parent with a different tree
// means rebase. Anything else fails closed: the closed enumeration admits no
// unknown value.
func (p *Publisher) classifyMergeMethod(ctx context.Context, repository, mergeCommitSHA, headSHA string) (string, error) {
	for _, objectID := range []string{mergeCommitSHA, headSHA} {
		if !commitObjectIDPattern.MatchString(objectID) {
			return "", port.Permanent(errors.New("merge method classification received a malformed object id"))
		}
	}
	mergeCommit, err := p.commitNode(ctx, repository, mergeCommitSHA)
	if err != nil {
		return "", err
	}
	switch len(mergeCommit.Parents) {
	case 2:
		return domain.MergeMethodMerge, nil
	case 1:
		headCommit, err := p.commitNode(ctx, repository, headSHA)
		if err != nil {
			return "", err
		}
		if mergeCommit.Tree == "" || headCommit.Tree == "" {
			return "", port.Permanent(errors.New("merge method classification lacks commit tree identity"))
		}
		if mergeCommit.Tree == headCommit.Tree {
			return domain.MergeMethodSquash, nil
		}
		return domain.MergeMethodRebase, nil
	default:
		return "", port.Permanent(errors.New("merge method cannot be determined from merge commit parents"))
	}
}

type commitNode struct {
	Parents []string
	Tree    string
}

func (p *Publisher) commitNode(ctx context.Context, repository, sha string) (commitNode, error) {
	var raw struct {
		Parents []struct {
			SHA string `json:"sha"`
		} `json:"parents"`
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if err := p.ghJSON(ctx, &raw, "api", "repos/"+repository+"/commits/"+sha); err != nil {
		return commitNode{}, err
	}
	node := commitNode{Tree: raw.Tree.SHA}
	for _, parent := range raw.Parents {
		node.Parents = append(node.Parents, parent.SHA)
	}
	return node, nil
}

type repositoryRecord struct{ ID, NameWithOwner, URL string }

func (p *Publisher) repository(ctx context.Context, name string) (repositoryRecord, error) {
	var raw struct {
		NodeID   string `json:"node_id"`
		FullName string `json:"full_name"`
		HTMLURL  string `json:"html_url"`
	}
	if err := p.ghJSON(ctx, &raw, "api", "repos/"+name); err != nil {
		return repositoryRecord{}, err
	}
	if raw.NodeID == "" || raw.FullName == "" || raw.HTMLURL == "" {
		return repositoryRecord{}, errors.New("incomplete GitHub repository identity")
	}
	return repositoryRecord{raw.NodeID, raw.FullName, raw.HTMLURL}, nil
}

func (p *Publisher) actor(ctx context.Context) (string, error) {
	var raw struct {
		Login string `json:"login"`
	}
	if err := p.ghJSON(ctx, &raw, "api", "user"); err != nil {
		return "", err
	}
	if raw.Login == "" {
		return "", errors.New("GitHub actor login is empty")
	}
	return raw.Login, nil
}

type pullRequest struct {
	ID                  string `json:"id"`
	URL                 string `json:"url"`
	Body                string `json:"body"`
	State               string `json:"state"`
	HeadRefName         string `json:"headRefName"`
	HeadRefOID          string `json:"headRefOid"`
	BaseRefName         string `json:"baseRefName"`
	BaseRefOID          string `json:"baseRefOid"`
	MergedAt            string `json:"mergedAt"`
	Number              int    `json:"number"`
	IsDraft             bool   `json:"isDraft"`
	IsCrossRepository   bool   `json:"isCrossRepository"`
	HeadRepositoryOwner struct {
		Login string `json:"login"`
	} `json:"headRepositoryOwner"`
	MergedBy struct {
		Login string `json:"login"`
	} `json:"mergedBy"`
	MergeCommit struct {
		OID string `json:"oid"`
	} `json:"mergeCommit"`
}

func (p *Publisher) reconcilePR(ctx context.Context, intent domain.PublicationIntent, body string) (pullRequest, error) {
	prs, err := p.listPRs(ctx, intent)
	if err != nil {
		return pullRequest{}, err
	}
	if len(prs) > 1 {
		return pullRequest{}, port.Permanent(errors.New("multiple PRs match the Marshal branch"))
	}
	if len(prs) == 1 {
		pr := prs[0]
		if !prBelongsToIntent(pr, intent) {
			return pullRequest{}, port.Permanent(errors.New("existing PR head repository does not match the Marshal target"))
		}
		if !strings.Contains(pr.Body, intent.Marker) {
			return pullRequest{}, port.Permanent(errors.New("existing PR branch is not owned by this Marshal run"))
		}
		if !pr.IsDraft || pr.State != "OPEN" {
			return pullRequest{}, port.Permanent(errors.New("existing Marshal PR is not an open draft"))
		}
		bodyPath, err := writeBody(body)
		if err != nil {
			return pullRequest{}, err
		}
		defer os.Remove(bodyPath)
		if _, err := p.gh(ctx, "pr", "edit", strconv.Itoa(pr.Number), "--repo", intent.Repository, "--title", title(intent), "--body-file", bodyPath); err != nil {
			return pullRequest{}, err
		}
		return p.viewPR(ctx, intent.Repository, strconv.Itoa(pr.Number))
	}
	bodyPath, err := writeBody(body)
	if err != nil {
		return pullRequest{}, err
	}
	defer os.Remove(bodyPath)
	_, createErr := p.gh(ctx, "pr", "create", "--repo", intent.Repository, "--draft", "--no-maintainer-edit", "--base", intent.BaseBranch, "--head", intent.HeadBranch, "--title", title(intent), "--body-file", bodyPath)
	var listErr error
	for attempt := 0; attempt < 10; attempt++ {
		prs, listErr = p.listPRs(ctx, intent)
		if listErr == nil && len(prs) == 1 && strings.Contains(prs[0].Body, intent.Marker) {
			return p.viewPR(ctx, intent.Repository, strconv.Itoa(prs[0].Number))
		}
		if listErr == nil && len(prs) > 1 {
			return pullRequest{}, port.Permanent(errors.New("multiple PRs appeared while reconciling creation"))
		}
		if attempt < 9 && p.reconcileDelay > 0 {
			timer := time.NewTimer(p.reconcileDelay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return pullRequest{}, ctx.Err()
			case <-timer.C:
			}
		}
	}
	cause := errors.Join(createErr, listErr)
	if cause == nil {
		cause = errors.New("created PR was not observable before the reconciliation deadline")
	}
	return pullRequest{}, fmt.Errorf("ambiguous PR creation: %w", cause)
}

func (p *Publisher) listPRs(ctx context.Context, intent domain.PublicationIntent) ([]pullRequest, error) {
	var result []pullRequest
	if _, _, err := parseGitHubRepository(intent.RemoteURL); err != nil {
		return nil, err
	}
	err := p.ghJSON(ctx, &result, "pr", "list", "--repo", intent.Repository, "--state", "all", "--head", intent.HeadBranch, "--limit", "100", "--json", prViewFields)
	return result, err
}

// prViewFields is the frozen gh pr view/list --json field set; it carries the
// ADR 0026 merge facts (mergedAt, mergedBy, mergeCommit) and baseRefOid in
// addition to the publication identity fields.
const prViewFields = "id,number,url,isDraft,state,headRefName,headRefOid,headRepositoryOwner,isCrossRepository,baseRefName,baseRefOid,mergedAt,mergedBy,mergeCommit,body"

func (p *Publisher) viewPR(ctx context.Context, repository, number string) (pullRequest, error) {
	var result pullRequest
	err := p.ghJSON(ctx, &result, "pr", "view", number, "--repo", repository, "--json", prViewFields)
	return result, err
}

func prBelongsToIntent(pr pullRequest, intent domain.PublicationIntent) bool {
	owner, _, err := parseGitHubRepository(intent.RemoteURL)
	return err == nil && !pr.IsCrossRepository && pr.HeadRepositoryOwner.Login == owner && pr.HeadRefName == intent.HeadBranch && pr.HeadRefOID == intent.CommitSHA && pr.BaseRefName == intent.BaseBranch
}

func prMatchesPublication(pr pullRequest, publication domain.PublicationRecord) bool {
	owner, _, ok := strings.Cut(publication.Repository.NameWithOwner, "/")
	if !ok || pr.ID != publication.Request.ID || pr.IsCrossRepository ||
		pr.HeadRepositoryOwner.Login != owner || pr.HeadRefName != publication.HeadBranch || pr.HeadRefOID != publication.HeadSHA ||
		pr.BaseRefName != publication.BaseBranch || !strings.Contains(pr.Body, publication.Marker) {
		return false
	}
	// Issue #25: before merge the PR must be OPEN/Draft; after a maintainer
	// merges outside Marshal the PR node is MERGED and keeps the authoritative
	// head/base OIDs and merge commit. Both states bind the same identity.
	return (pr.IsDraft && pr.State == "OPEN") || pr.State == "MERGED"
}

func (p *Publisher) remoteHead(ctx context.Context, askpass, remoteURL, branch string) (string, error) {
	output, err := p.git(ctx, askpass, "ls-remote", "--heads", remoteURL, "refs/heads/"+branch)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(output))
	if len(fields) == 0 {
		return "", nil
	}
	if len(fields) != 2 || !regexp.MustCompile(`^[0-9a-f]{40,64}$`).MatchString(fields[0]) {
		return "", errors.New("malformed remote branch observation")
	}
	return fields[0], nil
}

func (p *Publisher) push(ctx context.Context, askpass string, intent domain.PublicationIntent) error {
	_, err := p.git(ctx, askpass, "push", "--porcelain", "--no-force", intent.RemoteURL, intent.CommitSHA+":refs/heads/"+intent.HeadBranch)
	return err
}

func (p *Publisher) writeAskPass(directory string) (string, error) {
	path := filepath.Join(directory, "askpass")
	script := "#!/bin/sh\ncase \"$1\" in\n*Username*) printf '%s\\n' x-access-token ;;\n*Password*) exec \"" + strings.ReplaceAll(p.ghPath, "\"", "\\\"") + "\" auth token ;;\n*) exit 1 ;;\nesac\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		return "", err
	}
	return path, nil
}

func (p *Publisher) git(ctx context.Context, askpass string, args ...string) ([]byte, error) {
	commandCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	command := exec.CommandContext(commandCtx, p.gitPath, args...)
	command.Dir = p.repositoryRoot
	command.Env = append(baseEnvironment(), "GH_CONFIG_DIR="+p.configDir, "GIT_ASKPASS="+askpass, "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=credential.helper", "GIT_CONFIG_VALUE_0=", "GIT_CONFIG_KEY_1=core.hooksPath", "GIT_CONFIG_VALUE_1=/dev/null")
	output, err := command.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git %s failed: %w", args[0], err)
	}
	return output, nil
}

func (p *Publisher) gh(ctx context.Context, args ...string) ([]byte, error) {
	commandCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	command := exec.CommandContext(commandCtx, p.ghPath, args...)
	command.Dir = p.repositoryRoot
	command.Env = append(baseEnvironment(), "GH_CONFIG_DIR="+p.configDir, "GH_PROMPT_DISABLED=1", "NO_COLOR=1")
	output, err := command.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("gh %s failed: %w", args[0], err)
	}
	return output, nil
}

func (p *Publisher) ghAllowPending(ctx context.Context, args ...string) ([]byte, error) {
	commandCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	command := exec.CommandContext(commandCtx, p.ghPath, args...)
	command.Dir = p.repositoryRoot
	command.Env = append(baseEnvironment(), "GH_CONFIG_DIR="+p.configDir, "GH_PROMPT_DISABLED=1", "NO_COLOR=1")
	output, err := command.CombinedOutput()
	if err != nil {
		var exitErr *exec.ExitError
		if !(errors.As(err, &exitErr) && exitErr.ExitCode() == 8) {
			return nil, fmt.Errorf("gh %s failed: %w", args[0], err)
		}
	}
	return output, nil
}

func (p *Publisher) ghJSON(ctx context.Context, target any, args ...string) error {
	output, err := p.gh(ctx, args...)
	if err != nil {
		return err
	}
	if len(output) > 4<<20 {
		return errors.New("GitHub CLI output exceeds limit")
	}
	return json.Unmarshal(output, target)
}

func parseGitHubRepository(remote string) (string, string, error) {
	const prefix = "https://github.com/"
	if !strings.HasPrefix(remote, prefix) || strings.Contains(remote, "?") || strings.Contains(remote, "#") {
		return "", "", errors.New("only canonical HTTPS GitHub remotes are supported")
	}
	path := strings.TrimSuffix(strings.TrimPrefix(remote, prefix), ".git")
	parts := strings.Split(path, "/")
	valid := regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	if len(parts) != 2 || !valid.MatchString(parts[0]) || !valid.MatchString(parts[1]) {
		return "", "", errors.New("invalid GitHub repository URL")
	}
	return parts[0], parts[1], nil
}

func ParseRepositoryURL(remote string) (string, error) {
	owner, name, err := parseGitHubRepository(remote)
	if err != nil {
		return "", err
	}
	return owner + "/" + name, nil
}

func renderBody(intent domain.PublicationIntent) string {
	return fmt.Sprintf("## 目标\n\n%s\n\n## 验证\n\n- Verification digest: `%s`\n- Evidence digest: `%s`\n- Snapshot digest: `%s`\n\n## 风险与回滚\n\n这是 Draft PR；Marshal 不执行 merge。\n\n## 来源信息\n\n- Task: `%s`\n- Run: `%s`\n- Base: `%s`\n- Head: `%s`\n\n%s\n", intent.Summary, intent.VerificationDigest, intent.EvidenceDigest, intent.SnapshotDigest, intent.TaskID, intent.RunID, intent.BaseSHA, intent.CommitSHA, intent.Marker)
}

func pullRequestTitle(summary string) string {
	const limit = 240
	runes := []rune(strings.TrimSpace(summary))
	if len(runes) > limit {
		runes = runes[:limit]
	}
	return string(runes)
}

func title(intent domain.PublicationIntent) string {
	if value := pullRequestTitle(intent.Summary); value != "" {
		return value
	}
	return "Marshal: " + intent.TaskID
}

func writeBody(body string) (string, error) {
	file, err := os.CreateTemp("", "marshal-pr-body-*.md")
	if err != nil {
		return "", err
	}
	name := file.Name()
	if err = file.Chmod(0o600); err == nil {
		_, err = file.WriteString(body)
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(name)
		return "", err
	}
	return name, nil
}

func baseEnvironment() []string {
	var result []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if key == "HOME" || key == "PATH" || key == "LANG" || key == "LC_ALL" || key == "TMPDIR" {
			result = append(result, entry)
		}
	}
	return result
}
