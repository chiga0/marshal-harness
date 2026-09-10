# UI-1 独立验收运行记录（预填骨架，定稿以最终执行状态为准）

日期：2026-09-10。设计基线 commit `5bbfa0f4`，实现基线随当前分支 HEAD（自动化填入）。

能力范围声明：UI-1 提交为「受控/固定配置下的任务全流程」。表格所列场景的执行状态与结论由主负责人在脚本与独立验收者处统一核对，不以作者自检代替。

## 状态速查

- 独立真实额引证型 / 足以书面论证的场景：记录其证据位置和负责人。
- 标签：PASS=通过；FAIL=失败；BLOCKED=当前阻塞而外部动作所要求（默认各项累计允许后才可断言为完全交付所需）

| ID | 需求 | 状态 | 证据与环境 | 备注 |
| --- | --- | --- | --- | --- |
| E01 | P01 连接与就绪 | NOT_RUN | — | — |
| E02 | P01 凭据错误/不可达 | NOT_RUN | — | — |
| E03 | P02 分页与筛选 | NOT_RUN | — | — |
| E04 | P03 超限与草稿保留 | NOT_RUN | — | — |
| E05 | P03 重复创建与 Idempotency-Key | NOT_RUN | — | — |
| E06 | P04 零问题确认 | NOT_RUN | — | — |
| E07 | P04 问题-答复-确认 | NOT_RUN | — | — |
| E08 | P04 双页版本争用 | NOT_RUN | — | — |
| E09 | P05/P08 双 Worker + 单等待 | NOT_RUN | — | — |
| E10 | P06 预解答与延迟 ACK | NOT_RUN | — | — |
| E11 | P06 串题与过期 | NOT_RUN | — | — |
| E12 | P07 暂停-恢复 | NOT_RUN | — | — |
| E13 | P07 单 Worker 取消与 501 | NOT_RUN | — | — |
| E14 | P07 取消竞态 | NOT_RUN | — | — |
| E15 | P09 候选/部分 | NOT_RUN | — | — |
| E16 | P09 最终成果下载 | NOT_RUN | — | — |
| E17 | P09 越权 / 8MiB | NOT_RUN | — | — |
| E18 | P10 Leader 授权允许/拒绝 | NOT_RUN | — | — |
| E19 | P10 后验失败/unknown | NOT_RUN | — | — |
| E20 | P11 断线后重放核对 | NOT_RUN | — | — |
| E21 | P11 轮询乱序/去重/隐藏页 | NOT_RUN | — | — |
| E22 | P11 服务重启与每次连接要求 | NOT_RUN | — | — |
| E23 | P08/P11 usage=null/501 | NOT_RUN | — | — |
| E24 | P12 主题与窄屏/缩放 | NOT_RUN | — | — |
| E25 | P12 仅键盘 | NOT_RUN | — | — |
| E26 | P01 外 Origin/Bearer 源头 | NOT_RUN | — | — |
| E27 | P01/P12 token/存储清理 | NOT_RUN | — | — |
| E28 | P09/P12 危险内容 | NOT_RUN | — | — |
| E29 | P13 安装与兼容 | NOT_RUN | — | — |
| E30 | P13 回滚 | NOT_RUN | — | 可选 |
| E31 | P06 Leader business pendingRequest | NOT_RUN | — | — |
| E32 | P01/P13 静态路径与 CSP | NOT_RUN | — | — |

## 资产

- 设计审查：`docs/ui-1/review.md` 两路关零 P0/P1。
- 实现列表：见 PR 审查；真实证据链必须由独立验收者从执行场景收集。

## 已知卡住（以不受影响项阻断）

记录当前 GOOD 值阻塞（外部权限、外部事故等）。若无，当前表报销 No自lane无法启动阻塞。
