#!/usr/bin/env python3
"""Fail-closed ReviewPacket freshness preflight and atomic action claim.

Marshal Run data is read only.  The only write is an operator-local history
claim, committed with an O_EXCL lock, raw-history CAS and atomic rename before
the selected action is returned. Marshal Core remains the JSON authority;
the fixed `bin/marshal internal review-freshness-check` command imports the
canonical, contract, domain and verification implementations directly.
"""

from __future__ import annotations

import argparse
import atexit
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import selectors
import stat
import subprocess
import tempfile
import threading


DIGEST_RE = re.compile(r"^sha256:[0-9a-f]{64}$")
SHA_RE = re.compile(r"^[0-9a-f]{40,64}$")
MAX_FILE_BYTES = 128 << 20
CORE_KINDS = {
    "RunState", "ReviewPacket", "Task", "PolicySnapshot",
    "CapabilitySnapshot", "VerificationReport", "ArtifactManifest",
    "WorkerResult", "Candidate", "ReviewDecision", "RunEvent", "ApprovalRecord", "InterventionRecord",
}
_CORE_BINARY: Path | None = None
_CORE_PROCESS: subprocess.Popen[str] | None = None
_CORE_HELD = None
_CORE_STDERR_LIMIT = 64 << 10
_CORE_STDERR = bytearray()
_CORE_STDERR_OVERFLOW = False
_CORE_STDERR_THREAD: threading.Thread | None = None


class PreflightError(Exception):
    def __init__(self, reason_code: str):
        super().__init__(reason_code)
        self.reason_code = reason_code


def load_stable_marshal_module():
    path = Path(__file__).with_name("stable_marshal.py")
    spec = importlib.util.spec_from_file_location("marshal_stable_marshal", path)
    if spec is None or spec.loader is None:
        fail("core-probe-unavailable")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class HeldRelativeParent:
    """Hold every directory in a relative parent chain for atomic leaf I/O."""

    def __init__(self, root: Path, relative: object):
        clean = clean_relative(relative)
        parts = Path(clean).parts
        self.root = absolute_clean(root)
        self.leaf = parts[-1]
        self.components = list(parts[:-1])
        self.fds = [open_dir_nofollow(root)]
        try:
            for component in self.components:
                descriptor = os.open(component, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0) | getattr(os, "O_NOFOLLOW", 0), dir_fd=self.fds[-1])
                self.fds.append(descriptor)
        except OSError:
            self.close()
            fail("history-parent-invalid")
        self.identities = [self._directory_identity(descriptor) for descriptor in self.fds]
        atexit.register(self.close)

    @staticmethod
    def _directory_identity(descriptor: int) -> tuple[int, int, int]:
        metadata = os.fstat(descriptor)
        if not stat.S_ISDIR(metadata.st_mode):
            fail("history-parent-invalid")
        return metadata.st_dev, metadata.st_ino, metadata.st_mode

    @property
    def parent_fd(self) -> int:
        if not self.fds:
            fail("history-parent-invalid")
        return self.fds[-1]

    def verify(self) -> None:
        current_root = open_dir_nofollow(self.root)
        try:
            if self._directory_identity(current_root) != self.identities[0]:
                fail("history-parent-changed")
        finally:
            os.close(current_root)
        for index, component in enumerate(self.components):
            try:
                metadata = os.stat(component, dir_fd=self.fds[index], follow_symlinks=False)
            except OSError:
                fail("history-parent-changed")
            identity = (metadata.st_dev, metadata.st_ino, metadata.st_mode)
            if stat.S_ISLNK(metadata.st_mode) or identity != self.identities[index + 1]:
                fail("history-parent-changed")

    def read(self, reason: str, limit: int) -> tuple[bytes, tuple[int, int, int, int]]:
        self.verify()
        return read_regular_at(self.parent_fd, self.leaf, reason, limit)

    def close(self) -> None:
        while getattr(self, "fds", []):
            os.close(self.fds.pop())


class HeldAbsoluteDirectory:
    """Hold and later revalidate every component of an absolute directory."""

    def __init__(self, path: Path):
        self.path = absolute_clean(path)
        flags = os.O_RDONLY | getattr(os, "O_DIRECTORY", 0) | getattr(os, "O_NOFOLLOW", 0)
        self.fds = [os.open(self.path.anchor, flags)]
        try:
            for component in self.path.parts[1:]:
                self.fds.append(os.open(component, flags, dir_fd=self.fds[-1]))
        except OSError:
            self.close()
            fail("authority-namespace-derivation-invalid")
        self.identities = [self._identity(descriptor) for descriptor in self.fds]
        atexit.register(self.close)

    @staticmethod
    def _identity(descriptor: int) -> tuple[int, int, int]:
        metadata = os.fstat(descriptor)
        if not stat.S_ISDIR(metadata.st_mode):
            fail("authority-namespace-derivation-invalid")
        return metadata.st_dev, metadata.st_ino, metadata.st_mode

    def verify(self, reason: str) -> None:
        flags = os.O_RDONLY | getattr(os, "O_DIRECTORY", 0) | getattr(os, "O_NOFOLLOW", 0)
        try:
            current_root = os.open(self.path.anchor, flags)
        except OSError:
            fail(reason)
        try:
            if self._identity(current_root) != self.identities[0]:
                fail(reason)
        finally:
            os.close(current_root)
        for index, component in enumerate(self.path.parts[1:]):
            try:
                metadata = os.stat(component, dir_fd=self.fds[index], follow_symlinks=False)
            except OSError:
                fail(reason)
            identity = (metadata.st_dev, metadata.st_ino, metadata.st_mode)
            if stat.S_ISLNK(metadata.st_mode) or identity != self.identities[index + 1]:
                fail(reason)

    def close(self) -> None:
        while getattr(self, "fds", []):
            os.close(self.fds.pop())


def fail(reason_code: str) -> None:
    raise PreflightError(reason_code)


def raw_digest(data: bytes) -> str:
    return "sha256:" + hashlib.sha256(data).hexdigest()


def reject_duplicate_pairs(pairs: list[tuple[str, object]]) -> dict:
    value: dict = {}
    for key, child in pairs:
        if key in value:
            fail("duplicate-json-key")
        value[key] = child
    return value


def parse_json(data: bytes, reason: str) -> dict:
    try:
        value = json.loads(data.decode("utf-8"), object_pairs_hook=reject_duplicate_pairs)
    except PreflightError:
        raise
    except (UnicodeError, json.JSONDecodeError):
        fail(reason)
    if not isinstance(value, dict):
        fail(reason)
    return value


def clean_relative(value: object) -> str:
    if not isinstance(value, str) or not value or len(value) > 2048 or "\\" in value or "\x00" in value:
        fail("path-boundary-invalid")
    segments = value.split("/")
    if any(segment in {"", ".", ".."} for segment in segments):
        fail("path-boundary-invalid")
    path = Path(value)
    if path.is_absolute() or any(part in {"", ".", ".."} for part in path.parts):
        fail("path-boundary-invalid")
    return path.as_posix()


def absolute_clean(path: Path) -> Path:
    if not path.is_absolute() or ".." in path.parts or "\x00" in str(path):
        fail("path-boundary-invalid")
    # macOS exposes /var as a compatibility symlink to /private/var. Resolve
    # that alias once, then hold every real component with O_NOFOLLOW. This
    # preserves the anti-swap invariant without rejecting a legal system path.
    resolved = Path(os.path.realpath(path))
    if not resolved.is_absolute() or ".." in resolved.parts:
        fail("path-boundary-invalid")
    return resolved


def open_dir_nofollow(path: Path) -> int:
    path = absolute_clean(path)
    flags = os.O_RDONLY | getattr(os, "O_DIRECTORY", 0) | getattr(os, "O_NOFOLLOW", 0)
    descriptor = os.open(path.anchor, flags)
    try:
        for component in path.parts[1:]:
            next_descriptor = os.open(component, flags, dir_fd=descriptor)
            os.close(descriptor)
            descriptor = next_descriptor
        return descriptor
    except OSError:
        os.close(descriptor)
        fail("path-symlink-rejected")


def read_regular(root: Path, relative: object, reason: str, limit: int = MAX_FILE_BYTES) -> tuple[bytes, tuple[int, int, int, int]]:
    clean = clean_relative(relative)
    parts = Path(clean).parts
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
            chunks: list[bytes] = []
            remaining = limit + 1
            while remaining:
                chunk = os.read(file_descriptor, min(65536, remaining))
                if not chunk:
                    break
                chunks.append(chunk)
                remaining -= len(chunk)
            data = b"".join(chunks)
            if len(data) > limit:
                fail(reason)
            return data, (metadata.st_dev, metadata.st_ino, metadata.st_size, metadata.st_mtime_ns)
        finally:
            os.close(file_descriptor)
    except FileNotFoundError:
        fail(reason)
    except OSError:
        fail("path-symlink-rejected")
    finally:
        os.close(descriptor)


def read_regular_at(parent_fd: int, leaf: str, reason: str, limit: int = MAX_FILE_BYTES) -> tuple[bytes, tuple[int, int, int, int]]:
    try:
        file_descriptor = os.open(leaf, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0), dir_fd=parent_fd)
    except FileNotFoundError:
        fail(reason)
    except OSError:
        fail("path-symlink-rejected")
    try:
        metadata = os.fstat(file_descriptor)
        if not stat.S_ISREG(metadata.st_mode) or metadata.st_size > limit:
            fail(reason)
        chunks: list[bytes] = []
        remaining = limit + 1
        while remaining:
            chunk = os.read(file_descriptor, min(65536, remaining))
            if not chunk:
                break
            chunks.append(chunk)
            remaining -= len(chunk)
        data = b"".join(chunks)
        if len(data) > limit:
            fail(reason)
        return data, (metadata.st_dev, metadata.st_ino, metadata.st_size, metadata.st_mtime_ns)
    finally:
        os.close(file_descriptor)


def enumerate_worker_results(run_root: Path) -> dict[str, tuple[bytes, tuple[int, int, int, int]]]:
    root_fd = open_dir_nofollow(run_root)
    try:
        try:
            attempts_fd = os.open("attempts", os.O_RDONLY | getattr(os, "O_DIRECTORY", 0) | getattr(os, "O_NOFOLLOW", 0), dir_fd=root_fd)
        except OSError:
            fail("worker-results-directory-invalid")
    finally:
        os.close(root_fd)
    results: dict[str, tuple[bytes, tuple[int, int, int, int]]] = {}
    try:
        try:
            attempt_entries = sorted(os.listdir(attempts_fd))
        except OSError:
            fail("worker-results-directory-invalid")
        for attempt_id in attempt_entries:
            try:
                metadata = os.stat(attempt_id, dir_fd=attempts_fd, follow_symlinks=False)
            except OSError:
                fail("worker-results-directory-invalid")
            if stat.S_ISLNK(metadata.st_mode):
                fail("worker-results-path-symlink-rejected")
            if not stat.S_ISDIR(metadata.st_mode):
                continue
            try:
                attempt_fd = os.open(attempt_id, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0) | getattr(os, "O_NOFOLLOW", 0), dir_fd=attempts_fd)
            except OSError:
                fail("worker-results-path-symlink-rejected")
            try:
                try:
                    result_metadata = os.stat("worker-result.json", dir_fd=attempt_fd, follow_symlinks=False)
                except FileNotFoundError:
                    continue
                if stat.S_ISLNK(result_metadata.st_mode):
                    fail("path-symlink-rejected")
                if not stat.S_ISREG(result_metadata.st_mode):
                    fail("worker-result-not-regular")
                relative = f"attempts/{attempt_id}/worker-result.json"
                results[relative] = read_regular_at(attempt_fd, "worker-result.json", "packet-input-unreadable")
            finally:
                os.close(attempt_fd)
    finally:
        os.close(attempts_fd)
    return results


def regular_presence(root: Path, relative: object) -> bool:
    clean = clean_relative(relative)
    path = root / clean
    descriptor = open_dir_nofollow(path.parent)
    try:
        try:
            metadata = os.stat(path.name, dir_fd=descriptor, follow_symlinks=False)
        except FileNotFoundError:
            return False
        if stat.S_ISLNK(metadata.st_mode):
            fail("path-symlink-rejected")
        if not stat.S_ISREG(metadata.st_mode):
            fail("path-unreadable")
        return True
    finally:
        os.close(descriptor)


def exact_keys(value: object, required: set[str], allowed: set[str], reason: str = "manifest-shape-invalid") -> dict:
    if not isinstance(value, dict) or required - set(value) or set(value) - allowed:
        fail(reason)
    return value


def checked_stable_marshal(value: str) -> Path:
    path = Path(value)
    if not path.is_absolute() or path.resolve() != path:
        fail("core-probe-unavailable")
    try:
        metadata = path.lstat()
    except OSError:
        fail("core-probe-unavailable")
    if stat.S_ISLNK(metadata.st_mode) or not stat.S_ISREG(metadata.st_mode) or metadata.st_mode & 0o111 == 0:
        fail("core-probe-unavailable")
    return path


def core_binary(script: Path) -> Path:
    global _CORE_BINARY
    if _CORE_BINARY is not None:
        return _CORE_BINARY
    fail("core-probe-unavailable")


def close_core_process() -> None:
    global _CORE_PROCESS, _CORE_HELD, _CORE_STDERR_THREAD
    if _CORE_PROCESS is None:
        return
    if _CORE_PROCESS.stdin is not None:
        _CORE_PROCESS.stdin.close()
    try:
        _CORE_PROCESS.wait(timeout=2)
    except subprocess.TimeoutExpired:
        _CORE_PROCESS.terminate()
        _CORE_PROCESS.wait(timeout=2)
    _CORE_PROCESS = None
    if _CORE_STDERR_THREAD is not None:
        _CORE_STDERR_THREAD.join(timeout=2)
        _CORE_STDERR_THREAD = None
    if _CORE_HELD is not None:
        _CORE_HELD.close()
        _CORE_HELD = None


def _drain_core_stderr(stream) -> None:
    global _CORE_STDERR_OVERFLOW
    try:
        while True:
            chunk = os.read(stream.fileno(), 8192)
            if not chunk:
                return
            if len(_CORE_STDERR) < _CORE_STDERR_LIMIT:
                _CORE_STDERR.extend(chunk[: _CORE_STDERR_LIMIT - len(_CORE_STDERR)])
            if len(_CORE_STDERR) + len(chunk) > _CORE_STDERR_LIMIT:
                _CORE_STDERR_OVERFLOW = True
    except OSError:
        return


def _assert_core_stderr_clean() -> None:
    if _CORE_STDERR_OVERFLOW or _CORE_STDERR:
        close_core_process()
        fail("core-probe-output-invalid")


def core_process(script: Path) -> subprocess.Popen[str]:
    global _CORE_PROCESS, _CORE_HELD, _CORE_STDERR, _CORE_STDERR_OVERFLOW, _CORE_STDERR_THREAD
    if _CORE_PROCESS is not None and _CORE_PROCESS.poll() is None:
        return _CORE_PROCESS
    command = [str(core_binary(script)), "internal", "review-freshness-check", "--attestation-ready"]
    environment = {"PATH": "/usr/bin:/bin:/usr/sbin:/sbin", "LC_ALL": "C"}
    identity = load_stable_marshal_module()
    try:
        _CORE_HELD = identity.hold(core_binary(script))
        _CORE_STDERR = bytearray()
        _CORE_STDERR_OVERFLOW = False
        _CORE_PROCESS = subprocess.Popen(command, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, bufsize=1, env=environment)
        assert _CORE_PROCESS.stderr is not None
        _CORE_STDERR_THREAD = threading.Thread(target=_drain_core_stderr, args=(_CORE_PROCESS.stderr,), daemon=True)
        _CORE_STDERR_THREAD.start()
        identity.attest(_CORE_HELD, _CORE_PROCESS)
        if _CORE_PROCESS.stdin is None:
            fail("core-probe-unavailable")
        _CORE_PROCESS.stdin.write("\0")
        _CORE_PROCESS.stdin.flush()
    except Exception:
        if _CORE_PROCESS is not None:
            _CORE_PROCESS.terminate()
            _CORE_PROCESS.wait(timeout=2)
            _CORE_PROCESS = None
        if _CORE_HELD is not None:
            _CORE_HELD.close()
            _CORE_HELD = None
        fail("core-probe-unavailable")
    atexit.register(close_core_process)
    return _CORE_PROCESS


def core_probe(script: Path, mode: str, kind_or_schema: str, path: Path) -> str:
    process = core_process(script)
    if process.stdin is None or process.stdout is None:
        fail("core-probe-unavailable")
    try:
        process.stdin.write(json.dumps({"mode": mode, "kindOrSchema": kind_or_schema, "path": str(path)}, separators=(",", ":")) + "\n")
        process.stdin.flush()
        selector = selectors.DefaultSelector()
        try:
            selector.register(process.stdout, selectors.EVENT_READ)
            if not selector.select(timeout=30):
                fail("core-probe-timeout")
        finally:
            selector.close()
        line = process.stdout.readline(64 << 10)
        if len(line) > 64 << 10:
            fail("core-probe-output-invalid")
        _assert_core_stderr_clean()
        response = json.loads(line)
    except (BrokenPipeError, OSError, UnicodeError, json.JSONDecodeError):
        fail("core-probe-unavailable")
    if not isinstance(response, dict) or response.get("ok") is not True or not isinstance(response.get("digest"), str):
        if mode == "schema": fail("operator-schema-invalid")
        if mode == "contract": fail("core-contract-invalid")
        if mode == "candidate": fail("candidate-record-core-invalid")
        if mode == "observe": fail("worktree-observation-invalid")
        fail("core-canonicalization-rejected")
    return response["digest"]


def write_temp_json(value: object) -> Path:
    descriptor, name = tempfile.mkstemp(prefix="marshal-review-freshness.", suffix=".json", dir="/private/tmp")
    try:
        os.write(descriptor, json.dumps(value, ensure_ascii=False, separators=(",", ":")).encode("utf-8"))
    finally:
        os.close(descriptor)
    return Path(name)


def core_digest_value(script: Path, value: object) -> str:
    path = write_temp_json(value)
    try:
        return core_probe(script, "canonical", "-", path)
    finally:
        path.unlink(missing_ok=True)


def core_validate_bytes(script: Path, kind: str, data: bytes) -> str:
    if kind not in CORE_KINDS:
        fail("core-contract-kind-invalid")
    descriptor, name = tempfile.mkstemp(prefix="marshal-review-input.", suffix=".json", dir="/private/tmp")
    try:
        os.write(descriptor, data)
    finally:
        os.close(descriptor)
    path = Path(name)
    try:
        return core_probe(script, "contract", kind, path)
    finally:
        path.unlink(missing_ok=True)


def core_validate_candidate(script: Path, data: bytes) -> str:
    descriptor, name = tempfile.mkstemp(prefix="marshal-review-candidate.", suffix=".json", dir="/private/tmp")
    try:
        os.write(descriptor, data)
    finally:
        os.close(descriptor)
    path = Path(name)
    try:
        return core_probe(script, "candidate", "Candidate", path)
    finally:
        path.unlink(missing_ok=True)


def git_repository_identity(path: Path, reason: str = "authority-namespace-derivation-invalid") -> tuple[str, str]:
    path = absolute_clean(path)
    descriptor = open_dir_nofollow(path)
    os.close(descriptor)
    try:
        result = subprocess.run(
            ["/usr/bin/git", "-c", "core.fsmonitor=false", "-c", "gc.auto=0", "rev-parse", "--show-toplevel", "--git-common-dir"],
            cwd=path, env={"PATH": "/usr/bin:/bin", "LC_ALL": "C", "GIT_OPTIONAL_LOCKS": "0"},
            stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
            timeout=5, check=False,
        )
    except (OSError, subprocess.TimeoutExpired):
        fail(reason)
    try:
        lines = result.stdout.decode("utf-8").splitlines()
    except UnicodeError:
        fail(reason)
    if result.returncode or len(lines) != 2:
        fail(reason)
    root = Path(os.path.realpath(lines[0]))
    common = Path(lines[1])
    if not common.is_absolute():
        common = path / common
    common = Path(os.path.realpath(common))
    try:
        if not root.is_dir() or not common.is_dir():
            fail(reason)
    except OSError:
        fail(reason)
    return str(root), str(common)


class AuthorityPathBinding:
    """Bind repository/worktree membership and every directory-path inode."""

    def __init__(self, repository_path: Path, worktree: Path):
        self.repository_path = absolute_clean(repository_path)
        self.worktree_path = absolute_clean(worktree)
        self.repository_hold = HeldAbsoluteDirectory(repository_path)
        self.worktree_hold = HeldAbsoluteDirectory(worktree)
        repository_git_root, repository_common = git_repository_identity(repository_path)
        worktree_git_root, worktree_common = git_repository_identity(worktree)
        if repository_git_root != str(repository_path) or worktree_git_root != str(worktree) or repository_common != worktree_common:
            fail("authority-namespace-derivation-invalid")
        self.receipt = (repository_git_root, repository_common, worktree_git_root, worktree_common)
        self.common_hold = HeldAbsoluteDirectory(Path(repository_common))
        self.verify("authority-namespace-derivation-invalid")

    def verify(self, reason: str = "authority-changed-during-preflight") -> None:
        self.repository_hold.verify(reason)
        self.worktree_hold.verify(reason)
        self.common_hold.verify(reason)
        current = (*git_repository_identity(self.repository_path, reason), *git_repository_identity(self.worktree_path, reason))
        if current != self.receipt:
            fail(reason)


def core_derive_authority_namespace(script: Path, repository_identity: dict, worktree: Path) -> tuple[str, AuthorityPathBinding]:
    exact_keys(repository_identity, {"apiVersion", "kind", "repositoryRoot"}, {"apiVersion", "kind", "repositoryRoot"}, "repository-identity-invalid")
    repository_root = repository_identity.get("repositoryRoot")
    if repository_identity.get("apiVersion") != "marshal.dev/v1alpha1" or repository_identity.get("kind") != "RepositoryIdentity" or not isinstance(repository_root, str) or not repository_root:
        fail("repository-identity-invalid")
    try:
        repository_path = absolute_clean(Path(repository_root))
        binding = AuthorityPathBinding(repository_path, worktree)
    except (OSError, ValueError):
        fail("authority-namespace-derivation-invalid")
    path = write_temp_json({"tenantNamespace": "local", "controlPlaneId": "default", "authorityScopeId": repository_root})
    try:
        digest = core_probe(script, "canonical", "-", path)
    finally:
        path.unlink(missing_ok=True)
    if not DIGEST_RE.fullmatch(digest):
        fail("authority-namespace-derivation-invalid")
    return digest, binding


def validate_operator_schema(script: Path, schema_name: str, data: bytes) -> None:
    descriptor, name = tempfile.mkstemp(prefix="marshal-review-schema.", suffix=".json", dir="/private/tmp")
    try:
        os.write(descriptor, data)
    finally:
        os.close(descriptor)
    path = Path(name)
    try:
        core_probe(script, "schema", str(script.with_name(schema_name)), path)
    finally:
        path.unlink(missing_ok=True)


def git_head(worktree: Path) -> str:
    worktree = absolute_clean(worktree)
    descriptor = open_dir_nofollow(worktree)
    os.close(descriptor)
    result = subprocess.run(
        ["/usr/bin/git", "-c", "core.fsmonitor=false", "-c", "gc.auto=0", "rev-parse", "HEAD"],
        cwd=worktree, env={"PATH": "/usr/bin:/bin", "LC_ALL": "C", "GIT_OPTIONAL_LOCKS": "0"},
        stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
        timeout=5, check=False,
    )
    head = result.stdout.decode("ascii", errors="ignore").strip()
    if result.returncode or not SHA_RE.fullmatch(head):
        fail("source-head-unreadable")
    return head


def observe(script: Path, worktree: Path, base_sha: str) -> dict:
    raw = core_probe(script, "observe", base_sha, worktree)
    return parse_json(raw.encode(), "worktree-observation-invalid")


def validate_manifest(manifest: dict) -> tuple[dict, dict]:
    exact_keys(manifest, {"apiVersion", "kind", "expected", "files"}, {"apiVersion", "kind", "expected", "files"})
    if manifest["apiVersion"] != "marshal.operator/v1alpha1" or manifest["kind"] != "ReviewFreshnessPreflight":
        fail("manifest-shape-invalid")
    expected = exact_keys(
        manifest["expected"],
        {"taskId", "runId", "state", "stateSequence", "currentAttemptId", "sourceHead", "baseSha", "reviewRound"},
        {"taskId", "runId", "state", "stateSequence", "currentAttemptId", "sourceHead", "baseSha", "reviewRound"},
    )
    if expected["state"] != "REVIEW_PENDING" or not SHA_RE.fullmatch(str(expected["sourceHead"])) or not SHA_RE.fullmatch(str(expected["baseSha"])):
        fail("manifest-shape-invalid")
    if any(not isinstance(expected[name], str) or not expected[name] for name in ("taskId", "runId", "currentAttemptId")):
        fail("manifest-shape-invalid")
    if isinstance(expected["stateSequence"], bool) or not isinstance(expected["stateSequence"], int) or expected["stateSequence"] < 1:
        fail("manifest-shape-invalid")
    if isinstance(expected["reviewRound"], bool) or not isinstance(expected["reviewRound"], int) or expected["reviewRound"] < 1:
        fail("manifest-shape-invalid")
    names = {"statePath", "eventsPath", "packetPath", "taskSpecPath", "verificationReportPath", "artifactManifestPath", "policySnapshotPath", "capabilitySnapshotPath", "controlRecordsPath", "historyPath"}
    files = exact_keys(manifest["files"], names, names)
    for value in files.values():
        clean_relative(value)
    return expected, files


def validate_control_records(script: Path, raw: bytes) -> list[str]:
    digests: list[str] = []
    for line in raw.splitlines():
        if not line.strip():
            continue
        record = parse_json(line, "control-record-invalid")
        kind = record.get("kind")
        if kind not in {"ApprovalRecord", "InterventionRecord"}:
            fail("control-record-kind-invalid")
        digests.append(core_validate_bytes(script, kind, line))
    return digests


def validate_events(script: Path, raw: bytes, state: dict) -> tuple[list[str], list[dict], str, str]:
    events: list[dict] = []
    digests: list[str] = []
    seen: set[str] = set()
    # Mirror runstore.decodeEvents: only complete newline-terminated records
    # enter authority; an incomplete trailing fragment is ignored.
    for line in raw.splitlines(keepends=True):
        if not line.endswith(b"\n"):
            continue
        stripped = line.strip()
        if not stripped:
            continue
        event = parse_json(stripped, "event-record-invalid")
        digest = core_validate_bytes(script, "RunEvent", stripped)
        if event.get("sequence") != len(events) + 1 or event.get("eventId") in seen:
            fail("event-journal-sequence-invalid")
        if event.get("runId") != state["runId"]:
            fail("event-run-identity-mismatch")
        seen.add(event["eventId"])
        events.append(event)
        digests.append(digest)
    if len(events) != state["sequence"]:
        fail("event-journal-state-mismatch")
    for event in reversed(events):
        if event.get("type") != "verification.completed":
            continue
        if event.get("actor") != {"type": "system", "id": "marshal-verifier"}:
            fail("verification-event-actor-invalid")
        payload = event.get("payload", {})
        report_digest = payload.get("reportDigest")
        artifact_digest = payload.get("artifactManifestDigest")
        if not isinstance(report_digest, str) or not DIGEST_RE.fullmatch(report_digest) or not isinstance(artifact_digest, str) or not DIGEST_RE.fullmatch(artifact_digest):
            fail("verification-event-digests-missing")
        return digests, events, report_digest, artifact_digest
    fail("verification-event-missing")


def business_events(events: list[dict], initial_state: str | None = None) -> list[dict]:
    """Skip only Core's exact snapshot-repair audit; retain every business event."""
    result: list[dict] = []
    current_state = initial_state
    for event in events:
        if event.get("type") != "reconciliation.snapshot-repaired":
            if current_state is not None and event.get("stateFrom") != current_state:
                fail("business-event-state-lineage-invalid")
            result.append(event)
            current_state = event.get("stateTo")
            continue
        payload = event.get("payload")
        source = payload.get("sourceJournalSequence") if isinstance(payload, dict) else None
        if (
            event.get("stateFrom") != event.get("stateTo")
            or (current_state is not None and event.get("stateFrom") != current_state)
            or event.get("attemptId", "") != ""
            or event.get("actor") != {"type": "system", "id": "marshal-reconciliation"}
            or not isinstance(payload, dict)
            or set(payload) != {"repairKind", "sourceJournalSequence"}
            or payload.get("repairKind") != "snapshot-rebuild"
            or isinstance(source, bool)
            or not isinstance(source, int)
            or source != event.get("sequence", 0) - 1
        ):
            fail("repair-audit-event-invalid")
    return result


def validate_current_generation_inputs(
    script: Path,
    run_root: Path,
    state: dict,
    worktree: Path,
    task: dict,
    task_digest: str,
    report: dict,
    verification_digest: str,
    artifacts: dict,
    artifact_digest: str,
    expected_authority_namespace: str,
    require_candidate: bool = True,
) -> tuple[dict, list[tuple[str, bytes, tuple[int, int, int, int]]], dict]:
    """Recompute the inputs Core will use when it replaces/generates a packet."""
    records: list[tuple[str, bytes, tuple[int, int, int, int]]] = []
    if task.get("metadata", {}).get("id") != state["taskId"] or task_digest != state["specDigest"]:
        fail("task-spec-binding-mismatch")
    if report.get("taskId") != state["taskId"] or report.get("runId") != state["runId"] or report.get("specDigest") != state["specDigest"] or report.get("baseSha") != state["baseSha"]:
        fail("verification-binding-mismatch")
    if artifacts.get("taskId") != state["taskId"] or artifacts.get("runId") != state["runId"]:
        fail("packet-input-identity-mismatch")

    patch, patch_identity = read_regular(run_root, "observed.patch", "packet-input-unreadable")
    records.append(("observed.patch", patch, patch_identity))
    patch_digest = raw_digest(patch)
    observed_artifact = [item for item in artifacts.get("artifacts", []) if item.get("relativePath") == "observed.patch"]
    if len(observed_artifact) != 1 or observed_artifact[0].get("producer") != "verifier" or observed_artifact[0].get("status") != "validated" or observed_artifact[0].get("digest") != patch_digest or observed_artifact[0].get("byteSize") != len(patch):
        fail("observed-patch-binding-mismatch")

    persisted_workers = enumerate_worker_results(run_root)
    if not persisted_workers:
        fail("worker-result-set-mismatch")
    worker_digests: list[str] = []
    worker_attempt_ids: list[str] = []
    for worker_path in sorted(persisted_workers):
        parts = Path(worker_path).parts
        if len(parts) != 3 or parts[0] != "attempts" or parts[2] != "worker-result.json":
            fail("worker-result-path-invalid")
        data, identity = persisted_workers[worker_path]
        worker = parse_json(data, "packet-input-invalid-json")
        if worker.get("taskId") != state["taskId"] or worker.get("runId") != state["runId"] or worker.get("attemptId") != parts[1]:
            fail("worker-result-identity-mismatch")
        worker_attempt_ids.append(parts[1])
        worker_digests.append(core_validate_bytes(script, "WorkerResult", data))
        records.append((worker_path, data, identity))
    if state["currentAttemptId"] not in worker_attempt_ids or len(set(worker_attempt_ids)) != len(worker_attempt_ids):
        fail("worker-result-attempt-set-mismatch")

    observation = observe(script, worktree, state["baseSha"])
    report_observed = report.get("observed", {})
    observed_fields = (
        "snapshotDigest", "diffDigest", "changedFiles", "changedFileCount",
        "diffBytes", "hasUntrackedFiles",
    )
    if any(observation.get(field) != report_observed.get(field) for field in observed_fields) or observation.get("diffDigest") != patch_digest:
        fail("worktree-evidence-changed-after-verification")

    candidate = report.get("candidateDigest", "")
    worker_candidate = report.get("workerCandidateDigest", "")
    if bool(candidate) != bool(worker_candidate):
        fail("legacy-candidate-partial-requires-migration")
    if not candidate and require_candidate:
        fail("current-round-candidate-missing")
    candidate_chain: dict = {}
    if candidate:
        candidate_chain = validate_candidate_chain(
            script, run_root, state, artifacts, candidate, worker_candidate,
            "observed.patch", patch_digest, expected_authority_namespace, records,
        )
    elif any(artifact.get("candidateDigest") for artifact in artifacts.get("artifacts", [])):
        fail("legacy-candidate-partial-requires-migration")

    return {
        "specDigest": task_digest,
        "verificationDigest": verification_digest,
        "artifactManifestDigest": artifact_digest,
        "workerResultDigests": worker_digests,
        "patchDigest": patch_digest,
        "snapshotDigest": observation.get("snapshotDigest"),
        "changedFiles": observation.get("changedFiles"),
        "changedFileCount": observation.get("changedFileCount"),
        "diffBytes": observation.get("diffBytes"),
        "hasUntrackedFiles": observation.get("hasUntrackedFiles"),
        "candidateDigest": candidate,
        "workerCandidateDigest": worker_candidate,
    }, records, candidate_chain


def validate_candidate_chain(
    script: Path,
    run_root: Path,
    state: dict,
    artifacts: dict,
    candidate: str,
    worker_candidate: str,
    observed_patch_path: str,
    observed_patch_digest: str,
    expected_authority_namespace: str,
    records: list[tuple[str, bytes, tuple[int, int, int, int]]],
) -> dict:
    candidate_records: dict[str, dict] = {}
    for digest in sorted({candidate, worker_candidate}):
        path = f"candidates/{digest}.json"
        data, identity = read_regular(run_root, path, "candidate-record-unreadable")
        recomputed = core_validate_candidate(script, data)
        record = parse_json(data, "candidate-record-invalid-json")
        if record.get("candidateDigest") != digest or recomputed != digest:
            fail("candidate-record-digest-mismatch")
        exact = {
            "taskId": state["taskId"], "runId": state["runId"],
            "attemptId": state["currentAttemptId"], "baseSha": state["baseSha"],
            "authorityNamespaceId": expected_authority_namespace,
        }
        if any(record.get(field) != value for field, value in exact.items()):
            fail("candidate-record-identity-mismatch")
        # The local verifier's frozen Candidate builder does not project a
        # provider allocation lease.  Presence of either optional field is
        # therefore a foreign identity, including an explicit zero.
        if "allocationId" in record or "generation" in record:
            fail("candidate-allocation-generation-mismatch")
        candidate_records[digest] = record
        records.append((path, data, identity))

    worker_record = candidate_records[worker_candidate]
    head_record = candidate_records[candidate]
    if worker_record.get("producerKind") != "worker" or worker_record.get("producer") != "worker" or worker_record.get("predecessorCandidateDigest"):
        fail("candidate-chain-mismatch")
    if candidate != worker_candidate:
        if head_record.get("producerKind") != "normalizer" or head_record.get("producer") != "verifier:format-normalize" or head_record.get("predecessorCandidateDigest") != worker_candidate:
            fail("candidate-chain-mismatch")
    elif head_record != worker_record:
        fail("candidate-chain-mismatch")
    if head_record.get("contentDigest") != observed_patch_digest:
        fail("candidate-content-mismatch")

    worker_patch, worker_patch_identity = read_regular(run_root, "worker.patch", "worker-patch-unreadable")
    records.append(("worker.patch", worker_patch, worker_patch_identity))
    worker_patch_digest = raw_digest(worker_patch)
    if worker_record.get("contentDigest") != worker_patch_digest:
        fail("worker-candidate-content-mismatch")
    observed_artifacts = [artifact for artifact in artifacts.get("artifacts", []) if artifact.get("relativePath") == observed_patch_path]
    worker_artifacts = [artifact for artifact in artifacts.get("artifacts", []) if artifact.get("relativePath") == "worker.patch"]
    if len(observed_artifacts) != 1 or observed_artifacts[0].get("candidateDigest") != candidate:
        fail("candidate-artifact-binding-mismatch")
    if (
        len(worker_artifacts) != 1
        or worker_artifacts[0].get("producer") != "verifier"
        or worker_artifacts[0].get("status") != "validated"
        or worker_artifacts[0].get("digest") != worker_patch_digest
        or worker_artifacts[0].get("byteSize") != len(worker_patch)
        or worker_artifacts[0].get("candidateDigest") != worker_candidate
    ):
        fail("worker-patch-artifact-binding-mismatch")
    return {
        "candidateRecords": candidate_records,
        "workerPatchDigest": worker_patch_digest,
        "workerPatchByteSize": len(worker_patch),
        "normalizationApplied": candidate != worker_candidate,
    }


def validate_current_worker_completion_binding(
    run_root: Path,
    state: dict,
    previous_bindings: dict,
    current_bindings: dict,
    candidate_chain: dict,
) -> tuple[dict, tuple[str, bytes, tuple[int, int, int, int]]]:
    """Bind worker.completed to the pre-normalization Worker observation.

    Verification may deterministically normalize the worktree after
    worker.completed.  The event therefore binds the current attempt's
    immutable worker Candidate/worker.patch observation, while the current
    VerificationReport and live worktree bind the head Candidate separately.
    """
    attempt_id = clean_relative(state["currentAttemptId"])
    relative = f"attempts/{attempt_id}/worktree-snapshot.json"
    snapshot_raw, snapshot_identity = read_regular(
        run_root, relative, "current-round-worker-snapshot-unreadable", 4 << 20,
    )
    snapshot = parse_json(snapshot_raw, "current-round-worker-snapshot-invalid")
    fields = {
        "snapshotDigest", "diffDigest", "changedFiles", "changedFileCount",
        "diffBytes", "hasUntrackedFiles",
    }
    exact_keys(snapshot, fields, fields, "current-round-worker-snapshot-invalid")
    if not DIGEST_RE.fullmatch(str(snapshot.get("snapshotDigest", ""))) or not DIGEST_RE.fullmatch(str(snapshot.get("diffDigest", ""))):
        fail("current-round-worker-snapshot-invalid")
    changed_files = snapshot.get("changedFiles")
    if (
        not isinstance(changed_files, list)
        or any(not isinstance(path, str) for path in changed_files)
        or changed_files != sorted(set(changed_files))
    ):
        fail("current-round-worker-snapshot-invalid")
    for path in changed_files:
        clean_relative(path)
    changed_count = snapshot.get("changedFileCount")
    diff_bytes = snapshot.get("diffBytes")
    if (
        isinstance(changed_count, bool)
        or not isinstance(changed_count, int)
        or changed_count != len(changed_files)
        or isinstance(diff_bytes, bool)
        or not isinstance(diff_bytes, int)
        or diff_bytes < 0
        or not isinstance(snapshot.get("hasUntrackedFiles"), bool)
    ):
        fail("current-round-worker-snapshot-invalid")

    worker_patch_digest = candidate_chain.get("workerPatchDigest")
    worker_patch_size = candidate_chain.get("workerPatchByteSize")
    if (
        not DIGEST_RE.fullmatch(str(worker_patch_digest or ""))
        or snapshot["diffDigest"] != worker_patch_digest
        or snapshot["diffBytes"] != worker_patch_size
    ):
        fail("current-round-worker-snapshot-binding-mismatch")
    if (
        previous_bindings["currentWorkerCompletedSnapshotDigest"] != snapshot["snapshotDigest"]
        or previous_bindings["currentWorkerCompletedDiffDigest"] != snapshot["diffDigest"]
    ):
        fail("current-round-worker-completed-binding-mismatch")

    final_fields = (
        "snapshotDigest", "patchDigest", "changedFiles", "changedFileCount",
        "diffBytes", "hasUntrackedFiles",
    )
    if candidate_chain.get("normalizationApplied"):
        if (
            snapshot["diffDigest"] == current_bindings["patchDigest"]
            or snapshot["snapshotDigest"] == current_bindings["snapshotDigest"]
            or snapshot["changedFiles"] != current_bindings["changedFiles"]
            or snapshot["changedFileCount"] != current_bindings["changedFileCount"]
            or snapshot["hasUntrackedFiles"] != current_bindings["hasUntrackedFiles"]
        ):
            fail("current-round-normalization-binding-mismatch")
    else:
        worker_as_final = dict(snapshot)
        worker_as_final["patchDigest"] = worker_as_final.pop("diffDigest")
        if any(worker_as_final[field] != current_bindings[field] for field in final_fields):
            fail("current-round-worker-snapshot-binding-mismatch")

    return snapshot, (relative, snapshot_raw, snapshot_identity)


def validate_previous_round_lineage(
    script: Path,
    run_root: Path,
    packet_raw: bytes,
    packet: dict,
    packet_digest: str,
    state: dict,
    events: list[dict],
) -> tuple[dict, list[tuple[str, bytes, tuple[int, int, int, int]]]]:
    """Prove a live stale packet is the committed predecessor, never an orphan."""
    previous_round = state["reviewRound"] - 1
    if previous_round < 1 or packet.get("reviewRound") != previous_round:
        fail("packet-identity-mismatch")
    if packet.get("taskId") != state["taskId"] or packet.get("runId") != state["runId"] or packet.get("baseSha") != state["baseSha"] or packet.get("specDigest") != state["specDigest"]:
        fail("packet-identity-mismatch")
    inputs = exact_keys(packet.get("inputs"), {"taskSpec", "patch", "verificationReport", "artifactManifest", "workerResults"}, {"taskSpec", "patch", "verificationReport", "artifactManifest", "workerResults"}, "packet-inputs-invalid")
    if not isinstance(inputs["workerResults"], list) or not inputs["workerResults"]:
        fail("packet-inputs-invalid")
    prior_attempts: list[str] = []
    for path in inputs["workerResults"]:
        parts = Path(clean_relative(path)).parts
        if len(parts) != 3 or parts[0] != "attempts" or parts[2] != "worker-result.json":
            fail("worker-result-path-invalid")
        prior_attempts.append(parts[1])
    if state["currentAttemptId"] in prior_attempts:
        fail("previous-round-current-attempt-conflict")

    packet_path = f"review-packets/packet-{previous_round:03d}.json"
    decision_path = f"decisions/decision-{previous_round:03d}.json"
    archived_packet_raw, archived_packet_identity = read_regular(run_root, packet_path, "previous-round-packet-unreadable", 4 << 20)
    decision_raw, decision_identity = read_regular(run_root, decision_path, "previous-round-decision-unreadable", 4 << 20)
    if archived_packet_raw != packet_raw or core_validate_bytes(script, "ReviewPacket", archived_packet_raw) != packet_digest:
        fail("previous-round-packet-archive-mismatch")
    decision_digest = core_validate_bytes(script, "ReviewDecision", decision_raw)
    decision = parse_json(decision_raw, "previous-round-decision-invalid-json")
    exact = {
        "taskId": state["taskId"], "runId": state["runId"], "reviewRound": previous_round,
        "specDigest": state["specDigest"], "reviewPacketDigest": packet_digest,
        "verificationDigest": packet.get("verificationDigest"),
        "artifactManifestDigest": packet.get("artifactManifestDigest"),
        "evidenceDigest": packet.get("evidenceDigest"), "verdict": "rework",
    }
    if any(decision.get(key) != value for key, value in exact.items()):
        fail("previous-round-decision-binding-mismatch")

    matching_rework = []
    for index, event in enumerate(events):
        if event.get("type") != "review.rework":
            continue
        payload = event.get("payload", {})
        if payload.get("decisionDigest") == decision_digest and payload.get("evidenceDigest") == packet.get("evidenceDigest") and payload.get("verdict") == "rework":
            matching_rework.append(index)
    if len(matching_rework) != 1:
        fail("previous-round-review-event-missing")
    rework_index = matching_rework[0]
    rework_event = events[rework_index]
    if rework_event.get("actor") != {"type": "system", "id": "marshal-review"} or rework_event.get("stateFrom") != "REVIEW_PENDING" or rework_event.get("stateTo") != "REWORK_REQUESTED" or rework_event.get("attemptId", "") != "":
        fail("previous-round-review-event-invalid")
    prior_business = business_events(events[:rework_index])
    if not prior_business or prior_business[-1].get("type") != "verification.completed":
        fail("previous-round-verification-event-missing")
    prior_verification = prior_business[-1]
    prior_index = max(index for index, event in enumerate(events[:rework_index]) if event.get("type") != "reconciliation.snapshot-repaired")
    business_events(events[prior_index:rework_index], prior_verification.get("stateFrom"))
    prior_payload = prior_verification.get("payload", {})
    if prior_verification.get("actor") != {"type": "system", "id": "marshal-verifier"} or prior_verification.get("stateFrom") != "VERIFYING" or prior_verification.get("stateTo") != "REVIEW_PENDING" or prior_payload.get("reportDigest") != packet.get("verificationDigest") or prior_payload.get("artifactManifestDigest") != packet.get("artifactManifestDigest"):
        fail("previous-round-verification-event-invalid")

    successor = business_events(events[rework_index + 1:], "REWORK_REQUESTED")
    if len(successor) != 3:
        fail("current-round-event-lineage-invalid")
    started, completed, verified = successor
    if started.get("type") != "worker.started" or started.get("stateFrom") != "REWORK_REQUESTED" or started.get("stateTo") != "RUNNING" or started.get("attemptId") != state["currentAttemptId"] or started.get("actor") != {"type": "system", "id": "marshal-worker-runner"}:
        fail("current-round-event-lineage-invalid")
    if completed.get("type") != "worker.completed" or completed.get("stateFrom") != "RUNNING" or completed.get("stateTo") != "VERIFYING" or completed.get("attemptId") != state["currentAttemptId"] or completed.get("actor") != {"type": "system", "id": "marshal-worker-runner"}:
        fail("current-round-event-lineage-invalid")
    if verified.get("type") != "verification.completed" or verified.get("stateFrom") != "VERIFYING" or verified.get("stateTo") != "REVIEW_PENDING" or verified.get("actor") != {"type": "system", "id": "marshal-verifier"}:
        fail("current-round-event-lineage-invalid")
    if verified.get("payload", {}).get("reportDigest") == packet.get("verificationDigest"):
        fail("current-round-verification-not-new")
    return {
        "previousReviewRound": previous_round,
        "previousPacketDigest": packet_digest,
        "previousDecisionDigest": decision_digest,
        "previousReviewEventSequence": rework_event.get("sequence"),
        "previousAttemptIds": prior_attempts,
        "currentWorkerCompletedSnapshotDigest": completed.get("payload", {}).get("snapshotDigest"),
        "currentWorkerCompletedDiffDigest": completed.get("payload", {}).get("diffDigest"),
    }, [
        (packet_path, archived_packet_raw, archived_packet_identity),
        (decision_path, decision_raw, decision_identity),
    ]


def validate_packet_inputs(script: Path, run_root: Path, packet: dict, state: dict, worktree: Path, expected_authority_namespace: str) -> tuple[dict, list[tuple[str, bytes, tuple[int, int, int, int]]]]:
    inputs = exact_keys(packet.get("inputs"), {"taskSpec", "patch", "verificationReport", "artifactManifest", "workerResults"}, {"taskSpec", "patch", "verificationReport", "artifactManifest", "workerResults"}, "packet-inputs-invalid")
    if not isinstance(inputs["workerResults"], list) or not inputs["workerResults"]:
        fail("packet-inputs-invalid")
    records: list[tuple[str, bytes, tuple[int, int, int, int]]] = []
    loaded: dict[str, tuple[bytes, dict, str]] = {}
    for name, kind in (("taskSpec", "Task"), ("verificationReport", "VerificationReport"), ("artifactManifest", "ArtifactManifest")):
        data, identity = read_regular(run_root, inputs[name], "packet-input-unreadable")
        digest = core_validate_bytes(script, kind, data)
        loaded[name] = (data, parse_json(data, "packet-input-invalid-json"), digest)
        records.append((clean_relative(inputs[name]), data, identity))
    patch, patch_identity = read_regular(run_root, inputs["patch"], "packet-input-unreadable")
    records.append((clean_relative(inputs["patch"]), patch, patch_identity))
    persisted_workers = enumerate_worker_results(run_root)
    declared_worker_paths = [clean_relative(path) for path in inputs["workerResults"]]
    if declared_worker_paths != sorted(persisted_workers):
        fail("worker-result-set-mismatch")
    worker_digests: list[str] = []
    worker_attempt_ids: list[str] = []
    for worker_path in declared_worker_paths:
        parts = Path(worker_path).parts
        if len(parts) != 3 or parts[0] != "attempts" or parts[2] != "worker-result.json":
            fail("worker-result-path-invalid")
        data, identity = persisted_workers[worker_path]
        worker = parse_json(data, "packet-input-invalid-json")
        if worker.get("taskId") != state["taskId"] or worker.get("runId") != state["runId"] or worker.get("attemptId") != parts[1]:
            fail("worker-result-identity-mismatch")
        worker_attempt_ids.append(parts[1])
        worker_digests.append(core_validate_bytes(script, "WorkerResult", data))
        records.append((clean_relative(worker_path), data, identity))
    if state["currentAttemptId"] not in worker_attempt_ids or len(set(worker_attempt_ids)) != len(worker_attempt_ids):
        fail("worker-result-attempt-set-mismatch")
    task, task_digest = loaded["taskSpec"][1], loaded["taskSpec"][2]
    report, verification_digest = loaded["verificationReport"][1], loaded["verificationReport"][2]
    artifacts, artifact_digest = loaded["artifactManifest"][1], loaded["artifactManifest"][2]
    exact = {
        "taskId": state["taskId"], "runId": state["runId"], "specDigest": state["specDigest"], "baseSha": state["baseSha"],
    }
    if task.get("metadata", {}).get("id") != state["taskId"] or task_digest != state["specDigest"]:
        fail("task-spec-binding-mismatch")
    for document in (report, artifacts):
        if document.get("taskId") != exact["taskId"] or document.get("runId") != exact["runId"]:
            fail("packet-input-identity-mismatch")
    if report.get("specDigest") != exact["specDigest"] or report.get("baseSha") != exact["baseSha"]:
        fail("verification-binding-mismatch")
    patch_digest = raw_digest(patch)
    observed_artifact = [item for item in artifacts.get("artifacts", []) if item.get("relativePath") == inputs["patch"]]
    if len(observed_artifact) != 1 or observed_artifact[0].get("producer") != "verifier" or observed_artifact[0].get("status") != "validated" or observed_artifact[0].get("digest") != patch_digest or observed_artifact[0].get("byteSize") != len(patch):
        fail("observed-patch-binding-mismatch")
    bindings = {
        "specDigest": task_digest, "verificationDigest": verification_digest,
        "artifactManifestDigest": artifact_digest, "workerResultDigests": worker_digests,
        "patchDigest": patch_digest,
    }
    for field, value in (("specDigest", task_digest), ("verificationDigest", verification_digest), ("artifactManifestDigest", artifact_digest), ("workerResultDigests", worker_digests)):
        if packet.get(field) != value:
            fail("packet-recomputed-binding-mismatch")
    candidate = packet.get("candidateDigest", "")
    worker_candidate = packet.get("workerCandidateDigest", "")
    if bool(candidate) != bool(worker_candidate):
        fail("legacy-candidate-partial-requires-migration")
    if candidate:
        if report.get("candidateDigest") != candidate or report.get("workerCandidateDigest") != worker_candidate:
            fail("candidate-binding-mismatch")
        validate_candidate_chain(
            script, run_root, state, artifacts, candidate, worker_candidate,
            clean_relative(inputs["patch"]), patch_digest, expected_authority_namespace, records,
        )
    elif report.get("candidateDigest") or report.get("workerCandidateDigest"):
        fail("legacy-candidate-partial-requires-migration")
    observation = observe(script, worktree, state["baseSha"])
    report_observed = report.get("observed", {})
    if observation.get("snapshotDigest") != packet.get("snapshotDigest") or observation.get("diffDigest") != packet.get("diffDigest") or report_observed.get("snapshotDigest") != packet.get("snapshotDigest") or report_observed.get("diffDigest") != packet.get("diffDigest") or observation.get("diffDigest") != patch_digest:
        fail("worktree-evidence-changed-after-verification")
    eligibility_digest = ""
    if "codexEligibilityBinding" in packet:
        eligibility_digest = core_digest_value(script, packet["codexEligibilityBinding"])
    evidence = {
        "specDigest": task_digest, "patchDigest": patch_digest,
        "verificationDigest": verification_digest, "artifactManifestDigest": artifact_digest,
        "workerResultDigests": worker_digests,
        "previousBlockingFindings": packet.get("previousBlockingFindings", []),
    }
    if candidate:
        evidence["candidateDigest"] = candidate
        evidence["workerCandidateDigest"] = worker_candidate
    if eligibility_digest:
        evidence["eligibilityBindingDigest"] = eligibility_digest
    if core_digest_value(script, evidence) != packet.get("evidenceDigest"):
        fail("evidence-digest-recompute-mismatch")
    return bindings, records


def claim_history(script: Path, authority: HeldRelativeParent, initial_raw: bytes, initial_identity: tuple[int, int, int, int], action: str, reason: str, dedupe: str, fingerprint: str, final_authority_check) -> None:
    lock_leaf = authority.leaf + ".claim.lock"
    temporary_leaf = authority.leaf + f".pending.{os.getpid()}"
    authority.verify()
    try:
        lock_fd = os.open(lock_leaf, os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0), 0o600, dir_fd=authority.parent_fd)
    except FileExistsError:
        fail("history-claim-contended")
    except OSError:
        fail("history-claim-unavailable")
    try:
        os.close(lock_fd)
        authority.verify()
        current_raw, current_identity = authority.read("history-unreadable", 4 << 20)
        if current_raw != initial_raw or current_identity != initial_identity:
            fail("history-cas-mismatch")
        history = parse_json(current_raw, "history-invalid")
        claims = history.get("claims")
        if not isinstance(claims, list):
            fail("history-invalid")
        if any(entry.get("dedupeKey") == dedupe or entry.get("freshnessFingerprint") == fingerprint for entry in claims if isinstance(entry, dict)):
            fail("action-already-claimed")
        final_authority_check()
        claims.append({"action": action, "reasonCode": reason, "dedupeKey": dedupe, "freshnessFingerprint": fingerprint, "previousHistoryRawDigest": raw_digest(initial_raw)})
        payload = json.dumps(history, ensure_ascii=False, indent=2).encode("utf-8") + b"\n"
        validate_operator_schema(script, "review-freshness-history.schema.json", payload)
        try:
            fd = os.open(temporary_leaf, os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0), 0o600, dir_fd=authority.parent_fd)
        except OSError:
            fail("history-claim-unavailable")
        try:
            view = memoryview(payload)
            while view:
                written = os.write(fd, view)
                if written <= 0:
                    fail("history-claim-unavailable")
                view = view[written:]
            os.fsync(fd)
        finally:
            os.close(fd)
        authority.verify()
        final_authority_check()
        os.replace(temporary_leaf, authority.leaf, src_dir_fd=authority.parent_fd, dst_dir_fd=authority.parent_fd)
        authority.verify()
        os.fsync(authority.parent_fd)
    finally:
        try:
            os.unlink(temporary_leaf, dir_fd=authority.parent_fd)
        except FileNotFoundError:
            pass
        try:
            os.unlink(lock_leaf, dir_fd=authority.parent_fd)
        except FileNotFoundError:
            pass


def run(arguments: argparse.Namespace) -> dict:
    global _CORE_BINARY
    marshal = getattr(arguments, "marshal", None)
    if not marshal:
        fail("core-probe-unavailable")
    _CORE_BINARY = checked_stable_marshal(marshal)
    script = Path(__file__).absolute()
    run_root, operator_root, worktree, manifest_path = map(Path, (arguments.run_root, arguments.operator_root, arguments.worktree, arguments.manifest))
    for root in (run_root, operator_root, worktree):
        absolute_clean(root)
        descriptor = open_dir_nofollow(root)
        os.close(descriptor)
    absolute_clean(manifest_path)
    try:
        manifest_relative = manifest_path.relative_to(operator_root).as_posix()
    except ValueError:
        fail("path-boundary-invalid")
    manifest_raw, _ = read_regular(operator_root, manifest_relative, "manifest-unreadable", 4 << 20)
    validate_operator_schema(script, "review-freshness-preflight.schema.json", manifest_raw)
    manifest = parse_json(manifest_raw, "manifest-invalid-json")
    expected, files = validate_manifest(manifest)

    tracked: list[tuple[Path, str, bytes, tuple[int, int, int, int]]] = []
    def load(root: Path, relative: str, reason: str, limit: int = MAX_FILE_BYTES) -> bytes:
        data, identity = read_regular(root, relative, reason, limit)
        tracked.append((root, clean_relative(relative), data, identity))
        return data

    if run_root.parent.name != "runs":
        fail("state-root-layout-invalid")
    state_root = run_root.parent.parent
    repository_identity_raw = load(state_root, "repo.json", "repository-identity-unreadable", 4 << 20)
    repository_identity = parse_json(repository_identity_raw, "repository-identity-invalid")
    expected_authority_namespace, authority_binding = core_derive_authority_namespace(script, repository_identity, worktree)

    state_raw = load(run_root, files["statePath"], "state-unreadable", 4 << 20)
    state_digest = core_validate_bytes(script, "RunState", state_raw)
    state = parse_json(state_raw, "state-invalid-json")
    exact = {"taskId": expected["taskId"], "runId": expected["runId"], "state": "REVIEW_PENDING", "sequence": expected["stateSequence"], "currentAttemptId": expected["currentAttemptId"], "baseSha": expected["baseSha"], "reviewRound": expected["reviewRound"]}
    if any(state.get(key) != value for key, value in exact.items()):
        fail("state-identity-mismatch")
    if any(not DIGEST_RE.fullmatch(str(state.get(key, ""))) for key in ("specDigest", "policyDigest", "capabilityDigest")):
        fail("state-frozen-inputs-missing")
    if Path(str(state.get("worktreePath", ""))) != worktree:
        fail("state-worktree-mismatch")
    if git_head(worktree) != expected["sourceHead"]:
        fail("source-head-mismatch")

    policy_raw = load(run_root, files["policySnapshotPath"], "policy-unreadable", 4 << 20)
    capability_raw = load(run_root, files["capabilitySnapshotPath"], "capability-unreadable", 4 << 20)
    task_raw = load(run_root, files["taskSpecPath"], "task-spec-unreadable", 4 << 20)
    task_digest = core_validate_bytes(script, "Task", task_raw)
    task = parse_json(task_raw, "task-spec-invalid-json")
    if core_validate_bytes(script, "PolicySnapshot", policy_raw) != state.get("policyDigest") or core_validate_bytes(script, "CapabilitySnapshot", capability_raw) != state.get("capabilityDigest") or task_digest != state.get("specDigest"):
        fail("frozen-input-digest-mismatch")
    control_raw = load(run_root, files["controlRecordsPath"], "control-records-unreadable", 16 << 20)
    control_digests = validate_control_records(script, control_raw)
    events_raw = load(run_root, files["eventsPath"], "events-unreadable", 32 << 20)
    event_digests, events, frozen_verification_digest, frozen_artifact_digest = validate_events(script, events_raw, state)
    frozen_report_raw = load(run_root, files["verificationReportPath"], "verification-report-unreadable", 16 << 20)
    frozen_manifest_raw = load(run_root, files["artifactManifestPath"], "artifact-manifest-unreadable", 16 << 20)
    current_verification_digest = core_validate_bytes(script, "VerificationReport", frozen_report_raw)
    current_artifact_digest = core_validate_bytes(script, "ArtifactManifest", frozen_manifest_raw)
    frozen_report = parse_json(frozen_report_raw, "verification-report-invalid-json")
    frozen_manifest = parse_json(frozen_manifest_raw, "artifact-manifest-invalid-json")
    if frozen_report.get("taskId") != state["taskId"] or frozen_report.get("runId") != state["runId"] or frozen_report.get("specDigest") != state["specDigest"] or frozen_report.get("baseSha") != state["baseSha"]:
        fail("verification-binding-mismatch")
    if frozen_manifest.get("taskId") != state["taskId"] or frozen_manifest.get("runId") != state["runId"]:
        fail("packet-input-identity-mismatch")
    if current_verification_digest != frozen_verification_digest or current_artifact_digest != frozen_artifact_digest:
        fail("verification-event-binding-mismatch")
    history_authority = HeldRelativeParent(operator_root, files["historyPath"])
    history_raw, history_identity = history_authority.read("history-unreadable", 4 << 20)
    validate_operator_schema(script, "review-freshness-history.schema.json", history_raw)
    history = parse_json(history_raw, "history-invalid")

    packet_present = regular_presence(run_root, files["packetPath"])
    packet_digest = ""
    input_bindings: dict = {}
    current_generation_bindings: dict = {}
    missing_observation: dict = {}
    if packet_present:
        packet_raw = load(run_root, files["packetPath"], "packet-unreadable", 4 << 20)
        packet_digest = core_validate_bytes(script, "ReviewPacket", packet_raw)
        packet = parse_json(packet_raw, "packet-invalid-json")
        if packet.get("taskId") != state["taskId"] or packet.get("runId") != state["runId"] or packet.get("baseSha") != state["baseSha"] or packet.get("specDigest") != state["specDigest"]:
            fail("packet-identity-mismatch")
        if packet.get("reviewRound") == state["reviewRound"]:
            if packet.get("inputs", {}).get("taskSpec") != files["taskSpecPath"] or packet.get("inputs", {}).get("verificationReport") != files["verificationReportPath"] or packet.get("inputs", {}).get("artifactManifest") != files["artifactManifestPath"]:
                fail("packet-input-path-mismatch")
            input_bindings, packet_records = validate_packet_inputs(script, run_root, packet, state, worktree, expected_authority_namespace)
            if input_bindings.get("verificationDigest") != frozen_verification_digest or input_bindings.get("artifactManifestDigest") != frozen_artifact_digest:
                fail("verification-event-binding-mismatch")
            for relative, data, identity in packet_records:
                tracked.append((run_root, relative, data, identity))
            action, reason = "dispatch-reviewer", "fresh-review-packet-claimed"
        else:
            previous_bindings, previous_records = validate_previous_round_lineage(script, run_root, packet_raw, packet, packet_digest, state, events)
            for relative, data, identity in previous_records:
                tracked.append((run_root, relative, data, identity))
            current_generation_bindings, generation_records, candidate_chain = validate_current_generation_inputs(
                script, run_root, state, worktree, task, task_digest, frozen_report,
                current_verification_digest, frozen_manifest, current_artifact_digest,
                expected_authority_namespace,
            )
            if current_generation_bindings["verificationDigest"] != frozen_verification_digest or current_generation_bindings["artifactManifestDigest"] != frozen_artifact_digest:
                fail("verification-event-binding-mismatch")
            worker_observation, worker_observation_record = validate_current_worker_completion_binding(
                run_root, state, previous_bindings, current_generation_bindings, candidate_chain,
            )
            if candidate_chain.get("normalizationApplied"):
                current_generation_bindings["workerObservation"] = worker_observation
            relative, data, identity = worker_observation_record
            tracked.append((run_root, relative, data, identity))
            for relative, data, identity in generation_records:
                tracked.append((run_root, relative, data, identity))
            input_bindings = {"previousRoundLineage": previous_bindings, "currentGeneration": current_generation_bindings}
            action, reason = "generate-review-packet", "previous-round-packet-generation-claimed"
    else:
        report_candidate = frozen_report.get("candidateDigest", "")
        report_worker_candidate = frozen_report.get("workerCandidateDigest", "")
        if not report_candidate and not report_worker_candidate:
            legacy_observation = observe(script, worktree, state["baseSha"])
            if (
                legacy_observation.get("diffDigest") != raw_digest(b"")
                or legacy_observation.get("changedFileCount") != 0
                or legacy_observation.get("hasUntrackedFiles") is not False
            ):
                fail("packet-missing-worktree-not-clean")
        current_generation_bindings, generation_records, _candidate_chain = validate_current_generation_inputs(
            script, run_root, state, worktree, task, task_digest, frozen_report,
            current_verification_digest, frozen_manifest, current_artifact_digest,
            expected_authority_namespace,
            require_candidate=False,
        )
        if current_generation_bindings["verificationDigest"] != frozen_verification_digest or current_generation_bindings["artifactManifestDigest"] != frozen_artifact_digest:
            fail("verification-event-binding-mismatch")
        for relative, data, identity in generation_records:
            tracked.append((run_root, relative, data, identity))
        missing_observation = {
            field: current_generation_bindings[field]
            for field in (
                "snapshotDigest", "patchDigest", "changedFiles", "changedFileCount",
                "diffBytes", "hasUntrackedFiles",
            )
        }
        missing_observation["diffDigest"] = missing_observation.pop("patchDigest")
        if not current_generation_bindings["candidateDigest"] and (
            missing_observation.get("diffDigest") != raw_digest(b"")
            or missing_observation.get("changedFileCount") != 0
            or missing_observation.get("hasUntrackedFiles") is not False
        ):
            fail("packet-missing-worktree-not-clean")
        input_bindings = {
            "missingPacketWorktreeObservation": missing_observation,
            "currentGeneration": current_generation_bindings,
        }
        action, reason = "generate-review-packet", "packet-missing-generation-claimed"

    identity = {
        "schemaVersion": "marshal.operator.review-freshness.v2",
        "action": action, "taskId": state["taskId"], "runId": state["runId"],
        "stateRawDigest": raw_digest(state_raw), "stateCanonicalDigest": state_digest,
        "state": state, "sourceHead": expected["sourceHead"],
        "taskSpecRawDigest": raw_digest(task_raw), "policyRawDigest": raw_digest(policy_raw),
        "capabilityRawDigest": raw_digest(capability_raw), "controlRecordsRawDigest": raw_digest(control_raw),
        "controlRecordDigests": control_digests, "eventsRawDigest": raw_digest(events_raw),
        "eventRecordDigests": event_digests, "frozenVerificationDigest": frozen_verification_digest,
        "frozenArtifactManifestDigest": frozen_artifact_digest,
        "verificationReportRawDigest": raw_digest(frozen_report_raw),
        "artifactManifestRawDigest": raw_digest(frozen_manifest_raw),
        "packetPresence": "present" if packet_present else "missing",
        "reviewPacketDigest": packet_digest, "packetInputBindings": input_bindings,
    }
    fingerprint = core_digest_value(script, identity)
    dedupe = core_digest_value(script, {"schemaVersion": "marshal.operator.review-action-dedupe.v2", "action": action, "fingerprint": fingerprint})
    claims = history.get("claims", [])
    if any(entry.get("dedupeKey") == dedupe or entry.get("freshnessFingerprint") == fingerprint for entry in claims if isinstance(entry, dict)):
        fail("action-already-claimed")

    # Re-open every authority input and compare inode metadata and raw bytes.
    for root, relative, before_raw, before_identity in tracked:
        after_raw, after_identity = read_regular(root, relative, "input-unreadable-after-preflight", MAX_FILE_BYTES)
        if after_identity != before_identity or after_raw != before_raw:
            fail("input-changed-during-preflight")
    if regular_presence(run_root, files["packetPath"]) != packet_present or git_head(worktree) != expected["sourceHead"]:
        fail("authority-changed-during-preflight")
    final_observation = observe(script, worktree, state["baseSha"])
    if action == "dispatch-reviewer":
        if final_observation.get("snapshotDigest") != packet.get("snapshotDigest") or final_observation.get("diffDigest") != packet.get("diffDigest"):
            fail("worktree-changed-during-preflight")
    elif packet_present and (final_observation.get("snapshotDigest") != current_generation_bindings.get("snapshotDigest") or final_observation.get("diffDigest") != current_generation_bindings.get("patchDigest")):
        fail("worktree-changed-during-preflight")
    elif not packet_present and final_observation != missing_observation:
        fail("worktree-changed-during-preflight")

    claim_history(script, history_authority, history_raw, history_identity, action, reason, dedupe, fingerprint, authority_binding.verify)
    history_authority.close()
    return {"ok": True, "action": action, "reasonCode": reason, "dedupeKey": dedupe, "freshnessFingerprint": fingerprint, "reviewPacketDigest": packet_digest or None, "historyClaimed": True}


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--run-root", required=True)
    parser.add_argument("--operator-root", required=True)
    parser.add_argument("--manifest", required=True)
    parser.add_argument("--worktree", required=True)
    parser.add_argument("--marshal", help="absolute stable Marshal executable")
    arguments = parser.parse_args()
    try:
        if not arguments.marshal:
            fail("core-probe-unavailable")
        result = run(arguments)
    except PreflightError as error:
        print(json.dumps({"ok": False, "action": "intervention", "reasonCode": error.reason_code}, separators=(",", ":")))
        return 2
    print(json.dumps(result, separators=(",", ":"), sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
