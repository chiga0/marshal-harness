from __future__ import annotations

import hashlib
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest import mock


REPOSITORY_ROOT = Path(__file__).resolve().parents[5]
SKILL_ROOT = REPOSITORY_ROOT / ".agents/skills/marshal"
VALIDATOR = SKILL_ROOT / "references/validate-codex-provider-schema-preflight.py"
PROFILE = SKILL_ROOT / "references/codex-0.145-provider-schema-profile.json"
RECEIPT_SCHEMA = SKILL_ROOT / "references/codex-provider-schema-preflight-receipt.schema.json"
RECEIPT_PROBE = SKILL_ROOT / "references/tests/codex_provider_schema_receipt_probe.go"
TEMPLATE = SKILL_ROOT / "templates/codex-provider-schema-preflight-receipt.json"
FIXTURE_ROOT = SKILL_ROOT / "references/fixtures/codex-provider-schema"
VALID = FIXTURE_ROOT / "valid-r16-provider-schema.json"
INVALID_AGGREGATE = FIXTURE_ROOT / "invalid-aggregate-r1-r10.json"


def load_validator_module():
    spec = importlib.util.spec_from_file_location("codex_provider_schema_preflight", VALIDATOR)
    if spec is None or spec.loader is None:
        raise RuntimeError("cannot load validator")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


VALIDATOR_MODULE = load_validator_module()


class CodexProviderSchemaPreflightTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.checker_directory = tempfile.TemporaryDirectory()
        cls.marshal = (Path(cls.checker_directory.name) / "marshal").resolve()
        cls.receipt_probe = Path(cls.checker_directory.name) / "codex-provider-schema-receipt-probe"
        commit = subprocess.run(["git", "rev-parse", "HEAD"], cwd=REPOSITORY_ROOT, check=True, text=True, stdout=subprocess.PIPE).stdout.strip()
        completed = subprocess.run(
            ["go", "build", "-ldflags", f"-X github.com/chiga0/marshal-harness/internal/buildinfo.commit={commit}", "-o", str(cls.marshal), "./cmd/marshal"],
            cwd=REPOSITORY_ROOT,
            check=False,
            capture_output=True,
            text=True,
            timeout=120,
        )
        if completed.returncode != 0:
            raise RuntimeError(completed.stderr)
        completed = subprocess.run(
            ["go", "build", "-o", str(cls.receipt_probe), str(RECEIPT_PROBE)],
            cwd=REPOSITORY_ROOT,
            check=False,
            capture_output=True,
            text=True,
            timeout=120,
        )
        if completed.returncode != 0:
            raise RuntimeError(completed.stderr)

    @classmethod
    def tearDownClass(cls) -> None:
        cls.checker_directory.cleanup()

    def invoke(
        self,
        root: Path,
        schema: str,
        profile: str = ".agents/skills/marshal/references/codex-0.145-provider-schema-profile.json",
    ) -> subprocess.CompletedProcess[str]:
        return subprocess.run(
            [
                sys.executable,
                "-I",
                "-B",
                str(VALIDATOR),
                "--root",
                str(root),
                "--profile",
                profile,
                "--schema",
                schema,
                "--marshal",
                str(self.marshal),
            ],
            cwd=REPOSITORY_ROOT,
            check=False,
            capture_output=True,
            text=True,
            timeout=30,
        )

    def test_fixed_marshal_internal_command_passes(self) -> None:
        relative = VALID.relative_to(REPOSITORY_ROOT).as_posix()
        completed = self.invoke(REPOSITORY_ROOT, relative)
        self.assertEqual(completed.returncode, 0, completed.stderr)
        receipt = json.loads(completed.stdout)
        self.assertEqual(receipt["reasonCode"], "codex-provider-schema-compatible")

    def assert_fatal(self, completed: subprocess.CompletedProcess[str], reason: str) -> dict:
        self.assertEqual(completed.returncode, 2, completed.stdout + completed.stderr)
        payload = json.loads(completed.stderr)
        self.assertEqual(payload["status"], "fail")
        self.assertEqual(payload["reasonCode"], reason)
        return payload

    def temporary_inputs(self, directory: str) -> Path:
        root = Path(directory)
        shutil.copyfile(PROFILE, root / "profile.json")
        shutil.copyfile(VALID, root / "schema.json")
        return root

    def test_real_r16_provider_schema_passes_with_bound_profile_and_raw_digest(self) -> None:
        relative = VALID.relative_to(REPOSITORY_ROOT).as_posix()
        completed = self.invoke(REPOSITORY_ROOT, relative)
        self.assertEqual(completed.returncode, 0, completed.stderr)
        receipt = json.loads(completed.stdout)
        self.assertEqual(receipt["status"], "pass")
        self.assertEqual(receipt["reasonCode"], "codex-provider-schema-compatible")
        self.assertEqual(receipt["adapterId"], "codex")
        self.assertEqual(receipt["cliCompatibilityLine"], "0.145.x")
        self.assertEqual(receipt["authorityScope"], "mac-ordinary-user-operator-local")
        self.assertEqual(receipt["authorityClaim"], "none")
        self.assertEqual(
            receipt["schema"]["rawDigest"],
            "sha256:" + hashlib.sha256(VALID.read_bytes()).hexdigest(),
        )
        self.assertEqual(
            receipt["profileDigest"],
            "sha256:" + hashlib.sha256(PROFILE.read_bytes()).hexdigest(),
        )
        self.assertTrue(receipt["schema"]["nofollow"])
        self.assertTrue(receipt["schema"]["boundedRead"])
        self.assertEqual(receipt["issueCount"], 0)
        self.assertEqual(receipt["issues"], [])

    def test_one_invocation_aggregates_every_r1_to_r10_schema_failure(self) -> None:
        relative = INVALID_AGGREGATE.relative_to(REPOSITORY_ROOT).as_posix()
        first = self.invoke(REPOSITORY_ROOT, relative)
        second = self.invoke(REPOSITORY_ROOT, relative)
        self.assertEqual(first.returncode, 1, first.stdout + first.stderr)
        self.assertEqual(first.stderr, second.stderr)
        receipt = json.loads(first.stderr)
        self.assertEqual(receipt["reasonCode"], "codex-provider-schema-incompatible")
        expected = [
            {"code": "unsupported-keyword", "jsonPointer": "/not", "keyword": "not"},
            {"code": "missing-type", "jsonPointer": "/not", "keyword": "type"},
            {"code": "missing-type", "jsonPointer": "/not/anyOf/0", "keyword": "type"},
            {"code": "unsupported-keyword", "jsonPointer": "/not/anyOf/0/pattern", "keyword": "pattern"},
            {"code": "required-properties-mismatch", "jsonPointer": "/properties/adapter/required", "keyword": "required"},
            {"code": "missing-type", "jsonPointer": "/properties/apiVersion", "keyword": "type"},
            {"code": "unsupported-keyword", "jsonPointer": "/properties/apiVersion/const", "keyword": "const"},
            {"code": "unsupported-keyword", "jsonPointer": "/properties/declaredArtifacts/items/oneOf", "keyword": "oneOf"},
            {"code": "unsupported-keyword", "jsonPointer": "/properties/declaredArtifacts/items/properties/uri/format", "keyword": "format"},
            {"code": "unsupported-keyword", "jsonPointer": "/properties/declaredChangedFiles/uniqueItems", "keyword": "uniqueItems"},
        ]
        expected.sort(key=lambda issue: (issue["jsonPointer"], issue["code"], issue["keyword"]))
        self.assertEqual(receipt["issues"], expected)
        self.assertEqual(receipt["issueCount"], len(expected))

    def test_enum_rejects_numerically_equivalent_nested_values(self) -> None:
        schemas = (
            '{"type":"number","enum":[1,1.0,1e0]}',
            '{"type":"array","items":{"type":"number"},'
            '"enum":[[1,{"a":2.0,"b":[3]}],[1.0,{"b":[3e0],"a":2e0}]]}',
        )
        for schema in schemas:
            with self.subTest(schema=schema), tempfile.TemporaryDirectory() as directory:
                root = self.temporary_inputs(directory)
                (root / "schema.json").write_text(schema)
                completed = self.invoke(root, "schema.json", "profile.json")
                self.assertEqual(completed.returncode, 1, completed.stderr)
                receipt = json.loads(completed.stderr)
                self.assertEqual(
                    receipt["issues"],
                    [
                        {
                            "code": "enum-shape-invalid",
                            "jsonPointer": "/enum",
                            "keyword": "enum",
                        }
                    ],
                )

    def test_unknown_keyword_fails_closed(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = self.temporary_inputs(directory)
            schema = json.loads((root / "schema.json").read_text())
            schema["unevaluatedProperties"] = False
            (root / "schema.json").write_text(json.dumps(schema))
            completed = self.invoke(root, "schema.json", "profile.json")
            self.assertEqual(completed.returncode, 1, completed.stderr)
            receipt = json.loads(completed.stderr)
            self.assertIn(
                {
                    "code": "unknown-keyword",
                    "jsonPointer": "/unevaluatedProperties",
                    "keyword": "unevaluatedProperties",
                },
                receipt["issues"],
            )

    def test_ambiguous_and_nonfinite_json_is_rejected_before_schema_walk(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            shutil.copyfile(PROFILE, root / "profile.json")
            for raw in (
                '{"type":"object","type":"object"}',
                '{"type":"number","default":NaN}',
                '{"type":"number","default":Infinity}',
                '{"type":"number","default":-Infinity}',
                '{"type":"number","default":1e9999}',
            ):
                with self.subTest(raw=raw):
                    (root / "schema.json").write_text(raw)
                    completed = self.invoke(root, "schema.json", "profile.json")
                    self.assert_fatal(completed, "codex-provider-schema-json-invalid")

    def test_schema_symlink_is_rejected_by_nofollow_open(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            shutil.copyfile(PROFILE, root / "profile.json")
            shutil.copyfile(VALID, root / "target.json")
            (root / "schema.json").symlink_to("target.json")
            completed = self.invoke(root, "schema.json", "profile.json")
            self.assert_fatal(completed, "codex-provider-schema-unreadable")

    @unittest.skipUnless(hasattr(os, "mkfifo"), "FIFO requires POSIX")
    def test_fifo_is_rejected_without_blocking(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            shutil.copyfile(PROFILE, root / "profile.json")
            os.mkfifo(root / "schema.json")
            completed = self.invoke(root, "schema.json", "profile.json")
            self.assert_fatal(completed, "codex-provider-schema-unreadable")

    def test_oversize_schema_is_rejected_before_reading_contents(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            shutil.copyfile(PROFILE, root / "profile.json")
            with (root / "schema.json").open("wb") as output:
                output.truncate(4 * 1024 * 1024 + 1)
            completed = self.invoke(root, "schema.json", "profile.json")
            self.assert_fatal(completed, "codex-provider-schema-too-large")

    def test_parent_swap_is_detected_after_held_dirfd_read(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            parent = root / "parent"
            parent.mkdir()
            shutil.copyfile(VALID, parent / "schema.json")
            root_fd = VALIDATOR_MODULE.open_root(root)
            real_read = os.read
            swapped = False

            def swap_then_read(file_descriptor: int, size: int) -> bytes:
                nonlocal swapped
                if not swapped:
                    swapped = True
                    parent.rename(root / "parent-old")
                    parent.mkdir()
                    shutil.copyfile(INVALID_AGGREGATE, parent / "schema.json")
                return real_read(file_descriptor, size)

            try:
                with mock.patch.object(VALIDATOR_MODULE.os, "read", side_effect=swap_then_read):
                    with self.assertRaises(VALIDATOR_MODULE.PreflightError) as captured:
                        VALIDATOR_MODULE.read_regular_file_at(
                            root_fd,
                            "parent/schema.json",
                            4 * 1024 * 1024,
                            path_reason="path",
                            unreadable_reason="unreadable",
                            too_large_reason="too-large",
                            changed_reason="changed",
                        )
                self.assertEqual(captured.exception.reason_code, "changed")
            finally:
                os.close(root_fd)

    def test_leaf_replacement_and_growth_are_detected(self) -> None:
        for mode in ("replace", "grow"):
            with self.subTest(mode=mode), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                shutil.copyfile(VALID, root / "schema.json")
                root_fd = VALIDATOR_MODULE.open_root(root)
                real_read = os.read
                changed = False

                def change_then_read(file_descriptor: int, size: int) -> bytes:
                    nonlocal changed
                    if not changed:
                        changed = True
                        if mode == "replace":
                            (root / "schema.json").rename(root / "schema-old.json")
                            shutil.copyfile(INVALID_AGGREGATE, root / "schema.json")
                        else:
                            with (root / "schema.json").open("ab") as output:
                                output.write(b" ")
                    return real_read(file_descriptor, size)

                try:
                    with mock.patch.object(VALIDATOR_MODULE.os, "read", side_effect=change_then_read):
                        with self.assertRaises(VALIDATOR_MODULE.PreflightError) as captured:
                            VALIDATOR_MODULE.read_regular_file_at(
                                root_fd, "schema.json", 4 * 1024 * 1024,
                                path_reason="path", unreadable_reason="unreadable",
                                too_large_reason="too-large", changed_reason="changed",
                            )
                    self.assertEqual(captured.exception.reason_code, "changed")
                finally:
                    os.close(root_fd)

    def test_profile_parent_fifo_symlink_and_oversize_fail_closed(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "profiles").mkdir()
            shutil.copyfile(PROFILE, root / "profiles" / "profile.json")
            shutil.copyfile(VALID, root / "schema.json")
            root_fd = VALIDATOR_MODULE.open_root(root)
            real_read = os.read
            swapped = False

            def swap_profile_parent(file_descriptor: int, size: int) -> bytes:
                nonlocal swapped
                if not swapped:
                    swapped = True
                    (root / "profiles").rename(root / "profiles-old")
                    (root / "profiles").mkdir()
                    shutil.copyfile(PROFILE, root / "profiles" / "profile.json")
                return real_read(file_descriptor, size)

            try:
                with mock.patch.object(VALIDATOR_MODULE.os, "read", side_effect=swap_profile_parent):
                    with self.assertRaises(VALIDATOR_MODULE.PreflightError) as captured:
                        VALIDATOR_MODULE.read_regular_file_at(
                            root_fd, "profiles/profile.json", 64 * 1024,
                            path_reason="path", unreadable_reason="unreadable",
                            too_large_reason="too-large", changed_reason="changed",
                        )
                self.assertEqual(captured.exception.reason_code, "changed")
            finally:
                os.close(root_fd)

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            shutil.copyfile(VALID, root / "schema.json")
            shutil.copyfile(PROFILE, root / "target.json")
            (root / "profile.json").symlink_to("target.json")
            self.assert_fatal(self.invoke(root, "schema.json", "profile.json"), "codex-provider-profile-unreadable")

        if hasattr(os, "mkfifo"):
            with tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                shutil.copyfile(VALID, root / "schema.json")
                os.mkfifo(root / "profile.json")
                self.assert_fatal(self.invoke(root, "schema.json", "profile.json"), "codex-provider-profile-unreadable")

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            shutil.copyfile(VALID, root / "schema.json")
            with (root / "profile.json").open("wb") as output:
                output.truncate(64 * 1024 + 1)
            self.assert_fatal(self.invoke(root, "schema.json", "profile.json"), "codex-provider-profile-too-large")

    def test_profile_rule_drift_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = self.temporary_inputs(directory)
            profile = json.loads((root / "profile.json").read_text())
            profile["allowedKeywords"].append("description")
            (root / "profile.json").write_text(json.dumps(profile))
            completed = self.invoke(root, "schema.json", "profile.json")
            self.assert_fatal(completed, "codex-provider-profile-invalid")

    def test_absolute_and_parent_escape_schema_paths_are_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = self.temporary_inputs(directory)
            for path in (str(root / "schema.json"), "../schema.json", "./schema.json"):
                with self.subTest(path=path):
                    completed = self.invoke(root, path, "profile.json")
                    self.assert_fatal(completed, "codex-provider-schema-path-invalid")

    def test_symlink_root_fails_with_stable_reason_instead_of_traceback(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            parent = Path(directory)
            real_root = parent / "real"
            real_root.mkdir()
            link_root = parent / "link"
            link_root.symlink_to(real_root, target_is_directory=True)
            completed = self.invoke(link_root, "schema.json", "profile.json")
            self.assert_fatal(completed, "codex-provider-preflight-root-invalid")

    def test_receipt_schema_compiles_and_validates_template_and_real_receipts(self) -> None:
        valid_relative = VALID.relative_to(REPOSITORY_ROOT).as_posix()
        invalid_relative = INVALID_AGGREGATE.relative_to(REPOSITORY_ROOT).as_posix()
        passed = self.invoke(REPOSITORY_ROOT, valid_relative)
        failed = self.invoke(REPOSITORY_ROOT, invalid_relative)
        self.assertEqual(passed.returncode, 0, passed.stderr)
        self.assertEqual(failed.returncode, 1, failed.stdout + failed.stderr)
        with tempfile.TemporaryDirectory() as directory:
            pass_receipt = Path(directory) / "pass.json"
            fail_receipt = Path(directory) / "fail.json"
            pass_receipt.write_text(passed.stdout)
            fail_receipt.write_text(failed.stderr)
            completed = subprocess.run(
                [
                    str(self.receipt_probe),
                    str(RECEIPT_SCHEMA),
                    str(TEMPLATE),
                    str(pass_receipt),
                    str(fail_receipt),
                ],
                cwd=REPOSITORY_ROOT,
                check=False,
                capture_output=True,
                text=True,
                timeout=120,
            )
        self.assertEqual(completed.returncode, 0, completed.stderr)
        self.assertIn("draft-2020-12-receipt-schema-and-instances-ok", completed.stdout)

        invalid_paths = ("./schema.json", "../schema.json", "/schema.json", "a//schema.json", "a\\schema.json")
        for invalid_path in invalid_paths:
            with self.subTest(invalid_path=invalid_path), tempfile.TemporaryDirectory() as directory:
                receipt = json.loads(passed.stdout)
                receipt["schema"]["path"] = invalid_path
                invalid = Path(directory) / "invalid-receipt.json"
                invalid.write_text(json.dumps(receipt))
                rejected = subprocess.run(
                    [str(self.receipt_probe), str(RECEIPT_SCHEMA), str(invalid)],
                    cwd=REPOSITORY_ROOT, check=False, capture_output=True, text=True, timeout=120,
                )
                self.assertNotEqual(rejected.returncode, 0, invalid_path)


if __name__ == "__main__":
    unittest.main()
