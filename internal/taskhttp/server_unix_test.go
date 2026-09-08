//go:build darwin || linux

package taskhttp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Test-only control directory authority. The resident composition uses the
// real FixedEndpointAuthority; this fixture tests listener/token boundaries.
type testControlAuthority struct{ dir *os.File }

func (a testControlAuthority) Recheck(context.Context) error { return nil }
func (a testControlAuthority) WithControlMutation(_ context.Context, fn func(*os.File) error) error {
	return fn(a.dir)
}

func TestTaskHTTPServerProtectedLoopback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root := t.TempDir()
	dir, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	config := ServerConfig{Application: &taskPortFixture{}, Address: "127.0.0.1:0", RecordName: "task-http-test.json", ControlPath: root, Authority: testControlAuthority{dir}, Mutation: func(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) }}
	bad := config
	bad.Address = "0.0.0.0:0"
	if s, err := OpenServer(ctx, bad); err == nil || s != nil {
		t.Fatal("non-loopback admitted")
	}
	s, err := OpenServer(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Serve() }()
	t.Cleanup(func() {
		cancel()
		_ = s.StopAccept()
		deadline, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		_ = s.Shutdown(deadline)
		_ = s.CloseRecord(context.Background())
	})
	info, err := os.Stat(s.ConnectionFile)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("connection file not private")
	}
	// Reading a token created exclusively by this fixture is intentional.
	raw, err := os.ReadFile(s.ConnectionFile)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]string
	if json.Unmarshal(raw, &record) != nil || len(record["token"]) != 64 {
		t.Fatal("invalid connection record")
	}
	client := &http.Client{Timeout: time.Second}
	r, err := client.Get(s.Address + "/v1/capabilities")
	if err != nil {
		t.Fatal(err)
	}
	_ = r.Body.Close()
	if r.StatusCode != 401 {
		t.Fatal("anonymous request accepted")
	}
	request, _ := http.NewRequest(http.MethodGet, s.Address+"/v1/capabilities", nil)
	request.Header.Set("Authorization", "Bearer "+record["token"])
	r, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(r.Body)
	_ = r.Body.Close()
	if r.StatusCode != 200 || len(body) == 0 {
		t.Fatalf("authenticated read: %d", r.StatusCode)
	}
	// Replacing the path cannot retarget the held token identity.
	old := filepath.Join(root, "old-record")
	if err := os.Rename(s.ConnectionFile, old); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.ConnectionFile, raw, 0600); err != nil {
		t.Fatal(err)
	}
	r, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = r.Body.Close()
	if r.StatusCode != 503 {
		t.Fatal("replaced token record remained current")
	}
	if err := s.CloseRecord(ctx); err == nil {
		t.Fatal("cleanup removed foreign replacement")
	}
	if _, err := os.Stat(s.ConnectionFile); errors.Is(err, os.ErrNotExist) {
		t.Fatal("replacement deleted")
	}
	_ = s.StopAccept()
	deadline, stop := context.WithTimeout(ctx, time.Second)
	defer stop()
	if err := s.Shutdown(deadline); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
