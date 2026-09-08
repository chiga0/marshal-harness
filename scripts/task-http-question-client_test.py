#!/usr/bin/env python3
"""问答 HTTP 消费者负例与重放；仅自有 Fake HTTP，不证明真实 Agent 交付。"""

import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import stat
import subprocess
import sys
import tempfile
import unittest


def load(name, filename):
    spec = importlib.util.spec_from_file_location(name, Path(__file__).with_name(filename))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


client = load("questions", "task-http-question-client.py")
fixture = load("http_fixture", "task-http-demo-client_test.py")
TASK = "task:demo"
QUESTION = "question:currency"
QUERY = "/v1/tasks/" + TASK + "/questions"
ANSWER = QUERY + "/" + QUESTION + "/answers"
SECRET = "private business answer; never print me"


def body():
    return {"expectedRevision": 9007199254740993, "previewDigest": fixture.DIGEST,
            "questionRevision": 1, "answer": SECRET}


def response():
    value = fixture.task()
    value.update(revision=9007199254740994, questionId=QUESTION,
                 answerFactDigest="sha256:" + "d" * 64,
                 acceptedPreviewDigest=fixture.DIGEST, acceptedRevision=9007199254740994,
                 replayed=False)
    return value


def private(path, value):
    with open(path, "x", encoding="utf-8") as handle:
        os.chmod(path, 0o600)
        json.dump(value, handle)


class QuestionTests(unittest.TestCase):
    def setup_fake(self, fake, tmp):
        fake.supported.extend(["questions", "answer"])
        fake.raw[QUERY] = json.dumps({"taskId": TASK, "revision": 1,
            "previewDigest": fixture.DIGEST, "subjectDigest": fixture.DIGEST,
            "confirmBefore": "2026-09-09T00:00:00Z", "questions": [
                {"id": QUESTION, "slotId": "currency", "revision": 1,
                 "prompt": SECRET, "status": "unanswered"}]}).encode()
        fake.raw[ANSWER] = json.dumps(response()).encode()
        private(Path(tmp) / "connection.json", fake.record())
        private(Path(tmp) / "answer.json", body())

    def arguments(self, tmp, command="answer", output="result.json"):
        args = ["--connection", str(Path(tmp) / "connection.json"), command, "--task-id", TASK]
        if command == "questions":
            return args + ["--output", str(Path(tmp) / output)]
        return args + ["--question-id", QUESTION, "--answer-file", str(Path(tmp) / "answer.json"),
                       "--key", "answer:original", "--preview-out", str(Path(tmp) / output)]

    def run_cli(self, args):
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            rc = client.main(args)
        text = out.getvalue()
        for secret in (SECRET, fixture.SECRET, fixture.TOKEN):
            self.assertNotIn(secret, text)
        return rc, json.loads(text)

    def test_question_query_preserves_private_projection_and_zero_questions(self):
        with fixture.Fake() as fake, tempfile.TemporaryDirectory() as tmp:
            self.setup_fake(fake, tmp)
            rc, summary = self.run_cli(self.arguments(tmp, "questions"))
            self.assertEqual(rc, 0)
            self.assertEqual(summary["questionCount"], 1)
            saved = Path(tmp) / "result.json"
            self.assertEqual(stat.S_IMODE(saved.stat().st_mode), 0o600)
            self.assertEqual(json.loads(saved.read_text()), json.loads(fake.raw[QUERY]))
            value = json.loads(fake.raw[QUERY])
            value["questions"] = []
            fake.raw[QUERY] = json.dumps(value).encode()
            rc, summary = self.run_cli(self.arguments(tmp, "questions", "empty.json"))
            self.assertEqual((rc, summary["questionCount"]), (0, 0))
            self.assertTrue(all(c["method"] == "GET" for c in fake.calls))

    def test_answer_lost_response_replays_exact_key_bytes_and_never_approves(self):
        with fixture.Fake() as fake, tempfile.TemporaryDirectory() as tmp:
            self.setup_fake(fake, tmp)
            fake.lose.add(ANSWER)
            rc, summary = self.run_cli(self.arguments(tmp))
            self.assertEqual(rc, 0, summary)
            posts = [c for c in fake.calls if c["method"] == "POST"]
            self.assertEqual(len(posts), 2)
            self.assertEqual(posts[0], posts[1])
            self.assertEqual(json.loads(posts[0]["body"]), body())
            self.assertEqual(posts[0]["key"], "answer:original")
            self.assertEqual([c["path"] for c in fake.calls], ["/v1/capabilities", ANSWER, ANSWER])
            self.assertFalse(summary["approvalRequested"])
            self.assertFalse(summary["deliveryComplete"])
            self.assertEqual(json.loads((Path(tmp) / "result.json").read_text()), response())

    def test_replay_can_return_later_cancelled_projection_without_new_acceptance(self):
        with fixture.Fake() as fake, tempfile.TemporaryDirectory() as tmp:
            self.setup_fake(fake, tmp)
            result = response()
            result.update(replayed=True, status="cancelled", revision=result["revision"] + 1)
            fake.raw[ANSWER] = json.dumps(result).encode()
            rc, summary = self.run_cli(self.arguments(tmp))
            self.assertEqual(rc, 0)
            self.assertTrue(summary["replayed"])
            self.assertLess(summary["acceptedRevision"], summary["revision"])
            self.assertNotIn("status", summary)

    def test_409_and_410_are_not_retried_and_do_not_refresh_revision(self):
        for code in (409, 410):
            with self.subTest(code=code), fixture.Fake() as fake, tempfile.TemporaryDirectory() as tmp:
                self.setup_fake(fake, tmp)
                fake.status[ANSWER] = code
                fake.raw[ANSWER] = (SECRET + fixture.TOKEN).encode()
                rc, summary = self.run_cli(self.arguments(tmp))
                self.assertEqual((rc, summary["code"]), (2, "http-status-" + str(code)))
                self.assertEqual([c["path"] for c in fake.calls], ["/v1/capabilities", ANSWER])
                self.assertFalse((Path(tmp) / "result.json").exists())

    def test_old_capability_refuses_before_write(self):
        with fixture.Fake() as fake, tempfile.TemporaryDirectory() as tmp:
            self.setup_fake(fake, tmp)
            fake.supported.remove("answer")
            rc, summary = self.run_cli(self.arguments(tmp))
            self.assertEqual((rc, summary["code"]), (2, "task-question-operation-not-supported"))
            self.assertEqual(len(fake.calls), 1)

    def test_closed_body_exact_int64_utf8_and_nul_limits(self):
        variants = [{"expectedRevision": v} for v in (True, 0, -1, 1.5, "2", 1 << 63)]
        variants += [{"questionRevision": v} for v in (False, 0, 2, 1 << 63)]
        variants += [{"answer": v} for v in (None, {}, [], "", " \n\t", "x\x00y", "\ud800", "界" * 1366)]
        variants += [{"previewDigest": "bad"}, {"extra": "reject"}]
        for change in variants:
            value = body()
            value.update(change)
            with self.subTest(change=repr(change)), self.assertRaises(client.ClientError):
                client.validate_answer(value)
        value = body()
        value["answer"] = "界" * 1365 + "x"
        self.assertEqual(client.validate_answer(value), value)

    def test_private_input_and_duplicate_fields_fail_before_network(self):
        for kind in ("public", "symlink", "duplicate"):
            with self.subTest(kind=kind), fixture.Fake() as fake, tempfile.TemporaryDirectory() as tmp:
                self.setup_fake(fake, tmp)
                source = Path(tmp) / "answer.json"
                if kind == "public":
                    source.chmod(0o644)
                elif kind == "symlink":
                    source.rename(Path(tmp) / "held.json")
                    source.symlink_to(Path(tmp) / "held.json")
                else:
                    source.write_text('{"answer":"x","answer":"y"}')
                rc, _ = self.run_cli(self.arguments(tmp))
                self.assertEqual(rc, 2)
                self.assertEqual(fake.calls, [])

    def test_existing_output_never_overwritten_and_retry_is_explicit(self):
        with fixture.Fake() as fake, tempfile.TemporaryDirectory() as tmp:
            self.setup_fake(fake, tmp)
            output = Path(tmp) / "result.json"
            output.symlink_to(Path(tmp) / "answer.json")
            rc, summary = self.run_cli(self.arguments(tmp))
            self.assertEqual(rc, 2)
            self.assertTrue(summary["operationMayHaveCommitted"])
            self.assertIn("original-answer-file-and-key", summary["recovery"])
            self.assertEqual(json.loads(output.read_text()), body())
            self.assertEqual(len([c for c in fake.calls if c["method"] == "POST"]), 1)

    def test_wrong_task_receipt_or_preview_is_not_saved(self):
        changes = [{"id": "other"}, {"questionId": "other"}, {"preview": None},
                   {"replayed": "true"}, {"acceptedRevision": 1 << 63},
                   {"acceptedRevision": 9007199254740995}, {"acceptedRevision": 9007199254740993},
                   {"acceptedPreviewDigest": "sha256:" + "e" * 64}, {"answerFactDigest": "bad"}]
        with fixture.Fake() as fake, tempfile.TemporaryDirectory() as tmp:
            self.setup_fake(fake, tmp)
            for change in changes:
                value = response()
                value.update(change)
                fake.raw[ANSWER] = json.dumps(value).encode()
                rc, _ = self.run_cli(self.arguments(tmp))
                self.assertEqual(rc, 2, change)
                self.assertFalse((Path(tmp) / "result.json").exists())

    def test_original_create_pauses_on_awaiting_answer_without_implicit_approval(self):
        with fixture.Fake() as fake, tempfile.TemporaryDirectory() as tmp:
            self.setup_fake(fake, tmp)
            fake.task["status"] = "awaiting-answer"
            private(Path(tmp) / "submission.json", {"template": "test-clarification", "intent": SECRET})
            out = io.StringIO()
            with contextlib.redirect_stdout(out):
                rc = client.transport.main(["--connection", str(Path(tmp) / "connection.json"),
                    "create", "--submission", str(Path(tmp) / "submission.json"), "--key", "create:key",
                    "--preview-out", str(Path(tmp) / "preview.json")])
            self.assertEqual(rc, 0, out.getvalue())
            rows = [json.loads(line) for line in out.getvalue().splitlines()]
            self.assertEqual(rows[-1]["status"], "awaiting-answer")
            self.assertTrue(all(not row["deliveryComplete"] for row in rows))
            self.assertEqual([c["path"] for c in fake.calls if c["method"] == "POST"], ["/v1/tasks"])
            for secret in (SECRET, fixture.SECRET, fixture.TOKEN):
                self.assertNotIn(secret, out.getvalue())

    def test_subprocess_entrypoint_uses_real_http_without_sensitive_stdout(self):
        with fixture.Fake() as fake, tempfile.TemporaryDirectory() as tmp:
            self.setup_fake(fake, tmp)
            result = subprocess.run([sys.executable, "-B", str(Path(client.__file__))]
                                    + self.arguments(tmp), capture_output=True, timeout=10)
            self.assertEqual(result.returncode, 0, result.stderr.decode())
            self.assertEqual(result.stderr, b"")
            for secret in (SECRET, fixture.TOKEN, fixture.SECRET):
                self.assertNotIn(secret.encode(), result.stdout)
            self.assertEqual(json.loads(result.stdout)["event"], "private-answer-response-saved")


if __name__ == "__main__":
    unittest.main()
