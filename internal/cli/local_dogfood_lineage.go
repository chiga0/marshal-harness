package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"

	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/planning"
	"github.com/chiga0/marshal-harness/internal/runstore"
	"github.com/chiga0/marshal-harness/internal/selfidentity"
	"github.com/chiga0/marshal-harness/internal/verification"
	"golang.org/x/sys/unix"
)

func freshLocalDogfoodObservation(commandClass string) (selfidentity.LocalSelfIdentityObservationV2, error) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		return selfidentity.LocalSelfIdentityObservationV2{}, localPhaseRejected()
	}
	build := localBuildInfo()
	observation, err := selfidentity.Admit(os.Getenv(selfidentity.ActivationEnv), commandClass, workingDirectory,
		selfidentity.BuildIdentity{SourceHead: build.Commit, SelfProfile: build.SelfProfile}, localNow())
	if err != nil {
		return selfidentity.LocalSelfIdentityObservationV2{}, localPhaseRejected()
	}
	return observation, nil
}

func localPhaseRejected() error {
	return &selfidentity.GateError{ReasonCode: selfidentity.ReasonCrossProfileEvidence}
}

func prepareLocalVerificationBinding(_ context.Context, lease *runstore.Lease, state domain.RunState, entry *selfidentity.LocalSelfIdentityObservationV2, validator *contract.Validator) (*verification.LocalSelfIdentityInput, error) {
	if entry == nil {
		return nil, nil
	}
	if lease == nil || state.CurrentAttemptID == "" || validator == nil {
		return nil, localPhaseRejected()
	}
	policyData, err := runstore.ReadFileUnderLease(lease, 2<<20, "policy-snapshot.json")
	if err != nil {
		return nil, localPhaseRejected()
	}
	applicability, err := planning.LocalDogfoodApplicability(policyData, validator, entry)
	if err != nil || applicability == nil {
		return nil, localPhaseRejected()
	}
	fresh, err := freshLocalDogfoodObservation(selfidentity.CommandTaskVerify)
	if err != nil || selfidentity.SameSubject(*entry, fresh) != nil || planning.ValidateLocalDogfoodEnvironmentBinding(policyData, validator, &fresh) != nil {
		return nil, localPhaseRejected()
	}
	attempt, err := runstore.OpenDirectoryUnderLease(lease, "attempts", state.CurrentAttemptID)
	if err != nil {
		return nil, localPhaseRejected()
	}
	defer attempt.Close()
	dispatch, err := selfidentity.ReadPhaseObservationIn(attempt, "local-self-identity-dispatch.json")
	if err != nil {
		return nil, localPhaseRejected()
	}
	ingress, err := selfidentity.ReadPhaseObservationIn(attempt, "local-self-identity-ingress.json")
	if err != nil || selfidentity.SameSubject(dispatch, ingress) != nil || selfidentity.SameSubject(ingress, fresh) != nil {
		return nil, localPhaseRejected()
	}
	if err := validateLocalAttemptLineage(lease, state.CurrentAttemptID, attempt, validator, dispatch, ingress); err != nil {
		return nil, localPhaseRejected()
	}
	stored, err := selfidentity.PersistVersionedPhaseObservation(attempt, "verification", fresh)
	if err != nil {
		return nil, localPhaseRejected()
	}
	return &verification.LocalSelfIdentityInput{Applicability: *applicability, Dispatch: dispatch, Ingress: ingress, Verification: stored}, nil
}

func validateLocalAttemptLineage(lease *runstore.Lease, attemptID string, attempt *runstore.BoundDirectory, validator *contract.Validator, dispatch, ingress selfidentity.LocalSelfIdentityObservationV2) error {
	events, truncated, err := runstore.ReadEventsUnderLease(lease)
	if err != nil || truncated {
		return errors.New("local Attempt journal is invalid")
	}
	sealedLineage, err := validateLocalAttemptEvents(events, attemptID, dispatch.ObservationDigest, ingress.ObservationDigest)
	if err != nil {
		return err
	}
	if sealedLineage {
		if _, requestErr := runstore.ReadFileInDirectory(attempt, "worker-request.json", 2<<20); !errors.Is(requestErr, unix.ENOENT) {
			return errors.New("local sealed lineage carries a legacy WorkerRequest")
		}
		return attempt.Recheck()
	}
	requestData, err := runstore.ReadFileInDirectory(attempt, "worker-request.json", 2<<20)
	if err != nil || validator.Validate(domain.KindWorkerRequest, requestData) != nil {
		return errors.New("local WorkerRequest is invalid")
	}
	var request struct {
		Binding *selfidentity.LocalSelfIdentityBindingV2 `json:"localSelfIdentityBinding"`
	}
	if json.Unmarshal(requestData, &request) != nil || request.Binding == nil || selfidentity.ValidateBinding(*request.Binding, dispatch) != nil {
		return errors.New("local WorkerRequest binding is invalid")
	}
	return attempt.Recheck()
}

func validateLocalAttemptEvents(events []domain.RunEvent, attemptID, dispatchDigest, ingressDigest string) (bool, error) {
	legacyStartedIndex, legacyCompletedIndex := -1, -1
	sealedStartedIndex, sealedCompletedIndex := -1, -1
	for index := range events {
		event := events[index]
		if event.AttemptID != attemptID {
			continue
		}
		switch event.Type {
		case "run.start-outcome":
			if sealedStartedIndex >= 0 || event.Actor == nil || event.Actor.Type != "system" || event.Actor.ID != "marshal-run-start-projector" ||
				event.StateFrom != domain.StateReady || event.StateTo != domain.StateRunning ||
				localPayloadString(event.Payload, "protocolRevision") != "run-start-outcome/v2" ||
				localPayloadString(event.Payload, "dispatchObservationDigest") != dispatchDigest {
				return false, errors.New("local sealed run.start-outcome binding is invalid")
			}
			sealedStartedIndex = index
		case "worker.started":
			if legacyStartedIndex >= 0 || event.Actor == nil || event.Actor.Type != "system" || event.Actor.ID != "marshal-worker-runner" || localPayloadString(event.Payload, "dispatchObservationDigest") != dispatchDigest {
				return false, errors.New("local worker.started binding is invalid")
			}
			legacyStartedIndex = index
		case "worker.completed":
			if event.Actor == nil || event.Actor.Type != "system" {
				return false, errors.New("local worker.completed actor is invalid")
			}
			switch event.Actor.ID {
			case "marshal-worker-runner":
				if legacyCompletedIndex >= 0 || localPayloadString(event.Payload, "dispatchObservationDigest") != dispatchDigest || localPayloadString(event.Payload, "ingressObservationDigest") != ingressDigest {
					return false, errors.New("local worker.completed binding is invalid")
				}
				legacyCompletedIndex = index
			case "marshal-production-runtime":
				if sealedCompletedIndex >= 0 || event.StateFrom != domain.StateRunning || event.StateTo != domain.StateVerifying || localPayloadString(event.Payload, "dispatchObservationDigest") != dispatchDigest || localPayloadString(event.Payload, "ingressObservationDigest") != ingressDigest {
					return false, errors.New("local sealed worker.completed binding is invalid")
				}
				sealedCompletedIndex = index
			default:
				return false, errors.New("local worker.completed producer is invalid")
			}
		}
	}
	legacyLineage := legacyStartedIndex >= 0 || legacyCompletedIndex >= 0
	sealedLineage := sealedStartedIndex >= 0 || sealedCompletedIndex >= 0
	if legacyLineage == sealedLineage {
		return false, errors.New("local Attempt lineage is mixed or missing")
	}
	if sealedLineage {
		if sealedStartedIndex < 0 || sealedCompletedIndex != sealedStartedIndex+1 {
			return false, errors.New("local sealed event lineage is not adjacent")
		}
		return true, nil
	}
	if legacyStartedIndex < 0 || legacyCompletedIndex != legacyStartedIndex+1 {
		return false, errors.New("local worker event lineage is not adjacent")
	}
	return false, nil
}

func prepareLocalReviewBinding(_ context.Context, lease *runstore.Lease, state domain.RunState, entry *selfidentity.LocalSelfIdentityObservationV2, validator *contract.Validator, report verification.Report, manifest verification.ArtifactManifest, create bool) (*selfidentity.LocalReviewBindingV2, error) {
	if entry == nil {
		if report.LocalSelfIdentityBinding != nil || manifest.LocalSelfIdentityBinding != nil {
			return nil, localPhaseRejected()
		}
		return nil, nil
	}
	if lease == nil || state.CurrentAttemptID == "" || report.LocalSelfIdentityBinding == nil || manifest.LocalSelfIdentityBinding == nil || !reflect.DeepEqual(report.LocalSelfIdentityBinding, manifest.LocalSelfIdentityBinding) {
		return nil, localPhaseRejected()
	}
	policyData, err := runstore.ReadFileUnderLease(lease, 2<<20, "policy-snapshot.json")
	if err != nil {
		return nil, localPhaseRejected()
	}
	applicability, err := planning.LocalDogfoodApplicability(policyData, validator, entry)
	if err != nil || applicability == nil {
		return nil, localPhaseRejected()
	}
	fresh, err := freshLocalDogfoodObservation(selfidentity.CommandTaskReview)
	if err != nil || selfidentity.SameSubject(*entry, fresh) != nil || planning.ValidateLocalDogfoodEnvironmentBinding(policyData, validator, &fresh) != nil {
		return nil, localPhaseRejected()
	}
	attempt, err := runstore.OpenDirectoryUnderLease(lease, "attempts", state.CurrentAttemptID)
	if err != nil {
		return nil, localPhaseRejected()
	}
	defer attempt.Close()
	dispatch, err := selfidentity.ReadPhaseObservationIn(attempt, "local-self-identity-dispatch.json")
	if err != nil {
		return nil, localPhaseRejected()
	}
	ingress, err := selfidentity.ReadPhaseObservationIn(attempt, "local-self-identity-ingress.json")
	if err != nil || validateLocalAttemptLineage(lease, state.CurrentAttemptID, attempt, validator, dispatch, ingress) != nil {
		return nil, localPhaseRejected()
	}
	verificationObservation, err := selfidentity.ReadVersionedPhaseObservation(attempt, "verification", report.LocalSelfIdentityBinding.VerificationObservationDigest)
	if err != nil || selfidentity.ValidateVerificationBinding(*report.LocalSelfIdentityBinding, state.CurrentAttemptID, *applicability, dispatch, ingress, verificationObservation) != nil || selfidentity.SameSubject(verificationObservation, fresh) != nil {
		return nil, localPhaseRejected()
	}
	verificationBindingDigest, err := selfidentity.DigestVerificationBinding(*report.LocalSelfIdentityBinding)
	if err != nil || !verificationEventBinds(lease, verificationBindingDigest) {
		return nil, localPhaseRejected()
	}
	packet, exists, packetErr := readExistingLocalReviewPacket(lease, validator)
	if packetErr != nil {
		return nil, localPhaseRejected()
	}
	if exists {
		if packet.LocalSelfIdentityBinding == nil {
			return nil, localPhaseRejected()
		}
		stored, readErr := selfidentity.ReadVersionedPhaseObservation(attempt, fmt.Sprintf("review-%d", state.ReviewRound), packet.LocalSelfIdentityBinding.ReviewObservationDigest)
		if readErr != nil || selfidentity.SameSubject(stored, fresh) != nil {
			return nil, localPhaseRejected()
		}
		expected, buildErr := selfidentity.BuildReviewBinding(state.CurrentAttemptID, state.ReviewRound, *report.LocalSelfIdentityBinding, stored)
		if buildErr != nil || !reflect.DeepEqual(expected, *packet.LocalSelfIdentityBinding) {
			return nil, localPhaseRejected()
		}
		return &expected, nil
	}
	if !create {
		return nil, localPhaseRejected()
	}
	reviewObservation, err := selfidentity.PersistVersionedPhaseObservation(attempt, fmt.Sprintf("review-%d", state.ReviewRound), fresh)
	if err != nil {
		return nil, localPhaseRejected()
	}
	binding, err := selfidentity.BuildReviewBinding(state.CurrentAttemptID, state.ReviewRound, *report.LocalSelfIdentityBinding, reviewObservation)
	if err != nil {
		return nil, localPhaseRejected()
	}
	return &binding, nil
}

func readExistingLocalReviewPacket(lease *runstore.Lease, validator *contract.Validator) (domain.ReviewPacket, bool, error) {
	data, err := runstore.ReadFileUnderLease(lease, 8<<20, "review-packet.json")
	if errors.Is(err, unix.ENOENT) {
		return domain.ReviewPacket{}, false, nil
	}
	if err != nil || validator.Validate(domain.KindReviewPacket, data) != nil {
		return domain.ReviewPacket{}, true, errors.New("existing local ReviewPacket is invalid")
	}
	var packet domain.ReviewPacket
	if json.Unmarshal(data, &packet) != nil {
		return domain.ReviewPacket{}, true, errors.New("existing local ReviewPacket is malformed")
	}
	exact, marshalErr := json.MarshalIndent(packet, "", "  ")
	if marshalErr != nil || !bytes.Equal(append(exact, '\n'), data) {
		return domain.ReviewPacket{}, true, errors.New("existing local ReviewPacket is not canonical repository spelling")
	}
	return packet, true, nil
}

func verificationEventBinds(lease *runstore.Lease, bindingDigest string) bool {
	events, truncated, err := runstore.ReadEventsUnderLease(lease)
	if err != nil || truncated {
		return false
	}
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		if event.Type != "verification.completed" {
			continue
		}
		return event.Actor != nil && event.Actor.Type == "system" && event.Actor.ID == "marshal-verifier" && localPayloadString(event.Payload, "localSelfIdentityBindingDigest") == bindingDigest
	}
	return false
}

func localPayloadString(payload map[string]any, key string) string {
	value, _ := payload[key].(string)
	return value
}
