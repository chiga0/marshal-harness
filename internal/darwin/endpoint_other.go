//go:build !darwin

package darwin

import "errors"

type AuthorityEndpointIdentity struct {
	Path       string
	Device     uint64
	Inode      uint64
	OwnerUID   uint32
	Mode       uint32
	SocketType uint32
}

func InspectAuthorityEndpoint(string, uint32) (AuthorityEndpointIdentity, error) {
	return AuthorityEndpointIdentity{}, errors.New("darwin authority endpoint is unavailable on this platform")
}
