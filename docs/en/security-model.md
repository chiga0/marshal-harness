# Security Model

> English · 中文原文见 [安全模型](../security-model.md)

## Security positioning

Marshal orchestrates processes that can edit files and execute repository code, which is inherently high-impact. The MVP targets a single developer, trusted Worker Binaries, and developer-controlled repositories. It provides strict auditing and workflow control, but host isolation only within an enforceable Sandbox Profile.

Security claims must bind to the effective Execution Profile and be recorded in the Outcome Bundle. Merely telling the model "operate safely" does not make a Run sandboxed.

## Assets under protection

- source repositories and uncommitted work;
- Git history and remote Branches;
- SSH Keys, Forge Tokens, Cloud Credentials, and Signing Keys;
- private source, prompts, logs, and generated deliverables;
- maintainer workstations and local services;
- CI minutes, model budgets, and network resources;
- integrity of Review and Publication Decisions.

## Participants

- maintainers and the Lead Agent;
- Marshal Core, Verifier, and Publisher;
- Worker Binaries and selected Models/Providers;
- repository content, instructions, dependencies, tests, and hooks;
- Forge and CI Providers;
- third-party Adapter or Plugin Authors.

Even when a repository is developer-controlled, the text inside it is still untrusted instruction input; dependencies and build scripts can execute arbitrary code.

## Trust boundaries

### Semantic boundary

TaskSpec and Repository Policy take precedence over Worker Prompts and auto-discovered repository instructions. Prompt Injection must never widen paths, enable publication, expose credentials, or exempt gates.

### Filesystem boundary

Worktree isolation separates task changes from the primary Checkout, but ordinary file permissions cannot stop a malicious host process from reading other paths. `workspace-write` is therefore protection against accidents, not a strong security boundary.

The repository-local `.marshal/` holds Runs, Logs, and linked worktrees, and is ignored by Git by default. The ignore rule only prevents accidental commits and is not access control; the Verifier and Publisher must still reject any result that brings `.marshal/` content into business Diffs, Source Artifacts, or Commit Trees.

### Credential boundary

Workers run with a constructed environment and never receive Publisher Tokens, `SSH_AUTH_SOCK`, Cloud Profiles, or known Secret Variables. This reduces exposure, but it must not be treated as strong isolation when the Home Directory, Keychain, Credential Helper, or local network services remain reachable.

Strong isolation requires the `hardened` profile, with explicit Mounts, Network Policy, and the host Credential Store removed.

### Network boundary

A Network Intent in the TaskSpec is only considered enforced when the Process Sandbox can actually filter network traffic. When enforcement is impossible, it must be recorded as `unenforced`, and Repository Policy may refuse the Run.

## Execution Profiles

| Profile | Use case | Claimable guarantees |
| --- | --- | --- |
| `read-only` | Inspection and Review | Marshal grants no Edit Tool; Host Process isolation still depends on the Sandbox |
| `workspace-write` | Trusted local coding | dedicated Worktree, filtered environment, workflow gates; does not isolate malicious code |
| `hardened` | Untrusted code or unattended use | Container/VM/OS Sandbox enforcing Mount, Network, Resource, and Credential Isolation |

Repository Policy selects the minimum Profile. When an Adapter cannot meet it, it must fail before `READY`.

## Threats and mitigations

| Threat | Mitigation | Residual risk |
| --- | --- | --- |
| Worker modifies unrelated files | Allow/Deny paths and independent Diff | without Hardening, commands may still affect paths outside the Worktree |
| Worker falsely reports tests passing | Marshal reruns the exact commands | tests themselves may be incomplete or flaky |
| Worker pushes or opens PRs | credentials removed, publication Tool forbidden, Publisher separation | without Hardening, ambient credentials may still be reachable |
| Repository prompt injection | frozen TaskSpec precedence, instruction digest recorded, policy relaxation forbidden | the model may still write wrong code within allowed bounds |
| Malicious test/build scripts | Hardened Profile, explicit commands, network/resource limits | `workspace-write` cannot isolate malicious scripts |
| Secrets written to logs | environment allowlist, bounded capture, redaction, restricted file permissions | not all secrets can be identified |
| Symlink/path traversal | canonicalization, `..` forbidden, root checks, escape-hunting collection | platform-specific races need testing |
| Output/resource exhaustion | time, byte, process, and provider-native budgets | model cost may still be incurred before termination |
| Stale decisions publishing new code | evidence digests and pre-publication snapshot recheck | hash/canonicalization implementation bugs |
| Duplicate or overwritten PRs | provider IDs, task markers, force-push disabled by default | manual remote edits need reconciliation |
| Git hooks causing side effects | Publisher uses controlled/disabled hooks | verification commands may still execute repository scripts |
| Adapter/Plugin compromise | explicit install trust, subprocess boundary, version snapshots | the MVP cannot safely run arbitrary in-process Plugins |

## Default-denied side effects

Workers are forbidden from:

- `git push`, Forge APIs, PR/MR creation, merge, releases, deployments, and package publishing;
- reading credential stores or actively discovering secrets;
- modifying Git remotes, global Git config, hooks, or repository settings;
- modifying files outside the task Worktree;
- enabling network or extra tools on their own;
- spawning other Coding Agents without explicit TaskSpec/Policy authorization.

Prompt prohibitions must be enforced by Process/Tool Policy wherever possible. When a Provider cannot satisfy them, the Capability Probe should fail, or the Run must be explicitly marked as lower assurance.

## Environment construction

Marshal constructs the environment from an allowlist instead of inheriting the environment and merely deleting a few known variables. It only provides the Paths, Locale, Temporary Storage, approved Toolchains, and explicit non-secret configuration required for execution.

A native PTY must likewise never inherit the ambient environment of a Desktop, cmux, or login shell. Marshal hands the exact environment to a trusted launcher via an owner-only, single-use `LaunchEnvelope`; the visible argv contains only the envelope path, and the launcher deletes the envelope before `exec`ing the Worker. Environment values must never enter the screen, Journal, or ordinary logs. This mechanism reduces accidental leakage but does not isolate a malicious host process with the same UID; strong isolation still requires the `hardened` profile.

Secrets are resolved just-in-time only inside the authorized components that need them. Publisher credentials must never be written into TaskSpec, Events, Prompts, or Outcome files.

## Temporary files and permissions

- State and Logs use owner-only permissions where the platform supports them.
- `marshal init` excludes `/.marshal/` via `.git/info/exclude` by default; the tracked `.gitignore` is only modified when explicitly opted in.
- Temporary Files live in Run-owned directories with unpredictable names and atomic renames.
- Workers use a dedicated temporary directory.
- Unix Sockets, FIFOs, Device Files, and Symlinks must not be ordinary Artifacts by default.
- Cleanup never follows Symlinks out of the Run/Worktree Root.

## Supply chain

- The Go toolchain and Marshal dependencies are locked; `go.mod` and `go.sum` are committed.
- Worker executable paths are resolved and versions recorded.
- Workers must never auto-update during a Run.
- Initial third-party Adapters must never run in-process.
- Marshal's own CI runs dependency audit, format, vet, static checks, tests, and secret scan.
- Adapter documentation is reference only; real support is decided by Feature Probes and conformance tests.

## Review and publication integrity

- A ReviewDecision must carry Evidence Identity.
- The Publisher recomputes the Snapshot and Evidence before side effects.
- Controlled commits use raw blobs for ordinary files and link targets for symlinks; observation and publication both mask repository-local filters, ambient hooks, credential helpers, and system/global Git config.
- When a mandatory gate failed, Accept is impossible without a policy-valid Waiver.
- The Publisher records the authenticated Forge identity but never exposes the Token.
- The Publisher only accepts new-branch creation without force-push, or rework fast-forwards proven via `previousHeadSha`; CI must bind the same repository, Draft PR, and head SHA.
- Actual merges are not within MVP authority.

## Security readiness levels

### Local MVP

Suitable for the maintainer's own trusted repositories with interactive supervision. The CLI must clearly state that Host Containment and Network Denial carry no strong guarantees.

### Unattended Isolated Runner

Requires an ephemeral runner/container, minimal Forge Token, read-only base checkout before publication, explicit Network Policy, and a separate publication job.

### Multi-user / Hostile-code Service

Out of current scope. A dedicated threat model and hardened isolation review must be completed first; the Local MVP must never be advertised as meeting this level.

## Security acceptance criteria

Before the MVP may be declared usable, the following must be proven:

- Worker output cannot widen the TaskSpec scope;
- the Worker environment contains no Publisher credentials;
- stale evidence cannot publish a changed Snapshot;
- path traversal and symlink escape fixtures fail by default;
- cancellation terminates the child process tree;
- logs/state use restricted permissions where the platform supports them;
- the CLI clearly shows the effective assurance profile;
- documentation continuously states that ordinary host execution is not a malicious-code sandbox.
