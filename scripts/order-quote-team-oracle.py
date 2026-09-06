#!/usr/bin/env python3
"""订单报价团队的 HTTP 集成 oracle；不提供执行隔离或发布授权。"""

import copy
import http.client
import json
from http.server import BaseHTTPRequestHandler, HTTPServer
import socketserver
import threading
from urllib.parse import urlsplit


def check_transport(client_quote):
    """Verifier-owned response challenges, not evidence of a correct service.

    The outer verifier must bound candidate execution. Same-UID Python is not
    an isolation boundary against a hostile client.
    """
    class LoopbackServer(HTTPServer):
        def server_bind(self):
            socketserver.TCPServer.server_bind(self)
            self.server_name = "localhost"
            self.server_port = self.server_address[1]

        def get_request(self):
            connection, address = super().get_request()
            connection.settimeout(1)
            return connection, address

    items = [{"unit_price_cents": 1200, "quantity": 2}]
    # Intentionally not the pricing oracle: verify that the client consumes
    # the HTTP response instead of silently implementing its own pricing.
    for status, response in [(200, {"subtotal_cents": 137, "shipping_cents": 0, "total_cents": 137}),
                             (422, {"error": "invalid-order"})]:
        calls = []

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *_):
                pass

            def do_POST(self):
                length = self.headers.get("Content-Length", "")
                if not length.isdecimal() or not 0 < int(length) <= 65536 or self.headers.get("Transfer-Encoding"):
                    self.send_error(400)
                    return
                payload = self.rfile.read(int(length))
                calls.append((self.path, self.headers.get("Content-Type", "").split(";", 1)[0], payload))
                data = json.dumps(response).encode()
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

        server = LoopbackServer(("127.0.0.1", 0), Handler)
        worker = threading.Thread(target=server.serve_forever, kwargs={"poll_interval": 0.01}, daemon=True)
        worker.start()
        before = copy.deepcopy(items)
        try:
            try:
                result = client_quote(f"http://127.0.0.1:{server.server_port}", items)
            except ValueError:
                if status == 200:
                    raise ValueError("client-response-not-consumed") from None
            else:
                if status != 200 or result != response or type(result) is not dict or any(type(v) is not int for v in result.values()):
                    raise ValueError("client-response-not-consumed")
            if len(calls) != 1 or calls[0][:2] != ("/quote", "application/json") or json.loads(calls[0][2]) != {"items": before}:
                raise ValueError("client-request-not-observed")
            if items != before:
                raise ValueError("client-input-mutation")
        finally:
            server.shutdown()
            server.server_close()
            worker.join(2)
            if worker.is_alive():
                raise RuntimeError("transport-observer-did-not-stop")
    return 2


def request(base_url, payload, *, raw=False, path="/quote"):
    endpoint = urlsplit(base_url)
    if endpoint.scheme != "http" or endpoint.hostname != "127.0.0.1" or not endpoint.port or endpoint.username or endpoint.password or endpoint.path or endpoint.query or endpoint.fragment:
        raise ValueError("loopback-endpoint-required")
    body = payload if raw else json.dumps(payload, separators=(",", ":")).encode()
    connection = http.client.HTTPConnection("127.0.0.1", endpoint.port, timeout=2)
    try:
        connection.request("POST", path, body, {"Content-Type": "application/json"})
        response = connection.getresponse()
        data = response.read(65537)
        if len(data) > 65536 or response.getheader("Content-Type", "").split(";", 1)[0] != "application/json":
            raise ValueError("response-envelope")
        return response.status, json.loads(data)
    finally:
        connection.close()


def check(base_url, client_quote):
    """Check server independently, then the client against that same server.

    The controlling verifier must bound the whole workload. Transport checks
    use separate response challenges; passing alone is not a Team verdict.
    """
    count = 0
    for items, subtotal, shipping in [
        ([{"unit_price_cents": 1200, "quantity": 2}], 2400, 500),
        ([{"unit_price_cents": 4999, "quantity": 1}], 4999, 500),
        ([{"unit_price_cents": 2500, "quantity": 2}], 5000, 0),
        ([{"unit_price_cents": 0, "quantity": 1}], 0, 500),
        ([{"unit_price_cents": 2300, "quantity": 2}, {"unit_price_cents": 400, "quantity": 1}], 5000, 0),
        ([{"unit_price_cents": 10**18 + 3, "quantity": 7}], (10**18 + 3) * 7, 0),
    ]:
        expected = {"subtotal_cents": subtotal, "shipping_cents": shipping, "total_cents": subtotal + shipping}
        before = copy.deepcopy(items)
        status, observed = request(base_url, {"items": items})
        if status != 200 or observed != expected or any(type(v) is not int for v in observed.values()):
            raise ValueError("api-business-result")
        result = client_quote(base_url, items)
        if type(result) is not dict or result != expected or any(type(v) is not int for v in result.values()) or items != before:
            raise ValueError("client-business-result")
        count += 2
    for payload in [None, {}, {"items": []}, {"items": None}, {"items": [], "extra": 1},
                    {"items": [{"unit_price_cents": True, "quantity": 1}]},
                    {"items": [{"unit_price_cents": 100, "quantity": 0}]}]:
        if request(base_url, payload) != (422, {"error": "invalid-order"}):
            raise ValueError("api-invalid-order")
        count += 1
    if request(base_url, b"{", raw=True) != (400, {"error": "invalid-json"}):
        raise ValueError("api-invalid-json")
    if request(base_url, {"items": []}, path="/missing") != (404, {"error": "not-found"}):
        raise ValueError("api-route")
    for items in [[], [{"unit_price_cents": 100, "quantity": 0}]]:
        before = copy.deepcopy(items)
        try:
            client_quote(base_url, items)
        except ValueError:
            pass
        else:
            raise ValueError("client-invalid-order")
        if items != before:
            raise ValueError("client-input-mutation")
    return count + 4 + check_transport(client_quote)
