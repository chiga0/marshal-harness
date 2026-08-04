package review

import (
	"errors"
	"io"
	"os"
	"path/filepath"
)

func readBounded(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	reader := io.LimitReader(file, limit+1)
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("input exceeds safe byte limit")
	}
	return data, nil
}

func atomicWrite(path string, data []byte, overwrite bool) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if !overwrite {
		if _, err := os.Lstat(path); err == nil {
			return errors.New("immutable record already exists")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".marshal-review-*.tmp")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if !overwrite {
		if err := os.Link(name, path); err != nil {
			return err
		}
		if err := os.Remove(name); err != nil {
			return err
		}
	} else if err := os.Rename(name, path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
