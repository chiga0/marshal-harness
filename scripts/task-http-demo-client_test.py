#!/usr/bin/env python3
"""仅自有 loopback Fake HTTP server；不运行 Agent、Go 或外部服务。"""

import contextlib
import copy
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import socket
import stat
import sys
import tempfile
import threading
import time
import unittest
import zipfile
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
        self.media = {}
        self.delay_headers = False
        self.declared_port = None
        self.supported = ["create", "query", "confirm", "graph", "workers"]
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
                    result = {"profile": "task-draft/v1", "supported": outer.supported, "pending": demo.PENDING}
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
                self.send_header("Content-Type", outer.media.get(self.path, "application/json"))
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
    def cancellation(self, fake, status="cancelling"):
        fake.supported.append("cancel")
        fake.task.update(status=status, cancellationRequested=True)
        fake.status["/v1/tasks/task:demo/cancel"] = 202

    def test_cancel_replays_original_revision_and_key_without_claiming_stopped(self):
        with Fake() as fake, tempfile.TemporaryDirectory() as tmp:
            self.cancellation(fake)
            fake.lose.add("/v1/tasks/task:demo/cancel")
            rc, rows = self.run_cli(fake, tmp, ["cancel", "--task-id", "task:demo",
                "--expected-revision", "9007199254740993", "--key", "cancel:key"])
            self.assertEqual(rc, 0, rows)
            sent = [call for call in fake.calls if call["method"] == "POST"]
            self.assertEqual(len(sent), 2)
            self.assertEqual(sent[0], sent[1])
            self.assertEqual(json.loads(sent[0]["body"]), {"expectedRevision": 9007199254740993})
            self.assertEqual(sent[0]["key"], "cancel:key")
            self.assertEqual(rows[0]["status"], "cancelling")
            self.assertFalse(rows[0]["taskCancellationComplete"])
            self.assertTrue(all(not row["deliveryComplete"] for row in rows))
            self.assertNotIn("cancel", rows[0]["pending"])

    def test_cancel_capability_missing_never_sends_mutation(self):
        with Fake() as fake, tempfile.TemporaryDirectory() as tmp:
            rc, rows = self.run_cli(fake, tmp, ["cancel", "--task-id", "task:demo",
                "--expected-revision", "9", "--key", "cancel:key"])
            self.assertEqual(rc, 2)
            self.assertEqual(rows[0]["code"], "task-cancellation-not-supported")
            self.assertEqual([c["method"] for c in fake.calls], ["GET"])

    def test_cancel_conflict_never_refreshes_or_creates_replacement(self):
        with Fake() as fake, tempfile.TemporaryDirectory() as tmp:
            self.cancellation(fake)
            fake.status["/v1/tasks/task:demo/cancel"] = 409
            rc, rows = self.run_cli(fake, tmp, ["cancel", "--task-id", "task:demo",
                "--expected-revision", "8", "--key", "cancel:key"])
            self.assertEqual(rc, 2)
            self.assertEqual(rows[0]["code"], "http-status-409")
            self.assertEqual(rows[0]["taskId"], "task:demo")
            self.assertEqual(len(fake.calls), 2)

    def test_cancel_validates_identity_int64_revision_and_response(self):
        with Fake() as fake:
            self.cancellation(fake)
            client = demo.Client(fake.record())
            client.capabilities()
            for revision in (True, 0, -1, 1.5, "9", 1 << 63):
                with self.subTest(revision=revision), self.assertRaises(demo.ClientError):
                    client.cancel("task:demo", revision, "key")
            self.assertEqual(len(fake.calls), 1)
            for changes in ({"id": "other"}, {"status": "completed"}, {"status": []},
                            {"status": {}}, {"cancellationRequested": False}):
                original = copy.deepcopy(fake.task)
                fake.task.update(changes)
                with self.assertRaisesRegex(demo.ClientError, "invalid-cancellation-response"):
                    client.cancel("task:demo", 9, "key")
                fake.task = original

    def test_cancelled_response_and_observation_are_not_delivery(self):
        with Fake() as fake, tempfile.TemporaryDirectory() as tmp:
            self.cancellation(fake, "cancelled")
            rc, rows = self.run_cli(fake, tmp, ["cancel", "--task-id", "task:demo",
                "--expected-revision", "9", "--key", "cancel:key", "--watch-seconds", "10"])
            self.assertEqual(rc, 0, rows)
            self.assertEqual(len(rows), 2)
            self.assertTrue(all(row["taskCancellationComplete"] for row in rows))
            self.assertTrue(all(not row["deliveryComplete"] for row in rows))

    def test_cancel_response_loss_preserves_original_task_for_recovery(self):
        with Fake() as fake, tempfile.TemporaryDirectory() as tmp:
            self.cancellation(fake)
            original = demo.Client._once
            def lose(client, method, path, *args, **kwargs):
                response = original(client, method, path, *args, **kwargs)
                if path.endswith("/cancel"):
                    raise demo.TransportError("transport-unavailable-operation-may-have-committed")
                return response
            with mock.patch.object(demo.Client, "_once", lose):
                rc, rows = self.run_cli(fake, tmp, ["cancel", "--task-id", "task:demo",
                    "--expected-revision", "9", "--key", "cancel:key"])
            self.assertEqual(rc, 2)
            self.assertEqual(rows[0]["taskId"], "task:demo")
            self.assertTrue(rows[0]["operationMayHaveCommitted"])
            self.assertIn("original-cancel-key", rows[0]["recovery"])
            sent = [c for c in fake.calls if c["method"] == "POST"]
            self.assertEqual(len(sent), 2)
            self.assertEqual(sent[0], sent[1])

    def test_interrupt_after_cancel_request_does_not_deny_possible_cancellation(self):
        with Fake() as fake, tempfile.TemporaryDirectory() as tmp:
            self.cancellation(fake)
            with mock.patch.object(demo.Client, "observe", side_effect=KeyboardInterrupt):
                rc, rows = self.run_cli(fake, tmp, ["cancel", "--task-id", "task:demo",
                    "--expected-revision", "9", "--key", "cancel:key"])
            self.assertEqual(rc, 130)
            self.assertEqual(rows[-1]["taskId"], "task:demo")
            self.assertEqual(rows[-1]["taskCancellationStatus"], "unknown")
            self.assertNotIn("workerCancellationRequested", rows[-1])
            self.assertFalse(rows[-1]["deliveryComplete"])
            self.assertEqual(len([c for c in fake.calls if c["method"] == "POST"]), 1)

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


API_SOURCE = b'''import json
from http.server import BaseHTTPRequestHandler, HTTPServer
from socketserver import TCPServer
def create_server(host, port):
    class Server(HTTPServer):
        def server_bind(self):
            TCPServer.server_bind(self)
            self.server_name = 'localhost'
            self.server_port = self.server_address[1]
    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *args): pass
        def do_POST(self):
            try:
                value = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
            except ValueError:
                status, result = 400, {'error': 'invalid-json'}
            else:
                try:
                    if type(value) is not dict or set(value) != {'items'}: raise ValueError()
                    items = value['items']
                    if type(items) is not list or not items: raise ValueError()
                    subtotal = 0
                    for item in items:
                        if type(item) is not dict or set(item) != {'unit_price_cents', 'quantity'}: raise ValueError()
                        price, count = item['unit_price_cents'], item['quantity']
                        if type(price) is not int or price < 0 or type(count) is not int or count <= 0: raise ValueError()
                        subtotal += price * count
                    shipping = 0 if subtotal >= 5000 else 500
                    status, result = 200, {'subtotal_cents': subtotal, 'shipping_cents': shipping, 'total_cents': subtotal+shipping}
                except ValueError:
                    status, result = 422, {'error': 'invalid-order'}
            if self.path != '/quote': status, result = 404, {'error': 'not-found'}
            data = json.dumps(result).encode()
            self.send_response(status)
            self.send_header('Content-Type', 'application/json')
            self.send_header('Content-Length', str(len(data)))
            self.end_headers()
            self.wfile.write(data)
    return Server((host, port), Handler)
'''
CLIENT_SOURCE = b'''import json, http.client
from urllib.parse import urlsplit
def quote_order(url, items):
    endpoint = urlsplit(url)
    if endpoint.scheme != 'http' or endpoint.hostname != '127.0.0.1' or not endpoint.port or endpoint.username is not None or endpoint.password is not None or endpoint.path or endpoint.query or endpoint.fragment: raise ValueError()
    connection = http.client.HTTPConnection('127.0.0.1', endpoint.port, timeout=2)
    try:
        connection.request('POST', '/quote', json.dumps({'items': items}), {'Content-Type': 'application/json'})
        response = connection.getresponse()
        result = json.loads(response.read(65537))
        if response.status != 200 or type(result) is not dict or set(result) != {'subtotal_cents', 'shipping_cents', 'total_cents'} or any(type(v) is not int for v in result.values()): raise ValueError()
        return result
    finally:
        connection.close()
'''


def delivery_files(api=API_SOURCE, client=CLIENT_SOURCE):
    delivery = {"version": "order-quote-delivery/v1", "apiSha256": hashlib.sha256(api).hexdigest(),
                "clientSha256": hashlib.sha256(client).hexdigest(), "apiEntryPoint": "quote_api.create_server",
                "clientEntryPoint": "quote_client.quote_order", "sampleItems": [{"unit_price_cents": 1200, "quantity": 2}],
                "sampleQuote": {"subtotal_cents": 2400, "shipping_cents": 500, "total_cents": 2900}}
    return {"quote_api.py": api, "quote_client.py": client, "quote_delivery.json": json.dumps(delivery).encode()}


def zip_bundle(files, mutate=None):
    target = io.BytesIO()
    with zipfile.ZipFile(target, "w") as archive:
        for name, content in files.items():
            info = zipfile.ZipInfo(name, date_time=(1980, 1, 1, 0, 0, 0))
            info.create_system = 3
            info.external_attr = (stat.S_IFREG | 0o644) << 16
            info.compress_type = zipfile.ZIP_DEFLATED
            if mutate:
                mutate(info)
            archive.writestr(info, content)
    return target.getvalue()


def manifest_for(files, bundle):
    return {"goalId": "task:demo", "outcomeFactDigest": DIGEST, "planFactDigest": DIGEST,
            "integrationRunId": "run:integration", "integrationBaseSha": "a" * 40,
            "candidateDigests": [DIGEST] * 3, "patchDigests": [DIGEST] * 3,
            "decisionDigests": [DIGEST] * 3, "contentDigest": demo.digest_bytes(bundle),
            "contentBytes": len(bundle), "mediaType": "application/zip", "factDigest": DIGEST,
            "files": [{"path": name, "sha256": demo.digest_bytes(content), "bytes": len(content)} for name, content in files.items()]}


class DeliveryConsumerTests(unittest.TestCase):
    private = DemoTests.private
    run_cli = DemoTests.run_cli
    def setup_download(self, fake, files=None):
        files = files or delivery_files()
        bundle = zip_bundle(files)
        fake.task["status"] = "completed"
        fake.task["delivery"] = manifest_for(files, bundle)
        fake.raw["/v1/tasks/task:demo/artifact"] = bundle
        fake.media["/v1/tasks/task:demo/artifact"] = "application/zip"
        return files, bundle

    def test_download_only_keeps_business_verdict_pending(self):
        with Fake() as fake, tempfile.TemporaryDirectory() as tmp:
            files, _ = self.setup_download(fake)
            folder = Path(tmp) / "delivery"
            with mock.patch.object(demo, "run_delivery_oracle", side_effect=AssertionError("implicit execution")):
                rc, records = self.run_cli(fake, tmp, ["download", "--task-id", "task:demo", "--output-dir", str(folder)])
            self.assertEqual(rc, 0)
            self.assertEqual(records[0]["businessOracle"], "not-run")
            self.assertFalse(records[0]["deliveryComplete"])
            self.assertFalse(records[0]["coreStateMutated"])
            self.assertEqual(folder.stat().st_mode & 0o777, 0o700)
            self.assertEqual({p.name: p.read_bytes() for p in folder.iterdir()}, files)
            self.assertTrue(all(p.stat().st_mode & 0o777 == 0o644 for p in folder.iterdir()))
            self.assertTrue(all(call["method"] == "GET" and call["auth"] == "Bearer " + TOKEN for call in fake.calls))

    def test_fixed_oracle_consumes_actual_downloaded_http_components_in_new_directory(self):
        with Fake() as fake, tempfile.TemporaryDirectory() as tmp:
            self.setup_download(fake)
            rc, records = self.run_cli(fake, tmp, ["download", "--task-id", "task:demo", "--output-dir", str(Path(tmp) / "fresh"), "--run-oracle"])
            self.assertEqual(rc, 0, records)
            self.assertEqual(records[0]["event"], "delivery-consumed")
            self.assertTrue(records[0]["deliveryComplete"])
            self.assertFalse(records[0]["productionReleaseProven"])

    def test_downloaded_business_bug_fails_even_with_valid_archive_and_manifest(self):
        with Fake() as fake, tempfile.TemporaryDirectory() as tmp:
            self.setup_download(fake, delivery_files(API_SOURCE.replace(b"subtotal >= 5000", b"subtotal > 5000")))
            rc, records = self.run_cli(fake, tmp, ["download", "--task-id", "task:demo", "--output-dir", str(Path(tmp) / "fresh"), "--run-oracle"])
            self.assertEqual(rc, 2)
            self.assertFalse(records[0]["deliveryComplete"])
            self.assertEqual(records[0]["code"], "business-oracle-failed")

    def test_not_completed_cannot_download(self):
        with Fake() as fake, tempfile.TemporaryDirectory() as tmp:
            self.setup_download(fake)
            fake.task["status"] = "verified-awaiting-delivery"
            with self.assertRaisesRegex(demo.ClientError, "task-delivery-not-ready"):
                demo.Client(fake.record()).download("task:demo", Path(tmp) / "fresh")
            self.assertFalse(any(call["path"].endswith("/artifact") for call in fake.calls))

    def test_manifest_hostile_shapes_and_bindings_are_rejected(self):
        files = delivery_files()
        good = manifest_for(files, zip_bundle(files))
        demo.validate_manifest(good, "task:demo")
        mutations = [("goalId", "other"), ("integrationBaseSha", {}), ("contentBytes", True),
                     ("contentBytes", demo.BUNDLE_LIMIT + 1), ("mediaType", "text/plain"),
                     ("decisionDigests", []), ("decisionDigests", [DIGEST, DIGEST, {}]),
                     ("files", {}), ("factDigest", "bad"), ("command", ["arbitrary"])]
        for key, value in mutations:
            with self.subTest(key=key, value=value):
                bad = copy.deepcopy(good)
                bad[key] = value
                with self.assertRaises(demo.ClientError):
                    demo.validate_manifest(bad, "task:demo")
        for path in ["../quote_api.py", "/quote_api.py", "quote_api.py/", "QUOTE_API.PY", "a\\quote_api.py"]:
            bad = copy.deepcopy(good)
            bad["files"][0]["path"] = path
            with self.subTest(path=path), self.assertRaises(demo.ClientError):
                demo.validate_manifest(bad, "task:demo")

    def test_corrupted_content_and_file_digests_are_rejected_before_output(self):
        with Fake() as fake, tempfile.TemporaryDirectory() as tmp:
            self.setup_download(fake)
            fake.task["delivery"]["contentDigest"] = DIGEST
            with self.assertRaisesRegex(demo.ClientError, "delivery-content-mismatch"):
                demo.Client(fake.record()).download("task:demo", Path(tmp) / "fresh")
            self.assertFalse((Path(tmp) / "fresh").exists())
        files = delivery_files()
        raw = zip_bundle(files)
        manifest = manifest_for(files, raw)
        manifest["files"][0]["sha256"] = DIGEST
        with self.assertRaisesRegex(demo.ClientError, "delivery-file-mismatch"):
            demo.validate_bundle(manifest, raw)

    def test_zip_paths_duplicates_symlinks_devices_modes_and_extra_files_are_rejected(self):
        files = delivery_files()
        for names in [("../escape", "quote_client.py", "quote_delivery.json"),
                      ("quote_api.py", "quote_client.py", "quote_api.py"),
                      ("/quote_api.py", "quote_client.py", "quote_delivery.json"),
                      ("quote_api.py", "quote_client.py", "quote_delivery.json", "extra")]:
            target = io.BytesIO()
            import warnings
            with warnings.catch_warnings(), zipfile.ZipFile(target, "w") as archive:
                warnings.simplefilter("ignore", UserWarning)
                for name in names:
                    archive.writestr(name, b"bad")
            raw = target.getvalue()
            with self.subTest(names=names), self.assertRaises(demo.ClientError):
                demo.validate_bundle(manifest_for(files, raw), raw)
        for mode in [stat.S_IFLNK | 0o644, stat.S_IFDIR | 0o644, stat.S_IFCHR | 0o644, stat.S_IFREG | 0o755, stat.S_IFREG | 0o4644]:
            raw = zip_bundle(files, lambda info: setattr(info, "external_attr", mode << 16))
            with self.subTest(mode=mode), self.assertRaises(demo.ClientError):
                demo.validate_bundle(manifest_for(files, raw), raw)

    def test_go_zip_timestamp_extra_is_allowed(self):
        files = delivery_files()
        raw = zip_bundle(files, lambda info: setattr(info, "extra", b"\x55\x54\x05\x00\x01\x00\x00\x00\x00"))
        self.assertEqual(demo.validate_bundle(manifest_for(files, raw), raw), files)

    def test_existing_or_symlink_output_is_never_reused(self):
        with tempfile.TemporaryDirectory() as tmp:
            files = delivery_files()
            existing = Path(tmp) / "exists"
            existing.mkdir()
            sentinel = existing / "sentinel"
            sentinel.write_text("keep")
            link = Path(tmp) / "symlink"
            link.symlink_to(existing, target_is_directory=True)
            for folder in (existing, link):
                with self.assertRaises(demo.ClientError):
                    demo.materialize(folder, files)
            self.assertEqual(sentinel.read_text(), "keep")
            self.assertEqual(list(existing.iterdir()), [sentinel])

    def test_oracle_source_drift_and_post_execution_mutation_are_rejected(self):
        with tempfile.TemporaryDirectory() as tmp:
            files = delivery_files()
            folder = demo.materialize(Path(tmp) / "fresh", files)
            with mock.patch.object(demo, "ORACLE_SHA", "0" * 64), self.assertRaisesRegex(demo.ClientError, "fixed-oracle-drift"):
                demo.run_delivery_oracle(folder, files)
            def mutate(*args):
                (folder / "quote_api.py").write_text("changed")
            with mock.patch.object(demo, "bounded_oracle_process", side_effect=mutate), self.assertRaisesRegex(demo.ClientError, "delivery-files-changed"):
                demo.run_delivery_oracle(folder, files)

    def test_oracle_process_is_bounded_and_credentials_not_inherited(self):
        with tempfile.TemporaryDirectory() as tmp:
            code = "import os,json; assert 'MARSHAL_TEST_SECRET' not in os.environ; print(json.dumps({'checks':34,'scope':'integration'}))"
            with mock.patch.dict(os.environ, {"MARSHAL_TEST_SECRET": TOKEN}):
                demo.bounded_oracle_process([sys.executable, "-I", "-B", "-c", code], b"x", tmp)
            for code, reason in [("import time; time.sleep(5)", "business-oracle-timeout"),
                                 ("print('x'*10000)", "business-oracle-output-limit"),
                                 ("print('{\"checks\":34.0,\"scope\":\"integration\"}')", "business-oracle-failed"),
                                 ("import os; os._exit(0)", "invalid-json")]:
                with self.subTest(reason=reason), self.assertRaisesRegex(demo.ClientError, reason):
                    demo.bounded_oracle_process([sys.executable, "-I", "-B", "-c", code], b"x", tmp, timeout=0.15)

    def test_early_exit_descendant_retaining_pipes_is_killed_before_leader_reap(self):
        code = "import os,time; child=os.fork(); os._exit(0) if child else time.sleep(10)"
        self.check_group_cleanup(code, "business-oracle-timeout")

    def test_successful_exit_silent_background_descendant_is_also_killed(self):
        code = ("import os,time\nchild=os.fork()\n"
                "if child:\n os.write(1,b'{\"checks\":34,\"scope\":\"integration\"}'); os._exit(0)\n"
                "os.close(0); os.close(1); os.close(2); time.sleep(10)\n")
        self.check_group_cleanup(code, None)

    def check_group_cleanup(self, code, error):
        real_popen = demo.subprocess.Popen
        real_killpg = os.killpg
        processes, signals = [], []

        def signal_group(pid, value):
            signals.append((pid, value))
            return real_killpg(pid, value)

        def spawn(*args, **kwargs):
            process = real_popen(*args, **kwargs)
            processes.append(process)
            wait = process.wait

            def checked_wait(*a, **kw):
                self.assertIn((process.pid, demo.signal.SIGKILL), signals,
                              "leader identity released before group cleanup")
                return wait(*a, **kw)

            process.wait = checked_wait
            process.poll = mock.Mock(side_effect=AssertionError("premature PID reap"))
            return process

        with tempfile.TemporaryDirectory() as tmp, mock.patch.object(demo.subprocess, "Popen", spawn), mock.patch.object(demo.os, "killpg", signal_group):
            if error:
                with self.assertRaisesRegex(demo.ClientError, error):
                    demo.bounded_oracle_process([sys.executable, "-I", "-B", "-c", code], b"x", tmp, timeout=0.25)
            else:
                demo.bounded_oracle_process([sys.executable, "-I", "-B", "-c", code], b"x", tmp, timeout=1)
        self.assertEqual(len(processes), 1)
        process = processes[0]
        self.assertIsNotNone(process.returncode)
        deadline = time.monotonic() + 2
        while True:
            try:
                real_killpg(process.pid, 0)
            except ProcessLookupError:
                break
            if time.monotonic() >= deadline:
                self.fail("owned process group survived cleanup")
            time.sleep(0.01)


if __name__ == "__main__":
    unittest.main()
