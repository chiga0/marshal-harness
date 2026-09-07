#!/usr/bin/env python3
"""合成 HTTP 回归：只验证参考 oracle，不运行 Agent 或发布二进制。"""

from contextlib import contextmanager
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import importlib.util
import json
from pathlib import Path
import socketserver
import subprocess
import sys
import tempfile
import threading
import types
import unittest
from unittest.mock import patch


def load(name, leaf):
    spec = importlib.util.spec_from_file_location(name, Path(__file__).with_name(leaf))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


oracle = load("team_oracle", "order-quote-team-oracle.py")
baseline = load("quote_fixture", "order-quote-oracle_test.py")


class LoopbackFixtureServer(ThreadingHTTPServer):
    def server_bind(self):
        # HTTPServer performs reverse DNS even for this numeric test address.
        # The fixture needs no DNS; keep its local identity explicit.
        socketserver.TCPServer.server_bind(self)
        self.server_name = "localhost"
        self.server_port = self.server_address[1]


def make_server(mutant=None):
    calls = []

    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *_):
            pass

        def do_POST(self):
            body = self.rfile.read(int(self.headers["Content-Length"]))
            calls.append((self.path, body))
            if self.path != "/quote":
                status, result = 404, {"error": "not-found"}
            else:
                try:
                    payload = json.loads(body)
                except ValueError:
                    status, result = 400, {"error": "invalid-json"}
                else:
                    try:
                        if type(payload) is not dict or set(payload) != {"items"}:
                            raise ValueError()
                        result = baseline.correct(payload["items"])
                        status = 200
                    except ValueError:
                        status, result = 422, {"error": "invalid-order"}
            if mutant:
                status, result = mutant(status, result)
            data = json.dumps(result).encode()
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)

    server = LoopbackFixtureServer(("127.0.0.1", 0), Handler)
    return server, calls


@contextmanager
def fixture(mutant=None):
    server, calls = make_server(mutant)
    worker = threading.Thread(target=server.serve_forever, kwargs={"poll_interval": 0.01}, daemon=True)
    worker.start()
    try:
        yield f"http://127.0.0.1:{server.server_port}", calls
    finally:
        server.shutdown()
        server.server_close()
        worker.join(2)
        if worker.is_alive():
            raise RuntimeError("fixture-did-not-stop")


def client(url, items):
    status, value = oracle.request(url, {"items": items})
    if status != 200:
        raise ValueError("invalid-order")
    return value


class TeamOracleTests(unittest.TestCase):
    def test_component_and_integration_entrypoints_use_real_http(self):
        def candidate(path, _):
            if path == "api.py":
                return types.SimpleNamespace(create_server=lambda host, port: make_server()[0])
            return types.SimpleNamespace(quote_order=client)
        with patch.object(oracle, "load_candidate", side_effect=candidate):
            self.assertEqual(oracle.run_component("api.py"), 25)
            self.assertEqual(oracle.run_component(client_path="client.py"), 2)
            self.assertEqual(oracle.run_component("api.py", "client.py"), 25)

    def test_cli_rejects_missing_symlink_early_exit_and_candidate_exception(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            early = root/"early.py"
            early.write_text("raise SystemExit(0)\n")
            exception = root/"exception.py"
            exception.write_text("raise ValueError('must-not-echo-this')\n")
            link = root/"link.py"
            link.symlink_to(early)
            for path in (early, exception, link, root/"missing.py"):
                result = subprocess.run([sys.executable, "-I", "-B", str(Path(__file__).with_name("order-quote-team-oracle.py")), "--client", str(path)], capture_output=True, timeout=5)
                self.assertEqual(result.returncode, 1)
                self.assertNotIn(b"checks", result.stdout)
                self.assertNotIn(b"must-not-echo-this", result.stderr)

    def test_real_http_roundtrips(self):
        with fixture() as (url, calls):
            self.assertEqual(oracle.check(url, client), 25)
            self.assertEqual(len(calls), 23)

    def test_rejects_client_that_computes_locally(self):
        with fixture() as (url, _):
            with self.assertRaisesRegex(ValueError, "client-response-not-consumed"):
                oracle.check(url, lambda _url, items: baseline.correct(items))

    def test_rejects_client_that_ignores_service_response(self):
        def ignores(url, items):
            oracle.request(url, {"items": items})
            return baseline.correct(items)
        with self.assertRaisesRegex(ValueError, "client-response-not-consumed"):
            oracle.check_transport(ignores)

    def test_rejects_hardcoded_response_without_request(self):
        with self.assertRaisesRegex(ValueError, "client-request-not-observed"):
            oracle.check_transport(lambda *_: {"subtotal_cents": 137, "shipping_cents": 0, "total_cents": 137})

    def test_rejects_wrong_request_body(self):
        def wrong_body(url, items):
            status, response = oracle.request(url, {"items": []})
            if status != 200:
                raise ValueError("invalid-order")
            return response
        with self.assertRaisesRegex(ValueError, "client-request-not-observed"):
            oracle.check_transport(wrong_body)

    def test_rejects_wrong_service_quote(self):
        with fixture(lambda status, value: (status, dict(value, total_cents=-1)) if status == 200 else (status, value)) as (url, _):
            with self.assertRaisesRegex(ValueError, "api-business-result"):
                oracle.check(url, client)

    def test_rejects_wrong_error_status(self):
        with fixture(lambda status, value: (200, value) if status == 422 else (status, value)) as (url, _):
            with self.assertRaisesRegex(ValueError, "api-invalid-order"):
                oracle.check(url, client)

    def test_rejects_client_mutation(self):
        def mutating(url, items):
            value = client(url, items)
            items.clear()
            return value
        with fixture() as (url, _):
            with self.assertRaisesRegex(ValueError, "client-business-result"):
                oracle.check(url, mutating)

    def test_rejects_external_or_ambiguous_endpoint_before_network(self):
        for url in ["https://127.0.0.1:80", "http://localhost:80", "http://example.com:80", "http://user@127.0.0.1:80", "http://127.0.0.1:80/path", "http://127.0.0.1:80?key=value"]:
            with self.subTest(url=url), self.assertRaises(ValueError):
                oracle.request(url, {})


if __name__ == "__main__":
    unittest.main()
