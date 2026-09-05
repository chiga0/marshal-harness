package resultingress

import (
	"errors"

	"github.com/chiga0/marshal-harness/internal/processsupervisor"
)

var (
	ErrPreparedExecutionNotTerminal = errors.New("resultingress: prepared execution is not terminal")
	ErrPreparedExecutionNotClosable = errors.New("resultingress: prepared execution is not ready to close")
)

// PreparedExecutionTerminalObservation is the exact durable Inspect outcome
// that a business process-terminal transition must cite. The caller maps the
// producer-owned state to ProcessTerminalKind; it must not synthesize another
// observation or outcome digest.
type PreparedExecutionTerminalObservation struct {
	Identity          AttemptIdentity
	OutcomeFactDigest string
	Evidence          SupervisorCommandEvidence
}

// PreparedExecutionClose is the exact durable Close outcome plus the
// producer-authenticated absence evidence required to construct
// ProcessSupervisorClosed. ResultIngress deliberately does not append that
// business lifecycle transition here.
type PreparedExecutionClose struct {
	Identity          AttemptIdentity
	OutcomeFactDigest string
	Evidence          SupervisorCommandEvidence
	Recovery          processsupervisor.CommittedCloseRecoveryEvidence
	RecoveryV2        *processsupervisor.CommittedCloseRecoveryEvidenceV2
}

func (c PreparedExecutionClose) SupervisorClosed(authority ProcessSupervisorCloseAuthority) (ProcessSupervisorClosed, error) {
	if c.RecoveryV2 != nil {
		if c.Recovery.Outcome.Command != "" || c.Evidence.ProtocolRevision != processsupervisor.DormantV2ProtocolContract().ProtocolRevision {
			return ProcessSupervisorClosed{}, ErrAttemptAuthorityConflict
		}
		return NewProcessSupervisorClosedFromRecoveryV2(authority, *c.RecoveryV2)
	}
	return NewProcessSupervisorClosedFromRecovery(authority, c.Recovery)
}
