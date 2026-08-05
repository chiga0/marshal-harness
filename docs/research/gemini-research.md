这份指南抛弃泛泛而谈的通用 Agent 框架论述，直接针对 **Marshal Harness** 的五核心约束进行落地设计：

1. **CLI 驱动**，本地状态/策略唯一权威；
2. **拒绝 Worker 自证**，凭据隔离；
3. **单 Worktree 最多单写入者**；
4. **证据绑定**（`ReviewDecision` 绑定精确 SHA256 摘要）；
5. **Fail-Closed 崩溃可恢复**。

---

## 一、 核心架构：非对称三段式编排 (Asymmetric Sandwich Architecture)

通用框架（如 AutoGen、CrewAI）在多 Agent 协同写代码时普遍陷入“Git 冲突地狱”与“凭据越权”。Marshal 的突破口在于：**绝对禁止“编码阶段（Coding Phase）”的多 Worker 并行写入**，将 Fan-out 限制在**只读探索**与**独立验证**两端。

```
                       ┌──────────────────────────────┐
                       │   CLI Trigger (Task Input)   │
                       └──────────────┬───────────────┘
                                      │
                         [0. Tiering Router (规则引擎)]
                                      │
                   ┌──────────────────┴──────────────────┐
                   ▼                                     ▼
        【Tier 0: 串行快速通道】                   【Tier 1/2: 门禁编排通道】
                   │                                     │
                   │                       ┌─────────────┴─────────────┐
                   │                       │ 1. Read-Only Explorer Fan-out│
                   │                       │    (只读 Worktree 1...N)  │
                   │                       └─────────────┬─────────────┘
                   │                                     │
                   │                       [2. TaskSpec Freeze (Lead 合并)]
                   │                                     │
                   ├─────────────────────────────────────┘
                   ▼
     ┌───────────────────────────┐
     │ 3. Single-Writer Worker   │ <--- 独占 Worktree，唯一代码写入权限
     └─────────────┬─────────────┘
                   │
                   ▼
     ┌───────────────────────────┐
     │ 4. Verification Jury      │ <--- Fan-out 并行验证 (只读应用 Patch)
     │   - Red Team Agent        │
     │   - Static/Sec Analyzer   │
     │   - Spec/Test Runner      │
     └─────────────┬─────────────┘
                   │
                   ▼
     [5. Evidence Aggregator & Decision Gate] <--- SHA256 证据绑定
                   │
                   ▼
     ┌───────────────────────────┐
     │ 6. Draft PR & Publisher   │ <--- 隔离凭据，发布 PR
     └───────────────────────────┘

```

---

## 二、 阶段详细实施规范

### 阶段 1：Pre-Coding 探索 Fan-out（只读并行）

* **场景**：处理跨模块重构或需求模糊的大任务，防止单 Worker 遗漏隐式依赖。
* **隔离机制**：CLI 为每个 Explorer Agent 分配一个 **Temporary Read-Only Git Worktree**（或只读文件系统映射），剥离一切写入工具（无 `write_file` / `git commit`）。
* **探索角色划分**：
* **Explorer A (Call-Graph Focus)**：专注上游/下游调用链依赖分析。
* **Explorer B (Data-Model Focus)**：专注 DB Schema、类型定义与状态流转。
* **Explorer C (Test/Edge Focus)**：专注现有测试覆盖率与历史 Bug 约束。


* **产物合并与 TaskSpec 冻结**：
* 每个 Explorer 返回结构化 `ResearchFinding`。
* 由 **Lead Agent** 执行纯结构化 JSON 合并（非自由文本总结），生成确定性的 `TaskSpec.json` 并写入本地持久化数据库，状态标记为 `TASK_SPEC_FROZEN`。一旦冻结，后续步骤严格不可更改。



---

### 阶段 2：Single-Writer 编码核心（串行独占）

* **执行原则**：继承现有 MVP 的成熟逻辑。
* **锁机制**：挂载全局文件锁 `worktree.lock`，指定唯一的写入 Agent（如 OpenCode / Qwen）。
* **凭据边界**：该 Worker 仅拥有本地文件写入与本地 Git Commit 权限，**无 GitHub Token，无 CI 触发权限**。
* **产物输出**：写入完成后，提取 `Git Patch`，生成 `Patch_SHA256`。

---

### 阶段 3：Post-Coding 证据门禁 Jury（并行验证）

这是多 Agent 带来最高收益且完全符合信任模型的阶段。**Worker 提交的代码严禁由自身进行测试与评审**。

#### 1. 验证陪审团（Jury）架构

CLI 创建 $N$ 个干净的临时 Worktree，并将 `Patch_SHA256` 对应的 Diff 应用到这些 Worktree 中。并行启动 $N$ 个独立的 Verifier：

```
                    ┌─────────────────────────┐
                    │      Git Patch          │
                    └────────────┬────────────┘
                                 │
         ┌───────────────────────┼───────────────────────┐
         ▼                       ▼                       ▼
┌──────────────────┐   ┌──────────────────┐   ┌──────────────────┐
│ Verifier 1:      │   │ Verifier 2:      │   │ Verifier 3:      │
│ Deterministic    │   │ Red-Team         │   │ Structural       │
│ Test Runner      │   │ Security Agent   │   │ Spec Auditor     │
└────────┬─────────┘   └────────┬─────────┘   └────────┬─────────┘
         │                       │                       │
         └───────────────────────┼───────────────────────┘
                                 ▼
                    ┌─────────────────────────┐
                    │ Evidence Aggregator     │
                    │ (硬编码确定性裁决)      │
                    └─────────────────────────┘

```

* **Verifier 1：确定性构建/测试器 (Deterministic Runner)**
* *非 LLM*。纯 CLI 脚本执行 `npm test` / `go test` / `cargo clippy`。
* 收集 `stdout`, `stderr`, `exit_code`, `coverage`。


* **Verifier 2：红队安全与边缘条件评估者 (Red-Team Agent)**
* *提示词设定*：“假设该 Patch 包含隐蔽的安全漏洞、内存泄漏或注入风险，寻找能击穿该代码的输入案例。”
* 要求产出具体的 POC 复现步骤或代码分析路径。


* **Verifier 3：TaskSpec 契约审计员 (Spec Auditor)**
* 对比 `TaskSpec.json` 中的 Acceptance Criteria，逐条核对代码 Diff。



---

## 三、 数据契约与 Schema 设计 (Data Schemas)

为了保障“证据绑定”和“崩溃恢复”，所有 Agent 间的交互产物必须使用严格的强类型 JSON 协议，禁止传输非结构化自由文本。

### 1. 证据摘要格式 (`EvidenceDigest`)

```json
{
  "version": "v1.0",
  "task_id": "task_20260805_001",
  "patch_hash": "sha256:a1b2c3d4...",
  "verifications": [
    {
      "verifier_id": "verifier_deterministic_test",
      "type": "STATIC_RUNNER",
      "result": "PASSED",
      "raw_logs_hash": "sha256:e5f6g7h8...",
      "metrics": { "passed_tests": 42, "failed_tests": 0 }
    },
    {
      "verifier_id": "verifier_red_team_sec",
      "type": "LLM_AUDITOR",
      "result": "FAILED",
      "findings": [
        {
          "severity": "CRITICAL",
          "file": "src/auth/session.ts",
          "line": 45,
          "assertion": "Potential timing attack on token comparison",
          "evidence_snippet": "if (token === user.token) {"
        }
      ]
    }
  ],
  "timestamp": "2026-08-05T09:00:00Z"
}

```

### 2. 最终评审决策 (`ReviewDecision`)

```json
{
  "task_id": "task_20260805_001",
  "decision": "REJECTED", 
  "evidence_digest_hash": "sha256:9x8y7z6w...",
  "rejection_reasons": [
    "Security Veto: Critical flaw detected by verifier_red_team_sec in src/auth/session.ts:45"
  ],
  "publisher_signature": "sig_rsa_pub_key_xyz..."
}

```

---

## 四、 投票矩阵与裁决机制 (Consensus Matrix)

为了避免“平均数的平庸”，合并机制采用 **“确定性规则优先 + 安全一票否决”** 矩阵，而非 LLM 的投票平均法。

| 验证项 | 结果 | 裁决权重/规则 | 对最终决策的影响 |
| --- | --- | --- | --- |
| **Deterministic Test** | **FAILED** | **最高级别 (Hard Veto)** | 直接归类为 `REJECTED`，无需调用其他 LLM 评审，终止流程（Fast Failure）。 |
| **Red-Team Agent** | **CRITICAL / HIGH** | **安全一票否决 (Security Veto)** | 归类为 `REJECTED`，反馈具体行号与 CWE 缺陷至 TaskSpec 修复管道。 |
| **Spec Auditor** | **UNFULFILLED** | **契约未满足** | 归类为 `REJECTED`，退回 Worker 追加开发。 |
| **Red-Team Agent** | **MEDIUM / LOW** | **警告 (Warning)** | 允许 `APPROVED`，但在 Draft PR 内容中自动附带 `Security Note` 标记。 |
| **所有 Verifier** | **ALL PASSED** | **通过 (Pass)** | 生成 `ReviewDecision(APPROVED)`，签名交由 Publisher 发送 Draft PR。 |

---

## 五、 协议开销控制：任务分级 Routing (Task Tiering)

为解决“小任务协议开销超过收益”的问题，CLI 在初始化阶段引入确定性的 Task Router：

```
Task Complexity Assessment:
  ├─ Modified File Count <= 2 && Lines Changed < 50 ---> Tier 0 (Micro)
  ├─ Modified Core Files (Auth/Crypto/DB) === false ---> Tier 1 (Standard)
  └─ Otherwise ---> Tier 2 (Complex)

```

| 维度 | Tier 0 (Micro Fix) | Tier 1 (Standard Feature) | Tier 2 (Complex/Core) |
| --- | --- | --- | --- |
| **应用场景** | Typo 修复、单文件单元测试补充 | 独立模块开发、常规 Bug 修复 | 核心逻辑改动、跨服务接口定义 |
| **Pre-Explore** | 禁用 (0 Explorer) | 禁用 (0 Explorer) | **启用 (2–3 Parallel Explorers)** |
| **Worker** | 单 Agent | 单 Agent | 单 Agent |
| **Verification** | 仅本地确定性测试 (Static) | 确定性测试 + 1 个 Spec Auditor | **全套 Jury (Static + Red-Team + Spec)** |
| **协议耗时比** | $< 5\%$ | $\approx 20\%$ | $\approx 40\%$ |
| **开销降低控制** | **极致节省 (Skip LLM Overhead)** | 平衡效率与质量 | 信任优先，允许高 Token 开销 |

---

## 六、 持久化状态机与崩溃恢复 (Crash-Resilient State Engine)

Marshal Harness 必须依靠持久化状态机保证 Fail-Closed 与幂等恢复。推荐采用 **SQLite WAL + Append-Only Event Log** 在本地管理：

### 1. 状态生命周期表

```
[TASK_CREATED]
      │
      ▼
[EXPLORING] ──(Crash)──> [RECOVER_EXPLORING] (重新拉取只读 Worktree)
      │
      ▼
[TASK_SPEC_FROZEN] <--- (硬门禁：不可变更)
      │
      ▼
[WORKER_EXECUTING] ──(Crash)──> [RECOVER_WORKER] (检测 Uncommitted Changes/Reset Worktree)
      │
      ▼
[PATCH_GENERATED] (计算 Patch SHA256)
      │
      ▼
[VERIFYING_JURY] ──(Crash)──> [RECOVER_JURY] (根据已存日志判定是否需重跑 Verifier)
      │
      ▼
[DECISION_BOUND] (ReviewDecision 写入 DB + SHA256 锁定)
      │
      ▼
[PUBLISHING_PR] (调用独立凭据 Publisher)

```

### 2. 崩溃恢复原则

* **幂等屏障（Idempotency Guard）**：所有重试必须校验 `Patch_SHA256`。如果代码发生任何微小改变，之前的 Verification 结果自动失效（Cache Invalidation），强行重跑 Jury。
* **凭据隔离点**：只有状态机进入 `DECISION_BOUND` 且结果为 `APPROVED` 时，控制权才移交给拥有 GitHub Auth Token 的 `Publisher Process`。`Worker Process` 即使崩溃或被注入，也永远无法触及 `Publisher` 凭据。

---

## 七、 实施演进路线图

建议按以下四个 Phase 逐步实施：

* **Phase A: Data Protocols & Deterministic Jury (最快见效)**
* 定义 `EvidenceDigest` 和 `ReviewDecision` JSON Schema。
* 把“确定性测试 Runner”从 Worker 中剥离，作为第一个独立的 Post-Coding Verifier。


* **Phase B: Parallel Verification Jury Engine**
* 实现基于 Git Worktree 的并行 Verifier 调度器（实现 1 个确定性 Runner + 1 个 Red-Team Security Agent）。
* 实现确定性硬编码裁决器（Security Veto 逻辑）。


* **Phase C: Task Router & Tiering System**
* 加入文件变动与复杂度预判逻辑，上线 Tier 0 快速通道，解决小任务开销问题。


* **Phase D: Pre-Coding Read-Only Exploration Fan-out**
* 挂载只读 Worktree，实现多 Explorer 探索并由 Lead 合并生成 `TaskSpec.json`。



---

## 八、 下一步行动计划

如果你准备开始实施，我们可以深入以下具体实现细节：