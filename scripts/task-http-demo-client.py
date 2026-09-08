#!/usr/bin/env python3
"""Task HTTP 演示消费者：默认不批准；原始预览只写私有文件，不打印提示词。

create --submission FILE --key KEY --preview-out NEW_FILE
approve --preview FILE --confirm-preview-digest sha256:... --key KEY
inspect --task-id ID [--watch-seconds 60]
cancel --task-id ID --expected-revision N --key KEY [--watch-seconds 60]
download --task-id ID --output-dir NEW_DIR [--run-oracle]

各命令均需 --connection FILE（服务端生成的 0600 连接文件）。批准必须
先人工查看预览文件，再原样提供其摘要；客户端不是验收器或权威状态源。
download 只接受 completed Task 的固定订单报价 bundle；--run-oracle 才执行
本仓库固定验收器（可信代码、本机普通用户，不是恶意代码沙箱）。仅下载
不等于业务成功，消费结果不回写 Core。cancel 仅在服务声明支持后发送；
202/cancelling 仅表示受理，cancelled 才表示 Task 收口，不表示所有节点曾启动。
"""

import argparse
import hashlib
import http.client
import io
import json
import os
from pathlib import Path
import re
import selectors
import signal
import socket
import stat
import subprocess
import sys
import threading
import time
import urllib.parse
import zipfile
import zlib


PROFILE = "task-draft/v1"
LIMIT = 2 << 20
BUNDLE_LIMIT = 8 << 20
DELIVERY_FILES = ("quote_api.py", "quote_client.py", "quote_delivery.json")
FILE_LIMITS = (60000, 60000, 16384)
ORACLE_SHA = "dfa7965c65b896e5d0542fa7edfe5d9699150e4c76f7dbc1a5b1f570ab0db7f2"
ID = re.compile(r"[A-Za-z0-9][A-Za-z0-9._:-]{0,159}\Z")
DIGEST = re.compile(r"sha256:[0-9a-f]{64}\Z")
PENDING = ["cancel", "automatic-decision", "artifact-download"]
STATUSES = {"awaiting-confirmation", "confirmation-expired", "approved",
            "running", "blocked", "review-pending", "verified-awaiting-delivery", "completed",
            "cancelling", "cancelled"}
STOPS = {"awaiting-confirmation", "confirmation-expired", "blocked",
         "review-pending", "verified-awaiting-delivery", "completed", "cancelled"}


class ClientError(Exception):
    """Only constant, non-sensitive diagnostic codes may cross this boundary."""


class TransportError(ClientError):
    pass


def decode(raw):
    def pairs(items):
        result = {}
        for key, value in items:
            if key in result:
                raise ValueError("duplicate")
            result[key] = value
        return result

    try:
        value = json.loads(raw, object_pairs_hook=pairs,
                           parse_constant=lambda _: (_ for _ in ()).throw(ValueError()))
        if not isinstance(value, dict):
            raise ValueError()
        return value
    except (ValueError, UnicodeError, RecursionError):
        raise ClientError("invalid-json") from None


def private_json(path, limit=LIMIT):
    """Read a held regular, singly linked, current-user private file."""
    try:
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
        with os.fdopen(fd, "rb") as handle:
            st = os.fstat(handle.fileno())
            if (not stat.S_ISREG(st.st_mode) or st.st_uid != os.getuid()
                    or st.st_mode & 0o077 or st.st_nlink != 1 or st.st_size > limit):
                raise ClientError("input-file-not-private")
            raw = handle.read(limit + 1)
            if len(raw) > limit:
                raise ClientError("input-too-large")
            return decode(raw)
    except OSError:
        raise ClientError("input-file-unavailable") from None


def valid_id(value):
    return isinstance(value, str) and ID.fullmatch(value) is not None


def preview_identity(value):
    if (not valid_id(value.get("id")) or type(value.get("revision")) is not int
            or not 1 <= value["revision"] <= (1 << 63) - 1
            or not isinstance(value.get("previewDigest"), str)
            or not DIGEST.fullmatch(value["previewDigest"])):
        raise ClientError("invalid-preview-identity")
    return value["id"]


class Client:
    def __init__(self, record, request_seconds=15):
        try:
            url = urllib.parse.urlsplit(record.get("url", ""))
            port = url.port
            token = record.get("token")
            if (record.get("profile") != PROFILE or url.scheme != "http"
                    or url.hostname != "127.0.0.1" or url.username is not None
                    or url.password is not None or url.path or url.query or url.fragment
                    or port is None or not 1 <= port <= 65535
                    or record["url"] != "http://127.0.0.1:" + str(port)
                    or not isinstance(token, str)
                    or re.fullmatch(r"[0-9a-f]{64}", token) is None):
                raise ValueError()
        except (ValueError, TypeError, AttributeError):
            raise ClientError("invalid-loopback-connection") from None
        self.port, self.token = port, token
        self.request_seconds = request_seconds
        self.pending = list(PENDING)

    def request(self, method, path, value=None, key=None, deadline=None, binary=False):
        # One transport retry only; writes reuse the exact bytes and caller key.
        raw = None if value is None else json.dumps(value, separators=(",", ":")).encode()
        if raw is not None and len(raw) > 32 << 10:
            raise ClientError("request-too-large")
        for attempt in range(2):
            try:
                return self._once(method, path, raw, key, deadline, binary)
            except TransportError:
                if attempt or (deadline is not None and time.monotonic() >= deadline):
                    raise

    def _once(self, method, path, raw, key, deadline, binary=False):
        started = time.monotonic()
        timeout = self.request_seconds
        if deadline is not None:
            timeout = min(timeout, deadline - time.monotonic())
        if timeout <= 0:
            raise TransportError("observation-timeout")
        connection = http.client.HTTPConnection("127.0.0.1", self.port, timeout=timeout)
        timer = None
        expired = threading.Event()
        try:
            # Direct numeric loopback connection: no DNS, proxy or redirect handling.
            connection.connect()
            remaining = timeout - (time.monotonic() - started)
            if remaining <= 0:
                raise TransportError("transport-deadline")
            held_socket = connection.sock

            def expire():
                expired.set()
                try:
                    held_socket.shutdown(socket.SHUT_RDWR)
                except OSError:
                    pass

            timer = threading.Timer(remaining, expire)
            timer.daemon = True
            timer.start()
            headers = {"Authorization": "Bearer " + self.token,
                       "Accept": "application/zip" if binary else "application/json"}
            if raw is not None:
                headers["Content-Type"] = "application/json"
            if key is not None:
                if not valid_id(key) or len(key) > 128:
                    raise ClientError("invalid-idempotency-key")
                headers["Idempotency-Key"] = key
            connection.request(method, path, body=raw, headers=headers)
            response = connection.getresponse()
            if expired.is_set():
                raise TransportError("transport-deadline")
            if response.status >= 300:
                # Never echo response text, Location, exception or reason (may contain secrets).
                raise ClientError("http-status-" + str(response.status))
            if (response.getheader("Content-Encoding") is not None
                    or response.getheader("Content-Type", "").split(";", 1)[0] != ("application/zip" if binary else "application/json")):
                raise ClientError("invalid-response-content-type")
            limit = BUNDLE_LIMIT if binary else LIMIT
            body = response.read(limit + 1)
            if expired.is_set():
                raise TransportError("transport-deadline")
            if len(body) > limit:
                raise ClientError("response-too-large")
            return body if binary else decode(body)
        except (OSError, http.client.HTTPException):
            raise TransportError("transport-unavailable-operation-may-have-committed") from None
        finally:
            if timer is not None:
                timer.cancel()
            connection.close()

    def capabilities(self):
        caps = self.request("GET", "/v1/capabilities")
        if (caps.get("profile") != PROFILE or not isinstance(caps.get("supported"), list)
                or any(not isinstance(x, str) for x in caps["supported"])
                or not {"create", "query", "confirm", "graph", "workers"}.issubset(caps["supported"])):
            raise ClientError("unsupported-task-profile")
        self.pending = [cap for cap in PENDING if cap not in caps["supported"]]
        return caps

    def create(self, submission, key):
        return self.request("POST", "/v1/tasks", submission, key)

    def approve(self, preview, digest, key):
        task_id = preview_identity(preview)
        if digest != preview["previewDigest"]:
            raise ClientError("explicit-confirmation-mismatch")
        # Deliberately no fresh-GET revision substitution. Server alone resolves stale/replay.
        return self.request("POST", "/v1/tasks/" + task_id + "/approve",
                            {"expectedRevision": preview["revision"],
                             "previewDigest": preview["previewDigest"]}, key)

    def download(self, task_id, output_dir, run_oracle=False):
        if not valid_id(task_id):
            raise ClientError("invalid-task-id")
        deadline = time.monotonic() + self.request_seconds * 3
        task = self.request("GET", "/v1/tasks/" + task_id, deadline=deadline)
        if preview_identity(task) != task_id or task.get("status") != "completed":
            raise ClientError("task-delivery-not-ready")
        manifest = task.get("delivery")
        validate_manifest(manifest, task_id)
        bundle = self.request("GET", "/v1/tasks/" + task_id + "/artifact", deadline=deadline, binary=True)
        files = validate_bundle(manifest, bundle)
        folder = materialize(output_dir, files)
        if run_oracle:
            run_delivery_oracle(folder, files)
        return {"event": "delivery-consumed" if run_oracle else "delivery-downloaded",
                "taskId": task_id, "contentDigest": manifest["contentDigest"],
                "factDigest": manifest["factDigest"], "fileCount": len(files),
                "businessOracle": "passed" if run_oracle else "not-run",
                "deliveryComplete": run_oracle, "coreStateMutated": False,
                "profile": "trusted-order-quote-consumer/v1", "pending": self.pending,
                "productionReleaseProven": False}

    def cancel(self, task_id, expected_revision, key):
        if (not valid_id(task_id) or type(expected_revision) is not int
                or not 0 < expected_revision <= (1 << 63) - 1):
            raise ClientError("invalid-cancellation-request")
        if "cancel" in self.pending:
            raise ClientError("task-cancellation-not-supported")
        # No fresh revision substitution and no PID/Run-level fallback.
        result = self.request("POST", "/v1/tasks/" + task_id + "/cancel",
                              {"expectedRevision": expected_revision}, key)
        if (preview_identity(result) != task_id
                or not isinstance(result.get("status"), str)
                or result.get("status") not in {"cancelling", "cancelled"}
                or result.get("cancellationRequested") is not True):
            raise ClientError("invalid-cancellation-response")
        return {"event": "cancellation-response-received", "taskId": task_id,
                "status": result["status"], "taskCancellationRequested": True,
                "taskCancellationComplete": result["status"] == "cancelled",
                "deliveryComplete": False, "pending": self.pending}

    def observe(self, task_id, seconds=0, interval=2):
        if not valid_id(task_id):
            raise ClientError("invalid-task-id")
        started = time.monotonic()
        deadline = started + (seconds if seconds else self.request_seconds * 3)
        while True:
            try:
                task = self.request("GET", "/v1/tasks/" + task_id, deadline=deadline)
                graph = self.request("GET", "/v1/tasks/" + task_id + "/graph", deadline=deadline)
                workers = self.request("GET", "/v1/tasks/" + task_id + "/workers", deadline=deadline)
                if (preview_identity(task) != task_id or graph.get("taskId") != task_id
                        or workers.get("taskId") != task_id or not isinstance(task.get("status"), str)
                        or task["status"] not in STATUSES):
                    raise ClientError("invalid-task-projection")
                # Independent GETs are not an atomic snapshot. Never infer Task status from workers.
                worker_list, edges = workers.get("workers"), graph.get("edges")
                if not isinstance(worker_list, list) or not isinstance(edges, list):
                    raise ClientError("invalid-task-projection")
                summary = {"event": "observation", "taskId": task_id,
                           "status": task["status"], "workerCount": len(worker_list),
                           "edgeCount": len(edges), "atomicSnapshot": False,
                           "elapsedSeconds": round(time.monotonic() - started, 3),
                           "deliveryComplete": False,
                           "taskCancellationComplete": task["status"] == "cancelled",
                           "pending": self.pending + (["business-consumption"] if task["status"] == "completed" else [])}
                # Return per-worker state/role only from validated enums; never the nested Run or work.
                summary["workers"] = [safe_worker(w) for w in worker_list]
                if any(not isinstance(e, dict) or not valid_id(e.get("from"))
                       or not valid_id(e.get("to")) for e in edges):
                    raise ClientError("invalid-graph-projection")
                summary["edges"] = [{"from": e["from"], "to": e["to"]} for e in edges]
                yield summary
                if task["status"] in STOPS or not seconds:
                    return
            except TransportError:
                if time.monotonic() < deadline:
                    raise
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                yield {"event": "observation-timeout", "taskId": task_id,
                       "elapsedSeconds": round(time.monotonic() - started, 3),
                       "workerCancellationRequested": False, "deliveryComplete": False,
                       "pending": self.pending}
                return
            time.sleep(min(interval, remaining))


def digest_bytes(value):
    return "sha256:" + hashlib.sha256(value).hexdigest()


def validate_manifest(manifest, task_id):
    # Shape/content binding only: the client does not recreate a Core ledger or Decision.
    fields = {"goalId", "outcomeFactDigest", "planFactDigest", "integrationRunId",
              "integrationBaseSha", "candidateDigests", "patchDigests", "decisionDigests",
              "files", "contentDigest", "contentBytes", "mediaType", "factDigest"}
    if not isinstance(manifest, dict) or set(manifest) != fields:
        raise ClientError("invalid-delivery-manifest")
    if (manifest["goalId"] != task_id or not valid_id(manifest["integrationRunId"])
            or not isinstance(manifest["integrationBaseSha"], str)
            or re.fullmatch(r"[0-9a-f]{40}", manifest["integrationBaseSha"]) is None
            or type(manifest["contentBytes"]) is not int
            or not 0 < manifest["contentBytes"] <= BUNDLE_LIMIT
            or manifest["mediaType"] != "application/zip"):
        raise ClientError("invalid-delivery-manifest")
    digests = [manifest[key] for key in ("outcomeFactDigest", "planFactDigest", "contentDigest", "factDigest")]
    for key in ("candidateDigests", "patchDigests", "decisionDigests"):
        if not isinstance(manifest[key], list) or len(manifest[key]) != 3:
            raise ClientError("invalid-delivery-manifest")
        digests.extend(manifest[key])
    if any(not isinstance(d, str) or DIGEST.fullmatch(d) is None for d in digests):
        raise ClientError("invalid-delivery-manifest")
    files = manifest["files"]
    if not isinstance(files, list) or len(files) != len(DELIVERY_FILES):
        raise ClientError("invalid-delivery-files")
    for record, name, limit in zip(files, DELIVERY_FILES, FILE_LIMITS):
        if (not isinstance(record, dict) or set(record) != {"path", "sha256", "bytes"}
                or record["path"] != name or type(record["bytes"]) is not int
                or not 0 < record["bytes"] <= limit or not isinstance(record["sha256"], str)
                or DIGEST.fullmatch(record["sha256"]) is None):
            raise ClientError("invalid-delivery-files")


def validate_bundle(manifest, bundle):
    if len(bundle) != manifest["contentBytes"] or digest_bytes(bundle) != manifest["contentDigest"]:
        raise ClientError("delivery-content-mismatch")
    try:
        with zipfile.ZipFile(io.BytesIO(bundle), "r") as archive:
            entries = archive.infolist()
            if tuple(info.filename for info in entries) != DELIVERY_FILES or archive.comment:
                raise ClientError("invalid-delivery-archive")
            result = {}
            for info, expected in zip(entries, manifest["files"]):
                mode = info.external_attr >> 16
                if (info.orig_filename != info.filename or info.is_dir()
                        or info.create_system != 3 or mode != stat.S_IFREG | 0o644
                        or info.flag_bits & 1 or len(info.extra) > 256 or info.comment
                        or info.compress_type not in {zipfile.ZIP_STORED, zipfile.ZIP_DEFLATED}
                        or info.file_size != expected["bytes"]):
                    raise ClientError("invalid-delivery-archive")
                with archive.open(info, "r") as source:
                    content = source.read(expected["bytes"] + 1)
                if len(content) != expected["bytes"] or digest_bytes(content) != expected["sha256"]:
                    raise ClientError("delivery-file-mismatch")
                result[info.filename] = content
            return result
    except (OSError, ValueError, RuntimeError, NotImplementedError, EOFError, zipfile.BadZipFile, zlib.error):
        raise ClientError("invalid-delivery-archive") from None


def materialize(output_dir, files):
    # Fresh directory only; flat frozen names, no extractall or archive-chosen paths.
    folder = Path(output_dir).absolute()
    parent = directory = None
    try:
        parent = os.open(folder.parent, os.O_RDONLY | os.O_DIRECTORY)
        os.mkdir(folder.name, 0o700, dir_fd=parent)
        directory = os.open(folder.name, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=parent)
        for name in DELIVERY_FILES:
            fd = os.open(name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o644, dir_fd=directory)
            with os.fdopen(fd, "wb") as target:
                target.write(files[name])
                target.flush()
                os.fchmod(target.fileno(), 0o644)
                os.fsync(target.fileno())
        os.fsync(directory)
        os.fsync(parent)
        # Returned canonical path is client-selected, never taken from a manifest.
        return folder.resolve(strict=True)
    except OSError:
        # Keep partial evidence for the operator; never reuse/overwrite it on retry.
        raise ClientError("delivery-output-unavailable-use-new-directory") from None
    finally:
        if directory is not None:
            os.close(directory)
        if parent is not None:
            os.close(parent)


def recheck_files(folder, files):
    try:
        if sorted(os.listdir(folder)) != list(DELIVERY_FILES):
            raise ClientError("delivery-files-changed")
        for name, content in files.items():
            fd = os.open(folder / name, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
            with os.fdopen(fd, "rb") as source:
                st = os.fstat(source.fileno())
                if (not stat.S_ISREG(st.st_mode) or st.st_nlink != 1 or st.st_mode & 0o7777 != 0o644
                        or st.st_size != len(content) or source.read(len(content) + 1) != content):
                    raise ClientError("delivery-files-changed")
    except OSError:
        raise ClientError("delivery-files-changed") from None


def run_delivery_oracle(folder, files):
    # Local pinned source, never a command or program supplied in HTTP JSON.
    # A trusted-code consumer, not a sandbox against same-UID malicious Python.
    oracle = Path(__file__).with_name("order-quote-team-oracle.py")
    try:
        fd = os.open(oracle, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
        with os.fdopen(fd, "rb") as source:
            if not stat.S_ISREG(os.fstat(source.fileno()).st_mode):
                raise ClientError("fixed-oracle-unavailable")
            code = source.read(65537)
    except OSError:
        raise ClientError("fixed-oracle-unavailable") from None
    if hashlib.sha256(code).hexdigest() != ORACLE_SHA:
        raise ClientError("fixed-oracle-drift")
    recheck_files(folder, files)
    # Feed the held, checked bytes, avoiding a verify-path-then-execute-path race.
    bootstrap = ("import hashlib,sys; data=sys.stdin.buffer.read(65537); "
                 "hashlib.sha256(data).hexdigest()==sys.argv[1] or sys.exit(2); "
                 "sys.argv=['trusted-order-quote-oracle.py','--api','quote_api.py',"
                 "'--client','quote_client.py','--delivery','quote_delivery.json']; "
                 "exec(compile(data,sys.argv[0],'exec'),{'__name__':'__main__','__file__':sys.argv[0]})")
    bounded_oracle_process([sys.executable, "-I", "-B", "-c", bootstrap, ORACLE_SHA], code, folder)
    recheck_files(folder, files)


def bounded_oracle_process(argv, code, folder, timeout=30):
    process = None
    status_read = status_write = None
    try:
        deadline = time.monotonic() + timeout
        # Keep the session leader alive until group cleanup. Waiting/reaping
        # the oracle directly would release its PID while descendants can still
        # own our pipes (or continue silently after closing them). The tiny
        # guard reports only its direct child's exit status, then waits for us;
        # it is not an independent worker, sandbox or platform supervisor.
        guard = ("import os,signal,sys\n"
                 "fd=int(sys.argv[1]); child=os.fork()\n"
                 "if child==0:\n"
                 " os.close(fd); os.execv(sys.argv[2],sys.argv[2:])\n"
                 "os.close(0); os.close(1); os.close(2)\n"
                 "_,status=os.waitpid(child,0)\n"
                 "result=os.WEXITSTATUS(status) if os.WIFEXITED(status) else -os.WTERMSIG(status)\n"
                 "os.write(fd,str(result).encode('ascii')); os.close(fd)\n"
                 "while True: signal.pause()\n")
        status_read, status_write = os.pipe()
        process = subprocess.Popen([sys.executable, "-I", "-B", "-c", guard, str(status_write)] + argv,
                                   cwd=folder, env={"PATH": "/usr/bin:/bin", "LANG": "C.UTF-8"},
                                   stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                   start_new_session=True, pass_fds=(status_write,))
        os.close(status_write)
        status_write = None
        output = {"stdout": bytearray(), "stderr": bytearray(), "status": bytearray()}
        with selectors.DefaultSelector() as selector:
            os.set_blocking(process.stdin.fileno(), False)
            selector.register(process.stdin, selectors.EVENT_WRITE, "stdin")
            offset = 0
            os.set_blocking(status_read, False)
            selector.register(status_read, selectors.EVENT_READ, "status")
            for stream, key in ((process.stdout, "stdout"), (process.stderr, "stderr")):
                os.set_blocking(stream.fileno(), False)
                selector.register(stream, selectors.EVENT_READ, key)
            while selector.get_map():
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    raise ClientError("business-oracle-timeout")
                for selected, _ in selector.select(remaining):
                    if selected.data == "stdin":
                        offset += os.write(selected.fd, code[offset:offset + 4096])
                        if offset == len(code):
                            selector.unregister(selected.fileobj)
                            process.stdin.close()
                        continue
                    data = os.read(selected.fd, 4097)
                    output[selected.data].extend(data)
                    if sum(len(value) for value in output.values()) > 4000:
                        raise ClientError("business-oracle-output-limit")
                    if not data:
                        selector.unregister(selected.fileobj)
        # No poll/wait here: the guard's PID must remain reserved until the
        # finally block signals the whole owned group, even on success.
        if bytes(output["status"]) != b"0" or output["stderr"]:
            raise ClientError("business-oracle-failed")
        verdict = decode(bytes(output["stdout"]))
        if verdict != {"checks": 34, "scope": "integration"} or type(verdict.get("checks")) is not int:
            raise ClientError("business-oracle-failed")
    except (OSError, BrokenPipeError):
        raise ClientError("business-oracle-unavailable") from None
    finally:
        if process is not None:
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            process.wait(timeout=5)
            for stream in (process.stdin, process.stdout, process.stderr):
                stream.close()
        for descriptor in (status_read, status_write):
            if descriptor is not None:
                os.close(descriptor)


def safe_worker(worker):
    if not isinstance(worker, dict) or not valid_id(worker.get("id")) or not valid_id(worker.get("nodeId")):
        raise ClientError("invalid-worker-projection")
    # Free-text roles and unknown states remain private; the authority payload is not a log.
    states = {"planned", "creating", "busy", "CREATED", "READY", "RUNNING", "VERIFYING",
              "REVIEW_PENDING", "ACCEPTED", "FAILED", "BLOCKED", "CANCELLED", "REWORK_REQUESTED"}
    return {"id": worker["id"], "nodeId": worker["nodeId"],
            "status": worker.get("status") if isinstance(worker.get("status"), str) and worker["status"] in states else "unknown",
            "role": worker.get("role") if isinstance(worker.get("role"), str) and worker["role"] in {"implement", "integrate"} else "unreported"}


def save_preview(path, task):
    preview_identity(task)
    if not isinstance(task.get("preview"), dict):
        raise ClientError("invalid-preview")
    # Original preview is preserved, not reconstructed from a display summary.
    value = {key: task[key] for key in ("id", "revision", "previewDigest", "preview")}
    value["confirmBefore"] = task.get("confirmBefore")
    try:
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            json.dump(value, handle, ensure_ascii=False, indent=2)
            handle.write("\n")
            handle.flush()
            os.fsync(handle.fileno())
    except OSError:
        raise ClientError("preview-save-failed-replay-create-with-same-key") from None


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--connection", required=True)
    commands = parser.add_subparsers(dest="command", required=True)
    create = commands.add_parser("create")
    create.add_argument("--submission", required=True)
    create.add_argument("--key", required=True)
    create.add_argument("--preview-out", required=True)
    approve = commands.add_parser("approve")
    approve.add_argument("--preview", required=True)
    approve.add_argument("--confirm-preview-digest", required=True)
    approve.add_argument("--key", required=True)
    inspect = commands.add_parser("inspect")
    inspect.add_argument("--task-id", required=True)
    cancel = commands.add_parser("cancel")
    cancel.add_argument("--task-id", required=True)
    cancel.add_argument("--expected-revision", type=int, required=True)
    cancel.add_argument("--key", required=True)
    download = commands.add_parser("download")
    download.add_argument("--task-id", required=True)
    download.add_argument("--output-dir", required=True)
    download.add_argument("--run-oracle", action="store_true",
                          help="在新目录运行固定业务验收器；仅适用于可信代码，并非沙箱")
    for command in (approve, inspect, cancel):
        command.add_argument("--watch-seconds", type=int, default=0, choices=range(601), metavar="0..600")
    args = parser.parse_args(argv)
    started = time.monotonic()
    client = None

    def emit(value):
        raw = json.dumps(value, ensure_ascii=False)
        if client is not None:
            raw = raw.replace(client.token, "[redacted]")
        print(raw, flush=True)

    try:
        client = Client(private_json(args.connection, 4096))
        client.capabilities()
        if args.command == "download":
            result = client.download(args.task_id, args.output_dir, args.run_oracle)
            result["elapsedSeconds"] = round(time.monotonic() - started, 3)
            emit(result)
            return 0
        if args.command == "cancel":
            emit(client.cancel(args.task_id, args.expected_revision, args.key))
            task_id, seconds = args.task_id, args.watch_seconds
        elif args.command == "create":
            task = client.create(private_json(args.submission, 32 << 10), args.key)
            task_id = preview_identity(task)
            save_preview(args.preview_out, task)
            emit({"event": "private-preview-saved", "taskId": task_id,
                  "revision": task["revision"], "previewDigest": task["previewDigest"],
                  "approvalRequested": False, "deliveryComplete": False,
                  "elapsedSeconds": round(time.monotonic() - started, 3), "pending": client.pending})
            seconds = 0
        elif args.command == "approve":
            preview = private_json(args.preview)
            task_id = preview_identity(preview)
            result = client.approve(preview, args.confirm_preview_digest, args.key)
            if preview_identity(result) != task_id:
                raise ClientError("invalid-task-projection")
            emit({"event": "approval-response-received", "taskId": task_id,
                  "deliveryComplete": False, "pending": client.pending})
            seconds = args.watch_seconds
        else:
            task_id, seconds = args.task_id, args.watch_seconds
        for observation in client.observe(task_id, seconds):
            emit(observation)
            if observation["event"] == "observation-timeout":
                return 3
            if observation.get("status") in {"blocked", "confirmation-expired"}:
                return 4
        return 0  # Successful client operation, explicitly NOT a delivery verdict.
    except ClientError as exc:
        error = {"event": "client-error", "code": str(exc), "deliveryComplete": False,
                 "pending": client.pending if client is not None else PENDING,
                 "elapsedSeconds": round(time.monotonic() - started, 3)}
        if args.command == "cancel" and valid_id(args.task_id):
            error["taskId"] = args.task_id
            if isinstance(exc, TransportError):
                error.update(operationMayHaveCommitted=True,
                             recovery="retry-original-cancel-key-and-revision")
        emit(error)
        return 2
    except KeyboardInterrupt:
        interrupted = {"event": "observation-interrupted", "deliveryComplete": False,
                       "pending": client.pending if client is not None else PENDING}
        if args.command == "cancel" and valid_id(args.task_id):
            interrupted.update(taskId=args.task_id, taskCancellationStatus="unknown",
                               recovery="inspect-original-task-or-retry-original-cancel-key-and-revision")
        else:
            interrupted["workerCancellationRequested"] = False
        emit(interrupted)
        return 130


if __name__ == "__main__":
    sys.exit(main())
