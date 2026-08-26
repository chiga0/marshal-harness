from __future__ import annotations

import copy
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest import mock


REFERENCES = Path(__file__).resolve().parents[1]
REPOSITORY = REFERENCES.parents[3]
# macOS host security policies treat every random executable path as a new
# identity. Keep test helpers at one stable, gitignored path and permit local
# runs to reuse an already-approved fixed Marshal binary.
TEST_BUILD_ROOT = REPOSITORY / "bin" / "test" / "marshal-plan-premortem"
VALIDATOR = REFERENCES / "validate-plan-premortem-preflight.py"
SCHEMA = REFERENCES / "plan-premortem-preflight.schema.json"
TEMPLATE = REFERENCES.parent / "templates" / "plan-premortem-preflight.json"


def compact(value: object) -> bytes:
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode()


def digest(data: bytes) -> str:
    return "sha256:" + hashlib.sha256(data).hexdigest()


def run(command: list[str], cwd: Path) -> str:
    result = subprocess.run(command, cwd=cwd, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=True)
    return result.stdout.strip()


class PlanPremortemPreflightTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        TEST_BUILD_ROOT.mkdir(parents=True, exist_ok=True)
        cls.build = TEST_BUILD_ROOT.resolve()
        configured = os.environ.get("MARSHAL_TEST_BINARY")
        if configured:
            cls.marshal = Path(configured).resolve()
        else:
            cls.marshal = cls.build / "marshal"
            commit = run(["git", "rev-parse", "HEAD"], REPOSITORY)
            subprocess.run(
                ["go", "build", "-ldflags", f"-X github.com/chiga0/marshal-harness/internal/buildinfo.commit={commit}", "-o", str(cls.marshal), "./cmd/marshal"],
                cwd=REPOSITORY,
                check=True,
            )

    @classmethod
    def tearDownClass(cls) -> None:
        pass

    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory(prefix="marshal-plan-premortem-test.")
        self.root = Path(self.temporary.name).resolve()
        self.repository = self.root / "repository"
        self.operator = self.root / "operator"
        self.repository.mkdir()
        self.operator.mkdir()
        run(["git", "init", "-q"], self.repository)
        run(["git", "config", "user.name", "Marshal Test"], self.repository)
        run(["git", "config", "user.email", "marshal@example.invalid"], self.repository)
        (self.repository / "reports").mkdir()
        (self.repository / "reports" / ".keep").write_text("tracked\n", encoding="utf-8")
        run(["git", "add", "reports/.keep"], self.repository)
        run(["git", "commit", "-qm", "base"], self.repository)
        self.source_head = run(["git", "rev-parse", "HEAD"], self.repository)
        self.worker_marker = self.root / "worker-launched"
        self.core_probe_marker = self.root / "core-probe-launched"
        self.environment_path = os.environ.get("PATH", "")
        self.home = self.root / "home"
        (self.home / ".qwen").mkdir(parents=True)
        (self.home / ".qwen" / "settings.json").write_text(
            json.dumps({"security": {"auth": {"selectedType": "qwen-oauth"}}}),
            encoding="utf-8",
        )
        self.environment_home = str(self.home)
        self.qoder = self.fake_executable("qoder", "1.1.27")
        self.codex = self.fake_executable("codex", "codex-cli 0.145.0")
        self.qwen = self.fake_node_executable("qwen", "0.21.5")
        self.pi = self.fake_node_executable("pi", "0.84.1")
        self.task = self.task_fixture()
        self.policy = self.policy_fixture()

    def tearDown(self) -> None:
        self.temporary.cleanup()

    def fake_executable(self, name: str, version: str) -> Path:
        executable = self.root / name
        executable.write_text(
            "#!/bin/sh\n"
            "for arg in \"$@\"; do\n"
            "  if [ \"$arg\" = \"--version\" ]; then\n"
            f"    printf '%s\\n' '{version}'\n"
            "    exit 0\n"
            "  fi\n"
            "done\n"
            f": > '{self.worker_marker}'\n"
            "exit 97\n",
            encoding="utf-8",
        )
        executable.chmod(0o700)
        return executable

    def fake_node_executable(self, name: str, version: str) -> Path:
        directory = self.root / f"{name}-bin"
        directory.mkdir()
        node = directory / "node"
        node.write_text(
            "#!/bin/sh\n"
            "script=$1\n"
            "shift\n"
            "exec /bin/sh \"$script\" \"$@\"\n",
            encoding="utf-8",
        )
        node.chmod(0o700)
        executable = directory / name
        executable.write_text(
            "#!/usr/bin/env node\n"
            "if command -v marshal-path-poison >/dev/null 2>&1; then\n"
            f"  : > '{self.worker_marker}'\n"
            "  exit 96\n"
            "fi\n"
            "for arg in \"$@\"; do\n"
            "  if [ \"$arg\" = \"--version\" ]; then\n"
            f"    printf '%s\\n' '{version}'\n"
            "    exit 0\n"
            "  fi\n"
            "done\n"
            f": > '{self.worker_marker}'\n"
            "exit 97\n",
            encoding="utf-8",
        )
        executable.chmod(0o700)
        return executable

    def install_path_poison(self, directory: Path) -> None:
        directory.mkdir(parents=True, exist_ok=True)
        poison = directory / "marshal-path-poison"
        poison.write_text(
            "#!/bin/sh\n"
            f": > '{self.worker_marker}'\n"
            "exit 95\n",
            encoding="utf-8",
        )
        poison.chmod(0o700)

    def fake_core_probe(self) -> Path:
        executable = self.root / "marshal-probe-spy"
        executable.write_text(
            "#!/bin/sh\n"
            f": > '{self.core_probe_marker}'\n"
            "exit 94\n",
            encoding="utf-8",
        )
        executable.chmod(0o700)
        return executable.resolve()

    def task_fixture(self) -> dict:
        return {
            "apiVersion": "marshal.dev/v1alpha1", "kind": "Task",
            "metadata": {"id": "PREMORTEM-1", "title": "plan pre-mortem"},
            "repository": {"path": str(self.repository), "baseRef": self.source_head, "remote": "origin"},
            "work": {"objective": "create report", "constraints": [], "nonGoals": []},
            "scope": {"allowPaths": ["reports/**"], "denyPaths": [".marshal/**"], "allowSubmodules": False, "maxChangedFiles": 1, "maxDiffBytes": 10000},
            "acceptance": {"commands": [{"id": "report-check", "argv": ["test", "-f", "reports/result.md"], "cwd": ".", "timeoutSeconds": 10, "required": True, "baselinePolicy": "none", "maxLogBytes": 10000}], "allowNoChange": False},
            "deliverables": [{"id": "report", "kind": "report", "required": True, "pathGlob": "reports/result.md", "minimumCount": 1}],
            "worker": {"preferredAdapter": "qoder", "fallbackAdapters": [], "executionProfile": "workspace-write", "sessionPolicy": "ephemeral"},
            "budgets": {"runTimeoutSeconds": 60, "attemptTimeoutSeconds": 30, "maxAttempts": 1, "maxOperationalRetries": 0, "maxReworkRounds": 0, "maxOutputBytes": 100000},
            "publication": {"required": False, "provider": "none", "mode": "none", "remote": "origin", "baseBranch": "main", "mergePolicy": "never", "requiredChecks": []},
        }

    def policy_fixture(self) -> dict:
        value = {
            "apiVersion": "marshal.dev/v1alpha1", "kind": "PolicySnapshot",
            "taskId": "PREMORTEM-1", "runId": "run-premortem-1",
            "sources": [{"scope": "builtin", "digest": "sha256:" + "b" * 64, "required": True}],
            "effective": {
                "minimumExecutionProfile": "workspace-write", "requireEnforcedNetworkPolicy": False,
                "networkPolicy": "unenforced", "allowFallbackWorkers": False,
                "allowWorkerSubagents": False, "allowPublication": False, "allowMerge": False,
                "allowGateWaivers": False, "allowedAdapters": ["qoder"],
                "environmentAllowlist": ["PATH"], "retentionDays": 30,
            },
            "control": {"autonomyProfile": "balanced", "requiredApprovals": ["plan", "publish"], "allowMediatedSteering": False, "directPtyPolicy": "deny", "maxSteeringRounds": 0},
            "policyDigest": "", "generatedAt": "2026-08-20T00:00:00Z",
        }
        return self.seal_policy(value)

    def seal_policy(self, value: dict) -> dict:
        result = copy.deepcopy(value)
        result["policyDigest"] = ""
        result["policyDigest"] = digest(compact(result))
        return result

    def invoke(self, *, selected_adapter: object | None = None, marshal: Path | None = None) -> tuple[int, dict]:
        task_raw = compact(self.task)
        policy_raw = compact(self.policy)
        (self.operator / "task-spec.json").write_bytes(task_raw)
        (self.operator / "policy-snapshot.json").write_bytes(policy_raw)
        manifest = {
            "apiVersion": "marshal.operator/v1alpha1", "kind": "PlanPremortemPreflight",
            "runId": "run-premortem-1",
            "selectedAdapter": self.task["worker"]["preferredAdapter"] if selected_adapter is None else selected_adapter,
            "sourceHead": self.source_head,
            "taskSpec": {"path": "task-spec.json", "digest": digest(task_raw)},
            "policySnapshot": {"path": "policy-snapshot.json", "digest": digest(policy_raw)},
        }
        (self.operator / "manifest.json").write_bytes(compact(manifest))
        environment = os.environ.copy()
        environment.update({
            "PATH": self.environment_path,
            "HOME": self.environment_home,
            "MARSHAL_OPENCODE_PATH": "", "MARSHAL_QWEN_PATH": str(self.qwen), "MARSHAL_PI_PATH": str(self.pi),
            "MARSHAL_QODER_PATH": str(self.qoder), "MARSHAL_QODER_MODE": "ordinary-user",
            "MARSHAL_QODER_CONFORMANCE_CONFIG": "", "MARSHAL_CODEX_PATH": str(self.codex),
            "MARSHAL_CODEX_MODE": "ordinary-user", "MARSHAL_CODEX_AUTHORITY_CONFIG": "",
        })
        completed = subprocess.run(
            [sys.executable, "-I", "-B", str(VALIDATOR), "--root", str(self.operator), "--manifest", "manifest.json", "--marshal", str(marshal or self.marshal)],
            cwd=REPOSITORY, env=environment, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False,
        )
        self.assertEqual(completed.stderr, "")
        return completed.returncode, json.loads(completed.stdout)

    def assert_reason(self, expected: str) -> None:
        code, result = self.invoke()
        self.assertEqual(code, 1)
        self.assertEqual(result, {"reasonCode": expected, "status": "fail"})
        self.assertFalse(self.worker_marker.exists(), "pre-mortem must never launch Worker.Run")

    def test_qoder_ordinary_user_passes_without_launching_worker(self) -> None:
        code, result = self.invoke()
        self.assertEqual(code, 0, result)
        self.assertEqual(result["reasonCode"], "plan-premortem-pass")
        self.assertEqual(result["authorityMode"], "ordinary-user")
        self.assertEqual(result["selectedAdapter"], "qoder")
        self.assertFalse(self.worker_marker.exists())

    def test_internal_command_passes_with_stable_marshal(self) -> None:
        code, result = self.invoke()
        self.assertEqual(code, 0, result)
        self.assertEqual(result["reasonCode"], "plan-premortem-pass")
        self.assertEqual(result["marshal"]["internalCommandVersion"], "plan-premortem-check/v1")
        self.assertFalse(self.worker_marker.exists())

    def test_qoder_explicit_empty_worker_tools_passes(self) -> None:
        self.task["worker"]["tools"] = []
        code, result = self.invoke()
        self.assertEqual((code, result["reasonCode"]), (0, "plan-premortem-pass"))
        self.assertFalse(self.worker_marker.exists())

    def test_qoder_named_worker_tools_fail_before_probe(self) -> None:
        self.task["worker"]["tools"] = ["read", "write"]
        self.assert_reason("adapter-named-worker-tools-unsupported")

    def test_missing_required_acceptance_command_fails_before_probe(self) -> None:
        self.task["acceptance"]["commands"][0]["required"] = False
        self.assert_reason("acceptance-required-command-missing")

    def test_policy_approval_gate_conflict_is_stable(self) -> None:
        self.policy["control"]["requiredApprovals"] = ["plan"]
        self.policy = self.seal_policy(self.policy)
        self.assert_reason("policy-approval-gates-conflict")

    def test_publication_merge_policy_conflict_is_stable(self) -> None:
        self.policy["effective"]["allowMerge"] = True
        self.policy = self.seal_policy(self.policy)
        self.assert_reason("policy-publication-merge-conflict")

    def test_ordinary_user_execution_profile_is_checked_by_core_capability(self) -> None:
        self.task["worker"]["executionProfile"] = "hardened"
        self.policy["effective"]["minimumExecutionProfile"] = "hardened"
        self.policy = self.seal_policy(self.policy)
        self.assert_reason("adapter-ordinary-user-execution-profile-unsupported")

    def test_qoder_missing_locked_tree_parent_fails_before_probe(self) -> None:
        self.task["deliverables"][0]["pathGlob"] = "missing/result.md"
        self.assert_reason("qoder-deliverable-parent-missing")

    def test_qwen_ordinary_user_forwards_home_for_user_config_capability(self) -> None:
        poison = self.root / "ambient-poison"
        self.install_path_poison(poison)
        self.install_path_poison(self.pi.parent)
        self.environment_path = f"{poison}:{self.environment_path}"
        self.task["worker"]["preferredAdapter"] = "qwen"
        self.policy["effective"]["allowedAdapters"] = ["qwen"]
        self.policy = self.seal_policy(self.policy)
        code, result = self.invoke()
        self.assertEqual(code, 0, result)
        self.assertEqual(result["selectedAdapter"], "qwen")
        self.assertNotIn("authorityMode", result)
        self.assertFalse(self.worker_marker.exists())

    def test_pi_env_node_probe_uses_only_selected_executable_parent(self) -> None:
        poison = self.root / "ambient-poison"
        self.install_path_poison(poison)
        self.install_path_poison(self.qwen.parent)
        self.environment_path = f"{poison}:{self.environment_path}"
        self.task["worker"]["preferredAdapter"] = "pi"
        self.policy["effective"]["allowedAdapters"] = ["pi"]
        self.policy = self.seal_policy(self.policy)
        code, result = self.invoke()
        self.assertEqual(code, 0, result)
        self.assertEqual(result["selectedAdapter"], "pi")
        self.assertFalse(self.worker_marker.exists())

    def test_pi_probe_rejects_malformed_executable_paths(self) -> None:
        malformed = (
            "relative/pi",
            str(self.pi.parent / "nested" / ".." / "pi"),
            str(self.pi.parent) + ":forged/pi",
            str(self.pi) + "\nforged",
            str(self.pi) + "\rforged",
        )
        for value in malformed:
            with self.subTest(value=repr(value)):
                self.pi = Path(value)
                self.task["worker"]["preferredAdapter"] = "pi"
                self.policy["effective"]["allowedAdapters"] = ["pi"]
                self.policy = self.seal_policy(self.policy)
                self.assert_reason("core-probe-environment-invalid")

    def test_script_adapter_path_rejects_nul_without_starting_probe(self) -> None:
        spec = importlib.util.spec_from_file_location("marshal_plan_premortem_validator_test", VALIDATOR)
        self.assertIsNotNone(spec)
        self.assertIsNotNone(spec.loader)
        validator = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(validator)
        inherited = dict(os.environ)
        inherited["MARSHAL_PI_PATH"] = "/valid/pi\x00forged"
        with mock.patch.object(validator.os, "environ", inherited):
            with self.assertRaises(validator.PreflightError) as caught:
                validator.checked_probe_path({"selectedAdapter": "pi"})
        self.assertEqual(caught.exception.reason_code, "core-probe-environment-invalid")
        self.assertFalse(self.core_probe_marker.exists())

    def test_non_string_selected_adapter_fails_closed_before_core_probe(self) -> None:
        probe = self.fake_core_probe()
        for selected in ({"forged": "pi"}, ["pi"]):
            with self.subTest(selected=selected):
                code, result = self.invoke(selected_adapter=selected, marshal=probe)
                self.assertEqual(code, 1)
                self.assertEqual(result, {"reasonCode": "manifest-shape-invalid", "status": "fail"})
                self.assertFalse(self.core_probe_marker.exists())
                self.assertFalse(self.worker_marker.exists())

    def test_unclean_home_fails_closed_before_core_probe(self) -> None:
        self.environment_home = str(self.home) + "/"
        self.assert_reason("core-probe-environment-invalid")

    def test_codex_ordinary_user_uses_same_core_capability_path(self) -> None:
        self.task["worker"]["preferredAdapter"] = "codex"
        self.policy["effective"]["allowedAdapters"] = ["codex"]
        self.policy = self.seal_policy(self.policy)
        code, result = self.invoke()
        self.assertEqual(code, 0, result)
        self.assertEqual((result["selectedAdapter"], result["authorityMode"]), ("codex", "ordinary-user"))
        self.assertFalse(self.worker_marker.exists())

    def test_codex_explicit_empty_worker_tools_passes(self) -> None:
        self.task["worker"]["preferredAdapter"] = "codex"
        self.task["worker"]["tools"] = []
        self.policy["effective"]["allowedAdapters"] = ["codex"]
        self.policy = self.seal_policy(self.policy)
        code, result = self.invoke()
        self.assertEqual((code, result["reasonCode"]), (0, "plan-premortem-pass"))
        self.assertFalse(self.worker_marker.exists())

    def test_codex_named_worker_tools_fail_before_probe(self) -> None:
        self.task["worker"]["preferredAdapter"] = "codex"
        self.task["worker"]["tools"] = ["read", "write"]
        self.policy["effective"]["allowedAdapters"] = ["codex"]
        self.policy = self.seal_policy(self.policy)
        self.assert_reason("adapter-named-worker-tools-unsupported")

    def test_template_validates_against_draft_2020_12_schema(self) -> None:
        task = self.operator / "template-task.json"
        policy = self.operator / "template-policy.json"
        task.write_bytes(compact(self.task))
        policy.write_bytes(compact(self.policy))
        completed = subprocess.run(
            [
                str(self.marshal), "internal", "plan-premortem-check", "--attestation-ready",
                "--manifest", str(TEMPLATE), "--task-spec", str(task),
                "--policy-snapshot", str(policy), "--schema", str(SCHEMA),
            ],
            input=b"\0", stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False,
        )
        self.assertEqual(completed.returncode, 1)
        self.assertEqual(completed.stdout, b"")
        self.assertEqual(
            json.loads(completed.stderr),
            {"reasonCode": "input-digest-mismatch", "status": "fail"},
        )

    def test_operator_root_parent_symlink_into_repository_marshal_is_rejected(self) -> None:
        marshal_operator = self.repository / ".marshal" / "operator"
        marshal_operator.mkdir(parents=True)
        linked_parent = self.root / "linked-parent"
        linked_parent.symlink_to(self.repository / ".marshal", target_is_directory=True)
        completed = subprocess.run(
            [
                sys.executable, "-I", "-B", str(VALIDATOR),
                "--root", str(linked_parent / "operator"),
                "--manifest", "manifest.json", "--marshal", str(self.marshal),
            ],
            cwd=REPOSITORY,
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            check=False,
        )
        self.assertEqual(completed.returncode, 1)
        self.assertEqual(json.loads(completed.stdout), {"reasonCode": "operator-root-invalid", "status": "fail"})
        self.assertEqual(completed.stderr, "")
        self.assertFalse(self.worker_marker.exists())


if __name__ == "__main__":
    unittest.main()
