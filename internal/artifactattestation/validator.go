package artifactattestation

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"path"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/chiga0/marshal-harness/internal/authorityprovider"
	"github.com/chiga0/marshal-harness/internal/canonical"
	marshalSchemas "github.com/chiga0/marshal-harness/schemas"
	"github.com/dlclark/regexp2"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

const schemaID = "https://marshal.local/schemas/selfidentity/artifact-attestation/v1"

type schemaRegexp regexp2.Regexp

func (re *schemaRegexp) MatchString(value string) bool {
	matched, err := (*regexp2.Regexp)(re).MatchString(value)
	return err == nil && matched
}
func (re *schemaRegexp) String() string { return (*regexp2.Regexp)(re).String() }

type Validator struct {
	schemas map[string]*jsonschema.Schema
}

func NewValidator() (*Validator, error) {
	data, err := marshalSchemas.FS.ReadFile("selfidentity/artifact-attestation.schema.json")
	if err != nil {
		return nil, fmt.Errorf("artifact attestation schema: %w", err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.AssertFormat()
	compiler.UseRegexpEngine(func(pattern string) (jsonschema.Regexp, error) {
		re, compileErr := regexp2.Compile(pattern, regexp2.ECMAScript)
		return (*schemaRegexp)(re), compileErr
	})
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("artifact attestation schema decode: %w", err)
	}
	if err := compiler.AddResource(schemaID, document); err != nil {
		return nil, fmt.Errorf("artifact attestation schema register: %w", err)
	}
	names := []string{"sourceManifest", "compileRootManifest", "generatedSourceStage", "externalMaterialManifest", "buildRecord", "buildAttestation"}
	compiled := make(map[string]*jsonschema.Schema, len(names)+1)
	compiled["root"], err = compiler.Compile(schemaID)
	if err != nil {
		return nil, fmt.Errorf("artifact attestation root schema: %w", err)
	}
	for _, name := range names {
		compiled[name], err = compiler.Compile(schemaID + "#/$defs/" + name)
		if err != nil {
			return nil, fmt.Errorf("artifact attestation schema %s: %w", name, err)
		}
	}
	return &Validator{schemas: compiled}, nil
}

// ValidateArtifactChain validates a complete, already-resolved immutable
// protected-build chain. It has no filesystem, process, network, or signing
// side effects and can be called directly by the next artifact signer or
// installer consumer.
func (v *Validator) ValidateArtifactChain(raw RawObjectSet, policy ValidationPolicy) (VerifiedChain, error) {
	if v == nil || !withinBudgets(raw) || !withinPolicyBudgets(policy) {
		return VerifiedChain{}, ErrRejected
	}
	raw = snapshotRaw(raw)
	policy = snapshotValidationPolicy(policy)
	if !validValidationPolicy(policy) {
		return VerifiedChain{}, ErrRejected
	}
	var result VerifiedChain
	if err := v.admit("sourceManifest", raw.SourceManifest, &result.SourceManifest); err != nil {
		return VerifiedChain{}, ErrRejected
	}
	if err := v.admit("compileRootManifest", raw.CompileRootManifest, &result.CompileRootManifest); err != nil {
		return VerifiedChain{}, ErrRejected
	}
	if len(raw.GeneratedSourceStage) > 0 {
		var stage GeneratedSourceStageV1
		if err := v.admit("generatedSourceStage", raw.GeneratedSourceStage, &stage); err != nil {
			return VerifiedChain{}, ErrRejected
		}
		result.GeneratedSourceStage = &stage
	}
	result.ExternalMaterials = make([]ExternalBuildMaterialManifestV1, len(raw.ExternalMaterialManifests))
	for i := range raw.ExternalMaterialManifests {
		if err := v.admit("externalMaterialManifest", raw.ExternalMaterialManifests[i], &result.ExternalMaterials[i]); err != nil {
			return VerifiedChain{}, ErrRejected
		}
	}
	if err := v.admit("buildRecord", raw.BuildRecord, &result.BuildRecord); err != nil {
		return VerifiedChain{}, ErrRejected
	}
	if err := v.admit("buildAttestation", raw.BuildAttestation, &result.BuildAttestation); err != nil {
		return VerifiedChain{}, ErrRejected
	}
	if err := validateChain(raw, &result, policy); err != nil {
		return VerifiedChain{}, ErrRejected
	}
	return result, nil
}

func (v *Validator) admit(name string, raw []byte, destination any) error {
	if len(raw) == 0 || len(raw) > maxObjectBytes {
		return ErrRejected
	}
	canonicalRaw, err := canonical.JSON(raw)
	if err != nil || !bytes.Equal(raw, canonicalRaw) {
		return ErrRejected
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil || v.schemas[name] == nil || v.schemas[name].Validate(document) != nil {
		return ErrRejected
	}
	if err := json.Unmarshal(raw, destination); err != nil {
		return ErrRejected
	}
	return nil
}

func validateChain(raw RawObjectSet, c *VerifiedChain, policy ValidationPolicy) error {
	if c.SourceManifest.Repository != policy.ExpectedRepository || c.SourceManifest.SourceHead != policy.ExpectedSourceHead || c.SourceManifest.BuildProfile != policy.ExpectedBuildProfile || c.SourceManifest.SourceBundleDigest != policy.ExpectedSourceBundleDigest || c.SourceManifest.ManifestDigest != policy.ExpectedSourceManifestDigest || c.CompileRootManifest.ManifestDigest != policy.ExpectedCompileRootManifestDigest || !reflect.DeepEqual(c.SourceManifest.GoModDigest, policy.ExpectedGoModDigest) || !reflect.DeepEqual(c.SourceManifest.GoSumDigest, policy.ExpectedGoSumDigest) || !reflect.DeepEqual(c.SourceManifest.Submodules, policy.ExpectedSubmodules) || !reflect.DeepEqual(c.SourceManifest.LFSObjects, policy.ExpectedLFSObjects) || c.SourceManifest.DependencyMode != policy.ExpectedDependencyMode || c.SourceManifest.ModuleGraphDigest != policy.ExpectedModuleGraphDigest || c.SourceManifest.BuildInvocationDigest != policy.ExpectedBuildInvocationDigest || c.SourceManifest.EnvironmentPolicyDigest != policy.ExpectedEnvironmentPolicyDigest || c.SourceManifest.ToolchainDistributionDigest != policy.ExpectedToolchainMaterialDigest || c.SourceManifest.TargetArch != policy.ExpectedTargetArch || c.SourceManifest.GoVersion != policy.ExpectedGoVersion || c.SourceManifest.SubmodulePolicyDigest != policy.ExpectedSubmodulePolicyDigest || c.SourceManifest.LFSPolicyDigest != policy.ExpectedLFSPolicyDigest ||
		c.BuildRecord.Repository != policy.ExpectedRepository || c.BuildRecord.SourceHead != policy.ExpectedSourceHead || c.BuildRecord.BuildProfile != policy.ExpectedBuildProfile ||
		c.BuildAttestation.Repository != policy.ExpectedRepository || c.BuildAttestation.SourceHead != policy.ExpectedSourceHead || c.BuildAttestation.BuildProfile != policy.ExpectedBuildProfile {
		return ErrRejected
	}
	if !authorityFactsMatchPolicy(c, policy) {
		return ErrRejected
	}
	if !validSourceManifest(raw.SourceManifest, &c.SourceManifest) || !sourceMaterialClosure(c.SourceManifest, policy) || !validCompileRoot(raw.CompileRootManifest, &c.CompileRootManifest) {
		return ErrRejected
	}
	if c.CompileRootManifest.Repository != c.SourceManifest.Repository || c.CompileRootManifest.SourceHead != c.SourceManifest.SourceHead || c.CompileRootManifest.SourceManifestDigest != c.SourceManifest.ManifestDigest {
		return ErrRejected
	}
	if err := validateGenerated(raw.GeneratedSourceStage, c, policy); err != nil {
		return err
	}
	externalByDigest := make(map[string]ExternalBuildMaterialManifestV1, len(c.ExternalMaterials))
	logicalContent := make(map[string]string)
	for i := range c.ExternalMaterials {
		material := &c.ExternalMaterials[i]
		if !validExternalManifest(raw.ExternalMaterialManifests[i], material, logicalContent) {
			return ErrRejected
		}
		if _, duplicate := externalByDigest[material.ManifestDigest]; duplicate {
			return ErrRejected
		}
		externalByDigest[material.ManifestDigest] = *material
	}
	if !externalMaterialsMatchPolicy(c.ExternalMaterials, externalByDigest, policy) {
		return ErrRejected
	}
	if !validBuildRecord(raw.BuildRecord, &c.BuildRecord, externalByDigest) {
		return ErrRejected
	}
	createdAt, ok := canonicalTime(c.BuildRecord.CreatedAt)
	if !ok {
		return ErrRejected
	}
	buildKey, err := verifyEnvelope(raw.BuildRecord, []string{"recordDigest", "signedObjectEnvelope"}, c.BuildRecord.RecordDigest, c.BuildRecord.SignedObjectEnvelope, BuildRecordDomain, BuildRecordKeyUsage, c.BuildRecord.BuilderPrincipalID, createdAt, policy.Trust.BuildRecord)
	if err != nil {
		return err
	}
	if !validAttestation(raw.BuildAttestation, &c.BuildAttestation) || !attestationMatchesChain(c, externalByDigest) {
		return ErrRejected
	}
	issuedAt, ok := canonicalTime(c.BuildAttestation.IssuedAt)
	if !ok {
		return ErrRejected
	}
	attestationKey, err := verifyEnvelope(raw.BuildAttestation, []string{"attestationDigest", "signedObjectEnvelope"}, c.BuildAttestation.AttestationDigest, c.BuildAttestation.SignedObjectEnvelope, BuildAttestationDomain, BuildAttestationUsage, c.BuildAttestation.ArtifactAttestationProducerPrincipalID, issuedAt, policy.Trust.BuildAttestation)
	if err != nil || c.BuildRecord.BuilderPrincipalID == c.BuildAttestation.ArtifactAttestationProducerPrincipalID || sameSigningKey(buildKey, attestationKey) {
		return ErrRejected
	}
	return nil
}

func validSourceManifest(raw []byte, source *SourceManifestV1) bool {
	if source.SchemaVersion != SourceManifestSchema || !validHead(source.SourceHead, source.GitObjectFormat) || !validEntries(source.Entries) || digestOf(source.Entries) != source.RootDigest || digestExcluding(raw, "manifestDigest") != source.ManifestDigest {
		return false
	}
	return validPathObjects(source.Submodules, func(item SubmoduleV1) string { return item.Path }) && validPathObjects(source.LFSObjects, func(item LFSObjectV1) string { return item.Path })
}

func validCompileRoot(raw []byte, compile *CompileRootManifestV1) bool {
	return compile.SchemaVersion == CompileRootManifestSchema && validEntries(compile.Entries) && digestOf(compile.Entries) == compile.RootDigest && digestExcluding(raw, "manifestDigest") == compile.ManifestDigest
}

func validateGenerated(raw []byte, c *VerifiedChain, policy ValidationPolicy) error {
	compile := c.CompileRootManifest
	if compile.GeneratedSourceStageDigest == nil {
		if policy.ExpectedGenerated || c.GeneratedSourceStage != nil || c.SourceManifest.GeneratedSourcePolicy != nil || compile.RootDigest != c.SourceManifest.RootDigest || !reflect.DeepEqual(compile.Entries, c.SourceManifest.Entries) {
			return ErrRejected
		}
		return nil
	}
	if !policy.ExpectedGenerated || c.GeneratedSourceStage == nil || c.SourceManifest.GeneratedSourcePolicy == nil {
		return ErrRejected
	}
	stage, declaration := c.GeneratedSourceStage, c.SourceManifest.GeneratedSourcePolicy
	if stage.SchemaVersion != GeneratedSourceStageSchema || digestExcluding(raw, "stageDigest") != stage.StageDigest || *compile.GeneratedSourceStageDigest != stage.StageDigest || stage.SourceManifestDigest != c.SourceManifest.ManifestDigest ||
		stage.StageID != declaration.GeneratedStageID || stage.GeneratorMaterialDigest != declaration.GeneratorMaterialDigest || stage.GeneratorToolchainDigest != declaration.GeneratorToolchainDigest || stage.GeneratorInvocationDigest != declaration.GeneratorInvocationDigest || stage.GeneratorInputDigest != declaration.GeneratorInputDigest || declaration.GeneratorOutputManifestDigest != stage.RootDigest ||
		stage.StageDigest != policy.ExpectedGeneratedStageDigest || stage.GeneratorMaterialDigest != policy.ExpectedGeneratorMaterialDigest || stage.GeneratorToolchainDigest != policy.ExpectedGeneratorToolchainDigest || stage.GeneratorInvocationDigest != policy.ExpectedGeneratorInvocationDigest || stage.GeneratorInputDigest != policy.ExpectedGeneratorInputDigest ||
		!validEntries(stage.Entries) || digestOf(stage.Entries) != stage.RootDigest || compile.RootDigest != stage.RootDigest || !reflect.DeepEqual(compile.Entries, stage.Entries) {
		return ErrRejected
	}
	return nil
}

func validExternalManifest(raw []byte, material *ExternalBuildMaterialManifestV1, logicalContent map[string]string) bool {
	if material.SchemaVersion != ExternalMaterialSchema || digestExcluding(raw, "manifestDigest") != material.ManifestDigest || len(material.Entries) == 0 {
		return false
	}
	previous := ""
	foldedPaths := make(map[string]struct{}, len(material.Entries))
	folder := cases.Fold()
	for _, entry := range material.Entries {
		identity := entry.LogicalIdentity + "\x00" + entry.Path
		if identity <= previous || !validRelativePath(entry.Path) || !sortedUnique(entry.ReferencedBy) {
			return false
		}
		previous = identity
		foldedPath := folder.String(entry.Path)
		if _, collision := foldedPaths[foldedPath]; collision {
			return false
		}
		foldedPaths[foldedPath] = struct{}{}
		content := fmt.Sprintf("%s\x00%d\x00%d\x00%s", entry.EntryType, entry.Mode, entry.Length, pointerValue(entry.SHA256))
		if prior, exists := logicalContent[entry.LogicalIdentity]; exists && prior != content {
			return false
		}
		logicalContent[entry.LogicalIdentity] = content
		switch entry.EntryType {
		case "regular":
			if entry.SHA256 == nil {
				return false
			}
		case "directory":
			if entry.Length != 0 || entry.SHA256 != nil {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func validBuildRecord(raw []byte, record *MarshalArtifactBuildRecordV1, external map[string]ExternalBuildMaterialManifestV1) bool {
	if record.SchemaVersion != BuildRecordSchema || !sortedUnique(record.ExternalMaterialManifestDigests) || len(record.ExternalMaterialManifestDigests) != len(external) || digestExcluding(raw, "recordDigest", "signedObjectEnvelope") != record.RecordDigest || record.SignedObjectEnvelope.ObjectDigest != record.RecordDigest || !validArtifact(record.UnsignedArtifact) {
		return false
	}
	for _, digest := range record.ExternalMaterialManifestDigests {
		if _, ok := external[digest]; !ok {
			return false
		}
	}
	toolchain, ok := external[record.ToolchainMaterialDigest]
	return ok && toolchain.MaterialKind == "go-toolchain"
}

func validAttestation(raw []byte, attestation *MarshalArtifactBuildAttestationV1) bool {
	if attestation.SchemaVersion != BuildAttestationSchema || !sortedUnique(attestation.ExternalMaterialManifestDigests) || digestExcluding(raw, "attestationDigest", "signedObjectEnvelope") != attestation.AttestationDigest || attestation.SignedObjectEnvelope.ObjectDigest != attestation.AttestationDigest || !validArtifact(attestation.UnsignedArtifact) || !validArtifact(attestation.FinalArtifact) {
		return false
	}
	identity, observation := attestation.CodeSignatureIdentity, attestation.CodeSignatureObservation
	if identity.Identifier != MarshalBinaryIdentifier || identity.LeafCertificateSHA256 == nil || identity.CertificateChainSHA256 == nil || observation.ObservedFinalRawSHA256 != attestation.FinalArtifact.RawSHA256 || observation.ObservedFileSize != attestation.FinalArtifact.FileSize || observation.ObserverWorkflowID != attestation.ArtifactAttestationWorkflowIdentity || attestation.CodeSigningWorkflowIdentity == attestation.ArtifactAttestationWorkflowIdentity || attestation.FinalArtifact.RawSHA256 == attestation.UnsignedArtifact.RawSHA256 {
		return false
	}
	if _, ok := canonicalTime(observation.ObservedAt); !ok {
		return false
	}
	switch attestation.BuildProfile {
	case "darwin-managed-development":
		return identity.SignatureKind == "managed-development"
	case "darwin-notarized-release":
		return identity.SignatureKind == "developer-id-application" && identity.TeamIdentifier != nil && identity.HardenedRuntime && identity.SecureTimestamp
	default:
		return false
	}
}

func attestationMatchesChain(c *VerifiedChain, external map[string]ExternalBuildMaterialManifestV1) bool {
	s, r, a := c.SourceManifest, c.BuildRecord, c.BuildAttestation
	if a.BuildRecordDigest != r.RecordDigest || a.SourceManifestDigest != s.ManifestDigest || a.CompileRootManifestDigest != c.CompileRootManifest.ManifestDigest || a.SubmodulePolicyDigest != s.SubmodulePolicyDigest || a.LFSPolicyDigest != s.LFSPolicyDigest ||
		a.Repository != r.Repository || a.SourceHead != r.SourceHead || a.SourceBundleDigest != r.SourceBundleDigest || a.BuildProfile != r.BuildProfile || a.BuildInvocationDigest != r.BuildInvocationDigest || a.EnvironmentPolicyDigest != r.EnvironmentPolicyDigest || a.ToolchainMaterialDigest != r.ToolchainMaterialDigest || a.ModuleGraphDigest != r.ModuleGraphDigest ||
		a.BuilderPrincipalID != r.BuilderPrincipalID || a.BuilderWorkflowIdentity != r.BuilderWorkflowIdentity || a.BuilderIsolationProfile != r.BuilderIsolationProfile || !reflect.DeepEqual(a.UnsignedArtifact, r.UnsignedArtifact) || !reflect.DeepEqual(a.ExternalMaterialManifestDigests, r.ExternalMaterialManifestDigests) || a.GeneratedSourceStageDigest == nil != (c.GeneratedSourceStage == nil) ||
		a.CodeSigningWorkflowIdentity == r.BuilderWorkflowIdentity || a.ArtifactAttestationWorkflowIdentity == r.BuilderWorkflowIdentity {
		return false
	}
	if a.GeneratedSourceStageDigest != nil && *a.GeneratedSourceStageDigest != c.GeneratedSourceStage.StageDigest {
		return false
	}
	createdAt, createdOK := canonicalTime(r.CreatedAt)
	issuedAt, issuedOK := canonicalTime(a.IssuedAt)
	observedAt, observedOK := canonicalTime(a.CodeSignatureObservation.ObservedAt)
	if !createdOK || !issuedOK || !observedOK || observedAt.Before(createdAt) || issuedAt.Before(observedAt) ||
		a.FinalArtifact.GoBuildID != a.UnsignedArtifact.GoBuildID || a.FinalArtifact.OS != a.UnsignedArtifact.OS || a.FinalArtifact.Arch != a.UnsignedArtifact.Arch || a.FinalArtifact.Version != a.UnsignedArtifact.Version || a.FinalArtifact.BuildDate != a.UnsignedArtifact.BuildDate ||
		s.TargetOS != r.UnsignedArtifact.OS || s.TargetArch != r.UnsignedArtifact.Arch {
		return false
	}
	if r.SourceBundleDigest != s.SourceBundleDigest || r.SourceManifestDigest != s.ManifestDigest || r.CompileRootManifestDigest != c.CompileRootManifest.ManifestDigest || r.BuildInvocationDigest != s.BuildInvocationDigest || r.EnvironmentPolicyDigest != s.EnvironmentPolicyDigest || r.ModuleGraphDigest != s.ModuleGraphDigest || r.ToolchainMaterialDigest != s.ToolchainDistributionDigest {
		return false
	}
	if material, ok := external[r.ToolchainMaterialDigest]; !ok || material.MaterialKind != "go-toolchain" {
		return false
	}
	if c.GeneratedSourceStage != nil {
		if material, ok := external[c.GeneratedSourceStage.GeneratorMaterialDigest]; !ok || material.MaterialKind != "generator-tool" {
			return false
		}
		if material, ok := external[c.GeneratedSourceStage.GeneratorToolchainDigest]; !ok || material.MaterialKind != "go-toolchain" {
			return false
		}
	}
	return true
}

func verifyEnvelope(raw []byte, excluded []string, expectedDigest string, envelope authorityprovider.SignedObjectEnvelopeV1, domain, usage, producer string, issuedAt time.Time, policy CurrentKeyPolicy) (KeyRecord, error) {
	if digestExcluding(raw, excluded...) != expectedDigest || envelope.ObjectDigest != expectedDigest || envelope.SignatureAlgorithm != authorityprovider.SignatureAlgorithmEd25519 || envelope.SignatureEncoding != authorityprovider.SignatureEncodingBase64URL || envelope.SignatureDomain != domain || envelope.KeyEpoch > math.MaxInt64 || policy.ProducerPrincipalID != producer || envelope.KeyEpoch != policy.CurrentKeyEpoch {
		return KeyRecord{}, ErrRejected
	}
	var matched *KeyRecord
	for i := range policy.Keys {
		key := &policy.Keys[i]
		if key.KeyID == envelope.KeyID && key.KeyEpoch == envelope.KeyEpoch && key.Usage == usage {
			if matched != nil {
				return KeyRecord{}, ErrRejected
			}
			matched = key
		}
	}
	if matched == nil || len(matched.PublicKey) != ed25519.PublicKeySize || matched.ValidFrom.IsZero() || matched.ValidUntil.IsZero() || issuedAt.Before(matched.ValidFrom) || !issuedAt.Before(matched.ValidUntil) || matched.RevokedAt != nil {
		return KeyRecord{}, ErrRejected
	}
	signature, err := base64.RawURLEncoding.Strict().DecodeString(envelope.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize || base64.RawURLEncoding.EncodeToString(signature) != envelope.Signature || !ed25519.Verify(matched.PublicKey, append([]byte(domain), []byte(expectedDigest)...), signature) {
		return KeyRecord{}, ErrRejected
	}
	copyKey := *matched
	copyKey.PublicKey = append(ed25519.PublicKey(nil), matched.PublicKey...)
	return copyKey, nil
}

func validEntries(entries []ManifestEntryV1) bool {
	previous, folded := "", make(map[string]struct{}, len(entries))
	folder := cases.Fold()
	for _, entry := range entries {
		if !validRelativePath(entry.Path) || entry.Path <= previous {
			return false
		}
		previous = entry.Path
		foldedPath := folder.String(entry.Path)
		if _, collision := folded[foldedPath]; collision {
			return false
		}
		folded[foldedPath] = struct{}{}
		switch entry.EntryType {
		case "regular":
			if entry.SHA256 == nil || entry.SymlinkTarget != nil || entry.SymlinkBoundary != nil {
				return false
			}
		case "directory":
			if entry.Length != 0 || entry.SHA256 != nil || entry.SymlinkTarget != nil || entry.SymlinkBoundary != nil {
				return false
			}
		case "symlink":
			if entry.SHA256 == nil || entry.SymlinkTarget == nil || entry.SymlinkBoundary == nil || *entry.SymlinkBoundary != "within-sealed-root" || !validSymlinkTarget(entry.Path, *entry.SymlinkTarget) || entry.Length != uint64(len([]byte(*entry.SymlinkTarget))) || *entry.SHA256 != canonical.DigestBytes([]byte(*entry.SymlinkTarget)) {
				return false
			}
		default:
			return false
		}
	}
	return len(entries) > 0
}

func validRelativePath(value string) bool {
	return value != "" && utf8.ValidString(value) && norm.NFC.IsNormalString(value) && !strings.Contains(value, "\\") && !strings.ContainsRune(value, '\x00') && !strings.HasPrefix(value, "/") && path.Clean(value) == value && value != "." && value != ".." && !strings.HasPrefix(value, "../")
}

func validSymlinkTarget(linkPath, target string) bool {
	if target == "" || !utf8.ValidString(target) || !norm.NFC.IsNormalString(target) || strings.Contains(target, "\\") || strings.ContainsRune(target, '\x00') || strings.HasPrefix(target, "/") || path.Clean(target) != target {
		return false
	}
	resolved := path.Clean(path.Join(path.Dir(linkPath), target))
	return resolved != ".." && !strings.HasPrefix(resolved, "../") && !strings.HasPrefix(resolved, "/")
}

func validPathObjects[T any](items []T, getPath func(T) string) bool {
	previous := ""
	folded := make(map[string]struct{}, len(items))
	folder := cases.Fold()
	for _, item := range items {
		value := getPath(item)
		if !validRelativePath(value) || value <= previous {
			return false
		}
		previous = value
		key := folder.String(value)
		if _, duplicate := folded[key]; duplicate {
			return false
		}
		folded[key] = struct{}{}
	}
	return true
}

func validArtifact(artifact ArtifactV1) bool {
	_, ok := canonicalTime(artifact.BuildDate)
	return ok && artifact.FileSize > 0 && artifact.OS == "darwin"
}

func validHead(head, format string) bool {
	if format == "sha1" {
		return len(head) == 40
	}
	return format == "sha256" && len(head) == 64
}

func digestOf(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	digest, err := canonical.DigestJSON(raw)
	if err != nil {
		return ""
	}
	return digest
}

func digestExcluding(raw []byte, fields ...string) string {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil {
		return ""
	}
	for _, field := range fields {
		delete(object, field)
	}
	detached, err := json.Marshal(object)
	if err != nil {
		return ""
	}
	digest, err := canonical.DigestJSON(detached)
	if err != nil {
		return ""
	}
	return digest
}

func canonicalTime(value string) (time.Time, bool) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return parsed, err == nil && parsed.UTC().Format(time.RFC3339Nano) == value
}

func sortedUnique(values []string) bool {
	if len(values) == 0 || !sort.StringsAreSorted(values) {
		return false
	}
	for i := 1; i < len(values); i++ {
		if values[i] == values[i-1] {
			return false
		}
	}
	return true
}

func pointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func sameSigningKey(left, right KeyRecord) bool {
	return bytes.Equal(left.PublicKey, right.PublicKey)
}

func validValidationPolicy(policy ValidationPolicy) bool {
	if policy.ExpectedRepository == "" || policy.ExpectedSourceHead == "" || policy.ExpectedBuildProfile == "" ||
		policy.ExpectedSourceBundleDigest == "" || policy.ExpectedSourceManifestDigest == "" || policy.ExpectedCompileRootManifestDigest == "" ||
		policy.ExpectedDependencyMode == "" || policy.ExpectedModuleGraphDigest == "" || policy.ExpectedBuildInvocationDigest == "" ||
		policy.ExpectedEnvironmentPolicyDigest == "" || policy.ExpectedToolchainMaterialDigest == "" || policy.ExpectedTargetArch == "" ||
		policy.ExpectedGoVersion == "" || policy.ExpectedSubmodulePolicyDigest == "" || policy.ExpectedLFSPolicyDigest == "" ||
		policy.ExpectedBuilderPrincipalID == "" || policy.ExpectedBuilderWorkflowIdentity == "" || policy.ExpectedBuilderIsolationProfile == "" ||
		policy.ExpectedArtifactAttestationProducerPrincipalID == "" || policy.ExpectedCodeSigningWorkflowIdentity == "" || policy.ExpectedArtifactAttestationWorkflowIdentity == "" ||
		len(policy.ExpectedExternalMaterials) == 0 || len(policy.ExpectedExternalMaterials) > maxExternalManifests {
		return false
	}
	switch policy.ExpectedDependencyMode {
	case "modules", "vendor", "workspace", "local-replace":
	default:
		return false
	}
	if !validPathObjects(policy.ExpectedSubmodules, func(item SubmoduleV1) string { return item.Path }) || !validPathObjects(policy.ExpectedLFSObjects, func(item LFSObjectV1) string { return item.Path }) {
		return false
	}
	kinds := make(map[string]bool)
	for digest, expectation := range policy.ExpectedExternalMaterials {
		if digest == "" || expectation.MaterialKind == "" || len(expectation.Entries) == 0 {
			return false
		}
		for key, references := range expectation.Entries {
			if key == "" || !sortedUnique(references) {
				return false
			}
		}
		kinds[expectation.MaterialKind] = true
	}
	if !materialKindsMatchMode(policy.ExpectedDependencyMode, kinds) {
		return false
	}
	if policy.Trust.BuildRecord.ProducerPrincipalID != policy.ExpectedBuilderPrincipalID || policy.Trust.BuildAttestation.ProducerPrincipalID != policy.ExpectedArtifactAttestationProducerPrincipalID ||
		policy.ExpectedBuilderPrincipalID == policy.ExpectedArtifactAttestationProducerPrincipalID || policy.ExpectedBuilderWorkflowIdentity == policy.ExpectedCodeSigningWorkflowIdentity || policy.ExpectedBuilderWorkflowIdentity == policy.ExpectedArtifactAttestationWorkflowIdentity || policy.ExpectedCodeSigningWorkflowIdentity == policy.ExpectedArtifactAttestationWorkflowIdentity ||
		!validExpectedCodeSignatureIdentity(policy.ExpectedCodeSignatureIdentity, policy.ExpectedBuildProfile) {
		return false
	}
	toolchain, ok := policy.ExpectedExternalMaterials[policy.ExpectedToolchainMaterialDigest]
	if !ok || toolchain.MaterialKind != "go-toolchain" {
		return false
	}
	if policy.ExpectedGenerated {
		if policy.ExpectedGeneratedStageDigest == "" || policy.ExpectedGeneratorInvocationDigest == "" || policy.ExpectedGeneratorInputDigest == "" || policy.ExpectedGeneratorMaterialDigest == "" || policy.ExpectedGeneratorToolchainDigest == "" {
			return false
		}
		generator, generatorOK := policy.ExpectedExternalMaterials[policy.ExpectedGeneratorMaterialDigest]
		generatorToolchain, toolchainOK := policy.ExpectedExternalMaterials[policy.ExpectedGeneratorToolchainDigest]
		return generatorOK && generator.MaterialKind == "generator-tool" && toolchainOK && generatorToolchain.MaterialKind == "go-toolchain"
	}
	return policy.ExpectedGeneratedStageDigest == "" && policy.ExpectedGeneratorInvocationDigest == "" && policy.ExpectedGeneratorInputDigest == "" && policy.ExpectedGeneratorMaterialDigest == "" && policy.ExpectedGeneratorToolchainDigest == ""
}

func authorityFactsMatchPolicy(c *VerifiedChain, policy ValidationPolicy) bool {
	r, a := c.BuildRecord, c.BuildAttestation
	return r.BuilderPrincipalID == policy.ExpectedBuilderPrincipalID && r.BuilderWorkflowIdentity == policy.ExpectedBuilderWorkflowIdentity && r.BuilderIsolationProfile == policy.ExpectedBuilderIsolationProfile &&
		a.BuilderPrincipalID == policy.ExpectedBuilderPrincipalID && a.BuilderWorkflowIdentity == policy.ExpectedBuilderWorkflowIdentity && a.BuilderIsolationProfile == policy.ExpectedBuilderIsolationProfile &&
		a.ArtifactAttestationProducerPrincipalID == policy.ExpectedArtifactAttestationProducerPrincipalID && a.CodeSigningWorkflowIdentity == policy.ExpectedCodeSigningWorkflowIdentity && a.ArtifactAttestationWorkflowIdentity == policy.ExpectedArtifactAttestationWorkflowIdentity &&
		reflect.DeepEqual(a.CodeSignatureIdentity, policy.ExpectedCodeSignatureIdentity)
}

func validExpectedCodeSignatureIdentity(identity CodeSignatureIdentityV1, profile string) bool {
	if identity.Identifier != MarshalBinaryIdentifier || !validLowerHex(identity.CDHash, 40, 64) || identity.DesignatedRequirement == "" || identity.LeafCertificateSHA256 == nil || identity.CertificateChainSHA256 == nil || !validDigestLiteral(*identity.LeafCertificateSHA256) || !validDigestLiteral(*identity.CertificateChainSHA256) || (identity.TeamIdentifier != nil && *identity.TeamIdentifier == "") {
		return false
	}
	switch profile {
	case "darwin-managed-development":
		return identity.SignatureKind == "managed-development"
	case "darwin-notarized-release":
		return identity.SignatureKind == "developer-id-application" && identity.TeamIdentifier != nil && identity.HardenedRuntime && identity.SecureTimestamp
	default:
		return false
	}
}

func validLowerHex(value string, lengths ...int) bool {
	validLength := false
	for _, length := range lengths {
		validLength = validLength || len(value) == length
	}
	if !validLength {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func validDigestLiteral(value string) bool {
	return strings.HasPrefix(value, "sha256:") && validLowerHex(strings.TrimPrefix(value, "sha256:"), 64)
}

type policyBudgetLimits struct {
	Submodules, LFSObjects, ExternalManifests, EntriesPerManifest uint64
	AggregateEntries, ReferencesPerEntry, AggregateReferences     uint64
	KeysPerAuthority, PublicKeyBytes, AggregatePublicKeyBytes     uint64
	AggregateBytes                                                uint64
}

type policyBudgetUsage struct {
	Entries, References, PublicKeyBytes, Bytes uint64
}

var productionPolicyBudgetLimits = policyBudgetLimits{
	Submodules: 1024, LFSObjects: 16384, ExternalManifests: maxExternalManifests, EntriesPerManifest: 16384,
	AggregateEntries: maxPolicyEntries, ReferencesPerEntry: 1024, AggregateReferences: maxPolicyReferences,
	KeysPerAuthority: maxCurrentKeys, PublicKeyBytes: maxPolicyKeyBytes, AggregatePublicKeyBytes: maxPolicyAllKeyBytes,
	AggregateBytes: maxPolicyBytes,
}

func withinPolicyBudgets(policy ValidationPolicy) bool {
	return withinPolicyBudgetsWithLimits(policy, productionPolicyBudgetLimits)
}

func withinPolicyBudgetsWithLimits(policy ValidationPolicy, limits policyBudgetLimits) bool {
	if uint64(len(policy.ExpectedSubmodules)) > limits.Submodules || uint64(len(policy.ExpectedLFSObjects)) > limits.LFSObjects || len(policy.ExpectedExternalMaterials) == 0 || uint64(len(policy.ExpectedExternalMaterials)) > limits.ExternalManifests ||
		uint64(len(policy.Trust.BuildRecord.Keys)) > limits.KeysPerAuthority || uint64(len(policy.Trust.BuildAttestation.Keys)) > limits.KeysPerAuthority {
		return false
	}
	usage, ok := measurePolicyUsage(policy, limits)
	return ok && usage.Entries <= limits.AggregateEntries && usage.References <= limits.AggregateReferences && usage.PublicKeyBytes <= limits.AggregatePublicKeyBytes && usage.Bytes <= limits.AggregateBytes
}

func measurePolicyUsage(policy ValidationPolicy, limits policyBudgetLimits) (policyBudgetUsage, bool) {
	var usage policyBudgetUsage
	addString := func(value string) bool { return addBounded(&usage.Bytes, uint64(len(value)), limits.AggregateBytes) }
	topLevel := []string{
		policy.ExpectedRepository, policy.ExpectedSourceHead, policy.ExpectedBuildProfile, policy.ExpectedSourceBundleDigest,
		policy.ExpectedSourceManifestDigest, policy.ExpectedCompileRootManifestDigest, policy.ExpectedBuildInvocationDigest,
		policy.ExpectedEnvironmentPolicyDigest, policy.ExpectedToolchainMaterialDigest, policy.ExpectedModuleGraphDigest,
		policy.ExpectedTargetArch, policy.ExpectedGoVersion, policy.ExpectedSubmodulePolicyDigest, policy.ExpectedLFSPolicyDigest,
		policy.ExpectedDependencyMode, policy.ExpectedGeneratedStageDigest, policy.ExpectedGeneratorInvocationDigest,
		policy.ExpectedGeneratorInputDigest, policy.ExpectedGeneratorMaterialDigest, policy.ExpectedGeneratorToolchainDigest,
		policy.ExpectedBuilderPrincipalID, policy.ExpectedBuilderWorkflowIdentity, policy.ExpectedBuilderIsolationProfile,
		policy.ExpectedArtifactAttestationProducerPrincipalID, policy.ExpectedCodeSigningWorkflowIdentity,
		policy.ExpectedArtifactAttestationWorkflowIdentity,
	}
	for _, value := range topLevel {
		if !addString(value) {
			return policyBudgetUsage{}, false
		}
	}
	for _, pointer := range []*string{policy.ExpectedGoModDigest, policy.ExpectedGoSumDigest, policy.ExpectedCodeSignatureIdentity.TeamIdentifier, policy.ExpectedCodeSignatureIdentity.LeafCertificateSHA256, policy.ExpectedCodeSignatureIdentity.CertificateChainSHA256} {
		if pointer != nil && !addString(*pointer) {
			return policyBudgetUsage{}, false
		}
	}
	for _, value := range []string{policy.ExpectedCodeSignatureIdentity.SignatureKind, policy.ExpectedCodeSignatureIdentity.Identifier, policy.ExpectedCodeSignatureIdentity.CDHash, policy.ExpectedCodeSignatureIdentity.DesignatedRequirement} {
		if !addString(value) {
			return policyBudgetUsage{}, false
		}
	}
	for _, item := range policy.ExpectedSubmodules {
		for _, value := range []string{item.Path, item.PinnedCommit, item.MaterializedTreeDigest} {
			if !addString(value) {
				return policyBudgetUsage{}, false
			}
		}
	}
	for _, item := range policy.ExpectedLFSObjects {
		for _, value := range []string{item.Path, item.PointerDigest, item.MaterializedObjectDigest} {
			if !addString(value) {
				return policyBudgetUsage{}, false
			}
		}
	}
	for digest, expectation := range policy.ExpectedExternalMaterials {
		if len(expectation.Entries) == 0 || uint64(len(expectation.Entries)) > limits.EntriesPerManifest || !addBounded(&usage.Entries, uint64(len(expectation.Entries)), limits.AggregateEntries) || !addString(digest) || !addString(expectation.MaterialKind) {
			return policyBudgetUsage{}, false
		}
		for key, references := range expectation.Entries {
			if len(references) == 0 || uint64(len(references)) > limits.ReferencesPerEntry || !addBounded(&usage.References, uint64(len(references)), limits.AggregateReferences) || !addString(key) {
				return policyBudgetUsage{}, false
			}
			for _, reference := range references {
				if !addString(reference) {
					return policyBudgetUsage{}, false
				}
			}
		}
	}
	for _, current := range []CurrentKeyPolicy{policy.Trust.BuildRecord, policy.Trust.BuildAttestation} {
		if !addString(current.ProducerPrincipalID) {
			return policyBudgetUsage{}, false
		}
		for _, key := range current.Keys {
			if uint64(len(key.PublicKey)) > limits.PublicKeyBytes || !addBounded(&usage.PublicKeyBytes, uint64(len(key.PublicKey)), limits.AggregatePublicKeyBytes) || !addBounded(&usage.Bytes, uint64(len(key.PublicKey)), limits.AggregateBytes) || !addString(key.KeyID) || !addString(key.Usage) {
				return policyBudgetUsage{}, false
			}
		}
	}
	return usage, true
}

func addBounded(total *uint64, value, limit uint64) bool {
	if value > limit || *total > limit-value {
		return false
	}
	*total += value
	return true
}

func snapshotRaw(raw RawObjectSet) RawObjectSet {
	result := RawObjectSet{
		SourceManifest:       append([]byte(nil), raw.SourceManifest...),
		CompileRootManifest:  append([]byte(nil), raw.CompileRootManifest...),
		GeneratedSourceStage: append([]byte(nil), raw.GeneratedSourceStage...),
		BuildRecord:          append([]byte(nil), raw.BuildRecord...),
		BuildAttestation:     append([]byte(nil), raw.BuildAttestation...),
	}
	result.ExternalMaterialManifests = make([][]byte, len(raw.ExternalMaterialManifests))
	for i := range raw.ExternalMaterialManifests {
		result.ExternalMaterialManifests[i] = append([]byte(nil), raw.ExternalMaterialManifests[i]...)
	}
	return result
}

func snapshotValidationPolicy(policy ValidationPolicy) ValidationPolicy {
	result := policy
	result.ExpectedGoModDigest = copyStringPointer(policy.ExpectedGoModDigest)
	result.ExpectedGoSumDigest = copyStringPointer(policy.ExpectedGoSumDigest)
	result.ExpectedCodeSignatureIdentity = snapshotCodeSignatureIdentity(policy.ExpectedCodeSignatureIdentity)
	result.ExpectedSubmodules = append([]SubmoduleV1{}, policy.ExpectedSubmodules...)
	result.ExpectedLFSObjects = append([]LFSObjectV1{}, policy.ExpectedLFSObjects...)
	result.ExpectedExternalMaterials = make(map[string]ExternalMaterialExpectation, len(policy.ExpectedExternalMaterials))
	for digest, expectation := range policy.ExpectedExternalMaterials {
		entries := make(map[string][]string, len(expectation.Entries))
		for key, references := range expectation.Entries {
			entries[key] = append([]string(nil), references...)
		}
		result.ExpectedExternalMaterials[digest] = ExternalMaterialExpectation{MaterialKind: expectation.MaterialKind, Entries: entries}
	}
	result.Trust.BuildRecord = snapshotCurrentKeyPolicy(policy.Trust.BuildRecord)
	result.Trust.BuildAttestation = snapshotCurrentKeyPolicy(policy.Trust.BuildAttestation)
	return result
}

func snapshotCodeSignatureIdentity(identity CodeSignatureIdentityV1) CodeSignatureIdentityV1 {
	result := identity
	result.TeamIdentifier = copyStringPointer(identity.TeamIdentifier)
	result.LeafCertificateSHA256 = copyStringPointer(identity.LeafCertificateSHA256)
	result.CertificateChainSHA256 = copyStringPointer(identity.CertificateChainSHA256)
	return result
}

func snapshotCurrentKeyPolicy(policy CurrentKeyPolicy) CurrentKeyPolicy {
	result := policy
	result.Keys = make([]KeyRecord, len(policy.Keys))
	for i, key := range policy.Keys {
		result.Keys[i] = key
		result.Keys[i].PublicKey = append(ed25519.PublicKey(nil), key.PublicKey...)
		if key.RevokedAt != nil {
			revokedAt := *key.RevokedAt
			result.Keys[i].RevokedAt = &revokedAt
		}
	}
	return result
}

func copyStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func materialKindsMatchMode(mode string, kinds map[string]bool) bool {
	switch mode {
	case "modules":
		return !kinds["vendor-tree"] && !kinds["workspace-module"] && !kinds["local-replace"]
	case "vendor":
		return kinds["vendor-tree"] && !kinds["go-module-source"] && !kinds["workspace-module"] && !kinds["local-replace"]
	case "workspace":
		return kinds["workspace-module"] && !kinds["vendor-tree"] && !kinds["local-replace"]
	case "local-replace":
		return kinds["local-replace"] && !kinds["vendor-tree"] && !kinds["workspace-module"]
	default:
		return false
	}
}

func sourceMaterialClosure(source SourceManifestV1, policy ValidationPolicy) bool {
	// The materializer is responsible for observing submodule trees and LFS
	// objects. ADR 0048 does not define a subtree digest algorithm here, so this
	// consumer enforces the caller's exact observations without inventing a new
	// persisted contract.
	entryByPath := make(map[string]ManifestEntryV1, len(source.Entries))
	for _, entry := range source.Entries {
		entryByPath[entry.Path] = entry
	}
	for _, entry := range source.Entries {
		ancestor := path.Dir(entry.Path)
		for ancestor != "." {
			if parent, exists := entryByPath[ancestor]; exists && parent.EntryType != "directory" {
				return false
			}
			ancestor = path.Dir(ancestor)
		}
	}
	for _, object := range policy.ExpectedLFSObjects {
		entry, ok := entryByPath[object.Path]
		if !ok || entry.EntryType != "regular" || entry.SHA256 == nil || *entry.SHA256 != object.MaterializedObjectDigest {
			return false
		}
	}
	for _, submodule := range policy.ExpectedSubmodules {
		root, found := entryByPath[submodule.Path]
		if !found || root.EntryType != "directory" {
			return false
		}
	}
	return true
}

func externalMaterialsMatchPolicy(materials []ExternalBuildMaterialManifestV1, byDigest map[string]ExternalBuildMaterialManifestV1, policy ValidationPolicy) bool {
	if len(materials) != len(policy.ExpectedExternalMaterials) || len(byDigest) != len(policy.ExpectedExternalMaterials) {
		return false
	}
	for _, material := range materials {
		expected, ok := policy.ExpectedExternalMaterials[material.ManifestDigest]
		if !ok || material.MaterialKind != expected.MaterialKind || material.PolicyDigest != policy.ExpectedEnvironmentPolicyDigest || len(material.Entries) != len(expected.Entries) {
			return false
		}
		for _, entry := range material.Entries {
			references, ok := expected.Entries[externalEntryKey(entry)]
			if !ok || !reflect.DeepEqual(entry.ReferencedBy, references) {
				return false
			}
		}
	}
	return true
}

func externalEntryKey(entry ExternalMaterialEntryV1) string {
	return entry.LogicalIdentity + "\x00" + entry.Path
}

func withinBudgets(raw RawObjectSet) bool {
	if len(raw.ExternalMaterialManifests) == 0 || len(raw.ExternalMaterialManifests) > maxExternalManifests {
		return false
	}
	objects := [][]byte{raw.SourceManifest, raw.CompileRootManifest, raw.GeneratedSourceStage, raw.BuildRecord, raw.BuildAttestation}
	objects = append(objects, raw.ExternalMaterialManifests...)
	total := uint64(0)
	for _, object := range objects {
		if len(object) > maxObjectBytes {
			return false
		}
		next := total + uint64(len(object))
		if next < total || next > maxChainBytes {
			return false
		}
		total = next
	}
	return true
}
