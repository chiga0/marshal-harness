package selfidentity

import (
	"errors"
	"reflect"

	"github.com/chiga0/marshal-harness/internal/evidencebinding"
)

const LocalApplicabilitySchema = "marshal.local-dogfood-environment-binding.v2"

// LocalApplicabilityV2 is the closed ADR 0051 ordinary-user projection. The
// frozen Policy is its only producer; Run and evidence objects only copy it.
type LocalApplicabilityV2 = evidencebinding.ExecutionApplicabilityV2

func ApplicabilityForObservation(observation LocalSelfIdentityObservationV2) (LocalApplicabilityV2, error) {
	if err := ValidateObservation(observation); err != nil {
		return LocalApplicabilityV2{}, err
	}
	return LocalApplicabilityV2{
		SchemaVersion: LocalApplicabilitySchema, SelfProfile: LocalProfile,
		ActivationDigest: observation.ActivationDigest, IdentitySubjectDigest: observation.IdentitySubjectDigest,
		Assurance: "ordinary-user", Execution: "workspace-write", Production: false, Publication: "none",
	}, nil
}

func ValidateApplicability(applicability LocalApplicabilityV2, observation LocalSelfIdentityObservationV2) error {
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
func ValidateApplicabilityShape(applicability LocalApplicabilityV2) error {
	if applicability.SchemaVersion != LocalApplicabilitySchema || applicability.SelfProfile != LocalProfile ||
		applicability.ActivationDigest == "" || applicability.IdentitySubjectDigest == "" ||
		applicability.Assurance != "ordinary-user" || applicability.Execution != "workspace-write" ||
		applicability.Production || applicability.Publication != "none" {
		return errors.New("local applicability projection is invalid")
	}
	return nil
}

func SameApplicability(left, right LocalApplicabilityV2) error {
	if left.SchemaVersion != LocalApplicabilitySchema || left.SelfProfile != LocalProfile ||
		left.Assurance != "ordinary-user" || left.Execution != "workspace-write" || left.Production || left.Publication != "none" ||
		!reflect.DeepEqual(left, right) {
		return errors.New("local applicability projection differs")
	}
	return nil
}
