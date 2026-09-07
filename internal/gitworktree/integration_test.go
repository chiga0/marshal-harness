package gitworktree

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func integrationPatch(path, content string) []byte {
	return []byte("diff --git a/" + path + " b/" + path + "\nnew file mode 100644\n--- /dev/null\n+++ b/" + path + "\n@@ -0,0 +1 @@\n+" + content + "\n")
}

func integrationRepository(t *testing.T) (Repository, string, string) {
	t.Helper()
	root, base := fixtureRepository(t)
	initializeMarshalState(t, root)
	r, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(r.Root, ".marshal")
	if err := os.MkdirAll(filepath.Join(state, "locks"), 0o700); err != nil {
		t.Fatal(err)
	}
	return r, state, base
}

func TestIntegrationBaseUsesExactPatchesWithoutChangingCheckout(t *testing.T) {
	r, state, base := integrationRepository(t)
	// Preserve an unrelated staged user edit, not merely a clean checkout.
	if err := os.WriteFile(filepath.Join(r.Root, "user.txt"), []byte("user edit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, r.Root, "add", "user.txt")
	before := gitCommand(t, r.Root, "diff", "--cached", "--binary")
	patches := [][]byte{integrationPatch("service.py", "service"), integrationPatch("client.py", "client")}
	binding := "sha256:" + strings.Repeat("1", 64)
	first, err := r.CombineAcceptedPatches(context.Background(), state, base, binding, patches)
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.CombineAcceptedPatches(context.Background(), state, base, binding, patches)
	if err != nil || second != first {
		t.Fatalf("same input changed derived base: %+v %+v %v", first, second, err)
	}
	if got := gitCommand(t, r.Root, "show", first.CommitSHA+":service.py"); got != "service\n" {
		t.Fatalf("wrong service bytes: %q", got)
	}
	if got := gitCommand(t, r.Root, "show", first.CommitSHA+":client.py"); got != "client\n" {
		t.Fatalf("wrong client bytes: %q", got)
	}
	if got := strings.TrimSpace(gitCommand(t, r.Root, "rev-list", "--parents", "-n", "1", first.CommitSHA)); got != first.CommitSHA+" "+base {
		t.Fatalf("changed parent: %q", got)
	}
	if strings.TrimSpace(gitCommand(t, r.Root, "rev-parse", "HEAD")) != base || gitCommand(t, r.Root, "diff", "--cached", "--binary") != before {
		t.Fatal("integration changed user HEAD/index")
	}
	for _, path := range []string{"service.py", "client.py"} {
		if _, err := os.Lstat(filepath.Join(r.Root, path)); !os.IsNotExist(err) {
			t.Fatal("integration wrote source checkout", path, err)
		}
	}
	third, err := r.CombineAcceptedPatches(context.Background(), state, base, "sha256:"+strings.Repeat("2", 64), patches)
	if err != nil || third.TreeSHA != first.TreeSHA || third.CommitSHA == first.CommitSHA {
		t.Fatal("accepted input binding is not part of deterministic commit", err)
	}
	entries, err := os.ReadDir(filepath.Join(state, "locks"))
	if err != nil || len(entries) != 0 {
		t.Fatal("private indexes were retained", entries, err)
	}
}

func TestIntegrationBaseRejectsConflictAndUnsafeInputs(t *testing.T) {
	r, state, base := integrationRepository(t)
	patches := [][]byte{integrationPatch("service.py", "service"), integrationPatch("client.py", "client")}
	binding := "sha256:" + strings.Repeat("1", 64)
	for _, mode := range []string{"conflict", "traversal", "empty", "count", "bad-base", "bad-binding", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			input := append([][]byte(nil), patches...)
			baseInput, bindingInput := base, binding
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch mode {
			case "conflict":
				input[1] = integrationPatch("service.py", "conflicting service")
			case "traversal":
				input[1] = integrationPatch("../outside", "outside")
			case "empty":
				input[0] = nil
			case "count":
				input = input[:1]
			case "bad-base":
				baseInput = "HEAD"
			case "bad-binding":
				bindingInput = "unbound"
			case "cancelled":
				cancel()
			}
			if result, err := r.CombineAcceptedPatches(ctx, state, baseInput, bindingInput, input); err == nil || result != (IntegrationBase{}) {
				t.Fatalf("invalid integration accepted: %+v %v", result, err)
			}
		})
	}
}

func TestIntegrationOutputBoundedWhileDraining(t *testing.T) {
	var out integrationOutput
	data := make([]byte, 128<<10)
	if n, err := out.Write(data); n != len(data) || err != nil || !out.overflow || out.Len() != 64<<10 {
		t.Fatal("output not bounded/drained")
	}
	if n, err := out.Write(data); n != len(data) || err != nil || out.Len() != 64<<10 {
		t.Fatal("overflow output grew")
	}
	// os/exec drains via io.Copy. An embedded bytes.Buffer would accidentally
	// promote ReaderFrom and let that fast path bypass our bounded Write.
	if _, bypass := any(&out).(io.ReaderFrom); bypass {
		t.Fatal("unbounded ReaderFrom bypass")
	}
}
