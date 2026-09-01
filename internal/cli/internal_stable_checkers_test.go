package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/selfidentity"
)

func TestStablePlanPremortemCommandRejectsArguments(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if exit := Run([]string{"internal", "plan-premortem-check", "extra"}, strings.NewReader(""), &stdout, &stderr); exit != ExitUsage {
		t.Fatalf("exit=%d want=%d stderr=%q", exit, ExitUsage, stderr.String())
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "core-probe-usage-invalid") {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestStableReviewFreshnessCommandRejectsArguments(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if exit := Run([]string{"internal", "review-freshness-check", "extra"}, strings.NewReader(""), &stdout, &stderr); exit != ExitUsage {
		t.Fatalf("exit=%d want=%d stderr=%q", exit, ExitUsage, stderr.String())
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "checker-arguments-invalid") {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestReviewFreshnessLocalReviewChainUsesCoreProjection(t *testing.T) {
	observation := phaseTestObservationForStableChecker(t)
	applicability, err := selfidentity.ApplicabilityForObservation(observation)
	if err != nil {
		t.Fatal(err)
	}
	verification := selfidentity.LocalVerificationBindingV2{
		SchemaVersion:                 selfidentity.VerificationBindingSchema,
		SelfProfile:                   selfidentity.LocalProfile,
		ActivationDigest:              observation.ActivationDigest,
		IdentitySubjectDigest:         observation.IdentitySubjectDigest,
		AttemptID:                     "attempt-1",
		DispatchObservationDigest:     observation.ObservationDigest,
		IngressObservationDigest:      observation.ObservationDigest,
		VerificationObservationDigest: observation.ObservationDigest,
		Applicability:                 applicability,
	}
	review, err := selfidentity.BuildReviewBinding("attempt-1", 2, verification, observation)
	if err != nil {
		t.Fatal(err)
	}
	request := localReviewIdentityChain{
		CurrentAttemptID: "attempt-1", ReviewRound: 2,
		ReviewBinding: review, VerificationBinding: verification,
		ReviewObservation: observation,
	}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	path := t.TempDir() + "/chain.json"
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	validator, err := contract.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := processReviewFreshness(reviewFreshnessRequest{Mode: "local-review-chain", Path: path}, validator, nil); err != nil {
		t.Fatalf("valid chain rejected: %v", err)
	}
	request.ReviewBinding.ReviewRound++
	raw, _ = json.Marshal(request)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := processReviewFreshness(reviewFreshnessRequest{Mode: "local-review-chain", Path: path}, validator, nil); err == nil {
		t.Fatal("forged review round accepted")
	}
}

func phaseTestObservationForStableChecker(t *testing.T) selfidentity.LocalSelfIdentityObservationV2 {
	t.Helper()
	observation := selfidentity.LocalSelfIdentityObservationV2{
		SchemaVersion:           selfidentity.ObservationSchema,
		ActivationDigest:        "sha256:" + strings.Repeat("1", 64),
		ProcessID:               42,
		ProcessExecutablePath:   "/fixed/bin/marshal",
		RepositoryIdentity:      canonical.DigestBytes([]byte("fixture-repository")),
		CanonicalRepositoryRoot: "/fixed/repository",
		CurrentPathObject: selfidentity.CurrentPathObjectV2{
			CanonicalPath: "/fixed/bin/marshal", Device: "1", Inode: "2", Size: 3,
			RawSHA256: "sha256:" + strings.Repeat("2", 64), PathRechecked: true,
			ObservationKind: "darwin-current-path-fd-object",
		},
		SourceHead: strings.Repeat("3", 40), SelfProfile: selfidentity.LocalProfile,
		ObservedAt: time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
		Status:     "pass", ReasonCode: selfidentity.ReasonObserved,
	}
	subjectRaw, _ := json.Marshal(map[string]any{
		"activationDigest":        observation.ActivationDigest,
		"repositoryIdentity":      observation.RepositoryIdentity,
		"canonicalRepositoryRoot": observation.CanonicalRepositoryRoot,
		"canonicalExecutablePath": observation.CurrentPathObject.CanonicalPath,
		"size":                    observation.CurrentPathObject.Size,
		"rawSHA256":               observation.CurrentPathObject.RawSHA256,
		"sourceHead":              observation.SourceHead,
		"selfProfile":             observation.SelfProfile,
	})
	observation.IdentitySubjectDigest, _ = canonical.DigestJSON(subjectRaw)
	observationRaw, _ := json.Marshal(map[string]any{
		"schemaVersion":           observation.SchemaVersion,
		"activationDigest":        observation.ActivationDigest,
		"processId":               observation.ProcessID,
		"processExecutablePath":   observation.ProcessExecutablePath,
		"repositoryIdentity":      observation.RepositoryIdentity,
		"canonicalRepositoryRoot": observation.CanonicalRepositoryRoot,
		"currentPathObject":       observation.CurrentPathObject,
		"sourceHead":              observation.SourceHead,
		"selfProfile":             observation.SelfProfile,
		"observedAt":              observation.ObservedAt,
		"status":                  observation.Status,
		"reasonCode":              observation.ReasonCode,
		"identitySubjectDigest":   observation.IdentitySubjectDigest,
	})
	observation.ObservationDigest, _ = canonical.DigestJSON(observationRaw)
	if err := selfidentity.ValidateObservation(observation); err != nil {
		t.Fatal(err)
	}
	return observation
}

func TestStableCodexSchemaCommandRejectsOversizeInput(t *testing.T) {
	var stdout, stderr bytes.Buffer
	input := strings.Repeat("x", codexSchemaCheckMaxInputBytes+1)
	if exit := Run([]string{"internal", "codex-provider-schema-check"}, strings.NewReader(input), &stdout, &stderr); exit != ExitUsage {
		t.Fatalf("exit=%d want=%d", exit, ExitUsage)
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "codex-provider-checker-input-invalid") {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}
