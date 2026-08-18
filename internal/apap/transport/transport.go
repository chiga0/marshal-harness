// Package transport implements the local, non-secret APAP control transport.
//
// Version one deliberately exposes held file descriptors instead of paths. A
// path may be retained by a caller for audit display, but it is never accepted
// here as authority and is never reopened after admission.
package transport

import (
	"errors"
	"fmt"
	"sync"

	"github.com/chiga0/marshal-harness/internal/authorityprovider"
)

const (
	MaxPacketBytes = 64 << 10
	MaxFDs         = 32
)

var (
	ErrUnsupported    = errors.New("apap transport: unsupported platform")
	ErrPeerRejected   = errors.New("apap transport: peer rejected")
	ErrPacketRejected = errors.New("apap transport: packet rejected")
	ErrFDRejected     = errors.New("apap transport: descriptor rejected")
)

// PeerPolicy binds an OS peer to the already configured APAP principal. Zero
// PID means that any PID with the exact UID/GID and executable identity may be
// admitted. Worker is never an admissible control principal.
type PeerPolicy struct {
	UID                uint32
	GID                uint32
	PID                int32
	ExecutableIdentity ObjectIdentity
	PrincipalDigest    string
	Role               authorityprovider.Principal
}

// ObjectIdentity is derived from a held kernel object. It intentionally has no
// pathname field. ContentSHA256 is populated for regular files.
type ObjectIdentity struct {
	Device        uint64
	Inode         uint64
	Mode          uint32
	UID           uint32
	GID           uint32
	Size          int64
	Links         uint64
	ContentSHA256 string
}

type FDExpectation struct {
	Ref      authorityprovider.FDRef
	Identity ObjectIdentity
}

// HeldFD owns FD until Close is called. FD must not be persisted or converted
// back to a pathname.
type HeldFD struct {
	FD       int
	Ref      authorityprovider.FDRef
	Identity ObjectIdentity
	once     sync.Once
}

func (h *HeldFD) Close() error {
	if h == nil {
		return nil
	}
	var err error
	h.once.Do(func() { err = closeFD(h.FD); h.FD = -1 })
	return err
}

type Packet struct {
	Payload []byte
	Peer    authorityprovider.PeerIdentity
	FDs     []*HeldFD
}

func (p *Packet) Close() error {
	if p == nil {
		return nil
	}
	var first error
	for _, fd := range p.FDs {
		if err := fd.Close(); err != nil && first == nil {
			first = err
		}
	}
	p.FDs = nil
	return first
}

func validatePolicy(p PeerPolicy) error {
	if p.Role == "" || p.Role == authorityprovider.PrincipalWorker {
		return fmt.Errorf("%w: principal", ErrPeerRejected)
	}
	if p.PID <= 0 || !validDigest(p.ExecutableIdentity.ContentSHA256) {
		return fmt.Errorf("%w: executable identity", ErrPeerRejected)
	}
	if !validDigest(p.PrincipalDigest) {
		return fmt.Errorf("%w: principal identity", ErrPeerRejected)
	}
	return nil
}

func validDigest(value string) bool {
	if len(value) != 71 || value[:7] != "sha256:" {
		return false
	}
	for _, b := range []byte(value[7:]) {
		if b < '0' || b > '9' && b < 'a' || b > 'f' {
			return false
		}
	}
	return true
}

func validateExpectations(op authorityprovider.Operation, expectations []FDExpectation) error {
	refs := make([]authorityprovider.FDRef, len(expectations))
	for i, expectation := range expectations {
		if expectation.Ref.Role == authorityprovider.FDCredentialCapability || expectation.Ref.Role == authorityprovider.FDCredentialRoot {
			return fmt.Errorf("%w: credential authority", ErrFDRejected)
		}
		refs[i] = expectation.Ref
	}
	if err := authorityprovider.ValidateControlFDRoles(op, refs); err != nil {
		return fmt.Errorf("%w: table", ErrFDRejected)
	}
	return nil
}
