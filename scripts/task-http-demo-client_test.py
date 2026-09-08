#!/usr/bin/env python3
"""仅自有 loopback Fake HTTP server；不运行 Agent、Go 或外部服务。"""

import contextlib
import copy
import importlib.util
import io
import json
import os
from pathlib import Path
import socket
import tempfile
import threading
import time
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from unittest import mock


spec = importlib.util.spec_from_file_location("demo", Path(__file__).with_name("task-http-demo-client.py"))
demo = importlib.util.module_from_spec(spec)
spec.loader.exec_module(demo)
TOKEN = "ab" * 32
SECRET = "private intent and context must never be logged"
DIGEST = "sha256:" + "c" * 64


def task(status="awaiting-confirmation"):
    return {"id": "task:demo", "status": status, "revision": 9,
            "previewDigest": DIGEST, "confirmBefore": "2026-09-08T00:30:00Z",
            "preview": {"nodes": [{"id": "node:first", "work": {"objective": SECRET}}]},
            "request": {"intent": SECRET}, "reason": SECRET,
            "workers": [{"id": "run:first", "nodeId": "node:first", "status": "RUNNING",
                         "role": "implement", "run": {"prompt": SECRET}}],
            "edges": [{"from": "node:first", "to": "node:integration"}]}


class Fake:
    def __init__(self):
        self.calls = []
        self.task = task()
        self.lose = set()
        self.status = {}
        self.raw = {}
        self.delay_headers = False
        self.declared_port = None
        outer = self

        class Handler(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def log_message(self, *args):
                pass

            def handle(self):
                try:
                    super().handle()
                except (ConnectionResetError, BrokenPipeError):
                    pass

            def do_GET(self):
                self.respond()

            def do_POST(self):
                self.respond()

            def respond(self):
                body = self.rfile.read(int(self.headers.get("Content-Length", 0)))
                outer.calls.append({"method": self.command, "path": self.path,
                                    "auth": self.headers.get("Authorization"),
                                    "key": self.headers.get("Idempotency-Key"), "body": body})
                if self.path in outer.lose:
                    outer.lose.remove(self.path)
                    self.connection.shutdown(socket.SHUT_RDWR)
                    self.connection.close()
                    return
                if outer.delay_headers:
                    try:
                        # Continuous bytes would evade a per-recv inactivity timeout.
                        self.connection.sendall(b"HTTP/1.1 200 OK\r\nX-Slow: ")
                        for _ in range(40):
                            time.sleep(0.01)
                            self.connection.sendall(b"x")
                    except OSError:
                        pass
                    return
                result = outer.task
                if self.path == "/v1/capabilities":
                    result = {"profile": "task-draft/v1", "supported": ["create", "query", "confirm", "graph", "workers"], "pending": demo.PENDING}
                elif self.path.endswith("/graph"):
                    result = {"taskId": outer.task["id"], "edges": outer.task["edges"], "status": outer.task["status"]}
                elif self.path.endswith("/workers"):
                    result = {"taskId": outer.task["id"], "workers": outer.task["workers"]}
                elif self.path.endswith("/approve"):
                    result = copy.deepcopy(outer.task)
                    result["status"] = "approved"
                encoded = outer.raw.get(self.path, json.dumps(result).encode())
                code = outer.status.get(self.path, 200)
                self.send_response(code)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(encoded)))
                if code == 302:
                    self.send_header("Location", "http://127.0.0.1:" + str(outer.declared_port) + "/stolen")
                self.end_headers()
                try:
                    self.wfile.write(encoded)
                except OSError:
                    pass

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.thread = threading.Thread(target=lambda: self.server.serve_forever(poll_interval=0.01), daemon=True)

    def __enter__(self):
        self.thread.start()
        return self

    def __exit__(self, *args):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=2)

    def record(self):
        return {"url": "http://127.0.0.1:" + str(self.server.server_port), "token": TOKEN, "profile": demo.PROFILE}


class DemoTests(unittest.TestCase):
    def private(self, path, value):
        path.write_text(json.dumps(value), encoding="utf-8")
        path.chmod(0o600)
        return str(path)

    def run_cli(self, fake, directory, args):
        connection = self.private(Path(directory) / "connection", fake.record())
        output = io.StringIO()
        with contextlib.redirect_stdout(output):
            rc = demo.main(["--connection", connection] + args)
        raw = output.getvalue()
        self.assertNotIn(TOKEN, raw)
        self.assertNotIn(SECRET, raw)
        return rc, [json.loads(line) for line in raw.splitlines()]

    def test_default_create_is_private_preview_and_read_only_observation(self):
        with Fake() as fake, tempfile.TemporaryDirectory() as tmp:
            submission = self.private(Path(tmp) / "submission", {"template": "order-quote/v1", "intent": SECRET, "context": {"text": SECRET}})
            preview = Path(tmp) / "preview"
            rc, result = self.run_cli(fake, tmp, ["create", "--submission", submission, "--key", "create:key", "--preview-out", str(preview)])
            self.assertEqual(rc, 0)
            self.assertEqual(result[0]["event"], "private-preview-saved")
            self.assertFalse(result[0]["approvalRequested"])
            self.assertEqual(json.loads(preview.read_text())["preview"], fake.task["preview"])
            self.assertEqual(preview.stat().st_mode & 0o777, 0o600)
            self.assertEqual([x["path"] for x in fake.calls], ["/v1/capabilities", "/v1/tasks", "/v1/tasks/task:demo", "/v1/tasks/task:demo/graph", "/v1/tasks/task:demo/workers"])
            self.assertTrue(all(x["auth"] == "Bearer " + TOKEN for x in fake.calls))
            self.assertTrue(all(not row["deliveryComplete"] for row in result))
            self.assertEqual(result[-1]["edges"], fake.task["edges"])
            self.assertEqual(result[-1]["workers"][0]["status"], "RUNNING")
            self.assertFalse(result[-1]["atomicSnapshot"])

    def test_lost_creation_response_replays_identical_key_and_bytes_once(self):
        with Fake() as fake:
            fake.lose.add("/v1/tasks")
            client = demo.Client(fake.record())
            self.assertEqual(client.create({"intent": SECRET}, "same:key")["id"], "task:demo")
            self.assertEqual(len(fake.calls), 2)
            self.assertEqual(fake.calls[0], fake.calls[1])

    def test_explicit_approval_preserves_original_revision_and_digest(self):
        with Fake() as fake, tempfile.TemporaryDirectory() as tmp:
            preview = task()
            preview["revision"] = 9007199254740993  # No JS float round-trip.
            path = self.private(Path(tmp) / "preview", preview)
            fake.lose.add("/v1/tasks/task:demo/approve")
            rc, result = self.run_cli(fake, tmp, ["approve", "--preview", path, "--confirm-preview-digest", DIGEST, "--key", "approve:key"])
            self.assertEqual(rc, 0)
            approvals = [x for x in fake.calls if x["path"].endswith("/approve")]
            self.assertEqual(len(approvals), 2)
            self.assertEqual(approvals[0], approvals[1])
            self.assertEqual(json.loads(approvals[0]["body"]), {"expectedRevision": 9007199254740993, "previewDigest": DIGEST})
            self.assertEqual(result[0]["event"], "approval-response-received")

    def test_mismatched_confirmation_never_posts(self):
        with Fake() as fake:
            with self.assertRaisesRegex(demo.ClientError, "explicit-confirmation-mismatch"):
                demo.Client(fake.record()).approve(task(), "sha256:" + "d" * 64, "approve:key")
            self.assertEqual(fake.calls, [])

    def test_conflict_does_not_refresh_or_retry_approval(self):
        with Fake() as fake:
            fake.status["/v1/tasks/task:demo/approve"] = 409
            with self.assertRaisesRegex(demo.ClientError, "http-status-409"):
                demo.Client(fake.record()).approve(task(), DIGEST, "approve:key")
            self.assertEqual(len(fake.calls), 1)
            self.assertEqual(json.loads(fake.calls[0]["body"])["expectedRevision"], 9)

    def test_no_redirect_or_proxy_credential_forwarding(self):
        with Fake() as fake, Fake() as trap:
            fake.status["/v1/capabilities"] = 302
            fake.declared_port = trap.server.server_port
            with mock.patch.dict(os.environ, {"HTTP_PROXY": trap.record()["url"], "http_proxy": trap.record()["url"], "NO_PROXY": ""}):
                with self.assertRaisesRegex(demo.ClientError, "http-status-302"):
                    demo.Client(fake.record()).capabilities()
            self.assertEqual(trap.calls, [])

    def test_invalid_connection_never_connects(self):
        for url in ["https://127.0.0.1:80", "http://localhost:80", "http://example.com:80",
                    "http://127.0.0.1:80/", "http://127.0.0.1:80?x=1", "http://user@127.0.0.1:80",
                    "http://127.0.0.1:0", "http://127.0.0.1:65536", "http://127.0.0.1:080",
                    "http://127.0.0.1:80#secret"]:
            with self.subTest(url=url), mock.patch.object(demo.http.client.HTTPConnection, "connect", side_effect=AssertionError("network")):
                with self.assertRaises(demo.ClientError):
                    demo.Client({"url": url, "token": TOKEN, "profile": demo.PROFILE})

    def test_private_input_rejects_symlink_permissions_hardlink_and_fifo(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "private"
            self.private(path, {"x": 1})
            self.assertEqual(demo.private_json(path), {"x": 1})
            path.chmod(0o644)
            with self.assertRaises(demo.ClientError):
                demo.private_json(path)
            path.chmod(0o600)
            link = Path(tmp) / "link"
            link.symlink_to(path)
            with self.assertRaises(demo.ClientError):
                demo.private_json(link)
            hard = Path(tmp) / "hard"
            os.link(path, hard)
            with self.assertRaises(demo.ClientError):
                demo.private_json(path)
            fifo = Path(tmp) / "fifo"
            os.mkfifo(fifo, 0o600)
            with self.assertRaises(demo.ClientError):
                demo.private_json(fifo)

    def test_preview_never_overwrites_existing_file(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "preview"
            path.write_text("existing")
            with self.assertRaises(demo.ClientError):
                demo.save_preview(path, task())
            self.assertEqual(path.read_text(), "existing")

    def test_blocked_expired_and_unavailable_features_never_become_successful_delivery(self):
        for status in ["blocked", "confirmation-expired", "review-pending", "verified-awaiting-delivery"]:
            with self.subTest(status=status), Fake() as fake, tempfile.TemporaryDirectory() as tmp:
                fake.task["status"] = status
                rc, records = self.run_cli(fake, tmp, ["inspect", "--task-id", "task:demo", "--watch-seconds", "600"])
                self.assertEqual(rc, 4 if status in {"blocked", "confirmation-expired"} else 0)
                self.assertEqual(len(records), 1)
                self.assertFalse(records[0]["deliveryComplete"])
                self.assertEqual(records[0]["pending"], demo.PENDING)
                self.assertEqual(len(fake.calls), 4)

    def test_poll_deadline_never_cancels_or_reports_worker_failure(self):
        with Fake() as fake:
            fake.task["status"] = "running"
            observations = list(demo.Client(fake.record()).observe("task:demo", seconds=0.06, interval=0.01))
            self.assertEqual(observations[-1]["event"], "observation-timeout")
            self.assertFalse(observations[-1]["workerCancellationRequested"])
            self.assertFalse(observations[-1]["deliveryComplete"])
            self.assertTrue(all(call["method"] == "GET" for call in fake.calls))

    def test_absolute_timeout_bounds_slow_header_stream(self):
        with Fake() as fake:
            fake.delay_headers = True
            before = time.monotonic()
            with self.assertRaises(demo.TransportError):
                demo.Client(fake.record(), request_seconds=0.05).capabilities()
            self.assertLess(time.monotonic() - before, 0.8)
            self.assertEqual(len(fake.calls), 2)

    def test_connect_time_uses_same_absolute_request_budget(self):
        with Fake() as fake:
            real_connect = demo.http.client.HTTPConnection.connect
            real_timer = threading.Timer
            intervals = []

            def delayed_connect(connection):
                time.sleep(0.03)
                real_connect(connection)

            def timer(interval, callback):
                intervals.append(interval)
                return real_timer(interval, callback)

            with mock.patch.object(demo.http.client.HTTPConnection, "connect", delayed_connect), mock.patch.object(demo.threading, "Timer", timer):
                demo.Client(fake.record(), request_seconds=0.2).capabilities()
            self.assertEqual(len(intervals), 1)
            self.assertLess(intervals[0], 0.18)

    def test_wrong_json_types_are_sanitized_errors_not_tracebacks(self):
        for value in [None, {}, [["create"]], ["create", {}]]:
            with self.subTest(value=value), Fake() as fake:
                fake.raw["/v1/capabilities"] = json.dumps({"profile": demo.PROFILE, "supported": value}).encode()
                with self.assertRaises(demo.ClientError):
                    demo.Client(fake.record()).capabilities()
        for field, value in [("status", {}), ("status", []), ("revision", True), ("previewDigest", {}), ("id", [])]:
            with self.subTest(field=field, value=value), Fake() as fake:
                fake.task[field] = value
                with self.assertRaises(demo.ClientError):
                    list(demo.Client(fake.record()).observe("task:demo"))
        for worker in [[], {"id": {}}, {"id": "ok", "nodeId": []}]:
            with self.subTest(worker=worker):
                with self.assertRaises(demo.ClientError):
                    demo.safe_worker(worker)
        self.assertEqual(demo.safe_worker({"id": "ok", "nodeId": "ok", "status": {}, "role": []})["status"], "unknown")

    def test_malformed_oversize_unknown_projections_fail_closed(self):
        for raw in [b'{"id":"one","id":"two"}', b'{"x":NaN}', b'[]', b'x' * (demo.LIMIT + 1)]:
            with self.subTest(size=len(raw)), Fake() as fake:
                fake.raw["/v1/capabilities"] = raw
                with self.assertRaises(demo.ClientError):
                    demo.Client(fake.record()).capabilities()
        with Fake() as fake:
            fake.task["status"] = "complete"
            with self.assertRaisesRegex(demo.ClientError, "invalid-task-projection"):
                list(demo.Client(fake.record()).observe("task:demo"))

    def test_error_body_never_leaks(self):
        with Fake() as fake, tempfile.TemporaryDirectory() as tmp:
            fake.status["/v1/capabilities"] = 503
            fake.raw["/v1/capabilities"] = json.dumps({"code": SECRET + TOKEN}).encode()
            rc, records = self.run_cli(fake, tmp, ["inspect", "--task-id", "task:demo"])
            self.assertEqual(rc, 2)
            self.assertEqual(records[0]["code"], "http-status-503")

    def test_token_echo_in_id_is_redacted_from_cli(self):
        with Fake() as fake, tempfile.TemporaryDirectory() as tmp:
            fake.task["id"] = TOKEN
            rc, records = self.run_cli(fake, tmp, ["inspect", "--task-id", TOKEN])
            self.assertEqual(rc, 0)
            self.assertEqual(records[0]["taskId"], "[redacted]")


if __name__ == "__main__":
    unittest.main()
