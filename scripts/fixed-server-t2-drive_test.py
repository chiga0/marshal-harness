#!/usr/bin/env python3
"""Regression tests use injected calls; never execute a candidate or Worker."""

import copy
import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("t2drive", Path(__file__).with_name("fixed-server-t2-drive.py"))
driver = importlib.util.module_from_spec(spec)
spec.loader.exec_module(driver)


def run(state, sequence):
    return {"runId": "run-test", "attemptId": "attempt-test", "taskId": "task-test", "state": state,
            "sequence": sequence, "authorityHead": "sha256:" + str(sequence) * 64}


def result(state, sequence, **fields):
    value = run(state, sequence)
    return {"Projection": dict(run=value, **fields), "Receipt": {"runId": value["runId"], "attemptId": value["attemptId"],
                                                              "postRevision": sequence, "postAuthorityHead": value["authorityHead"]}}


class DriverTest(unittest.TestCase):
    def test_deadline_is_canonical_rfc3339_for_fractional_and_whole_seconds(self):
        for deadline, expected in ((30.12, "1970-01-01T00:00:30.12Z"), (30, "1970-01-01T00:00:30Z"), (30.123456, "1970-01-01T00:00:30.123456Z")):
            with self.subTest(deadline=deadline):
                requests = []

                def call(args, remaining):
                    requests.append(args)
                    if args[0] == "inspect":
                        return 0, run("RUNNING", 1)
                    return 1, {}

                with self.assertRaises(driver.DriveError):
                    driver.drive(call, lambda *_: None, "run-test", deadline, now=lambda: 0)
                self.assertEqual(requests[1][requests[1].index("--deadline") + 1], expected)

    def exercise(self, responses, deadline=30):
        saved, calls, tick = {}, [], [0]

        def call(args, remaining):
            calls.append((list(args), remaining))
            return responses.pop(0)

        def save(name, value):
            self.assertNotIn(name, saved)
            saved[name] = copy.deepcopy(value)

        def pause(seconds):
            tick[0] += seconds

        return lambda: driver.drive(call, save, "run-test", deadline, now=lambda: tick[0], pause=pause), saved, calls

    def happy(self, status="pass"):
        return [(0, run("RUNNING", 1)), (3, driver.LIVE_PENDING), (0, result("VERIFYING", 2)),
                (0, result("REVIEW_PENDING", 3, status=status)),
                (0, result("REVIEW_PENDING", 3, packetDigest="sha256:" + "a" * 64, packet={})),
                (0, run("REVIEW_PENDING", 3))]

    def test_waits_only_on_live_and_freezes_request(self):
        execute, saved, calls = self.exercise(self.happy())
        summary = execute()
        self.assertEqual(calls[1][0], calls[2][0])
        self.assertLess(calls[2][1], calls[1][1])
        self.assertEqual(summary["runningObservations"], 1)
        self.assertFalse(summary["accepted"])
        self.assertEqual(summary["stage"], "review-pending")
        self.assertIn("review-packet.json", saved)
        self.assertNotIn("decision", [args[0] for args, _ in calls])

    def test_unknown_failures_never_retry(self):
        for code, response in [(1, driver.LIVE_PENDING), (3, {"disposition": "pending", "reasonCode": "delivery-pending"}),
                               (3, dict(driver.LIVE_PENDING, receipt={})), (137, {})]:
            with self.subTest(code=code, response=response):
                execute, _, calls = self.exercise([(0, run("RUNNING", 1)), (code, response)])
                with self.assertRaisesRegex(driver.DriveError, "unresolved-no-automatic-retry"):
                    execute()
                self.assertEqual(len(calls), 2)

    def test_deadline_does_not_restart_attempt(self):
        execute, _, calls = self.exercise([(0, run("RUNNING", 1)), (3, driver.LIVE_PENDING)], deadline=1)
        with self.assertRaisesRegex(driver.DriveError, "deadline-exceeded"):
            execute()
        self.assertEqual(len(calls), 2)

    def test_stale_receipt_and_wrong_attempt_stop_before_verify(self):
        for field, value in [("attemptId", "other-attempt"), ("postRevision", 9), ("postAuthorityHead", "sha256:" + "c" * 64)]:
            responses = self.happy()
            responses[2][1]["Receipt"][field] = value
            execute, _, calls = self.exercise(responses)
            with self.assertRaisesRegex(driver.DriveError, "receipt-projection-mismatch"):
                execute()
            self.assertNotIn("verify", [args[0] for args, _ in calls])

    def test_failed_verification_preserves_packet_without_acceptance(self):
        execute, saved, _ = self.exercise(self.happy(status="fail"))
        with self.assertRaisesRegex(driver.DriveError, "business-verification-failed"):
            execute()
        self.assertIn("review-packet.json", saved)
        self.assertFalse(saved["review-summary.json"]["accepted"])

    def test_final_query_drift_rejected(self):
        responses = self.happy()
        responses[-1] = (0, run("REVIEW_PENDING", 4))
        execute, saved, _ = self.exercise(responses)
        with self.assertRaises(driver.DriveError):
            execute()
        self.assertNotIn("review-summary.json", saved)


if __name__ == "__main__":
    unittest.main()
