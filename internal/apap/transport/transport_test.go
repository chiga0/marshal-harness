package transport

import (
	"errors"
	"testing"

	"github.com/chiga0/marshal-harness/internal/authorityprovider"
)

func portableDigest(b byte) string {
	value := make([]byte, 71)
	copy(value, "sha256:")
	for i := 7; i < len(value); i++ {
		value[i] = "0123456789abcdef"[b&15]
	}
	return string(value)
}

func TestPolicyRejectsWorkerMissingPIDAndMalformedIdentities(t *testing.T) {
	valid := PeerPolicy{PID: 1, ExecutableIdentity: ObjectIdentity{ContentSHA256: portableDigest(1)}, PrincipalDigest: portableDigest(2), Role: authorityprovider.PrincipalVerifierController}
	if err := validatePolicy(valid); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*PeerPolicy){
		"worker":     func(v *PeerPolicy) { v.Role = authorityprovider.PrincipalWorker },
		"pid":        func(v *PeerPolicy) { v.PID = 0 },
		"executable": func(v *PeerPolicy) { v.ExecutableIdentity.ContentSHA256 = "sha256:invalid" },
		"principal":  func(v *PeerPolicy) { v.PrincipalDigest = portableDigest(3)[:70] + "G" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			mutate(&candidate)
			if err := validatePolicy(candidate); !errors.Is(err, ErrPeerRejected) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestControlTableAlwaysRejectsCredentialAuthority(t *testing.T) {
	for _, role := range []authorityprovider.FDRole{authorityprovider.FDCredentialRoot, authorityprovider.FDCredentialCapability} {
		if err := validateExpectations(authorityprovider.OperationDescribe, []FDExpectation{{Ref: authorityprovider.FDRef{Role: role}}}); !errors.Is(err, ErrFDRejected) {
			t.Fatalf("role %s: %v", role, err)
		}
	}
}
