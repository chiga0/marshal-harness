package codex

// compiledCodexContractBinding derives every signed contract field from the
// constants and builders used by this binary. Human labels are versioned only
// to make intentional contract changes explicit in review; no authority input
// participates in these digests.
func compiledCodexContractBinding() (CodexContractBindingV1, error) {
	digest := func(value any) (string, error) { return canonicalDigest(value) }
	adapterDigest, err := digest(struct {
		AdapterID, AdapterVersion string
		Capabilities              map[string]any
	}{adapterID, adapterVersion, expectedCapabilities()})
	if err != nil {
		return CodexContractBindingV1{}, err
	}
	launcherDigest, err := digest(struct {
		Contract                string
		LauncherArgument        string
		WorktreeFD, TargetFD    int
		CloseWorktreeBeforeExec bool
		CloseTargetOnExec       bool
	}{"linux-execveat-sealed-memfd-ptrace-v1", codexLauncherArgument, launcherWorktreeFD, 7, true, true})
	if err != nil {
		return CodexContractBindingV1{}, err
	}
	profiles := []string{"workspace-write"}
	profileDigest, err := digest(profiles)
	if err != nil {
		return CodexContractBindingV1{}, err
	}
	argvDigest, err := digest([][]string{
		buildArgs("/proc/self/fd/3", "/proc/self/fd/4", ""),
		buildArgs("/proc/self/fd/3", "/proc/self/fd/4", "model-id"),
	})
	if err != nil {
		return CodexContractBindingV1{}, err
	}
	environmentDigest, err := digest(struct {
		InheritedKeys []string
		Fixed         []string
	}{
		[]string{"CODEX_HOME", "HOME", "LANG", "LC_*", "LOGNAME", "PATH", "SHELL", "TEMP", "TERM", "TMP", "TMPDIR", "USER", "XDG_CACHE_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME"},
		[]string{"CI=1", "GH_PROMPT_DISABLED=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0"},
	})
	if err != nil {
		return CodexContractBindingV1{}, err
	}
	eventDigest, err := digest(struct{ EventContract, ProtocolVersion string }{conformanceEventContract, codexProtocolVersion})
	if err != nil {
		return CodexContractBindingV1{}, err
	}
	permissionDigest, err := digest(struct{ Mode, NetworkOverride, Approval string }{codexPermissionMode, sandboxNetworkOverride, "never"})
	if err != nil {
		return CodexContractBindingV1{}, err
	}
	toolDigest, err := digest(struct {
		DeclaredToolsPolicy string
		ShellExpansion      bool
	}{"reject-non-empty", false})
	if err != nil {
		return CodexContractBindingV1{}, err
	}
	resultDigest, err := digest(struct {
		Schema, Source, Normalizer string
	}{"worker-result", "held-output-last-message-fd", "pi.NormalizeDeclaredWorkerResult"})
	if err != nil {
		return CodexContractBindingV1{}, err
	}
	outputDigest, err := digest(struct{ Result, Stderr, Version int }{maxResultBytes, stderrLimit, maxVersionBytes})
	if err != nil {
		return CodexContractBindingV1{}, err
	}
	nativeBudgetsDigest, err := digest([]string{})
	if err != nil {
		return CodexContractBindingV1{}, err
	}
	binding := CodexContractBindingV1{
		AdapterContractDigest: adapterDigest, LauncherBuildDigest: launcherDigest, ProfileDigest: profileDigest,
		ArgvMatrixDigest: argvDigest, EnvironmentDigest: environmentDigest, EventContractDigest: eventDigest,
		PermissionContractDigest: permissionDigest, ToolPolicyDigest: toolDigest, ResultContractDigest: resultDigest,
		OutputLimitDigest: outputDigest, NativeBudgetsDigest: nativeBudgetsDigest, ExecutionProfiles: profiles,
	}
	if err := binding.Validate(); err != nil {
		return CodexContractBindingV1{}, err
	}
	return binding, nil
}

func equalCodexContractBinding(left, right CodexContractBindingV1) bool {
	return left.AdapterContractDigest == right.AdapterContractDigest && left.LauncherBuildDigest == right.LauncherBuildDigest && left.ProfileDigest == right.ProfileDigest && left.ArgvMatrixDigest == right.ArgvMatrixDigest && left.EnvironmentDigest == right.EnvironmentDigest && left.EventContractDigest == right.EventContractDigest && left.PermissionContractDigest == right.PermissionContractDigest && left.ToolPolicyDigest == right.ToolPolicyDigest && left.ResultContractDigest == right.ResultContractDigest && left.OutputLimitDigest == right.OutputLimitDigest && left.NativeBudgetsDigest == right.NativeBudgetsDigest && equalStrings(left.ExecutionProfiles, right.ExecutionProfiles)
}
