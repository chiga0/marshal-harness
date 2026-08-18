package codex

import "errors"

// authorityExecutableIdentity projects the exact descriptor retained for
// version probing and later exec into the ADR 0037 authority identity. It
// never reopens the configured pathname.
func (snapshot *executableSnapshot) authorityExecutableIdentity() (ExecutableIdentityV1, error) {
	if snapshot == nil || snapshot.file == nil {
		return ExecutableIdentityV1{}, errors.New("held codex executable is unavailable")
	}
	stat, err := heldExecutableStat(snapshot.file)
	if err != nil {
		return ExecutableIdentityV1{}, err
	}
	identity := ExecutableIdentityV1{
		CanonicalRealpath:   snapshot.identity.path,
		DeviceMajor:         stat.deviceMajor,
		DeviceMinor:         stat.deviceMinor,
		Inode:               stat.inode,
		MountIDUnique:       stat.mountIDUnique,
		Size:                stat.size,
		Mode:                stat.mode,
		SHA256:              snapshot.identity.digest,
		Version:             snapshot.identity.version,
		VersionOutputDigest: digestBytesHex([]byte("codex-cli " + snapshot.identity.version + "\n")),
	}
	if err := identity.Validate(); err != nil {
		return ExecutableIdentityV1{}, err
	}
	return identity, nil
}

type heldExecutableStatV1 struct {
	deviceMajor, deviceMinor, inode, mountIDUnique, size uint64
	mode                                                 uint32
}
