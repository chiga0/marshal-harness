//go:build !darwin

package processsupervisor

import "os"

func NewPlatformMechanics(*os.File) (Mechanics, error) { return nil, ErrUnavailable }
