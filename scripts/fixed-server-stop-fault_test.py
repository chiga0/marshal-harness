#!/usr/bin/env python3
"""Observer regression only: synthetic records, no Worker/server/signals."""

import copy
import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("fault", Path(__file__).with_name("fixed-server-stop-fault.py"))
fault = importlib.util.module_from_spec(spec)
spec.loader.exec_module(fault)


def seal(value):
    value = copy.deepcopy(value)
    value["digest"] = ""
    value["digest"] = fault.digest(fault.canonical(value))
    return fault.canonical(value) + b"\n"


class StopFaultTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.run = "run-test"
        self.events = self.root / ".marshal/runs/run-test/events.jsonl"
        self.ledger = self.root / ".marshal/runtime-v1/result-ingress/result-ingress.jsonl"
        for path in (self.events, self.ledger):
            path.parent.mkdir(parents=True)
        self.event = {"runId": self.run, "attemptId": "attempt-test", "sequence": 1, "stateTo": "RUNNING", "type": "run.start-outcome"}
        self.barrier = {"factType": "terminalization-barrier", "sequence": 1, "attemptKey": "attempt-key",
                        "transition": {"identity": {"runId": self.run, "attemptId": "attempt-test"}, "stopIntent": {
                            "category": "attempt-deadline-exceeded", "intentDigest": "sha256:" + "a" * 64,
                            "expectedSequence": 1, "expectedAuthorityHead": "sha256:" + "b" * 64}}}
        self.events.write_bytes(fault.canonical(self.event) + b"\n")
        self.ledger.write_bytes(seal(self.barrier))

    def observe(self):
        return fault.observe(self.root, self.run)

    def test_original_stop_survives_interruption_with_advanced_cleanup(self):
        before = self.observe()
        with self.ledger.open("ab") as handle:
            handle.write(seal({"factType": "process-terminal", "sequence": 2, "attemptKey": "attempt-key"}))
        after = self.observe()
        self.assertEqual(fault.verify_interrupted(before, after), after)
        self.assertEqual(after["lastAttemptFactType"], "process-terminal")
        self.assertEqual(before["barrierFactDigest"], after["barrierFactDigest"])

    def test_no_barrier_is_wait_not_permission_to_crash(self):
        self.ledger.write_bytes(seal({"factType": "process-started", "sequence": 1}))
        self.assertIsNone(self.observe())

    def test_other_run_does_not_supply_stop(self):
        self.barrier["transition"]["identity"]["runId"] = "other-run"
        self.ledger.write_bytes(seal(self.barrier))
        self.assertIsNone(self.observe())

    def test_terminal_before_or_after_signal_is_missed_not_success(self):
        for state in ("BLOCKED", "ACCEPTED", "VERIFYING"):
            with self.subTest(state=state):
                event = dict(self.event, stateTo=state)
                self.events.write_bytes(fault.canonical(event) + b"\n")
                with self.assertRaisesRegex(fault.FaultError, "window-missed"):
                    self.observe()

    def test_wrong_journal_subject_or_sequence_rejected(self):
        for changes in ({"runId": "other"}, {"sequence": 2}):
            self.events.write_bytes(fault.canonical(dict(self.event, **changes)) + b"\n")
            with self.assertRaisesRegex(fault.FaultError, "journal-subject"):
                self.observe()

    def test_uncommitted_tail_is_reported_not_a_fact(self):
        with self.ledger.open("ab") as handle:
            handle.write(b'{"factType":"process-terminal"')
        got = self.observe()
        self.assertTrue(got["ingressPartialTail"])
        self.assertEqual(got["lastAttemptFactType"], "terminalization-barrier")

    def test_barrier_must_name_current_attempt_and_run_revision(self):
        good = copy.deepcopy(self.barrier)
        for field, value in (("expectedSequence", 2), ("intentDigest", "wrong"), ("expectedAuthorityHead", None)):
            altered = copy.deepcopy(good)
            altered["transition"]["stopIntent"][field] = value
            self.ledger.write_bytes(seal(altered))
            with self.assertRaisesRegex(fault.FaultError, "stop-subject"):
                self.observe()
        altered = copy.deepcopy(good)
        altered["transition"]["identity"]["attemptId"] = "other-attempt"
        self.ledger.write_bytes(seal(altered))
        with self.assertRaisesRegex(fault.FaultError, "stop-subject"):
            self.observe()

    def test_bad_digest_or_noncanonical_record_rejected(self):
        good = seal(self.barrier)
        for raw in (good.replace(b'"sequence":1', b'"sequence":2'), b" " + good):
            self.ledger.write_bytes(raw)
            with self.assertRaises(fault.FaultError):
                self.observe()

    def test_ambiguous_or_operator_stop_rejected(self):
        self.ledger.write_bytes(seal(self.barrier) * 2)
        with self.assertRaisesRegex(fault.FaultError, "ambiguous"):
            self.observe()
        self.barrier["transition"]["stopIntent"]["category"] = "aborted-by-operator"
        self.ledger.write_bytes(seal(self.barrier))
        with self.assertRaisesRegex(fault.FaultError, "not-business-timeout"):
            self.observe()

    def test_after_must_preserve_exact_subject_stop_and_sequence(self):
        before = self.observe()
        for key in ("runId", "attemptKey", "stopIntentDigest", "barrierFactDigest", "runSequence"):
            after = dict(before, **{key: "changed"})
            with self.assertRaisesRegex(fault.FaultError, "drift"):
                fault.verify_interrupted(before, after)
        with self.assertRaisesRegex(fault.FaultError, "regressed"):
            fault.verify_interrupted(before, dict(before, lastFactSequence=0))
        with self.assertRaises(fault.FaultError):
            fault.verify_interrupted(before, None)

    def test_wait_is_bounded_and_does_not_swallow_structural_failure(self):
        clock, calls = [0.0], []
        def pause(seconds):
            clock[0] += seconds
        def probe():
            calls.append(1)
            return None
        with self.assertRaisesRegex(fault.FaultError, "timeout"):
            fault.await_window(probe, 0.05, now=lambda: clock[0], pause=pause)
        self.assertLessEqual(len(calls), 4)
        def bad():
            raise fault.FaultError("structural")
        with self.assertRaisesRegex(fault.FaultError, "structural"):
            fault.await_window(bad, 10, now=lambda: clock[0], pause=pause)

    def test_linked_special_and_oversize_files_rejected(self):
        original = self.ledger.read_bytes()
        self.ledger.unlink()
        other = self.root / "other"
        other.write_bytes(original)
        self.ledger.symlink_to(other)
        with self.assertRaises(OSError):
            self.observe()
        self.ledger.unlink()
        os.link(other, self.ledger)
        with self.assertRaises(fault.FaultError):
            self.observe()
        self.ledger.unlink()
        os.mkfifo(self.ledger)
        with self.assertRaises(fault.FaultError):
            self.observe()
        self.ledger.unlink()
        with self.ledger.open("wb") as handle:
            handle.truncate(fault.LIMIT + 1)
        with self.assertRaises(fault.FaultError):
            self.observe()


if __name__ == "__main__":
    unittest.main()
