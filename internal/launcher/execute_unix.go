//go:build !windows

package launcher

import (
	"os"
	"syscall"
)

func executeEnvelope(envelope Envelope) error {
	if err := os.Chdir(envelope.WorkingDirectory); err != nil {
		return err
	}
	argv := append([]string{envelope.Executable.Path}, envelope.Arguments...)
	return syscall.Exec(envelope.Executable.Path, argv, envelope.Environment)
}
