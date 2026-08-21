#!/usr/bin/env python3
"""Read-only, operator-local pre-mortem for one proposed Marshal plan."""

from __future__ import annotations

import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path, PurePosixPath
import stat
import subprocess
import sys
import tempfile


MAX_INPUT_BYTES = 2 << 20
MAX_CHECKER_BYTES = 64 << 20
MAX_OUTPUT_BYTES = 64 << 10


def load_stable_marshal_module():
    path = Path(__file__).with_name("stable_marshal.py")
    spec = importlib.util.spec_from_file_location("marshal_stable_marshal", path)
    if spec is None or spec.loader is None:
        fail("core-probe-invalid")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class PreflightError(Exception):
    def __init__(self, reason_code: str):
        super().__init__(reason_code)
        self.reason_code = reason_code


def fail(reason_code: str) -> None:
    raise PreflightError(reason_code)


def parse_json(data: bytes) -> dict:
    def reject_duplicates(pairs: list[tuple[str, object]]) -> dict:
        result: dict = {}
        for key, value in pairs:
            if key in result:
                fail("duplicate-json-key")
            result[key] = value
        return result

    try:
        value = json.loads(data.decode("utf-8"), object_pairs_hook=reject_duplicates)
    except PreflightError:
        raise
    except (UnicodeError, json.JSONDecodeError):
        fail("invalid-json")
    if not isinstance(value, dict):
        fail("invalid-json")
    return value


def clean_relative_file(value: object) -> str:
    if not isinstance(value, str) or not value or "\\" in value or "\x00" in value:
        fail("path-boundary-invalid")
    path = PurePosixPath(value)
    if path.is_absolute() or value.endswith("/") or any(part in {"", ".", "..", ".marshal"} for part in path.parts):
        fail("path-boundary-invalid")
    return value


def open_root(root: Path) -> int:
    if not root.is_absolute():
        fail("operator-root-invalid")
    components = root.parts[1:]
    if not components or any(component in {"", ".", "..", ".marshal"} for component in components):
        fail("operator-root-invalid")
    current: int | None = None
    try:
        current = os.open("/", os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        for component in components:
            following = os.open(
                component,
                os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW,
                dir_fd=current,
            )
            os.close(current)
            current = following
        metadata = os.fstat(current)
        if not stat.S_ISDIR(metadata.st_mode):
            fail("operator-root-invalid")
        result = current
        current = None
        return result
    except PreflightError:
        raise
    except OSError:
        fail("operator-root-invalid")
    finally:
        if current is not None:
            os.close(current)


def root_identity(descriptor: int) -> tuple[int, int, int, int]:
    metadata = os.fstat(descriptor)
    return metadata.st_dev, metadata.st_ino, metadata.st_mode, metadata.st_nlink


def read_relative(root_descriptor: int, relative: str) -> bytes:
    components = PurePosixPath(clean_relative_file(relative)).parts
    current = os.dup(root_descriptor)
    try:
        for component in components[:-1]:
            next_descriptor = os.open(
                component,
                os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW,
                dir_fd=current,
            )
            os.close(current)
            current = next_descriptor
        descriptor = os.open(components[-1], os.O_RDONLY | os.O_NOFOLLOW, dir_fd=current)
        try:
            before = os.fstat(descriptor)
            if not stat.S_ISREG(before.st_mode) or before.st_size < 1 or before.st_size > MAX_INPUT_BYTES:
                fail("input-file-invalid")
            chunks: list[bytes] = []
            remaining = before.st_size
            while remaining:
                chunk = os.read(descriptor, min(remaining, 1 << 20))
                if not chunk:
                    fail("input-file-drift")
                chunks.append(chunk)
                remaining -= len(chunk)
            if os.read(descriptor, 1):
                fail("input-file-drift")
            after = os.fstat(descriptor)
            identity_before = (before.st_dev, before.st_ino, before.st_mode, before.st_size, before.st_mtime_ns)
            identity_after = (after.st_dev, after.st_ino, after.st_mode, after.st_size, after.st_mtime_ns)
            if identity_before != identity_after:
                fail("input-file-drift")
            return b"".join(chunks)
        finally:
            os.close(descriptor)
    except PreflightError:
        raise
    except OSError:
        fail("input-file-invalid")
    finally:
        os.close(current)


def binding(manifest: dict, name: str) -> tuple[str, str]:
    value = manifest.get(name)
    if not isinstance(value, dict) or set(value) != {"path", "digest"}:
        fail("manifest-shape-invalid")
    relative = clean_relative_file(value.get("path"))
    digest = value.get("digest")
    if not isinstance(digest, str) or len(digest) != 71 or not digest.startswith("sha256:"):
        fail("manifest-shape-invalid")
    try:
        int(digest[7:], 16)
    except ValueError:
        fail("manifest-shape-invalid")
    return relative, digest


def raw_digest(data: bytes) -> str:
    return "sha256:" + hashlib.sha256(data).hexdigest()


def checked_marshal(value: str) -> Path:
    marshal = Path(value)
    if not marshal.is_absolute() or marshal.resolve() != marshal:
        fail("core-probe-invalid")
    try:
        metadata = marshal.lstat()
    except OSError:
        fail("core-probe-invalid")
    if stat.S_ISLNK(metadata.st_mode) or not stat.S_ISREG(metadata.st_mode) or not metadata.st_mode & 0o111:
        fail("core-probe-invalid")
    if metadata.st_size < 1 or metadata.st_size > MAX_CHECKER_BYTES:
        fail("core-probe-invalid")
    return marshal


def run_preflight(root: Path, manifest_relative: str, marshal: Path) -> dict:
    root_descriptor = open_root(root)
    try:
        identity_before = root_identity(root_descriptor)
        manifest_raw = read_relative(root_descriptor, manifest_relative)
        manifest = parse_json(manifest_raw)
        task_relative, task_digest = binding(manifest, "taskSpec")
        policy_relative, policy_digest = binding(manifest, "policySnapshot")
        task_raw = read_relative(root_descriptor, task_relative)
        policy_raw = read_relative(root_descriptor, policy_relative)
        reopened = open_root(root)
        try:
            if root_identity(reopened) != identity_before:
                fail("operator-root-drift")
        finally:
            os.close(reopened)
    finally:
        os.close(root_descriptor)
    if raw_digest(task_raw) != task_digest or raw_digest(policy_raw) != policy_digest:
        fail("input-digest-mismatch")

    schema = Path(__file__).with_name("plan-premortem-preflight.schema.json")
    try:
        schema_raw = schema.read_bytes()
    except OSError:
        fail("operator-schema-unavailable")
    if not 0 < len(schema_raw) <= MAX_INPUT_BYTES:
        fail("operator-schema-unavailable")

    with tempfile.TemporaryDirectory(prefix="marshal-plan-premortem.") as temporary:
        directory = Path(temporary)
        copies = {
            "manifest": ("manifest.json", manifest_raw),
            "task": ("task-spec.json", task_raw),
            "policy": ("policy-snapshot.json", policy_raw),
            "schema": ("schema.json", schema_raw),
        }
        paths: dict[str, Path] = {}
        for name, (filename, data) in copies.items():
            path = directory / filename
            descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
            try:
                written = 0
                while written < len(data):
                    written += os.write(descriptor, data[written:])
                os.fsync(descriptor)
            finally:
                os.close(descriptor)
            paths[name] = path
        try:
            before = marshal.lstat()
            with marshal.open("rb") as executable:
                marshal_raw = executable.read(MAX_CHECKER_BYTES + 1)
            if len(marshal_raw) != before.st_size:
                fail("core-probe-invalid")
            marshal_digest = raw_digest(marshal_raw)
            command = [
                str(marshal),
                "internal",
                "plan-premortem-check",
                "--attestation-ready",
            ]
            command.extend([
                "--manifest", str(paths["manifest"]),
                "--task-spec", str(paths["task"]),
                "--policy-snapshot", str(paths["policy"]),
                "--schema", str(paths["schema"]),
            ])
            environment = {
                "PATH": "/usr/bin:/bin:/usr/sbin:/sbin",
                "LC_ALL": "C",
                **{
                    key: os.environ[key]
                    for key in (
                        "MARSHAL_QODER_PATH", "MARSHAL_QODER_MODE",
                        "MARSHAL_QODER_CONFORMANCE_CONFIG",
                        "MARSHAL_CODEX_PATH", "MARSHAL_CODEX_MODE",
                        "MARSHAL_CODEX_AUTHORITY_CONFIG",
                        "MARSHAL_QWEN_PATH", "MARSHAL_QWEN_MODE",
                        "MARSHAL_PI_PATH", "MARSHAL_PI_MODE",
                    )
                    if key in os.environ
                },
            }
            identity = load_stable_marshal_module()
            try:
                held = identity.hold(marshal)
            except Exception:
                fail("core-probe-invalid")
            process = subprocess.Popen(command, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, env=environment)
            try:
                stdout, stderr, returncode = identity.execute(held, process, b"\0", 20, MAX_OUTPUT_BYTES, MAX_OUTPUT_BYTES, MAX_OUTPUT_BYTES * 2)
            except Exception:
                fail("core-probe-unavailable")
            finally:
                held.close()
            completed = subprocess.CompletedProcess(command, returncode, stdout, stderr)
        except (OSError, subprocess.TimeoutExpired):
            fail("core-probe-unavailable")
    try:
        after = marshal.lstat()
        with marshal.open("rb") as executable:
            after_raw = executable.read(MAX_CHECKER_BYTES + 1)
    except OSError:
        fail("core-probe-invalid")
    if (
        stat.S_ISLNK(after.st_mode)
        or (before.st_dev, before.st_ino, before.st_size, before.st_mtime_ns, before.st_ctime_ns)
        != (after.st_dev, after.st_ino, after.st_size, after.st_mtime_ns, after.st_ctime_ns)
        or raw_digest(after_raw) != marshal_digest
    ):
        fail("core-probe-changed-during-execution")
    if completed.returncode == 0 and completed.stderr:
        fail("core-probe-output-invalid")
    if completed.returncode != 0 and completed.stdout:
        fail("core-probe-output-invalid")
    output = completed.stdout if completed.returncode == 0 else completed.stderr
    if len(output) > MAX_OUTPUT_BYTES:
        fail("core-probe-output-invalid")
    response = parse_json(output)
    allowed = {
        "status", "reasonCode", "taskSpecDigest", "policySnapshotDigest",
        "sourceHead", "selectedAdapter", "authorityMode", "capabilityDigest", "marshal",
    }
    if set(response) - allowed or response.get("status") not in {"pass", "fail"}:
        fail("core-probe-output-invalid")
    reason = response.get("reasonCode")
    if not isinstance(reason, str) or not reason:
        fail("core-probe-output-invalid")
    if response["status"] == "pass":
        if completed.returncode != 0 or reason != "plan-premortem-pass":
            fail("core-probe-output-invalid")
        identity = response.get("marshal")
        if not isinstance(identity, dict) or set(identity) != {"version", "commit", "internalCommandVersion", "inputDigest"}:
            fail("core-probe-output-invalid")
        if not isinstance(identity.get("commit"), str) or len(identity["commit"]) != 40 or any(ch not in "0123456789abcdef" for ch in identity["commit"]):
            fail("core-probe-output-invalid")
        expected_input_digest = raw_digest(manifest_raw + task_raw + policy_raw + schema_raw)
        if identity.get("inputDigest") != expected_input_digest:
            fail("core-probe-input-digest-mismatch")
        if identity.get("internalCommandVersion") != "plan-premortem-check/v1":
            fail("core-probe-command-version-mismatch")
    elif completed.returncode != 1:
        fail("core-probe-output-invalid")
    return response


def emit(value: dict) -> None:
    print(json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")))


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", required=True, help="absolute compact operator root outside .marshal")
    parser.add_argument("--manifest", required=True, help="manifest path relative to --root")
    parser.add_argument("--marshal", required=True, help="absolute stable Marshal executable")
    arguments = parser.parse_args()
    try:
        root = Path(arguments.root)
        marshal = checked_marshal(arguments.marshal)
        response = run_preflight(
            root,
            clean_relative_file(arguments.manifest),
            marshal,
        )
    except PreflightError as error:
        emit({"status": "fail", "reasonCode": error.reason_code})
        return 1
    emit(response)
    return 0 if response["status"] == "pass" else 1


if __name__ == "__main__":
    sys.exit(main())
