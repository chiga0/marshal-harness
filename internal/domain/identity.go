package domain

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
)

type TaskID string
type RunID string
type AttemptID string
type EventID string

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,159}$`)

func ValidateID(value string) error {
	if !idPattern.MatchString(value) {
		return fmt.Errorf("invalid Marshal ID %q", value)
	}
	return nil
}

func NewID(prefix string) (string, error) {
	if !regexp.MustCompile(`^[a-z][a-z0-9-]*$`).MatchString(prefix) {
		return "", fmt.Errorf("invalid ID prefix %q", prefix)
	}
	var entropy [16]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", fmt.Errorf("generate ID entropy: %w", err)
	}
	return prefix + ":" + hex.EncodeToString(entropy[:]), nil
}
