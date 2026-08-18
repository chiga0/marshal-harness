package authorityprovider

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

const (
	SignatureAlgorithmEd25519  = "Ed25519"
	SignatureEncodingBase64URL = "base64url-unpadded"
)

type SignedObjectEnvelopeV1 struct {
	ObjectDigest       string `json:"objectDigest"`
	SignatureAlgorithm string `json:"signatureAlgorithm"`
	SignatureEncoding  string `json:"signatureEncoding"`
	KeyID              string `json:"keyId"`
	KeyEpoch           uint64 `json:"keyEpoch"`
	SignatureDomain    string `json:"signatureDomain"`
	Signature          string `json:"signature"`
}

type VerificationKey struct {
	PublicKey ed25519.PublicKey
	Usage     string
	Revoked   bool
}

type KeyResolver interface {
	Resolve(keyID string, keyEpoch uint64) (VerificationKey, bool)
}

type StaticKeyring map[string]VerificationKey

func (s StaticKeyring) Resolve(keyID string, keyEpoch uint64) (VerificationKey, bool) {
	key, ok := s[keyID+":"+uintString(keyEpoch)]
	return key, ok
}

func ObjectDigest(unsignedCanonicalObject []byte) (string, error) {
	return canonical.DigestJSON(unsignedCanonicalObject)
}

func ValidateSignedObject(unsignedObject []byte, envelope SignedObjectEnvelopeV1, expectedDomain, expectedUsage string, resolver KeyResolver) error {
	if resolver == nil || expectedDomain == "" || expectedUsage == "" {
		return protocolError(CodeIdentityMismatch, "signing-policy-invalid")
	}
	digest, err := ObjectDigest(unsignedObject)
	if err != nil || digest != envelope.ObjectDigest {
		return protocolError(CodeIdentityMismatch, "signed-object-digest-invalid")
	}
	if envelope.SignatureAlgorithm != SignatureAlgorithmEd25519 || envelope.SignatureEncoding != SignatureEncodingBase64URL || envelope.SignatureDomain != expectedDomain || envelope.KeyID == "" || envelope.KeyEpoch == 0 {
		return protocolError(CodeIdentityMismatch, "signed-object-envelope-invalid")
	}
	key, ok := resolver.Resolve(envelope.KeyID, envelope.KeyEpoch)
	if !ok || key.Revoked || key.Usage != expectedUsage || len(key.PublicKey) != ed25519.PublicKeySize {
		return protocolError(CodeIdentityMismatch, "signing-key-invalid")
	}
	encoding := base64.RawURLEncoding.Strict()
	signature, err := encoding.DecodeString(envelope.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize || encoding.EncodeToString(signature) != envelope.Signature {
		return protocolError(CodeIdentityMismatch, "signature-invalid")
	}
	message := append([]byte(envelope.SignatureDomain), []byte(envelope.ObjectDigest)...)
	if !ed25519.Verify(key.PublicKey, message, signature) {
		return protocolError(CodeIdentityMismatch, "signature-invalid")
	}
	return nil
}

func SignObjectForFake(unsignedObject []byte, domain, keyID string, keyEpoch uint64, privateKey ed25519.PrivateKey) (SignedObjectEnvelopeV1, error) {
	if len(privateKey) != ed25519.PrivateKeySize || keyID == "" || keyEpoch == 0 || domain == "" {
		return SignedObjectEnvelopeV1{}, errors.New("authorityprovider: invalid fake signing configuration")
	}
	digest, err := ObjectDigest(unsignedObject)
	if err != nil {
		return SignedObjectEnvelopeV1{}, err
	}
	message := append([]byte(domain), []byte(digest)...)
	signature := ed25519.Sign(privateKey, message)
	return SignedObjectEnvelopeV1{ObjectDigest: digest, SignatureAlgorithm: SignatureAlgorithmEd25519, SignatureEncoding: SignatureEncodingBase64URL, KeyID: keyID, KeyEpoch: keyEpoch, SignatureDomain: domain, Signature: base64.RawURLEncoding.EncodeToString(signature)}, nil
}

func uintString(value uint64) string {
	if value == 0 {
		return "0"
	}
	var buffer [20]byte
	index := len(buffer)
	for value > 0 {
		index--
		buffer[index] = byte('0' + value%10)
		value /= 10
	}
	return string(buffer[index:])
}
