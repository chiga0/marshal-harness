package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/chiga0/marshal-harness/internal/adapter/qoder"
	"github.com/chiga0/marshal-harness/internal/buildinfo"
	"github.com/chiga0/marshal-harness/internal/canonical"
)

const qoderTranscriptCheckMaxInputBytes int64 = 32 << 20
const qoderTranscriptCheckCommandVersion = "qoder-transcript-check/v2"

// runInternalQoderTranscriptCheck is an intentionally hidden operator-local
// bridge to the production Qoder transcript validator. The public validator
// invokes this command through the already-built Marshal executable so macOS
// sees one stable executable identity rather than a newly built helper.
func runInternalQoderTranscriptCheck(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	var err error
	args, err = consumeQoderStableAttestation(args, stdin)
	if err != nil {
		return writeQoderTranscriptCheckFailure(stderr, "checker-handshake-invalid", ExitFailure)
	}
	if len(args) != 0 {
		return writeQoderTranscriptCheckFailure(stderr, "checker-arguments-invalid", ExitUsage)
	}
	raw, err := io.ReadAll(io.LimitReader(stdin, qoderTranscriptCheckMaxInputBytes+1))
	if err != nil {
		return writeQoderTranscriptCheckFailure(stderr, "checker-input-read-failed", ExitFailure)
	}
	if int64(len(raw)) > qoderTranscriptCheckMaxInputBytes {
		return writeQoderTranscriptCheckFailure(stderr, "checker-input-too-large", ExitFailure)
	}
	// Canonical admission is used only as a closed JSON/duplicate-key gate.
	// The production semantic validator still receives the decoded original
	// values, and no input-derived error text is exposed.
	if _, err := canonical.JSON(raw); err != nil {
		return writeQoderTranscriptCheckFailure(stderr, "checker-input-invalid", ExitFailure)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var input qoder.TranscriptAttestationInput
	if err := decoder.Decode(&input); err != nil {
		return writeQoderTranscriptCheckFailure(stderr, "checker-input-invalid", ExitFailure)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return writeQoderTranscriptCheckFailure(stderr, "checker-input-trailing", ExitFailure)
	}
	build := buildinfo.Current()
	if !isLowerHexCommit(build.Commit) {
		return writeQoderTranscriptCheckFailure(stderr, "checker-build-identity-invalid", ExitFailure)
	}
	observation, err := qoder.ValidateTranscriptAttestation(input)
	if err != nil {
		reason := err.Error()
		if !isClosedQoderTranscriptReason(reason) {
			reason = "checker-core-rejected"
		}
		return writeQoderTranscriptCheckFailure(stderr, reason, ExitFailure)
	}
	output := struct {
		Status      string                                 `json:"status"`
		ReasonCode  string                                 `json:"reasonCode"`
		Identity    map[string]string                      `json:"identity"`
		Marshal     qoderTranscriptCheckMarshalIdentity    `json:"marshal"`
		Observation qoder.TranscriptAttestationObservation `json:"observation"`
	}{
		Status:     "pass",
		ReasonCode: "transcript-attestation-pass",
		Identity:   qoder.TranscriptAttestationImplementationIdentity(),
		Marshal: qoderTranscriptCheckMarshalIdentity{
			Version:                build.Version,
			Commit:                 build.Commit,
			InternalCommandVersion: qoderTranscriptCheckCommandVersion,
			InputDigest:            canonical.DigestBytes(raw),
		},
		Observation: observation,
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(output); err != nil {
		return ExitFailure
	}
	return ExitOK
}

func consumeQoderStableAttestation(args []string, stdin io.Reader) ([]string, error) {
	if len(args) == 0 || args[0] != "--attestation-ready" {
		return args, nil
	}
	var token [1]byte
	if _, err := io.ReadFull(stdin, token[:]); err != nil || token[0] != 0 {
		return nil, fmt.Errorf("qoder transcript checker handshake is invalid")
	}
	return args[1:], nil
}

func isLowerHexCommit(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

type qoderTranscriptCheckMarshalIdentity struct {
	Version                string `json:"version"`
	Commit                 string `json:"commit"`
	InternalCommandVersion string `json:"internalCommandVersion"`
	InputDigest            string `json:"inputDigest"`
}

func isClosedQoderTranscriptReason(reason string) bool {
	switch reason {
	case "adapter-identity-mismatch", "admission-evidence-missing", "bash-input-invalid",
		"blank-line", "capability-identity-mismatch", "capability-invalid", "closed-json-invalid",
		"closed-transcript-json-invalid", "command-binding-mismatch", "contract-validator-unavailable",
		"core-contract-invalid", "declared-command-count-mismatch", "forbidden-command-executed",
		"invalid-jsonl", "not-canonical", "not-directory", "profile-identity-mismatch",
		"profile-invalid", "qoder-v7-transcript-invalid", "required-constraint-missing",
		"subject-or-jcs-binding-mismatch", "task-spec-invalid", "tee-payload-invalid",
		"tee-payload-mismatch", "tee-result-not-explicit-success", "tool-input-invalid",
		"tool-not-allowed", "tool-path-ambiguous", "tool-path-escape", "tool-path-invalid",
		"tool-path-noncanonical", "tool-path-out-of-scope", "tool-path-symlink-escape",
		"trailing-json", "transcript-identity-mismatch", "transcript-meta-mismatch",
		"unreviewed-tool", "worktree-invalid", "write-path-not-declared":
		return true
	default:
		return false
	}
}

func writeQoderTranscriptCheckFailure(stderr io.Writer, reason string, exitCode int) int {
	// reason is either one of this command's closed constants or a closed
	// reason emitted by ValidateTranscriptAttestation; it never contains raw
	// transcript, paths, prompts, commands, or credentials.
	fmt.Fprintf(stderr, "{\"status\":\"fail\",\"reasonCode\":%q}\n", reason)
	return exitCode
}
