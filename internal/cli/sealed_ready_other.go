//go:build !darwin || !arm64

package cli

import (
	"context"
	"fmt"
	"io"
)

// runSealedReadyBranch is the sealed production composition entry. The
// darwin/arm64 fresh-start mechanics are the only sealed implementation, so
// every other platform fails closed before any filesystem or process effect.
func runSealedReadyBranch(_ context.Context, _, _ string, _, _ string, _, stderr io.Writer) int {
	fmt.Fprintln(stderr, "运行失败：sealed 生产组合仅在 darwin/arm64 提供。")
	return ExitUnavailable
}
