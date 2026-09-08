//go:build !darwin && !linux

package sqlitestore

type privateFiles struct{}

func openPrivateFiles(string, bool) (*privateFiles, error) { return nil, ErrUnavailable }
func (*privateFiles) check() error                         { return ErrUnavailable }
func (*privateFiles) sync() error                          { return ErrUnavailable }
func (*privateFiles) close() error                         { return nil }
