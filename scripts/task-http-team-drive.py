#!/usr/bin/env python3
"""已启动 Task HTTP 服务的两阶段演示入口，不启动服务或逐 Run 推生命周期。

prepare --submission FILE --key KEY --preview-out PRIVATE_FILE --evidence-dir NEW_DIR
complete --preview FILE --confirm-preview-digest SHA256 --key KEY --evidence-dir NEW_DIR
         --output-dir NEW_DIR [--timeout-seconds 600]

两阶段均需 --connection FILE。先人工查看 prepare 保存的原预览，再执行
complete；digest 是唯一显式确认。原预览可能含业务正文，单独存放，不进入
脱敏证据目录。complete 会在新目录执行已固定的订单报价业务 oracle，仅适用
于可信代码、本机普通用户；它不是恶意代码沙箱或正式发布验收。

--timeout-seconds 只限制观察窗口；下载与业务 oracle 使用现有客户端的有界
期限。超时不取消、不把 Task 判失败。重启后可用新 connection、原 preview
和同一个 key 重放原确认，另选新的 evidence/output 目录，不重建 Task。
"""

import argparse
import importlib.util
import json
import math
import os
from pathlib import Path
import sys
import time


SPEC = importlib.util.spec_from_file_location("task_http_consumer", Path(__file__).with_name("task-http-demo-client.py"))
consumer = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(consumer)
POLL_SECONDS = 2
MAX_EVIDENCE_BYTES = 512 << 10


class Evidence:
    """Bounded private observations, not a second Task authority store."""

    def __init__(self, folder):
        self.directory = self.log = None
        self.written = 0
        self.token = ""
        try:
            os.mkdir(folder, 0o700)
            self.directory = os.open(folder, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
            self.log = os.open("observations.jsonl", os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW,
                               0o600, dir_fd=self.directory)
        except OSError:
            self.close()
            raise consumer.ClientError("evidence-directory-unavailable-use-new-directory") from None

    def encoded(self, value):
        raw = json.dumps(value, ensure_ascii=False, separators=(",", ":"))
        if self.token:
            raw = raw.replace(self.token, "[redacted]")
        return (raw + "\n").encode("utf-8")

    def append(self, value):
        raw = self.encoded(value)
        if self.written + len(raw) > MAX_EVIDENCE_BYTES:
            raise consumer.ClientError("http-observation-evidence-limit")
        try:
            self.write_all(self.log, raw)
            os.fsync(self.log)
        except OSError:
            raise consumer.ClientError("http-observation-evidence-unavailable") from None
        self.written += len(raw)

    @staticmethod
    def write_all(fd, raw):
        while raw:
            size = os.write(fd, raw)
            if size <= 0:
                raise OSError("short write")
            raw = raw[size:]

    def finish(self, value):
        raw = self.encoded(value)
        if len(raw) > 16384:
            raise consumer.ClientError("summary-limit")
        try:
            fd = os.open("summary.json", os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW,
                         0o600, dir_fd=self.directory)
            with os.fdopen(fd, "wb") as target:
                target.write(raw)
                target.flush()
                os.fsync(target.fileno())
            os.fsync(self.directory)
        except OSError:
            raise consumer.ClientError("summary-unavailable") from None

    def close(self):
        for field in ("log", "directory"):
            fd = getattr(self, field)
            if fd is not None:
                os.close(fd)
                setattr(self, field, None)


def observation_window(value):
    seconds = float(value)
    if not math.isfinite(seconds) or not 0 < seconds <= 600:
        raise argparse.ArgumentTypeError("观察期限须在 0 到 600 秒之间")
    return seconds


def run(args, evidence, summary):
    preview = None
    if args.command == "complete":
        preview = consumer.private_json(args.preview)
        summary["taskId"] = consumer.preview_identity(preview)
        summary["recovery"] = "use-original-preview-and-same-key-with-current-connection-and-new-output-directories"
        if os.path.commonpath((Path(args.output_dir).absolute().resolve(), Path(args.evidence_dir).resolve())) == str(Path(args.evidence_dir).resolve()):
            raise consumer.ClientError("business-output-must-be-outside-evidence-directory")
    elif os.path.commonpath((Path(args.preview_out).absolute().resolve(), Path(args.evidence_dir).resolve())) == str(Path(args.evidence_dir).resolve()):
        raise consumer.ClientError("raw-preview-must-be-outside-evidence-directory")
    client = consumer.Client(consumer.private_json(args.connection, 4096))
    evidence.token = client.token
    client.capabilities()
    if args.command == "prepare":
        submission = consumer.private_json(args.submission, 32 << 10)
        task = client.create(submission, args.key)
        summary["taskId"] = consumer.preview_identity(task)
        summary.update({"result": "draft-preview-saved", "revision": task["revision"],
                        "previewDigest": task["previewDigest"], "approvalRequested": False,
                        "recovery": "replay-create-with-original-submission-and-same-key-and-new-preview-output"})
        evidence.append({"event": "draft-response-received", "taskId": summary["taskId"],
                         "revision": task["revision"], "previewDigest": task["previewDigest"]})
        # Preview is intentionally outside the sanitized evidence directory.
        consumer.save_preview(args.preview_out, task)
        return 0

    task_id = summary["taskId"]
    evidence.append({"event": "original-task-selected", "taskId": task_id,
                     "revision": preview["revision"], "previewDigest": preview["previewDigest"]})
    approved = client.approve(preview, args.confirm_preview_digest, args.key)
    if consumer.preview_identity(approved) != task_id:
        raise consumer.ClientError("approval-task-identity-mismatch")
    evidence.append({"event": "approval-response-received", "taskId": task_id})
    summary["approvalRequested"] = True
    deadline = time.monotonic() + args.timeout_seconds
    while time.monotonic() < deadline:
        # Each observe() uses three GETs. Fit their complete timeout allowance
        # into this remaining window; never replace/extend the server's budget.
        client.request_seconds = min(15, (deadline - time.monotonic()) / 3)
        if client.request_seconds <= 0:
            break
        for item in client.observe(task_id):
            item["processOverlapEvidence"] = "unavailable"
            evidence.append(item)
            if item["event"] == "observation-timeout":
                break
            status = item["status"]
            summary["lastObservedTaskStatus"] = status
            if status in {"blocked", "confirmation-expired", "cancelled"}:
                summary["result"] = "task-not-delivered"
                return 4
            if status == "completed":
                client.request_seconds = 15
                result = client.download(task_id, args.output_dir, run_oracle=True)
                evidence.append(result)
                if not result.get("deliveryComplete") or result.get("businessOracle") != "passed":
                    raise consumer.ClientError("business-consumption-not-proven")
                summary.update({"result": "delivery-consumed", "deliveryComplete": True,
                                "contentDigest": result["contentDigest"], "factDigest": result["factDigest"],
                                "businessOracle": "passed"})
                return 0
            # review-pending is an observation, never a request for this driver
            # to Verify/Decision/Accept. Only the resident application advances it.
        time.sleep(min(POLL_SECONDS, max(0, deadline - time.monotonic())))
    summary["result"] = "observation-window-ended"
    return 3


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--connection", required=True)
    commands = parser.add_subparsers(dest="command", required=True)
    prepare = commands.add_parser("prepare")
    prepare.add_argument("--submission", required=True)
    prepare.add_argument("--preview-out", required=True)
    complete = commands.add_parser("complete")
    complete.add_argument("--preview", required=True)
    complete.add_argument("--confirm-preview-digest", required=True)
    complete.add_argument("--output-dir", required=True)
    complete.add_argument("--timeout-seconds", type=observation_window, default=600)
    for command in (prepare, complete):
        command.add_argument("--key", required=True)
        command.add_argument("--evidence-dir", required=True)
    args = parser.parse_args(argv)
    started = time.monotonic()
    summary = {"stage": args.command, "result": "client-error", "deliveryComplete": False,
               "workerCancellationRequested": False, "processOverlapEvidence": "unavailable",
               "proofScope": "http-and-local-business-consumer-only", "productionReleaseProven": False}
    evidence = None
    try:
        evidence = Evidence(args.evidence_dir)
        result = run(args, evidence, summary)
    except consumer.ClientError as exc:
        summary.update({"result": "client-error", "code": str(exc), "deliveryComplete": False})
        result = 2
    except OSError:
        summary.update({"result": "client-error", "code": "local-io-unavailable", "deliveryComplete": False})
        result = 2
    except KeyboardInterrupt:
        summary.update({"result": "observation-interrupted", "deliveryComplete": False})
        result = 130
    summary["elapsedSeconds"] = round(time.monotonic() - started, 3)
    if evidence is not None:
        try:
            evidence.finish(summary)
        except consumer.ClientError as exc:
            summary.update({"result": "client-error", "code": str(exc), "deliveryComplete": False})
            result = 2
        finally:
            evidence.close()
        rendered = evidence.encoded(summary).decode("utf-8")
    else:
        rendered = json.dumps(summary) + "\n"
    print(rendered, end="", flush=True)
    return result


if __name__ == "__main__":
    sys.exit(main())
