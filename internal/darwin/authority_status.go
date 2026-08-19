package darwin

import "runtime"

// AuthorityEndpointStatus is deliberately diagnostic only. It never admits
// an adapter or mints authority; production support still requires the
// signed APAP bundle, verifier evidence and launcher barrier.
const (
	AuthorityEndpointNotConfigured = "not-configured"
	AuthorityEndpointUnsupported   = "unsupported-platform"
	AuthorityEndpointUnavailable   = "unavailable"
	AuthorityEndpointUnsafe        = "unsafe"
	AuthorityEndpointReady         = "ready"
)

// InspectAuthorityEndpointStatus reports a non-secret, fail-closed status for
// the externally provisioned APAP socket. Darwin requires a root-owned socket
// with a private path; other platforms remain explicitly unsupported.
func InspectAuthorityEndpointStatus(path string) string {
	if path == "" {
		return AuthorityEndpointNotConfigured
	}
	if runtime.GOOS != "darwin" {
		return AuthorityEndpointUnsupported
	}
	if _, err := InspectAuthorityEndpoint(path, 0); err != nil {
		return AuthorityEndpointUnavailable
	}
	return AuthorityEndpointReady
}
