package qoder

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/port"
)

func TestCandidateLiveVerifierProductionBoundaryRemainsHardDisabled(t *testing.T) {
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewCandidateLiveVerifier(nil, "caller", publicKey); !errors.Is(err, ErrConformancePending) || !port.IsPermanent(err) {
		t.Fatalf("caller established production verifier: %v", err)
	}
	if _, _, err := SignLiveConformanceObservation([]byte(`{}`), nil); !errors.Is(err, ErrConformancePending) || !port.IsPermanent(err) {
		t.Fatalf("caller established production signer: %v", err)
	}
}

func TestCandidateAuthorityRejectsCrossRoleReceiptKey(t *testing.T) {
	receiptPublic, _, _ := ed25519.GenerateKey(rand.Reader)
	credentialPublic, _, _ := ed25519.GenerateKey(rand.Reader)
	verifierPublic, verifierPrivate, _ := ed25519.GenerateKey(rand.Reader)
	policy := candidateAuthorityPolicy{receiptRole: "evidence", receiptIssuer: "issuer", receiptKeyID: "cross-role", receiptPublicKey: receiptPublic, receiptLedgerTailDigest: digest("9"), credentialProviderKeyID: "credential", credentialProviderPublicKey: credentialPublic, verifierKeyID: "verifier", verifierPublicKey: verifierPublic}
	if _, err := newCandidateLiveVerifier(candidateLegacySandboxFixture{t: t}, policy, verifierPrivate); err == nil {
		t.Fatal("cross-role key established receipt authority")
	}
}

func TestLegacySandboxCannotCreateLiveAuthority(t *testing.T) {
	publicKey, privateKey, _ := ed25519.GenerateKey(rand.Reader)
	credentialPublic, _, _ := ed25519.GenerateKey(rand.Reader)
	verifierPublic, verifierPrivate, _ := ed25519.GenerateKey(rand.Reader)
	sandbox := CandidateProbeSandbox(candidateLegacySandboxFixture{t: t, key: privateKey})
	verifier, err := newCandidateLiveVerifier(sandbox, candidateAuthorityPolicy{receiptRole: "receipt", receiptIssuer: "receipt-authority", receiptKeyID: "receipt-key", receiptKeyEpoch: 7, receiptPublicKey: publicKey, receiptLedgerTailDigest: digest("9"), credentialProviderKeyID: "credential", credentialProviderPublicKey: credentialPublic, verifierKeyID: "verifier", verifierPublicKey: verifierPublic}, verifierPrivate)
	if err != nil {
		t.Fatal(err)
	}
	request := candidateProbeRequestFixture(t)
	if _, _, err := verifier.Verify(context.Background(), request); err == nil {
		t.Fatalf("legacy sandbox produced live authority: %v", err)
	}
}

type candidateLegacySandboxFixture struct {
	t   *testing.T
	key ed25519.PrivateKey
}

func (fixture candidateLegacySandboxFixture) RunProbe(_ context.Context, invocation CandidateProbeInvocation) (CandidateProbeResult, error) {
	fixture.t.Helper()
	writeMarkerAt(fixture.t, invocation.WorkingDirectory.File, []byte(invocation.ChallengeDigest+"\n"))
	model := invocation.ExpectedModel
	if model == "" {
		model = "provider/default"
	}
	return CandidateProbeResult{Transcript: candidateTranscript(invocation, model), ExecutionReceipt: []byte(`{}`), AuthorityBacked: false}, nil
}

func TestExactReceiptRejectsUnknownMissingOrNonCanonicalFields(t *testing.T) {
	for name, mutate := range map[string]func([]byte) []byte{
		"unknown": func(document []byte) []byte {
			var value map[string]any
			_ = json.Unmarshal(document, &value)
			value["unknown"] = true
			changed, _ := json.Marshal(value)
			return changed
		},
		"missing": func(document []byte) []byte {
			var value map[string]any
			_ = json.Unmarshal(document, &value)
			delete(value, "hostIdentityDigest")
			changed, _ := json.Marshal(value)
			return changed
		},
		"non-canonical": func(document []byte) []byte { return append([]byte(" \n"), document...) },
		"duplicate": func(document []byte) []byte {
			return bytes.Replace(document, []byte(`"kind":"QoderProbeExecutionReceipt"`), []byte(`"kind":"QoderProbeExecutionReceipt","kind":"QoderProbeExecutionReceipt"`), 1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			verifier, request, _, authority, _ := productionCandidateVerifierFixture(t)
			authority.documentMutate = mutate
			if _, _, err := verifier.Verify(context.Background(), request); err == nil {
				t.Fatal("invalid exact receipt was accepted")
			}
		})
	}
}

func TestCandidateProbeRejectsPathAliasesAndSymlinkParents(t *testing.T) {
	verifier, request, _, _, _ := productionCandidateVerifierFixture(t)
	request.CredentialConfigRoot = request.ScratchRoot
	if _, _, err := verifier.Verify(context.Background(), request); err == nil {
		t.Fatal("same root identity accepted")
	}
	verifier, request, _, _, _ = productionCandidateVerifierFixture(t)
	request.ScratchRoot = request.ScratchRoot + "/../" + filepath.Base(request.ScratchRoot)
	if _, _, err := verifier.Verify(context.Background(), request); err == nil {
		t.Fatal("unclean path alias accepted")
	}
	verifier, request, _, _, _ = productionCandidateVerifierFixture(t)
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
		t.Skip("Darwin path alias")
	}
	verifier, request, _, _, _ := productionCandidateVerifierFixture(t)
	privateRoot, err := os.MkdirTemp("/private/tmp", "qoder-alias-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(privateRoot) })
	if err := os.Chmod(privateRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	request.ScratchRoot = strings.Replace(privateRoot, "/private/tmp/", "/tmp/", 1)
	if _, _, err := verifier.Verify(context.Background(), request); err == nil {
		t.Fatal("/tmp alias accepted")
	}
}

func candidateProbeRequestFixture(t *testing.T) CandidateLiveProbeRequest {
	t.Helper()
	executable := fakeExecutable(t, "1.1.23", "exit 0")
	realpath, err := filepath.EvalSymlinks(executable)
	if err != nil {
		t.Fatal(err)
	}
	return CandidateLiveProbeRequest{RunnerID: "external-qoder-probe", RunnerVersion: "1", Executable: realpath, CredentialConfigRoot: realPrivateTempDir(t), ScratchRoot: realPrivateTempDir(t), BusinessRepositoryRoots: []string{realPrivateTempDir(t)}, Model: "provider/model", AuthorityGeneration: 1, ProbeArtifactDigest: digest("a"), ChallengeDigest: digest("c"), TrustRootKeyID: "root", Validity: time.Hour}
}
