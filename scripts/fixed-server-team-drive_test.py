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
    def exercise_main(self, live=False, rejected=False, integration_state="ACCEPTED"):
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
            def drive(call, save, run_id, deadline, require_pass=True):
                self.assertEqual(require_pass, not live)
                driven.append(run_id)
                save("review-packet.json", {"Projection": {"packet": {"runId": run_id}}})
                summary = {"run": {**running(run_id), "state": "REVIEW_PENDING"}, "packetDigest": "sha256:" + "c"*64}
                save("review-summary.json", summary)
                return summary
            def capture(root, run, packet, archive):
                archive.write_bytes(b"closed-test-archive")
            def review(call, save, reviews, output, unchanged, seconds, nodes=("service", "client")):
                self.assertTrue((output / "review.ready").is_file())
                self.assertTrue(unchanged())
                self.assertGreater(seconds, 0)
                self.assertLessEqual(seconds, 1200)
                for node in nodes:
                    self.assertEqual((output / node / "review-inputs.tar").read_bytes(), b"closed-test-archive")
                    self.assertEqual(json.loads((output / node / "review-ready.json").read_bytes())["run"]["runId"], RUNS[node])
                if nodes == ("integration",):
                    self.assertTrue((output / "integration.review.ready").is_file())
                    self.assertTrue((output / "implement-summary.json").is_file())
                    return {"integration": {"state": integration_state}}
                return {node: {"state": "REJECTED" if rejected and node == "client" else "ACCEPTED"} for node in ("service", "client")}
            with mock.patch.object(driver, "__file__", str(root / "scripts/fixed-server-team-drive.py")), \
                    mock.patch("sys.argv", ["driver", "--evidence-root", str(evidence), "--await-review-seconds", "1200" if live else "0"]), \
                    mock.patch.object(driver, "subjects", return_value=RUNS), \
                    mock.patch.object(driver.subprocess, "run", side_effect=execute), \
                    mock.patch.object(driver.t2, "drive", side_effect=drive), \
                    mock.patch.object(driver.t2, "capture_review_inputs", side_effect=capture) as captured, \
                    mock.patch.object(driver, "await_integration") as awaited, \
                    mock.patch.object(driver, "review_team", side_effect=review) as reviewed:
                self.assertEqual(driver.main(), 1 if rejected or integration_state == "REJECTED" else 0)
            self.assertEqual(calls, ["team-approve", "inspect", "inspect"])
            integrated = live and not rejected
            self.assertEqual(driven, [RUNS["service"], RUNS["client"]] + ([RUNS["integration"]] if integrated else []))
            self.assertEqual(captured.call_count, 2 + int(integrated))
            self.assertEqual(reviewed.call_count, int(live) + int(integrated))
            self.assertEqual(awaited.call_count, int(integrated))
            summary = json.loads((evidence / "team/summary.json").read_bytes())
            self.assertFalse(summary["accepted"])
            self.assertEqual(summary["integrationExecuted"], integrated)
            self.assertEqual(summary["stage"], "integration-reviewed" if integrated else "two-implement-reviewed" if live else "two-implement-review-pending")
            if integrated:
                self.assertFalse(summary["goalOutcomeAvailable"])
                self.assertEqual(summary["reviewedRuns"]["integration"]["state"], integration_state)
            if rejected:
                self.assertEqual(summary["reviewedRuns"]["service"]["state"], "ACCEPTED")
                self.assertEqual(json.loads((evidence / "team/failure.json").read_bytes())["automaticRetry"], False)

    def test_main_one_approval_two_existing_drives_no_start(self):
        self.exercise_main()

    def test_main_closes_both_archives_before_live_review_without_claiming_team_acceptance(self):
        self.exercise_main(live=True)

    def test_main_reject_preserves_other_acceptance_without_retry(self):
        self.exercise_main(live=True, rejected=True)

    def test_integration_no_change_is_not_misreported_as_team_accepted(self):
        self.exercise_main(live=True, integration_state="NO_CHANGE")

    def test_integration_reject_keeps_all_review_results_without_retry(self):
        self.exercise_main(live=True, integration_state="REJECTED")

    def test_integration_observation_never_starts_and_failure_is_not_retried(self):
        clock, calls, saved = [0], [], {}
        def call(args, remaining):
            calls.append(args)
            return 0, {"runId": RUNS["integration"], "state": "READY"} if clock[0] == 0 else running(RUNS["integration"])
        result = driver.await_integration(call, saved.__setitem__, lambda _: True, RUNS["integration"], 3,
                                          now=lambda: clock[0], pause=lambda seconds: clock.__setitem__(0, clock[0] + seconds))
        self.assertEqual(result["state"], "RUNNING")
        self.assertEqual(calls, [["inspect", "--run", RUNS["integration"]]] * 2)
        for response in ((1, {}), (0, running("wrong")), (0, {"runId": RUNS["integration"], "state": "BLOCKED"})):
            with self.subTest(response=response), self.assertRaises(driver.Error):
                driver.await_integration(lambda *_: response, lambda *_: None, lambda _: True,
                                         RUNS["integration"], 1, now=lambda: 0)
        with self.assertRaisesRegex(driver.Error, "dispatch-deadline"):
            driver.await_integration(lambda *_: self.fail("missing Run"), lambda *_: None, lambda _: False,
                                     RUNS["integration"], 0, now=lambda: 0)

    def test_reviews_arrive_out_of_order_and_reject_does_not_discard_other_node(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory)
            for node in ("service", "client"):
                (output / node).mkdir()
            client = output / "client/review-decision.json"
            service = output / "service/review-decision.json"
            client.write_text('{"verdict":"reject"}')
            clock, calls = [0], []
            def pause(seconds):
                clock[0] += seconds
                if not service.exists():
                    service.write_text('{"verdict":"accept"}')
            def finalize(call, save, summary, packet, decision, path, deadline, require_accepted):
                self.assertFalse(require_accepted)
                self.assertLessEqual(deadline, 10)
                calls.append(path.parent.name)
                return {"state": "ACCEPTED" if decision["verdict"] == "accept" else "REJECTED"}
            with mock.patch.object(driver.t2, "finalize_review", side_effect=finalize):
                result = driver.review_team(None, lambda *_: None, {node: ({}, {}) for node in RUNS}, output,
                                            lambda: True, 10, now=lambda: clock[0], wall=lambda: clock[0], pause=pause)
            self.assertEqual(calls, ["client", "service"])
            self.assertEqual(result["service"]["state"], "ACCEPTED")
            self.assertEqual(result["client"]["state"], "REJECTED")

    def test_shared_review_deadline_and_binary_drift_do_not_retry(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory)
            (output / "client").mkdir()
            clock = [0]
            with mock.patch.object(driver.t2, "finalize_review") as finalize:
                with self.assertRaisesRegex(driver.Error, "wait-expired"):
                    driver.review_team(None, None, {}, output, lambda: True, 2, now=lambda: clock[0],
                                       pause=lambda seconds: clock.__setitem__(0, clock[0] + seconds))
                (output / "client/review-decision.json").write_text('{}')
                with self.assertRaisesRegex(driver.Error, "binary-drift"):
                    driver.review_team(None, None, {}, output, lambda: False, 2)
                finalize.assert_not_called()

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
