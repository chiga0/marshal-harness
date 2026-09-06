package fixedcontrolplane

// requestStageError adds local diagnostic context only. It changes neither
// wire responses nor errors.Is classification and never formats its cause.
type requestStageError struct {
	stage string
	cause error
}

func (err *requestStageError) Error() string { return "fixedcontrolplane: request stage failed" }
func (err *requestStageError) Unwrap() error { return err.cause }

func atRequestStage(stage string, err error) error {
	if err == nil {
		return nil
	}
	return &requestStageError{stage: stage, cause: err}
}

// DiagnosticStage inspects one error node, not an unbounded error chain.
// Callers may walk their existing bounded chain and emit only this allowlist.
func DiagnosticStage(err error) string {
	value, ok := err.(*requestStageError)
	if !ok || value == nil {
		return ""
	}
	switch value.stage {
	case "client-dial", "client-write", "client-response", "client-operation",
		"client-recheck", "client-half-close", "server-read", "server-admission",
		"server-precheck", "server-dispatch", "server-postcheck", "server-response",
		"server-half-close":
		return value.stage
	default:
		return ""
	}
}
