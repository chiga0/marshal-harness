// Package artifactattestation validates the immutable protected-build object
// chain frozen by ADR 0048. It is a consumer-only package: callers provide
// canonical object bytes and externally provisioned current public-key policy.
package artifactattestation

import (
	"crypto/ed25519"
	"errors"
	"time"

	"github.com/chiga0/marshal-harness/internal/authorityprovider"
)

const (
	SourceManifestSchema       = "marshal.source-manifest.v1"
	CompileRootManifestSchema  = "marshal.compile-root-manifest.v1"
	GeneratedSourceStageSchema = "marshal.generated-source-stage.v1"
	ExternalMaterialSchema     = "marshal.external-build-material-manifest.v1"
	BuildRecordSchema          = "marshal.artifact-build-record.v1"
	BuildAttestationSchema     = "marshal.artifact-build-attestation.v1"

	BuildRecordDomain      = "marshal-artifact-build-record-v1\x00"
	BuildRecordKeyUsage    = "marshal-artifact-build-record"
	BuildAttestationDomain = "marshal-artifact-build-attestation-v1\x00"
	BuildAttestationUsage  = "marshal-artifact-build-attestation"

	MarshalBinaryIdentifier = "com.github.chiga0.marshal"
	maxObjectBytes          = 8 << 20
	maxChainBytes           = 64 << 20
	maxExternalManifests    = 1024
	maxPolicyBytes          = 64 << 20
	maxCurrentKeys          = 32
	maxPolicyKeyBytes       = 4096
	maxPolicyAllKeyBytes    = 64 << 10
	maxPolicyEntries        = 1 << 18
	maxPolicyReferences     = 1 << 20
)

var ErrRejected = errors.New("artifact attestation: rejected")

type ManifestEntryV1 struct {
	Path            string  `json:"path"`
	EntryType       string  `json:"entryType"`
	Mode            uint32  `json:"mode"`
	Length          uint64  `json:"length"`
	SHA256          *string `json:"sha256"`
	SymlinkTarget   *string `json:"symlinkTarget"`
	SymlinkBoundary *string `json:"symlinkBoundary"`
}

type GeneratorPolicyV1 struct {
	GeneratedStageID              string `json:"generatedStageId"`
	GeneratorMaterialDigest       string `json:"generatorMaterialDigest"`
	GeneratorToolchainDigest      string `json:"generatorToolchainDigest"`
	GeneratorInvocationDigest     string `json:"generatorInvocationDigest"`
	GeneratorInputDigest          string `json:"generatorInputDigest"`
	GeneratorOutputManifestDigest string `json:"generatorOutputManifestDigest"`
}

type SubmoduleV1 struct {
	Path                   string `json:"path"`
	PinnedCommit           string `json:"pinnedCommit"`
	MaterializedTreeDigest string `json:"materializedTreeDigest"`
}

type LFSObjectV1 struct {
	Path                     string `json:"path"`
	PointerDigest            string `json:"pointerDigest"`
	MaterializedObjectDigest string `json:"materializedObjectDigest"`
}

type SourceManifestV1 struct {
	SchemaVersion               string             `json:"schemaVersion"`
	ManifestID                  string             `json:"manifestId"`
	Repository                  string             `json:"repository"`
	SourceHead                  string             `json:"sourceHead"`
	GitObjectFormat             string             `json:"gitObjectFormat"`
	SourceBundleDigest          string             `json:"sourceBundleDigest"`
	Entries                     []ManifestEntryV1  `json:"entries"`
	RootDigest                  string             `json:"rootDigest"`
	SubmodulePolicyDigest       string             `json:"submodulePolicyDigest"`
	Submodules                  []SubmoduleV1      `json:"submodules"`
	LFSPolicyDigest             string             `json:"lfsPolicyDigest"`
	LFSObjects                  []LFSObjectV1      `json:"lfsObjects"`
	GeneratedSourcePolicy       *GeneratorPolicyV1 `json:"generatedSourcePolicy"`
	GoModDigest                 *string            `json:"goModDigest"`
	GoSumDigest                 *string            `json:"goSumDigest"`
	DependencyMode              string             `json:"dependencyMode"`
	ModuleGraphDigest           string             `json:"moduleGraphDigest"`
	BuildInvocationDigest       string             `json:"buildInvocationDigest"`
	EnvironmentPolicyDigest     string             `json:"environmentPolicyDigest"`
	ToolchainDistributionDigest string             `json:"toolchainDistributionDigest"`
	GoVersion                   string             `json:"goVersion"`
	TargetOS                    string             `json:"targetOS"`
	TargetArch                  string             `json:"targetArch"`
	BuildProfile                string             `json:"buildProfile"`
	ProducerObservationIdentity string             `json:"producerObservationIdentity"`
	ManifestDigest              string             `json:"manifestDigest"`
}

type CompileRootManifestV1 struct {
	SchemaVersion               string            `json:"schemaVersion"`
	ManifestID                  string            `json:"manifestId"`
	Repository                  string            `json:"repository"`
	SourceHead                  string            `json:"sourceHead"`
	SourceManifestDigest        string            `json:"sourceManifestDigest"`
	GeneratedSourceStageDigest  *string           `json:"generatedSourceStageDigest"`
	Entries                     []ManifestEntryV1 `json:"entries"`
	RootDigest                  string            `json:"rootDigest"`
	ProducerObservationIdentity string            `json:"producerObservationIdentity"`
	ManifestDigest              string            `json:"manifestDigest"`
}

type GeneratedSourceStageV1 struct {
	SchemaVersion               string            `json:"schemaVersion"`
	StageID                     string            `json:"stageId"`
	SourceManifestDigest        string            `json:"sourceManifestDigest"`
	GeneratorMaterialDigest     string            `json:"generatorMaterialDigest"`
	GeneratorToolchainDigest    string            `json:"generatorToolchainDigest"`
	GeneratorInvocationDigest   string            `json:"generatorInvocationDigest"`
	GeneratorInputDigest        string            `json:"generatorInputDigest"`
	Entries                     []ManifestEntryV1 `json:"entries"`
	RootDigest                  string            `json:"rootDigest"`
	ProducerObservationIdentity string            `json:"producerObservationIdentity"`
	StageDigest                 string            `json:"stageDigest"`
}

type ExternalMaterialEntryV1 struct {
	LogicalIdentity string   `json:"logicalIdentity"`
	Path            string   `json:"path"`
	EntryType       string   `json:"entryType"`
	Mode            uint32   `json:"mode"`
	Length          uint64   `json:"length"`
	SHA256          *string  `json:"sha256"`
	SourceIdentity  string   `json:"sourceIdentity"`
	ReferencedBy    []string `json:"referencedBy"`
}

type ExternalBuildMaterialManifestV1 struct {
	SchemaVersion               string                    `json:"schemaVersion"`
	MaterialSetID               string                    `json:"materialSetId"`
	MaterialKind                string                    `json:"materialKind"`
	ProducerObservationIdentity string                    `json:"producerObservationIdentity"`
	PolicyDigest                string                    `json:"policyDigest"`
	Entries                     []ExternalMaterialEntryV1 `json:"entries"`
	ManifestDigest              string                    `json:"manifestDigest"`
}

type ArtifactV1 struct {
	RawSHA256 string `json:"rawSHA256"`
	FileSize  uint64 `json:"fileSize"`
	GoBuildID string `json:"goBuildId"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	Version   string `json:"version"`
	BuildDate string `json:"buildDate"`
}

type MarshalArtifactBuildRecordV1 struct {
	SchemaVersion                   string                                   `json:"schemaVersion"`
	RecordID                        string                                   `json:"recordId"`
	CreatedAt                       string                                   `json:"createdAt"`
	BuildProfile                    string                                   `json:"buildProfile"`
	Repository                      string                                   `json:"repository"`
	SourceHead                      string                                   `json:"sourceHead"`
	SourceBundleDigest              string                                   `json:"sourceBundleDigest"`
	SourceManifestDigest            string                                   `json:"sourceManifestDigest"`
	CompileRootManifestDigest       string                                   `json:"compileRootManifestDigest"`
	ExternalMaterialManifestDigests []string                                 `json:"externalMaterialManifestDigests"`
	BuildInvocationDigest           string                                   `json:"buildInvocationDigest"`
	EnvironmentPolicyDigest         string                                   `json:"environmentPolicyDigest"`
	ToolchainMaterialDigest         string                                   `json:"toolchainMaterialDigest"`
	ModuleGraphDigest               string                                   `json:"moduleGraphDigest"`
	BuilderPrincipalID              string                                   `json:"builderPrincipalId"`
	BuilderWorkflowIdentity         string                                   `json:"builderWorkflowIdentity"`
	BuilderIsolationProfile         string                                   `json:"builderIsolationProfile"`
	UnsignedArtifact                ArtifactV1                               `json:"unsignedArtifact"`
	RecordDigest                    string                                   `json:"recordDigest"`
	SignedObjectEnvelope            authorityprovider.SignedObjectEnvelopeV1 `json:"signedObjectEnvelope"`
}

type CodeSignatureIdentityV1 struct {
	SignatureKind          string  `json:"signatureKind"`
	Identifier             string  `json:"identifier"`
	TeamIdentifier         *string `json:"teamIdentifier"`
	CDHash                 string  `json:"cdHash"`
	DesignatedRequirement  string  `json:"designatedRequirement"`
	LeafCertificateSHA256  *string `json:"leafCertificateSHA256"`
	CertificateChainSHA256 *string `json:"certificateChainSHA256"`
	HardenedRuntime        bool    `json:"hardenedRuntime"`
	SecureTimestamp        bool    `json:"secureTimestamp"`
}

type CodeSignatureObservationV1 struct {
	ObservedFinalRawSHA256 string `json:"observedFinalRawSHA256"`
	ObservedFileSize       uint64 `json:"observedFileSize"`
	ObservedAt             string `json:"observedAt"`
	ObserverWorkflowID     string `json:"observerWorkflowIdentity"`
}

type MarshalArtifactBuildAttestationV1 struct {
	SchemaVersion                          string                                   `json:"schemaVersion"`
	AttestationID                          string                                   `json:"attestationId"`
	IssuedAt                               string                                   `json:"issuedAt"`
	BuildProfile                           string                                   `json:"buildProfile"`
	Repository                             string                                   `json:"repository"`
	SourceHead                             string                                   `json:"sourceHead"`
	SourceBundleDigest                     string                                   `json:"sourceBundleDigest"`
	SourceManifestDigest                   string                                   `json:"sourceManifestDigest"`
	CompileRootManifestDigest              string                                   `json:"compileRootManifestDigest"`
	BuildRecordDigest                      string                                   `json:"buildRecordDigest"`
	SubmodulePolicyDigest                  string                                   `json:"submodulePolicyDigest"`
	LFSPolicyDigest                        string                                   `json:"lfsPolicyDigest"`
	GeneratedSourceStageDigest             *string                                  `json:"generatedSourceStageDigest"`
	BuildInvocationDigest                  string                                   `json:"buildInvocationDigest"`
	EnvironmentPolicyDigest                string                                   `json:"environmentPolicyDigest"`
	ExternalMaterialManifestDigests        []string                                 `json:"externalMaterialManifestDigests"`
	ToolchainMaterialDigest                string                                   `json:"toolchainMaterialDigest"`
	ModuleGraphDigest                      string                                   `json:"moduleGraphDigest"`
	BuilderPrincipalID                     string                                   `json:"builderPrincipalId"`
	BuilderWorkflowIdentity                string                                   `json:"builderWorkflowIdentity"`
	BuilderIsolationProfile                string                                   `json:"builderIsolationProfile"`
	ArtifactAttestationProducerPrincipalID string                                   `json:"artifactAttestationProducerPrincipalId"`
	CodeSigningWorkflowIdentity            string                                   `json:"codeSigningWorkflowIdentity"`
	ArtifactAttestationWorkflowIdentity    string                                   `json:"artifactAttestationWorkflowIdentity"`
	UnsignedArtifact                       ArtifactV1                               `json:"unsignedArtifact"`
	FinalArtifact                          ArtifactV1                               `json:"finalArtifact"`
	CodeSignatureIdentity                  CodeSignatureIdentityV1                  `json:"codeSignatureIdentity"`
	CodeSignatureObservation               CodeSignatureObservationV1               `json:"codeSignatureObservation"`
	AttestationDigest                      string                                   `json:"attestationDigest"`
	SignedObjectEnvelope                   authorityprovider.SignedObjectEnvelopeV1 `json:"signedObjectEnvelope"`
}

// RawObjectSet contains the complete content-addressed object chain a
// protected artifact consumer must resolve before it can accept an
// attestation. The validator never resolves mutable pathnames.
type RawObjectSet struct {
	SourceManifest            []byte   `json:"sourceManifest"`
	CompileRootManifest       []byte   `json:"compileRootManifest"`
	GeneratedSourceStage      []byte   `json:"generatedSourceStage"`
	ExternalMaterialManifests [][]byte `json:"externalMaterialManifests"`
	BuildRecord               []byte   `json:"buildRecord"`
	BuildAttestation          []byte   `json:"buildAttestation"`
}

// RawBuildRecordSet is the pre-sign portion of RawObjectSet. It deliberately
// cannot carry a build attestation: the artifact signer must validate the
// immutable build-record chain before a final artifact or attestation exists.
type RawBuildRecordSet struct {
	SourceManifest            []byte   `json:"sourceManifest"`
	CompileRootManifest       []byte   `json:"compileRootManifest"`
	GeneratedSourceStage      []byte   `json:"generatedSourceStage"`
	ExternalMaterialManifests [][]byte `json:"externalMaterialManifests"`
	BuildRecord               []byte   `json:"buildRecord"`
}

// KeyRecord is externally provisioned verification material. It is deliberately
// separate from the candidate object so an object cannot select its own trust
// root, current epoch, usage, validity, or revocation state.
type KeyRecord struct {
	KeyID      string            `json:"keyId"`
	KeyEpoch   uint64            `json:"keyEpoch"`
	Usage      string            `json:"usage"`
	PublicKey  ed25519.PublicKey `json:"publicKey"`
	ValidFrom  time.Time         `json:"validFrom"`
	ValidUntil time.Time         `json:"validUntil"`
	RevokedAt  *time.Time        `json:"revokedAt"`
}

// CurrentKeyPolicy is the authenticated current projection supplied by the
// builder or artifact authority's external key service.
type CurrentKeyPolicy struct {
	ProducerPrincipalID string      `json:"producerPrincipalId"`
	CurrentKeyEpoch     uint64      `json:"currentKeyEpoch"`
	Keys                []KeyRecord `json:"keys"`
}

type TrustPolicies struct {
	BuildRecord      CurrentKeyPolicy `json:"buildRecord"`
	BuildAttestation CurrentKeyPolicy `json:"buildAttestation"`
}

// ValidationPolicy is resolved by the release consumer, never by the
// candidate object. Exact repository/head/profile matching prevents a valid
// signature from being replayed into a different release decision.
type ValidationPolicy struct {
	ExpectedRepository                             string                                 `json:"expectedRepository"`
	ExpectedSourceHead                             string                                 `json:"expectedSourceHead"`
	ExpectedBuildProfile                           string                                 `json:"expectedBuildProfile"`
	ExpectedSourceBundleDigest                     string                                 `json:"expectedSourceBundleDigest"`
	ExpectedSourceManifestDigest                   string                                 `json:"expectedSourceManifestDigest"`
	ExpectedCompileRootManifestDigest              string                                 `json:"expectedCompileRootManifestDigest"`
	ExpectedGoModDigest                            *string                                `json:"expectedGoModDigest"`
	ExpectedGoSumDigest                            *string                                `json:"expectedGoSumDigest"`
	ExpectedBuildInvocationDigest                  string                                 `json:"expectedBuildInvocationDigest"`
	ExpectedEnvironmentPolicyDigest                string                                 `json:"expectedEnvironmentPolicyDigest"`
	ExpectedToolchainMaterialDigest                string                                 `json:"expectedToolchainMaterialDigest"`
	ExpectedModuleGraphDigest                      string                                 `json:"expectedModuleGraphDigest"`
	ExpectedTargetArch                             string                                 `json:"expectedTargetArch"`
	ExpectedGoVersion                              string                                 `json:"expectedGoVersion"`
	ExpectedSubmodulePolicyDigest                  string                                 `json:"expectedSubmodulePolicyDigest"`
	ExpectedLFSPolicyDigest                        string                                 `json:"expectedLFSPolicyDigest"`
	ExpectedDependencyMode                         string                                 `json:"expectedDependencyMode"`
	ExpectedSubmodules                             []SubmoduleV1                          `json:"expectedSubmodules"`
	ExpectedLFSObjects                             []LFSObjectV1                          `json:"expectedLFSObjects"`
	ExpectedExternalMaterials                      map[string]ExternalMaterialExpectation `json:"expectedExternalMaterials"`
	ExpectedGenerated                              bool                                   `json:"expectedGenerated"`
	ExpectedGeneratedStageDigest                   string                                 `json:"expectedGeneratedStageDigest"`
	ExpectedGeneratorInvocationDigest              string                                 `json:"expectedGeneratorInvocationDigest"`
	ExpectedGeneratorInputDigest                   string                                 `json:"expectedGeneratorInputDigest"`
	ExpectedGeneratorMaterialDigest                string                                 `json:"expectedGeneratorMaterialDigest"`
	ExpectedGeneratorToolchainDigest               string                                 `json:"expectedGeneratorToolchainDigest"`
	ExpectedBuilderPrincipalID                     string                                 `json:"expectedBuilderPrincipalId"`
	ExpectedBuilderWorkflowIdentity                string                                 `json:"expectedBuilderWorkflowIdentity"`
	ExpectedBuilderIsolationProfile                string                                 `json:"expectedBuilderIsolationProfile"`
	ExpectedArtifactAttestationProducerPrincipalID string                                 `json:"expectedArtifactAttestationProducerPrincipalId"`
	ExpectedCodeSigningWorkflowIdentity            string                                 `json:"expectedCodeSigningWorkflowIdentity"`
	ExpectedArtifactAttestationWorkflowIdentity    string                                 `json:"expectedArtifactAttestationWorkflowIdentity"`
	ExpectedCodeSignatureIdentity                  CodeSignatureIdentityV1                `json:"expectedCodeSignatureIdentity"`
	Trust                                          TrustPolicies                          `json:"trust"`
}

// BuildRecordValidationPolicy is trusted caller input for the pre-sign
// boundary. It contains only facts that must already exist before code signing;
// future artifact-attestation producers and code-signature observations cannot
// be selected or anticipated by the candidate build record.
type BuildRecordValidationPolicy struct {
	ExpectedRepository                string                                 `json:"expectedRepository"`
	ExpectedSourceHead                string                                 `json:"expectedSourceHead"`
	ExpectedBuildProfile              string                                 `json:"expectedBuildProfile"`
	ExpectedSourceBundleDigest        string                                 `json:"expectedSourceBundleDigest"`
	ExpectedSourceManifestDigest      string                                 `json:"expectedSourceManifestDigest"`
	ExpectedCompileRootManifestDigest string                                 `json:"expectedCompileRootManifestDigest"`
	ExpectedGoModDigest               *string                                `json:"expectedGoModDigest"`
	ExpectedGoSumDigest               *string                                `json:"expectedGoSumDigest"`
	ExpectedBuildInvocationDigest     string                                 `json:"expectedBuildInvocationDigest"`
	ExpectedEnvironmentPolicyDigest   string                                 `json:"expectedEnvironmentPolicyDigest"`
	ExpectedToolchainMaterialDigest   string                                 `json:"expectedToolchainMaterialDigest"`
	ExpectedModuleGraphDigest         string                                 `json:"expectedModuleGraphDigest"`
	ExpectedTargetArch                string                                 `json:"expectedTargetArch"`
	ExpectedGoVersion                 string                                 `json:"expectedGoVersion"`
	ExpectedSubmodulePolicyDigest     string                                 `json:"expectedSubmodulePolicyDigest"`
	ExpectedLFSPolicyDigest           string                                 `json:"expectedLFSPolicyDigest"`
	ExpectedDependencyMode            string                                 `json:"expectedDependencyMode"`
	ExpectedSubmodules                []SubmoduleV1                          `json:"expectedSubmodules"`
	ExpectedLFSObjects                []LFSObjectV1                          `json:"expectedLFSObjects"`
	ExpectedExternalMaterials         map[string]ExternalMaterialExpectation `json:"expectedExternalMaterials"`
	ExpectedGenerated                 bool                                   `json:"expectedGenerated"`
	ExpectedGeneratedStageDigest      string                                 `json:"expectedGeneratedStageDigest"`
	ExpectedGeneratorInvocationDigest string                                 `json:"expectedGeneratorInvocationDigest"`
	ExpectedGeneratorInputDigest      string                                 `json:"expectedGeneratorInputDigest"`
	ExpectedGeneratorMaterialDigest   string                                 `json:"expectedGeneratorMaterialDigest"`
	ExpectedGeneratorToolchainDigest  string                                 `json:"expectedGeneratorToolchainDigest"`
	ExpectedBuilderPrincipalID        string                                 `json:"expectedBuilderPrincipalId"`
	ExpectedBuilderWorkflowIdentity   string                                 `json:"expectedBuilderWorkflowIdentity"`
	ExpectedBuilderIsolationProfile   string                                 `json:"expectedBuilderIsolationProfile"`
	Trust                             CurrentKeyPolicy                       `json:"trust"`
}

// ExternalMaterialExpectation is trusted caller policy, never parsed from a
// candidate artifact object. Every field is required for an expected digest.
type ExternalMaterialExpectation struct {
	MaterialKind string              `json:"materialKind"`
	Entries      map[string][]string `json:"entries"`
}

type VerifiedChain struct {
	SourceManifest       SourceManifestV1
	CompileRootManifest  CompileRootManifestV1
	GeneratedSourceStage *GeneratedSourceStageV1
	ExternalMaterials    []ExternalBuildMaterialManifestV1
	BuildRecord          MarshalArtifactBuildRecordV1
	BuildAttestation     MarshalArtifactBuildAttestationV1
}

// VerifiedBuildRecordChain is a verified pre-sign result. It carries no final
// artifact or attestation verdict and therefore cannot be confused with a
// complete release-chain decision.
type VerifiedBuildRecordChain struct {
	SourceManifest       SourceManifestV1
	CompileRootManifest  CompileRootManifestV1
	GeneratedSourceStage *GeneratedSourceStageV1
	ExternalMaterials    []ExternalBuildMaterialManifestV1
	BuildRecord          MarshalArtifactBuildRecordV1
}
