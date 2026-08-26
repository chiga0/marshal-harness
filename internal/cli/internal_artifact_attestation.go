package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"strings"

	"github.com/chiga0/marshal-harness/internal/artifactattestation"
	"github.com/chiga0/marshal-harness/internal/buildinfo"
	"github.com/chiga0/marshal-harness/internal/canonical"
)

const (
	artifactAttestationCheckCommandVersion = "artifact-attestation-check/v1"
	// A maximally admitted 64 MiB raw chain expands to less than 86 MiB under
	// JSON base64. The remaining 10+ MiB is a fixed transport allowance for the
	// closed request and ordinary external policy; the CLI intentionally has a
	// tighter independent transport cap than the in-process policy validator.
	artifactAttestationCheckMaxInputBytes int64 = 96 << 20
	artifactAttestationCheckRequestV1           = "marshal.artifact-attestation-check-request.v1"
)

type artifactAttestationCheckRequest struct {
	SchemaVersion  string                                           `json:"schemaVersion"`
	Phase          string                                           `json:"phase"`
	BuildChain     *artifactattestation.RawBuildRecordSet           `json:"buildChain"`
	BuildPolicy    *artifactattestation.BuildRecordValidationPolicy `json:"buildPolicy"`
	ArtifactChain  *artifactattestation.RawObjectSet                `json:"artifactChain"`
	ArtifactPolicy *artifactattestation.ValidationPolicy            `json:"artifactPolicy"`
}

// artifactAttestationCheckV1Fields is the closed wire manifest. It is
// intentionally independent of the set of exported fields on the in-process
// domain structs: adding a future domain field cannot silently expand v1.
var artifactAttestationCheckV1Fields = map[reflect.Type]map[string]reflect.Type{
	reflect.TypeOf(artifactAttestationCheckRequest{}): frozenArtifactAttestationFields(artifactAttestationCheckRequest{},
		"schemaVersion", "phase", "buildChain", "buildPolicy", "artifactChain", "artifactPolicy"),
	reflect.TypeOf(artifactattestation.RawBuildRecordSet{}): frozenArtifactAttestationFields(artifactattestation.RawBuildRecordSet{},
		"sourceManifest", "compileRootManifest", "generatedSourceStage", "externalMaterialManifests", "buildRecord"),
	reflect.TypeOf(artifactattestation.RawObjectSet{}): frozenArtifactAttestationFields(artifactattestation.RawObjectSet{},
		"sourceManifest", "compileRootManifest", "generatedSourceStage", "externalMaterialManifests", "buildRecord", "buildAttestation"),
	reflect.TypeOf(artifactattestation.BuildRecordValidationPolicy{}): frozenArtifactAttestationFields(artifactattestation.BuildRecordValidationPolicy{},
		"expectedRepository", "expectedSourceHead", "expectedBuildProfile", "expectedSourceBundleDigest", "expectedSourceManifestDigest", "expectedCompileRootManifestDigest",
		"expectedGoModDigest", "expectedGoSumDigest", "expectedBuildInvocationDigest", "expectedEnvironmentPolicyDigest", "expectedToolchainMaterialDigest", "expectedModuleGraphDigest",
		"expectedTargetArch", "expectedGoVersion", "expectedSubmodulePolicyDigest", "expectedLFSPolicyDigest", "expectedDependencyMode", "expectedSubmodules", "expectedLFSObjects",
		"expectedExternalMaterials", "expectedGenerated", "expectedGeneratedStageDigest", "expectedGeneratorInvocationDigest", "expectedGeneratorInputDigest", "expectedGeneratorMaterialDigest",
		"expectedGeneratorToolchainDigest", "expectedBuilderPrincipalId", "expectedBuilderWorkflowIdentity", "expectedBuilderIsolationProfile", "trust"),
	reflect.TypeOf(artifactattestation.ValidationPolicy{}): frozenArtifactAttestationFields(artifactattestation.ValidationPolicy{},
		"expectedRepository", "expectedSourceHead", "expectedBuildProfile", "expectedSourceBundleDigest", "expectedSourceManifestDigest", "expectedCompileRootManifestDigest",
		"expectedGoModDigest", "expectedGoSumDigest", "expectedBuildInvocationDigest", "expectedEnvironmentPolicyDigest", "expectedToolchainMaterialDigest", "expectedModuleGraphDigest",
		"expectedTargetArch", "expectedGoVersion", "expectedSubmodulePolicyDigest", "expectedLFSPolicyDigest", "expectedDependencyMode", "expectedSubmodules", "expectedLFSObjects",
		"expectedExternalMaterials", "expectedGenerated", "expectedGeneratedStageDigest", "expectedGeneratorInvocationDigest", "expectedGeneratorInputDigest", "expectedGeneratorMaterialDigest",
		"expectedGeneratorToolchainDigest", "expectedBuilderPrincipalId", "expectedBuilderWorkflowIdentity", "expectedBuilderIsolationProfile", "expectedArtifactAttestationProducerPrincipalId",
		"expectedCodeSigningWorkflowIdentity", "expectedArtifactAttestationWorkflowIdentity", "expectedCodeSignatureIdentity", "trust"),
	reflect.TypeOf(artifactattestation.SubmoduleV1{}): frozenArtifactAttestationFields(artifactattestation.SubmoduleV1{},
		"path", "pinnedCommit", "materializedTreeDigest"),
	reflect.TypeOf(artifactattestation.LFSObjectV1{}): frozenArtifactAttestationFields(artifactattestation.LFSObjectV1{},
		"path", "pointerDigest", "materializedObjectDigest"),
	reflect.TypeOf(artifactattestation.ExternalMaterialExpectation{}): frozenArtifactAttestationFields(artifactattestation.ExternalMaterialExpectation{},
		"materialKind", "entries"),
	reflect.TypeOf(artifactattestation.CurrentKeyPolicy{}): frozenArtifactAttestationFields(artifactattestation.CurrentKeyPolicy{},
		"producerPrincipalId", "currentKeyEpoch", "keys"),
	reflect.TypeOf(artifactattestation.TrustPolicies{}): frozenArtifactAttestationFields(artifactattestation.TrustPolicies{},
		"buildRecord", "buildAttestation"),
	reflect.TypeOf(artifactattestation.KeyRecord{}): frozenArtifactAttestationFields(artifactattestation.KeyRecord{},
		"keyId", "keyEpoch", "usage", "publicKey", "validFrom", "validUntil", "revokedAt"),
	reflect.TypeOf(artifactattestation.CodeSignatureIdentityV1{}): frozenArtifactAttestationFields(artifactattestation.CodeSignatureIdentityV1{},
		"signatureKind", "identifier", "teamIdentifier", "cdHash", "designatedRequirement", "leafCertificateSHA256", "certificateChainSHA256", "hardenedRuntime", "secureTimestamp"),
}

type artifactAttestationCheckCoreResult struct {
	SourceHead                string
	SourceManifestDigest      string
	CompileRootManifestDigest string
	BuildRecordDigest         string
	AttestationDigest         string
}

type artifactAttestationCheckMarshalIdentity struct {
	Version                string `json:"version"`
	Commit                 string `json:"commit"`
	InternalCommandVersion string `json:"internalCommandVersion"`
	InputDigest            string `json:"inputDigest"`
}

type artifactAttestationCheckOutput struct {
	Pass                      bool                                    `json:"pass"`
	Status                    string                                  `json:"status"`
	ReasonCode                string                                  `json:"reasonCode"`
	Phase                     string                                  `json:"phase"`
	Marshal                   artifactAttestationCheckMarshalIdentity `json:"marshal"`
	SourceHead                string                                  `json:"sourceHead"`
	SourceManifestDigest      string                                  `json:"sourceManifestDigest"`
	CompileRootManifestDigest string                                  `json:"compileRootManifestDigest"`
	BuildRecordDigest         string                                  `json:"buildRecordDigest"`
	AttestationDigest         *string                                 `json:"attestationDigest,omitempty"`
}

var artifactAttestationCheckBuildInfo = buildinfo.Current
var artifactAttestationCheckCore = validateArtifactAttestationCheckCore

// runInternalArtifactAttestationCheck keeps both protected-build validation
// phases inside the already-built Marshal executable. It reads no files,
// environment, network, or process state and never launches a child process.
func runInternalArtifactAttestationCheck(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	var err error
	args, err = consumeStableAttestation(args, stdin)
	if err != nil {
		return writeArtifactAttestationCheckFailure(stderr, "checker-handshake-invalid", ExitFailure)
	}
	if len(args) != 0 {
		return writeArtifactAttestationCheckFailure(stderr, "checker-arguments-invalid", ExitUsage)
	}
	raw, reason := readArtifactAttestationCheckInput(stdin, artifactAttestationCheckMaxInputBytes)
	if reason != "" {
		return writeArtifactAttestationCheckFailure(stderr, reason, ExitFailure)
	}
	request, reason := decodeArtifactAttestationCheckRequest(raw)
	if reason != "" {
		return writeArtifactAttestationCheckFailure(stderr, reason, ExitFailure)
	}
	build := artifactAttestationCheckBuildInfo()
	if !isLowerHexCommit(build.Commit) || build.Version == "" || build.Version == "unknown" {
		return writeArtifactAttestationCheckFailure(stderr, "checker-build-identity-invalid", ExitFailure)
	}
	result, err := artifactAttestationCheckCore(request)
	if err != nil {
		return writeArtifactAttestationCheckFailure(stderr, "checker-core-rejected", ExitFailure)
	}
	output := artifactAttestationCheckOutput{
		Pass: true, Status: "pass", ReasonCode: "artifact-attestation-check-pass", Phase: request.Phase,
		Marshal:    artifactAttestationCheckMarshalIdentity{Version: build.Version, Commit: build.Commit, InternalCommandVersion: artifactAttestationCheckCommandVersion, InputDigest: canonical.DigestBytes(raw)},
		SourceHead: result.SourceHead, SourceManifestDigest: result.SourceManifestDigest, CompileRootManifestDigest: result.CompileRootManifestDigest, BuildRecordDigest: result.BuildRecordDigest,
	}
	if request.Phase == "post-sign" {
		output.AttestationDigest = &result.AttestationDigest
	}
	if err := writeCanonicalArtifactAttestationJSON(stdout, output); err != nil {
		return ExitFailure
	}
	return ExitOK
}

func readArtifactAttestationCheckInput(input io.Reader, limit int64) ([]byte, string) {
	raw, err := io.ReadAll(io.LimitReader(input, limit+1))
	if err != nil {
		return nil, "checker-input-read-failed"
	}
	if int64(len(raw)) > limit {
		return nil, "checker-input-too-large"
	}
	return raw, ""
}

func decodeArtifactAttestationCheckRequest(raw []byte) (artifactAttestationCheckRequest, string) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var request artifactAttestationCheckRequest
	if err := decoder.Decode(&request); err != nil {
		return artifactAttestationCheckRequest{}, "checker-input-invalid"
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return artifactAttestationCheckRequest{}, "checker-input-trailing"
	}
	canonicalRaw, err := canonical.JSON(raw)
	if err != nil || !bytes.Equal(raw, canonicalRaw) {
		return artifactAttestationCheckRequest{}, "checker-input-invalid"
	}
	var document any
	if json.Unmarshal(raw, &document) != nil || !exactArtifactAttestationJSONShape(document, reflect.TypeOf(request)) {
		return artifactAttestationCheckRequest{}, "checker-input-invalid"
	}
	if request.SchemaVersion != artifactAttestationCheckRequestV1 {
		return artifactAttestationCheckRequest{}, "checker-request-version-invalid"
	}
	switch request.Phase {
	case "pre-sign":
		if request.BuildChain == nil || request.BuildPolicy == nil || request.ArtifactChain != nil || request.ArtifactPolicy != nil {
			return artifactAttestationCheckRequest{}, "checker-phase-input-invalid"
		}
	case "post-sign":
		if request.BuildChain != nil || request.BuildPolicy != nil || request.ArtifactChain == nil || request.ArtifactPolicy == nil {
			return artifactAttestationCheckRequest{}, "checker-phase-input-invalid"
		}
	default:
		return artifactAttestationCheckRequest{}, "checker-phase-invalid"
	}
	return request, ""
}

func exactArtifactAttestationJSONShape(value any, target reflect.Type) bool {
	for target.Kind() == reflect.Pointer {
		if value == nil {
			return true
		}
		target = target.Elem()
	}
	switch target.Kind() {
	case reflect.Struct:
		object, ok := value.(map[string]any)
		if !ok {
			// Custom scalar decoders such as time.Time remain the JSON decoder's
			// responsibility; there are no member names to admit here.
			return true
		}
		fields, frozen := artifactAttestationCheckV1Fields[target]
		if !frozen {
			return false
		}
		for name, member := range object {
			fieldType, ok := fields[name]
			if !ok || !exactArtifactAttestationJSONShape(member, fieldType) {
				return false
			}
		}
		return true
	case reflect.Map:
		object, ok := value.(map[string]any)
		if !ok {
			return value == nil
		}
		for _, member := range object {
			if !exactArtifactAttestationJSONShape(member, target.Elem()) {
				return false
			}
		}
		return true
	case reflect.Slice, reflect.Array:
		if target.Elem().Kind() == reflect.Uint8 {
			_, ok := value.(string)
			return ok || value == nil
		}
		array, ok := value.([]any)
		if !ok {
			return value == nil
		}
		for _, member := range array {
			if !exactArtifactAttestationJSONShape(member, target.Elem()) {
				return false
			}
		}
		return true
	default:
		return true
	}
}

func frozenArtifactAttestationFields(value any, names ...string) map[string]reflect.Type {
	target := reflect.TypeOf(value)
	result := make(map[string]reflect.Type, len(names))
	for _, name := range names {
		var matched *reflect.StructField
		for index := 0; index < target.NumField(); index++ {
			field := target.Field(index)
			wireName := strings.Split(field.Tag.Get("json"), ",")[0]
			if wireName == name {
				copy := field
				matched = &copy
				break
			}
		}
		if matched == nil {
			panic("artifact attestation v1 wire member missing: " + name)
		}
		result[name] = matched.Type
	}
	return result
}

func validateArtifactAttestationCheckCore(request artifactAttestationCheckRequest) (artifactAttestationCheckCoreResult, error) {
	validator, err := artifactattestation.NewValidator()
	if err != nil {
		return artifactAttestationCheckCoreResult{}, err
	}
	switch request.Phase {
	case "pre-sign":
		verified, err := validator.ValidateBuildRecordChain(*request.BuildChain, *request.BuildPolicy)
		if err != nil {
			return artifactAttestationCheckCoreResult{}, err
		}
		return artifactAttestationCheckCoreResult{SourceHead: verified.SourceManifest.SourceHead, SourceManifestDigest: verified.SourceManifest.ManifestDigest, CompileRootManifestDigest: verified.CompileRootManifest.ManifestDigest, BuildRecordDigest: verified.BuildRecord.RecordDigest}, nil
	case "post-sign":
		verified, err := validator.ValidateArtifactChain(*request.ArtifactChain, *request.ArtifactPolicy)
		if err != nil {
			return artifactAttestationCheckCoreResult{}, err
		}
		return artifactAttestationCheckCoreResult{SourceHead: verified.SourceManifest.SourceHead, SourceManifestDigest: verified.SourceManifest.ManifestDigest, CompileRootManifestDigest: verified.CompileRootManifest.ManifestDigest, BuildRecordDigest: verified.BuildRecord.RecordDigest, AttestationDigest: verified.BuildAttestation.AttestationDigest}, nil
	default:
		return artifactAttestationCheckCoreResult{}, artifactattestation.ErrRejected
	}
}

func writeArtifactAttestationCheckFailure(stderr io.Writer, reason string, code int) int {
	// reason is selected solely from this command's closed constants and never
	// includes decoder, validator, key, object, policy, or input-derived text.
	failure := struct {
		Status     string `json:"status"`
		ReasonCode string `json:"reasonCode"`
	}{Status: "fail", ReasonCode: reason}
	if err := writeCanonicalArtifactAttestationJSON(stderr, failure); err != nil {
		return ExitFailure
	}
	return code
}

func writeCanonicalArtifactAttestationJSON(output io.Writer, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	canonicalRaw, err := canonical.JSON(raw)
	if err != nil {
		return err
	}
	written, err := output.Write(canonicalRaw)
	if err != nil {
		return err
	}
	if written != len(canonicalRaw) {
		return io.ErrShortWrite
	}
	return nil
}
