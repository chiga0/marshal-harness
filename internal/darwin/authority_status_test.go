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
