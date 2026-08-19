package contract

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

// ReadCurrentBundleRequestPayload is the transport-neutral request payload
// frozen by ADR 0038. It is intentionally validated separately from the
// closed APAP operation registry: ReadCurrentBundle remains unregistered
// until its complete request/response schema and conformance vectors land.
type ReadCurrentBundleRequestPayload struct {
	MinProviderSequence uint64
}

// ReadCurrentBundleResponsePayload carries only non-fd current-bundle data.
// Manifest, detached signature, and anchor receipt are retained as canonical
// JSON so the profile-specific authority package can apply its own closed
// schemas and signature domains before eligibility is considered.
type ReadCurrentBundleResponsePayload struct {
	BundleDigest      string
	Manifest          []byte
	DetachedSignature []byte
	AnchorReceipt     []byte
}

// ValidateReadCurrentBundleRequestPayload validates the shared request
// boundary without granting eligibility or registering the operation.
func ValidateReadCurrentBundleRequestPayload(raw []byte) (ReadCurrentBundleRequestPayload, error) {
	object, err := admitObject(raw, []string{"minProviderSequence"})
	if err != nil {
		return ReadCurrentBundleRequestPayload{}, err
	}
	sequence, ok := rawUint(object["minProviderSequence"])
	if !ok {
		return ReadCurrentBundleRequestPayload{}, errors.New("apap contract: current bundle sequence rejected")
	}
	return ReadCurrentBundleRequestPayload{MinProviderSequence: sequence}, nil
}

// ValidateReadCurrentBundleResponsePayload validates the shared response
// boundary. The nested objects are required to be canonical JSON objects and
// are cloned before returning; profile-specific validation must still verify
// their exact fields, signatures, and anchor continuity.
func ValidateReadCurrentBundleResponsePayload(raw []byte, minimumSequence uint64) (ReadCurrentBundleResponsePayload, error) {
	object, err := admitObject(raw, []string{"anchorReceipt", "bundleDigest", "detachedSignature", "manifest"})
	if err != nil || !validDigest(rawString(object["bundleDigest"])) {
		return ReadCurrentBundleResponsePayload{}, errors.New("apap contract: current bundle response framing rejected")
	}
	for _, field := range []string{"manifest", "detachedSignature", "anchorReceipt"} {
		if err := admitCanonicalObject(object[field]); err != nil {
			return ReadCurrentBundleResponsePayload{}, errors.New("apap contract: current bundle response object rejected")
		}
	}
	manifest := slices.Clone(object["manifest"])
	if sequence, ok := objectUint(manifest, "providerSequence"); ok && sequence < minimumSequence {
		return ReadCurrentBundleResponsePayload{}, errors.New("apap contract: current bundle sequence below minimum")
	}
	return ReadCurrentBundleResponsePayload{
		BundleDigest:      rawString(object["bundleDigest"]),
		Manifest:          manifest,
		DetachedSignature: slices.Clone(object["detachedSignature"]),
		AnchorReceipt:     slices.Clone(object["anchorReceipt"]),
	}, nil
}

func admitCanonicalObject(raw json.RawMessage) error {
	if len(raw) == 0 || raw[0] != '{' || raw[len(raw)-1] != '}' {
		return errors.New("object required")
	}
	canonicalRaw, err := canonical.JSON(raw)
	if err != nil || !bytes.Equal(raw, canonicalRaw) {
		return errors.New("canonical object required")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return errors.New("object required")
	}
	return nil
}

func objectUint(raw []byte, field string) (uint64, bool) {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil {
		return 0, false
	}
	value, ok := object[field]
	if !ok {
		return 0, false
	}
	return rawUint(value)
}
