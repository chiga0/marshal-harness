#!/usr/bin/env python3
"""团队实机客户端边界测试；不冒充真实 Worker 证据。"""

import importlib.util
import json
from pathlib import Path
import tempfile
import types
import unittest
from unittest import mock


spec = importlib.util.spec_from_file_location("team_drive", Path(__file__).with_name("fixed-server-team-drive.py"))
driver = importlib.util.module_from_spec(spec)
spec.loader.exec_module(driver)
RUNS = {node: "run-" + node for node in ("service", "client", "integration")}


def running(run):
    return {"runId": run, "state": "RUNNING", "attemptId": "attempt-1", "sequence": 3,
            "authorityHead": "sha256:" + "a" * 64}


class TeamDriveTest(unittest.TestCase):
    def test_main_one_approval_two_existing_drives_no_start(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            (root / "scripts").mkdir()
            (root / "bin").mkdir()
            (root / "bin/marshal").write_bytes(b"not-executed-test-fixture")
            evidence = root / ".marshal/fixed-server-t1-canary/canary-1"
            evidence.mkdir(parents=True)
            (evidence / "team-request.json").write_text(json.dumps({"inputs": {"baseSha": "a"*40}, "inputsDigest": "sha256:" + "b"*64}))
            for node in ("service", "client"):
                state = root / ".marshal/runs" / RUNS[node] / "state.json"
                state.parent.mkdir(parents=True)
                state.write_text("{}")
            calls, driven = [], []
            def execute(command, **kwargs):
                calls.append(command[2])
                self.assertEqual(command[:2], [str(root / "bin/marshal"), "control-plane"])
                value = {} if command[2] == "team-approve" else running(command[-1])
                return types.SimpleNamespace(returncode=0, stdout=json.dumps(value).encode(), stderr=b"")
            def drive(call, save, run_id, deadline):
                driven.append(run_id)
                save("review-packet.json", {"Projection": {"packet": {"runId": run_id}}})
            with mock.patch.object(driver, "__file__", str(root / "scripts/fixed-server-team-drive.py")), \
                    mock.patch("sys.argv", ["driver", "--evidence-root", str(evidence)]), \
                    mock.patch.object(driver, "subjects", return_value=RUNS), \
                    mock.patch.object(driver.subprocess, "run", side_effect=execute), \
                    mock.patch.object(driver.t2, "drive", side_effect=drive), \
                    mock.patch.object(driver.t2, "capture_review_inputs") as capture:
                self.assertEqual(driver.main(), 0)
            self.assertEqual(calls, ["team-approve", "inspect", "inspect"])
            self.assertEqual(driven, [RUNS["service"], RUNS["client"]])
            self.assertEqual(capture.call_count, 2)
            summary = json.loads((evidence / "team/summary.json").read_bytes())
            self.assertFalse(summary["accepted"])
            self.assertFalse(summary["integrationExecuted"])

    def test_approval_exact_binding(self):
        bundle = {"spec": {"goalId": "goal-1", "authorityNamespaceId": {}},
                  "proposal": {"proposalId": "plan-1"},
                  "nodes": [{"nodeId": n, "role": "integrate" if n == "integration" else "implement"} for n in RUNS]}
        request = {"inputs": bundle, "inputsDigest": driver.inputs.digest(bundle)}
        proof = {"goalId": "goal-1", "inputsDigest": request["inputsDigest"], "requestDigest": driver.inputs.digest(request),
                 "planRevision": 1, "obligationCount": 3, "factDigest": "sha256:" + "a" * 64}
        approval = {"found": True, "approval": proof}
        result = driver.subjects(request, approval)
        self.assertEqual(len(set(result.values())), 3)
        for field in ("goalId", "inputsDigest", "requestDigest", "planRevision", "obligationCount", "factDigest"):
            with self.subTest(field=field), self.assertRaises(driver.Error):
                driver.subjects(request, {"found": True, "approval": {**proof, field: "wrong"}})
        with self.assertRaises(driver.Error):
            driver.subjects(request, {"found": False, "approval": proof})

    def test_wait_does_not_start_or_infer_overlap(self):
        clock, calls, saved = [0], [], {}
        def call(args, remaining):
            calls.append(args)
            run = args[-1]
            return 0, running(run) if clock[0] > 0 else {"runId": run, "state": "READY"}
        driver.await_initial(call, saved.__setitem__, lambda run: run != RUNS["integration"], RUNS, 3,
                             now=lambda: clock[0], pause=lambda seconds: clock.__setitem__(0, clock[0] + seconds))
        self.assertEqual({args[0] for args in calls}, {"inspect"})
        self.assertFalse(saved["resident-running.json"]["processOverlapProven"])
        self.assertEqual(saved["resident-running.json"]["externalStartCalls"], 0)

    def test_missing_runs_are_bounded_and_never_started(self):
        clock, calls = [0], []
        with self.assertRaisesRegex(driver.Error, "dispatch-deadline"):
            driver.await_initial(lambda *args: calls.append(args), lambda *args: None, lambda run: False, RUNS, 2,
                                 now=lambda: clock[0], pause=lambda seconds: clock.__setitem__(0, clock[0] + seconds))
        self.assertEqual(calls, [])
        self.assertEqual(clock[0], 2)

    def test_failure_terminal_and_early_integration_do_not_retry(self):
        for code, value in ((1, {}), (0, {"runId": RUNS["service"], "state": "BLOCKED"}), (0, running("wrong-run"))):
            calls = []
            def call(*args):
                calls.append(args)
                return code, value
            with self.subTest(code=code, value=value), self.assertRaises(driver.Error):
                driver.await_initial(call, lambda *args: None, lambda run: run != RUNS["integration"], RUNS, 1, now=lambda: 0)
            self.assertEqual(len(calls), 1)
        with self.assertRaisesRegex(driver.Error, "integration-materialized"):
            driver.await_initial(lambda *args: self.fail("must not call"), lambda *args: None,
                                 lambda run: True, RUNS, 1, now=lambda: 0)


if __name__ == "__main__":
    unittest.main()
