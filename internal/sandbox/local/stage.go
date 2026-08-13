package local

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/sandbox"
)

// Stage implements sandbox.SandboxProvider. Content-addressed inputs land on
// the real filesystem of the allocation directory: inline bytes are written
// to their relative target, locator inputs are copied from the bound store
// alias directory. The sha256 digest is recomputed once before consumption
// (a mismatch fails the attempt closed with ErrStageInputMismatch and no
// receipt) and once after consumption; the receipt carries the recomputed
// digests, never an echo of the declared digest.
func (r *LocalRunner) Stage(ctx context.Context, request sandbox.StageRequest) (*sandbox.StageReport, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.enterOperation(sandbox.OperationStage, request.Identity, true); err != nil {
		return nil, err
	}
	entry, err := r.resolveAllocation(sandbox.OperationStage, request.Identity, request.AllocationId, true)
	if err != nil {
		return nil, err
	}
	if err := sandbox.ValidateStageRequest(request.Inputs, entry.meta.AllowedStoreIds); err != nil {
		return nil, err
	}
	report := &sandbox.StageReport{Receipts: make([]sandbox.StageReceipt, 0, len(request.Inputs))}
	for _, input := range request.Inputs {
		target, targetErr := resolveStageTarget(entry.dir, input.InputId)
		if targetErr != nil {
			r.appendDiagnostic(sandbox.OperationStage, request.AllocationId, "stage target rejected: "+targetErr.Error())
			return nil, targetErr
		}
		content, contentErr := r.resolveContent(input)
		if contentErr != nil {
			return nil, contentErr
		}
		// Recompute before consumption: a mismatch fails the attempt closed
		// and produces no receipt.
		pre := sandbox.RecomputeSHA256(content)
		if pre != input.DeclaredSHA256 {
			entry.meta.State = sandbox.AllocationFailed
			appendLog(entry, "stage rejected: digest mismatch before consumption for "+input.InputId)
			r.appendDiagnostic(sandbox.OperationStage, request.AllocationId, "stage input digest mismatch detected before consumption for "+input.InputId)
			return nil, fmt.Errorf("%w: input %q", sandbox.ErrStageInputMismatch, input.InputId)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return nil, fmt.Errorf("local: stage: %w", err)
		}
		if err := os.WriteFile(target, content, 0o600); err != nil {
			return nil, fmt.Errorf("local: stage: %w", err)
		}
		readBack, err := os.ReadFile(target)
		if err != nil {
			return nil, fmt.Errorf("local: stage: %w", err)
		}
		// Recompute after consumption from the bytes actually on disk, so
		// the receipt can never be satisfied by echoing the declared digest.
		post := sandbox.RecomputeSHA256(readBack)
		rel, _ := filepath.Rel(entry.dir, target)
		entry.staged[input.InputId] = filepath.ToSlash(rel)
		appendLog(entry, "staged: "+input.InputId)
		report.Receipts = append(report.Receipts, sandbox.StageReceipt{
			InputId:               input.InputId,
			RecomputedSHA256:      pre,
			PostConsumptionSHA256: post,
			SizeBytes:             int64(len(content)),
		})
	}
	r.recordIntent(request.Identity, sandbox.OperationStage, authority.DispositionClassSandboxStage, request.AllocationId)
	return report, nil
}

// resolveStageTarget derives the on-disk target of one staged input and
// rejects every write target outside the allocation directory: absolute
// paths are rejected outright and relative paths must not traverse.
func resolveStageTarget(dir, inputId string) (string, error) {
	cleaned, err := validateRelativeTarget(inputId)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, cleaned), nil
}

// validateRelativeTarget fails closed on absolute paths and parent
// traversal, returning the cleaned relative path otherwise.
func validateRelativeTarget(rel string) (string, error) {
	if strings.TrimSpace(rel) == "" {
		return "", fmt.Errorf("%w: the write target must be a non-empty relative path", sandbox.ErrInvalidRequest)
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("%w: the write target must be a relative path, absolute paths are rejected", sandbox.ErrInvalidRequest)
	}
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if part == ".." {
			return "", fmt.Errorf("%w: the write target escapes the allocation directory", sandbox.ErrInvalidRequest)
		}
	}
	cleaned := filepath.Clean(filepath.FromSlash(rel))
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: the write target escapes the allocation directory", sandbox.ErrInvalidRequest)
	}
	return cleaned, nil
}

// resolveContent returns the bytes behind one stage input: inline bytes are
// copied, locator inputs are read from the bound store alias directory.
func (r *LocalRunner) resolveContent(input sandbox.StageInput) ([]byte, error) {
	if input.Locator == nil {
		return append([]byte(nil), input.Inline...), nil
	}
	path, err := r.storeObjectPath(input.Locator.StoreId, input.Locator.SHA256)
	if err != nil {
		return nil, err
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: store %q does not hold object %q", sandbox.ErrLocatorUnresolved, input.Locator.StoreId, input.Locator.SHA256)
	}
	return content, nil
}

// storeObjectPath derives the host path of one content-addressed store
// object: <root>/stores/<alias>/<digest-hex>.
func (r *LocalRunner) storeObjectPath(storeId, sha256Ref string) (string, error) {
	if err := validateStoreAliasShape(storeId); err != nil {
		return "", err
	}
	if !strings.HasPrefix(sha256Ref, sandbox.DigestPrefix) {
		return "", fmt.Errorf("%w: locator.sha256 must carry the %s digest prefix", sandbox.ErrInvalidLocator, sandbox.DigestPrefix)
	}
	return filepath.Join(r.root, "stores", storeId, strings.TrimPrefix(sha256Ref, sandbox.DigestPrefix)), nil
}

// SeedStore places deterministic content behind one store alias and digest
// so staged locators resolve against the real filesystem.
func (r *LocalRunner) SeedStore(storeId, sha256 string, content []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	path, err := r.storeObjectPath(storeId, sha256)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("local: seed store: %w", err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		return fmt.Errorf("local: seed store: %w", err)
	}
	return nil
}

// validateStoreAliasShape mirrors the closed shape rules of the sandbox
// package: an alias is a bound name, never a URL, path or credential
// carrier.
func validateStoreAliasShape(storeId string) error {
	if strings.TrimSpace(storeId) == "" {
		return fmt.Errorf("%w: storeId must be a non-empty artifact store alias", sandbox.ErrInvalidLocator)
	}
	if storeId != strings.TrimSpace(storeId) {
		return fmt.Errorf("%w: storeId must not carry surrounding whitespace", sandbox.ErrInvalidLocator)
	}
	if strings.ContainsAny(storeId, ":/?#\\@") {
		return fmt.Errorf("%w: storeId must be a bound alias, never an external URL, path or credential carrier", sandbox.ErrInvalidLocator)
	}
	return nil
}
