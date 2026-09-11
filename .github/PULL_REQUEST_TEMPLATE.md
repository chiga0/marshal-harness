## 动机 / Motivation
<!-- 关联 Issue；一句话说明本 PR 解决什么 -->

## 变更内容 / Changes
<!-- 分点列出；涉及 Schema/生命周期/权限的变更请注明对应 ADR -->

## 验证 / Verification
- [ ] `node --test --test-concurrency=1 packages/*/*.test.mjs` 本地通过
- [ ] apps/task-web 变更时：`npm run typecheck && npm run build && npx vitest run`（在 apps/task-web 内）通过
- [ ] 新增/更新测试覆盖失败路径
- [ ] 文档同步更新（中文，术语保留英文）

## 不变量自查 / Invariants
- [ ] 不违反 AGENTS.md 任一不可破坏不变量
