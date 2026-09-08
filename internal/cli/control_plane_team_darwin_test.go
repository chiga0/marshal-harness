//go:build darwin && arm64

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"golang.org/x/sys/unix"
)

func TestInitialTeamRequestFileIsBoundedClosedAndNonblocking(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "request.json")
	input := []byte(`{"fixture":true}`)
	request := application.ApproveInitialTeamRequest{ProtocolRevision: application.InitialTeamApprovalProtocol, RequestID: "request-1", Deadline: "2020-01-01T00:00:00Z", InputsDigest: canonical.DigestBytes(input), Inputs: input}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	got, err := readInitialTeamRequest(path)
	if err != nil || got.Deadline != request.Deadline || !bytes.Equal(got.Inputs, input) {
		t.Fatalf("read: %v", err)
	}
	var out, diagnostics bytes.Buffer
	if exit := runControlPlaneTeam(context.Background(), "team-approve", []string{"--request-file", path}, &out, &diagnostics); exit != ExitUsage || out.Len() != 0 {
		t.Fatal("expired approval attempted connection")
	}
	symlink := filepath.Join(dir, "link")
	if err := os.Symlink(path, symlink); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(dir, "fifo")
	if err := unix.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	for _, denied := range []string{symlink, fifo, dir, filepath.Join(dir, "missing")} {
		if _, err := readInitialTeamRequest(denied); err == nil {
			t.Fatalf("nonregular input accepted: %s", filepath.Base(denied))
		}
	}
	for _, bad := range [][]byte{
		append(append([]byte{}, raw[:len(raw)-1]...), []byte(`,"actor":"not-authority"}`)...),
		append(append([]byte{}, raw[:len(raw)-1]...), []byte(`,"requestId":"duplicate"}`)...),
		bytes.Repeat([]byte{' '}, (1<<20)+1),
	} {
		if err := os.WriteFile(path, bad, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readInitialTeamRequest(path); err == nil {
			t.Fatal("bad request accepted")
		}
	}
}
