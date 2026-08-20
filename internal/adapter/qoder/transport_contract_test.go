package qoder

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestReviewedEventContractReferenceMatchesCurrentTransportIdentity(t *testing.T) {
	path := filepath.Join("..", "..", "..", ".agents", "skills", "marshal", "references", "qoder-1.1.23-event-contract.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var reference struct {
		AdapterVersion                      string   `json:"adapterVersion"`
		EventContract                       string   `json:"eventContract"`
		WorkerResultTransportDigest         string   `json:"workerResultTransportDigest"`
		AssistantMessageIdentityField       string   `json:"assistantMessageIdentityField"`
		AssistantMessageFragmentPolicy      string   `json:"assistantMessageFragmentPolicy"`
		AssistantMessageExactReplayPolicy   string   `json:"assistantMessageExactReplayPolicy"`
		AssistantMessageConflictDisposition string   `json:"assistantMessageConflictDisposition"`
		AssistantMessageClosedOnStopReasons []string `json:"assistantMessageClosedOnStopReasons"`
		AssistantMessageCrossIDDisposition  string   `json:"assistantMessageCrossIdBeforeCloseDisposition"`
	}
	if err := json.Unmarshal(data, &reference); err != nil {
		t.Fatal(err)
	}
	if reference.AdapterVersion != adapterVersion || reference.EventContract != conformanceEventContract || reference.WorkerResultTransportDigest != expectedWorkerResultTransportDigest() {
		t.Fatalf("reference identity = %+v, want adapter=%s event=%s transport=%s", reference, adapterVersion, conformanceEventContract, expectedWorkerResultTransportDigest())
	}
	if reference.AssistantMessageIdentityField != "message.id" ||
		reference.AssistantMessageFragmentPolicy != "same-id-cumulative-distinct-tool-use" ||
		reference.AssistantMessageExactReplayPolicy != "same-id-same-tool-id-name-canonical-input-fold-once" ||
		reference.AssistantMessageConflictDisposition != "protocol-invalid-do-not-retry" ||
		reference.AssistantMessageCrossIDDisposition != "protocol-invalid-do-not-retry" ||
		len(reference.AssistantMessageClosedOnStopReasons) != 2 ||
		reference.AssistantMessageClosedOnStopReasons[0] != "tool_use" || reference.AssistantMessageClosedOnStopReasons[1] != "end_turn" {
		t.Fatalf("reference assistant fragmentation contract = %+v", reference)
	}
}

func validWorkerResultTransportObservation(now time.Time) LiveConformanceObservation {
	return LiveConformanceObservation{
		RunnerID: "independent-qoder-verifier", RunnerVersion: "1", ObservedAt: now.Add(-time.Minute), ValidUntil: now.Add(time.Hour),
		AdapterVersion: adapterVersion, Executable: "/opt/qoder/qodercli", ExecutableDigest: digest("e"), BinaryVersion: supportedBinary, QoderCLIVersion: supportedBinary,
		HostOS: runtime.GOOS, HostArch: runtime.GOARCH, HostFingerprint: digest("f"), AuthorityGeneration: 1,
		ProbeSuiteDigest: expectedProbeSuiteDigest(), ProbeArtifactDigest: digest("a"), ChallengeDigest: digest("c"), CapabilitiesDigest: expectedCapabilitiesDigest(), ProbeProfileDigest: expectedProbeProfileDigest(), ArgvDigest: expectedProbeArgvDigest(), EnvironmentDigest: expectedProbeEnvironmentDigest(), ToolPolicyDigest: expectedProbeToolPolicyDigest(), WorkerResultTransportDigest: expectedWorkerResultTransportDigest(),
		TranscriptDigest: digest("a"), ExecutionReceiptDigest: digest("b"), ExecutionReceiptDigests: []string{digest("1"), digest("2"), digest("3"), digest("4")}, ExecutionReceipts: []json.RawMessage{json.RawMessage(`{}`), json.RawMessage(`{}`), json.RawMessage(`{}`), json.RawMessage(`{}`)}, EvidenceClass: candidateEvidenceClassLive,
		ReceiptAuthorityKeyID: "receipt-root", ReceiptAuthorityPublicKeyDigest: digest("c"), VerifierKeyID: "verifier-root", VerifierPublicKeyDigest: digest("d"), VerifierSignature: base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize)),
		CredentialVerified: true, LiveProtocolVerified: true, WorkspaceWriteVerified: true, EventContract: conformanceEventContract, ProtocolVersion: qoderProtocolVersion, PermissionMode: qoderPermissionMode, TrustRootKeyID: "root-1",
	}
}

func TestWorkerResultTransportContractMutationChangesSuiteAndIsRejected(t *testing.T) {
	now := time.Now().UTC()
	base := expectedWorkerResultTransportContract()
	baseDigest := expectedWorkerResultTransportDigest()
	baseSuite := expectedProbeSuiteDigest()
	mutations := map[string]func(*workerResultTransportContract){
		"staging basename":                       func(v *workerResultTransportContract) { v.StagingBasename += ".changed" },
		"staging file type":                      func(v *workerResultTransportContract) { v.StagingFileType = "fifo" },
		"staging mode":                           func(v *workerResultTransportContract) { v.StagingMode = "0644" },
		"creation flags":                         func(v *workerResultTransportContract) { v.CreationFlags = "O_RDWR|O_CREAT" },
		"unlink before launch":                   func(v *workerResultTransportContract) { v.UnlinkBeforeLaunch = false },
		"unlinked link count":                    func(v *workerResultTransportContract) { v.UnlinkedLinkCount = 1 },
		"worker path exposure":                   func(v *workerResultTransportContract) { v.WorkerPathExposure = "staging-path" },
		"worker descriptor exposure":             func(v *workerResultTransportContract) { v.WorkerDescriptorExposure = "staging-fd" },
		"control inode relationship":             func(v *workerResultTransportContract) { v.ControlInodeRelationship = "may-alias" },
		"held directory binding":                 func(v *workerResultTransportContract) { v.HeldDirectoryBinding = "path-reopen" },
		"held inode commit":                      func(v *workerResultTransportContract) { v.HeldInodeCommit = "pre-terminal" },
		"held inode consume":                     func(v *workerResultTransportContract) { v.HeldInodeConsume = "path-read" },
		"held inode cleanup":                     func(v *workerResultTransportContract) { v.HeldInodeCleanup = "unlink-current-path" },
		"tool name":                              func(v *workerResultTransportContract) { v.ToolName = "bash" },
		"tool input contract":                    func(v *workerResultTransportContract) { v.ToolInputContract = "unknown-fields-allowed" },
		"description required":                   func(v *workerResultTransportContract) { v.ToolInputDescriptionRequired = false },
		"description authority":                  func(v *workerResultTransportContract) { v.ToolInputDescriptionAuthority = "authoritative" },
		"description min bytes":                  func(v *workerResultTransportContract) { v.ToolInputDescriptionMinBytes++ },
		"description max bytes":                  func(v *workerResultTransportContract) { v.ToolInputDescriptionMaxBytes++ },
		"description utf8":                       func(v *workerResultTransportContract) { v.ToolInputDescriptionUTF8Required = false },
		"description controls":                   func(v *workerResultTransportContract) { v.ToolInputDescriptionControls = "allowed" },
		"canonical member order":                 func(v *workerResultTransportContract) { v.ToolInputCanonicalMemberOrder = "description,command" },
		"unknown members":                        func(v *workerResultTransportContract) { v.ToolInputUnknownMembers = "allowed" },
		"canonical command":                      func(v *workerResultTransportContract) { v.CanonicalCommand += "\n" },
		"tee sequence":                           func(v *workerResultTransportContract) { v.TeeSequence = "at-least-once" },
		"declaration runtime metadata authority": func(v *workerResultTransportContract) { v.DeclarationRuntimeMetadataAuthority = "worker-authoritative" },
		"declaration semantic synthesis":         func(v *workerResultTransportContract) { v.DeclarationSemanticSynthesis = "allowed" },
		"declaration identity binding":           func(v *workerResultTransportContract) { v.DeclarationIdentityBinding = "adapter-synthesized" },
		"invalid declaration disposition":        func(v *workerResultTransportContract) { v.InvalidDeclarationDisposition = "retryable" },
		"denial extractor":                       func(v *workerResultTransportContract) { v.DenialExtractor += "-changed" },
		"transcript event contract":              func(v *workerResultTransportContract) { v.TranscriptEventContract = "qoder-stream-json-1.2.0-v3" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := base
			mutate(&changed)
			changedDigest := workerResultTransportContractDigest(changed)
			if changedDigest == baseDigest {
				t.Fatal("transport field mutation did not change its digest")
			}
			changedSuite := probeSuiteDigestForWorkerResultTransport(changedDigest)
			if changedSuite == baseSuite {
				t.Fatal("transport field mutation did not change the suite digest")
			}
			observation := validWorkerResultTransportObservation(now)
			observation.WorkerResultTransportDigest = changedDigest
			observation.ProbeSuiteDigest = changedSuite
			if err := validateLiveConformanceObservation(observation, now); err == nil {
				t.Fatal("mutated WorkerResult transport contract was accepted")
			}
		})
	}
}

func TestOldQoderConformanceIdentityIsRejected(t *testing.T) {
	now := time.Now().UTC()
	for name, mutate := range map[string]func(*LiveConformanceObservation){
		"adapter 0.1.2":     func(v *LiveConformanceObservation) { v.AdapterVersion = "0.1.2" },
		"adapter 0.1.3":     func(v *LiveConformanceObservation) { v.AdapterVersion = "0.1.3" },
		"adapter 0.1.4":     func(v *LiveConformanceObservation) { v.AdapterVersion = "0.1.4" },
		"adapter 0.1.5":     func(v *LiveConformanceObservation) { v.AdapterVersion = "0.1.5" },
		"event contract v3": func(v *LiveConformanceObservation) { v.EventContract = "qoder-stream-json-1.2.0-v3" },
		"event contract v4": func(v *LiveConformanceObservation) { v.EventContract = "qoder-stream-json-1.2.0-v4" },
		"event contract v5": func(v *LiveConformanceObservation) { v.EventContract = "qoder-stream-json-1.2.0-v5" },
		"event contract v6": func(v *LiveConformanceObservation) { v.EventContract = "qoder-stream-json-1.2.0-v6" },
	} {
		t.Run("observation "+name, func(t *testing.T) {
			observation := validWorkerResultTransportObservation(now)
			mutate(&observation)
			if err := validateLiveConformanceObservation(observation, now); err == nil {
				t.Fatal("old conformance observation identity was accepted")
			}
		})
	}

	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	observation := validWorkerResultTransportObservation(now)
	evidence := ConformanceEvidence{
		RunnerID: observation.RunnerID, RunnerVersion: observation.RunnerVersion, ObservedAt: observation.ObservedAt.Format(time.RFC3339Nano), ValidUntil: observation.ValidUntil.Format(time.RFC3339Nano),
		AdapterVersion: observation.AdapterVersion, Executable: observation.Executable, ExecutableDigest: observation.ExecutableDigest, BinaryVersion: observation.BinaryVersion, HostOS: observation.HostOS, HostArch: observation.HostArch, HostFingerprint: observation.HostFingerprint, AuthorityGeneration: observation.AuthorityGeneration,
		ProbeSuiteDigest: observation.ProbeSuiteDigest, ProbeArtifactDigest: observation.ProbeArtifactDigest, ChallengeDigest: observation.ChallengeDigest, CapabilitiesDigest: observation.CapabilitiesDigest, ProbeProfileDigest: observation.ProbeProfileDigest, ArgvDigest: observation.ArgvDigest, EnvironmentDigest: observation.EnvironmentDigest, ToolPolicyDigest: observation.ToolPolicyDigest, WorkerResultTransportDigest: observation.WorkerResultTransportDigest,
		TranscriptDigest: observation.TranscriptDigest, ExecutionReceiptDigest: observation.ExecutionReceiptDigest, ExecutionReceiptDigests: append([]string(nil), observation.ExecutionReceiptDigests...), EvidenceClass: observation.EvidenceClass, ReceiptAuthorityKeyID: observation.ReceiptAuthorityKeyID, ReceiptAuthorityPublicKeyDigest: observation.ReceiptAuthorityPublicKeyDigest, VerifierKeyID: observation.VerifierKeyID, VerifierPublicKeyDigest: observation.VerifierPublicKeyDigest,
		CredentialVerified: true, LiveProtocolVerified: true, WorkspaceWriteVerified: true, EventContract: observation.EventContract, QoderCLIVersion: observation.QoderCLIVersion, ProtocolVersion: observation.ProtocolVersion, PermissionMode: observation.PermissionMode, TrustRootKeyID: "root-1",
	}
	sign := func(value *ConformanceEvidence) {
		value.EvidenceDigest, err = value.digest()
		if err != nil {
			t.Fatal(err)
		}
		message, signingErr := value.signingBytes()
		if signingErr != nil {
			t.Fatal(signingErr)
		}
		value.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, message))
	}
	sign(&evidence)
	if err := evidence.validate(now, map[string]ed25519.PublicKey{"root-1": publicKey}); err != nil {
		t.Fatalf("current conformance evidence rejected: %v", err)
	}
	for name, mutate := range map[string]func(*ConformanceEvidence){
		"adapter 0.1.2":     func(v *ConformanceEvidence) { v.AdapterVersion = "0.1.2" },
		"adapter 0.1.3":     func(v *ConformanceEvidence) { v.AdapterVersion = "0.1.3" },
		"adapter 0.1.4":     func(v *ConformanceEvidence) { v.AdapterVersion = "0.1.4" },
		"adapter 0.1.5":     func(v *ConformanceEvidence) { v.AdapterVersion = "0.1.5" },
		"event contract v3": func(v *ConformanceEvidence) { v.EventContract = "qoder-stream-json-1.2.0-v3" },
		"event contract v4": func(v *ConformanceEvidence) { v.EventContract = "qoder-stream-json-1.2.0-v4" },
		"event contract v5": func(v *ConformanceEvidence) { v.EventContract = "qoder-stream-json-1.2.0-v5" },
		"event contract v6": func(v *ConformanceEvidence) { v.EventContract = "qoder-stream-json-1.2.0-v6" },
		"v4 transport identity": func(v *ConformanceEvidence) {
			v.AdapterVersion = "0.1.3"
			v.EventContract = "qoder-stream-json-1.2.0-v4"
			v.ProbeProfileDigest = probeProfileDigestForEventContract(v.EventContract)
			v.WorkerResultTransportDigest = "sha256:ee5a504d0757447c83d8e7c9dc58ae7985791747e7f591c0f803276e5203ffd7"
			v.ProbeSuiteDigest = probeSuiteDigestForIdentity(v.AdapterVersion, v.EventContract, v.ProbeProfileDigest, v.WorkerResultTransportDigest)
		},
		"v5 transport identity": func(v *ConformanceEvidence) {
			v.AdapterVersion = "0.1.4"
			v.EventContract = "qoder-stream-json-1.2.0-v5"
			v.ProbeProfileDigest = probeProfileDigestForEventContract(v.EventContract)
			v.WorkerResultTransportDigest = "sha256:a8868a7f8e9cfc2d126d324204502caacc5462946405ab0835627fca8a121d48"
			v.ProbeSuiteDigest = probeSuiteDigestForIdentity(v.AdapterVersion, v.EventContract, v.ProbeProfileDigest, v.WorkerResultTransportDigest)
		},
	} {
		t.Run("signed evidence "+name, func(t *testing.T) {
			old := evidence
			mutate(&old)
			sign(&old)
			if err := old.validate(now, map[string]ed25519.PublicKey{"root-1": publicKey}); err == nil {
				t.Fatal("correctly signed old conformance evidence was accepted")
			}
		})
	}
}
