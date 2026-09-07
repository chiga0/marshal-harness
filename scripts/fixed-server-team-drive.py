#!/usr/bin/env python3
"""观察 resident 创建/启动，通过既有接口收集实现与集成；不代替团队调度器。"""

import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import time


def load(name):
    spec = importlib.util.spec_from_file_location(name, Path(__file__).with_name(name + ".py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


inputs = load("fixed-server-team-inputs")
t2 = load("fixed-server-t2-drive")
Error = t2.DriveError


def subjects(request, approval):
    bundle = request["inputs"]
    if inputs.digest(bundle) != request["inputsDigest"]:
        raise Error("team-input-digest")
    proof = approval.get("approval", {})
    if (approval.get("found") is not True or proof.get("goalId") != bundle["spec"]["goalId"]
            or proof.get("inputsDigest") != request["inputsDigest"]
            or proof.get("requestDigest") != inputs.digest(request)
            or proof.get("planRevision") != 1 or proof.get("obligationCount") != 3
            or not t2.DIGEST.fullmatch(proof.get("factDigest", ""))):
        raise Error("team-approval-binding")
    if [(n["nodeId"], n["role"]) for n in bundle["nodes"]] != [("service", "implement"), ("client", "implement"), ("integration", "integrate")]:
        raise Error("team-reference-shape")
    return {node: inputs.node_ids(bundle["spec"]["authorityNamespaceId"], bundle["spec"]["goalId"],
                                 bundle["proposal"]["proposalId"], node)[1] for node in inputs.PATHS}


def await_initial(call, save, ready, runs, deadline, now=time.time, pause=time.sleep):
    """Filesystem readiness is only a polling hint; only fixed Inspect proves state.

    Missing nodes may be observed until the fixed deadline. No error, terminal
    state, new Attempt or transport failure authorizes a restart or reapproval.
    RUNNING projections are not proof of overlapping process execution.
    """
    previous, tick = {}, 0
    while now() < deadline:
        if ready(runs["integration"]):
            raise Error("integration-materialized-before-acceptance")
        observed = {}
        for node in ("service", "client"):
            run = runs[node]
            if not ready(run):
                continue
            code, value = call(["inspect", "--run", run], deadline - now())
            save(f"initial-{tick}-{node}.json", {"exitCode": code, "response": value})
            if code != 0 or not isinstance(value, dict) or value.get("runId") != run:
                raise Error("team-inspect-unavailable")
            if value.get("state") in {"CREATED", "PLANNED", "READY"}:
                if node in previous:
                    raise Error("team-state-regressed")
                continue
            current = t2.run_projection(value, run, "RUNNING", previous.get(node))
            previous[node] = current
            observed[node] = current
        if len(observed) == 2:
            save("resident-running.json", {"runs": observed, "processOverlapProven": False,
                                          "externalStartCalls": 0})
            return observed
        tick += 1
        pause(min(1, max(0, deadline - now())))
    raise Error("team-resident-dispatch-deadline")


def await_integration(call, save, ready, run, deadline, now=time.time, pause=time.sleep):
    """Only Inspect observes Core dispatch; this client never creates or Starts."""
    tick = 0
    while now() < deadline:
        if ready(run):
            code, value = call(["inspect", "--run", run], deadline - now())
            save(f"integration-initial-{tick}.json", {"exitCode": code, "response": value})
            if code != 0 or not isinstance(value, dict) or value.get("runId") != run:
                raise Error("integration-inspect-unavailable")
            if value.get("state") not in {"CREATED", "PLANNED", "READY"}:
                return t2.run_projection(value, run, "RUNNING")
        tick += 1
        pause(min(1, max(0, deadline - now())))
    raise Error("integration-resident-dispatch-deadline")


def expose_review(save, output, node, summary, binary_digest):
    directory = output / node
    directory.mkdir(mode=0o700, exist_ok=False)
    for leaf in ("review-inputs.tar", "review-summary.json", "review-packet.json"):
        with (directory / leaf).open("xb") as destination:
            destination.write((output / (node + "-" + leaf)).read_bytes())
    save(node + "/review-ready.json", {"run": summary["run"], "packetDigest": summary["packetDigest"],
                                      "archive": "review-inputs.tar", "binarySHA256": binary_digest})


def review_team(call, save, reviews, output, binary_unchanged, seconds, now=time.monotonic, wall=time.time, pause=time.sleep,
                nodes=("service", "client")):
    """Consume whichever independent Decision arrives first, with one budget.

    A known reject is evidence, not a reason to discard the other node. Any
    unknown mutation still aborts: no retry, new approval or integration here.
    """
    deadline, reviewed = now() + seconds, {}
    while len(reviewed) < len(nodes):
        if now() >= deadline:
            raise Error("independent-review-wait-expired")
        for node in nodes:
            if node in reviewed:
                continue
            decision_path = output / node / "review-decision.json"
            if not decision_path.exists() and not decision_path.is_symlink():
                continue
            decision = t2.await_external_decision(decision_path, deadline, now=now, pause=pause)
            if not binary_unchanged():
                raise Error("fixed-binary-drift")
            summary, packet = reviews[node]
            node_save = lambda name, value: save(node + "-" + name, value)
            reviewed[node] = t2.finalize_review(call, node_save, summary, packet, decision, decision_path,
                                               wall() + min(300, max(0, deadline - now())), require_accepted=False)
        if len(reviewed) < len(nodes):
            pause(min(1, max(0, deadline - now())))
    return reviewed


def await_team_outcome(call, save, request_path, approval, runs, deadline, now=time.time, pause=time.sleep):
    sequence = 0
    while now() < deadline:
        code, response = call(["team-reconcile", "--request-file", str(request_path)], min(30, deadline-now()))
        sequence += 1
        save(f"outcome-observation-{sequence}.json", {"exitCode": code, "response": response})
        if code != 0 or not isinstance(response, dict) or response.get("found") is not True or response.get("approval") != approval:
            raise Error("team-outcome-authority-unresolved")
        result = response.get("outcome")
        if result is not None:
            if (not isinstance(result, dict) or not isinstance(result.get("outcome"), dict)
                    or result["outcome"].get("state") != "completed"
                    or result["outcome"].get("goalId") != approval["goalId"]
                    or result.get("planFactDigest") != approval["factDigest"]
                    or result.get("integrationRunId") != runs["integration"]
                    or type(result.get("attemptsUsed")) is not int or result["attemptsUsed"] != 3
                    or result.get("measurement") != "attempt-counts-only"):
                raise Error("team-outcome-subject-conflict")
            # Fixed client has already authenticated peer and re-read this
            # exact durable fact. This script is only a diagnostic consumer.
            return result
        pause(min(1, max(0, deadline-now())))
    raise Error("team-outcome-observation-deadline")


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--evidence-root", required=True)
    parser.add_argument("--await-review-seconds", type=int, default=0)
    args = parser.parse_args()
    if not 0 <= args.await_review_seconds <= 1200:
        parser.error("invalid review deadline")
    root = Path(__file__).resolve().parent.parent
    binary, evidence = root / "bin/marshal", Path(args.evidence_root)
    if (not evidence.is_absolute() or evidence.resolve() != evidence
            or evidence.parent != root / ".marshal/fixed-server-t1-canary"
            or not t2.ID.fullmatch(evidence.name) or binary.is_symlink() or not binary.is_file()):
        parser.error("fixed binary/canonical evidence required")
    output = evidence / "team"
    output.mkdir(mode=0o700, exist_ok=False)

    def save(name, value):
        with (output / name).open("x", encoding="utf-8") as target:
            json.dump(value, target, ensure_ascii=False, sort_keys=True, separators=(",", ":"))
            target.write("\n")

    sequence = 0
    binary_digest = hashlib.sha256(binary.read_bytes()).hexdigest()

    def call(command, remaining):
        nonlocal sequence
        sequence += 1
        if remaining <= 0:
            raise Error("team-driver-deadline")
        try:
            result = subprocess.run([str(binary), "control-plane", *command], stdin=subprocess.DEVNULL,
                                    stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=remaining, check=False)
        except subprocess.TimeoutExpired as exc:
            raise Error("fixed-cli-response-timeout") from exc
        save(f"call-{sequence}.json", {"operation": command[0], "exitCode": result.returncode,
                                     "stdoutSHA256": hashlib.sha256(result.stdout).hexdigest(),
                                     "stderrSHA256": hashlib.sha256(result.stderr).hexdigest(),
                                     "transportStages": t2.safe_transport_stages(result.stderr)})
        if len(result.stdout) > 2 << 20 or len(result.stderr) > 64 << 10:
            raise Error("fixed-cli-output-limit")
        try:
            return result.returncode, json.loads(result.stdout)
        except (ValueError, UnicodeDecodeError) as exc:
            raise Error("fixed-cli-invalid-response") from exc

    try:
        request_path = evidence / "team-request.json"
        request = json.loads(request_path.read_bytes())
        # Exactly one mutating approval. Any unknown outcome is retained for
        # operator reconciliation; this client never retries paid work.
        code, approval = call(["team-approve", "--request-file", str(request_path)], 120)
        save("approval.json", {"exitCode": code, "response": approval})
        if code != 0:
            raise Error("team-approval-unresolved")
        runs = subjects(request, approval)
        save("subject.json", {"runs": runs, "sourceHead": request["inputs"]["baseSha"],
                              "binarySHA256": binary_digest, "inputsDigest": request["inputsDigest"]})
        ready = lambda run: (root / ".marshal/runs" / run / "state.json").is_file()
        await_initial(call, save, ready, runs, time.time() + 120)
        deadline = time.time() + 360
        reviews = {}
        for node in ("service", "client"):
            node_save = lambda name, value: save(node + "-" + name, value)
            summary = t2.drive(call, node_save, runs[node], deadline, require_pass=not args.await_review_seconds)
            packet = json.loads((output / (node + "-review-packet.json")).read_bytes())["Projection"]["packet"]
            t2.capture_review_inputs(root, runs[node], packet, output / (node + "-review-inputs.tar"))
            reviews[node] = (summary, packet)
        if ready(runs["integration"]):
            raise Error("integration-materialized-before-acceptance")
        if hashlib.sha256(binary.read_bytes()).hexdigest() != binary_digest:
            raise Error("fixed-binary-drift")
        reviewed = {}
        review_deadline = time.monotonic() + args.await_review_seconds
        if args.await_review_seconds:
            # Closed, diagnostic copies only. Never restore an authority store
            # or turn these files into approval for another runner.
            for node in ("service", "client"):
                summary, _ = reviews[node]
                expose_review(save, output, node, summary, binary_digest)
            with (output / "review.ready").open("xb"):
                pass
            reviewed = review_team(call, save, reviews, output,
                                   lambda: hashlib.sha256(binary.read_bytes()).hexdigest() == binary_digest,
                                   max(0, review_deadline - time.monotonic()))
        summary = {"stage": "two-implement-reviewed" if reviewed else "two-implement-review-pending", "accepted": False,
                              "integrationExecuted": False, "processOverlapProven": False,
                              "externalStartCalls": 0, "runs": runs, "reviewedRuns": reviewed}
        if reviewed and any(run["state"] != "ACCEPTED" for run in reviewed.values()):
            save("summary.json", summary)
            raise Error("team-independent-review-not-accepted")
        if reviewed:
            # Keep implementation evidence even if dispatch/verification fails.
            save("implement-summary.json", summary)
            remaining = lambda: max(0, review_deadline - time.monotonic())
            if remaining() <= 0:
                raise Error("independent-review-wait-expired")
            await_integration(call, save, ready, runs["integration"], time.time() + min(120, remaining()))
            node_save = lambda name, value: save("integration-" + name, value)
            integration = t2.drive(call, node_save, runs["integration"], time.time() + min(360, remaining()), require_pass=False)
            packet = json.loads((output / "integration-review-packet.json").read_bytes())["Projection"]["packet"]
            t2.capture_review_inputs(root, runs["integration"], packet, output / "integration-review-inputs.tar")
            expose_review(save, output, "integration", integration, binary_digest)
            with (output / "integration.review.ready").open("xb"):
                pass
            result = review_team(call, save, {"integration": (integration, packet)}, output,
                                 lambda: hashlib.sha256(binary.read_bytes()).hexdigest() == binary_digest,
                                 remaining(), nodes=("integration",))
            summary.update(stage="integration-reviewed", integrationExecuted=True, reviewedRuns={**reviewed, **result})
            summary["goalOutcomeAvailable"] = False
            if result["integration"]["state"] != "ACCEPTED":
                # Old diagnostic NO_CHANGE is not a completed code delivery.
                save("summary.json", summary)
                raise Error("team-integration-review-not-successful")
            save("integration-summary.json", summary)
            outcome = await_team_outcome(call, save, request_path, approval["approval"], runs,
                                         time.time()+min(60, remaining()))
            summary.update(stage="team-completed", accepted=True, goalOutcomeAvailable=True, goalOutcome=outcome)
            save("summary.json", summary)
        else:
            save("summary.json", summary)
        return 0
    except (Error, OSError, ValueError, KeyError, TypeError) as exc:
        # Do not disclose raw provider output, config, paths or exception text.
        save("failure.json", {"reason": str(exc) if isinstance(exc, Error) else "team-driver-input-or-io",
                              "automaticRetry": False, "accepted": False})
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
