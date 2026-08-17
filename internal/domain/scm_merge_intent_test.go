package domain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// mergeIntentDigest returns a canonical sha256 digest with the given 64-char
// hex body, matching the frozen "sha256:<64 hex>" representation.
func mergeIntentDigest(body string) string { return "sha256:" + body }

// validMergeIntent returns a SCMMergeIntent whose every field satisfies the
// frozen field constraints except the detached intentDigest, which the
// caller seals with Digest before persisting or validating.
func validMergeIntent() SCMMergeIntent {
	return SCMMergeIntent{
		APIVersion:               APIVersionV1Alpha1,
		Kind:                     KindSCMMergeIntent,
		IntentID:                 "intent-01",
		AuthorityNamespaceID:     "authority:main",
		TaskID:                   "task-01",
		RunID:                    "run-01",
		PublicationRecordID:      mergeIntentDigest(strings.Repeat("b", 64)),
		PublicationDigest:        mergeIntentDigest(strings.Repeat("b", 64)),
		ReviewDecisionDigest:     mergeIntentDigest(strings.Repeat("d", 64)),
		VerificationDigest:       mergeIntentDigest(strings.Repeat("e", 64)),
		EvidenceDigest:           mergeIntentDigest(strings.Repeat("f", 64)),
		PolicyDigest:             mergeIntentDigest(strings.Repeat("1", 64)),
		PublishApprovalRecordID:  "approval-01",
		PublishApprovalDigest:    mergeIntentDigest(strings.Repeat("2", 64)),
		RemoteCheckRecordDigest:  mergeIntentDigest(strings.Repeat("3", 64)),
		RepositoryRef:            "example-org/example-repo",
		PRNumber:                 7,
		HeadOid:                  "0123456789abcdef0123456789abcdef01234567",
		BaseOid:                  "89abcdef0123456789abcdef0123456789abcdef",
		MergeMethod:              MergeMethodSquash,
		RequestedBy:              "maintainer",
		RequestedAt:              time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC),
		ExpectedMergedBy:         "github-login:maintainer",
		MergerSecurityDomainID:   "marshal:publication:scm-merger",
		MergerCredentialIdentity: mergeIntentDigest(strings.Repeat("4", 64)),
	}
}

// sealedMergeIntent returns a valid SCMMergeIntent whose intentDigest is the
// freshly recomputed detached digest, so Validate accepts it.
func sealedMergeIntent(t *testing.T) SCMMergeIntent {
	t.Helper()
	intent := validMergeIntent()
	digest, err := intent.Digest()
	if err != nil {
		t.Fatalf("compute detached intent digest: %v", err)
	}
	intent.IntentDigest = digest
	return intent
}

func TestSCMMergeIntentValidateHappyPath(t *testing.T) {
	if err := sealedMergeIntent(t).Validate(); err != nil {
		t.Fatalf("sealed SCMMergeIntent Validate() = %v, want nil", err)
	}
}

func TestSCMMergeIntentDetachedDigestMatchesSelfReport(t *testing.T) {
	intent := sealedMergeIntent(t)
	data, err := json.Marshal(intent)
	if err != nil {
		t.Fatalf("marshal intent: %v", err)
	}
	recomputed, err := DetachedIntentDigest(data)
	if err != nil {
		t.Fatalf("DetachedIntentDigest() = %v", err)
	}
	if recomputed != intent.IntentDigest {
		t.Fatalf("DetachedIntentDigest() = %q, want self-reported %q", recomputed, intent.IntentDigest)
	}
}

func TestSCMMergeIntentRejectsForgedDigest(t *testing.T) {
	intent := sealedMergeIntent(t)
	// Tamper with a bound field after sealing: the self-reported digest no
	// longer matches the authoritative detached recompute and must fail.
	intent.HeadOid = strings.Repeat("a", 40)
	if err := intent.Validate(); err == nil {
		t.Fatal("Validate() accepted an intent whose headOid was tampered after sealing")
	}
}

func TestSCMMergeIntentRejectsSelfReportedForgery(t *testing.T) {
	intent := sealedMergeIntent(t)
	// Keep every field intact but replace the digest with a well-formed
	// placeholder: the detached recompute must reject the self-reported value.
	intent.IntentDigest = mergeIntentDigest(strings.Repeat("9", 64))
	if err := intent.Validate(); err == nil {
		t.Fatal("Validate() accepted a forged self-reported intentDigest")
	}
}

func TestSCMMergeIntentRejectsPublicationRecordDigestMismatch(t *testing.T) {
	intent := sealedMergeIntent(t)
	intent.PublicationRecordID = mergeIntentDigest(strings.Repeat("5", 64))
	if err := intent.Validate(); err == nil {
		t.Fatal("Validate() accepted publicationRecordId != publicationDigest")
	}
}

func TestSCMMergeIntentRejectsEmptyIdentity(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*SCMMergeIntent)
	}{
		{"intentId", func(i *SCMMergeIntent) { i.IntentID = "" }},
		{"authorityNamespaceId", func(i *SCMMergeIntent) { i.AuthorityNamespaceID = "" }},
		{"taskId", func(i *SCMMergeIntent) { i.TaskID = "" }},
		{"runId", func(i *SCMMergeIntent) { i.RunID = "" }},
		{"expectedMergedBy", func(i *SCMMergeIntent) { i.ExpectedMergedBy = "" }},
		{"mergerSecurityDomainId", func(i *SCMMergeIntent) { i.MergerSecurityDomainID = "" }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			intent := sealedMergeIntent(t)
			test.mutate(&intent)
			if err := intent.Validate(); err == nil {
				t.Fatalf("Validate() accepted an intent with empty %s", test.name)
			}
		})
	}
}

func TestSCMMergeIntentRejectsMergeMethodOutsideEnumeration(t *testing.T) {
	intent := sealedMergeIntent(t)
	intent.MergeMethod = "octopus"
	if err := intent.Validate(); err == nil {
		t.Fatal("Validate() accepted mergeMethod outside the closed enumeration")
	}
}

func TestSCMMergeIntentRejectsMergedByEqualRequester(t *testing.T) {
	intent := sealedMergeIntent(t)
	intent.ExpectedMergedBy = "github-login:maintainer"
	intent.RequestedBy = "github-login:maintainer"
	if err := intent.Validate(); err == nil {
		t.Fatal("Validate() accepted expectedMergedBy equal to requestedBy")
	}
}
