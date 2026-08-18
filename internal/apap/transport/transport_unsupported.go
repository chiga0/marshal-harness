//go:build !linux

package transport

import (
	"fmt"

	"github.com/chiga0/marshal-harness/internal/authorityprovider"
)

type Listener struct{}
type Conn struct{}

func ListenRoot(string, PeerPolicy) (*Listener, error) { return nil, ErrUnsupported }
func (l *Listener) Accept() (*Conn, error)             { return nil, ErrUnsupported }
func (l *Listener) Close() error                       { return nil }
func (c *Conn) Close() error                           { return nil }
func (c *Conn) Receive(authorityprovider.Operation, []FDExpectation) (*Packet, error) {
	return nil, ErrUnsupported
}
func MeasureFD(int) (ObjectIdentity, error) { return ObjectIdentity{}, ErrUnsupported }
func Send(int, []byte, []int) error         { return ErrUnsupported }
func closeFD(int) error                     { return fmt.Errorf("%w: descriptor ownership unavailable", ErrUnsupported) }
