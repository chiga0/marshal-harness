#!/usr/bin/env python3
"""Task HTTP 演示消费者：默认不批准；原始预览只写私有文件，不打印提示词。

create --submission FILE --key KEY --preview-out NEW_FILE
approve --preview FILE --confirm-preview-digest sha256:... --key KEY
inspect --task-id ID [--watch-seconds 60]

各命令均需 --connection FILE（服务端生成的 0600 连接文件）。批准必须
先人工查看预览文件，再原样提供其摘要；客户端不是验收器或权威状态源。
task-draft/v1 尚无取消、自动 Decision、成果下载，不能证明完整交付。
"""

import argparse
import http.client
import json
import os
import re
import socket
import stat
import sys
import threading
import time
import urllib.parse


PROFILE = "task-draft/v1"
LIMIT = 2 << 20
ID = re.compile(r"[A-Za-z0-9][A-Za-z0-9._:-]{0,159}\Z")
DIGEST = re.compile(r"sha256:[0-9a-f]{64}\Z")
PENDING = ["cancel", "automatic-decision", "artifact-download"]
STATUSES = {"awaiting-confirmation", "confirmation-expired", "approved",
            "running", "blocked", "review-pending", "verified-awaiting-delivery"}
STOPS = {"awaiting-confirmation", "confirmation-expired", "blocked",
         "review-pending", "verified-awaiting-delivery"}


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

    def request(self, method, path, value=None, key=None, deadline=None):
        # One transport retry only; writes reuse the exact bytes and caller key.
        raw = None if value is None else json.dumps(value, separators=(",", ":")).encode()
        if raw is not None and len(raw) > 32 << 10:
            raise ClientError("request-too-large")
        for attempt in range(2):
            try:
                return self._once(method, path, raw, key, deadline)
            except TransportError:
                if attempt or (deadline is not None and time.monotonic() >= deadline):
                    raise

    def _once(self, method, path, raw, key, deadline):
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
                       "Accept": "application/json"}
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
                    or response.getheader("Content-Type", "").split(";", 1)[0] != "application/json"):
                raise ClientError("invalid-response-content-type")
            body = response.read(LIMIT + 1)
            if expired.is_set():
                raise TransportError("transport-deadline")
            if len(body) > LIMIT:
                raise ClientError("response-too-large")
            return decode(body)
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
                           "deliveryComplete": False, "pending": PENDING}
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
                       "pending": PENDING}
                return
            time.sleep(min(interval, remaining))


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
    for command in (approve, inspect):
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
        if args.command == "create":
            task = client.create(private_json(args.submission, 32 << 10), args.key)
            task_id = preview_identity(task)
            save_preview(args.preview_out, task)
            emit({"event": "private-preview-saved", "taskId": task_id,
                  "revision": task["revision"], "previewDigest": task["previewDigest"],
                  "approvalRequested": False, "deliveryComplete": False,
                  "elapsedSeconds": round(time.monotonic() - started, 3), "pending": PENDING})
            seconds = 0
        elif args.command == "approve":
            preview = private_json(args.preview)
            task_id = preview_identity(preview)
            result = client.approve(preview, args.confirm_preview_digest, args.key)
            if preview_identity(result) != task_id:
                raise ClientError("invalid-task-projection")
            emit({"event": "approval-response-received", "taskId": task_id,
                  "deliveryComplete": False, "pending": PENDING})
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
        emit({"event": "client-error", "code": str(exc), "deliveryComplete": False,
              "pending": PENDING, "elapsedSeconds": round(time.monotonic() - started, 3)})
        return 2
    except KeyboardInterrupt:
        emit({"event": "observation-interrupted", "workerCancellationRequested": False,
              "deliveryComplete": False, "pending": PENDING})
        return 130


if __name__ == "__main__":
    sys.exit(main())
