//go:build !linux

package codex

import (
	"errors"
	"os"
)

func heldExecutableStat(*os.File) (heldExecutableStatV1, error) {
	return heldExecutableStatV1{}, errors.New("held codex executable identity requires linux statx")
}
