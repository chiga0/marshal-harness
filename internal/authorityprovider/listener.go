package authorityprovider

import (
	"errors"
	"net"
	"path/filepath"
)

func listenControl(path string, network string, stream bool) (*ControlListener, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) {
		return nil, errors.New("APAP endpoint must be an absolute clean path")
	}
	listener, err := net.ListenUnix(network, &net.UnixAddr{Name: path, Net: network})
	if err != nil {
		return nil, err
	}
	return &ControlListener{listener: listener, stream: stream}, nil
}

// ListenControl creates an APAP endpoint without replacing an existing path.
// Owner/mode and launchd lifecycle remain external provisioning concerns.
func ListenControl(path string) (*ControlListener, error) {
	return listenControl(path, controlNetwork, controlStream)
}
