package verification

import (
	"context"
	"errors"
	"time"

	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/verificationbuiltin"
)

const (
	builtinTaskSpecMaxBytes = int64(1 << 20)
	builtinTaskSpecValid    = "contract-task-spec-valid\n"
	reasonDeliverableDenied = "contract-deliverable-denied"
	reasonArtifactDenied    = "contract-artifact-denied"
	reasonArtifactTooLarge  = "contract-artifact-too-large"
	reasonSchemaInvalid     = "contract-schema-invalid"
	reasonSemanticInvalid   = "contract-semantic-invalid"
	reasonBuiltinTimeout    = "contract-timeout"
	reasonBuiltinInternal   = "contract-internal-failure"
)

type builtinArtifact struct {
	Bytes  []byte
	Digest string
}

func runTaskSpecBuiltin(ctx context.Context, isolate string, spec CommandSpec, plan verificationbuiltin.Plan) (CommandResult, string, bool, string) {
	started := time.Now().UTC()
	result := CommandResult{
		Status:    "error",
		StartedAt: started,
		Record: CommandRecord{
			Argv:           append([]string(nil), spec.Argv...),
			CWD:            ".",
			Executable:     verificationbuiltin.TaskSpecV1,
			StartedAt:      started,
			BaselineStatus: "not-run",
		},
	}
	deadlineContext, cancel := context.WithTimeout(ctx, spec.Timeout)
	defer cancel()
	if deadlineContext.Err() != nil {
		return finishTaskSpecBuiltin(result, reasonBuiltinTimeout, false), "", false, reasonBuiltinTimeout
	}
	artifact, reason := readTaskSpecBuiltinArtifact(deadlineContext, isolate, plan.PathGlob, builtinArtifactReadHooks{})
	if reason != "" {
		return finishTaskSpecBuiltin(result, reason, false), "", false, reason
	}
	if deadlineContext.Err() != nil {
		return finishTaskSpecBuiltin(result, reasonBuiltinTimeout, false), artifact.Digest, true, reasonBuiltinTimeout
	}
	validator, err := contract.NewValidator()
	if err != nil {
		return finishTaskSpecBuiltin(result, reasonBuiltinInternal, false), artifact.Digest, true, reasonBuiltinInternal
	}
	if deadlineContext.Err() != nil {
		return finishTaskSpecBuiltin(result, reasonBuiltinTimeout, false), artifact.Digest, true, reasonBuiltinTimeout
	}
	err = validator.Validate(domain.KindTask, artifact.Bytes)
	if deadlineContext.Err() != nil {
		return finishTaskSpecBuiltin(result, reasonBuiltinTimeout, false), artifact.Digest, true, reasonBuiltinTimeout
	}
	if err != nil {
		var semantic *contract.SemanticError
		if errors.As(err, &semantic) {
			return finishTaskSpecBuiltin(result, reasonSemanticInvalid, false), artifact.Digest, true, reasonSemanticInvalid
		}
		return finishTaskSpecBuiltin(result, reasonSchemaInvalid, false), artifact.Digest, true, reasonSchemaInvalid
	}
	return finishTaskSpecBuiltin(result, builtinTaskSpecValid, true), artifact.Digest, true, ""
}

func closedTaskSpecBuiltinCommand(spec CommandSpec, reason string) CommandResult {
	started := time.Now().UTC()
	result := CommandResult{
		Status:    "error",
		StartedAt: started,
		Record: CommandRecord{
			Argv:           append([]string(nil), spec.Argv...),
			CWD:            ".",
			Executable:     verificationbuiltin.TaskSpecV1,
			StartedAt:      started,
			BaselineStatus: "not-run",
		},
	}
	result = finishTaskSpecBuiltin(result, reason, false)
	result.Status = "error"
	return result
}

func finishTaskSpecBuiltin(result CommandResult, output string, passed bool) CommandResult {
	ended := time.Now().UTC()
	exitCode := 1
	if passed {
		exitCode = 0
		result.Status = "pass"
		result.Stdout = []byte(output)
	} else {
		result.Status = "fail"
		result.Stderr = []byte(output + "\n")
	}
	result.EndedAt = ended
	result.Record.CompletedAt = ended
	result.Record.DurationMilliseconds = ended.Sub(result.StartedAt).Milliseconds()
	result.Record.ExitCode = &exitCode
	return result
}

func parseBuiltinPlan(spec CommandSpec, deliverables []Deliverable) (verificationbuiltin.Plan, bool, error) {
	command := domain.TaskCommand{ID: spec.ID, Argv: append([]string(nil), spec.Argv...), CWD: spec.CWD, TimeoutSeconds: int64(spec.Timeout / time.Second), Required: spec.Required, BaselinePolicy: spec.BaselinePolicy, MaxLogBytes: spec.MaxLogBytes}
	domainDeliverables := make([]domain.TaskDeliverable, 0, len(deliverables))
	for _, item := range deliverables {
		domainDeliverables = append(domainDeliverables, domain.TaskDeliverable{ID: item.ID, Kind: item.Kind, Required: item.Required, PathGlob: item.PathGlob, MediaType: item.MediaType, MinimumCount: item.MinimumCount, Description: item.Description})
	}
	return verificationbuiltin.Parse(command, domainDeliverables)
}
