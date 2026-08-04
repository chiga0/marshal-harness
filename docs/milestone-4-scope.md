# Milestone 4 冻结范围

状态：`FROZEN_FOR_IMPLEMENTATION`

冻结日期：2026-08-04

## 目标

以 OpenCode `1.18.12` 实现首个真实 Worker Adapter，使冻结 WorkerRequest 能在受管 worktree 中完成一次有界、可取消、可审计的非交互编辑，并产生 Schema-valid WorkerResult。不得执行发布、commit、push 或 PR/MR 副作用。

## Adapter 契约

- Adapter ID 固定为 `opencode`；构造时要求显式 absolute executable，不在运行时搜索相似命令；
- `Probe` 记录 executable realpath、binary SHA-256、精确 version、结构化输出、session、model、profile 与 budget 能力；未知版本默认 `unsupported`，显式实验开关才可标记 `experimental`；
- `Run` 只接受 Schema-valid WorkerRequest，且 task/run/attempt/adapter/worktree 身份一致；
- argv 使用 `opencode run --pure --format json`，不得拼接 shell command；模型选择只来自冻结 TaskSpec/Request；
- 通过 `OPENCODE_CONFIG_CONTENT` 注入 fail-closed Permission，禁用外部目录、web、subagent、skill、question 与发布命令；不使用 ambient auto-approve 作为安全边界；
- 环境采用 allowlist，并显式删除 GitHub/GitLab/Cloud/SSH/Publisher credential；允许 OpenCode 使用自身认证存储，但不得把认证内容写入 transcript；
- stdout JSONL 逐行解析并规范化；保存有界 raw transcript、首尾截断信息、session ID、exit/signal/usage 与 permission denial；
- Worker 写出的 result 文件只是声明，必须通过 WorkerResult Schema；Adapter 覆盖或验证不可伪造的 identity、executable、version、session 与时间字段；
- 超时或 Context Cancellation 终止完整进程组；输出超过预算立即取消并返回有界失败记录。

## Core 与 CLI

实现 `marshal task run --run RUN_ID [--json]` 的最小真实执行路径：

- 只接受 `READY`、`RETRY_PENDING` 或 `REWORK_REQUESTED`；
- 持有 Run Lease 与 Task Worktree Lease 后分配新 Attempt；
- 验证冻结 TaskSpec、CapabilitySnapshot 与预算，再进入 `RUNNING`；
- 保存 WorkerRequest、Prompt、raw/normalized transcript 与 WorkerResult；
- Worker 协议完成后记录真实 snapshot 并进入 `VERIFYING`；
- Provider/协议错误按 `maxOperationalRetries` 进入 `RETRY_PENDING`，预算耗尽或缺能力进入 `BLOCKED`；
- 任何路径都不得把 Worker declaration 当作 Verification evidence，也不得自动调用 `task verify`。

## 测试与退出条件

- Unit：版本解析、exact executable、binary digest、argv、环境过滤、Permission config、JSONL parser、output cap、result identity；
- Conformance：Fake 与 OpenCode Adapter 通过同一成功、失败、阻塞、取消、超限 Fixture；
- Integration：使用 fake OpenCode executable 验证 process group cancellation、session capture、malformed JSONL、permission denial 与 crash-safe transcript；
- Live E2E：本机真实 OpenCode 在临时仓库/受管 worktree 完成一个最小文件修改，随后 Marshal 独立 Verification 通过；测试不 commit、不 push、不访问 Publisher；
- Security：Worker 环境没有 Publisher credential，外部目录和显式发布命令被 deny；unsupported version 在进程启动前失败；
- `make ci`、独立 OpenCode 只读审计、提交推送与远端 CI 全绿后才进入 M5。

## 明确不在本阶段

- Qwen 与 Pi 正式 Adapter；
- Session resume；
- Hardened container/VM；
- Git commit、push、GitHub Draft PR、Remote CI 或 merge；
- 自动 Recovery/Reconciliation 与 Cleanup。
