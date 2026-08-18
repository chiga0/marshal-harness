package codex

import (
	"context"
	"errors"
	"runtime"
	"strings"

	"github.com/chiga0/marshal-harness/internal/contract"
)

var ErrCodexConformancePending = errors.New("codex credentialed live conformance pending")

// NewFromAuthorityConfig is intentionally a hard-disabled production seam.
// This core slice can parse and verify hermetic authority objects, but no
// registry, consumer, or live-evidence enablement is authorized by ADR 0037
// acceptance alone.
func NewFromAuthorityConfig(ctx context.Context, executable string, validator *contract.Validator, configPath string) (*Adapter, error) {
	if runtime.GOOS != "linux" {
		return nil, newAuthorityFailure("constructor", "codex_platform_unsupported", "Codex production authority is unsupported on this platform", AuthorityFailureDetails{Platform: runtime.GOOS}, ErrCodexConformancePending, authorityNow())
	}
	if ctx == nil || ctx.Err() != nil || validator == nil || strings.TrimSpace(executable) == "" || strings.TrimSpace(configPath) == "" {
		return nil, newAuthorityFailure("constructor", "codex_conformance_pending", "Codex credentialed live conformance is pending", AuthorityFailureDetails{}, ErrCodexConformancePending, authorityNow())
	}
	return nil, newAuthorityFailure("constructor", "codex_conformance_pending", "Codex credentialed live conformance is pending", AuthorityFailureDetails{}, ErrCodexConformancePending, authorityNow())
}
