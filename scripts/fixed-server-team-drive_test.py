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
APPROVAL = {"goalId": "goal-quote", "factDigest": "sha256:" + "d" * 64}
OUTCOME = {"outcome": {"state": "completed", "goalId": "goal-quote"}, "planFactDigest": APPROVAL["factDigest"],
           "integrationRunId": RUNS["integration"], "attemptsUsed": 3, "measurement": "attempt-counts-only"}


def running(run):
    return {"runId": run, "state": "RUNNING", "attemptId": "attempt-1", "sequence": 3,
            "authorityHead": "sha256:" + "a" * 64}


class TeamDriveTest(unittest.TestCase):
    def test_blocked_peer_ends_wait_without_requesting_packet_or_work(self):
        previous = running(RUNS["client"])
        for defect in (None, "runId", "attemptId", "sequence", "authorityHead", "skipped", "verifying"):
            previous = running(RUNS["client"])
            value = {**previous, "state": "BLOCKED", "sequence": 4, "authorityHead": "sha256:" + "b"*64}
            if defect == "skipped":
                value["sequence"] = 99
            elif defect == "verifying":
                previous.update(state="VERIFYING", sequence=4)
                value["sequence"] = 5
            elif defect:
                value[defect] = previous[defect] if defect in ("sequence", "authorityHead") else "other"
            calls, saved = [], {}
            def call(command, remaining):
                calls.append(command)
                return 0, value
            with self.subTest(defect=defect), self.assertRaises(driver.Error) as failure:
                driver.observe_review(call, lambda k, v: saved.setdefault(k, v), RUNS["service"], 10,
                                      now=lambda: 0, peers={RUNS["client"]: previous})
            self.assertEqual(calls, [["inspect", "--run", RUNS["client"]]])
            self.assertEqual(str(failure.exception) == "team-peer-blocked", defect is None)
            self.assertEqual(len(saved), 1)

    def test_observer_waits_without_executing_work_including_fast_completion(self):
        for phases in (("RUNNING", "VERIFYING", "REVIEW_PENDING"), ("RUNNING", "REVIEW_PENDING"), ("REVIEW_PENDING",)):
            clock, calls, saved = [0], [], {}
            def projection(phase):
                step = ("RUNNING", "VERIFYING", "REVIEW_PENDING").index(phase)
                return {**running(RUNS["service"]), "state": phase, "sequence": 3+step,
                        "authorityHead": "sha256:" + "abc"[step]*64}
            final = projection("REVIEW_PENDING")
            peer_final = {**final, "runId": RUNS["client"]}
            peer_initial = peer_final if len(phases) == 1 else running(RUNS["client"])
            pending = iter(phases)
            def call(args, remaining):
                calls.append(args[0])
                self.assertGreater(remaining, 0)
                if args[0] == "inspect":
                    if args[-1] == RUNS["client"]:
                        return 0, peer_final
                    return 0, projection(next(pending, "REVIEW_PENDING"))
                self.assertEqual(args[0], "review-packet")
                return 0, {"Projection": {"run": final, "packetDigest": "sha256:" + "d"*64,
                                           "packet": {"runId": RUNS["service"]}},
                           "Receipt": {"runId": RUNS["service"], "attemptId": "attempt-1",
                                       "postRevision": final["sequence"], "postAuthorityHead": final["authorityHead"]}}
            result = driver.observe_review(call, lambda key, value: saved.setdefault(key, value), RUNS["service"], 10,
                                           now=lambda: clock[0], pause=lambda n: clock.__setitem__(0, clock[0]+n),
                                           peers={RUNS["client"]: peer_initial})
            self.assertEqual(result["run"], final)
            self.assertEqual(calls, ["inspect"]*(2*len(phases)) + ["review-packet", "inspect"])
            self.assertFalse(result["accepted"])
            self.assertNotIn("verificationStatus", result)

    def test_observer_stops_on_failure_or_drift_without_retry(self):
        original = running(RUNS["service"])
        variants = [{**original, "state": "FAILED"}, {**original, "runId": RUNS["client"]},
                    {**original, "attemptId": "attempt-2"}, {**original, "sequence": 4},
                    {**original, "authorityHead": "sha256:" + "f"*64},
                    {**original, "state": "REVIEW_PENDING", "sequence": 4}]
        for bad in variants:
            clock, calls = [0], []
            def call(args, remaining):
                calls.append(args[0])
                return 0, original if len(calls) == 1 else bad
            with self.subTest(bad=bad), self.assertRaises(driver.Error):
                driver.observe_review(call, lambda *_: None, RUNS["service"], 5,
                                      now=lambda: clock[0], pause=lambda n: clock.__setitem__(0, clock[0]+n))
            self.assertEqual(calls, ["inspect", "inspect"])
        with self.assertRaisesRegex(driver.Error, "progress-deadline"):
            driver.observe_review(lambda *_: self.fail("expired observer performed IO"), lambda *_: None,
                                  RUNS["service"], 0, now=lambda: 0)

    def test_capture_status_is_diagnostic_and_failed_business_gate_is_retained(self):
        packet = {"runId": RUNS["service"], "taskId": "task-1", "specDigest": "sha256:"+"a"*64, "baseSha": "b"*40}
        summary, saved = {}, {}
        with mock.patch.object(driver.t2, "capture_review_inputs", return_value=json.dumps({**packet, "status": "fail"}).encode()):
            with self.assertRaisesRegex(driver.Error, "business-verification-failed"):
                driver.capture_review(None, RUNS["service"], packet, None, summary,
                                      lambda k, v: saved.setdefault(k, v.copy()), True)
        self.assertEqual(saved["review-summary.json"]["verificationStatus"], "fail")
        self.assertEqual(summary["verificationStatusSource"], "captured-report-diagnostic-only")
        with mock.patch.object(driver.t2, "capture_review_inputs", return_value=json.dumps({**packet, "runId": "other", "status": "pass"}).encode()):
            with self.assertRaises(driver.Error):
                driver.capture_review(None, RUNS["service"], packet, None, {}, lambda *_: None, False)

    def test_observer_rejects_packet_receipt_and_final_state_drift(self):
        current = {**running(RUNS["service"]), "state": "REVIEW_PENDING"}
        for defect in ("receipt", "packet", "final"):
            calls = []
            def call(args, remaining):
                calls.append(args[0])
                if args[0] == "inspect":
                    if defect == "final" and len(calls) > 1:
                        return 0, {**current, "attemptId": "attempt-other"}
                    return 0, current
                return 0, {"Projection": {"run": current, "packetDigest": "sha256:"+"d"*64,
                                           "packet": {"runId": "other" if defect == "packet" else RUNS["service"]}},
                           "Receipt": {"runId": RUNS["service"], "attemptId": "attempt-1",
                                       "postRevision": 4 if defect == "receipt" else 3,
                                       "postAuthorityHead": current["authorityHead"]}}
            with self.subTest(defect=defect), self.assertRaises(driver.Error):
                driver.observe_review(call, lambda *_: None, RUNS["service"], 10, now=lambda: 0)
            self.assertEqual(calls, ["inspect", "review-packet"] + (["inspect"] if defect == "final" else []))

    def test_read_only_inspect_budget_allows_wait_behind_verifier(self):
        clock, calls = [0], []
        current = {**running(RUNS["service"]), "state": "REVIEW_PENDING"}
        def call(args, remaining):
            calls.append(args[0])
            if len(calls) == 1:
                self.assertEqual(remaining, 90)
                # Existing verifier may legitimately hold its lease >30s.
                clock[0] += 40
            if args[0] == "inspect":
                return 0, current
            return 0, {"Projection": {"run": current, "packetDigest": "sha256:"+"d"*64,
                                       "packet": {"runId": RUNS["service"]}},
                       "Receipt": {"runId": RUNS["service"], "attemptId": "attempt-1",
                                   "postRevision": 3, "postAuthorityHead": current["authorityHead"]}}
        driver.observe_review(call, lambda *_: None, RUNS["service"], 90, now=lambda: clock[0])
        self.assertEqual(calls, ["inspect", "review-packet", "inspect"])

    def test_timeout_is_saved_without_output_or_retry(self):
        self.exercise_main(timeout=True)

    def exercise_main(self, live=False, rejected=False, integration_state="ACCEPTED", timeout=False):
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
                if timeout:
                    raise driver.subprocess.TimeoutExpired(command, kwargs["timeout"], output=b"private-output", stderr=b"private-error")
                value = {"found": True, "approval": APPROVAL} if command[2] == "team-approve" else running(command[-1])
                if command[2] == "team-reconcile":
                    value = {"found": True, "approval": APPROVAL, "outcome": OUTCOME}
                return types.SimpleNamespace(returncode=0, stdout=json.dumps(value).encode(), stderr=b"")
            def drive(call, save, run_id, deadline, initial=None, peers=None):
                driven.append(run_id)
                save("review-packet.json", {"Projection": {"packet": {"runId": run_id, "taskId": "task-1", "specDigest": "sha256:" + "e"*64, "baseSha": "f"*40}}})
                summary = {"run": {**running(run_id), "state": "REVIEW_PENDING"}, "packetDigest": "sha256:" + "c"*64}
                return summary
            def capture(root, run, packet, archive):
                archive.write_bytes(b"closed-test-archive")
                return json.dumps({**packet, "status": "pass"}).encode()
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
                    mock.patch.object(driver, "observe_review", side_effect=drive), \
                    mock.patch.object(driver.t2, "capture_review_inputs", side_effect=capture) as captured, \
                    mock.patch.object(driver, "await_integration") as awaited, \
                    mock.patch.object(driver, "review_team", side_effect=review) as reviewed:
                self.assertEqual(driver.main(), 1 if timeout or rejected or (live and integration_state != "ACCEPTED") else 0)
            if timeout:
                self.assertEqual(calls, ["team-approve"])
                raw = (evidence / "team/call-1.json").read_text()
                recorded = json.loads(raw)
                self.assertTrue(recorded["timedOut"])
                self.assertEqual(recorded["timeoutSeconds"], 120)
                self.assertGreaterEqual(recorded["elapsedSeconds"], 0)
                self.assertNotIn("private", raw)
                self.assertEqual(recorded["stdoutSHA256"], driver.hashlib.sha256(b"private-output").hexdigest())
                self.assertFalse(json.loads((evidence / "team/failure.json").read_bytes())["automaticRetry"])
                return
            completed = live and not rejected and integration_state == "ACCEPTED"
            self.assertEqual(calls, ["team-approve", "inspect", "inspect"] + (["team-reconcile"] if completed else []))
            integrated = live and not rejected
            self.assertEqual(driven, [RUNS["service"], RUNS["client"]] + ([RUNS["integration"]] if integrated else []))
            self.assertEqual(captured.call_count, 2 + int(integrated))
            self.assertEqual(reviewed.call_count, int(live) + int(integrated))
            self.assertEqual(awaited.call_count, int(integrated))
            summary = json.loads((evidence / "team/summary.json").read_bytes())
            self.assertEqual(summary["accepted"], completed)
            self.assertEqual(summary["externalCollectCalls"], 0)
            self.assertEqual(summary["externalVerifyCalls"], 0)
            self.assertEqual(summary["integrationExecuted"], integrated)
            self.assertEqual(summary["stage"], "team-completed" if completed else "integration-reviewed" if integrated else "two-implement-reviewed" if live else "two-implement-review-pending")
            if integrated:
                self.assertEqual(summary["goalOutcomeAvailable"], completed)
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

    def test_outcome_wait_only_queries_exact_original_approval(self):
        clock, calls = [0], []
        def call(args, seconds):
            calls.append(args)
            response = {"found": True, "approval": APPROVAL}
            if clock[0] > 0: response["outcome"] = OUTCOME
            return 0, response
        value = driver.await_team_outcome(call, lambda *_: None, "request.json", APPROVAL, RUNS, 3,
                                          now=lambda: clock[0], pause=lambda seconds: clock.__setitem__(0, clock[0]+seconds))
        self.assertEqual(value, OUTCOME)
        self.assertEqual(calls, [["team-reconcile", "--request-file", "request.json"]] * 2)

    def test_outcome_wait_rejects_wrong_subject_claims_and_deadline(self):
        for field, value in (("planFactDigest", "wrong"), ("integrationRunId", RUNS["client"]), ("attemptsUsed", 3.0),
                             ("measurement", "all-tokens-measured"), ("outcome", {"state": "completed", "goalId": "other"})):
            result = {**OUTCOME, field: value}
            with self.subTest(field=field), self.assertRaises(driver.Error):
                driver.await_team_outcome(lambda *_: (0, {"found": True, "approval": APPROVAL, "outcome": result}),
                                          lambda *_: None, "request.json", APPROVAL, RUNS, 1, now=lambda: 0)
        with self.assertRaisesRegex(driver.Error, "observation-deadline"):
            driver.await_team_outcome(lambda *_: self.fail("expired wait performed IO"), lambda *_: None,
                                      "request.json", APPROVAL, RUNS, 0, now=lambda: 0)

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
