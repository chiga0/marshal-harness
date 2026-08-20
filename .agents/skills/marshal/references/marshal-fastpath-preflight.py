#!/usr/bin/env python3
"""Run the fail-closed operator-local preflight for one Marshal workflow phase."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import signal
import stat
import subprocess
import sys
import time


MAX_INPUT_BYTES = 2 << 20
MAX_OUTPUT_BYTES = 64 << 10
CONTENT_DELIVERABLE_KINDS = {"documentation", "report"}
TEXT_CARRIER_KINDS = {"diagnostic", "other"}
TEXT_APPLICATION_MEDIA_TYPES = {"application/markdown", "application/xml", "application/xhtml+xml"}
SOURCE_HEAD_RE = re.compile(r"^[0-9a-f]{40}$")
DEFAULT_SEMANTIC_TIMEOUT_SECONDS = 180
DEFAULT_PREMORTEM_TIMEOUT_SECONDS = 30
MAX_SEMANTIC_TIMEOUT_SECONDS = 600
MAX_PREMORTEM_TIMEOUT_SECONDS = 120
TERMINATION_GRACE_SECONDS = 0.25
TERMINATION_VERIFY_SECONDS = 1.0


class FastpathError(Exception):
    def __init__(self, reason_code: str, stage: str = "combined"):
        super().__init__(reason_code)
        self.reason_code = reason_code
        self.stage = stage


def fail(reason_code: str, stage: str = "combined") -> None:
    raise FastpathError(reason_code, stage)


def compact(value: object) -> bytes:
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode()


def digest_bytes(data: bytes) -> str:
    return "sha256:" + hashlib.sha256(data).hexdigest()


def digest_object(value: object) -> str:
    return digest_bytes(compact(value))


def parse_json(data: bytes, reason_code: str = "invalid-json") -> dict:
    def reject_duplicates(pairs: list[tuple[str, object]]) -> dict:
        result: dict = {}
        for key, value in pairs:
            if key in result:
                fail("duplicate-json-key")
            result[key] = value
        return result

    try:
        value = json.loads(data.decode("utf-8"), object_pairs_hook=reject_duplicates)
    except FastpathError:
        raise
    except (UnicodeError, json.JSONDecodeError):
        fail(reason_code)
    if not isinstance(value, dict):
        fail(reason_code)
    return value


def clean_relative_file(value: object) -> str:
    if not isinstance(value, str) or not value or "\\" in value or "\x00" in value:
        fail("path-boundary-invalid")
    path = PurePosixPath(value)
    if path.is_absolute() or value.endswith("/") or any(
        component in {"", ".", "..", ".marshal"} for component in path.parts
    ):
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
        result = current
        current = None
        return result
    except OSError:
        fail("operator-root-invalid")
    finally:
        if current is not None:
            os.close(current)


def read_relative(root_descriptor: int, relative: str) -> bytes:
    components = PurePosixPath(clean_relative_file(relative)).parts
    current = os.dup(root_descriptor)
    try:
        for component in components[:-1]:
            following = os.open(
                component,
                os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW,
                dir_fd=current,
            )
            os.close(current)
            current = following
        descriptor = os.open(components[-1], os.O_RDONLY | os.O_NOFOLLOW, dir_fd=current)
        try:
            before = os.fstat(descriptor)
            if not stat.S_ISREG(before.st_mode) or not 0 < before.st_size <= MAX_INPUT_BYTES:
                fail("input-file-invalid")
            data = bytearray()
            while len(data) < before.st_size:
                chunk = os.read(descriptor, min(1 << 20, before.st_size - len(data)))
                if not chunk:
                    fail("input-file-drift")
                data.extend(chunk)
            if os.read(descriptor, 1):
                fail("input-file-drift")
            after = os.fstat(descriptor)
            if (
                before.st_dev,
                before.st_ino,
                before.st_mode,
                before.st_size,
                before.st_mtime_ns,
            ) != (
                after.st_dev,
                after.st_ino,
                after.st_mode,
                after.st_size,
                after.st_mtime_ns,
            ):
                fail("input-file-drift")
            return bytes(data)
        finally:
            os.close(descriptor)
    except FastpathError:
        raise
    except OSError:
        fail("input-file-invalid")
    finally:
        os.close(current)


def plan_inputs(root: Path, plan_manifest_relative: str) -> tuple[dict, str, bytes]:
    descriptor = open_root(root)
    try:
        manifest = parse_json(read_relative(descriptor, plan_manifest_relative))
        task_binding = manifest.get("taskSpec")
        if not isinstance(task_binding, dict) or set(task_binding) != {"path", "digest"}:
            fail("plan-manifest-shape-invalid")
        task_relative = clean_relative_file(task_binding.get("path"))
        task_raw = read_relative(descriptor, task_relative)
    finally:
        os.close(descriptor)
    if task_binding.get("digest") != digest_bytes(task_raw):
        fail("input-digest-mismatch")
    return manifest, task_relative, task_raw


def semantic_evidence(root: Path, manifest_relative: str) -> dict:
    try:
        descriptor = open_root(root)
        try:
            manifest_raw = read_relative(descriptor, manifest_relative)
            manifest = parse_json(manifest_raw)
            fixtures = manifest.get("fixtures")
            if not isinstance(fixtures, dict) or set(fixtures) != {"positive", "negative"}:
                fail("semantic-manifest-shape-invalid")
            records: list[dict] = []
            for fixture_class in ("positive", "negative"):
                entries = fixtures.get(fixture_class)
                if not isinstance(entries, list) or not entries:
                    fail("semantic-manifest-shape-invalid")
                for index, entry in enumerate(entries):
                    if not isinstance(entry, dict):
                        fail("semantic-manifest-shape-invalid")
                    fixture_id = entry.get("id")
                    if not isinstance(fixture_id, str) or not fixture_id:
                        fail("semantic-manifest-shape-invalid")
                    relative = clean_relative_file(entry.get("path"))
                    raw = read_relative(descriptor, relative)
                    raw_digest = digest_bytes(raw)
                    if entry.get("digest") != raw_digest:
                        fail("fixture-digest-mismatch")
                    records.append(
                        {
                            "class": fixture_class,
                            "index": index,
                            "id": fixture_id,
                            "path": relative,
                            "rawDigest": raw_digest,
                        }
                    )
        finally:
            os.close(descriptor)
    except FastpathError as error:
        raise FastpathError(error.reason_code, "acceptance-semantic") from error
    return {
        "semanticManifestDigest": digest_bytes(manifest_raw),
        "fixtureAggregateDigest": digest_object(records),
        "fixtureCount": len(records),
    }


def cross_check_semantic_evidence(before: dict, child_receipt: dict, after: dict) -> dict:
    child = {
        "semanticManifestDigest": child_receipt.get("semanticManifestDigest"),
        "fixtureAggregateDigest": child_receipt.get("fixtureAggregateDigest"),
        "fixtureCount": child_receipt.get("fixtureCount"),
    }
    if before != child or child != after:
        fail("semantic-input-drift", "acceptance-semantic")
    return child


def content_signals(task_spec: dict) -> list[str]:
    signals: list[str] = []
    deliverables = task_spec.get("deliverables")
    if isinstance(deliverables, list) and any(
        isinstance(item, dict)
        and item.get("required") is True
        and item.get("kind") in CONTENT_DELIVERABLE_KINDS
        for item in deliverables
    ):
        signals.append("required-content-deliverable")
    if isinstance(deliverables, list) and any(
        isinstance(item, dict)
        and item.get("required") is True
        and item.get("kind") in TEXT_CARRIER_KINDS
        and isinstance(item.get("mediaType"), str)
        and (
            item["mediaType"].split(";", 1)[0].strip().casefold().startswith("text/")
            or item["mediaType"].split(";", 1)[0].strip().casefold()
            in TEXT_APPLICATION_MEDIA_TYPES
        )
        for item in deliverables
    ):
        signals.append("required-text-carrier")
    acceptance = task_spec.get("acceptance")
    commands = acceptance.get("commands") if isinstance(acceptance, dict) else None
    if isinstance(commands, list):
        for item in commands:
            argv = item.get("argv") if isinstance(item, dict) and item.get("required") is True else None
            if (
                isinstance(argv, list)
                and len(argv) == 5
                and argv[:4] == ["python3", "-I", "-B", "-c"]
                and isinstance(argv[4], str)
                and all(token in argv[4] for token in ("required_all", "required_any", "forbidden"))
            ):
                signals.append("canonical-content-gate")
                break
    return signals


def child_payload(completed: subprocess.CompletedProcess[bytes], stage: str) -> dict:
    output = completed.stdout if completed.stdout else completed.stderr
    if not output or len(output) > MAX_OUTPUT_BYTES:
        fail("preflight-output-invalid", stage)
    try:
        payload = parse_json(output, "preflight-output-invalid")
    except FastpathError as error:
        raise FastpathError(error.reason_code, stage) from error
    status = payload.get("status")
    reason = payload.get("reasonCode")
    if status == "fail" and completed.returncode != 0 and isinstance(reason, str) and reason:
        fail(reason, stage)
    if status != "pass" or completed.returncode != 0:
        fail("preflight-output-invalid", stage)
    return payload


def process_group_exists(group_id: int) -> bool:
    try:
        os.killpg(group_id, 0)
        return True
    except ProcessLookupError:
        return False
    except PermissionError:
        try:
            completed = subprocess.run(
                ["/bin/ps", "-axo", "pgid="],
                stdin=subprocess.DEVNULL,
                stdout=subprocess.PIPE,
                stderr=subprocess.DEVNULL,
                timeout=1,
                check=False,
                text=True,
            )
        except (OSError, subprocess.TimeoutExpired):
            return True
        return completed.returncode != 0 or any(
            value.strip().isdigit() and int(value.strip()) == group_id
            for value in completed.stdout.splitlines()
        )


def terminate_owned_process_group(process: subprocess.Popen[bytes], stage: str) -> None:
    group_id = process.pid
    try:
        os.killpg(group_id, signal.SIGTERM)
    except ProcessLookupError:
        pass
    grace_deadline = time.monotonic() + TERMINATION_GRACE_SECONDS
    while process_group_exists(group_id) and time.monotonic() < grace_deadline:
        process.poll()
        time.sleep(0.01)
    if process_group_exists(group_id):
        try:
            os.killpg(group_id, signal.SIGKILL)
        except (ProcessLookupError, PermissionError):
            pass
    try:
        process.communicate(timeout=TERMINATION_VERIFY_SECONDS)
    except subprocess.TimeoutExpired:
        try:
            os.killpg(group_id, signal.SIGKILL)
        except (ProcessLookupError, PermissionError):
            pass
        process.communicate()
    verify_deadline = time.monotonic() + TERMINATION_VERIFY_SECONDS
    while process_group_exists(group_id) and time.monotonic() < verify_deadline:
        time.sleep(0.01)
    if process_group_exists(group_id):
        fail("preflight-process-group-survived", stage)


def run_child(argv: list[str], stage: str, timeout_seconds: int | float) -> dict:
    try:
        process = subprocess.Popen(
            argv,
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            start_new_session=True,
        )
    except OSError:
        fail("preflight-unavailable", stage)
    try:
        stdout, stderr = process.communicate(timeout=timeout_seconds)
    except subprocess.TimeoutExpired:
        terminate_owned_process_group(process, stage)
        fail(f"{stage}-timeout", stage)
    completed = subprocess.CompletedProcess(argv, process.returncode, stdout, stderr)
    if completed.stdout and completed.stderr:
        fail("preflight-output-invalid", stage)
    return child_payload(completed, stage)


def run_plan(arguments: argparse.Namespace) -> dict:
    root = Path(arguments.root)
    plan_manifest_relative = clean_relative_file(arguments.plan_manifest)
    plan_manifest, task_relative, task_raw = plan_inputs(root, plan_manifest_relative)
    task_digest = digest_bytes(task_raw)
    task_spec = parse_json(task_raw)
    signals = content_signals(task_spec)

    if arguments.task_kind == "content" and not arguments.acceptance_manifest:
        fail("acceptance-semantic-manifest-required", "acceptance-semantic")
    if arguments.task_kind == "non-content" and arguments.acceptance_manifest:
        fail("acceptance-semantic-manifest-unexpected", "acceptance-semantic")
    if arguments.task_kind == "non-content" and signals:
        fail("content-task-semantic-manifest-required", "acceptance-semantic")

    source_head = plan_manifest.get("sourceHead")
    if not isinstance(source_head, str) or not SOURCE_HEAD_RE.fullmatch(source_head):
        fail("source-head-invalid")

    if arguments.task_kind == "content":
        semantic_before = semantic_evidence(
            root, clean_relative_file(arguments.acceptance_manifest)
        )
        acceptance_validator = Path(__file__).with_name("validate-acceptance-semantic-preflight.py")
        acceptance_argv = [
            sys.executable,
            "-I",
            "-B",
            str(acceptance_validator),
            "--root",
            str(root),
            "--manifest",
            str(root / clean_relative_file(arguments.acceptance_manifest)),
            "--task-spec",
            str(root / task_relative),
            "--source-head",
            source_head,
        ]
        for protected_root in arguments.protected_root:
            acceptance_argv.extend(["--protected-root", protected_root])
        acceptance_receipt = run_child(
            acceptance_argv,
            "acceptance-semantic",
            arguments.semantic_timeout_seconds,
        )
        semantic_after = semantic_evidence(
            root, clean_relative_file(arguments.acceptance_manifest)
        )
        semantic_child = cross_check_semantic_evidence(
            semantic_before, acceptance_receipt, semantic_after
        )
        if acceptance_receipt.get("taskSpecDigest") != task_digest:
            fail("acceptance-semantic-binding-mismatch", "acceptance-semantic")
        acceptance_projection = {
            "status": "pass",
            "taskSpecDigest": task_digest,
            "sourceHead": source_head,
            "receiptDigest": digest_object(acceptance_receipt),
            "commandId": acceptance_receipt.get("commandId"),
            "normalizer": acceptance_receipt.get("normalizer"),
            "positiveFixtures": acceptance_receipt.get("positiveFixtures"),
            "negativeFixtures": acceptance_receipt.get("negativeFixtures"),
            **semantic_child,
        }
    else:
        acceptance_projection = {
            "status": "not-applicable",
            "reasonCode": "non-content-task-declared",
            "contentSignals": [],
            "taskSpecDigest": task_digest,
            "sourceHead": source_head,
        }

    plan_validator = Path(__file__).with_name("validate-plan-premortem-preflight.py")
    plan_receipt = run_child(
        [
            sys.executable,
            "-I",
            "-B",
            str(plan_validator),
            "--root",
            str(root),
            "--manifest",
            plan_manifest_relative,
            "--checker",
            arguments.checker,
        ],
        "plan-premortem",
        arguments.premortem_timeout_seconds,
    )
    if plan_receipt.get("taskSpecDigest") != task_digest or plan_receipt.get("sourceHead") != source_head:
        fail("plan-premortem-binding-mismatch", "plan-premortem")

    receipt = {
        "status": "pass",
        "reasonCode": "combined-plan-preflight-pass",
        "phase": "plan",
        "taskKind": arguments.task_kind,
        "taskSpecDigest": task_digest,
        "sourceHead": source_head,
        "selectedAdapter": plan_receipt.get("selectedAdapter"),
        "acceptanceSemantic": acceptance_projection,
        "planPremortem": {
            "status": "pass",
            "taskSpecDigest": task_digest,
            "sourceHead": source_head,
            "receiptDigest": digest_object(plan_receipt),
            "policySnapshotDigest": plan_receipt.get("policySnapshotDigest"),
            "authorityMode": plan_receipt.get("authorityMode"),
            "capabilityDigest": plan_receipt.get("capabilityDigest"),
        },
    }
    receipt["combinedDigest"] = digest_object(receipt)
    return receipt


def emit(value: dict) -> None:
    print(json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")))


def bounded_timeout(maximum: int):
    def parse(value: str) -> int:
        try:
            result = int(value)
        except ValueError as error:
            raise argparse.ArgumentTypeError("timeout must be an integer") from error
        if not 1 <= result <= maximum:
            raise argparse.ArgumentTypeError(f"timeout must be between 1 and {maximum} seconds")
        return result

    return parse


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--phase", required=True, choices=("plan",))
    parser.add_argument("--task-kind", required=True, choices=("content", "non-content"))
    parser.add_argument("--root", required=True, help="absolute compact operator root outside .marshal")
    parser.add_argument("--plan-manifest", required=True, help="plan pre-mortem manifest relative to --root")
    parser.add_argument("--checker", required=True, help="absolute prebuilt Core plan pre-mortem probe")
    parser.add_argument("--acceptance-manifest", help="content semantic manifest relative to --root")
    parser.add_argument("--protected-root", action="append", default=[], help="clean linked worktree bound to sourceHead")
    parser.add_argument(
        "--semantic-timeout-seconds",
        type=bounded_timeout(MAX_SEMANTIC_TIMEOUT_SECONDS),
        default=DEFAULT_SEMANTIC_TIMEOUT_SECONDS,
    )
    parser.add_argument(
        "--premortem-timeout-seconds",
        type=bounded_timeout(MAX_PREMORTEM_TIMEOUT_SECONDS),
        default=DEFAULT_PREMORTEM_TIMEOUT_SECONDS,
    )
    arguments = parser.parse_args()
    try:
        result = run_plan(arguments)
    except FastpathError as error:
        emit({"status": "fail", "reasonCode": error.reason_code, "phase": "plan", "stage": error.stage})
        return 1
    emit(result)
    return 0


if __name__ == "__main__":
    sys.exit(main())
