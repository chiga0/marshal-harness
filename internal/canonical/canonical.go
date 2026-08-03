package canonical

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/gowebpki/jcs"
)

func JSON(input []byte) ([]byte, error) {
	canonical, err := jcs.Transform(input)
	if err != nil {
		return nil, fmt.Errorf("canonicalize JSON: %w", err)
	}
	return canonical, nil
}

func DigestBytes(input []byte) string {
	sum := sha256.Sum256(input)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func DigestJSON(input []byte) (string, error) {
	canonical, err := JSON(input)
	if err != nil {
		return "", err
	}
	return DigestBytes(canonical), nil
}
