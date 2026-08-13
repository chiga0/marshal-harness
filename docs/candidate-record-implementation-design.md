# ADR 0027 实现设计：Candidate 一等不可变记录与 Verification 归一化双记录链

- 文档性质：**实现设计文档（冻结蓝图）**，以 [ADR 0027](adr/0027-candidate-record-and-verification-write-scope.md) 被接受为生效前提。
- ADR 0027 状态：**已接受（Accepted，2026-08-13）**，随 PR #82 合入 main。本设计撰写时 ADR 0027 尚未合入，§0.1 的决策摘要来自 PR #82；ADR 文本已合入后，如与本设计存在冲突，一律以 ADR 文本为准。
- 权威代码基线：`main` HEAD `f571547a4241e8902299f54fb67193b18c703335`。
- 本设计只定义实现蓝图与任务拆分，**不新增任何超出 ADR 0027 / ADR 0018 / ADR 0019 的权威决策**；所有与既有不变量（append-only ledger、Core-only 写权威、fail-closed、内容寻址）冲突的解释以既有 ADR 为准。

---

## 0. 摘要与决策基线

### 0.1 ADR 0027 决策摘要（PR #82）

1. **Candidate 是一等不可变权威记录**：属于 authority ledger 事实，append-only，一经写入不得原地改写或删除；由 `authorityNamespaceId` 拥有、只允许 Core 写入（ADR 0018 §10）。
2. **字段集合**：Candidate 绑定 `taskId`/`runId`/`attemptId`/`baseSha`/`contentDigest`/`producerKind`（封闭枚举 `worker|normalizer`）/`producer`/`predecessorCandidateDigest`/`createdAt`；远程 dispatch 启用后追加 `allocationId`/`generation`（ADR 0019 §3）。
3. **内容寻址**：`contentDigest` 是候选内容（observed patch 字节）的 sha256 内容寻址 digest。
4. **接纳语义**：digest-verified put-if-absent；同 key 冲突字节只能进 quarantine，永不覆盖当前对象（ADR 0018 §13）。
5. **Verification 归一化写域收窄**：归一化不得就地改写被验证对象并覆盖其 digest；归一化必须产出**新 Candidate**（`producerKind=normalizer`，`predecessorCandidateDigest` 指向被归一化的 worker Candidate），形成双记录链。

### 0.2 本设计覆盖范围

| # | 主题 | 章节 |
| --- | --- | --- |
| 1 | Candidate Schema 与 Go 类型设计 | §2 |
| 2 | verification 归一化双记录链重构 | §3 |
| 3 | Evidence `subjectDigest` 迁移路径（ADR 0019 §6） | §4 |
| 4 | 兼容与迁移（legacy 回退） | §5 |
| 5 | 实现任务拆分建议 | §6 |
| 6 | 测试要点 | §7 |
| 7 | 风险（artifact digest 语义变化对 CI/merge 的影响） | §8 |

---

## 1. 现状基线（as-is，main f571547）

本章只陈述与本设计相关的现状事实，作为重构的对照基线。

### 1.1 归一化当前是"就地改写"

`internal/verification/verifier.go` 的 `Verify()` 当前流程：

1. `ObserveContext(worktree, baseSHA, captureLimit)` → 归一化前 observation（`SnapshotDigest`/`DiffDigest`/`Patch`）。
2. `atomicWrite(runDir/observed.patch, observation.Patch)` 写入 worker 原始 patch 字节；manifest 追加 artifact `evidence:observed-patch`（`producer=verifier`，digest = 原始 patch digest）。
3. gates：`diff:observe`、`scope:changed-paths`（`EvaluateScope`）。
4. `normalizeFormat`（`internal/verification/formatnormalize.go`）：在 allowPaths 内对漂移的 `.go` 文件先 `gofmt -l` 检测、再 `gofmt -w` **就地改写 worktree 文件**。
5. 若发生归一化：重新 `ObserveContext` → **覆写** `observed.patch` → `refreshObservedPatchArtifact` **原地修改**既有 `evidence:observed-patch` artifact 的 `ByteSize`/`Digest`。
6. acceptance 命令执行后再次 Observe，比对 `SnapshotDigest`/`DiffDigest` 检测 Verifier 命令引入的未声明变更。

问题（即 ADR 0027 的动机）：worker 原始字节在证据链中被归一化字节覆盖，`evidence:observed-patch` 的 digest 被原地改写；"worker 产出了什么原始字节"没有不可变权威记录，也没有 predecessor 链可追溯。

### 1.2 当前 Evidence 绑定点清单

| 位置 | 绑定内容 |
| --- | --- |
| `internal/review/packet.go` `evidenceIdentity` | `specDigest`、`patchDigest`（observed.patch 字节 digest）、`verificationDigest`、`artifactManifestDigest`、`workerResultDigests`、`previousBlockingFindings` → canonical digest 即 `evidenceDigest` |
| `internal/review/packet.go` `ReviewPacket` 构造 | `SnapshotDigest` + `DiffDigest` + `BaseSHA` + `SpecDigest`；`validateObservedPatch` 复核 observed.patch 字节与 manifest artifact digest 一致；`ValidateCurrentObservation` 复核 worktree 未漂移 |
| `internal/review/decision.go` stale-fix 判定 | `PreviousFinding` 携带前轮 packet 的 `SnapshotDigest`+`VerificationDigest`；finding 关闭要求证据已变化 |
| `internal/publication/service.go` | `PublicationIntent`/`PublicationRecord` 绑定 `SnapshotDigest`+`DiffDigest`；commit trailer `Marshal-Snapshot-Digest`；commit 前后 re-observe 等值校验 |
| `internal/publisher/github/github.go` | PR body 展示 verification/evidence/snapshot digest；`RemoteCheckRecord` 绑定 `headSha` |
| `internal/execution/service.go` | `worker.completed` RunEvent 记录 `snapshotDigest`+`diffDigest`；attempt 目录写 `worktree-snapshot.json` |
| `internal/cleanup/expired.go` | orphan worktree 判定使用 `observation.DiffDigest` 与 archived digest 比对 |

### 1.3 既有权威记录模式（可直接复用）

- `internal/authority/namespace.go`：`AuthorityNamespaceId` 三元组（`tenantNamespace`/`controlPlaneId`/`authorityScopeId`），`Validate`/`Canonical`/`Digest`/`Equal`；`requireText`/`requireDigest` fail-closed helpers；canonicalJSON（RFC 8785 JCS 风格）。
- `internal/authority/effect.go`：`SideEffectIntent`/`SideEffectReceipt`/`ReconcileRecord` 的 authority 记录模式——fail-closed `Validate()` + `Canonical()` + detached `Digest()`。
- `internal/authority/edges.go`：**detached digest 模式**——`edgeDigest` 字段在序列化前置空再计算 canonical digest；`ReplayKey()` 使重复提交幂等归并；`ValidAt` 区分结构有效与使用时有效。
- `internal/canonical`：RFC 8785 JCS 唯一接纳门（递归拒绝重复 object member），`DigestBytes`/`DigestJSON`。
- Schema/契约模式：Draft 2020-12、`additionalProperties: false`、`$defs` 复用（`id`/`digest`/`gitSha`/`relativePath`）；`internal/contract/catalog.go` 以 `{Name, Kind}` Descriptor 注册 `<name>.schema.json` + `examples/happy-path/<name>.json` + `examples/invalid/<name>.json`。
- ADR 0018 §13：Artifact/Evidence/Checkpoint/Candidate 对象 key 为 `authorityNamespaceId` scoped immutable key，接纳走 **digest-verified put-if-absent**，陈旧/冲突 bytes 只进 quarantine namespace。
- ADR 0019 §3：Candidate 绑定 task/run/attempt/allocation/generation、base 与内容 digest；§6：`EvidenceDependencySet` 绑定 `subjectDigest`/`baseSha`/`environmentDigest`/`policyDigest`/`verifierCapabilityDigest`/`upstreamArtifactDigests`（可选 `validUntil`）。

---

## 2. Candidate Schema 与 Go 类型设计

### 2.1 `schemas/candidate-record.schema.json` 字段草案

约束风格与既有 schema 一致：Draft 2020-12、`additionalProperties: false`、`$defs` 复用。`predecessorCandidateDigest` 的"worker 必须缺省 / normalizer 必须存在"用 `allOf` + `if/then` 条件表达，保持 fail-closed。

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$id": "https://marshal.dev/schemas/v1alpha1/candidate-record.schema.json",
  "title": "Marshal Candidate",
  "type": "object",
  "additionalProperties": false,
  "required": [
    "apiVersion",
    "kind",
    "taskId",
    "runId",
    "attemptId",
    "authorityNamespaceId",
    "baseSha",
    "contentDigest",
    "producerKind",
    "producer",
    "createdAt",
    "candidateDigest"
  ],
  "properties": {
    "apiVersion": { "const": "marshal.dev/v1alpha1" },
    "kind": { "const": "Candidate" },
    "taskId": { "$ref": "#/$defs/id" },
    "runId": { "$ref": "#/$defs/id" },
    "attemptId": { "$ref": "#/$defs/id" },
    "authorityNamespaceId": { "$ref": "#/$defs/id" },
    "baseSha": { "$ref": "#/$defs/gitSha" },
    "contentDigest": { "$ref": "#/$defs/digest" },
    "producerKind": { "type": "string", "enum": ["worker", "normalizer"] },
    "producer": { "$ref": "#/$defs/id" },
    "predecessorCandidateDigest": { "$ref": "#/$defs/digest" },
    "createdAt": { "type": "string", "format": "date-time" },
    "allocationId": { "$ref": "#/$defs/id" },
    "generation": { "type": "integer", "minimum": 0 },
    "candidateDigest": { "$ref": "#/$defs/digest" }
  },
  "allOf": [
    {
      "if": {
        "required": ["producerKind"],
        "properties": { "producerKind": { "const": "worker" } }
      },
      "then": { "not": { "required": ["predecessorCandidateDigest"] } }
    },
    {
      "if": {
        "required": ["producerKind"],
        "properties": { "producerKind": { "const": "normalizer" } }
      },
      "then": { "required": ["predecessorCandidateDigest"] }
    }
  ],
  "$defs": {
    "id": {
      "type": "string",
      "minLength": 1,
      "maxLength": 160,
      "pattern": "^[A-Za-z0-9][A-Za-z0-9._:-]*$"
    },
    "digest": { "type": "string", "pattern": "^sha256:[0-9a-f]{64}$" },
    "gitSha": { "type": "string", "pattern": "^[0-9a-f]{40,64}$" }
  }
}
```

字段语义表：

| 字段 | 类型/约束 | 语义 |
| --- | --- | --- |
| `taskId`/`runId`/`attemptId` | id，必填 | Candidate 所属的 Task/Run/Attempt 身份（ADR 0019 §3） |
| `authorityNamespaceId` | id，必填 | 权威归属键空间的 canonical 字符串形式（ADR 0018 §10；与 `schemas/scm-merge-receipt.schema.json` 的既有先例一致：wire 层以 string 承载） |
| `baseSha` | gitSha，必填 | Observation 的锁定 base SHA；链上所有 Candidate 必须一致 |
| `contentDigest` | digest，必填 | **内容寻址**：候选内容（observed patch 字节）的 `sha256:` digest。相同内容 ⇒ 相同 contentDigest，跨 attempt 幂等归并 |
| `producerKind` | enum `worker\|normalizer` | 封闭枚举。`worker` = Worker 原始 observed patch；`normalizer` = 归一化产物 |
| `producer` | id，必填 | 产出主体的确定性身份。约定词表：worker Candidate 用 `worker`（adapter 细节已由 WorkerResult 承载，不重复进入 Candidate）；normalizer Candidate 用 `verifier:format-normalize` |
| `predecessorCandidateDigest` | digest，条件必填 | 指向被本 Candidate 取代/派生的前序 Candidate 的 **record digest**（见 §2.4）。`worker` 必须缺省（链根）；`normalizer` 必须存在 |
| `createdAt` | date-time | Core 接纳时间（本地 MVP 取 Verifier 时钟，测试可注入） |
| `allocationId`/`generation` | id / integer，**可选** | 远程 dispatch 启用后才必填并参与 fencing（ADR 0019 §3 / ADR 0018 §13）。本地 MVP 阶段保留字段、不承载语义；远程启用时由后续任务收紧（schema 条件收紧或 apiVersion 演进，见 §2.3） |
| `candidateDigest` | digest，必填 | **记录身份 digest**：detached canonical digest（序列化时将本字段置空后计算，edges.go `edgeDigest` 同款模式）。链引用与 Evidence 引用均使用它 |

### 2.2 Go 类型设计

新增 `internal/domain/candidate.go`（与既有 domain 类型同域；Candidate 同时是 schema-validated 耐久契约与 authority 记录）：

```go
package domain

import (
	"fmt"
	"time"
)

// ProducerKind is the closed enumeration of Candidate producers (ADR 0027).
type ProducerKind string

const (
	ProducerKindWorker     ProducerKind = "worker"
	ProducerKindNormalizer ProducerKind = "normalizer"
)

// ParseProducerKind rejects every value outside the closed enumeration.
func ParseProducerKind(value string) (ProducerKind, error) {
	kind := ProducerKind(value)
	if kind == ProducerKindWorker || kind == ProducerKindNormalizer {
		return kind, nil
	}
	return "", fmt.Errorf("unknown producer kind %q", value)
}

// Candidate is the first-class immutable candidate record (ADR 0027): an
// append-only authority ledger fact owned by authorityNamespaceId. It is
// never rewritten in place; supersession produces a new record linked by
// predecessorCandidateDigest.
type Candidate struct {
	APIVersion                 APIVersion   `json:"apiVersion"`
	Kind                       Kind         `json:"kind"`
	TaskID                     string       `json:"taskId"`
	RunID                      string       `json:"runId"`
	AttemptID                  string       `json:"attemptId"`
	AuthorityNamespaceID       string       `json:"authorityNamespaceId"`
	BaseSHA                    string       `json:"baseSha"`
	ContentDigest              string       `json:"contentDigest"`
	ProducerKind               ProducerKind `json:"producerKind"`
	Producer                   string       `json:"producer"`
	PredecessorCandidateDigest string       `json:"predecessorCandidateDigest,omitempty"`
	CreatedAt                  time.Time    `json:"createdAt"`
	AllocationID               string       `json:"allocationId,omitempty"`
	Generation                 uint64       `json:"generation,omitempty"`
	CandidateDigest            string       `json:"candidateDigest"`
}
```

配套要求：

- `internal/domain/record.go`：追加 `KindCandidate Kind = "Candidate"` 常量并注册进 `kinds` 切片，使 `ParseKind`/`Kinds()` 接纳。
- `Validate()`（fail-closed，风格对齐 `internal/authority`）：三个 id 非空、`authorityNamespaceId` 非空、`baseSha` 形如 git SHA、`contentDigest`/`candidateDigest` 为合法 sha256 digest、`producerKind` 封闭枚举、`producer` 非空、链条件（worker ⇒ predecessor 为空；normalizer ⇒ predecessor 非空且为合法 digest）、`createdAt` 非零值。
- `Digest()`（detached）：复制记录、置空 `CandidateDigest`、`json.Marshal` → `canonical.JSON`（RFC 8785 接纳门）→ `canonical.DigestBytes`；`Validate` 时复算并比对存储值，不符即 fail closed（edges.go `requireMatchingEdgeDigest` 同款）。
- `Equal`：逐字段相等（含 digest），用于幂等归并判定。

契约注册：

- `internal/contract/catalog.go`：追加 `Descriptor{Name: "candidate-record", Kind: domain.KindCandidate}`（自动派生 `schemas/candidate-record.schema.json`、`examples/happy-path/candidate-record.json`、`examples/invalid/candidate-record.json`）。
- 新增 happy-path 与 invalid fixture 各一份；invalid fixture 至少覆盖：未知 `producerKind`、worker 携带 predecessor、normalizer 缺失 predecessor、`contentDigest` 非法前缀。
- fixture 中一切 digest/secret 类字面量遵守 gitleaks-safe 约束：由两个字符串相加或 helper 构造（见 §7.7）。

### 2.3 与 ADR 0018 §10 `authorityNamespaceId` 的归属

- Candidate 是 **authority ledger 事实**：由 `authorityNamespaceId` 拥有、只允许 Core 写入；Worker/Verifier/Publisher 均不得写入或宣称拥有 Candidate 记录（Verifier 在本地 MVP 中是代 Core 执行接纳的唯一写入路径，写入即代表 Core 接纳决定）。
- **对象 key**（ADR 0018 §13）：`(authorityNamespaceId, taskId, runId, attemptId)` scoped immutable key + `contentDigest` 内容寻址；接纳走 digest-verified put-if-absent（§3.3 算法）。
- **本地 MVP**：单租户部署固定 authority 键空间（`tenantNamespace="default"` 口径与 ADR 0018 §10 一致）；wire 字段使用 canonical 字符串形式。具体常量值由 T1 任务冻结并写入测试，禁止自由文本。
- **远程启用后**（M9+）：`allocationId`/`generation` 转为必填并参与 lease fencing / expectedSequence CAS；收紧方式二选一（由远程 milestone 决定，不在本设计实现范围）：(a) schema 增加 `if/then` 条件收紧；(b) apiVersion 演进。字段已在 v1alpha1 schema 中预留，保证追加而非破坏。

### 2.4 双层 digest 语义（内容身份 vs 记录身份）

| digest | 计算对象 | 用途 |
| --- | --- | --- |
| `contentDigest` | patch 原始字节（`canonical.DigestBytes(bytes)`） | 内容寻址、幂等归并、Evidence `subjectDigest`（§4） |
| `candidateDigest` | detached canonical 记录序列化 | 记录身份、predecessor 链引用、artifact/report/packet 绑定 |

两条不变量：

1. `contentDigest` 相同的两个 Candidate 是同一"内容"；即使记录身份不同（不同 attempt/时间），Evidence 对内容的适用性可以跨记录复用（§4 依据）。
2. `predecessorCandidateDigest` 引用的是前序记录的 `candidateDigest`（记录身份），因而链上每一跳同时传递内容绑定与前序记录的全部元数据；任何对前序记录字段的篡改都会使复算 digest 不符并 fail closed。

---

## 3. Verification 归一化链重构（worker + normalizer 双记录链）

### 3.1 目标流程（to-be）

`Verifier.Verify()` 重构后的步骤（对照 §1.1 编号）：

1. `ObserveContext` → 归一化前 observation（不变）。
2. 写 **`worker.patch`**（新增，worker 原始字节，写入后不可变）；`observed.patch` 此刻同样写入原始字节。
3. **接纳 worker Candidate**（§3.3 算法）：`producerKind=worker`、`producer=worker`、`contentDigest=canonical.DigestBytes(原始 patch)`、无 predecessor（链根）。
4. gates `diff:observe`/`scope:changed-paths` 不变（仍基于归一化前 observation）。
5. `normalizeFormat` **不变**：仍是纯 gofmt 格式化步骤（确定性、不改语义、gofmt 不可用即 fail closed）。它继续改写 worktree 工作副本——ADR 0027 收窄的是**权威记录的写域**（不再覆盖 digest/不再丢失原始字节），worktree 作为工作副本仍被归一化。
6. 若 `len(normalized) > 0`：重新 `ObserveContext` → 覆写 `observed.patch` 为归一化字节（与现状相同的文件布局）→ **接纳 normalizer Candidate**：`producerKind=normalizer`、`producer=verifier:format-normalize`、`contentDigest=canonical.DigestBytes(归一化 patch)`、`predecessorCandidateDigest=worker Candidate 的 candidateDigest`。
7. 若 `len(normalized) == 0`：**不产生 normalizer Candidate**；head Candidate 即 worker Candidate。设计依据：归一化幂等（`normalize(normalize(x)) == normalize(x)`），无漂移即无新事实，链长度保持最小；重复 Verify 不会膨胀链。
8. **head Candidate 定义**：链上最后一个 Candidate（有 normalizer 则是 normalizer，否则是 worker）。report/artifact/packet 绑定全部指向 head，同时保留 predecessor 链可追溯。
9. artifact 绑定调整（§3.5）；`format:normalize` gate evidence 追加 Candidate 引用（§3.6）。
10. acceptance 命令后的漂移检测不变（仍比对 head observation 的 `SnapshotDigest`/`DiffDigest`）。

**删除项**：`refreshObservedPatchArtifact`（`formatnormalize.go`）整体移除——它"原地改写既有 artifact digest"的语义正是 ADR 0027 禁止的行为；其职责被"head 绑定 + worker 原始 artifact 常备"取代。

### 3.2 持久化布局（append-only）

Run 目录新增布局（均为 run 相对路径，0600，atomicWrite）：

```
<runDir>/
├── worker.patch                    # 新增：worker 原始字节，写入后不可变
├── observed.patch                  # 既有：head Candidate 内容（归一化后为归一化字节）
├── candidates/                     # 新增：Candidate 记录，append-only
│   ├── <candidateDigest>.json      # 文件名 = record digest，天然去重
│   └── quarantine/                 # 冲突字节隔离区（ADR 0018 §13）
│       └── <timestamp>-<contentDigest>.json
├── verification-report.json
└── artifact-manifest.json
```

规则：

- `candidates/*.json` 只追加、不改写、不删除；文件名即 `candidateDigest`，重复接纳是幂等 no-op。
- 冲突字节（同身份槽位、不同内容）写入 `candidates/quarantine/`，随后 Verify fail closed；quarantine 文件永不回流为当前证据。
- `worker.patch` 与 `observed.patch` 均受既有 `captureLimit`（默认 64 MiB 上限）约束；截断标志沿用 `Observation.DiffTruncated` 语义。

### 3.3 接纳算法（digest-verified put-if-absent）

```text
AdmitCandidate(record, payloadBytes):
  1. recomputed := canonical.DigestBytes(payloadBytes)
     recomputed != record.contentDigest            → FAIL（digest 验证）
  2. record.Validate()                              → FAIL（fail-closed 结构校验）
  3. detached := record with candidateDigest 置空
     canonical.DigestJSON(detached) != record.candidateDigest → FAIL
  4. producerKind == normalizer 时:
     predecessor 必须已存在于 store，且
     predecessor.baseSha == record.baseSha、
     predecessor.taskId/runId/attemptId 与 record 逐项一致   → 否则 FAIL
  5. 查找同身份槽位既有记录:
     identity := (taskId, runId, attemptId, producerKind,
                  predecessorCandidateDigest, contentDigest)
     - 命中且 candidateDigest 一致 → 幂等归并，返回既有记录（保留首次 createdAt）
     - 同槽位但字节冲突           → 冲突字节写入 quarantine/，FAIL
     - 未命中                     → atomicWrite candidates/<candidateDigest>.json
```

实现为独立 seam（§3.4 `CandidateStore`），本地 MVP 用文件系统实现；远程 authority ledger 是未来 backend，语义不变。

### 3.4 Go 函数接缝

新增 `internal/verification/candidate.go`：

```go
package verification

// CandidateStore is the admission seam for immutable Candidate records
// (ADR 0027). The local MVP implementation is append-only filesystem
// storage under <runDir>/candidates; a remote authority ledger is a
// future backend with identical put-if-absent semantics.
type CandidateStore interface {
	// Admit runs digest-verified put-if-absent. Identical records coalesce
	// idempotently; conflicting bytes are quarantined and an error is
	// returned (fail closed).
	Admit(candidate domain.Candidate, payload []byte) (domain.Candidate, error)
	// ByDigest resolves an admitted record by its candidateDigest.
	ByDigest(candidateDigest string) (domain.Candidate, error)
	// Head returns the latest admitted candidate of the attempt chain.
	Head() (domain.Candidate, bool, error)
}
```

`Verifier.Verify` 编排改动要点：

- `verification.Input` 追加 `AttemptID string` 字段（由 `internal/cli` 编排层从当前 run state 注入；`ValidateID` 校验）。
- worker Candidate 的 `createdAt` 与 normalizer Candidate 的 `createdAt` 均取 `v.now()`（沿用既有可注入时钟）。
- 归一化分支（§3.1 步骤 6）在重写 `observed.patch` 之后、`CollectArtifacts` 之前接纳 normalizer Candidate；任何接纳失败使 `format:normalize` gate 变为 required fail 并终止（与现状 gofmt 失败处理一致，fail closed）。

### 3.5 artifact manifest 绑定切换

| artifact | 现状 | to-be |
| --- | --- | --- |
| `evidence:observed-patch` | 归一化后 digest 被原地改写 | 始终绑定 **head Candidate 内容**（`observed.patch` 字节）；字节语义与现状完全一致（归一化后 = 归一化字节），但 digest 一次写入、不再改写；新增可选字段 `candidateDigest` 指向 head Candidate |
| `evidence:worker-patch`（新增） | — | 绑定 `worker.patch`（worker 原始字节），digest = worker Candidate `contentDigest`，`relatedGates: [diff:observe, scope:changed-paths, format:normalize]`，`candidateDigest` 指向 worker Candidate |

schema 影响：`schemas/artifact-manifest.schema.json` 的 `artifact` 定义追加**可选** `candidateDigest`（`$ref digest`）。可选追加保证旧 manifest 文档仍然 schema-valid（§5.3）。

### 3.6 gate 与 report 变化

- `format:normalize` gate：保留现有 `normalized:<path>` evidence 条目（透明性不回退）；追加 `candidate:<candidateDigest>` 条目（normalizer Candidate；no-op 时为 worker Candidate）。gofmt 失败 / 接纳失败时该 gate 为 required fail（现状语义保持）。
- `verification-report.schema.json` 追加**可选**顶层字段：`workerCandidateDigest`、`candidateDigest`（head）；`observed` 块不动。`Report` Go 类型对应追加 `omitempty` 字段。
- `diff:observe` gate evidence 追加 `artifact://evidence:worker-patch` 引用。

---

## 4. Evidence 绑定迁移（ADR 0019 §6）

### 4.1 目标形态：`EvidenceDependencySet.subjectDigest` → Candidate `contentDigest`

ADR 0019 §6 要求 Evidence 不可变、当前适用性由依赖图派生。目标字段绑定：

| EvidenceDependencySet 字段 | 绑定来源 |
| --- | --- |
| `subjectDigest` | **head Candidate 的 `contentDigest`**（内容寻址；内容不变则证据仍可适用，跨 attempt 可复用） |
| `baseSha` | Candidate `baseSha` |
| `environmentDigest` | verification 输入环境摘要（本地 MVP 首版可由 scope 摘要派生；冻结口径由 M13 任务定义） |
| `policyDigest` | TaskSpec/Policy 冻结摘要（本地为 `specDigest` 起步） |
| `verifierCapabilityDigest` | Verifier 能力摘要（本地 MVP 暂以固定常量标识内建 verifier；远程启用时绑定 ProviderCapabilitySnapshot digest） |
| `upstreamArtifactDigests` | `[worker.patch digest, observed.patch digest, verification-report digest, artifact-manifest digest]` |
| `validUntil` | 可选，本地 MVP 不使用 |

supersession 派生（§6 要求）：出现 contentDigest 不同的新 Candidate 时，Core 追加 supersession event，旧 Evidence 对**新** Candidate 不适用；仅失效依赖该 subject 的 gate 与后继节点，不做全局失效。

### 4.2 本地 MVP 迁移路径（分阶段，不一步引入完整 Evidence 对象）

本地 MVP 尚无独立 Evidence 权威对象；evidence 身份由 `evidenceIdentity`/`evidenceDigest`（review packet）承载。迁移分两阶段：

**阶段 A（本设计的 T3 任务）**——把 Candidate 绑定进既有 evidence identity：

- `evidenceIdentity` 追加两个成员：`candidateDigest`（head）、`workerCandidateDigest`；canonical 序列化后 `evidenceDigest` 自然携带 Candidate 绑定。
- `ReviewPacket` 追加可选字段 `candidateDigest`/`workerCandidateDigest`（schema 可选追加）。
- `PreviousFinding` 追加可选 `candidateDigest`；`decision.go` stale-fix 判定升级为：**双方 candidateDigest 均存在时以其比对为准；任一侧缺省时回退 SnapshotDigest+VerificationDigest 比对**（legacy 语义，见 §5.2）。
- `ValidateCurrentObservation` 不变（worktree 漂移检测与 Candidate 无关）。

**阶段 B（M13，超出本设计实现范围）**——引入 `EvidenceDependencySet` 权威对象与 supersession/ineligibility event，`subjectDigest := contentDigest`，按 §4.1 表完整绑定。

阶段 A 是阶段 B 的前置：Candidate 链存在后，`subjectDigest` 的来源才从"worktree observation digest 对"迁移到"内容寻址的 Candidate"。

---

## 5. 兼容与迁移（legacy 回退）

### 5.1 无 Candidate 记录的既有 Run：回退语义

- **判定**：读路径以"candidate 可用性"为开关——`<runDir>/candidates/` 存在且至少含一条 schema-valid、链完整（worker 根存在；若有 normalizer 则 predecessor 可解析）的记录 ⇒ candidate 模式；否则 ⇒ legacy 模式。
- **legacy 模式行为逐字保持现状**：`SnapshotDigest`+`DiffDigest`+`patchDigest` 路径不变；`evidenceIdentity` 的 candidate 成员以空值参与 canonical 序列化（或按字段缺省语义省略），与旧文档逐字节兼容的判定以"重新计算旧 fixture 的 evidenceDigest 结果不变"为准（测试断言，见 §7.5）。
- **旧 review packet 可验证性不变**：`validateObservedPatch`、`evidenceDigest` 复算、`VerificationDigest`/`ArtifactManifestDigest` 绑定均不依赖 Candidate；归档 packet 原样复验必须通过。
- **旧 PublicationIntent/Record**：字段集合不变；T3 只做可选追加，不改写任何既有字段语义。

### 5.2 跨采纳边界的 rework 轮次

同一 Run 的 Review Round N（legacy）与 Round N+1（candidate 模式）混合时：

- `PreviousFinding.candidateDigest` 为空 ⇒ stale-fix 判定自动回退 SnapshotDigest+VerificationDigest 比对（§4.2 阶段 A 规则），不产生误判。
- packet 间 digest 等值比较一律逐字段进行；不使用 evidenceDigest 做跨轮等值（成员集合变化会使新旧轮 evidenceDigest 必然不同，见 §8 风险 R6）。

### 5.3 推广与回滚

- Candidate 产出是 ADR 0027 接受后的**强制行为**，不设运行时开关；legacy 回退只是读路径兼容，不是 opt-out。
- 回滚 = 实现提交整体 revert；由于所有 schema 变更均为可选追加、所有文件布局均为新增，revert 后不遗留不可读数据。
- 历史 `.marshal` 归档**不做回填**：append-only 事实不追溯改写；读路径双模兼容即完成迁移。

---

## 6. 实现任务拆分建议

顺序硬依赖：T1 → T2 → T3；每个任务独立可验证（合入前整仓检查通过），T2/T3 必须携带 legacy 零回归 fixture。T4 仅列顺序，不在本次实现范围。

### T1：Schema + domain（无前置）

- **写域**：`schemas/candidate-record.schema.json`、`schemas/examples/happy-path/candidate-record.json`、`schemas/examples/invalid/candidate-record.json`、`internal/domain/record.go`（追加 Kind）、`internal/domain/candidate.go`（新增）、`internal/contract/catalog.go`（Descriptor 注册）+ 对应测试文件。
- **预估写域**：约 5 个文件、400–600 行（含 fixture 与测试）。
- **交付**：`KindCandidate` 注册、`ProducerKind` 封闭枚举、`Candidate` 类型 + fail-closed `Validate()` + detached `Digest()`；contract happy/invalid 对通过。
- **前置**：无。**门禁**：schema 与 Go 类型字段逐字对齐（§2.1 表）；detached digest 复算负例。

### T2：verification 双记录链重构（前置 T1）

- **写域**：`internal/verification/candidate.go`（新增：CandidateStore + 本地实现 + 接纳算法）、`internal/verification/verifier.go`（编排改动、Input.AttemptID）、`internal/verification/formatnormalize.go`（移除 `refreshObservedPatchArtifact`、gate evidence 扩展）、`internal/verification/types.go`（Report 可选字段）、`schemas/verification-report.schema.json`、`schemas/artifact-manifest.schema.json`（可选追加）、对应测试。
- **预估写域**：约 6–8 个文件、800–1200 行（含测试与 fixture）。
- **交付**：§3 全部目标流程；`worker.patch`/`candidates/` 布局；put-if-absent + quarantine；`evidence:worker-patch` artifact；legacy 无 candidate fixture 零回归。
- **前置**：T1 合入。**门禁**：§7 第 1–5 类断言全部落地。

### T3：review/publish 绑定切换（前置 T2）

- **写域**：`internal/review/packet.go`（evidenceIdentity/packet 字段、validateObservedPatch 不动）、`internal/review/decision.go`（stale-fix 双路径）、`internal/domain/review.go`（PreviousFinding/ReviewPacket 可选字段）、`schemas/review-packet.schema.json`、`internal/publication/service.go`（candidate digest 可选追加绑定与 trailer 追加）、`internal/publisher/github/github.go`（PR body 追加展示）+ 对应测试。
- **预估写域**：约 6–8 个文件、500–800 行。
- **交付**：candidate 模式下 packet/evidence/publish 绑定完整；legacy 模式逐字节回退；跨边界 rework 轮次判定正确。
- **前置**：T2 合入。**门禁**：旧归档 packet 复验通过 + 新链路 digest 断言。

### T4（顺序预留，远程/M13，超出本设计实现范围）

`EvidenceDependencySet` 权威对象 + supersession/ineligibility event + `allocationId`/`generation` 启用与 fencing。前置：ADR 0018/0019 远程 milestone 与 T3。

---

## 7. 测试要点

### 7.1 worker 原始字节可追溯断言

- 触发归一化的 fixture：`worker.patch` 字节 == 归一化前 observation.Patch；`digest(worker.patch)` == worker Candidate `contentDigest`；`observed.patch` == 归一化字节且 != `worker.patch`；链长度恰为 2；normalizer `predecessorCandidateDigest` == worker `candidateDigest`。
- `evidence:worker-patch` artifact digest 与 worker Candidate contentDigest 等值；`evidence:observed-patch` digest 与 head contentDigest 等值。

### 7.2 归一化幂等

- 对已归一化 worktree 再次走 Verify 归一化路径：`normalized` 为空集、不产生第三个 Candidate、head 不变、`candidates/` 目录文件数不变。
- `normalizeFormat` 纯函数性断言保持现状测试不回退。

### 7.3 predecessor 链完整性

- 正例：链上所有记录 `baseSha`/`taskId`/`runId`/`attemptId` 逐项一致；复算每条 `candidateDigest` 与存储值等值。
- 负例（全部 fail closed）：worker 携带 predecessor；normalizer 缺失 predecessor；`predecessorCandidateDigest` 指向不存在的 digest；predecessor 与后继 `baseSha` 不一致；篡改前序记录任一字段后复算 digest 不符。

### 7.4 put-if-absent 与 quarantine

- 相同记录重复 Admit ⇒ 幂等归并，`candidateDigest`/`createdAt` 稳定，文件系统只有一份。
- 同身份槽位、不同 payload 字节 ⇒ quarantine 文件落盘 + Verify 返回错误（fail closed），当前对象不被覆盖。
- `contentDigest` 与 payload 复算不符 ⇒ 拒绝。

### 7.5 legacy 回退零回归

- 无 `candidates/` 目录的 fixture run：packet 构造、evidenceDigest、publication intent 绑定与既有基线断言逐项一致（重新计算旧 fixture 的 digest 结果不变）。
- 归档旧 review packet 原样复验通过；`validateObservedPatch` 行为不变。
- 跨边界 rework：Round N legacy + Round N+1 candidate 的 stale-fix 判定不误关、不误放。

### 7.6 schema 与 canonical 负例

- happy/invalid fixture 对；`producerKind` 枚举违规；条件必填（§2.1 `allOf`）双向负例；JCS 重复 object member 拒绝（canonical.ErrRejected 路径）。

### 7.7 工程硬约束（全部任务适用）

- **gitleaks-safe fixture**：测试中 Key/Digest/Secret/Token 类字面量一律由两个字符串相加或 helper 构造，例如：

```go
func fixtureDigest(seed string) string {
	return "sha256:" + strings.Repeat(seed+"0", 64)[:64]
}

var workerSecret = "test-" + "secret" // 两个字符串相加，避免整词字面量
```

- 一切读写操作使用相对路径；绝对路径一律拒绝。
- 代码严格 gofmt（tab 缩进、结构体 tag 列对齐）。

---

## 8. 风险

| # | 风险 | 影响 | 缓解 |
| --- | --- | --- | --- |
| R1 | **artifact digest 语义跨边界变化**：`evidence:observed-patch` 的 digest 在采纳前后对同一逻辑内容可能不同（采纳前 = 覆写后的归一化字节 digest，采纳后 = head contentDigest，数值上通常一致，但**权威来源**从"可变 artifact 字段"变为"不可变 Candidate 绑定"）；以旧 digest 字段做跨 run 等值比较的外部 CI/脚本可能在边界处观察到口径切换 | 外部 CI/merge 工具链的 digest 比对 | digest 数值语义尽量保持（head 内容 == 现状 observed.patch 字节）；变更以文档公告；新绑定一律**追加**字段/trailer，不替换既有字段 |
| R2 | **既有 CI/merge 流程的实际绑定点核查**：`RemoteCheckRecord` 绑定 `headSha`（不绑 digest）、`SCMMergeReceipt`（ADR 0026）绑定 OID、`PublicationIntent.SnapshotDigest/DiffDigest` 绑定 worktree 状态（语义不变）——三者均不受影响；真正消费 patch digest 的只有 evidence identity 与 review packet | 误判影响面 | T3 任务必须先落 §1.2 清单的回归断言，再做绑定切换 |
| R3 | **存储近似翻倍**：`worker.patch` 追加保存原始字节 | run 目录体积 | 受既有 `captureLimit` 上界约束；`candidates/*.json` 为小记录；quarantine 只在冲突时产生 |
| R4 | **Reviewer 判断口径变化**：reviewer 首次可见 worker 原始字节（新 artifact），可能改变 rework 判断依据 | review 行为（非破坏性） | 仅追加可见性，不改变 packet 强制绑定；PR body/报告文案明确标注 normalized 与 worker 原样的关系 |
| R5 | **跨采纳边界轮次**：同一 run 的 rework 轮次横跨 legacy/candidate 模式 | stale-fix 误判 | §5.2 双路径判定 + §7.5 专项 fixture |
| R6 | **evidenceDigest 数值变化**：`evidenceIdentity` 追加成员后，新旧文档 evidenceDigest 必然不同 | 任何跨轮 evidenceDigest 等值比较 | 禁止以 evidenceDigest 做跨轮等值；等值判定一律逐字段（§5.2） |
| R7 | **quarantine fail-closed 提高错误率**：磁盘/权限异常时接纳失败使 Verify 失败 | verification 可用性 | 错误信息分类明确（对齐既有 benign/fatal 分级先例）；quarantine 永不回流 |

---

## 附录 A：受影响既有位置清单

| 文件 | 位置 | 现状行为 | 目标行为 |
| --- | --- | --- | --- |
| `internal/verification/verifier.go` | `Verify` | 归一化后重 Observe、覆写 observed.patch、改写 artifact digest | 双 Candidate 链接纳；head 绑定；digest 一次写入不改写 |
| `internal/verification/formatnormalize.go` | `refreshObservedPatchArtifact` | 原地改写既有 artifact 的 ByteSize/Digest | **移除**；由 head 绑定 + worker artifact 常备取代 |
| `internal/verification/formatnormalize.go` | `formatNormalizeGate` | evidence 仅 `normalized:<path>` | 追加 `candidate:<digest>` 条目 |
| `internal/verification/types.go` | `Report` | 无 candidate 字段 | 追加可选 `workerCandidateDigest`/`candidateDigest` |
| `internal/review/packet.go` | `evidenceIdentity`/`Build` | 绑定 specDigest/patchDigest/... | 追加 candidateDigest/workerCandidateDigest |
| `internal/review/decision.go` | stale-fix | SnapshotDigest+VerificationDigest 比对 | candidateDigest 优先、缺省回退 |
| `internal/publication/service.go` | intent/record/trailer | SnapshotDigest+DiffDigest | 可选追加 candidate digest 绑定与 trailer |
| `internal/publisher/github/github.go` | PR body | 展示 verification/evidence/snapshot digest | 追加 candidate digest 展示 |
| `internal/domain/record.go` | `kinds` | 18 个 kind | 追加 `KindCandidate` |
| `internal/contract/catalog.go` | descriptors | 18 个 descriptor | 追加 `candidate-record` |

## 附录 B：与既有 ADR 的符合性对照

| 要求 | 来源 | 本设计落点 |
| --- | --- | --- |
| Candidate 为一等不可变权威记录、append-only | ADR 0027 | §2 类型/schema、§3.2 持久化布局、§3.3 接纳算法 |
| 字段集合 taskId/runId/attemptId/baseSha/contentDigest/producerKind/producer/predecessorCandidateDigest/createdAt（远程追加 allocationId/generation） | ADR 0027 / ADR 0019 §3 | §2.1 字段表 |
| contentDigest 内容寻址 | ADR 0027 | §2.4、§3.3 步骤 1 |
| digest-verified put-if-absent、冲突字节进 quarantine | ADR 0027 / ADR 0018 §13 | §3.3、§7.4 |
| 归一化产出新 Candidate（normalizer + predecessor 链），不就地改写覆盖 digest | ADR 0027 | §3.1、删除 `refreshObservedPatchArtifact` |
| Candidate 归 authorityNamespaceId 拥有、Core-only 写 | ADR 0018 §10 | §2.3 |
| Evidence 不可变、subjectDigest 依赖图派生 | ADR 0019 §6 | §4 |
| Implement 类 typed execution 输出 Candidate、diff、声明 | ADR 0019 §2/§3 | worker Candidate 记录 Worker 原始 observed patch |
