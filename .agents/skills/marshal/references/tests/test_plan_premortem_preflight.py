from __future__ import annotations

import copy
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest


REFERENCES = Path(__file__).resolve().parents[1]
REPOSITORY = REFERENCES.parents[3]
VALIDATOR = REFERENCES / "validate-plan-premortem-preflight.py"
PROBE = Path(__file__).with_name("plan_premortem_core_probe.go")
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
        cls.build = Path(tempfile.mkdtemp(prefix="marshal-plan-premortem-build.")).resolve()
        cls.checker = cls.build / "plan-premortem-core-probe"
        subprocess.run(["go", "build", "-o", str(cls.checker), str(PROBE)], cwd=REPOSITORY, check=True)

    @classmethod
    def tearDownClass(cls) -> None:
        shutil.rmtree(cls.build)

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
        self.qoder = self.fake_executable("qoder", "1.1.23")
        self.codex = self.fake_executable("codex", "codex-cli 0.145.0")
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

    def invoke(self) -> tuple[int, dict]:
        task_raw = compact(self.task)
        policy_raw = compact(self.policy)
        (self.operator / "task-spec.json").write_bytes(task_raw)
        (self.operator / "policy-snapshot.json").write_bytes(policy_raw)
        manifest = {
            "apiVersion": "marshal.operator/v1alpha1", "kind": "PlanPremortemPreflight",
            "runId": "run-premortem-1", "selectedAdapter": self.task["worker"]["preferredAdapter"],
            "sourceHead": self.source_head,
            "taskSpec": {"path": "task-spec.json", "digest": digest(task_raw)},
            "policySnapshot": {"path": "policy-snapshot.json", "digest": digest(policy_raw)},
        }
        (self.operator / "manifest.json").write_bytes(compact(manifest))
        environment = os.environ.copy()
        environment.update({
            "MARSHAL_OPENCODE_PATH": "", "MARSHAL_QWEN_PATH": "", "MARSHAL_PI_PATH": "",
            "MARSHAL_QODER_PATH": str(self.qoder), "MARSHAL_QODER_MODE": "ordinary-user",
            "MARSHAL_QODER_CONFORMANCE_CONFIG": "", "MARSHAL_CODEX_PATH": "",
            "MARSHAL_CODEX_MODE": "", "MARSHAL_CODEX_AUTHORITY_CONFIG": "",
        })
        completed = subprocess.run(
            [sys.executable, "-I", "-B", str(VALIDATOR), "--root", str(self.operator), "--manifest", "manifest.json", "--checker", str(self.checker)],
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
        self.assertEqual(code, 0)
        self.assertEqual(result["reasonCode"], "plan-premortem-pass")
        self.assertEqual(result["authorityMode"], "ordinary-user")
        self.assertEqual(result["selectedAdapter"], "qoder")
        self.assertFalse(self.worker_marker.exists())

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

    def test_codex_ordinary_user_uses_same_core_capability_path(self) -> None:
        self.task["worker"]["preferredAdapter"] = "codex"
        self.policy["effective"]["allowedAdapters"] = ["codex"]
        self.policy = self.seal_policy(self.policy)
        task_raw = compact(self.task)
        policy_raw = compact(self.policy)
        (self.operator / "task-spec.json").write_bytes(task_raw)
        (self.operator / "policy-snapshot.json").write_bytes(policy_raw)
        manifest = {
            "apiVersion": "marshal.operator/v1alpha1", "kind": "PlanPremortemPreflight",
            "runId": "run-premortem-1", "selectedAdapter": "codex", "sourceHead": self.source_head,
            "taskSpec": {"path": "task-spec.json", "digest": digest(task_raw)},
            "policySnapshot": {"path": "policy-snapshot.json", "digest": digest(policy_raw)},
        }
        (self.operator / "manifest.json").write_bytes(compact(manifest))
        environment = os.environ.copy()
        environment.update({
            "MARSHAL_OPENCODE_PATH": "", "MARSHAL_QWEN_PATH": "", "MARSHAL_PI_PATH": "",
            "MARSHAL_QODER_PATH": "", "MARSHAL_QODER_MODE": "", "MARSHAL_QODER_CONFORMANCE_CONFIG": "",
            "MARSHAL_CODEX_PATH": str(self.codex), "MARSHAL_CODEX_MODE": "ordinary-user",
            "MARSHAL_CODEX_AUTHORITY_CONFIG": "",
        })
        completed = subprocess.run(
            [sys.executable, "-I", "-B", str(VALIDATOR), "--root", str(self.operator), "--manifest", "manifest.json", "--checker", str(self.checker)],
            cwd=REPOSITORY, env=environment, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False,
        )
        self.assertEqual(completed.returncode, 0, completed.stdout)
        result = json.loads(completed.stdout)
        self.assertEqual((result["selectedAdapter"], result["authorityMode"]), ("codex", "ordinary-user"))
        self.assertFalse(self.worker_marker.exists())

    def test_template_validates_against_draft_2020_12_schema(self) -> None:
        probe = self.build / "schema-probe"
        source = Path(__file__).with_name("acceptance_semantic_schema_probe.go")
        subprocess.run(["go", "build", "-o", str(probe), str(source)], cwd=REPOSITORY, check=True)
        completed = subprocess.run([str(probe), str(SCHEMA), str(TEMPLATE)], text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False)
        self.assertEqual(completed.returncode, 0, completed.stderr)

    def test_operator_root_parent_symlink_into_repository_marshal_is_rejected(self) -> None:
        marshal_operator = self.repository / ".marshal" / "operator"
        marshal_operator.mkdir(parents=True)
        linked_parent = self.root / "linked-parent"
        linked_parent.symlink_to(self.repository / ".marshal", target_is_directory=True)
        completed = subprocess.run(
            [
                sys.executable, "-I", "-B", str(VALIDATOR),
                "--root", str(linked_parent / "operator"),
                "--manifest", "manifest.json", "--checker", str(self.checker),
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
