package qoder

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/port"
	"golang.org/x/sys/unix"
)

type attestedCandidateSandbox struct {
	t                   *testing.T
	privateKey          ed25519.PrivateKey
	class               string
	mutate              func(*CandidateExecutionReceipt)
	markerKind          string
	replay              bool
	scratchRoot         string
	before              func(CandidateProbeInvocation)
	after               func(CandidateProbeInvocation)
	documentMutate      func([]byte) []byte
	transcriptChallenge string
	transcriptModel     string
	receiptTopology     func(CandidateProbeInvocation) string
	lastCompleted       time.Time
}

func (sandbox *attestedCandidateSandbox) RunProbe(_ context.Context, invocation CandidateProbeInvocation) (CandidateProbeResult, error) {
	sandbox.t.Helper()
	if sandbox.before != nil {
		sandbox.before(invocation)
	}
	topologyDigest := invocation.ExpectedTopologyDigest
	if sandbox.receiptTopology != nil {
		topologyDigest = sandbox.receiptTopology(invocation)
	}
	session := fmt.Sprintf("live-session-%d", invocation.VariantIndex)
	if sandbox.replay {
		session = "replayed-session"
	}
	model := invocation.ExpectedModel
	if model == "" {
		model = "provider/default"
	}
	if sandbox.transcriptModel != "" {
		model = sandbox.transcriptModel
	}
	transcriptChallenge := invocation.ChallengeDigest
	if sandbox.transcriptChallenge != "" {
		transcriptChallenge = sandbox.transcriptChallenge
	}
	transcript := []byte(fmt.Sprintf("{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":%q,\"model\":%q,\"qodercli_version\":\"1.1.23\",\"protocol_version\":\"1.2.0\",\"permissionMode\":\"acceptEdits\"}\n{\"type\":\"assistant\",\"message\":{\"role\":\"assistant\",\"content\":[{\"challengeDigest\":%q}]}}\n{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"terminal_reason\":\"completed\",\"usage\":{\"input_tokens\":10,\"output_tokens\":5}}\n", session, model, transcriptChallenge))
	marker := []byte(invocation.ChallengeDigest + "\n")
	switch sandbox.markerKind {
	case "symlink":
		if err := unix.Symlinkat("target", int(invocation.WorkingDirectory.File.Fd()), ".marshal-qoder-probe-challenge"); err != nil {
			sandbox.t.Fatal(err)
		}
	case "fifo":
		path := filepath.Join(sandbox.scratchRoot, invocation.WorkingDirectory.File.Name(), ".marshal-qoder-probe-challenge")
		if err := unix.Mkfifo(path, 0o600); err != nil {
			sandbox.t.Fatal(err)
		}
	case "oversize":
		writeMarkerAt(sandbox.t, invocation.WorkingDirectory.File, []byte(strings.Repeat("a", candidateMarkerLimit+1)))
	case "hardlink":
		writeMarkerNamedAt(sandbox.t, invocation.WorkingDirectory.File, "marker-source", marker)
		if err := unix.Linkat(int(invocation.WorkingDirectory.File.Fd()), "marker-source", int(invocation.WorkingDirectory.File.Fd()), ".marshal-qoder-probe-challenge", 0); err != nil {
			sandbox.t.Fatal(err)
		}
	default:
		writeMarkerAt(sandbox.t, invocation.WorkingDirectory.File, marker)
	}
	if sandbox.after != nil {
		sandbox.after(invocation)
	}
	startedAt := time.Now().UTC().Add(-time.Second)
	if !sandbox.lastCompleted.IsZero() {
		startedAt = sandbox.lastCompleted.Add(time.Nanosecond)
	}
	completedAt := startedAt.Add(time.Millisecond)
	sandbox.lastCompleted = completedAt
	scratchArgumentPath := fmt.Sprintf("/sandbox/objects/scratch-%d-%d", invocation.WorkingDirectory.Identity.Device, invocation.WorkingDirectory.Identity.Inode)
	credentialArgumentPath := fmt.Sprintf("/sandbox/objects/credential-%d-%d", invocation.CredentialConfigRoot.Identity.Device, invocation.CredentialConfigRoot.Identity.Inode)
	actualArguments := substituteBoundPaths(invocation.Arguments, scratchArgumentPath, credentialArgumentPath)
	actualEnvironment := substituteBoundPaths(invocation.Environment, scratchArgumentPath, credentialArgumentPath)
	audit := CandidateIsolationAudit{LaunchAuditDigest: digest("a"), DenialAuditDigest: digest("b"), ExitAuditDigest: digest("c"), AncestorChainDigest: topologyDigest, CredentialReadOnlyEnforced: true, BusinessRootsDenied: true, ScratchOnlyWriteEnforced: true, NetworkPolicyEnforced: true, AmbientStateDenied: true}
	for index := range invocation.BusinessRepositoryRoots {
		audit.BusinessRootDenialDigests = append(audit.BusinessRootDenialDigests, digest(fmt.Sprintf("%x", index+1)))
	}
	principal := CandidateIsolationPrincipal{ProviderIdentity: "fixture-isolation-provider", ProcessIdentity: "fixture-qoder-principal", Profile: candidateIsolationProfile}
	authority := CandidateReceiptAuthorityIdentity{ProviderIdentity: "fixture-receipt-provider", ProcessIdentity: "fixture-receipt-principal", Issuer: "external-live-sandbox", KeyID: "receipt-root-1"}
	receipt := CandidateExecutionReceipt{
		Kind: candidateReceiptKind, EvidenceClass: sandbox.class, SandboxID: "external-live-sandbox", SandboxVersion: "1", ReceiptAuthorityKeyID: "receipt-root-1",
		InvocationDigest: invocation.InvocationDigest, ProbeRunID: invocation.ProbeRunID, ReceiptSequence: invocation.ReceiptSequence, PreviousReceiptDigest: invocation.PreviousReceiptDigest, InvocationManifestDigest: invocation.InvocationManifestDigest, VariantIndex: invocation.VariantIndex,
		Executable: invocation.Executable.Identity, ExecutableDigest: invocation.Executable.Digest,
		Arguments: actualArguments, Environment: actualEnvironment, ScratchArgumentPath: scratchArgumentPath, CredentialArgumentPath: credentialArgumentPath,
		WorkingDirectory: invocation.WorkingDirectory.Identity, CredentialConfigRoot: invocation.CredentialConfigRoot.Identity,
		WritableRoots: objectIdentities(invocation.WritableRoots), BusinessRepositoryRoots: objectIdentities(invocation.BusinessRepositoryRoots),
		ChallengeDigest: invocation.ChallengeDigest, TranscriptDigest: digestBytes(transcript), SessionID: session, ObservedModel: model,
		BinaryVersion: "1.1.23", ProtocolVersion: qoderProtocolVersion, PermissionMode: qoderPermissionMode, MarkerDigest: digestBytes(marker),
		StartedAt: startedAt.Format(time.RFC3339Nano), CompletedAt: completedAt.Format(time.RFC3339Nano),
		TopologyDigest:   topologyDigest,
		IsolationProfile: principal.Profile, IsolationProviderID: principal.ProviderIdentity, IsolationProcessID: principal.ProcessIdentity, ReceiptAuthorityProviderID: authority.ProviderIdentity, ReceiptAuthorityProcessID: authority.ProcessIdentity, IsolationAudit: audit,
	}
	if sandbox.mutate != nil {
		sandbox.mutate(&receipt)
	}
	document := signCandidateReceipt(sandbox.t, receipt, sandbox.privateKey)
	if sandbox.documentMutate != nil {
		document = sandbox.documentMutate(document)
	}
	return CandidateProbeResult{Transcript: transcript, ExecutionReceipt: document, IsolationPrincipal: principal, ReceiptAuthorityIdentity: authority, IsolationAudit: audit}, nil
}

func substituteBoundPaths(values []string, scratch, credential string) []string {
	result := append([]string(nil), values...)
	for index, value := range result {
		value = strings.ReplaceAll(value, candidateBoundScratchToken, scratch)
		value = strings.ReplaceAll(value, candidateBoundCredentialToken, credential)
		result[index] = value
	}
	return result
}

func writeMarkerAt(t *testing.T, directory *os.File, data []byte) {
	t.Helper()
	writeMarkerNamedAt(t, directory, ".marshal-qoder-probe-challenge", data)
}

func writeMarkerNamedAt(t *testing.T, directory *os.File, name string, data []byte) {
	t.Helper()
	fd, err := unix.Openat(int(directory.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	file := os.NewFile(uintptr(fd), name)
	if _, err := file.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func signCandidateReceipt(t *testing.T, receipt CandidateExecutionReceipt, key ed25519.PrivateKey) []byte {
	t.Helper()
	message, err := receipt.signingBytes()
	if err != nil {
		t.Fatal(err)
	}
	receipt.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(key, message))
	data, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	data, err = canonical.JSON(data)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func candidateVerifierFixture(t *testing.T, class string) (*CandidateLiveVerifier, CandidateLiveProbeRequest, *attestedCandidateSandbox) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sandbox := &attestedCandidateSandbox{t: t, privateKey: privateKey, class: class}
	verifierPublicKey, verifierPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	policy := candidateAuthorityPolicy{receiptIssuer: "external-live-sandbox", receiptKeyID: "receipt-root-1", receiptPublicKey: publicKey, verifierKeyID: "verifier-root-1", verifierPublicKey: verifierPublicKey}
	verifier, err := newCandidateLiveVerifier(sandbox, policy, verifierPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	request := CandidateLiveProbeRequest{
		RunnerID: "external-qoder-probe", RunnerVersion: "1", Executable: fakeExecutable(t, "1.1.23", "exit 0"),
		CredentialConfigRoot: realPrivateTempDir(t), ScratchRoot: realPrivateTempDir(t), BusinessRepositoryRoots: []string{realPrivateTempDir(t)},
		Model: "provider/model", AuthorityGeneration: 1, ProbeArtifactDigest: digest("a"), ChallengeDigest: digest("c"), TrustRootKeyID: "root", Validity: time.Hour,
	}
	request.Executable, err = filepath.EvalSymlinks(request.Executable)
	if err != nil {
		t.Fatal(err)
	}
	sandbox.scratchRoot = request.ScratchRoot
	return verifier, request, sandbox
}

func TestHermeticReceiptCannotProduceSignableLiveObservation(t *testing.T) {
	verifier, request, _ := candidateVerifierFixture(t, candidateEvidenceClassHermetic)
	if _, _, err := verifier.Verify(context.Background(), request); err == nil || !strings.Contains(err.Error(), "not credentialed-live") {
		t.Fatalf("error = %v, want hermetic receipt rejection", err)
	}
}

func TestCallerOwnedKeysCannotEstablishCandidateAuthority(t *testing.T) {
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	verifier, request, sandbox := candidateVerifierFixture(t, candidateEvidenceClassLive)
	if _, err := NewCandidateLiveVerifier(sandbox, "caller-sandbox", publicKey); !errors.Is(err, ErrConformancePending) || !port.IsPermanent(err) {
		t.Fatalf("caller established exported verifier authority: %v", err)
	}
	document, _, err := verifier.Verify(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	_, evidencePrivate, _ := ed25519.GenerateKey(rand.Reader)
	otherReceiptPublic, _, _ := ed25519.GenerateKey(rand.Reader)
	otherVerifierPublic, _, _ := ed25519.GenerateKey(rand.Reader)
	untrusted := candidateAuthorityPolicy{receiptIssuer: "caller-sandbox", receiptKeyID: "caller-receipt", receiptPublicKey: otherReceiptPublic, verifierKeyID: "caller-verifier", verifierPublicKey: otherVerifierPublic}
	if _, _, err := signCandidateLiveConformanceObservation(document, untrusted, evidencePrivate); err == nil {
		t.Fatal("caller-owned authority policy signed a synthetic observation")
	}
	if _, _, err := SignLiveConformanceObservation(document, evidencePrivate); !errors.Is(err, ErrConformancePending) || !port.IsPermanent(err) {
		t.Fatalf("exported signer accepted caller-owned observation: %v", err)
	}
}

func TestCandidateSignerRejectsUnsignedOrCandidateSubstitutedObservation(t *testing.T) {
	verifier, request, _ := candidateVerifierFixture(t, candidateEvidenceClassLive)
	document, _, err := verifier.Verify(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	_, evidencePrivate, _ := ed25519.GenerateKey(rand.Reader)
	observation, err := decodeLiveConformanceObservation(document)
	if err != nil {
		t.Fatal(err)
	}
	observation.VerifierSignature = base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))
	unsigned, _, err := EncodeLiveConformanceObservation(observation)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := signCandidateLiveConformanceObservation(unsigned, verifier.policy, evidencePrivate); err == nil {
		t.Fatal("candidate signer accepted an observation without verifier authority")
	}
	observation.ExecutableDigest = digest("f")
	message, err := liveObservationSigningBytes(observation)
	if err != nil {
		t.Fatal(err)
	}
	observation.VerifierSignature = base64.StdEncoding.EncodeToString(ed25519.Sign(verifier.verifierPrivateKey, message))
	substituted, _, err := EncodeLiveConformanceObservation(observation)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := signCandidateLiveConformanceObservation(substituted, verifier.policy, evidencePrivate); err == nil {
		t.Fatal("candidate signer accepted candidate identity substitution")
	}
}

func TestExecutionReceiptRejectsEveryBoundFieldSubstitution(t *testing.T) {
	mutations := map[string]func(*CandidateExecutionReceipt){
		"invocation":           func(value *CandidateExecutionReceipt) { value.InvocationDigest = digest("f") },
		"probe run":            func(value *CandidateExecutionReceipt) { value.ProbeRunID = "substitute" },
		"receipt sequence":     func(value *CandidateExecutionReceipt) { value.ReceiptSequence++ },
		"previous receipt":     func(value *CandidateExecutionReceipt) { value.PreviousReceiptDigest = digest("f") },
		"invocation manifest":  func(value *CandidateExecutionReceipt) { value.InvocationManifestDigest = digest("f") },
		"variant":              func(value *CandidateExecutionReceipt) { value.VariantIndex++ },
		"executable identity":  func(value *CandidateExecutionReceipt) { value.Executable.Inode++ },
		"executable digest":    func(value *CandidateExecutionReceipt) { value.ExecutableDigest = digest("f") },
		"argv":                 func(value *CandidateExecutionReceipt) { value.Arguments = append(value.Arguments, "--substitute") },
		"environment":          func(value *CandidateExecutionReceipt) { value.Environment = append(value.Environment, "SUBSTITUTE=1") },
		"scratch argv path":    func(value *CandidateExecutionReceipt) { value.ScratchArgumentPath = "/sandbox/substitute" },
		"credential argv path": func(value *CandidateExecutionReceipt) { value.CredentialArgumentPath = "/sandbox/substitute" },
		"write root":           func(value *CandidateExecutionReceipt) { value.WritableRoots[0].Inode++ },
		"deny roots":           func(value *CandidateExecutionReceipt) { value.BusinessRepositoryRoots = nil },
		"credential root":      func(value *CandidateExecutionReceipt) { value.CredentialConfigRoot.Inode++ },
		"scratch root":         func(value *CandidateExecutionReceipt) { value.WorkingDirectory.Inode++ },
		"challenge":            func(value *CandidateExecutionReceipt) { value.ChallengeDigest = digest("f") },
		"transcript":           func(value *CandidateExecutionReceipt) { value.TranscriptDigest = digest("f") },
		"session":              func(value *CandidateExecutionReceipt) { value.SessionID = "substitute" },
		"model":                func(value *CandidateExecutionReceipt) { value.ObservedModel = "substitute" },
		"version":              func(value *CandidateExecutionReceipt) { value.BinaryVersion = "1.1.24" },
		"protocol":             func(value *CandidateExecutionReceipt) { value.ProtocolVersion = "substitute" },
		"permission":           func(value *CandidateExecutionReceipt) { value.PermissionMode = "substitute" },
		"marker":               func(value *CandidateExecutionReceipt) { value.MarkerDigest = digest("f") },
		"isolation audit":      func(value *CandidateExecutionReceipt) { value.IsolationAudit.ScratchOnlyWriteEnforced = false },
		"isolation principal":  func(value *CandidateExecutionReceipt) { value.IsolationProcessID = value.ReceiptAuthorityProcessID },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			verifier, request, sandbox := candidateVerifierFixture(t, candidateEvidenceClassLive)
			sandbox.mutate = mutate
			if _, _, err := verifier.Verify(context.Background(), request); err == nil {
				t.Fatal("substituted execution receipt was accepted")
			}
		})
	}
}

func TestExecutionReceiptRejectsUnknownOrUnsignedDocument(t *testing.T) {
	verifier, request, sandbox := candidateVerifierFixture(t, candidateEvidenceClassLive)
	sandbox.mutate = func(value *CandidateExecutionReceipt) { value.Signature = "ignored-before-sign" }
	// A different receipt key cannot be substituted even when every field is exact.
	_, wrongKey, _ := ed25519.GenerateKey(rand.Reader)
	sandbox.privateKey = wrongKey
	if _, _, err := verifier.Verify(context.Background(), request); err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("error = %v, want signature rejection", err)
	}
}

func TestExecutionReceiptRejectsIgnoredOrRemovedFields(t *testing.T) {
	for name, mutate := range map[string]func(map[string]any){
		"unknown": func(value map[string]any) { value["ignored"] = true },
		"removed": func(value map[string]any) { delete(value, "invocationDigest") },
	} {
		t.Run(name, func(t *testing.T) {
			verifier, request, sandbox := candidateVerifierFixture(t, candidateEvidenceClassLive)
			sandbox.documentMutate = func(document []byte) []byte {
				var value map[string]any
				if err := json.Unmarshal(document, &value); err != nil {
					t.Fatal(err)
				}
				mutate(value)
				changed, _ := json.Marshal(value)
				return changed
			}
			if _, _, err := verifier.Verify(context.Background(), request); err == nil {
				t.Fatal("non-closed receipt was accepted")
			}
		})
	}
}

func TestCandidateMarkerRejectsAbnormalObjects(t *testing.T) {
	for _, kind := range []string{"symlink", "fifo", "oversize", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			verifier, request, sandbox := candidateVerifierFixture(t, candidateEvidenceClassLive)
			sandbox.markerKind = kind
			if _, _, err := verifier.Verify(context.Background(), request); err == nil {
				t.Fatal("abnormal marker accepted")
			}
		})
	}
}

func TestCandidateProbeRejectsPathAliasesAndSymlinkParents(t *testing.T) {
	verifier, request, _ := candidateVerifierFixture(t, candidateEvidenceClassLive)
	request.CredentialConfigRoot = request.ScratchRoot
	if _, _, err := verifier.Verify(context.Background(), request); err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("same identity accepted: %v", err)
	}
	verifier, request, _ = candidateVerifierFixture(t, candidateEvidenceClassLive)
	request.ScratchRoot = request.ScratchRoot + "/../" + filepath.Base(request.ScratchRoot)
	if _, _, err := verifier.Verify(context.Background(), request); err == nil {
		t.Fatal("unclean alias accepted")
	}
	verifier, request, _ = candidateVerifierFixture(t, candidateEvidenceClassLive)
	parent := realPrivateTempDir(t)
	link := filepath.Join(realPrivateTempDir(t), "linked-parent")
	if err := os.Symlink(parent, link); err != nil {
		t.Fatal(err)
	}
	request.ScratchRoot = link
	if _, _, err := verifier.Verify(context.Background(), request); err == nil {
		t.Fatal("symlink parent accepted")
	}
}

func TestCandidateProbeRejectsTmpPrivateTmpAlias(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("/tmp to /private/tmp alias is Darwin-specific")
	}
	verifier, request, _ := candidateVerifierFixture(t, candidateEvidenceClassLive)
	privateRoot, err := os.MkdirTemp("/private/tmp", "qoder-alias-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(privateRoot) })
	if err := os.Chmod(privateRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	request.ScratchRoot = privateRoot
	request.ScratchRoot = strings.Replace(request.ScratchRoot, "/private/tmp/", "/tmp/", 1)
	if _, _, err := verifier.Verify(context.Background(), request); err == nil {
		t.Fatal("/tmp symlink alias was accepted")
	}
}

func TestCandidateProbeRejectsCrossVariantReplay(t *testing.T) {
	verifier, request, sandbox := candidateVerifierFixture(t, candidateEvidenceClassLive)
	sandbox.replay = true
	if _, _, err := verifier.Verify(context.Background(), request); err == nil || !strings.Contains(err.Error(), "replayed") {
		t.Fatalf("error = %v, want replay rejection", err)
	}
}

func TestCandidateProbeRejectsTranscriptChallengeOrModelSubstitution(t *testing.T) {
	for name, configure := range map[string]func(*attestedCandidateSandbox){
		"challenge": func(sandbox *attestedCandidateSandbox) { sandbox.transcriptChallenge = digest("f") },
		"model":     func(sandbox *attestedCandidateSandbox) { sandbox.transcriptModel = "provider/substitute" },
	} {
		t.Run(name, func(t *testing.T) {
			verifier, request, sandbox := candidateVerifierFixture(t, candidateEvidenceClassLive)
			configure(sandbox)
			if _, _, err := verifier.Verify(context.Background(), request); err == nil || !strings.Contains(err.Error(), "protocol") {
				t.Fatalf("error = %v, want transcript substitution rejection", err)
			}
		})
	}
}

func TestCandidateProbeUsesHeldIdentitiesAcrossPathSwap(t *testing.T) {
	verifier, request, sandbox := candidateVerifierFixture(t, candidateEvidenceClassLive)
	sandbox.replay = true // ensure this synthetic mechanism test cannot emit a signable observation
	originalExecutableDigest, err := digestFile(request.Executable)
	if err != nil {
		t.Fatal(err)
	}
	var once bool
	var restored bool
	var seenExecutableDigest string
	sandbox.before = func(invocation CandidateProbeInvocation) {
		seenExecutableDigest = invocation.Executable.Digest
		if once {
			return
		}
		once = true
		for _, path := range []string{request.CredentialConfigRoot, request.ScratchRoot, request.BusinessRepositoryRoots[0]} {
			held := path + ".held"
			if err := os.Rename(path, held); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		heldExecutable := request.Executable + ".held"
		if err := os.Rename(request.Executable, heldExecutable); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(request.Executable, []byte("#!/bin/sh\nexit 99\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	sandbox.after = func(_ CandidateProbeInvocation) {
		if restored {
			return
		}
		restored = true
		for _, path := range []string{request.CredentialConfigRoot, request.ScratchRoot, request.BusinessRepositoryRoots[0]} {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(path+".held", path); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Remove(request.Executable); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(request.Executable+".held", request.Executable); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := verifier.Verify(context.Background(), request); err == nil || !strings.Contains(err.Error(), "replayed") {
		t.Fatalf("error = %v, want non-signable replay stop", err)
	}
	if seenExecutableDigest != originalExecutableDigest {
		t.Fatalf("sandbox rebound to swapped executable: %s", seenExecutableDigest)
	}
}

func TestCandidateProbeRejectsProtectedRootNestedDuringExecutionAndSwappedBack(t *testing.T) {
	for _, target := range []string{"credential", "business"} {
		t.Run(target, func(t *testing.T) {
			verifier, request, sandbox := candidateVerifierFixture(t, candidateEvidenceClassLive)
			var original, nested string
			sandbox.before = func(invocation CandidateProbeInvocation) {
				if original != "" {
					return
				}
				if target == "credential" {
					original = request.CredentialConfigRoot
				} else {
					original = request.BusinessRepositoryRoots[0]
				}
				nested = filepath.Join(sandbox.scratchRoot, invocation.WorkingDirectory.File.Name(), "nested-"+target)
				if err := os.Rename(original, nested); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(original, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			sandbox.receiptTopology = func(invocation CandidateProbeInvocation) string {
				handles := &candidateProbeHandles{credential: invocation.CredentialConfigRoot, scratchRoot: invocation.ScratchRoot, business: invocation.BusinessRepositoryRoots}
				value, err := validateCandidateTopology(handles, invocation.WorkingDirectory)
				if err != nil {
					return digest("f")
				}
				return value
			}
			sandbox.after = func(_ CandidateProbeInvocation) {
				if original == "" || nested == "" {
					return
				}
				if err := os.Remove(original); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(nested, original); err != nil {
					t.Fatal(err)
				}
				nested = ""
			}
			if _, _, err := verifier.Verify(context.Background(), request); err == nil || !strings.Contains(err.Error(), "topology") {
				t.Fatalf("error = %v, want execution-topology rejection", err)
			}
		})
	}
}
