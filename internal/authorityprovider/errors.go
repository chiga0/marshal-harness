package authorityprovider

import "fmt"

type SafeClass string

const (
	ClassOK        SafeClass = "ok"
	ClassPermanent SafeClass = "permanent"
	ClassTransient SafeClass = "transient"
	ClassReconcile SafeClass = "reconcile-required"
)

type SafeCode string

const (
	CodeOK                           SafeCode = "ok"
	CodePlatformUnsupported          SafeCode = "platform-unsupported"
	CodeProfileUnsupported           SafeCode = "profile-unsupported"
	CodePrincipalUnauthorized        SafeCode = "principal-unauthorized"
	CodeIdentityMismatch             SafeCode = "identity-mismatch"
	CodeBundleInvalid                SafeCode = "bundle-invalid"
	CodeBundleRollback               SafeCode = "bundle-rollback"
	CodeEvidenceInvalid              SafeCode = "evidence-invalid"
	CodeEvidenceRevoked              SafeCode = "evidence-revoked"
	CodeEvidenceExpired              SafeCode = "evidence-expired"
	CodeHostAttestationInvalid       SafeCode = "host-attestation-invalid"
	CodeIsolationUnavailable         SafeCode = "isolation-unavailable"
	CodeLaunchReceiptInvalid         SafeCode = "launch-receipt-invalid"
	CodeSecretBoundaryViolation      SafeCode = "secret-boundary-violation"
	CodeProviderBusy                 SafeCode = "provider-busy"
	CodeAnchorTemporarilyUnavailable SafeCode = "anchor-temporarily-unavailable"
	CodeBundleCommitAmbiguous        SafeCode = "bundle-commit-ambiguous"
	CodeLaunchOutcomeAmbiguous       SafeCode = "launch-outcome-ambiguous"
	CodeInternalFailClosed           SafeCode = "internal-fail-closed"
)

func (c SafeCode) Class() (SafeClass, bool) {
	switch c {
	case CodeOK:
		return ClassOK, true
	case CodeProviderBusy, CodeAnchorTemporarilyUnavailable:
		return ClassTransient, true
	case CodeBundleCommitAmbiguous, CodeLaunchOutcomeAmbiguous:
		return ClassReconcile, true
	case CodePlatformUnsupported, CodeProfileUnsupported, CodePrincipalUnauthorized, CodeIdentityMismatch, CodeBundleInvalid, CodeBundleRollback, CodeEvidenceInvalid, CodeEvidenceRevoked, CodeEvidenceExpired, CodeHostAttestationInvalid, CodeIsolationUnavailable, CodeLaunchReceiptInvalid, CodeSecretBoundaryViolation, CodeInternalFailClosed:
		return ClassPermanent, true
	default:
		return "", false
	}
}

type ProtocolError struct {
	Code    SafeCode
	message string
}

func (e *ProtocolError) Error() string { return "authorityprovider: " + e.message }
func protocolError(code SafeCode, message string) error {
	return &ProtocolError{Code: code, message: message}
}
func ErrorCode(err error) SafeCode {
	if e, ok := err.(*ProtocolError); ok {
		return e.Code
	}
	return CodeInternalFailClosed
}
func (c SafeCode) Validate() error {
	if _, ok := c.Class(); !ok {
		return fmt.Errorf("authorityprovider: unknown safe code")
	}
	return nil
}
