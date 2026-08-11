// Package canonical provides RFC 8785 JSON Canonicalization Scheme (JCS)
// admission for Marshal. JSON and DigestJSON delegate to jcs.Transform as the
// sole arbiter of input validity — including recursive rejection of duplicate
// object member names — and map every parse or canonicalization failure to a
// single stable error that exposes none of the original input.
package canonical

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/gowebpki/jcs"
)

// ErrRejected is the single stable error returned for any input that cannot be
// canonicalized, including malformed JSON and objects that contain two members
// whose unescaped names are equal. Its text is fixed and never includes the
// original input, member name, value, or token, so sensitive material can never
// surface through this path. It is a sentinel rather than a wrapped error: the
// underlying JCS error text — which may itself echo a duplicate member name or
// other input-derived material — is discarded entirely and never wrapped.
var ErrRejected = errors.New("canonical: input rejected")

// JSON canonicalizes input according to RFC 8785 JCS. jcs.Transform is called
// exactly once and is the sole arbiter of input validity; it recursively
// rejects duplicate object members at the root, in nested objects, and in
// objects embedded in arrays, including escaped-name and empty-name duplicates.
// No second parser, pre-scan, or alternative serializer is used, so there is
// no divergence in validity decisions, stack budget, or allocation behavior
// from a second pass over untrusted input.
//
// On success the canonical bytes, property ordering, whitespace handling, and
// number normalization (including the equivalence of 1 and 1.0) are exactly
// those produced by jcs.Transform and are returned unchanged.
//
// Any parse or canonicalization failure is mapped to ErrRejected. Callers can
// therefore rely on errors.Is(err, ErrRejected) without risking leakage of the
// underlying JCS text, a member name, a value, or a credential.
func JSON(input []byte) ([]byte, error) {
	canonical, err := jcs.Transform(input)
	if err != nil {
		return nil, ErrRejected
	}
	return canonical, nil
}

// DigestBytes returns the "sha256:"-prefixed hex digest of input.
func DigestBytes(input []byte) string {
	sum := sha256.Sum256(input)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// DigestJSON canonicalizes input with JSON and returns its "sha256:" digest.
// On any rejection it returns an empty digest together with the same stable
// ErrRejected error, so callers never observe a partial digest for rejected
// input.
func DigestJSON(input []byte) (string, error) {
	canonical, err := JSON(input)
	if err != nil {
		return "", err
	}
	return DigestBytes(canonical), nil
}
