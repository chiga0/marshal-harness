package repository

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type State struct {
	RepositoryRoot string
	StateRoot      string
}

func Discover(start string) (State, error) {
	command := exec.Command("git", "-C", start, "rev-parse", "--show-toplevel")
	output, err := command.Output()
	if err != nil {
		return State{}, fmt.Errorf("locate Git repository: %w", err)
	}
	root, err := filepath.EvalSymlinks(strings.TrimSpace(string(output)))
	if err != nil {
		return State{}, fmt.Errorf("canonicalize repository root: %w", err)
	}
	stateRoot := os.Getenv("MARSHAL_STATE_DIR")
	if stateRoot == "" {
		stateRoot = filepath.Join(root, ".marshal")
	} else if !filepath.IsAbs(stateRoot) {
		return State{}, errors.New("MARSHAL_STATE_DIR must be absolute")
	}
	stateRoot = filepath.Clean(stateRoot)
	if canonicalParent, evalErr := filepath.EvalSymlinks(filepath.Dir(stateRoot)); evalErr == nil {
		stateRoot = filepath.Join(canonicalParent, filepath.Base(stateRoot))
	}
	relative, err := filepath.Rel(root, stateRoot)
	if err != nil {
		return State{}, fmt.Errorf("compare state directory with repository: %w", err)
	}
	if relative != ".marshal" && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return State{}, errors.New("MARSHAL_STATE_DIR inside the repository must be the default .marshal directory")
	}
	return State{RepositoryRoot: root, StateRoot: stateRoot}, nil
}

func (s State) Init() error {
	if err := os.MkdirAll(s.StateRoot, 0o700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	if err := s.bindIdentity(); err != nil {
		return err
	}
	if s.StateRoot != filepath.Join(s.RepositoryRoot, ".marshal") {
		return nil
	}
	gitDirectory, err := gitPath(s.RepositoryRoot, "info/exclude")
	if err != nil {
		return err
	}
	file, err := os.OpenFile(gitDirectory, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open Git exclude file: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) == "/.marshal/" {
			return scanner.Err()
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		return err
	}
	separator := ""
	if info.Size() > 0 {
		separator = "\n"
	}
	if _, err := file.WriteString(separator + "/.marshal/\n"); err != nil {
		return fmt.Errorf("update Git exclude file: %w", err)
	}
	return file.Sync()
}

func (s State) ValidateIdentity() error {
	data, err := os.ReadFile(filepath.Join(s.StateRoot, "repo.json"))
	if err != nil {
		return fmt.Errorf("read repository identity: %w", err)
	}
	var identity struct {
		APIVersion     string `json:"apiVersion"`
		Kind           string `json:"kind"`
		RepositoryRoot string `json:"repositoryRoot"`
	}
	if err := json.Unmarshal(data, &identity); err != nil {
		return fmt.Errorf("decode repository identity: %w", err)
	}
	if identity.APIVersion != "marshal.dev/v1alpha1" || identity.Kind != "RepositoryIdentity" {
		return errors.New("unsupported repository identity record")
	}
	if identity.RepositoryRoot != s.RepositoryRoot {
		return fmt.Errorf("state directory belongs to repository %q, not %q", identity.RepositoryRoot, s.RepositoryRoot)
	}
	return nil
}

func (s State) bindIdentity() error {
	path := filepath.Join(s.StateRoot, "repo.json")
	_, err := os.Stat(path)
	if err == nil {
		return s.ValidateIdentity()
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read repository identity: %w", err)
	}
	data, err := json.MarshalIndent(struct {
		APIVersion     string `json:"apiVersion"`
		Kind           string `json:"kind"`
		RepositoryRoot string `json:"repositoryRoot"`
	}{"marshal.dev/v1alpha1", "RepositoryIdentity", s.RepositoryRoot}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write repository identity: %w", err)
	}
	return nil
}

func gitPath(root, name string) (string, error) {
	command := exec.Command("git", "-C", root, "rev-parse", "--git-path", name)
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("resolve Git path: %w", err)
	}
	path := strings.TrimSpace(string(output))
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	return filepath.Clean(path), nil
}
