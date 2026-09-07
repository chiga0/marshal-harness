#!/usr/bin/env python3
"""只观察 resident 创建/启动，再用既有公开接口收集两个节点；不代替团队调度器。"""

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


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--evidence-root", required=True)
    args = parser.parse_args()
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
        for node in ("service", "client"):
            node_save = lambda name, value: save(node + "-" + name, value)
            t2.drive(call, node_save, runs[node], deadline)
            packet = json.loads((output / (node + "-review-packet.json")).read_bytes())["Projection"]["packet"]
            t2.capture_review_inputs(root, runs[node], packet, output / (node + "-review-inputs.tar"))
        if ready(runs["integration"]):
            raise Error("integration-materialized-before-acceptance")
        if hashlib.sha256(binary.read_bytes()).hexdigest() != binary_digest:
            raise Error("fixed-binary-drift")
        save("summary.json", {"stage": "two-implement-review-pending", "accepted": False,
                              "integrationExecuted": False, "processOverlapProven": False,
                              "externalStartCalls": 0, "runs": runs})
        return 0
    except (Error, OSError, ValueError, KeyError, TypeError) as exc:
        # Do not disclose raw provider output, config, paths or exception text.
        save("failure.json", {"reason": str(exc) if isinstance(exc, Error) else "team-driver-input-or-io",
                              "automaticRetry": False, "accepted": False})
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
