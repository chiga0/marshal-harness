//go:build !darwin

package verification

import "context"

type builtinArtifactPlatformHooks struct{}

// The first verifier builtin is deliberately Darwin-only. Reserved commands
// fail closed here and are never sent to PATH or a shell.
func readTaskSpecBuiltinArtifact(context.Context, string, string, builtinArtifactReadHooks) (builtinArtifact, string) {
	return builtinArtifact{}, "contract-builtin-denied"
}
