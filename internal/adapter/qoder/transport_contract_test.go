package qoder

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"runtime"
	"testing"
	"time"
)

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
		"staging basename":           func(v *workerResultTransportContract) { v.StagingBasename += ".changed" },
		"staging file type":          func(v *workerResultTransportContract) { v.StagingFileType = "fifo" },
		"staging mode":               func(v *workerResultTransportContract) { v.StagingMode = "0644" },
		"creation flags":             func(v *workerResultTransportContract) { v.CreationFlags = "O_RDWR|O_CREAT" },
		"unlink before launch":       func(v *workerResultTransportContract) { v.UnlinkBeforeLaunch = false },
		"unlinked link count":        func(v *workerResultTransportContract) { v.UnlinkedLinkCount = 1 },
		"worker path exposure":       func(v *workerResultTransportContract) { v.WorkerPathExposure = "staging-path" },
		"worker descriptor exposure": func(v *workerResultTransportContract) { v.WorkerDescriptorExposure = "staging-fd" },
		"control inode relationship": func(v *workerResultTransportContract) { v.ControlInodeRelationship = "may-alias" },
		"held directory binding":     func(v *workerResultTransportContract) { v.HeldDirectoryBinding = "path-reopen" },
		"held inode commit":          func(v *workerResultTransportContract) { v.HeldInodeCommit = "pre-terminal" },
		"held inode consume":         func(v *workerResultTransportContract) { v.HeldInodeConsume = "path-read" },
		"held inode cleanup":         func(v *workerResultTransportContract) { v.HeldInodeCleanup = "unlink-current-path" },
		"tool name":                  func(v *workerResultTransportContract) { v.ToolName = "bash" },
		"tool input contract":        func(v *workerResultTransportContract) { v.ToolInputContract = "unknown-fields-allowed" },
		"canonical command":          func(v *workerResultTransportContract) { v.CanonicalCommand += "\n" },
		"tee sequence":               func(v *workerResultTransportContract) { v.TeeSequence = "at-least-once" },
		"denial extractor":           func(v *workerResultTransportContract) { v.DenialExtractor += "-changed" },
		"transcript event contract":  func(v *workerResultTransportContract) { v.TranscriptEventContract = "qoder-stream-json-1.2.0-v3" },
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
		"event contract v3": func(v *LiveConformanceObservation) { v.EventContract = "qoder-stream-json-1.2.0-v3" },
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
		"event contract v3": func(v *ConformanceEvidence) { v.EventContract = "qoder-stream-json-1.2.0-v3" },
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
