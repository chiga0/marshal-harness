package artifactattestation

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/authorityprovider"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

type chainFixture struct {
	raw       RawObjectSet
	policy    ValidationPolicy
	recordKey ed25519.PrivateKey
	attestKey ed25519.PrivateKey
}

func TestValidateArtifactChainAcceptsNoGeneratorChain(t *testing.T) {
	t.Parallel()
	fixture := newChainFixture(t, false)
	validator := mustValidator(t)
	verified, err := validator.ValidateArtifactChain(fixture.raw, fixture.policy)
	if err != nil {
		t.Fatalf("ValidateArtifactChain: %v", err)
	}
	if verified.GeneratedSourceStage != nil || verified.BuildAttestation.AttestationDigest == "" {
		t.Fatal("verified projection is incomplete")
	}
}

func TestValidateArtifactChainAcceptsGeneratedSourceChain(t *testing.T) {
	t.Parallel()
	fixture := newChainFixture(t, true)
	verified, err := mustValidator(t).ValidateArtifactChain(fixture.raw, fixture.policy)
	if err != nil {
		t.Fatalf("ValidateArtifactChain: %v", err)
	}
	if verified.GeneratedSourceStage == nil {
		t.Fatal("generated stage missing")
	}
}

func TestValidateBuildRecordChainAcceptsBothSourceModes(t *testing.T) {
	t.Parallel()
	for _, generated := range []bool{false, true} {
		generated := generated
		t.Run(map[bool]string{false: "no-generator", true: "generated"}[generated], func(t *testing.T) {
			t.Parallel()
			fixture := newChainFixture(t, generated)
			verified, err := mustValidator(t).ValidateBuildRecordChain(projectBuildRecordRaw(fixture.raw), projectBuildRecordPolicy(fixture.policy))
			if err != nil {
				t.Fatalf("ValidateBuildRecordChain: %v", err)
			}
			if (verified.GeneratedSourceStage != nil) != generated || verified.BuildRecord.RecordDigest == "" {
				t.Fatal("verified pre-sign projection is incomplete")
			}
		})
	}
}

func TestValidateBuildRecordChainRejectsReboundCandidateAndPolicyFacts(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*chainFixture){
		"external-policy": func(f *chainFixture) {
			f.policy.ExpectedEnvironmentPolicyDigest = digest("other-environment-policy")
		},
		"record": func(f *chainFixture) {
			var record MarshalArtifactBuildRecordV1
			mustUnmarshal(t, f.raw.BuildRecord, &record)
			record.BuildInvocationDigest = digest("candidate-selected-invocation")
			f.raw.BuildRecord = signRecord(t, record, f.recordKey)
		},
		"source": func(f *chainFixture) {
			var source SourceManifestV1
			mustUnmarshal(t, f.raw.SourceManifest, &source)
			source.BuildInvocationDigest = digest("candidate-selected-invocation")
			resignFromSource(t, f, source)
		},
		"external-material": func(f *chainFixture) {
			var material ExternalBuildMaterialManifestV1
			mustUnmarshal(t, f.raw.ExternalMaterialManifests[0], &material)
			material.Entries[0].SourceIdentity = "candidate-selected-source"
			f.raw.ExternalMaterialManifests[0] = marshalWithDigest(t, material, "manifestDigest")
			rebindExternalChain(t, f, false)
		},
		"material-reference": func(f *chainFixture) {
			for digest, expectation := range f.policy.ExpectedExternalMaterials {
				for key := range expectation.Entries {
					expectation.Entries[key] = []string{"candidate-selected-reference"}
					f.policy.ExpectedExternalMaterials[digest] = expectation
					return
				}
			}
		},
		"toolchain": func(f *chainFixture) {
			f.policy.ExpectedToolchainMaterialDigest = digest("candidate-selected-toolchain")
		},
	}
	for name, mutate := range cases {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fixture := newChainFixture(t, false)
			mutate(&fixture)
			if _, err := mustValidator(t).ValidateBuildRecordChain(projectBuildRecordRaw(fixture.raw), projectBuildRecordPolicy(fixture.policy)); err == nil {
				t.Fatal("rebound pre-sign chain escaped external/current policy")
			}
		})
	}
}

func TestRootSchemaRejectsArbitraryJSON(t *testing.T) {
	t.Parallel()
	validator := mustValidator(t)
	for _, raw := range []string{`{}`, `true`, `{"notAContract":true}`} {
		document, err := jsonschema.UnmarshalJSON(strings.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		if err := validator.schemas["root"].Validate(document); err == nil {
			t.Fatalf("root schema accepted %s", raw)
		}
	}
	valid, err := jsonschema.UnmarshalJSON(strings.NewReader(string(newChainFixture(t, false).raw.SourceManifest)))
	if err != nil || validator.schemas["root"].Validate(valid) != nil {
		t.Fatal("root schema rejected a closed source manifest")
	}
}

func TestFramingAndClosedSchemaRejections(t *testing.T) {
	t.Parallel()
	fixture := newChainFixture(t, false)
	var source map[string]json.RawMessage
	if err := json.Unmarshal(fixture.raw.SourceManifest, &source); err != nil {
		t.Fatal(err)
	}
	source["unknown"] = json.RawMessage(`true`)
	unknown := mustCanonical(t, source)
	cases := map[string][]byte{
		"unknown":      unknown,
		"trailing":     append(append([]byte(nil), fixture.raw.SourceManifest...), '\n'),
		"noncanonical": append([]byte(" "), fixture.raw.SourceManifest...),
		"duplicate":    []byte(`{"schemaVersion":"marshal.source-manifest.v1","schemaVersion":"marshal.source-manifest.v1"}`),
	}
	for name, raw := range cases {
		name, raw := name, raw
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			candidate := fixture.raw
			candidate.SourceManifest = raw
			if _, err := mustValidator(t).ValidateArtifactChain(candidate, fixture.policy); err == nil {
				t.Fatal("rejected framing was accepted")
			}
		})
	}
}

func TestManifestEntryHostileMatrix(t *testing.T) {
	t.Parallel()
	base := newChainFixture(t, false)
	mutations := map[string]func(*SourceManifestV1){
		"regular-null-hash": func(s *SourceManifestV1) { s.Entries[0].SHA256 = nil },
		"directory-hash": func(s *SourceManifestV1) {
			digest := digest("directory")
			s.Entries[1].SHA256 = &digest
		},
		"unsorted": func(s *SourceManifestV1) { s.Entries[0], s.Entries[1] = s.Entries[1], s.Entries[0] },
		"absolute": func(s *SourceManifestV1) { s.Entries[0].Path = "/main.go" },
		"dotdot":   func(s *SourceManifestV1) { s.Entries[0].Path = "../main.go" },
		"non-nfc":  func(s *SourceManifestV1) { s.Entries[0].Path = "cafe\u0301.go" },
		"casefold-collision": func(s *SourceManifestV1) {
			s.Entries = append(s.Entries, s.Entries[0])
			s.Entries[2].Path = "Main.go"
		},
		"symlink-escape": func(s *SourceManifestV1) {
			target, boundary := "../outside", "within-sealed-root"
			hash := canonical.DigestBytes([]byte(target))
			s.Entries[0] = ManifestEntryV1{Path: "link", EntryType: "symlink", Mode: 0777, Length: uint64(len(target)), SHA256: &hash, SymlinkTarget: &target, SymlinkBoundary: &boundary}
		},
		"symlink-nul": func(s *SourceManifestV1) {
			target, boundary := "../x\x00", "within-sealed-root"
			hash := canonical.DigestBytes([]byte(target))
			s.Entries[0] = ManifestEntryV1{Path: "nested/link", EntryType: "symlink", Mode: 0777, Length: uint64(len(target)), SHA256: &hash, SymlinkTarget: &target, SymlinkBoundary: &boundary}
		},
	}
	for name, mutate := range mutations {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var source SourceManifestV1
			mustUnmarshal(t, base.raw.SourceManifest, &source)
			mutate(&source)
			source.RootDigest = digestOf(source.Entries)
			baseRaw := marshalWithDigest(t, source, "manifestDigest")
			candidate := base.raw
			candidate.SourceManifest = baseRaw
			if _, err := mustValidator(t).ValidateArtifactChain(candidate, base.policy); err == nil {
				t.Fatal("hostile entry was accepted")
			}
		})
	}
}

func TestCompileRootAndGeneratedStageEquality(t *testing.T) {
	t.Parallel()
	for _, generated := range []bool{false, true} {
		generated := generated
		t.Run(map[bool]string{false: "no-generator", true: "generated"}[generated], func(t *testing.T) {
			t.Parallel()
			fixture := newChainFixture(t, generated)
			var compile CompileRootManifestV1
			mustUnmarshal(t, fixture.raw.CompileRootManifest, &compile)
			hash := digest("replacement")
			compile.Entries[0].SHA256 = &hash
			compile.RootDigest = digestOf(compile.Entries)
			fixture.raw.CompileRootManifest = marshalWithDigest(t, compile, "manifestDigest")
			if _, err := mustValidator(t).ValidateArtifactChain(fixture.raw, fixture.policy); err == nil {
				t.Fatal("replaced compile root was accepted")
			}
		})
	}

	generated := newChainFixture(t, true)
	var stage GeneratedSourceStageV1
	mustUnmarshal(t, generated.raw.GeneratedSourceStage, &stage)
	stage.GeneratorInputDigest = digest("wrong-input")
	generated.raw.GeneratedSourceStage = marshalWithDigest(t, stage, "stageDigest")
	if _, err := mustValidator(t).ValidateArtifactChain(generated.raw, generated.policy); err == nil {
		t.Fatal("generator declaration mismatch was accepted")
	}
}

func TestExternalMaterialAndRecordBindingMatrix(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*chainFixture){
		"empty-materials": func(f *chainFixture) { f.raw.ExternalMaterialManifests = nil },
		"toolchain-kind": func(f *chainFixture) {
			var material ExternalBuildMaterialManifestV1
			mustUnmarshal(t, f.raw.ExternalMaterialManifests[len(f.raw.ExternalMaterialManifests)-1], &material)
			material.MaterialKind = "external-build-tool"
			f.raw.ExternalMaterialManifests[len(f.raw.ExternalMaterialManifests)-1] = marshalWithDigest(t, material, "manifestDigest")
		},
		"logical-identity-different-bytes": func(f *chainFixture) {
			var material ExternalBuildMaterialManifestV1
			mustUnmarshal(t, f.raw.ExternalMaterialManifests[0], &material)
			copyMaterial := material
			copyMaterial.MaterialSetID = "second-set"
			copyMaterial.Entries = append([]ExternalMaterialEntryV1(nil), material.Entries...)
			hash := digest("different")
			copyMaterial.Entries[0].SHA256 = &hash
			f.raw.ExternalMaterialManifests = append(f.raw.ExternalMaterialManifests, marshalWithDigest(t, copyMaterial, "manifestDigest"))
		},
		"record-extra-digest": func(f *chainFixture) {
			var record MarshalArtifactBuildRecordV1
			mustUnmarshal(t, f.raw.BuildRecord, &record)
			record.ExternalMaterialManifestDigests = append(record.ExternalMaterialManifestDigests, digest("unknown"))
			sortStrings(record.ExternalMaterialManifestDigests)
			f.raw.BuildRecord = signRecord(t, record, f.recordKey)
		},
	}
	for name, mutate := range cases {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fixture := newChainFixture(t, false)
			mutate(&fixture)
			if _, err := mustValidator(t).ValidateArtifactChain(fixture.raw, fixture.policy); err == nil {
				t.Fatal("invalid material/record binding was accepted")
			}
		})
	}
}

func TestExternalReleasePolicyCannotBeCandidateSelected(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*ValidationPolicy){
		"source-bundle":   func(p *ValidationPolicy) { p.ExpectedSourceBundleDigest = digest("other-bundle") },
		"source-manifest": func(p *ValidationPolicy) { p.ExpectedSourceManifestDigest = digest("other-source") },
		"compile-root":    func(p *ValidationPolicy) { p.ExpectedCompileRootManifestDigest = digest("other-compile") },
		"go-mod":          func(p *ValidationPolicy) { p.ExpectedGoModDigest = stringPointer(digest("other-go-mod")) },
		"go-sum-none":     func(p *ValidationPolicy) { p.ExpectedGoSumDigest = nil },
		"module":          func(p *ValidationPolicy) { p.ExpectedModuleGraphDigest = digest("other-module") },
		"invocation":      func(p *ValidationPolicy) { p.ExpectedBuildInvocationDigest = digest("other-invocation") },
		"environment":     func(p *ValidationPolicy) { p.ExpectedEnvironmentPolicyDigest = digest("other-environment") },
		"toolchain":       func(p *ValidationPolicy) { p.ExpectedToolchainMaterialDigest = digest("other-toolchain") },
		"dependency-mode": func(p *ValidationPolicy) { p.ExpectedDependencyMode = "vendor" },
		"arch":            func(p *ValidationPolicy) { p.ExpectedTargetArch = "amd64" },
		"go-version":      func(p *ValidationPolicy) { p.ExpectedGoVersion = "go1.25.0" },
		"submodule":       func(p *ValidationPolicy) { p.ExpectedSubmodulePolicyDigest = digest("other-submodule") },
		"lfs":             func(p *ValidationPolicy) { p.ExpectedLFSPolicyDigest = digest("other-lfs") },
		"empty-anchor":    func(p *ValidationPolicy) { p.ExpectedBuildInvocationDigest = "" },
		"nil-materials":   func(p *ValidationPolicy) { p.ExpectedExternalMaterials = nil },
		"missing-material": func(p *ValidationPolicy) {
			for key := range p.ExpectedExternalMaterials {
				delete(p.ExpectedExternalMaterials, key)
				break
			}
		},
		"extra-material": func(p *ValidationPolicy) {
			p.ExpectedExternalMaterials[digest("extra")] = ExternalMaterialExpectation{MaterialKind: "go-module-source", Entries: map[string][]string{"extra\x00extra.go": {"build-invocation"}}}
		},
	}
	for name, mutate := range cases {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fixture := newChainFixture(t, false)
			mutate(&fixture.policy)
			if _, err := mustValidator(t).ValidateArtifactChain(fixture.raw, fixture.policy); err == nil {
				t.Fatal("candidate-selected release policy was accepted")
			}
		})
	}
}

func TestExternalReferencedByExactClosure(t *testing.T) {
	t.Parallel()
	cases := map[string][]string{"missing": {"build-invocation"}, "extra": {"build-invocation", "module-graph", "other"}, "mismatch": {"build-invocation", "other"}}
	for name, tc := range cases {
		name, tc := name, tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fixture := newChainFixture(t, false)
			var material ExternalBuildMaterialManifestV1
			mustUnmarshal(t, fixture.raw.ExternalMaterialManifests[0], &material)
			material.Entries[0].ReferencedBy = append([]string(nil), tc...)
			sortStrings(material.Entries[0].ReferencedBy)
			fixture.raw.ExternalMaterialManifests[0] = marshalWithDigest(t, material, "manifestDigest")
			rebindExternalChain(t, &fixture, false)
			if _, err := mustValidator(t).ValidateArtifactChain(fixture.raw, fixture.policy); err == nil {
				t.Fatal("invalid referencedBy closure was accepted after valid downstream re-signing")
			}
		})
	}
}

func TestExternalReferencesArePinnedPerEntry(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"swap", "missing-expectation", "extra-expectation"} {
		name := name
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fixture := newChainFixture(t, false)
			var module ExternalBuildMaterialManifestV1
			mustUnmarshal(t, fixture.raw.ExternalMaterialManifests[0], &module)
			expectation := fixture.policy.ExpectedExternalMaterials[module.ManifestDigest]
			switch name {
			case "swap":
				module.Entries[0].ReferencedBy, module.Entries[1].ReferencedBy = module.Entries[1].ReferencedBy, module.Entries[0].ReferencedBy
				fixture.raw.ExternalMaterialManifests[0] = marshalWithDigest(t, module, "manifestDigest")
				rebindExternalChain(t, &fixture, false)
			case "missing-expectation":
				delete(expectation.Entries, externalEntryKey(module.Entries[1]))
				fixture.policy.ExpectedExternalMaterials[module.ManifestDigest] = expectation
			case "extra-expectation":
				expectation.Entries["other\x00other.go"] = []string{"build-invocation"}
				fixture.policy.ExpectedExternalMaterials[module.ManifestDigest] = expectation
			}
			if _, err := mustValidator(t).ValidateArtifactChain(fixture.raw, fixture.policy); err == nil {
				t.Fatal("per-entry reference mismatch was accepted")
			}
		})
	}
}

func TestObservedExternalSetCannotRewriteTrustedPolicy(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"missing", "extra"} {
		name := name
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fixture := newChainFixture(t, false)
			if name == "missing" {
				fixture.raw.ExternalMaterialManifests = fixture.raw.ExternalMaterialManifests[1:]
			} else {
				extra := ExternalBuildMaterialManifestV1{SchemaVersion: ExternalMaterialSchema, MaterialSetID: "extra-module", MaterialKind: "go-module-source", ProducerObservationIdentity: "source-producer-1", PolicyDigest: fixture.policy.ExpectedEnvironmentPolicyDigest, Entries: []ExternalMaterialEntryV1{{LogicalIdentity: "module:example.com/extra@v1.0.0", Path: "example.com/extra@v1.0.0/extra.go", EntryType: "regular", Mode: 0644, Length: 10, SHA256: stringPointer(digest("extra-bytes")), SourceIdentity: "proxy.example/extra", ReferencedBy: []string{"build-invocation", "module-graph"}}}}
				fixture.raw.ExternalMaterialManifests = append(fixture.raw.ExternalMaterialManifests, marshalWithDigest(t, extra, "manifestDigest"))
			}
			rebindExternalChain(t, &fixture, false)
			if _, err := mustValidator(t).ValidateArtifactChain(fixture.raw, fixture.policy); err == nil {
				t.Fatal("observed external set rewrote trusted expected materials")
			}
		})
	}
}

func TestExternalKindAndPolicyAreExternallyAnchored(t *testing.T) {
	t.Parallel()
	for _, field := range []string{"kind", "policy"} {
		field := field
		t.Run(field, func(t *testing.T) {
			fixture := newChainFixture(t, false)
			var material ExternalBuildMaterialManifestV1
			mustUnmarshal(t, fixture.raw.ExternalMaterialManifests[0], &material)
			if field == "kind" {
				material.MaterialKind = "workspace-module"
			} else {
				material.PolicyDigest = digest("candidate-policy")
			}
			fixture.raw.ExternalMaterialManifests[0] = marshalWithDigest(t, material, "manifestDigest")
			rebindExternalChain(t, &fixture, false)
			if _, err := mustValidator(t).ValidateArtifactChain(fixture.raw, fixture.policy); err == nil {
				t.Fatal("candidate-selected external facts were accepted")
			}
		})
	}
}

func TestGeneratedFactsAreExternallyAnchored(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*SourceManifestV1, *GeneratedSourceStageV1){
		"invocation": func(s *SourceManifestV1, g *GeneratedSourceStageV1) {
			s.GeneratedSourcePolicy.GeneratorInvocationDigest = digest("drift-invocation")
			g.GeneratorInvocationDigest = s.GeneratedSourcePolicy.GeneratorInvocationDigest
		},
		"input": func(s *SourceManifestV1, g *GeneratedSourceStageV1) {
			s.GeneratedSourcePolicy.GeneratorInputDigest = digest("drift-input")
			g.GeneratorInputDigest = s.GeneratedSourcePolicy.GeneratorInputDigest
		},
		"material": func(s *SourceManifestV1, g *GeneratedSourceStageV1) {
			s.GeneratedSourcePolicy.GeneratorMaterialDigest = digest("drift-material")
			g.GeneratorMaterialDigest = s.GeneratedSourcePolicy.GeneratorMaterialDigest
		},
		"toolchain": func(s *SourceManifestV1, g *GeneratedSourceStageV1) {
			s.GeneratedSourcePolicy.GeneratorToolchainDigest = digest("drift-toolchain")
			g.GeneratorToolchainDigest = s.GeneratedSourcePolicy.GeneratorToolchainDigest
		},
	}
	for name, mutate := range cases {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fixture := newChainFixture(t, true)
			var source SourceManifestV1
			var stage GeneratedSourceStageV1
			mustUnmarshal(t, fixture.raw.SourceManifest, &source)
			mustUnmarshal(t, fixture.raw.GeneratedSourceStage, &stage)
			mutate(&source, &stage)
			fixture.raw.SourceManifest = marshalWithDigest(t, source, "manifestDigest")
			mustUnmarshal(t, fixture.raw.SourceManifest, &source)
			stage.SourceManifestDigest = source.ManifestDigest
			fixture.raw.GeneratedSourceStage = marshalWithDigest(t, stage, "stageDigest")
			mustUnmarshal(t, fixture.raw.GeneratedSourceStage, &stage)
			var compile CompileRootManifestV1
			mustUnmarshal(t, fixture.raw.CompileRootManifest, &compile)
			compile.SourceManifestDigest = source.ManifestDigest
			compile.GeneratedSourceStageDigest = &stage.StageDigest
			fixture.raw.CompileRootManifest = marshalWithDigest(t, compile, "manifestDigest")
			resignFromSource(t, &fixture, source)
			var reboundSource SourceManifestV1
			var reboundCompile CompileRootManifestV1
			var reboundStage GeneratedSourceStageV1
			mustUnmarshal(t, fixture.raw.SourceManifest, &reboundSource)
			mustUnmarshal(t, fixture.raw.CompileRootManifest, &reboundCompile)
			mustUnmarshal(t, fixture.raw.GeneratedSourceStage, &reboundStage)
			fixture.policy.ExpectedSourceManifestDigest = reboundSource.ManifestDigest
			fixture.policy.ExpectedCompileRootManifestDigest = reboundCompile.ManifestDigest
			fixture.policy.ExpectedGeneratedStageDigest = reboundStage.StageDigest
			if _, err := mustValidator(t).ValidateArtifactChain(fixture.raw, fixture.policy); err == nil {
				t.Fatal("simultaneous source/stage drift was accepted")
			}
		})
	}
}

func TestGeneratedPolicyPresenceIsExact(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*chainFixture){
		"unexpected-generated": func(f *chainFixture) {
			f.policy.ExpectedGenerated = false
			f.policy.ExpectedGeneratedStageDigest = ""
			f.policy.ExpectedGeneratorInvocationDigest = ""
			f.policy.ExpectedGeneratorInputDigest = ""
			f.policy.ExpectedGeneratorMaterialDigest = ""
			f.policy.ExpectedGeneratorToolchainDigest = ""
		},
		"missing-stage-anchor": func(f *chainFixture) { f.policy.ExpectedGeneratedStageDigest = "" },
		"missing-invocation":   func(f *chainFixture) { f.policy.ExpectedGeneratorInvocationDigest = "" },
		"missing-input":        func(f *chainFixture) { f.policy.ExpectedGeneratorInputDigest = "" },
		"missing-material":     func(f *chainFixture) { f.policy.ExpectedGeneratorMaterialDigest = "" },
		"missing-toolchain":    func(f *chainFixture) { f.policy.ExpectedGeneratorToolchainDigest = "" },
	}
	for name, mutate := range cases {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fixture := newChainFixture(t, true)
			mutate(&fixture)
			if _, err := mustValidator(t).ValidateArtifactChain(fixture.raw, fixture.policy); err == nil {
				t.Fatal("incomplete generated policy was accepted")
			}
		})
	}
	noGenerator := newChainFixture(t, false)
	noGenerator.policy.ExpectedGeneratorInputDigest = digest("unexpected")
	if _, err := mustValidator(t).ValidateArtifactChain(noGenerator.raw, noGenerator.policy); err == nil {
		t.Fatal("generator anchor was accepted for a no-generator chain")
	}
}

func TestSourceObservationDigestCannotBeReSignedIntoPolicy(t *testing.T) {
	t.Parallel()
	fixture := newChainFixture(t, false)
	var source SourceManifestV1
	mustUnmarshal(t, fixture.raw.SourceManifest, &source)
	replacement := digest("replaced-source-bytes")
	source.Entries[0].SHA256 = &replacement
	source.RootDigest = digestOf(source.Entries)
	source.SourceBundleDigest = digest("replaced-source-bundle")
	resignFromSource(t, &fixture, source)
	if _, err := mustValidator(t).ValidateArtifactChain(fixture.raw, fixture.policy); err == nil {
		t.Fatal("simultaneously re-digested and re-signed source escaped external observation anchors")
	}
}

func TestSubmoduleExactObservationAndMaterialPresence(t *testing.T) {
	t.Parallel()
	base := newChainFixture(t, false)
	var source SourceManifestV1
	mustUnmarshal(t, base.raw.SourceManifest, &source)
	subHash := digest("submodule-file")
	source.Entries = append(source.Entries,
		ManifestEntryV1{Path: "third_party/repo", EntryType: "directory", Mode: 0755},
		ManifestEntryV1{Path: "third_party/repo/file.go", EntryType: "regular", Mode: 0644, Length: 20, SHA256: &subHash},
	)
	source.RootDigest = digestOf(source.Entries)
	source.SubmodulePolicyDigest = digest("submodule-policy")
	source.Submodules = []SubmoduleV1{{Path: "third_party/repo", PinnedCommit: strings.Repeat("d", 40), MaterializedTreeDigest: digest("submodule-tree")}}
	resignFromSource(t, &base, source)
	adoptObservedPolicy(t, &base)
	if _, err := mustValidator(t).ValidateArtifactChain(base.raw, base.policy); err != nil {
		t.Fatalf("valid externally observed submodule: %v", err)
	}
	cases := map[string]func(*SourceManifestV1){
		"missing-observation": func(s *SourceManifestV1) { s.Submodules = []SubmoduleV1{} },
		"duplicate":           func(s *SourceManifestV1) { s.Submodules = append(s.Submodules, s.Submodules[0]) },
		"wrong-digest":        func(s *SourceManifestV1) { s.Submodules[0].MaterializedTreeDigest = digest("other-tree") },
		"regular-root": func(s *SourceManifestV1) {
			hash := digest("not-a-directory")
			s.Entries[2] = ManifestEntryV1{Path: "third_party/repo", EntryType: "regular", Mode: 0644, Length: 10, SHA256: &hash}
		},
		"symlink-root": func(s *SourceManifestV1) {
			target, boundary := "../target", "within-sealed-root"
			hash := canonical.DigestBytes([]byte(target))
			s.Entries[2] = ManifestEntryV1{Path: "third_party/repo", EntryType: "symlink", Mode: 0777, Length: uint64(len(target)), SHA256: &hash, SymlinkTarget: &target, SymlinkBoundary: &boundary}
		},
		"missing-root": func(s *SourceManifestV1) {
			s.Entries = append(s.Entries[:2], s.Entries[3:]...)
			s.RootDigest = digestOf(s.Entries)
		},
	}
	for name, mutate := range cases {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			fixture := base
			fixture.policy = cloneValidationPolicy(base.policy)
			var candidate SourceManifestV1
			mustUnmarshal(t, fixture.raw.SourceManifest, &candidate)
			mutate(&candidate)
			candidate.RootDigest = digestOf(candidate.Entries)
			resignFromSource(t, &fixture, candidate)
			adoptObjectDigestsOnly(t, &fixture)
			if _, err := mustValidator(t).ValidateArtifactChain(fixture.raw, fixture.policy); err == nil {
				t.Fatal("invalid submodule observation was accepted after full re-sign")
			}
		})
	}
}

func TestLFSExactObservationMatchesMaterializedSourceEntry(t *testing.T) {
	t.Parallel()
	base := newChainFixture(t, false)
	var source SourceManifestV1
	mustUnmarshal(t, base.raw.SourceManifest, &source)
	lfsDigest := digest("lfs-materialized-object")
	source.Entries = append([]ManifestEntryV1{{Path: "assets/model.bin", EntryType: "regular", Mode: 0644, Length: 2000, SHA256: &lfsDigest}}, source.Entries...)
	source.RootDigest = digestOf(source.Entries)
	source.LFSPolicyDigest = digest("lfs-policy")
	source.LFSObjects = []LFSObjectV1{{Path: "assets/model.bin", PointerDigest: digest("lfs-pointer"), MaterializedObjectDigest: lfsDigest}}
	resignFromSource(t, &base, source)
	adoptObservedPolicy(t, &base)
	if _, err := mustValidator(t).ValidateArtifactChain(base.raw, base.policy); err != nil {
		t.Fatalf("valid externally observed LFS object: %v", err)
	}
	cases := map[string]func(*SourceManifestV1){
		"missing":      func(s *SourceManifestV1) { s.LFSObjects = []LFSObjectV1{} },
		"duplicate":    func(s *SourceManifestV1) { s.LFSObjects = append(s.LFSObjects, s.LFSObjects[0]) },
		"wrong-object": func(s *SourceManifestV1) { s.LFSObjects[0].MaterializedObjectDigest = digest("other-lfs-object") },
		"wrong-entry": func(s *SourceManifestV1) {
			other := digest("entry-is-not-lfs-object")
			s.Entries[0].SHA256 = &other
			s.RootDigest = digestOf(s.Entries)
		},
		"missing-entry": func(s *SourceManifestV1) { s.Entries = s.Entries[1:]; s.RootDigest = digestOf(s.Entries) },
	}
	for name, mutate := range cases {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			fixture := base
			fixture.policy = cloneValidationPolicy(base.policy)
			var candidate SourceManifestV1
			mustUnmarshal(t, fixture.raw.SourceManifest, &candidate)
			mutate(&candidate)
			resignFromSource(t, &fixture, candidate)
			adoptObjectDigestsOnly(t, &fixture)
			if _, err := mustValidator(t).ValidateArtifactChain(fixture.raw, fixture.policy); err == nil {
				t.Fatal("invalid LFS observation was accepted after full re-sign")
			}
		})
	}
}

func TestDependencyModePreservesAcceptedV1Range(t *testing.T) {
	t.Parallel()
	kindByMode := map[string]string{"modules": "go-module-source", "vendor": "vendor-tree", "workspace": "workspace-module", "local-replace": "local-replace"}
	for mode, kind := range kindByMode {
		mode, kind := mode, kind
		t.Run(mode, func(t *testing.T) {
			fixture := newChainFixture(t, false)
			var source SourceManifestV1
			var material ExternalBuildMaterialManifestV1
			mustUnmarshal(t, fixture.raw.SourceManifest, &source)
			mustUnmarshal(t, fixture.raw.ExternalMaterialManifests[0], &material)
			source.DependencyMode = mode
			fixture.raw.SourceManifest = marshalWithDigest(t, source, "manifestDigest")
			material.MaterialKind = kind
			fixture.raw.ExternalMaterialManifests[0] = marshalWithDigest(t, material, "manifestDigest")
			rebindExternalChain(t, &fixture, false)
			adoptObservedPolicy(t, &fixture)
			if _, err := mustValidator(t).ValidateArtifactChain(fixture.raw, fixture.policy); err != nil {
				t.Fatalf("accepted V1 dependency mode rejected: %v", err)
			}
		})
	}
	fixture := newChainFixture(t, false)
	var source SourceManifestV1
	mustUnmarshal(t, fixture.raw.SourceManifest, &source)
	source.DependencyMode = "vendor"
	resignFromSource(t, &fixture, source)
	adoptObservedPolicy(t, &fixture)
	if _, err := mustValidator(t).ValidateArtifactChain(fixture.raw, fixture.policy); err == nil {
		t.Fatal("vendor mode without exact vendor-tree material was accepted")
	}
	noDependencies := newChainFixture(t, false)
	noDependencies.raw.ExternalMaterialManifests = noDependencies.raw.ExternalMaterialManifests[1:]
	rebindExternalChain(t, &noDependencies, false)
	adoptObservedPolicy(t, &noDependencies)
	if _, err := mustValidator(t).ValidateArtifactChain(noDependencies.raw, noDependencies.policy); err != nil {
		t.Fatalf("modules mode without module dependencies was rejected: %v", err)
	}
}

func TestCurrentProducerKeyPolicyMatrix(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 26, 1, 0, 0, 0, time.UTC)
	cases := map[string]func(*chainFixture){
		"wrong-producer": func(f *chainFixture) { f.policy.Trust.BuildRecord.ProducerPrincipalID = "other-builder" },
		"wrong-usage":    func(f *chainFixture) { f.policy.Trust.BuildRecord.Keys[0].Usage = "other-usage" },
		"old-epoch":      func(f *chainFixture) { f.policy.Trust.BuildRecord.CurrentKeyEpoch++ },
		"future-epoch": func(f *chainFixture) {
			var record MarshalArtifactBuildRecordV1
			mustUnmarshal(t, f.raw.BuildRecord, &record)
			record.SignedObjectEnvelope.KeyEpoch++
			f.raw.BuildRecord = mustCanonical(t, record)
		},
		"before-validity": func(f *chainFixture) { f.policy.Trust.BuildRecord.Keys[0].ValidFrom = now.Add(time.Hour) },
		"after-validity":  func(f *chainFixture) { f.policy.Trust.BuildRecord.Keys[0].ValidUntil = now },
		"revoked": func(f *chainFixture) {
			revoked := now.Add(-time.Hour)
			f.policy.Trust.BuildRecord.Keys[0].RevokedAt = &revoked
		},
		"wrong-domain": func(f *chainFixture) {
			var record MarshalArtifactBuildRecordV1
			mustUnmarshal(t, f.raw.BuildRecord, &record)
			record.SignedObjectEnvelope.SignatureDomain = "wrong\x00"
			f.raw.BuildRecord = mustCanonical(t, record)
		},
		"attestation-role-key-reuse": func(f *chainFixture) {
			var attestation MarshalArtifactBuildAttestationV1
			mustUnmarshal(t, f.raw.BuildAttestation, &attestation)
			f.raw.BuildAttestation = signAttestation(t, attestation, f.recordKey)
			f.policy.Trust.BuildAttestation.Keys[0].PublicKey = append(ed25519.PublicKey(nil), f.policy.Trust.BuildRecord.Keys[0].PublicKey...)
		},
	}
	for name, mutate := range cases {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fixture := newChainFixture(t, false)
			mutate(&fixture)
			if _, err := mustValidator(t).ValidateArtifactChain(fixture.raw, fixture.policy); err == nil {
				t.Fatal("invalid current key policy was accepted")
			}
		})
	}
}

func TestAttestationCurrentProducerKeyPolicyMatrix(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 26, 1, 0, 0, 0, time.UTC)
	cases := map[string]func(*chainFixture){
		"wrong-producer": func(f *chainFixture) {
			f.policy.Trust.BuildAttestation.ProducerPrincipalID = "other-artifact-authority"
		},
		"wrong-usage": func(f *chainFixture) { f.policy.Trust.BuildAttestation.Keys[0].Usage = "apple-code-signing" },
		"old-epoch":   func(f *chainFixture) { f.policy.Trust.BuildAttestation.CurrentKeyEpoch++ },
		"issued-after-validity": func(f *chainFixture) {
			f.policy.Trust.BuildAttestation.Keys[0].ValidUntil = now.Add(30 * time.Second)
		},
		"revoked": func(f *chainFixture) {
			revoked := now.Add(-time.Minute)
			f.policy.Trust.BuildAttestation.Keys[0].RevokedAt = &revoked
		},
		"wrong-domain": func(f *chainFixture) {
			var attestation MarshalArtifactBuildAttestationV1
			mustUnmarshal(t, f.raw.BuildAttestation, &attestation)
			attestation.SignedObjectEnvelope.SignatureDomain = "apple-code-signing\x00"
			f.raw.BuildAttestation = mustCanonical(t, attestation)
		},
	}
	for name, mutate := range cases {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fixture := newChainFixture(t, false)
			mutate(&fixture)
			if _, err := mustValidator(t).ValidateArtifactChain(fixture.raw, fixture.policy); err == nil {
				t.Fatal("invalid attestation current key policy was accepted")
			}
		})
	}
}

func TestAttestationCrossObjectAndCodeSignatureMatrix(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*MarshalArtifactBuildAttestationV1){
		"record-source-head": func(a *MarshalArtifactBuildAttestationV1) { a.SourceHead = strings.Repeat("b", 40) },
		"builder-principal":  func(a *MarshalArtifactBuildAttestationV1) { a.BuilderPrincipalID = "other-builder" },
		"unsigned-output":    func(a *MarshalArtifactBuildAttestationV1) { a.UnsignedArtifact.RawSHA256 = digest("other-unsigned") },
		"final-observation-digest": func(a *MarshalArtifactBuildAttestationV1) {
			a.CodeSignatureObservation.ObservedFinalRawSHA256 = digest("other-final")
		},
		"final-observation-size": func(a *MarshalArtifactBuildAttestationV1) { a.CodeSignatureObservation.ObservedFileSize++ },
		"observer-workflow": func(a *MarshalArtifactBuildAttestationV1) {
			a.CodeSignatureObservation.ObserverWorkflowID = "other-observer"
		},
		"workflow-role-collapse": func(a *MarshalArtifactBuildAttestationV1) {
			a.CodeSigningWorkflowIdentity = a.ArtifactAttestationWorkflowIdentity
		},
		"code-signing-is-builder": func(a *MarshalArtifactBuildAttestationV1) {
			a.CodeSigningWorkflowIdentity = a.BuilderWorkflowIdentity
		},
		"attestation-is-builder": func(a *MarshalArtifactBuildAttestationV1) {
			a.ArtifactAttestationWorkflowIdentity = a.BuilderWorkflowIdentity
			a.CodeSignatureObservation.ObserverWorkflowID = a.BuilderWorkflowIdentity
		},
		"unsigned-equals-final": func(a *MarshalArtifactBuildAttestationV1) {
			a.FinalArtifact.RawSHA256 = a.UnsignedArtifact.RawSHA256
			a.CodeSignatureObservation.ObservedFinalRawSHA256 = a.UnsignedArtifact.RawSHA256
		},
		"wrong-identifier":          func(a *MarshalArtifactBuildAttestationV1) { a.CodeSignatureIdentity.Identifier = "com.example.marshal" },
		"missing-certificate-chain": func(a *MarshalArtifactBuildAttestationV1) { a.CodeSignatureIdentity.CertificateChainSHA256 = nil },
		"release-without-team": func(a *MarshalArtifactBuildAttestationV1) {
			a.BuildProfile = "darwin-notarized-release"
			a.CodeSignatureIdentity.SignatureKind = "developer-id-application"
			a.CodeSignatureIdentity.TeamIdentifier = nil
			a.CodeSignatureIdentity.HardenedRuntime = true
			a.CodeSignatureIdentity.SecureTimestamp = true
		},
	}
	for name, mutate := range cases {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fixture := newChainFixture(t, false)
			var attestation MarshalArtifactBuildAttestationV1
			mustUnmarshal(t, fixture.raw.BuildAttestation, &attestation)
			mutate(&attestation)
			fixture.raw.BuildAttestation = signAttestation(t, attestation, fixture.attestKey)
			if _, err := mustValidator(t).ValidateArtifactChain(fixture.raw, fixture.policy); err == nil {
				t.Fatal("invalid attestation binding was accepted")
			}
		})
	}
}

func TestRecordAndAttestationCannotJointlyDriftFromExternalAnchors(t *testing.T) {
	t.Parallel()
	fixture := newChainFixture(t, false)
	var record MarshalArtifactBuildRecordV1
	var attestation MarshalArtifactBuildAttestationV1
	mustUnmarshal(t, fixture.raw.BuildRecord, &record)
	mustUnmarshal(t, fixture.raw.BuildAttestation, &attestation)
	record.BuildInvocationDigest = digest("joint-record-drift")
	fixture.raw.BuildRecord = signRecord(t, record, fixture.recordKey)
	mustUnmarshal(t, fixture.raw.BuildRecord, &record)
	attestation.BuildRecordDigest = record.RecordDigest
	attestation.BuildInvocationDigest = record.BuildInvocationDigest
	fixture.raw.BuildAttestation = signAttestation(t, attestation, fixture.attestKey)
	if _, err := mustValidator(t).ValidateArtifactChain(fixture.raw, fixture.policy); err == nil {
		t.Fatal("jointly re-signed record and attestation escaped the external invocation anchor")
	}
}

func TestBuilderFactsCannotJointlyDriftWithValidSignatures(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*MarshalArtifactBuildRecordV1, *MarshalArtifactBuildAttestationV1){
		"principal": func(r *MarshalArtifactBuildRecordV1, a *MarshalArtifactBuildAttestationV1) {
			r.BuilderPrincipalID = "other-builder"
			a.BuilderPrincipalID = r.BuilderPrincipalID
		},
		"workflow": func(r *MarshalArtifactBuildRecordV1, a *MarshalArtifactBuildAttestationV1) {
			r.BuilderWorkflowIdentity = "other-builder-workflow"
			a.BuilderWorkflowIdentity = r.BuilderWorkflowIdentity
		},
		"isolation": func(r *MarshalArtifactBuildRecordV1, a *MarshalArtifactBuildAttestationV1) {
			r.BuilderIsolationProfile = "other-isolation"
			a.BuilderIsolationProfile = r.BuilderIsolationProfile
		},
	}
	for name, mutate := range cases {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fixture := newChainFixture(t, false)
			var record MarshalArtifactBuildRecordV1
			var attestation MarshalArtifactBuildAttestationV1
			mustUnmarshal(t, fixture.raw.BuildRecord, &record)
			mustUnmarshal(t, fixture.raw.BuildAttestation, &attestation)
			mutate(&record, &attestation)
			fixture.raw.BuildRecord = signRecord(t, record, fixture.recordKey)
			mustUnmarshal(t, fixture.raw.BuildRecord, &record)
			attestation.BuildRecordDigest = record.RecordDigest
			fixture.raw.BuildAttestation = signAttestation(t, attestation, fixture.attestKey)
			if _, err := mustValidator(t).ValidateArtifactChain(fixture.raw, fixture.policy); err == nil {
				t.Fatal("jointly re-signed builder facts escaped external current policy")
			}
		})
	}
}

func TestCodeSigningAndReleaseFactsCannotBeReSignedIntoPolicy(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*MarshalArtifactBuildAttestationV1){
		"code-sign-workflow": func(a *MarshalArtifactBuildAttestationV1) { a.CodeSigningWorkflowIdentity = "other-code-sign-workflow" },
		"attestation-workflow": func(a *MarshalArtifactBuildAttestationV1) {
			a.ArtifactAttestationWorkflowIdentity = "other-attestation-workflow"
			a.CodeSignatureObservation.ObserverWorkflowID = a.ArtifactAttestationWorkflowIdentity
		},
		"producer": func(a *MarshalArtifactBuildAttestationV1) {
			a.ArtifactAttestationProducerPrincipalID = "other-artifact-producer"
		},
		"cdhash": func(a *MarshalArtifactBuildAttestationV1) { a.CodeSignatureIdentity.CDHash = strings.Repeat("d", 40) },
		"team": func(a *MarshalArtifactBuildAttestationV1) {
			a.CodeSignatureIdentity.TeamIdentifier = stringPointer("TEAM123")
		},
		"requirement": func(a *MarshalArtifactBuildAttestationV1) {
			a.CodeSignatureIdentity.DesignatedRequirement = "identifier com.github.chiga0.marshal and certificate leaf[subject.OU] = TEAM123"
		},
		"leaf": func(a *MarshalArtifactBuildAttestationV1) {
			a.CodeSignatureIdentity.LeafCertificateSHA256 = stringPointer(digest("other-leaf"))
		},
		"chain": func(a *MarshalArtifactBuildAttestationV1) {
			a.CodeSignatureIdentity.CertificateChainSHA256 = stringPointer(digest("other-chain"))
		},
		"kind": func(a *MarshalArtifactBuildAttestationV1) {
			a.CodeSignatureIdentity.SignatureKind = "developer-id-application"
		},
		"hardened":  func(a *MarshalArtifactBuildAttestationV1) { a.CodeSignatureIdentity.HardenedRuntime = true },
		"timestamp": func(a *MarshalArtifactBuildAttestationV1) { a.CodeSignatureIdentity.SecureTimestamp = true },
	}
	for name, mutate := range cases {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fixture := newChainFixture(t, false)
			var attestation MarshalArtifactBuildAttestationV1
			mustUnmarshal(t, fixture.raw.BuildAttestation, &attestation)
			mutate(&attestation)
			fixture.raw.BuildAttestation = signAttestation(t, attestation, fixture.attestKey)
			if _, err := mustValidator(t).ValidateArtifactChain(fixture.raw, fixture.policy); err == nil {
				t.Fatal("validly re-signed code-sign/release fact escaped external policy")
			}
		})
	}
}

func TestRawObjectBudgets(t *testing.T) {
	t.Parallel()
	maximumCount := RawObjectSet{ExternalMaterialManifests: make([][]byte, maxExternalManifests)}
	if !withinBudgets(maximumCount) {
		t.Fatal("maximum external object count was rejected by the framing budget")
	}
	maximumCount.ExternalMaterialManifests = append(maximumCount.ExternalMaterialManifests, nil)
	if withinBudgets(maximumCount) {
		t.Fatal("external object count above maximum was accepted")
	}
	block := make([]byte, maxObjectBytes)
	boundary := RawObjectSet{SourceManifest: block, CompileRootManifest: block, BuildRecord: block, BuildAttestation: block, ExternalMaterialManifests: [][]byte{block, block, block, block}}
	if !withinBudgets(boundary) {
		t.Fatal("exact aggregate byte boundary was rejected")
	}
	boundary.ExternalMaterialManifests = append(boundary.ExternalMaterialManifests, []byte{0})
	if withinBudgets(boundary) {
		t.Fatal("aggregate bytes above maximum were accepted")
	}
}

func TestPolicyBudgetsBeforeSnapshot(t *testing.T) {
	t.Parallel()
	fixture := newChainFixture(t, false)
	usage, ok := measurePolicyUsage(fixture.policy, productionPolicyBudgetLimits)
	if !ok || !withinPolicyBudgets(fixture.policy) {
		t.Fatal("valid policy did not fit production budgets")
	}
	cases := map[string]func(*policyBudgetLimits){
		"external-manifests": func(l *policyBudgetLimits) {
			l.ExternalManifests = uint64(len(fixture.policy.ExpectedExternalMaterials) - 1)
		},
		"entries-per-manifest": func(l *policyBudgetLimits) { l.EntriesPerManifest = 1 },
		"refs-per-entry":       func(l *policyBudgetLimits) { l.ReferencesPerEntry = 1 },
		"aggregate-entries":    func(l *policyBudgetLimits) { l.AggregateEntries = usage.Entries - 1 },
		"aggregate-references": func(l *policyBudgetLimits) { l.AggregateReferences = usage.References - 1 },
		"aggregate-bytes":      func(l *policyBudgetLimits) { l.AggregateBytes = usage.Bytes - 1 },
		"keys":                 func(l *policyBudgetLimits) { l.KeysPerAuthority = 0 },
		"per-key-bytes":        func(l *policyBudgetLimits) { l.PublicKeyBytes = ed25519.PublicKeySize - 1 },
		"all-key-bytes":        func(l *policyBudgetLimits) { l.AggregatePublicKeyBytes = usage.PublicKeyBytes - 1 },
	}
	for name, shrink := range cases {
		name, shrink := name, shrink
		t.Run(name, func(t *testing.T) {
			limits := productionPolicyBudgetLimits
			shrink(&limits)
			if withinPolicyBudgetsWithLimits(fixture.policy, limits) {
				t.Fatal("policy above pre-copy budget was accepted")
			}
		})
	}
	boundary := productionPolicyBudgetLimits
	boundary.AggregateEntries = usage.Entries
	boundary.AggregateReferences = usage.References
	boundary.AggregateBytes = usage.Bytes
	boundary.ExternalManifests = uint64(len(fixture.policy.ExpectedExternalMaterials))
	boundary.EntriesPerManifest = 2
	boundary.ReferencesPerEntry = 2
	boundary.KeysPerAuthority = 1
	boundary.PublicKeyBytes = ed25519.PublicKeySize
	boundary.AggregatePublicKeyBytes = usage.PublicKeyBytes
	if !withinPolicyBudgetsWithLimits(fixture.policy, boundary) {
		t.Fatal("exact policy budget boundary was rejected")
	}
	overflow := ^uint64(0)
	if addBounded(&overflow, 1, ^uint64(0)) {
		t.Fatal("overflowing policy byte addition was accepted")
	}
	submodules := fixture.policy
	submodules.ExpectedSubmodules = []SubmoduleV1{{Path: "one"}, {Path: "two"}}
	limits := productionPolicyBudgetLimits
	limits.Submodules = 1
	if withinPolicyBudgetsWithLimits(submodules, limits) {
		t.Fatal("submodule count above schema budget was accepted")
	}
	lfs := fixture.policy
	lfs.ExpectedLFSObjects = []LFSObjectV1{{Path: "one"}, {Path: "two"}}
	limits = productionPolicyBudgetLimits
	limits.LFSObjects = 1
	if withinPolicyBudgetsWithLimits(lfs, limits) {
		t.Fatal("LFS count above schema budget was accepted")
	}
}

func newChainFixture(t *testing.T, generated bool) chainFixture {
	t.Helper()
	now := time.Date(2026, 8, 26, 1, 0, 0, 0, time.UTC)
	recordKey := ed25519.NewKeyFromSeed(bytesOf(1, ed25519.SeedSize))
	attestKey := ed25519.NewKeyFromSeed(bytesOf(2, ed25519.SeedSize))
	repository, sourceHead := "github.com/chiga0/marshal-harness", strings.Repeat("a", 40)
	stageID := "generated-1"
	moduleGraphDigest := digest("module-graph")
	invocationDigest := digest("build-invocation")
	environmentPolicyDigest := digest("environment-policy")
	mainHash := digest("package main")
	entries := []ManifestEntryV1{{Path: "main.go", EntryType: "regular", Mode: 0644, Length: 12, SHA256: &mainHash}, {Path: "pkg", EntryType: "directory", Mode: 0755}}
	toolchainReferences := []string{"build-invocation"}
	if generated {
		toolchainReferences = append(toolchainReferences, "generated-stage")
		sortStrings(toolchainReferences)
	}
	toolchain := ExternalBuildMaterialManifestV1{SchemaVersion: ExternalMaterialSchema, MaterialSetID: "go-1.26.6", MaterialKind: "go-toolchain", ProducerObservationIdentity: "source-producer-1", PolicyDigest: environmentPolicyDigest, Entries: []ExternalMaterialEntryV1{{LogicalIdentity: "go-toolchain:1.26.6", Path: "bin/go", EntryType: "regular", Mode: 0755, Length: 100, SHA256: stringPointer(digest("go-toolchain")), SourceIdentity: "go.dev/dl/go1.26.6", ReferencedBy: toolchainReferences}}}
	toolchainRaw := marshalWithDigest(t, toolchain, "manifestDigest")
	mustUnmarshal(t, toolchainRaw, &toolchain)
	module := ExternalBuildMaterialManifestV1{SchemaVersion: ExternalMaterialSchema, MaterialSetID: "module-jcs-1.0.1", MaterialKind: "go-module-source", ProducerObservationIdentity: "source-producer-1", PolicyDigest: environmentPolicyDigest, Entries: []ExternalMaterialEntryV1{
		{LogicalIdentity: "module:github.com/gowebpki/jcs@v1.0.1", Path: "github.com/gowebpki/jcs@v1.0.1/jcs.go", EntryType: "regular", Mode: 0644, Length: 200, SHA256: stringPointer(digest("module-source")), SourceIdentity: "proxy.golang.org/github.com/gowebpki/jcs@v1.0.1", ReferencedBy: []string{"build-invocation", "module-graph"}},
		{LogicalIdentity: "module:github.com/gowebpki/jcs@v1.0.1:embed", Path: "github.com/gowebpki/jcs@v1.0.1/testdata/cert.pem", EntryType: "regular", Mode: 0644, Length: 80, SHA256: stringPointer(digest("module-embed")), SourceIdentity: "proxy.golang.org/github.com/gowebpki/jcs@v1.0.1", ReferencedBy: []string{"build-invocation"}},
	}}
	moduleRaw := marshalWithDigest(t, module, "manifestDigest")
	mustUnmarshal(t, moduleRaw, &module)
	var generator ExternalBuildMaterialManifestV1
	if generated {
		generator = ExternalBuildMaterialManifestV1{SchemaVersion: ExternalMaterialSchema, MaterialSetID: "generator-1", MaterialKind: "generator-tool", ProducerObservationIdentity: "source-producer-1", PolicyDigest: environmentPolicyDigest, Entries: []ExternalMaterialEntryV1{{LogicalIdentity: "generator:one", Path: "bin/generator", EntryType: "regular", Mode: 0755, Length: 50, SHA256: stringPointer(digest("generator")), SourceIdentity: "generator-release-1", ReferencedBy: []string{"build-invocation", "generated-stage"}}}}
		generatorRaw := marshalWithDigest(t, generator, "manifestDigest")
		mustUnmarshal(t, generatorRaw, &generator)
	}
	externalRaw := [][]byte{moduleRaw, toolchainRaw}
	externalDigests := []string{module.ManifestDigest, toolchain.ManifestDigest}
	if generated {
		externalRaw = append(externalRaw, marshalWithDigest(t, generator, "manifestDigest"))
		externalDigests = append(externalDigests, generator.ManifestDigest)
	}
	sortStrings(externalDigests)
	source := SourceManifestV1{SchemaVersion: SourceManifestSchema, ManifestID: "source-1", Repository: repository, SourceHead: sourceHead, GitObjectFormat: "sha1", SourceBundleDigest: digest("bundle"), Entries: entries, RootDigest: digestOf(entries), SubmodulePolicyDigest: digest("no-submodules"), Submodules: []SubmoduleV1{}, LFSPolicyDigest: digest("no-lfs"), LFSObjects: []LFSObjectV1{}, GoModDigest: stringPointer(digest("go.mod")), GoSumDigest: stringPointer(digest("go.sum")), DependencyMode: "modules", ModuleGraphDigest: moduleGraphDigest, BuildInvocationDigest: invocationDigest, EnvironmentPolicyDigest: environmentPolicyDigest, ToolchainDistributionDigest: toolchain.ManifestDigest, GoVersion: "go1.26.6", TargetOS: "darwin", TargetArch: "arm64", BuildProfile: "darwin-managed-development", ProducerObservationIdentity: "source-producer-1"}
	if generated {
		source.GeneratedSourcePolicy = &GeneratorPolicyV1{GeneratedStageID: stageID, GeneratorMaterialDigest: generator.ManifestDigest, GeneratorToolchainDigest: toolchain.ManifestDigest, GeneratorInvocationDigest: digest("generator-invocation"), GeneratorInputDigest: digest("generator-input")}
	}
	sourceRaw := marshalWithDigest(t, source, "manifestDigest")
	mustUnmarshal(t, sourceRaw, &source)
	compileEntries := append([]ManifestEntryV1(nil), entries...)
	compile := CompileRootManifestV1{SchemaVersion: CompileRootManifestSchema, ManifestID: "compile-1", Repository: source.Repository, SourceHead: source.SourceHead, SourceManifestDigest: source.ManifestDigest, Entries: compileEntries, RootDigest: digestOf(compileEntries), ProducerObservationIdentity: "compile-producer-1"}
	var stageRaw []byte
	if generated {
		generatedHash := digest("generated bytes")
		compileEntries = []ManifestEntryV1{{Path: "generated.go", EntryType: "regular", Mode: 0644, Length: 15, SHA256: &generatedHash}, entries[0], entries[1]}
		compile.Entries, compile.RootDigest = compileEntries, digestOf(compileEntries)
		source.GeneratedSourcePolicy.GeneratorOutputManifestDigest = compile.RootDigest
		sourceRaw = marshalWithDigest(t, source, "manifestDigest")
		mustUnmarshal(t, sourceRaw, &source)
		compile.SourceManifestDigest = source.ManifestDigest
		stage := GeneratedSourceStageV1{SchemaVersion: GeneratedSourceStageSchema, StageID: stageID, SourceManifestDigest: source.ManifestDigest, GeneratorMaterialDigest: source.GeneratedSourcePolicy.GeneratorMaterialDigest, GeneratorToolchainDigest: source.GeneratedSourcePolicy.GeneratorToolchainDigest, GeneratorInvocationDigest: source.GeneratedSourcePolicy.GeneratorInvocationDigest, GeneratorInputDigest: source.GeneratedSourcePolicy.GeneratorInputDigest, Entries: compileEntries, RootDigest: compile.RootDigest, ProducerObservationIdentity: "generator-workflow-1"}
		stageRaw = marshalWithDigest(t, stage, "stageDigest")
		mustUnmarshal(t, stageRaw, &stage)
		compile.GeneratedSourceStageDigest = &stage.StageDigest
	}
	compileRaw := marshalWithDigest(t, compile, "manifestDigest")
	mustUnmarshal(t, compileRaw, &compile)
	artifact := ArtifactV1{RawSHA256: digest("unsigned"), FileSize: 1000, GoBuildID: "build-id-1", OS: "darwin", Arch: "arm64", Version: "v1.0.0", BuildDate: now.Format(time.RFC3339Nano)}
	record := MarshalArtifactBuildRecordV1{SchemaVersion: BuildRecordSchema, RecordID: "record-1", CreatedAt: now.Format(time.RFC3339Nano), BuildProfile: source.BuildProfile, Repository: source.Repository, SourceHead: source.SourceHead, SourceBundleDigest: source.SourceBundleDigest, SourceManifestDigest: source.ManifestDigest, CompileRootManifestDigest: compile.ManifestDigest, ExternalMaterialManifestDigests: externalDigests, BuildInvocationDigest: source.BuildInvocationDigest, EnvironmentPolicyDigest: source.EnvironmentPolicyDigest, ToolchainMaterialDigest: toolchain.ManifestDigest, ModuleGraphDigest: source.ModuleGraphDigest, BuilderPrincipalID: "builder-principal", BuilderWorkflowIdentity: "builder-workflow-1", BuilderIsolationProfile: "protected-linux-builder-v1", UnsignedArtifact: artifact}
	recordRaw := signRecord(t, record, recordKey)
	mustUnmarshal(t, recordRaw, &record)
	final := artifact
	final.RawSHA256, final.FileSize = digest("signed-final"), 1200
	leaf, chain := digest("leaf-cert"), digest("cert-chain")
	attestation := MarshalArtifactBuildAttestationV1{SchemaVersion: BuildAttestationSchema, AttestationID: "attestation-1", IssuedAt: now.Add(time.Minute).Format(time.RFC3339Nano), BuildProfile: record.BuildProfile, Repository: record.Repository, SourceHead: record.SourceHead, SourceBundleDigest: record.SourceBundleDigest, SourceManifestDigest: record.SourceManifestDigest, CompileRootManifestDigest: record.CompileRootManifestDigest, BuildRecordDigest: record.RecordDigest, SubmodulePolicyDigest: source.SubmodulePolicyDigest, LFSPolicyDigest: source.LFSPolicyDigest, GeneratedSourceStageDigest: compile.GeneratedSourceStageDigest, BuildInvocationDigest: record.BuildInvocationDigest, EnvironmentPolicyDigest: record.EnvironmentPolicyDigest, ExternalMaterialManifestDigests: record.ExternalMaterialManifestDigests, ToolchainMaterialDigest: record.ToolchainMaterialDigest, ModuleGraphDigest: record.ModuleGraphDigest, BuilderPrincipalID: record.BuilderPrincipalID, BuilderWorkflowIdentity: record.BuilderWorkflowIdentity, BuilderIsolationProfile: record.BuilderIsolationProfile, ArtifactAttestationProducerPrincipalID: "artifact-principal", CodeSigningWorkflowIdentity: "code-sign-workflow-1", ArtifactAttestationWorkflowIdentity: "artifact-observer-workflow-1", UnsignedArtifact: artifact, FinalArtifact: final, CodeSignatureIdentity: CodeSignatureIdentityV1{SignatureKind: "managed-development", Identifier: MarshalBinaryIdentifier, CDHash: strings.Repeat("c", 40), DesignatedRequirement: "identifier com.github.chiga0.marshal", LeafCertificateSHA256: &leaf, CertificateChainSHA256: &chain}, CodeSignatureObservation: CodeSignatureObservationV1{ObservedFinalRawSHA256: final.RawSHA256, ObservedFileSize: final.FileSize, ObservedAt: now.Add(time.Minute).Format(time.RFC3339Nano), ObserverWorkflowID: "artifact-observer-workflow-1"}}
	attestationRaw := signAttestation(t, attestation, attestKey)
	expectedExternal := map[string]ExternalMaterialExpectation{module.ManifestDigest: expectationFor(module), toolchain.ManifestDigest: expectationFor(toolchain)}
	if generated {
		expectedExternal[generator.ManifestDigest] = expectationFor(generator)
	}
	policy := ValidationPolicy{
		ExpectedRepository: source.Repository, ExpectedSourceHead: source.SourceHead, ExpectedBuildProfile: source.BuildProfile,
		ExpectedSourceBundleDigest: source.SourceBundleDigest, ExpectedSourceManifestDigest: source.ManifestDigest, ExpectedCompileRootManifestDigest: compile.ManifestDigest,
		ExpectedGoModDigest: stringPointer(*source.GoModDigest), ExpectedGoSumDigest: stringPointer(*source.GoSumDigest),
		ExpectedDependencyMode: source.DependencyMode, ExpectedModuleGraphDigest: source.ModuleGraphDigest, ExpectedBuildInvocationDigest: source.BuildInvocationDigest,
		ExpectedEnvironmentPolicyDigest: source.EnvironmentPolicyDigest, ExpectedToolchainMaterialDigest: toolchain.ManifestDigest,
		ExpectedTargetArch: source.TargetArch, ExpectedGoVersion: source.GoVersion, ExpectedSubmodulePolicyDigest: source.SubmodulePolicyDigest,
		ExpectedLFSPolicyDigest: source.LFSPolicyDigest, ExpectedSubmodules: append([]SubmoduleV1{}, source.Submodules...), ExpectedLFSObjects: append([]LFSObjectV1{}, source.LFSObjects...), ExpectedExternalMaterials: expectedExternal, ExpectedGenerated: generated,
		ExpectedBuilderPrincipalID: record.BuilderPrincipalID, ExpectedBuilderWorkflowIdentity: record.BuilderWorkflowIdentity, ExpectedBuilderIsolationProfile: record.BuilderIsolationProfile,
		ExpectedArtifactAttestationProducerPrincipalID: attestation.ArtifactAttestationProducerPrincipalID, ExpectedCodeSigningWorkflowIdentity: attestation.CodeSigningWorkflowIdentity,
		ExpectedArtifactAttestationWorkflowIdentity: attestation.ArtifactAttestationWorkflowIdentity, ExpectedCodeSignatureIdentity: snapshotCodeSignatureIdentity(attestation.CodeSignatureIdentity),
	}
	if generated {
		var stage GeneratedSourceStageV1
		mustUnmarshal(t, stageRaw, &stage)
		policy.ExpectedGeneratedStageDigest = stage.StageDigest
		policy.ExpectedGeneratorInvocationDigest = stage.GeneratorInvocationDigest
		policy.ExpectedGeneratorInputDigest = stage.GeneratorInputDigest
		policy.ExpectedGeneratorMaterialDigest = stage.GeneratorMaterialDigest
		policy.ExpectedGeneratorToolchainDigest = stage.GeneratorToolchainDigest
	}
	policy.Trust = TrustPolicies{
		BuildRecord:      CurrentKeyPolicy{ProducerPrincipalID: record.BuilderPrincipalID, CurrentKeyEpoch: 7, Keys: []KeyRecord{{KeyID: "builder-key", KeyEpoch: 7, Usage: BuildRecordKeyUsage, PublicKey: recordKey.Public().(ed25519.PublicKey), ValidFrom: now.Add(-time.Hour), ValidUntil: now.Add(time.Hour)}}},
		BuildAttestation: CurrentKeyPolicy{ProducerPrincipalID: attestation.ArtifactAttestationProducerPrincipalID, CurrentKeyEpoch: 11, Keys: []KeyRecord{{KeyID: "artifact-key", KeyEpoch: 11, Usage: BuildAttestationUsage, PublicKey: attestKey.Public().(ed25519.PublicKey), ValidFrom: now.Add(-time.Hour), ValidUntil: now.Add(time.Hour)}}},
	}
	return chainFixture{
		raw:       RawObjectSet{SourceManifest: sourceRaw, CompileRootManifest: compileRaw, GeneratedSourceStage: stageRaw, ExternalMaterialManifests: externalRaw, BuildRecord: recordRaw, BuildAttestation: attestationRaw},
		policy:    policy,
		recordKey: recordKey,
		attestKey: attestKey,
	}
}

func rebindExternalChain(t *testing.T, fixture *chainFixture, _ bool) {
	t.Helper()
	var source SourceManifestV1
	var compile CompileRootManifestV1
	var record MarshalArtifactBuildRecordV1
	var attestation MarshalArtifactBuildAttestationV1
	var stage GeneratedSourceStageV1
	mustUnmarshal(t, fixture.raw.SourceManifest, &source)
	mustUnmarshal(t, fixture.raw.CompileRootManifest, &compile)
	mustUnmarshal(t, fixture.raw.BuildRecord, &record)
	mustUnmarshal(t, fixture.raw.BuildAttestation, &attestation)
	generated := len(fixture.raw.GeneratedSourceStage) > 0
	if generated {
		mustUnmarshal(t, fixture.raw.GeneratedSourceStage, &stage)
	}
	externalDigests := make([]string, 0, len(fixture.raw.ExternalMaterialManifests))
	toolchainDigest := ""
	generatorDigest := ""
	for _, raw := range fixture.raw.ExternalMaterialManifests {
		var material ExternalBuildMaterialManifestV1
		mustUnmarshal(t, raw, &material)
		externalDigests = append(externalDigests, material.ManifestDigest)
		switch material.MaterialKind {
		case "go-toolchain":
			toolchainDigest = material.ManifestDigest
		case "generator-tool":
			generatorDigest = material.ManifestDigest
		}
	}
	sortStrings(externalDigests)
	source.ToolchainDistributionDigest = toolchainDigest
	if generated {
		source.GeneratedSourcePolicy.GeneratorMaterialDigest = generatorDigest
		source.GeneratedSourcePolicy.GeneratorToolchainDigest = toolchainDigest
	}
	fixture.raw.SourceManifest = marshalWithDigest(t, source, "manifestDigest")
	mustUnmarshal(t, fixture.raw.SourceManifest, &source)
	compile.SourceManifestDigest = source.ManifestDigest
	if generated {
		stage.SourceManifestDigest = source.ManifestDigest
		stage.GeneratorMaterialDigest = generatorDigest
		stage.GeneratorToolchainDigest = toolchainDigest
		fixture.raw.GeneratedSourceStage = marshalWithDigest(t, stage, "stageDigest")
		mustUnmarshal(t, fixture.raw.GeneratedSourceStage, &stage)
		compile.GeneratedSourceStageDigest = &stage.StageDigest
		compile.Entries = stage.Entries
		compile.RootDigest = stage.RootDigest
	} else {
		compile.GeneratedSourceStageDigest = nil
		compile.Entries = source.Entries
		compile.RootDigest = source.RootDigest
	}
	fixture.raw.CompileRootManifest = marshalWithDigest(t, compile, "manifestDigest")
	mustUnmarshal(t, fixture.raw.CompileRootManifest, &compile)
	record.SourceBundleDigest = source.SourceBundleDigest
	record.SourceManifestDigest = source.ManifestDigest
	record.CompileRootManifestDigest = compile.ManifestDigest
	record.ExternalMaterialManifestDigests = externalDigests
	record.ToolchainMaterialDigest = toolchainDigest
	fixture.raw.BuildRecord = signRecord(t, record, fixture.recordKey)
	mustUnmarshal(t, fixture.raw.BuildRecord, &record)
	attestation.SourceBundleDigest = record.SourceBundleDigest
	attestation.SourceManifestDigest = record.SourceManifestDigest
	attestation.CompileRootManifestDigest = record.CompileRootManifestDigest
	attestation.BuildRecordDigest = record.RecordDigest
	attestation.ExternalMaterialManifestDigests = record.ExternalMaterialManifestDigests
	attestation.BuildInvocationDigest = record.BuildInvocationDigest
	attestation.EnvironmentPolicyDigest = record.EnvironmentPolicyDigest
	attestation.ToolchainMaterialDigest = record.ToolchainMaterialDigest
	attestation.ModuleGraphDigest = record.ModuleGraphDigest
	attestation.GeneratedSourceStageDigest = compile.GeneratedSourceStageDigest
	fixture.raw.BuildAttestation = signAttestation(t, attestation, fixture.attestKey)
}

func resignFromSource(t *testing.T, fixture *chainFixture, source SourceManifestV1) {
	t.Helper()
	var compile CompileRootManifestV1
	var record MarshalArtifactBuildRecordV1
	var attestation MarshalArtifactBuildAttestationV1
	mustUnmarshal(t, fixture.raw.CompileRootManifest, &compile)
	mustUnmarshal(t, fixture.raw.BuildRecord, &record)
	mustUnmarshal(t, fixture.raw.BuildAttestation, &attestation)
	fixture.raw.SourceManifest = marshalWithDigest(t, source, "manifestDigest")
	mustUnmarshal(t, fixture.raw.SourceManifest, &source)
	compile.SourceManifestDigest = source.ManifestDigest
	if len(fixture.raw.GeneratedSourceStage) == 0 {
		compile.GeneratedSourceStageDigest = nil
		compile.Entries = source.Entries
		compile.RootDigest = source.RootDigest
	}
	fixture.raw.CompileRootManifest = marshalWithDigest(t, compile, "manifestDigest")
	mustUnmarshal(t, fixture.raw.CompileRootManifest, &compile)
	record.SourceBundleDigest = source.SourceBundleDigest
	record.SourceManifestDigest = source.ManifestDigest
	record.CompileRootManifestDigest = compile.ManifestDigest
	record.BuildInvocationDigest = source.BuildInvocationDigest
	record.EnvironmentPolicyDigest = source.EnvironmentPolicyDigest
	record.ToolchainMaterialDigest = source.ToolchainDistributionDigest
	record.ModuleGraphDigest = source.ModuleGraphDigest
	fixture.raw.BuildRecord = signRecord(t, record, fixture.recordKey)
	mustUnmarshal(t, fixture.raw.BuildRecord, &record)
	attestation.SourceBundleDigest = record.SourceBundleDigest
	attestation.SourceManifestDigest = source.ManifestDigest
	attestation.CompileRootManifestDigest = compile.ManifestDigest
	attestation.BuildRecordDigest = record.RecordDigest
	attestation.SubmodulePolicyDigest = source.SubmodulePolicyDigest
	attestation.LFSPolicyDigest = source.LFSPolicyDigest
	attestation.BuildInvocationDigest = record.BuildInvocationDigest
	attestation.EnvironmentPolicyDigest = record.EnvironmentPolicyDigest
	attestation.ToolchainMaterialDigest = record.ToolchainMaterialDigest
	attestation.ModuleGraphDigest = record.ModuleGraphDigest
	fixture.raw.BuildAttestation = signAttestation(t, attestation, fixture.attestKey)
}

func adoptObjectDigestsOnly(t *testing.T, fixture *chainFixture) {
	t.Helper()
	var source SourceManifestV1
	var compile CompileRootManifestV1
	mustUnmarshal(t, fixture.raw.SourceManifest, &source)
	mustUnmarshal(t, fixture.raw.CompileRootManifest, &compile)
	fixture.policy.ExpectedSourceBundleDigest = source.SourceBundleDigest
	fixture.policy.ExpectedSourceManifestDigest = source.ManifestDigest
	fixture.policy.ExpectedCompileRootManifestDigest = compile.ManifestDigest
}

func adoptObservedPolicy(t *testing.T, fixture *chainFixture) {
	t.Helper()
	var source SourceManifestV1
	var compile CompileRootManifestV1
	mustUnmarshal(t, fixture.raw.SourceManifest, &source)
	mustUnmarshal(t, fixture.raw.CompileRootManifest, &compile)
	p := &fixture.policy
	p.ExpectedRepository = source.Repository
	p.ExpectedSourceHead = source.SourceHead
	p.ExpectedBuildProfile = source.BuildProfile
	p.ExpectedSourceBundleDigest = source.SourceBundleDigest
	p.ExpectedSourceManifestDigest = source.ManifestDigest
	p.ExpectedCompileRootManifestDigest = compile.ManifestDigest
	p.ExpectedGoModDigest = cloneStringPointer(source.GoModDigest)
	p.ExpectedGoSumDigest = cloneStringPointer(source.GoSumDigest)
	p.ExpectedDependencyMode = source.DependencyMode
	p.ExpectedModuleGraphDigest = source.ModuleGraphDigest
	p.ExpectedBuildInvocationDigest = source.BuildInvocationDigest
	p.ExpectedEnvironmentPolicyDigest = source.EnvironmentPolicyDigest
	p.ExpectedToolchainMaterialDigest = source.ToolchainDistributionDigest
	p.ExpectedTargetArch = source.TargetArch
	p.ExpectedGoVersion = source.GoVersion
	p.ExpectedSubmodulePolicyDigest = source.SubmodulePolicyDigest
	p.ExpectedLFSPolicyDigest = source.LFSPolicyDigest
	p.ExpectedSubmodules = append([]SubmoduleV1{}, source.Submodules...)
	p.ExpectedLFSObjects = append([]LFSObjectV1{}, source.LFSObjects...)
	p.ExpectedExternalMaterials = make(map[string]ExternalMaterialExpectation, len(fixture.raw.ExternalMaterialManifests))
	for _, raw := range fixture.raw.ExternalMaterialManifests {
		var material ExternalBuildMaterialManifestV1
		mustUnmarshal(t, raw, &material)
		p.ExpectedExternalMaterials[material.ManifestDigest] = expectationFor(material)
	}
	p.ExpectedGenerated = len(fixture.raw.GeneratedSourceStage) > 0
	p.ExpectedGeneratedStageDigest = ""
	p.ExpectedGeneratorInvocationDigest = ""
	p.ExpectedGeneratorInputDigest = ""
	p.ExpectedGeneratorMaterialDigest = ""
	p.ExpectedGeneratorToolchainDigest = ""
	if p.ExpectedGenerated {
		var stage GeneratedSourceStageV1
		mustUnmarshal(t, fixture.raw.GeneratedSourceStage, &stage)
		p.ExpectedGeneratedStageDigest = stage.StageDigest
		p.ExpectedGeneratorInvocationDigest = stage.GeneratorInvocationDigest
		p.ExpectedGeneratorInputDigest = stage.GeneratorInputDigest
		p.ExpectedGeneratorMaterialDigest = stage.GeneratorMaterialDigest
		p.ExpectedGeneratorToolchainDigest = stage.GeneratorToolchainDigest
	}
	var record MarshalArtifactBuildRecordV1
	var attestation MarshalArtifactBuildAttestationV1
	mustUnmarshal(t, fixture.raw.BuildRecord, &record)
	mustUnmarshal(t, fixture.raw.BuildAttestation, &attestation)
	p.ExpectedBuilderPrincipalID = record.BuilderPrincipalID
	p.ExpectedBuilderWorkflowIdentity = record.BuilderWorkflowIdentity
	p.ExpectedBuilderIsolationProfile = record.BuilderIsolationProfile
	p.ExpectedArtifactAttestationProducerPrincipalID = attestation.ArtifactAttestationProducerPrincipalID
	p.ExpectedCodeSigningWorkflowIdentity = attestation.CodeSigningWorkflowIdentity
	p.ExpectedArtifactAttestationWorkflowIdentity = attestation.ArtifactAttestationWorkflowIdentity
	p.ExpectedCodeSignatureIdentity = snapshotCodeSignatureIdentity(attestation.CodeSignatureIdentity)
}

func cloneValidationPolicy(source ValidationPolicy) ValidationPolicy {
	clone := source
	clone.ExpectedGoModDigest = cloneStringPointer(source.ExpectedGoModDigest)
	clone.ExpectedGoSumDigest = cloneStringPointer(source.ExpectedGoSumDigest)
	clone.ExpectedCodeSignatureIdentity = snapshotCodeSignatureIdentity(source.ExpectedCodeSignatureIdentity)
	clone.ExpectedSubmodules = append([]SubmoduleV1{}, source.ExpectedSubmodules...)
	clone.ExpectedLFSObjects = append([]LFSObjectV1{}, source.ExpectedLFSObjects...)
	clone.ExpectedExternalMaterials = make(map[string]ExternalMaterialExpectation, len(source.ExpectedExternalMaterials))
	for digest, expectation := range source.ExpectedExternalMaterials {
		entries := make(map[string][]string, len(expectation.Entries))
		for key, references := range expectation.Entries {
			entries[key] = append([]string(nil), references...)
		}
		clone.ExpectedExternalMaterials[digest] = ExternalMaterialExpectation{MaterialKind: expectation.MaterialKind, Entries: entries}
	}
	return clone
}

func cloneStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func signRecord(t *testing.T, record MarshalArtifactBuildRecordV1, key ed25519.PrivateKey) []byte {
	t.Helper()
	record.RecordDigest = ""
	record.SignedObjectEnvelope = authorityprovider.SignedObjectEnvelopeV1{}
	unsigned := mustCanonical(t, record)
	record.RecordDigest = digestExcluding(unsigned, "recordDigest", "signedObjectEnvelope")
	record.SignedObjectEnvelope = signEnvelope(record.RecordDigest, BuildRecordDomain, "builder-key", 7, key)
	return mustCanonical(t, record)
}

func signAttestation(t *testing.T, attestation MarshalArtifactBuildAttestationV1, key ed25519.PrivateKey) []byte {
	t.Helper()
	attestation.AttestationDigest = ""
	attestation.SignedObjectEnvelope = authorityprovider.SignedObjectEnvelopeV1{}
	unsigned := mustCanonical(t, attestation)
	attestation.AttestationDigest = digestExcluding(unsigned, "attestationDigest", "signedObjectEnvelope")
	attestation.SignedObjectEnvelope = signEnvelope(attestation.AttestationDigest, BuildAttestationDomain, "artifact-key", 11, key)
	return mustCanonical(t, attestation)
}

func signEnvelope(objectDigest, domain, keyID string, epoch uint64, key ed25519.PrivateKey) authorityprovider.SignedObjectEnvelopeV1 {
	signature := ed25519.Sign(key, append([]byte(domain), []byte(objectDigest)...))
	return authorityprovider.SignedObjectEnvelopeV1{ObjectDigest: objectDigest, SignatureAlgorithm: authorityprovider.SignatureAlgorithmEd25519, SignatureEncoding: authorityprovider.SignatureEncodingBase64URL, KeyID: keyID, KeyEpoch: epoch, SignatureDomain: domain, Signature: base64.RawURLEncoding.EncodeToString(signature)}
}

func marshalWithDigest(t *testing.T, value any, digestField string) []byte {
	t.Helper()
	raw := mustCanonical(t, value)
	digestValue := digestExcluding(raw, digestField)
	var object map[string]json.RawMessage
	mustUnmarshal(t, raw, &object)
	encoded, err := json.Marshal(digestValue)
	if err != nil {
		t.Fatal(err)
	}
	object[digestField] = encoded
	return mustCanonical(t, object)
}

func mustCanonical(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	result, err := canonical.JSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func mustUnmarshal(t *testing.T, raw []byte, value any) {
	t.Helper()
	if err := json.Unmarshal(raw, value); err != nil {
		t.Fatal(err)
	}
}

func mustValidator(t *testing.T) *Validator {
	t.Helper()
	validator, err := NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	return validator
}

func digest(value string) string         { return canonical.DigestBytes([]byte(value)) }
func stringPointer(value string) *string { return &value }
func bytesOf(value byte, count int) []byte {
	return []byte(strings.Repeat(string([]byte{value}), count))
}
func sortStrings(values []string) {
	for i := range values {
		for j := i + 1; j < len(values); j++ {
			if values[j] < values[i] {
				values[i], values[j] = values[j], values[i]
			}
		}
	}
}

func expectationFor(material ExternalBuildMaterialManifestV1) ExternalMaterialExpectation {
	entries := make(map[string][]string, len(material.Entries))
	for _, entry := range material.Entries {
		entries[externalEntryKey(entry)] = append([]string(nil), entry.ReferencedBy...)
	}
	return ExternalMaterialExpectation{MaterialKind: material.MaterialKind, Entries: entries}
}
