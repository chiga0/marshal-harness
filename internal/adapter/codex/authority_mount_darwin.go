//go:build darwin

package codex

import "errors"

func heldMountNamespaceIdentity() (uint64, uint64, error) {
	return 0, 0, errors.New("codex production mount identity is unsupported on Darwin")
}

func mountObjectIdentityForFD(_ int, _ string, _ *string) (MountObjectIdentityV1, error) {
	return MountObjectIdentityV1{}, errors.New("codex production mount identity is unsupported on Darwin")
}
