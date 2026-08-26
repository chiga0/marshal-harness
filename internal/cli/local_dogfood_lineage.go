package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/planning"
	"github.com/chiga0/marshal-harness/internal/runstore"
	"github.com/chiga0/marshal-harness/internal/selfidentity"
	"github.com/chiga0/marshal-harness/internal/verification"
)

func freshLocalDogfoodObservation(commandClass string) (selfidentity.LocalSelfIdentityObservationV1, error) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		return selfidentity.LocalSelfIdentityObservationV1{}, &selfidentity.GateError{ReasonCode: selfidentity.ReasonCrossProfileEvidence}
	}
	build := localBuildInfo()
	observation, err := selfidentity.Admit(os.Getenv(selfidentity.ActivationEnv), commandClass, workingDirectory,
		selfidentity.BuildIdentity{SourceHead: build.Commit, SelfProfile: build.SelfProfile}, localNow())
	if err != nil {
		return selfidentity.LocalSelfIdentityObservationV1{}, &selfidentity.GateError{ReasonCode: selfidentity.ReasonCrossProfileEvidence}
	}
	return observation, nil
}

func localPhaseRejected() error {
	return &selfidentity.GateError{ReasonCode: selfidentity.ReasonCrossProfileEvidence}
}

func prepareLocalVerificationBinding(_ context.Context, stateRoot string, state domain.RunState, entry *selfidentity.LocalSelfIdentityObservationV1, validator *contract.Validator) (*selfidentity.LocalVerificationBindingV1, error) {
	if entry == nil {
		return nil, nil
	}
	if state.CurrentAttemptID == "" || validator == nil {
		return nil, localPhaseRejected()
	}
	runDirectory := filepath.Join(stateRoot, "runs", state.RunID)
	policyData, err := readInput(filepath.Join(runDirectory, "policy-snapshot.json"), nil)
	if err != nil || planning.ValidateLocalDogfoodEnvironmentBinding(policyData, validator, entry) != nil {
		return nil, localPhaseRejected()
	}
	fresh, err := freshLocalDogfoodObservation(selfidentity.CommandTaskVerify)
	if err != nil || selfidentity.SameSubject(*entry, fresh) != nil || planning.ValidateLocalDogfoodEnvironmentBinding(policyData, validator, &fresh) != nil {
		return nil, localPhaseRejected()
	}
	attemptDirectory := filepath.Join(runDirectory, "attempts", state.CurrentAttemptID)
	dispatch, err := selfidentity.ReadPhaseObservation(filepath.Join(attemptDirectory, "local-self-identity-dispatch.json"))
	if err != nil {
		return nil, localPhaseRejected()
	}
	ingress, err := selfidentity.ReadPhaseObservation(filepath.Join(attemptDirectory, "local-self-identity-ingress.json"))
	if err != nil || selfidentity.SameSubject(dispatch, ingress) != nil || selfidentity.SameSubject(ingress, fresh) != nil {
		return nil, localPhaseRejected()
	}
	if err := validateLocalAttemptLineage(stateRoot, state.RunID, state.CurrentAttemptID, attemptDirectory, validator, dispatch, ingress); err != nil {
		return nil, localPhaseRejected()
	}
	stored, err := selfidentity.LoadOrPersistPhaseObservation(attemptDirectory, "local-self-identity-verification.json", fresh)
	if err != nil {
		return nil, localPhaseRejected()
	}
	binding, err := selfidentity.BuildVerificationBinding(state.CurrentAttemptID, dispatch, ingress, stored)
	if err != nil {
		return nil, localPhaseRejected()
	}
	return &binding, nil
}

func validateLocalAttemptLineage(stateRoot, runID, attemptID, attemptDirectory string, validator *contract.Validator, dispatch, ingress selfidentity.LocalSelfIdentityObservationV1) error {
	requestData, err := os.ReadFile(filepath.Join(attemptDirectory, "worker-request.json"))
	if err != nil || validator.Validate(domain.KindWorkerRequest, requestData) != nil {
		return errors.New("local WorkerRequest is invalid")
	}
	var request struct {
		Binding *selfidentity.LocalSelfIdentityBindingV1 `json:"localSelfIdentityBinding"`
	}
	if json.Unmarshal(requestData, &request) != nil || request.Binding == nil ||
		selfidentity.ValidateBinding(*request.Binding, dispatch) != nil {
		return errors.New("local WorkerRequest binding is invalid")
	}
	events, truncated, err := runstore.New(stateRoot).ReadEvents(runID)
	if err != nil || truncated {
		return errors.New("local Attempt journal is invalid")
	}
	startedIndex, completedIndex := -1, -1
	for index := range events {
		event := events[index]
		if event.AttemptID != attemptID {
			continue
		}
		switch event.Type {
		case "worker.started":
			if startedIndex >= 0 || event.Actor == nil || event.Actor.Type != "system" || event.Actor.ID != "marshal-worker-runner" || localPayloadString(event.Payload, "dispatchObservationDigest") != dispatch.ObservationDigest {
				return errors.New("local worker.started binding is invalid")
			}
			startedIndex = index
		case "worker.completed":
			if completedIndex >= 0 || event.Actor == nil || event.Actor.Type != "system" || event.Actor.ID != "marshal-worker-runner" ||
				localPayloadString(event.Payload, "dispatchObservationDigest") != dispatch.ObservationDigest || localPayloadString(event.Payload, "ingressObservationDigest") != ingress.ObservationDigest {
				return errors.New("local worker.completed binding is invalid")
			}
			completedIndex = index
		}
	}
	if startedIndex < 0 || completedIndex != startedIndex+1 {
		return errors.New("local worker event lineage is not adjacent")
	}
	return nil
}

func prepareLocalReviewBinding(_ context.Context, stateRoot string, state domain.RunState, entry *selfidentity.LocalSelfIdentityObservationV1, validator *contract.Validator, report verification.Report, manifest verification.ArtifactManifest, create bool) (*selfidentity.LocalReviewBindingV1, error) {
	if entry == nil {
		if report.LocalSelfIdentityBinding != nil || manifest.LocalSelfIdentityBinding != nil {
			return nil, localPhaseRejected()
		}
		return nil, nil
	}
	if state.CurrentAttemptID == "" || report.LocalSelfIdentityBinding == nil || manifest.LocalSelfIdentityBinding == nil ||
		!reflect.DeepEqual(report.LocalSelfIdentityBinding, manifest.LocalSelfIdentityBinding) {
		return nil, localPhaseRejected()
	}
	runDirectory := filepath.Join(stateRoot, "runs", state.RunID)
	policyData, err := readInput(filepath.Join(runDirectory, "policy-snapshot.json"), nil)
	if err != nil || planning.ValidateLocalDogfoodEnvironmentBinding(policyData, validator, entry) != nil {
		return nil, localPhaseRejected()
	}
	fresh, err := freshLocalDogfoodObservation(selfidentity.CommandTaskReview)
	if err != nil || selfidentity.SameSubject(*entry, fresh) != nil || planning.ValidateLocalDogfoodEnvironmentBinding(policyData, validator, &fresh) != nil {
		return nil, localPhaseRejected()
	}
	attemptDirectory := filepath.Join(runDirectory, "attempts", state.CurrentAttemptID)
	dispatch, err := selfidentity.ReadPhaseObservation(filepath.Join(attemptDirectory, "local-self-identity-dispatch.json"))
	if err != nil {
		return nil, localPhaseRejected()
	}
	ingress, err := selfidentity.ReadPhaseObservation(filepath.Join(attemptDirectory, "local-self-identity-ingress.json"))
	if err != nil {
		return nil, localPhaseRejected()
	}
	if err := validateLocalAttemptLineage(stateRoot, state.RunID, state.CurrentAttemptID, attemptDirectory, validator, dispatch, ingress); err != nil {
		return nil, localPhaseRejected()
	}
	verificationObservation, err := selfidentity.ReadPhaseObservation(filepath.Join(attemptDirectory, "local-self-identity-verification.json"))
	if err != nil || selfidentity.ValidateVerificationBinding(*report.LocalSelfIdentityBinding, state.CurrentAttemptID, dispatch, ingress, verificationObservation) != nil || selfidentity.SameSubject(verificationObservation, fresh) != nil {
		return nil, localPhaseRejected()
	}
	verificationBindingDigest, err := selfidentity.DigestVerificationBinding(*report.LocalSelfIdentityBinding)
	if err != nil || !verificationEventBinds(stateRoot, state.RunID, verificationBindingDigest) {
		return nil, localPhaseRejected()
	}
	reviewPath := filepath.Join(attemptDirectory, fmt.Sprintf("local-self-identity-review-%d.json", state.ReviewRound))
	var reviewObservation selfidentity.LocalSelfIdentityObservationV1
	if create {
		reviewObservation, err = selfidentity.LoadOrPersistPhaseObservation(attemptDirectory, filepath.Base(reviewPath), fresh)
	} else {
		reviewObservation, err = selfidentity.ReadPhaseObservation(reviewPath)
	}
	if err != nil || selfidentity.SameSubject(reviewObservation, fresh) != nil {
		return nil, localPhaseRejected()
	}
	binding, err := selfidentity.BuildReviewBinding(state.CurrentAttemptID, state.ReviewRound, *report.LocalSelfIdentityBinding, reviewObservation)
	if err != nil {
		return nil, localPhaseRejected()
	}
	return &binding, nil
}

func verificationEventBinds(stateRoot, runID, bindingDigest string) bool {
	events, truncated, err := runstore.New(stateRoot).ReadEvents(runID)
	if err != nil || truncated {
		return false
	}
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		if event.Type != "verification.completed" {
			continue
		}
		return event.Actor != nil && event.Actor.Type == "system" && event.Actor.ID == "marshal-verifier" &&
			localPayloadString(event.Payload, "localSelfIdentityBindingDigest") == bindingDigest
	}
	return false
}

func localPayloadString(payload map[string]any, key string) string {
	value, _ := payload[key].(string)
	return value
}
