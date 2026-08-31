//go:build !unix

package runstore

import (
	"errors"
	"os"
)

func newFromStateRootDescriptor(*os.File) (*Store, error) {
	return nil, errors.New("descriptor-bound run store is unsupported on this platform")
}

func duplicateDirectory(*os.File) (*os.File, error) {
	return nil, errors.New("descriptor-bound run store is unsupported on this platform")
}
