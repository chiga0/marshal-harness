#!/usr/bin/env python3
"""B1 实时只读 collector；不调用 HTTP、Core、Worker 或进程控制命令。

operator 显式提供原受保护 ledger、runs-root、TaskID 和预建 0700 output-dir。
生产路径分别为 <stateRoot>/runtime-v1/result-ingress/result-ingress.jsonl 和
<stateRoot>/runs；读取从文件开头开始，最多 64 MiB ledger、每 Run 4 MiB events、
每文件 65536 行、120 秒。不修复部分追加，不轮转/截断源，不重启或取消 Task。
计划原事实决定两个 implement Run，再用原 start-outcome 引用选出九条原事实；
齐全立即复用独立 observer 的校验和 A1 B1 A2 B2 libproc 采样。

输出 task-worker-overlap.snapshot.json 保留原记录字符串，可能含 prompt/context，
并未脱敏；仅 task-worker-overlap.json 和 stdout 为脱敏观察。两文件均新建 0600。
源真实性依赖 operator 从原受保护状态核对；不是 Core authority 或抗同 UID 篡改
证明，也不声称 CPU 同时忙。合成 fixture/自身采样不构成真实 Provider 团队证据。
"""

import argparse
import contextlib
import importlib.util
import os
from pathlib import Path
import stat
import sys
import time


sys.dont_write_bytecode = True
SPEC = importlib.util.spec_from_file_location("worker_overlap", Path(__file__).with_name("task-worker-overlap.py"))
observer = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(observer)
require = observer.require
Unavailable = observer.Unavailable
MAX_LEDGER = 64 << 20
MAX_EVENTS = 4 << 20
MAX_LINES = 65536
BATCH = 1 << 20
MAX_SELECTED = 32
SNAPSHOT = "task-worker-overlap.snapshot.json"
REPORT = "task-worker-overlap.json"


def identity(info):
    return info.st_dev, info.st_ino, info.st_mode, info.st_uid, info.st_nlink


class Directory:
    def __init__(self, path):
        require(os.path.isabs(path) and os.path.realpath(path) == path, "source-path-not-absolute-or-symlink-free")
        self.path = path
        self.fd = os.open(path, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        try:
            self.info = os.fstat(self.fd)
            self.check()
        except BaseException:
            self.close()
            raise

    def check(self):
        held = os.fstat(self.fd)
        named = os.stat(self.path, follow_symlinks=False)
        # Directory link count changes normally when new Run directories appear.
        require((held.st_dev, held.st_ino) == (named.st_dev, named.st_ino) ==
                (self.info.st_dev, self.info.st_ino) and os.path.realpath(self.path) == self.path and
                stat.S_ISDIR(named.st_mode) and named.st_uid == os.geteuid() and
                stat.S_IMODE(named.st_mode) == 0o700, "source-directory-changed-or-not-private")

    def close(self):
        os.close(self.fd)


class LiveJSONL:
    """仅保留未完成的一行；允许同 inode 正常追加，拒绝替换/缩短/非私有文件。"""
    def __init__(self, path, limit):
        self.parent = Directory(os.path.dirname(path))
        try:
            self.fd = os.open(os.path.basename(path), os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK,
                              dir_fd=self.parent.fd)
        except BaseException:
            self.parent.close()
            raise
        self.leaf, self.limit = os.path.basename(path), limit
        self.info = os.fstat(self.fd)
        self.offset = self.sequence = self.high_size = 0
        self.tail = b""
        try:
            self.check()
        except BaseException:
            self.close()
            raise

    def check(self):
        self.parent.check()
        held = os.fstat(self.fd)
        named = os.stat(self.leaf, dir_fd=self.parent.fd, follow_symlinks=False)
        require(identity(held) == identity(named) == identity(self.info) and stat.S_ISREG(held.st_mode) and
                held.st_uid == os.geteuid() and held.st_nlink == 1 and stat.S_IMODE(held.st_mode) == 0o600,
                "source-file-changed-or-not-private")
        require(held.st_size >= self.high_size, "source-file-shrank")
        require(held.st_size <= self.limit, "source-history-limit")
        self.high_size = held.st_size
        return held.st_size

    def read(self):
        size = self.check()
        data = os.pread(self.fd, min(BATCH, size - self.offset), self.offset)
        require(len(data) == min(BATCH, size - self.offset), "source-short-read")
        self.offset += len(data)
        self.check()
        parts = (self.tail + data).split(b"\n")
        self.tail = parts.pop()
        require(len(self.tail) <= observer.MAX_RECORD, "record-too-large")
        result = []
        for raw in parts:
            require(len(raw) <= observer.MAX_RECORD, "record-too-large")
            value, original = observer.record(raw.decode("utf-8"))
            require(type(value.get("sequence")) is int and value["sequence"] == self.sequence + 1,
                    "source-sequence-conflict")
            self.sequence += 1
            require(self.sequence <= MAX_LINES, "source-history-limit")
            result.append((value, original))
        return result, self.offset < self.high_size

    def close(self):
        os.close(self.fd)
        self.parent.close()


class Selection:
    def __init__(self, task_id):
        self.task_id = task_id
        self.plan = None
        self.runs, self.creations, self.facts, self.attempts, self.events = {}, {}, {}, set(), {}

    def fact(self, value, raw):
        kind = value.get("factType")
        if kind == "team-plan-accepted" and value.get("plan", {}).get("inputs", {}).get("spec", {}).get("goalId") == self.task_id:
            observer.record(raw, True)
            require(self.plan is None, "ambiguous-original-plan")
            nodes = value["plan"]["inputs"]["nodes"]
            require(len(nodes) == 3 and sorted(n["role"] for n in nodes) == ["implement", "implement", "integrate"],
                    "unsupported-team")
            node_ids = {n["nodeId"] for n in nodes if n["role"] == "implement"}
            mats = value["plan"]["materializations"]
            selected = [m for m in mats if m["nodeId"] in node_ids]
            require(len(selected) == 2 and len({m["nodeId"] for m in selected}) == 2, "unsupported-team")
            require(all(isinstance(m["runId"], str) and observer.ID.fullmatch(m["runId"]) for m in selected),
                    "invalid-derived-run-id")
            self.runs = {m["runId"]: m["nodeId"] for m in selected}
            require(len(self.runs) == 2, "duplicate-derived-run")
            self.plan = (value, raw)
        elif self.plan and kind == "team-run-inputs-frozen" and value.get("creation", {}).get("goalId") == self.task_id:
            creation = value["creation"]
            if creation.get("runId") in self.runs:
                require(creation["runId"] not in self.creations, "ambiguous-original-creation")
                observer.record(raw, True)
                self.creations[creation["runId"]] = (value, raw)
        elif self.plan and kind == "process-started" and value.get("transition", {}).get("identity", {}).get("runId") in self.runs:
            self.keep(value, raw)
            self.attempts.add(value["attemptKey"])
        elif kind in ("process-supervisor-command-intent", "process-supervisor-command-outcome") and value.get("attemptKey") in self.attempts:
            if value.get("intent", value.get("outcome", {})).get("command") == "resume":
                self.keep(value, raw)

    def keep(self, value, raw):
        observer.record(raw, True)
        require(value["digest"] not in self.facts and len(self.facts) < MAX_SELECTED, "selected-fact-limit-or-conflict")
        self.facts[value["digest"]] = (value, raw)

    def event(self, run, value, raw):
        require(value.get("runId") == run, "cross-run-event")
        if value.get("type") == "run.start-outcome":
            require(run not in self.events, "ambiguous-original-start-event")
            self.events[run] = (value, raw)

    def snapshot(self):
        if self.plan is None or len(self.creations) != 2 or len(self.events) != 2:
            return None
        facts = [self.plan[1]]
        events = []
        for run in sorted(self.runs):
            event, raw = self.events[run]
            payload = event["payload"]
            started = self.facts.get(observer.digest_field(payload["processStartedFactDigest"]))
            resumed = self.facts.get(observer.digest_field(payload["resumeOutcomeFactDigest"]))
            if started is None or resumed is None:
                return None
            intent = self.facts.get(observer.digest_field(resumed[0]["previousRecoveryFactDigest"]))
            if intent is None:
                return None
            facts.extend((self.creations[run][1], started[1], intent[1], resumed[1]))
            events.append(raw)
        return {"schemaVersion": observer.SCHEMA, "facts": facts, "events": events}


def collect(ledger_path, runs_root, task_id, timeout, clock=time.monotonic, pause=time.sleep):
    require(isinstance(task_id, str) and observer.ID.fullmatch(task_id), "invalid-task-id")
    require(0.05 <= timeout <= 120, "invalid-collection-timeout")
    deadline = clock() + timeout
    selection = Selection(task_id)
    with contextlib.ExitStack() as stack:
        ledger = LiveJSONL(ledger_path, MAX_LEDGER)
        stack.callback(ledger.close)
        root = Directory(runs_root)
        stack.callback(root.close)
        readers = {}
        while clock() < deadline:
            root.check()
            records, backlog = ledger.read()
            for value, raw in records:
                selection.fact(value, raw)
            for run in selection.runs:
                if run not in readers:
                    try:
                        reader = LiveJSONL(os.path.join(runs_root, run, "events.jsonl"), MAX_EVENTS)
                    except FileNotFoundError:
                        continue
                    readers[run] = reader
                    stack.callback(reader.close)
                records, more = readers[run].read()
                backlog = backlog or more
                for value, raw in records:
                    selection.event(run, value, raw)
            # Finish the currently available prefix before selecting: do not hide a duplicate
            # plan/start event merely because it falls in the next bounded read batch.
            if not backlog:
                snapshot = selection.snapshot()
                if snapshot is not None:
                    workers, digest = observer.extract_workers(snapshot, task_id)
                    return snapshot, workers, digest
                pause(min(0.05, max(0, deadline - clock())))
        raise Unavailable("collection-timeout-partial-append" if ledger.tail or any(r.tail for r in readers.values())
                          else "collection-timeout-incomplete-evidence")


def write_snapshot(path, snapshot):
    raw = observer.compact(snapshot) + b"\n"
    require(len(raw) <= observer.MAX_INPUT, "snapshot-too-large")
    parent, leaf = observer.private_parent(path)
    try:
        fd = os.open(leaf, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600, dir_fd=parent)
        with os.fdopen(fd, "wb") as stream:
            stream.write(raw)
            stream.flush()
            os.fsync(stream.fileno())
        os.fsync(parent)
    finally:
        os.close(parent)
    return observer.sha(raw)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    for name in ("ledger", "runs-root", "task-id", "output-dir"):
        parser.add_argument("--" + name, required=True)
    parser.add_argument("--timeout-seconds", type=float, default=60)
    args = parser.parse_args(argv)
    report = {"schemaVersion": "task-worker-overlap-observation/v1", "proofScope": observer.PROOF_SCOPE,
              "status": "unavailable", "processOverlapProven": False}
    output_ready = False
    snapshot = None
    try:
        with contextlib.closing(Directory(args.output_dir)):
            for source in (os.path.dirname(args.ledger), args.runs_root):
                require(os.path.commonpath((args.output_dir, os.path.abspath(source))) != os.path.abspath(source),
                        "output-overlaps-source")
            require(not any(os.path.lexists(os.path.join(args.output_dir, name)) for name in (SNAPSHOT, REPORT)),
                    "output-already-exists")
        output_ready = True
        probe = observer.DarwinProbe()
        snapshot, workers, digest = collect(args.ledger, args.runs_root, args.task_id, args.timeout_seconds)
        report.update(taskId=args.task_id, planFactDigest=digest, observer=probe.description,
                      workers=[{k: v for k, v in w.items() if not k.startswith("_")} for w in workers])
        # Sample before snapshot fsync to avoid spending the short overlap window on output.
        report.update(observer.overlap(workers, probe, window=2.0))
        report.update(status="proven", processOverlapProven=True)
    except Unavailable as error:
        report["reason"] = str(error)
    except (OSError, ValueError, KeyError, TypeError, IndexError, AttributeError, RecursionError, OverflowError):
        report["reason"] = "unsupported-or-unavailable-evidence"
    try:
        if snapshot is not None:
            report["sourceSnapshotDigest"] = write_snapshot(os.path.join(args.output_dir, SNAPSHOT), snapshot)
        if output_ready:
            observer.write_report(os.path.join(args.output_dir, REPORT), report)
    except (Unavailable, OSError):
        report = {"status": "unavailable", "processOverlapProven": False, "reason": "evidence-write-unavailable",
                  "proofScope": observer.PROOF_SCOPE}
    print(observer.compact(report).decode())
    return 0 if report["processOverlapProven"] else 2


if __name__ == "__main__":
    raise SystemExit(main())
