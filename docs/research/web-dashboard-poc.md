# 只读 Web 概览 + 实时 DAG POC（实验分支，不进主干）

- 分支：`exp/web-dashboard`
- 日期：2026-08-10
- 关联：[发展方向调研](marshal-future-directions.md) §2.1

## 结论

**真实可行。** `internal/dashboard` 以标准库实现只读可观测面，直接以 `.marshal` 的
`state.json` + `events.jsonl` 为事件源，HTTP + SSE 呈现实时 Task→Run 概览/DAG。
handler 层真实 HTTP 测试（`httptest`）通过：`/`、`/api/runs`、`/api/runs/{id}`、
`/api/stream`（SSE）均工作，且非 GET 一律 405（只读）。

## 设计约束（不碰信任边界）

- **只读**：仅 GET；approve/publish 等控制仍留在 CLI/Skill，Web 不成为第二权威；
- 默认仅 `127.0.0.1`；`--addr` 非 loopback 时打印警告（生产需自行加认证/反向代理/TLS）；
- 不输出凭据/环境，只投影 Run 状态字段。

## 运行

```bash
marshal serve --addr 127.0.0.1:7717   # 实验分支
```

## 生产化前置（本 POC 之外）

1. 认证/授权与 TLS/反向代理（多用户/远程）；
2. 远程状态源（独立机器部署，见方向 §1.1）；
3. 更完整 DAG（Attempt/Review/Publish 节点与边）、检索/分页（Run 增多后）；
4. 与 SQLite 索引（方向 §4.1）协同避免全量 JSONL 扫描。

## 验证

- `go test ./internal/dashboard/` 通过（ListRuns/ReadEvents/只读端点/SSE）；
- 真实数据：本机 47 个 Run 可被 `/api/runs` 投影（handler 层验证）。
