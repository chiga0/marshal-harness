from __future__ import annotations

import hashlib
import importlib.util
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import types
import unittest
from unittest import mock


REPOSITORY_ROOT = Path(__file__).resolve().parents[5]
SKILL_ROOT = REPOSITORY_ROOT / ".agents/skills/marshal"
VALIDATOR = SKILL_ROOT / "references/validate-acceptance-semantic-preflight.py"
SCHEMA = SKILL_ROOT / "references/acceptance-semantic-manifest.schema.json"
SCHEMA_PROBE = SKILL_ROOT / "references/tests/acceptance_semantic_schema_probe.go"
TEMPLATE = SKILL_ROOT / "templates/acceptance-semantic-manifest.json"
FIXTURE_RELATIVE = Path(".agents/skills/marshal/references/fixtures/acceptance-semantic")
FIXTURES = REPOSITORY_ROOT / FIXTURE_RELATIVE


def digest(path: Path) -> str:
    return "sha256:" + hashlib.sha256(path.read_bytes()).hexdigest()


def canonical_digest(value: object) -> str:
    encoded = json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode()
    return "sha256:" + hashlib.sha256(encoded).hexdigest()


def load_validator_module():
    spec = importlib.util.spec_from_file_location("acceptance_semantic_preflight", VALIDATOR)
    if spec is None or spec.loader is None:
        raise RuntimeError("cannot load validator")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


VALIDATOR_MODULE = load_validator_module()


class AcceptanceSemanticPreflightTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls._binary_directory = tempfile.TemporaryDirectory()
        cls.marshal_binary = Path(cls._binary_directory.name) / "marshal"
        completed = subprocess.run(
            ["go", "build", "-o", str(cls.marshal_binary), "./cmd/marshal"],
            cwd=REPOSITORY_ROOT,
            check=False,
            capture_output=True,
            text=True,
        )
        if completed.returncode != 0:
            raise RuntimeError(completed.stderr)

    @classmethod
    def tearDownClass(cls) -> None:
        cls._binary_directory.cleanup()

    def invoke(self, root: Path, manifest_name: str = "manifest-r2.json", task_name: str = "task-spec-r2.json") -> subprocess.CompletedProcess[str]:
        fixtures = root / FIXTURE_RELATIVE
        return subprocess.run(
            [
                sys.executable,
                "-B",
                str(VALIDATOR),
                "--root",
                str(root),
                "--schema",
                str(SCHEMA),
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
        self.assertEqual(payload["reasonCode"], reason, completed.stderr)

    def copied_fixtures(self, directory: str) -> tuple[Path, Path]:
        root = Path(directory)
        destination = root / FIXTURE_RELATIVE
        destination.parent.mkdir(parents=True)
        shutil.copytree(FIXTURES, destination)
        return root, destination

    def load_pair(self, fixtures: Path) -> tuple[Path, dict, Path, dict]:
        task_path = fixtures / "task-spec-r2.json"
        manifest_path = fixtures / "manifest-r2.json"
        return (
            task_path,
            json.loads(task_path.read_text()),
            manifest_path,
            json.loads(manifest_path.read_text()),
        )

    def write_pair(self, task_path: Path, task: dict, manifest_path: Path, manifest: dict, bind_command: bool = False) -> None:
        task_path.write_text(json.dumps(task, ensure_ascii=False, indent=2) + "\n")
        manifest["taskSpecDigest"] = digest(task_path)
        if bind_command:
            command = task["acceptance"]["commands"][0]
            manifest["command"].update(command)
            manifest["command"]["argvDigest"] = canonical_digest(command["argv"])
            manifest["command"]["tupleDigest"] = canonical_digest(command)
        manifest_path.write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + "\n")

    def test_adr0035_r1_without_markdown_backtick_normalizer_fails(self) -> None:
        completed = self.invoke(REPOSITORY_ROOT, "manifest-r1.json", "task-spec-r1.json")
        self.assert_failure(completed, "positive-fixture-failed")
        self.assertIn("missing-required-all", completed.stderr)

    def test_adr0035_r2_passes_all_semantic_and_boundary_negatives(self) -> None:
        completed = self.invoke(REPOSITORY_ROOT)
        self.assertEqual(completed.returncode, 0, completed.stderr)
        payload = json.loads(completed.stdout)
        self.assertEqual(payload["normalizer"], "markdown-backtick-strip+nfkc-casefold")
        self.assertEqual(payload["positiveFixtures"], 1)
        self.assertEqual(payload["negativeFixtures"], 5)

    def test_fixture_tasks_are_real_contract_valid_taskspecs(self) -> None:
        for name in ("task-spec-r1.json", "task-spec-r2.json"):
            completed = subprocess.run(
                [str(self.marshal_binary), "contract", "validate", "--schema", "task-spec", str(FIXTURES / name)],
                cwd=REPOSITORY_ROOT,
                check=False,
                capture_output=True,
                text=True,
            )
            self.assertEqual(completed.returncode, 0, completed.stderr)
            self.assertIn("有效：Task", completed.stdout)

    def test_optional_selected_command_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root, fixtures = self.copied_fixtures(directory)
            task_path, task, manifest_path, manifest = self.load_pair(fixtures)
            task["acceptance"]["commands"][0]["required"] = False
            self.write_pair(task_path, task, manifest_path, manifest)
            self.assert_failure(self.invoke(root), "command-not-required")

    def test_command_cwd_drift_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root, fixtures = self.copied_fixtures(directory)
            task_path, task, manifest_path, manifest = self.load_pair(fixtures)
            task["acceptance"]["commands"][0]["cwd"] = "checks"
            self.write_pair(task_path, task, manifest_path, manifest)
            self.assert_failure(self.invoke(root), "command-tuple-mismatch")

    def test_incomplete_command_tuple_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root, fixtures = self.copied_fixtures(directory)
            task_path, task, manifest_path, manifest = self.load_pair(fixtures)
            del task["acceptance"]["commands"][0]["baselinePolicy"]
            self.write_pair(task_path, task, manifest_path, manifest)
            self.assert_failure(self.invoke(root), "command-tuple-incomplete")

    def test_command_omitted_rule_is_rejected_after_rebinding(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root, fixtures = self.copied_fixtures(directory)
            task_path, task, manifest_path, manifest = self.load_pair(fixtures)
            command = task["acceptance"]["commands"][0]
            command["argv"][4] = command["argv"][4].replace(
                "['reviewdecision binds digest','four-dimensional gate']",
                "['reviewdecision binds digest']",
            )
            self.write_pair(task_path, task, manifest_path, manifest, bind_command=True)
            self.assert_failure(self.invoke(root), "semantic-manifest-mismatch")

    def test_command_extra_rule_is_rejected_after_rebinding(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root, fixtures = self.copied_fixtures(directory)
            task_path, task, manifest_path, manifest = self.load_pair(fixtures)
            command = task["acceptance"]["commands"][0]
            command["argv"][4] = command["argv"][4].replace(
                "['unsafe override']", "['unsafe override','extra forbidden']"
            )
            self.write_pair(task_path, task, manifest_path, manifest, bind_command=True)
            self.assert_failure(self.invoke(root), "semantic-manifest-mismatch")

    def test_normalizer_drift_is_rejected_after_rebinding(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root, fixtures = self.copied_fixtures(directory)
            task_path, task, manifest_path, manifest = self.load_pair(fixtures)
            command = task["acceptance"]["commands"][0]
            command["argv"][4] = command["argv"][4].replace("s.replace('`','')", "s")
            self.write_pair(task_path, task, manifest_path, manifest, bind_command=True)
            self.assert_failure(self.invoke(root), "normalizer-drift")

    def test_unknown_extra_ast_statement_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root, fixtures = self.copied_fixtures(directory)
            task_path, task, manifest_path, manifest = self.load_pair(fixtures)
            task["acceptance"]["commands"][0]["argv"][4] += "; print('extra')"
            self.write_pair(task_path, task, manifest_path, manifest, bind_command=True)
            self.assert_failure(self.invoke(root), "unsupported-content-gate-grammar")

    def test_missing_isolated_mode_is_rejected_with_fixed_reason(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root, fixtures = self.copied_fixtures(directory)
            task_path, task, manifest_path, manifest = self.load_pair(fixtures)
            task["acceptance"]["commands"][0]["argv"].remove("-I")
            self.write_pair(task_path, task, manifest_path, manifest, bind_command=True)
            self.assert_failure(self.invoke(root), "python-isolation-required")

    def test_local_module_shadow_canary_is_not_loaded_and_gate_still_passes(
        self,
    ) -> None:
        command = json.loads((FIXTURES / "task-spec-r2.json").read_text())["acceptance"]["commands"][0]
        fixture = FIXTURES / "report-positive.md"
        protected_before = VALIDATOR_MODULE.snapshot_protected_roots([REPOSITORY_ROOT])
        self.assertEqual(
            VALIDATOR_MODULE.run_command(
                command,
                "reports/adr-0035.md",
                fixture,
                protected_before,
            ),
            0,
        )

    def test_prompt_required_any_literal_is_mandatory(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root, fixtures = self.copied_fixtures(directory)
            task_path, task, manifest_path, manifest = self.load_pair(fixtures)
            task["work"]["context"][0] = task["work"]["context"][0].replace(
                "required_any[0][1]=`trust boundary`", "required_any[0][1]=`trusted boundary`"
            )
            self.write_pair(task_path, task, manifest_path, manifest)
            self.assert_failure(self.invoke(root), "prompt-literal-unmapped")

    def test_prompt_forbidden_literal_is_mandatory(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root, fixtures = self.copied_fixtures(directory)
            task_path, task, manifest_path, manifest = self.load_pair(fixtures)
            task["work"]["context"][0] = task["work"]["context"][0].replace(
                "forbidden[0]=`unsafe override`", "forbidden[0]=`unsafe bypass`"
            )
            self.write_pair(task_path, task, manifest_path, manifest)
            self.assert_failure(self.invoke(root), "prompt-literal-unmapped")

    def test_prompt_path_count_and_size_literals_are_mandatory(self) -> None:
        markers = (
            "commandPath=`reports/adr-0035.md`",
            "deliverablePath=`reports/adr-0035.md`",
            "minimumDeliverableCount=`1`",
            "minimumLineCount=`4`",
            "maximumBytes=`4096`",
        )
        for marker in markers:
            with self.subTest(marker=marker), tempfile.TemporaryDirectory() as directory:
                root, fixtures = self.copied_fixtures(directory)
                task_path, task, manifest_path, manifest = self.load_pair(fixtures)
                task["work"]["context"][0] = task["work"]["context"][0].replace(marker, "removed-marker")
                self.write_pair(task_path, task, manifest_path, manifest)
                self.assert_failure(self.invoke(root), "prompt-literal-unmapped")

    def test_one_item_equivalence_group_is_schema_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root, fixtures = self.copied_fixtures(directory)
            manifest_path = fixtures / "manifest-r2.json"
            manifest = json.loads(manifest_path.read_text())
            manifest["contentGate"]["required_any"] = [["authority boundary"]]
            manifest_path.write_text(json.dumps(manifest, ensure_ascii=False))
            self.assert_failure(self.invoke(root), "manifest-schema-invalid")

    def test_absolute_content_path_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root, fixtures = self.copied_fixtures(directory)
            task_path, task, manifest_path, manifest = self.load_pair(fixtures)
            command = task["acceptance"]["commands"][0]
            command["argv"][4] = command["argv"][4].replace(
                "Path('reports/adr-0035.md')", "Path('/tmp/adr-0035.md')"
            )
            self.write_pair(task_path, task, manifest_path, manifest, bind_command=True)
            self.assert_failure(self.invoke(root), "path-boundary-invalid")

    def test_parent_traversal_content_path_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root, fixtures = self.copied_fixtures(directory)
            task_path, task, manifest_path, manifest = self.load_pair(fixtures)
            command = task["acceptance"]["commands"][0]
            command["argv"][4] = command["argv"][4].replace(
                "Path('reports/adr-0035.md')", "Path('../adr-0035.md')"
            )
            self.write_pair(task_path, task, manifest_path, manifest, bind_command=True)
            self.assert_failure(self.invoke(root), "path-boundary-invalid")

    def test_python_startup_and_import_reserved_paths_are_rejected(self) -> None:
        cases = (
            (".", "pathlib.py", "pathlib.py"),
            (".", "sitecustomize/report.md", "sitecustomize/report.md"),
            ("unicodedata", "report.md", "unicodedata/report.md"),
            (".", "usercustomize.py", "usercustomize.py"),
        )
        for cwd, command_path, deliverable_path in cases:
            with self.subTest(path=deliverable_path), tempfile.TemporaryDirectory() as directory:
                root, fixtures = self.copied_fixtures(directory)
                task_path, task, manifest_path, manifest = self.load_pair(fixtures)
                command = task["acceptance"]["commands"][0]
                command["cwd"] = cwd
                command["argv"][4] = command["argv"][4].replace(
                    "Path('reports/adr-0035.md')", f"Path({command_path!r})"
                )
                task["deliverables"][0]["pathGlob"] = deliverable_path
                task["work"]["context"][0] = task["work"]["context"][0].replace(
                    "commandPath=`reports/adr-0035.md`", f"commandPath=`{command_path}`"
                ).replace(
                    "deliverablePath=`reports/adr-0035.md`", f"deliverablePath=`{deliverable_path}`"
                )
                manifest["contentGate"]["commandPath"] = command_path
                manifest["contentGate"]["deliverablePath"] = deliverable_path
                for mapping in manifest["prompt_literals"]:
                    if mapping["rule"] == "commandPath":
                        mapping["literal"] = command_path
                    elif mapping["rule"] == "deliverablePath":
                        mapping["literal"] = deliverable_path
                self.write_pair(task_path, task, manifest_path, manifest, bind_command=True)
                self.assert_failure(self.invoke(root), "python-import-shadow-path")

    def test_internal_fixture_symlink_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root, fixtures = self.copied_fixtures(directory)
            fixture = fixtures / "report-positive.md"
            target = fixtures / "report-positive-target.md"
            fixture.rename(target)
            fixture.symlink_to(target.name)
            self.assert_failure(self.invoke(root), "path-symlink-rejected")

    def test_external_fixture_symlink_is_rejected(self) -> None:
        with (
            tempfile.TemporaryDirectory() as directory,
            tempfile.TemporaryDirectory() as external_directory,
        ):
            root, fixtures = self.copied_fixtures(directory)
            fixture = fixtures / "report-positive.md"
            external = Path(external_directory) / "report-positive.md"
            external.write_bytes(fixture.read_bytes())
            fixture.unlink()
            fixture.symlink_to(external)
            self.assert_failure(self.invoke(root), "path-symlink-rejected")

    def test_fixture_chain_swap_to_symlink_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            parent = root / "fixtures"
            parent.mkdir()
            fixture = parent / "report.md"
            fixture.write_text("valid", encoding="utf-8")
            real_parent = root / "fixtures-real"
            original_lstat = Path.lstat
            swapped = False

            def swapping_lstat(path: Path):
                nonlocal swapped
                metadata = original_lstat(path)
                if path == fixture and not swapped:
                    parent.rename(real_parent)
                    parent.symlink_to(real_parent.name, target_is_directory=True)
                    swapped = True
                return metadata

            with mock.patch.object(Path, "lstat", autospec=True, side_effect=swapping_lstat):
                with self.assertRaises(VALIDATOR_MODULE.PreflightError) as raised:
                    VALIDATOR_MODULE.relative_file(root, "fixtures/report.md", "fixture.path")
            self.assertEqual(raised.exception.reason_code, "path-symlink-rejected")

    def test_embedded_protected_root_reference_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root, fixtures = self.copied_fixtures(directory)
            task_path, task, manifest_path, manifest = self.load_pair(fixtures)
            protected_token = str(root.resolve()).casefold()
            command = task["acceptance"]["commands"][0]
            command["argv"][4] = command["argv"][4].replace(
                "['unsafe override']", repr(["unsafe override", protected_token])
            )
            manifest["contentGate"]["forbidden"].append(protected_token)
            manifest["prompt_literals"].append({"rule": "forbidden[1]", "literal": protected_token})
            task["work"]["context"][0] += f"；forbidden[1]=`{protected_token}`。"
            self.write_pair(task_path, task, manifest_path, manifest, bind_command=True)
            self.assert_failure(self.invoke(root), "protected-root-reference")

    def test_external_write_ast_is_rejected_without_executing_it(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root, fixtures = self.copied_fixtures(directory)
            task_path, task, manifest_path, manifest = self.load_pair(fixtures)
            external = root / "external-write"
            task["acceptance"]["commands"][0]["argv"][4] += f"; Path({str(external)!r}).write_text('x')"
            self.write_pair(task_path, task, manifest_path, manifest, bind_command=True)
            self.assert_failure(self.invoke(root), "purity-side-effect-rejected")
            self.assertFalse(external.exists())

    def test_actual_protected_root_change_has_fixed_side_effect_reason(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            protected = Path(directory)
            marker = protected / "marker.txt"
            marker.write_text("before")
            fixture = protected / "fixture.md"
            fixture.write_text("valid")
            before = VALIDATOR_MODULE.snapshot_protected_roots([protected])
            command = json.loads((FIXTURES / "task-spec-r2.json").read_text())["acceptance"]["commands"][0]

            def mutate(*_args, **_kwargs):
                marker.write_text("after")
                return types.SimpleNamespace(returncode=0)

            with mock.patch.object(VALIDATOR_MODULE.subprocess, "run", side_effect=mutate):
                with self.assertRaises(VALIDATOR_MODULE.PreflightError) as raised:
                    VALIDATOR_MODULE.run_command(command, "reports/adr-0035.md", fixture, before)
            self.assertEqual(raised.exception.reason_code, "protected-root-side-effect")

    def test_deleting_required_token_from_positive_fixture_fails(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root, fixtures = self.copied_fixtures(directory)
            report = fixtures / "report-positive.md"
            report.write_text(report.read_text().replace("four-dimensional gate", "dimension checks"))
            manifest_path = fixtures / "manifest-r2.json"
            manifest = json.loads(manifest_path.read_text())
            manifest["fixtures"]["positive"][0]["digest"] = digest(report)
            manifest_path.write_text(json.dumps(manifest, ensure_ascii=False))
            self.assert_failure(self.invoke(root), "positive-fixture-failed")

    def test_forbidden_token_in_positive_fixture_fails(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root, fixtures = self.copied_fixtures(directory)
            report = fixtures / "report-positive.md"
            report.write_text(report.read_text() + "\nunsafe override\n")
            manifest_path = fixtures / "manifest-r2.json"
            manifest = json.loads(manifest_path.read_text())
            manifest["fixtures"]["positive"][0]["digest"] = digest(report)
            manifest_path.write_text(json.dumps(manifest, ensure_ascii=False))
            self.assert_failure(self.invoke(root), "positive-fixture-failed")

    def test_fixture_digest_mismatch_fails_before_execution(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root, fixtures = self.copied_fixtures(directory)
            (fixtures / "report-positive.md").write_text("changed")
            self.assert_failure(self.invoke(root), "fixture-digest-mismatch")

    def test_non_utf8_fixture_has_fixed_reason(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root, fixtures = self.copied_fixtures(directory)
            report = fixtures / "report-positive.md"
            report.write_bytes(b"\xff")
            manifest_path = fixtures / "manifest-r2.json"
            manifest = json.loads(manifest_path.read_text())
            manifest["fixtures"]["positive"][0]["digest"] = digest(report)
            manifest_path.write_text(json.dumps(manifest, ensure_ascii=False))
            self.assert_failure(self.invoke(root), "fixture-unreadable")

    def test_manifest_mutation_is_rejected_by_loaded_schema(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root, fixtures = self.copied_fixtures(directory)
            manifest_path = fixtures / "manifest-r2.json"
            manifest = json.loads(manifest_path.read_text())
            del manifest["command"]["tupleDigest"]
            manifest_path.write_text(json.dumps(manifest, ensure_ascii=False))
            self.assert_failure(self.invoke(root), "manifest-schema-invalid")

    def test_unknown_schema_keyword_fails_closed(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root, fixtures = self.copied_fixtures(directory)
            schema_path = Path(directory) / "schema.json"
            schema = json.loads(SCHEMA.read_text())
            schema["inventedKeyword"] = True
            schema_path.write_text(json.dumps(schema, ensure_ascii=False))
            completed = subprocess.run(
                [
                    sys.executable,
                    "-B",
                    str(VALIDATOR),
                    "--root",
                    str(root),
                    "--schema",
                    str(schema_path),
                    "--manifest",
                    str(fixtures / "manifest-r2.json"),
                    "--task-spec",
                    str(fixtures / "task-spec-r2.json"),
                ],
                check=False,
                capture_output=True,
                text=True,
            )
            self.assert_failure(completed, "schema-document-invalid")

    def test_draft_2020_12_schema_compiles_and_validates_all_instances(self) -> None:
        completed = subprocess.run(
            [
                "go",
                "run",
                str(SCHEMA_PROBE),
                str(SCHEMA),
                str(TEMPLATE),
                str(FIXTURES / "manifest-r1.json"),
                str(FIXTURES / "manifest-r2.json"),
            ],
            cwd=REPOSITORY_ROOT,
            check=False,
            capture_output=True,
            text=True,
        )
        self.assertEqual(completed.returncode, 0, completed.stderr)
        self.assertIn("draft-2020-12-schema-and-instances-ok", completed.stdout)
        template = json.loads(TEMPLATE.read_text())
        command = template["command"]
        command_tuple = {
            key: command[key]
            for key in (
                "id",
                "argv",
                "cwd",
                "timeoutSeconds",
                "required",
                "baselinePolicy",
                "maxLogBytes",
            )
        }
        self.assertEqual(command["argvDigest"], canonical_digest(command["argv"]))
        self.assertEqual(command["tupleDigest"], canonical_digest(command_tuple))


if __name__ == "__main__":
    unittest.main()
