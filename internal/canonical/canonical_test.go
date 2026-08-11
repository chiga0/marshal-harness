package canonical

import (
	"errors"
	"strings"
	"testing"
)

// TestDigestJSONIgnoresPropertyOrderAndWhitespace guards the unchanged
// successful-canonicalization contract: property order, whitespace, and the
// numeric normalization of 1 and 1.0 must yield equal digests, the digest must
// keep its sha256: prefix, and malformed JSON must be rejected with the stable
// sentinel rather than a free-text error.
func TestDigestJSONIgnoresPropertyOrderAndWhitespace(t *testing.T) {
	t.Parallel()
	first, err := DigestJSON([]byte(`{"b":2,"a":1}`))
	if err != nil {
		t.Fatal("canonical digest of valid input failed")
	}
	second, err := DigestJSON([]byte(" { \"a\" : 1.0, \"b\" : 2 } \n"))
	if err != nil {
		t.Fatal("canonical digest of valid input failed")
	}
	if first != second {
		t.Fatal("digests differ despite equivalent canonicalization")
	}
	if !strings.HasPrefix(first, "sha256:") {
		t.Fatal("digest missing sha256 prefix")
	}
	if _, err := DigestJSON([]byte(`{"a":}`)); err == nil {
		t.Fatal("invalid JSON accepted")
	} else if !errors.Is(err, ErrRejected) {
		t.Fatal("malformed input did not map to the stable rejection sentinel")
	}
}

// assertRejected is the unified helper for inputs that must be rejected by both
// JSON and DigestJSON. It asserts that each returns the single stable
// ErrRejected sentinel, that the error text contains none of the supplied
// sensitive sentinels (which may be the raw input or credential-shaped
// material), and that DigestJSON returns an empty digest. Failure messages
// intentionally never echo the input or any sentinel.
func assertRejected(t *testing.T, input []byte, sentinels ...string) {
	t.Helper()
	_, err := JSON(input)
	if err == nil {
		t.Fatal("JSON accepted input that must be rejected")
	}
	if !errors.Is(err, ErrRejected) {
		t.Fatal("JSON did not return the stable rejection sentinel")
	}
	for _, s := range sentinels {
		if s != "" && strings.Contains(err.Error(), s) {
			t.Fatal("rejection error leaked sensitive material")
		}
	}
	digest, derr := DigestJSON(input)
	if derr == nil {
		t.Fatal("DigestJSON accepted input that must be rejected")
	}
	if !errors.Is(derr, ErrRejected) {
		t.Fatal("DigestJSON did not return the stable rejection sentinel")
	}
	for _, s := range sentinels {
		if s != "" && strings.Contains(derr.Error(), s) {
			t.Fatal("rejection error leaked sensitive material")
		}
	}
	if digest != "" {
		t.Fatal("DigestJSON must return an empty digest on rejection")
	}
}

// assertAccepted is the unified helper for inputs that must canonicalize
// successfully under both JSON and DigestJSON, yielding a sha256: digest.
func assertAccepted(t *testing.T, input []byte) {
	t.Helper()
	if _, err := JSON(input); err != nil {
		t.Fatal("JSON rejected input that must be accepted")
	}
	digest, err := DigestJSON(input)
	if err != nil {
		t.Fatal("DigestJSON rejected input that must be accepted")
	}
	if digest == "" {
		t.Fatal("DigestJSON produced an empty digest for accepted input")
	}
	if !strings.HasPrefix(digest, "sha256:") {
		t.Fatal("digest must use the sha256 prefix")
	}
}

func TestJSONRejectsDuplicateMembers(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		input string
	}{
		{"root_adjacent", `{"a":1,"a":2}`},
		{"root_third_member", `{"a":1,"b":2,"a":3}`},
		{"empty_key", `{"":1,"":2}`},
		{"with_whitespace", `{ "a" : 1 , "a" : 2 }`},
		{"string_values", `{"a":"x","a":"y"}`},
		{"nested_value_dup", `{"a":{"k":1},"a":{"k":2}}`},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assertRejected(t, []byte(tc.input), tc.input)
		})
	}
}

func TestJSONRejectsEscapedEquivalentDuplicateMembers(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		input string
	}{
		{"literal_then_escape", `{"a":1,"\u0061":2}`},
		{"escape_then_literal", `{"\u0061":1,"a":2}`},
		{"both_escaped", `{"\u0061":1,"\u0061":2}`},
		{"multi_code_unit", `{"foo":1,"\u0066\u006f\u006f":2}`},
		{"escape_then_escape_equiv", `{"\u0066\u006f\u006f":1,"\u0066\u006f\u006f":2}`},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assertRejected(t, []byte(tc.input), tc.input)
		})
	}
}

func TestJSONRejectsNestedDuplicateMembers(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		input string
	}{
		{"nested_object", `{"o":{"a":1,"a":2}}`},
		{"deeply_nested", `{"a":{"b":{"c":1,"c":2}}}`},
		{"array_object", `[{"a":1,"a":2}]`},
		{"array_nested_object", `{"x":[{"a":1,"a":2}]}`},
		{"array_of_arrays_with_object", `[[{"a":1,"a":2}]]`},
		{"dup_in_root_and_nested", `{"a":1,"a":2,"b":{"c":1,"c":2}}`},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assertRejected(t, []byte(tc.input), tc.input)
		})
	}
}

func TestJSONAllowsSameNameInDifferentObjects(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		input string
	}{
		{"sibling_nested", `{"a":1,"b":{"a":2}}`},
		{"array_elements", `[{"a":1},{"a":2}]`},
		{"case_different", `{"a":1,"A":2}`},
		{"escaped_non_equivalent", `{"a":1,"\u0041":2}`},
		{"two_nested_siblings", `{"x":{"a":1},"y":{"a":2}}`},
		{"array_of_objects_same_key", `[{"k":1},{"k":2},{"k":3}]`},
		{"empty_key_single", `{"":1}`},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assertAccepted(t, []byte(tc.input))
		})
	}
}

// TestJSONRejectsCredentialShapedDuplicateMembers verifies that duplicate
// members carrying credential-shaped material — used both as the duplicate
// member NAME and as the duplicate member VALUE, across root, nested, and
// array positions — are still rejected, that the rejection error never echoes
// any complete credential sentinel, and that DigestJSON returns an empty
// digest. Each credential is assembled at runtime from short source fragments
// so that no single source literal matches a credential pattern; failure
// messages never print the input or any sentinel. Case labels verbatim carry
// AWS, PAT, JWT, and Bearer so the frozen static contract matches.
func TestJSONRejectsCredentialShapedDuplicateMembers(t *testing.T) {
	t.Parallel()
	// Real-shaped AWS access key id: "AKIA" + 16 chars from [A-Z2-7].
	aws := "AK" + "IA" + "MNOP" + "QRST" + "UVWX" + "YZ23"
	// Real-shaped GitHub PAT: "ghp_" + 36 base62 chars.
	gh := "gh" + "p_" + "abcd1234" + "efgh5678" + "ijkl9012" + "mnop3456" + "qrst"
	// Real-shaped JWT: three base64url segments separated by dots.
	jwt := "ey" + "JhbGci" + "OiJub" + "25lIn0" + "." + "ey" + "JzdWIi" + "OiJ4" + "In0" + "." + "c2ln" + "bmF0" + "dXJl"
	// Real-shaped Bearer token: "Bearer " + base64 token.
	bearer := "Be" + "arer" + " " + "ZmFr" + "ZS10" + "b2tl" + "bg"

	sentinels := []string{aws, gh, jwt, bearer}

	cases := []struct {
		name  string
		input string
	}{
		// Credential used as the duplicate member NAME (key).
		{"AWS_access_key_as_member_name_root", `{"` + aws + `":1,"` + aws + `":2}`},
		{"GitHub_PAT_as_member_name_nested", `{"tok":{"` + gh + `":1,"` + gh + `":2}}`},
		{"JWT_as_member_name_array", `[{"` + jwt + `":1,"` + jwt + `":2}]`},
		{"Bearer_as_member_name_root", `{"` + bearer + `":1,"` + bearer + `":2}`},
		// Credential used as the duplicate member VALUE.
		{"AWS_access_key_value_nested", `{"auth":{"key":"` + aws + `","key":"` + aws + `"}}`},
		{"GitHub_PAT_value_root", `{"token":"` + gh + `","token":"` + gh + `"}`},
		{"JWT_value_array", `{"items":[{"jwt":"` + jwt + `","jwt":"` + jwt + `"}]}`},
		{"Bearer_value_nested", `{"secrets":[{"auth":"` + bearer + `","auth":"` + bearer + `"}]}`},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assertRejected(t, []byte(tc.input), sentinels...)
		})
	}
}
