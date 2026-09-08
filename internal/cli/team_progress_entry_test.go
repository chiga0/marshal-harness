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
		{[]string{"control-plane", "serve", "--task-http-address", "127.0.0.1:0", "--task-template", "/operator/team.json"}, true},
		{[]string{"control-plane", "serve", "--task-template", "/operator/team.json", "--auto-team-progress", "--task-http-address", "127.0.0.1:4321"}, true},
		{[]string{"control-plane", "serve", "--task-http-address", "127.0.0.1:0"}, false},
		{[]string{"control-plane", "serve", "--task-template", "/operator/team.json"}, false},
		{[]string{"control-plane", "serve", "--task-http-address", "0.0.0.0:0", "--task-template", "/operator/team.json"}, false},
		{[]string{"control-plane", "serve", "--task-http-address", "localhost:0", "--task-template", "/operator/team.json"}, false},
		{[]string{"control-plane", "serve", "--task-http-address", "127.0.0.1:70000", "--task-template", "/operator/team.json"}, false},
		{[]string{"control-plane", "serve", "--task-http-address", "127.0.0.1:0", "--task-template", "relative.json"}, false},
		{[]string{"control-plane", "serve", "--task-http-address", "127.0.0.1:0", "--task-template", "/operator/team.json", "--task-template", "/operator/other.json"}, false},
	} {
		class, reason := localDogfoodCommandClass(tc.args, nil)
		if tc.allowed && (class != selfidentity.CommandControlPlaneServe || reason != "") || !tc.allowed && reason != selfidentity.ReasonCommandDenied {
			t.Fatalf("args=%v class=%s reason=%s", tc.args, class, reason)
		}
	}
}
