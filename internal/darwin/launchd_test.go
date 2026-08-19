package darwin

import (
	"bytes"
	"strings"
	"testing"
)

func TestRenderLaunchdPlistIsDeterministicAndClosed(t *testing.T) {
	spec := LaunchdAuthoritySpec{Label: DefaultAuthorityLabel, ServiceBinary: "/Library/PrivilegedHelperTools/marshal-apap", LauncherBinary: "/Library/PrivilegedHelperTools/marshal-darwin-launcher", Endpoint: DefaultAuthorityEndpoint}
	first, err := spec.RenderLaunchdPlist()
	if err != nil {
		t.Fatal(err)
	}
	second, err := spec.RenderLaunchdPlist()
	if err != nil || !bytes.Equal(first, second) {
		t.Fatalf("plist is not deterministic: %v", err)
	}
	text := string(first)
	for _, required := range []string{DefaultAuthorityLabel, spec.ServiceBinary, spec.LauncherBinary, spec.Endpoint, "<true/>", "<integer>63</integer>"} {
		if !strings.Contains(text, required) {
			t.Fatalf("plist missing %q: %s", required, text)
		}
	}
}

func TestRenderLaunchdPlistRejectsMutablePathShapes(t *testing.T) {
	base := LaunchdAuthoritySpec{Label: DefaultAuthorityLabel, ServiceBinary: "/Library/PrivilegedHelperTools/marshal-apap", LauncherBinary: "/Library/PrivilegedHelperTools/marshal-darwin-launcher", Endpoint: DefaultAuthorityEndpoint}
	mutations := map[string]func(*LaunchdAuthoritySpec){
		"relative service": func(spec *LaunchdAuthoritySpec) { spec.ServiceBinary = "./marshal-apap" },
		"path traversal":   func(spec *LaunchdAuthoritySpec) { spec.Endpoint = "/private/var/run/../tmp.sock" },
		"bad label":        func(spec *LaunchdAuthoritySpec) { spec.Label = "com.marshal/apap" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			spec := base
			mutate(&spec)
			if _, err := spec.RenderLaunchdPlist(); err == nil {
				t.Fatal("invalid launchd spec was accepted")
			}
		})
	}
}
