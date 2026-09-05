#!/usr/bin/env python3
"""Drive an already approved/running T2 Run through the fixed server to review."""

import argparse
import datetime
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import time


DIGEST = re.compile(r"^sha256:[0-9a-f]{64}$")
ID = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._:-]{2,120}$")
LIVE_PENDING = {"disposition": "pending", "reasonCode": "attempt-still-running"}


class DriveError(Exception):
    pass


def run_projection(value, run_id, state, prior=None, advance=False):
    if not isinstance(value, dict) or value.get("runId") != run_id or value.get("state") != state:
        raise DriveError("unexpected-run-projection")
    if not ID.fullmatch(value.get("attemptId", "")) or not DIGEST.fullmatch(value.get("authorityHead", "")):
        raise DriveError("invalid-run-identity")
    if type(value.get("sequence")) is not int or value["sequence"] <= 0:
        raise DriveError("invalid-run-sequence")
    if prior is not None:
        if value["attemptId"] != prior["attemptId"] or value["sequence"] != prior["sequence"] + int(advance):
            raise DriveError("unexpected-run-successor")
        if (value["authorityHead"] != prior["authorityHead"]) != advance:
            raise DriveError("unexpected-run-head")
    return value


def drive(call, save, run_id, deadline, now=time.time, pause=time.sleep):
    """Only positive running observations authorize bounded identical polling.

    Neither generic pending, timeout nor a failed process is classified as a
    retryable result. The driver is a client, never a new business state store.
    """
    # The fixed CLI requires Go's canonical RFC3339Nano form: fractional
    # trailing zeroes are forbidden. Python isoformat alone retains them and
    # would intermittently reject an otherwise identical valid deadline.
    deadline_text = datetime.datetime.fromtimestamp(deadline, datetime.timezone.utc).replace(tzinfo=None).isoformat(timespec="microseconds").rstrip("0").rstrip(".") + "Z"

    def invoke(args):
        remaining = deadline - now()
        if remaining <= 0:
            raise DriveError("driver-deadline-exceeded")
        return call(args, remaining)

    code, current = invoke(["inspect", "--run", run_id])
    if code != 0:
        raise DriveError("inspect-unavailable")
    current = run_projection(current, run_id, "RUNNING")
    save("initial-run.json", current)
    summary = {"runId": run_id, "attemptId": current["attemptId"], "stage": "collect", "runningObservations": 0,
               "accepted": False, "transport": "fixed-control-plane", "startedAt": now()}
    for operation, target, advance in (("collect", "VERIFYING", True), ("verify", "REVIEW_PENDING", True), ("review-packet", "REVIEW_PENDING", False)):
        summary["stage"] = operation
        request = [operation, "--run", run_id, "--attempt", current["attemptId"],
                   "--expected-sequence", str(current["sequence"]), "--expected-authority-head", current["authorityHead"],
                   "--request-key", f"t2:{run_id}:{operation}:{current['sequence']}", "--deadline", deadline_text]
        save(f"{operation}-request.json", {"args": request})
        polls = 0
        while True:
            code, value = invoke(request)
            save(f"{operation}-response-{polls}.json", {"exitCode": code, "response": value})
            if code == 0:
                break
            if operation != "collect" or code != 3 or value != LIVE_PENDING:
                raise DriveError(f"{operation}-unresolved-no-automatic-retry")
            summary["runningObservations"] += 1
            polls += 1
            # The deadline/key/head never change between observations. There is
            # no successor Run, new Attempt or new request after a failure.
            remaining = deadline - now()
            if remaining <= 0:
                raise DriveError("driver-deadline-exceeded")
            pause(min(5, remaining))
        if not isinstance(value, dict) or not isinstance(value.get("Projection"), dict) or not isinstance(value.get("Receipt"), dict):
            raise DriveError("missing-verified-cli-result")
        projection, receipt = value["Projection"], value["Receipt"]
        next_run = run_projection(projection.get("run"), run_id, target, current, advance)
        if receipt.get("runId") != run_id or receipt.get("attemptId") != current["attemptId"] or receipt.get("postRevision") != next_run["sequence"] or receipt.get("postAuthorityHead") != next_run["authorityHead"]:
            raise DriveError("receipt-projection-mismatch")
        if operation == "verify":
            summary["verificationStatus"] = projection.get("status")
        if operation == "review-packet":
            if not DIGEST.fullmatch(projection.get("packetDigest", "")) or not isinstance(projection.get("packet"), dict):
                raise DriveError("invalid-review-packet")
            summary["packetDigest"] = projection["packetDigest"]
        save(f"{operation}.json", value)
        current = next_run
    code, inspected = invoke(["inspect", "--run", run_id])
    if code != 0 or run_projection(inspected, run_id, "REVIEW_PENDING", current) != current:
        raise DriveError("final-inspection-mismatch")
    summary.update(stage="review-pending", finishedAt=now(), run=current)
    save("review-summary.json", summary)
    if summary.get("verificationStatus") != "pass":
        raise DriveError("business-verification-failed")
    return summary


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--run", required=True)
    parser.add_argument("--evidence-dir", required=True)
    parser.add_argument("--timeout-seconds", type=int, default=480)
    args = parser.parse_args()
    root = Path(__file__).resolve().parent.parent
    binary = root / "bin" / "marshal"
    evidence = Path(args.evidence_dir)
    if not ID.fullmatch(args.run) or not 1 <= args.timeout_seconds <= 480:
        parser.error("invalid run/deadline")
    if binary.is_symlink() or not binary.is_file() or binary.resolve() != binary:
        parser.error("fixed bin/marshal is required")
    if not evidence.is_absolute() or evidence.resolve() != evidence or evidence.parent != root / ".marshal" / "fixed-server-t1-canary" / args.run:
        parser.error("evidence-dir must be the fresh t2 child of this Run's canary evidence")
    if evidence.name != "t2":
        parser.error("evidence leaf must be t2")
    evidence.mkdir(mode=0o700, exist_ok=False)

    def save(name, value):
        with (evidence / name).open("x", encoding="utf-8") as handle:
            json.dump(value, handle, ensure_ascii=False, sort_keys=True, separators=(",", ":"))
            handle.write("\n")

    binary_digest = hashlib.sha256(binary.read_bytes()).hexdigest()
    save("driver-subject.json", {"runId": args.run, "binarySHA256": binary_digest, "timeoutSeconds": args.timeout_seconds})
    invocation = 0

    def call(command, remaining):
        nonlocal invocation
        invocation += 1
        try:
            completed = subprocess.run([str(binary), "control-plane"] + command, stdin=subprocess.DEVNULL,
                                       stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=remaining, check=False)
        except subprocess.TimeoutExpired as exc:
            # subprocess.run only terminates/reaps its own fixed CLI child;
            # it does not kill the server, Worker or a process group.
            raise DriveError("fixed-cli-response-timeout") from exc
        # The authenticated fixed client already bounds its response frame.
        # Never echo a response/diagnostic into CI logs.
        if len(completed.stdout) > 2 << 20 or len(completed.stderr) > 64 << 10:
            raise DriveError("fixed-cli-output-limit")
        save(f"call-{invocation}.json", {"operation": command[0], "exitCode": completed.returncode,
                                       "stdoutSHA256": hashlib.sha256(completed.stdout).hexdigest(),
                                       "stderrSHA256": hashlib.sha256(completed.stderr).hexdigest()})
        try:
            value = json.loads(completed.stdout)
        except (ValueError, UnicodeDecodeError):
            raise DriveError("fixed-cli-invalid-response") from None
        return completed.returncode, value

    try:
        drive(call, save, args.run, time.time() + args.timeout_seconds)
        if hashlib.sha256(binary.read_bytes()).hexdigest() != binary_digest:
            raise DriveError("fixed-binary-drift")
    except DriveError as exc:
        save("driver-failure.json", {"reasonCode": str(exc), "accepted": False})
        print(f"fixed-server-t2: {exc}", file=sys.stderr)
        return 1
    print("fixed-server-t2: REVIEW_PENDING; independent Decision still required")
    return 0


if __name__ == "__main__":
    sys.exit(main())
