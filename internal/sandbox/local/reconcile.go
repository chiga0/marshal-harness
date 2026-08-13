package local

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/sandbox"
)

// Reconcile implements sandbox.SandboxProvider: it reconciles the real host
// state (filesystem versus the allocation registry) for one (runId,
// attemptId) scope. Drift fails closed: the report carries DriftDetected,
// one authority.ReconcileRecord is constructed (record only — the Local
// provider never writes an authority ledger; runtime wiring is M9) and an
// error is returned.
func (r *LocalRunner) Reconcile(ctx context.Context, request sandbox.ReconcileRequest) (*sandbox.ReconcileReport, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.enterOperation(sandbox.OperationReconcile, request.Identity, true); err != nil {
		return nil, err
	}
	if strings.TrimSpace(request.RunId) == "" || strings.TrimSpace(request.AttemptId) == "" {
		return nil, fmt.Errorf("%w: runId and attemptId must be non-empty strings", sandbox.ErrInvalidRequest)
	}
	if request.Identity.RunId != request.RunId || request.Identity.AttemptId != request.AttemptId {
		return nil, fmt.Errorf("%w: the identity does not bind the requested scope", sandbox.ErrInvalidRequest)
	}
	metas := r.allocationsInScope(request.RunId, request.AttemptId)
	var currentGeneration int64
	for _, meta := range metas {
		if meta.Generation > currentGeneration {
			currentGeneration = meta.Generation
		}
	}
	report := &sandbox.ReconcileReport{
		ActiveAllocationIds: []string{},
		OrphanAllocationIds: []string{},
	}
	for _, meta := range metas {
		if meta.State != sandbox.AllocationActive {
			continue
		}
		if meta.Generation == currentGeneration {
			report.ActiveAllocationIds = append(report.ActiveAllocationIds, meta.AllocationId)
		} else {
			report.OrphanAllocationIds = append(report.OrphanAllocationIds, meta.AllocationId)
		}
	}
	fsDrift := r.filesystemDrift(request.RunId, request.AttemptId)
	report.DriftDetected = fsDrift || len(report.ActiveAllocationIds) > 1 || len(report.OrphanAllocationIds) > 0
	if report.DriftDetected {
		record := r.buildReconcileRecord(request.RunId, request.AttemptId, fsDrift)
		r.reconcileLog = append(r.reconcileLog, record)
		reason := "allocation bookkeeping drift observed against the host"
		if fsDrift {
			reason = "filesystem state disagrees with the allocation registry"
		}
		r.appendDiagnostic(sandbox.OperationReconcile, request.Identity.AllocationId, "reconcile drift detected fail closed: "+reason)
		return report, fmt.Errorf("local: reconcile drift detected fail closed for scope %s/%s: %s", request.RunId, request.AttemptId, reason)
	}
	return report, nil
}

// filesystemDrift reports whether the allocation directories observed on the
// host disagree with the registry: an active or failed allocation must keep
// its directory, and a terminal replaced/terminated allocation must not.
func (r *LocalRunner) filesystemDrift(runId, attemptId string) bool {
	drift := false
	for _, entry := range r.allocations {
		if entry.meta.RunId != runId || entry.meta.AttemptId != attemptId {
			continue
		}
		expectDir := entry.meta.State == sandbox.AllocationActive || entry.meta.State == sandbox.AllocationFailed
		_, statErr := os.Stat(entry.dir)
		exists := statErr == nil
		if exists != expectDir {
			r.appendDiagnostic(sandbox.OperationReconcile, entry.meta.AllocationId, fmt.Sprintf("filesystem drift: directory presence %t against state %s", exists, string(entry.meta.State)))
			drift = true
		}
	}
	return drift
}

func (r *LocalRunner) buildReconcileRecord(runId, attemptId string, filesystemDrift bool) authority.ReconcileRecord {
	scope := scopeKey(runId, attemptId)
	intentDigest := sandbox.RecomputeSHA256([]byte("local-reconcile" + "\x00" + "no-intent" + "\x00" + scope))
	for index := len(r.intents) - 1; index >= 0; index-- {
		if r.intents[index].AuthorityNamespaceId.AuthorityScopeId != scope {
			continue
		}
		if digest, err := r.intents[index].Digest(); err == nil {
			intentDigest = digest
		}
		break
	}
	observation := authority.ObservationPartiallyApplied
	if filesystemDrift {
		observation = authority.ObservationConflict
	}
	return authority.ReconcileRecord{
		AuthorityNamespaceId: authority.AuthorityNamespaceId{
			TenantNamespace:  "marshal-harness",
			ControlPlaneId:   "local-sandbox-provider",
			AuthorityScopeId: scope,
		},
		Observation:   observation,
		Decision:      authority.DecisionBlock,
		IntentDigest:  intentDigest,
		ReceiptDigest: sandbox.RecomputeSHA256([]byte("local-reconcile" + "\x00" + scope + "\x00" + string(observation))),
	}
}

// ReconcileRecords returns a copy of the ReconcileRecord observations built
// by drift adjudication.
func (r *LocalRunner) ReconcileRecords() []authority.ReconcileRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]authority.ReconcileRecord(nil), r.reconcileLog...)
}
