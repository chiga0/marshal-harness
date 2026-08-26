//go:build !darwin

package selfidentity

import "os"

func observeCurrentPath(string, func()) (CurrentPathObjectV1, error) {
	return CurrentPathObjectV1{}, reject(ReasonProfileMismatch)
}

func observePathIdentity(string) (CurrentPathObjectV1, error) {
	return CurrentPathObjectV1{}, reject(ReasonProfileMismatch)
}

func openActivationFile(string) (*os.File, error) { return nil, reject(ReasonProfileMismatch) }

func platformSupported() bool { return false }
