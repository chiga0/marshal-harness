#!/usr/bin/env python3
"""纯 HTTP 合成团队演示验收；不启动 Marshal、真实 Agent 或远端服务。"""

import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest import mock


def load(name, filename):
    spec = importlib.util.spec_from_file_location(name, Path(__file__).with_name(filename))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


driver = load("task_http_driver", "task-http-team-drive.py")
fixtures = load("task_http_fixtures", "task-http-demo-client_test.py")


def private(path, value):
    path.write_text(json.dumps(value), encoding="utf-8")
    path.chmod(0o600)
    return str(path)


def delivery(fake, statuses):
    files = fixtures.delivery_files()
    bundle = fixtures.zip_bundle(files)
    fake.task["delivery"] = fixtures.manifest_for(files, bundle)
    fake.raw["/v1/tasks/task:demo/artifact"] = bundle
    fake.media["/v1/tasks/task:demo/artifact"] = "application/zip"
    remaining = list(statuses)
    respond = fake.server.RequestHandlerClass.respond

    def advance(handler):
        if handler.command == "GET" and handler.path == "/v1/tasks/task:demo":
            fake.task["status"] = remaining[0]
            if len(remaining) > 1:
                remaining.pop(0)
        respond(handler)

    fake.server.RequestHandlerClass.respond = advance


class DriverTests(unittest.TestCase):
    def call(self, fake, root, args):
        connection = private(root / "connection.json", fake.record())
        output = io.StringIO()
        with contextlib.redirect_stdout(output), mock.patch.object(driver, "POLL_SECONDS", 0.005):
            result = driver.main(["--connection", connection] + args)
        raw = output.getvalue()
        self.assertNotIn(fixtures.TOKEN, raw)
        self.assertNotIn(fixtures.SECRET, raw)
        return result, json.loads(raw)

    def prepare(self, fake, root):
        submission = private(root / "submission.json", {"template": "order-quote/v1", "intent": fixtures.SECRET, "context": {"text": fixtures.SECRET}})
        preview = root / "preview.json"
        result, summary = self.call(fake, root, ["prepare", "--submission", submission, "--key", "create:key",
                                                "--preview-out", str(preview), "--evidence-dir", str(root / "prepare-evidence")])
        self.assertEqual(result, 0, summary)
        return preview

    def complete(self, fake, root, preview, name="complete-evidence", seconds="1"):
        return self.call(fake, root, ["complete", "--preview", str(preview), "--confirm-preview-digest", fixtures.DIGEST,
                                     "--key", "approve:key", "--evidence-dir", str(root / name),
                                     "--output-dir", str(root / (name + "-delivery")), "--timeout-seconds", seconds])

    def assert_private_evidence(self, folder):
        self.assertEqual(folder.stat().st_mode & 0o777, 0o700)
        self.assertEqual(sorted(p.name for p in folder.iterdir()), ["observations.jsonl", "summary.json"])
        for path in folder.iterdir():
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)
            self.assertLess(path.stat().st_size, driver.MAX_EVIDENCE_BYTES)
            raw = path.read_text()
            self.assertNotIn(fixtures.SECRET, raw)
            self.assertNotIn(fixtures.TOKEN, raw)

    def test_prepare_then_http_core_progress_then_actual_downloaded_business_consumption(self):
        with fixtures.Fake() as fake, tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            preview = self.prepare(fake, root)
            self.assertIn(fixtures.SECRET, preview.read_text())
            self.assertEqual([c["path"] for c in fake.calls], ["/v1/capabilities", "/v1/tasks"])
            delivery(fake, ["running", "review-pending", "completed"])
            result, summary = self.complete(fake, root, preview)
            self.assertEqual(result, 0, summary)
            self.assertTrue(summary["deliveryComplete"])
            self.assertEqual(summary["businessOracle"], "passed")
            self.assertEqual(summary["processOverlapEvidence"], "unavailable")
            self.assertFalse(summary["productionReleaseProven"])
            mutations = [c for c in fake.calls if c["method"] == "POST"]
            self.assertEqual([c["path"] for c in mutations], ["/v1/tasks", "/v1/tasks/task:demo/approve"])
            self.assertEqual(json.loads(mutations[1]["body"]), {"expectedRevision": 9, "previewDigest": fixtures.DIGEST})
            self.assertEqual(mutations[1]["key"], "approve:key")
            observations = [json.loads(line) for line in (root / "complete-evidence/observations.jsonl").read_text().splitlines()]
            self.assertEqual([r["status"] for r in observations if "status" in r], ["running", "review-pending", "completed"])
            self.assert_private_evidence(root / "prepare-evidence")
            self.assert_private_evidence(root / "complete-evidence")

    def test_prepare_preview_cannot_leak_into_sanitized_evidence_directory(self):
        with fixtures.Fake() as fake, tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            source = private(root / "submission", {"intent": fixtures.SECRET})
            result, summary = self.call(fake, root, ["prepare", "--submission", source, "--key", "key",
                "--preview-out", str(root / "evidence/preview"), "--evidence-dir", str(root / "evidence")])
            self.assertEqual(result, 2)
            self.assertEqual(summary["code"], "raw-preview-must-be-outside-evidence-directory")
            self.assertEqual(fake.calls, [])

    def test_observation_timeout_preserves_task_without_failure_cancel_or_recreation(self):
        with fixtures.Fake() as fake, tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            preview = self.prepare(fake, root)
            delivery(fake, ["running"])
            result, summary = self.complete(fake, root, preview, seconds="0.04")
            self.assertEqual(result, 3, summary)
            self.assertEqual(summary["result"], "observation-window-ended")
            self.assertEqual(summary["taskId"], "task:demo")
            self.assertEqual(summary["lastObservedTaskStatus"], "running")
            self.assertFalse(summary["workerCancellationRequested"])
            self.assertFalse(summary["deliveryComplete"])
            self.assertIn("same-key", summary["recovery"])
            self.assertEqual(sum(c["path"] == "/v1/tasks" for c in fake.calls), 1)
            self.assertFalse(any("cancel" in c["path"] for c in fake.calls))
            self.assert_private_evidence(root / "complete-evidence")

    def test_invalid_confirmation_never_reaches_approval_http(self):
        with fixtures.Fake() as fake, tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            preview = self.prepare(fake, root)
            result, summary = self.call(fake, root, ["complete", "--preview", str(preview),
                "--confirm-preview-digest", "sha256:" + "d" * 64, "--key", "approve:key",
                "--evidence-dir", str(root / "evidence"), "--output-dir", str(root / "output")])
            self.assertEqual(result, 2)
            self.assertEqual(summary["taskId"], "task:demo")
            self.assertFalse(any(c["path"].endswith("/approve") for c in fake.calls))

    def test_business_output_cannot_contaminate_sanitized_evidence(self):
        with fixtures.Fake() as fake, tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            preview = self.prepare(fake, root)
            result, summary = self.call(fake, root, ["complete", "--preview", str(preview),
                "--confirm-preview-digest", fixtures.DIGEST, "--key", "approve:key",
                "--evidence-dir", str(root / "evidence"), "--output-dir", str(root / "evidence/output")])
            self.assertEqual(result, 2)
            self.assertEqual(summary["code"], "business-output-must-be-outside-evidence-directory")
            self.assertFalse(any(c["path"].endswith("/approve") for c in fake.calls))

    def test_current_connection_and_original_preview_resume_original_task(self):
        with fixtures.Fake() as before, tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            preview = self.prepare(before, root)
            with fixtures.Fake() as after:
                delivery(after, ["completed"])
                result, summary = self.complete(after, root, preview)
                self.assertEqual(result, 0, summary)
                self.assertEqual(summary["taskId"], "task:demo")
                self.assertFalse(any(c["path"] == "/v1/tasks" for c in after.calls))
                approvals = [c for c in after.calls if c["method"] == "POST"]
                self.assertEqual(len(approvals), 1)
                self.assertEqual(approvals[0]["key"], "approve:key")
                self.assertEqual(json.loads(approvals[0]["body"])["previewDigest"], fixtures.DIGEST)

    def test_conflict_is_not_retried_or_refreshed(self):
        with fixtures.Fake() as fake, tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            preview = self.prepare(fake, root)
            fake.status["/v1/tasks/task:demo/approve"] = 409
            result, summary = self.complete(fake, root, preview)
            self.assertEqual(result, 2)
            self.assertEqual(summary["code"], "http-status-409")
            self.assertEqual(summary["taskId"], "task:demo")
            self.assertEqual(sum(c["path"].endswith("/approve") for c in fake.calls), 1)
            self.assert_private_evidence(root / "complete-evidence")

    def test_blocked_state_is_observed_and_not_replaced(self):
        with fixtures.Fake() as fake, tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            preview = self.prepare(fake, root)
            delivery(fake, ["blocked"])
            result, summary = self.complete(fake, root, preview)
            self.assertEqual(result, 4)
            self.assertEqual(summary["lastObservedTaskStatus"], "blocked")
            self.assertFalse(summary["deliveryComplete"])
            self.assertFalse(any(c["path"].endswith("/artifact") for c in fake.calls))

    def test_completed_status_without_valid_delivery_is_not_demo_success(self):
        with fixtures.Fake() as fake, tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            preview = self.prepare(fake, root)
            delivery(fake, ["completed"])
            del fake.task["delivery"]
            result, summary = self.complete(fake, root, preview)
            self.assertEqual(result, 2)
            self.assertFalse(summary["deliveryComplete"])
            self.assertEqual(summary["code"], "invalid-delivery-manifest")
            self.assertFalse((root / "complete-evidence-delivery").exists())

    def test_external_cancellation_ends_delivery_observation_without_new_mutation(self):
        with fixtures.Fake() as fake, tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            preview = self.prepare(fake, root)
            delivery(fake, ["cancelling", "cancelled"])
            result, summary = self.complete(fake, root, preview)
            self.assertEqual(result, 4, summary)
            self.assertEqual(summary["lastObservedTaskStatus"], "cancelled")
            self.assertFalse(summary["deliveryComplete"])
            self.assertFalse(any(c["path"].endswith(("/cancel", "/artifact")) for c in fake.calls))

    def test_existing_download_is_not_overwritten_on_replayed_confirmation(self):
        with fixtures.Fake() as fake, tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            preview = self.prepare(fake, root)
            delivery(fake, ["completed"])
            output = root / "complete-evidence-delivery"
            output.mkdir()
            (output / "keep").write_text("original")
            result, summary = self.complete(fake, root, preview)
            self.assertEqual(result, 2)
            self.assertFalse(summary["deliveryComplete"])
            self.assertEqual((output / "keep").read_text(), "original")
            self.assertEqual(summary["taskId"], "task:demo")

    def test_evidence_limit_retains_failure_summary_with_task_id(self):
        with fixtures.Fake() as fake, tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            preview = self.prepare(fake, root)
            delivery(fake, ["running"])
            with mock.patch.object(driver, "MAX_EVIDENCE_BYTES", 10):
                result, summary = self.complete(fake, root, preview)
            self.assertEqual(result, 2)
            self.assertEqual(summary["code"], "http-observation-evidence-limit")
            self.assertEqual(summary["taskId"], "task:demo")
            saved = json.loads((root / "complete-evidence/summary.json").read_text())
            self.assertEqual(saved, summary)
            self.assertFalse(any(c["path"].endswith("/approve") for c in fake.calls))


if __name__ == "__main__":
    unittest.main()
