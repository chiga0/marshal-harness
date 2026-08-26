package selfidentity

import (
	"errors"
	"reflect"

	"github.com/chiga0/marshal-harness/internal/evidencebinding"
)

const LocalApplicabilitySchema = "marshal.local-dogfood-environment-binding.v1"

// LocalApplicabilityV1 is the closed ADR 0051 ordinary-user projection. The
// frozen Policy is its only producer; Run and evidence objects only copy it.
type LocalApplicabilityV1 = evidencebinding.ExecutionApplicabilityV1

func ApplicabilityForObservation(observation LocalSelfIdentityObservationV1) (LocalApplicabilityV1, error) {
	if err := ValidateObservation(observation); err != nil {
		return LocalApplicabilityV1{}, err
	}
	return LocalApplicabilityV1{
		SchemaVersion: LocalApplicabilitySchema, SelfProfile: LocalProfile,
		ActivationDigest: observation.ActivationDigest, IdentitySubjectDigest: observation.IdentitySubjectDigest,
		Assurance: "ordinary-user", Execution: "workspace-write", Production: false, Publication: "none",
	}, nil
}

func ValidateApplicability(applicability LocalApplicabilityV1, observation LocalSelfIdentityObservationV1) error {
	if err := ValidateApplicabilityShape(applicability); err != nil {
		return err
	}
	expected, err := ApplicabilityForObservation(observation)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(applicability, expected) {
		return errors.New("local applicability does not match the current identity")
	}
	return nil
}

// ValidateApplicabilityShape validates the closed ordinary-user projection
// without treating the projection as authority for an executable identity.
func ValidateApplicabilityShape(applicability LocalApplicabilityV1) error {
	if applicability.SchemaVersion != LocalApplicabilitySchema || applicability.SelfProfile != LocalProfile ||
		applicability.ActivationDigest == "" || applicability.IdentitySubjectDigest == "" ||
		applicability.Assurance != "ordinary-user" || applicability.Execution != "workspace-write" ||
		applicability.Production || applicability.Publication != "none" {
		return errors.New("local applicability projection is invalid")
	}
	return nil
}

func SameApplicability(left, right LocalApplicabilityV1) error {
	if left.SchemaVersion != LocalApplicabilitySchema || left.SelfProfile != LocalProfile ||
		left.Assurance != "ordinary-user" || left.Execution != "workspace-write" || left.Production || left.Publication != "none" ||
		!reflect.DeepEqual(left, right) {
		return errors.New("local applicability projection differs")
	}
	return nil
}
