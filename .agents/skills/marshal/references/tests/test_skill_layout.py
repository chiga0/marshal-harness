from __future__ import annotations

import re
import unittest
from pathlib import Path


SKILL_ROOT = Path(__file__).resolve().parents[2]
SKILL = SKILL_ROOT / "SKILL.md"
REQUIRED_REFERENCES = {
    "admission-and-acceptance.md": (
        "零 Attempt admission",
        "新 preflight 的正向路径门禁",
        "validate-acceptance-semantic-preflight.py",
        "verifier-worktree-mutated",
        "id/argv/cwd/timeoutSeconds/required=true/baselinePolicy/maxLogBytes",
        "fstat",
        "validate-plan-premortem-preflight.py",
        "marshal-fastpath-preflight.py",
        "combined-plan-preflight-pass",
        "content-task-semantic-manifest-required",
        "fixture bytes",
        "acceptance-semantic-timeout",
        "plan-premortem-timeout",
        "adapter-ordinary-user-execution-profile-unsupported",
        "qoder-deliverable-parent-missing",
        "不得为此单独提交空目录或骨架",
        "能创建父目录的 Adapter",
        "bin/marshal internal plan-premortem-check",
        "临时目录只承载输入文件",
    ),
    "review-and-rework.md": (
        "historyClaimed=true",
        "action=dispatch-reviewer",
        "action=generate-review-packet",
        "reasonCode",
        "validate-closure-matrix-preflight.py",
        "O_EXCL",
        "attemptId",
        "maxReworkRounds=1",
        "一次 aggregate rework",
        "--marshal \"$REPOSITORY_ROOT/bin/marshal\"",
    ),
    "adapter-promotion-and-mac.md": (
        "authorityMode=ordinary-user",
        "Ordinary-user 诊断路径",
        "Strict/exact 晋升路径",
        "transcript-attestation-pass",
        "protocol-invalid/do-not-retry",
        "WorkerResult",
        "codex-provider-schema-compatible",
        "status=fail",
        "Qoder v7 transcript attestation",
        "qoder-stream-json-1.2.0-v7",
        "codex-provider-schema-check",
        "review-freshness-check",
    ),
    "watchdog-and-capacity.md": (
        "memoryAvailableBytes",
        "slotsAvailable",
        "processOwnership",
        "dedupeKey",
    ),
    "delivery-supervision.md": (
        "Supervisor 是 Lead 控制面的只读观察职责",
        "正向路径先行",
        "continue",
        "freeze-fanout",
        "replan",
        "intervene",
        "production dependency graph",
        "Lead + 最多 2 authors + 1 shared reviewer",
        "默认禁止 skeleton-only commit",
        "同 signature 第二次出现",
    ),
    "publication-and-reconcile.md": (
        "ObserveChecks",
        "RemoteCheckRecord",
        "PUBLISHING",
        "CI_PENDING",
        "pendingRemoteSync",
    ),
    "engineering-and-release.md": (
        "sourceHead",
        "localMergeSha",
        "make check",
        "git merge-tree",
        "同一未变化候选",
        "纯 Markdown/Skill 机械变更",
        "release candidate",
    ),
}
LINK = re.compile(r"\[[^\]]*\]\(([^)]+)\)")


class SkillLayoutTest(unittest.TestCase):
    def test_top_level_has_bounded_default_read_cost(self) -> None:
        data = SKILL.read_bytes()
        self.assertLessEqual(len(data), 12_288)
        self.assertEqual(data.decode("utf-8").encode("utf-8"), data)

    def test_routes_every_required_reference(self) -> None:
        content = SKILL.read_text(encoding="utf-8")
        for name in REQUIRED_REFERENCES:
            with self.subTest(reference=name):
                self.assertIn(f"](references/{name})", content)
                reference = SKILL_ROOT / "references" / name
                self.assertTrue(reference.is_file())
                first_lines = "\n".join(reference.read_text(encoding="utf-8").splitlines()[:5])
                self.assertIn("何时必须读取：", first_lines)

    def test_moved_contract_anchors_remain_machine_checked(self) -> None:
        for name, anchors in REQUIRED_REFERENCES.items():
            content = (SKILL_ROOT / "references" / name).read_text(encoding="utf-8")
            for anchor in anchors:
                with self.subTest(reference=name, anchor=anchor):
                    self.assertIn(anchor, content)

    def test_top_level_keeps_authority_lifecycle_and_truthful_boundary(self) -> None:
        content = SKILL.read_text(encoding="utf-8")
        for anchor in (
            "明确要求“使用 Marshal”",
            "主 Agent（pi、Codex 等编码 Agent）",
            "不要绕过 Core",
            "marshal task review",
            "Worker 不能为自己的工作提供权威验证",
            "PUBLISHING",
            "CI_PENDING",
            "ACCEPTED",
            "NO_CHANGE",
            "REJECTED",
            "BLOCKED",
            "authorityMode=ordinary-user",
            "不得称为 hardened authority、APAP、sandbox",
            "historyClaimed=true",
            "action=dispatch-reviewer",
            "action=generate-review-packet",
            "reasonCode",
        ):
            with self.subTest(anchor=anchor):
                self.assertIn(anchor, content)

    def test_reference_routing_is_just_in_time(self) -> None:
        content = SKILL.read_text(encoding="utf-8")
        for anchor in (
            "Just-in-time",
            "下一项具体动作",
            "禁止为了未来可能进入的 lifecycle",
            "只读审计",
            "维护者本地机械小修",
            "本次实际受影响的 reference",
        ):
            with self.subTest(anchor=anchor):
                self.assertIn(anchor, content)

    def test_efficiency_contract_has_truthful_single_verdict_and_rule_budget(self) -> None:
        skill = SKILL.read_text(encoding="utf-8")
        supervision = (SKILL_ROOT / "references" / "delivery-supervision.md").read_text(encoding="utf-8")
        review = (SKILL_ROOT / "references" / "review-and-rework.md").read_text(encoding="utf-8")
        migration = (SKILL_ROOT / "references" / "skill-rule-migration.md").read_text(encoding="utf-8")

        for anchor in (
            "一个面向 operator 的机器化 verdict 入口",
            "Skill 规则采用替换预算",
            "按 defect-owner 先归属",
            "唯一 reviewer 的一次 aggregate rework",
        ):
            with self.subTest(document="SKILL.md", anchor=anchor):
                self.assertIn(anchor, skill)
        for anchor in (
            "Python preflight 可以作为该 phase 的唯一 operator verdict 入口",
            "新增 reviewer 代替唯一独立 reviewer",
        ):
            with self.subTest(document="delivery-supervision.md", anchor=anchor):
                self.assertIn(anchor, supervision)
        for anchor in (
            "validate-review-freshness-preflight.py` 是当前 review freshness phase 的唯一 operator verdict 入口",
            "固定 internal command 当前只返回 `{ok,digest}` primitive",
        ):
            with self.subTest(document="review-and-rework.md", anchor=anchor):
                self.assertIn(anchor, review)
        self.assertIn("当前 review freshness operator verdict 位于 Python preflight", migration)

        # Guard against again documenting the fixed primitive as if it owned
        # the wrapper's complete identity/lineage/action verdict.
        self.assertNotIn("Operator 只检查固定命令返回的 closed", review)
        self.assertNotIn("固定 Marshal 内部命令/Core package 是字段级语义的唯一实现", supervision)

    def test_qoder_transcript_reference_tracks_current_v7_contract(self) -> None:
        content = (SKILL_ROOT / "references" / "transcript-attestation-preflight.md").read_text(encoding="utf-8")
        self.assertIn("Qoder v7", content)
        self.assertIn("qoder-stream-json-1.2.0-v7", content)
        self.assertIn("v7 尚须取得 fresh Mac evidence", content)
        self.assertIn("internal qoder-transcript-check", content)
        self.assertIn("--marshal /ABSOLUTE/REPOSITORY/bin/marshal", content)
        self.assertIn("marshal-transcript-attestation-v3", content)
        self.assertNotIn("transcript-attestation-checker", content)
        self.assertNotIn("--checker /ABSOLUTE/OPERATOR/DIR/transcript-attestation-checker", content)
        self.assertNotIn("当前实现只支持版本化冻结的 Qoder v6", content)

    def test_plan_and_provider_references_forbid_anonymous_production_checkers(self) -> None:
        admission = (SKILL_ROOT / "references" / "admission-and-acceptance.md").read_text(encoding="utf-8")
        adapter = (SKILL_ROOT / "references" / "adapter-promotion-and-mac.md").read_text(encoding="utf-8")
        self.assertIn("--marshal \"$REPOSITORY_ROOT/bin/marshal\"", admission)
        self.assertNotIn("go build -o \"$OPERATOR_ROOT/plan-premortem-core-probe\"", admission)
        self.assertIn("codex-provider-schema-check", adapter)
        self.assertNotIn('go build -o "$OPERATOR_DIR/codex-provider-schema-checker"', adapter)
        closure = (SKILL_ROOT / "references" / "review-and-rework.md").read_text(encoding="utf-8")
        self.assertIn("--marshal \"$REPOSITORY_ROOT/bin/marshal\"", closure)
        self.assertNotIn("go run", closure)
        closure_validator = (SKILL_ROOT / "references" / "validate-closure-matrix-preflight.py").read_text(encoding="utf-8")
        self.assertNotIn("go run", closure_validator)
        self.assertIn("closure-matrix-check", closure_validator)
        for filename in (
            "validate-plan-premortem-preflight.py",
            "validate-codex-provider-schema-preflight.py",
            "validate-review-freshness-preflight.py",
            "validate-closure-matrix-preflight.py",
            "marshal-fastpath-preflight.py",
        ):
            content = (SKILL_ROOT / "references" / filename).read_text(encoding="utf-8")
            with self.subTest(filename=filename):
                self.assertNotIn("--checker", content)
                self.assertNotIn("go run", content)
                self.assertNotIn("go build -o", content)

    def test_test_binaries_are_built_inside_repository(self) -> None:
        # macOS host security policies may refuse to execute freshly built
        # unsigned binaries from the per-user system temp directory; every
        # compiled test helper must land under the repository's gitignored
        # bin/ tree instead of an anonymous temp directory.
        tests_root = SKILL_ROOT / "references" / "tests"
        for path in sorted(tests_root.glob("test_*.py")):
            content = path.read_text(encoding="utf-8")
            if '"go", "build"' not in content and '"make", "build"' not in content:
                continue
            with self.subTest(filename=path.name):
                self.assertTrue(
                    '"bin" / "test"' in content or '"bin/marshal"' in content,
                    f"{path.name} builds a binary outside the repository bin/ tree",
                )
        repository_root = SKILL_ROOT.parents[2]
        for path in sorted(repository_root.rglob("*_test.go")):
            parts = path.relative_to(repository_root).parts
            if parts and parts[0] in (".git", ".marshal", "bin"):
                continue
            content = path.read_text(encoding="utf-8")
            if '"go", "build"' not in content:
                continue
            with self.subTest(filename=path.relative_to(repository_root).as_posix()):
                self.assertIn('"bin", "test"', content)

    def test_operator_commands_and_installer_use_stable_marshal_path(self) -> None:
        for filename in ("docs/development.md", "docs/en/quick-start.md", "docs/mac-first-authority-handoff.md"):
            content = (SKILL_ROOT.parents[2] / filename).read_text(encoding="utf-8")
            with self.subTest(filename=filename):
                self.assertEqual(content.count("go run ./cmd/marshal"), 1)
                self.assertIn("bin/marshal", content)
        installer = (SKILL_ROOT.parents[2] / "scripts/install.sh").read_text(encoding="utf-8")
        self.assertIn(".marshal-staging", installer)
        self.assertNotIn('"${TMP_DIR}/${BIN_NAME}" ./cmd/marshal', installer)

    def test_all_relative_markdown_links_exist(self) -> None:
        documents = [SKILL, SKILL_ROOT / "references" / "skill-rule-migration.md"]
        documents.extend(SKILL_ROOT / "references" / name for name in REQUIRED_REFERENCES)
        for document in documents:
            content = document.read_text(encoding="utf-8")
            for raw_target in LINK.findall(content):
                target = raw_target.split("#", 1)[0]
                if not target or "://" in target or target.startswith("mailto:"):
                    continue
                resolved = (document.parent / target).resolve()
                with self.subTest(document=document.name, target=raw_target):
                    self.assertTrue(resolved.exists(), f"missing Markdown target: {resolved}")

    def test_migration_map_covers_every_route(self) -> None:
        migration = (SKILL_ROOT / "references" / "skill-rule-migration.md").read_text(encoding="utf-8")
        for name in REQUIRED_REFERENCES:
            with self.subTest(reference=name):
                self.assertIn(f"`{name}`", migration)


if __name__ == "__main__":
    unittest.main()
