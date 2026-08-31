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
}
