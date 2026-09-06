#!/usr/bin/env python3
"""Drive an already approved/running T2 Run through the fixed server to review."""

import argparse
import datetime
import hashlib
import io
import json
import os
from pathlib import Path
import re
import subprocess
import stat
import sys
import tarfile
import time


DIGEST = re.compile(r"^sha256:[0-9a-f]{64}$")
ID = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._:-]{2,120}$")
LIVE_PENDING = {"disposition": "pending", "reasonCode": "attempt-still-running"}


class DriveError(Exception):
    pass


def capture_review_inputs(root, run_id, packet, archive):
    """Copy only the packet's review inputs, not a restorable authority store.

    These bytes still require independent digest/semantic verification against
    the packet. This diagnostic archive grants no Decision or import authority.
    """
    fixed = {"taskSpec": "task-spec.json", "patch": "observed.patch",
             "verificationReport": "verification-report.json", "artifactManifest": "artifact-manifest.json"}
    if not isinstance(packet, dict) or not ID.fullmatch(run_id):
        raise DriveError("invalid-review-inputs")
    inputs = packet.get("inputs")
    if packet.get("runId") != run_id or not isinstance(inputs, dict) or any(inputs.get(key) != path for key, path in fixed.items()):
        raise DriveError("invalid-review-inputs")
    workers = inputs.get("workerResults")
    if not isinstance(workers, list) or len(workers) > 16 or any(not isinstance(path, str) or not re.fullmatch(r"attempts/[A-Za-z0-9][A-Za-z0-9._:-]{2,120}/worker-result\.json", path) for path in workers):
        raise DriveError("invalid-review-worker-inputs")
    paths = ["review-packet.json", *fixed.values(), *workers]
    if len(set(workers)) != len(workers):
        raise DriveError("duplicate-review-worker-inputs")
    if packet.get("workerCandidateDigest"):
        paths.append("worker.patch")
    for key in ("candidateDigest", "workerCandidateDigest"):
        digest = packet.get(key)
        if digest:
            if not isinstance(digest, str) or not DIGEST.fullmatch(digest):
                raise DriveError("invalid-review-candidate-input")
            paths.append(f"candidates/{digest}.json")
    payloads, total = {}, 0
    try:
        root_fd = os.open(root, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        try:
            for path in dict.fromkeys(paths):
                directory = os.dup(root_fd)
                try:
                    parts = [".marshal", "runs", run_id, *path.split("/")]
                    for part in parts[:-1]:
                        child = os.open(part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=directory)
                        os.close(directory)
                        directory = child
                    fd = os.open(parts[-1], os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=directory)
                    with os.fdopen(fd, "rb") as source:
                        before = os.fstat(source.fileno())
                        if not stat.S_ISREG(before.st_mode) or before.st_nlink != 1 or before.st_size > 8 << 20:
                            raise DriveError("review-input-type-or-size")
                        raw = source.read((8 << 20) + 1)
                        after = os.fstat(source.fileno())
                    if len(raw) != before.st_size or (before.st_size, before.st_mtime_ns, before.st_ctime_ns) != (after.st_size, after.st_mtime_ns, after.st_ctime_ns):
                        raise DriveError("review-input-drift")
                    total += len(raw)
                    if total > 64 << 20:
                        raise DriveError("review-input-total-limit")
                    payloads[path] = raw
                finally:
                    os.close(directory)
        finally:
            os.close(root_fd)
        if json.loads(payloads["review-packet.json"]) != packet:
            raise DriveError("review-packet-changed")
        manifest = {"purpose": "review-only-not-authority-import", "runId": run_id,
                    "files": [{"path": path, "bytes": len(raw), "sha256": hashlib.sha256(raw).hexdigest()} for path, raw in payloads.items()]}
        payloads["capture-manifest.json"] = json.dumps(manifest, sort_keys=True, separators=(",", ":")).encode() + b"\n"
        # Tar preserves colon-containing Attempt IDs without exposing them as
        # upload-artifact filenames. Every member is generated as regular data.
        with archive.open("xb") as output, tarfile.open(fileobj=output, mode="w") as bundle:
            for path, raw in payloads.items():
                member = tarfile.TarInfo(path)
                member.size, member.mode = len(raw), 0o600
                bundle.addfile(member, io.BytesIO(raw))
    except (OSError, ValueError, TypeError) as exc:
        raise DriveError("review-input-capture-unavailable") from exc


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


def cancel_run(call, save, run_id, deadline, now=time.time, previous=None):
    """Prove an explicit operator stop through fixed-client operations only.

    No retry follows an uncertain response. The second cancel is a deliberate
    exact replay after a verified receipt, not a retry of unknown side effects.
    """
    deadline_text = datetime.datetime.fromtimestamp(deadline, datetime.timezone.utc).replace(tzinfo=None).isoformat(timespec="microseconds").rstrip("0").rstrip(".") + "Z"

    def invoke(args):
        remaining = deadline - now()
        if remaining <= 0:
            raise DriveError("cancel-driver-deadline-exceeded")
        return call(args, remaining)

    code, value = invoke(["inspect", "--run", run_id])
    if code != 0:
        raise DriveError("cancel-initial-inspect-unavailable")
    if previous is None:
        current = run_projection(value, run_id, "RUNNING")
    else:
        current = run_projection(previous["initial"], run_id, "RUNNING")
        expected = previous["response"]
        stopped = run_projection(expected["Projection"]["run"], run_id, "BLOCKED", current, True)
        if value != stopped:
            raise DriveError("cancel-recovery-query-mismatch")
    save("cancel-initial-run.json", current)
    frozen = ["--run", run_id, "--attempt", current["attemptId"], "--expected-sequence", str(current["sequence"]),
              "--expected-authority-head", current["authorityHead"], "--deadline", deadline_text]
    request = ["cancel", *frozen, "--request-key", f"t2:{run_id}:cancel:{current['sequence']}"]
    if previous is not None and previous["request"] != {"args": request}:
        raise DriveError("cancel-recovery-frozen-request-mismatch")
    save("cancel-request.json", {"args": request})
    code, value = invoke(request)
    save("cancel-response.json", {"exitCode": code, "response": value})
    if code != 0 or not isinstance(value, dict) or not isinstance(value.get("Projection"), dict) or not isinstance(value.get("Receipt"), dict):
        raise DriveError("cancel-unresolved-no-automatic-retry")
    if previous is not None and value != previous["response"]:
        raise DriveError("cancel-recovery-receipt-mismatch")
    projection, receipt = value["Projection"], value["Receipt"]
    stopped = run_projection(projection.get("run"), run_id, "BLOCKED", current, True)
    if projection.get("protocolRevision") != "run-stop/v1" or projection.get("terminalReason") != "aborted-by-operator":
        raise DriveError("cancel-reason-mismatch")
    if any(not isinstance(projection.get(key), str) or not DIGEST.fullmatch(projection[key]) for key in ("requestDigest", "stopIntentDigest", "outcomeDigest")):
        raise DriveError("cancel-terminal-digests-missing")
    if receipt.get("runId") != run_id or receipt.get("attemptId") != current["attemptId"] or receipt.get("postRevision") != stopped["sequence"] or receipt.get("postAuthorityHead") != stopped["authorityHead"]:
        raise DriveError("cancel-receipt-mismatch")
    code, replay = invoke(request)
    save("cancel-replay.json", {"exitCode": code, "response": replay})
    if code != 0 or replay != value:
        raise DriveError("cancel-replay-mismatch")
    # This is a new request, not a replay of a pre-stop Collect pending. Its
    # delivery binding must name the proved current BLOCKED head. The original
    # cancel request above remains byte-identical, including after restart.
    collect = ["collect", "--run", run_id, "--attempt", stopped["attemptId"],
               "--expected-sequence", str(stopped["sequence"]), "--expected-authority-head", stopped["authorityHead"],
               "--deadline", deadline_text, "--request-key", f"t2:{run_id}:collect-after-cancel:{stopped['sequence']}"]
    code, collected = invoke(collect)
    save("collect-after-cancel.json", {"exitCode": code, "response": collected})
    if code != 1 or collected != {"disposition": "stopped", "reasonCode": "run-stopped"}:
        raise DriveError("cancel-collect-did-not-stop")
    code, inspected = invoke(["inspect", "--run", run_id])
    if code != 0 or inspected != stopped:
        raise DriveError("cancel-final-inspect-mismatch")
    summary = {"runId": run_id, "run": stopped, "stage": "cancelled", "accepted": False,
               "terminalReason": projection["terminalReason"], "outcomeDigest": projection["outcomeDigest"],
               "stopIntentDigest": projection["stopIntentDigest"], "transport": "fixed-control-plane"}
    save("cancel-summary.json", summary)
    return summary


def observe_business_stop(call, save, run_id, deadline, now=time.time, pause=time.sleep):
    """Observe resident stopping before any Collect; never issue Cancel.

    This proves the public stopped path, not its reason or deadline witness.
    Those require a separate check of the retained journal/ingress evidence.
    """
    initial = None
    started = now()

    def invoke(args):
        remaining = deadline - now()
        if remaining <= 0:
            raise DriveError("business-stop-observation-deadline")
        return call(args, min(30, remaining))

    while True:
        code, value = invoke(["inspect", "--run", run_id])
        if code != 0 or not isinstance(value, dict):
            raise DriveError("business-stop-inspect-unavailable")
        state = value.get("state")
        if state == "BLOCKED":
            stopped = run_projection(value, run_id, "BLOCKED", initial, initial is not None)
            break
        current = run_projection(value, run_id, "RUNNING", initial)
        if initial is None:
            initial = current
            save("business-stop-initial-run.json", initial)
        remaining = deadline - now()
        if remaining <= 0:
            raise DriveError("business-stop-observation-deadline")
        pause(min(2, remaining))

    save("business-stop-observed.json", {"run": stopped, "elapsedSeconds": now() - started,
                                        "observedRunning": initial is not None})
    deadline_text = datetime.datetime.fromtimestamp(deadline, datetime.timezone.utc).replace(tzinfo=None).isoformat(timespec="microseconds").rstrip("0").rstrip(".") + "Z"
    request = ["collect", "--run", run_id, "--attempt", stopped["attemptId"],
               "--expected-sequence", str(stopped["sequence"]), "--expected-authority-head", stopped["authorityHead"],
               "--deadline", deadline_text, "--request-key", f"t2:{run_id}:collect-after-business-stop:{stopped['sequence']}"]
    save("business-stop-collect-request.json", {"args": request})
    code, collected = invoke(request)
    save("collect-after-business-stop.json", {"exitCode": code, "response": collected})
    if code != 1 or collected != {"disposition": "stopped", "reasonCode": "run-stopped"}:
        raise DriveError("business-stop-collect-mismatch")
    code, final = invoke(["inspect", "--run", run_id])
    if code != 0 or final != stopped:
        raise DriveError("business-stop-final-inspect-mismatch")
    summary = {"runId": run_id, "run": stopped, "stage": "resident-stop-observed", "accepted": False,
               "deadlineWitnessVerified": False, "transport": "fixed-control-plane"}
    save("business-stop-summary.json", summary)
    return summary


def finalize_review(call, save, summary, packet, decision, decision_path, deadline, now=time.time):
    """Deliver an external review; neither construct one nor retry mutation.

    These checks catch transport mistakes. The fixed client and server still
    own canonical digest validation, current-ledger admission and Outcome.
    """
    current = summary["run"]
    if summary.get("verificationStatus") != "pass" or summary.get("accepted") is not False:
        raise DriveError("review-not-ready")
    if not isinstance(decision, dict) or decision.get("kind") != "ReviewDecision":
        raise DriveError("invalid-external-decision")
    if decision.get("runId") != current["runId"] or decision.get("reviewPacketDigest") != summary["packetDigest"]:
        raise DriveError("external-decision-packet-mismatch")
    for key in ("taskId", "reviewRound", "specDigest", "verificationDigest", "artifactManifestDigest", "evidenceDigest", "localSelfIdentityBindingDigest"):
        if key in packet and (key not in decision or decision[key] != packet[key]):
            raise DriveError("external-decision-evidence-mismatch")
    reviewer = decision.get("reviewer")
    if not isinstance(reviewer, dict) or reviewer.get("type") not in ("human", "lead-agent") or not reviewer.get("id"):
        raise DriveError("external-reviewer-required")

    def invoke(args):
        remaining = deadline - now()
        if remaining <= 0:
            raise DriveError("decision-deadline-exceeded")
        return call(args, remaining)

    code, inspected = invoke(["inspect", "--run", current["runId"]])
    if code != 0 or run_projection(inspected, current["runId"], "REVIEW_PENDING", current) != current:
        raise DriveError("review-current-inspection-mismatch")
    deadline_text = datetime.datetime.fromtimestamp(deadline, datetime.timezone.utc).replace(tzinfo=None).isoformat(timespec="microseconds").rstrip("0").rstrip(".") + "Z"
    request = ["decision", "--run", current["runId"], "--attempt", current["attemptId"],
               "--expected-sequence", str(current["sequence"]), "--expected-authority-head", current["authorityHead"],
               "--request-key", f"t2:{current['runId']}:decision:{current['sequence']}", "--deadline", deadline_text,
               "--decision", str(decision_path)]
    save("decision-request.json", {"args": request})
    code, value = invoke(request)
    save("decision-response.json", {"exitCode": code, "response": value})
    if code != 0 or not isinstance(value, dict) or not isinstance(value.get("Projection"), dict) or not isinstance(value.get("Receipt"), dict):
        raise DriveError("decision-unresolved-no-automatic-retry")
    projection, receipt = value["Projection"], value["Receipt"]
    observed = projection.get("run")
    if not isinstance(observed, dict) or observed.get("state") not in ("ACCEPTED", "NO_CHANGE", "REJECTED", "BLOCKED", "RETRY_PENDING"):
        raise DriveError("unexpected-decision-state")
    after = run_projection(observed, current["runId"], observed["state"], current, True)
    if any(receipt.get(key) != expected for key, expected in {"runId": after["runId"], "attemptId": after["attemptId"], "postRevision": after["sequence"], "postAuthorityHead": after["authorityHead"]}.items()):
        raise DriveError("decision-receipt-mismatch")
    if projection.get("verdict") != decision.get("verdict") or projection.get("evidenceDigest") != decision.get("evidenceDigest") or not DIGEST.fullmatch(projection.get("decisionDigest", "")):
        raise DriveError("decision-projection-mismatch")
    if after["state"] != "RETRY_PENDING" and not DIGEST.fullmatch(projection.get("outcomeDigest", "")):
        raise DriveError("decision-outcome-missing")
    code, inspected = invoke(["inspect", "--run", current["runId"]])
    if code != 0 or run_projection(inspected, current["runId"], after["state"], after) != after:
        raise DriveError("decision-final-inspection-mismatch")
    accepted = after["state"] == "ACCEPTED" and decision.get("verdict") == "accept"
    save("decision-summary.json", {"run": after, "accepted": accepted, "decisionDigest": projection["decisionDigest"],
                                   "outcomeDigest": projection.get("outcomeDigest"), "finishedAt": now()})
    if not accepted:
        raise DriveError("independent-review-not-accepted")
    return after


def await_external_decision(path, deadline, now=time.monotonic, pause=time.sleep):
    """Wait for atomic publication, not for an invalid record to turn valid."""
    def unique_object(pairs):
        value = {}
        for key, item in pairs:
            if key in value:
                raise ValueError("duplicate key")
            value[key] = item
        return value

    while True:
        if now() >= deadline:
            raise DriveError("independent-review-wait-expired")
        try:
            fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
        except FileNotFoundError:
            pause(min(2, max(0, deadline - now())))
            continue
        except OSError:
            raise DriveError("external-decision-unavailable") from None
        try:
            with os.fdopen(fd, "rb") as source:
                before = os.fstat(source.fileno())
                if not stat.S_ISREG(before.st_mode) or before.st_nlink != 1 or before.st_size > 1 << 20:
                    raise DriveError("external-decision-type-or-size")
                raw = source.read((1 << 20) + 1)
                after = os.fstat(source.fileno())
            if len(raw) != before.st_size or (before.st_size, before.st_mtime_ns, before.st_ctime_ns) != (after.st_size, after.st_mtime_ns, after.st_ctime_ns):
                raise DriveError("external-decision-drift")
            value = json.loads(raw, object_pairs_hook=unique_object)
            if not isinstance(value, dict):
                raise ValueError("not object")
            return value
        except (OSError, ValueError, UnicodeDecodeError):
            raise DriveError("external-decision-invalid") from None


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--run", required=True)
    parser.add_argument("--evidence-dir", required=True)
    parser.add_argument("--timeout-seconds", type=int, default=480)
    parser.add_argument("--await-review-seconds", type=int, default=0)
    parser.add_argument("--cancel", action="store_true", help="verify explicit stop, exact replay and stopped Collect instead of business acceptance")
    parser.add_argument("--cancel-recovery", action="store_true", help="verify the same proved stop after server restart, without extending its deadline")
    parser.add_argument("--observe-business-stop", action="store_true", help="observe resident stopping without Cancel or pre-stop Collect")
    args = parser.parse_args()
    root = Path(__file__).resolve().parent.parent
    binary = root / "bin" / "marshal"
    evidence = Path(args.evidence_dir)
    if not ID.fullmatch(args.run) or not 1 <= args.timeout_seconds <= 480 or not 0 <= args.await_review_seconds <= 1200:
        parser.error("invalid run/deadline")
    if sum((args.cancel, args.cancel_recovery, args.observe_business_stop)) > 1 or (args.cancel or args.cancel_recovery or args.observe_business_stop) and args.await_review_seconds:
        parser.error("cancel cannot request independent business acceptance")
    if binary.is_symlink() or not binary.is_file() or binary.resolve() != binary:
        parser.error("fixed bin/marshal is required")
    if not evidence.is_absolute() or evidence.resolve() != evidence or evidence.parent != root / ".marshal" / "fixed-server-t1-canary" / args.run:
        parser.error("evidence-dir must be the fresh t2 child of this Run's canary evidence")
    if evidence.name != ("t2-recovery" if args.cancel_recovery else "t2"):
        parser.error("invalid evidence leaf")
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
        if args.observe_business_stop:
            observe_business_stop(call, save, args.run, time.time() + args.timeout_seconds)
            if hashlib.sha256(binary.read_bytes()).hexdigest() != binary_digest:
                raise DriveError("fixed-binary-drift")
            return 0
        if args.cancel or args.cancel_recovery:
            previous = None
            deadline = time.time() + args.timeout_seconds
            if args.cancel_recovery:
                prior = evidence.parent / "t2"
                if prior.is_symlink() or not prior.is_dir() or prior.resolve() != prior:
                    raise DriveError("cancel-recovery-evidence-path")

                def read_prior(name):
                    path = prior / name
                    if path.is_symlink() or not path.is_file() or path.stat().st_size > 2 << 20:
                        raise DriveError("cancel-recovery-evidence-file")
                    return json.loads(path.read_bytes())

                subject = read_prior("driver-subject.json")
                summary = read_prior("cancel-summary.json")
                response = read_prior("cancel-response.json")
                if subject["binarySHA256"] != binary_digest or subject["runId"] != args.run or summary["accepted"] is not False or summary["stage"] != "cancelled" or response["exitCode"] != 0:
                    raise DriveError("cancel-recovery-subject-mismatch")
                previous = {"initial": read_prior("cancel-initial-run.json"), "request": read_prior("cancel-request.json"), "response": response["response"]}
                frozen = previous["request"]["args"]
                # Reconstruct the complete argument list inside cancel_run;
                # a local evidence file never supplies arbitrary CLI options.
                deadline = datetime.datetime.fromisoformat(frozen[frozen.index("--deadline") + 1].replace("Z", "+00:00")).timestamp()
                if not 0 < deadline - time.time() <= args.timeout_seconds:
                    raise DriveError("cancel-recovery-deadline")
            cancel_run(call, save, args.run, deadline, previous=previous)
            if hashlib.sha256(binary.read_bytes()).hexdigest() != binary_digest:
                raise DriveError("fixed-binary-drift")
            return 0
        summary = drive(call, save, args.run, time.time() + args.timeout_seconds)
        packet = json.loads((evidence / "review-packet.json").read_bytes())["Projection"]["packet"]
        capture_review_inputs(root, args.run, packet, evidence / "review-inputs.tar")
        if hashlib.sha256(binary.read_bytes()).hexdigest() != binary_digest:
            raise DriveError("fixed-binary-drift")
        if args.await_review_seconds:
            # This signal follows the closed archive, and is not authority.
            save("review-ready.json", {"run": summary["run"], "packetDigest": summary["packetDigest"],
                                       "archive": "review-inputs.tar", "binarySHA256": binary_digest})
            # Existence is signalled only after all referenced files close.
            with (evidence / "review.ready").open("xb"):
                pass
            decision_path = evidence / "review-decision.json"
            decision = await_external_decision(decision_path, time.monotonic() + args.await_review_seconds)
            if hashlib.sha256(binary.read_bytes()).hexdigest() != binary_digest:
                raise DriveError("fixed-binary-drift")
            finalize_review(call, save, summary, packet, decision, decision_path, time.time() + 300)
            print("fixed-server-t2: ACCEPTED by independent Decision through the same server")
            return 0
    except DriveError as exc:
        save("driver-failure.json", {"reasonCode": str(exc), "accepted": False})
        print(f"fixed-server-t2: {exc}", file=sys.stderr)
        return 1
    except (OSError, ValueError, KeyError, TypeError, IndexError, OverflowError):
        save("driver-failure.json", {"reasonCode": "driver-evidence-invalid", "accepted": False})
        print("fixed-server-t2: driver-evidence-invalid", file=sys.stderr)
        return 1
    print("fixed-server-t2: REVIEW_PENDING; independent Decision still required")
    return 0


if __name__ == "__main__":
    sys.exit(main())
