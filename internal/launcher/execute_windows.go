//go:build windows

package launcher

import (
	"os"
	"os/exec"
)

func executeEnvelope(envelope Envelope) error {
	command := exec.Command(envelope.Executable.Path, envelope.Arguments...)
	command.Dir = envelope.WorkingDirectory
	command.Env = envelope.Environment
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	return command.Run()
}
