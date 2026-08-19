package darwin

import "testing"

func TestInspectAuthorityEndpointRejectsInvalidOrUnprovisionedEndpoint(t *testing.T) {
	if _, err := InspectAuthorityEndpoint("relative.sock", 0); err == nil {
		t.Fatal("relative authority endpoint was accepted")
	}
	if _, err := InspectAuthorityEndpoint("/private/var/run/marshal-missing.sock", 0); err == nil {
		t.Fatal("missing authority endpoint was accepted")
	}
}
