package pi

import (
	"errors"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Private provenance prevents provider-authored strings from becoming a
// diagnostic code. The original error remains available to internal tests;
// public composition uses only the closed code, never Error() or raw bytes.
type productionResultFailure struct {
	code  string
	cause error
}

func (e *productionResultFailure) Error() string { return e.cause.Error() }
func (e *productionResultFailure) Unwrap() error { return e.cause }

// ProductionResultFailureCode is diagnostic only. It grants no admission,
// retry or normalization authority, and is empty for unrelated errors.
func ProductionResultFailureCode(err error) string {
	var failure *productionResultFailure
	if !errors.As(err, &failure) || failure == nil {
		return ""
	}
	switch failure.code {
	case "input", "transcript", "output-limit", "provider-terminal", "session-missing",
		"transcript-read", "transcript-json", "transcript-session", "transcript-event", "transcript-agent-end",
		"transcript-tool", "transcript-compaction", "transcript-retry", "transcript-settled", "transcript-framing", "transcript-closure",
		"final-message", "final-object-missing", "final-object-trailing", "validator",
		"final-event-decode", "final-event-empty", "final-role", "final-content-shape", "final-content-type", "final-content-text",
		"declared-schema", "declared-decode", "declared-identity", "declared-session",
		"normalization", "normalized-schema":
		code := "pi-result-" + failure.code
		if failure.code == "declared-schema" || failure.code == "normalized-schema" {
			code += schemaFailureField(failure.cause)
		}
		return code
	default:
		return ""
	}
}

// Only a schema-owned top-level field label may leave this function. Never
// return instance values, arbitrary property names, indices or error text.
func schemaFailureField(err error) string {
	var root *jsonschema.ValidationError
	if !errors.As(err, &root) || root == nil {
		return ""
	}
	queue := []*jsonschema.ValidationError{root}
	seen := make(map[*jsonschema.ValidationError]bool)
	for visited := 0; len(queue) > 0 && visited < 64; visited++ {
		node := queue[0]
		queue = queue[1:]
		if node == nil || seen[node] {
			continue
		}
		seen[node] = true
		if len(node.InstanceLocation) > 0 {
			switch node.InstanceLocation[0] {
			case "adapter":
				return "-adapter"
			case "session":
				return "-session"
			case "usage":
				return "-usage"
			case "declaredArtifacts":
				return "-artifacts"
			case "declaredCommands":
				return "-commands"
			case "declaredChangedFiles":
				return "-files"
			case "startedAt", "completedAt":
				return "-timing"
			case "taskId", "runId", "attemptId":
				return "-identity"
			}
		}
		for _, cause := range node.Causes {
			if len(queue)+visited >= 64 {
				break
			}
			queue = append(queue, cause)
		}
	}
	return ""
}
