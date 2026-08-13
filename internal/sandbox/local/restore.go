package local

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/chiga0/marshal-harness/internal/sandbox"
)

// Restore implements sandbox.SandboxProvider on top of the frozen
// sandbox.PlanRestore semantics. The default is a replacement allocation
// with a fresh allocationId and a monotonically increasing generation; an
// in-place restore is permitted only when the caller confirms it AND this
// implementation re-verifies that no live process tree exists. The identity
// must carry the post-restore generation. After a successful restore the
// scope lease is resealed at the new generation and fencing token, so every
// stale handle replay fails closed, and writes against the previous
// (replaced) allocation are rejected.
func (r *LocalRunner) Restore(ctx context.Context, request sandbox.RestoreOperationRequest) (*sandbox.RestoreReceipt, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.enterOperation(sandbox.OperationRestore, request.Identity, false); err != nil {
		return nil, err
	}
	if strings.TrimSpace(request.PreviousAllocationId) == "" {
		return nil, fmt.Errorf("%w: previousAllocationId must be a non-empty string", sandbox.ErrInvalidRequest)
	}
	previous, ok := r.allocations[request.PreviousAllocationId]
	if !ok {
		return nil, fmt.Errorf("%w: %q", sandbox.ErrAllocationNotFound, request.PreviousAllocationId)
	}
	if previous.meta.State.IsTerminal() {
		return nil, fmt.Errorf("%w: %q is %s", sandbox.ErrAllocationNotActive, request.PreviousAllocationId, string(previous.meta.State))
	}
	if request.Identity.AllocationId != request.PreviousAllocationId && request.Identity.AllocationId != request.NextAllocationId {
		return nil, fmt.Errorf("%w: the identity must bind the previous or the next allocation", sandbox.ErrInvalidRequest)
	}
	if request.Identity.WorkloadRole != previous.role {
		r.appendDiagnostic(sandbox.OperationRestore, request.PreviousAllocationId, "cross-role restore rejected")
		return nil, fmt.Errorf("%w: cross-role allocation reuse rejected", sandbox.ErrInvalidRequest)
	}
	next, err := sandbox.PlanRestore(sandbox.RestoreRequest{
		Previous:         previous.meta,
		NextAllocationId: request.NextAllocationId,
		InPlaceConfirmed: request.InPlaceConfirmed,
	})
	if err != nil {
		return nil, err
	}
	if request.Identity.Generation != next.Generation {
		r.appendDiagnostic(sandbox.OperationRestore, request.PreviousAllocationId, fmt.Sprintf("restore identity carries generation %d, the post-restore generation is %d", request.Identity.Generation, next.Generation))
		return nil, fmt.Errorf("%w: the restore identity must carry the post-restore generation %d", sandbox.ErrStaleAllocationGeneration, next.Generation)
	}
	if request.InPlaceConfirmed && previous.liveProcess != nil {
		r.appendDiagnostic(sandbox.OperationRestore, request.PreviousAllocationId, "in-place restore re-verification failed: a live process tree exists")
		return nil, fmt.Errorf("%w: in-place restore re-verification failed: the previous process tree is still alive", sandbox.ErrRestoreRejected)
	}
	if err := sandbox.CheckSingleActive(r.allocationsInScope(next.RunId, next.AttemptId), next); err != nil {
		r.appendDiagnostic(sandbox.OperationRestore, next.AllocationId, "single-active check rejected the restore: "+err.Error())
		return nil, err
	}
	lease, err := r.sealScopeLease(request.Identity, next.AllocationId)
	if err != nil {
		return nil, err
	}
	next.State = sandbox.AllocationActive
	if request.InPlaceConfirmed {
		previous.meta = next
		appendLog(previous, fmt.Sprintf("restored in-place at generation %d", next.Generation))
	} else {
		if _, exists := r.allocations[next.AllocationId]; exists {
			return nil, fmt.Errorf("%w: allocation %q already exists", sandbox.ErrInvalidRequest, next.AllocationId)
		}
		nextDir := filepath.Join(r.root, "allocations", allocationDirName(next.AllocationId))
		if err := os.MkdirAll(nextDir, 0o700); err != nil {
			return nil, fmt.Errorf("local: restore: %w", err)
		}
		if err := copyDirectoryTree(previous.dir, nextDir); err != nil {
			_ = os.RemoveAll(nextDir)
			return nil, fmt.Errorf("local: restore: %w", err)
		}
		staged := make(map[string]string, len(previous.staged))
		for inputId, rel := range previous.staged {
			staged[inputId] = rel
		}
		previous.meta.State = sandbox.AllocationReplaced
		appendLog(previous, "replaced by "+next.AllocationId)
		_ = os.RemoveAll(previous.dir)
		r.allocations[next.AllocationId] = &allocation{
			meta:   next,
			role:   previous.role,
			dir:    nextDir,
			staged: staged,
		}
	}
	r.leases[scopeKey(next.RunId, next.AttemptId)] = lease
	if fault, active := r.matchFault(sandbox.OperationRestore); active && fault.Kind == FaultDropResponse {
		r.appendDiagnostic(sandbox.OperationRestore, next.AllocationId, "restore response dropped after the host side effect was applied")
		return nil, sandbox.ErrResponseLost
	}
	return &sandbox.RestoreReceipt{Allocation: next}, nil
}

// copyDirectoryTree copies every regular file under src into dst preserving
// relative paths, in deterministic sorted order.
func copyDirectoryTree(src, dst string) error {
	files, err := collectFiles(src)
	if err != nil {
		return err
	}
	for _, file := range files {
		content, readErr := os.ReadFile(filepath.Join(src, filepath.FromSlash(file.rel)))
		if readErr != nil {
			return readErr
		}
		target := filepath.Join(dst, filepath.FromSlash(file.rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(target, content, 0o600); err != nil {
			return err
		}
	}
	return nil
}
