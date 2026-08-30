//go:build darwin

package allocationcontrol

import "os"

// ExistingWorktreeDescriptorGraphV1 is borrowed from the RB1 session while
// the repository owner lock is held. Locator strings may only be traversed
// descriptor-relative from FilesystemRoot. RepositoryRoot and its current
// parent/name edge are used for the fixed projection graph.
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
	beforeDotGitRead               func()
	afterDotGitRead                func()
}
