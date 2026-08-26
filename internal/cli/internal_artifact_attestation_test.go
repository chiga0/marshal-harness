package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/chiga0/marshal-harness/internal/artifactattestation"
	"github.com/chiga0/marshal-harness/internal/buildinfo"
	"github.com/chiga0/marshal-harness/internal/canonical"
)

func TestInternalArtifactAttestationCheckIsHiddenAndClosesFraming(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if exit := Run([]string{"help"}, strings.NewReader(""), &stdout, &stderr); exit != ExitOK {
		t.Fatalf("help exit=%d", exit)
	}
	if strings.Contains(stdout.String(), "artifact-attestation-check") {
		t.Fatal("artifact attestation checker appeared in public help")
	}

	valid := canonicalArtifactCheckRequest(t, artifactAttestationCheckRequest{
		SchemaVersion: artifactAttestationCheckRequestV1,
		Phase:         "pre-sign",
		BuildChain:    &artifactattestation.RawBuildRecordSet{},
		BuildPolicy:   &artifactattestation.BuildRecordValidationPolicy{},
	})
	cases := []struct {
		name, input, reason string
	}{
		{"unknown", `{"buildChain":{},"buildPolicy":{},"future":true,"phase":"pre-sign","schemaVersion":"marshal.artifact-attestation-check-request.v1"}`, "checker-input-invalid"},
		{"wrong-case", `{"BuildChain":{},"buildPolicy":{},"phase":"pre-sign","schemaVersion":"marshal.artifact-attestation-check-request.v1"}`, "checker-input-invalid"},
		{"duplicate", `{"buildChain":{},"buildPolicy":{},"phase":"pre-sign","phase":"pre-sign","schemaVersion":"marshal.artifact-attestation-check-request.v1"}`, "checker-input-invalid"},
		{"trailing", valid + `{}`, "checker-input-trailing"},
		{"noncanonical", " " + valid, "checker-input-invalid"},
		{"unknown-version", strings.Replace(valid, artifactAttestationCheckRequestV1, "future", 1), "checker-request-version-invalid"},
		{"unknown-phase", strings.Replace(valid, "pre-sign", "future", 1), "checker-phase-invalid"},
		{"attestation-in-pre-sign", `{"artifactChain":{},"artifactPolicy":{},"buildChain":{},"buildPolicy":{},"phase":"pre-sign","schemaVersion":"marshal.artifact-attestation-check-request.v1"}`, "checker-phase-input-invalid"},
		{"nested-attestation-in-pre-sign", `{"buildChain":{"buildAttestation":"eA=="},"buildPolicy":{},"phase":"pre-sign","schemaVersion":"marshal.artifact-attestation-check-request.v1"}`, "checker-input-invalid"},
		{"future-signer-policy-in-pre-sign", `{"buildChain":{},"buildPolicy":{"expectedCodeSignatureIdentity":{}},"phase":"pre-sign","schemaVersion":"marshal.artifact-attestation-check-request.v1"}`, "checker-input-invalid"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			stdout.Reset()
			stderr.Reset()
			exit := Run([]string{"internal", "artifact-attestation-check"}, strings.NewReader(test.input), &stdout, &stderr)
			wantFailure := `{"reasonCode":"` + test.reason + `","status":"fail"}`
			if exit != ExitFailure || stdout.Len() != 0 || stderr.String() != wantFailure {
				t.Fatalf("exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
			}
		})
	}
}

func TestInternalArtifactAttestationCheckArgumentsHandshakeAndBoundedRead(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if exit := Run([]string{"internal", "artifact-attestation-check", "extra"}, strings.NewReader(""), &stdout, &stderr); exit != ExitUsage || !strings.Contains(stderr.String(), "checker-arguments-invalid") {
		t.Fatalf("argument exit=%d stderr=%q", exit, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if exit := Run([]string{"internal", "artifact-attestation-check", "--attestation-ready"}, strings.NewReader(""), &stdout, &stderr); exit != ExitFailure || !strings.Contains(stderr.String(), "checker-handshake-invalid") {
		t.Fatalf("handshake exit=%d stderr=%q", exit, stderr.String())
	}
	if _, reason := readArtifactAttestationCheckInput(strings.NewReader("12345"), 4); reason != "checker-input-too-large" {
		t.Fatalf("oversize reason=%q", reason)
	}
	if _, reason := readArtifactAttestationCheckInput(failingTranscriptReader{}, 4); reason != "checker-input-read-failed" {
		t.Fatalf("read reason=%q", reason)
	}
	stdout.Reset()
	stderr.Reset()
	if exit := Run([]string{"internal", "artifact-attestation-check"}, failingTranscriptReader{}, &stdout, &stderr); exit != ExitFailure || stdout.Len() != 0 || !strings.Contains(stderr.String(), "checker-input-read-failed") {
		t.Fatalf("read failure exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
	const admittedRawChainBytes int64 = 64 << 20
	base64Bytes := ((admittedRawChainBytes + 2) / 3) * 4
	if artifactAttestationCheckMaxInputBytes < base64Bytes+(8<<20) {
		t.Fatalf("stdin cap %d does not admit a 64 MiB raw chain plus bounded envelope", artifactAttestationCheckMaxInputBytes)
	}
}

func TestInternalArtifactAttestationCheckRejectsUnknownBuildIdentity(t *testing.T) {
	request := canonicalArtifactCheckRequest(t, artifactAttestationCheckRequest{
		SchemaVersion: artifactAttestationCheckRequestV1,
		Phase:         "pre-sign",
		BuildChain:    &artifactattestation.RawBuildRecordSet{},
		BuildPolicy:   &artifactattestation.BuildRecordValidationPolicy{},
	})
	withArtifactAttestationCheckerSeams(t, func() buildinfo.Info { return buildinfo.Info{Version: "dev", Commit: "unknown"} }, func(artifactAttestationCheckRequest) (artifactAttestationCheckCoreResult, error) {
		return artifactAttestationCheckCoreResult{}, nil
	})
	var stdout, stderr bytes.Buffer
	if exit := Run([]string{"internal", "artifact-attestation-check"}, strings.NewReader(request), &stdout, &stderr); exit != ExitFailure || stdout.Len() != 0 || !strings.Contains(stderr.String(), "checker-build-identity-invalid") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
}

func TestInternalArtifactAttestationCheckSuccessSummaryAndPhaseSeparation(t *testing.T) {
	build := func() buildinfo.Info { return buildinfo.Info{Version: "v1.0.0", Commit: strings.Repeat("a", 40)} }
	core := func(request artifactAttestationCheckRequest) (artifactAttestationCheckCoreResult, error) {
		if request.Phase != "pre-sign" && request.Phase != "post-sign" {
			return artifactAttestationCheckCoreResult{}, errors.New("unexpected phase")
		}
		return artifactAttestationCheckCoreResult{SourceHead: strings.Repeat("b", 40), SourceManifestDigest: "sha256:source", CompileRootManifestDigest: "sha256:compile", BuildRecordDigest: "sha256:record", AttestationDigest: "sha256:attestation"}, nil
	}
	withArtifactAttestationCheckerSeams(t, build, core)

	for _, phase := range []string{"pre-sign", "post-sign"} {
		t.Run(phase, func(t *testing.T) {
			request := artifactAttestationCheckRequest{SchemaVersion: artifactAttestationCheckRequestV1, Phase: phase}
			external := map[string]artifactattestation.ExternalMaterialExpectation{"leak-marker-digest": {MaterialKind: "leak-marker-kind", Entries: map[string][]string{"leak-marker-entry": {"leak-marker-reference"}}}}
			key := artifactattestation.CurrentKeyPolicy{ProducerPrincipalID: "leak-marker-producer", Keys: []artifactattestation.KeyRecord{{KeyID: "leak-marker-key"}}}
			if phase == "pre-sign" {
				request.BuildChain = &artifactattestation.RawBuildRecordSet{SourceManifest: []byte("secret-raw-object")}
				request.BuildPolicy = &artifactattestation.BuildRecordValidationPolicy{ExpectedRepository: "leak-marker-policy", ExpectedSubmodules: []artifactattestation.SubmoduleV1{{Path: "leak-marker-path"}}, ExpectedExternalMaterials: external, Trust: key}
			} else {
				request.ArtifactChain = &artifactattestation.RawObjectSet{SourceManifest: []byte("secret-raw-object")}
				request.ArtifactPolicy = &artifactattestation.ValidationPolicy{ExpectedRepository: "leak-marker-policy", ExpectedSubmodules: []artifactattestation.SubmoduleV1{{Path: "leak-marker-path"}}, ExpectedExternalMaterials: external, ExpectedCodeSignatureIdentity: artifactattestation.CodeSignatureIdentityV1{DesignatedRequirement: "leak-marker-signature"}, Trust: artifactattestation.TrustPolicies{BuildRecord: key, BuildAttestation: key}}
			}
			input := canonicalArtifactCheckRequest(t, request)
			var stdout, stderr bytes.Buffer
			exit := Run([]string{"internal", "artifact-attestation-check", "--attestation-ready"}, strings.NewReader("x"+input), &stdout, &stderr)
			if exit != ExitOK || stderr.Len() != 0 {
				t.Fatalf("exit=%d stderr=%q", exit, stderr.String())
			}
			var output artifactAttestationCheckOutput
			if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
				t.Fatal(err)
			}
			if !output.Pass || output.Status != "pass" || output.Phase != phase || output.BuildRecordDigest != "sha256:record" || output.Marshal.InputDigest != canonical.DigestBytes([]byte(input)) {
				t.Fatalf("output=%+v", output)
			}
			if (output.AttestationDigest != nil) != (phase == "post-sign") {
				t.Fatalf("phase=%s attestationDigest=%v", phase, output.AttestationDigest)
			}
			if strings.Contains(stdout.String(), "secret") || strings.Contains(stdout.String(), "leak-marker") {
				t.Fatalf("output leaked input: %q", stdout.String())
			}
			attestationField := ""
			if phase == "post-sign" {
				attestationField = `"attestationDigest":"sha256:attestation",`
			}
			want := `{` + attestationField + `"buildRecordDigest":"sha256:record","compileRootManifestDigest":"sha256:compile","marshal":{"commit":"` + strings.Repeat("a", 40) + `","inputDigest":"` + canonical.DigestBytes([]byte(input)) + `","internalCommandVersion":"artifact-attestation-check/v1","version":"v1.0.0"},"pass":true,"phase":"` + phase + `","reasonCode":"artifact-attestation-check-pass","sourceHead":"` + strings.Repeat("b", 40) + `","sourceManifestDigest":"sha256:source","status":"pass"}`
			if stdout.String() != want || strings.HasSuffix(stdout.String(), "\n") {
				t.Fatalf("non-canonical success\n got: %q\nwant: %q", stdout.String(), want)
			}
			canonicalOutput, err := canonical.JSON(stdout.Bytes())
			if err != nil || !bytes.Equal(canonicalOutput, stdout.Bytes()) {
				t.Fatalf("success is not exact JCS: %v", err)
			}
		})
	}
}

func TestArtifactAttestationCheckV1FieldManifestIsFrozen(t *testing.T) {
	document := map[string]any{"futureDomainField": true}
	if exactArtifactAttestationJSONShape(document, reflect.TypeOf(artifactattestation.BuildRecordValidationPolicy{})) {
		t.Fatal("future domain member expanded request v1")
	}
	wantTop := []string{"artifactChain", "artifactPolicy", "buildChain", "buildPolicy", "phase", "schemaVersion"}
	gotTop := make([]string, 0, len(artifactAttestationCheckV1Fields[reflect.TypeOf(artifactAttestationCheckRequest{})]))
	for name := range artifactAttestationCheckV1Fields[reflect.TypeOf(artifactAttestationCheckRequest{})] {
		gotTop = append(gotTop, name)
	}
	sort.Strings(gotTop)
	if !slices.Equal(gotTop, wantTop) {
		t.Fatalf("top-level v1 members=%v want=%v", gotTop, wantTop)
	}
}

func TestArtifactAttestationCheckCanonicalWriterFailureIsFatal(t *testing.T) {
	for name, writer := range map[string]io.Writer{"error": rejectingArtifactAttestationWriter{}, "short": shortArtifactAttestationWriter{}} {
		if code := writeArtifactAttestationCheckFailure(writer, "checker-input-invalid", ExitUsage); code != ExitFailure {
			t.Fatalf("%s failure writer code=%d want=%d", name, code, ExitFailure)
		}
	}
	withArtifactAttestationCheckerSeams(t,
		func() buildinfo.Info { return buildinfo.Info{Version: "v1", Commit: strings.Repeat("a", 40)} },
		func(artifactAttestationCheckRequest) (artifactAttestationCheckCoreResult, error) {
			return artifactAttestationCheckCoreResult{SourceHead: strings.Repeat("b", 40), SourceManifestDigest: "sha256:source", CompileRootManifestDigest: "sha256:compile", BuildRecordDigest: "sha256:record"}, nil
		})
	request := canonicalArtifactCheckRequest(t, artifactAttestationCheckRequest{SchemaVersion: artifactAttestationCheckRequestV1, Phase: "pre-sign", BuildChain: &artifactattestation.RawBuildRecordSet{}, BuildPolicy: &artifactattestation.BuildRecordValidationPolicy{}})
	for name, writer := range map[string]io.Writer{"error": rejectingArtifactAttestationWriter{}, "short": shortArtifactAttestationWriter{}} {
		var stderr bytes.Buffer
		if code := Run([]string{"internal", "artifact-attestation-check"}, strings.NewReader(request), writer, &stderr); code != ExitFailure || stderr.Len() != 0 {
			t.Fatalf("%s success writer code=%d stderr=%q", name, code, stderr.String())
		}
	}
}

type rejectingArtifactAttestationWriter struct{}

func (rejectingArtifactAttestationWriter) Write([]byte) (int, error) {
	return 0, errors.New("fixture write failure")
}

type shortArtifactAttestationWriter struct{}

func (shortArtifactAttestationWriter) Write(input []byte) (int, error) {
	return len(input) - 1, nil
}

func canonicalArtifactCheckRequest(t *testing.T, request artifactAttestationCheckRequest) string {
	t.Helper()
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	canonicalRaw, err := canonical.JSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	return string(canonicalRaw)
}

func withArtifactAttestationCheckerSeams(t *testing.T, build func() buildinfo.Info, core func(artifactAttestationCheckRequest) (artifactAttestationCheckCoreResult, error)) {
	t.Helper()
	priorBuild, priorCore := artifactAttestationCheckBuildInfo, artifactAttestationCheckCore
	artifactAttestationCheckBuildInfo, artifactAttestationCheckCore = build, core
	t.Cleanup(func() {
		artifactAttestationCheckBuildInfo, artifactAttestationCheckCore = priorBuild, priorCore
	})
}
