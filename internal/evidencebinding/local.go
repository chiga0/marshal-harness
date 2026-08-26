// Package evidencebinding owns provider-neutral wire projections that bind
// Core evidence to one execution environment. Profile-specific observers may
// construct these values, but generic domain records do not import them.
package evidencebinding

type ExecutionApplicabilityV1 struct {
	SchemaVersion         string `json:"schemaVersion"`
	SelfProfile           string `json:"selfProfile"`
	ActivationDigest      string `json:"activationDigest"`
	IdentitySubjectDigest string `json:"identitySubjectDigest"`
	Assurance             string `json:"assurance"`
	Execution             string `json:"execution"`
	Production            bool   `json:"production"`
	Publication           string `json:"publication"`
}

type VerificationIdentityBindingV1 struct {
	SchemaVersion                 string                   `json:"schemaVersion"`
	SelfProfile                   string                   `json:"selfProfile"`
	ActivationDigest              string                   `json:"activationDigest"`
	IdentitySubjectDigest         string                   `json:"identitySubjectDigest"`
	AttemptID                     string                   `json:"attemptId"`
	DispatchObservationDigest     string                   `json:"dispatchObservationDigest"`
	IngressObservationDigest      string                   `json:"ingressObservationDigest"`
	VerificationObservationDigest string                   `json:"verificationObservationDigest"`
	Applicability                 ExecutionApplicabilityV1 `json:"applicability"`
}

type ReviewIdentityBindingV1 struct {
	SchemaVersion                 string                   `json:"schemaVersion"`
	SelfProfile                   string                   `json:"selfProfile"`
	ActivationDigest              string                   `json:"activationDigest"`
	IdentitySubjectDigest         string                   `json:"identitySubjectDigest"`
	AttemptID                     string                   `json:"attemptId"`
	ReviewRound                   uint                     `json:"reviewRound"`
	VerificationBindingDigest     string                   `json:"verificationBindingDigest"`
	VerificationObservationDigest string                   `json:"verificationObservationDigest"`
	ReviewObservationDigest       string                   `json:"reviewObservationDigest"`
	Applicability                 ExecutionApplicabilityV1 `json:"applicability"`
}
