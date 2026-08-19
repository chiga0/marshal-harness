package qoder

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/verification"
)

const TranscriptAttestationProfileVersion = "qoder-v5-transcript-attestation-v2"

type TranscriptAttestationSubject struct {
	SourceHead      string `json:"sourceHead"`
	TaskID          string `json:"taskId"`
	RunID           string `json:"runId"`
	AttemptID       string `json:"attemptId"`
	AdapterID       string `json:"adapterId"`
	BinaryVersion   string `json:"binaryVersion"`
	ProtocolVersion string `json:"protocolVersion"`
	EventContract   string `json:"eventContract"`
	PermissionMode  string `json:"permissionMode"`
}

type TranscriptAttestationInput struct {
	Subject            TranscriptAttestationSubject `json:"subject"`
	Transcript         []byte                       `json:"transcript"`
	TranscriptMeta     []byte                       `json:"transcriptMeta"`
	WorkerRequest      []byte                       `json:"workerRequest"`
	WorkerResult       []byte                       `json:"workerResult"`
	TaskSpec           []byte                       `json:"taskSpec"`
	CapabilitySnapshot []byte                       `json:"capabilitySnapshot"`
	Profile            []byte                       `json:"profile"`
}

type TranscriptAttestationCommand struct {
	CommandID     string `json:"commandId"`
	CommandDigest string `json:"commandDigest"`
	Status        string `json:"status"`
}

type TranscriptAttestationObservation struct {
	EventCount                  int                            `json:"eventCount"`
	AssistantMessages           int                            `json:"assistantMessages"`
	ToolCalls                   int                            `json:"toolCalls"`
	CommandCalls                int                            `json:"commandCalls"`
	WorkerResultTeeLast         bool                           `json:"workerResultTeeLast"`
	Commands                    []TranscriptAttestationCommand `json:"commands"`
	CapabilityDigest            string                         `json:"capabilityDigest"`
	AdmissionEvidenceDigest     string                         `json:"admissionEvidenceDigest"`
	ExecutableDigest            string                         `json:"executableDigest"`
	WorkerResultTransportDigest string                         `json:"workerResultTransportDigest"`
	ProfileDigest               string                         `json:"profileDigest"`
}

type transcriptAttestationProfile struct {
	ProfileVersion        string   `json:"profileVersion"`
	AdapterID             string   `json:"adapterId"`
	BinaryVersion         string   `json:"binaryVersion"`
	ProtocolVersion       string   `json:"protocolVersion"`
	EventContract         string   `json:"eventContract"`
	PermissionMode        string   `json:"permissionMode"`
	DefaultAllowedTools   []string `json:"defaultAllowedTools"`
	ForbiddenCommandWords []string `json:"forbiddenCommandWords"`
	RequiredConstraints   []string `json:"requiredConstraints"`
	Commands              []struct {
		CommandID     string `json:"commandId"`
		CommandDigest string `json:"commandDigest"`
	} `json:"commands"`
}

type attestationWorkerRequest struct {
	TaskID           string `json:"taskId"`
	RunID            string `json:"runId"`
	AttemptID        string `json:"attemptId"`
	SpecDigest       string `json:"specDigest"`
	CapabilityDigest string `json:"capabilityDigest"`
	BaseSHA          string `json:"baseSha"`
	WorktreePath     string `json:"worktreePath"`
	AdapterID        string `json:"adapterId"`
}

type attestationWorkerResult struct {
	TaskID               string                                          `json:"taskId"`
	RunID                string                                          `json:"runId"`
	AttemptID            string                                          `json:"attemptId"`
	Adapter              struct{ ID, Executable, Version, Model string } `json:"adapter"`
	Status               string                                          `json:"status"`
	DeclaredChangedFiles []string                                        `json:"declaredChangedFiles"`
	DeclaredCommands     []struct{ CommandID, Status string }            `json:"declaredCommands"`
}

type attestationCapability struct {
	AdapterID                 string `json:"adapterId"`
	AdapterVersion            string `json:"adapterVersion"`
	Executable                string `json:"executable"`
	ExecutableDigest          string `json:"executableDigest"`
	BinaryVersion             string `json:"binaryVersion"`
	ProbeStatus               string `json:"probeStatus"`
	AuthorityMode             string `json:"authorityMode"`
	ConformanceEvidenceDigest string `json:"conformanceEvidenceDigest"`
}

type attestationMeta struct {
	AssistantMessages        int      `json:"assistantMessages"`
	CapturedBytes            int      `json:"capturedBytes"`
	EventCount               int      `json:"eventCount"`
	ExitCode                 int      `json:"exitCode"`
	OutputTruncated          bool     `json:"outputTruncated"`
	PermissionMode           string   `json:"permissionMode"`
	ProtocolVersion          string   `json:"protocolVersion"`
	QoderCLIVersion          string   `json:"qodercliVersion"`
	ToolCalls                int      `json:"toolCalls"`
	ToolNames                []string `json:"toolNames"`
	WorkerResultTeeAttempts  int      `json:"workerResultTeeAttempts"`
	WorkerResultTeeLast      bool     `json:"workerResultTeeLast"`
	WorkerResultTeeSuccesses int      `json:"workerResultTeeSuccesses"`
	ContextError             string   `json:"contextError"`
	DenialsBenign            int      `json:"denialsBenign"`
	DenialsFatal             int      `json:"denialsFatal"`
	FailureKind              string   `json:"failureKind"`
	InputTokens              int      `json:"inputTokens"`
	Model                    string   `json:"model"`
	OutputTokens             int      `json:"outputTokens"`
	PermissionDenied         bool     `json:"permissionDenied"`
	RetryDisposition         string   `json:"retryDisposition"`
	SessionID                string   `json:"sessionId"`
	Signal                   string   `json:"signal"`
	StderrBytes              int      `json:"stderrBytes"`
	StderrTruncated          bool     `json:"stderrTruncated"`
}

func ValidateTranscriptAttestation(input TranscriptAttestationInput) (TranscriptAttestationObservation, error) {
	var observation TranscriptAttestationObservation
	validator, err := contract.NewValidator()
	if err != nil {
		return observation, errors.New("contract-validator-unavailable")
	}
	for _, record := range []struct {
		kind domain.Kind
		raw  []byte
	}{
		{domain.KindTask, input.TaskSpec}, {domain.KindWorkerRequest, input.WorkerRequest},
		{domain.KindWorkerResult, input.WorkerResult}, {domain.KindCapabilitySnapshot, input.CapabilitySnapshot},
	} {
		if err := validator.Validate(record.kind, record.raw); err != nil {
			return observation, errors.New("core-contract-invalid")
		}
	}
	var task domain.TaskSpec
	var request attestationWorkerRequest
	var result attestationWorkerResult
	var capability attestationCapability
	var meta attestationMeta
	var profile transcriptAttestationProfile
	for _, item := range []struct {
		raw    []byte
		target any
	}{{input.TaskSpec, &task}, {input.WorkerRequest, &request}, {input.WorkerResult, &result}, {input.CapabilitySnapshot, &capability}} {
		if err := json.Unmarshal(item.raw, item.target); err != nil {
			return observation, errors.New("closed-json-invalid")
		}
	}
	if strictJSON(input.TranscriptMeta, &meta) != nil || strictJSON(input.Profile, &profile) != nil {
		return observation, errors.New("closed-json-invalid")
	}
	if profile.ProfileVersion != TranscriptAttestationProfileVersion || profile.AdapterID != adapterID || profile.BinaryVersion != supportedBinary || profile.ProtocolVersion != qoderProtocolVersion || profile.EventContract != conformanceEventContract || profile.PermissionMode != qoderPermissionMode {
		return observation, errors.New("profile-identity-mismatch")
	}
	profileDigest, err := canonical.DigestJSON(input.Profile)
	if err != nil {
		return observation, errors.New("profile-invalid")
	}
	specDigest, err := canonical.DigestJSON(input.TaskSpec)
	if err != nil {
		return observation, errors.New("task-spec-invalid")
	}
	capabilityDigest, err := canonical.DigestJSON(input.CapabilitySnapshot)
	if err != nil {
		return observation, errors.New("capability-invalid")
	}
	s := input.Subject
	if request.TaskID != s.TaskID || result.TaskID != s.TaskID || task.Metadata.ID != s.TaskID || request.RunID != s.RunID || result.RunID != s.RunID || request.AttemptID != s.AttemptID || result.AttemptID != s.AttemptID || request.BaseSHA != s.SourceHead || request.SpecDigest != specDigest || request.CapabilityDigest != capabilityDigest {
		return observation, errors.New("subject-or-jcs-binding-mismatch")
	}
	if s.AdapterID != adapterID || s.BinaryVersion != supportedBinary || s.ProtocolVersion != qoderProtocolVersion || s.EventContract != conformanceEventContract || s.PermissionMode != qoderPermissionMode || request.AdapterID != adapterID || result.Adapter.ID != adapterID || result.Adapter.Version != supportedBinary {
		return observation, errors.New("adapter-identity-mismatch")
	}
	if capability.AdapterID != adapterID || capability.BinaryVersion != supportedBinary || capability.ProbeStatus != "supported" || capability.ExecutableDigest == "" || capability.Executable != result.Adapter.Executable {
		return observation, errors.New("capability-identity-mismatch")
	}
	admissionDigest := capability.ConformanceEvidenceDigest
	if capability.AuthorityMode == "ordinary-user" {
		admissionDigest = capabilityDigest
	}
	if !validSHA256Digest(admissionDigest) {
		return observation, errors.New("admission-evidence-missing")
	}
	for _, required := range profile.RequiredConstraints {
		found := false
		for _, actual := range task.Work.Constraints {
			if actual == required {
				found = true
				break
			}
		}
		if !found {
			return observation, errors.New("required-constraint-missing")
		}
	}
	if err := validateCanonicalTranscriptLines(input.Transcript); err != nil {
		return observation, errors.New("closed-transcript-json-invalid")
	}
	capture := decodeTranscript(input.Transcript)
	if capture.err != nil || !capture.terminal.seen || !capture.terminal.success || validateWorkerResultTransportSequence(capture) != nil {
		return observation, errors.New("qoder-v5-transcript-invalid")
	}
	if capture.cliVersion != s.BinaryVersion || capture.protocolVersion != s.ProtocolVersion || capture.permissionMode != s.PermissionMode {
		return observation, errors.New("transcript-identity-mismatch")
	}
	if meta.CapturedBytes != len(input.Transcript) || meta.EventCount != capture.eventCount || meta.AssistantMessages != capture.assistantCount || meta.ToolCalls != capture.toolCalls || meta.QoderCLIVersion != capture.cliVersion || meta.ProtocolVersion != capture.protocolVersion || meta.PermissionMode != capture.permissionMode || meta.SessionID != capture.sessionID || meta.Model != capture.model || meta.InputTokens != capture.inputTokens || meta.OutputTokens != capture.outputTokens || meta.OutputTruncated || meta.StderrTruncated || meta.ExitCode != 0 || meta.Signal != "" || meta.ContextError != "" || meta.FailureKind != "" || meta.RetryDisposition != "" || meta.PermissionDenied || meta.DenialsBenign != 0 || meta.DenialsFatal != 0 || meta.WorkerResultTeeAttempts != capture.resultTransport.attempts || meta.WorkerResultTeeSuccesses != capture.resultTransport.successes || !meta.WorkerResultTeeLast {
		return observation, errors.New("transcript-meta-mismatch")
	}
	if len(capture.observedTools) == 0 || !capture.observedTools[len(capture.observedTools)-1].resultTransportExplicit(capture) {
		return observation, errors.New("tee-result-not-explicit-success")
	}
	allowed := map[string]bool{}
	tools := task.Worker.Tools
	if len(tools) == 0 {
		tools = profile.DefaultAllowedTools
	}
	for _, tool := range tools {
		allowed[tool] = true
	}
	declaredChanged := map[string]bool{}
	for _, path := range result.DeclaredChangedFiles {
		declaredChanged[path] = true
	}
	commandProfile := map[string]string{}
	for _, command := range profile.Commands {
		commandProfile[command.CommandDigest] = command.CommandID
	}
	declaredCommands := map[string]string{}
	for _, command := range result.DeclaredCommands {
		declaredCommands[command.CommandID] = command.Status
	}
	worktree, err := canonicalDirectory(request.WorktreePath)
	if err != nil {
		return observation, errors.New("worktree-invalid")
	}
	scope, _, _ := verification.PolicyFromTask(task)
	for _, call := range capture.observedTools {
		if !allowed[call.tool] {
			return observation, errors.New("tool-not-allowed")
		}
		switch call.tool {
		case "read", "write", "edit", "grep", "glob":
			relative, err := attestToolPath(worktree, call.input)
			if err != nil {
				return observation, err
			}
			gate := verification.EvaluateScope(verification.Observation{ChangedFileCount: 1, Changes: []verification.Change{{Path: relative}}}, scope)
			if gate.Status != "pass" {
				return observation, errors.New("tool-path-out-of-scope")
			}
			if (call.tool == "write" || call.tool == "edit") && !declaredChanged[relative] {
				return observation, errors.New("write-path-not-declared")
			}
		case "bash":
			if call.resultTransport {
				continue
			}
			command, ok := qoderCommand(call.input)
			if !ok {
				return observation, errors.New("bash-input-invalid")
			}
			for _, word := range profile.ForbiddenCommandWords {
				if commandWord(command, word) {
					return observation, errors.New("forbidden-command-executed")
				}
			}
			digest := canonical.DigestBytes([]byte(command))
			id, ok := commandProfile[digest]
			if !ok || declaredCommands[id] != call.status {
				return observation, errors.New("command-binding-mismatch")
			}
			observation.Commands = append(observation.Commands, TranscriptAttestationCommand{CommandID: id, CommandDigest: digest, Status: call.status})
		default:
			return observation, errors.New("unreviewed-tool")
		}
	}
	if len(observation.Commands) != len(result.DeclaredCommands) {
		return observation, errors.New("declared-command-count-mismatch")
	}
	if err := bindTeePayload(capture.resultTransport.payload, input.WorkerResult); err != nil {
		return observation, err
	}
	observation.EventCount = capture.eventCount
	observation.AssistantMessages = capture.assistantCount
	observation.ToolCalls = capture.toolCalls
	observation.CommandCalls = len(observation.Commands)
	observation.WorkerResultTeeLast = true
	observation.CapabilityDigest = capabilityDigest
	observation.AdmissionEvidenceDigest = admissionDigest
	observation.ExecutableDigest = capability.ExecutableDigest
	observation.WorkerResultTransportDigest = expectedWorkerResultTransportDigest()
	observation.ProfileDigest = profileDigest
	if observation.Commands == nil {
		observation.Commands = []TranscriptAttestationCommand{}
	}
	return observation, nil
}

func validateCanonicalTranscriptLines(raw []byte) error {
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 64<<10), 8<<20)
	count := 0
	for scanner.Scan() {
		count++
		if len(scanner.Bytes()) == 0 {
			return errors.New("blank-line")
		}
		if _, err := canonical.JSON(scanner.Bytes()); err != nil {
			return err
		}
	}
	if scanner.Err() != nil || count == 0 {
		return errors.New("invalid-jsonl")
	}
	return nil
}

func bindTeePayload(payload, accepted []byte) error {
	if _, err := canonical.JSON(payload); err != nil {
		return errors.New("tee-payload-invalid")
	}
	var declared map[string]json.RawMessage
	var result map[string]json.RawMessage
	if strictJSON(payload, &declared) != nil || strictJSON(accepted, &result) != nil {
		return errors.New("tee-payload-invalid")
	}
	for _, field := range []string{"apiVersion", "kind", "taskId", "runId", "attemptId", "status", "summary", "declaredChangedFiles", "declaredArtifacts", "declaredCommands", "declaredRisks"} {
		left, lok := declared[field]
		right, rok := result[field]
		if lok != rok {
			return errors.New("tee-payload-mismatch")
		}
		if lok {
			lc, _ := canonical.JSON(left)
			rc, _ := canonical.JSON(right)
			if !bytes.Equal(lc, rc) {
				return errors.New("tee-payload-mismatch")
			}
		}
	}
	return nil
}

func (call observedToolCall) resultTransportExplicit(capture captureResult) bool {
	return call.ordinal == capture.resultTransport.successfulOrdinal && call.explicitSuccess
}

func strictJSON(raw []byte, target any) error {
	if _, err := canonical.JSON(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("trailing-json")
	}
	return nil
}

func canonicalDirectory(path string) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", errors.New("not-canonical")
	}
	actual, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(actual)
	if err != nil || !info.IsDir() {
		return "", errors.New("not-directory")
	}
	return actual, nil
}

func attestToolPath(worktree string, raw json.RawMessage) (string, error) {
	var value map[string]json.RawMessage
	if strictJSON(raw, &value) != nil {
		return "", errors.New("tool-input-invalid")
	}
	var target string
	for _, key := range []string{"filePath", "file_path", "absolute_path", "path", "directory", "dir"} {
		if encoded, ok := value[key]; ok {
			var candidate string
			if json.Unmarshal(encoded, &candidate) != nil || candidate == "" || target != "" {
				return "", errors.New("tool-path-ambiguous")
			}
			target = candidate
		}
	}
	if target == "" || strings.ContainsAny(target, "\\\x00") {
		return "", errors.New("tool-path-invalid")
	}
	var absolute string
	if filepath.IsAbs(target) {
		if filepath.Clean(target) != target {
			return "", errors.New("tool-path-noncanonical")
		}
		absolute = target
	} else {
		if filepath.Clean(target) != target || target == "." || strings.HasPrefix(target, ".."+string(filepath.Separator)) {
			return "", errors.New("tool-path-noncanonical")
		}
		absolute = filepath.Join(worktree, target)
	}
	rel, err := filepath.Rel(worktree, absolute)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("tool-path-escape")
	}
	componentPath := worktree
	for _, component := range strings.Split(rel, string(filepath.Separator)) {
		componentPath = filepath.Join(componentPath, component)
		info, statErr := os.Lstat(componentPath)
		if statErr == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("tool-path-symlink-escape")
		}
		if errors.Is(statErr, os.ErrNotExist) {
			break
		}
		if statErr != nil {
			return "", errors.New("tool-path-invalid")
		}
	}
	probe := absolute
	for {
		resolved, resolveErr := filepath.EvalSymlinks(probe)
		if resolveErr == nil {
			relResolved, e := filepath.Rel(worktree, resolved)
			if e != nil || relResolved == ".." || strings.HasPrefix(relResolved, ".."+string(filepath.Separator)) {
				return "", errors.New("tool-path-symlink-escape")
			}
			break
		}
		if !errors.Is(resolveErr, os.ErrNotExist) {
			return "", errors.New("tool-path-invalid")
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return "", errors.New("tool-path-invalid")
		}
		probe = parent
	}
	return filepath.ToSlash(rel), nil
}

func qoderCommand(raw json.RawMessage) (string, bool) { return decodeCanonicalQoderBashInput(raw) }
func commandWord(command, word string) bool {
	pattern := `(^|[^A-Za-z0-9._+\-])` + regexp.QuoteMeta(word) + `([^A-Za-z0-9._+\-]|$)`
	return regexp.MustCompile(pattern).FindStringIndex(command) != nil
}

func TranscriptAttestationImplementationIdentity() map[string]string {
	return map[string]string{"profileVersion": TranscriptAttestationProfileVersion, "adapterVersion": adapterVersion, "eventContract": conformanceEventContract, "workerResultTransportDigest": expectedWorkerResultTransportDigest()}
}
