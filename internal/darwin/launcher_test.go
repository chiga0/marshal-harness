package darwin

import "testing"

func TestLauncherPolicyRequiresExactIdentity(t *testing.T) {
	identity := ExecutableIdentity{SHA256: "sha", TeamID: "team", CDHash: "cdhash", Identifier: "launcher"}
	policy := LauncherPolicy{SHA256: "sha", TeamID: "team", CDHash: "cdhash", Identifier: "launcher"}
	if err := policy.validate(identity); err != nil {
		t.Fatalf("matching policy rejected: %v", err)
	}
	for name, mutate := range map[string]func(*LauncherPolicy){
		"missing digest":   func(p *LauncherPolicy) { p.SHA256 = "" },
		"wrong team":       func(p *LauncherPolicy) { p.TeamID = "other" },
		"wrong cdhash":     func(p *LauncherPolicy) { p.CDHash = "other" },
		"wrong identifier": func(p *LauncherPolicy) { p.Identifier = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := policy
			mutate(&candidate)
			if err := candidate.validate(identity); err == nil {
				t.Fatal("mismatched policy was accepted")
			}
		})
	}
}

func TestLauncherPolicyDoesNotAuthorizeObservationAlone(t *testing.T) {
	identity := ExecutableIdentity{SHA256: "sha", TeamID: "team", CDHash: "cdhash", Identifier: "launcher"}
	if err := (LauncherPolicy{}).validate(identity); err == nil {
		t.Fatal("empty authority policy was accepted")
	}
}
