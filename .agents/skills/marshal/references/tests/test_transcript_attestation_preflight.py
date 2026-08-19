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


REPOSITORY_ROOT = Path(__file__).resolve().parents[5]
SKILL_ROOT = REPOSITORY_ROOT / ".agents/skills/marshal"
VALIDATOR = SKILL_ROOT / "references/validate-transcript-attestation-preflight.py"
SCHEMA = SKILL_ROOT / "references/transcript-attestation-preflight.schema.json"
SCHEMA_PROBE = SKILL_ROOT / "references/tests/transcript_attestation_schema_probe.go"
TEMPLATE = SKILL_ROOT / "templates/transcript-attestation-preflight.json"
FIXTURES = SKILL_ROOT / "references/fixtures/transcript-attestation"
R3_RECEIPT = FIXTURES / "mac-qoder-v5-conformance-r3-receipt.json"


def load_validator_module():
    spec = importlib.util.spec_from_file_location("transcript_attestation_preflight", VALIDATOR)
    if spec is None or spec.loader is None:
        raise RuntimeError("cannot load transcript attestation validator")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


VALIDATOR_MODULE = load_validator_module()


def digest(path: Path) -> str:
    return "sha256:" + hashlib.sha256(path.read_bytes()).hexdigest()


class TranscriptAttestationPreflightTest(unittest.TestCase):
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

    def copied_fixtures(self, directory: str) -> Path:
        destination = Path(directory) / "inputs"
        shutil.copytree(FIXTURES, destination)
        return destination

    def invoke(
        self, root: Path, manifest: str = "manifest-positive.json"
    ) -> subprocess.CompletedProcess[str]:
        return subprocess.run(
            [
                sys.executable,
                "-B",
                str(VALIDATOR),
                "--root",
                str(root),
                "--manifest",
                manifest,
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

    def load_manifest(self, root: Path, name: str = "manifest-positive.json") -> tuple[Path, dict]:
        path = root / name
        return path, json.loads(path.read_text())

    def write_json(self, path: Path, value: object) -> None:
        path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n")

    def rebind(self, root: Path, manifest: dict, label: str) -> None:
        descriptor = manifest["inputs"][label]
        descriptor["sha256"] = digest(root / descriptor["path"])

    def rebind_task_and_request(self, root: Path, manifest: dict) -> None:
        self.rebind(root, manifest, "taskSpec")
        request_path = root / manifest["inputs"]["workerRequest"]["path"]
        request = json.loads(request_path.read_text())
        request["specDigest"] = manifest["inputs"]["taskSpec"]["sha256"]
        self.write_json(request_path, request)
        self.rebind(root, manifest, "workerRequest")

    def rewrite_transcript(
        self, root: Path, manifest: dict, events: list[dict], meta: dict
    ) -> None:
        transcript = root / manifest["inputs"]["transcript"]["path"]
        transcript.write_text("".join(json.dumps(event, separators=(",", ":")) + "\n" for event in events))
        meta["capturedBytes"] = transcript.stat().st_size
        meta["eventCount"] = len(events)
        meta_path = root / manifest["inputs"]["transcriptMeta"]["path"]
        self.write_json(meta_path, meta)
        self.rebind(root, manifest, "transcript")
        self.rebind(root, manifest, "transcriptMeta")

    def load_transcript_pair(self, root: Path, manifest: dict) -> tuple[list[dict], dict]:
        transcript = root / manifest["inputs"]["transcript"]["path"]
        events = [json.loads(line) for line in transcript.read_text().splitlines()]
        meta = json.loads((root / manifest["inputs"]["transcriptMeta"]["path"]).read_text())
        return events, meta

    def test_positive_fixture_passes_with_raw_digest_attestation(self) -> None:
        completed = self.invoke(FIXTURES)
        self.assertEqual(completed.returncode, 0, completed.stderr)
        payload = json.loads(completed.stdout)
        self.assertEqual(payload["status"], "pass")
        self.assertEqual(payload["reasonCode"], "transcript-attestation-pass")
        self.assertEqual(payload["observation"]["toolCalls"], 2)
        self.assertEqual(payload["observation"]["commandCalls"], 0)
        self.assertTrue(payload["observation"]["workerResultTeeLast"])
        self.assertRegex(payload["attestationDigest"], r"^sha256:[0-9a-f]{64}$")

    def test_real_r3_receipt_is_sanitized_and_digest_bound(self) -> None:
        receipt = json.loads(R3_RECEIPT.read_text())
        self.assertEqual(receipt["status"], "pass")
        self.assertEqual(receipt["reasonCode"], "transcript-attestation-pass")
        self.assertEqual(
            receipt["attestationDigest"],
            "sha256:145f55f871c2fd46c8ae26eff04238011dc11aad134fa2e504beac5bcfe302ea",
        )
        self.assertEqual(receipt["subject"]["attemptId"], "attempt:c3cf8a35b57dce6b257a229603b0df5d")
        self.assertEqual(receipt["observation"]["commandCalls"], 0)
        self.assertTrue(receipt["observation"]["workerResultTeeLast"])
        serialized = json.dumps(receipt, sort_keys=True)
        for forbidden in ("/Users/", "prompt", "summary", "message", "description"):
            self.assertNotIn(forbidden, serialized)

    def test_skill_requires_attestation_before_independent_reviewer(self) -> None:
        skill = (SKILL_ROOT / "SKILL.md").read_text()
        for required in (
            "Qoder v5 transcript attestation（独立 reviewer 前强制）",
            "validate-transcript-attestation-preflight.py",
            "reasonCode=transcript-attestation-pass",
            "pre-review/operator-local",
            "不替代 Core",
            "protocol-invalid/do-not-retry",
        ):
            self.assertIn(required, skill)

    def test_real_contracts_and_operator_schema_accept_fixtures(self) -> None:
        for schema_name, instance in (
            ("task-spec", FIXTURES / "task-spec.json"),
            ("worker-request", FIXTURES / "worker-request.json"),
            ("worker-result", FIXTURES / "worker-result.json"),
        ):
            completed = subprocess.run(
                [str(self.marshal_binary), "contract", "validate", "--schema", schema_name, str(instance)],
                cwd=REPOSITORY_ROOT,
                check=False,
                capture_output=True,
                text=True,
            )
            self.assertEqual(completed.returncode, 0, completed.stderr)
        completed = subprocess.run(
            [
                "go",
                "run",
                str(SCHEMA_PROBE),
                str(SCHEMA),
                str(TEMPLATE),
                str(FIXTURES / "manifest-positive.json"),
                str(FIXTURES / "manifest-negative-undeclared-wc.json"),
            ],
            cwd=REPOSITORY_ROOT,
            check=False,
            capture_output=True,
            text=True,
        )
        self.assertEqual(completed.returncode, 0, completed.stderr)
        self.assertIn("draft-2020-12-schema-and-instances-ok", completed.stdout)

    def test_qoder_v5_r1_undeclared_wc_fixture_is_rejected(self) -> None:
        completed = self.invoke(FIXTURES, "manifest-negative-undeclared-wc.json")
        self.assert_failure(completed, "forbidden-command-executed")

    def test_bound_command_and_matching_declaration_pass(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = self.copied_fixtures(directory)
            manifest_path, manifest = self.load_manifest(root)
            events, meta = self.load_transcript_pair(root, manifest)
            command = "go test ./internal/example"
            events[3:3] = [
                {
                    "type": "assistant",
                    "message": {
                        "content": [
                            {
                                "type": "tool_use",
                                "id": "test-1",
                                "name": "Bash",
                                "input": {"command": command, "description": "Run focused test"},
                            }
                        ]
                    },
                },
                {
                    "type": "user",
                    "message": {"content": [{"type": "tool_result", "tool_use_id": "test-1", "content": "ok"}]},
                    "tool_use_result": {"kind": "completed", "exitCode": 0, "interrupted": False},
                },
            ]
            declaration = {"commandId": "focused-test", "status": "passed"}
            tee_tool = events[5]["message"]["content"][0]
            lines = tee_tool["input"]["command"].split("\n")
            tee_payload = json.loads("\n".join(lines[1:-1]))
            tee_payload["declaredCommands"] = [declaration]
            tee_tool["input"]["command"] = "\n".join(
                [lines[0], json.dumps(tee_payload, separators=(",", ":")), lines[-1]]
            )
            meta["toolCalls"] = 3
            self.rewrite_transcript(root, manifest, events, meta)

            worker_path = root / manifest["inputs"]["workerResult"]["path"]
            worker = json.loads(worker_path.read_text())
            worker["declaredCommands"] = [declaration]
            self.write_json(worker_path, worker)
            self.rebind(root, manifest, "workerResult")

            task_path = root / manifest["inputs"]["taskSpec"]["path"]
            task = json.loads(task_path.read_text())
            command_constraint = "实际执行的开发或自测命令必须逐项绑定 declaredCommands。"
            task["work"]["constraints"][-1] = command_constraint
            self.write_json(task_path, task)
            self.rebind_task_and_request(root, manifest)

            manifest["policy"]["requiredConstraintLiterals"][-1] = command_constraint
            manifest["policy"]["commandBindings"] = [
                {
                    "toolUseId": "test-1",
                    "commandDigest": "sha256:" + hashlib.sha256(command.encode()).hexdigest(),
                    "commandId": "focused-test",
                }
            ]
            self.write_json(manifest_path, manifest)
            completed = self.invoke(root)
            self.assertEqual(completed.returncode, 0, completed.stderr)
            self.assertEqual(json.loads(completed.stdout)["observation"]["commandCalls"], 1)

    def test_unknown_event_contract_fails_closed(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = self.copied_fixtures(directory)
            manifest_path, manifest = self.load_manifest(root)
            manifest["subject"]["eventContract"] = "qoder-stream-json-unknown"
            self.write_json(manifest_path, manifest)
            self.assert_failure(self.invoke(root), "manifest-schema-invalid")

    def test_unknown_binary_version_fails_closed(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = self.copied_fixtures(directory)
            manifest_path, manifest = self.load_manifest(root)
            manifest["subject"]["binaryVersion"] = "1.1.24"
            self.write_json(manifest_path, manifest)
            self.assert_failure(self.invoke(root), "manifest-schema-invalid")

    def test_duplicate_json_key_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = self.copied_fixtures(directory)
            manifest_path, manifest = self.load_manifest(root)
            transcript = root / manifest["inputs"]["transcript"]["path"]
            raw = transcript.read_bytes().replace(
                b'{"type":"system"', b'{"type":"system","type":"system"', 1
            )
            transcript.write_bytes(raw)
            self.rebind(root, manifest, "transcript")
            self.write_json(manifest_path, manifest)
            self.assert_failure(self.invoke(root), "duplicate-json-key")

    def test_raw_byte_digest_drift_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = self.copied_fixtures(directory)
            transcript = root / "transcript-positive.jsonl"
            transcript.write_bytes(transcript.read_bytes() + b" ")
            self.assert_failure(self.invoke(root), "input-digest-mismatch")

    def test_symlinked_input_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = self.copied_fixtures(directory)
            transcript = root / "transcript-positive.jsonl"
            target = root / "transcript-target.jsonl"
            transcript.rename(target)
            transcript.symlink_to(target.name)
            self.assert_failure(self.invoke(root), "input-path-invalid")

    def test_fifo_input_is_rejected_without_blocking(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = self.copied_fixtures(directory)
            transcript = root / "transcript-positive.jsonl"
            transcript.unlink()
            os.mkfifo(transcript)
            completed = self.invoke(root)
            self.assert_failure(completed, "input-file-invalid")

    def test_input_bound_smaller_than_regular_file_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = self.copied_fixtures(directory)
            manifest_path, manifest = self.load_manifest(root)
            manifest["inputs"]["transcript"]["maxBytes"] = 64
            self.write_json(manifest_path, manifest)
            self.assert_failure(self.invoke(root), "input-too-large")

    def test_symlinked_parent_component_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = self.copied_fixtures(directory)
            manifest_path, manifest = self.load_manifest(root)
            real = root / "real"
            real.mkdir()
            transcript = root / "transcript-positive.jsonl"
            transcript.rename(real / transcript.name)
            (root / "linked").symlink_to(real.name, target_is_directory=True)
            manifest["inputs"]["transcript"]["path"] = "linked/transcript-positive.jsonl"
            self.write_json(manifest_path, manifest)
            self.assert_failure(self.invoke(root), "input-path-invalid")

    def test_parent_swap_during_read_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            parent = root / "parent"
            parent.mkdir()
            (parent / "input.json").write_bytes(b'{"original":true}\n')
            real_read = VALIDATOR_MODULE.os.read
            swapped = False

            def swap_then_read(descriptor: int, size: int) -> bytes:
                nonlocal swapped
                if not swapped:
                    swapped = True
                    parent.rename(root / "held-parent")
                    parent.mkdir()
                    (parent / "input.json").write_bytes(b'{"attacker":true}\n')
                return real_read(descriptor, size)

            VALIDATOR_MODULE.os.read = swap_then_read
            try:
                with self.assertRaises(VALIDATOR_MODULE.PreflightError) as raised:
                    VALIDATOR_MODULE.read_relative_nofollow(
                        root, "parent/input.json", 1024, "parent-swap-fixture"
                    )
            finally:
                VALIDATOR_MODULE.os.read = real_read
            self.assertEqual(raised.exception.reason_code, "input-changed-during-read")

    def test_forbidden_tool_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = self.copied_fixtures(directory)
            manifest_path, manifest = self.load_manifest(root)
            events, meta = self.load_transcript_pair(root, manifest)
            events[3:3] = [
                {
                    "type": "assistant",
                    "message": {
                        "content": [
                            {"type": "tool_use", "id": "read-1", "name": "Read", "input": {"file_path": "report.md"}}
                        ]
                    },
                },
                {"type": "user", "message": {"content": [{"type": "tool_result", "tool_use_id": "read-1", "content": "fixture"}]}},
            ]
            meta["toolCalls"] = 3
            meta["toolNames"] = ["bash", "read", "write"]
            self.rewrite_transcript(root, manifest, events, meta)
            self.write_json(manifest_path, manifest)
            self.assert_failure(self.invoke(root), "forbidden-tool-executed")

    def test_taskspec_tool_allowlist_mismatch_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = self.copied_fixtures(directory)
            manifest_path, manifest = self.load_manifest(root)
            task_path = root / manifest["inputs"]["taskSpec"]["path"]
            task = json.loads(task_path.read_text())
            task["worker"]["tools"].remove("write")
            self.write_json(task_path, task)
            self.rebind_task_and_request(root, manifest)
            self.write_json(manifest_path, manifest)
            self.assert_failure(self.invoke(root), "task-tool-policy-mismatch")

    def test_orphan_tool_result_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = self.copied_fixtures(directory)
            manifest_path, manifest = self.load_manifest(root)
            events, meta = self.load_transcript_pair(root, manifest)
            events[-1:-1] = [
                {
                    "type": "user",
                    "message": {
                        "content": [
                            {"type": "tool_result", "tool_use_id": "missing-tool", "content": "orphan"}
                        ]
                    },
                }
            ]
            self.rewrite_transcript(root, manifest, events, meta)
            self.write_json(manifest_path, manifest)
            self.assert_failure(self.invoke(root), "transcript-invalid")

    def test_post_result_tool_use_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = self.copied_fixtures(directory)
            manifest_path, manifest = self.load_manifest(root)
            events, meta = self.load_transcript_pair(root, manifest)
            events[-1:-1] = [
                {
                    "type": "assistant",
                    "message": {
                        "content": [
                            {"type": "tool_use", "id": "write-after", "name": "Write", "input": {"file_path": "report.md", "content": "changed"}}
                        ]
                    },
                },
                {"type": "user", "message": {"content": [{"type": "tool_result", "tool_use_id": "write-after", "content": "updated"}]}},
            ]
            meta["toolCalls"] = 3
            meta["workerResultTeeLast"] = False
            self.rewrite_transcript(root, manifest, events, meta)
            self.write_json(manifest_path, manifest)
            self.assert_failure(self.invoke(root), "post-result-tool-use")

    def test_duplicate_result_tee_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = self.copied_fixtures(directory)
            manifest_path, manifest = self.load_manifest(root)
            events, meta = self.load_transcript_pair(root, manifest)
            duplicate_use = json.loads(json.dumps(events[3]))
            duplicate_use["message"]["content"][0]["id"] = "tee-2"
            duplicate_result = json.loads(json.dumps(events[4]))
            duplicate_result["message"]["content"][0]["tool_use_id"] = "tee-2"
            events[-1:-1] = [duplicate_use, duplicate_result]
            meta["toolCalls"] = 3
            meta["workerResultTeeAttempts"] = 2
            meta["workerResultTeeSuccesses"] = 2
            self.rewrite_transcript(root, manifest, events, meta)
            self.write_json(manifest_path, manifest)
            self.assert_failure(self.invoke(root), "result-tee-count-invalid")

    def test_failed_result_tee_followed_by_second_tee_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = self.copied_fixtures(directory)
            manifest_path, manifest = self.load_manifest(root)
            events, meta = self.load_transcript_pair(root, manifest)
            events[4]["tool_use_result"]["exitCode"] = 1
            duplicate_use = json.loads(json.dumps(events[3]))
            duplicate_use["message"]["content"][0]["id"] = "tee-2"
            duplicate_result = json.loads(json.dumps(events[4]))
            duplicate_result["message"]["content"][0]["tool_use_id"] = "tee-2"
            duplicate_result["tool_use_result"]["exitCode"] = 0
            events[-1:-1] = [duplicate_use, duplicate_result]
            meta["toolCalls"] = 3
            meta["workerResultTeeAttempts"] = 2
            meta["workerResultTeeSuccesses"] = 1
            self.rewrite_transcript(root, manifest, events, meta)
            self.write_json(manifest_path, manifest)
            self.assert_failure(self.invoke(root), "result-tee-count-invalid")

    def test_noncanonical_result_tee_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = self.copied_fixtures(directory)
            manifest_path, manifest = self.load_manifest(root)
            events, meta = self.load_transcript_pair(root, manifest)
            tool = events[3]["message"]["content"][0]
            tool["input"]["command"] += "\n"
            self.rewrite_transcript(root, manifest, events, meta)
            self.write_json(manifest_path, manifest)
            self.assert_failure(self.invoke(root), "transport-command-invalid")

    def test_result_tee_metadata_drift_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = self.copied_fixtures(directory)
            manifest_path, manifest = self.load_manifest(root)
            events, meta = self.load_transcript_pair(root, manifest)
            events[3]["message"]["content"][0]["input"]["timeout"] = 30
            self.rewrite_transcript(root, manifest, events, meta)
            self.write_json(manifest_path, manifest)
            self.assert_failure(self.invoke(root), "transport-command-invalid")

    def test_worker_result_declaration_drift_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = self.copied_fixtures(directory)
            manifest_path, manifest = self.load_manifest(root)
            worker_path = root / manifest["inputs"]["workerResult"]["path"]
            worker = json.loads(worker_path.read_text())
            worker["declaredCommands"] = [{"commandId": "hidden-check", "status": "passed"}]
            self.write_json(worker_path, worker)
            self.rebind(root, manifest, "workerResult")
            self.write_json(manifest_path, manifest)
            self.assert_failure(self.invoke(root), "declared-command-mismatch")

    def test_worker_result_declared_commands_omission_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = self.copied_fixtures(directory)
            manifest_path, manifest = self.load_manifest(root)
            worker_path = root / manifest["inputs"]["workerResult"]["path"]
            worker = json.loads(worker_path.read_text())
            del worker["declaredCommands"]
            self.write_json(worker_path, worker)
            self.rebind(root, manifest, "workerResult")
            self.write_json(manifest_path, manifest)
            self.assert_failure(self.invoke(root), "worker-result-invalid")

    def test_worker_result_identity_drift_is_rejected(self) -> None:
        for field, replacement in (
            ("taskId", "TA-OTHER"),
            ("runId", "ta-other-r1"),
            ("attemptId", "attempt:other"),
        ):
            with self.subTest(field=field), tempfile.TemporaryDirectory() as directory:
                root = self.copied_fixtures(directory)
                manifest_path, manifest = self.load_manifest(root)
                worker_path = root / manifest["inputs"]["workerResult"]["path"]
                worker = json.loads(worker_path.read_text())
                worker[field] = replacement
                self.write_json(worker_path, worker)
                self.rebind(root, manifest, "workerResult")
                self.write_json(manifest_path, manifest)
                self.assert_failure(self.invoke(root), "subject-mismatch")

    def test_two_declared_commands_in_reverse_order_are_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = self.copied_fixtures(directory)
            manifest_path, manifest = self.load_manifest(root)
            events, meta = self.load_transcript_pair(root, manifest)
            commands = [("cmd-1", "go test ./a", "test-a"), ("cmd-2", "go test ./b", "test-b")]
            injected: list[dict] = []
            for tool_id, command, _ in commands:
                injected.extend(
                    [
                        {
                            "type": "assistant",
                            "message": {
                                "content": [
                                    {
                                        "type": "tool_use",
                                        "id": tool_id,
                                        "name": "Bash",
                                        "input": {"command": command, "description": "Run focused test"},
                                    }
                                ]
                            },
                        },
                        {
                            "type": "user",
                            "message": {
                                "content": [{"type": "tool_result", "tool_use_id": tool_id, "content": "ok"}]
                            },
                            "tool_use_result": {"kind": "completed", "exitCode": 0, "interrupted": False},
                        },
                    ]
                )
            events[3:3] = injected
            declarations = [
                {"commandId": command_id, "status": "passed"} for _, _, command_id in reversed(commands)
            ]
            tee_tool = events[7]["message"]["content"][0]
            lines = tee_tool["input"]["command"].split("\n")
            tee_payload = json.loads("\n".join(lines[1:-1]))
            tee_payload["declaredCommands"] = declarations
            tee_tool["input"]["command"] = "\n".join(
                [lines[0], json.dumps(tee_payload, separators=(",", ":")), lines[-1]]
            )
            meta["toolCalls"] = 4
            self.rewrite_transcript(root, manifest, events, meta)

            worker_path = root / manifest["inputs"]["workerResult"]["path"]
            worker = json.loads(worker_path.read_text())
            worker["declaredCommands"] = declarations
            self.write_json(worker_path, worker)
            self.rebind(root, manifest, "workerResult")

            task_path = root / manifest["inputs"]["taskSpec"]["path"]
            task = json.loads(task_path.read_text())
            command_constraint = "实际执行的开发或自测命令必须逐项绑定 declaredCommands。"
            task["work"]["constraints"][-1] = command_constraint
            self.write_json(task_path, task)
            self.rebind_task_and_request(root, manifest)
            manifest["policy"]["requiredConstraintLiterals"][-1] = command_constraint
            manifest["policy"]["commandBindings"] = [
                {
                    "toolUseId": tool_id,
                    "commandDigest": "sha256:" + hashlib.sha256(command.encode()).hexdigest(),
                    "commandId": command_id,
                }
                for tool_id, command, command_id in commands
            ]
            self.write_json(manifest_path, manifest)
            self.assert_failure(self.invoke(root), "declared-command-mismatch")

    def test_worker_request_source_head_drift_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = self.copied_fixtures(directory)
            manifest_path, manifest = self.load_manifest(root)
            request_path = root / manifest["inputs"]["workerRequest"]["path"]
            request = json.loads(request_path.read_text())
            request["baseSha"] = "2222222222222222222222222222222222222222"
            self.write_json(request_path, request)
            self.rebind(root, manifest, "workerRequest")
            self.write_json(manifest_path, manifest)
            self.assert_failure(self.invoke(root), "source-head-mismatch")

    def test_missing_exact_taskspec_constraint_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = self.copied_fixtures(directory)
            manifest_path, manifest = self.load_manifest(root)
            task_path = root / manifest["inputs"]["taskSpec"]["path"]
            task = json.loads(task_path.read_text())
            task["work"]["constraints"].pop()
            self.write_json(task_path, task)
            self.rebind_task_and_request(root, manifest)
            self.write_json(manifest_path, manifest)
            self.assert_failure(self.invoke(root), "task-constraint-mismatch")

    def test_transcript_metadata_drift_is_rejected_even_when_rebound(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = self.copied_fixtures(directory)
            manifest_path, manifest = self.load_manifest(root)
            meta_path = root / manifest["inputs"]["transcriptMeta"]["path"]
            meta = json.loads(meta_path.read_text())
            meta["capturedBytes"] += 1
            self.write_json(meta_path, meta)
            self.rebind(root, manifest, "transcriptMeta")
            self.write_json(manifest_path, manifest)
            self.assert_failure(self.invoke(root), "transcript-meta-mismatch")


if __name__ == "__main__":
    unittest.main()
