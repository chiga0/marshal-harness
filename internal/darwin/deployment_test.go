package darwin

import "testing"

func TestInspectLaunchdDeploymentFailsClosedBeforeExternalProvisioning(t *testing.T) {
	spec := LaunchdAuthoritySpec{Label: DefaultAuthorityLabel, ServiceBinary: "/Library/PrivilegedHelperTools/marshal-apap", LauncherBinary: "/Library/PrivilegedHelperTools/marshal-darwin-launcher", Endpoint: DefaultAuthorityEndpoint}
	policy := LaunchdDeploymentPolicy{
		Service:  LauncherPolicy{SHA256: "sha256:missing", TeamID: "missing", CDHash: "missing", Identifier: "missing"},
		Launcher: LauncherPolicy{SHA256: "sha256:missing", TeamID: "missing", CDHash: "missing", Identifier: "missing"},
		OwnerUID: 0,
	}
	if _, err := InspectLaunchdDeployment(spec, policy); err == nil {
		t.Fatal("unprovisioned launchd deployment was accepted")
	}
}
