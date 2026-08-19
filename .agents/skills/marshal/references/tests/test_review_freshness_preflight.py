#!/usr/bin/env python3

from __future__ import annotations

import hashlib
import json
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


HERE = Path(__file__).resolve().parent
REFERENCES = HERE.parent
FIXTURES = REFERENCES / "fixtures" / "review-freshness"
VALIDATOR = REFERENCES / "validate-review-freshness-preflight.py"
DIGESTS = {
    "spec": "sha256:" + "1" * 64,
    "verification": "sha256:" + "6" * 64,
    "artifact": "sha256:" + "7" * 64,
    "worker": "sha256:" + "8" * 64,
    "evidence": "sha256:" + "9" * 64,
    "candidate": "sha256:" + "b" * 64,
    "workerCandidate": "sha256:" + "c" * 64,
}


def raw_digest(path: Path) -> str:
    return "sha256:" + hashlib.sha256(path.read_bytes()).hexdigest()


def canonical_digest(value: object) -> str:
    payload = json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode()
    return "sha256:" + hashlib.sha256(payload).hexdigest()


class ReviewFreshnessPreflightTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temp = Path(tempfile.mkdtemp(prefix="review-freshness.", dir="/private/tmp")).resolve()
        self.run_root = self.temp / "run"
        self.operator_root = self.temp / "operator"
        self.worktree = self.temp / "worktree"
        self.run_root.mkdir()
        self.operator_root.mkdir()
        self.worktree.mkdir()
        for name in ("state.json", "review-packet.json"):
            shutil.copyfile(FIXTURES / name, self.run_root / name)
        shutil.copyfile(FIXTURES / "review-freshness-history.json", self.operator_root / "review-freshness-history.json")
        subprocess.run(["/usr/bin/git", "init", "-q"], cwd=self.worktree, check=True)
        subprocess.run(["/usr/bin/git", "config", "user.name", "Fixture"], cwd=self.worktree, check=True)
        subprocess.run(["/usr/bin/git", "config", "user.email", "fixture@example.invalid"], cwd=self.worktree, check=True)
        (self.worktree / "README").write_text("fixture\n", encoding="utf-8")
        subprocess.run(["/usr/bin/git", "add", "README"], cwd=self.worktree, check=True)
        subprocess.run(["/usr/bin/git", "-c", "core.hooksPath=/dev/null", "commit", "-qm", "fixture"], cwd=self.worktree, check=True)
        self.head = subprocess.check_output(["/usr/bin/git", "rev-parse", "HEAD"], cwd=self.worktree, text=True).strip()
        self.manifest_path = self.operator_root / "manifest.json"

    def tearDown(self) -> None:
        shutil.rmtree(self.temp)

    def packet(self) -> dict:
        return json.loads((self.run_root / "review-packet.json").read_text(encoding="utf-8"))

    def history(self) -> dict:
        return json.loads((self.operator_root / "review-freshness-history.json").read_text(encoding="utf-8"))

    def write_history(self, value: dict) -> None:
        (self.operator_root / "review-freshness-history.json").write_text(json.dumps(value, indent=2) + "\n", encoding="utf-8")

    def build_manifest(self, present: bool = True) -> dict:
        dedupe = "sha256:" + "d" * 64
        expected = {
            "taskId": "REVIEW-FRESHNESS-FIXTURE",
            "runId": "review-freshness-fixture-r1",
            "state": "REVIEW_PENDING",
            "stateSequence": 7,
            "currentAttemptId": "attempt:fixture-01",
            "sourceHead": self.head,
            "baseSha": "a" * 40,
            "reviewRound": 2,
            "specDigest": DIGESTS["spec"],
        }
        files = {
            "statePath": "state.json",
            "stateRawDigest": raw_digest(self.run_root / "state.json"),
            "packetPath": "review-packet.json",
            "packetPresence": "present" if present else "missing",
            "historyPath": "review-freshness-history.json",
            "historyRawDigest": raw_digest(self.operator_root / "review-freshness-history.json"),
        }
        if present:
            packet = self.packet()
            bindings = {
                "verificationDigest": DIGESTS["verification"],
                "artifactManifestDigest": DIGESTS["artifact"],
                "evidenceDigest": DIGESTS["evidence"],
                "candidateDigest": DIGESTS["candidate"],
                "workerCandidateDigest": DIGESTS["workerCandidate"],
                "workerResultDigests": [DIGESTS["worker"]],
            }
            expected["packetBindings"] = bindings
            files["packetRawDigest"] = raw_digest(self.run_root / "review-packet.json")
            files["packetCanonicalDigest"] = canonical_digest(packet)
            packet_marker = {
                "presence": "present",
                "rawDigest": files["packetRawDigest"],
                "canonicalDigest": files["packetCanonicalDigest"],
                **bindings,
            }
        else:
            packet_marker = {"presence": "missing"}
        identity = {
            "schemaVersion": "marshal.operator.review-freshness.v1",
            "dedupeKey": dedupe,
            "taskId": expected["taskId"],
            "runId": expected["runId"],
            "state": expected["state"],
            "stateSequence": expected["stateSequence"],
            "currentAttemptId": expected["currentAttemptId"],
            "sourceHead": expected["sourceHead"],
            "baseSha": expected["baseSha"],
            "reviewRound": expected["reviewRound"],
            "specDigest": expected["specDigest"],
            "packet": packet_marker,
        }
        return {
            "apiVersion": "marshal.operator/v1alpha1",
            "kind": "ReviewFreshnessPreflight",
            "dedupeKey": dedupe,
            "expected": expected,
            "files": files,
            "freshnessFingerprint": canonical_digest(identity),
        }

    def invoke(self, manifest: dict) -> tuple[int, dict]:
        self.manifest_path.write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
        result = subprocess.run(
            ["python3", "-I", "-B", str(VALIDATOR), "--run-root", str(self.run_root), "--operator-root", str(self.operator_root), "--manifest", str(self.manifest_path), "--worktree", str(self.worktree)],
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            check=False,
        )
        self.assertEqual(result.stderr, "")
        return result.returncode, json.loads(result.stdout)

    def assert_reason(self, manifest: dict, reason: str) -> None:
        code, result = self.invoke(manifest)
        self.assertEqual(code, 2, result)
        self.assertEqual(result, {"ok": False, "action": "intervention", "reasonCode": reason})

    def test_present_fresh_packet_allows_one_dispatch_without_writes(self) -> None:
        before_run = {p.name: p.read_bytes() for p in self.run_root.iterdir()}
        before_operator = {p.name: p.read_bytes() for p in self.operator_root.iterdir()}
        before_status = subprocess.check_output(["/usr/bin/git", "status", "--porcelain=v1"], cwd=self.worktree)
        manifest = self.build_manifest()
        code, result = self.invoke(manifest)
        self.assertEqual(code, 0, result)
        self.assertEqual(result["action"], "dispatch-reviewer")
        self.assertEqual(result["reasonCode"], "reviewer-dispatch-allowed")
        self.assertEqual(result["reviewPacketDigest"], manifest["files"]["packetCanonicalDigest"])
        self.assertEqual({p.name: p.read_bytes() for p in self.run_root.iterdir()}, before_run)
        # The caller-created manifest is the only expected operator-local
        # change; neither history nor Marshal Run inputs are mutated.
        self.assertEqual((self.operator_root / "review-freshness-history.json").read_bytes(), before_operator["review-freshness-history.json"])
        self.assertEqual(subprocess.check_output(["/usr/bin/git", "status", "--porcelain=v1"], cwd=self.worktree), before_status)

    def test_missing_packet_first_generation_is_allowed(self) -> None:
        (self.run_root / "review-packet.json").unlink()
        manifest = self.build_manifest(present=False)
        code, result = self.invoke(manifest)
        self.assertEqual(code, 0, result)
        self.assertEqual(result["action"], "generate-review-packet")
        self.assertEqual(result["reasonCode"], "packet-missing-first-generation-allowed")

    def test_missing_packet_same_dedupe_generation_failure_intervenes(self) -> None:
        (self.run_root / "review-packet.json").unlink()
        manifest = self.build_manifest(present=False)
        history = self.history()
        history["generationAttempts"].append({"dedupeKey": manifest["dedupeKey"]})
        self.write_history(history)
        manifest = self.build_manifest(present=False)
        self.assert_reason(manifest, "packet-generation-failed-same-dedupe-key")

    def test_same_fingerprint_cannot_dispatch_twice(self) -> None:
        manifest = self.build_manifest()
        history = self.history()
        history["reviewerDispatches"].append({"freshnessFingerprint": manifest["freshnessFingerprint"]})
        self.write_history(history)
        manifest = self.build_manifest()
        self.assert_reason(manifest, "reviewer-dispatch-duplicate-fingerprint")

    def test_stale_fingerprint_is_rejected(self) -> None:
        manifest = self.build_manifest()
        manifest["freshnessFingerprint"] = "sha256:" + "e" * 64
        self.assert_reason(manifest, "freshness-fingerprint-mismatch")

    def test_state_sequence_drift_is_rejected(self) -> None:
        shutil.copyfile(FIXTURES / "state-stale-sequence.json", self.run_root / "state.json")
        manifest = self.build_manifest()
        manifest["expected"]["stateSequence"] = 7
        self.assert_reason(manifest, "state-identity-mismatch")

    def test_source_head_drift_is_rejected(self) -> None:
        manifest = self.build_manifest()
        manifest["expected"]["sourceHead"] = "f" * 40
        self.assert_reason(manifest, "source-head-mismatch")

    def test_packet_raw_digest_drift_is_rejected(self) -> None:
        manifest = self.build_manifest()
        manifest["files"]["packetRawDigest"] = "sha256:" + "f" * 64
        self.assert_reason(manifest, "packet-raw-digest-mismatch")

    def test_missing_candidate_binding_is_rejected(self) -> None:
        shutil.copyfile(FIXTURES / "review-packet-missing-candidate.json", self.run_root / "review-packet.json")
        manifest = self.build_manifest()
        self.assert_reason(manifest, "packet-binding-missing")

    def test_symlink_input_is_rejected(self) -> None:
        actual = self.run_root / "real-state.json"
        (self.run_root / "state.json").rename(actual)
        (self.run_root / "state.json").symlink_to(actual.name)
        manifest = self.build_manifest()
        manifest["files"]["stateRawDigest"] = raw_digest(actual)
        self.assert_reason(manifest, "path-symlink-rejected")

    def test_packet_presence_change_is_rejected(self) -> None:
        manifest = self.build_manifest()
        (self.run_root / "review-packet.json").unlink()
        self.assert_reason(manifest, "packet-presence-mismatch")

    def test_history_digest_drift_is_rejected(self) -> None:
        manifest = self.build_manifest()
        manifest["files"]["historyRawDigest"] = "sha256:" + "f" * 64
        self.assert_reason(manifest, "history-raw-digest-mismatch")


if __name__ == "__main__":
    unittest.main()
