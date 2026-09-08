#!/usr/bin/env python3
"""独立 observer 单测；合成事实和自身 libproc 检查均非 Provider 团队证明。"""

import contextlib
import copy
import importlib.util
import io
import json
import os
from pathlib import Path
import platform
import sys
import tempfile
import unittest
from unittest.mock import patch


sys.dont_write_bytecode = True
SPEC = importlib.util.spec_from_file_location("overlap_observer", Path(__file__).with_name("task-worker-overlap.py"))
observer = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(observer)


def d(label):
    return observer.sha(label.encode())


def raw(value):
    return observer.compact(value).decode()


def seal(value):
    value = copy.deepcopy(value)
    value["digest"] = ""
    value["digest"] = d(raw(value))
    return value


def fixture():
    """仿原字段的合成 fixture；不伪装来自真实 operator/Core。"""
    task_id = "task-fixture"
    namespace = {"tenantNamespace": "fixture", "controlPlaneId": "local", "authorityScopeId": "fixture"}
    scope = {"authorityNamespaceId": namespace, "repositoryIdentityDigest": d("repository")}
    inputs = {"schemaVersion": "bounded-team-inputs/v1", "spec": {"goalId": task_id, "authorityNamespaceId": namespace},
              "proposal": {"goalId": task_id, "authorityNamespaceId": namespace},
              "nodes": [{"nodeId": n, "role": role} for n, role in
                        (("service", "implement"), ("client", "implement"), ("integration", "integrate"))]}
    mats = [{"nodeId": n, "runId": "run-" + n, "taskId": "work-" + n} for n in ("service", "client", "integration")]
    plan = seal({"protocolRevision": "bounded-team-plan/v1", "factType": "team-plan-accepted", "sequence": 1,
                 "scope": scope, "plan": {"inputs": inputs, "approval": {"inputsDigest": d(raw(inputs)), "taskDraftDigest": d("draft")},
                                         "revision": {"goalId": task_id}, "materializations": mats}})
    facts, events = [plan], []
    for index, node in enumerate(("service", "client")):
        seq = 2 + index * 4
        creation_inputs = {"runId": "run-" + node, "capability": {"adapterId": "pi", "probeStatus": "supported"},
                          "task": {"metadata": {"id": "work-" + node},
                          "context": "PRIVATE_BUSINESS_CONTEXT_NOT_FOR_OUTPUT"}}
        creation = seal({"protocolRevision": "bounded-team-run-creation/v1", "factType": "team-run-inputs-frozen",
                         "sequence": seq, "scope": scope, "creation": {"goalId": task_id, "nodeId": node,
                         "runId": "run-" + node, "planFactDigest": plan["digest"], "inputs": creation_inputs,
                         "inputsDigest": d(raw(creation_inputs))}})
        process = {"pid": 5001 + index, "birthSeconds": 1000, "birthMicroseconds": index + 10,
                   "sessionId": 4000, "processGroupId": 5001 + index}
        identity = {"authorityNamespaceId": namespace, "runId": "run-" + node,
                    "taskId": "work-" + node, "attemptId": "attempt-" + node}
        mechanics = {"process": process, "runtimeObjectDigest": d("runtime"), "workingObjectDigest": d(node),
                     "sourceGateRevision": "darwin-source-gate/v1", "exactSetDigest": d("set-" + node),
                     "observerIdentity": "darwin-fixed-process-supervisor/v2", "state": "exec-stopped", "mechanicsState": "exec-stopped"}
        spawn = {"protocolRevision": "process-supervisor/v2", "sessionId": "session-" + node,
                 "command": "spawn", "disposition": "ok", "outcome": mechanics}
        started = seal({"protocolRevision": "attempt-authority/v1", "factType": "process-started", "sequence": seq + 1,
                        "revision": 8, "attemptKey": d("attempt-" + node), "transition": {"kind": "process-started",
                        "identity": identity, "supervisorEvidence": spawn, "process": {"pid": process["pid"],
                        "pgid": process["pid"], "birthSeconds": 1000, "birthMicroseconds": index + 10,
                        "executablePath": "/private/fixture/runtime", "executableSha256": d("runtime-bytes")}}})
        intent = {"protocolRevision": "process-supervisor/v2", "command": "resume", "sessionId": "session-" + node,
                  "commandId": "resume-" + node, "sequence": 3, "previousCommandHead": d("prior"),
                  "currentAuthorityHead": started["digest"], "requestDigest": d("resume-request-" + node),
                  "rebuild": {"processStartedFactDigest": started["digest"]}}
        intent_fact = seal({"protocolRevision": "process-supervisor-command-recovery/v2", "factType": "process-supervisor-command-intent",
                            "sequence": seq + 2, "attemptKey": started["attemptKey"], "attemptRevision": 8,
                            "attemptAuthorityHead": started["digest"], "intent": intent})
        outcome = {k: v for k, v in intent.items() if k != "rebuild"}
        outcome.update(disposition="ok", reasonCode="process-resumed", outcome=dict(mechanics, state="running", mechanicsState="running"),
                       v2Preparation={"projection": {"processStartedFactDigest": started["digest"]}})
        resume = seal({"protocolRevision": "process-supervisor-command-recovery/v2", "factType": "process-supervisor-command-outcome",
                       "sequence": seq + 3, "attemptKey": started["attemptKey"], "attemptRevision": 8,
                       "attemptAuthorityHead": started["digest"], "previousRecoveryFactDigest": intent_fact["digest"], "outcome": outcome})
        event = {"type": "run.start-outcome", "stateFrom": "READY", "stateTo": "RUNNING", "runId": "run-" + node,
                 "attemptId": "attempt-" + node, "payload": {"protocolRevision": "run-start-outcome/v2", "taskId": "work-" + node,
                 "processStartedFactDigest": started["digest"], "resumeOutcomeFactDigest": resume["digest"]}}
        facts.extend((creation, started, intent_fact, resume))
        events.append(event)
    return task_id, {"schemaVersion": observer.SCHEMA, "facts": list(map(raw, facts)), "events": list(map(raw, events))}


def samples(workers, timings=None):
    timings = timings or [(10, 20), (30, 40), (60, 70), (80, 90)]
    for index, (before, after) in zip((0, 1, 0, 1), timings):
        yield {"process": copy.deepcopy(workers[index]["process"]), "uid": os.geteuid(), "status": 3, "flags": 0,
               "_executablePath": workers[index]["_executablePath"], "beforeNs": before, "afterNs": after}


class EvidenceTests(unittest.TestCase):
    def setUp(self):
        self.task, self.snapshot = fixture()

    def extract(self):
        return observer.extract_workers(self.snapshot, self.task)

    def test_original_shape_derives_implement_roles_and_identity(self):
        workers, digest = self.extract()
        self.assertEqual([w["nodeId"] for w in workers], ["client", "service"])
        self.assertEqual(len({w["process"]["pid"] for w in workers}), 2)
        self.assertEqual(digest, json.loads(self.snapshot["facts"][0])["digest"])

    def test_source_bytes_not_reserialized_and_digest_tampering_rejected(self):
        fact = self.snapshot["facts"][1]
        self.snapshot["facts"][1] = fact.replace("PRIVATE_BUSINESS_CONTEXT", "ALTERED_PRIVATE_CONTEXT")
        with self.assertRaisesRegex(observer.Unavailable, "fact-digest-mismatch"):
            self.extract()

    def test_duplicate_keys_and_pid_only_snapshot_rejected(self):
        with self.assertRaisesRegex(observer.Unavailable, "duplicate-json-member"):
            observer.decode('{"pid":1,"pid":2}')
        with self.assertRaises(observer.Unavailable):
            observer.extract_workers({"role": "implement", "pid": 123}, self.task)

    def test_wrong_task_and_missing_facts_fail_before_os_observation(self):
        with self.assertRaises(observer.Unavailable):
            observer.extract_workers(self.snapshot, "another-task")
        self.snapshot["facts"].pop()
        with self.assertRaisesRegex(observer.Unavailable, "incomplete-snapshot"):
            self.extract()

    def test_cross_run_attempt_event_and_unrelated_role_rejected(self):
        event = json.loads(self.snapshot["events"][0])
        event["attemptId"] = "attempt-unrelated"
        self.snapshot["events"][0] = raw(event)
        with self.assertRaises(observer.Unavailable):
            self.extract()

    def test_missing_or_failed_resume_never_proves_process_execution(self):
        event = json.loads(self.snapshot["events"][0])
        event["payload"]["resumeOutcomeFactDigest"] = event["payload"]["processStartedFactDigest"]
        self.snapshot["events"][0] = raw(event)
        with self.assertRaises((observer.Unavailable, KeyError)):
            self.extract()

    def test_resealed_semantically_wrong_resume_is_not_enough(self):
        for mutation in ("process", "state", "attemptKey", "currentAuthorityHead"):
            with self.subTest(mutation=mutation):
                self.task, self.snapshot = fixture()
                fact = json.loads(self.snapshot["facts"][4])
                if mutation == "process":
                    fact["outcome"]["outcome"]["process"]["birthMicroseconds"] += 1
                elif mutation == "state":
                    fact["outcome"]["outcome"]["state"] = "exec-stopped"
                elif mutation == "attemptKey":
                    fact["attemptKey"] = d("unrelated-attempt")
                else:
                    fact["outcome"]["currentAuthorityHead"] = d("unrelated-head")
                fact = seal(fact)
                self.snapshot["facts"][4] = raw(fact)
                event = json.loads(self.snapshot["events"][0])
                event["payload"]["resumeOutcomeFactDigest"] = fact["digest"]
                self.snapshot["events"][0] = raw(event)
                with self.assertRaises(observer.Unavailable):
                    self.extract()

    def test_provider_must_come_from_original_creation_capability(self):
        creation = json.loads(self.snapshot["facts"][1])
        creation["creation"]["inputs"]["capability"]["adapterId"] = "fake"
        creation["creation"]["inputsDigest"] = d(raw(creation["creation"]["inputs"]))
        self.snapshot["facts"][1] = raw(seal(creation))
        with self.assertRaisesRegex(observer.Unavailable, "unsupported-provider-evidence"):
            self.extract()


class SamplingTests(unittest.TestCase):
    def setUp(self):
        task, snapshot = fixture()
        self.workers, _ = observer.extract_workers(snapshot, task)

    def run_samples(self, values):
        iterator = iter(values)
        return observer.overlap(self.workers, lambda _: next(iterator), clock=lambda: 0.0, pause=lambda _: None)

    def test_interleaved_exact_birth_positive_interval(self):
        result = self.run_samples(samples(self.workers))
        self.assertEqual(result["overlapLowerBoundNs"], 20)
        self.assertEqual(result["sampleOrder"], ["client", "service", "client", "service"])
        self.assertNotIn("_executablePath", json.dumps(result))

    def test_stale_running_or_cached_resume_cannot_replace_live_samples(self):
        with self.assertRaisesRegex(observer.Unavailable, "process-observation-unavailable"):
            observer.overlap(self.workers, lambda _: (_ for _ in ()).throw(observer.Unavailable("process-observation-unavailable")))

    def test_pid_reuse_wrong_parent_process_and_runtime_rejected(self):
        for field in ("birthMicroseconds", "pid", "sessionId", "processGroupId"):
            with self.subTest(field=field):
                values = list(samples(self.workers))
                values[2]["process"][field] += 1
                with self.assertRaisesRegex(observer.Unavailable, "process-identity-mismatch"):
                    self.run_samples(values)
        values = list(samples(self.workers))
        values[0]["_executablePath"] = "/private/not-the-provider"
        with self.assertRaises(observer.Unavailable):
            self.run_samples(values)

    def test_zombie_stop_in_creation_and_in_exit_rejected(self):
        for status, flags in ((1, 0), (4, 0), (5, 0), (0, 0), (2, 4)):
            with self.subTest(status=status, flags=flags):
                values = list(samples(self.workers))
                values[1].update(status=status, flags=flags)
                with self.assertRaisesRegex(observer.Unavailable, "process-not-live"):
                    self.run_samples(values)

    def test_no_positive_intersection_and_sequential_execution(self):
        zero = list(samples(self.workers, [(0, 0)] * 4)) * 100
        with self.assertRaisesRegex(observer.Unavailable, "no-positive-live-intersection"):
            self.run_samples(zero)
        values = list(samples(self.workers))
        values[2]["status"] = 5  # A already exited while durable RUNNING remains.
        with self.assertRaises(observer.Unavailable):
            self.run_samples(values)

    def test_monotonic_drift_window_and_clock_bounds(self):
        values = list(samples(self.workers))
        values[2]["beforeNs"] = 25
        with self.assertRaisesRegex(observer.Unavailable, "invalid-monotonic-observation"):
            self.run_samples(values)
        for window in (float("nan"), float("inf"), 0, 11):
            with self.assertRaises(observer.Unavailable):
                observer.overlap(self.workers, None, window)


class FileAndCLITests(unittest.TestCase):
    def test_private_input_output_no_overwrite_symlinks_or_source_disclosure(self):
        task, snapshot = fixture()
        with tempfile.TemporaryDirectory(dir=os.path.realpath(tempfile.gettempdir())) as root:
            source = Path(root, "snapshot.json")
            source.write_text(raw(snapshot))
            source.chmod(0o600)
            loaded, source_digest = observer.read_snapshot(source)
            self.assertEqual(loaded, snapshot)
            self.assertEqual(source_digest, observer.sha(source.read_bytes()))
            dest = Path(root, "report.json")
            observer.write_report(dest, {"status": "fixture-only", "processOverlapProven": False})
            self.assertEqual(dest.stat().st_mode & 0o777, 0o600)
            with self.assertRaises(FileExistsError):
                observer.write_report(dest, {})
            alias = Path(root, "alias.json")
            alias.symlink_to(source)
            with self.assertRaises(observer.Unavailable):
                observer.read_snapshot(alias)
            source.chmod(0o644)
            with self.assertRaises(observer.Unavailable):
                observer.read_snapshot(source)

    def test_failure_saved_without_echoing_raw_input(self):
        with tempfile.TemporaryDirectory(dir=os.path.realpath(tempfile.gettempdir())) as root:
            source, dest = Path(root, "source.json"), Path(root, "report.json")
            source.write_text('{"secret":"DO_NOT_ECHO_RAW_INPUT"}')
            source.chmod(0o600)
            output = io.StringIO()
            with contextlib.redirect_stdout(output):
                code = observer.main(["--task-id", "task-fixture", "--snapshot", str(source), "--output", str(dest)])
            self.assertEqual(code, 2)
            self.assertNotIn("DO_NOT_ECHO_RAW_INPUT", output.getvalue() + dest.read_text())
            self.assertFalse(json.loads(output.getvalue())["processOverlapProven"])

    def test_bounded_input_output_and_nonprivate_parent(self):
        with tempfile.TemporaryDirectory(dir=os.path.realpath(tempfile.gettempdir())) as root:
            source = Path(root, "source.json")
            source.write_bytes(b"{}")
            source.chmod(0o600)
            with patch.object(observer, "MAX_INPUT", 1):
                with self.assertRaisesRegex(observer.Unavailable, "snapshot-too-large"):
                    observer.read_snapshot(source)
            with patch.object(observer, "MAX_OUTPUT", 1):
                with self.assertRaisesRegex(observer.Unavailable, "report-too-large"):
                    observer.write_report(Path(root, "output.json"), {})
            Path(root).chmod(0o755)
            with self.assertRaisesRegex(observer.Unavailable, "evidence-parent-not-private"):
                observer.read_snapshot(source)
            Path(root).chmod(0o700)

    def test_proven_cli_output_is_redacted_and_retains_scope(self):
        task, snapshot = fixture()
        workers, _ = observer.extract_workers(snapshot, task)
        with tempfile.TemporaryDirectory(dir=os.path.realpath(tempfile.gettempdir())) as root:
            source, dest = Path(root, "source.json"), Path(root, "report.json")
            source.write_text(raw(snapshot))
            source.chmod(0o600)
            rows = iter(samples(workers))
            probe = lambda _: next(rows)
            probe.description = {"selfCheck": "synthetic-test-only"}
            output = io.StringIO()
            with patch.object(observer, "DarwinProbe", return_value=probe), patch.object(observer.time, "sleep"), contextlib.redirect_stdout(output):
                code = observer.main(["--task-id", task, "--snapshot", str(source), "--output", str(dest)])
            self.assertEqual(code, 0)
            report = json.loads(dest.read_text())
            self.assertTrue(report["processOverlapProven"])
            self.assertEqual(report["proofScope"], observer.PROOF_SCOPE)
            for forbidden in ("PRIVATE_BUSINESS_CONTEXT", "/private/fixture", "_executablePath"):
                self.assertNotIn(forbidden, dest.read_text() + output.getvalue())

    def test_unsupported_platform_and_short_libproc_return_rejected(self):
        with patch.object(observer.platform, "system", return_value="Linux"):
            with self.assertRaisesRegex(observer.Unavailable, "unsupported-observer-platform"):
                observer.DarwinProbe()
        probe = object.__new__(observer.DarwinProbe)
        class ShortRead:
            @staticmethod
            def proc_pidinfo(*_):
                return 128
        probe.lib = ShortRead()
        with self.assertRaisesRegex(observer.Unavailable, "process-observation-unavailable"):
            probe.read(os.getpid())

    @unittest.skipUnless(platform.system() == "Darwin" and platform.machine() == "arm64" and
                         platform.mac_ver()[0].startswith("26."), "only verified Darwin ABI")
    def test_local_self_probe_only_is_not_provider_team_evidence(self):
        probe = observer.DarwinProbe()
        own = probe.read(os.getpid())
        self.assertEqual(own["process"]["pid"], os.getpid())
        self.assertEqual(own["uid"], os.geteuid())
        self.assertIn(own["status"], (2, 3))
        output = io.StringIO()
        with contextlib.redirect_stdout(output):
            self.assertEqual(observer.main(["--self-check"]), 0)
        report = json.loads(output.getvalue())
        self.assertEqual(report["status"], "self-check-only")
        self.assertFalse(report["processOverlapProven"])


if __name__ == "__main__":
    unittest.main()
