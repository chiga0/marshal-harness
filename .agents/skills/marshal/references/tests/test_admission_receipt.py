#!/usr/bin/env python3

from __future__ import annotations

from datetime import datetime, timedelta, timezone
import hashlib
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

    def test_generator_creates_and_immediately_validates_receipt(self) -> None:
        self.launch_env = {
            "HOME": str(self.temp / "home-private-value"),
            "LANG": "C",
            "LC_ALL": "C",
            "MARSHAL_PI_PATH": str(self.adapter),
            "MARSHAL_WATCH_COHORT_FILE": str(self.temp / "cohort.json"),
            "MARSHAL_WATCH_NOTIFY": "0",
            "PATH": "/usr/bin:/bin",
            "TMPDIR": str(self.temp),
        }
        self.doctor_worker.update({"adapterId": "pi", "environmentVariable": "MARSHAL_PI_PATH"})
        self.doctor_worker.pop("authorityMode")
        self.watch_capacity["providerSignals"] = [{"adapterId": "pi", "status": "available"}]
        self.write_tools()
        outside = self.temp / "outside-receipt-target"
        outside.write_text("sentinel\n")
        (self.operator / "generated.json").symlink_to(outside)
        completed = subprocess.run(
            ["/usr/bin/python3", "-I", "-B", str(GENERATOR),
             "--operator-root", str(self.operator),
             "--receipt", "generated.json", "--run-root", str(self.run_root),
             "--workspace-root", str(self.workspace), "--adapter-id", "pi",
             "--adapter-mode", "host-user"],
            cwd=REPOSITORY, env=self.launch_env, text=True,
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False,
        )
        result = json.loads(completed.stdout)
        self.assertEqual((completed.returncode, completed.stderr), (0, ""), result)
        self.assertEqual(result["reasonCode"], "operator-receipt-created-and-valid")
        generated = json.loads((self.operator / "generated.json").read_text())
        self.assertFalse((self.operator / "generated.json").is_symlink())
        self.assertEqual(outside.read_text(), "sentinel\n")
        self.assertEqual(generated["format"], "marshal-skill/operator-admission-receipt-v3")
        self.assertNotIn("scopeExclusive", generated["checks"])
        self.assertNotIn("acceptancePure", generated["checks"])
        rendered = json.dumps(result)
        for secret_or_path in (self.launch_env["HOME"], self.launch_env["MARSHAL_PI_PATH"], self.launch_env["PATH"]):
            self.assertNotIn(secret_or_path, rendered)

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

    def test_reference_keeps_core_as_the_only_admission_authority(self) -> None:
        reference = (REFERENCES / "admission-and-acceptance.md").read_text(encoding="utf-8")
        for anchor in ("Core 的 `task run` 是唯一 admission authority", "可选诊断",
                       "validate-admission-receipt.py", "reasonCode=operator-receipt-valid",
                       "slotsAvailable>=1", "stdout、validator 输出和日志不得回显这些路径"):
            self.assertIn(anchor, reference)


if __name__ == "__main__":
    unittest.main()
