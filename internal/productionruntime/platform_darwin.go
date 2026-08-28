//go:build darwin && arm64

package productionruntime

func platformProfile() (string, error) { return DarwinLocalDogfoodProfile, nil }
