#!/usr/bin/env python3
"""Regression tests use injected calls; never execute a candidate or Worker."""

import copy
import importlib.util
import hashlib
import json
import os
from pathlib import Path
import tarfile
import tempfile
import threading
import time
import unittest
from types import SimpleNamespace

spec = importlib.util.spec_from_file_location("t2drive", Path(__file__).with_name("fixed-server-t2-drive.py"))
driver = importlib.util.module_from_spec(spec)
spec.loader.exec_module(driver)


class TransportDiagnosticTest(unittest.TestCase):
    def test_only_exact_allowlisted_labels_are_archived(self):
        raw = (b"private/path credential\n"
               b"control-plane request failed: stage=client-recheck reasonCode=transport-failure\n"
               b"control-plane request failed: stage=client-recheck reasonCode=transport-failure\n"
               b"control-plane request failed: stage=secret reasonCode=transport-failure\n"
               b"control-plane request failed: stage=client-dial reasonCode=transport-failure extra-secret\n"
               b"\xff\n")
        self.assertEqual(driver.safe_transport_stages(raw), ["client-recheck"])

    def test_unknown_and_generic_failures_do_not_become_retry_admission(self):
        self.assertEqual(driver.safe_transport_stages(b"reasonCode=transport-failure\n"), [])


class TimeoutTaskTest(unittest.TestCase):
    def test_budget_is_frozen_in_task_without_changing_normal_task(self):
        task_spec = importlib.util.spec_from_file_location("t2task", Path(__file__).with_name("fixed-server-t2-task.py"))
        renderer = importlib.util.module_from_spec(task_spec)
        task_spec.loader.exec_module(renderer)
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary).resolve()
            (root / ".git").mkdir()
            (root / "scripts").mkdir()
            (root / "scripts/order-quote-oracle.py").write_text("# synthetic oracle bytes\n")
            (root / "doctor.json").write_text(json.dumps({"policyEnvironmentBinding": {"digest": "sha256:" + "a" * 64}}))
            args = SimpleNamespace(repository=str(root), base_ref="a" * 40, task_id="task-test", run_id="run-test",
                                   model="provider/model", doctor=str(root / "doctor.json"),
                                   task_out=str(root / "task.json"), policy_out=str(root / "policy.json"))
            for scenario, seconds, run_seconds in (("order-quote", 300, 600), ("order-quote-timeout", 60, 600), ("order-quote-run-timeout", 60, 60)):
                args.scenario = scenario
                renderer.render(args)
                task = json.loads((root / "task.json").read_bytes())
                self.assertEqual(task["budgets"]["attemptTimeoutSeconds"], seconds)
                self.assertEqual(task["budgets"]["runTimeoutSeconds"], run_seconds)
                self.assertLessEqual(task["budgets"]["attemptTimeoutSeconds"], task["budgets"]["runTimeoutSeconds"])
                self.assertEqual(task["budgets"]["maxAttempts"], 1)
                self.assertEqual(task["budgets"]["maxOperationalRetries"], 0)
                self.assertEqual(task["scope"]["allowPaths"], ["quote_order.py"])
                self.assertEqual(task["publication"]["provider"], "none")
            args.scenario, args.long_verify = "order-quote", True
            renderer.render(args)
            peer = json.loads((root / "task.json").read_bytes())
            self.assertEqual([c["id"] for c in peer["acceptance"]["commands"]],
                             ["order-quote-business", "cross-run-long-verification"])
            command = peer["acceptance"]["commands"][-1]
            self.assertEqual(command["timeoutSeconds"], 120)
            self.assertEqual(command["argv"][-2:], [str(root / ".marshal/fixed-server-t1-canary/run-test/verification-started.json"), "run-test"])
            compile(command["argv"][4], "frozen-verifier", "exec")
            self.assertEqual(peer["budgets"], {**task["budgets"], "attemptTimeoutSeconds": 300, "runTimeoutSeconds": 600})
            args.scenario = "order-quote-timeout"
            with self.assertRaises(SystemExit):
                renderer.render(args)


class CrossRunTest(unittest.TestCase):
    def test_rendezvous_is_bounded_and_subject_bound(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "signal.json"
            done = threading.Event()
            path.write_text(json.dumps({"runId": "run-peer", "startedAt": 10}))
            self.assertEqual(driver.await_verifier(path, "run-peer", 20, done, now=lambda: 11)["startedAt"], 10)
            with self.assertRaises(driver.DriveError):
                driver.await_verifier(path, "run-other", 20, done, now=lambda: 11)
            done.set()
            with self.assertRaises(driver.DriveError):
                driver.await_verifier(path, "run-peer", 20, done, now=lambda: 11)
            done.clear()
            path.unlink()
            path.symlink_to(Path(tmp) / "missing")
            with self.assertRaises(OSError):
                driver.await_verifier(path, "run-peer", 20, done, now=lambda: 11)
            path.unlink()
            with self.assertRaises(driver.DriveError):
                driver.await_verifier(path, "run-peer", 11, done, now=lambda: 11)

    def fixture(self):
        digest = lambda v: "sha256:" + hashlib.sha256(json.dumps(v, sort_keys=True, separators=(",", ":")).encode()).hexdigest()
        task = {"acceptance": {"commands": [{"id": "cross-run-long-verification", "argv": ["/usr/bin/python3", "test"]}]}}
        report = {"status": "pass", "runId": "run-peer", "specDigest": digest(task), "gates": [
            {"id": "command:cross-run-long-verification", "status": "pass", "command": {
                "argv": task["acceptance"]["commands"][0]["argv"], "exitCode": 0,
                "startedAt": "2026-09-07T00:00:00Z", "completedAt": "2026-09-07T00:01:40Z"}}]}
        projection = {"reportDigest": digest(report), "run": {"runId": "run-peer"}}
        return task, report, projection, digest

    def test_overlap_binds_report_task_and_actual_command_interval(self):
        task, report, projection, _ = self.fixture()
        stamp = lambda v: driver.datetime.datetime.fromisoformat(v.replace("Z", "+00:00")).timestamp()
        start = stamp(report["gates"][0]["command"]["startedAt"])
        self.assertFalse(driver.cross_run_overlap(report, task, projection, start + 65)["accepted"])
        for stopped in (start - 1, start, start + 100, start + 101):
            with self.assertRaises(driver.DriveError):
                driver.cross_run_overlap(report, task, projection, stopped)
        for mutation in ("digest", "argv", "duration", "spec", "status"):
            task, report, projection, digest = self.fixture()
            if mutation == "digest": projection["reportDigest"] = "wrong"
            if mutation == "argv": report["gates"][0]["command"]["argv"] = ["other"]
            if mutation == "duration": report["gates"][0]["command"]["completedAt"] = "2026-09-07T00:01:10Z"
            if mutation == "spec": report["specDigest"] = "wrong"
            if mutation == "status": report["status"] = "fail"
            if mutation != "digest": projection["reportDigest"] = digest(report)
            with self.subTest(mutation=mutation), self.assertRaises(driver.DriveError):
                driver.cross_run_overlap(report, task, projection, start + 65)

    def test_start_failure_does_not_retry(self):
        calls, saved = [], {}
        def call(args, remaining):
            calls.append(args)
            if len(calls) == 1:
                return 0, {"runId": "run-stop", "state": "READY", "sequence": 2, "authorityHead": "sha256:" + "a" * 64}
            return 1, {}
        with self.assertRaises(driver.DriveError):
            driver.start_ready(call, saved.__setitem__, "run-stop", time.time() + 60)
        self.assertEqual([args[0] for args in calls], ["inspect", "start"])
        self.assertIn("start-response.json", saved)


def run(state, sequence):
    return {"runId": "run-test", "attemptId": "attempt-test", "taskId": "task-test", "state": state,
            "sequence": sequence, "authorityHead": "sha256:" + str(sequence) * 64}


def result(state, sequence, **fields):
    value = run(state, sequence)
    return {"Projection": dict(run=value, **fields), "Receipt": {"runId": value["runId"], "attemptId": value["attemptId"],
                                                              "postRevision": sequence, "postAuthorityHead": value["authorityHead"]}}


class BusinessStopObservationTest(unittest.TestCase):
    def invoke(self, replies, limit=20):
        calls, saved, clock = [], {}, [0]

        def call(args, remaining):
            self.assertGreater(remaining, 0)
            calls.append(list(args))
            return replies[min(len(calls) - 1, len(replies) - 1)]

        def pause(seconds):
            clock[0] += seconds

        def drive():
            return driver.observe_business_stop(call, saved.__setitem__, "run-test", limit,
                                                now=lambda: clock[0], pause=pause)
        return drive, calls, saved

    def test_observe_only_until_stopped_then_collect_current_head(self):
        drive, calls, saved = self.invoke([(0, run("RUNNING", 3)), (0, run("RUNNING", 3)),
                                          (0, run("BLOCKED", 4)),
                                          (1, {"disposition": "stopped", "reasonCode": "run-stopped"}),
                                          (0, run("BLOCKED", 4))])
        summary = drive()
        self.assertEqual([c[0] for c in calls], ["inspect", "inspect", "inspect", "collect", "inspect"])
        self.assertEqual(calls[3][calls[3].index("--expected-sequence") + 1], "4")
        self.assertFalse(summary["accepted"])
        self.assertFalse(summary["deadlineWitnessVerified"])
        self.assertEqual(saved["business-stop-observed.json"]["elapsedSeconds"], 4)

    def test_expiry_is_failure_not_cancel_or_collect(self):
        drive, calls, saved = self.invoke([(0, run("RUNNING", 3))], limit=3)
        with self.assertRaisesRegex(driver.DriveError, "observation-deadline"):
            drive()
        self.assertTrue(all(c[0] == "inspect" for c in calls))
        self.assertNotIn("business-stop-summary.json", saved)

    def test_unavailable_wrong_successor_or_completion_is_not_retried(self):
        for reply in ((1, {}), (3, driver.LIVE_PENDING), (0, run("ACCEPTED", 6)),
                      (0, run("BLOCKED", 5)), (0, run("RUNNING", 4))):
            drive, calls, saved = self.invoke([(0, run("RUNNING", 3)), reply])
            with self.assertRaises(driver.DriveError):
                drive()
            self.assertEqual(len(calls), 2)
            self.assertNotIn("business-stop-summary.json", saved)

    def test_already_blocked_is_only_observation_not_deadline_proof(self):
        drive, calls, saved = self.invoke([(0, run("BLOCKED", 4)),
                                          (1, {"disposition": "stopped", "reasonCode": "run-stopped"}),
                                          (0, run("BLOCKED", 4))])
        self.assertFalse(drive()["deadlineWitnessVerified"])
        self.assertFalse(saved["business-stop-observed.json"]["observedRunning"])

    def test_stopped_collect_and_final_head_must_match(self):
        for replies in ([(0, run("BLOCKED", 4)), (3, driver.LIVE_PENDING)],
                        [(0, run("BLOCKED", 4)), (1, {"disposition": "stopped", "reasonCode": "run-stopped"}),
                         (0, run("BLOCKED", 5))]):
            drive, calls, saved = self.invoke(replies)
            with self.assertRaises(driver.DriveError):
                drive()
            self.assertEqual(len(calls), len(replies))
            self.assertNotIn("business-stop-summary.json", saved)


class BusinessStopRecoveryTest(unittest.TestCase):
    def previous(self):
        saved = {}
        replies = iter([(0, run("BLOCKED", 4)), (1, {"disposition": "stopped", "reasonCode": "run-stopped"}), (0, run("BLOCKED", 4))])
        driver.observe_business_stop(lambda *a: next(replies), saved.__setitem__, "run-test", 100, now=lambda: 0)
        saved["driver-subject.json"] = {"binarySHA256": "binary-one", "runId": "run-test"}
        return saved

    def test_cold_recovery_reuses_exact_request_and_deadline(self):
        original = self.previous()
        prior, deadline = driver.business_stop_recovery(original.__getitem__, "binary-one", "run-test", 10, 480)
        self.assertEqual(deadline, 100)
        replies = iter([(0, run("BLOCKED", 4)), (1, {"disposition": "stopped", "reasonCode": "run-stopped"}), (0, run("BLOCKED", 4))])
        calls, saved = [], {}
        def call(args, remaining):
            calls.append(list(args))
            return next(replies)
        summary = driver.observe_business_stop(call, saved.__setitem__, "run-test", deadline, now=lambda: 10, previous=prior)
        self.assertFalse(summary["accepted"])
        self.assertEqual([c[0] for c in calls], ["inspect", "collect", "inspect"])
        self.assertEqual(saved["business-stop-collect-request.json"], original["business-stop-collect-request.json"])

    def test_identity_expiry_and_unproved_prior_are_rejected(self):
        for kind in ("binary", "run", "accepted", "stage", "expired", "future"):
            saved = self.previous(); now = 10
            if kind == "binary": saved["driver-subject.json"]["binarySHA256"] = "other"
            if kind == "run": saved["business-stop-summary.json"]["runId"] = "other-run"
            if kind == "accepted": saved["business-stop-summary.json"]["accepted"] = True
            if kind == "stage": saved["business-stop-summary.json"]["stage"] = "unproved"
            if kind == "expired": now = 100
            if kind == "future": now = -500
            with self.assertRaises(driver.DriveError):
                driver.business_stop_recovery(saved.__getitem__, "binary-one", "run-test", now, 480)

    def test_recovery_cannot_wait_for_new_stop_or_change_frozen_arguments(self):
        for kind in ("running", "head", "deadline", "key", "operation"):
            prior, deadline = driver.business_stop_recovery(self.previous().__getitem__, "binary-one", "run-test", 10, 480)
            value = run("BLOCKED", 4)
            if kind == "running": value = run("RUNNING", 3)
            if kind == "head": value["authorityHead"] = "sha256:" + "f" * 64
            if kind == "deadline": deadline = 110
            if kind == "key": prior["request"]["args"][-1] = "other-key"
            if kind == "operation": prior["request"]["args"][0] = "cancel"
            calls = []
            def call(args, remaining):
                calls.append(args)
                return 0, value
            with self.assertRaises(driver.DriveError):
                driver.observe_business_stop(call, lambda *a: None, "run-test", deadline, now=lambda: 10, previous=prior)
            self.assertEqual([c[0] for c in calls], ["inspect"])


class CancelRunTest(unittest.TestCase):
    def replies(self):
        stopped = result("BLOCKED", 4, protocolRevision="run-stop/v1", terminalReason="aborted-by-operator",
                         requestDigest="sha256:" + "a" * 64, stopIntentDigest="sha256:" + "b" * 64,
                         outcomeDigest="sha256:" + "c" * 64)
        return [(0, run("RUNNING", 3)), (0, stopped), (0, copy.deepcopy(stopped)),
                (1, {"disposition": "stopped", "reasonCode": "run-stopped"}), (0, run("BLOCKED", 4))]

    def invoke(self, replies):
        calls, saved = [], {}

        def call(args, remaining):
            self.assertGreater(remaining, 0)
            calls.append(list(args))
            return replies[len(calls) - 1]

        return call, calls, saved

    def test_cancel_replay_and_collect_stop_without_acceptance(self):
        call, calls, saved = self.invoke(self.replies())
        summary = driver.cancel_run(call, saved.__setitem__, "run-test", 100, now=lambda: 0)
        self.assertFalse(summary["accepted"])
        self.assertEqual(summary["stage"], "cancelled")
        self.assertEqual(calls[1], calls[2])
        self.assertEqual([item[0] for item in calls], ["inspect", "cancel", "cancel", "collect", "inspect"])
        self.assertEqual(calls[3][calls[3].index("--expected-sequence") + 1], "4")
        self.assertEqual(calls[3][calls[3].index("--expected-authority-head") + 1], run("BLOCKED", 4)["authorityHead"])
        self.assertEqual(calls[1][calls[1].index("--expected-sequence") + 1], "3")
        self.assertEqual(calls[3][calls[3].index("--deadline") + 1], calls[1][calls[1].index("--deadline") + 1])
        self.assertEqual(saved["cancel-summary.json"]["run"], run("BLOCKED", 4))
        self.assertNotIn("review-summary.json", saved)

    def test_uncertain_cancel_is_never_retried(self):
        replies = self.replies()
        replies[1] = (3, driver.LIVE_PENDING)
        call, calls, saved = self.invoke(replies)
        with self.assertRaisesRegex(driver.DriveError, "cancel-unresolved"):
            driver.cancel_run(call, saved.__setitem__, "run-test", 100, now=lambda: 0)
        self.assertEqual(len(calls), 2)
        self.assertNotIn("cancel-summary.json", saved)

    def test_rejects_partial_wrong_reason_and_wrong_receipt(self):
        for kind in ("digest", "reason", "receipt", "attempt"):
            replies = self.replies()
            value = replies[1][1]
            if kind == "digest":
                del value["Projection"]["outcomeDigest"]
            elif kind == "reason":
                value["Projection"]["terminalReason"] = "attempt-deadline-exceeded"
            elif kind == "receipt":
                value["Receipt"]["postRevision"] += 1
            else:
                value["Projection"]["run"]["attemptId"] = "attempt-other"
            call, calls, saved = self.invoke(replies)
            with self.assertRaises(driver.DriveError):
                driver.cancel_run(call, saved.__setitem__, "run-test", 100, now=lambda: 0)
            self.assertEqual(len(calls), 2)

    def test_post_stop_replay_collect_and_query_must_match(self):
        for index, value in ((2, (1, {})), (3, (3, driver.LIVE_PENDING)), (4, (0, run("RUNNING", 3)))):
            replies = self.replies()
            replies[index] = value
            call, calls, saved = self.invoke(replies)
            with self.assertRaises(driver.DriveError):
                driver.cancel_run(call, saved.__setitem__, "run-test", 100, now=lambda: 0)
            self.assertEqual(len(calls), index + 1)
            self.assertNotIn("cancel-summary.json", saved)

    def test_expired_client_deadline_does_not_dispatch_cancel(self):
        call, calls, saved = self.invoke(self.replies())
        with self.assertRaisesRegex(driver.DriveError, "cancel-driver-deadline"):
            driver.cancel_run(call, saved.__setitem__, "run-test", 100, now=lambda: 100)
        self.assertEqual(calls, [])

    def test_restart_uses_original_request_receipt_and_deadline(self):
        call, calls, saved = self.invoke(self.replies())
        driver.cancel_run(call, saved.__setitem__, "run-test", 100, now=lambda: 0)
        previous = {"initial": saved["cancel-initial-run.json"], "request": saved["cancel-request.json"],
                    "response": saved["cancel-response.json"]["response"]}
        replies = self.replies()
        replies[0] = (0, run("BLOCKED", 4))
        recovered, recovery_calls, recovery_saved = self.invoke(replies)
        driver.cancel_run(recovered, recovery_saved.__setitem__, "run-test", 100, now=lambda: 30, previous=previous)
        self.assertEqual(recovery_calls[1], calls[1])
        self.assertEqual(recovery_calls[3], calls[3])
        self.assertFalse(recovery_saved["cancel-summary.json"]["accepted"])
        for kind in ("extended-deadline", "new-attempt", "changed-receipt", "injected-args"):
            altered = copy.deepcopy(previous)
            answers = copy.deepcopy(replies)
            if kind == "new-attempt":
                answers[0] = (0, run("RUNNING", 3))
            elif kind == "changed-receipt":
                answers[1][1]["Projection"]["outcomeDigest"] = "sha256:" + "e" * 64
            elif kind == "injected-args":
                altered["request"]["args"].extend(["--actor", "forged"])
            failed, attempted, evidence = self.invoke(answers)
            with self.assertRaises(driver.DriveError):
                driver.cancel_run(failed, evidence.__setitem__, "run-test", 101 if kind == "extended-deadline" else 100,
                                  now=lambda: 30, previous=altered)
            self.assertLessEqual(len(attempted), 2)
            self.assertNotIn("cancel-summary.json", evidence)


class FinalizeReviewTest(unittest.TestCase):
    def test_wait_is_bounded_and_invalid_publication_is_not_retried(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "decision.json"
            tick = [0]

            def pause(seconds):
                tick[0] += seconds

            with self.assertRaisesRegex(driver.DriveError, "wait-expired"):
                driver.await_external_decision(path, 3, now=lambda: tick[0], pause=pause)
            self.assertEqual(tick[0], 3)
            for raw in (b"", b"{", b'{"kind":1,"kind":2}', b"[]"):
                path.write_bytes(raw)
                with self.assertRaisesRegex(driver.DriveError, "external-decision-invalid"):
                    driver.await_external_decision(path, 1, now=lambda: 0, pause=lambda _: self.fail("invalid input retried"))

    def test_wait_rejects_nonregular_and_linked_records(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = root / "source"
            source.write_text("{}")
            symbolic, hard, fifo = root / "symbolic", root / "hard", root / "fifo"
            symbolic.symlink_to(source)
            os.link(source, hard)
            os.mkfifo(fifo)
            for path in (symbolic, hard, fifo):
                with self.assertRaises(driver.DriveError):
                    driver.await_external_decision(path, 1, now=lambda: 0)

    def test_wait_accepts_complete_record_without_synthesizing_decision(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "decision.json"
            path.write_text('{"kind":"ReviewDecision","verdict":"reject"}')
            self.assertEqual(driver.await_external_decision(path, 1, now=lambda: 0),
                             {"kind": "ReviewDecision", "verdict": "reject"})

    def setup_delivery(self, verdict="accept", state="ACCEPTED"):
        packet = {"taskId": "task-test", "reviewRound": 1, "specDigest": "sha256:" + "b" * 64,
                  "evidenceDigest": "sha256:" + "c" * 64}
        summary = {"run": run("REVIEW_PENDING", 3), "verificationStatus": "pass", "accepted": False,
                   "packetDigest": "sha256:" + "a" * 64}
        decision = dict(packet, kind="ReviewDecision", runId="run-test", verdict=verdict,
                        reviewer={"type": "human", "id": "independent-reviewer"}, reviewPacketDigest=summary["packetDigest"])
        response = result(state, 4, verdict=verdict, evidenceDigest=packet["evidenceDigest"],
                          decisionDigest="sha256:" + "d" * 64, outcomeDigest="sha256:" + "e" * 64)
        responses = [(0, run("REVIEW_PENDING", 3)), (0, response), (0, run(state, 4))]
        saved, calls = {}, []

        def call(args, remaining):
            calls.append(args)
            return responses.pop(0)

        def execute():
            return driver.finalize_review(call, lambda name, value: saved.update({name: value}), summary, packet,
                                          decision, "/fixed/review-decision.json", 30, now=lambda: 0)

        return execute, decision, responses, saved, calls

    def test_external_accept_needs_receipt_outcome_and_current_inspection(self):
        execute, _, _, saved, calls = self.setup_delivery()
        self.assertEqual(execute()["state"], "ACCEPTED")
        self.assertTrue(saved["decision-summary.json"]["accepted"])
        self.assertEqual([args[0] for args in calls], ["inspect", "decision", "inspect"])
        self.assertEqual(calls[1][calls[1].index("--expected-sequence") + 1], "3")

    def test_wrong_packet_or_worker_reviewer_never_invokes(self):
        for key, value in (("reviewPacketDigest", "sha256:" + "0" * 64), ("specDigest", "sha256:" + "0" * 64),
                           ("reviewer", {"type": "worker", "id": "author"})):
            execute, decision, _, _, calls = self.setup_delivery()
            decision[key] = value
            with self.assertRaises(driver.DriveError):
                execute()
            self.assertEqual(calls, [])

    def test_stale_current_head_stops_before_decision(self):
        execute, _, responses, _, calls = self.setup_delivery()
        responses[0] = (0, run("REVIEW_PENDING", 9))
        with self.assertRaises(driver.DriveError):
            execute()
        self.assertEqual(len(calls), 1)

    def test_failed_mutation_is_not_retried(self):
        execute, _, responses, _, calls = self.setup_delivery()
        responses[1] = (3, {"disposition": "pending"})
        with self.assertRaisesRegex(driver.DriveError, "no-automatic-retry"):
            execute()
        self.assertEqual(len(calls), 2)

    def test_no_receipt_or_outcome_cannot_claim_acceptance(self):
        for target, key in (("Projection", "outcomeDigest"), ("Receipt", "postAuthorityHead")):
            execute, _, responses, saved, _ = self.setup_delivery()
            del responses[1][1][target][key]
            with self.assertRaises(driver.DriveError):
                execute()
            self.assertNotIn("decision-summary.json", saved)

    def test_independent_reject_is_delivered_but_not_claimed_accepted(self):
        execute, _, _, saved, calls = self.setup_delivery("reject", "REJECTED")
        with self.assertRaisesRegex(driver.DriveError, "independent-review-not-accepted"):
            execute()
        self.assertFalse(saved["decision-summary.json"]["accepted"])
        self.assertEqual([args[0] for args in calls], ["inspect", "decision", "inspect"])


class DriverTest(unittest.TestCase):
    def test_peer_starts_only_after_collection_and_once_before_verify(self):
        responses = iter(self.happy())
        operations, hooks = [], []
        def call(args, remaining):
            operations.append(args[0])
            if args[0] == "verify":
                self.assertEqual(hooks, ["begin"])
            return next(responses)
        def hook():
            self.assertEqual(operations, ["inspect", "collect", "collect"])
            hooks.append("begin")
        summary = driver.drive(call, lambda *_: None, "run-test", 30,
                               now=lambda: 0, pause=lambda _: None, before_verify=hook)
        self.assertFalse(summary["accepted"])
        self.assertEqual(hooks, ["begin"])

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


class ReviewCaptureTest(unittest.TestCase):
    def fixture(self, root):
        run_dir = root / ".marshal" / "runs" / "run-test"
        run_dir.mkdir(parents=True)
        worker = "attempts/attempt:one/worker-result.json"
        packet = {"runId": "run-test", "inputs": {"taskSpec": "task-spec.json", "patch": "observed.patch",
                  "verificationReport": "verification-report.json", "artifactManifest": "artifact-manifest.json", "workerResults": [worker]},
                  "candidateDigest": "sha256:" + "a" * 64, "workerCandidateDigest": "sha256:" + "a" * 64}
        files = {"review-packet.json": json.dumps(packet).encode(), "task-spec.json": b"{}", "observed.patch": b"business patch",
                 "verification-report.json": b"{}", "artifact-manifest.json": b"{}", worker: b"{}", "worker.patch": b"business patch",
                 "candidates/sha256:" + "a" * 64 + ".json": b"{}"}
        for path, raw in files.items():
            target = run_dir / path
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(raw)
        (run_dir / "credentials.json").write_text("NEVER_EXPORT")
        (run_dir / "stdout.log").write_text("NEVER_EXPORT")
        return run_dir, packet, files

    def test_capture_preserves_exact_bytes_without_logs_or_authority_import(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp).resolve()
            _, packet, files = self.fixture(root)
            archive = root / "review.tar"
            driver.capture_review_inputs(root, "run-test", packet, archive)
            with tarfile.open(archive) as bundle:
                self.assertEqual(set(bundle.getnames()), set(files) | {"capture-manifest.json"})
                for path, raw in files.items():
                    self.assertTrue(bundle.getmember(path).isreg())
                    self.assertEqual(bundle.extractfile(path).read(), raw)
                manifest = json.load(bundle.extractfile("capture-manifest.json"))
                self.assertEqual(manifest["purpose"], "review-only-not-authority-import")
                for entry in manifest["files"]:
                    self.assertEqual(entry["sha256"], hashlib.sha256(files[entry["path"]]).hexdigest())
            original = archive.read_bytes()
            with self.assertRaises(driver.DriveError):
                driver.capture_review_inputs(root, "run-test", packet, archive)
            self.assertEqual(archive.read_bytes(), original)

    def test_rejects_missing_linked_special_oversized_and_changed_input(self):
        for mutation in ("missing", "symlink", "hardlink", "directory-link", "fifo", "large", "packet-drift", "traversal"):
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp).resolve()
                run_dir, packet, _ = self.fixture(root)
                target = run_dir / "observed.patch"
                if mutation in ("missing", "symlink", "hardlink", "fifo"):
                    target.unlink()
                if mutation == "symlink":
                    target.symlink_to(run_dir / "credentials.json")
                elif mutation == "hardlink":
                    os.link(run_dir / "credentials.json", target)
                elif mutation == "directory-link":
                    (run_dir / "attempts").rename(run_dir / "real-attempts")
                    (run_dir / "attempts").symlink_to(run_dir / "real-attempts", target_is_directory=True)
                elif mutation == "fifo":
                    os.mkfifo(target)
                elif mutation == "large":
                    with target.open("wb") as handle:
                        handle.truncate((8 << 20) + 1)
                elif mutation == "packet-drift":
                    (run_dir / "review-packet.json").write_text("{}")
                elif mutation == "traversal":
                    packet["inputs"]["workerResults"] = ["attempts/../../credentials.json"]
                archive = root / "review.tar"
                with self.assertRaises(driver.DriveError):
                    driver.capture_review_inputs(root, "run-test", packet, archive)
                self.assertFalse(archive.exists())


if __name__ == "__main__":
    unittest.main()
