#!/usr/bin/env python3

from __future__ import annotations

from datetime import datetime, timedelta, timezone
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import platform
import shutil
import subprocess
import tempfile
import threading
import time
import unittest
from unittest import mock
import warnings


HERE = Path(__file__).resolve().parent
REFERENCES = HERE.parent
REPOSITORY = REFERENCES.parents[3]
VALIDATOR = REFERENCES / "validate-admission-receipt.py"
GENERATOR = REFERENCES / "create-admission-receipt.py"
JQ = REFERENCES / "validate-admission-receipt.jq"
SCHEMA = REFERENCES / "admission-receipt.schema.json"
SCHEMA_PROBE = HERE / "admission_receipt_schema_probe.go"


def canonical_bytes(value: object) -> bytes:
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode()


def digest(value: object) -> str:
    raw = value if isinstance(value, bytes) else canonical_bytes(value)
    return "sha256:" + hashlib.sha256(raw).hexdigest()


def file_identity(path: Path) -> dict:
    metadata = path.stat()
    return {"digest": digest(path.read_bytes()), "device": metadata.st_dev, "inode": metadata.st_ino}


def json_bytes(value: object) -> bytes:
    return json.dumps(value, ensure_ascii=False, indent=2).encode() + b"\n"


def path_snapshot(path: Path) -> tuple[bytes, tuple[int, int, int, int, int]]:
    metadata = path.stat()
    return path.read_bytes(), (metadata.st_dev, metadata.st_ino, metadata.st_size,
                               metadata.st_mtime_ns, metadata.st_mode)


def load_python(path: Path, name: str):
    spec = importlib.util.spec_from_file_location(name, path)
    if spec is None or spec.loader is None:
        raise RuntimeError(name)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class AdmissionReceiptTest(unittest.TestCase):
    maxDiff = None

    def setUp(self) -> None:
        temp_parent = Path("/private/tmp") if Path("/private/tmp").is_dir() else Path("/tmp")
        self.temp = Path(tempfile.mkdtemp(prefix="admission-receipt.", dir=temp_parent))
        self.operator = self.temp / "operator"
        self.run_root = self.temp / "run"
        self.workspace = self.temp / "workspace"
        self.worktree = self.temp / "worktree"
        for path in (self.operator, self.run_root / "control", self.workspace / "bin", self.workspace / "scripts", self.worktree):
            path.mkdir(parents=True, exist_ok=True)
        subprocess.run(["/usr/bin/git", "init", "-q"], cwd=self.worktree, check=True)
        subprocess.run(["/usr/bin/git", "config", "user.name", "Fixture"], cwd=self.worktree, check=True)
        subprocess.run(["/usr/bin/git", "config", "user.email", "fixture@example.invalid"], cwd=self.worktree, check=True)
        (self.worktree / "README").write_text("fixture\n", encoding="utf-8")
        subprocess.run(["/usr/bin/git", "add", "README"], cwd=self.worktree, check=True)
        subprocess.run(["/usr/bin/git", "-c", "core.hooksPath=/dev/null", "commit", "-qm", "fixture"], cwd=self.worktree, check=True)
        self.head = subprocess.check_output(["/usr/bin/git", "rev-parse", "HEAD"], cwd=self.worktree, text=True).strip()

        self.adapter = self.temp / "qodercli-1.1.23"
        self.adapter.write_text("#!/bin/sh\nprintf '1.1.23\\n'\n", encoding="utf-8")
        self.adapter.chmod(0o700)
        self.launch_env = {
            "HOME": str(self.temp / "home-private-value"),
            "LANG": "C",
            "LC_ALL": "C",
            "MARSHAL_QODER_MODE": "ordinary-user",
            "MARSHAL_QODER_PATH": str(self.adapter),
            "MARSHAL_WATCH_COHORT_FILE": str(self.temp / "cohort.json"),
            "MARSHAL_WATCH_NOTIFY": "0",
            "PATH": "/usr/bin:/bin",
            "TMPDIR": str(self.temp),
        }
        now = datetime.now(timezone.utc)
        self.observed = now - timedelta(seconds=1)
        self.valid_until = now + timedelta(seconds=45)
        sha = digest(b"fixture")
        self.state = {
            "apiVersion": "marshal.dev/v1alpha1", "kind": "RunState",
            "taskId": "ADMISSION-FIXTURE", "runId": "admission-fixture-r1",
            "state": "READY", "sequence": 2, "specDigest": sha,
            "policyDigest": sha, "capabilityDigest": sha, "baseSha": self.head,
            "worktreePath": str(self.worktree), "reviewRound": 0, "attemptsUsed": 0,
            "operationalRetriesUsed": 0, "reworkRoundsUsed": 0,
            "createdAt": (now - timedelta(minutes=1)).isoformat().replace("+00:00", "Z"),
            "updatedAt": (now - timedelta(seconds=2)).isoformat().replace("+00:00", "Z"),
        }
        self.approval = {
            "apiVersion": "marshal.dev/v1alpha1", "kind": "ApprovalRecord",
            "recordId": "approval:fixture", "taskId": self.state["taskId"],
            "runId": self.state["runId"], "controlSequence": 1, "gate": "plan",
            "source": {"type": "human", "id": "fixture-maintainer"},
            "binding": {"stateSequence": 2, "specDigest": sha, "policyDigest": sha,
                        "capabilityDigest": sha, "baseSha": self.head},
            "outcome": "approved",
            "createdAt": (now - timedelta(seconds=2)).isoformat().replace("+00:00", "Z"),
        }
        (self.run_root / "state.json").write_bytes(json_bytes(self.state))
        (self.run_root / "control/records.jsonl").write_bytes(canonical_bytes(self.approval) + b"\n")
        self.doctor_worker = {
            "adapterId": "qoder", "environmentVariable": "MARSHAL_QODER_PATH",
            "configured": True, "registered": True, "outcome": "registered",
            "authorityMode": "ordinary-user", "compatibility": "supported",
            "adapterVersion": "0.1.4", "binaryVersion": "1.1.23",
            "executableDigest": digest(self.adapter.read_bytes()),
        }
        self.watch_capacity = {
            "pressure": "ok", "queueSignalStatus": "ok", "cpuStatus": "ok",
            "providerStatus": "ok", "slotsAvailable": 2,
            "providerSignals": [{"adapterId": "qoder", "status": "available"}],
        }
        self.write_tools()
        self.receipt = self.make_receipt()
        self.receipt_path = self.operator / "admission-receipt.json"
        self.write_receipt()

    def tearDown(self) -> None:
        shutil.rmtree(self.temp)

    def doctor_report(self) -> dict:
        return {
            "status": "ok", "workers": [self.doctor_worker],
            "run": {"runId": self.state["runId"], "status": "ok", "snapshotSequence": 2,
                    "journalSequence": 2, "state": self.state, "findings": []},
        }

    def watch_report(self) -> dict:
        return {"queueVersion": "marshal-watch/v2", "advisoryOnly": True,
                "generatedAt": self.observed.isoformat().replace("+00:00", "Z"),
                "capacity": self.watch_capacity, "items": [], "historicalItems": []}

    def write_tools(self, doctor_delay: float = 0, doctor_marker: Path | None = None,
                    watch_delay: float = 0, watch_marker: Path | None = None) -> None:
        report = json.dumps(self.doctor_report(), separators=(",", ":"))
        checks = ""
        if doctor_marker is not None:
            checks += f"Path({str(doctor_marker)!r}).write_text('ready')\n"
        if doctor_delay:
            checks += f"time.sleep({doctor_delay!r})\n"
        marshal = self.workspace / "bin/marshal"
        marshal.write_text(
            "#!/usr/bin/python3\nimport json,os,sys,time\nfrom pathlib import Path\n"
            f"expected={self.launch_env!r}\n"
            "if sys.argv[1:] != ['doctor','--run','admission-fixture-r1','--json']:\n sys.exit(9)\n"
            "if any(os.environ.get(k) != v for k,v in expected.items()):\n sys.exit(8)\n"
            + checks + f"print({report!r})\n",
            encoding="utf-8",
        )
        marshal.chmod(0o700)
        watch = self.workspace / "scripts/marshal-watch.sh"
        watch_json = json.dumps(self.watch_report(), separators=(",", ":"))
        watch_prefix = "#!/bin/bash\n"
        if watch_marker is not None:
            watch_prefix += "/usr/bin/touch " + repr(str(watch_marker)) + "\n"
        if watch_delay:
            watch_prefix += "/bin/sleep " + repr(str(watch_delay)) + "\n"
        watch.write_text(watch_prefix + "printf '%s\\n' " + repr(watch_json) + "\n", encoding="utf-8")
        watch.chmod(0o700)

    @staticmethod
    def replace_same_bytes(path: Path) -> None:
        replacement = path.with_name(path.name + ".replacement")
        replacement.write_bytes(path.read_bytes())
        replacement.chmod(path.stat().st_mode & 0o777)
        os.replace(replacement, path)

    @staticmethod
    def wait_for(path: Path) -> None:
        deadline = time.monotonic() + 5
        while not path.exists() and time.monotonic() < deadline:
            time.sleep(0.01)

    def doctor_projection(self) -> dict:
        worker = self.doctor_worker
        return {
            "reportStatus": "ok", "runStatus": "ok", "snapshotSequence": 2,
            "adapterId": "qoder", "configured": True, "registered": True,
            "compatibility": worker["compatibility"],
            "authorityMode": worker.get("authorityMode", ""),
            "binaryVersion": worker["binaryVersion"],
            "executableDigest": worker["executableDigest"],
        }

    def capacity_projection(self) -> dict:
        return {"pressure": self.watch_capacity["pressure"],
                "queueSignalStatus": self.watch_capacity["queueSignalStatus"],
                "cpuStatus": self.watch_capacity["cpuStatus"],
                "providerStatus": self.watch_capacity["providerStatus"],
                "capacityAvailable": True}

    def make_receipt(self) -> dict:
        adapter_identity = file_identity(self.adapter)
        empty_digest = digest(b"")
        sha = digest(b"fixture")
        return {
            "format": "marshal-skill/operator-admission-receipt-v3",
            "authority": "operator-local-non-core", "taskId": self.state["taskId"],
            "runId": self.state["runId"], "observationSequence": 2, "stateEventSequence": 2,
            "observedAt": self.observed.isoformat().replace("+00:00", "Z"),
            "validUntil": self.valid_until.isoformat().replace("+00:00", "Z"),
            "bindings": {"sourceHead": self.head, "baseSha": self.head,
                         "specDigest": sha, "policyDigest": sha, "capabilityDigest": sha,
                         "runStateDigest": digest(self.state), "planApprovalDigest": digest(self.approval)},
            "host": {"os": platform.system().lower(),
                     "arch": "arm64" if platform.machine().lower() in {"arm64", "aarch64"} else "amd64"},
            "adapter": {"id": "qoder", "mode": "ordinary-user", "binaryVersion": "1.1.23",
                        "executable": {"canonicalPath": str(self.adapter), **adapter_identity}},
            "worktree": {"canonicalPath": str(self.worktree), "headSha": self.head,
                         "statusDigest": empty_digest},
            "files": {"statePath": "state.json", "controlRecordsPath": "control/records.jsonl"},
            "planApproval": {"recordId": self.approval["recordId"], "controlSequence": 1},
            "launchEnvironment": {"keys": sorted(self.launch_env), "digest": digest(self.launch_env)},
            "tooling": {"marshalExecutable": file_identity(self.workspace / "bin/marshal"),
                        "watchScript": file_identity(self.workspace / "scripts/marshal-watch.sh")},
            "dynamicEvidence": {"doctorDigest": digest(self.doctor_projection()),
                                "capacityDigest": digest(self.capacity_projection()),
                                "providerBackpressureDigest": digest(self.watch_capacity["providerSignals"][0])},
            "checks": {key: True for key in ("stateReady", "currentPlanApproved", "doctorConfigured",
                       "doctorSupported", "worktreeClean", "capacityAvailable",
                       "providerBackpressureAbsent")},
            "decision": "observe", "reasonCode": "operator-sampled",
        }

    def refresh_tooling(self) -> None:
        self.receipt["tooling"] = {"marshalExecutable": file_identity(self.workspace / "bin/marshal"),
                                   "watchScript": file_identity(self.workspace / "scripts/marshal-watch.sh")}

    def write_receipt(self) -> None:
        self.receipt_path.write_bytes(json_bytes(self.receipt))

    def invoke(self, env: dict[str, str] | None = None) -> tuple[int, dict, str]:
        completed = subprocess.run(
            ["/usr/bin/python3", "-I", "-B", str(VALIDATOR),
             "--operator-root", str(self.operator), "--receipt", self.receipt_path.name,
             "--run-root", str(self.run_root), "--workspace-root", str(self.workspace)],
            cwd=REPOSITORY, env=env or self.launch_env, text=True,
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False,
        )
        return completed.returncode, json.loads(completed.stdout), completed.stderr

    def configure_adapter(self, adapter_id: str, mode: str) -> None:
        path_key = {"qoder": "MARSHAL_QODER_PATH", "codex": "MARSHAL_CODEX_PATH",
                    "qwen": "MARSHAL_QWEN_PATH", "pi": "MARSHAL_PI_PATH"}[adapter_id]
        self.launch_env = {
            "HOME": str(self.temp / "home-private-value"), "LANG": "C", "LC_ALL": "C",
            path_key: str(self.adapter),
            "MARSHAL_WATCH_COHORT_FILE": str(self.temp / "cohort.json"),
            "MARSHAL_WATCH_NOTIFY": "0", "PATH": "/usr/bin:/bin", "TMPDIR": str(self.temp),
        }
        if mode == "ordinary-user":
            self.launch_env[{"qoder": "MARSHAL_QODER_MODE", "codex": "MARSHAL_CODEX_MODE"}[adapter_id]] = "ordinary-user"
        self.doctor_worker.update({"adapterId": adapter_id, "environmentVariable": path_key})
        if mode == "ordinary-user":
            self.doctor_worker["authorityMode"] = "ordinary-user"
        else:
            self.doctor_worker.pop("authorityMode", None)
        self.watch_capacity["providerSignals"] = [{"adapterId": adapter_id, "status": "available"}]
        self.observed = datetime.now(timezone.utc)
        self.write_tools()

    def invoke_generator(self, adapter_id: str, mode: str, receipt: str,
                         env: dict[str, str] | None = None,
                         operator_root: Path | None = None) -> tuple[int, dict, str]:
        completed = subprocess.run(
            ["/usr/bin/python3", "-I", "-B", str(GENERATOR),
             "--operator-root", str(operator_root or self.operator), "--receipt", receipt,
             "--run-root", str(self.run_root), "--workspace-root", str(self.workspace),
             "--adapter-id", adapter_id, "--adapter-mode", mode],
            cwd=REPOSITORY, env=env or self.launch_env, text=True,
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False,
        )
        return completed.returncode, json.loads(completed.stdout), completed.stderr

    def assert_reason(self, reason: str, env: dict[str, str] | None = None) -> None:
        code, result, stderr = self.invoke(env)
        self.assertEqual(stderr, "")
        self.assertEqual(code, 2, result)
        self.assertEqual(result, {"reasonCode": reason, "status": "fail"})

    def test_valid_receipt_re_samples_and_redacts(self) -> None:
        code, result, stderr = self.invoke()
        self.assertEqual((code, stderr), (0, ""), result)
        self.assertEqual(result["reasonCode"], "operator-receipt-valid")
        rendered = json.dumps(result)
        for secret_or_path in (self.launch_env["HOME"], self.launch_env["MARSHAL_QODER_PATH"], self.launch_env["PATH"]):
            self.assertNotIn(secret_or_path, rendered)
        self.assertNotIn(str(self.adapter), rendered)

    def test_generator_success_matrix_is_0600_and_immediately_valid(self) -> None:
        for adapter_id, mode in (("qoder", "ordinary-user"), ("codex", "ordinary-user"),
                                 ("qwen", "host-user"), ("pi", "host-user")):
            with self.subTest(adapter_id=adapter_id):
                self.configure_adapter(adapter_id, mode)
                if adapter_id == "qoder":
                    self.launch_env["MARSHAL_QODER_CONFORMANCE_CONFIG"] = str(self.temp / "qoder-config")
                elif adapter_id == "codex":
                    self.launch_env["MARSHAL_CODEX_AUTHORITY_CONFIG"] = str(self.temp / "codex-config")
                self.write_tools()
                name = f"generated-{adapter_id}.json"
                code, result, stderr = self.invoke_generator(adapter_id, mode, name)
                self.assertEqual((code, stderr), (0, ""), result)
                self.assertEqual(result["reasonCode"], "operator-receipt-created-and-valid")
                target = self.operator / name
                self.assertEqual(target.stat().st_mode & 0o777, 0o600)
                generated = json.loads(target.read_text())
                self.assertEqual(generated["adapter"]["id"], adapter_id)
                self.assertEqual(generated["launchEnvironment"]["keys"], sorted(self.launch_env))
                self.assertNotIn("scopeExclusive", generated["checks"])

    def test_generator_never_replaces_existing_symlink(self) -> None:
        self.configure_adapter("pi", "host-user")
        outside = self.temp / "outside-receipt-target"
        outside.write_text("sentinel\n")
        target = self.operator / "generated.json"
        target.symlink_to(outside)
        code, result, stderr = self.invoke_generator("pi", "host-user", target.name)
        self.assertEqual((code, stderr), (2, ""), result)
        self.assertEqual(result["reasonCode"], "receipt-output-exists")
        self.assertTrue(target.is_symlink())
        self.assertEqual(outside.read_text(), "sentinel\n")

    def test_generator_rejects_host_user_for_qoder(self) -> None:
        completed = subprocess.run(
            ["/usr/bin/python3", "-I", "-B", str(GENERATOR),
             "--operator-root", str(self.operator),
             "--receipt", "generated.json", "--run-root", str(self.run_root),
             "--workspace-root", str(self.workspace), "--adapter-id", "qoder",
             "--adapter-mode", "host-user"],
            cwd=REPOSITORY, env=self.launch_env, text=True,
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False,
        )
        self.assertEqual(completed.returncode, 2)
        self.assertEqual(json.loads(completed.stdout), {"reasonCode": "launch-authority-mode-mismatch", "status": "fail"})

    def test_generator_rejects_symlink_output_parent(self) -> None:
        self.launch_env = {
            "HOME": str(self.temp / "home-private-value"), "LANG": "C", "LC_ALL": "C",
            "MARSHAL_PI_PATH": str(self.adapter),
            "MARSHAL_WATCH_COHORT_FILE": str(self.temp / "cohort.json"),
            "MARSHAL_WATCH_NOTIFY": "0", "PATH": "/usr/bin:/bin", "TMPDIR": str(self.temp),
        }
        self.doctor_worker.update({"adapterId": "pi", "environmentVariable": "MARSHAL_PI_PATH"})
        self.doctor_worker.pop("authorityMode")
        self.watch_capacity["providerSignals"] = [{"adapterId": "pi", "status": "available"}]
        self.write_tools()
        outside = self.temp / "outside-output-parent"
        outside.mkdir()
        (self.operator / "linked-parent").symlink_to(outside, target_is_directory=True)
        completed = subprocess.run(
            ["/usr/bin/python3", "-I", "-B", str(GENERATOR),
             "--operator-root", str(self.operator),
             "--receipt", "linked-parent/generated.json", "--run-root", str(self.run_root),
             "--workspace-root", str(self.workspace), "--adapter-id", "pi",
             "--adapter-mode", "host-user"],
            cwd=REPOSITORY, env=self.launch_env, text=True,
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False,
        )
        self.assertEqual(completed.returncode, 2)
        self.assertEqual(json.loads(completed.stdout), {"reasonCode": "path-symlink-rejected", "status": "fail"})
        self.assertFalse((outside / "generated.json").exists())

    def test_generator_rejects_unselected_adapter_environment(self) -> None:
        changed = dict(self.launch_env)
        changed["MARSHAL_CODEX_PATH"] = str(self.adapter)
        completed = subprocess.run(
            ["/usr/bin/python3", "-I", "-B", str(GENERATOR),
             "--operator-root", str(self.operator),
             "--receipt", "generated.json", "--run-root", str(self.run_root),
             "--workspace-root", str(self.workspace), "--adapter-id", "qoder",
             "--adapter-mode", "ordinary-user"],
            cwd=REPOSITORY, env=changed, text=True,
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False,
        )
        self.assertEqual(completed.returncode, 2)
        self.assertEqual(json.loads(completed.stdout), {"reasonCode": "launch-environment-invalid", "status": "fail"})

    def test_generator_rejects_opencode_and_cross_adapter_configuration(self) -> None:
        cases = (
            ("qoder", "ordinary-user", "MARSHAL_CODEX_AUTHORITY_CONFIG"),
            ("codex", "ordinary-user", "MARSHAL_QODER_CONFORMANCE_CONFIG"),
            ("qwen", "host-user", "MARSHAL_QODER_FENCE_ROOT"),
            ("pi", "host-user", "MARSHAL_CODEX_AUTHORITY_CONFIG"),
            ("pi", "host-user", "MARSHAL_OPENCODE_PATH"),
        )
        for adapter_id, mode, polluted_key in cases:
            with self.subTest(adapter_id=adapter_id, polluted_key=polluted_key):
                self.configure_adapter(adapter_id, mode)
                changed = dict(self.launch_env)
                changed[polluted_key] = str(self.temp / "polluted-private-value")
                code, result, stderr = self.invoke_generator(adapter_id, mode,
                                                               f"polluted-{adapter_id}-{polluted_key}.json",
                                                               changed)
                self.assertEqual((code, stderr), (2, ""), result)
                self.assertEqual(result["reasonCode"], "launch-environment-invalid")

    def test_generator_cannot_overwrite_protected_inputs(self) -> None:
        self.configure_adapter("pi", "host-user")
        protected = (
            self.run_root / "state.json", self.run_root / "control/records.jsonl",
            self.worktree / "README", self.workspace / "bin/marshal",
            self.workspace / "scripts/marshal-watch.sh", SCHEMA,
        )
        snapshots = {path: path_snapshot(path) for path in protected}
        for path in protected:
            relative = path.as_posix().lstrip("/")
            code, result, stderr = self.invoke_generator("pi", "host-user", relative,
                                                          operator_root=Path("/"))
            self.assertEqual((code, stderr), (2, ""), result)
            self.assertEqual(result["reasonCode"], "receipt-output-exists")
        self.assertEqual({path: path_snapshot(path) for path in protected}, snapshots)

    def test_generator_rejects_new_output_inside_protected_roots(self) -> None:
        self.configure_adapter("pi", "host-user")
        for protected_root in (self.run_root, self.workspace, self.worktree):
            with self.subTest(protected_root=protected_root):
                name = "must-not-be-created.json"
                target = protected_root / name
                code, result, stderr = self.invoke_generator("pi", "host-user", name,
                                                              operator_root=protected_root)
                self.assertEqual((code, stderr), (2, ""), result)
                self.assertEqual(result["reasonCode"], "receipt-output-boundary-invalid")
                self.assertFalse(target.exists())

    def test_generator_rechecks_ttl_before_creating_output(self) -> None:
        module = load_python(GENERATOR, "admission_generator_expiry_test")
        target = module.OutputTarget(self.operator, "expired-generated.json")
        expired = dict(self.receipt)
        expired["observedAt"] = (datetime.now(timezone.utc) - timedelta(seconds=3)).isoformat().replace("+00:00", "Z")
        expired["validUntil"] = (datetime.now(timezone.utc) - timedelta(seconds=2)).isoformat().replace("+00:00", "Z")
        try:
            with self.assertRaises(module.V.AdmissionError) as raised:
                target.write(expired)
            self.assertEqual(raised.exception.reason_code, "receipt-expired")
        finally:
            target.close()
        self.assertFalse((self.operator / "expired-generated.json").exists())

    def test_generator_rejects_symlink_adapter_with_fixed_reason(self) -> None:
        link = self.temp / "qoder-link"
        link.symlink_to(self.adapter)
        changed = dict(self.launch_env)
        changed["MARSHAL_QODER_PATH"] = str(link)
        completed = subprocess.run(
            ["/usr/bin/python3", "-I", "-B", str(GENERATOR),
             "--operator-root", str(self.operator),
             "--receipt", "generated.json", "--run-root", str(self.run_root),
             "--workspace-root", str(self.workspace), "--adapter-id", "qoder",
             "--adapter-mode", "ordinary-user"],
            cwd=REPOSITORY, env=changed, text=True,
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False,
        )
        self.assertEqual(completed.returncode, 2)
        self.assertEqual(json.loads(completed.stdout), {"reasonCode": "adapter-executable-invalid", "status": "fail"})

    def test_jq_is_shape_lint_not_final_admit(self) -> None:
        lint = subprocess.run(["jq", "-e", "-f", str(JQ), str(self.receipt_path)], stdout=subprocess.DEVNULL)
        self.assertEqual(lint.returncode, 0)
        self.adapter.write_text("#!/bin/sh\nprintf 'drift\\n'\n", encoding="utf-8")
        self.adapter.chmod(0o700)
        self.assert_reason("adapter-executable-identity-mismatch")

    def test_dirty_or_wrong_head_worktree_fails_closed(self) -> None:
        (self.worktree / "untracked").write_text("dirty")
        self.assert_reason("worktree-not-clean")

    def test_launch_environment_drift_is_redacted(self) -> None:
        changed = dict(self.launch_env); changed["HOME"] = str(self.temp / "different-secret-path")
        code, result, stderr = self.invoke(changed)
        self.assertEqual((code, stderr), (2, ""))
        self.assertEqual(result["reasonCode"], "launch-environment-drift")
        self.assertNotIn(changed["HOME"], json.dumps(result))

    def test_validator_rejects_unlisted_governed_environment_pollution(self) -> None:
        for key in ("MARSHAL_QODER_CONFORMANCE_CONFIG", "MARSHAL_CODEX_AUTHORITY_CONFIG",
                    "MARSHAL_OPENCODE_PATH"):
            with self.subTest(key=key):
                changed = dict(self.launch_env)
                changed[key] = str(self.temp / "private-pollution")
                self.assert_reason("launch-environment-invalid", changed)

    def test_validator_rechecks_ttl_after_dynamic_probes(self) -> None:
        now = datetime.now(timezone.utc)
        self.observed = now - timedelta(seconds=1)
        self.receipt["observedAt"] = self.observed.isoformat().replace("+00:00", "Z")
        self.receipt["validUntil"] = (now + timedelta(milliseconds=300)).isoformat().replace("+00:00", "Z")
        self.write_tools(doctor_delay=0.6)
        self.refresh_tooling()
        self.write_receipt()
        self.assert_reason("receipt-expired")

    def test_validator_rechecks_ttl_after_final_identity_checks(self) -> None:
        module = load_python(VALIDATOR, "admission_validator_final_expiry_test")
        original = module.validate_receipt_freshness
        calls = 0

        def freshness(receipt: dict) -> None:
            nonlocal calls
            calls += 1
            original(receipt)
            if calls == 3:
                raise module.AdmissionError("receipt-expired")

        module.validate_receipt_freshness = freshness
        args = type("Args", (), {
            "operator_root": str(self.operator), "receipt": self.receipt_path.name,
            "run_root": str(self.run_root), "workspace_root": str(self.workspace),
        })()
        with mock.patch.dict(os.environ, self.launch_env, clear=True):
            with warnings.catch_warnings():
                warnings.simplefilter("ignore", ResourceWarning)
                with self.assertRaises(module.AdmissionError) as raised:
                    module.validate(args)
        self.assertEqual(raised.exception.reason_code, "receipt-expired")
        self.assertEqual(calls, 3)

    def test_malformed_launch_keys_fail_without_traceback(self) -> None:
        self.receipt["launchEnvironment"]["keys"] = [{}]
        self.write_receipt()
        self.assert_reason("launch-environment-invalid")

    def test_doctor_authority_identity_mismatch_fails_before_digest(self) -> None:
        self.doctor_worker["authorityMode"] = ""
        self.write_tools(); self.refresh_tooling(); self.write_receipt()
        self.assert_reason("doctor-authority-mode-mismatch")

    def test_capacity_and_provider_backpressure_fail_closed(self) -> None:
        self.watch_capacity["providerStatus"] = "backpressure"
        self.watch_capacity["providerSignals"] = [{"adapterId": "qoder", "status": "backpressure", "failureKind": "rate-limited"}]
        self.write_tools(); self.refresh_tooling(); self.write_receipt()
        self.assert_reason("capacity-unavailable")

    def test_plan_approval_binding_mismatch_fails_closed(self) -> None:
        self.approval["binding"]["baseSha"] = "f" * 40
        (self.run_root / "control/records.jsonl").write_bytes(canonical_bytes(self.approval) + b"\n")
        self.assert_reason("plan-approval-binding-mismatch")

    def test_older_matching_plan_approval_is_stale(self) -> None:
        newer = dict(self.approval)
        newer["recordId"] = "approval:newer"
        newer["controlSequence"] = 2
        records = canonical_bytes(self.approval) + b"\n" + canonical_bytes(newer) + b"\n"
        (self.run_root / "control/records.jsonl").write_bytes(records)
        self.assert_reason("plan-approval-stale")

    def test_state_identity_change_during_sampling_fails_closed(self) -> None:
        marker = self.workspace / "doctor-started"
        self.write_tools(doctor_delay=0.4, doctor_marker=marker); self.refresh_tooling(); self.write_receipt()

        def replace_state() -> None:
            self.wait_for(marker)
            replacement = self.run_root / "state.replacement"
            replacement.write_bytes(json_bytes(self.state))
            os.replace(replacement, self.run_root / "state.json")

        thread = threading.Thread(target=replace_state)
        thread.start()
        self.assert_reason("admission-evidence-drift")
        thread.join(timeout=5)

    def test_adapter_same_bytes_new_inode_during_probe_is_rejected(self) -> None:
        marker = self.workspace / "doctor-started"
        self.write_tools(doctor_delay=0.4, doctor_marker=marker); self.refresh_tooling(); self.write_receipt()
        thread = threading.Thread(target=lambda: (self.wait_for(marker), self.replace_same_bytes(self.adapter)))
        thread.start(); self.assert_reason("adapter-executable-drift"); thread.join(timeout=5)

    def test_marshal_same_bytes_new_inode_during_probe_is_rejected(self) -> None:
        marker = self.workspace / "doctor-started"
        self.write_tools(doctor_delay=0.4, doctor_marker=marker); self.refresh_tooling(); self.write_receipt()
        marshal = self.workspace / "bin/marshal"
        thread = threading.Thread(target=lambda: (self.wait_for(marker), self.replace_same_bytes(marshal)))
        thread.start(); self.assert_reason("marshal-executable-drift"); thread.join(timeout=5)

    def test_watch_same_bytes_new_inode_during_probe_is_rejected(self) -> None:
        marker = self.workspace / "watch-started"
        self.write_tools(watch_delay=0.4, watch_marker=marker); self.refresh_tooling(); self.write_receipt()
        watch = self.workspace / "scripts/marshal-watch.sh"
        thread = threading.Thread(target=lambda: (self.wait_for(marker), self.replace_same_bytes(watch)))
        thread.start(); self.assert_reason("watch-script-drift"); thread.join(timeout=5)

    def test_late_dirty_worktree_is_rejected(self) -> None:
        marker = self.workspace / "doctor-started"
        self.write_tools(doctor_delay=0.4, doctor_marker=marker); self.refresh_tooling(); self.write_receipt()

        def dirty() -> None:
            self.wait_for(marker)
            (self.worktree / "late-untracked").write_text("late\n")

        thread = threading.Thread(target=dirty)
        thread.start(); self.assert_reason("worktree-status-drift"); thread.join(timeout=5)

    def test_late_head_change_is_rejected(self) -> None:
        marker = self.workspace / "doctor-started"
        self.write_tools(doctor_delay=0.5, doctor_marker=marker); self.refresh_tooling(); self.write_receipt()

        def commit() -> None:
            self.wait_for(marker)
            (self.worktree / "README").write_text("late head\n")
            subprocess.run(["/usr/bin/git", "add", "README"], cwd=self.worktree, check=True)
            subprocess.run(["/usr/bin/git", "-c", "core.hooksPath=/dev/null", "commit", "-qm", "late"], cwd=self.worktree, check=True)

        thread = threading.Thread(target=commit)
        thread.start(); self.assert_reason("worktree-head-drift"); thread.join(timeout=5)

    def test_expiry_and_symlink_are_rejected(self) -> None:
        self.receipt["validUntil"] = (datetime.now(timezone.utc) - timedelta(seconds=1)).isoformat().replace("+00:00", "Z")
        self.write_receipt(); self.assert_reason("receipt-expired")

    def test_schema_is_draft_2020_12_and_accepts_fixture(self) -> None:
        completed = subprocess.run(["go", "run", str(SCHEMA_PROBE), str(SCHEMA), str(self.receipt_path)], cwd=REPOSITORY, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        self.assertEqual(completed.returncode, 0, completed.stderr)
        self.assertIn("draft-2020-12-schema-and-instance-ok", completed.stdout)

    def test_schema_python_and_jq_share_time_and_identity_language(self) -> None:
        module = load_python(VALIDATOR, "admission_validator_language_test")
        zero_identity = json.loads(json.dumps(self.receipt))
        for item in (zero_identity["adapter"]["executable"],
                     zero_identity["tooling"]["marshalExecutable"],
                     zero_identity["tooling"]["watchScript"]):
            item["device"] = 0
            item["inode"] = 0
        module.validate_receipt_shape(zero_identity)
        zero_path = self.operator / "zero-identity.json"
        zero_path.write_bytes(json_bytes(zero_identity))
        jq_result = subprocess.run(["jq", "-e", "-f", str(JQ), str(zero_path)],
                                   stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, text=True)
        self.assertEqual(jq_result.returncode, 0, jq_result.stderr)
        schema_result = subprocess.run(["go", "run", str(SCHEMA_PROBE), str(SCHEMA), str(zero_path)],
                                       cwd=REPOSITORY, text=True,
                                       stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        self.assertEqual(schema_result.returncode, 0, schema_result.stderr)

        invalid_time = json.loads(json.dumps(self.receipt))
        invalid_time["observedAt"] = invalid_time["observedAt"].replace("Z", "+00:00")
        invalid_path = self.operator / "invalid-time.json"
        invalid_path.write_bytes(json_bytes(invalid_time))
        with self.assertRaises(module.AdmissionError) as raised:
            module.parse_time(invalid_time["observedAt"])
        self.assertEqual(raised.exception.reason_code, "receipt-time-invalid")
        jq_result = subprocess.run(["jq", "-e", "-f", str(JQ), str(invalid_path)],
                                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        self.assertNotEqual(jq_result.returncode, 0)
        schema_result = subprocess.run(["go", "run", str(SCHEMA_PROBE), str(SCHEMA), str(invalid_path)],
                                       cwd=REPOSITORY, stdout=subprocess.DEVNULL,
                                       stderr=subprocess.DEVNULL)
        self.assertNotEqual(schema_result.returncode, 0)

    def test_reference_keeps_core_as_the_only_admission_authority(self) -> None:
        reference = (REFERENCES / "admission-and-acceptance.md").read_text(encoding="utf-8")
        for anchor in ("Core 的 `task run` 是唯一 admission authority", "可选诊断",
                       "validate-admission-receipt.py", "reasonCode=operator-receipt-valid",
                       "slotsAvailable>=1", "stdout、validator 输出和日志不得回显这些路径"):
            self.assertIn(anchor, reference)


if __name__ == "__main__":
    unittest.main()
