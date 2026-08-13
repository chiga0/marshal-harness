package sandbox

import (
	"context"
	"errors"
	"testing"
)

func validInlineInput(inputId string, content []byte) StageInput {
	return StageInput{
		InputId:        inputId,
		DeclaredSHA256: RecomputeSHA256(content),
		Inline:         append([]byte(nil), content...),
	}
}

// TestStageInputValidate freezes the per-input fail-closed validation.
func TestStageInputValidate(t *testing.T) {
	allowed := []string{"store-" + "a"}
	if err := validInlineInput("input-"+"1", []byte("payload")).Validate(allowed); err != nil {
		t.Fatalf("Validate rejected a valid inline input: %v", err)
	}
	cases := []struct {
		name   string
		mutate func(*StageInput)
	}{
		{"empty inputId", func(in *StageInput) { in.InputId = "" }},
		{"empty declared digest", func(in *StageInput) { in.DeclaredSHA256 = "" }},
		{"digest without prefix", func(in *StageInput) { in.DeclaredSHA256 = "md5:" + "00" }},
		{"malformed declared digest", func(in *StageInput) { in.DeclaredSHA256 = "sha256:" + "zz" }},
		{"both sources present", func(in *StageInput) {
			in.Locator = &Locator{StoreId: "store-" + "a", SHA256: fixtureDigest("object" + "-1"), SizeBytes: 7}
		}},
		{"no source present", func(in *StageInput) { in.Inline = nil }},
	}
	for _, tc := range cases {
		input := validInlineInput("input-"+"1", []byte("payload"))
		tc.mutate(&input)
		if err := input.Validate(allowed); err == nil {
			t.Fatalf("Validate accepted the stage input with %s", tc.name)
		}
	}
}

// TestInlineObjectLimit freezes the 1 MiB single-object limit: at the limit
// the input passes, above it the fixed error demands a locator.
func TestInlineObjectLimit(t *testing.T) {
	allowed := []string{"store-" + "a"}
	atLimit := make([]byte, MaxInlineObjectBytes)
	if err := validInlineInput("input-limit", atLimit).Validate(allowed); err != nil {
		t.Fatalf("Validate rejected an inline object exactly at the 1 MiB limit: %v", err)
	}
	overLimit := make([]byte, MaxInlineObjectBytes+1)
	err := validInlineInput("input-over", overLimit).Validate(allowed)
	if !errors.Is(err, ErrInlineTooLarge) {
		t.Fatalf("an oversized inline object must fail with ErrInlineTooLarge, got %v", err)
	}
}

// TestStageRequestTotalLimit freezes the 16 MiB per-request total limit.
func TestStageRequestTotalLimit(t *testing.T) {
	allowed := []string{"store-" + "a"}
	inline := validInlineInput("input-inline", []byte("payload"))
	locatorAtLimit := StageInput{
		InputId:        "input-locator",
		DeclaredSHA256: fixtureDigest("declared" + "-1"),
		Locator: &Locator{
			StoreId:   "store-" + "a",
			SHA256:    fixtureDigest("object" + "-1"),
			SizeBytes: MaxStageRequestBytes - int64(len(inline.Inline)),
		},
	}
	if err := ValidateStageRequest([]StageInput{inline, locatorAtLimit}, allowed); err != nil {
		t.Fatalf("ValidateStageRequest rejected a request exactly at the 16 MiB limit: %v", err)
	}
	locatorOver := locatorAtLimit
	locatorOver.Locator = &Locator{
		StoreId:   "store-" + "a",
		SHA256:    fixtureDigest("object" + "-1"),
		SizeBytes: MaxStageRequestBytes - int64(len(inline.Inline)) + 1,
	}
	err := ValidateStageRequest([]StageInput{inline, locatorOver}, allowed)
	if !errors.Is(err, ErrStageRequestTooLarge) {
		t.Fatalf("a request above the 16 MiB limit must fail with ErrStageRequestTooLarge, got %v", err)
	}
}

// TestStageInputIdUniqueness freezes input-id uniqueness within one request
// and rejects empty requests.
func TestStageInputIdUniqueness(t *testing.T) {
	allowed := []string{"store-" + "a"}
	inputs := []StageInput{
		validInlineInput("input-"+"1", []byte("one")),
		validInlineInput("input-"+"1", []byte("two")),
	}
	err := ValidateStageRequest(inputs, allowed)
	if !errors.Is(err, ErrDuplicateStageInputId) {
		t.Fatalf("duplicate input ids must fail with ErrDuplicateStageInputId, got %v", err)
	}
	if err := ValidateStageRequest(nil, allowed); err == nil {
		t.Fatal("ValidateStageRequest accepted an empty request")
	}
}

// TestLocatorValidate freezes that a locator is never an external URL and
// never carries credentials: URL-shaped, path-shaped and credential-shaped
// aliases are rejected, as are unbound aliases and malformed digests.
func TestLocatorValidate(t *testing.T) {
	allowed := []string{"store-" + "a", "store-" + "b"}
	objectDigest := fixtureDigest("object" + "-1")
	valid := Locator{StoreId: "store-" + "a", SHA256: objectDigest, SizeBytes: 4}
	if err := valid.Validate(allowed); err != nil {
		t.Fatalf("Validate rejected a valid bound locator: %v", err)
	}
	rejected := []Locator{
		{StoreId: "https://example.com/store", SHA256: objectDigest, SizeBytes: 4},
		{StoreId: "s3://user:" + "pass@bucket", SHA256: objectDigest, SizeBytes: 4},
		{StoreId: "store/path", SHA256: objectDigest, SizeBytes: 4},
		{StoreId: "store?" + "token=abcd", SHA256: objectDigest, SizeBytes: 4},
		{StoreId: "store#fragment", SHA256: objectDigest, SizeBytes: 4},
		{StoreId: " store-a", SHA256: objectDigest, SizeBytes: 4},
		{StoreId: "store-" + "unbound", SHA256: objectDigest, SizeBytes: 4},
		{StoreId: "store-" + "a", SHA256: "sha256:" + "00", SizeBytes: 4},
		{StoreId: "store-" + "a", SHA256: objectDigest, SizeBytes: 0},
	}
	for index, locator := range rejected {
		err := locator.Validate(allowed)
		if err == nil {
			t.Fatalf("locator case %d must be rejected", index)
		}
		if !errors.Is(err, ErrInvalidLocator) {
			t.Fatalf("locator case %d must surface ErrInvalidLocator, got %v", index, err)
		}
	}
}

// TestStageMismatchFailsClosed freezes the tampered-bytes behavior: a
// pre-consumption digest mismatch fails the attempt closed with the fixed
// sentinel, and the failed allocation rejects subsequent operations.
func TestStageMismatchFailsClosed(t *testing.T) {
	ctx := context.Background()
	fake := NewFakeProvider(FakeConfig{})
	allocationId := "allocation-" + "mismatch"
	provisioned, err := fake.Provision(ctx, ProvisionRequest{
		Identity:        testIdentity(allocationId, "command-provision"),
		Requirements:    workspaceWriteRequirements(),
		AllowedStoreIds: []string{"store-" + "a"},
	})
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	tampered := StageInput{
		InputId:        "input-" + "1",
		DeclaredSHA256: RecomputeSHA256([]byte("original-" + "content")),
		Inline:         []byte("tampered-" + "content"),
	}
	_, err = fake.Stage(ctx, StageRequest{
		Identity:     testIdentity(allocationId, "command-stage"),
		AllocationId: provisioned.Allocation.AllocationId,
		Inputs:       []StageInput{tampered},
	})
	if !errors.Is(err, ErrStageInputMismatch) {
		t.Fatalf("a tampered stage input must fail with ErrStageInputMismatch, got %v", err)
	}
	_, err = fake.Exec(ctx, ExecRequest{
		Identity:     testIdentity(allocationId, "command-exec"),
		AllocationId: provisioned.Allocation.AllocationId,
		Command:      []string{"echo-" + "1"},
	})
	if err == nil {
		t.Fatal("the attempt must fail closed after a stage mismatch")
	}
}
