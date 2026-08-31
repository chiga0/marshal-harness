package productionruntime

// AttemptLaunchIdentity is the precise reserved attempt identity handed to
// the injected argv builder. TaskID and RunID come from the durable READY Run
// projection; AttemptID is the creation-once reserved attempt id produced by
// ReserveAttempt. They are the only per-attempt variable inputs to the
// deterministic production argv and are passed to the builder only after
// ReserveAttempt and ensureAttemptLease have fixed the exact identity.
type AttemptLaunchIdentity struct {
	TaskID    string
	RunID     string
	AttemptID string
}

// AttemptLaunchArgv is the deterministic noninteractive production argv and
// prompt for one attempt, returned by the injected builder. Argv must be a
// complete executable argv whose first element is the canonical Node runtime
// and whose second element is the canonical Pi entrypoint, matching the
// frozen launch closure shape; the prompt is the single trailing positional
// argument.
type AttemptLaunchArgv struct {
	Argv   []string
	Prompt string
}

// AttemptLaunchArgvBuilder constructs the deterministic noninteractive
// production argv for one attempt from its precise reserved identity. It is
// injected by the fixed CLI (the only package allowed to import the pi
// adapter) so productionruntime never depends on adapter/pi. The builder
// must be pure: identical identity inputs produce identical argv bytes, so
// fresh and replay seal a byte-identical closure.
//
// Path B (existing-worktree binding) requires a non-nil builder and fails
// closed without one: NewCompositionLedger rejects a nil builder when path B
// inputs are present. Path A (staging provision) tolerates a nil builder and
// keeps the composition-time argv for backward compatibility; a non-nil
// builder replaces the argv exactly as on path B.
type AttemptLaunchArgvBuilder func(identity AttemptLaunchIdentity) (AttemptLaunchArgv, error)
