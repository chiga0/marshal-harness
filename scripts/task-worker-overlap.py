#!/usr/bin/env python3
"""B1 独立只读重叠观测；不调用 HTTP、Worker、Attach 或账本写接口。

输入 --snapshot 是 operator 从原受保护状态提取的 0600 JSON（父目录 0700）：
{"schemaVersion":"task-worker-overlap-snapshot/v1", "facts":[原 JSON 行字符串...],
 "events":[两个原 run.start-outcome JSON 行字符串]}
facts 恰含一个 team-plan-accepted，以及每个 implement 节点的 creation、
process-started、resume intent、resume outcome，共九行。必须保留原 JSON 字节，
不能裁剪/重签事实；本工具不发现状态目录、不重放 Core。snapshot 可能含业务
prompt/context，并未脱敏；它的真实性由 operator 从原状态核对，不由自洽 hash
授予。输出仅含摘要和必要进程字段，不含原事实、路径、环境或 transcript。

仅支持本机已验证的 macOS 26 / arm64 LP64 libproc ABI；其他平台 fail closed。
--self-check 只检查采样器自身，永远不构成真实 Provider 团队证明。
"""

import argparse
import ctypes
import hashlib
import json
import os
import platform
import re
import stat
import time


SCHEMA = "task-worker-overlap-snapshot/v1"
PROOF_SCOPE = "operator-supplied-original-facts-and-local-process-lifecycle;not-core-authority;not-cpu-concurrency"
MAX_INPUT = 8 << 20
MAX_RECORD = 1 << 20
MAX_OUTPUT = 32 << 10
DIGEST = re.compile(r"sha256:[0-9a-f]{64}\Z")
ID = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]{0,255}\Z")
PROCESS_KEYS = ("pid", "birthSeconds", "birthMicroseconds", "sessionId", "processGroupId")


class Unavailable(Exception):
    """仅传固定 reason code，绝不回显输入。"""


def require(condition, reason="evidence-binding-conflict"):
    if not condition:
        raise Unavailable(reason)


def sha(raw):
    return "sha256:" + hashlib.sha256(raw).hexdigest()


def pairs(items):
    result = {}
    for key, value in items:
        require(key not in result, "duplicate-json-member")
        result[key] = value
    return result


def bad_number(_):
    raise Unavailable("unsupported-json-number")


DECODER = json.JSONDecoder(object_pairs_hook=pairs, parse_constant=bad_number)


def decode(raw):
    try:
        return DECODER.decode(raw)
    except (ValueError, RecursionError):
        raise Unavailable("invalid-json") from None


def member_span(raw, wanted):
    """定位原对象成员；只切原字节，不实现另一套 JCS 数值序列化。"""
    require(raw.startswith("{") and raw.endswith("}"), "unsupported-source-record")
    pos = 1
    while pos < len(raw) - 1:
        key, end = DECODER.raw_decode(raw, pos)
        require(isinstance(key, str) and raw[end:end + 1] == ":", "noncompact-source-record")
        start = end + 1
        _, end = DECODER.raw_decode(raw, start)
        if key == wanted:
            return start, end
        require(raw[end:end + 1] == ",", "missing-source-member")
        pos = end + 1
    raise Unavailable("missing-source-member")


def member(raw, key):
    start, end = member_span(raw, key)
    return raw[start:end]


def record(raw, fact=False):
    require(isinstance(raw, str) and len(raw.encode("utf-8")) <= MAX_RECORD, "record-too-large")
    raw = raw.removesuffix("\n")
    value = decode(raw)
    require(isinstance(value, dict), "unsupported-source-record")
    if fact:
        digest = value.get("digest", "")
        require(isinstance(digest, str) and DIGEST.fullmatch(digest), "invalid-fact-digest")
        start, end = member_span(raw, "digest")
        # RB1 appendLine seals the original canonical record with digest="".
        require(sha((raw[:start] + '""' + raw[end:]).encode()) == digest, "fact-digest-mismatch")
        require(type(value.get("sequence")) is int and value["sequence"] > 0, "invalid-fact-sequence")
    return value, raw


def digest_field(value):
    require(isinstance(value, str) and DIGEST.fullmatch(value), "invalid-reference-digest")
    return value


def compact(value):
    # Only observer output / integer-and-string identity keys use this helper.
    return json.dumps(value, ensure_ascii=False, separators=(",", ":"), sort_keys=True).encode()


def process_identity(value):
    require(isinstance(value, dict) and set(value) == set(PROCESS_KEYS), "unsupported-process-identity")
    require(all(type(value[k]) is int for k in PROCESS_KEYS), "invalid-process-identity")
    require(value["pid"] > 1 and value["birthSeconds"] > 0 and
            0 <= value["birthMicroseconds"] < 1_000_000 and value["sessionId"] > 0 and
            value["processGroupId"] == value["pid"], "invalid-process-identity")
    return dict(value)


def extract_workers(snapshot, task_id):
    """只校验所需原事实交叉绑定；不宣称验证完整 ledger/当前 Core authority。"""
    require(ID.fullmatch(task_id) and isinstance(snapshot, dict), "invalid-task-id")
    require(set(snapshot) == {"schemaVersion", "facts", "events"} and snapshot["schemaVersion"] == SCHEMA,
            "unsupported-snapshot")
    require(isinstance(snapshot["facts"], list) and len(snapshot["facts"]) == 9 and
            isinstance(snapshot["events"], list) and len(snapshot["events"]) == 2, "incomplete-snapshot")
    records = [record(raw, True) for raw in snapshot["facts"]]
    by_digest = {item[0]["digest"]: item for item in records}
    require(len(by_digest) == 9 and len({v["sequence"] for v, _ in records}) == 9, "duplicate-source-fact")
    plans = [(v, raw) for v, raw in records if v.get("factType") == "team-plan-accepted"]
    require(len(plans) == 1, "missing-original-plan")
    plan_fact, plan_raw = plans[0]
    require(plan_fact["protocolRevision"] == "bounded-team-plan/v1", "unsupported-plan")
    plan = plan_fact["plan"]
    inputs = plan["inputs"]
    require(inputs["schemaVersion"] == "bounded-team-inputs/v1" and
            inputs["spec"]["goalId"] == inputs["proposal"]["goalId"] == plan["revision"]["goalId"] == task_id)
    namespace = plan_fact["scope"]["authorityNamespaceId"]
    require(isinstance(namespace, dict) and set(namespace) == {"tenantNamespace", "controlPlaneId", "authorityScopeId"} and
            all(isinstance(v, str) and v.strip() for v in namespace.values()), "unsupported-authority-namespace")
    require(inputs["spec"]["authorityNamespaceId"] == inputs["proposal"]["authorityNamespaceId"] == namespace)
    require(plan["approval"]["inputsDigest"] == sha(member(member(plan_raw, "plan"), "inputs").encode()))
    digest_field(plan["approval"]["taskDraftDigest"])
    nodes = inputs["nodes"]
    require(len(nodes) == 3 and len({n["nodeId"] for n in nodes}) == 3 and
            sorted(n["role"] for n in nodes) == ["implement", "implement", "integrate"], "unsupported-team")
    implement = sorted(n["nodeId"] for n in nodes if n["role"] == "implement")
    materializations = plan["materializations"]
    require(len(materializations) == 3 and len({m["nodeId"] for m in materializations}) == 3)
    materializations = {m["nodeId"]: m for m in materializations}
    require(set(materializations) == {n["nodeId"] for n in nodes})
    events = [record(raw) for raw in snapshot["events"]]
    used = {plan_fact["digest"]}
    workers = []
    for node_id in implement:
        mat = materializations[node_id]
        require(ID.fullmatch(mat["runId"]) and ID.fullmatch(mat["taskId"]))
        creations = [(v, raw) for v, raw in records if v.get("factType") == "team-run-inputs-frozen" and
                     v.get("creation", {}).get("nodeId") == node_id]
        require(len(creations) == 1, "missing-original-creation")
        cf, cr = creations[0]
        creation = cf["creation"]
        require(cf["protocolRevision"] == "bounded-team-run-creation/v1" and cf["scope"] == plan_fact["scope"] and
                creation["goalId"] == task_id and creation["planFactDigest"] == plan_fact["digest"] and
                creation["runId"] == creation["inputs"]["runId"] == mat["runId"] and
                creation["inputs"]["task"]["metadata"]["id"] == mat["taskId"])
        require(creation["inputsDigest"] == sha(member(member(cr, "creation"), "inputs").encode()))
        capability = creation["inputs"]["capability"]
        require(capability["adapterId"] == "pi" and capability["probeStatus"] == "supported", "unsupported-provider-evidence")
        run_events = [(v, raw) for v, raw in events if v.get("runId") == mat["runId"]]
        require(len(run_events) == 1, "missing-original-start-event")
        event, event_raw = run_events[0]
        payload = event["payload"]
        require(event["type"] == "run.start-outcome" and event["stateFrom"] == "READY" and
                event["stateTo"] == "RUNNING" and payload["protocolRevision"] == "run-start-outcome/v2" and
                payload["taskId"] == mat["taskId"] and ID.fullmatch(event["attemptId"]))
        started, _ = by_digest[digest_field(payload["processStartedFactDigest"])]
        resume, _ = by_digest[digest_field(payload["resumeOutcomeFactDigest"])]
        intent_fact, _ = by_digest[digest_field(resume["previousRecoveryFactDigest"])]
        transition, outcome, intent = started["transition"], resume["outcome"], intent_fact["intent"]
        identity = transition["identity"]
        require(started["protocolRevision"] == "attempt-authority/v1" and started["factType"] ==
                transition["kind"] == "process-started")
        require(identity["runId"] == mat["runId"] and identity["taskId"] == mat["taskId"] and
                identity["attemptId"] == event["attemptId"] and identity["authorityNamespaceId"] == namespace)
        require(resume["factType"] == "process-supervisor-command-outcome" and
                intent_fact["factType"] == "process-supervisor-command-intent" and
                resume["protocolRevision"] == intent_fact["protocolRevision"] == "process-supervisor-command-recovery/v2")
        require(started["attemptKey"] == resume["attemptKey"] == intent_fact["attemptKey"] and
                resume["attemptAuthorityHead"] == intent_fact["attemptAuthorityHead"] == started["digest"] and
                resume["attemptRevision"] == intent_fact["attemptRevision"] == started["revision"])
        require(plan_fact["sequence"] < cf["sequence"] < started["sequence"] < intent_fact["sequence"] < resume["sequence"])
        spawn = transition["supervisorEvidence"]
        require(spawn["protocolRevision"] == outcome["protocolRevision"] == intent["protocolRevision"] == "process-supervisor/v2")
        require(spawn["command"] == "spawn" and spawn["disposition"] == "ok" and
                spawn["outcome"]["state"] == spawn["outcome"]["mechanicsState"] == "exec-stopped")
        require(outcome["command"] == "resume" and outcome["disposition"] == "ok" and
                outcome["reasonCode"] == "process-resumed" and outcome["outcome"]["state"] ==
                outcome["outcome"]["mechanicsState"] == "running", "missing-successful-resume")
        for key in ("sessionId", "command", "commandId", "sequence", "previousCommandHead", "currentAuthorityHead", "requestDigest"):
            require(outcome[key] == intent[key])
        require(outcome["sessionId"] == spawn["sessionId"] and outcome["currentAuthorityHead"] == started["digest"] and
                intent["rebuild"]["processStartedFactDigest"] == started["digest"] and
                outcome["v2Preparation"]["projection"]["processStartedFactDigest"] == started["digest"])
        for key in ("process", "runtimeObjectDigest", "workingObjectDigest", "sourceGateRevision", "exactSetDigest"):
            require(spawn["outcome"][key] == outcome["outcome"][key])
        child = process_identity(outcome["outcome"]["process"])
        require(outcome["outcome"]["sourceGateRevision"] == "darwin-source-gate/v1" and
                outcome["outcome"]["observerIdentity"] == "darwin-fixed-process-supervisor/v2")
        for key in ("runtimeObjectDigest", "workingObjectDigest", "exactSetDigest"):
            digest_field(outcome["outcome"][key])
        observed = transition["process"]
        require(all(observed[k] == child[k] for k in ("pid", "birthSeconds", "birthMicroseconds")) and
                observed["pgid"] == child["processGroupId"])
        require(os.path.isabs(observed["executablePath"]), "missing-runtime-identity")
        digest_field(observed["executableSha256"])
        used.update((cf["digest"], started["digest"], intent_fact["digest"], resume["digest"]))
        workers.append({"nodeId": node_id, "runId": mat["runId"], "attemptId": event["attemptId"], "provider": capability["adapterId"],
                        "process": child, "creationDigest": cf["digest"], "startEventDigest": sha(event_raw.encode()),
                        "processStartedFactDigest": started["digest"], "resumeOutcomeFactDigest": resume["digest"],
                        "runtimeObjectDigest": outcome["outcome"]["runtimeObjectDigest"],
                        "executableDigest": observed["executableSha256"], "_executablePath": observed["executablePath"]})
    require(used == set(by_digest) and len({w["runId"] for w in workers}) == 2 and
            len({w["attemptId"] for w in workers}) == 2 and len({w["process"]["pid"] for w in workers}) == 2,
            "duplicate-or-unrelated-worker")
    return workers, plan_fact["digest"]


class ProcBSDInfo(ctypes.Structure):
    # Public SDK proc_info.h, not Darwin's private/version-sensitive kinfo_proc.
    _fields_ = [(n, ctypes.c_uint32) for n in ("flags", "status", "xstatus", "pid", "ppid", "uid", "gid",
                "ruid", "rgid", "svuid", "svgid", "rfu")] + [("comm", ctypes.c_char * 16), ("name", ctypes.c_char * 32)] + [
                (n, ctypes.c_uint32) for n in ("nfiles", "pgid", "pjobc", "tdev", "tpgid")] + [
                ("nice", ctypes.c_int32), ("birthSec", ctypes.c_uint64), ("birthUsec", ctypes.c_uint64)]


class DarwinProbe:
    def __init__(self):
        require(platform.system() == "Darwin" and platform.machine() == "arm64" and
                platform.mac_ver()[0].split(".")[0] == "26", "unsupported-observer-platform")
        require(ctypes.sizeof(ctypes.c_void_p) == 8 and ctypes.sizeof(ProcBSDInfo) == 136 and
                (ProcBSDInfo.birthSec.offset, ProcBSDInfo.birthUsec.offset) == (120, 128), "unsupported-libproc-layout")
        self.lib = ctypes.CDLL("/usr/lib/libproc.dylib", use_errno=True)
        self.lib.proc_pidinfo.argtypes = [ctypes.c_int, ctypes.c_int, ctypes.c_uint64, ctypes.c_void_p, ctypes.c_int]
        self.lib.proc_pidinfo.restype = ctypes.c_int
        self.lib.proc_pidpath.argtypes = [ctypes.c_int, ctypes.c_void_p, ctypes.c_uint32]
        self.lib.proc_pidpath.restype = ctypes.c_int
        own = self.read(os.getpid())
        require(own["process"]["pid"] == os.getpid() and own["uid"] == os.geteuid() and
                own["process"]["processGroupId"] == os.getpgid(0) and own["process"]["sessionId"] == os.getsid(0) and
                own["process"]["birthSeconds"] > 0 and 0 <= own["process"]["birthMicroseconds"] < 1_000_000,
                "libproc-self-check-failed")
        self.description = {"kind": "python-ctypes-system-libproc", "system": platform.mac_ver()[0],
                            "machine": "arm64", "structBytes": 136, "selfCheck": "passed;not-provider-team-proof"}

    def read(self, pid):
        before = time.monotonic_ns()
        info = ProcBSDInfo()
        count = self.lib.proc_pidinfo(pid, 3, 0, ctypes.byref(info), ctypes.sizeof(info))
        require(count == 136 and info.pid == pid, "process-observation-unavailable")
        path = ctypes.create_string_buffer(4096)
        require(0 < self.lib.proc_pidpath(pid, path, len(path)) < len(path), "runtime-path-unavailable")
        sid = os.getsid(pid)
        after = time.monotonic_ns()
        return {"process": {"pid": info.pid, "birthSeconds": info.birthSec, "birthMicroseconds": info.birthUsec,
                            "processGroupId": info.pgid, "sessionId": sid}, "uid": info.uid,
                "status": info.status, "flags": info.flags, "beforeNs": before, "afterNs": after,
                "_executablePath": os.fsdecode(path.value)}

    def __call__(self, worker):
        return self.read(worker["process"]["pid"])


def overlap(workers, probe, window=2.0, clock=time.monotonic, pause=time.sleep):
    require(type(window) in (int, float) and 0.05 <= window <= 10, "invalid-observation-window")
    deadline = clock() + window
    for attempt in range(1, 101):
        samples = []
        for index in (0, 1, 0, 1):
            require(clock() < deadline, "observation-window-expired")
            worker = workers[index]
            sample = probe(worker)
            require(sample["process"] == worker["process"] and sample["uid"] == os.geteuid() and
                    sample["_executablePath"] == worker["_executablePath"], "process-identity-mismatch")
            require(sample["status"] in (2, 3) and not sample["flags"] & 4, "process-not-live")
            require(type(sample["beforeNs"]) is int and type(sample["afterNs"]) is int and
                    0 <= sample["beforeNs"] <= sample["afterNs"] and
                    (not samples or samples[-1]["afterNs"] <= sample["beforeNs"]), "invalid-monotonic-observation")
            samples.append({k: v for k, v in sample.items() if not k.startswith("_")})
            if len(samples) == 2:
                pause(min(0.02, max(0, deadline - clock())))
        start = max(samples[0]["afterNs"], samples[1]["afterNs"])
        end = min(samples[2]["beforeNs"], samples[3]["beforeNs"])
        if end > start:
            return {"samples": samples, "sampleOrder": [workers[i]["nodeId"] for i in (0, 1, 0, 1)],
                    "intersectionStartNs": start, "intersectionEndNs": end, "overlapLowerBoundNs": end - start,
                    "samplingAttempts": attempt}
    raise Unavailable("no-positive-live-intersection")


def private_parent(path):
    path = os.path.abspath(path)
    require(os.path.realpath(path) == path, "symlink-evidence-path")
    parent, leaf = os.path.split(path)
    fd = os.open(parent, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    info = os.fstat(fd)
    if info.st_uid != os.geteuid() or stat.S_IMODE(info.st_mode) & 0o077:
        os.close(fd)
        raise Unavailable("evidence-parent-not-private")
    return fd, leaf


def read_snapshot(path):
    parent, leaf = private_parent(path)
    try:
        fd = os.open(leaf, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=parent)
        with os.fdopen(fd, "rb") as stream:
            before = os.fstat(stream.fileno())
            require(stat.S_ISREG(before.st_mode) and before.st_uid == os.geteuid() and before.st_nlink == 1 and
                    stat.S_IMODE(before.st_mode) in (0o400, 0o600), "snapshot-not-private-regular-file")
            require(0 < before.st_size <= MAX_INPUT, "snapshot-too-large")
            raw = stream.read(MAX_INPUT + 1)
            after = os.fstat(stream.fileno())
            fields = ("st_dev", "st_ino", "st_uid", "st_mode", "st_nlink", "st_size", "st_mtime_ns", "st_ctime_ns")
            require(all(getattr(before, key) == getattr(after, key) for key in fields) and len(raw) == before.st_size,
                    "snapshot-changed")
        return decode(raw.decode("utf-8")), sha(raw)
    finally:
        os.close(parent)


def write_report(path, report):
    raw = compact(report) + b"\n"
    require(len(raw) <= MAX_OUTPUT, "report-too-large")
    parent, leaf = private_parent(path)
    try:
        fd = os.open(leaf, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600, dir_fd=parent)
        with os.fdopen(fd, "wb") as stream:
            stream.write(raw)
            stream.flush()
            os.fsync(stream.fileno())
        os.fsync(parent)
    finally:
        os.close(parent)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--task-id")
    parser.add_argument("--snapshot")
    parser.add_argument("--output")
    parser.add_argument("--window-seconds", type=float, default=2.0)
    parser.add_argument("--self-check", action="store_true")
    args = parser.parse_args(argv)
    report = {"schemaVersion": "task-worker-overlap-observation/v1", "proofScope": PROOF_SCOPE,
              "processOverlapProven": False, "status": "unavailable"}
    try:
        if args.self_check:
            require(not any((args.task_id, args.snapshot, args.output)), "mixed-self-check-and-team-input")
            report.update(status="self-check-only", observer=DarwinProbe().description)
        else:
            require(all((args.task_id, args.snapshot, args.output)), "missing-required-argument")
            snapshot, source_digest = read_snapshot(args.snapshot)
            workers, plan_digest = extract_workers(snapshot, args.task_id)
            observer = DarwinProbe()
            report.update(taskId=args.task_id, sourceSnapshotDigest=source_digest, planFactDigest=plan_digest,
                          observer=observer.description, workers=[{k: v for k, v in w.items() if not k.startswith("_")} for w in workers])
            report.update(overlap(workers, observer, args.window_seconds))
            report.update(status="proven", processOverlapProven=True)
    except Unavailable as error:
        report["reason"] = str(error)
    except (OSError, ValueError, KeyError, TypeError, IndexError, AttributeError, RecursionError, OverflowError):
        report["reason"] = "unsupported-or-unavailable-evidence"
    if args.output:
        try:
            write_report(args.output, report)
        except (Unavailable, OSError):
            # No stdout success if evidence could not be durably preserved.
            report = {"status": "unavailable", "processOverlapProven": False, "reason": "report-write-unavailable", "proofScope": PROOF_SCOPE}
    print(compact(report).decode())
    return 0 if report["status"] in ("proven", "self-check-only") else 2


if __name__ == "__main__":
    raise SystemExit(main())
