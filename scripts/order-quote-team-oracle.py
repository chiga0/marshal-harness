#!/usr/bin/env python3
"""订单报价团队的 HTTP 集成 oracle；不提供执行隔离或发布授权。"""

import copy
import http.client
import json
from urllib.parse import urlsplit


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

    The controlling verifier must bound the whole workload and independently
    observe client transport; passing these checks alone is not a Team verdict.
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
    return count + 4
