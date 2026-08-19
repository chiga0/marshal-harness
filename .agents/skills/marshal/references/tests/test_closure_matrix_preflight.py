from __future__ import annotations

import hashlib
import importlib.util
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest


REPOSITORY_ROOT = Path(__file__).resolve().parents[5]
SKILL_ROOT = REPOSITORY_ROOT / ".agents/skills/marshal"
VALIDATOR = SKILL_ROOT / "references/validate-closure-matrix-preflight.py"
SCHEMA = SKILL_ROOT / "references/closure-matrix-preflight.schema.json"
SCHEMA_PROBE = SKILL_ROOT / "references/tests/closure_matrix_schema_probe.go"
TEMPLATE = SKILL_ROOT / "templates/closure-matrix-preflight.json"
FIXTURE_RELATIVE = Path(".agents/skills/marshal/references/fixtures/closure-matrix")
FIXTURES = REPOSITORY_ROOT / FIXTURE_RELATIVE


def load_validator_module():
    spec = importlib.util.spec_from_file_location("closure_matrix_preflight", VALIDATOR)
    if spec is None or spec.loader is None:
        raise RuntimeError("cannot load validator")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


VALIDATOR_MODULE = load_validator_module()


def write_json(path: Path, value: object) -> None:
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


class ClosureMatrixPreflightTest(unittest.TestCase):
    def prepared(self, directory: str) -> tuple[Path, Path]:
        root = Path(directory)
        fixtures = root / FIXTURE_RELATIVE
        fixtures.parent.mkdir(parents=True)
        shutil.copytree(FIXTURES, fixtures)
        subprocess.run(["git", "init", "-q"], cwd=root, check=True)
        subprocess.run(["git", "add", "."], cwd=root, check=True)
        subprocess.run(
            ["git", "-c", "user.name=Marshal Test", "-c", "user.email=marshal@example.invalid", "commit", "-qm", "fixture"],
            cwd=root,
            check=True,
        )
        head = subprocess.run(["git", "rev-parse", "HEAD"], cwd=root, check=True, capture_output=True, text=True).stdout.strip()

        manifest_path = fixtures / "manifest.json"
        packet_path = fixtures / "review-packet.json"
        decision_path = fixtures / "review-decision.json"
        state_path = fixtures / "run-state.json"
        packet = json.loads(packet_path.read_text())
        manifest = json.loads(manifest_path.read_text())
        decision = json.loads(decision_path.read_text())

        for finding in manifest["findings"]:
            finding["id"] = VALIDATOR_MODULE.stable_finding_id(finding)
        first_id, second_id = (finding["id"] for finding in manifest["findings"])
        packet["previousBlockingFindings"][0]["id"] = first_id
        evidence_digests = VALIDATOR_MODULE.jcs_file_digests([
            fixtures / "verification-report.json",
            fixtures / "artifact-manifest.json",
            fixtures / "worker-result.json",
        ])
        packet["verificationDigest"] = evidence_digests[0]
        packet["artifactManifestDigest"] = evidence_digests[1]
        packet["workerResultDigests"] = [evidence_digests[2]]
        write_json(packet_path, packet)

        fresh = manifest["freshness"]
        fresh.update({
            "sourceHead": head,
            "verificationDigest": packet["verificationDigest"],
            "artifactManifestDigest": packet["artifactManifestDigest"],
            "reviewPacketDigest": VALIDATOR_MODULE.jcs_file_digests([packet_path])[0],
        })
        fresh["fingerprintDigest"] = VALIDATOR_MODULE.canonical_digest(
            {key: value for key, value in fresh.items() if key != "fingerprintDigest"}
        )
        manifest["negativeFixtures"][0]["findingId"] = first_id
        manifest["negativeFixtures"][1]["findingId"] = second_id
        for fixture in manifest["negativeFixtures"]:
            fixture["digest"] = VALIDATOR_MODULE.file_digest(root / fixture["path"])
        write_json(manifest_path, manifest)

        for key in (
            "taskId", "runId", "reviewRound", "specDigest", "reviewPacketDigest",
            "verificationDigest", "artifactManifestDigest", "evidenceDigest",
        ):
            decision[key] = fresh[key]
        decision["blockingFindings"] = [
            VALIDATOR_MODULE.projected_finding(finding) for finding in manifest["findings"]
        ]
        decision["nonBlockingFindings"] = []
        write_json(decision_path, decision)
        return root, fixtures

    def invoke(self, root: Path, fixtures: Path) -> subprocess.CompletedProcess[str]:
        return subprocess.run(
            [
                sys.executable, "-B", str(VALIDATOR),
                "--root", str(root),
                "--run-root", str(fixtures),
                "--source-root", str(root),
                "--manifest", str(fixtures / "manifest.json"),
                "--review-packet", str(fixtures / "review-packet.json"),
                "--review-decision", str(fixtures / "review-decision.json"),
                "--run-state", str(fixtures / "run-state.json"),
                "--schema", str(SCHEMA),
            ],
            check=False,
            capture_output=True,
            text=True,
        )

    def assert_failure(self, completed: subprocess.CompletedProcess[str], reason: str) -> None:
        self.assertNotEqual(completed.returncode, 0, completed.stdout)
        payload = json.loads(completed.stderr)
        self.assertEqual(payload["reasonCode"], reason, completed.stderr)

    def load(self, fixtures: Path, name: str) -> dict:
        return json.loads((fixtures / name).read_text())

    def test_schema_and_template_are_draft_2020_12_valid(self) -> None:
        completed = subprocess.run(
            ["go", "run", str(SCHEMA_PROBE), str(SCHEMA), str(TEMPLATE), str(FIXTURES / "manifest.json")],
            cwd=REPOSITORY_ROOT,
            check=False,
            capture_output=True,
            text=True,
        )
        self.assertEqual(completed.returncode, 0, completed.stderr)

    def test_fresh_continuation_and_same_domain_p2_pass(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root, fixtures = self.prepared(directory)
            completed = self.invoke(root, fixtures)
            self.assertEqual(completed.returncode, 0, completed.stderr)
            payload = json.loads(completed.stdout)
            self.assertEqual(payload["blockingFindings"], 2)

    def test_p1_omission_fails_closed(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root, fixtures = self.prepared(directory)
            decision = self.load(fixtures, "review-decision.json")
            decision["blockingFindings"] = decision["blockingFindings"][1:]
            write_json(fixtures / "review-decision.json", decision)
            self.assert_failure(self.invoke(root, fixtures), "blocking-finding-omitted")

    def test_same_domain_p2_cannot_be_deferred(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root, fixtures = self.prepared(directory)
            manifest = self.load(fixtures, "manifest.json")
            manifest["findings"][1]["disposition"] = "non-blocking"
            write_json(fixtures / "manifest.json", manifest)
            decision = self.load(fixtures, "review-decision.json")
            decision["nonBlockingFindings"] = [VALIDATOR_MODULE.projected_finding(manifest["findings"][1])]
            decision["blockingFindings"] = decision["blockingFindings"][:1]
            write_json(fixtures / "review-decision.json", decision)
            self.assert_failure(self.invoke(root, fixtures), "blocking-finding-omitted")

    def test_vague_required_outcome_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root, fixtures = self.prepared(directory)
            manifest = self.load(fixtures, "manifest.json")
            manifest["findings"][0]["requiredOutcome"]["assertions"][0]["expected"] = ["完整 identity"]
            write_json(fixtures / "manifest.json", manifest)
            self.assert_failure(self.invoke(root, fixtures), "required-outcome-open-ended")

    def test_duplicate_semantic_finding_is_rejected_as_split(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root, fixtures = self.prepared(directory)
            manifest = self.load(fixtures, "manifest.json")
            duplicate = json.loads(json.dumps(manifest["findings"][1]))
            duplicate["id"] = "F-aaaaaaaaaaaaaaaaaaaaaaaa"
            manifest["findings"].append(duplicate)
            write_json(fixtures / "manifest.json", manifest)
            self.assert_failure(self.invoke(root, fixtures), "finding-split")

    def test_stale_fingerprint_and_source_head_are_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root, fixtures = self.prepared(directory)
            manifest = self.load(fixtures, "manifest.json")
            manifest["freshness"]["sourceHead"] = "a" * 40
            write_json(fixtures / "manifest.json", manifest)
            self.assert_failure(self.invoke(root, fixtures), "freshness-fingerprint-mismatch")
            manifest["freshness"]["fingerprintDigest"] = VALIDATOR_MODULE.canonical_digest(
                {key: value for key, value in manifest["freshness"].items() if key != "fingerprintDigest"}
            )
            write_json(fixtures / "manifest.json", manifest)
            self.assert_failure(self.invoke(root, fixtures), "stale-review-packet")

    def test_missing_fixture_reference_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root, fixtures = self.prepared(directory)
            manifest = self.load(fixtures, "manifest.json")
            manifest["findings"][0]["requiredOutcome"]["negativeFixtureRefs"] = ["absent-fixture"]
            write_json(fixtures / "manifest.json", manifest)
            self.assert_failure(self.invoke(root, fixtures), "negative-fixture-ref-missing")

    def test_fixture_digest_and_verification_reference_are_bound(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root, fixtures = self.prepared(directory)
            manifest = self.load(fixtures, "manifest.json")
            manifest["negativeFixtures"][0]["digest"] = "sha256:" + "0" * 64
            write_json(fixtures / "manifest.json", manifest)
            self.assert_failure(self.invoke(root, fixtures), "fixture-digest-mismatch")
        with tempfile.TemporaryDirectory() as directory:
            root, fixtures = self.prepared(directory)
            manifest = self.load(fixtures, "manifest.json")
            manifest["findings"][0]["requiredOutcome"]["verificationRefs"][0]["id"] = "absent-gate"
            write_json(fixtures / "manifest.json", manifest)
            self.assert_failure(self.invoke(root, fixtures), "verification-ref-missing")

    def test_location_and_stable_id_are_exact(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root, fixtures = self.prepared(directory)
            manifest = self.load(fixtures, "manifest.json")
            manifest["findings"][0]["location"]["locator"] = "../internal/example.go"
            write_json(fixtures / "manifest.json", manifest)
            self.assert_failure(self.invoke(root, fixtures), "finding-location-imprecise")
        with tempfile.TemporaryDirectory() as directory:
            root, fixtures = self.prepared(directory)
            manifest = self.load(fixtures, "manifest.json")
            manifest["findings"][0]["subject"] = "renamed-subject"
            write_json(fixtures / "manifest.json", manifest)
            self.assert_failure(self.invoke(root, fixtures), "finding-id-unstable")

    def test_changed_packet_input_is_stale_digest(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root, fixtures = self.prepared(directory)
            verification = self.load(fixtures, "verification-report.json")
            verification["gates"][0]["summary"] = "changed after packet generation"
            write_json(fixtures / "verification-report.json", verification)
            self.assert_failure(self.invoke(root, fixtures), "stale-digest")

    def test_p0_cannot_be_non_blocking(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root, fixtures = self.prepared(directory)
            manifest = self.load(fixtures, "manifest.json")
            manifest["findings"][1]["severity"] = "P0"
            manifest["findings"][1]["disposition"] = "non-blocking"
            write_json(fixtures / "manifest.json", manifest)
            decision = self.load(fixtures, "review-decision.json")
            decision["blockingFindings"] = decision["blockingFindings"][:1]
            decision["nonBlockingFindings"] = [VALIDATOR_MODULE.projected_finding(manifest["findings"][1])]
            write_json(fixtures / "review-decision.json", decision)
            self.assert_failure(self.invoke(root, fixtures), "blocking-finding-omitted")

    def test_previous_finding_requires_explicit_lineage(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root, fixtures = self.prepared(directory)
            manifest = self.load(fixtures, "manifest.json")
            manifest["findings"] = manifest["findings"][1:]
            manifest["negativeFixtures"] = manifest["negativeFixtures"][1:]
            write_json(fixtures / "manifest.json", manifest)
            decision = self.load(fixtures, "review-decision.json")
            decision["blockingFindings"] = decision["blockingFindings"][1:]
            write_json(fixtures / "review-decision.json", decision)
            self.assert_failure(self.invoke(root, fixtures), "previous-finding-lineage-mismatch")

    def test_previous_finding_cannot_close_without_new_evidence(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root, fixtures = self.prepared(directory)
            packet = self.load(fixtures, "review-packet.json")
            packet["previousBlockingFindings"][0]["candidateDigest"] = packet["candidateDigest"]
            write_json(fixtures / "review-packet.json", packet)
            manifest = self.load(fixtures, "manifest.json")
            manifest["findings"][0]["classification"] = "closed-previous"
            manifest["findings"][0]["disposition"] = "closed-previous"
            manifest["freshness"]["reviewPacketDigest"] = VALIDATOR_MODULE.jcs_file_digests([fixtures / "review-packet.json"])[0]
            manifest["freshness"]["fingerprintDigest"] = VALIDATOR_MODULE.canonical_digest(
                {key: value for key, value in manifest["freshness"].items() if key != "fingerprintDigest"}
            )
            write_json(fixtures / "manifest.json", manifest)
            decision = self.load(fixtures, "review-decision.json")
            decision["reviewPacketDigest"] = manifest["freshness"]["reviewPacketDigest"]
            decision["blockingFindings"] = [VALIDATOR_MODULE.projected_finding(manifest["findings"][1])]
            write_json(fixtures / "review-decision.json", decision)
            self.assert_failure(self.invoke(root, fixtures), "previous-finding-lineage-mismatch")

    def test_projection_drift_and_unexpected_finding_are_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root, fixtures = self.prepared(directory)
            decision = self.load(fixtures, "review-decision.json")
            decision["blockingFindings"][0]["description"] += " drift"
            write_json(fixtures / "review-decision.json", decision)
            self.assert_failure(self.invoke(root, fixtures), "blocking-finding-omitted")
            decision = self.load(fixtures, "review-decision.json")
            decision["blockingFindings"].append({
                "id": "F-ffffffffffffffffffffffff", "severity": "P1", "title": "extra",
                "description": "extra", "file": "extra.go", "line": 1,
            })
            write_json(fixtures / "review-decision.json", decision)
            self.assert_failure(self.invoke(root, fixtures), "unexpected-decision-finding")


if __name__ == "__main__":
    unittest.main()
