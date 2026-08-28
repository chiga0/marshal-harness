//go:build !darwin

package processsupervisor

import "os"

func NewPlatformMechanics(*os.File) (Mechanics, error) { return nil, ErrUnavailable }

func inheritedInvocationKind() (string, error) { return "", ErrUnavailable }

func runLaunchChild() error { return ErrUnavailable }
