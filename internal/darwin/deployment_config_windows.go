//go:build windows

package darwin

import "errors"

func LoadLaunchdDeploymentConfig(string) (LaunchdDeploymentConfig, error) {
	return LaunchdDeploymentConfig{}, errors.New("Darwin launchd deployment is unavailable on Windows")
}

func InspectConfiguredLaunchdDeployment(string) (LaunchdDeploymentIdentity, error) {
	return LaunchdDeploymentIdentity{}, errors.New("Darwin launchd deployment is unavailable on Windows")
}
