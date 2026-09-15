# ADR 0102：TypeScript 工具链迁移与工程门禁

- 状态：Accepted（2026-09-15；维护者明确要求迁移 TypeScript 并补齐 lint/typecheck/测试/覆盖率与发布 CI 门禁；独立审查结论未完成前仅代表实施授权，发行按原门禁单独验收）。
- 基线：`80f6fed568e21b5488b35c7048048f79c42b8adb`。
- 关联：ADR0097（能力准入）、ADR0099（Node 主线）、ADR0100、ADR0101。本 ADR 不替代任何业务、HTTP、持久化、权限或发行合同。

## 问题

主线 296 个 `.mjs`、零构建步骤。两类成本持续升高：(a) 无类型系统，Port/闭集合同（Binding、WorkBinding、Fault 等扩展契约语义对象）跨包演进只能靠运行时断言与评审纪律，规模化重构风险高；(b) 质量入口只有 node:test，没有统一 lint、typecheck 与覆盖率门禁。直接引入构建链（tsc 产物与源码双份）违背"只有一种源"的分发原则，也会污染由 `git ls-tree` 锁定的发行清单。

## 决策

1. **迁移方式**：全部受跟踪 `.mjs` → `.ts` 一次性 `git mv` 重命名（保留 blame），文件内全部 `.mjs` 显式引用（import/export 说明符、动态 `load()`、spawn 参数、打包入口 manifest、参与配置摘要的文件名单）同步改为 `.ts`。不修改任何业务语义、HTTP 合同、持久格式或权限模型；测试期望中原样硬编码 `.mjs` 文件名者逐条列出修改。
2. **无构建运行时**：使用 Node ≥22.18 原生 type stripping（仅 erasable syntax），维持"源码即发行"。**运行时零依赖不变**。Node 支持下限由 ">=22" 精确为 ">=22.18.0"；已验证目标 22.22.1 / 24.15.0 不变。禁止 enum / namespace / 参数属性等非 erasable 语法（`tsconfig.erasableSyntaxOnly` 与 ESLint 双重兜底）。
3. **严格度分期**（本 ADR 覆盖的执行计划，后续逐包严格化不再单独立 ADR，涉及语义变化时按原规则另审）：
   - Phase A（本 PR）：`tsconfig` 启用 `noEmit` + `allowImportingTsExtensions` + `erasableSyntaxOnly`，不启用 `strict`/`noImplicitAny`——纯 JS 原文必然大面积隐式 any，机械打开属伪严格。首次全量 `tsc --noEmit` 报 3450 处推断型错误（219 文件，以解构形状 TS2339 为主），这些不是可执行缺陷；本 PR 内已顺带修复 2 处，提交基线为 3448。
   - Phase A 门禁形态：`scripts/typecheck-gate.ts` 全量运行 tsc，与提交的 `toolchain/typecheck-baseline.json`（每文件错误数）比较——任何文件错误数上升或出现基线外新错误文件即 CI 失败，**只降不升**；修复后以 `--update` 下调基线；上调基线必须在 PR 中说明并引用本 ADR。
   - Phase B+（后续 PR）：按包开启 `strict` 并消化基线，从纯数据/契约包（task-store、task-api 边界）开始，不改业务语义；每一步以既有测试全绿与覆盖率不降为门禁。
4. **ESLint**：新增根 `eslint.config.js`（flat）：`@eslint/js` recommended + Node globals + typescript-eslint parser（为类型注解预备，ts 规则集随 Phase B 收紧）。仅通过配置收敛规则集，不改产品语义；lint 首批暴露的 12 处真实问题（finally 内 return、死赋值等）单独 commit 修复，故意负例行内豁免。全部开发依赖（eslint、@eslint/js、typescript-eslint、globals、typescript、@types/node）精确钉版，仅开发期使用。**devDependencies 不进发行包**：候选清单由 `git ls-tree` 锁定范围决定，根工具链文件（package.json/tsconfig/eslint.config.js/toolchain/）不在其内。
5. **覆盖率与 CI**：CI 新增两个单 job 门禁（不做 OS/Node 矩阵重复付费）：`toolchain`（`npm ci && npm run lint && node scripts/typecheck-gate.ts`）与 `coverage`。覆盖率不直接使用内置 `--experimental-test-coverage` 报表：custody/recovery fixture 中的 SIGKILL 会截断继承 `NODE_V8_COVERAGE` 的子进程写入文件使内置合并整体放弃报告（本 PR 实测 2/2 复现），故解耦为 `NODE_V8_COVERAGE` 原始采集 + `scripts/coverage-gate.ts` 受控合并（剔损计数披露、叶子区间合并、covered 行/非空行），基线存于 `toolchain/coverage-baseline.json`，只升不降，下调须在本 ADR 追记原因。`package` job 依赖全部四项门禁。E2E 的品类扩展（task 级全链、UI playwright 之外）另行立项，不在本 PR。
6. **脚本/安装器边界**：
   - 面向**当前树候选**的 `node-candidate-admit.py` 与其 `*_test.py` 同步改写为 `.ts` 引用。
   - `prepare-node-mirror.py` 的 helper（distribution）下载改为 `.ts` / `.mjs` 按 SOURCE 时代双探；下一发行版的 helper 资产命名与 installer 常量在各自发行 PR 中处理。
   - **`install-node.sh` / `install-node-preview.sh` 与其 `install-node.test.py` 一字不改**——它们安装的是已发布的固定旧版 ZIP（内容确为 `.mjs`），改写将破坏对历史发行物的忠实安装。
   - `sync-node-oss.py` 中版本钉死的资产常量不改。
   - `v102-validator.fixture.txt` 等历史发行物字节副本不改。
7. **文档**：本 PR 不批量改写 `docs/`、各包 `README.md`、`skills/` 中的 `.mjs` 文本引用。冻结材料（`*-reference-*.md`、各发行记录、审计/验收记录、历史 ADR）一律禁动；非冻结说明文档的 `.mjs` 引用清扫列入后续任务，单独 PR。
8. **配置身份**：以受管配置源码文件名单参与摘要者（如 task-leader-report 配置摘要）随重命名变化；新旧摘要精确对应各自配置，旧服务根按原规则拒绝漂移。旧安装包、旧数据根、已发布资产不受影响。

## 验收与发行

- 既有全部 node --test 套件在 ubuntu/macos × Node 22.22.1/24.15.0 矩阵通过；`.mjs→.ts` 期望修改逐条出现在 diff 且不含断言语义变化。
- `npm ci && npm run typecheck && npm run lint` 通过；覆盖率阈值门禁生效。
- 候选 `pack / verify / restore-carrier / consume` 测试通过，manifest 反映 `.ts` 路径；针对历史 v1.0.2 包的 upgrade-validator 既有正负例不回归。
- 本 ADR 不触发新版本发行；OpenAPI、SQLite 格式、权限模型与已发布资产不变。
