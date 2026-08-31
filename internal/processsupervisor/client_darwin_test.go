//go:build darwin

package processsupervisor

import "testing"

func TestSupervisorCommandStartsInIndependentSession(t *testing.T) {
	command := newSupervisorCommand("/fixed/bin/marshal", nil, nil)
	if command.SysProcAttr == nil || !command.SysProcAttr.Setsid {
		t.Fatal("fixed-image Supervisor would inherit the transient CLI terminal session")
	}
	if len(command.Env) != 0 || len(command.ExtraFiles) != 2 {
		t.Fatalf("supervisor launch boundary drifted: env=%v extraFiles=%d", command.Env, len(command.ExtraFiles))
	}
}
