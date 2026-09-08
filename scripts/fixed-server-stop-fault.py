#!/usr/bin/env python3
"""Read-only stop-window observer; never signal processes or mutate authority."""

import argparse
import datetime
import hashlib
import json
import os
from pathlib import Path
import re
import stat
import sys
import time

LIMIT = 16 << 20


class FaultError(ValueError):
    pass


def canonical(value):
    return json.dumps(value, sort_keys=True, ensure_ascii=False, separators=(",", ":")).encode()


def digest(raw):
    return "sha256:" + hashlib.sha256(raw).hexdigest()


def read_bounded(root, parts):
    """Hold every directory edge, reject linked/special inputs, bound memory."""
    fd = os.open(root, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        for part in parts[:-1]:
            child = os.open(part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=fd)
            os.close(fd)
            fd = child
        leaf = os.open(parts[-1], os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=fd)
        try:
            before = os.fstat(leaf)
            if (not stat.S_ISREG(before.st_mode) or before.st_nlink != 1
                    or before.st_uid != os.geteuid() or before.st_mode & 0o022
                    or before.st_size > LIMIT):
                raise FaultError("input-boundary")
            raw = bytearray()
            while len(raw) <= LIMIT:
                chunk = os.read(leaf, min(65536, LIMIT + 1 - len(raw)))
                if not chunk:
                    break
                raw.extend(chunk)
            after = os.fstat(leaf)
            named = os.stat(parts[-1], dir_fd=fd, follow_symlinks=False)
            # Concurrent append is expected; replacement/type/permissions are not.
            if ((named.st_dev, named.st_ino) != (after.st_dev, after.st_ino)
                    or len(raw) > LIMIT or (before.st_dev, before.st_ino, before.st_mode,
                                    before.st_uid, before.st_nlink) !=
                    (after.st_dev, after.st_ino, after.st_mode, after.st_uid, after.st_nlink)):
                raise FaultError("input-drift")
            return bytes(raw)
        finally:
            os.close(leaf)
    finally:
        os.close(fd)


def records(raw, sealed=False):
    lines = raw.split(b"\n")
    values = []
    for line in lines[:-1]:
        value = json.loads(line)
        if not isinstance(value, dict) or canonical(value) != line:
            raise FaultError("noncanonical-record")
        if sealed:
            detached = dict(value)
            claimed = detached.get("digest")
            detached["digest"] = ""
            if digest(canonical(detached)) != claimed:
                raise FaultError("fact-digest")
        values.append(value)
    return values, bool(lines[-1])


def observe(root, run_id):
    journal = read_bounded(root, (".marshal", "runs", run_id, "events.jsonl"))
    ingress = read_bounded(root, (".marshal", "runtime-v1", "result-ingress", "result-ingress.jsonl"))
    events, event_tail = records(journal)
    facts, ingress_tail = records(ingress, sealed=True)
    if not events or any(e.get("runId") != run_id or e.get("sequence") != n
                         for n, e in enumerate(events, 1)):
        raise FaultError("run-journal-subject")
    if events[-1].get("stateTo") != "RUNNING" or any(e.get("type") == "worker.stopped" for e in events):
        raise FaultError("stop-window-missed")
    barriers = [f for f in facts if f.get("factType") == "terminalization-barrier"
                and f.get("transition", {}).get("identity", {}).get("runId") == run_id]
    if not barriers:
        return None
    if len(barriers) != 1:
        raise FaultError("ambiguous-stop")
    barrier = barriers[0]
    intent = barrier["transition"].get("stopIntent", {})
    if intent.get("category") not in ("attempt-deadline-exceeded", "run-deadline-exceeded"):
        raise FaultError("not-business-timeout")
    if (barrier["transition"]["identity"].get("attemptId") != events[-1].get("attemptId")
            or not isinstance(events[-1].get("attemptId"), str)
            or intent.get("expectedSequence") != events[-1]["sequence"]
            or any(not isinstance(intent.get(k), str) or not re.fullmatch(r"sha256:[0-9a-f]{64}", intent[k])
                   for k in ("intentDigest", "expectedAuthorityHead"))):
        raise FaultError("stop-subject")
    attempt = barrier.get("attemptKey")
    own = [f for f in facts if f.get("attemptKey") == attempt]
    if not isinstance(attempt, str) or not attempt or not own:
        raise FaultError("attempt-subject")
    return {"schemaVersion": "marshal.stop-fault-observation.v1", "runId": run_id,
            "window": "stop-intent-before-run-terminal", "attemptKey": attempt,
            "barrierFactDigest": barrier["digest"], "stopIntentDigest": intent.get("intentDigest"),
            "lastAttemptFactType": own[-1]["factType"], "lastFactSequence": facts[-1]["sequence"],
            "runSequence": events[-1]["sequence"], "journalSHA256": digest(journal),
            "ingressSHA256": digest(ingress), "journalPartialTail": event_tail,
            "ingressPartialTail": ingress_tail}


def await_window(probe, timeout, now=time.monotonic, pause=time.sleep):
    end = now() + timeout
    while now() < end:
        observation = probe()
        if observation is not None:
            return observation
        pause(min(0.02, max(0, end - now())))
    raise FaultError("stop-window-timeout")


def verify_interrupted(before, after):
    if after is None or any(before.get(k) != after.get(k) for k in
                            ("schemaVersion", "window", "runId", "attemptKey",
                             "barrierFactDigest", "stopIntentDigest", "runSequence")):
        raise FaultError("stop-window-drift")
    if after["lastFactSequence"] < before["lastFactSequence"]:
        raise FaultError("ledger-regressed")
    return after


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("mode", choices=("wait", "after"))
    parser.add_argument("--repository", required=True)
    parser.add_argument("--run", required=True)
    args = parser.parse_args()
    root = Path(args.repository)
    if not root.is_absolute() or root.resolve() != root or not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._:-]{2,120}", args.run):
        raise FaultError("subject")
    evidence_parts = (".marshal", "fixed-server-t1-canary", args.run)
    if args.mode == "wait":
        value = await_window(lambda: observe(root, args.run), 180)
    else:
        before = json.loads(read_bounded(root, evidence_parts + ("stop-crash-wait.json",)))
        value = verify_interrupted(before, observe(root, args.run))
    value["observedAt"] = datetime.datetime.now(datetime.timezone.utc).isoformat()
    # Output is only this canary's pre-created evidence directory; never Run/RB1.
    directory = root.joinpath(*evidence_parts)
    if directory.resolve() != directory:
        raise FaultError("evidence-boundary")
    fd = os.open(directory, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        output = os.open(f"stop-crash-{args.mode}.json", os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600, dir_fd=fd)
        with os.fdopen(output, "wb") as handle:
            handle.write(canonical(value) + b"\n")
    finally:
        os.close(fd)


if __name__ == "__main__":
    try:
        main()
    except (FaultError, OSError, ValueError, TypeError, KeyError):
        sys.exit("stop-fault-observer: FAIL (window not proved; no automatic retry)")
