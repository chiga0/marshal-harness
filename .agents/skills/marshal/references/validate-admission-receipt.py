#!/usr/bin/env python3
"""Fail-closed mechanical validation for an operator-local admission receipt.

The receipt is never Core authority.  This program re-samples every mutable
admission binding immediately before ``task run`` and emits only digests and a
fixed reason code.  It deliberately does not echo executable paths,
environment values, subprocess output, or exception text.
"""

from __future__ import annotations

import argparse
from datetime import datetime, timedelta, timezone
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import selectors
import signal
import stat
import subprocess
import time


MAX_RECEIPT_BYTES = 1 << 20
MAX_STATE_BYTES = 1 << 20
MAX_CONTROL_BYTES = 4 << 20
MAX_EXECUTABLE_BYTES = 512 << 20
MAX_COMMAND_BYTES = 4 << 20
COMMAND_TIMEOUT_SECONDS = 45
DIGEST_RE = re.compile(r"^sha256:[0-9a-f]{64}$")
SHA_RE = re.compile(r"^[0-9a-f]{40}$")
ID_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._:-]{0,159}$")

ADAPTER_ENV = {
    "qoder": "MARSHAL_QODER_PATH",
    "codex": "MARSHAL_CODEX_PATH",
    "qwen": "MARSHAL_QWEN_PATH",
    "pi": "MARSHAL_PI_PATH",
}
MODE_ENV = {"qoder": "MARSHAL_QODER_MODE", "codex": "MARSHAL_CODEX_MODE"}
ALLOWED_LAUNCH_ENV = {
    "PATH", "HOME", "TMPDIR", "LANG", "LC_ALL",
    "MARSHAL_OPENCODE_PATH", "MARSHAL_QWEN_PATH", "MARSHAL_QODER_PATH",
    "MARSHAL_CODEX_PATH", "MARSHAL_PI_PATH", "MARSHAL_QODER_MODE",
    "MARSHAL_CODEX_MODE", "MARSHAL_QODER_CONFORMANCE_CONFIG",
    "MARSHAL_CODEX_AUTHORITY_CONFIG", "MARSHAL_APAP_ENDPOINT",
    "MARSHAL_DARWIN_LAUNCHD_CONFIG", "MARSHAL_QODER_FENCE_DIGEST",
    "MARSHAL_QODER_FENCE_GENERATION", "MARSHAL_QODER_FENCE_HELPER",
    "MARSHAL_QODER_FENCE_ROOT", "MARSHAL_WATCH_NOTIFY",
    "MARSHAL_WATCH_COHORT_FILE", "MARSHAL_WATCH_CURRENT_WINDOW_SECONDS",
    "MARSHAL_WATCH_WORKER_RESERVE_BYTES",
    "MARSHAL_WATCH_PROVIDER_FAILURE_HOLD_SECONDS",
}


class AdmissionError(Exception):
    def __init__(self, reason_code: str):
        super().__init__(reason_code)
        self.reason_code = reason_code


def fail(reason_code: str) -> None:
    raise AdmissionError(reason_code)


class HeldRegular:
    """Keep the sampled regular file open and re-resolve its pathname later."""

    def __init__(self, path: Path, reason: str, limit: int):
        self.path = absolute_clean(path)
        self.reason = reason
        self.limit = limit
        self.parent_fd = open_dir_nofollow(self.path.parent)
        try:
            self.fd = os.open(self.path.name, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0), dir_fd=self.parent_fd)
            self.identity, self.digest = self._sample_fd(self.fd)
        except Exception:
            if hasattr(self, "fd"):
                os.close(self.fd)
            os.close(self.parent_fd)
            raise

    def _sample_fd(self, descriptor: int) -> tuple[tuple[int, int, int, int, int], str]:
        try:
            metadata = os.fstat(descriptor)
            if not stat.S_ISREG(metadata.st_mode) or metadata.st_size > self.limit or metadata.st_mode & 0o111 == 0:
                fail(self.reason)
            os.lseek(descriptor, 0, os.SEEK_SET)
            hasher = hashlib.sha256()
            total = 0
            while True:
                chunk = os.read(descriptor, min(1 << 20, self.limit + 1 - total))
                if not chunk:
                    break
                total += len(chunk)
                if total > self.limit:
                    fail(self.reason)
                hasher.update(chunk)
            final = os.fstat(descriptor)
            identity = (metadata.st_dev, metadata.st_ino, metadata.st_size, metadata.st_mtime_ns, metadata.st_mode)
            if (final.st_dev, final.st_ino, final.st_size, final.st_mtime_ns, final.st_mode) != identity:
                fail(self.reason)
            return identity, "sha256:" + hasher.hexdigest()
        except OSError:
            fail(self.reason)

    def verify_path(self, drift_reason: str) -> None:
        held_identity, held_digest = self._sample_fd(self.fd)
        try:
            parent_fd = open_dir_nofollow(self.path.parent)
            try:
                current_fd = os.open(self.path.name, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0), dir_fd=parent_fd)
                try:
                    current_identity, current_digest = self._sample_fd(current_fd)
                finally:
                    os.close(current_fd)
            finally:
                os.close(parent_fd)
        except AdmissionError:
            fail(drift_reason)
        except OSError:
            fail(drift_reason)
        if held_identity != self.identity or held_digest != self.digest or current_identity != self.identity or current_digest != self.digest:
            fail(drift_reason)

    def close(self) -> None:
        for descriptor in (getattr(self, "fd", None), getattr(self, "parent_fd", None)):
            if descriptor is not None:
                try:
                    os.close(descriptor)
                except OSError:
                    pass


class HeldDirectory:
    """Keep one nofollow directory descriptor across dynamic admission probes."""

    def __init__(self, path: Path):
        self.path = absolute_clean(path)
        self.fd = open_dir_nofollow(self.path)
        try:
            self.identity = self._identity(self.fd)
        except Exception:
            os.close(self.fd)
            raise

    @staticmethod
    def _identity(descriptor: int) -> tuple[int, int, int]:
        metadata = os.fstat(descriptor)
        if not stat.S_ISDIR(metadata.st_mode):
            fail("worktree-identity-invalid")
        return metadata.st_dev, metadata.st_ino, metadata.st_mode

    def verify_path(self) -> None:
        if self._identity(self.fd) != self.identity:
            fail("worktree-identity-drift")
        try:
            current_fd = open_dir_nofollow(self.path)
            try:
                if self._identity(current_fd) != self.identity:
                    fail("worktree-identity-drift")
            finally:
                os.close(current_fd)
        except AdmissionError:
            fail("worktree-identity-drift")

    def close(self) -> None:
        try:
            os.close(self.fd)
        except OSError:
            pass


def canonical_bytes(value: object) -> bytes:
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")


def digest_bytes(data: bytes) -> str:
    return "sha256:" + hashlib.sha256(data).hexdigest()


def canonical_digest(value: object) -> str:
    return digest_bytes(canonical_bytes(value))


def duplicate_rejector(pairs: list[tuple[str, object]]) -> dict:
    result: dict = {}
    for key, value in pairs:
        if key in result:
            fail("duplicate-json-key")
        result[key] = value
    return result


def parse_json(data: bytes, reason: str) -> dict:
    try:
        value = json.loads(
            data.decode("utf-8"), object_pairs_hook=duplicate_rejector,
            parse_constant=lambda _value: fail(reason),
        )
    except AdmissionError:
        raise
    except (UnicodeError, json.JSONDecodeError):
        fail(reason)
    if not isinstance(value, dict):
        fail(reason)
    return value


def parse_time(value: object) -> datetime:
    if not isinstance(value, str) or not value.endswith("Z"):
        fail("receipt-time-invalid")
    try:
        parsed = datetime.fromisoformat(value[:-1] + "+00:00")
    except ValueError:
        fail("receipt-time-invalid")
    return parsed.astimezone(timezone.utc)


def absolute_clean(path: Path) -> Path:
    if not path.is_absolute() or path != Path(os.path.abspath(path)) or ".." in path.parts:
        fail("path-boundary-invalid")
    return path


def clean_relative(value: object) -> str:
    if not isinstance(value, str) or not value or len(value) > 2048 or "\\" in value or "\x00" in value:
        fail("path-boundary-invalid")
    path = Path(value)
    if path.is_absolute() or any(part in {"", ".", ".."} for part in path.parts):
        fail("path-boundary-invalid")
    return path.as_posix()


def open_dir_nofollow(path: Path) -> int:
    absolute_clean(path)
    flags = os.O_RDONLY | getattr(os, "O_DIRECTORY", 0) | getattr(os, "O_NOFOLLOW", 0)
    try:
        descriptor = os.open(path.anchor, flags)
        for component in path.parts[1:]:
            next_descriptor = os.open(component, flags, dir_fd=descriptor)
            os.close(descriptor)
            descriptor = next_descriptor
        return descriptor
    except OSError:
        try:
            os.close(descriptor)
        except (OSError, UnboundLocalError):
            pass
        fail("path-symlink-rejected")


def read_relative(root: Path, relative: object, reason: str, limit: int) -> tuple[bytes, tuple[int, int, int, int]]:
    parts = Path(clean_relative(relative)).parts
    descriptor = open_dir_nofollow(root)
    try:
        for component in parts[:-1]:
            next_descriptor = os.open(component, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0) | getattr(os, "O_NOFOLLOW", 0), dir_fd=descriptor)
            os.close(descriptor)
            descriptor = next_descriptor
        file_descriptor = os.open(parts[-1], os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0), dir_fd=descriptor)
        try:
            metadata = os.fstat(file_descriptor)
            if not stat.S_ISREG(metadata.st_mode) or metadata.st_size > limit:
                fail(reason)
            hasher = hashlib.sha256()
            chunks: list[bytes] = []
            total = 0
            while True:
                chunk = os.read(file_descriptor, min(65536, limit + 1 - total))
                if not chunk:
                    break
                total += len(chunk)
                if total > limit:
                    fail(reason)
                hasher.update(chunk)
                chunks.append(chunk)
            final = os.fstat(file_descriptor)
            identity = (metadata.st_dev, metadata.st_ino, metadata.st_size, metadata.st_mtime_ns)
            if (final.st_dev, final.st_ino, final.st_size, final.st_mtime_ns) != identity:
                fail(reason)
            return b"".join(chunks), identity
        finally:
            os.close(file_descriptor)
    except FileNotFoundError:
        fail(reason)
    except OSError:
        fail("path-symlink-rejected")
    finally:
        os.close(descriptor)


def run_bounded(argv: list[str], cwd: Path, env: dict[str, str], reason: str) -> bytes:
    try:
        process = subprocess.Popen(
            argv, cwd=cwd, env=env, stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True,
        )
    except OSError:
        fail(reason)
    assert process.stdout is not None and process.stderr is not None
    selector = selectors.DefaultSelector()
    selector.register(process.stdout, selectors.EVENT_READ)
    selector.register(process.stderr, selectors.EVENT_READ)
    output = bytearray()
    deadline = time.monotonic() + COMMAND_TIMEOUT_SECONDS
    try:
        while selector.get_map():
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                fail(reason)
            events = selector.select(min(remaining, 0.25))
            for key, _ in events:
                chunk = os.read(key.fileobj.fileno(), 65536)
                if not chunk:
                    selector.unregister(key.fileobj)
                    continue
                output.extend(chunk)
                if len(output) > MAX_COMMAND_BYTES:
                    fail(reason)
        if process.wait(timeout=max(0.1, deadline - time.monotonic())) != 0:
            fail(reason)
        return bytes(output)
    except AdmissionError:
        try:
            os.killpg(process.pid, signal.SIGTERM)
            process.wait(timeout=1)
        except (OSError, subprocess.TimeoutExpired):
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except OSError:
                pass
            process.wait()
        raise
    except subprocess.TimeoutExpired:
        try:
            os.killpg(process.pid, signal.SIGTERM)
            process.wait(timeout=1)
        except (OSError, subprocess.TimeoutExpired):
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except OSError:
                pass
            process.wait()
        fail(reason)
    finally:
        selector.close()


def require_keys(value: object, expected: set[str], reason: str) -> dict:
    if not isinstance(value, dict) or set(value) != expected:
        fail(reason)
    return value


def require_digest(value: object) -> str:
    if not isinstance(value, str) or not DIGEST_RE.fullmatch(value):
        fail("receipt-shape-invalid")
    return value


def require_sha(value: object) -> str:
    if not isinstance(value, str) or not SHA_RE.fullmatch(value):
        fail("receipt-shape-invalid")
    return value


def validate_receipt_shape(receipt: dict) -> None:
    require_keys(receipt, {
        "format", "authority", "taskId", "runId", "observationSequence",
        "stateEventSequence", "observedAt", "validUntil", "bindings", "host",
        "adapter", "worktree", "files", "planApproval", "launchEnvironment",
        "tooling", "dynamicEvidence", "checks", "decision", "reasonCode",
    }, "receipt-shape-invalid")
    if receipt["format"] != "marshal-skill/operator-admission-receipt-v2" or receipt["authority"] != "operator-local-non-core":
        fail("receipt-shape-invalid")
    if not all(isinstance(receipt[field], str) and ID_RE.fullmatch(receipt[field]) for field in ("taskId", "runId")):
        fail("receipt-shape-invalid")
    for field in ("observationSequence", "stateEventSequence"):
        if isinstance(receipt[field], bool) or not isinstance(receipt[field], int) or receipt[field] < 1:
            fail("receipt-shape-invalid")
    bindings = require_keys(receipt["bindings"], {
        "sourceHead", "baseSha", "specDigest", "policyDigest", "capabilityDigest",
        "runStateDigest", "planApprovalDigest", "adapterConfigDigest",
        "eventContractDigest", "resultTransportDigest", "permissionProfileDigest",
    }, "receipt-shape-invalid")
    require_sha(bindings["sourceHead"]); require_sha(bindings["baseSha"])
    for field in set(bindings) - {"sourceHead", "baseSha"}:
        require_digest(bindings[field])
    host = require_keys(receipt["host"], {"os", "arch", "fingerprintDigest"}, "receipt-shape-invalid")
    if host["os"] not in {"darwin", "linux"} or host["arch"] not in {"arm64", "amd64"}:
        fail("receipt-shape-invalid")
    require_digest(host["fingerprintDigest"])
    adapter = require_keys(receipt["adapter"], {
        "id", "mode", "binaryVersion", "permissionMode", "resultPathIdentityDigest", "executable",
    }, "receipt-shape-invalid")
    if adapter["id"] not in ADAPTER_ENV or adapter["mode"] not in {"ordinary-user", "strict"}:
        fail("receipt-shape-invalid")
    if not isinstance(adapter["binaryVersion"], str) or not adapter["binaryVersion"] or not isinstance(adapter["permissionMode"], str) or not adapter["permissionMode"]:
        fail("receipt-shape-invalid")
    require_digest(adapter["resultPathIdentityDigest"])
    executable = require_keys(adapter["executable"], {"canonicalPath", "digest", "device", "inode"}, "receipt-shape-invalid")
    absolute_clean(Path(executable["canonicalPath"])) if isinstance(executable["canonicalPath"], str) else fail("receipt-shape-invalid")
    require_digest(executable["digest"])
    for field in ("device", "inode"):
        if isinstance(executable[field], bool) or not isinstance(executable[field], int) or executable[field] < 0:
            fail("receipt-shape-invalid")
    worktree = require_keys(receipt["worktree"], {"canonicalPath", "headSha", "statusDigest", "scopeLeaseDigest"}, "receipt-shape-invalid")
    absolute_clean(Path(worktree["canonicalPath"])) if isinstance(worktree["canonicalPath"], str) else fail("receipt-shape-invalid")
    require_sha(worktree["headSha"]); require_digest(worktree["statusDigest"]); require_digest(worktree["scopeLeaseDigest"])
    files = require_keys(receipt["files"], {"statePath", "controlRecordsPath"}, "receipt-shape-invalid")
    clean_relative(files["statePath"]); clean_relative(files["controlRecordsPath"])
    approval = require_keys(receipt["planApproval"], {"recordId", "controlSequence"}, "receipt-shape-invalid")
    if not isinstance(approval["recordId"], str) or not ID_RE.fullmatch(approval["recordId"]):
        fail("receipt-shape-invalid")
    if isinstance(approval["controlSequence"], bool) or not isinstance(approval["controlSequence"], int) or approval["controlSequence"] < 1:
        fail("receipt-shape-invalid")
    launch = require_keys(receipt["launchEnvironment"], {"keys", "digest"}, "receipt-shape-invalid")
    if not isinstance(launch["keys"], list) or not launch["keys"] or any(not isinstance(key, str) for key in launch["keys"]):
        fail("launch-environment-invalid")
    if launch["keys"] != sorted(launch["keys"]) or len(launch["keys"]) != len(set(launch["keys"])) or any(key not in ALLOWED_LAUNCH_ENV for key in launch["keys"]):
        fail("launch-environment-invalid")
    require_digest(launch["digest"])
    tooling = require_keys(receipt["tooling"], {"marshalExecutable", "watchScript"}, "receipt-shape-invalid")
    for item in tooling.values():
        require_keys(item, {"digest", "device", "inode"}, "receipt-shape-invalid")
        require_digest(item["digest"])
        if any(isinstance(item[field], bool) or not isinstance(item[field], int) or item[field] < 0 for field in ("device", "inode")):
            fail("receipt-shape-invalid")
    evidence = require_keys(receipt["dynamicEvidence"], {"doctorDigest", "capacityDigest", "providerBackpressureDigest"}, "receipt-shape-invalid")
    for value in evidence.values(): require_digest(value)
    checks = require_keys(receipt["checks"], {
        "stateReady", "currentPlanApproved", "doctorConfigured", "doctorSupported",
        "worktreeClean", "scopeExclusive", "capacityAvailable",
        "providerBackpressureAbsent", "acceptancePure",
    }, "receipt-shape-invalid")
    if any(value is not True for value in checks.values()) or receipt["decision"] != "admit" or receipt["reasonCode"] != "admitted":
        fail("receipt-self-check-denied")


def parse_records(data: bytes) -> list[dict]:
    records: list[dict] = []
    for line in data.splitlines():
        if not line.strip():
            continue
        if len(records) >= 4096:
            fail("control-records-invalid")
        records.append(parse_json(line, "control-records-invalid"))
    return records


def select_approval(receipt: dict, records: list[dict], state: dict) -> dict:
    expected = receipt["planApproval"]
    matches = [record for record in records if record.get("recordId") == expected["recordId"]]
    if len(matches) != 1:
        fail("plan-approval-missing")
    approval = matches[0]
    if approval.get("apiVersion") != "marshal.dev/v1alpha1" or approval.get("kind") != "ApprovalRecord" or approval.get("taskId") != receipt["taskId"] or approval.get("runId") != receipt["runId"] or approval.get("controlSequence") != expected["controlSequence"] or approval.get("gate") != "plan" or approval.get("outcome") != "approved":
        fail("plan-approval-invalid")
    plan_sequences = [
        record.get("controlSequence") for record in records
        if record.get("kind") == "ApprovalRecord" and record.get("taskId") == receipt["taskId"]
        and record.get("runId") == receipt["runId"] and record.get("gate") == "plan"
    ]
    if any(isinstance(sequence, bool) or not isinstance(sequence, int) or sequence < 1 for sequence in plan_sequences) or not plan_sequences or approval["controlSequence"] != max(plan_sequences):
        fail("plan-approval-stale")
    binding = approval.get("binding")
    expected_binding = receipt["bindings"]
    if not isinstance(binding, dict) or any(binding.get(field) != value for field, value in {
        "stateSequence": state["sequence"], "specDigest": expected_binding["specDigest"],
        "policyDigest": expected_binding["policyDigest"], "capabilityDigest": expected_binding["capabilityDigest"],
        "baseSha": expected_binding["baseSha"],
    }.items()):
        fail("plan-approval-binding-mismatch")
    if parse_time(approval.get("createdAt")) > parse_time(receipt["observedAt"]):
        fail("plan-approval-time-invalid")
    if canonical_digest(approval) != expected_binding["planApprovalDigest"]:
        fail("plan-approval-digest-mismatch")
    return approval


def validate_state(receipt: dict, state: dict) -> None:
    bindings = receipt["bindings"]
    if state.get("apiVersion") != "marshal.dev/v1alpha1" or state.get("kind") != "RunState" or state.get("taskId") != receipt["taskId"] or state.get("runId") != receipt["runId"]:
        fail("run-state-identity-mismatch")
    if state.get("state") != "READY" or state.get("sequence") != receipt["observationSequence"] or state.get("sequence") != receipt["stateEventSequence"]:
        fail("run-state-not-ready")
    for field in ("specDigest", "policyDigest", "capabilityDigest", "baseSha"):
        if state.get(field) != bindings[field]:
            fail("run-state-binding-mismatch")
    if state.get("worktreePath") != receipt["worktree"]["canonicalPath"]:
        fail("run-state-worktree-mismatch")
    if canonical_digest(state) != bindings["runStateDigest"]:
        fail("run-state-digest-mismatch")
    if parse_time(state.get("updatedAt")) > parse_time(receipt["observedAt"]):
        fail("run-state-time-invalid")


def launch_environment(receipt: dict) -> dict[str, str]:
    keys = receipt["launchEnvironment"]["keys"]
    values: dict[str, str] = {}
    for key in keys:
        value = os.environ.get(key)
        if value is None or "\x00" in value:
            fail("launch-environment-missing")
        values[key] = value
    if canonical_digest(values) != receipt["launchEnvironment"]["digest"]:
        fail("launch-environment-drift")
    adapter_id = receipt["adapter"]["id"]
    if values.get(ADAPTER_ENV[adapter_id]) != receipt["adapter"]["executable"]["canonicalPath"]:
        fail("launch-executable-binding-mismatch")
    mode_key = MODE_ENV.get(adapter_id)
    if receipt["adapter"]["mode"] == "ordinary-user":
        if mode_key is None or values.get(mode_key) != "ordinary-user":
            fail("launch-authority-mode-mismatch")
    elif mode_key is not None and values.get(mode_key, "") != "":
        fail("launch-authority-mode-mismatch")
    if values.get("MARSHAL_WATCH_NOTIFY") != "0":
        fail("launch-environment-invalid")
    return values


def sample_worktree(receipt: dict, held: HeldDirectory, initial: tuple[str, bytes] | None = None) -> tuple[str, bytes]:
    worktree = held.path
    held.verify_path()
    env = {"PATH": "/usr/bin:/bin"}
    head_raw = run_bounded(["/usr/bin/git", "-C", str(worktree), "rev-parse", "HEAD"], worktree, env, "worktree-git-failed")
    status_raw = run_bounded(["/usr/bin/git", "-C", str(worktree), "status", "--porcelain=v1", "-z", "--untracked-files=all"], worktree, env, "worktree-git-failed")
    held.verify_path()
    try:
        head = head_raw.decode("ascii").strip()
    except UnicodeError:
        fail("worktree-head-mismatch")
    if initial is not None:
        if head != initial[0]:
            fail("worktree-head-drift")
        if status_raw != initial[1]:
            fail("worktree-status-drift")
    elif head != receipt["worktree"]["headSha"] or head != receipt["bindings"]["baseSha"] or head != receipt["bindings"]["sourceHead"]:
        fail("worktree-head-mismatch")
    status_digest = digest_bytes(status_raw)
    if status_raw or status_digest != receipt["worktree"]["statusDigest"]:
        fail("worktree-not-clean" if initial is None else "worktree-status-drift")
    return head, status_raw


def projection_doctor(receipt: dict, report: dict, executable_digest: str) -> dict:
    if report.get("status") != "ok" or not isinstance(report.get("workers"), list):
        fail("doctor-unhealthy")
    workers = [worker for worker in report["workers"] if isinstance(worker, dict) and worker.get("adapterId") == receipt["adapter"]["id"]]
    if len(workers) != 1:
        fail("doctor-adapter-missing")
    worker = workers[0]
    if worker.get("configured") is not True or worker.get("registered") is not True:
        fail("doctor-not-configured")
    if worker.get("compatibility") != "supported":
        fail("doctor-not-supported")
    expected_mode = receipt["adapter"]["mode"]
    actual_mode = worker.get("authorityMode", "")
    if (expected_mode == "ordinary-user" and actual_mode != "ordinary-user") or (expected_mode == "strict" and actual_mode != ""):
        fail("doctor-authority-mode-mismatch")
    if worker.get("binaryVersion") != receipt["adapter"]["binaryVersion"] or worker.get("executableDigest") != executable_digest or worker.get("environmentVariable") != ADAPTER_ENV[receipt["adapter"]["id"]]:
        fail("doctor-binary-identity-mismatch")
    run = report.get("run")
    if not isinstance(run, dict) or run.get("runId") != receipt["runId"] or run.get("status") != "ok" or run.get("snapshotSequence") != receipt["stateEventSequence"] or run.get("state") is None:
        fail("doctor-run-mismatch")
    if canonical_digest(run["state"]) != receipt["bindings"]["runStateDigest"]:
        fail("doctor-run-mismatch")
    return {
        "reportStatus": report["status"], "runStatus": run["status"],
        "snapshotSequence": run["snapshotSequence"], "adapterId": worker["adapterId"],
        "configured": worker["configured"], "registered": worker["registered"],
        "compatibility": worker["compatibility"], "authorityMode": actual_mode,
        "binaryVersion": worker["binaryVersion"], "executableDigest": worker["executableDigest"],
    }


def projection_capacity(receipt: dict, report: dict) -> tuple[dict, dict]:
    if report.get("queueVersion") != "marshal-watch/v2" or report.get("advisoryOnly") is not True:
        fail("capacity-report-invalid")
    generated = parse_time(report.get("generatedAt"))
    now = datetime.now(timezone.utc)
    if generated < parse_time(receipt["observedAt"]) or generated < now - timedelta(seconds=10) or generated > now + timedelta(seconds=5):
        fail("capacity-report-stale")
    capacity = report.get("capacity")
    if not isinstance(capacity, dict):
        fail("capacity-report-invalid")
    slots = capacity.get("slotsAvailable")
    if isinstance(slots, bool) or not isinstance(slots, int) or slots < 1 or capacity.get("pressure") != "ok" or capacity.get("queueSignalStatus") != "ok" or capacity.get("cpuStatus") != "ok" or capacity.get("providerStatus") != "ok":
        fail("capacity-unavailable")
    signals = capacity.get("providerSignals")
    if not isinstance(signals, list):
        fail("provider-backpressure-unknown")
    matching = [signal for signal in signals if isinstance(signal, dict) and signal.get("adapterId") == receipt["adapter"]["id"]]
    if len(matching) != 1 or matching[0].get("status") != "available":
        fail("provider-backpressure-present")
    capacity_projection = {
        "pressure": capacity["pressure"], "queueSignalStatus": capacity["queueSignalStatus"],
        "cpuStatus": capacity["cpuStatus"], "providerStatus": capacity["providerStatus"],
        "capacityAvailable": True,
    }
    signal = matching[0]
    provider_projection = {key: signal[key] for key in ("adapterId", "status", "failureKind", "notBefore") if key in signal}
    return capacity_projection, provider_projection


def validate(args: argparse.Namespace) -> dict:
    operator_root = absolute_clean(Path(args.operator_root))
    run_root = absolute_clean(Path(args.run_root))
    workspace_root = absolute_clean(Path(args.workspace_root))
    receipt_raw, receipt_identity = read_relative(operator_root, args.receipt, "receipt-unreadable", MAX_RECEIPT_BYTES)
    receipt = parse_json(receipt_raw, "receipt-invalid-json")
    validate_receipt_shape(receipt)
    observed = parse_time(receipt["observedAt"]); valid_until = parse_time(receipt["validUntil"]); now = datetime.now(timezone.utc)
    if valid_until < observed or (valid_until - observed).total_seconds() > 60 or now < observed or now > valid_until:
        fail("receipt-expired")
    system = platform.system().lower(); machine = platform.machine().lower()
    arch = "arm64" if machine in {"arm64", "aarch64"} else "amd64" if machine in {"amd64", "x86_64"} else machine
    if receipt["host"]["os"] != system or receipt["host"]["arch"] != arch:
        fail("host-identity-mismatch")

    state_raw, state_identity = read_relative(run_root, receipt["files"]["statePath"], "run-state-unreadable", MAX_STATE_BYTES)
    control_raw, control_identity = read_relative(run_root, receipt["files"]["controlRecordsPath"], "control-records-unreadable", MAX_CONTROL_BYTES)
    state = parse_json(state_raw, "run-state-invalid-json")
    validate_state(receipt, state)
    select_approval(receipt, parse_records(control_raw), state)

    held_files: list[HeldRegular] = []
    held_worktree: HeldDirectory | None = None
    try:
        executable_path = Path(receipt["adapter"]["executable"]["canonicalPath"])
        executable = HeldRegular(executable_path, "adapter-executable-invalid", MAX_EXECUTABLE_BYTES)
        held_files.append(executable)
        expected_executable = receipt["adapter"]["executable"]
        if executable.digest != expected_executable["digest"] or executable.identity[:2] != (expected_executable["device"], expected_executable["inode"]):
            fail("adapter-executable-identity-mismatch")

        marshal_path = workspace_root / "bin/marshal"
        watch_path = workspace_root / "scripts/marshal-watch.sh"
        marshal_tool = HeldRegular(marshal_path, "marshal-executable-invalid", MAX_EXECUTABLE_BYTES)
        held_files.append(marshal_tool)
        watch_tool = HeldRegular(watch_path, "watch-script-invalid", MAX_STATE_BYTES)
        held_files.append(watch_tool)
        for name, held in (("marshalExecutable", marshal_tool), ("watchScript", watch_tool)):
            expected = receipt["tooling"][name]
            if held.digest != expected["digest"] or held.identity[:2] != (expected["device"], expected["inode"]):
                fail("tooling-identity-mismatch")

        env = launch_environment(receipt)
        held_worktree = HeldDirectory(Path(receipt["worktree"]["canonicalPath"]))
        initial_worktree = sample_worktree(receipt, held_worktree)
        doctor_raw = run_bounded([str(marshal_path), "doctor", "--run", receipt["runId"], "--json"], workspace_root, env, "doctor-command-failed")
        doctor = parse_json(doctor_raw, "doctor-output-invalid")
        doctor_projection = projection_doctor(receipt, doctor, executable.digest)
        watch_raw = run_bounded(["/bin/bash", str(watch_path), "--once", "--json"], workspace_root, env, "capacity-command-failed")
        watch = parse_json(watch_raw, "capacity-output-invalid")
        capacity_projection, provider_projection = projection_capacity(receipt, watch)
        evidence = receipt["dynamicEvidence"]
        if canonical_digest(doctor_projection) != evidence["doctorDigest"]:
            fail("doctor-evidence-drift")
        if canonical_digest(capacity_projection) != evidence["capacityDigest"]:
            fail("capacity-evidence-drift")
        if canonical_digest(provider_projection) != evidence["providerBackpressureDigest"]:
            fail("provider-evidence-drift")

        sample_worktree(receipt, held_worktree, initial_worktree)
        executable.verify_path("adapter-executable-drift")
        marshal_tool.verify_path("marshal-executable-drift")
        watch_tool.verify_path("watch-script-drift")
        state_after, state_identity_after = read_relative(run_root, receipt["files"]["statePath"], "run-state-unreadable", MAX_STATE_BYTES)
        control_after, control_identity_after = read_relative(run_root, receipt["files"]["controlRecordsPath"], "control-records-unreadable", MAX_CONTROL_BYTES)
        receipt_after, receipt_identity_after = read_relative(operator_root, args.receipt, "receipt-unreadable", MAX_RECEIPT_BYTES)
        if state_after != state_raw or state_identity_after != state_identity or control_after != control_raw or control_identity_after != control_identity or receipt_after != receipt_raw or receipt_identity_after != receipt_identity:
            fail("admission-evidence-drift")
        return {
            "status": "pass", "reasonCode": "admission-receipt-valid",
            "receiptDigest": digest_bytes(receipt_raw), "stateDigest": canonical_digest(state),
            "approvalDigest": receipt["bindings"]["planApprovalDigest"],
            "executableDigest": executable.digest,
            "launchEnvironmentDigest": receipt["launchEnvironment"]["digest"],
            "doctorDigest": evidence["doctorDigest"], "capacityDigest": evidence["capacityDigest"],
            "providerBackpressureDigest": evidence["providerBackpressureDigest"],
            "worktreeStatusDigest": digest_bytes(initial_worktree[1]),
        }
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
    args = parser.parse_args()
    try:
        result = validate(args)
    except AdmissionError as error:
        result = {"status": "fail", "reasonCode": error.reason_code}
        print(json.dumps(result, sort_keys=True, separators=(",", ":")))
        return 2
    except Exception:
        result = {"status": "fail", "reasonCode": "validator-internal-error"}
        print(json.dumps(result, sort_keys=True, separators=(",", ":")))
        return 2
    print(json.dumps(result, sort_keys=True, separators=(",", ":")))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
