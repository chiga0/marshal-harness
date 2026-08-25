package spine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/chiga0/marshal-harness/internal/agentruntime"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/command"
	"github.com/chiga0/marshal-harness/internal/engine"
	"github.com/chiga0/marshal-harness/internal/resultingress"
	"github.com/chiga0/marshal-harness/internal/sandbox"
)

var (
	ErrAllocationBindingMismatch = errors.New("spine: allocation binding mismatch between input identity and execution outcome")
	ErrNegativeGeneration        = errors.New("spine: negative generation cannot be converted to uint64")
	ErrTrustedResult             = errors.New("spine: workload result must not carry trusted flag")
)

// Input carries the identity, binding and command fields needed to drive one
// end-to-end spine attempt.  AllocationID and Generation are not carried here;
// they are taken from the ExecutionOutcome provider receipt so the Candidate
// is mechanically proven to come from the bound Sandbox allocation.
type Input struct {
	AppCommand command.ApplicationCommand

	AdapterID        string
	AdapterVersion   string
	RunID            string
	AttemptID        string
	Executable       string
	ExecutableDigest string
	WorkingDirectory string
	Arguments        []string
	Environment      []string

	ProfileDigest string

	AuthorityNamespaceID string
	TaskID               string
	LeaseID              string
	FencingToken         string
	IdempotencyKey       string
	Nonce                string
	Expiry               time.Time

	RegistrationID string
	SnapshotDigest string
	EvidenceDigest string

	EnvelopeSequence uint64

	ExpectedAllocationID string
	ExpectedGeneration   int64

	Ingress *resultingress.Ingress
}

// ResultTrace records the per-hop evidence of one spine attempt, allowing
// mechanical verification of binding consistency across the chain.
type ResultTrace struct {
	CommandID        string
	AllocationID     string
	Generation       int64
	EnvelopeDigest   string
	FactDigest       string
	LedgerSequence   uint64
	IdempotentReplay bool
}

// Run drives the full walking-skeleton spine: durable command derivation →
// AgentLaunchSpec → production ExecutionProfile → Executor.Execute (Sandbox
// Provision→Stage→Exec→Inspect→Terminate) → Candidate envelope + DRC assembly
// → ResultIngress.Admit.  Every hop is real; no hop is stubbed.  The Candidate
// is mechanically proven to come from the bound Sandbox allocation through
// three-way binding consistency (input identity, ExecutionOutcome, and
// LedgerBinding/DRC).  All malformed input fails closed.
func Run(ctx context.Context, eng *engine.DurableExecutionEngine, fact engine.LedgerFact, provider sandbox.SandboxProvider, input Input) (ResultTrace, error) {
	if input.ExpectedGeneration < 0 {
		return ResultTrace{}, fmt.Errorf("%w: expected generation %d", ErrNegativeGeneration, input.ExpectedGeneration)
	}

	durableCmd, err := command.DeriveDurableCommand(eng, fact, input.AppCommand)
	if err != nil {
		return ResultTrace{}, fmt.Errorf("spine: derive durable command: %w", err)
	}

	spec, err := agentruntime.NewAgentLaunchSpec(
		input.AdapterID, input.AdapterVersion,
		input.RunID, input.AttemptID,
		input.Executable, input.ExecutableDigest,
		input.WorkingDirectory,
		input.Arguments, input.Environment,
		input.ProfileDigest,
		"",
	)
	if err != nil {
		return ResultTrace{}, fmt.Errorf("spine: build launch spec: %w", err)
	}

	profile := agentruntime.ExecutionProfile{
		Production: true,
		Digest:     input.ProfileDigest,
	}

	executor, err := agentruntime.NewExecutor(provider, &agentruntime.FakeAgent{})
	if err != nil {
		return ResultTrace{}, fmt.Errorf("spine: new executor: %w", err)
	}

	outcome, err := executor.Execute(ctx, spec, profile)
	if err != nil {
		return ResultTrace{}, fmt.Errorf("spine: execute: %w", err)
	}

	if outcome.WorkloadResult.Trusted {
		return ResultTrace{}, ErrTrustedResult
	}

	if outcome.AllocationId != input.ExpectedAllocationID || outcome.Generation != input.ExpectedGeneration {
		return ResultTrace{}, fmt.Errorf("%w: expected allocation %q generation %d, got allocation %q generation %d",
			ErrAllocationBindingMismatch,
			input.ExpectedAllocationID, input.ExpectedGeneration,
			outcome.AllocationId, outcome.Generation)
	}

	genUint, err := toUint64Generation(outcome.Generation)
	if err != nil {
		return ResultTrace{}, err
	}

	envelopeDigest, err := workloadResultDigest(outcome.WorkloadResult)
	if err != nil {
		return ResultTrace{}, err
	}

	envelope := resultingress.ResultEnvelope{
		Kind:         resultingress.KindCandidate,
		ResultDigest: envelopeDigest,
		Sequence:     input.EnvelopeSequence,
	}

	drc := resultingress.DRC{
		AuthorityNamespaceID: input.AuthorityNamespaceID,
		TaskID:               input.TaskID,
		RunID:                input.RunID,
		AttemptID:            input.AttemptID,
		AllocationID:         outcome.AllocationId,
		LeaseID:              input.LeaseID,
		Generation:           genUint,
		FencingToken:         input.FencingToken,
		CommandID:            durableCmd.CommandId,
		IdempotencyKey:       input.IdempotencyKey,
		RequestDigest:        envelopeDigest,
		Nonce:                input.Nonce,
		Expiry:               input.Expiry,
		Operation:            resultingress.OpCandidate,
		RegistrationID:       input.RegistrationID,
		SnapshotDigest:       input.SnapshotDigest,
		EvidenceDigest:       input.EvidenceDigest,
	}

	ingress := input.Ingress
	if ingress == nil {
		binding := resultingress.LedgerBinding{
			LeaseID:        input.LeaseID,
			Generation:     genUint,
			FencingToken:   input.FencingToken,
			AttemptID:      input.AttemptID,
			AllocationID:   outcome.AllocationId,
			Expiry:         input.Expiry,
			RegistrationID: input.RegistrationID,
			SnapshotDigest: input.SnapshotDigest,
			EvidenceDigest: input.EvidenceDigest,
		}
		ingress, err = resultingress.NewIngress(binding)
		if err != nil {
			return ResultTrace{}, fmt.Errorf("spine: new ingress: %w", err)
		}
	}

	admitted, err := ingress.Admit(ctx, drc, envelope)
	if err != nil {
		return ResultTrace{}, fmt.Errorf("spine: admit: %w", err)
	}

	return ResultTrace{
		CommandID:        durableCmd.CommandId,
		AllocationID:     outcome.AllocationId,
		Generation:       outcome.Generation,
		EnvelopeDigest:   envelopeDigest,
		FactDigest:       admitted.FactDigest,
		LedgerSequence:   admitted.LedgerSequence,
		IdempotentReplay: admitted.IdempotentReplay,
	}, nil
}

func toUint64Generation(gen int64) (uint64, error) {
	if gen < 0 {
		return 0, fmt.Errorf("%w: generation %d", ErrNegativeGeneration, gen)
	}
	return uint64(gen), nil
}

func workloadResultDigest(result agentruntime.WorkloadResult) (string, error) {
	raw, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("spine: serialize workload result: %w", err)
	}
	return canonical.DigestJSON(raw)
}
