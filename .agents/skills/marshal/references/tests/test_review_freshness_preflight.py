#!/usr/bin/env python3

from __future__ import annotations

import hashlib
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


def digest_bytes(data: bytes) -> str:
    return "sha256:" + hashlib.sha256(data).hexdigest()


def json_bytes(value: object) -> bytes:
    return json.dumps(value, ensure_ascii=False, indent=2).encode() + b"\n"


class ReviewFreshnessPreflightTest(unittest.TestCase):
    maxDiff = None
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
        result = subprocess.run(["go", "run", str(CORE), mode, kind or "-", str(path)], cwd=REPOSITORY, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=True)
        return result.stdout.strip()

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
        paths = {"task-spec.json": task, "policy-snapshot.json": policy, "capability-snapshot.json": capability, "verification-report.json": report, "artifact-manifest.json": artifacts, "review-packet.json": packet, "state.json": state}
        for name, value in paths.items():
            (self.run_root / name).write_bytes(json_bytes(value))
        worker_path = self.run_root / "attempts" / "attempt:fixture-01" / "worker-result.json"
        worker_path.parent.mkdir(parents=True, exist_ok=True); worker_path.write_bytes(json_bytes(worker))
        (self.run_root / "observed.patch").write_bytes(empty_patch)
        (self.run_root / "control").mkdir(exist_ok=True); (self.run_root / "control" / "records.jsonl").write_bytes(b"")
        (self.operator_root / "review-freshness-history.json").write_bytes(json_bytes({"apiVersion": "marshal.operator/v1alpha1", "kind": "ReviewFreshnessHistory", "claims": []}))

    def manifest(self) -> dict:
        return {"apiVersion": "marshal.operator/v1alpha1", "kind": "ReviewFreshnessPreflight", "expected": {"taskId": "REVIEW-FRESHNESS-FIXTURE", "runId": "review-freshness-fixture-r1", "state": "REVIEW_PENDING", "stateSequence": 7, "currentAttemptId": "attempt:fixture-01", "sourceHead": self.head, "baseSha": self.head, "reviewRound": 2}, "files": {"statePath": "state.json", "packetPath": "review-packet.json", "taskSpecPath": "task-spec.json", "policySnapshotPath": "policy-snapshot.json", "capabilitySnapshotPath": "capability-snapshot.json", "controlRecordsPath": "control/records.jsonl", "historyPath": "review-freshness-history.json"}}

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

    def test_present_packet_dispatch_is_claimed_once(self) -> None:
        code, _ = self.invoke(); self.assertEqual(code, 0)
        self.assert_reason("action-already-claimed")

    def test_missing_packet_generation_is_claimed_once(self) -> None:
        (self.run_root / "review-packet.json").unlink()
        code, result = self.invoke(); self.assertEqual(code, 0, result); self.assertEqual(result["action"], "generate-review-packet")
        self.assert_reason("action-already-claimed")

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

    def test_history_claim_lock_fails_closed_without_dispatch(self) -> None:
        (self.operator_root / "review-freshness-history.json.claim.lock").write_text("held")
        self.assert_reason("history-claim-contended")
        history = json.loads((self.operator_root / "review-freshness-history.json").read_text()); self.assertEqual(history["claims"], [])

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

    def test_missing_and_directory_packet_inputs_fail_closed(self) -> None:
        (self.run_root / "verification-report.json").unlink(); self.assert_reason("packet-input-unreadable")
        self._write_fixture(); (self.run_root / "artifact-manifest.json").unlink(); (self.run_root / "artifact-manifest.json").mkdir()
        self.assert_reason("packet-input-unreadable")

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
        manifest = self.manifest(); manifest["files"]["statePath"] = "../状态.json"
        self.assert_reason("operator-schema-invalid", manifest)

    def test_state_policy_control_and_source_changes_change_claim(self) -> None:
        state_path = self.run_root / "state.json"; state = json.loads(state_path.read_text()); state["policyDigest"] = "sha256:" + "f" * 64; state_path.write_bytes(json_bytes(state))
        self.assert_reason("frozen-input-digest-mismatch")


if __name__ == "__main__":
    unittest.main()
