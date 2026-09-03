#!/usr/bin/env python3
"""Capture and verify fixed-server T1 exact-head canary evidence."""

import argparse
import copy
import hashlib
import json
import os
import re
import stat
import subprocess
import sys


DIGEST = re.compile(r"^sha256:[0-9a-f]{64}$")
HEX40 = re.compile(r"^[0-9a-f]{40}$")


class EvidenceError(RuntimeError):
    pass


def require(condition, message):
    if not condition:
        raise EvidenceError(message)


def canonical_bytes(value):
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")


def sha256_bytes(value):
    return "sha256:" + hashlib.sha256(value).hexdigest()


def sha256_file(path):
    digest = hashlib.sha256()
    with open(path, "rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return "sha256:" + digest.hexdigest()


def load_json(path):
    with open(path, "rb") as handle:
        raw = handle.read()
    value = json.loads(raw)
    require(isinstance(value, dict), f"{path}: JSON root must be object")
    return value


def load_jsonl(path):
    records = []
    with open(path, "rb") as handle:
        for number, raw in enumerate(handle, 1):
            require(raw.endswith(b"\n"), f"{path}:{number}: missing newline")
            body = raw[:-1]
            value = json.loads(body)
            require(isinstance(value, dict), f"{path}:{number}: record must be object")
            require(canonical_bytes(value) == body, f"{path}:{number}: record is not canonical JSON")
            records.append(value)
    require(records, f"{path}: empty ledger")
    return records


def sealed_digest(value, field="digest"):
    detached = copy.deepcopy(value)
    stored = detached.get(field)
    require(DIGEST.fullmatch(stored or "") is not None, f"invalid {field}")
    detached[field] = ""
    require(sha256_bytes(canonical_bytes(detached)) == stored, f"{field} mismatch")
    return stored


def observe_binary(args):
    path = os.path.realpath(args.binary)
    require(path == args.binary and os.path.isfile(path), "binary path must be canonical regular file")
    info = os.stat(path, follow_symlinks=False)
    require(stat.S_ISREG(info.st_mode) and info.st_nlink == 1 and info.st_mode & 0o111, "binary object is not fixed executable")
    require(info.st_mode & 0o6000 == 0, "binary object has set-id bits")
    version_value = load_json(args.version_json)
    codesign = subprocess.run(
        [args.codesign, "-d", "--verbose=4", path], check=True, capture_output=True, text=True
    )
    match = re.search(r"(?:^|\n)CDHash=([0-9A-Fa-f]{40})(?:\n|$)", codesign.stdout + codesign.stderr)
    require(match is not None, "codesign did not report a 40-hex CDHash")
    observation = {
        "schemaVersion": "marshal.fixed-server-t1-binary.v1",
        "canonicalPath": path,
        "device": info.st_dev,
        "inode": info.st_ino,
        "mode": info.st_mode,
        "linkCount": info.st_nlink,
        "size": info.st_size,
        "rawSHA256": sha256_file(path),
        "cdHash": match.group(1).lower(),
        "sourceHead": version_value.get("commit"),
        "version": version_value.get("version"),
        "selfProfile": version_value.get("selfProfile"),
        "os": version_value.get("os"),
        "arch": version_value.get("arch"),
    }
    require(HEX40.fullmatch(observation["sourceHead"] or "") is not None, "binary sourceHead is not exact")
    require(HEX40.fullmatch(observation["cdHash"]) is not None, "binary CDHash is not exact")
    with open(args.out, "wb") as handle:
        handle.write(canonical_bytes(observation) + b"\n")


def run_projection(path):
    value = load_json(path)
    require(isinstance(value, dict), f"{path}: missing control-plane inspect projection")
    return value


def status_projection(path):
    value = load_json(path)
    require(value.get("protocolRevision") == "public-application-port/v1", f"{path}: status protocol drift")
    require(value.get("availability") == "ready" and value.get("pendingRecovery") == 0, f"{path}: server not ready")
    return value


def record_for_digest(records, fact_type, digest):
    matches = [record for record in records if record.get("factType") == fact_type and record.get("digest") == digest]
    require(len(matches) == 1, f"expected one {fact_type} for {digest}, got {len(matches)}")
    sealed_digest(matches[0])
    return matches[0]


def command_for_attempt(records, attempt_key, command):
    matches = [
        record for record in records
        if record.get("factType") == "process-supervisor-command-outcome"
        and record.get("attemptKey") == attempt_key
        and (record.get("outcome") or {}).get("command") == command
        and (record.get("outcome") or {}).get("disposition") == "ok"
    ]
    require(len(matches) == 1, f"expected one successful {command}, got {len(matches)}")
    sealed_digest(matches[0])
    return matches[0]


def check_binary(observation, expected_head, current_sha):
    require(observation.get("schemaVersion") == "marshal.fixed-server-t1-binary.v1", "binary observation schema drift")
    require(observation.get("rawSHA256") == current_sha, "binary SHA-256 drift")
    require(observation.get("sourceHead") == expected_head, "binary sourceHead drift")
    require(HEX40.fullmatch(observation.get("cdHash", "")) is not None, "binary CDHash invalid")
    require(observation.get("selfProfile") == "darwin-local-dogfood", "binary profile drift")
    require(observation.get("os") == "darwin" and observation.get("arch") == "arm64", "binary platform drift")


def check(args):
    repository = os.path.realpath(args.repository)
    evidence = os.path.realpath(args.evidence_root)
    binary = os.path.realpath(args.binary)
    require(repository == args.repository and evidence == args.evidence_root and binary == args.binary, "all input paths must be canonical")
    require(HEX40.fullmatch(args.expected_head) is not None, "expected head must be 40 lowercase hex")

    first_binary = load_json(os.path.join(evidence, "binary-server1.json"))
    second_binary = load_json(os.path.join(evidence, "binary-server2.json"))
    current_sha = sha256_file(binary)
    check_binary(first_binary, args.expected_head, current_sha)
    check_binary(second_binary, args.expected_head, current_sha)
    require(first_binary == second_binary, "server1/server2 binary object identity differs")
    require(first_binary.get("canonicalPath") == binary, "binary observation path drift")

    activation = load_json(os.path.join(evidence, "activation.json"))
    require(activation.get("expectedRawSHA256") == current_sha, "activation SHA-256 drift")
    require(activation.get("expectedSourceHead") == args.expected_head, "activation sourceHead drift")
    require(activation.get("canonicalExecutablePath") == binary, "activation binary path drift")
    require(activation.get("canonicalRepositoryRoot") == repository, "activation repository drift")

    for ready_name in ("server1-ready.json", "server2-ready.json"):
        ready = load_json(os.path.join(evidence, ready_name))
        require(ready == {"availability": "ready", "protocolRevision": "darwin-fixed-control-endpoint/v1"}, f"{ready_name}: readiness drift")

    status1 = status_projection(os.path.join(evidence, "server1-status.json"))
    status2 = status_projection(os.path.join(evidence, "server2-status.json"))
    require(status2.get("ownerEpoch") == status1.get("ownerEpoch") + 1, "owner epoch is not E1+1")
    require(status1.get("ownerFactDigest") != status2.get("ownerFactDigest"), "owner fact did not advance")

    request = load_json(os.path.join(evidence, "start-request.json"))
    require(request.get("runId") == args.run_id, "request Run ID drift")
    request_key = request.get("requestKey")
    require(isinstance(request_key, str) and 1 <= len(request_key) <= 512 and request_key.strip() == request_key, "request key invalid")
    deadline = request.get("deadline")
    require(isinstance(deadline, str) and deadline.endswith("Z"), "frozen deadline invalid")

    ready_run = run_projection(os.path.join(evidence, "server1-ready-inspect.json"))
    require(ready_run.get("runId") == args.run_id and ready_run.get("state") == "READY", "pre-start projection is not exact READY")
    require(request.get("expectedSequence") == ready_run.get("sequence"), "request sequence drift")
    require(request.get("expectedAuthorityHead") == ready_run.get("authorityHead"), "request head drift")

    running_paths = [
        "server1-running-inspect.json",
        "server2-recovered-inspect.json",
        "server2-final-inspect.json",
    ]
    running = [run_projection(os.path.join(evidence, path)) for path in running_paths]
    require(all(item == running[0] for item in running[1:]), "RUNNING projection changed across restart/replay")
    current_run = running[0]
    require(current_run.get("runId") == args.run_id and current_run.get("state") == "RUNNING", "Run is not RUNNING")
    require(current_run.get("sequence") == ready_run.get("sequence") + 1, "Run successor sequence is not exact")
    attempt_id = current_run.get("attemptId")
    require(isinstance(attempt_id, str) and attempt_id, "RUNNING projection lacks Attempt")

    replay = load_json(os.path.join(evidence, "server2-start-replay.json"))
    require(set(replay) == {"Projection", "Receipt"}, "replay fixed CLI projection shape drift")
    started = replay.get("Projection")
    receipt_response = replay.get("Receipt")
    require(isinstance(started, dict) and isinstance(receipt_response, dict), "replay lacks start/receipt")
    require(started.get("run") == current_run, "replayed start projection drift")
    prepared = started.get("prepared") or {}
    require(prepared.get("runId") == args.run_id and prepared.get("attemptId") == attempt_id, "prepared Run/Attempt drift")
    require(prepared.get("sequence") == ready_run.get("sequence") and prepared.get("authorityHead") == ready_run.get("authorityHead"), "prepared predecessor drift")
    require(prepared.get("attemptOrdinal") == 1 and prepared.get("attemptsUsedBefore") == 0 and prepared.get("maxAttempts") == 1, "prepared attempt budget drift")

    run_root = os.path.join(repository, ".marshal", "runs")
    run_entries = sorted(os.listdir(run_root))
    require(run_entries == [args.run_id], f"second Run or unexpected run-root entry present: {run_entries}")
    run_dir = os.path.join(run_root, args.run_id)
    state = load_json(os.path.join(run_dir, "state.json"))
    require(state.get("runId") == args.run_id and state.get("state") == "RUNNING", "durable state is not RUNNING")
    require(state.get("currentAttemptId") == attempt_id and state.get("attemptsUsed") == 1, "attemptsUsed/current Attempt drift")
    require(state.get("sequence") == current_run.get("sequence") and state.get("baseSha") == args.expected_head, "durable Run identity drift")
    attempts_root = os.path.join(run_dir, "attempts")
    attempt_entries = sorted(os.listdir(attempts_root))
    require(attempt_entries == [attempt_id], f"expected one Attempt directory, got {attempt_entries}")

    events = load_jsonl(os.path.join(run_dir, "events.jsonl"))
    require(len(events) == state.get("sequence"), "Run sequence/event count drift")
    started_events = [event for event in events if event.get("type") == "run.start-outcome"]
    require(len(started_events) == 1, f"expected one sealed worker.started event, got {len(started_events)}")
    require(not any(event.get("type") == "worker.started" for event in events), "legacy worker.started fallback observed")
    started_event = started_events[0]
    require(started_event == events[-1], "sealed worker.started is not Run head")
    require(started_event.get("runId") == args.run_id and started_event.get("attemptId") == attempt_id, "worker.started identity drift")
    require(started_event.get("stateFrom") == "READY" and started_event.get("stateTo") == "RUNNING", "worker.started transition drift")
    require(sha256_bytes(canonical_bytes(started_event)) == current_run.get("authorityHead"), "Run authority head drift")
    payload = started_event.get("payload") or {}
    require(payload.get("protocolRevision") == "run-start-outcome/v2", "legacy Run-start projection observed")
    require(payload.get("preparationDigest") == prepared.get("preparationDigest"), "preparation digest drift")
    require(
        payload.get("taskId") == current_run.get("taskId") == prepared.get("taskId")
        and payload.get("attemptOrdinal") == 1
        and payload.get("attemptsUsedBefore") == 0
        and payload.get("maxAttempts") == 1
        and payload.get("readySequence") == ready_run.get("sequence")
        and payload.get("readyAuthorityHead") == ready_run.get("authorityHead"),
        "sealed worker.started predecessor/budget drift",
    )

    ingress_path = os.path.join(repository, ".marshal", "result-ingress", "result-ingress.jsonl")
    ingress = load_jsonl(ingress_path)
    owner_facts = [record for record in ingress if record.get("factType") == "control-owner-acquired"]
    require(
        len(owner_facts) == 2
        and [record.get("digest") for record in owner_facts] == [status1["ownerFactDigest"], status2["ownerFactDigest"]],
        "owner acquisition set is not exactly server1 then server2",
    )
    owner1 = record_for_digest(ingress, "control-owner-acquired", status1["ownerFactDigest"])
    owner2 = record_for_digest(ingress, "control-owner-acquired", status2["ownerFactDigest"])
    acquisition1, acquisition2 = owner1["acquisition"], owner2["acquisition"]
    require(acquisition1.get("ownerEpoch") == status1["ownerEpoch"], "server1 owner epoch drift")
    require(acquisition2.get("ownerEpoch") == status2["ownerEpoch"], "server2 owner epoch drift")
    require(owner2.get("previousOwnerEpoch") == status1["ownerEpoch"] and owner2.get("previousOwnerFactDigest") == status1["ownerFactDigest"], "owner lineage is not exact")
    require(acquisition1.get("ownerBinary") == acquisition2.get("ownerBinary"), "owner binary identity changed")
    owner_binary = acquisition1.get("ownerBinary") or {}
    require(owner_binary.get("rawSHA256") == current_sha, "owner binary SHA-256 drift")
    require(owner_binary.get("cdHash") == first_binary.get("cdHash"), "owner binary CDHash drift")
    require(owner_binary.get("sourceHead") == args.expected_head, "owner binary sourceHead drift")
    require(owner_binary.get("canonicalPath") == binary, "owner binary path drift")
    process1 = load_json(os.path.join(evidence, "server1-process.json"))
    process2 = load_json(os.path.join(evidence, "server2-process.json"))
    require(acquisition1.get("ownerProcess", {}).get("pid") == process1.get("pid"), "server1 PID not owner")
    require(acquisition2.get("ownerProcess", {}).get("pid") == process2.get("pid"), "server2 PID not owner")
    require(process1.get("stop") == "SIGKILL" and process1.get("waitStatus") == 137, "server1 was not crash-stopped")
    require(process2.get("stop") == "SIGTERM" and process2.get("waitStatus") == 0, "server2 did not exit normally")

    reservations = [record for record in ingress if record.get("factType") == "attempt-reserved" and (record.get("reservation") or {}).get("ready", {}).get("runId") == args.run_id]
    opened = [record for record in ingress if record.get("factType") == "attempt-opened" and (record.get("transition") or {}).get("identity", {}).get("runId") == args.run_id]
    process_started = [record for record in ingress if record.get("factType") == "process-started" and (record.get("transition") or {}).get("identity", {}).get("runId") == args.run_id]
    prepared_records = [
        record for record in ingress
        if record.get("factType") == "prepared-execution-created"
        and ((record.get("prepared") or {}).get("attemptIdentity") or {}).get("runId") == args.run_id
    ]
    require(len(reservations) == len(opened) == len(process_started) == len(prepared_records) == 1, "reservation/open/process-started/prepared is not unique")
    for record in reservations + opened + process_started + prepared_records:
        sealed_digest(record)
    reservation, open_record, process_record, prepared_record = reservations[0], opened[0], process_started[0], prepared_records[0]
    require((reservation.get("reservation") or {}).get("attemptId") == attempt_id, "reservation Attempt drift")
    require((reservation.get("reservation") or {}).get("attemptOrdinal") == 1, "reservation ordinal drift")
    require((open_record.get("transition") or {}).get("identity", {}).get("attemptId") == attempt_id, "opened Attempt identity drift")
    require((process_record.get("transition") or {}).get("identity", {}).get("attemptId") == attempt_id, "process-started Attempt identity drift")
    require(open_record.get("attemptKey") == process_record.get("attemptKey"), "Attempt key drift")
    require(open_record.get("digest") == payload.get("attemptOpenedFactDigest"), "opened digest drift")
    require(reservation.get("digest") == payload.get("reservationFactDigest"), "reservation digest drift")
    require(process_record.get("digest") == payload.get("processStartedFactDigest"), "process-started digest drift")
    prepared_value = prepared_record.get("prepared") or {}
    require(
        (prepared_value.get("attemptIdentity") or {}).get("attemptId") == attempt_id
        and prepared_value.get("reservationFactDigest") == reservation.get("digest")
        and prepared_value.get("attemptOpenedFactDigest") == open_record.get("digest")
        and prepared_value.get("attemptOrdinal") == 1
        and prepared_value.get("attemptsUsedBefore") == 0
        and prepared_value.get("maxAttempts") == 1,
        "prepared execution identity/budget drift",
    )
    detached_prepared = copy.deepcopy(prepared_value)
    preparation_digest = detached_prepared.get("preparationDigest")
    detached_prepared["preparationDigest"] = ""
    require(sha256_bytes(canonical_bytes(detached_prepared)) == preparation_digest == prepared.get("preparationDigest"), "prepared execution digest drift")

    attempt_key = open_record.get("attemptKey")
    spawn = command_for_attempt(ingress, attempt_key, "spawn")
    resume = command_for_attempt(ingress, attempt_key, "resume")
    transition = process_record.get("transition") or {}
    require(spawn.get("digest") == transition.get("supervisorOutcomeFactDigest"), "spawn/process-started binding drift")
    require(resume.get("digest") == payload.get("resumeOutcomeFactDigest"), "resume/Run binding drift")
    owner_bindings = [record for record in ingress if record.get("factType") == "control-owner-bound" and record.get("attemptKey") == attempt_key]
    require(len(owner_bindings) == 2, f"expected initial+recovery owner bindings, got {len(owner_bindings)}")
    for binding in owner_bindings:
        sealed_digest(binding)
    require((owner_bindings[0].get("transition") or {}).get("owner", {}).get("ownerEpoch") == status1["ownerEpoch"], "initial Attempt owner drift")
    require((owner_bindings[1].get("transition") or {}).get("owner", {}).get("ownerEpoch") == status2["ownerEpoch"], "recovered Attempt owner drift")
    bind_outcomes = [
        record for record in ingress
        if record.get("factType") == "process-supervisor-command-outcome"
        and record.get("attemptKey") == attempt_key
        and (record.get("outcome") or {}).get("command") == "bind-authority"
        and (record.get("outcome") or {}).get("disposition") == "ok"
    ]
    require(len(bind_outcomes) == 2, f"expected initial+recovery bind-authority outcomes, got {len(bind_outcomes)}")
    for outcome in bind_outcomes:
        sealed_digest(outcome)
    recovery_binds = [
        outcome for outcome in bind_outcomes
        if (outcome.get("outcome") or {}).get("boundAuthorityHead") == owner_bindings[1].get("digest")
    ]
    require(len(recovery_binds) == 1, "recovery bind-authority does not bind the E1+1 owner successor")

    delivery_dir = os.path.join(repository, ".marshal", "runtime-v1", "control", "delivery-v1")
    delivery_entries = sorted(os.listdir(delivery_dir))
    pending_names = [name for name in delivery_entries if re.fullmatch(r"p-[0-9a-f]{64}\.json", name)]
    receipt_names = [name for name in delivery_entries if re.fullmatch(r"r-[0-9a-f]{64}\.json", name)]
    require(len(pending_names) == len(receipt_names) == 1 and len(delivery_entries) == 2, f"delivery set is not exactly pending+receipt: {delivery_entries}")
    pending = load_json(os.path.join(delivery_dir, pending_names[0]))
    receipt = load_json(os.path.join(delivery_dir, receipt_names[0]))
    sealed_digest(pending)
    sealed_digest(receipt)
    require(
        pending.get("schemaVersion") == receipt.get("schemaVersion") == "fixed-delivery-record/v2"
        and pending.get("protocolRevision") == receipt.get("protocolRevision") == "darwin-fixed-delivery/v2"
        and pending.get("recordType") == "pending"
        and receipt.get("recordType") == "receipt-ref"
        and pending.get("operation") == receipt.get("operation") == "start-run",
        "delivery record protocol drift",
    )
    require(pending_names == [f"p-{pending['requestKeyDigest'].split(':', 1)[1]}.json"], "pending filename binding drift")
    require(receipt_names == [f"r-{pending['digest'].split(':', 1)[1]}.json"], "receipt filename binding drift")
    start_request = {"expectedAuthorityHead": request["expectedAuthorityHead"], "expectedSequence": request["expectedSequence"], "runId": request["runId"]}
    intent = {"operation": "start-run", "protocolRevision": "darwin-fixed-delivery/v2", "request": start_request}
    require(pending.get("requestKeyDigest") == sha256_bytes(request_key.encode("utf-8")), "pending request-key binding drift")
    require(pending.get("requestDigest") == sha256_bytes(canonical_bytes(start_request)), "pending request digest drift")
    require(pending.get("applicationIntentDigest") == sha256_bytes(canonical_bytes(intent)), "pending intent digest drift")
    require(pending.get("deadline") == deadline, "pending deadline drift")
    require(pending.get("ownerFactDigest") == status1["ownerFactDigest"], "pending is not bound to server1 owner")
    require(pending.get("ownerAcquisitionDigest") == sha256_bytes(canonical_bytes(acquisition1)), "pending owner acquisition drift")
    require(receipt.get("pendingDigest") == pending.get("digest"), "receipt does not bind pending")
    require(receipt.get("runId") == args.run_id and receipt.get("attemptId") == attempt_id, "receipt Run/Attempt drift")
    require(receipt.get("postRevision") == current_run.get("sequence") and receipt.get("postAuthorityHead") == current_run.get("authorityHead"), "receipt successor drift")
    require(receipt.get("applicationReceiptFactDigest") == current_run.get("authorityHead"), "receipt application fact drift")
    require(receipt.get("preparationDigest") == prepared.get("preparationDigest"), "receipt preparation drift")
    require(receipt_response == receipt, "replay response receipt differs from durable receipt")

    loss = load_json(os.path.join(evidence, "start-response-loss.json"))
    require(loss.get("disposition") == "response-discarded-at-caller", "start response was not discarded at caller")
    require(
        loss.get("boundary") == "fixed-cli-stdout"
        and loss.get("callerReceivedResponse") is False
        and loss.get("transportResponseDecodedByCLI") is True
        and loss.get("transportResponseLossProven") is False
        and loss.get("readEndClosedBeforeSpawn") is True
        and loss.get("exitCode") != 0,
        "caller-boundary broken pipe was not deterministic",
    )

    audit = load_jsonl(os.path.join(evidence, "command-audit.jsonl"))
    observed_commands = [(entry.get("server"), entry.get("operation"), entry.get("response")) for entry in audit]
    expected_commands = [
        ("server1", "serve", "ready"),
        ("server1", "status", "received"),
        ("server1", "inspect", "received-ready"),
        ("server1", "start", "discarded-at-caller"),
        ("server1", "inspect", "received-running"),
        ("server2", "serve", "ready"),
        ("server2", "status", "received"),
        ("server2", "inspect", "received-recovered"),
        ("server2", "start", "received-replay"),
        ("server2", "inspect", "received-final"),
    ]
    require(observed_commands == expected_commands, f"post-approval command audit drift: {observed_commands}")
    require(all(entry.get("surface") == "control-plane" and entry.get("afterApproval") is True for entry in audit), "CLI fallback present in audit")
    start_audit = [entry for entry in audit if entry.get("operation") == "start"]
    require(len(start_audit) == 2 and all(entry.get("request") == request for entry in start_audit), "start replay did not reuse the exact frozen request")

    summary = {
        "schemaVersion": "marshal.fixed-server-t1-evidence.v1",
        "result": "pass",
        "sourceHead": args.expected_head,
        "binarySHA256": current_sha,
        "cdHash": first_binary["cdHash"],
        "runId": args.run_id,
        "attemptId": attempt_id,
        "attemptsUsed": 1,
        "runSequence": current_run["sequence"],
        "runAuthorityHead": current_run["authorityHead"],
        "ownerEpochs": [status1["ownerEpoch"], status2["ownerEpoch"]],
        "unique": {"reservation": 1, "open": 1, "workerStarted": 1, "spawn": 1, "resume": 1, "recoveryBind": 1, "pending": 1, "receipt": 1},
        "cliFallback": False,
    }
    with open(args.out, "wb") as handle:
        handle.write(canonical_bytes(summary) + b"\n")
    print(json.dumps(summary, ensure_ascii=False, sort_keys=True))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    observe = commands.add_parser("observe-binary")
    observe.add_argument("--binary", required=True)
    observe.add_argument("--codesign", default="/usr/bin/codesign")
    observe.add_argument("--version-json", required=True)
    observe.add_argument("--out", required=True)
    observe.set_defaults(handler=observe_binary)
    verify = commands.add_parser("check")
    verify.add_argument("--repository", required=True)
    verify.add_argument("--evidence-root", required=True)
    verify.add_argument("--binary", required=True)
    verify.add_argument("--expected-head", required=True)
    verify.add_argument("--run-id", required=True)
    verify.add_argument("--out", required=True)
    verify.set_defaults(handler=check)
    args = parser.parse_args()
    try:
        args.handler(args)
    except (EvidenceError, KeyError, OSError, ValueError, subprocess.SubprocessError) as error:
        print(f"fixed-server-t1 evidence: FAIL: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
