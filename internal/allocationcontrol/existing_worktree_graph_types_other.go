//go:build !darwin

package allocationcontrol

import "os"

// ExistingWorktreeDescriptorGraphV1 retains the descriptor graph API on
// unsupported platforms while Darwin-only fault-injection hooks stay out of
// the off-platform build graph.
type ExistingWorktreeDescriptorGraphV1 struct {
	FilesystemRoot                 *os.File
	FilesystemRootIdentity         ObjectIdentityV1
	RepositoryParent               *os.File
	RepositoryRoot                 *os.File
	RepositoryCurrentName          CurrentNameIdentityV1
	RepositoryDotGitFile           *os.File
	RepositoryDotGitCurrentName    CurrentNameIdentityV1
	RepositoryDotGitDigest         string
	RepositoryCommonGitParent      *os.File
	RepositoryCommonGitDirectory   *os.File
	RepositoryCommonGitCurrentName CurrentNameIdentityV1
}
