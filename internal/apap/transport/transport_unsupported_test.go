//go:build !linux

package transport

import (
	"errors"
	"testing"

	"github.com/chiga0/marshal-harness/internal/authorityprovider"
)

func TestUnsupportedPlatformIsStableAndNeverClaimsSupport(t *testing.T) {
	if _, err := ListenRoot("/not-used", PeerPolicy{}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("ListenRoot: %v", err)
	}
	if _, err := MeasureFD(-1); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("MeasureFD: %v", err)
	}
	if err := Send(-1, []byte(`{}`), nil); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Send: %v", err)
	}
	var conn Conn
	if _, err := conn.Receive(authorityprovider.OperationDescribe, nil); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Receive: %v", err)
	}
}
