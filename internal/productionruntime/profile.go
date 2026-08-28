package productionruntime

import (
	"path/filepath"
	"regexp"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/launchidentity"
)

const (
	DarwinLocalDogfoodProfile = "darwin-local-dogfood"
	PiProviderName            = "pi"
	PiProviderVersion         = "0.84.3"
)

var profileDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// PiProfile is the only expected Agent profile admitted by this v1 foundation.
// Paths remain private to the composition root and never enter public status
// output. Construction validates shape, not executable identity: readiness and
// Start additionally require ProcessBridge.VerifyAgentProfile to recheck this
// exact tuple against its held launch objects. IdentityDigest is never accepted
// as proof merely because a caller supplied it.
type PiProfile struct {
	executablePath string
	runtimePath    string
	identityDigest string
}

func NewPi0843Profile(executablePath, runtimePath, identityDigest string) (PiProfile, error) {
	if !cleanAbsolutePath(executablePath) || !cleanAbsolutePath(runtimePath) || executablePath == runtimePath || !profileDigestPattern.MatchString(identityDigest) {
		return PiProfile{}, application.NewError("pi-profile", application.ReasonInvalidRequest)
	}
	return PiProfile{executablePath: executablePath, runtimePath: runtimePath, identityDigest: identityDigest}, nil
}

func (profile PiProfile) Validate() error {
	_, err := NewPi0843Profile(profile.executablePath, profile.runtimePath, profile.identityDigest)
	return err
}

func (profile PiProfile) ExecutablePath() string { return profile.executablePath }
func (profile PiProfile) RuntimePath() string    { return profile.runtimePath }
func (profile PiProfile) IdentityDigest() string { return profile.identityDigest }
func (profile PiProfile) ClosureProfileID() string {
	return launchidentity.Pi0843DarwinARM64Profile
}

func cleanAbsolutePath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path
}
