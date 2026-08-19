//go:build !darwin

package authorityprovider

const (
	controlNetwork = "unixpacket"
	controlStream  = false
)
