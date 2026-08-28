package sandboxlaunch

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestProtocolCanonicalRoundTrip(t *testing.T) {
	spec := validSpec()
	raw, err := spec.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.LaunchID != spec.LaunchID || !bytes.Equal(mustCanonical(t, decoded), raw) {
		t.Fatalf("round trip changed canonical spec")
	}
}

func TestProtocolAcceptsRealPipeIdentityWithZeroDeviceOrLinkCount(t *testing.T) {
	type pipePair struct{ read, write *os.File }
	pairs := make([]pipePair, 3)
	for index := range pairs {
		read, write, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		pairs[index] = pipePair{read: read, write: write}
		t.Cleanup(func() { _ = read.Close(); _ = write.Close() })
	}
	spec := validSpec()
	spec.SpecPipe = realPipeBinding(t, pairs[0].read)
	spec.ReadyPipe = realPipeBinding(t, pairs[1].write)
	spec.ReleasePipe = realPipeBinding(t, pairs[2].read)
	if _, err := spec.Canonical(); err != nil {
		t.Fatalf("real Darwin pipe binding rejected: %v", err)
	}
}

func TestProtocolRejectsNonCanonicalUnknownAndDuplicateInput(t *testing.T) {
	raw := mustCanonical(t, validSpec())
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	object["unknown"] = true
	withUnknown, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}

	for name, input := range map[string][]byte{
		"whitespace": append([]byte(" "), raw...),
		"unknown":    withUnknown,
		"duplicate":  []byte(strings.Replace(string(raw), `"launchId":"launch-1"`, `"launchId":"launch-1","launchId":"launch-2"`, 1)),
		"oversize":   bytes.Repeat([]byte{'x'}, MaxSpecBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode(input); !errors.Is(err, ErrProtocolRejected) {
				t.Fatalf("Decode error = %v", err)
			}
		})
	}
}

func TestProtocolRejectsAmbiguousEnvironmentAndMalformedBindings(t *testing.T) {
	for name, mutate := range map[string]func(*Spec){
		"duplicate environment":    func(spec *Spec) { spec.Environment = []string{"A=1", "A=2"} },
		"missing environment name": func(spec *Spec) { spec.Environment = []string{"=value"} },
		"nul argument":             func(spec *Spec) { spec.Arguments = []string{"/bin/tool\x00suffix"} },
		"argv executable mismatch": func(spec *Spec) { spec.Arguments[0] = "/fixed/other" },
		"empty digest":             func(spec *Spec) { spec.Executable.SHA256 = "" },
		"uppercase digest":         func(spec *Spec) { spec.Executable.SHA256 = "sha256:" + strings.Repeat("A", 64) },
		"zero pipe inode":          func(spec *Spec) { spec.ReleasePipe.Inode = 0 },
		"hardlinked executable":    func(spec *Spec) { spec.Executable.Nlink = 2 },
		"aliased control pipes":    func(spec *Spec) { spec.ReleasePipe = spec.ReadyPipe },
		"premature material": func(spec *Spec) {
			spec.Materials = []MaterialBinding{{Role: "provider-bundle", Path: "/fixed/provider/bundle.js", FD: MaterialFDBase, Object: spec.Executable}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			spec := validSpec()
			mutate(&spec)
			if _, err := spec.Canonical(); !errors.Is(err, ErrProtocolRejected) {
				t.Fatalf("Canonical error = %v", err)
			}
		})
	}
}

func validSpec() Spec {
	pipe := ObjectBinding{Device: 1, Inode: 2, Mode: 0o010600, UID: 501, GID: 20, Nlink: 1}
	directory := ObjectBinding{Device: 1, Inode: 3, Mode: 0o040700, UID: 501, GID: 20, Nlink: 2}
	executable := ObjectBinding{Device: 1, Inode: 4, Mode: 0o100700, UID: 501, GID: 20, Size: 42, Nlink: 1, SHA256: "sha256:" + strings.Repeat("a", 64)}
	marshal := executable
	marshal.Inode = 5
	readyPipe := pipe
	readyPipe.Inode = 6
	releasePipe := pipe
	releasePipe.Inode = 7
	return Spec{
		ProtocolRevision: ProtocolRevision,
		LaunchID:         "launch-1",
		ParentPID:        1234,
		Arguments:        []string{"/fixed/workload", "argument"},
		Environment:      []string{"LANG=C"},
		ExecutablePath:   "/fixed/workload",
		Materials:        []MaterialBinding{},
		SpecPipe:         pipe,
		ReadyPipe:        readyPipe,
		ReleasePipe:      releasePipe,
		WorkingDirectory: directory,
		Executable:       executable,
		Marshal:          marshal,
	}
}

func mustCanonical(t *testing.T, spec Spec) []byte {
	t.Helper()
	raw, err := spec.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func realPipeBinding(t *testing.T, file *os.File) ObjectBinding {
	t.Helper()
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		t.Fatal(err)
	}
	return ObjectBinding{Device: uint64(stat.Dev), Inode: stat.Ino, Mode: uint32(stat.Mode), UID: stat.Uid, GID: stat.Gid, Size: stat.Size, Nlink: uint64(stat.Nlink)}
}
