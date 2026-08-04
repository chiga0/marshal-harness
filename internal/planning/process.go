package planning

import (
	"context"
	"io"
	"os/exec"
	"time"
)

// commandReapBound bounds how long the runner waits for a killed process
// group to be reaped. On Unix the kill covers the entire group, so the bound
// is never reached in practice; it only guarantees the runner itself cannot
// hang forever after a context deadline or cancellation.
const commandReapBound = 5 * time.Second

// runDirectCommand runs argv[0] with argv[1:] directly, never through a
// shell, with env and stdout as given. Stderr is discarded. It does not use
// exec.CommandContext: Start and Wait are driven explicitly, and the context
// is observed via select, so there is no watcher goroutine racing the kill
// path. On Unix the command runs in its own process group and, when ctx is
// canceled or its deadline expires, the entire process group is terminated
// and reaped with a bounded wait. Context errors are returned as the
// context's own error; start failures and exit failures are returned as run
// errors unchanged.
func runDirectCommand(ctx context.Context, argv []string, env []string, stdout io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	command := exec.Command(argv[0], argv[1:]...)
	command.Env = env
	command.Stdout = stdout
	command.Stderr = io.Discard
	isolateCommandGroup(command)

	if err := command.Start(); err != nil {
		return err
	}

	waitDone := make(chan error, 1)
	go func() {
		waitDone <- command.Wait()
	}()

	select {
	case runErr := <-waitDone:
		return runErr
	case <-ctx.Done():
		killCommandTree(command)
		select {
		case <-waitDone:
		case <-time.After(commandReapBound):
		}
		return ctx.Err()
	}
}
