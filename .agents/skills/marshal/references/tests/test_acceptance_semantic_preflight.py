from __future__ import annotations

import hashlib
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest


REPOSITORY_ROOT = Path(__file__).resolve().parents[5]
SKILL_ROOT = REPOSITORY_ROOT / ".agents/skills/marshal"
VALIDATOR = SKILL_ROOT / "references/validate-acceptance-semantic-preflight.py"
FIXTURE_RELATIVE = Path(".agents/skills/marshal/references/fixtures/acceptance-semantic")
FIXTURES = REPOSITORY_ROOT / FIXTURE_RELATIVE


def digest(path: Path) -> str:
    return "sha256:" + hashlib.sha256(path.read_bytes()).hexdigest()


class AcceptanceSemanticPreflightTest(unittest.TestCase):
    def invoke(self, root: Path, manifest_name: str, task_name: str) -> subprocess.CompletedProcess[str]:
        fixtures = root / FIXTURE_RELATIVE
        return subprocess.run(
            [
                sys.executable,
                "-B",
                str(VALIDATOR),
                "--root",
                str(root),
                "--manifest",
                str(fixtures / manifest_name),
                "--task-spec",
                str(fixtures / task_name),
            ],
            check=False,
            capture_output=True,
            text=True,
        )

    def assert_failure(self, completed: subprocess.CompletedProcess[str], reason: str) -> None:
        self.assertNotEqual(completed.returncode, 0, completed.stdout)
        payload = json.loads(completed.stderr)
        self.assertEqual(payload["status"], "fail")
        self.assertEqual(payload["reasonCode"], reason)

    def copied_fixtures(self, directory: str) -> tuple[Path, Path]:
        root = Path(directory)
        destination = root / FIXTURE_RELATIVE
        destination.parent.mkdir(parents=True)
        shutil.copytree(FIXTURES, destination)
        return root, destination

    def test_adr0035_r1_without_markdown_backtick_normalizer_fails(self) -> None:
        completed = self.invoke(REPOSITORY_ROOT, "manifest-r1.json", "task-spec-r1.json")
        self.assert_failure(completed, "positive-fixture-failed")
        self.assertIn("missing-required-all", completed.stderr)

    def test_adr0035_r2_normalizes_and_exercises_all_negative_fixtures(self) -> None:
        completed = self.invoke(REPOSITORY_ROOT, "manifest-r2.json", "task-spec-r2.json")
        self.assertEqual(completed.returncode, 0, completed.stderr)
        payload = json.loads(completed.stdout)
        self.assertEqual(payload["status"], "pass")
        self.assertEqual(payload["normalizer"], "markdown-backtick-strip+nfkc-casefold")
        self.assertEqual(payload["positiveFixtures"], 1)
        self.assertEqual(payload["negativeFixtures"], 3)

    def test_deleting_required_token_from_positive_fixture_fails(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root, fixtures = self.copied_fixtures(directory)
            report = fixtures / "report-positive.md"
            report.write_text(report.read_text().replace("four-dimensional gate", "dimension checks"))
            manifest_path = fixtures / "manifest-r2.json"
            manifest = json.loads(manifest_path.read_text())
            manifest["fixtures"]["positive"][0]["digest"] = digest(report)
            manifest_path.write_text(json.dumps(manifest, ensure_ascii=False))
            self.assert_failure(
                self.invoke(root, "manifest-r2.json", "task-spec-r2.json"),
                "positive-fixture-failed",
            )

    def test_forbidden_token_in_positive_fixture_fails(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root, fixtures = self.copied_fixtures(directory)
            report = fixtures / "report-positive.md"
            report.write_text(report.read_text() + "\nunsafe override\n")
            manifest_path = fixtures / "manifest-r2.json"
            manifest = json.loads(manifest_path.read_text())
            manifest["fixtures"]["positive"][0]["digest"] = digest(report)
            manifest_path.write_text(json.dumps(manifest, ensure_ascii=False))
            self.assert_failure(
                self.invoke(root, "manifest-r2.json", "task-spec-r2.json"),
                "positive-fixture-failed",
            )

    def test_changed_prompt_literal_fails_after_refreshing_task_digest(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root, fixtures = self.copied_fixtures(directory)
            task_path = fixtures / "task-spec-r2.json"
            task = json.loads(task_path.read_text())
            task["work"]["context"][0] = task["work"]["context"][0].replace(
                "ReviewDecision binds digest", "ReviewDecision binds evidence"
            )
            task_path.write_text(json.dumps(task, ensure_ascii=False))
            manifest_path = fixtures / "manifest-r2.json"
            manifest = json.loads(manifest_path.read_text())
            manifest["taskSpecDigest"] = digest(task_path)
            manifest_path.write_text(json.dumps(manifest, ensure_ascii=False))
            self.assert_failure(
                self.invoke(root, "manifest-r2.json", "task-spec-r2.json"),
                "prompt-literal-unmapped",
            )

    def test_equivalent_content_requires_required_any_group(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root, fixtures = self.copied_fixtures(directory)
            manifest_path = fixtures / "manifest-r2.json"
            manifest = json.loads(manifest_path.read_text())
            manifest["required_any"] = [["authority boundary"]]
            manifest_path.write_text(json.dumps(manifest, ensure_ascii=False))
            self.assert_failure(
                self.invoke(root, "manifest-r2.json", "task-spec-r2.json"),
                "required-any-not-equivalent",
            )

    def test_changed_command_argv_fails_even_with_refreshed_task_digest(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root, fixtures = self.copied_fixtures(directory)
            task_path = fixtures / "task-spec-r2.json"
            task = json.loads(task_path.read_text())
            task["acceptance"]["commands"][0]["argv"][-1] += " "
            task_path.write_text(json.dumps(task, ensure_ascii=False))
            manifest_path = fixtures / "manifest-r2.json"
            manifest = json.loads(manifest_path.read_text())
            manifest["taskSpecDigest"] = digest(task_path)
            manifest_path.write_text(json.dumps(manifest, ensure_ascii=False))
            self.assert_failure(
                self.invoke(root, "manifest-r2.json", "task-spec-r2.json"),
                "argv-digest-mismatch",
            )

    def test_task_spec_digest_mismatch_fails_before_semantic_execution(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root, fixtures = self.copied_fixtures(directory)
            task_path = fixtures / "task-spec-r2.json"
            task_path.write_text(task_path.read_text() + "\n")
            self.assert_failure(
                self.invoke(root, "manifest-r2.json", "task-spec-r2.json"),
                "task-spec-digest-mismatch",
            )

    def test_fixture_digest_mismatch_fails_before_command_execution(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root, fixtures = self.copied_fixtures(directory)
            report = fixtures / "report-positive.md"
            report.write_text(report.read_text() + "\n")
            self.assert_failure(
                self.invoke(root, "manifest-r2.json", "task-spec-r2.json"),
                "fixture-digest-mismatch",
            )

    def test_non_utf8_fixture_fails_with_stable_reason(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root, fixtures = self.copied_fixtures(directory)
            report = fixtures / "report-positive.md"
            report.write_bytes(b"\xff")
            manifest_path = fixtures / "manifest-r2.json"
            manifest = json.loads(manifest_path.read_text())
            manifest["fixtures"]["positive"][0]["digest"] = digest(report)
            manifest_path.write_text(json.dumps(manifest, ensure_ascii=False))
            self.assert_failure(
                self.invoke(root, "manifest-r2.json", "task-spec-r2.json"),
                "fixture-unreadable",
            )

    def test_schema_and_template_are_json_and_operator_local(self) -> None:
        schema = json.loads((SKILL_ROOT / "references/acceptance-semantic-manifest.schema.json").read_text())
        template = json.loads((SKILL_ROOT / "templates/acceptance-semantic-manifest.json").read_text())
        self.assertEqual(schema["$schema"], "https://json-schema.org/draft/2020-12/schema")
        self.assertIn("operator-local", schema["$id"])
        self.assertEqual(template["manifestVersion"], "marshal-operator-acceptance-preflight/v1")


if __name__ == "__main__":
    unittest.main()
