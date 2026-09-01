//go:build !darwin

package selfidentity

import "os"

func observeCurrentPath(string, func()) (CurrentPathObjectV2, error) {
	return CurrentPathObjectV2{}, reject(ReasonProfileMismatch)
}

func openActivationFile(string) (*os.File, error) { return nil, reject(ReasonProfileMismatch) }

func platformSupported() bool { return false }
