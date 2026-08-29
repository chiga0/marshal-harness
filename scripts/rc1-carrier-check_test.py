#!/usr/bin/env python3
"""Hostile contract tests for rc1-carrier-check.py.

The fixture binary is inert data with executable mode.  This test never
executes a candidate or creates a publication effect.
"""

from __future__ import annotations

import copy
import hashlib
import importlib.util
import json
import os
import pathlib
import shutil
import subprocess
import sys
import tempfile
import unittest


ROOT = pathlib.Path(__file__).resolve().parent.parent
CHECKER = ROOT / "scripts" / "rc1-carrier-check.py"
SCHEMA = ROOT / "schemas" / "release" / "rc1-canary-receipt.schema.json"
VERSION = "1.0.0-rc1"
BINARY_NAME = f"marshal_{VERSION}_darwin_arm64"
SOURCE_HEAD = "1" * 40
WORKFLOW_RUN_ID = "123456"
ARTIFACT_ID = "654321"
ARTIFACT_DIGEST = "a" * 64
AUTHORITY_HEAD = "sha256:" + "b" * 64


def digest(mark: str) -> str:
    return "sha256:" + mark * 64


def sha(raw: bytes) -> str:
    return "sha256:" + hashlib.sha256(raw).hexdigest()


def raw_sha(raw: bytes) -> str:
    return hashlib.sha256(raw).hexdigest()


def payload_identity(contents: dict[str, bytes]) -> tuple[str, int]:
    hasher = hashlib.sha256()
    hasher.update(b"marshal.rc1-carrier-payload.v1\n")
    total = 0
    for name in (BINARY_NAME, "RELEASE-MANIFEST", "SHA256SUMS"):
        raw = contents[name]
        total += len(raw)
        hasher.update(f"{name} {len(raw)} {raw_sha(raw)}\n".encode("ascii"))
    return "sha256:" + hasher.hexdigest(), total


def detached_digest(receipt: dict[str, object]) -> str:
    value = copy.deepcopy(receipt)
    value["receiptDigest"] = ""
    canonical = json.dumps(
        value,
        ensure_ascii=False,
        sort_keys=True,
        separators=(",", ":"),
        allow_nan=False,
    ).encode("utf-8")
    return sha(canonical)


class CarrierFixture:
    def __init__(self) -> None:
        self.temporary = tempfile.TemporaryDirectory(prefix="marshal-rc1-carrier-test.")
        self.root = pathlib.Path(self.temporary.name).resolve()
        self.carrier = self.root / "carrier"
        self.carrier.mkdir(mode=0o700)
        self.binary = b"inert fixture bytes; never execute\n"
        self.build_date = "2026-08-29T00:00:00Z"
        self.go_version = "go1.26.6"
        manifest = (
            "schemaVersion marshal.rc1-release-manifest.v1\n"
            "repository https://github.com/chiga0/marshal-harness.git\n"
            "tag v1.0.0-rc1\n"
            f"sourceHead {SOURCE_HEAD}\n"
            f"buildDate {self.build_date}\n"
            f"goVersion {self.go_version}\n"
            "buildFlags -trimpath,-buildvcs=false,-mod=readonly,-buildid=\n"
            f"asset {raw_sha(self.binary)} {len(self.binary)} {BINARY_NAME} "
            "darwin arm64 darwin-local-dogfood\n"
        ).encode("utf-8")
        sums = f"{raw_sha(self.binary)}  {BINARY_NAME}\n".encode("ascii")
        self.contents = {
            BINARY_NAME: self.binary,
            "RELEASE-MANIFEST": manifest,
            "SHA256SUMS": sums,
        }
        for name, raw in self.contents.items():
            path = self.carrier / name
            path.write_bytes(raw)
            path.chmod(0o755 if name == BINARY_NAME else 0o644)
        payload_digest, payload_size = payload_identity(self.contents)
        binary_digest = sha(self.binary)
        self.receipt: dict[str, object] = {
            "schemaVersion": "marshal.rc1-canary-receipt.v1",
            "tag": "v1.0.0-rc1",
            "sourceHead": SOURCE_HEAD,
            "candidateWorkflow": {
                "runId": int(WORKFLOW_RUN_ID),
                "artifactId": int(ARTIFACT_ID),
                "artifactDigest": "sha256:" + ARTIFACT_DIGEST,
            },
            "payload": {
                "schemaVersion": "marshal.rc1-carrier-payload.v1",
                "sha256": payload_digest,
                "size": payload_size,
            },
            "manifest": {
                "path": "RELEASE-MANIFEST",
                "sha256": sha(manifest),
                "size": len(manifest),
            },
            "checksums": {
                "path": "SHA256SUMS",
                "sha256": sha(sums),
                "size": len(sums),
            },
            "binary": {
                "path": BINARY_NAME,
                "sha256": binary_digest,
                "size": len(self.binary),
                "version": VERSION,
                "buildDate": self.build_date,
                "goVersion": self.go_version,
                "os": "darwin",
                "arch": "arm64",
                "profile": "darwin-local-dogfood",
            },
            "activation": {
                "activationDigest": digest("2"),
                "identitySubjectDigest": digest("3"),
                "currentObjectObservationDigest": digest("4"),
                "currentCanonicalPath": "/opt/marshal/repository/bin/marshal",
                "currentObjectRawSHA256": binary_digest,
                "currentObjectSize": len(self.binary),
                "sourceHead": SOURCE_HEAD,
                "profile": "darwin-local-dogfood",
                "localSelfIdentityBindingDigest": digest("a"),
            },
            "canary": {
                "taskId": "RC1-CANARY",
                "runId": "rc1-live-run",
                "attemptId": "attempt-1",
                "specDigest": digest("d"),
                "baseSha": "2" * 40,
                "artifactManifestDigest": digest("e"),
                "workerResultDigests": [digest("5")],
                "localSelfIdentityBindingDigest": digest("a"),
                "agentProvider": "pi",
                "agentVersion": "0.84.3",
                "invocation": "real",
                "workerActorId": "worker:pi-0.84.3",
                "reviewPacket": {
                    "digest": digest("6"),
                    "runId": "rc1-live-run",
                    "attemptId": "attempt-1",
                    "reviewRound": 1,
                    "specDigest": digest("d"),
                    "baseSha": "2" * 40,
                    "verificationDigest": digest("7"),
                    "artifactManifestDigest": digest("e"),
                    "workerResultDigests": [digest("5")],
                    "evidenceDigest": digest("8"),
                    "localSelfIdentityBindingDigest": digest("a"),
                },
                "verification": {
                    "digest": digest("7"),
                    "runId": "rc1-live-run",
                    "attemptId": "attempt-1",
                    "specDigest": digest("d"),
                    "artifactManifestDigest": digest("e"),
                    "workerResultDigests": [digest("5")],
                    "evidenceDigest": digest("8"),
                    "localSelfIdentityBindingDigest": digest("a"),
                    "verifier": {
                        "type": "deterministic-verifier",
                        "id": "verifier:independent",
                    },
                    "status": "pass",
                    "independent": True,
                },
                "evidence": {
                    "digest": digest("8"),
                    "runId": "rc1-live-run",
                    "attemptId": "attempt-1",
                    "specDigest": digest("d"),
                    "baseSha": "2" * 40,
                    "artifactManifestDigest": digest("e"),
                    "workerResultDigests": [digest("5")],
                    "localSelfIdentityBindingDigest": digest("a"),
                },
                "reviewDecision": {
                    "digest": digest("9"),
                    "runId": "rc1-live-run",
                    "reviewRound": 1,
                    "reviewer": {
                        "type": "lead-agent",
                        "id": "reviewer:independent",
                    },
                    "independent": True,
                    "specDigest": digest("d"),
                    "reviewPacketDigest": digest("6"),
                    "verificationDigest": digest("7"),
                    "artifactManifestDigest": digest("e"),
                    "evidenceDigest": digest("8"),
                    "localSelfIdentityBindingDigest": digest("a"),
                    "verdict": "accept",
                    "blockingFindingCount": 0,
                    "publicationRecommendation": "not-applicable",
                },
                "outcome": {
                    "digest": digest("c"),
                    "runId": "rc1-live-run",
                    "terminalState": "ACCEPTED",
                    "verdict": "accept",
                    "finalReviewDigest": digest("9"),
                    "finalEvidenceDigest": digest("8"),
                    "publication": "none",
                },
                "publication": "none",
            },
            "authority": {
                "currentHeadDigest": AUTHORITY_HEAD,
                "revision": 42,
                "outcomeDigest": digest("c"),
            },
            "receiptDigest": "",
        }
        self.expected_receipt_digest = ""
        self.write_receipt(admit=True)

    def close(self) -> None:
        self.temporary.cleanup()

    def write_receipt(self, admit: bool = False) -> None:
        self.receipt["receiptDigest"] = detached_digest(self.receipt)
        if admit:
            self.expected_receipt_digest = str(self.receipt["receiptDigest"]).removeprefix("sha256:")
        path = self.carrier / "RC1-CANARY-RECEIPT.json"
        path.write_text(
            json.dumps(self.receipt, ensure_ascii=False, sort_keys=True, separators=(",", ":")) + "\n",
            encoding="utf-8",
        )
        path.chmod(0o644)

    def command(self, *overrides: str) -> list[str]:
        expected = (
            SOURCE_HEAD,
            WORKFLOW_RUN_ID,
            ARTIFACT_ID,
            ARTIFACT_DIGEST,
            AUTHORITY_HEAD,
            self.expected_receipt_digest,
        )
        values = overrides if overrides else expected
        return [sys.executable, "-I", "-B", str(CHECKER), str(self.carrier), *values]

    def run(self, *overrides: str) -> subprocess.CompletedProcess[str]:
        return subprocess.run(
            self.command(*overrides),
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            check=False,
            env={"LC_ALL": "C", "PATH": "/usr/bin:/bin"},
        )


class RC1CarrierCheckTest(unittest.TestCase):
    def setUp(self) -> None:
        self.fixture = CarrierFixture()

    def tearDown(self) -> None:
        self.fixture.close()

    def assert_rejected(self, message: str) -> None:
        result = self.fixture.run()
        self.assertNotEqual(result.returncode, 0, msg=f"{message}: {result.stdout}")
        self.assertIn("[rc1-carrier-check] ERROR:", result.stderr, msg=message)

    def mutate(self, function) -> None:
        function(self.fixture.receipt)
        self.fixture.write_receipt(admit=True)

    def test_valid_closed_carrier(self) -> None:
        result = self.fixture.run()
        self.assertEqual(result.returncode, 0, msg=result.stderr)
        self.assertEqual(result.stdout, "[rc1-carrier-check] PASS\n")

    def test_schema_is_one_draft_2020_12_closed_document(self) -> None:
        raw = SCHEMA.read_text(encoding="utf-8")
        schema = json.loads(raw)
        self.assertEqual(schema["$schema"], "https://json-schema.org/draft/2020-12/schema")
        self.assertFalse(schema["additionalProperties"])
        self.assertEqual(set(schema["required"]), set(schema["properties"]))

    def test_extra_member_rejected(self) -> None:
        (self.fixture.carrier / "unexpected").write_text("extra", encoding="utf-8")
        self.assert_rejected("extra carrier member")

    def test_path_traversal_claim_rejected(self) -> None:
        self.mutate(lambda value: value["binary"].__setitem__("path", "../marshal"))
        self.assert_rejected("path traversal")

    def test_symlink_member_rejected(self) -> None:
        target = self.fixture.root / "external-manifest"
        target.write_bytes(self.fixture.contents["RELEASE-MANIFEST"])
        path = self.fixture.carrier / "RELEASE-MANIFEST"
        path.unlink()
        path.symlink_to(target)
        self.assert_rejected("symlink member")

    def test_parent_symlink_rejected(self) -> None:
        linked = self.fixture.root / "linked-carrier"
        linked.symlink_to(self.fixture.carrier, target_is_directory=True)
        command = self.fixture.command()
        command[4] = str(linked)
        result = subprocess.run(command, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, check=False)
        self.assertNotEqual(result.returncode, 0)

    def test_hardlink_member_rejected(self) -> None:
        os.link(self.fixture.carrier / BINARY_NAME, self.fixture.root / "second-link")
        self.assert_rejected("hardlinked member")

    def test_wrong_mode_rejected(self) -> None:
        (self.fixture.carrier / "RELEASE-MANIFEST").chmod(0o600)
        self.assert_rejected("member mode drift")

    def test_empty_member_rejected(self) -> None:
        (self.fixture.carrier / BINARY_NAME).write_bytes(b"")
        self.assert_rejected("empty member")

    def test_oversize_member_rejected_without_reading_it(self) -> None:
        with (self.fixture.carrier / BINARY_NAME).open("r+b") as handle:
            handle.truncate((256 << 20) + 1)
        self.assert_rejected("oversize member")

    def test_binary_size_or_digest_drift_rejected(self) -> None:
        with (self.fixture.carrier / BINARY_NAME).open("ab") as handle:
            handle.write(b"drift")
        self.assert_rejected("binary size and digest drift")

    def test_duplicate_json_member_rejected(self) -> None:
        path = self.fixture.carrier / "RC1-CANARY-RECEIPT.json"
        raw = path.read_text(encoding="utf-8")
        path.write_text(raw.replace('{"activation":', '{"tag":"v1.0.0-rc1","activation":', 1), encoding="utf-8")
        self.assert_rejected("duplicate JSON member")

    def test_unknown_receipt_field_rejected(self) -> None:
        self.mutate(lambda value: value.__setitem__("publicationAuthority", True))
        self.assert_rejected("unknown receipt field")

    def test_wrong_record_run_rejected(self) -> None:
        self.mutate(lambda value: value["canary"]["reviewPacket"].__setitem__("runId", "other-run"))
        self.assert_rejected("cross-Run record replay")

    def test_non_accept_decision_rejected(self) -> None:
        self.mutate(lambda value: value["canary"]["reviewDecision"].__setitem__("verdict", "rework"))
        self.assert_rejected("non-accept ReviewDecision")

    def test_wrong_outcome_binding_rejected(self) -> None:
        self.mutate(lambda value: value["canary"]["outcome"].__setitem__("finalReviewDigest", digest("d")))
        self.assert_rejected("Outcome not bound to ReviewDecision")

    def test_non_accepted_outcome_rejected(self) -> None:
        self.mutate(lambda value: value["canary"]["outcome"].__setitem__("terminalState", "BLOCKED"))
        self.assert_rejected("non-ACCEPTED Outcome")

    def test_non_independent_verifier_rejected(self) -> None:
        def mutation(value) -> None:
            value["canary"]["verification"]["verifier"]["id"] = value["canary"]["workerActorId"]

        self.mutate(mutation)
        self.assert_rejected("worker self-verification")

    def test_non_independent_reviewer_rejected(self) -> None:
        self.mutate(lambda value: value["canary"]["reviewDecision"].__setitem__("independent", False))
        self.assert_rejected("non-independent reviewer")

    def test_external_source_head_drift_rejected(self) -> None:
        result = self.fixture.run("f" * 40, WORKFLOW_RUN_ID, ARTIFACT_ID, ARTIFACT_DIGEST, AUTHORITY_HEAD, self.fixture.expected_receipt_digest)
        self.assertNotEqual(result.returncode, 0)

    def test_external_workflow_run_drift_rejected(self) -> None:
        result = self.fixture.run(SOURCE_HEAD, "123457", ARTIFACT_ID, ARTIFACT_DIGEST, AUTHORITY_HEAD, self.fixture.expected_receipt_digest)
        self.assertNotEqual(result.returncode, 0)

    def test_external_artifact_id_drift_rejected(self) -> None:
        result = self.fixture.run(SOURCE_HEAD, WORKFLOW_RUN_ID, "654322", ARTIFACT_DIGEST, AUTHORITY_HEAD, self.fixture.expected_receipt_digest)
        self.assertNotEqual(result.returncode, 0)

    def test_external_artifact_digest_drift_rejected(self) -> None:
        result = self.fixture.run(SOURCE_HEAD, WORKFLOW_RUN_ID, ARTIFACT_ID, "e" * 64, AUTHORITY_HEAD, self.fixture.expected_receipt_digest)
        self.assertNotEqual(result.returncode, 0)

    def test_external_authority_head_drift_rejected(self) -> None:
        result = self.fixture.run(SOURCE_HEAD, WORKFLOW_RUN_ID, ARTIFACT_ID, ARTIFACT_DIGEST, digest("e"), self.fixture.expected_receipt_digest)
        self.assertNotEqual(result.returncode, 0)

    def test_external_receipt_digest_drift_rejected(self) -> None:
        result = self.fixture.run(SOURCE_HEAD, WORKFLOW_RUN_ID, ARTIFACT_ID, ARTIFACT_DIGEST, AUTHORITY_HEAD, "e" * 64)
        self.assertNotEqual(result.returncode, 0)

    def test_cross_profile_rejected(self) -> None:
        self.mutate(lambda value: value["binary"].__setitem__("profile", "darwin-managed-development"))
        self.assert_rejected("cross-profile replay")

    def test_publication_enabled_rejected(self) -> None:
        self.mutate(lambda value: value["canary"].__setitem__("publication", "publish"))
        self.assert_rejected("publication-enabled canary")

    def test_receipt_digest_drift_rejected(self) -> None:
        self.fixture.receipt["receiptDigest"] = digest("f")
        path = self.fixture.carrier / "RC1-CANARY-RECEIPT.json"
        path.write_text(json.dumps(self.fixture.receipt, sort_keys=True, separators=(",", ":")) + "\n", encoding="utf-8")
        self.assert_rejected("receipt digest drift")

    def test_recomputed_replacement_bundle_cannot_self_admit(self) -> None:
        original_expected = self.fixture.expected_receipt_digest
        canary = self.fixture.receipt["canary"]
        canary["runId"] = "replacement-run"
        for name in ("reviewPacket", "verification", "evidence", "reviewDecision", "outcome"):
            canary[name]["runId"] = "replacement-run"
        self.fixture.receipt["authority"]["currentHeadDigest"] = AUTHORITY_HEAD
        self.fixture.write_receipt(admit=False)
        self.assertEqual(self.fixture.expected_receipt_digest, original_expected)
        self.assert_rejected("receipt cannot self-admit a recomputed lifecycle bundle")

    def test_missing_external_receipt_digest_is_rejected(self) -> None:
        result = subprocess.run(
            self.fixture.command()[:-1],
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            check=False,
        )
        self.assertNotEqual(result.returncode, 0)

    def load_checker_module(self):
        name = "rc1_carrier_check_under_test"
        spec = importlib.util.spec_from_file_location(name, CHECKER)
        self.assertIsNotNone(spec)
        self.assertIsNotNone(spec.loader)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        return module

    def assert_post_validation_mutation_rejected(self, mutation) -> None:
        module = self.load_checker_module()
        original = module.validate_receipt

        def mutate_after_validation(*arguments, **keywords):
            original(*arguments, **keywords)
            mutation()

        module.validate_receipt = mutate_after_validation
        with self.assertRaises(SystemExit) as raised:
            module.main(self.fixture.command()[4:])
        self.assertIn("[rc1-carrier-check] ERROR:", str(raised.exception))

    def test_member_rename_aba_after_parse_rejected(self) -> None:
        def mutation() -> None:
            replacement = self.fixture.root / "replacement-binary"
            replacement.write_bytes(self.fixture.binary)
            replacement.chmod(0o755)
            os.replace(replacement, self.fixture.carrier / BINARY_NAME)

        self.assert_post_validation_mutation_rejected(mutation)

    def test_member_in_place_change_after_parse_rejected(self) -> None:
        def mutation() -> None:
            path = self.fixture.carrier / BINARY_NAME
            changed = b"X" + self.fixture.binary[1:]
            self.assertEqual(len(changed), len(self.fixture.binary))
            with path.open("r+b") as handle:
                handle.write(changed)
                handle.flush()
                os.fsync(handle.fileno())

        self.assert_post_validation_mutation_rejected(mutation)


if __name__ == "__main__":
    unittest.main(verbosity=2)
