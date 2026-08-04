package verification

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
)

func CollectArtifacts(root string, specs []Deliverable, now time.Time) ([]Artifact, []Gate) {
	var artifacts []Artifact
	var gates []Gate
	seenIDs := map[string]bool{}
	for _, spec := range specs {
		gateID := "artifact:" + spec.ID
		gate := Gate{ID: gateID, Category: "artifact", Required: spec.Required, Status: "pass", Evidence: []string{}}
		if spec.Kind == "publication" && spec.PathGlob == "" {
			artifacts = append(artifacts, Artifact{ID: spec.ID, Kind: spec.Kind, MediaType: spec.MediaType, Producer: "publisher", Required: spec.Required, Status: "expected", CreatedAt: now.UTC(), RelatedGates: []string{gateID}, Description: spec.Description})
			gate.Summary = "Publication Artifact 将在发布阶段产生"
			gates = append(gates, gate)
			continue
		}
		if spec.MinimumCount <= 0 {
			spec.MinimumCount = 1
		}
		matches, matchErr := matchingPaths(root, spec.PathGlob)
		if matchErr != nil {
			gate.Status, gate.Summary = "error", matchErr.Error()
			gates = append(gates, gate)
			continue
		}
		validCount := 0
		for index, path := range matches {
			artifactID := spec.ID
			if len(matches) > 1 {
				artifactID = fmt.Sprintf("%s:%d", spec.ID, index+1)
			}
			if seenIDs[artifactID] {
				gate.Status, gate.Summary = "error", "重复 Artifact ID: "+artifactID
				continue
			}
			seenIDs[artifactID] = true
			artifact := Artifact{ID: artifactID, Kind: spec.Kind, MediaType: spec.MediaType, Producer: "worker", Required: spec.Required, Status: "invalid", PathRoot: "repository", RelativePath: path, CreatedAt: now.UTC(), RelatedGates: []string{gateID}, Description: spec.Description}
			absolute, info, err := secureRegularFile(root, path)
			if err == nil {
				digest, prefix, readErr := inspectArtifactFile(absolute)
				if readErr != nil {
					err = readErr
				} else {
					artifact.Status = "validated"
					artifact.ByteSize = info.Size()
					artifact.Digest = digest
					if artifact.MediaType == "" {
						artifact.MediaType = http.DetectContentType(prefix)
					}
					validCount++
					gate.Evidence = append(gate.Evidence, "repository://"+path)
				}
			}
			if err != nil {
				artifact.Description = strings.TrimSpace(spec.Description + "; " + err.Error())
			}
			artifacts = append(artifacts, artifact)
		}
		if validCount < spec.MinimumCount {
			gate.Status = "fail"
			gate.Summary = fmt.Sprintf("有效交付物 %d，要求至少 %d", validCount, spec.MinimumCount)
			if len(matches) == 0 {
				artifactID := spec.ID
				if !seenIDs[artifactID] {
					seenIDs[artifactID] = true
					artifacts = append(artifacts, Artifact{ID: artifactID, Kind: spec.Kind, MediaType: spec.MediaType, Producer: "worker", Required: spec.Required, Status: "missing", CreatedAt: now.UTC(), RelatedGates: []string{gateID}, Description: spec.Description})
				}
			}
		} else {
			gate.Summary = fmt.Sprintf("已验证 %d 个交付物", validCount)
		}
		gates = append(gates, gate)
	}
	return artifacts, gates
}

func inspectArtifactFile(path string) (string, []byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", nil, err
	}
	defer file.Close()
	prefix := make([]byte, 512)
	count, err := io.ReadFull(file, prefix)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", nil, err
	}
	prefix = prefix[:count]
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", nil, err
	}
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", nil, err
	}
	return "sha256:" + hex.EncodeToString(hasher.Sum(nil)), prefix, nil
}

func matchingPaths(root, pattern string) ([]string, error) {
	if err := validateRelativePath(pattern); err != nil {
		return nil, err
	}
	var matches []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if relative == ".git" || strings.HasPrefix(relative, ".git/") || relative == ".marshal" || strings.HasPrefix(relative, ".marshal/") {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		matched, err := doublestar.Match(pattern, relative)
		if err != nil {
			return err
		}
		if matched && !entry.IsDir() {
			matches = append(matches, relative)
		}
		return nil
	})
	sort.Strings(matches)
	return matches, err
}

func secureRegularFile(root, relative string) (string, os.FileInfo, error) {
	if err := validateRelativePath(relative); err != nil {
		return "", nil, err
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", nil, err
	}
	candidate := filepath.Join(canonicalRoot, filepath.FromSlash(relative))
	info, err := os.Lstat(candidate)
	if err != nil {
		return "", nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, targetErr := filepath.EvalSymlinks(candidate)
		if targetErr == nil && !within(canonicalRoot, target) {
			return "", nil, fmt.Errorf("symlink escapes worktree: %s", relative)
		}
		return "", nil, fmt.Errorf("symlink is not a regular artifact: %s", relative)
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", nil, err
	}
	if !within(canonicalRoot, resolved) {
		return "", nil, fmt.Errorf("artifact escapes worktree: %s", relative)
	}
	info, err = os.Stat(resolved)
	if err != nil {
		return "", nil, err
	}
	if !info.Mode().IsRegular() {
		return "", nil, errors.New("artifact is not a regular file")
	}
	return resolved, info, nil
}

func within(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
