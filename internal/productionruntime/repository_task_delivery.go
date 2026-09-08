package productionruntime

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/goal"
	"github.com/chiga0/marshal-harness/internal/resultingress"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

var taskDeliveryPaths = []string{"quote_api.py", "quote_client.py", "quote_delivery.json"}

// BuildTaskDeliveryArchive is a data-only deterministic encoder. Authority
// comes from the three accepted sources, never this function or ZIP metadata.
func BuildTaskDeliveryArchive(files map[string][]byte) ([]byte, []goal.TaskDeliveryFile, error) {
	fail := application.NewError("task-delivery", application.ReasonAuthorityConflict)
	if len(files) != len(taskDeliveryPaths) {
		return nil, nil, fail
	}
	var out bytes.Buffer
	writer := zip.NewWriter(&out)
	manifest := []goal.TaskDeliveryFile{}
	total := 0
	for _, name := range taskDeliveryPaths {
		data, ok := files[name]
		total += len(data)
		if !ok || len(data) == 0 || total > goal.MaxTaskDeliveryBytes {
			return nil, nil, fail
		}
		header := &zip.FileHeader{Name: name, Method: zip.Store, Modified: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)}
		header.SetMode(0644)
		entry, err := writer.CreateHeader(header)
		if err != nil {
			return nil, nil, err
		}
		if _, err = entry.Write(data); err != nil {
			return nil, nil, err
		}
		manifest = append(manifest, goal.TaskDeliveryFile{Path: name, SHA256: canonical.DigestBytes(data), Bytes: int64(len(data))})
	}
	if err := writer.Close(); err != nil {
		return nil, nil, err
	}
	if out.Len() > goal.MaxTaskDeliveryBytes {
		return nil, nil, fail
	}
	return out.Bytes(), manifest, nil
}

func deliveryManifest(outcome resultingress.TeamDeliveryOutcome, content []byte, files []goal.TaskDeliveryFile) goal.TaskDelivery {
	v := goal.TaskDelivery{GoalID: outcome.Outcome.GoalId, OutcomeFactDigest: outcome.FactDigest, PlanFactDigest: outcome.PlanFactDigest, IntegrationRunID: outcome.Integration.RunID, IntegrationBaseSHA: outcome.IntegrationBaseSHA, Files: files, ContentDigest: canonical.DigestBytes(content), ContentBytes: int64(len(content)), MediaType: "application/zip"}
	for _, source := range append(slices.Clone(outcome.Upstreams), outcome.Integration) {
		v.CandidateDigests = append(v.CandidateDigests, source.CandidateDigest)
		v.PatchDigests = append(v.PatchDigests, source.PatchDigest)
		v.DecisionDigests = append(v.DecisionDigests, source.DecisionDigest)
	}
	return v
}

func currentDeliveryOutcome(session *RepositorySession, current resultingress.TeamDeliveryOutcome) (resultingress.TeamDeliveryOutcome, error) {
	stored, found, err := session.ingress.ReadTeamOutcome(session.acquisition.Scope, current.Outcome.GoalId)
	if err != nil {
		return current, err
	}
	comparison := stored
	comparison.FactDigest = ""
	if !found || !reflect.DeepEqual(current, comparison) {
		return current, application.NewError("task-delivery", application.ReasonAuthorityConflict)
	}
	return stored, nil
}

// NextTaskDelivery is a hint, not permission. Long Git reconstruction runs
// outside the global writer lane; the producer rechecks owner and all sources.
func (session *RepositorySession) NextTaskDelivery(ctx context.Context) (taskID, runID string, err error) {
	if ctx == nil {
		return "", "", application.NewError("task-delivery", application.ReasonInvalidRequest)
	}
	borrow, err := session.borrow()
	if err != nil {
		return "", "", err
	}
	defer borrow.Close()
	err = (repositoryApprovedTeamVerifier{session: session}).WithCurrentApprovedTeam(ctx, session.acquisition, resultingress.TeamPlanApproval{}, func() error {
		plans, e := session.ingress.ListTeamPlans(session.acquisition.Scope)
		if e != nil {
			return e
		}
		for _, plan := range plans {
			if plan.Approval.TaskDraftDigest == "" {
				continue
			}
			outcome, done, e := session.ingress.ReadTeamOutcome(session.acquisition.Scope, plan.Revision.GoalId)
			if e != nil {
				return e
			}
			if !done {
				continue
			}
			_, exists, e := session.ingress.ReadTaskDelivery(session.acquisition.Scope, plan.Revision.GoalId)
			if e != nil {
				return e
			}
			if !exists {
				taskID, runID = plan.Revision.GoalId, outcome.Integration.RunID
				return nil
			}
		}
		return nil
	})
	return
}

func (session *RepositorySession) BuildTaskDelivery(ctx context.Context, taskID string) (result goal.TaskDelivery, resultErr error) {
	// Keep storage/readiness sentinels behind the application boundary. The
	// resident consumer depends on typed application outcomes, not RB1.
	defer func() {
		if errors.Is(resultErr, resultingress.ErrTeamOutcomeNotReady) {
			resultErr = application.NewError("task-delivery", application.ReasonTaskArtifactNotReady)
		} else if errors.Is(resultErr, runstore.ErrLeaseHeld) {
			resultErr = application.NewError("task-delivery", application.ReasonCapacityBusy)
		}
	}()
	if ctx == nil || domain.ValidateID(taskID) != nil {
		return goal.TaskDelivery{}, application.NewError("task-delivery", application.ReasonInvalidRequest)
	}
	borrow, err := session.borrow()
	if err != nil {
		return goal.TaskDelivery{}, err
	}
	defer borrow.Close()
	if session.teamDeliveryExporter == nil {
		return goal.TaskDelivery{}, application.NewError("task-delivery", application.ReasonCompositionIncomplete)
	}
	plan, found, err := session.ingress.ReadTeamPlan(session.acquisition.Scope, taskID)
	if err != nil {
		return goal.TaskDelivery{}, err
	}
	if !found || plan.Approval.TaskDraftDigest == "" {
		return goal.TaskDelivery{}, application.NewError("task-delivery", application.ReasonAuthorityConflict)
	}
	var original goal.TeamInputs
	if json.Unmarshal(plan.Inputs, &original) != nil {
		return goal.TaskDelivery{}, application.NewError("task-delivery", application.ReasonAuthorityConflict)
	}
	var observed resultingress.TeamDeliveryOutcome
	var creation resultingress.TeamRunCreationState
	var patches [][]byte
	var finalPatch []byte
	verifier := repositoryCompletedTeamVerifier{session: session}
	err = verifier.withCurrentCompletedTeamInputs(ctx, session.acquisition, plan.Approval, taskID, plan.FactDigest, func(current resultingress.TeamDeliveryOutcome, upstreams []AcceptedTeamInput, final AcceptedTeamInput, frozen resultingress.TeamRunCreationState, _ *runstore.Lease) error {
		var e error
		observed, e = currentDeliveryOutcome(session, current)
		if e != nil {
			return e
		}
		creation = frozen
		for _, value := range upstreams {
			patches = append(patches, bytes.Clone(value.Candidate.Patch))
		}
		finalPatch = bytes.Clone(final.Candidate.Patch)
		return nil
	})
	if err != nil {
		return goal.TaskDelivery{}, err
	}
	base := creation.Integration
	files, err := session.teamDeliveryExporter(ctx, original.BaseSHA, base.InputsDigest, base.TreeSHA, base.CommitSHA, patches, finalPatch, slices.Clone(taskDeliveryPaths))
	if err != nil {
		return goal.TaskDelivery{}, err
	}
	content, fileManifest, err := BuildTaskDeliveryArchive(files)
	if err != nil {
		return goal.TaskDelivery{}, err
	}
	manifest := deliveryManifest(observed, content, fileManifest)
	if manifest.Validate() != nil {
		return goal.TaskDelivery{}, application.NewError("task-delivery", application.ReasonAuthorityConflict)
	}
	return session.ingress.RecordTaskDelivery(ctx, repositoryTaskDeliveryVerifier{session: session, plan: plan, manifest: manifest, content: content}, session.acquisition, taskID)
}

type repositoryTaskDeliveryVerifier struct {
	session  *RepositorySession
	plan     resultingress.TeamPlanState
	manifest goal.TaskDelivery
	content  []byte
}

func (v repositoryTaskDeliveryVerifier) WithCurrentTaskDelivery(ctx context.Context, owner resultingress.ControlOwnerAcquisition, taskID string, consume func(goal.TaskDelivery) error) error {
	return (repositoryCompletedTeamVerifier{session: v.session}).withCurrentCompletedTeamInputs(ctx, owner, v.plan.Approval, taskID, v.plan.FactDigest, func(current resultingress.TeamDeliveryOutcome, _ []AcceptedTeamInput, _ AcceptedTeamInput, _ resultingress.TeamRunCreationState, lease *runstore.Lease) error {
		stored, err := currentDeliveryOutcome(v.session, current)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(v.manifest, deliveryManifest(stored, v.content, v.manifest.Files)) {
			return application.NewError("task-delivery", application.ReasonAuthorityConflict)
		}
		directory, err := runstore.OpenDirectoryUnderLease(lease)
		if err != nil {
			return err
		}
		defer directory.Close()
		name := taskDeliveryFileName(v.manifest)
		if err := runstore.WriteFileInDirectory(directory, name, v.content, 0600); err != nil {
			return err
		}
		read, err := runstore.ReadFileUnderLease(lease, goal.MaxTaskDeliveryBytes, name)
		if err != nil || !bytes.Equal(read, v.content) {
			return application.NewError("task-delivery", application.ReasonAuthorityConflict)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return consume(v.manifest)
	})
}

func taskDeliveryFileName(v goal.TaskDelivery) string {
	return "task-delivery-" + strings.TrimPrefix(v.ContentDigest, "sha256:") + ".zip"
}

func (session *RepositorySession) ReadTaskArtifact(ctx context.Context, taskID string) (result application.TaskArtifact, resultErr error) {
	if ctx == nil || domain.ValidateID(taskID) != nil {
		return result, application.NewError("task-artifact", application.ReasonInvalidRequest)
	}
	borrow, err := session.borrow()
	if err != nil {
		return result, err
	}
	defer borrow.Close()
	_, found, err := session.ingress.ReadTaskDraft(session.acquisition.Scope, taskID)
	if err != nil {
		return result, err
	}
	if !found {
		return result, application.NewError("task-artifact", application.ReasonTaskNotFound)
	}
	manifest, found, err := session.ingress.ReadTaskDelivery(session.acquisition.Scope, taskID)
	if err != nil {
		return result, err
	}
	if !found {
		return result, application.NewError("task-artifact", application.ReasonTaskArtifactNotReady)
	}
	plan, found, err := session.ingress.ReadTeamPlan(session.acquisition.Scope, taskID)
	if err != nil {
		return result, err
	}
	if !found || manifest.Validate() != nil {
		return result, application.NewError("task-artifact", application.ReasonAuthorityConflict)
	}
	err = (repositoryCompletedTeamVerifier{session: session}).withCurrentCompletedTeamInputs(ctx, session.acquisition, plan.Approval, taskID, plan.FactDigest, func(current resultingress.TeamDeliveryOutcome, _ []AcceptedTeamInput, _ AcceptedTeamInput, _ resultingress.TeamRunCreationState, lease *runstore.Lease) error {
		stored, e := currentDeliveryOutcome(session, current)
		if e != nil {
			return e
		}
		content, e := runstore.ReadFileUnderLease(lease, goal.MaxTaskDeliveryBytes, taskDeliveryFileName(manifest))
		if e != nil {
			return e
		}
		expected := deliveryManifest(stored, content, manifest.Files)
		expected.FactDigest = manifest.FactDigest
		if !reflect.DeepEqual(expected, manifest) {
			return application.NewError("task-artifact", application.ReasonAuthorityConflict)
		}
		result = application.TaskArtifact{Manifest: manifest, Content: content}
		return nil
	})
	if err != nil {
		return application.TaskArtifact{}, err
	}
	return result, nil
}
