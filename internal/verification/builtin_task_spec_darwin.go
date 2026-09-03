//go:build darwin

package verification

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

type heldBuiltinName struct {
	parent int
	name   string
	stat   unix.Stat_t
}

type builtinArtifactPlatformHooks struct {
	afterLeafOpen      func()
	beforeFinalRecheck func()
}

func readTaskSpecBuiltinArtifact(ctx context.Context, isolate, pattern string, hooks builtinArtifactReadHooks) (builtinArtifact, string) {
	if ctx.Err() != nil {
		return builtinArtifact{}, reasonBuiltinTimeout
	}
	relative := pattern
	if err := validateRelativePath(relative); err != nil {
		return builtinArtifact{}, reasonArtifactDenied
	}
	canonicalIsolate, err := filepath.EvalSymlinks(isolate)
	if err != nil {
		return builtinArtifact{}, reasonArtifactDenied
	}
	root, err := unix.Open(canonicalIsolate, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY, 0)
	if err != nil {
		return builtinArtifact{}, reasonArtifactDenied
	}
	defer unix.Close(root)
	var rootStat unix.Stat_t
	if unix.Fstat(root, &rootStat) != nil || rootStat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return builtinArtifact{}, reasonArtifactDenied
	}
	current := root
	openedDirectories := []int{}
	heldNames := []heldBuiltinName{}
	parts := strings.Split(filepath.FromSlash(relative), string(filepath.Separator))
	for _, part := range parts[:len(parts)-1] {
		var named unix.Stat_t
		if part == "" || unix.Fstatat(current, part, &named, unix.AT_SYMLINK_NOFOLLOW) != nil || named.Mode&unix.S_IFMT != unix.S_IFDIR {
			closeBuiltinFDs(openedDirectories)
			return builtinArtifact{}, reasonArtifactDenied
		}
		next, openErr := unix.Openat(current, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY, 0)
		if openErr != nil {
			closeBuiltinFDs(openedDirectories)
			return builtinArtifact{}, reasonArtifactDenied
		}
		var held unix.Stat_t
		if unix.Fstat(next, &held) != nil || !sameBuiltinStat(named, held) {
			unix.Close(next)
			closeBuiltinFDs(openedDirectories)
			return builtinArtifact{}, reasonArtifactDenied
		}
		heldNames = append(heldNames, heldBuiltinName{parent: current, name: part, stat: held})
		openedDirectories = append(openedDirectories, next)
		current = next
	}
	defer closeBuiltinFDs(openedDirectories)
	leaf := parts[len(parts)-1]
	var namedLeaf unix.Stat_t
	if leaf == "" || unix.Fstatat(current, leaf, &namedLeaf, unix.AT_SYMLINK_NOFOLLOW) != nil || namedLeaf.Mode&unix.S_IFMT != unix.S_IFREG || namedLeaf.Nlink != 1 {
		return builtinArtifact{}, reasonArtifactDenied
	}
	if namedLeaf.Size < 0 || namedLeaf.Size > builtinTaskSpecMaxBytes {
		return builtinArtifact{}, reasonArtifactTooLarge
	}
	leafFD, err := unix.Openat(current, leaf, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY|unix.O_NONBLOCK, 0)
	if err != nil {
		return builtinArtifact{}, reasonArtifactDenied
	}
	file := os.NewFile(uintptr(leafFD), "marshal-verifier-builtin-artifact")
	if file == nil {
		unix.Close(leafFD)
		return builtinArtifact{}, reasonArtifactDenied
	}
	defer file.Close()
	var heldLeaf unix.Stat_t
	if unix.Fstat(leafFD, &heldLeaf) != nil || !sameBuiltinStat(namedLeaf, heldLeaf) || heldLeaf.Mode&unix.S_IFMT != unix.S_IFREG || heldLeaf.Nlink != 1 {
		return builtinArtifact{}, reasonArtifactDenied
	}
	if hooks.afterLeafOpen != nil {
		hooks.afterLeafOpen()
	}
	if ctx.Err() != nil {
		return builtinArtifact{}, reasonBuiltinTimeout
	}
	raw, err := io.ReadAll(io.LimitReader(file, builtinTaskSpecMaxBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return builtinArtifact{}, reasonBuiltinTimeout
		}
		return builtinArtifact{}, reasonArtifactDenied
	}
	if int64(len(raw)) > builtinTaskSpecMaxBytes {
		return builtinArtifact{}, reasonArtifactTooLarge
	}
	digestBytes := sha256.Sum256(raw)
	artifact := builtinArtifact{Bytes: raw, Digest: "sha256:" + hex.EncodeToString(digestBytes[:])}
	if hooks.beforeFinalRecheck != nil {
		hooks.beforeFinalRecheck()
	}
	var finalLeaf unix.Stat_t
	if unix.Fstat(leafFD, &finalLeaf) != nil || !sameBuiltinStat(heldLeaf, finalLeaf) || !recheckBuiltinNames(canonicalIsolate, rootStat, heldNames, current, leaf, heldLeaf) {
		return builtinArtifact{}, reasonArtifactDenied
	}
	if ctx.Err() != nil {
		return builtinArtifact{}, reasonBuiltinTimeout
	}
	return artifact, ""
}

func recheckBuiltinNames(isolate string, rootStat unix.Stat_t, held []heldBuiltinName, leafParent int, leaf string, leafStat unix.Stat_t) bool {
	reopened, err := unix.Open(isolate, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY, 0)
	if err != nil {
		return false
	}
	defer unix.Close(reopened)
	var currentRoot unix.Stat_t
	if unix.Fstat(reopened, &currentRoot) != nil || !sameBuiltinStat(rootStat, currentRoot) {
		return false
	}
	for _, item := range held {
		var named unix.Stat_t
		if unix.Fstatat(item.parent, item.name, &named, unix.AT_SYMLINK_NOFOLLOW) != nil || !sameBuiltinStat(item.stat, named) {
			return false
		}
	}
	var namedLeaf unix.Stat_t
	return unix.Fstatat(leafParent, leaf, &namedLeaf, unix.AT_SYMLINK_NOFOLLOW) == nil && sameBuiltinStat(leafStat, namedLeaf)
}

func sameBuiltinStat(left, right unix.Stat_t) bool {
	return left.Dev == right.Dev && left.Ino == right.Ino && left.Mode == right.Mode && left.Nlink == right.Nlink && left.Uid == right.Uid && left.Gid == right.Gid && left.Size == right.Size && left.Mtim == right.Mtim && left.Ctim == right.Ctim && left.Btim == right.Btim && left.Gen == right.Gen
}

func closeBuiltinFDs(fds []int) {
	for index := len(fds) - 1; index >= 0; index-- {
		_ = unix.Close(fds[index])
	}
}
