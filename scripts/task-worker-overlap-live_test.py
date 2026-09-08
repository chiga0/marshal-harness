#!/usr/bin/env python3
"""实时 collector 的合成/文件边界测试；不启动 Worker，不是 Provider 团队证明。"""

import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch


sys.dont_write_bytecode = True


def load(name, filename):
    spec = importlib.util.spec_from_file_location(name, Path(__file__).with_name(filename))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


live = load("live_overlap", "task-worker-overlap-live.py")
fixtures = load("overlap_fixtures", "task-worker-overlap_test.py")
observer = live.observer


class Clock:
    def __init__(self, action=lambda: None):
        self.now, self.action = 0.0, action

    def __call__(self):
        return self.now

    def pause(self, seconds):
        self.now += seconds
        self.action()


class CollectorTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(os.path.realpath(self.tmp.name))
        self.ledger = self.root / "state/runtime-v1/result-ingress/result-ingress.jsonl"
        self.runs = self.root / "state/runs"
        self.output = self.root / "evidence"
        for directory in (self.ledger.parent, self.runs, self.output):
            directory.mkdir(parents=True, mode=0o700)
        self.task, self.snapshot = fixtures.fixture()
        self.snapshot["events"] = [fixtures.raw(dict(json.loads(raw), sequence=2)) for raw in self.snapshot["events"]]
        self.write(self.ledger, b"")

    @staticmethod
    def write(path, data):
        with open(path, "wb") as stream:
            stream.write(data)
        os.chmod(path, 0o600)

    def install(self):
        self.write(self.ledger, ("\n".join(self.snapshot["facts"]) + "\n").encode())
        for raw in self.snapshot["events"]:
            event = json.loads(raw)
            directory = self.runs / event["runId"]
            directory.mkdir(mode=0o700, exist_ok=True)
            prefix = fixtures.raw({"type": "run.created", "sequence": 1, "runId": event["runId"]})
            self.write(directory / "events.jsonl", (prefix + "\n" + raw + "\n").encode())

    def collect(self, clock=None, timeout=0.2):
        clock = clock or Clock()
        return live.collect(str(self.ledger), str(self.runs), self.task, timeout, clock, clock.pause)

    def invoke(self, probe=None, *extra):
        if probe is None:
            workers, _ = observer.extract_workers(self.snapshot, self.task)
            samples = iter(fixtures.samples(workers))

            class FakeProbe:
                description = {"kind": "synthetic-fixture-not-provider-team-proof"}

                def __call__(self, worker):
                    return next(samples)
            probe = FakeProbe()
        stdout = io.StringIO()
        with contextlib.redirect_stdout(stdout), patch.object(observer, "DarwinProbe", return_value=probe):
            status = live.main(["--ledger", str(self.ledger), "--runs-root", str(self.runs), "--task-id", self.task,
                                "--output-dir", str(self.output), *extra])
        return status, json.loads(stdout.getvalue()), stdout.getvalue()

    def test_complete_original_records_are_preserved_and_bound(self):
        self.install()
        before = self.ledger.read_bytes()
        snapshot, workers, _ = self.collect()
        self.assertEqual(set(snapshot["facts"]), set(self.snapshot["facts"]))
        self.assertEqual(set(snapshot["events"]), set(self.snapshot["events"]))
        self.assertEqual([w["nodeId"] for w in workers], ["client", "service"])
        self.assertEqual(before, self.ledger.read_bytes())

    def test_incremental_partial_append_and_late_run_files(self):
        # Split one original record inside a UTF-8 character, preserving its sealed bytes.
        creation = json.loads(self.snapshot["facts"][1])
        creation["creation"]["inputs"]["task"]["context"] = "私有上下文"
        creation["creation"]["inputsDigest"] = fixtures.d(fixtures.raw(creation["creation"]["inputs"]))
        self.snapshot["facts"][1] = fixtures.raw(fixtures.seal(creation))
        self.install()
        original = self.ledger.read_bytes()
        events = {p: p.read_bytes() for p in self.runs.glob("*/events.jsonl")}
        for path in events:
            path.unlink()
        self.write(self.ledger, b"")
        cut = original.index("私".encode()) + 1
        chunks = iter((original[:cut], original[cut:]))
        ticks = []

        def append():
            ticks.append(1)
            try:
                chunk = next(chunks)
            except StopIteration:
                for path, raw in events.items():
                    self.write(path, raw)
                return
            with open(self.ledger, "ab") as stream:
                stream.write(chunk)

        snapshot, _, _ = self.collect(Clock(append), timeout=0.5)
        self.assertGreaterEqual(len(ticks), 3)
        self.assertEqual(self.ledger.read_bytes(), original)
        self.assertEqual(set(snapshot["facts"]), set(self.snapshot["facts"]))

    def test_partial_tail_times_out_without_repair(self):
        raw = b'{"sequence":1,"PRIVATE_BUSINESS_CONTEXT":'
        self.write(self.ledger, raw)
        with self.assertRaisesRegex(live.Unavailable, "collection-timeout-partial-append"):
            self.collect()
        self.assertEqual(self.ledger.read_bytes(), raw)

    def test_empty_or_wrong_task_times_out_without_fabrication(self):
        self.install()
        self.task = "other-task"
        with self.assertRaisesRegex(live.Unavailable, "collection-timeout-incomplete-evidence"):
            self.collect()

    def test_complete_malformed_record_and_sequence_conflict_fail(self):
        for raw, reason in ((b'{"sequence":1,}\n', "invalid-json"),
                            (b'{"sequence":1,"sequence":1}\n', "duplicate-json-member"),
                            (b'{"sequence":2}\n', "source-sequence-conflict")):
            with self.subTest(reason=reason):
                self.write(self.ledger, raw)
                with self.assertRaisesRegex(live.Unavailable, reason):
                    self.collect()

    def test_fact_tampering_cross_run_and_missing_resume_fail(self):
        for mutation in ("digest", "cross-run", "missing-resume"):
            with self.subTest(mutation=mutation):
                self.task, self.snapshot = fixtures.fixture()
                self.snapshot["events"] = [fixtures.raw(dict(json.loads(r), sequence=2)) for r in self.snapshot["events"]]
                if mutation == "digest":
                    self.snapshot["facts"][1] = self.snapshot["facts"][1].replace("PRIVATE_BUSINESS", "ALTERED_BUSINESS")
                else:
                    event = json.loads(self.snapshot["events"][0])
                    if mutation == "cross-run":
                        event["attemptId"] = "attempt-other"
                    else:
                        event["payload"]["resumeOutcomeFactDigest"] = fixtures.d("absent")
                    self.snapshot["events"][0] = fixtures.raw(event)
                self.install()
                with self.assertRaises(live.Unavailable):
                    self.collect()

    def test_duplicate_plan_and_start_event_are_not_silently_selected(self):
        self.install()
        plan = json.loads(self.snapshot["facts"][0])
        plan["sequence"] = 10
        with open(self.ledger, "ab") as stream:
            stream.write((fixtures.raw(fixtures.seal(plan)) + "\n").encode())
        with patch.object(live, "BATCH", 64):
            with self.assertRaisesRegex(live.Unavailable, "ambiguous-original-plan"):
                self.collect()
        self.install()
        event = json.loads(self.snapshot["events"][0])
        event["sequence"] = 3
        with open(self.runs / event["runId"] / "events.jsonl", "ab") as stream:
            stream.write((fixtures.raw(event) + "\n").encode())
        with self.assertRaisesRegex(live.Unavailable, "ambiguous-original-start-event"):
            self.collect()

    def test_caller_pid_or_unsafe_derived_run_is_not_an_input(self):
        plan = json.loads(self.snapshot["facts"][0])
        plan["plan"]["materializations"][0]["runId"] = "../unrelated"
        self.snapshot["facts"][0] = fixtures.raw(fixtures.seal(plan))
        self.install()
        with self.assertRaisesRegex(live.Unavailable, "invalid-derived-run-id"):
            self.collect()

    def test_history_line_and_record_limits(self):
        self.install()
        for target, amount, reason in (("MAX_LEDGER", 8, "source-history-limit"),
                                       ("MAX_LINES", 1, "source-history-limit")):
            with self.subTest(target=target), patch.object(live, target, amount):
                with self.assertRaisesRegex(live.Unavailable, reason):
                    self.collect()
        with patch.object(observer, "MAX_RECORD", 8):
            with self.assertRaisesRegex(live.Unavailable, "record-too-large"):
                self.collect()

    def test_incremental_reader_does_not_rescan_history(self):
        self.write(self.ledger, b'{"sequence":1}\n')
        with contextlib.closing(live.LiveJSONL(str(self.ledger), live.MAX_LEDGER)) as reader:
            self.assertEqual(len(reader.read()[0]), 1)
            with patch.object(os, "pread", wraps=os.pread) as read:
                self.assertEqual(reader.read()[0], [])
                self.assertEqual(read.call_args.args[1:], (0, len(b'{"sequence":1}\n')))

    def test_replacement_shrink_and_permissions_fail_closed(self):
        for action in ("replace", "shrink", "chmod"):
            with self.subTest(action=action):
                self.write(self.ledger, b'{"sequence":1}\n')
                with contextlib.closing(live.LiveJSONL(str(self.ledger), live.MAX_LEDGER)) as reader:
                    reader.read()
                    if action == "replace":
                        other = self.ledger.with_name("replacement")
                        self.write(other, self.ledger.read_bytes())
                        os.replace(other, self.ledger)
                    elif action == "shrink":
                        self.write(self.ledger, b"")
                    else:
                        os.chmod(self.ledger, 0o644)
                    with self.assertRaises(live.Unavailable):
                        reader.read()

    def test_symlinks_hardlinks_and_nonprivate_directories_fail(self):
        link = self.ledger.with_name("alias")
        link.symlink_to(self.ledger)
        with self.assertRaises(OSError):
            live.LiveJSONL(str(link), live.MAX_LEDGER)
        link.unlink()
        os.link(self.ledger, link)
        with self.assertRaises(live.Unavailable):
            self.collect()
        link.unlink()
        os.chmod(self.runs, 0o755)
        with self.assertRaises(live.Unavailable):
            self.collect()

    def test_sanitized_result_private_original_snapshot_and_no_overwrite(self):
        self.install()
        status, report, stdout = self.invoke()
        self.assertEqual(status, 0)
        self.assertTrue(report["processOverlapProven"])
        self.assertIn("not-core-authority", report["proofScope"])
        self.assertEqual(report["observer"]["kind"], "synthetic-fixture-not-provider-team-proof")
        source, digest = observer.read_snapshot(str(self.output / live.SNAPSHOT))
        self.assertEqual(digest, report["sourceSnapshotDigest"])
        self.assertEqual(set(source["facts"]), set(self.snapshot["facts"]))
        self.assertIn("PRIVATE_BUSINESS_CONTEXT", (self.output / live.SNAPSHOT).read_text())
        self.assertNotIn("PRIVATE_BUSINESS_CONTEXT", stdout)
        self.assertNotIn(str(self.root), stdout)
        self.assertNotIn("/private/fixture/runtime", stdout)
        self.assertEqual(json.loads((self.output / live.REPORT).read_text()), report)
        for name in (live.SNAPSHOT, live.REPORT):
            self.assertEqual((self.output / name).stat().st_mode & 0o777, 0o600)
        prior = (self.output / live.REPORT).read_bytes()
        status, report, _ = self.invoke()
        self.assertEqual(status, 2)
        self.assertEqual(report["reason"], "output-already-exists")
        self.assertEqual((self.output / live.REPORT).read_bytes(), prior)

    def test_os_identity_failure_keeps_snapshot_but_never_claims_overlap(self):
        self.install()

        class Gone:
            description = {"kind": "synthetic-fixture-not-provider-team-proof"}

            def __call__(self, worker):
                raise live.Unavailable("process-identity-changed")
        status, report, _ = self.invoke(Gone())
        self.assertEqual(status, 2)
        self.assertFalse(report["processOverlapProven"])
        self.assertTrue((self.output / live.SNAPSHOT).exists())

    def test_output_cannot_write_source_and_write_failure_cannot_claim_success(self):
        self.install()
        self.output = self.runs
        status, report, _ = self.invoke()
        self.assertEqual(status, 2)
        self.assertEqual(report["reason"], "output-overlaps-source")
        self.assertFalse((self.runs / live.REPORT).exists())
        self.output = self.root / "evidence"
        with patch.object(observer, "write_report", side_effect=OSError("PRIVATE_SECRET")):
            status, report, stdout = self.invoke()
        self.assertEqual(status, 2)
        self.assertFalse(report["processOverlapProven"])
        self.assertNotIn("PRIVATE_SECRET", stdout)

    def test_malformed_source_never_leaks_content_to_cli_result(self):
        self.write(self.ledger, b'{"sequence":1,"PRIVATE_SECRET":}\n')
        status, report, stdout = self.invoke()
        self.assertEqual(status, 2)
        self.assertEqual(report["reason"], "invalid-json")
        self.assertNotIn("PRIVATE_SECRET", stdout)
        self.assertFalse((self.output / live.SNAPSHOT).exists())

    def test_all_source_descriptors_are_read_only(self):
        self.install()
        with patch.object(os, "open", wraps=os.open) as opened:
            status, _, _ = self.invoke()
        self.assertEqual(status, 0)
        for call in opened.call_args_list:
            path, flags = call.args[:2]
            if path not in (live.SNAPSHOT, live.REPORT):
                self.assertEqual(flags & (os.O_WRONLY | os.O_RDWR | os.O_CREAT | os.O_TRUNC | os.O_APPEND), 0)

    def test_invalid_timeout_and_nonprivate_output_fail_without_observing_workers(self):
        self.install()
        for timeout in (0, float("inf"), float("nan"), 121):
            with self.subTest(timeout=timeout):
                with self.assertRaisesRegex(live.Unavailable, "invalid-collection-timeout"):
                    self.collect(timeout=timeout)
        os.chmod(self.output, 0o755)
        status, report, _ = self.invoke()
        self.assertEqual(status, 2)
        self.assertFalse(report["processOverlapProven"])
        self.assertFalse((self.output / live.REPORT).exists())


if __name__ == "__main__":
    unittest.main()
