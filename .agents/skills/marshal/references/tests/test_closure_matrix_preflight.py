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
    @classmethod
    def setUpClass(cls) -> None:
        cls.build_directory = Path(tempfile.mkdtemp(prefix="closure-matrix-marshal.")).resolve()
        cls.marshal = cls.build_directory / "marshal"
        commit = subprocess.run(["git", "rev-parse", "HEAD"], cwd=REPOSITORY_ROOT, check=True, capture_output=True, text=True).stdout.strip()
        subprocess.run(
            ["go", "build", "-ldflags", f"-X github.com/chiga0/marshal-harness/internal/buildinfo.commit={commit}", "-o", str(cls.marshal), "./cmd/marshal"],
            cwd=REPOSITORY_ROOT, check=True,
        )

    @classmethod
    def tearDownClass(cls) -> None:
        shutil.rmtree(cls.build_directory)

    def prepared(self, directory: str) -> tuple[Path, Path]:
        root = Path(directory)
        fixtures = root / FIXTURE_RELATIVE
        fixtures.parent.mkdir(parents=True)
        shutil.copytree(FIXTURES, fixtures)
        shutil.copy2(
            SKILL_ROOT / "references/closure_matrix_negative_fixture.py",
            root / ".agents/skills/marshal/references/closure_matrix_negative_fixture.py",
        )
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
        state = json.loads(state_path.read_text())
        task_path = fixtures / "task-spec.json"
        verification_path = fixtures / "verification-report.json"
        artifact_path = fixtures / "artifact-manifest.json"
        worker_path = fixtures / "worker-result.json"
        patch_path = fixtures / "observed.patch"
        task = json.loads(task_path.read_text())
        verification = json.loads(verification_path.read_text())
        artifacts = json.loads(artifact_path.read_text())

        initial = VALIDATOR_MODULE.core_probe([], [task_path], [patch_path], self.marshal)
        spec_digest = initial["jcs"][0]
        patch_digest = initial["raw"][0]
        verification["specDigest"] = spec_digest
        verification["observed"]["diffDigest"] = patch_digest
        artifacts["artifacts"][0]["digest"] = patch_digest
        artifacts["artifacts"][0]["byteSize"] = patch_path.stat().st_size
        write_json(verification_path, verification)
        write_json(artifact_path, artifacts)

        for finding in manifest["findings"]:
            finding["outcomeKey"] = VALIDATOR_MODULE.outcome_key(finding)
            finding["id"] = VALIDATOR_MODULE.stable_finding_id(finding)
        first_id, second_id = (finding["id"] for finding in manifest["findings"])
        manifest["findings"][0]["parentFindingId"] = first_id
        previous = VALIDATOR_MODULE.projected_finding(manifest["findings"][0])
        previous.update({
            "evidenceDigest": "sha256:" + "a" * 64,
            "snapshotDigest": "sha256:" + "b" * 64,
            "verificationDigest": "sha256:" + "c" * 64,
            "candidateDigest": "sha256:" + "d" * 64,
        })
        packet["previousBlockingFindings"] = [previous]
        evidence_digests = VALIDATOR_MODULE.core_probe(
            [], [verification_path, artifact_path, worker_path], [], self.marshal
        )["jcs"]
        packet["specDigest"] = spec_digest
        packet["diffDigest"] = patch_digest
        packet["verificationDigest"] = evidence_digests[0]
        packet["artifactManifestDigest"] = evidence_digests[1]
        packet["workerResultDigests"] = [evidence_digests[2]]
        write_json(packet_path, packet)

        fresh = manifest["freshness"]
        fresh.update({
            "sourceHead": head,
            "specDigest": spec_digest,
            "diffDigest": patch_digest,
            "verificationDigest": packet["verificationDigest"],
            "artifactManifestDigest": packet["artifactManifestDigest"],
            "reviewPacketDigest": VALIDATOR_MODULE.core_probe([], [packet_path], [], self.marshal)["jcs"][0],
        })
        fresh["fingerprintDigest"] = VALIDATOR_MODULE.canonical_digest(
            {key: value for key, value in fresh.items() if key != "fingerprintDigest"}
        )
        manifest["negativeFixtures"][0]["findingId"] = first_id
        manifest["negativeFixtures"][1]["findingId"] = second_id
        for fixture in manifest["negativeFixtures"]:
            fixture["digest"] = VALIDATOR_MODULE.file_digest(root / fixture["path"])
            receipt = fixture["receipt"]
            receipt["argv"][0] = sys.executable
            receipt["inputDigest"] = fixture["digest"]
            receipt["reasonCode"] = fixture["expectedReasonCode"]
            completed = subprocess.run(receipt["argv"], cwd=root, check=False, capture_output=True)
            receipt["exitCode"] = completed.returncode
            receipt["outputDigest"] = "sha256:" + hashlib.sha256(completed.stdout).hexdigest()
        write_json(manifest_path, manifest)

        state["specDigest"] = spec_digest
        write_json(state_path, state)

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
                "--marshal", str(self.marshal),
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

    def test_previous_p1_can_close_with_fresh_evidence(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root, fixtures = self.prepared(directory)
            manifest = self.load(fixtures, "manifest.json")
            manifest["findings"][0]["classification"] = "closed-previous"
            manifest["findings"][0]["disposition"] = "closed-previous"
            write_json(fixtures / "manifest.json", manifest)
            decision = self.load(fixtures, "review-decision.json")
            decision["blockingFindings"] = [
                VALIDATOR_MODULE.projected_finding(manifest["findings"][1])
            ]
            write_json(fixtures / "review-decision.json", decision)
            completed = self.invoke(root, fixtures)
            self.assertEqual(completed.returncode, 0, completed.stderr)
            payload = json.loads(completed.stdout)
            self.assertEqual(payload["closedPreviousFindings"], 1)
            self.assertEqual(payload["blockingFindings"], 1)

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
            duplicate["subject"] = "renamed-subject-must-not-split-outcome"
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
            self.assert_failure(self.invoke(root, fixtures), "previous-finding-lineage-mismatch")

    def test_outcome_key_is_bound_to_closed_outcome(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root, fixtures = self.prepared(directory)
            manifest = self.load(fixtures, "manifest.json")
            manifest["findings"][1]["outcomeKey"] = "O-aaaaaaaaaaaaaaaaaaaaaaaa"
            write_json(fixtures / "manifest.json", manifest)
            self.assert_failure(self.invoke(root, fixtures), "finding-id-unstable")

    def test_changed_packet_input_is_stale_digest(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root, fixtures = self.prepared(directory)
            verification = self.load(fixtures, "verification-report.json")
            verification["gates"][0]["summary"] = "changed after packet generation"
            write_json(fixtures / "verification-report.json", verification)
            self.assert_failure(self.invoke(root, fixtures), "stale-digest")

    def test_every_packet_input_is_nofollow_and_digest_bound(self) -> None:
        mutations = (
            ("task-spec.json", lambda value: value["metadata"].update(title="mutated"), "stale-digest"),
            ("verification-report.json", lambda value: value["gates"][0].update(summary="mutated"), "stale-digest"),
            ("artifact-manifest.json", lambda value: value.update(generatedAt="2026-08-20T00:00:02Z"), "stale-digest"),
            ("worker-result.json", lambda value: value.update(summary="mutated"), "stale-review-packet"),
        )
        for name, mutate, reason in mutations:
            with self.subTest(mode="mutation", input=name), tempfile.TemporaryDirectory() as directory:
                root, fixtures = self.prepared(directory)
                value = self.load(fixtures, name)
                mutate(value)
                write_json(fixtures / name, value)
                self.assert_failure(self.invoke(root, fixtures), reason)
        with self.subTest(mode="mutation", input="observed.patch"), tempfile.TemporaryDirectory() as directory:
            root, fixtures = self.prepared(directory)
            (fixtures / "observed.patch").write_bytes(b"mutated patch\n")
            self.assert_failure(self.invoke(root, fixtures), "patch-digest-mismatch")

        for name in (
            "task-spec.json", "observed.patch", "verification-report.json",
            "artifact-manifest.json", "worker-result.json",
        ):
            with self.subTest(mode="missing", input=name), tempfile.TemporaryDirectory() as directory:
                root, fixtures = self.prepared(directory)
                (fixtures / name).unlink()
                self.assert_failure(self.invoke(root, fixtures), "fixture-unreadable")

        with self.subTest(mode="symlink"), tempfile.TemporaryDirectory() as directory:
            root, fixtures = self.prepared(directory)
            task_path = fixtures / "task-spec.json"
            target_path = fixtures / "task-spec-target.json"
            task_path.rename(target_path)
            task_path.symlink_to(target_path.name)
            self.assert_failure(self.invoke(root, fixtures), "path-symlink-rejected")

    def test_every_core_evidence_contract_is_validated(self) -> None:
        cases = (
            ("review-packet.json", "generatedAt"),
            ("review-decision.json", "summary"),
            ("run-state.json", "sequence"),
            ("verification-report.json", "status"),
            ("artifact-manifest.json", "generatedAt"),
            ("worker-result.json", "status"),
        )
        for name, required_field in cases:
            with self.subTest(kind=name, missing=required_field), tempfile.TemporaryDirectory() as directory:
                root, fixtures = self.prepared(directory)
                value = self.load(fixtures, name)
                del value[required_field]
                write_json(fixtures / name, value)
                self.assert_failure(self.invoke(root, fixtures), "core-contract-invalid")

    def test_closure_refs_require_passing_digest_bound_verifier_evidence(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root, fixtures = self.prepared(directory)
            manifest = self.load(fixtures, "manifest.json")
            manifest["findings"][0]["requiredOutcome"]["verificationRefs"] = [
                {"kind": "gate", "id": "gate-identity"}
            ]
            write_json(fixtures / "manifest.json", manifest)
            self.assert_failure(self.invoke(root, fixtures), "verification-ref-missing")
        with tempfile.TemporaryDirectory() as directory:
            root, fixtures = self.prepared(directory)
            manifest = self.load(fixtures, "manifest.json")
            manifest["findings"][0]["requiredOutcome"]["verificationRefs"] = [
                {"kind": "gate", "id": "gate-unbound-pass"}
            ]
            write_json(fixtures / "manifest.json", manifest)
            self.assert_failure(self.invoke(root, fixtures), "verification-ref-missing")
        with tempfile.TemporaryDirectory() as directory:
            root, fixtures = self.prepared(directory)
            manifest = self.load(fixtures, "manifest.json")
            manifest["findings"][0]["observableDefect"]["evidenceRefs"] = [
                {"kind": "artifact", "id": "absent-verifier-artifact"}
            ]
            write_json(fixtures / "manifest.json", manifest)
            self.assert_failure(self.invoke(root, fixtures), "verification-ref-missing")

    def test_raw_patch_requires_matching_validated_artifact_digest(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root, fixtures = self.prepared(directory)
            artifacts = self.load(fixtures, "artifact-manifest.json")
            artifacts["artifacts"][0]["digest"] = "sha256:" + "0" * 64
            write_json(fixtures / "artifact-manifest.json", artifacts)
            artifact_digest = VALIDATOR_MODULE.core_probe(
                [], [fixtures / "artifact-manifest.json"], [], self.marshal
            )["jcs"][0]
            packet = self.load(fixtures, "review-packet.json")
            packet["artifactManifestDigest"] = artifact_digest
            write_json(fixtures / "review-packet.json", packet)
            packet_digest = VALIDATOR_MODULE.core_probe(
                [], [fixtures / "review-packet.json"], [], self.marshal
            )["jcs"][0]
            manifest = self.load(fixtures, "manifest.json")
            manifest["freshness"]["artifactManifestDigest"] = artifact_digest
            manifest["freshness"]["reviewPacketDigest"] = packet_digest
            manifest["freshness"]["fingerprintDigest"] = VALIDATOR_MODULE.canonical_digest(
                {key: value for key, value in manifest["freshness"].items() if key != "fingerprintDigest"}
            )
            write_json(fixtures / "manifest.json", manifest)
            decision = self.load(fixtures, "review-decision.json")
            decision["artifactManifestDigest"] = artifact_digest
            decision["reviewPacketDigest"] = packet_digest
            write_json(fixtures / "review-decision.json", decision)
            self.assert_failure(self.invoke(root, fixtures), "patch-artifact-mismatch")

    def test_negative_fixture_receipt_is_execution_bound(self) -> None:
        cases = (
            (lambda receipt: receipt.update(outputDigest="sha256:" + "0" * 64), "negative-fixture-receipt-invalid"),
            (lambda receipt: receipt.update(inputDigest="sha256:" + "0" * 64), "negative-fixture-receipt-invalid"),
            (lambda receipt: receipt.update(exitCode=2), "negative-fixture-receipt-invalid"),
            (lambda receipt: receipt.update(reasonCode="wrong-reason"), "negative-fixture-wrong-reason"),
            (lambda receipt: receipt["argv"].__setitem__(1, "-E"), "negative-fixture-receipt-invalid"),
        )
        for mutate, reason in cases:
            with self.subTest(reason=reason), tempfile.TemporaryDirectory() as directory:
                root, fixtures = self.prepared(directory)
                manifest = self.load(fixtures, "manifest.json")
                mutate(manifest["negativeFixtures"][0]["receipt"])
                write_json(fixtures / "manifest.json", manifest)
                self.assert_failure(self.invoke(root, fixtures), reason)

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
            manifest["freshness"]["reviewPacketDigest"] = VALIDATOR_MODULE.core_probe(
                [], [fixtures / "review-packet.json"], [], self.marshal
            )["jcs"][0]
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
