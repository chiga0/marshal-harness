// Package contract implements the closed, transport-neutral APAP v1 wire
// contracts frozen by ADR 0038. It intentionally registers only Describe,
// BeginProbe, and StageBundleLeafBatch; adding another operation requires its
// own closed request/response schema and conformance vectors first.
package contract

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

const (
	MaxEnvelopeBytes = 64 << 10
	ControlFamily    = "marshal.agent-production-authority"
	ControlAudience  = "marshal.agent-production-authority.local"
	RequestSchema    = "marshal.agent-production-authority.request.v1"
	ResponseSchema   = "marshal.agent-production-authority.response.v1"
	FDTableSchema    = "marshal.agent-production-authority.fd-table.v1"
)

type Operation string

const (
	Describe             Operation = "Describe"
	BeginProbe           Operation = "BeginProbe"
	StageBundleLeafBatch Operation = "StageBundleLeafBatch"
)

type FDDescriptor struct {
	Role           string `json:"role"`
	Index          uint64 `json:"index"`
	IdentityDigest string `json:"identityDigest"`
}

type ValidatedRequest struct {
	Operation                Operation
	AuthorityProfile         string
	ExpectedProviderSequence *uint64
	Payload                  json.RawMessage
	RequestEnvelopeDigest    string
}

type ValidatedResponse struct {
	Operation              Operation
	AuthorityProfile       string
	ObservedSequence       uint64
	SafeCode               string
	Payload                json.RawMessage
	ResponseEnvelopeDigest string
}

// ValidateFDTable admits the canonical serialized descriptor table before any
// SCM_RIGHTS handle is interpreted. The transport must still bind each table
// entry to the same-position received fd and verify the held identity digest.
func ValidateFDTable(raw []byte) (Operation, []FDDescriptor, error) {
	object, err := admitObject(raw, []string{"descriptors", "operation", "schemaVersion"})
	if err != nil || !equalString(object, "schemaVersion", FDTableSchema) {
		return "", nil, errors.New("apap contract: fd table framing rejected")
	}
	op := Operation(rawString(object["operation"]))
	var encoded []json.RawMessage
	if err := json.Unmarshal(object["descriptors"], &encoded); err != nil || len(encoded) > 32 {
		return "", nil, errors.New("apap contract: fd table cardinality rejected")
	}
	descriptors := make([]FDDescriptor, len(encoded))
	for i, rawDescriptor := range encoded {
		descriptor, err := admitObject(rawDescriptor, []string{"identityDigest", "index", "role"})
		index, indexOK := rawUint(descriptor["index"])
		if err != nil || !indexOK || !validDigest(rawString(descriptor["identityDigest"])) {
			return "", nil, errors.New("apap contract: fd descriptor rejected")
		}
		descriptors[i] = FDDescriptor{Role: rawString(descriptor["role"]), Index: index, IdentityDigest: rawString(descriptor["identityDigest"])}
	}
	switch op {
	case Describe:
		if len(descriptors) != 0 {
			return "", nil, errors.New("apap contract: Describe fd table rejected")
		}
	case BeginProbe:
		if err := validateBeginProbeFDs(descriptors); err != nil {
			return "", nil, err
		}
	case StageBundleLeafBatch:
		if len(descriptors) < 1 || len(descriptors) > 24 {
			return "", nil, errors.New("apap contract: stage fd cardinality rejected")
		}
		for i, descriptor := range descriptors {
			if descriptor.Role != "bundleLeaf" || descriptor.Index != uint64(i) {
				return "", nil, errors.New("apap contract: stage fd order rejected")
			}
		}
	default:
		return "", nil, errors.New("apap contract: operation has no registered schema")
	}
	return op, descriptors, nil
}

var (
	digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	idPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	noncePattern  = regexp.MustCompile(`^[A-Za-z0-9._:-]{8,128}$`)
)

var requestFields = []string{"audience", "authorityProfile", "callerPrincipalDigest", "commandId", "expectedProviderSequence", "expiresAt", "issuedAt", "nonce", "operation", "payload", "protocolFamily", "protocolVersion", "providerInstanceId", "requestEnvelopeDigest", "requestId", "schemaVersion"}
var responseFields = []string{"audience", "authorityProfile", "commandId", "observedProviderSequence", "operation", "payload", "protocolFamily", "protocolVersion", "providerInstanceId", "requestId", "responseEnvelopeDigest", "safeCode", "safeMessage", "schemaVersion"}

// ValidateControlRequest performs canonical admission, exact member/type
// validation, operation-specific payload validation, digest binding, and fd
// table validation. It never accepts an operation absent from the v1 schemas.
func ValidateControlRequest(raw []byte, fds []FDDescriptor) (ValidatedRequest, error) {
	object, err := admitObject(raw, requestFields)
	if err != nil {
		return ValidatedRequest{}, err
	}
	if !equalString(object, "schemaVersion", RequestSchema) || !equalString(object, "protocolFamily", ControlFamily) || !equalUint(object, "protocolVersion", 1) || !equalString(object, "audience", ControlAudience) {
		return ValidatedRequest{}, errors.New("apap contract: framing rejected")
	}
	for _, field := range []string{"requestId", "commandId", "providerInstanceId"} {
		if !validID(rawString(object[field])) {
			return ValidatedRequest{}, errors.New("apap contract: identity rejected")
		}
	}
	if !validDigest(rawString(object["callerPrincipalDigest"])) || !validProfile(rawString(object["authorityProfile"])) || !noncePattern.MatchString(rawString(object["nonce"])) {
		return ValidatedRequest{}, errors.New("apap contract: identity rejected")
	}
	issued, ok := canonicalTime(object["issuedAt"])
	if !ok {
		return ValidatedRequest{}, errors.New("apap contract: issuedAt rejected")
	}
	expires, ok := canonicalTime(object["expiresAt"])
	if !ok || !expires.After(issued) {
		return ValidatedRequest{}, errors.New("apap contract: expiresAt rejected")
	}
	op := Operation(rawString(object["operation"]))
	var expected *uint64
	if bytes.Equal(object["expectedProviderSequence"], []byte("null")) {
		if op != Describe {
			return ValidatedRequest{}, errors.New("apap contract: sequence rejected")
		}
	} else {
		value, ok := rawUint(object["expectedProviderSequence"])
		if !ok || op == Describe {
			return ValidatedRequest{}, errors.New("apap contract: sequence rejected")
		}
		expected = &value
	}
	if err := validateRequestPayload(op, object["payload"], fds, expires); err != nil {
		return ValidatedRequest{}, err
	}
	want := rawString(object["requestEnvelopeDigest"])
	if !validDigest(want) || digestDetached(object, "requestEnvelopeDigest") != want {
		return ValidatedRequest{}, errors.New("apap contract: request digest rejected")
	}
	return ValidatedRequest{Operation: op, AuthorityProfile: rawString(object["authorityProfile"]), ExpectedProviderSequence: expected, Payload: slices.Clone(object["payload"]), RequestEnvelopeDigest: want}, nil
}

// ValidateControlResponse validates the closed response surface. Successful
// responses require the operation-specific payload and an empty safeMessage;
// failures require a null payload and therefore cannot smuggle success data.
func ValidateControlResponse(raw []byte) (ValidatedResponse, error) {
	object, err := admitObject(raw, responseFields)
	if err != nil {
		return ValidatedResponse{}, err
	}
	if !equalString(object, "schemaVersion", ResponseSchema) || !equalString(object, "protocolFamily", ControlFamily) || !equalUint(object, "protocolVersion", 1) || !equalString(object, "audience", ControlAudience) {
		return ValidatedResponse{}, errors.New("apap contract: framing rejected")
	}
	for _, field := range []string{"requestId", "commandId", "providerInstanceId"} {
		if !validID(rawString(object[field])) {
			return ValidatedResponse{}, errors.New("apap contract: identity rejected")
		}
	}
	if !validProfile(rawString(object["authorityProfile"])) {
		return ValidatedResponse{}, errors.New("apap contract: profile rejected")
	}
	op := Operation(rawString(object["operation"]))
	sequence, ok := rawUint(object["observedProviderSequence"])
	if !ok {
		return ValidatedResponse{}, errors.New("apap contract: sequence rejected")
	}
	code := rawString(object["safeCode"])
	message, isString := decodeString(object["safeMessage"])
	if !isString || len(message) > 128 || !validSafeCode(code) {
		return ValidatedResponse{}, errors.New("apap contract: safe error rejected")
	}
	if code == "ok" {
		if message != "" {
			return ValidatedResponse{}, errors.New("apap contract: success message rejected")
		}
		if err := validateResponsePayload(op, object["payload"]); err != nil {
			return ValidatedResponse{}, err
		}
	} else if !bytes.Equal(object["payload"], []byte("null")) {
		return ValidatedResponse{}, errors.New("apap contract: failure payload rejected")
	}
	want := rawString(object["responseEnvelopeDigest"])
	if !validDigest(want) || digestDetached(object, "responseEnvelopeDigest") != want {
		return ValidatedResponse{}, errors.New("apap contract: response digest rejected")
	}
	return ValidatedResponse{Operation: op, AuthorityProfile: rawString(object["authorityProfile"]), ObservedSequence: sequence, SafeCode: code, Payload: slices.Clone(object["payload"]), ResponseEnvelopeDigest: want}, nil
}

// ValidateSignedObjectEnvelope validates the exact shared signing envelope,
// including strict unpadded base64url and the exact 64-byte Ed25519 signature.
func ValidateSignedObjectEnvelope(raw []byte, expectedDomain string) error {
	fields := []string{"keyEpoch", "keyId", "objectDigest", "signature", "signatureAlgorithm", "signatureDomain", "signatureEncoding"}
	object, err := admitObject(raw, fields)
	if err != nil {
		return err
	}
	if !validDigest(rawString(object["objectDigest"])) || !equalString(object, "signatureAlgorithm", "Ed25519") || !equalString(object, "signatureEncoding", "base64url-unpadded") || !validID(rawString(object["keyId"])) || !equalString(object, "signatureDomain", expectedDomain) {
		return errors.New("apap contract: signed envelope rejected")
	}
	epoch, ok := rawUint(object["keyEpoch"])
	if !ok || epoch == 0 || !validSignatureDomain(expectedDomain) {
		return errors.New("apap contract: signed envelope rejected")
	}
	encoded := rawString(object["signature"])
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(decoded) != 64 || base64.RawURLEncoding.EncodeToString(decoded) != encoded {
		return errors.New("apap contract: signature encoding rejected")
	}
	return nil
}

func validateRequestPayload(op Operation, raw json.RawMessage, fds []FDDescriptor, expires time.Time) error {
	switch op {
	case Describe:
		if _, err := admitObject(raw, nil); err != nil || len(fds) != 0 {
			return errors.New("apap contract: Describe payload or fd table rejected")
		}
	case BeginProbe:
		fields := []string{"candidateIdentityDigest", "challengeDigest", "deadline", "policyDigest", "probeArtifactDigest", "suiteDigest"}
		object, err := admitObject(raw, fields)
		if err != nil {
			return errors.New("apap contract: BeginProbe payload rejected")
		}
		for _, field := range []string{"candidateIdentityDigest", "suiteDigest", "probeArtifactDigest", "policyDigest", "challengeDigest"} {
			if !validDigest(rawString(object[field])) {
				return errors.New("apap contract: BeginProbe digest rejected")
			}
		}
		deadline, ok := canonicalTime(object["deadline"])
		if !ok || deadline.After(expires) {
			return errors.New("apap contract: BeginProbe deadline rejected")
		}
		if err := validateBeginProbeFDs(fds); err != nil {
			return err
		}
	case StageBundleLeafBatch:
		fields := []string{"bundleTransactionId", "orderedLeafDescriptors", "updateKind"}
		object, err := admitObject(raw, fields)
		if err != nil || !validID(rawString(object["bundleTransactionId"])) || !oneOf(rawString(object["updateKind"]), "evidence-update", "planned-rotation", "security-revocation") {
			return errors.New("apap contract: StageBundleLeafBatch payload rejected")
		}
		var leaves []json.RawMessage
		if err := json.Unmarshal(object["orderedLeafDescriptors"], &leaves); err != nil || len(leaves) < 1 || len(leaves) > 24 || len(leaves) != len(fds) {
			return errors.New("apap contract: leaf cardinality rejected")
		}
		previous := ""
		for i, leaf := range leaves {
			item, err := admitObject(leaf, []string{"digest", "leafKind", "mediaType", "size"})
			size, sizeOK := rawUint(item["size"])
			key := rawString(item["leafKind"]) + "\x00" + rawString(item["digest"])
			if err != nil || !validID(rawString(item["leafKind"])) || !validDigest(rawString(item["digest"])) || !equalString(item, "mediaType", "application/json") || !sizeOK || size == 0 || size > 1<<20 || (i > 0 && key <= previous) {
				return errors.New("apap contract: leaf descriptor rejected")
			}
			if fds[i].Role != "bundleLeaf" || fds[i].Index != uint64(i) || fds[i].IdentityDigest != rawString(item["digest"]) {
				return errors.New("apap contract: leaf fd binding rejected")
			}
			previous = key
		}
	default:
		return errors.New("apap contract: operation has no registered schema")
	}
	return nil
}

func validateResponsePayload(op Operation, raw json.RawMessage) error {
	switch op {
	case Describe:
		object, err := admitObject(raw, []string{"platform", "profiles", "providerBuildDigest"})
		if err != nil || !validDigest(rawString(object["providerBuildDigest"])) || !oneOf(rawString(object["platform"]), "linux", "darwin") {
			return errors.New("apap contract: Describe response rejected")
		}
		var profiles []json.RawMessage
		if err := json.Unmarshal(object["profiles"], &profiles); err != nil || len(profiles) < 1 || len(profiles) > 2 {
			return errors.New("apap contract: Describe profiles rejected")
		}
		seen := map[string]bool{}
		for _, value := range profiles {
			profile, err := admitObject(value, []string{"authorityProfile", "status"})
			name := rawString(profile["authorityProfile"])
			if err != nil || !validProfile(name) || seen[name] || !oneOf(rawString(profile["status"]), "available", "unsupported", "misconfigured") {
				return errors.New("apap contract: Describe profile rejected")
			}
			seen[name] = true
		}
	case BeginProbe:
		object, err := admitObject(raw, []string{"credentialIngressEndpointIdentityDigest", "expiresAt", "probeSessionId", "targetIsolationIdentityDigest"})
		if err != nil || !validID(rawString(object["probeSessionId"])) || !validDigest(rawString(object["targetIsolationIdentityDigest"])) || !validDigest(rawString(object["credentialIngressEndpointIdentityDigest"])) {
			return errors.New("apap contract: BeginProbe response rejected")
		}
		if _, ok := canonicalTime(object["expiresAt"]); !ok {
			return errors.New("apap contract: BeginProbe response expiry rejected")
		}
	case StageBundleLeafBatch:
		object, err := admitObject(raw, []string{"bundleTransactionId", "stagedLeafDigests", "stagingReceiptDigest"})
		if err != nil || !validID(rawString(object["bundleTransactionId"])) || !validDigest(rawString(object["stagingReceiptDigest"])) {
			return errors.New("apap contract: staging response rejected")
		}
		var digests []string
		if err := json.Unmarshal(object["stagedLeafDigests"], &digests); err != nil || len(digests) < 1 || len(digests) > 24 {
			return errors.New("apap contract: staged digests rejected")
		}
		for _, digest := range digests {
			if !validDigest(digest) {
				return errors.New("apap contract: staged digest rejected")
			}
		}
	default:
		return errors.New("apap contract: operation has no registered schema")
	}
	return nil
}

func validateBeginProbeFDs(fds []FDDescriptor) error {
	if len(fds) < 3 || len(fds) > 18 || fds[0].Role != "candidateExecutable" || fds[0].Index != 0 || fds[1].Role != "scratchRoot" || fds[1].Index != 0 {
		return errors.New("apap contract: BeginProbe fd table rejected")
	}
	for i, fd := range fds {
		if !validDigest(fd.IdentityDigest) {
			return errors.New("apap contract: fd identity rejected")
		}
		if i >= 2 && (fd.Role != "businessDenyRoot" || fd.Index != uint64(i-2)) {
			return errors.New("apap contract: business deny fd order rejected")
		}
	}
	return nil
}

func admitObject(raw []byte, fields []string) (map[string]json.RawMessage, error) {
	if len(raw) == 0 || len(raw) > MaxEnvelopeBytes {
		return nil, errors.New("apap contract: size rejected")
	}
	admitted, err := canonical.JSON(raw)
	if err != nil || !bytes.Equal(raw, admitted) {
		return nil, errors.New("apap contract: non-canonical JSON rejected")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(admitted, &object); err != nil || object == nil || len(object) != len(fields) {
		return nil, errors.New("apap contract: object shape rejected")
	}
	for _, field := range fields {
		if _, ok := object[field]; !ok {
			return nil, errors.New("apap contract: object shape rejected")
		}
	}
	return object, nil
}

func digestDetached(object map[string]json.RawMessage, field string) string {
	detached := make(map[string]json.RawMessage, len(object)-1)
	for name, value := range object {
		if name != field {
			detached[name] = value
		}
	}
	raw, err := json.Marshal(detached)
	if err != nil {
		return ""
	}
	digest, err := canonical.DigestJSON(raw)
	if err != nil {
		return ""
	}
	return digest
}

func canonicalTime(raw json.RawMessage) (time.Time, bool) {
	value, ok := decodeString(raw)
	if !ok || !strings.HasSuffix(value, "Z") {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || parsed.Format(time.RFC3339Nano) != value {
		return time.Time{}, false
	}
	return parsed, true
}

func rawString(raw json.RawMessage) string {
	value, _ := decodeString(raw)
	return value
}

func decodeString(raw json.RawMessage) (string, bool) {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	return value, true
}

func rawUint(raw json.RawMessage) (uint64, bool) {
	var value uint64
	if len(raw) == 0 || bytes.ContainsAny(raw, ".eE-+") || json.Unmarshal(raw, &value) != nil {
		return 0, false
	}
	return value, true
}

func equalString(object map[string]json.RawMessage, field, want string) bool {
	return rawString(object[field]) == want
}
func equalUint(object map[string]json.RawMessage, field string, want uint64) bool {
	value, ok := rawUint(object[field])
	return ok && value == want
}
func validDigest(value string) bool { return digestPattern.MatchString(value) }
func validID(value string) bool     { return idPattern.MatchString(value) }
func validProfile(value string) bool {
	return oneOf(value, "qoder-cli-adr0034-v1", "codex-cli-adr0037-v1")
}
func oneOf(value string, values ...string) bool { return slices.Contains(values, value) }

func validSafeCode(value string) bool {
	return oneOf(value, "ok", "platform-unsupported", "profile-unsupported", "principal-unauthorized", "identity-mismatch", "bundle-invalid", "bundle-rollback", "evidence-invalid", "evidence-revoked", "evidence-expired", "host-attestation-invalid", "isolation-unavailable", "launch-receipt-invalid", "secret-boundary-violation", "provider-busy", "anchor-temporarily-unavailable", "bundle-commit-ambiguous", "launch-outcome-ambiguous", "internal-fail-closed")
}

func validSignatureDomain(value string) bool {
	return oneOf(value,
		"marshal-credential-ingress-ticket-v1\x00", "marshal-credential-delivery-receipt-v1\x00", "marshal-credential-install-receipt-v1\x00", "marshal-apap-launch-binding-receipt-v1\x00", "marshal-agent-authority-bundle-v1\x00", "marshal-evidence-update-authorization-v1\x00", "marshal-rotation-authorization-v1\x00", "marshal-revocation-authorization-v1\x00", "marshal-bundle-prepared-receipt-v1\x00", "marshal-anchor-advance-receipt-v1\x00", "marshal-bundle-commit-receipt-v1\x00", "marshal-bundle-recovery-receipt-v1\x00")
}

// DetachedDigest returns sha256(JCS(object without field)). It is exported so
// producers can create request/response golden vectors without reimplementing
// the digest exclusion rule.
func DetachedDigest(raw []byte, field string) (string, error) {
	admitted, err := canonical.JSON(raw)
	if err != nil {
		return "", err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(admitted, &object); err != nil {
		return "", err
	}
	if _, ok := object[field]; !ok {
		return "", fmt.Errorf("apap contract: detached field absent")
	}
	return digestDetached(object, field), nil
}
