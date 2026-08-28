//go:build !darwin

package processcontrol

func newPlatformCoordinator(AttemptAuthority, string) (platformCoordinator, error) {
	return nil, ErrUnsupported
}
