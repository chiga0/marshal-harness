package contract

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/domain"
)

// contractValidMergeIntent returns a SCMMergeIntent whose every field
// satisfies the frozen constraints except the detached intentDigest.
func contractValidMergeIntent() domain.SCMMergeIntent {
	return domain.SCMMergeIntent{
		APIVersion:               domain.APIVersionV1Alpha1,
		Kind:                     domain.KindSCMMergeIntent,
		IntentID:                 "intent-01",
		AuthorityNamespaceID:     "authority:main",
		TaskID:                   "task-01",
		RunID:                    "run-01",
		PublicationRecordID:      "sha256:" + strings.Repeat("b", 64),
		PublicationDigest:        "sha256:" + strings.Repeat("b", 64),
		ReviewDecisionDigest:     "sha256:" + strings.Repeat("d", 64),
		VerificationDigest:       "sha256:" + strings.Repeat("e", 64),
		EvidenceDigest:           "sha256:" + strings.Repeat("f", 64),
		PolicyDigest:             "sha256:" + strings.Repeat("1", 64),
		PublishApprovalRecordID:  "approval-01",
		PublishApprovalDigest:    "sha256:" + strings.Repeat("2", 64),
		RemoteCheckRecordDigest:  "sha256:" + strings.Repeat("3", 64),
		RepositoryRef:            "example-org/example-repo",
		PRNumber:                 7,
		HeadOid:                  "0123456789abcdef0123456789abcdef01234567",
		BaseOid:                  "89abcdef0123456789abcdef0123456789abcdef",
		MergeMethod:              domain.MergeMethodSquash,
		RequestedBy:              "maintainer",
		RequestedAt:              time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC),
		ExpectedMergedBy:         "github-login:maintainer",
		MergerSecurityDomainID:   "marshal:publication:scm-merger",
		MergerCredentialIdentity: "sha256:" + strings.Repeat("4", 64),
	}
}

// sealedIntentDocument marshals a valid SCMMergeIntent with its freshly
// recomputed detached intentDigest, mirroring exactly how the production
// verifier re-derives the digest instead of trusting a self-reported value.
func sealedIntentDocument(t *testing.T) []byte {
	t.Helper()
	intent := contractValidMergeIntent()
	digest, err := intent.Digest()
	if err != nil {
		t.Fatalf("compute detached intent digest: %v", err)
	}
	intent.IntentDigest = digest
	data, err := json.Marshal(intent)
	if err != nil {
		t.Fatalf("marshal intent: %v", err)
	}
	return data
}

func mutateIntentDocument(t *testing.T, base []byte, mutate func(map[string]any)) []byte {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(base, &document); err != nil {
		t.Fatalf("decode intent: %v", err)
	}
	mutate(document)
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("marshal mutated intent: %v", err)
	}
	return data
}

// TestValidateSCMMergeIntentAuthoritativeDigest pins the ADR 0032 detached
// digest contract: the validator accepts a sealed intent and fails closed on
// a self-reported digest that does not equal the authoritative recompute.
func TestValidateSCMMergeIntentAuthoritativeDigest(t *testing.T) {
	validator := mustValidator(t)
	if err := validator.Validate(domain.KindSCMMergeIntent, sealedIntentDocument(t)); err != nil {
		t.Fatalf("sealed SCMMergeIntent failed validation: %v", err)
	}

	forged := mutateIntentDocument(t, sealedIntentDocument(t), func(document map[string]any) {
		document["intentDigest"] = "sha256:" + strings.Repeat("9", 64)
	})
	if err := validator.Validate(domain.KindSCMMergeIntent, forged); err == nil {
		t.Fatal("Validate() accepted a forged self-reported intentDigest")
	}
}

// TestValidateSCMMergeIntentPublicationIdentity pins the frozen dual-identity
// invariant (identity = digest): publicationRecordId must equal
// publicationDigest, and any divergence fails closed.
func TestValidateSCMMergeIntentPublicationIdentity(t *testing.T) {
	validator := mustValidator(t)
	mismatched := mutateIntentDocument(t, sealedIntentDocument(t), func(document map[string]any) {
		document["publicationRecordId"] = "sha256:" + strings.Repeat("5", 64)
	})
	if err := validator.Validate(domain.KindSCMMergeIntent, mismatched); err == nil {
		t.Fatal("Validate() accepted publicationRecordId != publicationDigest")
	}
}

// TestValidateSCMMergeIntentIdentityAndMethod pins the remaining fail-closed
// constraints: empty identity fields and mergeMethod outside the closed
// enumeration are all rejected.
func TestValidateSCMMergeIntentIdentityAndMethod(t *testing.T) {
	validator := mustValidator(t)
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"empty taskId", func(document map[string]any) { document["taskId"] = "" }},
		{"empty runId", func(document map[string]any) { document["runId"] = "" }},
		{"empty intentId", func(document map[string]any) { document["intentId"] = "" }},
		{"empty authorityNamespaceId", func(document map[string]any) { document["authorityNamespaceId"] = "" }},
		{"mergeMethod outside enumeration", func(document map[string]any) { document["mergeMethod"] = "octopus" }},
		{"expectedMergedBy missing principal", func(document map[string]any) { document["expectedMergedBy"] = "" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data := mutateIntentDocument(t, sealedIntentDocument(t), test.mutate)
			if err := validator.Validate(domain.KindSCMMergeIntent, data); err == nil {
				t.Fatal("Validate() unexpectedly accepted an invalid SCMMergeIntent")
			}
		})
	}
}

// TestValidateTaskPolicyMergeSemantics pins the TaskSpec policy-merge
// semantic layer: when mergePolicy is "policy", a closed mergeMethod and a
// non-empty de-duplicated requiredChecks set must both be present.
func TestValidateTaskPolicyMergeSemantics(t *testing.T) {
	applyPolicyMerge := func(document map[string]any) {
		publication := document["publication"].(map[string]any)
		publication["mergePolicy"] = "policy"
		publication["provider"] = "github"
		publication["mode"] = "draft"
		publication["mergeMethod"] = "squash"
		publication["requiredChecks"] = []any{"ci/test"}
	}

	// The fully declared policy-merge shape is semantically clean and passes
	// the full validator stack.
	data := mutateFixture(t, "examples/happy-path/task-spec.json", applyPolicyMerge)
	violations, err := validateTask(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, violation := range violations {
		t.Fatalf("valid policy-merge declaration produced a semantic violation: %+v", violation)
	}
	if err := mustValidator(t).Validate(domain.KindTask, data); err != nil {
		t.Fatalf("full validation rejected a valid policy-merge declaration: %v", err)
	}

	assertTaskViolation := func(t *testing.T, mutate func(map[string]any), path, code string) {
		t.Helper()
		data := mutateFixture(t, "examples/happy-path/task-spec.json", mutate)
		violations, err := validateTask(data)
		if err != nil {
			t.Fatal(err)
		}
		for _, violation := range violations {
			if violation.Path == path && violation.Code == code {
				return
			}
		}
		t.Fatalf("violations = %+v, want %s at %s", violations, code, path)
	}

	t.Run("missing mergeMethod", func(t *testing.T) {
		assertTaskViolation(t, func(document map[string]any) {
			applyPolicyMerge(document)
			delete(document["publication"].(map[string]any), "mergeMethod")
		}, "/publication/mergeMethod", "invalid-merge-method")
	})

	t.Run("mergeMethod outside enumeration", func(t *testing.T) {
		assertTaskViolation(t, func(document map[string]any) {
			applyPolicyMerge(document)
			document["publication"].(map[string]any)["mergeMethod"] = "fast-forward"
		}, "/publication/mergeMethod", "invalid-merge-method")
	})

	t.Run("empty requiredChecks", func(t *testing.T) {
		assertTaskViolation(t, func(document map[string]any) {
			applyPolicyMerge(document)
			document["publication"].(map[string]any)["requiredChecks"] = []any{}
		}, "/publication/requiredChecks", "required-checks-empty")
	})

	t.Run("duplicate requiredChecks", func(t *testing.T) {
		assertTaskViolation(t, func(document map[string]any) {
			applyPolicyMerge(document)
			document["publication"].(map[string]any)["requiredChecks"] = []any{"ci/test", "ci/test"}
		}, "/publication/requiredChecks/1", "duplicate-required-check")
	})
}

// TestSCMMergeIntentHappyFixtureDigestPinned proves the embedded happy fixture
// carries a real detached intentDigest. The value must equal the authoritative
// recompute over the raw document (blanked intentDigest → RFC 8785 JCS →
// sha256) and the struct-based recompute used by domain.SCMMergeIntent.Validate.
// It fails closed on an empty or self-inconsistent digest, and its failure
// message exposes the recomputed value so a stale fixture can be re-sealed
// mechanically instead of by trusting a self-reported digest.
func TestSCMMergeIntentHappyFixtureDigestPinned(t *testing.T) {
	data := readFixture(t, "examples/happy-path/scm-merge-intent.json")
	var intent domain.SCMMergeIntent
	if err := json.Unmarshal(data, &intent); err != nil {
		t.Fatalf("decode happy fixture: %v", err)
	}
	detached, err := domain.DetachedIntentDigest(data)
	if err != nil {
		t.Fatalf("DetachedIntentDigest: %v", err)
	}
	structDigest, err := intent.Digest()
	if err != nil {
		t.Fatalf("intent.Digest: %v", err)
	}
	if intent.IntentDigest == "" {
		t.Fatalf("happy fixture intentDigest is empty; recomputed detached digest is %q", detached)
	}
	if intent.IntentDigest != detached {
		t.Fatalf("happy fixture intentDigest = %q, want detached recompute %q", intent.IntentDigest, detached)
	}
	if intent.IntentDigest != structDigest {
		t.Fatalf("happy fixture intentDigest = %q, want struct recompute %q", intent.IntentDigest, structDigest)
	}
}
