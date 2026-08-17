//go:build !linux

package codex

import (
	"context"
	"fmt"
	"os"
)

const secureFDPlatformReason = "当前平台缺少可验证的 fd-exec：Codex launcher 与 CLI 无法在不重新解析可变 pathname 的前提下执行；需要 signed/privileged launcher ADR"

func secureFDExecutionAvailable() bool { return false }

func secureFDPath(fd int) string { return fmt.Sprintf("/dev/fd/%d", fd) }

func secureLauncherFD() (*os.File, error) {
	return nil, fmt.Errorf("%w: %s", errSecureFDExecutionUnavailable, secureFDPlatformReason)
}

func sealedExecutableFD(string) (*os.File, error) {
	return nil, fmt.Errorf("%w: %s", errSecureFDExecutionUnavailable, secureFDPlatformReason)
}

func readBinaryVersionFromFD(context.Context, *os.File) (string, error) {
	return "", fmt.Errorf("%w: %s", errSecureFDExecutionUnavailable, secureFDPlatformReason)
}
