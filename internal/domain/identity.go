package domain

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
)

// TaskID uniquely identifies a Task within Marshal.
type TaskID string

// RunID uniquely identifies one Run of a Task.
type RunID string

// AttemptID uniquely identifies one Worker Attempt within a Run.
type AttemptID string

// EventID uniquely identifies one Run Event in the Journal.
type EventID string

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,159}$`)

// ValidateID reports whether value is a syntactically valid Marshal ID.
func ValidateID(value string) error {
	if !idPattern.MatchString(value) {
		return fmt.Errorf("invalid Marshal ID %q", value)
	}
	return nil
}

// NewID generates a fresh Marshal ID composed of the given prefix and
// random entropy, e.g. "run:<32 hex chars>".
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
