#!/usr/bin/env python3
"""Synthetic end-to-end tests for fixed-server-t1-evidence.py."""

import importlib.util
import json
import os
import tempfile
import types
import unittest


HERE = os.path.dirname(os.path.realpath(__file__))
SPEC = importlib.util.spec_from_file_location("fixed_server_t1_evidence", os.path.join(HERE, "fixed-server-t1-evidence.py"))
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


def write_json(path, value):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "wb") as handle:
        handle.write(MODULE.canonical_bytes(value) + b"\n")


def write_jsonl(path, values):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "wb") as handle:
        for value in values:
            handle.write(MODULE.canonical_bytes(value) + b"\n")


def seal(value, field="digest"):
    value[field] = ""
    value[field] = MODULE.sha256_bytes(MODULE.canonical_bytes(value))
    return value


class EvidenceTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        # macOS exposes /var through /private/var; the checker intentionally
        # rejects lexical aliases, so fixtures must use the canonical path.
        self.repository = os.path.realpath(os.path.join(self.temp.name, "repository"))
        self.evidence = os.path.join(self.repository, ".marshal", "fixed-server-t1-canary", "run-t1")
        self.binary = os.path.join(self.repository, "bin", "marshal")
        os.makedirs(os.path.dirname(self.binary), exist_ok=True)
        with open(self.binary, "wb") as handle:
            handle.write(b"fixed marshal exact bytes\n")
        os.chmod(self.binary, 0o755)
        os.makedirs(self.evidence, exist_ok=True)
        self.head = "a" * 40
        self.run_id = "run-t1"
        self.attempt_id = "attempt:t1"
        self._build_fixture()

    def tearDown(self):
        self.temp.cleanup()

    def _build_fixture(self):
        binary_sha = MODULE.sha256_file(self.binary)
        binary_observation = {
            "schemaVersion": "marshal.fixed-server-t1-binary.v1",
            "canonicalPath": self.binary,
            "device": 1,
            "inode": 2,
            "mode": 0o100755,
            "linkCount": 1,
            "size": os.path.getsize(self.binary),
            "rawSHA256": binary_sha,
            "cdHash": "b" * 40,
            "sourceHead": self.head,
            "version": "1.0.0-rc1",
            "selfProfile": "darwin-local-dogfood",
            "os": "darwin",
            "arch": "arm64",
        }
        write_json(os.path.join(self.evidence, "binary-server1.json"), binary_observation)
        write_json(os.path.join(self.evidence, "binary-server2.json"), binary_observation)
        write_json(os.path.join(self.evidence, "activation.json"), {
            "expectedRawSHA256": binary_sha,
            "expectedSourceHead": self.head,
            "canonicalExecutablePath": self.binary,
            "canonicalRepositoryRoot": self.repository,
        })
        ready_line = {"availability": "ready", "protocolRevision": "darwin-fixed-control-endpoint/v1"}
        write_json(os.path.join(self.evidence, "server1-ready.json"), ready_line)
        write_json(os.path.join(self.evidence, "server2-ready.json"), ready_line)

        owner_binary = {
            "canonicalPath": self.binary,
            "device": 1,
            "inode": 2,
            "fileType": "regular",
            "uid": 501,
            "gid": 20,
            "mode": 0o100755,
            "linkCount": 1,
            "size": os.path.getsize(self.binary),
            "rawSHA256": binary_sha,
            "cdHash": "b" * 40,
            "sourceHead": self.head,
            "selfProfile": "darwin-local-dogfood",
        }
        scope = {"authorityNamespaceId": {"authorityScopeId": self.repository, "controlPlaneId": "default", "tenantNamespace": "local"}, "repositoryIdentityDigest": "sha256:" + "c" * 64}
        acquisition1 = {"scope": scope, "ownerEpoch": 7, "ownerUid": 501, "ownerGid": 20, "ownerProcess": {"pid": 1001}, "ownerBinary": owner_binary, "observerIdentity": "test", "observedAt": "2026-09-04T00:00:00Z"}
        acquisition2 = {"scope": scope, "ownerEpoch": 8, "ownerUid": 501, "ownerGid": 20, "ownerProcess": {"pid": 1002}, "ownerBinary": owner_binary, "observerIdentity": "test", "observedAt": "2026-09-04T00:01:00Z"}
        owner1 = seal({"protocolRevision": "control-owner-authority/v1", "factType": "control-owner-acquired", "sequence": 1, "scopeKey": "sha256:" + "d" * 64, "previousOwnerEpoch": 6, "previousOwnerFactDigest": "sha256:" + "e" * 64, "acquisition": acquisition1, "digest": ""})
        owner2 = seal({"protocolRevision": "control-owner-authority/v1", "factType": "control-owner-acquired", "sequence": 2, "scopeKey": "sha256:" + "d" * 64, "previousOwnerEpoch": 7, "previousOwnerFactDigest": owner1["digest"], "acquisition": acquisition2, "digest": ""})
        status_common = {"protocolRevision": "public-application-port/v1", "availability": "ready", "platformProfileId": "darwin-local-dogfood", "agentProvider": "pi", "agentVersion": "0.84.4", "agentClosureProfile": "pi", "agentIdentityDigest": "sha256:" + "f" * 64, "pendingRecovery": 0}
        write_json(os.path.join(self.evidence, "server1-status.json"), dict(status_common, ownerEpoch=7, ownerFactDigest=owner1["digest"]))
        write_json(os.path.join(self.evidence, "server2-status.json"), dict(status_common, ownerEpoch=8, ownerFactDigest=owner2["digest"]))

        ready_head = "sha256:" + "1" * 64
        ready = {"taskId": "task-t1", "runId": self.run_id, "state": "READY", "sequence": 2, "authorityHead": ready_head}
        preparation = {"protocolRevision": "prepared-run-start/v2", "taskId": "task-t1", "runId": self.run_id, "attemptId": self.attempt_id, "reservationFactDigest": "", "attemptOpenedFactDigest": "", "attemptOrdinal": 1, "attemptsUsedBefore": 0, "maxAttempts": 1, "state": "READY", "sequence": 2, "authorityHead": ready_head, "preparationDigest": ""}
        reservation = seal({"factType": "attempt-reserved", "reservation": {"ready": {"runId": self.run_id}, "attemptId": self.attempt_id, "attemptOrdinal": 1}, "digest": ""})
        opened = seal({"factType": "attempt-opened", "attemptKey": "sha256:" + "2" * 64, "transition": {"identity": {"runId": self.run_id, "attemptId": self.attempt_id}}, "digest": ""})
        preparation["reservationFactDigest"] = reservation["digest"]
        preparation["attemptOpenedFactDigest"] = opened["digest"]
        prepared_value = {
            "attemptIdentity": {"runId": self.run_id, "attemptId": self.attempt_id},
            "reservationFactDigest": reservation["digest"],
            "attemptOpenedFactDigest": opened["digest"],
            "attemptOrdinal": 1,
            "attemptsUsedBefore": 0,
            "maxAttempts": 1,
            "preparationDigest": "",
            "other": "fixture",
        }
        prepared_value["preparationDigest"] = MODULE.sha256_bytes(MODULE.canonical_bytes(prepared_value))
        preparation["preparationDigest"] = prepared_value["preparationDigest"]
        spawn = seal({"factType": "process-supervisor-command-outcome", "attemptKey": opened["attemptKey"], "outcome": {"command": "spawn", "disposition": "ok"}, "digest": ""})
        resume = seal({"factType": "process-supervisor-command-outcome", "attemptKey": opened["attemptKey"], "outcome": {"command": "resume", "disposition": "ok"}, "digest": ""})
        process_started = seal({"factType": "process-started", "attemptKey": opened["attemptKey"], "transition": {"identity": {"runId": self.run_id, "attemptId": self.attempt_id}, "supervisorOutcomeFactDigest": spawn["digest"]}, "digest": ""})
        prepared_record = seal({"factType": "prepared-execution-created", "prepared": prepared_value, "digest": ""})
        binding1 = seal({"factType": "control-owner-bound", "attemptKey": opened["attemptKey"], "transition": {"owner": {"ownerEpoch": 7}}, "digest": ""})
        binding2 = seal({"factType": "control-owner-bound", "attemptKey": opened["attemptKey"], "transition": {"owner": {"ownerEpoch": 8}}, "digest": ""})
        initial_bind = seal({"factType": "process-supervisor-command-outcome", "attemptKey": opened["attemptKey"], "outcome": {"command": "bind-authority", "disposition": "ok", "boundAuthorityHead": "sha256:" + "6" * 64}, "digest": ""})
        recovery_bind = seal({"factType": "process-supervisor-command-outcome", "attemptKey": opened["attemptKey"], "outcome": {"command": "bind-authority", "disposition": "ok", "boundAuthorityHead": binding2["digest"]}, "digest": ""})
        ingress = [owner1, reservation, opened, binding1, initial_bind, spawn, resume, process_started, prepared_record, owner2, binding2, recovery_bind]
        write_jsonl(os.path.join(self.repository, ".marshal", "result-ingress", "result-ingress.jsonl"), ingress)

        payload = {
            "protocolRevision": "run-start-outcome/v2",
            "taskId": "task-t1",
            "preparationDigest": preparation["preparationDigest"],
            "attemptOpenedFactDigest": opened["digest"],
            "reservationFactDigest": reservation["digest"],
            "processStartedFactDigest": process_started["digest"],
            "resumeOutcomeFactDigest": resume["digest"],
            "attemptOrdinal": 1,
            "attemptsUsedBefore": 0,
            "maxAttempts": 1,
            "readySequence": 2,
            "readyAuthorityHead": ready_head,
        }
        event1 = {"sequence": 1, "type": "run.created"}
        event2 = {"sequence": 2, "type": "run.ready"}
        event3 = {"runId": self.run_id, "attemptId": self.attempt_id, "sequence": 3, "type": "run.start-outcome", "stateFrom": "READY", "stateTo": "RUNNING", "payload": payload}
        running_head = MODULE.sha256_bytes(MODULE.canonical_bytes(event3))
        running = {"taskId": "task-t1", "runId": self.run_id, "attemptId": self.attempt_id, "state": "RUNNING", "sequence": 3, "authorityHead": running_head}
        write_json(os.path.join(self.evidence, "server1-ready-inspect.json"), ready)
        for name in ("server1-running-inspect.json", "server2-recovered-inspect.json", "server2-final-inspect.json"):
            write_json(os.path.join(self.evidence, name), running)

        run_dir = os.path.join(self.repository, ".marshal", "runs", self.run_id)
        os.makedirs(os.path.join(run_dir, "attempts", self.attempt_id))
        write_json(os.path.join(run_dir, "state.json"), {"runId": self.run_id, "state": "RUNNING", "currentAttemptId": self.attempt_id, "attemptsUsed": 1, "sequence": 3, "baseSha": self.head})
        write_jsonl(os.path.join(run_dir, "events.jsonl"), [event1, event2, event3])

        request_key = "request-t1"
        deadline = "2026-09-04T00:08:00Z"
        request = {"deadline": deadline, "expectedAuthorityHead": ready_head, "expectedSequence": 2, "requestKey": request_key, "runId": self.run_id}
        write_json(os.path.join(self.evidence, "start-request.json"), request)
        start_request = {"expectedAuthorityHead": ready_head, "expectedSequence": 2, "runId": self.run_id}
        pending = seal({"schemaVersion": "fixed-delivery-record/v2", "protocolRevision": "darwin-fixed-delivery/v2", "recordType": "pending", "operation": "start-run", "ownerAcquisitionDigest": MODULE.sha256_bytes(MODULE.canonical_bytes(acquisition1)), "ownerFactDigest": owner1["digest"], "repositoryDigest": "sha256:" + "3" * 64, "namespaceDigest": "sha256:" + "4" * 64, "authorityRootDigest": "sha256:" + "5" * 64, "requestKeyDigest": MODULE.sha256_bytes(request_key.encode()), "requestDigest": MODULE.sha256_bytes(MODULE.canonical_bytes(start_request)), "applicationIntentDigest": MODULE.sha256_bytes(MODULE.canonical_bytes({"operation": "start-run", "protocolRevision": "darwin-fixed-delivery/v2", "request": start_request})), "deadline": deadline, "digest": ""})
        receipt = seal({"schemaVersion": "fixed-delivery-record/v2", "protocolRevision": "darwin-fixed-delivery/v2", "recordType": "receipt-ref", "operation": "start-run", "pendingDigest": pending["digest"], "preparationDigest": preparation["preparationDigest"], "applicationReceiptFactDigest": running_head, "runId": self.run_id, "attemptId": self.attempt_id, "postRevision": 3, "postAuthorityHead": running_head, "digest": ""})
        delivery = os.path.join(self.repository, ".marshal", "runtime-v1", "control", "delivery-v1")
        write_json(os.path.join(delivery, "p-" + pending["requestKeyDigest"].split(":", 1)[1] + ".json"), pending)
        write_json(os.path.join(delivery, "r-" + pending["digest"].split(":", 1)[1] + ".json"), receipt)
        write_json(os.path.join(self.evidence, "server2-start-replay.json"), {"Projection": {"prepared": preparation, "run": running}, "Receipt": receipt})
        write_json(os.path.join(self.evidence, "start-response-loss.json"), {
            "boundary": "fixed-cli-stdout",
            "callerReceivedResponse": False,
            "disposition": "response-discarded-at-caller",
            "exitCode": -13,
            "failureMode": "SIGPIPE",
            "readEndClosedBeforeSpawn": True,
            "transportResponseDecodedByCLI": True,
            "transportResponseLossProven": False,
        })
        write_json(os.path.join(self.evidence, "server1-process.json"), {"pid": 1001, "stop": "SIGKILL", "waitStatus": 137})
        write_json(os.path.join(self.evidence, "server2-process.json"), {"pid": 1002, "stop": "SIGTERM", "waitStatus": 0})
        audit_tuples = [
            ("server1", "serve", "ready"), ("server1", "status", "received"),
            ("server1", "inspect", "received-ready"), ("server1", "start", "discarded-at-caller"),
            ("server1", "inspect", "received-running"), ("server2", "serve", "ready"),
            ("server2", "status", "received"), ("server2", "inspect", "received-recovered"),
            ("server2", "start", "received-replay"), ("server2", "inspect", "received-final"),
        ]
        audit = []
        for server, operation, response in audit_tuples:
            entry = {"afterApproval": True, "operation": operation, "response": response,
                     "server": server, "surface": "control-plane"}
            if operation == "start":
                entry["request"] = request
            audit.append(entry)
        write_jsonl(os.path.join(self.evidence, "command-audit.jsonl"), audit)

    def args(self):
        return types.SimpleNamespace(repository=self.repository, evidence_root=self.evidence, binary=self.binary, expected_head=self.head, run_id=self.run_id, out=os.path.join(self.evidence, "summary.json"))

    def test_complete_fixture_passes(self):
        MODULE.check(self.args())
        self.assertEqual(MODULE.load_json(self.args().out)["result"], "pass")

    def test_second_run_fails_closed(self):
        os.makedirs(os.path.join(self.repository, ".marshal", "runs", "run-second"))
        with self.assertRaisesRegex(MODULE.EvidenceError, "second Run"):
            MODULE.check(self.args())

    def test_duplicate_spawn_fails_closed(self):
        path = os.path.join(self.repository, ".marshal", "result-ingress", "result-ingress.jsonl")
        records = MODULE.load_jsonl(path)
        spawn = next(record for record in records if (record.get("outcome") or {}).get("command") == "spawn")
        records.append(spawn)
        write_jsonl(path, records)
        with self.assertRaisesRegex(MODULE.EvidenceError, "one successful spawn"):
            MODULE.check(self.args())

    def test_second_pending_fails_closed(self):
        delivery = os.path.join(self.repository, ".marshal", "runtime-v1", "control", "delivery-v1")
        write_json(os.path.join(delivery, "p-" + "9" * 64 + ".json"), {"fixture": True})
        with self.assertRaisesRegex(MODULE.EvidenceError, "delivery set"):
            MODULE.check(self.args())

    def test_cli_fallback_audit_fails_closed(self):
        path = os.path.join(self.evidence, "command-audit.jsonl")
        audit = MODULE.load_jsonl(path)
        audit[4]["surface"] = "direct-cli"
        write_jsonl(path, audit)
        with self.assertRaisesRegex(MODULE.EvidenceError, "CLI fallback"):
            MODULE.check(self.args())

    def test_transport_loss_overclaim_fails_closed(self):
        path = os.path.join(self.evidence, "start-response-loss.json")
        loss = MODULE.load_json(path)
        loss["transportResponseLossProven"] = True
        write_json(path, loss)
        with self.assertRaisesRegex(MODULE.EvidenceError, "caller-boundary broken pipe"):
            MODULE.check(self.args())


if __name__ == "__main__":
    unittest.main()
