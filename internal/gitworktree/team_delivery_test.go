package gitworktree

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTaskDeliveryExportsCompleteAcceptedTreeNotLastPatch(t *testing.T) {
	r, state, base := integrationRepository(t)
	binding := "sha256:" + strings.Repeat("b", 64)
	patches := [][]byte{integrationPatch("quote_client.py", "client"), integrationPatch("quote_api.py", "api")}
	derived, err := r.CombineAcceptedPatches(context.Background(), state, base, binding, patches)
	if err != nil {
		t.Fatal(err)
	}
	final := integrationPatch("quote_delivery.json", "{\"delivery\":true}")
	// A mutable checkout file with the same name must never become delivery.
	if err := os.WriteFile(filepath.Join(r.Root, "quote_api.py"), []byte("unaccepted"), 0600); err != nil {
		t.Fatal(err)
	}
	before := gitCommand(t, r.Root, "status", "--porcelain=v1")
	names := []string{"quote_api.py", "quote_client.py", "quote_delivery.json"}
	files, err := r.ExportTeamDelivery(context.Background(), state, base, binding, derived.TreeSHA, derived.CommitSHA, patches, final, names)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 || !bytes.Equal(files["quote_api.py"], []byte("api\n")) || !bytes.Equal(files["quote_client.py"], []byte("client\n")) || !bytes.Contains(files["quote_delivery.json"], []byte("delivery")) {
		t.Fatalf("incomplete export: %+v", files)
	}
	if gitCommand(t, r.Root, "status", "--porcelain=v1") != before {
		t.Fatal("changed checkout/index")
	}
	for _, mode := range []string{"wrong-tree", "wrong-commit", "wrong-final", "missing", "traversal", "duplicate", "symlink"} {
		t.Run(mode, func(t *testing.T) {
			tree, commit, p, patch, paths := derived.TreeSHA, derived.CommitSHA, patches, final, names
			switch mode {
			case "wrong-tree":
				tree = strings.Repeat("a", 40)
			case "wrong-commit":
				commit = strings.Repeat("a", 40)
			case "wrong-order":
				p = [][]byte{patches[1], patches[0]}
			case "wrong-final":
				patch = integrationPatch("quote_client.py", "conflict")
			case "missing":
				paths = []string{"missing"}
			case "traversal":
				paths = []string{"../quote_api.py"}
			case "duplicate":
				paths = []string{"quote_api.py", "quote_api.py"}
			case "symlink":
				patch = []byte("diff --git a/link b/link\nnew file mode 120000\n--- /dev/null\n+++ b/link\n@@ -0,0 +1 @@\n+quote_api.py\n")
				paths = []string{"link"}
			}
			if _, err := r.ExportTeamDelivery(context.Background(), state, base, binding, tree, commit, p, patch, paths); err == nil {
				t.Fatal("unsafe export accepted")
			}
		})
	}
}
