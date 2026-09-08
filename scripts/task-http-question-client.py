#!/usr/bin/env python3
"""Task HTTP 问答消费者（ADR 0086）；只保存私有原始响应，不自动批准。

--connection FILE questions --task-id ID --output NEW_FILE
--connection FILE answer --task-id ID --question-id ID --answer-file FILE
                         --key KEY --preview-out NEW_FILE

answer-file 必须为 0600 JSON，且仅含 expectedRevision、previewDigest、
questionRevision、answer（字符串）。先查看问题与预览，再提供原 revision。
409/410 不刷新 revision、不换 key；传输失败只重发相同请求一次。回答后仍须
显式查看最终预览并用 task-http-demo-client.py approve 确认；本客户端不启动
Worker，不写 Core 状态目录，不提供验收结论。questions 支持零问题响应。
"""

import argparse
import importlib.util
import json
import os
from pathlib import Path
import sys


_spec = importlib.util.spec_from_file_location(
    "task_http_transport", Path(__file__).with_name("task-http-demo-client.py"))
transport = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(transport)
ClientError = transport.ClientError


def valid_revision(value):
    return type(value) is int and 0 < value <= (1 << 63) - 1


def valid_digest(value):
    return isinstance(value, str) and transport.DIGEST.fullmatch(value) is not None


def validate_answer(value):
    if (set(value) != {"expectedRevision", "previewDigest", "questionRevision", "answer"}
            or not valid_revision(value["expectedRevision"])
            or type(value["questionRevision"]) is not int or value["questionRevision"] != 1
            or not valid_digest(value["previewDigest"])
            or not isinstance(value["answer"], str) or not value["answer"].strip()
            or "\x00" in value["answer"]):
        raise ClientError("invalid-answer-request")
    try:
        size = len(value["answer"].encode("utf-8"))
    except UnicodeError:
        raise ClientError("invalid-answer-request") from None
    if not 0 < size <= 4096:
        raise ClientError("invalid-answer-request")
    return value


def save_private(path, value):
    # Full original projection/receipt; no summary-to-authority reconstruction.
    try:
        raw = (json.dumps(value, ensure_ascii=False, indent=2) + "\n").encode("utf-8")
    except (UnicodeError, ValueError):
        raise ClientError("invalid-response-json") from None
    try:
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
        with os.fdopen(fd, "wb") as handle:
            handle.write(raw)
            handle.flush()
            os.fsync(handle.fileno())
    except OSError:
        raise ClientError("private-response-save-failed") from None


def questions(client, task_id):
    result = client.request("GET", "/v1/tasks/" + task_id + "/questions")
    if (result.get("taskId") != task_id or not valid_revision(result.get("revision"))
            or not valid_digest(result.get("previewDigest"))
            or not isinstance(result.get("questions"), list)):
        raise ClientError("invalid-questions-response")
    # Question text/answer and additional projection fields remain in the private
    # response. Core is the sole validator of slot and business semantics.
    return result


def answer(client, task_id, question_id, body, key):
    result = client.request("POST", "/v1/tasks/" + task_id + "/questions/"
                            + question_id + "/answers", body, key)
    if (transport.preview_identity(result) != task_id
            or not isinstance(result.get("preview"), dict)
            or result.get("questionId") != question_id
            or not valid_digest(result.get("answerFactDigest"))
            or not valid_digest(result.get("acceptedPreviewDigest"))
            or not valid_revision(result.get("acceptedRevision"))
            or result["acceptedRevision"] != body["expectedRevision"] + 1
            or result["acceptedRevision"] > result["revision"]
            or (result["acceptedRevision"] == result["revision"]
                and result["acceptedPreviewDigest"] != result["previewDigest"])
            or type(result.get("replayed")) is not bool):
        raise ClientError("invalid-answer-response")
    # A replay may expose a later cancelled/current projection. Never label it
    # a new acceptance or silently approve its current preview.
    return result


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__,
                                     formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--connection", required=True)
    commands = parser.add_subparsers(dest="command", required=True)
    query = commands.add_parser("questions")
    query.add_argument("--task-id", required=True)
    query.add_argument("--output", required=True)
    respond = commands.add_parser("answer")
    respond.add_argument("--task-id", required=True)
    respond.add_argument("--question-id", required=True)
    respond.add_argument("--answer-file", required=True)
    respond.add_argument("--key", required=True)
    respond.add_argument("--preview-out", required=True)
    args = parser.parse_args(argv)
    client = None
    answer_sent = False

    def emit(value):
        raw = json.dumps(value, ensure_ascii=False)
        if client is not None:
            raw = raw.replace(client.token, "[redacted]")
        print(raw, flush=True)

    try:
        if not transport.valid_id(args.task_id):
            raise ClientError("invalid-task-id")
        body = None
        if args.command == "answer":
            if not transport.valid_id(args.question_id):
                raise ClientError("invalid-question-id")
            if not transport.valid_id(args.key) or len(args.key) > 128:
                raise ClientError("invalid-idempotency-key")
            body = validate_answer(transport.private_json(args.answer_file, 32 << 10))
        client = transport.Client(transport.private_json(args.connection, 4096))
        caps = client.capabilities()
        if args.command not in caps["supported"]:
            raise ClientError("task-question-operation-not-supported")
        if args.command == "questions":
            result = questions(client, args.task_id)
            save_private(args.output, result)
            event = "private-questions-saved"
        else:
            answer_sent = True
            result = answer(client, args.task_id, args.question_id, body, args.key)
            save_private(args.preview_out, result)
            event = "private-answer-response-saved"
        summary = {"event": event, "taskId": args.task_id,
                   "revision": result["revision"], "previewDigest": result["previewDigest"],
                   "approvalRequested": False, "deliveryComplete": False}
        if args.command == "answer":
            summary.update(replayed=result["replayed"], acceptedRevision=result["acceptedRevision"])
        else:
            summary["questionCount"] = len(result["questions"])
        emit(summary)
        return 0
    except (ClientError, KeyboardInterrupt) as exc:
        error = {"event": "client-error", "code": "interrupted" if isinstance(exc, KeyboardInterrupt)
                 else str(exc), "approvalRequested": False, "deliveryComplete": False}
        if answer_sent:
            # Includes valid HTTP response followed by local save failure. Keep
            # the original request file/key, even if the current Task has moved.
            error.update(operationMayHaveCommitted=True,
                         recovery="inspect-task-or-replay-original-answer-file-and-key-with-new-output")
        emit(error)
        return 130 if isinstance(exc, KeyboardInterrupt) else 2


if __name__ == "__main__":
    sys.exit(main())
