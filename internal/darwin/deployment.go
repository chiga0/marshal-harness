package darwin

import "errors"

// LaunchdDeploymentPolicy binds both privileged binaries and the APAP socket
// to externally supplied authority observations. The policy is data-only;
// this package never creates, signs, installs or bootstraps launchd state.
type LaunchdDeploymentPolicy struct {
	Service  LauncherPolicy
	Launcher LauncherPolicy
	OwnerUID uint32
}

// LaunchdDeploymentIdentity is a non-secret snapshot produced while each
// binary and the endpoint are held. Callers must not treat it as authority
// until an independent provider signs and records the observation.
type LaunchdDeploymentIdentity struct {
	Service  ExecutableIdentity
	Launcher ExecutableIdentity
	Endpoint AuthorityEndpointIdentity
}

// InspectLaunchdDeployment verifies the exact deployed objects named by spec.
// It rejects path substitution by comparing the identity's held F_GETPATH
// projection with the manifest path and closes every descriptor before return.
func InspectLaunchdDeployment(spec LaunchdAuthoritySpec, policy LaunchdDeploymentPolicy) (LaunchdDeploymentIdentity, error) {
	if err := spec.validate(); err != nil {
		return LaunchdDeploymentIdentity{}, err
	}
	if policy.OwnerUID == ^uint32(0) {
		return LaunchdDeploymentIdentity{}, errors.New("darwin launchd owner uid is invalid")
	}
	service, err := OpenHeldLauncher(spec.ServiceBinary, policy.Service)
	if err != nil {
		return LaunchdDeploymentIdentity{}, errors.New("darwin APAP service identity is unavailable")
	}
	defer service.Close()
	serviceIdentity, err := service.Identity()
	if err != nil || serviceIdentity.Path != spec.ServiceBinary {
		return LaunchdDeploymentIdentity{}, errors.New("darwin APAP service identity changed")
	}
	launcher, err := OpenHeldLauncher(spec.LauncherBinary, policy.Launcher)
	if err != nil {
		return LaunchdDeploymentIdentity{}, errors.New("darwin launcher identity is unavailable")
	}
	defer launcher.Close()
	launcherIdentity, err := launcher.Identity()
	if err != nil || launcherIdentity.Path != spec.LauncherBinary {
		return LaunchdDeploymentIdentity{}, errors.New("darwin launcher identity changed")
	}
	endpoint, err := InspectAuthorityEndpoint(spec.Endpoint, policy.OwnerUID)
	if err != nil {
		return LaunchdDeploymentIdentity{}, errors.New("darwin APAP endpoint identity is unavailable")
	}
	return LaunchdDeploymentIdentity{Service: serviceIdentity, Launcher: launcherIdentity, Endpoint: endpoint}, nil
}
