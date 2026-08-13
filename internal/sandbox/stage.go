package sandbox

import (
	"errors"
	"fmt"
	"strings"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

// DigestPrefix prefixes every content digest used by the sandbox SPI.
const DigestPrefix = "sha256:"

const (
	// MaxInlineObjectBytes caps one inline stage object at 1 MiB; larger
	// inputs must be referenced through a locator.
	MaxInlineObjectBytes int64 = 1 << 20
	// MaxStageRequestBytes caps the total size of one stage request at
	// 16 MiB; larger requests must move inputs behind locators.
	MaxStageRequestBytes int64 = 16 << 20
)

var (
	// ErrStageInputMismatch is the fixed sentinel returned when the sha256
	// digest recomputed before consumption does not match the declared
	// digest. The attempt fails closed; no receipt is produced.
	ErrStageInputMismatch = errors.New("sandbox: stage input digest mismatch detected before consumption")
	// ErrInlineTooLarge rejects one inline object above MaxInlineObjectBytes
	// and demands a locator instead.
	ErrInlineTooLarge = errors.New("sandbox: inline stage object exceeds the 1 MiB limit, reference it through a locator instead")
	// ErrStageRequestTooLarge rejects one stage request whose total size
	// exceeds MaxStageRequestBytes and demands locators instead.
	ErrStageRequestTooLarge = errors.New("sandbox: stage request exceeds the 16 MiB total limit, reference large inputs through locators instead")
	// ErrDuplicateStageInputId rejects duplicate input ids inside one stage
	// request.
	ErrDuplicateStageInputId = errors.New("sandbox: stage input ids must be unique within one request")
	// ErrInvalidStageInput rejects a malformed stage input or request.
	ErrInvalidStageInput = errors.New("sandbox: invalid stage input")
	// ErrInvalidLocator rejects a locator whose store alias falls outside
	// the closed bound set, is URL-shaped or credential-shaped, or whose
	// digest or size is malformed.
	ErrInvalidLocator = errors.New("sandbox: invalid locator")
	// ErrLocatorUnresolved is returned when a provider cannot resolve the
	// content behind a well-formed locator.
	ErrLocatorUnresolved = errors.New("sandbox: locator content is not resolvable by the provider")
)

// Locator references one artifact inside an artifact store alias that was
// bound to the allocation at provision time. A locator is never an external
// URL and never carries credentials: the struct exposes exactly three
// fields, and Validate rejects every store alias outside the closed bound
// set as well as every URL-shaped, path-shaped or credential-shaped alias.
type Locator struct {
	StoreId   string `json:"storeId"`
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"sizeBytes"`
}

// Validate fails closed unless the store alias is a member of the closed set
// bound to the allocation, the digest is a well-formed sha256 digest and the
// size is positive.
func (locator Locator) Validate(allowedStoreIds []string) error {
	if err := validateStoreAlias(locator.StoreId, allowedStoreIds); err != nil {
		return err
	}
	if err := requireSHA256("locator.sha256", locator.SHA256); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidLocator, err)
	}
	if locator.SizeBytes < 1 {
		return fmt.Errorf("%w: locator.sizeBytes must be positive", ErrInvalidLocator)
	}
	return nil
}

// validateStoreAliasShape rejects empty, blank and URL-shaped, path-shaped
// or credential-shaped aliases.
func validateStoreAliasShape(storeId string) error {
	if strings.TrimSpace(storeId) == "" {
		return fmt.Errorf("%w: storeId must be a non-empty artifact store alias", ErrInvalidLocator)
	}
	if storeId != strings.TrimSpace(storeId) {
		return fmt.Errorf("%w: storeId must not carry surrounding whitespace", ErrInvalidLocator)
	}
	if strings.ContainsAny(storeId, ":/?#\\@") {
		return fmt.Errorf("%w: storeId must be a bound alias, never an external URL, path or credential carrier", ErrInvalidLocator)
	}
	return nil
}

// validateStoreAlias additionally enforces membership in the closed set of
// aliases bound to the allocation.
func validateStoreAlias(storeId string, allowedStoreIds []string) error {
	if err := validateStoreAliasShape(storeId); err != nil {
		return err
	}
	for _, allowed := range allowedStoreIds {
		if storeId == allowed {
			return nil
		}
	}
	return fmt.Errorf("%w: storeId %q is not bound to the allocation", ErrInvalidLocator, storeId)
}

// StageInput carries one content-addressed input of a stage request: either
// inline bytes (one object, at most MaxInlineObjectBytes) or a locator bound
// to an artifact store alias. Exactly one source must be present.
type StageInput struct {
	InputId        string   `json:"inputId"`
	DeclaredSHA256 string   `json:"declaredSha256"`
	Inline         []byte   `json:"inline,omitempty"`
	Locator        *Locator `json:"locator,omitempty"`
}

// Validate fails closed on a missing input id, a malformed declared digest,
// an ambiguous or missing source, an oversized inline object and a locator
// outside the bound alias set.
func (in StageInput) Validate(allowedStoreIds []string) error {
	if strings.TrimSpace(in.InputId) == "" {
		return fmt.Errorf("%w: inputId must be a non-empty string", ErrInvalidStageInput)
	}
	if err := requireSHA256("declaredSha256", in.DeclaredSHA256); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidStageInput, err)
	}
	hasInline := len(in.Inline) > 0
	hasLocator := in.Locator != nil
	if hasInline == hasLocator {
		return fmt.Errorf("%w: exactly one of inline or locator must be present", ErrInvalidStageInput)
	}
	if hasInline && int64(len(in.Inline)) > MaxInlineObjectBytes {
		return ErrInlineTooLarge
	}
	if hasLocator {
		if err := in.Locator.Validate(allowedStoreIds); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidStageInput, err)
		}
	}
	return nil
}

func (in StageInput) sizeBytes() int64 {
	if in.Locator != nil {
		return in.Locator.SizeBytes
	}
	return int64(len(in.Inline))
}

// ValidateStageRequest validates one whole stage request: input ids must be
// unique inside the request, every input must validate against the store
// aliases bound to the allocation, and the total size must stay within
// MaxStageRequestBytes.
func ValidateStageRequest(inputs []StageInput, allowedStoreIds []string) error {
	if len(inputs) == 0 {
		return fmt.Errorf("%w: a stage request requires at least one input", ErrInvalidStageInput)
	}
	seen := make(map[string]struct{}, len(inputs))
	var total int64
	for _, in := range inputs {
		if err := in.Validate(allowedStoreIds); err != nil {
			return err
		}
		if _, duplicate := seen[in.InputId]; duplicate {
			return fmt.Errorf("%w: %q", ErrDuplicateStageInputId, in.InputId)
		}
		seen[in.InputId] = struct{}{}
		total += in.sizeBytes()
		if total > MaxStageRequestBytes {
			return ErrStageRequestTooLarge
		}
	}
	return nil
}

// StageReceipt is the provider's record of one staged input. The provider
// must recompute the sha256 digest of the content once before consumption
// and once after consumption, and the receipt must carry those recomputed
// digests: echoing the declared digest without recomputation is a
// conformance failure. A pre-consumption mismatch fails the attempt closed
// with ErrStageInputMismatch instead of producing any receipt.
type StageReceipt struct {
	InputId               string `json:"inputId"`
	RecomputedSHA256      string `json:"recomputedSha256"`
	PostConsumptionSHA256 string `json:"postConsumptionSha256"`
	SizeBytes             int64  `json:"sizeBytes"`
}

// RecomputeSHA256 returns the canonical sha256 digest of data. It is the
// only content-addressing derivation the sandbox package uses, so provider
// recomputations and out-of-band verifier recomputations always agree.
func RecomputeSHA256(data []byte) string {
	return canonical.DigestBytes(data)
}

// requireSHA256 fails closed unless value is a DigestPrefix-prefixed 64
// character lowercase hex digest.
func requireSHA256(field, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s must be a non-empty digest", field)
	}
	if !strings.HasPrefix(value, DigestPrefix) {
		return fmt.Errorf("%s must carry the %s digest prefix", field, DigestPrefix)
	}
	hexPart := strings.TrimPrefix(value, DigestPrefix)
	if len(hexPart) != 64 {
		return fmt.Errorf("%s must be a 64 character sha256 hex digest", field)
	}
	for _, r := range hexPart {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return fmt.Errorf("%s must be lowercase hex", field)
		}
	}
	return nil
}
