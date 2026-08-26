#!/usr/bin/env python3
"""Create a short-lived, non-authoritative operator observation.

This helper deliberately writes only the receipt requested by the operator. It
does not mutate Core state and does not launch a Worker. Dynamic fields are
sampled with the same implementation used by the validator so generation
cannot silently drift from observation semantics. Core ``task run`` remains
the only admission authority; this helper cannot close hostile pathname ABA.
"""

from __future__ import annotations

import argparse
from datetime import datetime, timedelta, timezone
import importlib.util
import json
import os
from pathlib import Path
import platform
import secrets
import stat
import sys


def load_validator():
    path = Path(__file__).with_name("validate-admission-receipt.py")
    spec = importlib.util.spec_from_file_location("marshal_admission_validator", path)
    if spec is None or spec.loader is None:
        raise RuntimeError("validator-import-failed")
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


V = load_validator()

def fail(reason: str) -> None:
    raise V.AdmissionError(reason)


def regular_identity(path: Path, reason: str, limit: int) -> tuple[dict, object]:
    held = V.HeldRegular(path, reason, limit)
    return {
        "digest": held.digest,
        "device": held.identity[0],
        "inode": held.identity[1],
    }, held


def select_latest_approval(records: list[dict], state: dict) -> dict:
    matches = [
        record for record in records
        if record.get("kind") == "ApprovalRecord"
        and record.get("taskId") == state.get("taskId")
        and record.get("runId") == state.get("runId")
        and record.get("gate") == "plan"
        and record.get("outcome") == "approved"
    ]
    if not matches:
        fail("plan-approval-missing")
    if any(isinstance(item.get("controlSequence"), bool) or not isinstance(item.get("controlSequence"), int) for item in matches):
        fail("plan-approval-invalid")
    maximum = max(item["controlSequence"] for item in matches)
    latest = [item for item in matches if item["controlSequence"] == maximum]
    if len(latest) != 1:
        fail("plan-approval-ambiguous")
    return latest[0]


def launch_environment(adapter_id: str, adapter_mode: str) -> tuple[list[str], dict[str, str]]:
    allowed = V.allowed_launch_env(adapter_id)
    keys = sorted(key for key in allowed if key in os.environ)
    required = V.required_launch_env(adapter_id, adapter_mode)
    mode_key = V.MODE_ENV.get(adapter_id)
    polluted = {key for key in V.GOVERNED_LAUNCH_ENV if key in os.environ and key not in allowed}
    if not required.issubset(keys) or polluted:
        fail("launch-environment-invalid")
    values: dict[str, str] = {}
    for key in keys:
        value = os.environ.get(key)
        if value is None or "\x00" in value:
            fail("launch-environment-missing")
        values[key] = value
    executable = values.get(V.ADAPTER_ENV[adapter_id])
    if not executable:
        fail("launch-executable-binding-missing")
    if adapter_mode == "host-user":
        if adapter_id not in {"pi", "qwen"} or mode_key is not None:
            fail("launch-authority-mode-mismatch")
    elif adapter_mode == "ordinary-user":
        if mode_key is None or values.get(mode_key) != "ordinary-user":
            fail("launch-authority-mode-mismatch")
    if values.get("MARSHAL_WATCH_NOTIFY") != "0":
        fail("launch-environment-invalid")
    return keys, values


class OutputTarget:
    """Hold a new, nofollow output parent and never replace an existing name."""

    def __init__(self, operator_root: Path, relative: str):
        relative = V.clean_relative(relative)
        self.path = operator_root / relative
        if ".marshal" in self.path.parts:
            fail("receipt-output-boundary-invalid")
        self.parent_fd = V.open_dir_nofollow(self.path.parent)
        self.parent_identity = self._directory_identity(self.parent_fd)
        try:
            os.stat(self.path.name, dir_fd=self.parent_fd, follow_symlinks=False)
        except FileNotFoundError:
            pass
        except OSError:
            self.close()
            fail("receipt-output-invalid")
        else:
            self.close()
            fail("receipt-output-exists")

    @staticmethod
    def _directory_identity(descriptor: int) -> tuple[int, int]:
        metadata = os.fstat(descriptor)
        if not stat.S_ISDIR(metadata.st_mode):
            fail("receipt-output-invalid")
        return metadata.st_dev, metadata.st_ino

    @staticmethod
    def _path_identity(path: Path) -> tuple[int, int]:
        descriptor = V.open_dir_nofollow(path)
        try:
            return OutputTarget._directory_identity(descriptor)
        finally:
            os.close(descriptor)

    def _verify_parent(self) -> None:
        if self._directory_identity(self.parent_fd) != self.parent_identity:
            fail("receipt-output-drift")
        descriptor = V.open_dir_nofollow(self.path.parent)
        try:
            if self._directory_identity(descriptor) != self.parent_identity:
                fail("receipt-output-drift")
        finally:
            os.close(descriptor)

    def assert_isolated(self, *protected_roots: Path) -> None:
        protected = {self._path_identity(V.absolute_clean(path)) for path in protected_roots}
        current = self.path.parent
        while True:
            if self._path_identity(current) in protected:
                fail("receipt-output-boundary-invalid")
            if current == Path(current.anchor):
                break
            current = current.parent
        self._verify_parent()

    def write(self, receipt: dict) -> None:
        V.validate_receipt_freshness(receipt)
        self._verify_parent()
        payload = json.dumps(receipt, ensure_ascii=False, indent=2, sort_keys=True).encode("utf-8") + b"\n"
        if len(payload) > V.MAX_RECEIPT_BYTES:
            fail("receipt-output-invalid")
        temporary = "." + self.path.name + "." + secrets.token_hex(8) + ".new"
        descriptor = -1
        try:
            descriptor = os.open(
                temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0),
                0o600, dir_fd=self.parent_fd,
            )
            os.fchmod(descriptor, 0o600)
            written = 0
            while written < len(payload):
                written += os.write(descriptor, payload[written:])
            os.fsync(descriptor)
            os.close(descriptor)
            descriptor = -1
            self._verify_parent()
            os.link(temporary, self.path.name, src_dir_fd=self.parent_fd,
                    dst_dir_fd=self.parent_fd, follow_symlinks=False)
            os.unlink(temporary, dir_fd=self.parent_fd)
            temporary = ""
            os.fsync(self.parent_fd)
        except V.AdmissionError:
            raise
        except OSError:
            fail("receipt-output-invalid")
        finally:
            if descriptor >= 0:
                os.close(descriptor)
            if temporary:
                try:
                    os.unlink(temporary, dir_fd=self.parent_fd)
                except OSError:
                    pass

    def close(self) -> None:
        descriptor = getattr(self, "parent_fd", -1)
        if descriptor >= 0:
            try:
                os.close(descriptor)
            except OSError:
                pass
            self.parent_fd = -1


def create(args: argparse.Namespace, output_target: OutputTarget) -> dict:
    operator_root = V.absolute_clean(Path(args.operator_root))
    run_root = V.absolute_clean(Path(args.run_root))
    workspace_root = V.absolute_clean(Path(args.workspace_root))
    state_raw, _ = V.read_relative(run_root, args.state_path, "run-state-unreadable", V.MAX_STATE_BYTES)
    control_raw, _ = V.read_relative(run_root, args.control_records_path, "control-records-unreadable", V.MAX_CONTROL_BYTES)
    state = V.parse_json(state_raw, "run-state-invalid-json")
    if state.get("state") != "READY" or not isinstance(state.get("sequence"), int):
        fail("run-state-not-ready")
    approval = select_latest_approval(V.parse_records(control_raw), state)
    binding = approval.get("binding")
    if not isinstance(binding, dict) or any(binding.get(field) != state.get(field) for field in ("specDigest", "policyDigest", "capabilityDigest", "baseSha")) or binding.get("stateSequence") != state.get("sequence"):
        fail("plan-approval-binding-mismatch")

    launch_keys, env_values = launch_environment(args.adapter_id, args.adapter_mode)
    executable_path = V.absolute_clean(Path(env_values[V.ADAPTER_ENV[args.adapter_id]]))
    worktree_path = V.absolute_clean(Path(state.get("worktreePath", "")))
    output_target.assert_isolated(
        run_root, workspace_root, worktree_path, Path(__file__).resolve().parent.parent,
    )
    held_files: list[object] = []
    held_worktree = None
    try:
        executable_identity, executable = regular_identity(executable_path, "adapter-executable-invalid", V.MAX_EXECUTABLE_BYTES)
        held_files.append(executable)
        marshal_identity, marshal_tool = regular_identity(workspace_root / "bin/marshal", "marshal-executable-invalid", V.MAX_EXECUTABLE_BYTES)
        held_files.append(marshal_tool)
        watch_identity, watch_tool = regular_identity(workspace_root / "scripts/marshal-watch.sh", "watch-script-invalid", V.MAX_STATE_BYTES)
        held_files.append(watch_tool)
        held_worktree = V.HeldDirectory(worktree_path)

        head_raw = V.run_bounded(["/usr/bin/git", "-C", str(worktree_path), "rev-parse", "HEAD"], worktree_path, {"PATH": "/usr/bin:/bin"}, "worktree-git-failed")
        status_raw = V.run_bounded(["/usr/bin/git", "-C", str(worktree_path), "status", "--porcelain=v1", "-z", "--untracked-files=all"], worktree_path, {"PATH": "/usr/bin:/bin"}, "worktree-git-failed")
        head = head_raw.decode("ascii").strip()
        if status_raw or head != state.get("baseSha"):
            fail("worktree-not-clean" if status_raw else "worktree-head-mismatch")

        observed = datetime.now(timezone.utc) - timedelta(seconds=2)
        system = platform.system().lower()
        machine = platform.machine().lower()
        arch = "arm64" if machine in {"arm64", "aarch64"} else "amd64" if machine in {"amd64", "x86_64"} else machine
        receipt = {
            "format": "marshal-skill/operator-admission-receipt-v3",
            "authority": "operator-local-non-core",
            "taskId": state["taskId"],
            "runId": state["runId"],
            "observationSequence": state["sequence"],
            "stateEventSequence": state["sequence"],
            "observedAt": observed.isoformat().replace("+00:00", "Z"),
            "validUntil": (observed + timedelta(seconds=60)).isoformat().replace("+00:00", "Z"),
            "bindings": {
                "sourceHead": head,
                "baseSha": state["baseSha"],
                "specDigest": state["specDigest"],
                "policyDigest": state["policyDigest"],
                "capabilityDigest": state["capabilityDigest"],
                "runStateDigest": V.canonical_digest(state),
                "planApprovalDigest": V.canonical_digest(approval),
            },
            "host": {"os": system, "arch": arch},
            "adapter": {
                "id": args.adapter_id,
                "mode": args.adapter_mode,
                "binaryVersion": "pending-doctor-projection",
                "executable": {"canonicalPath": str(executable_path), **executable_identity},
            },
            "worktree": {
                "canonicalPath": str(worktree_path),
                "headSha": head,
                "statusDigest": V.digest_bytes(status_raw),
            },
            "files": {"statePath": args.state_path, "controlRecordsPath": args.control_records_path},
            "planApproval": {"recordId": approval["recordId"], "controlSequence": approval["controlSequence"]},
            "launchEnvironment": {"keys": launch_keys, "digest": V.canonical_digest(env_values)},
            "tooling": {"marshalExecutable": marshal_identity, "watchScript": watch_identity},
            "dynamicEvidence": {},
            "checks": {key: True for key in (
                "stateReady", "currentPlanApproved", "doctorConfigured", "doctorSupported",
                "worktreeClean", "capacityAvailable", "providerBackpressureAbsent",
            )},
            "decision": "observe",
            "reasonCode": "operator-sampled",
        }

        doctor_raw = V.run_bounded([str(workspace_root / "bin/marshal"), "doctor", "--run", state["runId"], "--json"], workspace_root, env_values, "doctor-command-failed")
        doctor = V.parse_json(doctor_raw, "doctor-output-invalid")
        workers = [worker for worker in doctor.get("workers", []) if isinstance(worker, dict) and worker.get("adapterId") == args.adapter_id]
        if len(workers) != 1 or not isinstance(workers[0].get("binaryVersion"), str) or not workers[0]["binaryVersion"]:
            fail("doctor-adapter-missing")
        receipt["adapter"]["binaryVersion"] = workers[0]["binaryVersion"]
        doctor_projection = V.projection_doctor(receipt, doctor, executable.digest)

        watch_raw = V.run_bounded(["/bin/bash", str(workspace_root / "scripts/marshal-watch.sh"), "--once", "--json"], workspace_root, env_values, "capacity-command-failed")
        watch = V.parse_json(watch_raw, "capacity-output-invalid")
        capacity_projection, provider_projection = V.projection_capacity(receipt, watch)
        receipt["dynamicEvidence"] = {
            "doctorDigest": V.canonical_digest(doctor_projection),
            "capacityDigest": V.canonical_digest(capacity_projection),
            "providerBackpressureDigest": V.canonical_digest(provider_projection),
        }

        held_worktree.verify_path()
        for held, reason in ((executable, "adapter-executable-drift"), (marshal_tool, "marshal-executable-drift"), (watch_tool, "watch-script-drift")):
            held.verify_path(reason)
        return receipt
    finally:
        if held_worktree is not None:
            held_worktree.close()
        for held in reversed(held_files):
            held.close()


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--operator-root", required=True)
    parser.add_argument("--receipt", required=True)
    parser.add_argument("--run-root", required=True)
    parser.add_argument("--workspace-root", required=True)
    parser.add_argument("--adapter-id", required=True, choices=sorted(V.ADAPTER_ENV))
    parser.add_argument("--adapter-mode", required=True, choices=("host-user", "ordinary-user"))
    parser.add_argument("--state-path", default="state.json")
    parser.add_argument("--control-records-path", default="control/records.jsonl")
    args = parser.parse_args()
    output_target = None
    try:
        root = V.absolute_clean(Path(args.operator_root))
        output_target = OutputTarget(root, args.receipt)
        receipt = create(args, output_target)
        output_target.write(receipt)
        validation = V.validate(argparse.Namespace(
            operator_root=args.operator_root,
            receipt=args.receipt,
            run_root=args.run_root,
            workspace_root=args.workspace_root,
        ))
        result = {"status": "pass", "reasonCode": "operator-receipt-created-and-valid", **{key: value for key, value in validation.items() if key.endswith("Digest")}}
        print(json.dumps(result, sort_keys=True, separators=(",", ":")))
        return 0
    except V.AdmissionError as error:
        print(json.dumps({"status": "fail", "reasonCode": error.reason_code}, sort_keys=True, separators=(",", ":")))
        return 2
    except Exception:
        print(json.dumps({"status": "fail", "reasonCode": "generator-internal-error"}, sort_keys=True, separators=(",", ":")))
        return 2
    finally:
        if output_target is not None:
            output_target.close()


if __name__ == "__main__":
    raise SystemExit(main())
