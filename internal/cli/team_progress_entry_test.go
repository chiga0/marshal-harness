package cli

import (
	"testing"

	"github.com/chiga0/marshal-harness/internal/selfidentity"
)

func TestTeamProgressEntryExactAllowlist(t *testing.T) {
	for _, tc := range []struct {
		args    []string
		allowed bool
	}{
		{[]string{"control-plane", "serve"}, true},
		{[]string{"control-plane", "serve", "--auto-team-progress"}, true},
		{[]string{"control-plane", "serve", "--auto-team-progress=false"}, false},
		{[]string{"control-plane", "serve", "--auto-team-progress", "--auto-team-progress"}, false},
		{[]string{"control-plane", "serve", "--unknown"}, false},
	} {
		class, reason := localDogfoodCommandClass(tc.args, nil)
		if tc.allowed && (class != selfidentity.CommandControlPlaneServe || reason != "") || !tc.allowed && reason != selfidentity.ReasonCommandDenied {
			t.Fatalf("args=%v class=%s reason=%s", tc.args, class, reason)
		}
	}
}
