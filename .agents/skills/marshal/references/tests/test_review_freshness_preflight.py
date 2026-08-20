#!/usr/bin/env python3

from __future__ import annotations

import hashlib
import importlib.util
import json
from pathlib import Path
import shutil
import subprocess
import tempfile
import threading
import unittest


HERE = Path(__file__).resolve().parent
REFERENCES = HERE.parent
REPOSITORY = REFERENCES.parents[3]
EXAMPLES = REPOSITORY / "schemas" / "examples" / "happy-path"
VALIDATOR = REFERENCES / "validate-review-freshness-preflight.py"
CORE = HERE / "review_freshness_core_probe.go"
PREFLIGHT_SPEC = importlib.util.spec_from_file_location("review_freshness_preflight", VALIDATOR)
assert PREFLIGHT_SPEC is not None and PREFLIGHT_SPEC.loader is not None
PREFLIGHT = importlib.util.module_from_spec(PREFLIGHT_SPEC)
PREFLIGHT_SPEC.loader.exec_module(PREFLIGHT)


def digest_bytes(data: bytes) -> str:
    return "sha256:" + hashlib.sha256(data).hexdigest()


def json_bytes(value: object) -> bytes:
    return json.dumps(value, ensure_ascii=False, indent=2).encode() + b"\n"


class ReviewFreshnessPreflightTest(unittest.TestCase):
    maxDiff = None
    @classmethod
    def setUpClass(cls) -> None:
        cls.core_build_dir = Path(tempfile.mkdtemp(prefix="review-freshness-core.", dir="/private/tmp"))
        cls.core_binary = cls.core_build_dir / "probe"
        subprocess.run(["go", "build", "-o", str(cls.core_binary), str(CORE)], cwd=REPOSITORY, check=True)
        cls.core_process = subprocess.Popen([str(cls.core_binary), "serve"], cwd=REPOSITORY, text=True, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE)

    @classmethod
    def tearDownClass(cls) -> None:
        if cls.core_process.stdin is not None:
            cls.core_process.stdin.close()
        cls.core_process.wait(timeout=5)
        shutil.rmtree(cls.core_build_dir)

    def setUp(self) -> None:
        self.temp = Path(tempfile.mkdtemp(prefix="review-freshness.", dir="/private/tmp"))
        self.run_root = self.temp / "run"
        self.operator_root = self.temp / "operator"
        self.worktree = self.temp / "worktree"
        self.run_root.mkdir(); self.operator_root.mkdir(); self.worktree.mkdir()
        subprocess.run(["/usr/bin/git", "init", "-q"], cwd=self.worktree, check=True)
        subprocess.run(["/usr/bin/git", "config", "user.name", "Fixture"], cwd=self.worktree, check=True)
        subprocess.run(["/usr/bin/git", "config", "user.email", "fixture@example.invalid"], cwd=self.worktree, check=True)
        (self.worktree / "README").write_text("fixture\n", encoding="utf-8")
        subprocess.run(["/usr/bin/git", "add", "README"], cwd=self.worktree, check=True)
        subprocess.run(["/usr/bin/git", "-c", "core.hooksPath=/dev/null", "commit", "-qm", "fixture"], cwd=self.worktree, check=True)
        self.head = subprocess.check_output(["/usr/bin/git", "rev-parse", "HEAD"], cwd=self.worktree, text=True).strip()
        self.manifest_path = self.operator_root / "manifest.json"
        self._write_fixture()

    def tearDown(self) -> None:
        shutil.rmtree(self.temp)

    def core_digest(self, value: object, kind: str | None = None) -> str:
        path = self.temp / "digest.json"
        path.write_bytes(json_bytes(value))
        mode = "contract" if kind else "canonical"
        self.assertIsNotNone(self.core_process.stdin); self.assertIsNotNone(self.core_process.stdout)
        self.core_process.stdin.write(json.dumps({"mode": mode, "kindOrSchema": kind or "-", "path": str(path)}, separators=(",", ":")) + "\n")
        self.core_process.stdin.flush()
        response = json.loads(self.core_process.stdout.readline())
        self.assertTrue(response["ok"], response)
        return response["digest"]

    def core_observe(self) -> dict:
        self.assertIsNotNone(self.core_process.stdin); self.assertIsNotNone(self.core_process.stdout)
        request = {"mode": "observe", "kindOrSchema": self.head, "path": str(self.worktree)}
        self.core_process.stdin.write(json.dumps(request, separators=(",", ":")) + "\n")
        self.core_process.stdin.flush()
        response = json.loads(self.core_process.stdout.readline())
        self.assertTrue(response["ok"], response)
        return json.loads(response["digest"])

    def observed_patch(self) -> bytes:
        environment = {"PATH": "/usr/bin:/bin", "LC_ALL": "C", "GIT_OPTIONAL_LOCKS": "0"}
        patch = subprocess.check_output(
            ["/usr/bin/git", "diff", "--no-ext-diff", "--no-textconv", "--binary", "--full-index", "--find-renames", self.head, "--"],
            cwd=self.worktree, env=environment,
        )
        untracked = subprocess.check_output(
            ["/usr/bin/git", "ls-files", "--others", "--exclude-standard", "-z"],
            cwd=self.worktree, env=environment,
        )
        for relative in sorted(path.decode() for path in untracked.split(b"\x00") if path):
            result = subprocess.run(
                ["/usr/bin/git", "diff", "--no-index", "--no-ext-diff", "--no-textconv", "--binary", "--full-index", "--", "/dev/null", relative],
                cwd=self.worktree, env=environment, stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False,
            )
            self.assertEqual(result.returncode, 1, result.stderr.decode(errors="replace"))
            patch += result.stdout
        return patch

    def example(self, name: str) -> dict:
        return json.loads((EXAMPLES / f"{name}.json").read_text())

    def _write_fixture(self) -> None:
        task = self.example("task-spec")
        task["metadata"]["id"] = "REVIEW-FRESHNESS-FIXTURE"
        task_digest = self.core_digest(task, "Task")
        policy = self.example("policy-snapshot")
        policy["taskId"] = "REVIEW-FRESHNESS-FIXTURE"; policy["runId"] = "review-freshness-fixture-r1"
        policy_digest = self.core_digest(policy, "PolicySnapshot")
        capability = self.example("capability-snapshot")
        capability_digest = self.core_digest(capability, "CapabilitySnapshot")
        empty_patch = b""
        empty_diff = digest_bytes(empty_patch)
        # verification.Observe marshals a nil change slice as JSON null.
        empty_snapshot = digest_bytes(b"null")
        worker = self.example("worker-result")
        worker["taskId"] = "REVIEW-FRESHNESS-FIXTURE"; worker["runId"] = "review-freshness-fixture-r1"; worker["attemptId"] = "attempt:fixture-01"
        worker_digest = self.core_digest(worker, "WorkerResult")
        report = self.example("verification-report")
        report.update({"taskId": "REVIEW-FRESHNESS-FIXTURE", "runId": "review-freshness-fixture-r1", "specDigest": task_digest, "baseSha": self.head})
        report["observed"] = {"snapshotDigest": empty_snapshot, "diffDigest": empty_diff, "changedFiles": [], "changedFileCount": 0, "diffBytes": 0, "hasUntrackedFiles": False}
        report_digest = self.core_digest(report, "VerificationReport")
        artifacts = self.example("artifact-manifest")
        artifacts["taskId"] = "REVIEW-FRESHNESS-FIXTURE"; artifacts["runId"] = "review-freshness-fixture-r1"
        artifacts["artifacts"] = [{"id": "evidence:observed-patch", "kind": "patch", "mediaType": "text/x-diff", "producer": "verifier", "required": True, "status": "validated", "pathRoot": "run", "relativePath": "observed.patch", "byteSize": 0, "digest": empty_diff, "createdAt": "2026-08-20T00:01:00Z", "redacted": False, "truncated": False, "relatedGates": ["scope"]}]
        artifact_digest = self.core_digest(artifacts, "ArtifactManifest")
        evidence = {"specDigest": task_digest, "patchDigest": empty_diff, "verificationDigest": report_digest, "artifactManifestDigest": artifact_digest, "workerResultDigests": [worker_digest], "previousBlockingFindings": []}
        evidence_digest = self.core_digest(evidence)
        packet = {"apiVersion": "marshal.dev/v1alpha1", "kind": "ReviewPacket", "taskId": "REVIEW-FRESHNESS-FIXTURE", "runId": "review-freshness-fixture-r1", "reviewRound": 2, "specDigest": task_digest, "baseSha": self.head, "snapshotDigest": empty_snapshot, "diffDigest": empty_diff, "verificationDigest": report_digest, "artifactManifestDigest": artifact_digest, "workerResultDigests": [worker_digest], "evidenceDigest": evidence_digest, "inputs": {"taskSpec": "task-spec.json", "patch": "observed.patch", "verificationReport": "verification-report.json", "artifactManifest": "artifact-manifest.json", "workerResults": ["attempts/attempt:fixture-01/worker-result.json"]}, "previousBlockingFindings": [], "generatedAt": report["completedAt"]}
        self.core_digest(packet, "ReviewPacket")
        state = {"apiVersion": "marshal.dev/v1alpha1", "kind": "RunState", "taskId": "REVIEW-FRESHNESS-FIXTURE", "runId": "review-freshness-fixture-r1", "state": "REVIEW_PENDING", "sequence": 7, "specDigest": task_digest, "policyDigest": policy_digest, "capabilityDigest": capability_digest, "baseSha": self.head, "worktreePath": str(self.worktree), "currentAttemptId": "attempt:fixture-01", "reviewRound": 2, "attemptsUsed": 1, "operationalRetriesUsed": 0, "reworkRoundsUsed": 1, "createdAt": "2026-08-20T00:00:00Z", "updatedAt": "2026-08-20T00:01:00Z"}
        self.core_digest(state, "RunState")
        events = []
        for sequence in range(1, 7):
            events.append({"apiVersion": "marshal.dev/v1alpha1", "kind": "RunEvent", "eventId": f"event:fixture-{sequence}", "runId": "review-freshness-fixture-r1", "sequence": sequence, "type": "fixture.step", "timestamp": f"2026-08-20T00:00:0{sequence}Z", "payload": {}})
        events.append({"apiVersion": "marshal.dev/v1alpha1", "kind": "RunEvent", "eventId": "event:fixture-7", "runId": "review-freshness-fixture-r1", "sequence": 7, "type": "verification.completed", "stateFrom": "VERIFYING", "stateTo": "REVIEW_PENDING", "timestamp": report["completedAt"], "actor": {"type": "system", "id": "marshal-verifier"}, "payload": {"reportDigest": report_digest, "artifactManifestDigest": artifact_digest, "status": report["status"]}})
        paths = {"task-spec.json": task, "policy-snapshot.json": policy, "capability-snapshot.json": capability, "verification-report.json": report, "artifact-manifest.json": artifacts, "review-packet.json": packet, "state.json": state}
        for name, value in paths.items():
            (self.run_root / name).write_bytes(json_bytes(value))
        worker_path = self.run_root / "attempts" / "attempt:fixture-01" / "worker-result.json"
        worker_path.parent.mkdir(parents=True, exist_ok=True); worker_path.write_bytes(json_bytes(worker))
        (self.run_root / "observed.patch").write_bytes(empty_patch)
        (self.run_root / "events.jsonl").write_bytes(b"".join(json.dumps(event, separators=(",", ":")).encode() + b"\n" for event in events))
        (self.run_root / "control").mkdir(exist_ok=True); (self.run_root / "control" / "records.jsonl").write_bytes(b"")
        (self.operator_root / "review-freshness-history.json").write_bytes(json_bytes({"apiVersion": "marshal.operator/v1alpha1", "kind": "ReviewFreshnessHistory", "claims": []}))

    def manifest(self) -> dict:
        return {"apiVersion": "marshal.operator/v1alpha1", "kind": "ReviewFreshnessPreflight", "expected": {"taskId": "REVIEW-FRESHNESS-FIXTURE", "runId": "review-freshness-fixture-r1", "state": "REVIEW_PENDING", "stateSequence": 7, "currentAttemptId": "attempt:fixture-01", "sourceHead": self.head, "baseSha": self.head, "reviewRound": 2}, "files": {"statePath": "state.json", "eventsPath": "events.jsonl", "packetPath": "review-packet.json", "taskSpecPath": "task-spec.json", "verificationReportPath": "verification-report.json", "artifactManifestPath": "artifact-manifest.json", "policySnapshotPath": "policy-snapshot.json", "capabilitySnapshotPath": "capability-snapshot.json", "controlRecordsPath": "control/records.jsonl", "historyPath": "review-freshness-history.json"}}

    def enable_candidate(self) -> None:
        candidate = {"apiVersion": "marshal.dev/v1alpha1", "kind": "Candidate", "taskId": "REVIEW-FRESHNESS-FIXTURE", "runId": "review-freshness-fixture-r1", "attemptId": "attempt:fixture-01", "authorityNamespaceId": "authority:fixture", "baseSha": self.head, "contentDigest": digest_bytes(b""), "producerKind": "worker", "producer": "worker:fixture", "createdAt": "2026-08-20T00:00:30Z"}
        candidate_digest = self.core_digest(candidate)
        candidate["candidateDigest"] = candidate_digest
        self.core_digest(candidate, "Candidate")
        candidate_dir = self.run_root / "candidates"; candidate_dir.mkdir(exist_ok=True); (candidate_dir / f"{candidate_digest}.json").write_bytes(json_bytes(candidate))
        report_path = self.run_root / "verification-report.json"; report = json.loads(report_path.read_text()); report["candidateDigest"] = candidate_digest; report["workerCandidateDigest"] = candidate_digest; report_path.write_bytes(json_bytes(report)); report_digest = self.core_digest(report, "VerificationReport")
        artifact_path = self.run_root / "artifact-manifest.json"; artifacts = json.loads(artifact_path.read_text()); artifacts["artifacts"][0]["candidateDigest"] = candidate_digest; artifact_path.write_bytes(json_bytes(artifacts)); artifact_digest = self.core_digest(artifacts, "ArtifactManifest")
        packet_path = self.run_root / "review-packet.json"; packet = json.loads(packet_path.read_text()); packet["candidateDigest"] = candidate_digest; packet["workerCandidateDigest"] = candidate_digest; packet["verificationDigest"] = report_digest; packet["artifactManifestDigest"] = artifact_digest
        evidence = {"specDigest": packet["specDigest"], "patchDigest": packet["diffDigest"], "verificationDigest": report_digest, "artifactManifestDigest": artifact_digest, "workerResultDigests": packet["workerResultDigests"], "previousBlockingFindings": [], "candidateDigest": candidate_digest, "workerCandidateDigest": candidate_digest}
        packet["evidenceDigest"] = self.core_digest(evidence); packet_path.write_bytes(json_bytes(packet)); self.core_digest(packet, "ReviewPacket")
        events_path = self.run_root / "events.jsonl"
        events = [json.loads(line) for line in events_path.read_text().splitlines()]
        events[-1]["payload"]["reportDigest"] = report_digest
        events[-1]["payload"]["artifactManifestDigest"] = artifact_digest
        events_path.write_bytes(b"".join(json.dumps(event, separators=(",", ":")).encode() + b"\n" for event in events))

    def enable_missing_packet_candidate(self, *, untracked: bool) -> None:
        (self.run_root / "review-packet.json").unlink()
        if untracked:
            (self.worktree / "deliverable.md").write_text("candidate deliverable\n", encoding="utf-8")
        else:
            (self.worktree / "README").write_text("tracked candidate\n", encoding="utf-8")
        observation = self.core_observe()
        patch = self.observed_patch()
        self.assertEqual(observation["diffDigest"], digest_bytes(patch))
        (self.run_root / "observed.patch").write_bytes(patch)

        candidate = {
            "apiVersion": "marshal.dev/v1alpha1", "kind": "Candidate",
            "taskId": "REVIEW-FRESHNESS-FIXTURE", "runId": "review-freshness-fixture-r1",
            "attemptId": "attempt:fixture-01", "authorityNamespaceId": "authority:fixture",
            "baseSha": self.head, "contentDigest": observation["diffDigest"],
            "producerKind": "worker", "producer": "worker:fixture",
            "createdAt": "2026-08-20T00:00:30Z",
        }
        candidate_digest = self.core_digest(candidate)
        candidate["candidateDigest"] = candidate_digest
        self.core_digest(candidate, "Candidate")
        candidate_dir = self.run_root / "candidates"
        candidate_dir.mkdir(exist_ok=True)
        (candidate_dir / f"{candidate_digest}.json").write_bytes(json_bytes(candidate))

        report_path = self.run_root / "verification-report.json"
        report = json.loads(report_path.read_text())
        report["observed"] = observation
        report["candidateDigest"] = candidate_digest
        report["workerCandidateDigest"] = candidate_digest
        report_path.write_bytes(json_bytes(report))
        report_digest = self.core_digest(report, "VerificationReport")

        artifact_path = self.run_root / "artifact-manifest.json"
        artifacts = json.loads(artifact_path.read_text())
        artifacts["artifacts"][0].update({
            "byteSize": len(patch), "digest": observation["diffDigest"],
            "candidateDigest": candidate_digest,
        })
        artifact_path.write_bytes(json_bytes(artifacts))
        artifact_digest = self.core_digest(artifacts, "ArtifactManifest")

        events_path = self.run_root / "events.jsonl"
        events = [json.loads(line) for line in events_path.read_text().splitlines()]
        events[-1]["payload"]["reportDigest"] = report_digest
        events[-1]["payload"]["artifactManifestDigest"] = artifact_digest
        events_path.write_bytes(b"".join(json.dumps(event, separators=(",", ":")).encode() + b"\n" for event in events))

    def make_previous_round_stale_packet(self) -> dict:
        for relative in ("review-packets", "decisions", "candidates", "attempts/attempt:fixture-02"):
            path = self.run_root / relative
            if path.exists():
                shutil.rmtree(path)
        packet_path = self.run_root / "review-packet.json"
        stale_packet = json.loads(packet_path.read_text())
        stale_packet["reviewRound"] = 1
        stale_packet_raw = json_bytes(stale_packet)
        stale_packet_digest = self.core_digest(stale_packet, "ReviewPacket")
        packet_path.write_bytes(stale_packet_raw)
        archive_dir = self.run_root / "review-packets"
        archive_dir.mkdir()
        (archive_dir / "packet-001.json").write_bytes(stale_packet_raw)

        decision = self.example("review-decision")
        decision.update({
            "taskId": stale_packet["taskId"], "runId": stale_packet["runId"], "reviewRound": 1,
            "specDigest": stale_packet["specDigest"], "reviewPacketDigest": stale_packet_digest,
            "verificationDigest": stale_packet["verificationDigest"],
            "artifactManifestDigest": stale_packet["artifactManifestDigest"],
            "evidenceDigest": stale_packet["evidenceDigest"], "verdict": "rework",
            "blockingFindings": [], "nonBlockingFindings": [],
            "publicationRecommendation": "do-not-publish", "mergeRecommendation": "do-not-merge",
        })
        decision_digest = self.core_digest(decision, "ReviewDecision")
        decision_dir = self.run_root / "decisions"
        decision_dir.mkdir()
        (decision_dir / "decision-001.json").write_bytes(json_bytes(decision))

        prior_worker_path = self.run_root / "attempts" / "attempt:fixture-01" / "worker-result.json"
        current_worker = json.loads(prior_worker_path.read_text())
        current_worker["attemptId"] = "attempt:fixture-02"
        current_worker_path = self.run_root / "attempts" / "attempt:fixture-02" / "worker-result.json"
        current_worker_path.parent.mkdir()
        current_worker_path.write_bytes(json_bytes(current_worker))

        report_path = self.run_root / "verification-report.json"
        report = json.loads(report_path.read_text())
        report["summary"] = "current round verification"
        report["completedAt"] = "2026-08-20T00:02:00Z"
        candidate = {
            "apiVersion": "marshal.dev/v1alpha1", "kind": "Candidate",
            "taskId": stale_packet["taskId"], "runId": stale_packet["runId"],
            "attemptId": "attempt:fixture-02", "authorityNamespaceId": "authority:fixture",
            "baseSha": self.head, "contentDigest": digest_bytes(b""), "producerKind": "worker",
            "producer": "worker:fixture", "createdAt": "2026-08-20T00:01:30Z",
        }
        candidate_digest = self.core_digest(candidate)
        candidate["candidateDigest"] = candidate_digest
        self.core_digest(candidate, "Candidate")
        candidate_dir = self.run_root / "candidates"
        candidate_dir.mkdir()
        (candidate_dir / f"{candidate_digest}.json").write_bytes(json_bytes(candidate))
        report["candidateDigest"] = candidate_digest
        report["workerCandidateDigest"] = candidate_digest
        report_path.write_bytes(json_bytes(report))
        current_report_digest = self.core_digest(report, "VerificationReport")

        artifact_path = self.run_root / "artifact-manifest.json"
        artifacts = json.loads(artifact_path.read_text())
        artifacts["artifacts"][0]["candidateDigest"] = candidate_digest
        artifact_path.write_bytes(json_bytes(artifacts))
        current_artifact_digest = self.core_digest(artifacts, "ArtifactManifest")

        state_path = self.run_root / "state.json"
        state = json.loads(state_path.read_text())
        state.update({"sequence": 9, "currentAttemptId": "attempt:fixture-02", "reviewRound": 2, "attemptsUsed": 2, "reworkRoundsUsed": 1})
        state_path.write_bytes(json_bytes(state))
        self.core_digest(state, "RunState")

        events = []
        for sequence in range(1, 5):
            events.append({"apiVersion": "marshal.dev/v1alpha1", "kind": "RunEvent", "eventId": f"event:fixture-{sequence}", "runId": stale_packet["runId"], "sequence": sequence, "type": "fixture.step", "timestamp": f"2026-08-20T00:00:0{sequence}Z", "payload": {}})
        events.extend([
            {"apiVersion": "marshal.dev/v1alpha1", "kind": "RunEvent", "eventId": "event:fixture-5", "runId": stale_packet["runId"], "sequence": 5, "type": "verification.completed", "stateFrom": "VERIFYING", "stateTo": "REVIEW_PENDING", "timestamp": "2026-08-20T00:00:05Z", "actor": {"type": "system", "id": "marshal-verifier"}, "payload": {"reportDigest": stale_packet["verificationDigest"], "artifactManifestDigest": stale_packet["artifactManifestDigest"], "status": "fail"}},
            {"apiVersion": "marshal.dev/v1alpha1", "kind": "RunEvent", "eventId": "event:fixture-6", "runId": stale_packet["runId"], "sequence": 6, "type": "review.rework", "stateFrom": "REVIEW_PENDING", "stateTo": "REWORK_REQUESTED", "timestamp": "2026-08-20T00:00:06Z", "actor": {"type": "system", "id": "marshal-review"}, "payload": {"decisionDigest": decision_digest, "evidenceDigest": stale_packet["evidenceDigest"], "verdict": "rework"}},
            {"apiVersion": "marshal.dev/v1alpha1", "kind": "RunEvent", "eventId": "event:fixture-7", "runId": stale_packet["runId"], "sequence": 7, "type": "worker.started", "stateFrom": "REWORK_REQUESTED", "stateTo": "RUNNING", "timestamp": "2026-08-20T00:01:00Z", "attemptId": "attempt:fixture-02", "actor": {"type": "system", "id": "marshal-worker-runner"}, "payload": {"adapterId": "fake", "fencingGeneration": 2}},
            {"apiVersion": "marshal.dev/v1alpha1", "kind": "RunEvent", "eventId": "event:fixture-8", "runId": stale_packet["runId"], "sequence": 8, "type": "worker.completed", "stateFrom": "RUNNING", "stateTo": "VERIFYING", "timestamp": "2026-08-20T00:01:30Z", "attemptId": "attempt:fixture-02", "actor": {"type": "system", "id": "marshal-worker-runner"}, "payload": {"diffDigest": digest_bytes(b""), "snapshotDigest": digest_bytes(b"null")}},
            {"apiVersion": "marshal.dev/v1alpha1", "kind": "RunEvent", "eventId": "event:fixture-9", "runId": stale_packet["runId"], "sequence": 9, "type": "verification.completed", "stateFrom": "VERIFYING", "stateTo": "REVIEW_PENDING", "timestamp": "2026-08-20T00:02:00Z", "actor": {"type": "system", "id": "marshal-verifier"}, "payload": {"reportDigest": current_report_digest, "artifactManifestDigest": current_artifact_digest, "status": report["status"]}},
        ])
        (self.run_root / "events.jsonl").write_bytes(b"".join(json.dumps(event, separators=(",", ":")).encode() + b"\n" for event in events))
        manifest = self.manifest()
        manifest["expected"].update({"stateSequence": 9, "currentAttemptId": "attempt:fixture-02", "reviewRound": 2})
        return manifest

    def insert_round_repair_audit(self, manifest: dict, mutate=None) -> None:
        events_path = self.run_root / "events.jsonl"
        events = [json.loads(line) for line in events_path.read_text().splitlines()]
        repair = {
            "apiVersion": "marshal.dev/v1alpha1", "kind": "RunEvent",
            "eventId": "event:fixture-repair-7", "runId": "review-freshness-fixture-r1",
            "sequence": 7, "type": "reconciliation.snapshot-repaired",
            "stateFrom": "REWORK_REQUESTED", "stateTo": "REWORK_REQUESTED",
            "timestamp": "2026-08-20T00:00:30Z",
            "actor": {"type": "system", "id": "marshal-reconciliation"},
            "payload": {"repairKind": "snapshot-rebuild", "sourceJournalSequence": 6},
        }
        if mutate is not None:
            mutate(repair)
        for event in events[6:]:
            event["sequence"] += 1
            event["eventId"] += "-shifted"
        events.insert(6, repair)
        events_path.write_bytes(b"".join(json.dumps(event, separators=(",", ":")).encode() + b"\n" for event in events))
        state_path = self.run_root / "state.json"
        state = json.loads(state_path.read_text()); state["sequence"] = 10; state_path.write_bytes(json_bytes(state))
        manifest["expected"]["stateSequence"] = 10

    def invoke(self, manifest: dict | None = None) -> tuple[int, dict]:
        self.manifest_path.write_bytes(json_bytes(manifest or self.manifest()))
        result = subprocess.run(["python3", "-I", "-B", str(VALIDATOR), "--run-root", str(self.run_root), "--operator-root", str(self.operator_root), "--manifest", str(self.manifest_path), "--worktree", str(self.worktree)], cwd=REPOSITORY, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False)
        self.assertEqual(result.stderr, "")
        return result.returncode, json.loads(result.stdout)

    def assert_reason(self, reason: str, manifest: dict | None = None) -> None:
        code, result = self.invoke(manifest)
        self.assertEqual(code, 2, result); self.assertEqual(result["reasonCode"], reason)

    def test_fresh_packet_is_atomically_claimed(self) -> None:
        code, result = self.invoke()
        self.assertEqual(code, 0, result); self.assertEqual(result["action"], "dispatch-reviewer"); self.assertTrue(result["historyClaimed"])
        history = json.loads((self.operator_root / "review-freshness-history.json").read_text())
        self.assertEqual(len(history["claims"]), 1); self.assertEqual(history["claims"][0]["dedupeKey"], result["dedupeKey"])

    def test_skill_e2e_documents_exact_command_and_branching(self) -> None:
        skill = (REFERENCES.parent / "SKILL.md").read_text(encoding="utf-8")
        for anchor in ("validate-review-freshness-preflight.py", "historyClaimed=true", "action=dispatch-reviewer", "action=generate-review-packet", "reasonCode"):
            self.assertIn(anchor, skill)
        code, result = self.invoke(); self.assertEqual(code, 0, result); self.assertTrue(result["historyClaimed"])

    def test_complete_candidate_packet_is_accepted(self) -> None:
        self.enable_candidate()
        code, result = self.invoke(); self.assertEqual(code, 0, result); self.assertEqual(result["action"], "dispatch-reviewer")

    def test_candidate_producer_and_body_tampering_use_domain_authority(self) -> None:
        for field, value in (("producer", "worker:tampered"), ("contentDigest", "sha256:" + "f" * 64)):
            with self.subTest(field=field):
                self._write_fixture(); self.enable_candidate()
                candidate_dir = self.run_root / "candidates"
                path = next(candidate_dir.iterdir())
                candidate = json.loads(path.read_text()); candidate[field] = value
                path.write_bytes(json_bytes(candidate))
                self.assert_reason("candidate-record-core-invalid")

    def test_present_packet_dispatch_is_claimed_once(self) -> None:
        code, _ = self.invoke(); self.assertEqual(code, 0)
        self.assert_reason("action-already-claimed")

    def test_missing_packet_generation_is_claimed_once(self) -> None:
        (self.run_root / "review-packet.json").unlink()
        code, result = self.invoke(); self.assertEqual(code, 0, result); self.assertEqual(result["action"], "generate-review-packet")
        self.assert_reason("action-already-claimed")

    def test_missing_packet_tracked_candidate_claims_generation(self) -> None:
        self.enable_missing_packet_candidate(untracked=False)
        code, result = self.invoke()
        self.assertEqual(code, 0, result)
        self.assertEqual(result["action"], "generate-review-packet")
        self.assertEqual(result["reasonCode"], "packet-missing-generation-claimed")
        self.assertTrue(result["historyClaimed"])

    def test_missing_packet_untracked_candidate_claims_generation(self) -> None:
        self.enable_missing_packet_candidate(untracked=True)
        code, result = self.invoke()
        self.assertEqual(code, 0, result)
        self.assertEqual(result["action"], "generate-review-packet")
        self.assertTrue(result["historyClaimed"])

    def test_missing_packet_candidate_rejects_extra_worktree_drift(self) -> None:
        self.enable_missing_packet_candidate(untracked=True)
        (self.worktree / "extra.txt").write_text("not verified\n", encoding="utf-8")
        self.assert_reason("worktree-evidence-changed-after-verification")
        history = json.loads((self.operator_root / "review-freshness-history.json").read_text())
        self.assertEqual(history["claims"], [])

    def test_missing_packet_candidate_rejects_identity_mismatch(self) -> None:
        self.enable_missing_packet_candidate(untracked=True)
        candidate_path = next((self.run_root / "candidates").iterdir())
        candidate = json.loads(candidate_path.read_text())
        candidate["attemptId"] = "attempt:other"
        del candidate["candidateDigest"]
        candidate_digest = self.core_digest(candidate)
        candidate["candidateDigest"] = candidate_digest
        self.core_digest(candidate, "Candidate")
        candidate_path.unlink()
        (candidate_path.parent / f"{candidate_digest}.json").write_bytes(json_bytes(candidate))

        report_path = self.run_root / "verification-report.json"
        report = json.loads(report_path.read_text())
        report["candidateDigest"] = candidate_digest
        report["workerCandidateDigest"] = candidate_digest
        report_path.write_bytes(json_bytes(report))
        report_digest = self.core_digest(report, "VerificationReport")
        artifact_path = self.run_root / "artifact-manifest.json"
        artifacts = json.loads(artifact_path.read_text())
        artifacts["artifacts"][0]["candidateDigest"] = candidate_digest
        artifact_path.write_bytes(json_bytes(artifacts))
        artifact_digest = self.core_digest(artifacts, "ArtifactManifest")
        events_path = self.run_root / "events.jsonl"
        events = [json.loads(line) for line in events_path.read_text().splitlines()]
        events[-1]["payload"]["reportDigest"] = report_digest
        events[-1]["payload"]["artifactManifestDigest"] = artifact_digest
        events_path.write_bytes(b"".join(json.dumps(event, separators=(",", ":")).encode() + b"\n" for event in events))

        self.assert_reason("candidate-record-identity-mismatch")
        history = json.loads((self.operator_root / "review-freshness-history.json").read_text())
        self.assertEqual(history["claims"], [])

    def test_missing_packet_candidate_concurrent_claim_is_exactly_once(self) -> None:
        self.enable_missing_packet_candidate(untracked=True)
        self.manifest_path.write_bytes(json_bytes(self.manifest()))
        command = ["python3", "-I", "-B", str(VALIDATOR), "--run-root", str(self.run_root), "--operator-root", str(self.operator_root), "--manifest", str(self.manifest_path), "--worktree", str(self.worktree)]
        first = subprocess.Popen(command, cwd=REPOSITORY, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        second = subprocess.Popen(command, cwd=REPOSITORY, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        results = [first.communicate(timeout=120), second.communicate(timeout=120)]
        self.assertEqual([first.returncode, second.returncode].count(0), 1, results)
        history = json.loads((self.operator_root / "review-freshness-history.json").read_text())
        self.assertEqual(len(history["claims"]), 1)
        self.assertEqual(history["claims"][0]["action"], "generate-review-packet")

    def test_committed_previous_round_packet_claims_current_generation(self) -> None:
        manifest = self.make_previous_round_stale_packet()
        code, result = self.invoke(manifest)
        self.assertEqual(code, 0, result)
        self.assertEqual(result["action"], "generate-review-packet")
        self.assertEqual(result["reasonCode"], "previous-round-packet-generation-claimed")
        self.assertTrue(result["historyClaimed"])

    def test_valid_repair_audit_is_the_only_skippable_lineage_event(self) -> None:
        manifest = self.make_previous_round_stale_packet()
        self.insert_round_repair_audit(manifest)
        code, result = self.invoke(manifest)
        self.assertEqual(code, 0, result)
        self.assertEqual(result["action"], "generate-review-packet")

    def test_previous_round_packet_requires_exact_archive_decision_and_event(self) -> None:
        attacks = (
            ("archive-bytes", "previous-round-packet-archive-mismatch"),
            ("decision-binding", "previous-round-decision-binding-mismatch"),
            ("event-actor", "previous-round-review-event-invalid"),
            ("event-attempt", "previous-round-review-event-invalid"),
            ("rogue-business", "current-round-event-lineage-invalid"),
            ("forged-repair", "repair-audit-event-invalid"),
            ("worker-completed-digests", "current-round-worker-completed-binding-mismatch"),
            ("current-attempt", "previous-round-current-attempt-conflict"),
            ("current-candidate", "candidate-record-core-invalid"),
        )
        for attack, reason in attacks:
            with self.subTest(attack=attack):
                self._write_fixture()
                manifest = self.make_previous_round_stale_packet()
                if attack == "archive-bytes":
                    archive = self.run_root / "review-packets" / "packet-001.json"
                    value = json.loads(archive.read_text()); value["generatedAt"] = "2026-08-20T00:00:59Z"; archive.write_bytes(json_bytes(value))
                elif attack == "decision-binding":
                    decision_path = self.run_root / "decisions" / "decision-001.json"
                    value = json.loads(decision_path.read_text()); value["evidenceDigest"] = "sha256:" + "f" * 64; decision_path.write_bytes(json_bytes(value))
                elif attack == "event-actor":
                    events_path = self.run_root / "events.jsonl"
                    values = [json.loads(line) for line in events_path.read_text().splitlines()]; values[5]["actor"]["id"] = "forged"; events_path.write_bytes(b"".join(json.dumps(value, separators=(",", ":")).encode() + b"\n" for value in values))
                elif attack == "event-attempt":
                    events_path = self.run_root / "events.jsonl"
                    values = [json.loads(line) for line in events_path.read_text().splitlines()]; values[5]["attemptId"] = "attempt:forged"; events_path.write_bytes(b"".join(json.dumps(value, separators=(",", ":")).encode() + b"\n" for value in values))
                elif attack == "rogue-business":
                    self.insert_round_repair_audit(manifest, lambda event: event.update({"type": "fixture.step", "actor": {"type": "system", "id": "fixture"}, "payload": {}}))
                elif attack == "forged-repair":
                    self.insert_round_repair_audit(manifest, lambda event: event["actor"].update({"id": "forged"}))
                elif attack == "worker-completed-digests":
                    events_path = self.run_root / "events.jsonl"
                    values = [json.loads(line) for line in events_path.read_text().splitlines()]
                    values[7]["payload"].update({"snapshotDigest": "sha256:" + "f" * 64, "diffDigest": "sha256:" + "e" * 64})
                    events_path.write_bytes(b"".join(json.dumps(value, separators=(",", ":")).encode() + b"\n" for value in values))
                elif attack == "current-attempt":
                    packet_path = self.run_root / "review-packet.json"
                    value = json.loads(packet_path.read_text()); value["inputs"]["workerResults"] = ["attempts/attempt:fixture-02/worker-result.json"]; packet_path.write_bytes(json_bytes(value))
                    (self.run_root / "review-packets" / "packet-001.json").write_bytes(json_bytes(value))
                else:
                    candidate_path = next((self.run_root / "candidates").iterdir())
                    value = json.loads(candidate_path.read_text()); value["attemptId"] = "attempt:fixture-01"; candidate_path.write_bytes(json_bytes(value))
                self.assert_reason(reason, manifest)
                history = json.loads((self.operator_root / "review-freshness-history.json").read_text())
                self.assertEqual(history["claims"], [])

    def test_missing_packet_dirty_worktree_never_consumes_generation_claim(self) -> None:
        (self.run_root / "review-packet.json").unlink()
        (self.worktree / "README").write_text("dirty\n")
        self.assert_reason("packet-missing-worktree-not-clean")
        history = json.loads((self.operator_root / "review-freshness-history.json").read_text())
        self.assertEqual(history["claims"], [])

    def test_missing_packet_report_tamper_never_claims(self) -> None:
        (self.run_root / "review-packet.json").unlink()
        report_path = self.run_root / "verification-report.json"
        report = json.loads(report_path.read_text())
        report["summary"] = "tampered"
        report_path.write_bytes(json_bytes(report))
        code, result = self.invoke()
        self.assertNotEqual(code, 0, result)
        history = json.loads((self.operator_root / "review-freshness-history.json").read_text())
        self.assertEqual(history["claims"], [])

    def test_missing_packet_manifest_tamper_never_claims(self) -> None:
        (self.run_root / "review-packet.json").unlink()
        manifest_path = self.run_root / "artifact-manifest.json"
        manifest = json.loads(manifest_path.read_text())
        manifest["artifacts"][0]["description"] = "tampered"
        manifest_path.write_bytes(json_bytes(manifest))
        code, result = self.invoke()
        self.assertNotEqual(code, 0, result)
        history = json.loads((self.operator_root / "review-freshness-history.json").read_text())
        self.assertEqual(history["claims"], [])

    def test_verification_event_freeze_rejects_self_consistent_report_tamper(self) -> None:
        report_path = self.run_root / "verification-report.json"
        report = json.loads(report_path.read_text()); report["summary"] = "self-consistent tamper"
        report_path.write_bytes(json_bytes(report)); report_digest = self.core_digest(report, "VerificationReport")
        packet_path = self.run_root / "review-packet.json"; packet = json.loads(packet_path.read_text())
        packet["verificationDigest"] = report_digest
        evidence = {"specDigest": packet["specDigest"], "patchDigest": packet["diffDigest"], "verificationDigest": report_digest, "artifactManifestDigest": packet["artifactManifestDigest"], "workerResultDigests": packet["workerResultDigests"], "previousBlockingFindings": []}
        packet["evidenceDigest"] = self.core_digest(evidence); packet_path.write_bytes(json_bytes(packet))
        self.core_digest(packet, "ReviewPacket")
        self.assert_reason("verification-event-binding-mismatch")

    def test_concurrent_barrier_allows_exactly_one_dispatch(self) -> None:
        self.manifest_path.write_bytes(json_bytes(self.manifest()))
        command = ["python3", "-I", "-B", str(VALIDATOR), "--run-root", str(self.run_root), "--operator-root", str(self.operator_root), "--manifest", str(self.manifest_path), "--worktree", str(self.worktree)]
        first = subprocess.Popen(command, cwd=REPOSITORY, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        second = subprocess.Popen(command, cwd=REPOSITORY, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        results = [first.communicate(timeout=120), second.communicate(timeout=120)]
        codes = [first.returncode, second.returncode]
        self.assertEqual(codes.count(0), 1, results)
        history = json.loads((self.operator_root / "review-freshness-history.json").read_text()); self.assertEqual(len(history["claims"]), 1)

    def test_all_packet_input_mutations_fail_closed(self) -> None:
        cases = ["task-spec.json", "verification-report.json", "artifact-manifest.json", "observed.patch", "attempts/attempt:fixture-01/worker-result.json"]
        for relative in cases:
            with self.subTest(relative=relative):
                self._write_fixture(); path = self.run_root / relative
                if relative.endswith(".json"):
                    value = json.loads(path.read_text())
                    if relative == "task-spec.json": value["work"]["objective"] += " drift"
                    elif relative == "verification-report.json": value["summary"] = "drift"
                    elif relative == "artifact-manifest.json": value["artifacts"][0]["description"] = "drift"
                    else: value["summary"] = "drift"
                    path.write_bytes(json_bytes(value))
                else:
                    path.write_bytes(b"drift")
                code, result = self.invoke()
                self.assertEqual(code, 2, result)

    def test_symlink_and_non_regular_inputs_fail_closed(self) -> None:
        actual = self.run_root / "real-state.json"; (self.run_root / "state.json").rename(actual); (self.run_root / "state.json").symlink_to(actual.name)
        self.assert_reason("path-symlink-rejected")

    def test_packet_input_symlink_fails_closed(self) -> None:
        target = self.run_root / "worker-real.json"
        worker = self.run_root / "attempts" / "attempt:fixture-01" / "worker-result.json"
        worker.rename(target); worker.symlink_to(target)
        self.assert_reason("path-symlink-rejected")

    def test_worker_result_persisted_set_is_exact(self) -> None:
        extra = self.example("worker-result")
        extra.update({"taskId": "REVIEW-FRESHNESS-FIXTURE", "runId": "review-freshness-fixture-r1", "attemptId": "attempt:fixture-00"})
        path = self.run_root / "attempts" / "attempt:fixture-00" / "worker-result.json"
        path.parent.mkdir(parents=True); path.write_bytes(json_bytes(extra))
        self.assert_reason("worker-result-set-mismatch")

    def test_worker_result_declared_extra_and_attempt_identity_fail_closed(self) -> None:
        packet_path = self.run_root / "review-packet.json"
        packet = json.loads(packet_path.read_text())
        packet["inputs"]["workerResults"].append("attempts/attempt:missing/worker-result.json")
        packet["workerResultDigests"].append(packet["workerResultDigests"][0])
        packet_path.write_bytes(json_bytes(packet))
        self.assert_reason("worker-result-set-mismatch")

        self._write_fixture()
        worker_path = self.run_root / "attempts" / "attempt:fixture-01" / "worker-result.json"
        worker = json.loads(worker_path.read_text()); worker["attemptId"] = "attempt:other"
        worker_path.write_bytes(json_bytes(worker)); worker_digest = self.core_digest(worker, "WorkerResult")
        packet = json.loads(packet_path.read_text()); packet["workerResultDigests"] = [worker_digest]
        evidence = {"specDigest": packet["specDigest"], "patchDigest": packet["diffDigest"], "verificationDigest": packet["verificationDigest"], "artifactManifestDigest": packet["artifactManifestDigest"], "workerResultDigests": [worker_digest], "previousBlockingFindings": []}
        packet["evidenceDigest"] = self.core_digest(evidence); packet_path.write_bytes(json_bytes(packet))
        self.assert_reason("worker-result-identity-mismatch")

    def test_worker_result_set_must_include_current_attempt(self) -> None:
        state_path = self.run_root / "state.json"; state = json.loads(state_path.read_text())
        state["currentAttemptId"] = "attempt:fixture-02"; state_path.write_bytes(json_bytes(state))
        manifest = self.manifest(); manifest["expected"]["currentAttemptId"] = "attempt:fixture-02"
        self.assert_reason("worker-result-attempt-set-mismatch", manifest)

    def test_history_claim_lock_fails_closed_without_dispatch(self) -> None:
        (self.operator_root / "review-freshness-history.json.claim.lock").write_text("held")
        self.assert_reason("history-claim-contended")
        history = json.loads((self.operator_root / "review-freshness-history.json").read_text()); self.assertEqual(history["claims"], [])

    def test_nested_history_parent_swap_never_dispatches(self) -> None:
        nested = self.operator_root / "nested"
        replacement = self.operator_root / "replacement"
        nested.mkdir(); replacement.mkdir()
        (self.operator_root / "review-freshness-history.json").replace(nested / "history.json")
        (replacement / "history.json").write_bytes(json_bytes({"apiVersion": "marshal.operator/v1alpha1", "kind": "ReviewFreshnessHistory", "claims": []}))
        manifest = self.manifest(); manifest["files"]["historyPath"] = "nested/history.json"
        stop = threading.Event()
        def swap_loop() -> None:
            transit = self.operator_root / "transit"
            while not stop.is_set():
                try:
                    nested.rename(transit); replacement.rename(nested); transit.rename(replacement)
                except FileNotFoundError:
                    continue
        thread = threading.Thread(target=swap_loop, daemon=True); thread.start()
        try:
            code, result = self.invoke(manifest)
            self.assertEqual(code, 2, result)
        finally:
            stop.set(); thread.join(timeout=2)
        for directory in (nested, replacement):
            if (directory / "history.json").exists():
                self.assertEqual(json.loads((directory / "history.json").read_text())["claims"], [])

    def test_same_byte_inode_replacement_race_never_claims(self) -> None:
        state_path = self.run_root / "state.json"; payload = state_path.read_bytes(); stop = threading.Event()
        def replace_loop() -> None:
            index = 0
            while not stop.is_set():
                temporary = self.run_root / f"state.pending.{index % 2}"
                temporary.write_bytes(payload); temporary.replace(state_path); index += 1
        thread = threading.Thread(target=replace_loop, daemon=True); thread.start()
        try:
            code, result = self.invoke(); self.assertEqual(code, 2, result)
        finally:
            stop.set(); thread.join(timeout=2)
        history = json.loads((self.operator_root / "review-freshness-history.json").read_text()); self.assertEqual(history["claims"], [])

    def test_events_same_byte_replacement_race_never_claims(self) -> None:
        events_path = self.run_root / "events.jsonl"; payload = events_path.read_bytes(); stop = threading.Event()
        def replace_loop() -> None:
            index = 0
            while not stop.is_set():
                temporary = self.run_root / f"events.pending.{index % 2}"
                temporary.write_bytes(payload); temporary.replace(events_path); index += 1
        thread = threading.Thread(target=replace_loop, daemon=True); thread.start()
        try:
            code, result = self.invoke(); self.assertEqual(code, 2, result)
        finally:
            stop.set(); thread.join(timeout=2)
        history = json.loads((self.operator_root / "review-freshness-history.json").read_text()); self.assertEqual(history["claims"], [])

    def test_missing_and_directory_packet_inputs_fail_closed(self) -> None:
        (self.run_root / "verification-report.json").unlink(); self.assert_reason("verification-report-unreadable")
        self._write_fixture(); (self.run_root / "artifact-manifest.json").unlink(); (self.run_root / "artifact-manifest.json").mkdir()
        self.assert_reason("artifact-manifest-unreadable")

    def test_source_and_state_sequence_drift_fail_closed(self) -> None:
        manifest = self.manifest(); manifest["expected"]["sourceHead"] = "f" * 40; self.assert_reason("source-head-mismatch", manifest)
        manifest = self.manifest(); manifest["expected"]["stateSequence"] = 8; self.assert_reason("state-identity-mismatch", manifest)

    def test_worktree_dirty_after_verification_fails_closed(self) -> None:
        (self.worktree / "README").write_text("changed\n")
        self.assert_reason("worktree-evidence-changed-after-verification")

    def test_partial_legacy_candidate_requires_intervention(self) -> None:
        packet_path = self.run_root / "review-packet.json"; packet = json.loads(packet_path.read_text()); packet["candidateDigest"] = "sha256:" + "a" * 64; packet_path.write_bytes(json_bytes(packet))
        self.assert_reason("legacy-candidate-partial-requires-migration")

    def test_duplicate_key_and_large_integer_use_core_authority(self) -> None:
        packet_path = self.run_root / "review-packet.json"
        packet_path.write_bytes(packet_path.read_bytes().replace(b'"reviewRound": 2,', b'"reviewRound": 2, "reviewRound": 9007199254740993,'))
        self.assert_reason("core-contract-invalid")

    def test_unicode_manifest_and_schema_path_parity(self) -> None:
        for value in ("../状态.json", "nested//state.json", "nested/", "nested\\state.json", "/state.json", "nested/\x00state.json", "x" * 2049):
            with self.subTest(value=repr(value)):
                manifest = self.manifest(); manifest["files"]["statePath"] = value
                self.assert_reason("operator-schema-invalid", manifest)
                with self.assertRaises(PREFLIGHT.PreflightError) as raised:
                    PREFLIGHT.clean_relative(value)
                self.assertEqual(raised.exception.reason_code, "path-boundary-invalid")
        self.assertEqual(PREFLIGHT.clean_relative("nested/状态.json"), "nested/状态.json")

    def test_state_policy_control_and_source_changes_change_claim(self) -> None:
        state_path = self.run_root / "state.json"; state = json.loads(state_path.read_text()); state["policyDigest"] = "sha256:" + "f" * 64; state_path.write_bytes(json_bytes(state))
        self.assert_reason("frozen-input-digest-mismatch")


if __name__ == "__main__":
    unittest.main()
