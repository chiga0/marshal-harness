//go:build !darwin || !arm64

package cli

import (
	"context"
	"io"
)

func runControlPlane(context.Context, []string, io.Writer, io.Writer) int {
	return ExitUnavailable
}
