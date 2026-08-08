# Task Lifecycle

> English · 中文原文见 [任务生命周期](../task-lifecycle.md)

## Purpose

The lifecycle is the persisted contract between Planning, Worker execution, Verification, Review, Publishing, and Recovery. Natural-language messages cannot change state; only guarded application commands may append transition events and atomically update the state snapshot.

## Identity

- **Task**: stable engineering intent identified by `taskId`.
- **Run**: one execution using the same frozen spec and locked baseline, identified by `runId`.
- **Attempt**: one Worker invocation within a Run, identified by `attemptId`.
- **Review Round**: one decision for one evidenceDigest.

Retry means an infrastructure or provider execution failure, and creates a new Attempt. Rework means the code or semantics failed a gate, and also creates a new Attempt. Neither may modify the frozen TaskSpec.

## States

| State | Meaning | Persistence precondition |
| --- | --- | --- |
| `CREATED` | Task identity created | initial metadata |
| `PLANNED` | TaskSpec draft exists | draft passes Schema |
| `READY` | Inputs frozen, ready to execute | base SHA, spec/policy digest, CapabilitySnapshot, worktree Lease |
| `RUNNING` | one Worker Attempt holds the worktree | Attempt Record and valid Lease |
| `RETRY_PENDING` | Attempt failed with a retryable issue | failure classification and saved worktree snapshot |
| `VERIFYING` | Marshal is observing Worker results | completed Attempt and snapshot identity |
| `REVIEW_PENDING` | full ReviewPacket awaits semantic judgment | VerificationReport and ArtifactManifest |
| `REWORK_REQUESTED` | next Attempt has explicit blocking feedback | ReviewDecision or mandatory gate failure, budget remaining |
| `PUBLISHING` | accepted evidence is being committed and published | Accept decision matching the current evidenceDigest |
| `PUBLISHED` | PR/MR created or updated | remote Publication Record |
| `CI_PENDING` | required remote checks have not finished | published changes and check set |
| `ACCEPTED` | all required gates and semantic Review passed | final decision plus required publication/CI evidence |
| `REJECTED` | work unsuitable and not continued | Reject decision or rework budget exhausted |
| `BLOCKED` | external input or capability needed | concrete Blocker Record |
| `ABORTED` | an authorized operator stopped the Run (reserved state) | abort reason and saved evidence; the v1 implementation expresses this as `BLOCKED` + `terminalReason=aborted-by-operator` (ADR 0012) |
| `NO_CHANGE` | Review confirmed no repository change is needed | no-change decision and diagnostic evidence |

`ACCEPTED`, `REJECTED`, `BLOCKED`, `ABORTED`, and `NO_CHANGE` are Run terminal states. Resolving a Blocker or changing a terminal decision must create a new Run linked to the old one.

## Transition table

| From | To | Guard |
| --- | --- | --- |
| `CREATED` | `PLANNED` | TaskSpec draft valid |
| `PLANNED` | `READY` | baseline resolvable, policy allows, Adapter Probe passes, state frozen |
| `READY` | `RUNNING` | Writer Lease acquired and Attempt budget remaining |
| `RUNNING` | `VERIFYING` | Worker protocol completed; process result and filesystem snapshot recorded |
| `RUNNING` | `RETRY_PENDING` | failure retryable and budget remaining |
| `RUNNING` | `BLOCKED` | missing capability, credentials, or input, or failure not safely retryable |
| `RETRY_PENDING` | `RUNNING` | backoff elapsed or explicit operator retry; new Attempt ID assigned |
| `VERIFYING` | `REVIEW_PENDING` | VerificationReport and Manifest complete, even if a mandatory gate failed |
| `REVIEW_PENDING` | `REWORK_REQUESTED` | verdict is rework and budget remains |
| `REVIEW_PENDING` | `REJECTED` | verdict is reject or rework budget exhausted |
| `REVIEW_PENDING` | `BLOCKED` | verdict requires external information or permissions |
| `REVIEW_PENDING` | `NO_CHANGE` | verdict is no_change and the TaskSpec allows it |
| `REVIEW_PENDING` | `PUBLISHING` | verdict is accept, mandatory gates pass, publication required |
| `REVIEW_PENDING` | `ACCEPTED` | verdict is accept, mandatory gates pass, no publication needed |
| `REWORK_REQUESTED` | `RUNNING` | new Attempt receives blocking issues and the Lease |
| `PUBLISHING` | `PUBLISHED` | idempotent publication succeeded |
| `PUBLISHING` | `BLOCKED` | credentials, authorization, or remote policy failure, not retryable |
| `PUBLISHED` | `CI_PENDING` | TaskSpec requires remote checks |
| `PUBLISHED` | `ACCEPTED` | no remote checks required |
| `CI_PENDING` | `ACCEPTED` | required checks pass for the current published head SHA |
| `CI_PENDING` | `REWORK_REQUESTED` | checks failed, budget remains, fixable via code |
| `CI_PENDING` | `BLOCKED` | failure is external or requires maintainer action |
| `RETRY_PENDING` | `BLOCKED` | explicit abort (`run.aborted`, ADR 0012): human actor, LeaseHeld, terminal Outcome written; v1 does not enable the `ABORTED` state |

An unexpected process exit never creates a transition automatically. Recovery must first compare the Journal, Snapshot, Process Lease, and worktree state, then choose a legal transition.

## Enforced invariants

### Frozen execution inputs

Upon entering `READY`, the following are frozen:

- canonical TaskSpec and digest;
- resolved base SHA;
- effective configuration and PolicySnapshot;
- Adapter executable paths and CapabilitySnapshot;
- mandatory acceptance commands and deliverables.

Changing any frozen item creates a new Run. Review feedback only describes which parts of the old contract were not met; it never changes the spec.

### Evidence binding

A VerificationReport binds `runId`, `specDigest`, `baseSha`, and the real snapshot/diff digest. A ReviewDecision binds the ReviewPacket, VerificationReport, and ArtifactManifest digest. The Publisher rejects decisions referencing stale evidence.

### Single writer

Only one Writer Lease holder — Worker Attempt, Verifier, or Publisher — may hold the worktree at a time. Verification commands that may produce files must also hold the Lease and re-check the dirty tree after execution.

### No silent waivers

With a failed mandatory gate, `accept` must not enter publication. If repository policy allows it, a maintainer may create a versioned Waiver Decision specifying the gate, reason, approver, expiry or scope, and evidenceDigest; natural-language comments cannot act as waivers.

## Retry and Rework budgets

- `maxAttempts`: total Worker invocations.
- `maxOperationalRetries`: retries for provider, protocol, or process failures.
- `maxReworkRounds`: implementation cycles caused by Verification or Review.
- `runTimeoutSeconds`: total Run wall time.
- `attemptTimeoutSeconds`: time for a single Worker invocation.

The first exhausted budget wins. When the code contract cannot be met, enter `REJECTED`; when external capacity or authorization prevents a normal attempt, enter `BLOCKED`.

## Empty changes and No-change

When the real diff is empty, it must not count as a successful Coding Change even if the Worker claims the repository was already correct.

- `allowNoChange=false`: an empty diff is a verification failure.
- `allowNoChange=true`: the Lead Agent may return `no_change` only when a diagnostic deliverable exists explaining why no change is needed.
- `NO_CHANGE` does not create a PR/MR by default.

## Post-publication CI

CI results must bind the exact published head SHA. Green checks for older commits cannot satisfy the gate. After rework updates the branch, old checks are invalidated and the lifecycle goes through Verification, Review, Publishing, and `CI_PENDING` again.

## Cleanup

Cleanup is not a state transition and must never destroy the Outcome Bundle.

- An accepted worktree may be removed after the publication record and patch digest are persisted.
- Dirty worktrees of Rejected, Blocked, or Aborted Runs are retained until explicit archiving or cleanup.
- The diff must be re-checked before cleanup; when new unarchived files exist, cleanup is refused by default.
