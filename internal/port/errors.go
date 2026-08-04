package port

import (
	"errors"
	"fmt"
)

type permanentError struct{ err error }

func (e permanentError) Error() string   { return e.err.Error() }
func (e permanentError) Unwrap() error   { return e.err }
func (e permanentError) Permanent() bool { return true }

func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return permanentError{err: err}
}

func Permanentf(format string, args ...any) error { return Permanent(fmt.Errorf(format, args...)) }

func IsPermanent(err error) bool {
	var target interface{ Permanent() bool }
	return errors.As(err, &target) && target.Permanent()
}
