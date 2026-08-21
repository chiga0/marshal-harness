# Quick Start

> English · 中文原文见 [开发指南](../development.md)

## Current stage

The repository has completed Milestones 0–6 and the Local MVP is marked `USABLE` (see [roadmap-status.md](../roadmap-status.md)). Version and release semantics are described in [CHANGELOG.md](https://github.com/chiga0/marshal-harness/blob/main/CHANGELOG.md); stage progression is defined by tags.

## Go baseline

- Module: `github.com/chiga0/marshal-harness`;
- Language Version: Go `1.26.0`;
- Toolchain: Go `1.26.5`;
- JSON Schema: Draft 2020-12;
- Glob: `doublestar/v4`, where `**` means recursive across directories;
- Static checks: `go vet` and a pinned `staticcheck` version (pinned via the `tool` directive in `go.mod`, a Go 1.24+ feature; do not `go install` a different version yourself, or results may diverge);
- Delivery: a single `marshal` executable;
- Toolchain downloads: when the local Go is older than `go.mod` requires, toolchain auto-download needs network access; in restricted environments install a matching version in advance.

The GitHub remote is bound to `github.com/chiga0/marshal-harness`, matching the module path.

## Package boundaries

```text
cmd/marshal/        CLI process entry point
internal/cli/       argument parsing and exit codes
internal/cleanup/   Cleanup Preview, Guard, Crash-safe Tombstone and explicit Apply
internal/app/       Application Service and dependency assembly
internal/domain/    Provider-neutral Domain Types
internal/contract/  Schema compilation and Semantic Validators
internal/port/      Worker, Verifier, Lead Agent, Publisher Ports
internal/adapter/   Adapter Registry, Fake and OpenCode Worker
internal/gitworktree/ Repository Identity, linked worktrees and locks
internal/verification/ independent Diff, Scope, Command and Artifact verification
internal/review/   ReviewPacket, Decision Guard, Outcome and crash-safe records
internal/publication/ controlled Commit, publication evidence gates and remote CI status
internal/publisher/github/ GitHub Draft Publisher with separate credentials
schemas/            JSON Schemas, fixtures and Embedded FS
.agents/skills/marshal/ lightweight Skill shared by Codex CLI/Desktop
```

The Core Domain and Contract packages must not import provider-specific packages. The CLI is only a thin entry point into the Application Service.

## Local commands

```bash
make format
make vet
make lint
make test
make build
make check
make vuln
make ci
```

`make check` runs Format Check, Vet, Staticcheck, Race Test and Build; `make ci` additionally runs `govulncheck`. **A full `make check` takes about 15 minutes in this repository** (dominated by the race-enabled full test suite); for fast daily feedback run `go test ./internal/<package>/...` first. Build output defaults to `bin/marshal`, a directory ignored by Git.

GitHub Actions runs the same quality gates and vulnerability scan on Linux and macOS, with Secret Scan in a separate job. External Actions are pinned to full commit SHAs and workflows default to `contents: read` permissions.

## Installation

The two user-facing installation paths (one-line script and source build) are described in the README's "Installation" section; the corresponding script is [`scripts/install.sh`](https://github.com/chiga0/marshal-harness/blob/main/scripts/install.sh):

- detects the platform (`darwin|linux` × `amd64|arm64`);
- when a GitHub release with a `v*` tag exists and contains a matching platform asset, downloads the prebuilt binary with `curl -fsSL`; if the release ships `SHA256SUMS`, sha256 verification is enforced (abort on mismatch); otherwise it warns and skips verification;
- otherwise falls back to a source build `go build -trimpath ./cmd/marshal` (the Go version must satisfy the `go` directive in `go.mod`); without a local checkout it shallow-clones `https://github.com/chiga0/marshal-harness.git` first (cloning the tag when a release tag is known);
- installs to `~/.local/bin` by default, never requests sudo, and prints next steps (`marshal init` / `marshal doctor`) when done.

During installation, the binary is built or downloaded only at the stable `${MARSHAL_INSTALL_DIR}/.marshal-staging/marshal` path, verified, copied to the final `marshal` path, and then removed from staging; no anonymous executable is generated or executed under a random `/tmp` path.

Environment variables: `MARSHAL_INSTALL_DIR` (install directory), `MARSHAL_REPO` (default `chiga0/marshal-harness`), `MARSHAL_TAG` (pin a release tag, skipping the latest-release lookup), `MARSHAL_FORCE_SOURCE=1` (skip the release and build from source directly).

### Release asset naming conventions

The release asset conventions that `scripts/install.sh` depends on (future release tooling must follow them):

- `marshal_<version>_<os>_<arch>`: prebuilt binary. `version` is the release tag without the `v` prefix; `os`/`arch` use Go-style `darwin|linux` × `amd64|arm64` (e.g. `marshal_0.1.0_darwin_arm64`);
- `SHA256SUMS`: checksum manifest for all assets in `sha256sum` format (`<hash>  <filename>`).

### Manual verification

The install script is not covered by `make check` (shellcheck is not a frozen dependency of this repository). After modifying `scripts/install.sh`, verify manually:

1. Run `bash scripts/install.sh` in a clean checkout (local source build path);
2. `MARSHAL_INSTALL_DIR=<empty dir> bash scripts/install.sh` to verify a custom install directory;
3. `MARSHAL_FORCE_SOURCE=1 bash scripts/install.sh` to verify the forced source-build path;
4. After installation, run `marshal version` and `marshal doctor --json` to confirm the binary works.

## Current CLI

```bash
marshal version [--json]
marshal doctor [--run RUN_ID] [--repair] [--print-env] [--json]
marshal contract validate [--schema NAME] <PATH|->
marshal contract schema [--all [--out DIR]] [--schema NAME] [--json]
marshal init [--json]
marshal task status --run RUN_ID [--json]
marshal task plan --task PATH --policy PATH --run RUN_ID [--json]
marshal task approve --run RUN_ID --gate plan|publish [--actor ID] [--json]
marshal task run --run RUN_ID [--through-verify] [--json]
marshal task verify --run RUN_ID [--json]
marshal task review --run RUN_ID [--decision PATH] [--json]
marshal task publish --run RUN_ID [--json]
marshal task accept --run RUN_ID [--json]
marshal task abort --run RUN_ID --actor ID --reason TEXT [--json]
marshal task cleanup --run RUN_ID [--apply] [--json]
marshal task <COMMAND>
```

`version`, `doctor`, `contract validate`, `task status` and `task cleanup` without `--apply` are read-only; `contract schema` is also read-only except in the `--all --out DIR` form, which only writes exported files to a user-specified directory and never touches repository state. `marshal init` creates the repository-bound state directory and writes the Git exclude. `task run` uses the frozen, selected Worker Adapter; `verify`, `review`, `publish`, `accept` each run an independent evidence gate. Publishing requires an absolute `MARSHAL_GH_PATH` and a separate `MARSHAL_GH_CONFIG_DIR`. Cleanup only lists exact managed worktrees by default; only an explicit `--apply` executes it, and it refuses targets with an Active Lease, a non-terminal Run, a missing Outcome, an active TerminalSession, or that are dirty/symlinks/identity-unknown. It never deletes Run evidence, local branches, remote branches, or PRs.

Examples:

```bash
make build COMMIT="$(git rev-parse HEAD)"
./bin/marshal doctor --json
./bin/marshal contract validate --schema task-spec schemas/examples/happy-path/task-spec.json
./bin/marshal contract schema
./bin/marshal contract schema --all --out /tmp/marshal-schemas
```

Development and operator examples deliberately use the stable `bin/marshal` path. Do not use `go run ./cmd/marshal`: Go may create an anonymous executable in its cache or a temporary directory, preventing stable Marshal identity reuse.

## Contract self-description

`marshal contract schema` exports contract self-descriptions from the Schema directory embedded in the binary (`schemas/` Embedded FS), so agents and external tools can consume them with zero prior knowledge; exported bytes match the embedded content byte-for-byte:

- no arguments: prints every Schema name and version line by line (e.g. `task-spec v1alpha1`); `--json` outputs the same content as a JSON array;
- `--schema NAME`: prints a single named Schema verbatim to stdout; `NAME` shares the same kebab-case names as `contract validate --schema`;
- `--all`: outputs the full catalog (`name`, `kind`, `version`, `$id`, SHA256 of file bytes and examples list for every Schema) as JSON to stdout;
- `--all --out DIR`: writes `<name>.schema.json` (0644), `examples/happy-path/<name>.json`, `examples/invalid/<name>.json` and the `catalog.json` manifest into `DIR`, all byte-identical to the embedded content; repeated exports overwrite idempotently.

## Environment variable reference

| Variable | Purpose | Nature |
| --- | --- | --- |
| `MARSHAL_STATE_DIR` | Overrides the default `.marshal/` state directory (absolute path; inside a repository it must equal the default directory) | run configuration |
| `MARSHAL_OPENCODE_PATH` | Absolute path of the OpenCode Worker executable | adapter registration |
| `MARSHAL_QWEN_PATH` | Absolute path of the Qwen Code Worker executable | adapter registration |
| `MARSHAL_PI_PATH` | Absolute path of the Pi Worker executable | adapter registration |
| `MARSHAL_GH_PATH` | Absolute path of the Publisher's `gh` executable | publication credential boundary |
| `MARSHAL_GH_CONFIG_DIR` | Publisher's separate credential directory (ambient config is never reused) | publication credential boundary |
| `MARSHAL_CMUX_PATH` | Path of the cmux executable (supervised Pilot) | optional backend |
| `MARSHAL_LIVE_CMUX` | Enables the cmux Live E2E | test switch |
| `MARSHAL_LIVE_GITHUB` / `MARSHAL_LIVE_GITHUB_FIXED_SUFFIX` | Enables GitHub Publisher live tests (the latter pins the branch/PR suffix for idempotent reruns) | test switch |
| `MARSHAL_LIVE_OPENCODE_PATH` / `MARSHAL_LIVE_QWEN_PATH` / `MARSHAL_LIVE_PI_PATH` | Enables Live E2E for the corresponding Adapter | test switch |
| `MARSHAL_DISCOVERY_KNOWN_LOCATIONS` | Overrides the known-location list for doctor's advisory discovery; `-` disables it, keeping only the PATH scan | test switch |

All `*_PATH` variables only accept absolute paths; registration never searches PATH or guesses approximate names (when unset, the workers section of `marshal doctor` shows `not-configured`). The discovery section of `marshal doctor` additionally provides advisory discovery, which only suggests and never auto-registers (see [Deploying to a new environment](#deploying-to-a-new-environment) below). Live tests are skipped by default; CI does not depend on any live switch.

## Deploying to a new environment

Deploying to a new environment turns "read the docs and guess" into "doctor suggests, the user registers with one line". The core principle is: **discovery advisory, registration explicit** — doctor only discovers and suggests; registration still requires explicit environment variables. This preserves the invariant that "the Registry must never silently swap binaries or versions".

doctor scans PATH directories and common install locations (`~/.local/bin`, `/opt/homebrew/bin`, `~/.opencode/bin`, fnm/node global bin, `npm root -g`) for known binary names `opencode`, `qwen`, `qwen-code`, `pi`. It never recursively scans disks nor guesses approximate names; for each candidate it only runs `<bin> --version` to read the version and computes realpath and SHA256, silently skipping any failed execution. Adapters already configured via environment variables are excluded from discovery.

### Interpreting doctor

```bash
marshal doctor --json
```

The report gains a `discovery` array with one entry per unregistered Adapter:

- `adapterId` / `environmentVariable`: the corresponding Adapter and registration variable;
- `candidates`: each candidate has `path` (discovered location), `realpath` (absolute path after resolving symlinks), `sha256`, `version`, `source` (`path` or `known-location`);
- `suggestedEnv`: the realpath suggested for the registration variable, ready for the user to paste.

`discovery` results only appear in doctor output; they never enter the CapabilitySnapshot and never change plan's fail-closed semantics: unregistered Adapters remain unavailable.

### Pasting the suggested line

```bash
marshal doctor --print-env
```

This flag prints the suggested `export` line directly, for example:

```bash
export MARSHAL_QWEN_PATH=/Users/me/.local/bin/qwen
```

Pasting it into a shell profile (or the current session) completes registration. doctor never writes environment variables, never touches shell profiles, and never auto-registers Adapters.

### Verifying registration

After pasting the export, re-run:

```bash
marshal doctor --json
```

Confirm that the corresponding Adapter's `workers` entry shows `outcome=registered`, `compatibility=supported`; the Adapter then disappears from the `discovery` section. If `compatibility=unsupported`, the binary version does not match the frozen supported versions; doctor reports it faithfully but does not let it pass.

This section cross-references Operator Runbook §9.1 "One-time environment setup": §9.1 shows how to freeze the five environment variables; this section shows how to use doctor to find the correct absolute paths.

## Contract policy

JSON Schema is the authoritative structural definition for persisted records. The Go side only defines the strongly typed enums the Core actually needs plus an opaque `Record` type, avoiding a second full mirror of the structures; tests enforce that:

- all 17 Schemas compile;
- every happy-path fixture passes;
- every invalid fixture fails;
- format assertions are enabled;
- `Kind` and lifecycle `State` constants align with the Schemas;
- Semantic Rules are enforced for Task Budget, ID uniqueness, paths, Verification Status, Review Findings, and `.marshal/` Source Artifacts.

Schemas use ECMA-262-style regular expressions. Go's standard regex engine does not support all of those escapes, so the Validator explicitly uses an ECMAScript Regex Engine; falling back to default Go regex while claiming contract compatibility is forbidden.
