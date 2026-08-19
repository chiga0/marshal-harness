package darwin

import "runtime"

// AuthorityEndpointStatus is deliberately diagnostic only. It never admits
// an adapter or mints authority; production support still requires the
// signed APAP bundle, verifier evidence and launcher barrier.
const (
	AuthorityEndpointNotConfigured   = "not-configured"
	AuthorityEndpointUnsupported     = "unsupported-platform"
	AuthorityEndpointUnavailable     = "unavailable"
	AuthorityEndpointUnsafe          = "unsafe"
	AuthorityEndpointReady           = "ready"
	AuthorityDeploymentNotConfigured = "not-configured"
	AuthorityDeploymentUnsupported   = "unsupported-platform"
	AuthorityDeploymentUnavailable   = "unavailable"
	AuthorityDeploymentUnsafe        = "unsafe"
	AuthorityDeploymentReady         = "ready"
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

// InspectLaunchdDeploymentConfigStatus reports only whether an externally
// supplied deployment record is readable and its held objects are present.
// It never changes registry admission and never returns config contents.
func InspectLaunchdDeploymentConfigStatus(path string) string {
	if path == "" {
		return AuthorityDeploymentNotConfigured
	}
	if runtime.GOOS != "darwin" {
		return AuthorityDeploymentUnsupported
	}
	config, err := LoadLaunchdDeploymentConfig(path)
	if err != nil {
		return AuthorityDeploymentUnsafe
	}
	if _, err := InspectLaunchdDeployment(config.Spec, config.Policy); err != nil {
		return AuthorityDeploymentUnavailable
	}
	return AuthorityDeploymentReady
}
