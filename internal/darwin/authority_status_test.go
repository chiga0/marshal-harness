package darwin

import (
	"runtime"
	"testing"
)

func TestInspectAuthorityEndpointStatusIsFailClosedAndNonSecret(t *testing.T) {
	if got := InspectAuthorityEndpointStatus(""); got != AuthorityEndpointNotConfigured {
		t.Fatalf("empty endpoint status = %q", got)
	}
	got := InspectAuthorityEndpointStatus("/private/var/run/marshal-apap.sock")
	if runtime.GOOS == "darwin" {
		if got != AuthorityEndpointUnavailable {
			t.Fatalf("missing Darwin endpoint status = %q", got)
		}
	} else if got != AuthorityEndpointUnsupported {
		t.Fatalf("non-Darwin endpoint status = %q", got)
	}
}

func TestInspectLaunchdDeploymentConfigStatusIsDiagnosticOnly(t *testing.T) {
	if got := InspectLaunchdDeploymentConfigStatus(""); got != AuthorityDeploymentNotConfigured {
		t.Fatalf("empty deployment config status = %q", got)
	}
	got := InspectLaunchdDeploymentConfigStatus("/private/var/run/marshal-deployment.json")
	if runtime.GOOS == "darwin" {
		if got != AuthorityDeploymentUnsafe {
			t.Fatalf("missing deployment config status = %q", got)
		}
	} else if got != AuthorityDeploymentUnsupported {
		t.Fatalf("non-Darwin deployment config status = %q", got)
	}
}
