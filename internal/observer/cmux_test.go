package observer

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// writeFakeCMUX writes an executable fake cmux control CLI script. The
// production code always invokes it directly via argv, never via a shell.
func writeFakeCMUX(t *testing.T, dir, name, script string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake cmux: %v", err)
	}
	return p
}

// mustRealpath resolves a path with EvalSymlinks so tests compare
// identities the same way the implementation does. This matters on macOS
// where t.TempDir() lives under /var (a symlink to /private/var).
func mustRealpath(t *testing.T, p string) string {
	t.Helper()
	rp, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", p, err)
	}
	return rp
}

// isolatedCMUXBackend returns a backend whose discovery can never reach a
// real cmux: PATH points at an empty directory and the app bundle
// candidates point at a nonexistent temp path, unless overridden.
func isolatedCMUXBackend(t *testing.T, explicit string) *CMUXBackend {
	t.Helper()
	b, err := NewCMUXBackend(explicit)
	if err != nil {
		t.Fatalf("NewCMUXBackend: %v", err)
	}
	t.Setenv("PATH", t.TempDir())
	b.appBundlePaths = []string{filepath.Join(t.TempDir(), "no-cmux")}
	return b
}

// readyJSON mirrors a real cmux `capabilities --json` payload:
// access_mode "password" (CLI authenticated via the stored password),
// dotted method names and a numeric version (real builds report the
// number 2).
const readyJSON = `{"access_mode":"password","methods":["pane.create","workspace.create","workspace.close","workspace.list","workspace.rename","surface.read_text","notification.create"],"protocol":"unix","socket_path":"/tmp/cmux.sock","version":2}`

func TestCMUXBackendInterface(t *testing.T) {
	var _ Backend = (*CMUXBackend)(nil)
	b := isolatedCMUXBackend(t, "")
	if got := b.ID(); got != CMUXBackendID {
		t.Errorf("ID = %q, want %q", got, CMUXBackendID)
	}
}

func TestCMUXExplicitPathMustBeAbsoluteAndClean(t *testing.T) {
	for _, p := range []string{"relative/cmux", "./cmux", "cmux"} {
		if _, err := NewCMUXBackend(p); err == nil {
			t.Errorf("NewCMUXBackend(%q): expected error for non-absolute path", p)
		}
	}
	// Absolute but not lexically clean must also be rejected.
	abs := filepath.Join(t.TempDir(), "cmux")
	for _, p := range []string{abs + "//cmux", filepath.Dir(abs) + "/./cmux", abs + "/../cmux"} {
		if _, err := NewCMUXBackend(p); err == nil {
			t.Errorf("NewCMUXBackend(%q): expected error for lexically unclean path", p)
		}
	}
	if _, err := NewCMUXBackend(abs); err != nil {
		t.Errorf("NewCMUXBackend(%q): %v", abs, err)
	}
	if _, err := NewCMUXBackend(""); err != nil {
		t.Errorf("NewCMUXBackend(\"\"): %v", err)
	}
}

func TestCMUXDiscoveryOrder(t *testing.T) {
	ctx := context.Background()

	t.Run("explicit path wins over PATH", func(t *testing.T) {
		explicit := writeFakeCMUX(t, t.TempDir(), "cmux-explicit", "#!/bin/sh\nprintf '"+readyJSON+"'\n")
		pathDir := t.TempDir()
		writeFakeCMUX(t, pathDir, "cmux", "#!/bin/sh\nprintf '{}'\n")
		t.Setenv("PATH", pathDir)

		b, err := NewCMUXBackend(explicit)
		if err != nil {
			t.Fatalf("NewCMUXBackend: %v", err)
		}
		b.appBundlePaths = []string{filepath.Join(t.TempDir(), "no-cmux")}
		res, err := b.Probe(ctx)
		if err != nil {
			t.Fatalf("Probe: %v", err)
		}
		if res.Executable != mustRealpath(t, explicit) {
			t.Errorf("Executable = %q, want explicit %q", res.Executable, mustRealpath(t, explicit))
		}
	})

	t.Run("PATH wins over app bundle", func(t *testing.T) {
		pathDir := t.TempDir()
		inPath := writeFakeCMUX(t, pathDir, "cmux", "#!/bin/sh\nprintf '"+readyJSON+"'\n")
		bundle := writeFakeCMUX(t, t.TempDir(), "cmux", "#!/bin/sh\nprintf '{}'\n")
		t.Setenv("PATH", pathDir)

		b, err := NewCMUXBackend("")
		if err != nil {
			t.Fatalf("NewCMUXBackend: %v", err)
		}
		b.appBundlePaths = []string{bundle}
		res, err := b.Probe(ctx)
		if err != nil {
			t.Fatalf("Probe: %v", err)
		}
		if res.Executable != mustRealpath(t, inPath) {
			t.Errorf("Executable = %q, want PATH candidate %q", res.Executable, mustRealpath(t, inPath))
		}
	})

	t.Run("app bundle fallback", func(t *testing.T) {
		bundle := writeFakeCMUX(t, t.TempDir(), "cmux", "#!/bin/sh\nprintf '"+readyJSON+"'\n")
		t.Setenv("PATH", t.TempDir()) // empty, no cmux on PATH

		b, err := NewCMUXBackend("")
		if err != nil {
			t.Fatalf("NewCMUXBackend: %v", err)
		}
		b.appBundlePaths = []string{bundle}
		res, err := b.Probe(ctx)
		if err != nil {
			t.Fatalf("Probe: %v", err)
		}
		if res.Executable != mustRealpath(t, bundle) {
			t.Errorf("Executable = %q, want bundle candidate %q", res.Executable, mustRealpath(t, bundle))
		}
	})

	t.Run("user-level app bundle fallback", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("HOME-based discovery is not exercised on windows")
		}
		home := t.TempDir()
		t.Setenv("HOME", home)

		// The default candidate list must contain the user-level bundle
		// path built from os.UserHomeDir.
		userBundle := filepath.Join(home, "Applications", "cmux.app", "Contents", "Resources", "bin", "cmux")
		found := false
		for _, c := range defaultCMUXAppBundleCandidates() {
			if c == userBundle {
				found = true
			}
		}
		if !found {
			t.Fatalf("defaultCMUXAppBundleCandidates() does not contain %q", userBundle)
		}

		// Probe through the user-level path in isolation so a real
		// system-wide install cannot shadow the fake.
		t.Setenv("PATH", t.TempDir()) // empty, no cmux on PATH
		if err := os.MkdirAll(filepath.Dir(userBundle), 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		writeFakeCMUX(t, filepath.Dir(userBundle), "cmux", "#!/bin/sh\nprintf '"+readyJSON+"'\n")

		b, err := NewCMUXBackend("")
		if err != nil {
			t.Fatalf("NewCMUXBackend: %v", err)
		}
		b.appBundlePaths = []string{userBundle}
		res, err := b.Probe(ctx)
		if err != nil {
			t.Fatalf("Probe: %v", err)
		}
		if res.Phase != PhaseReady {
			t.Fatalf("Phase = %q, want %q (diagnostic %q)", res.Phase, PhaseReady, res.Diagnostic)
		}
		if res.Executable != mustRealpath(t, userBundle) {
			t.Errorf("Executable = %q, want user bundle candidate %q", res.Executable, mustRealpath(t, userBundle))
		}
	})

	t.Run("nothing found reports not-installed", func(t *testing.T) {
		b := isolatedCMUXBackend(t, "")
		res, err := b.Probe(ctx)
		if err != nil {
			t.Fatalf("Probe: %v", err)
		}
		if res.Phase != PhaseNotInstalled {
			t.Errorf("Phase = %q, want %q", res.Phase, PhaseNotInstalled)
		}
		if res.Diagnostic != DiagCMUXNotInstalled {
			t.Errorf("Diagnostic = %q, want %q", res.Diagnostic, DiagCMUXNotInstalled)
		}
	})

	t.Run("invalid explicit path falls through to PATH", func(t *testing.T) {
		pathDir := t.TempDir()
		inPath := writeFakeCMUX(t, pathDir, "cmux", "#!/bin/sh\nprintf '"+readyJSON+"'\n")
		t.Setenv("PATH", pathDir)

		b, err := NewCMUXBackend(filepath.Join(t.TempDir(), "missing"))
		if err != nil {
			t.Fatalf("NewCMUXBackend: %v", err)
		}
		b.appBundlePaths = []string{filepath.Join(t.TempDir(), "no-cmux")}
		res, err := b.Probe(ctx)
		if err != nil {
			t.Fatalf("Probe: %v", err)
		}
		if res.Executable != mustRealpath(t, inPath) {
			t.Errorf("Executable = %q, want PATH candidate %q", res.Executable, mustRealpath(t, inPath))
		}
	})
}

func TestCMUXCandidateValidation(t *testing.T) {
	ctx := context.Background()

	t.Run("non-executable file is skipped", func(t *testing.T) {
		dir := t.TempDir()
		p := filepath.Join(dir, "cmux")
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		b := isolatedCMUXBackend(t, p)
		res, err := b.Probe(ctx)
		if err != nil {
			t.Fatalf("Probe: %v", err)
		}
		if res.Phase != PhaseNotInstalled {
			t.Errorf("Phase = %q, want %q", res.Phase, PhaseNotInstalled)
		}
	})

	t.Run("directory is skipped", func(t *testing.T) {
		b := isolatedCMUXBackend(t, t.TempDir())
		res, err := b.Probe(ctx)
		if err != nil {
			t.Fatalf("Probe: %v", err)
		}
		if res.Phase != PhaseNotInstalled {
			t.Errorf("Phase = %q, want %q", res.Phase, PhaseNotInstalled)
		}
	})

	t.Run("symlink resolves to real file", func(t *testing.T) {
		target := writeFakeCMUX(t, t.TempDir(), "cmux-real", "#!/bin/sh\nprintf '"+readyJSON+"'\n")
		link := filepath.Join(t.TempDir(), "cmux-link")
		if err := os.Symlink(target, link); err != nil {
			t.Fatalf("symlink: %v", err)
		}
		b := isolatedCMUXBackend(t, link)
		res, err := b.Probe(ctx)
		if err != nil {
			t.Fatalf("Probe: %v", err)
		}
		// Both sides are realpathed: /var -> /private/var on macOS.
		if res.Executable != mustRealpath(t, target) {
			t.Errorf("Executable = %q, want resolved target %q", res.Executable, mustRealpath(t, target))
		}
	})
}

func TestCMUXProbeReady(t *testing.T) {
	fake := writeFakeCMUX(t, t.TempDir(), "cmux", "#!/bin/sh\nprintf '"+readyJSON+"'\n")
	b := isolatedCMUXBackend(t, fake)
	res, err := b.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if res.Phase != PhaseReady {
		t.Fatalf("Phase = %q, want %q (diagnostic %q)", res.Phase, PhaseReady, res.Diagnostic)
	}
	if res.BackendID != CMUXBackendID {
		t.Errorf("BackendID = %q, want %q", res.BackendID, CMUXBackendID)
	}
	if res.Version != "2" {
		t.Errorf("Version = %q, want %q", res.Version, "2")
	}
	if res.AccessMode != "password" {
		t.Errorf("AccessMode = %q, want %q", res.AccessMode, "password")
	}
	if len(res.Methods) != 7 {
		t.Errorf("Methods = %v, want 7 entries", res.Methods)
	}
	// Only the four provably-mapped capabilities; workspace.close /
	// workspace.list / workspace.rename are real methods but map to no
	// capability, and progress/readonly-follow are never guessed.
	wantCaps := []Capability{CapabilityPaneCreate, CapabilityWorkspaceCreate, CapabilityScreenRead, CapabilityNotify}
	if len(res.Capabilities) != len(wantCaps) {
		t.Fatalf("Capabilities = %v, want %v", res.Capabilities, wantCaps)
	}
	for _, c := range wantCaps {
		found := false
		for _, got := range res.Capabilities {
			if got == c {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Capabilities = %v, missing %q", res.Capabilities, c)
		}
	}
	if res.Diagnostic != "" {
		t.Errorf("Diagnostic = %q, want empty", res.Diagnostic)
	}
}

// TestCMUXVersionDecoding pins strict version decoding: non-empty strings
// and finite numbers are normalized to strings, everything else yields the
// fixed protocol diagnostic without echoing the offending value.
func TestCMUXVersionDecoding(t *testing.T) {
	ctx := context.Background()

	t.Run("valid versions", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			version string
			want    string
		}{
			{"number", `2`, "2"},
			{"negative number", `-1`, "-1"},
			{"fractional number", `0.5`, "0.5"},
			{"string", `"0.64.20"`, "0.64.20"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				body := `{"access_mode":"password","methods":["notification.create"],"protocol":"unix","socket_path":"/tmp/cmux.sock","version":` + tc.version + `}`
				fake := writeFakeCMUX(t, t.TempDir(), "cmux", "#!/bin/sh\nprintf '"+body+"'\n")
				b := isolatedCMUXBackend(t, fake)
				res, err := b.Probe(ctx)
				if err != nil {
					t.Fatalf("Probe: %v", err)
				}
				if res.Phase != PhaseReady {
					t.Fatalf("Phase = %q, want %q (diagnostic %q)", res.Phase, PhaseReady, res.Diagnostic)
				}
				if res.Version != tc.want {
					t.Errorf("Version = %q, want %q", res.Version, tc.want)
				}
			})
		}
	})

	t.Run("invalid versions fail closed with protocol diagnostic", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			version string
		}{
			{"null", `null`},
			{"object", `{"major":2}`},
			{"array", `[2]`},
			{"boolean true", `true`},
			{"boolean false", `false`},
			{"empty string", `""`},
			{"non-finite number", `1e999`},
		} {
			t.Run(tc.name, func(t *testing.T) {
				body := `{"access_mode":"password","methods":["notification.create"],"protocol":"unix","socket_path":"/tmp/cmux.sock","version":` + tc.version + `}`
				fake := writeFakeCMUX(t, t.TempDir(), "cmux", "#!/bin/sh\nprintf '"+body+"'\n")
				b := isolatedCMUXBackend(t, fake)
				res, err := b.Probe(ctx)
				if err != nil {
					t.Fatalf("Probe: %v", err)
				}
				if res.Phase == PhaseReady {
					t.Errorf("Phase = %q, must not be ready with invalid version", res.Phase)
				}
				if res.Diagnostic != DiagCMUXProtocol {
					t.Errorf("Diagnostic = %q, want %q", res.Diagnostic, DiagCMUXProtocol)
				}
				if res.Version != "" {
					t.Errorf("Version = %q, want empty: rejected values must not be echoed", res.Version)
				}
				if len(res.Capabilities) != 0 {
					t.Errorf("Capabilities = %v, want none", res.Capabilities)
				}
			})
		}
	})
}

func TestCMUXAccessModes(t *testing.T) {
	ctx := context.Background()
	t.Run("authorized modes reach ready", func(t *testing.T) {
		for _, mode := range []string{"full", "readonly", "authorized", "password"} {
			t.Run(mode, func(t *testing.T) {
				body := `{"access_mode":"` + mode + `","methods":["notification.create"],"protocol":"unix","socket_path":"/tmp/cmux.sock","version":"1.0.0"}`
				fake := writeFakeCMUX(t, t.TempDir(), "cmux", "#!/bin/sh\nprintf '"+body+"'\n")
				b := isolatedCMUXBackend(t, fake)
				res, err := b.Probe(ctx)
				if err != nil {
					t.Fatalf("Probe: %v", err)
				}
				if res.Phase != PhaseReady {
					t.Errorf("access_mode %q: Phase = %q, want %q (diagnostic %q)", mode, res.Phase, PhaseReady, res.Diagnostic)
				}
			})
		}
	})
	t.Run("unauthorized modes fail closed", func(t *testing.T) {
		for _, mode := range []string{"none", "unauthorized", "", "godmode"} {
			t.Run("mode="+mode, func(t *testing.T) {
				body := `{"access_mode":"` + mode + `","methods":["notification.create"],"protocol":"unix","socket_path":"/tmp/cmux.sock","version":"1.0.0"}`
				fake := writeFakeCMUX(t, t.TempDir(), "cmux", "#!/bin/sh\nprintf '"+body+"'\n")
				b := isolatedCMUXBackend(t, fake)
				res, err := b.Probe(ctx)
				if err != nil {
					t.Fatalf("Probe: %v", err)
				}
				if res.Phase != PhaseReachable {
					t.Errorf("Phase = %q, want %q", res.Phase, PhaseReachable)
				}
				if res.Diagnostic != DiagCMUXUnauthorized {
					t.Errorf("Diagnostic = %q, want %q", res.Diagnostic, DiagCMUXUnauthorized)
				}
				if len(res.Capabilities) != 0 {
					t.Errorf("Capabilities = %v, want none", res.Capabilities)
				}
			})
		}
	})
}

func TestCMUXCapabilityMappingIgnoresUnknownMethods(t *testing.T) {
	body := `{"access_mode":"password","methods":["workspace.create","launch.missiles","workspace.create"],"protocol":"unix","socket_path":"/tmp/cmux.sock","version":"9.9.9"}`
	fake := writeFakeCMUX(t, t.TempDir(), "cmux", "#!/bin/sh\nprintf '"+body+"'\n")
	b := isolatedCMUXBackend(t, fake)
	res, err := b.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if res.Phase != PhaseReady {
		t.Fatalf("Phase = %q, want %q (diagnostic %q)", res.Phase, PhaseReady, res.Diagnostic)
	}
	if len(res.Capabilities) != 1 || res.Capabilities[0] != CapabilityWorkspaceCreate {
		t.Errorf("Capabilities = %v, want only %q", res.Capabilities, CapabilityWorkspaceCreate)
	}
}

// TestCMUXProbeMissingRequiredFields pins readiness gating: an authorized
// payload without version, protocol or socket path must never be ready.
func TestCMUXProbeMissingRequiredFields(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"missing version", `{"access_mode":"password","methods":["workspace.create"],"protocol":"unix","socket_path":"/tmp/cmux.sock","version":""}`},
		{"missing protocol", `{"access_mode":"password","methods":["workspace.create"],"protocol":"","socket_path":"/tmp/cmux.sock","version":"1.0.0"}`},
		{"missing socket path", `{"access_mode":"password","methods":["workspace.create"],"protocol":"unix","socket_path":"","version":"1.0.0"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := writeFakeCMUX(t, t.TempDir(), "cmux", "#!/bin/sh\nprintf '"+tc.body+"'\n")
			b := isolatedCMUXBackend(t, fake)
			res, err := b.Probe(context.Background())
			if err != nil {
				t.Fatalf("Probe: %v", err)
			}
			if res.Phase == PhaseReady {
				t.Errorf("Phase = %q, must not be ready with incomplete protocol envelope", res.Phase)
			}
			if res.Diagnostic != DiagCMUXProtocol {
				t.Errorf("Diagnostic = %q, want %q", res.Diagnostic, DiagCMUXProtocol)
			}
			if len(res.Capabilities) != 0 {
				t.Errorf("Capabilities = %v, want none", res.Capabilities)
			}
		})
	}
}

func TestCMUXProbeMalformedJSON(t *testing.T) {
	fake := writeFakeCMUX(t, t.TempDir(), "cmux", "#!/bin/sh\nprintf 'this is not json'\n")
	b := isolatedCMUXBackend(t, fake)
	res, err := b.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if res.Phase == PhaseNotInstalled {
		t.Errorf("Phase = %q, must not misreport as not-installed", res.Phase)
	}
	if res.Phase != PhaseInstalled {
		t.Errorf("Phase = %q, want %q", res.Phase, PhaseInstalled)
	}
	if res.Diagnostic != DiagCMUXInvalidJSON {
		t.Errorf("Diagnostic = %q, want %q", res.Diagnostic, DiagCMUXInvalidJSON)
	}
}

func TestCMUXProbeExecFailureNotMisreported(t *testing.T) {
	fake := writeFakeCMUX(t, t.TempDir(), "cmux", "#!/bin/sh\nexit 3\n")
	b := isolatedCMUXBackend(t, fake)
	res, err := b.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if res.Phase == PhaseNotInstalled {
		t.Errorf("Phase = %q, must not misreport as not-installed", res.Phase)
	}
	if res.Phase != PhaseInstalled {
		t.Errorf("Phase = %q, want %q", res.Phase, PhaseInstalled)
	}
	if res.Diagnostic != DiagCMUXExecFailed {
		t.Errorf("Diagnostic = %q, want %q", res.Diagnostic, DiagCMUXExecFailed)
	}
}

func TestCMUXProbeTimeout(t *testing.T) {
	fake := writeFakeCMUX(t, t.TempDir(), "cmux", "#!/bin/sh\nsleep 5\n")
	b := isolatedCMUXBackend(t, fake)
	b.timeout = 100 * time.Millisecond
	start := time.Now()
	res, err := b.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("Probe took %v, expected short built-in timeout", elapsed)
	}
	if res.Phase != PhaseInstalled {
		t.Errorf("Phase = %q, want %q", res.Phase, PhaseInstalled)
	}
	if res.Diagnostic != DiagCMUXTimeout {
		t.Errorf("Diagnostic = %q, want %q", res.Diagnostic, DiagCMUXTimeout)
	}
}

func TestCMUXProbeOutputTooLarge(t *testing.T) {
	// Produce oversized stdout using only shell builtins (printf + while),
	// so the test does not depend on any external tool being present on
	// the (deliberately emptied) PATH.
	script := "#!/bin/sh\ni=0\nwhile [ \"$i\" -lt 32 ]; do printf 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA'; i=$((i+1)); done\n"
	fake := writeFakeCMUX(t, t.TempDir(), "cmux", script)
	b := isolatedCMUXBackend(t, fake)
	b.maxStdout = 128
	res, err := b.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if res.Phase != PhaseInstalled {
		t.Errorf("Phase = %q, want %q", res.Phase, PhaseInstalled)
	}
	if res.Diagnostic != DiagCMUXOutputTooLarge {
		t.Errorf("Diagnostic = %q, want %q", res.Diagnostic, DiagCMUXOutputTooLarge)
	}
}

func TestCMUXProbeContextCancelled(t *testing.T) {
	fake := writeFakeCMUX(t, t.TempDir(), "cmux", "#!/bin/sh\nprintf '"+readyJSON+"'\n")
	b := isolatedCMUXBackend(t, fake)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := b.Probe(ctx); err == nil {
		t.Fatal("Probe: expected error on cancelled context")
	}
}

// TestCMUXHostileStderrNeverSurfaced pins the fixed classification for a
// hostile binary (secret-looking stderr + exit 1) and asserts that no
// error or probe result field carries the secret.
func TestCMUXHostileStderrNeverSurfaced(t *testing.T) {
	script := "#!/bin/sh\nprintf 'CMUX_SOCKET_PASSWORD=hunter2 token=abcdef' >&2\nprintf 'not json'\nexit 1\n"
	fake := writeFakeCMUX(t, t.TempDir(), "cmux", script)
	b := isolatedCMUXBackend(t, fake)
	res, err := b.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	// exit 1 classifies as exec-failed regardless of what stdout/stderr
	// contained; the classification is fixed and content-free.
	if res.Diagnostic != DiagCMUXExecFailed {
		t.Errorf("Diagnostic = %q, want %q", res.Diagnostic, DiagCMUXExecFailed)
	}
	fields := []string{res.Diagnostic, res.Executable, res.Version, res.AccessMode, res.BackendID, string(res.Phase)}
	fields = append(fields, res.Methods...)
	for _, s := range fields {
		if strings.Contains(s, "hunter2") || strings.Contains(s, "CMUX_SOCKET_PASSWORD") || strings.Contains(s, "abcdef") {
			t.Errorf("probe result leaks stderr content: %q", s)
		}
	}
}

// TestCMUXSocketPasswordEnvNeverRead asserts the backend never consumes
// CMUX_SOCKET_PASSWORD: probing succeeds purely from the CLI output and
// the environment value cannot surface anywhere.
func TestCMUXSocketPasswordEnvNeverRead(t *testing.T) {
	t.Setenv("CMUX_SOCKET_PASSWORD", "super-secret-password-value")
	fake := writeFakeCMUX(t, t.TempDir(), "cmux", "#!/bin/sh\nprintf '"+readyJSON+"'\n")
	b := isolatedCMUXBackend(t, fake)
	res, err := b.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if res.Phase != PhaseReady {
		t.Fatalf("Phase = %q, want %q (diagnostic %q)", res.Phase, PhaseReady, res.Diagnostic)
	}
	fields := []string{res.Diagnostic, res.Executable, res.Version, res.AccessMode, res.BackendID, string(res.Phase)}
	fields = append(fields, res.Methods...)
	for _, s := range fields {
		if strings.Contains(s, "super-secret-password-value") {
			t.Errorf("probe result leaks CMUX_SOCKET_PASSWORD: %q", s)
		}
	}
}

func TestCMUXBinaryReplacedFailsClosed(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "evil-ran")
	fake := writeFakeCMUX(t, dir, "cmux", "#!/bin/sh\nprintf '"+readyJSON+"'\n")
	b := isolatedCMUXBackend(t, fake)

	res, err := b.Probe(context.Background())
	if err != nil {
		t.Fatalf("first Probe: %v", err)
	}
	if res.Phase != PhaseReady {
		t.Fatalf("first Probe Phase = %q, want %q", res.Phase, PhaseReady)
	}

	// Replace the frozen binary in place with a hostile script.
	evil := "#!/bin/sh\ntouch '" + marker + "'\nprintf '" + readyJSON + "'\n"
	if err := os.WriteFile(fake, []byte(evil), 0o755); err != nil {
		t.Fatalf("replace fake: %v", err)
	}

	res, err = b.Probe(context.Background())
	if !errors.Is(err, ErrCMUXBinaryReplaced) {
		t.Fatalf("second Probe err = %v, want ErrCMUXBinaryReplaced", err)
	}
	if res.Diagnostic != DiagCMUXBinaryReplaced {
		t.Errorf("Diagnostic = %q, want %q", res.Diagnostic, DiagCMUXBinaryReplaced)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Error("replaced binary was executed; expected fail-closed behavior")
	}
}

// workspaceFake writes a fake control CLI that serves capabilities,
// logs every non-capabilities argv vector (one element per line) to
// argvLog, counts close calls in closeCount and executes closeBody for
// `workspace close <ref>`. Create always succeeds with the complete ref
// workspace:42, and close fails closed unless the argv vector is exactly
// `workspace close workspace:42` — a bare numeric ref or any extra,
// listed or guessed argument is rejected like real cmux 0.64.20 rejects
// the old flag form. The script uses only shell builtins plus printf.
func workspaceFake(t *testing.T, dir, argvLog, closeCount, closeBody string) string {
	t.Helper()
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = capabilities ]; then printf '" + readyJSON + "'; exit 0; fi\n" +
		"printf '%s\\n' \"$@\" >> '" + argvLog + "'\n" +
		"if [ \"$1\" = workspace ] && [ \"$2\" = create ]; then printf 'OK workspace:42\\n'; exit 0; fi\n" +
		"if [ \"$1\" = workspace ] && [ \"$2\" = close ]; then\n" +
		"  printf x >> '" + closeCount + "'\n" +
		"  if [ \"$#\" -ne 3 ] || [ \"$3\" != workspace:42 ]; then printf 'ERROR bad ref\\n'; exit 9; fi\n" +
		"  " + closeBody + "\n" +
		"fi\n" +
		"exit 9\n"
	return writeFakeCMUX(t, dir, "cmux", script)
}

func readLogLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}

func closeCallCount(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatalf("read %s: %v", path, err)
	}
	return len(data)
}

func validAttachRequest(cwd string) AttachRequest {
	return AttachRequest{
		RunID:            "run-1",
		AttemptID:        "attempt-1",
		Title:            "title",
		Description:      "description",
		WorkingDirectory: cwd,
	}
}

// TestCMUXAttachCreatesWorkspace pins the happy path and the argv
// contract: every argument, including hostile-looking title/description,
// must arrive as a single argv element and must never be executed.
func TestCMUXAttachCreatesWorkspace(t *testing.T) {
	dir := t.TempDir()
	argvLog := filepath.Join(dir, "argv.log")
	closeCount := filepath.Join(dir, "close.count")
	marker := filepath.Join(dir, "pwned")
	workspaceFake(t, dir, argvLog, closeCount, "printf 'OK\\n'; exit 0")
	b := isolatedCMUXBackend(t, filepath.Join(dir, "cmux"))

	cwd := t.TempDir()
	req := validAttachRequest(cwd)
	req.Title = "evil $(touch '" + marker + "'); rm -rf / name with spaces"
	req.Description = "desc with 'quotes' and $(shell) and `backticks`"

	h, err := b.Attach(context.Background(), req)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if h == nil {
		t.Fatal("Attach returned nil handle")
	}
	if want := "cmux:run-1:attempt-1:workspace:42"; h.ID() != want {
		t.Errorf("handle ID = %q, want %q carrying the complete created ref", h.ID(), want)
	}
	if strings.Contains(h.ID(), "workspace:workspace:") {
		t.Errorf("handle ID %q duplicates the ref prefix", h.ID())
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("shell injection marker exists: arguments were interpreted by a shell")
	}
	got := readLogLines(t, argvLog)
	want := []string{"workspace", "create",
		"--name", req.Title,
		"--description", req.Description,
		"--cwd", cwd,
		"--focus", "false"}
	if len(got) != len(want) {
		t.Fatalf("create argv = %v (%d elements), want %d single elements", got, len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("create argv[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestCMUXAttachValidation(t *testing.T) {
	cwd := t.TempDir()
	cases := []struct {
		name string
		req  AttachRequest
	}{
		{"missing RunID", func() AttachRequest { r := validAttachRequest(cwd); r.RunID = ""; return r }()},
		{"missing AttemptID", func() AttachRequest { r := validAttachRequest(cwd); r.AttemptID = ""; return r }()},
		{"relative cwd", func() AttachRequest { r := validAttachRequest(cwd); r.WorkingDirectory = "relative/dir"; return r }()},
		{"unclean cwd", func() AttachRequest {
			r := validAttachRequest(cwd)
			r.WorkingDirectory = filepath.Join(cwd, "sub", "..", "wd")
			return r
		}()},
		{"nonexistent cwd", func() AttachRequest {
			r := validAttachRequest(cwd)
			r.WorkingDirectory = filepath.Join(cwd, "missing")
			return r
		}()},
		{"cwd is a file", func() AttachRequest {
			p := filepath.Join(cwd, "a-file")
			if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}
			r := validAttachRequest(cwd)
			r.WorkingDirectory = p
			return r
		}()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// No fake installed: any reach into discovery would report
			// not-installed, not a validation error.
			b := isolatedCMUXBackend(t, "")
			if _, err := b.Attach(context.Background(), tc.req); err == nil {
				t.Error("Attach: expected error")
			}
		})
	}
}

// TestCMUXAttachRequiresReady pins that a probe not reaching the ready
// phase blocks Attach without creating a workspace.
func TestCMUXAttachRequiresReady(t *testing.T) {
	body := `{"access_mode":"none","methods":["workspace.create"],"protocol":"unix","socket_path":"/tmp/cmux.sock","version":"1.0.0"}`
	fake := writeFakeCMUX(t, t.TempDir(), "cmux", "#!/bin/sh\nprintf '"+body+"'\n")
	b := isolatedCMUXBackend(t, fake)
	_, err := b.Attach(context.Background(), validAttachRequest(t.TempDir()))
	if !errors.Is(err, ErrCMUXNotReady) {
		t.Fatalf("Attach err = %v, want ErrCMUXNotReady", err)
	}
}

// TestCMUXAttachRejectsMalformedCreateOutput pins strict parsing: the
// output must be exactly one line "OK workspace:<digits>". No echo of the
// offending output may appear in the error.
func TestCMUXAttachRejectsMalformedCreateOutput(t *testing.T) {
	for _, out := range []string{
		"",
		"OK workspace:",
		"OK workspace:abc",
		"OK workspace:12 trailing",
		"ok workspace:12",
		"OK workspace:12\nOK workspace:13",
		"OK workspace:12; rm -rf /",
		"ERROR workspace:12",
	} {
		t.Run(strings.ReplaceAll(strings.TrimSpace(out), "\n", "\\n"), func(t *testing.T) {
			lit := strings.ReplaceAll(out, "\n", `\n`)
			script := "#!/bin/sh\n" +
				"if [ \"$1\" = capabilities ]; then printf '" + readyJSON + "'; exit 0; fi\n" +
				"if [ \"$1\" = workspace ] && [ \"$2\" = create ]; then printf '" + lit + "'; exit 0; fi\n" +
				"exit 9\n"
			fake := writeFakeCMUX(t, t.TempDir(), "cmux", script)
			b := isolatedCMUXBackend(t, fake)
			h, err := b.Attach(context.Background(), validAttachRequest(t.TempDir()))
			if !errors.Is(err, ErrCMUXWorkspaceCreateFailed) {
				t.Fatalf("Attach err = %v, want ErrCMUXWorkspaceCreateFailed", err)
			}
			if h != nil {
				t.Errorf("handle = %v, want nil", h)
			}
			if strings.Contains(err.Error(), out) && out != "" {
				t.Errorf("error %q echoes CLI output", err)
			}
		})
	}
}

func TestCMUXAttachCancelledContext(t *testing.T) {
	dir := t.TempDir()
	workspaceFake(t, dir, filepath.Join(dir, "argv.log"), filepath.Join(dir, "close.count"), "exit 0")
	b := isolatedCMUXBackend(t, filepath.Join(dir, "cmux"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := b.Attach(ctx, validAttachRequest(t.TempDir())); err == nil {
		t.Fatal("Attach: expected error on cancelled context")
	}
}

func TestCMUXAttachCreateTimeout(t *testing.T) {
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = capabilities ]; then printf '" + readyJSON + "'; exit 0; fi\n" +
		"sleep 5\n"
	fake := writeFakeCMUX(t, t.TempDir(), "cmux", script)
	b := isolatedCMUXBackend(t, fake)
	b.timeout = 150 * time.Millisecond
	start := time.Now()
	h, err := b.Attach(context.Background(), validAttachRequest(t.TempDir()))
	if err == nil {
		t.Fatal("Attach: expected timeout error")
	}
	if h != nil {
		t.Errorf("handle = %v, want nil", h)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("Attach took %v, expected short built-in timeout", elapsed)
	}
}

// TestCMUXDetachClosesOnlyOwnRef pins the close argv contract and
// idempotence: a second Detach is a no-op and never re-invokes the CLI.
func TestCMUXDetachClosesOnlyOwnRef(t *testing.T) {
	dir := t.TempDir()
	argvLog := filepath.Join(dir, "argv.log")
	closeCount := filepath.Join(dir, "close.count")
	workspaceFake(t, dir, argvLog, closeCount, "printf 'OK\\n'; exit 0")
	b := isolatedCMUXBackend(t, filepath.Join(dir, "cmux"))
	h, err := b.Attach(context.Background(), validAttachRequest(t.TempDir()))
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if err := h.Detach(context.Background(), DetachRequest{Reason: "done"}); err != nil {
		t.Fatalf("Detach: %v", err)
	}
	lines := readLogLines(t, argvLog)
	closeArgv := lines[len(lines)-3:]
	want := []string{"workspace", "close", "workspace:42"}
	if len(closeArgv) != len(want) {
		t.Fatalf("close argv = %v, want %v (only the stored ref, never listed or guessed)", closeArgv, want)
	}
	for i := range want {
		if closeArgv[i] != want[i] {
			t.Fatalf("close argv[%d] = %q, want %q", i, closeArgv[i], want[i])
		}
	}
	if got := closeCallCount(t, closeCount); got != 1 {
		t.Fatalf("close invoked %d times, want 1", got)
	}
	if err := h.Detach(context.Background(), DetachRequest{}); err != nil {
		t.Fatalf("second Detach: %v, want idempotent nil", err)
	}
	if got := closeCallCount(t, closeCount); got != 1 {
		t.Errorf("close invoked %d times after idempotent Detach, want 1", got)
	}
}

func TestCMUXDetachConcurrency(t *testing.T) {
	dir := t.TempDir()
	argvLog := filepath.Join(dir, "argv.log")
	closeCount := filepath.Join(dir, "close.count")
	workspaceFake(t, dir, argvLog, closeCount, "printf 'OK\\n'; exit 0")
	b := isolatedCMUXBackend(t, filepath.Join(dir, "cmux"))
	h, err := b.Attach(context.Background(), validAttachRequest(t.TempDir()))
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	const n = 8
	errs := make([]error, n)
	gate := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-gate
			errs[i] = h.Detach(context.Background(), DetachRequest{})
		}(i)
	}
	close(gate)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("Detach goroutine %d: %v", i, err)
		}
	}
	if got := closeCallCount(t, closeCount); got != 1 {
		t.Errorf("close invoked %d times under concurrent Detach, want exactly 1", got)
	}
}

// TestCMUXDetachRetryAfterCloseFailure pins that a failed close does not
// mark the handle detached: Detach surfaces the error and a retry can
// still succeed.
func TestCMUXDetachRetryAfterCloseFailure(t *testing.T) {
	dir := t.TempDir()
	argvLog := filepath.Join(dir, "argv.log")
	closeCount := filepath.Join(dir, "close.count")
	failOnce := filepath.Join(dir, "failed-once")
	workspaceFake(t, dir, argvLog, closeCount,
		"if [ -f '"+failOnce+"' ]; then printf 'OK\\n'; exit 0; fi; printf x > '"+failOnce+"'; exit 1")
	b := isolatedCMUXBackend(t, filepath.Join(dir, "cmux"))
	h, err := b.Attach(context.Background(), validAttachRequest(t.TempDir()))
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if err := h.Detach(context.Background(), DetachRequest{}); err == nil {
		t.Fatal("first Detach: expected error")
	}
	if got := closeCallCount(t, closeCount); got != 1 {
		t.Fatalf("close invoked %d times, want 1", got)
	}
	if err := h.Detach(context.Background(), DetachRequest{}); err != nil {
		t.Fatalf("retry Detach: %v", err)
	}
	if got := closeCallCount(t, closeCount); got != 2 {
		t.Errorf("close invoked %d times, want 2", got)
	}
	if err := h.Detach(context.Background(), DetachRequest{}); err != nil {
		t.Fatalf("post-success Detach: %v", err)
	}
	if got := closeCallCount(t, closeCount); got != 2 {
		t.Errorf("close invoked %d times, want 2 (idempotent after success)", got)
	}
}

// TestCMUXDetachBinaryReplacedFailsClosedAndRetryable pins that identity
// is verified before close: a replaced binary is never executed, the
// handle stays attached and Detach succeeds once the original binary is
// restored.
func TestCMUXDetachBinaryReplacedFailsClosedAndRetryable(t *testing.T) {
	dir := t.TempDir()
	argvLog := filepath.Join(dir, "argv.log")
	closeCount := filepath.Join(dir, "close.count")
	marker := filepath.Join(dir, "evil-ran")
	fake := workspaceFake(t, dir, argvLog, closeCount, "printf 'OK\\n'; exit 0")
	original, err := os.ReadFile(fake)
	if err != nil {
		t.Fatalf("read fake: %v", err)
	}
	b := isolatedCMUXBackend(t, fake)
	h, err := b.Attach(context.Background(), validAttachRequest(t.TempDir()))
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}

	if err := os.WriteFile(fake, []byte("#!/bin/sh\ntouch '"+marker+"'\nprintf 'OK\\n'\n"), 0o755); err != nil {
		t.Fatalf("replace fake: %v", err)
	}
	if err := h.Detach(context.Background(), DetachRequest{}); !errors.Is(err, ErrCMUXBinaryReplaced) {
		t.Fatalf("Detach err = %v, want ErrCMUXBinaryReplaced", err)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Error("replaced binary was executed; expected fail-closed behavior")
	}
	if got := closeCallCount(t, closeCount); got != 0 {
		t.Errorf("close invoked %d times, want 0", got)
	}

	if err := os.WriteFile(fake, original, 0o755); err != nil {
		t.Fatalf("restore fake: %v", err)
	}
	if err := h.Detach(context.Background(), DetachRequest{}); err != nil {
		t.Fatalf("Detach after restore: %v", err)
	}
	if got := closeCallCount(t, closeCount); got != 1 {
		t.Errorf("close invoked %d times, want 1", got)
	}
}

// TestCMUXEnvAllowlistAndPasswordPassthrough pins the minimal env
// allowlist: only HOME, TMPDIR and CMUX_SOCKET_PASSWORD reach the CLI,
// and the password never surfaces in handle state or errors.
func TestCMUXEnvAllowlistAndPasswordPassthrough(t *testing.T) {
	const secret = "pass-secret-xyz"
	t.Setenv("CMUX_SOCKET_PASSWORD", secret)
	t.Setenv("MARSHAL_LEAK_CANARY", "canary-value")

	dir := t.TempDir()
	envDump := filepath.Join(dir, "env.txt")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = capabilities ]; then printf '" + readyJSON + "'; exit 0; fi\n" +
		"if [ \"$1\" = workspace ] && [ \"$2\" = create ]; then /usr/bin/env > '" + envDump + "'; printf 'OK workspace:7\\n'; exit 0; fi\n" +
		"exit 9\n"
	fake := writeFakeCMUX(t, dir, "cmux", script)
	b := isolatedCMUXBackend(t, fake)
	h, err := b.Attach(context.Background(), validAttachRequest(t.TempDir()))
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}

	allowed := map[string]bool{
		"HOME":                 true,
		"TMPDIR":               true,
		"CMUX_SOCKET_PASSWORD": true,
		// Set by /bin/sh itself regardless of the child environment; they
		// carry nothing we passed.
		"PWD":    true,
		"OLDPWD": true,
		"SHLVL":  true,
		"_":      true,
	}
	sawPassword := false
	for _, line := range readLogLines(t, envDump) {
		key, _, _ := strings.Cut(line, "=")
		if !allowed[key] {
			t.Errorf("env key %q leaked past the allowlist", key)
		}
		if key == "CMUX_SOCKET_PASSWORD" {
			sawPassword = true
			if !strings.HasSuffix(line, "="+secret) {
				t.Error("CMUX_SOCKET_PASSWORD was not passed through unchanged")
			}
		}
	}
	if !sawPassword {
		t.Error("CMUX_SOCKET_PASSWORD not passed through to the CLI")
	}
	if strings.Contains(h.ID(), secret) {
		t.Error("handle ID leaks CMUX_SOCKET_PASSWORD")
	}
}

// workspaceUpdateFake extends workspaceFake with the four update commands
// verified against real cmux 0.64.20. Update commands log their argv like
// every other invocation and then run updateBody, which must exit.
func workspaceUpdateFake(t *testing.T, dir, argvLog, closeCount, closeBody, updateBody string) string {
	t.Helper()
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = capabilities ]; then printf '" + readyJSON + "'; exit 0; fi\n" +
		"printf '%s\\n' \"$@\" >> '" + argvLog + "'\n" +
		"if [ \"$1\" = workspace ] && [ \"$2\" = create ]; then printf 'OK workspace:42\\n'; exit 0; fi\n" +
		"if [ \"$1\" = workspace ] && [ \"$2\" = close ]; then\n" +
		"  printf x >> '" + closeCount + "'\n" +
		"  if [ \"$#\" -ne 3 ] || [ \"$3\" != workspace:42 ]; then printf 'ERROR bad ref\\n'; exit 9; fi\n" +
		"  " + closeBody + "\n" +
		"fi\n" +
		"case \"$1\" in\n" +
		"  set-status|set-progress|log|notify) " + updateBody + " ;;\n" +
		"esac\n" +
		"exit 9\n"
	return writeFakeCMUX(t, dir, "cmux", script)
}

// attachForUpdate attaches a handle through workspaceUpdateFake and
// returns the handle plus the argv log and close count paths.
func attachForUpdate(t *testing.T, updateBody string) (Handle, string, string) {
	t.Helper()
	dir := t.TempDir()
	argvLog := filepath.Join(dir, "argv.log")
	closeCount := filepath.Join(dir, "close.count")
	workspaceUpdateFake(t, dir, argvLog, closeCount, "printf 'OK\\n'; exit 0", updateBody)
	b := isolatedCMUXBackend(t, filepath.Join(dir, "cmux"))
	h, err := b.Attach(context.Background(), validAttachRequest(t.TempDir()))
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	return h, argvLog, closeCount
}

// createArgvLineCount is the number of argv log lines produced by the
// single create invocation every attached handle has already performed.
const createArgvLineCount = 10

func assertArgvLog(t *testing.T, argvLog string, want []string) {
	t.Helper()
	got := readLogLines(t, argvLog)
	if len(got) != len(want) {
		t.Fatalf("argv log = %v (%d lines), want %d lines:\n%v", got, len(got), len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("argv line %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestCMUXUpdateFullSequence pins the exact command order (status ->
// progress -> log -> notification) and the argv boundaries: every value,
// including free text with spaces, arrives as a single argv element.
func TestCMUXUpdateFullSequence(t *testing.T) {
	cwd := t.TempDir()
	dir := t.TempDir()
	argvLog := filepath.Join(dir, "argv.log")
	closeCount := filepath.Join(dir, "close.count")
	workspaceUpdateFake(t, dir, argvLog, closeCount, "exit 0", "exit 0")
	b := isolatedCMUXBackend(t, filepath.Join(dir, "cmux"))
	h, err := b.Attach(context.Background(), validAttachRequest(cwd))
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	err = h.Update(context.Background(), UpdateRequest{
		Status:       "running",
		Progress:     ptr(0.5),
		LogLevel:     "warning",
		LogMessage:   "hello world",
		Notification: "all done",
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	want := append([]string(nil),
		"workspace", "create", "--name", "title", "--description", "description",
		"--cwd", cwd, "--focus", "false",
		"set-status", "marshal.status", "running", "--workspace", "workspace:42",
		"set-progress", "0.5", "--label", "running", "--workspace", "workspace:42",
		"log", "--level", "warning", "--source", "marshal", "--workspace", "workspace:42", "hello world",
		"notify", "--title", "Marshal run run-1", "--body", "all done", "--workspace", "workspace:42",
	)
	assertArgvLog(t, argvLog, want)
	if got := closeCallCount(t, closeCount); got != 0 {
		t.Errorf("close invoked %d times, want 0", got)
	}
}

// TestCMUXUpdateOnlyNonEmptyFields pins that only non-empty fields run a
// command, in the fixed order, and that an empty update succeeds with no
// side effects at all.
func TestCMUXUpdateOnlyNonEmptyFields(t *testing.T) {
	cases := []struct {
		name string
		req  UpdateRequest
		want []string // flat argv lines after the create invocation
	}{
		{"empty update", UpdateRequest{}, nil},
		{"status only", UpdateRequest{Status: "running"}, []string{
			"set-status", "marshal.status", "running", "--workspace", "workspace:42",
		}},
		{"progress only", UpdateRequest{Progress: ptr(0.25)}, []string{
			"set-progress", "0.25", "--label", "Marshal", "--workspace", "workspace:42",
		}},
		{"log only", UpdateRequest{LogMessage: "step one"}, []string{
			"log", "--level", "info", "--source", "marshal", "--workspace", "workspace:42", "step one",
		}},
		{"notification only", UpdateRequest{Notification: "finished"}, []string{
			"notify", "--title", "Marshal run run-1", "--body", "finished", "--workspace", "workspace:42",
		}},
		{"status after notification skipped", UpdateRequest{Notification: "n", Status: "s"}, []string{
			"set-status", "marshal.status", "s", "--workspace", "workspace:42",
			"notify", "--title", "Marshal run run-1", "--body", "n", "--workspace", "workspace:42",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, argvLog, _ := attachForUpdate(t, "exit 0")
			if err := h.Update(context.Background(), tc.req); err != nil {
				t.Fatalf("Update: %v", err)
			}
			got := readLogLines(t, argvLog)
			if len(got) != createArgvLineCount+len(tc.want) {
				t.Fatalf("argv log = %v (%d lines), want %d lines after create",
					got, len(got), len(tc.want))
			}
			if got[0] != "workspace" || got[1] != "create" {
				t.Fatalf("argv log = %v, expected create invocation first", got)
			}
			for i, w := range tc.want {
				if got[createArgvLineCount+i] != w {
					t.Fatalf("argv line %d = %q, want %q", createArgvLineCount+i, got[createArgvLineCount+i], w)
				}
			}
		})
	}
}

// TestCMUXUpdateProgressFormat pins the stable finite decimal rendering of
// progress values passed to set-progress.
func TestCMUXUpdateProgressFormat(t *testing.T) {
	for _, tc := range []struct {
		in   float64
		want string
	}{
		{0, "0"},
		{0.5, "0.5"},
		{0.125, "0.125"},
		{1, "1"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			h, argvLog, _ := attachForUpdate(t, "exit 0")
			if err := h.Update(context.Background(), UpdateRequest{Progress: ptr(tc.in)}); err != nil {
				t.Fatalf("Update: %v", err)
			}
			got := readLogLines(t, argvLog)
			if len(got) != 16 || got[10] != "set-progress" || got[11] != tc.want {
				t.Fatalf("argv log = %v, want set-progress %q after create", got, tc.want)
			}
		})
	}
}

// TestCMUXUpdateProgressValidation pins that NaN, infinities and
// out-of-range values are rejected by Validate before any command runs.
func TestCMUXUpdateProgressValidation(t *testing.T) {
	for _, bad := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -0.1, 1.5} {
		t.Run(strconv.FormatFloat(bad, 'g', -1, 64), func(t *testing.T) {
			h, argvLog, closeCount := attachForUpdate(t, "exit 0")
			// Combine with other fields to prove nothing else runs either.
			err := h.Update(context.Background(), UpdateRequest{
				Status: "running", Progress: ptr(bad),
				LogMessage: "msg", Notification: "note",
			})
			if err == nil {
				t.Fatal("Update: expected error for invalid progress")
			}
			got := readLogLines(t, argvLog)
			if len(got) != createArgvLineCount {
				t.Fatalf("argv log = %v, want only the create invocation", got)
			}
			if n := closeCallCount(t, closeCount); n != 0 {
				t.Errorf("close invoked %d times, want 0", n)
			}
		})
	}
}

// TestCMUXUpdateInvalidLogLevel pins that a level outside the real cmux
// set is rejected with the fixed error before any command runs, even when
// other fields would have produced commands.
func TestCMUXUpdateInvalidLogLevel(t *testing.T) {
	for _, level := range []string{"DEBUG", "Info", "trace", "warn", "fatal", " "} {
		t.Run(level, func(t *testing.T) {
			h, argvLog, closeCount := attachForUpdate(t, "exit 0")
			err := h.Update(context.Background(), UpdateRequest{
				Status: "running", LogLevel: level, LogMessage: "msg",
				Notification: "note",
			})
			if !errors.Is(err, ErrCMUXInvalidLogLevel) {
				t.Fatalf("Update err = %v, want ErrCMUXInvalidLogLevel", err)
			}
			if got := readLogLines(t, argvLog); len(got) != 10 {
				t.Fatalf("argv log = %v, want only the create invocation", got)
			}
			if n := closeCallCount(t, closeCount); n != 0 {
				t.Errorf("close invoked %d times, want 0", n)
			}
		})
	}
}

// TestCMUXUpdateValidLevels pins that every real cmux level is accepted
// and that an empty level defaults to info.
func TestCMUXUpdateValidLevels(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"info", "info"},
		{"progress", "progress"},
		{"success", "success"},
		{"warning", "warning"},
		{"error", "error"},
		{"", "info"},
	} {
		t.Run("level="+tc.want, func(t *testing.T) {
			h, argvLog, _ := attachForUpdate(t, "exit 0")
			if err := h.Update(context.Background(), UpdateRequest{LogLevel: tc.in, LogMessage: "m"}); err != nil {
				t.Fatalf("Update: %v", err)
			}
			got := readLogLines(t, argvLog)
			if len(got) != 18 || got[10] != "log" || got[11] != "--level" || got[12] != tc.want {
				t.Fatalf("argv log = %v, want log --level %q after create", got, tc.want)
			}
		})
	}
}

// TestCMUXUpdateProgressLabel pins the label rule: Status when set,
// otherwise the fixed fallback "Marshal".
func TestCMUXUpdateProgressLabel(t *testing.T) {
	t.Run("label from status", func(t *testing.T) {
		h, argvLog, _ := attachForUpdate(t, "exit 0")
		if err := h.Update(context.Background(), UpdateRequest{Status: "building", Progress: ptr(0.5)}); err != nil {
			t.Fatalf("Update: %v", err)
		}
		got := readLogLines(t, argvLog)
		idx := 10 + 5 // create(10) + set-status argv(5)
		if len(got) != idx+6 || got[idx] != "set-progress" || got[idx+2] != "--label" || got[idx+3] != "building" {
			t.Fatalf("argv log = %v, want set-progress --label building", got)
		}
	})
	t.Run("label fallback", func(t *testing.T) {
		h, argvLog, _ := attachForUpdate(t, "exit 0")
		if err := h.Update(context.Background(), UpdateRequest{Progress: ptr(0.5)}); err != nil {
			t.Fatalf("Update: %v", err)
		}
		got := readLogLines(t, argvLog)
		if len(got) != 16 || got[10] != "set-progress" || got[12] != "--label" || got[13] != "Marshal" {
			t.Fatalf("argv log = %v, want set-progress --label Marshal", got)
		}
	})
}

// TestCMUXUpdateInjectionNeverExecuted pins that hostile status values,
// labels, log messages and notification bodies arrive as single argv
// elements and are never interpreted by a shell.
func TestCMUXUpdateInjectionNeverExecuted(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "pwned")
	argvLog := filepath.Join(dir, "argv.log")
	closeCount := filepath.Join(dir, "close.count")
	workspaceUpdateFake(t, dir, argvLog, closeCount, "exit 0", "exit 0")
	b := isolatedCMUXBackend(t, filepath.Join(dir, "cmux"))
	h, err := b.Attach(context.Background(), validAttachRequest(t.TempDir()))
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	evilStatus := "evil $(touch '" + marker + "'); rm -rf /"
	evilLog := "msg with `touch '" + marker + "'` and \"quotes\""
	evilNote := "note $(touch '" + marker + "') && done"
	err = h.Update(context.Background(), UpdateRequest{
		Status:       evilStatus,
		Progress:     ptr(0.5),
		LogLevel:     "info",
		LogMessage:   evilLog,
		Notification: evilNote,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Error("shell injection marker exists: update arguments were interpreted by a shell")
	}
	got := readLogLines(t, argvLog)
	// Every hostile value must appear verbatim as exactly one argv element
	// per use; the status is used twice (set-status value and progress
	// label), everything else exactly once.
	counts := map[string]int{}
	for _, line := range got {
		counts[line]++
	}
	for _, tc := range []struct {
		value string
		want  int
	}{
		{evilStatus, 2},
		{evilLog, 1},
		{evilNote, 1},
		{"Marshal run run-1", 1},
	} {
		if counts[tc.value] != tc.want {
			t.Errorf("injected value not preserved as single argv elements: %q (count %d, want %d)", tc.value, counts[tc.value], tc.want)
		}
	}
	if counts[evilStatus+";"] != 0 || strings.Contains(strings.Join(got, "\n"), "rm -rf / ") {
		t.Error("argv log shows split or mangled injected values")
	}
}

// TestCMUXUpdateEmptyIsNoOp pins that an empty update succeeds and
// produces no child processes beyond the original create.
func TestCMUXUpdateEmptyIsNoOp(t *testing.T) {
	h, argvLog, closeCount := attachForUpdate(t, "exit 0")
	if err := h.Update(context.Background(), UpdateRequest{}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got := readLogLines(t, argvLog); len(got) != 10 {
		t.Fatalf("argv log = %v, want only the create invocation", got)
	}
	if n := closeCallCount(t, closeCount); n != 0 {
		t.Errorf("close invoked %d times, want 0", n)
	}
}

// TestCMUXUpdateCancelledContext pins that a cancelled context is rejected
// before any command runs.
func TestCMUXUpdateCancelledContext(t *testing.T) {
	h, argvLog, closeCount := attachForUpdate(t, "exit 0")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := h.Update(ctx, UpdateRequest{Status: "running"}); err == nil {
		t.Fatal("Update: expected error on cancelled context")
	}
	if got := readLogLines(t, argvLog); len(got) != 10 {
		t.Fatalf("argv log = %v, want only the create invocation", got)
	}
	if n := closeCallCount(t, closeCount); n != 0 {
		t.Errorf("close invoked %d times, want 0", n)
	}
}

// TestCMUXUpdateTimeout pins that a hanging update command is bounded by
// the backend timeout and surfaces a fixed error.
func TestCMUXUpdateTimeout(t *testing.T) {
	dir := t.TempDir()
	argvLog := filepath.Join(dir, "argv.log")
	closeCount := filepath.Join(dir, "close.count")
	workspaceUpdateFake(t, dir, argvLog, closeCount, "exit 0",
		"if [ \"$1\" = set-status ]; then sleep 5; fi; exit 0")
	b := isolatedCMUXBackend(t, filepath.Join(dir, "cmux"))
	h, err := b.Attach(context.Background(), validAttachRequest(t.TempDir()))
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	// Shorten the timeout only after attach so the probe itself is not
	// affected.
	b.timeout = 150 * time.Millisecond
	start := time.Now()
	err = h.Update(context.Background(), UpdateRequest{Status: "running", Notification: "note"})
	if err == nil {
		t.Fatal("Update: expected timeout error")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("Update took %v, expected short built-in timeout", elapsed)
	}
	// The timed-out set-status was invoked, but notify must never run.
	got := readLogLines(t, argvLog)
	for _, line := range got {
		if line == "notify" {
			t.Fatalf("argv log = %v: notify ran after set-status timeout", got)
		}
	}
}

// TestCMUXUpdateStopsOnFirstFailure pins that the first failing command
// aborts the remaining commands and the error is fixed and content-free.
func TestCMUXUpdateStopsOnFirstFailure(t *testing.T) {
	dir := t.TempDir()
	argvLog := filepath.Join(dir, "argv.log")
	closeCount := filepath.Join(dir, "close.count")
	workspaceUpdateFake(t, dir, argvLog, closeCount, "exit 0",
		"if [ \"$1\" = log ]; then printf 'hostile-secret' >&2; exit 3; fi; exit 0")
	b := isolatedCMUXBackend(t, filepath.Join(dir, "cmux"))
	h, err := b.Attach(context.Background(), validAttachRequest(t.TempDir()))
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	err = h.Update(context.Background(), UpdateRequest{
		Status: "running", Progress: ptr(0.5),
		LogLevel: "info", LogMessage: "msg", Notification: "note",
	})
	if err == nil {
		t.Fatal("Update: expected error from failing log command")
	}
	if strings.Contains(err.Error(), "hostile-secret") {
		t.Errorf("error %q leaks CLI output", err)
	}
	got := readLogLines(t, argvLog)
	sawSetStatus, sawSetProgress, sawLog, sawNotify := false, false, false, false
	for _, line := range got {
		switch line {
		case "set-status":
			sawSetStatus = true
		case "set-progress":
			sawSetProgress = true
		case "log":
			sawLog = true
		case "notify":
			sawNotify = true
		}
	}
	if !sawSetStatus || !sawSetProgress || !sawLog {
		t.Errorf("argv log = %v: commands before the failure must have run", got)
	}
	if sawNotify {
		t.Errorf("argv log = %v: notify ran after the failure", got)
	}
	if n := closeCallCount(t, closeCount); n != 0 {
		t.Errorf("close invoked %d times, want 0", n)
	}
}

// TestCMUXUpdateBinaryReplacedFailsClosed pins that identity is verified
// before every update command: a replaced binary is never executed and the
// handle stays attached, so a later update succeeds once the original
// binary is restored.
func TestCMUXUpdateBinaryReplacedFailsClosed(t *testing.T) {
	dir := t.TempDir()
	argvLog := filepath.Join(dir, "argv.log")
	closeCount := filepath.Join(dir, "close.count")
	marker := filepath.Join(dir, "evil-ran")
	fake := workspaceUpdateFake(t, dir, argvLog, closeCount, "exit 0", "exit 0")
	original, err := os.ReadFile(fake)
	if err != nil {
		t.Fatalf("read fake: %v", err)
	}
	b := isolatedCMUXBackend(t, fake)
	h, err := b.Attach(context.Background(), validAttachRequest(t.TempDir()))
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}

	if err := os.WriteFile(fake, []byte("#!/bin/sh\ntouch '"+marker+"'\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("replace fake: %v", err)
	}
	if err := h.Update(context.Background(), UpdateRequest{Status: "running"}); !errors.Is(err, ErrCMUXBinaryReplaced) {
		t.Fatalf("Update err = %v, want ErrCMUXBinaryReplaced", err)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Error("replaced binary was executed; expected fail-closed behavior")
	}
	linesAfterReplace := readLogLines(t, argvLog)

	if err := os.WriteFile(fake, original, 0o755); err != nil {
		t.Fatalf("restore fake: %v", err)
	}
	if err := h.Update(context.Background(), UpdateRequest{Status: "running"}); err != nil {
		t.Fatalf("Update after restore: %v", err)
	}
	got := readLogLines(t, argvLog)
	if len(got) != len(linesAfterReplace)+5 {
		t.Errorf("argv log = %v, want exactly the restored set-status invocation appended", got)
	}
	if n := closeCallCount(t, closeCount); n != 0 {
		t.Errorf("close invoked %d times, want 0", n)
	}
}

// sequencingFake records BEGIN/END markers around every close and update
// command so tests can prove that invocations never overlap.
func sequencingFake(t *testing.T, dir, seqLog, closeCount string) string {
	t.Helper()
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = capabilities ]; then printf '" + readyJSON + "'; exit 0; fi\n" +
		"if [ \"$1\" = workspace ] && [ \"$2\" = create ]; then printf 'OK workspace:42\\n'; exit 0; fi\n" +
		"if [ \"$1\" = workspace ] && [ \"$2\" = close ]; then\n" +
		"  printf x >> '" + closeCount + "'\n" +
		"  printf 'BEGIN close\\n' >> '" + seqLog + "'\n" +
		"  sleep 0.05\n" +
		"  printf 'END close\\n' >> '" + seqLog + "'\n" +
		"  exit 0\n" +
		"fi\n" +
		"case \"$1\" in\n" +
		"  set-status|set-progress|log|notify)\n" +
		"    printf 'BEGIN %s\\n' \"$1\" >> '" + seqLog + "'\n" +
		"    sleep 0.05\n" +
		"    printf 'END %s\\n' \"$1\" >> '" + seqLog + "'\n" +
		"    exit 0 ;;\n" +
		"esac\n" +
		"exit 9\n"
	return writeFakeCMUX(t, dir, "cmux", script)
}

// TestCMUXUpdateDetachConcurrency pins that concurrent Update and Detach
// calls never interleave: every BEGIN is immediately followed by its own
// END, close runs exactly once, and updates after detach return the fixed
// ErrCMUXDetached.
func TestCMUXUpdateDetachConcurrency(t *testing.T) {
	dir := t.TempDir()
	seqLog := filepath.Join(dir, "seq.log")
	closeCount := filepath.Join(dir, "close.count")
	sequencingFake(t, dir, seqLog, closeCount)
	b := isolatedCMUXBackend(t, filepath.Join(dir, "cmux"))
	h, err := b.Attach(context.Background(), validAttachRequest(t.TempDir()))
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	const nUpdaters, nDetachers = 6, 2
	updateErrs := make([]error, nUpdaters)
	detachErrs := make([]error, nDetachers)
	gate := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < nUpdaters; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-gate
			updateErrs[i] = h.Update(context.Background(), UpdateRequest{
				Status: "running", Progress: ptr(0.5), LogMessage: "tick",
			})
		}(i)
	}
	for i := 0; i < nDetachers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-gate
			detachErrs[i] = h.Detach(context.Background(), DetachRequest{})
		}(i)
	}
	close(gate)
	wg.Wait()
	for i, err := range detachErrs {
		if err != nil {
			t.Errorf("Detach goroutine %d: %v", i, err)
		}
	}
	for i, err := range updateErrs {
		if err != nil && !errors.Is(err, ErrCMUXDetached) {
			t.Errorf("Update goroutine %d err = %v, want nil or ErrCMUXDetached", i, err)
		}
	}
	if got := closeCallCount(t, closeCount); got != 1 {
		t.Errorf("close invoked %d times, want exactly 1", got)
	}
	// The BEGIN/END sequence must be strictly non-overlapping.
	var inFlight string
	for _, line := range readLogLines(t, seqLog) {
		if kind, name, ok := strings.Cut(line, " "); ok {
			switch kind {
			case "BEGIN":
				if inFlight != "" {
					t.Fatalf("command %q started while %q was still running: close and update interleaved", name, inFlight)
				}
				inFlight = name
			case "END":
				if name != inFlight {
					t.Fatalf("END %q does not match in-flight %q", name, inFlight)
				}
				inFlight = ""
			default:
				t.Fatalf("unexpected seq log line %q", line)
			}
		}
	}
	if inFlight != "" {
		t.Fatalf("command %q never ended", inFlight)
	}
}

// TestCMUXUpdateAfterDetachNoSideEffects pins that updates on a detached
// handle return the fixed ErrCMUXDetached and spawn no child processes,
// even for empty updates.
func TestCMUXUpdateAfterDetachNoSideEffects(t *testing.T) {
	h, argvLog, closeCount := attachForUpdate(t, "exit 0")
	if err := h.Detach(context.Background(), DetachRequest{Reason: "done"}); err != nil {
		t.Fatalf("Detach: %v", err)
	}
	linesAfterDetach := readLogLines(t, argvLog)
	for _, req := range []UpdateRequest{
		{},
		{Status: "running", Progress: ptr(0.5), LogMessage: "msg", Notification: "note"},
	} {
		if err := h.Update(context.Background(), req); !errors.Is(err, ErrCMUXDetached) {
			t.Errorf("Update err = %v, want ErrCMUXDetached", err)
		}
	}
	assertArgvLog(t, argvLog, linesAfterDetach)
	if got := closeCallCount(t, closeCount); got != 1 {
		t.Errorf("close invoked %d times, want 1 (no update re-triggered close)", got)
	}
}
