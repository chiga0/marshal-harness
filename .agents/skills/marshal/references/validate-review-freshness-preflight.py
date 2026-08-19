#!/usr/bin/env python3
"""Read-only, operator-local ReviewPacket freshness and dispatch preflight.

This helper does not mutate Marshal state and is not a marshal.dev authority.
It binds the live REVIEW_PENDING state, current Git head, exact packet bytes,
canonical packet identity and an operator-owned action history.  Its JSON
result tells the caller which single action is admissible; the caller records
that action in the history before performing it.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import stat
import subprocess
import sys


DIGEST_RE = re.compile(r"^sha256:[0-9a-f]{64}$")
ID_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._:-]{0,159}$")
SHA_RE = re.compile(r"^[0-9a-f]{40,64}$")
MAX_JSON_BYTES = 4 << 20


class PreflightError(Exception):
    def __init__(self, reason_code: str):
        super().__init__(reason_code)
        self.reason_code = reason_code


def fail(reason_code: str) -> None:
    raise PreflightError(reason_code)


def digest_bytes(data: bytes) -> str:
    return "sha256:" + hashlib.sha256(data).hexdigest()


def reject_duplicate_pairs(pairs: list[tuple[str, object]]) -> dict:
    result: dict = {}
    for key, value in pairs:
        if key in result:
            fail("duplicate-json-key")
        result[key] = value
    return result


def parse_json(data: bytes, reason_code: str) -> dict:
    if len(data) > MAX_JSON_BYTES:
        fail(reason_code)
    try:
        value = json.loads(data.decode("utf-8"), object_pairs_hook=reject_duplicate_pairs)
    except PreflightError:
        raise
    except (UnicodeError, json.JSONDecodeError):
        fail(reason_code)
    if not isinstance(value, dict):
        fail(reason_code)
    return value


def reject_noncanonical_number_or_key(value: object) -> None:
    if isinstance(value, float):
        # ReviewPacket contains no floating-point contract fields.  Rejecting
        # them avoids silently approximating RFC 8785 number serialization.
        fail("packet-canonicalization-unsupported")
    if isinstance(value, dict):
        for key, child in value.items():
            if not isinstance(key, str) or not key.isascii():
                fail("packet-canonicalization-unsupported")
            reject_noncanonical_number_or_key(child)
    elif isinstance(value, list):
        for child in value:
            reject_noncanonical_number_or_key(child)


def canonical_json(value: object) -> bytes:
    reject_noncanonical_number_or_key(value)
    return json.dumps(
        value, ensure_ascii=False, sort_keys=True, separators=(",", ":")
    ).encode("utf-8")


def canonical_digest(value: object) -> str:
    return digest_bytes(canonical_json(value))


def require_exact_keys(value: object, required: set[str], allowed: set[str]) -> dict:
    if not isinstance(value, dict):
        fail("manifest-shape-invalid")
    if set(value) - allowed or required - set(value):
        fail("manifest-shape-invalid")
    return value


def require_string(value: object, pattern: re.Pattern[str] | None = None) -> str:
    if not isinstance(value, str) or not value:
        fail("manifest-shape-invalid")
    if pattern is not None and not pattern.fullmatch(value):
        fail("manifest-shape-invalid")
    return value


def require_positive_int(value: object) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or value < 1:
        fail("manifest-shape-invalid")
    return value


def clean_relative_path(value: object) -> str:
    raw = require_string(value)
    parts = Path(raw).parts
    if (
        Path(raw).is_absolute()
        or not parts
        or any(part in {"", ".", ".."} for part in parts)
        or "\\" in raw
        or "\x00" in raw
    ):
        fail("path-boundary-invalid")
    return Path(raw).as_posix()


def assert_absolute_tree_without_symlinks(path: Path, require_leaf: bool = True) -> None:
    if not path.is_absolute() or path != Path(os.path.abspath(path)) or ".." in path.parts:
        fail("path-boundary-invalid")
    current = Path(path.anchor)
    parts = path.parts[1:] if path.anchor else path.parts
    for index, part in enumerate(parts):
        current = current / part
        try:
            metadata = current.lstat()
        except OSError:
            if not require_leaf and index == len(parts) - 1:
                return
            fail("path-unreadable")
        if stat.S_ISLNK(metadata.st_mode):
            fail("path-symlink-rejected")


def open_directory_nofollow(path: Path) -> int:
    assert_absolute_tree_without_symlinks(path)
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


def read_absolute_regular_file(path: Path, reason_code: str) -> tuple[bytes, tuple[int, int, int, int]]:
    assert_absolute_tree_without_symlinks(path)
    flags = os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0)
    parent_descriptor = open_directory_nofollow(path.parent)
    try:
        descriptor = os.open(path.name, flags, dir_fd=parent_descriptor)
        try:
            metadata = os.fstat(descriptor)
            if not stat.S_ISREG(metadata.st_mode) or metadata.st_size > MAX_JSON_BYTES:
                fail(reason_code)
            chunks: list[bytes] = []
            remaining = MAX_JSON_BYTES + 1
            while remaining > 0:
                chunk = os.read(descriptor, min(65536, remaining))
                if not chunk:
                    break
                chunks.append(chunk)
                remaining -= len(chunk)
            data = b"".join(chunks)
            if len(data) > MAX_JSON_BYTES:
                fail(reason_code)
            identity = (metadata.st_dev, metadata.st_ino, metadata.st_size, metadata.st_mtime_ns)
            return data, identity
        finally:
            os.close(descriptor)
    except PreflightError:
        raise
    except OSError:
        fail(reason_code)
    finally:
        os.close(parent_descriptor)


def relative_path(root: Path, raw: object) -> Path:
    relative = clean_relative_path(raw)
    return root / relative


def read_relative_regular_file(
    root: Path, raw: object, reason_code: str
) -> tuple[bytes, tuple[int, int, int, int]]:
    path = relative_path(root, raw)
    try:
        path.relative_to(root)
    except ValueError:
        fail("path-boundary-invalid")
    return read_absolute_regular_file(path, reason_code)


def relative_exists(root: Path, raw: object) -> bool:
    path = relative_path(root, raw)
    parent_descriptor = open_directory_nofollow(path.parent)
    try:
        try:
            metadata = os.stat(path.name, dir_fd=parent_descriptor, follow_symlinks=False)
        except FileNotFoundError:
            return False
        except OSError:
            fail("path-unreadable")
        if stat.S_ISLNK(metadata.st_mode):
            fail("path-symlink-rejected")
        if not stat.S_ISREG(metadata.st_mode):
            fail("path-unreadable")
        return True
    finally:
        os.close(parent_descriptor)


def git_head(worktree: Path) -> str:
    assert_absolute_tree_without_symlinks(worktree)
    if not worktree.is_dir():
        fail("worktree-invalid")
    git = "/usr/bin/git" if Path("/usr/bin/git").is_file() else shutil.which("git")
    if not git:
        fail("git-unavailable")
    environment = {
        "PATH": "/usr/bin:/bin:/usr/sbin:/sbin",
        "LC_ALL": "C",
        "GIT_OPTIONAL_LOCKS": "0",
    }
    try:
        result = subprocess.run(
            [git, "-c", "core.fsmonitor=false", "-c", "gc.auto=0", "rev-parse", "HEAD"],
            cwd=worktree,
            env=environment,
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL,
            timeout=5,
            check=False,
        )
    except (OSError, subprocess.TimeoutExpired):
        fail("source-head-unreadable")
    head = result.stdout.decode("ascii", errors="ignore").strip()
    if result.returncode != 0 or not SHA_RE.fullmatch(head):
        fail("source-head-unreadable")
    return head


def validate_manifest_shape(manifest: dict) -> tuple[dict, dict]:
    require_exact_keys(
        manifest,
        {"apiVersion", "kind", "dedupeKey", "expected", "files", "freshnessFingerprint"},
        {"apiVersion", "kind", "dedupeKey", "expected", "files", "freshnessFingerprint"},
    )
    if manifest["apiVersion"] != "marshal.operator/v1alpha1" or manifest["kind"] != "ReviewFreshnessPreflight":
        fail("manifest-shape-invalid")
    require_string(manifest["dedupeKey"], DIGEST_RE)
    require_string(manifest["freshnessFingerprint"], DIGEST_RE)
    expected = require_exact_keys(
        manifest["expected"],
        {"taskId", "runId", "state", "stateSequence", "currentAttemptId", "sourceHead", "baseSha", "reviewRound", "specDigest"},
        {"taskId", "runId", "state", "stateSequence", "currentAttemptId", "sourceHead", "baseSha", "reviewRound", "specDigest", "packetBindings"},
    )
    for key in ("taskId", "runId", "currentAttemptId"):
        require_string(expected[key], ID_RE)
    if expected["state"] != "REVIEW_PENDING":
        fail("manifest-shape-invalid")
    require_positive_int(expected["stateSequence"])
    require_positive_int(expected["reviewRound"])
    require_string(expected["sourceHead"], SHA_RE)
    require_string(expected["baseSha"], SHA_RE)
    require_string(expected["specDigest"], DIGEST_RE)
    files = require_exact_keys(
        manifest["files"],
        {"statePath", "stateRawDigest", "packetPath", "packetPresence", "historyPath", "historyRawDigest"},
        {"statePath", "stateRawDigest", "packetPath", "packetPresence", "packetRawDigest", "packetCanonicalDigest", "historyPath", "historyRawDigest"},
    )
    for key in ("statePath", "packetPath", "historyPath"):
        clean_relative_path(files[key])
    for key in ("stateRawDigest", "historyRawDigest"):
        require_string(files[key], DIGEST_RE)
    if files["packetPresence"] not in {"present", "missing"}:
        fail("manifest-shape-invalid")
    packet_bindings = expected.get("packetBindings")
    if files["packetPresence"] == "present":
        for key in ("packetRawDigest", "packetCanonicalDigest"):
            if key not in files:
                fail("manifest-shape-invalid")
            require_string(files[key], DIGEST_RE)
        bindings = require_exact_keys(
            packet_bindings,
            {"verificationDigest", "artifactManifestDigest", "evidenceDigest", "candidateDigest", "workerCandidateDigest", "workerResultDigests"},
            {"verificationDigest", "artifactManifestDigest", "evidenceDigest", "candidateDigest", "workerCandidateDigest", "workerResultDigests"},
        )
        for key in ("verificationDigest", "artifactManifestDigest", "evidenceDigest", "candidateDigest", "workerCandidateDigest"):
            require_string(bindings[key], DIGEST_RE)
        results = bindings["workerResultDigests"]
        if not isinstance(results, list) or not results or len(set(results)) != len(results):
            fail("manifest-shape-invalid")
        for item in results:
            require_string(item, DIGEST_RE)
    else:
        if packet_bindings is not None or "packetRawDigest" in files or "packetCanonicalDigest" in files:
            fail("manifest-shape-invalid")
    return expected, files


def validate_adjacent_schemas(script_path: Path) -> None:
    for name in ("review-freshness-preflight.schema.json", "review-freshness-history.schema.json"):
        schema_path = script_path.with_name(name)
        raw, _ = read_absolute_regular_file(schema_path, "schema-unreadable")
        schema = parse_json(raw, "schema-invalid")
        if schema.get("$schema") != "https://json-schema.org/draft/2020-12/schema" or schema.get("type") != "object":
            fail("schema-invalid")


def validate_state(state: dict, expected: dict) -> None:
    exact = {
        "taskId": expected["taskId"],
        "runId": expected["runId"],
        "state": expected["state"],
        "sequence": expected["stateSequence"],
        "currentAttemptId": expected["currentAttemptId"],
        "reviewRound": expected["reviewRound"],
        "specDigest": expected["specDigest"],
        "baseSha": expected["baseSha"],
    }
    for key, wanted in exact.items():
        if state.get(key) != wanted:
            if key == "state":
                fail("state-not-review-pending")
            fail("state-identity-mismatch")


def validate_history(history: dict) -> tuple[set[str], set[str]]:
    require_exact_keys(
        history,
        {"apiVersion", "kind", "generationAttempts", "reviewerDispatches"},
        {"apiVersion", "kind", "generationAttempts", "reviewerDispatches"},
    )
    if history["apiVersion"] != "marshal.operator/v1alpha1" or history["kind"] != "ReviewFreshnessHistory":
        fail("history-invalid")
    generation = history["generationAttempts"]
    dispatch = history["reviewerDispatches"]
    if not isinstance(generation, list) or not isinstance(dispatch, list):
        fail("history-invalid")
    generation_keys: set[str] = set()
    dispatch_keys: set[str] = set()
    for entry in generation:
        require_exact_keys(entry, {"dedupeKey"}, {"dedupeKey"})
        key = require_string(entry["dedupeKey"], DIGEST_RE)
        if key in generation_keys:
            fail("history-invalid")
        generation_keys.add(key)
    for entry in dispatch:
        require_exact_keys(entry, {"freshnessFingerprint"}, {"freshnessFingerprint"})
        key = require_string(entry["freshnessFingerprint"], DIGEST_RE)
        if key in dispatch_keys:
            fail("history-invalid")
        dispatch_keys.add(key)
    return generation_keys, dispatch_keys


def validate_packet(packet: dict, expected: dict) -> dict:
    bindings = expected["packetBindings"]
    exact = {
        "taskId": expected["taskId"],
        "runId": expected["runId"],
        "reviewRound": expected["reviewRound"],
        "baseSha": expected["baseSha"],
        "specDigest": expected["specDigest"],
        "verificationDigest": bindings["verificationDigest"],
        "artifactManifestDigest": bindings["artifactManifestDigest"],
        "evidenceDigest": bindings["evidenceDigest"],
        "candidateDigest": bindings["candidateDigest"],
        "workerCandidateDigest": bindings["workerCandidateDigest"],
        "workerResultDigests": bindings["workerResultDigests"],
    }
    if packet.get("apiVersion") != "marshal.dev/v1alpha1" or packet.get("kind") != "ReviewPacket":
        fail("packet-identity-mismatch")
    for key, wanted in exact.items():
        if key not in packet:
            fail("packet-binding-missing")
        if packet[key] != wanted:
            fail("packet-identity-mismatch")
    return bindings


def fingerprint(
    manifest: dict,
    expected: dict,
    packet_marker: dict,
) -> str:
    identity = {
        "schemaVersion": "marshal.operator.review-freshness.v1",
        "dedupeKey": manifest["dedupeKey"],
        "taskId": expected["taskId"],
        "runId": expected["runId"],
        "state": expected["state"],
        "stateSequence": expected["stateSequence"],
        "currentAttemptId": expected["currentAttemptId"],
        "sourceHead": expected["sourceHead"],
        "baseSha": expected["baseSha"],
        "reviewRound": expected["reviewRound"],
        "specDigest": expected["specDigest"],
        "packet": packet_marker,
    }
    return canonical_digest(identity)


def run(arguments: argparse.Namespace) -> dict:
    script_path = Path(__file__).absolute()
    assert_absolute_tree_without_symlinks(script_path)
    validate_adjacent_schemas(script_path)

    run_root = Path(arguments.run_root)
    operator_root = Path(arguments.operator_root)
    worktree = Path(arguments.worktree)
    manifest_path = Path(arguments.manifest)
    for path in (run_root, operator_root, worktree, manifest_path):
        if not path.is_absolute():
            fail("path-boundary-invalid")
    assert_absolute_tree_without_symlinks(run_root)
    assert_absolute_tree_without_symlinks(operator_root)
    assert_absolute_tree_without_symlinks(worktree)
    try:
        manifest_path.relative_to(operator_root)
    except ValueError:
        fail("path-boundary-invalid")

    manifest_raw, _ = read_absolute_regular_file(manifest_path, "manifest-unreadable")
    manifest = parse_json(manifest_raw, "manifest-invalid-json")
    expected, files = validate_manifest_shape(manifest)

    state_raw, state_identity = read_relative_regular_file(run_root, files["statePath"], "state-unreadable")
    if digest_bytes(state_raw) != files["stateRawDigest"]:
        fail("state-raw-digest-mismatch")
    state = parse_json(state_raw, "state-invalid-json")
    validate_state(state, expected)

    history_raw, history_identity = read_relative_regular_file(operator_root, files["historyPath"], "history-unreadable")
    if digest_bytes(history_raw) != files["historyRawDigest"]:
        fail("history-raw-digest-mismatch")
    history = parse_json(history_raw, "history-invalid")
    generation_keys, dispatch_keys = validate_history(history)

    first_head = git_head(worktree)
    if first_head != expected["sourceHead"]:
        fail("source-head-mismatch")

    packet_present = relative_exists(run_root, files["packetPath"])
    declared_present = files["packetPresence"] == "present"
    if packet_present != declared_present:
        fail("packet-presence-mismatch")

    packet_identity = None
    if not packet_present:
        packet_marker = {"presence": "missing"}
        computed_fingerprint = fingerprint(manifest, expected, packet_marker)
        if computed_fingerprint != manifest["freshnessFingerprint"]:
            fail("freshness-fingerprint-mismatch")
        if manifest["dedupeKey"] in generation_keys:
            fail("packet-generation-failed-same-dedupe-key")
        result = {
            "ok": True,
            "action": "generate-review-packet",
            "reasonCode": "packet-missing-first-generation-allowed",
            "freshnessFingerprint": computed_fingerprint,
            "historyRecord": {"generationAttempts": [{"dedupeKey": manifest["dedupeKey"]}]},
        }
    else:
        packet_raw, packet_identity = read_relative_regular_file(run_root, files["packetPath"], "packet-unreadable")
        if digest_bytes(packet_raw) != files["packetRawDigest"]:
            fail("packet-raw-digest-mismatch")
        packet = parse_json(packet_raw, "packet-invalid-json")
        canonical = canonical_digest(packet)
        if canonical != files["packetCanonicalDigest"]:
            fail("packet-canonical-digest-mismatch")
        bindings = validate_packet(packet, expected)
        packet_marker = {
            "presence": "present",
            "rawDigest": files["packetRawDigest"],
            "canonicalDigest": canonical,
            "verificationDigest": bindings["verificationDigest"],
            "artifactManifestDigest": bindings["artifactManifestDigest"],
            "evidenceDigest": bindings["evidenceDigest"],
            "candidateDigest": bindings["candidateDigest"],
            "workerCandidateDigest": bindings["workerCandidateDigest"],
            "workerResultDigests": bindings["workerResultDigests"],
        }
        computed_fingerprint = fingerprint(manifest, expected, packet_marker)
        if computed_fingerprint != manifest["freshnessFingerprint"]:
            fail("freshness-fingerprint-mismatch")
        if computed_fingerprint in dispatch_keys:
            fail("reviewer-dispatch-duplicate-fingerprint")
        result = {
            "ok": True,
            "action": "dispatch-reviewer",
            "reasonCode": "reviewer-dispatch-allowed",
            "freshnessFingerprint": computed_fingerprint,
            "reviewPacketDigest": canonical,
            "historyRecord": {"reviewerDispatches": [{"freshnessFingerprint": computed_fingerprint}]},
        }

    # A replacement, mutation, state transition, new commit, or history update
    # during this read-only preflight invalidates the action before it escapes.
    state_raw_after, state_identity_after = read_relative_regular_file(run_root, files["statePath"], "state-unreadable")
    if state_identity_after != state_identity or state_raw_after != state_raw:
        fail("state-changed-during-preflight")
    history_raw_after, history_identity_after = read_relative_regular_file(operator_root, files["historyPath"], "history-unreadable")
    if history_identity_after != history_identity or history_raw_after != history_raw:
        fail("history-changed-during-preflight")
    if relative_exists(run_root, files["packetPath"]) != packet_present:
        fail("packet-changed-during-preflight")
    if packet_present:
        packet_raw_after, packet_identity_after = read_relative_regular_file(run_root, files["packetPath"], "packet-unreadable")
        if packet_identity_after != packet_identity or packet_raw_after != packet_raw:
            fail("packet-changed-during-preflight")
    if git_head(worktree) != first_head:
        fail("source-head-changed-during-preflight")
    return result


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--run-root", required=True, help="absolute read-only Marshal Run directory containing state and packet")
    parser.add_argument("--operator-root", required=True, help="absolute operator-local directory containing manifest and history")
    parser.add_argument("--manifest", required=True, help="absolute manifest path contained by --operator-root")
    parser.add_argument("--worktree", required=True, help="absolute Git worktree whose HEAD is reviewed")
    arguments = parser.parse_args()
    try:
        result = run(arguments)
    except PreflightError as error:
        print(json.dumps({"ok": False, "action": "intervention", "reasonCode": error.reason_code}, separators=(",", ":")))
        return 2
    print(json.dumps(result, separators=(",", ":"), sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
