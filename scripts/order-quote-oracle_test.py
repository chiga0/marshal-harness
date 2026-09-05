#!/usr/bin/env python3
"""参考 oracle 与 T2 业务任务生成回归，不构成真实 Agent 验收证据。"""

import argparse
import copy
import hashlib
import importlib.util
import inspect
import json
import pathlib
import subprocess
import sys
import tempfile
import unittest


ROOT = pathlib.Path(__file__).resolve().parent.parent


def load(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


oracle = load("oracle", ROOT / "scripts/order-quote-oracle.py")
renderer = load("renderer", ROOT / "scripts/fixed-server-t2-task.py")


def correct(items):
    if type(items) is not list or not items:
        raise ValueError()
    subtotal = 0
    for item in items:
        if type(item) is not dict or set(item) != {"unit_price_cents", "quantity"}:
            raise ValueError()
        price, qty = item["unit_price_cents"], item["quantity"]
        if type(price) is not int or price < 0 or type(qty) is not int or qty <= 0:
            raise ValueError()
        subtotal += price * qty
    shipping = 0 if subtotal >= 5000 else 500
    return dict(subtotal_cents=subtotal, shipping_cents=shipping, total_cents=subtotal + shipping)


class OracleTests(unittest.TestCase):
    def test_positive_oracle(self):
        self.assertEqual(oracle.check(correct), 28)

    def test_rejects_semantic_mutants(self):
        def threshold(items):
            result = correct(items)
            if result["subtotal_cents"] == 5000:
                result.update(shipping_cents=500, total_cents=5500)
            return result

        def floats(items):
            return {k: float(v) for k, v in correct(items).items()}

        def mutate(items):
            result = correct(items)
            items.clear()
            return result

        def accept_invalid(items):
            try:
                return correct(items)
            except ValueError:
                return {}

        def accepts_bool(items):
            changed = copy.deepcopy(items)
            if type(changed) is list:
                for item in changed:
                    if type(item) is dict:
                        for k, v in item.items():
                            if type(v) is bool:
                                item[k] = int(v)
            return correct(changed)

        for mutant in (threshold, floats, mutate, accept_invalid, accepts_bool):
            with self.subTest(mutant=mutant.__name__), self.assertRaises(ValueError):
                oracle.check(mutant)

    def test_cli_missing_symlink_and_early_exit_fail(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            early = root / "early.py"
            early.write_text("raise SystemExit(0)\n", encoding="utf-8")
            link = root / "link.py"
            link.symlink_to(early)
            for candidate in (early, link, root / "missing.py"):
                result = subprocess.run(
                    [sys.executable, "-I", "-B", str(ROOT / "scripts/order-quote-oracle.py"), str(candidate)],
                    capture_output=True, timeout=10,
                )
                self.assertEqual(result.returncode, 1)
                self.assertNotIn(b"PASS", result.stdout)

    def test_cli_runs_correct_candidate(self):
        with tempfile.TemporaryDirectory() as directory:
            candidate = pathlib.Path(directory) / "quote_order.py"
            candidate.write_text(inspect.getsource(correct).replace("def correct(", "def quote_order("), encoding="utf-8")
            result = subprocess.run(
                [sys.executable, "-I", "-B", str(ROOT / "scripts/order-quote-oracle.py"), str(candidate)],
                capture_output=True, timeout=10,
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(result.stdout, b"order-quote: PASS checks=28\n")

    def test_task_renderer_preserves_policy_and_marker(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            doctor = root / "doctor.json"
            doctor.write_text(json.dumps({"policyEnvironmentBinding": {"test": "fixture-only"}}))
            args = argparse.Namespace(
                repository=str(ROOT), base_ref="a" * 40, task_id="business-task", run_id="business-run",
                model="test/model", doctor=str(doctor), task_out=str(root / "task.json"),
                policy_out=str(root / "policy.json"), scenario="marker",
            )
            renderer.render(args)
            marker = json.loads(pathlib.Path(args.task_out).read_text())
            args.scenario = "order-quote"
            renderer.render(args)
            business = json.loads(pathlib.Path(args.task_out).read_text())
            policy = json.loads(pathlib.Path(args.policy_out).read_text())
            for field in ("repository", "worker", "budgets", "publication"):
                self.assertEqual(business[field], marker[field])
            self.assertEqual(marker["scope"]["allowPaths"], ["fixed-server-t2-canary.txt"])
            self.assertEqual(business["scope"]["allowPaths"], ["quote_order.py"])
            command = business["acceptance"]["commands"][0]["argv"]
            self.assertEqual(command[:4], ["/usr/bin/python3", "-I", "-B", "-c"])
            self.assertEqual(command[-3:], [str(ROOT / "scripts/order-quote-oracle.py"),
                hashlib.sha256((ROOT / "scripts/order-quote-oracle.py").read_bytes()).hexdigest(),
                "quote_order.py"])
            (root / "quote_order.py").write_text(
                inspect.getsource(correct).replace("def correct(", "def quote_order("), encoding="utf-8"
            )
            result = subprocess.run(command, cwd=root, capture_output=True, timeout=10)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(result.stdout, b"order-quote: PASS checks=28\n")
            drift = list(command)
            drift[-2] = "0" * 64
            result = subprocess.run(drift, cwd=root, capture_output=True, timeout=10)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn(b"oracle-drift", result.stderr)
            self.assertFalse(policy["effective"]["allowPublication"])
            self.assertFalse(policy["effective"]["allowWorkerSubagents"])
            self.assertEqual(business["budgets"]["maxAttempts"], 1)


if __name__ == "__main__":
    unittest.main()
