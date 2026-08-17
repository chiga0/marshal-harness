// ADR 0032 independent SCMMerger port inside the Publication trust domain.
// It reuses the frozen Publisher-side credential path (MARSHAL_GH_PATH /
// MARSHAL_GH_CONFIG_DIR) and never exposes admin, force, bypass,
// branch-delete, close or auto-merge queue operations.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/port"
)

// Merger is the ADR 0032 credentialed merge executor. It is a distinct type
// from Publisher (the interface is separated), but shares the identical
// credential resolution path so only the Publication trust domain ever holds
// GitHub credentials.
type Merger struct {
	ghPath, configDir, repositoryRoot string
	validator                         *contract.Validator
}

const maxGHOutputBytes = 4 << 20

type cappedOutput struct {
	mu sync.Mutex
	bytes.Buffer
	limit int
}

func (w *cappedOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.Buffer.Len()+len(p) > w.limit {
		return 0, errors.New("GitHub CLI output exceeds limit")
	}
	return w.Buffer.Write(p)
}
func (w *cappedOutput) data() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.Buffer.Bytes()...)
}
func (w *cappedOutput) text() string { return string(w.data()) }

// NewMerger returns the concrete SCMMerger. It resolves the gh binary, gh
// config dir and repository to their real absolute paths exactly like
// Publisher.New, so the credential identity digest derives from the same
// resolved facts.
func NewMerger(ghPath, configDir, repositoryRoot string, validator *contract.Validator) (*Merger, error) {
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
	if info, statErr := os.Stat(realGH); statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return nil, errors.New("GitHub CLI must be an executable regular file")
	}
	realConfig, err := filepath.EvalSymlinks(configDir)
	if err != nil {
		return nil, fmt.Errorf("resolve GitHub config dir: %w", err)
	}
	if info, statErr := os.Stat(realConfig); statErr != nil || !info.IsDir() {
		return nil, errors.New("GitHub config dir must exist")
	}
	realRepository, err := filepath.EvalSymlinks(repositoryRoot)
	if err != nil {
		return nil, err
	}
	if info, statErr := os.Stat(realRepository); statErr != nil || !info.IsDir() {
		return nil, errors.New("repository must be an existing directory")
	}
	snapshot, err := snapshotExecutable(realRepository, realGH)
	if err != nil {
		return nil, err
	}
	return &Merger{ghPath: snapshot, configDir: realConfig, repositoryRoot: realRepository, validator: validator}, nil
}

func snapshotExecutable(repositoryRoot, source string) (string, error) {
	data, err := os.ReadFile(source)
	if err != nil {
		return "", err
	}
	if len(data) == 0 || len(data) > 256<<20 {
		return "", errors.New("GitHub CLI executable snapshot size is invalid")
	}
	digest := strings.TrimPrefix(canonical.DigestBytes(data), "sha256:")
	directory := filepath.Join(repositoryRoot, ".marshal", "exec-snapshots")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(directory, "gh-"+digest)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
	if errors.Is(err, os.ErrExist) {
		existing, readErr := os.ReadFile(path)
		if readErr != nil || canonical.DigestBytes(existing) != canonical.DigestBytes(data) {
			return "", errors.New("GitHub CLI executable snapshot conflicts")
		}
		return path, nil
	}
	if err != nil {
		return "", err
	}
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

// BindsExpectedHead reports that the GitHub merge API mechanically binds the
// expected head SHA (the REST "sha" parameter) into the merge request, so
// the provider satisfies the ADR 0032 §4 atomicity requirement.
func (m *Merger) BindsExpectedHead() bool { return true }

// SecurityDomainID returns the SCMMerger actor-side composite security domain
// (ADR 0018 §10): tenantNamespace=local, trustDomainKind=publication,
// isolationDomainId=scm-merger, canonicalized and digested. It is a
// mechanical, secret-free declaration: admission freezes it into the intent
// and every mutation re-checks it against the frozen value, so the actual
// mutation actor is provably the one the intent bound.
func (m *Merger) SecurityDomainID() string {
	securityDomain := authority.SecurityDomainId{TenantNamespace: "local", TrustDomainKind: authority.TrustDomainKindPublication, IsolationDomainId: "scm-merger"}
	digest, _ := securityDomain.Digest()
	return digest
}

// ReadyForReview marks the intent-bound Draft PR ready for review. It is
// idempotent: a PR that is already ready is observed as success. The
// credential pre-flight gate re-observes the authenticated principal and
// credential-resolution identity and fails closed on any drift.
func (m *Merger) ReadyForReview(ctx context.Context, intent domain.SCMMergeIntent) error {
	if err := m.requireIntent(ctx, intent); err != nil {
		return err
	}
	if _, err := m.gh(ctx, "pr", "ready", strconv.Itoa(intent.PRNumber), "--repo", intent.RepositoryRef); err != nil {
		return classifyMergeError(err)
	}
	return nil
}

// Merge executes the controlled merge with expectedHeadOid mechanically
// bound (the REST "sha" field) and intent.MergeMethod applied. The
// credential pre-flight gate runs before the merge request exactly as it does
// before ready.
func (m *Merger) Merge(ctx context.Context, intent domain.SCMMergeIntent) error {
	if err := m.requireIntent(ctx, intent); err != nil {
		return err
	}
	method, err := mergeMethodFlag(intent.MergeMethod)
	if err != nil {
		return port.Permanent(err)
	}
	endpoint := "repos/" + intent.RepositoryRef + "/pulls/" + strconv.Itoa(intent.PRNumber) + "/merge"
	_, err = m.gh(ctx, "api", "--method", "PUT", endpoint, "-f", "merge_method="+method, "-f", "sha="+intent.HeadOid)
	return classifyMergeError(err)
}

// ObserveCredentialIdentity observes the current authenticated merge executor
// principal and the canonical credential-resolution identity digest. The
// digest input is the (gh binary resolved path, gh config dir resolved path,
// principal) tuple only; no token or secret enters either value.
func (m *Merger) ObserveCredentialIdentity(ctx context.Context) (string, string, error) {
	var raw struct {
		Login string `json:"login"`
	}
	if err := m.ghJSON(ctx, &raw, "api", "user"); err != nil {
		return "", "", err
	}
	if raw.Login == "" {
		return "", "", errors.New("GitHub actor login is empty")
	}
	principal := "github-login:" + raw.Login
	return principal, credentialIdentityDigest(m.ghPath, m.configDir, principal), nil
}

// ObserveTarget observes the fresh pre-merge PR facts bound to the intent
// (ADR 0032 §2, §5). It is strictly read-only and never mutates the remote.
func (m *Merger) ObserveTarget(ctx context.Context, intent domain.SCMMergeIntent) (domain.SCMMergeTarget, error) {
	if err := validateMergeTargetIntent(intent); err != nil {
		return domain.SCMMergeTarget{}, err
	}
	pr, err := m.viewPR(ctx, intent.RepositoryRef, strconv.Itoa(intent.PRNumber))
	if err != nil {
		return domain.SCMMergeTarget{}, err
	}
	var state string
	switch pr.State {
	case "OPEN":
		state = domain.MergeTargetStateOpen
	case "MERGED":
		state = domain.MergeTargetStateMerged
	case "CLOSED":
		state = domain.MergeTargetStateClosed
	default:
		return domain.SCMMergeTarget{}, port.Permanent(fmt.Errorf("remote PR reports an unknown state %q", pr.State))
	}
	return domain.SCMMergeTarget{
		Repository:    intent.RepositoryRef,
		PRNumber:      intent.PRNumber,
		HeadOid:       pr.HeadRefOID,
		BaseBranch:    pr.BaseRefName,
		BaseOid:       pr.BaseRefOID,
		Draft:         pr.IsDraft,
		State:         state,
		MarkerPresent: strings.Contains(pr.Body, mergeMarker(intent.TaskID, intent.RunID)),
	}, nil
}

// requireIntent validates the sealed intent and runs the credential
// pre-flight gate before every mutation (ADR 0032 §4): the security domain,
// authenticated principal and credential-resolution identity must each bind
// the frozen intent values, otherwise the mutation fails closed with zero
// remote side effect.
func (m *Merger) requireIntent(ctx context.Context, intent domain.SCMMergeIntent) error {
	if err := intent.Validate(); err != nil {
		return port.Permanent(err)
	}
	if m.SecurityDomainID() != intent.MergerSecurityDomainID {
		return port.Permanent(fmt.Errorf("%w: security domain does not bind the merge intent", port.ErrMergeIdentityMismatch))
	}
	principal, digest, err := m.ObserveCredentialIdentity(ctx)
	if err != nil {
		return err
	}
	if principal != intent.ExpectedMergedBy || digest != intent.MergerCredentialIdentity {
		return port.Permanent(fmt.Errorf("%w: credential identity does not bind the merge intent", port.ErrMergeIdentityMismatch))
	}
	return nil
}

func validateMergeTargetIntent(intent domain.SCMMergeIntent) error {
	if intent.RepositoryRef == "" || intent.PRNumber < 1 {
		return port.Permanent(errors.New("merge target observation requires a bound repository and PR number"))
	}
	return nil
}

func mergeMethodFlag(method string) (string, error) {
	switch method {
	case domain.MergeMethodMerge:
		return "merge", nil
	case domain.MergeMethodSquash:
		return "squash", nil
	case domain.MergeMethodRebase:
		return "rebase", nil
	default:
		return "", fmt.Errorf("merge method %q is outside the closed enumeration", method)
	}
}

// mergeMarker derives the run-unique PR marker from the intent's task/run
// identity. It is the same deterministic form the Publisher embeds in every
// controlled PR body.
func mergeMarker(taskID, runID string) string {
	return "<!-- marshal task=" + taskID + " run=" + runID + " -->"
}

// credentialIdentityDigest is the canonical digest of the credential
// resolution identity tuple (gh binary resolved path, gh config dir resolved
// path, principal). It is one-way and never includes credential material.
func credentialIdentityDigest(ghPath, configDir, principal string) string {
	document, err := json.Marshal(map[string]string{
		"ghConfigDir": configDir,
		"ghPath":      ghPath,
		"principal":   principal,
	})
	if err != nil {
		return ""
	}
	digest, err := canonical.DigestJSON(document)
	if err != nil {
		return ""
	}
	return digest
}

func (m *Merger) viewPR(ctx context.Context, repository, number string) (pullRequest, error) {
	var result pullRequest
	err := m.ghJSON(ctx, &result, "pr", "view", number, "--repo", repository, "--json", prViewFields)
	return result, err
}

func (m *Merger) gh(ctx context.Context, args ...string) ([]byte, error) {
	commandCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	command := exec.CommandContext(commandCtx, m.ghPath, args...)
	command.Dir = m.repositoryRoot
	command.Env = append(baseEnvironment(), "GH_CONFIG_DIR="+m.configDir, "GH_PROMPT_DISABLED=1", "NO_COLOR=1")
	output := &cappedOutput{limit: maxGHOutputBytes}
	command.Stdout, command.Stderr = output, output
	err := command.Run()
	if err != nil {
		return nil, &ghCommandError{operation: args[0], cause: err, output: output.text()}
	}
	return output.data(), nil
}

// ghCommandError retains provider output only for local typed classification.
// Error deliberately excludes output because provider diagnostics can contain
// repository details and must never become a persisted Outcome or event.
type ghCommandError struct {
	operation string
	cause     error
	output    string
}

func (err *ghCommandError) Error() string {
	return fmt.Sprintf("gh %s failed: %v", err.operation, err.cause)
}
func (err *ghCommandError) Unwrap() error { return err.cause }

func classifyMergeError(err error) error {
	if err == nil || port.IsPermanent(err) {
		return err
	}
	var commandErr *ghCommandError
	if !errors.As(err, &commandErr) {
		return err
	}
	diagnostic := strings.ToLower(commandErr.output)
	if containsAny(diagnostic, "http 401", "http 403", "status 401", "status 403", "resource not accessible", "permission denied", "forbidden", "authentication required") {
		return port.Permanent(fmt.Errorf("%w: GitHub rejected the credentialed operation", port.ErrMergePermissionDenied))
	}
	if containsAny(diagnostic, "http 405", "http 409", "http 422", "status 405", "status 409", "status 422", "not mergeable", "merge conflict", "head branch was modified", "base branch was modified") {
		return port.Permanent(fmt.Errorf("%w: GitHub rejected the frozen merge target", port.ErrMergeNotMergeable))
	}
	return err
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}

func (m *Merger) ghJSON(ctx context.Context, target any, args ...string) error {
	output, err := m.gh(ctx, args...)
	if err != nil {
		return err
	}
	return json.Unmarshal(output, target)
}
