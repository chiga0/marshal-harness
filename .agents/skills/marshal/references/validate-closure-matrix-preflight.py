#!/usr/bin/env python3
"""Fail-closed operator-local preflight for one rework closure matrix."""

from __future__ import annotations

import argparse
import hashlib
import importlib.util
import json
from pathlib import Path
import re
import stat
import subprocess
import sys
import unicodedata


DIGEST_RE = re.compile(r"^sha256:[0-9a-f]{64}$")
FINDING_ID_RE = re.compile(r"^F-[0-9a-f]{24}$")
DEFECT_PREFIX = "marshal.closure.defect.v1:"
OUTCOME_PREFIX = "marshal.closure.outcome.v1:"
VAGUE_ENGLISH = re.compile(
    r"\b(?:complete|correct|adequate|sufficient|proper|relevant|as needed|etc)\b",
    re.IGNORECASE,
)
VAGUE_CHINESE = ("完整", "正确", "充分", "适当", "相关", "必要", "等价处理", "等等")


class PreflightError(Exception):
    def __init__(self, reason_code: str, message: str):
        super().__init__(message)
        self.reason_code = reason_code


def fail(reason_code: str, message: str) -> None:
    raise PreflightError(reason_code, message)


def load_support_module(path: Path):
    spec = importlib.util.spec_from_file_location("marshal_acceptance_preflight_support", path)
    if spec is None or spec.loader is None:
        fail("schema-document-invalid", "cannot load adjacent schema validator")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def load_json(path: Path, label: str) -> dict:
    def reject_duplicates(pairs: list[tuple[str, object]]) -> dict:
        result: dict = {}
        for key, value in pairs:
            if key in result:
                fail("duplicate-json-key", f"{label} contains a duplicate object member")
            result[key] = value
        return result

    try:
        value = json.loads(
            path.read_text(encoding="utf-8"),
            object_pairs_hook=reject_duplicates,
        )
    except PreflightError:
        raise
    except (OSError, UnicodeError, json.JSONDecodeError) as error:
        fail("invalid-json", f"cannot read {label}: {error.__class__.__name__}")
    if not isinstance(value, dict):
        fail("invalid-json", f"{label} must be a JSON object")
    return value


def canonical_bytes(value: object) -> bytes:
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")


def canonical_digest(value: object) -> str:
    return "sha256:" + hashlib.sha256(canonical_bytes(value)).hexdigest()


def file_digest(path: Path) -> str:
    try:
        return "sha256:" + hashlib.sha256(path.read_bytes()).hexdigest()
    except OSError as error:
        fail("fixture-unreadable", f"cannot read fixture: {error.__class__.__name__}")


def clean_relative_path(value: object, label: str) -> str:
    if not isinstance(value, str) or not value:
        fail("finding-location-imprecise", f"{label} must be a non-empty relative path")
    path = Path(value)
    if path.is_absolute() or ".." in path.parts or "\\" in value or "\x00" in value or value == ".":
        fail("finding-location-imprecise", f"{label} must be a clean relative path")
    return path.as_posix()


def secure_file(root: Path, value: object, label: str) -> Path:
    relative = clean_relative_path(value, label)
    root = root.resolve()
    current = root
    identities: list[tuple[Path, tuple[int, int, int]]] = []
    for part in Path(relative).parts:
        current = current / part
        try:
            metadata = current.lstat()
            if stat.S_ISLNK(metadata.st_mode):
                fail("path-symlink-rejected", f"{label} contains a symbolic link")
            identities.append((current, (metadata.st_dev, metadata.st_ino, metadata.st_mode)))
        except OSError as error:
            fail("fixture-unreadable", f"cannot inspect {label}: {error.__class__.__name__}")
    try:
        resolved = current.resolve(strict=True)
        resolved.relative_to(root)
    except (OSError, ValueError):
        fail("fixture-unreadable", f"{label} is missing or outside the declared root")
    for path, identity in identities:
        try:
            metadata = path.lstat()
        except OSError:
            fail("path-symlink-rejected", f"{label} changed during validation")
        if stat.S_ISLNK(metadata.st_mode) or (metadata.st_dev, metadata.st_ino, metadata.st_mode) != identity:
            fail("path-symlink-rejected", f"{label} changed during validation")
    if not current.is_file() or current.is_symlink():
        fail("fixture-unreadable", f"{label} must be a regular non-symlink file")
    return current


def jcs_file_digests(paths: list[Path]) -> list[str]:
    helper = Path(__file__).with_name("tests") / "closure_matrix_jcs_digest.go"
    module_root = Path(__file__).resolve().parents[4]
    completed = subprocess.run(
        ["go", "run", str(helper), *(str(path) for path in paths)],
        cwd=module_root,
        check=False,
        capture_output=True,
        text=True,
    )
    if completed.returncode != 0:
        fail("core-contract-invalid", "Core JCS digest helper rejected an evidence document")
    try:
        digests = json.loads(completed.stdout)
    except json.JSONDecodeError:
        fail("core-contract-invalid", "Core JCS digest helper returned invalid output")
    if not isinstance(digests, list) or len(digests) != len(paths) or any(not isinstance(item, str) or not DIGEST_RE.fullmatch(item) for item in digests):
        fail("core-contract-invalid", "Core JCS digest helper returned an invalid digest set")
    return digests


def require_digest(value: object, label: str) -> str:
    if not isinstance(value, str) or not DIGEST_RE.fullmatch(value):
        fail("manifest-schema-invalid", f"{label} must be a lowercase sha256 digest")
    return value


def contains_vague(value: str) -> bool:
    normalized = unicodedata.normalize("NFKC", value).casefold()
    return bool(VAGUE_ENGLISH.search(normalized)) or any(token in normalized for token in VAGUE_CHINESE)


def walk_strings(value: object):
    if isinstance(value, str):
        yield value
    elif isinstance(value, list):
        for item in value:
            yield from walk_strings(item)
    elif isinstance(value, dict):
        for item in value.values():
            yield from walk_strings(item)


def stable_identity(finding: dict) -> dict:
    location = finding["location"]
    return {
        "domainKey": finding["domainKey"],
        "ruleId": finding["ruleId"],
        "subject": finding["subject"],
        "location": {"kind": location["kind"], "locator": location["locator"]},
    }


def stable_finding_id(finding: dict) -> str:
    digest = hashlib.sha256(canonical_bytes(stable_identity(finding))).hexdigest()
    return "F-" + digest[:24]


def projected_finding(finding: dict) -> dict:
    description = {
        "classification": finding["classification"],
        "domainKey": finding["domainKey"],
        "observableDefect": finding["observableDefect"],
        "ruleId": finding["ruleId"],
        "subject": finding["subject"],
    }
    projected = {
        "id": finding["id"],
        "severity": finding["severity"],
        "title": finding["title"],
        "description": DEFECT_PREFIX + canonical_bytes(description).decode("utf-8"),
        "requiredOutcome": OUTCOME_PREFIX + canonical_bytes(finding["requiredOutcome"]).decode("utf-8"),
    }
    location = finding["location"]
    if location["kind"] == "source":
        projected["file"] = location["locator"]
        projected["line"] = location["line"]
    elif location["kind"] == "gate":
        projected["gateId"] = location["locator"]
    else:
        projected["artifactId"] = location["locator"]
    return projected


def validate_location(location: dict, finding_id: str) -> None:
    kind = location["kind"]
    if kind == "source":
        clean_relative_path(location["locator"], f"finding {finding_id} source location")
        if not isinstance(location.get("line"), int) or isinstance(location.get("line"), bool) or location["line"] < 1:
            fail("finding-location-imprecise", f"source finding {finding_id} requires an exact line")
    elif "line" in location:
        fail("finding-location-imprecise", f"non-source finding {finding_id} cannot carry a line")


def validate_outcome(finding: dict) -> None:
    outcome = finding["requiredOutcome"]
    finding_id = finding["id"]
    assertions = outcome["assertions"]
    identities: set[bytes] = set()
    for assertion in assertions:
        encoded = canonical_bytes(assertion)
        if encoded in identities:
            fail("required-outcome-unclosed", f"finding {finding_id} repeats an assertion")
        identities.add(encoded)
        operator = assertion["operator"]
        expected = assertion["expected"]
        if outcome["kind"] == "digest":
            if operator != "matches-digest" or any(not DIGEST_RE.fullmatch(item) for item in expected):
                fail("required-outcome-unclosed", f"finding {finding_id} digest outcome is not exact")
        if outcome["kind"] == "reason-code" and operator != "returns-reason-code":
            fail("required-outcome-unclosed", f"finding {finding_id} reason-code outcome has the wrong operator")
        if outcome["kind"] == "state-transition" and operator != "transitions-to":
            fail("required-outcome-unclosed", f"finding {finding_id} state outcome has the wrong operator")
        if outcome["kind"] in {"identity-tuple", "config-key", "env-key", "enum-set"} and operator not in {"equals", "contains-all"}:
            fail("required-outcome-unclosed", f"finding {finding_id} tuple outcome is not closed")
    if any(contains_vague(text) for text in walk_strings(finding)):
        fail("required-outcome-open-ended", f"finding {finding_id} contains open-ended wording")


def validate_refs(finding: dict, gate_ids: set[str], artifact_ids: set[str], fixtures: dict[str, dict]) -> None:
    finding_id = finding["id"]
    location = finding["location"]
    if location["kind"] == "gate" and location["locator"] not in gate_ids:
        fail("finding-location-imprecise", f"finding {finding_id} names an absent gate location")
    if location["kind"] == "artifact" and location["locator"] not in artifact_ids:
        fail("finding-location-imprecise", f"finding {finding_id} names an absent artifact location")
    for group in (finding["observableDefect"]["evidenceRefs"], finding["requiredOutcome"]["verificationRefs"]):
        for reference in group:
            available = gate_ids if reference["kind"] == "gate" else artifact_ids
            if reference["id"] not in available:
                fail("verification-ref-missing", f"finding {finding_id} references absent {reference['kind']}")
    refs = finding["requiredOutcome"]["negativeFixtureRefs"]
    if not refs:
        fail("negative-fixture-ref-missing", f"finding {finding_id} has no negative fixture")
    for fixture_id in refs:
        fixture = fixtures.get(fixture_id)
        if fixture is None or fixture["findingId"] != finding_id:
            fail("negative-fixture-ref-missing", f"finding {finding_id} has an unbound negative fixture")


def input_document(run_root: Path, packet: dict, key: str, label: str) -> tuple[Path, dict]:
    try:
        relative = packet["inputs"][key]
    except (KeyError, TypeError):
        fail("core-contract-invalid", f"ReviewPacket lacks {label} input")
    path = secure_file(run_root, relative, f"ReviewPacket.inputs.{key}")
    return path, load_json(path, label)


def validate_freshness(
    manifest: dict,
    packet: dict,
    decision: dict,
    run_state: dict,
    packet_path: Path,
    run_root: Path,
    source_root: Path,
) -> tuple[dict, dict]:
    fresh = manifest["freshness"]
    without_fingerprint = {key: value for key, value in fresh.items() if key != "fingerprintDigest"}
    if canonical_digest(without_fingerprint) != fresh["fingerprintDigest"]:
        fail("freshness-fingerprint-mismatch", "freshness fingerprint does not match its tuple")
    if jcs_file_digests([packet_path])[0] != fresh["reviewPacketDigest"]:
        fail("packet-digest-mismatch", "reviewPacketDigest does not match canonical ReviewPacket")
    if run_state.get("state") != "REVIEW_PENDING":
        fail("stale-review-packet", "RunState is not REVIEW_PENDING")
    packet_pairs = {
        "taskId": "taskId", "runId": "runId", "reviewRound": "reviewRound",
        "specDigest": "specDigest", "verificationDigest": "verificationDigest",
        "artifactManifestDigest": "artifactManifestDigest", "evidenceDigest": "evidenceDigest",
        "snapshotDigest": "snapshotDigest", "diffDigest": "diffDigest", "candidateDigest": "candidateDigest",
    }
    for manifest_key, packet_key in packet_pairs.items():
        if fresh.get(manifest_key) != packet.get(packet_key):
            fail("stale-review-packet", f"freshness {manifest_key} differs from ReviewPacket")
    for key in ("taskId", "runId", "reviewRound", "specDigest"):
        if fresh.get(key) != run_state.get(key):
            fail("stale-review-packet", f"freshness {key} differs from RunState")
    if fresh["attemptId"] != run_state.get("currentAttemptId"):
        fail("stale-review-packet", "freshness attemptId differs from RunState")
    completed = subprocess.run(
        ["git", "rev-parse", "HEAD"], cwd=source_root, check=False, capture_output=True, text=True
    )
    if completed.returncode != 0:
        fail("source-head-unreadable", "cannot resolve source worktree HEAD")
    if fresh["sourceHead"] != completed.stdout.strip():
        fail("stale-review-packet", "freshness sourceHead differs from worktree HEAD")

    verification_path, verification = input_document(run_root, packet, "verificationReport", "VerificationReport")
    artifact_path, artifacts = input_document(run_root, packet, "artifactManifest", "ArtifactManifest")
    worker_paths = packet.get("inputs", {}).get("workerResults", [])
    worker_digests = packet.get("workerResultDigests", [])
    attempts: set[str] = set()
    worker_files: list[Path] = []
    for index, relative in enumerate(worker_paths):
        path = secure_file(run_root, relative, f"ReviewPacket.inputs.workerResults[{index}]")
        worker = load_json(path, f"WorkerResult[{index}]")
        attempts.add(worker.get("attemptId", ""))
        worker_files.append(path)
    observed = jcs_file_digests([verification_path, artifact_path, *worker_files])
    if observed[0] != packet["verificationDigest"] or observed[1] != packet["artifactManifestDigest"]:
        fail("stale-digest", "packet input digest does not match current input bytes")
    observed_digests = observed[2:]
    if observed_digests != worker_digests or fresh["attemptId"] not in attempts:
        fail("stale-review-packet", "current attempt is not exactly bound by workerResults")
    decision_bindings = (
        "taskId", "runId", "reviewRound", "specDigest", "reviewPacketDigest",
        "verificationDigest", "artifactManifestDigest", "evidenceDigest",
    )
    if any(decision.get(key) != fresh.get(key) for key in decision_bindings):
        fail("decision-binding-mismatch", "ReviewDecision does not copy the freshness tuple")
    if decision.get("verdict") != "rework":
        fail("unsupported-verdict", "closure matrix preflight only admits verdict=rework")
    return verification, artifacts


def validate(
    manifest_path: Path,
    review_packet_path: Path,
    review_decision_path: Path,
    run_state_path: Path,
    root: Path,
    run_root: Path,
    source_root: Path,
    schema_path: Path,
) -> dict:
    support = load_support_module(Path(__file__).with_name("validate-acceptance-semantic-preflight.py"))
    schema = load_json(schema_path, "manifest schema")
    try:
        support.validate_schema_document(schema)
    except support.PreflightError as error:
        fail(error.reason_code, str(error))
    manifest = load_json(manifest_path, "closure matrix manifest")
    try:
        support.validate_schema_instance(manifest, schema, schema)
    except support.PreflightError as error:
        fail(error.reason_code, str(error))
    packet = load_json(review_packet_path, "ReviewPacket")
    decision = load_json(review_decision_path, "ReviewDecision")
    run_state = load_json(run_state_path, "RunState")
    verification, artifacts = validate_freshness(
        manifest, packet, decision, run_state, review_packet_path, run_root.resolve(), source_root.resolve()
    )

    fixture_by_id: dict[str, dict] = {}
    for fixture in manifest["negativeFixtures"]:
        if fixture["id"] in fixture_by_id:
            fail("finding-duplicate", "negative fixture id is duplicated")
        path = secure_file(root.resolve(), fixture["path"], f"negative fixture {fixture['id']}")
        if file_digest(path) != fixture["digest"]:
            fail("fixture-digest-mismatch", f"negative fixture {fixture['id']} digest differs")
        payload = load_json(path, f"negative fixture {fixture['id']}")
        if set(payload) != {"reasonCode"} or payload["reasonCode"] != fixture["expectedReasonCode"]:
            fail("negative-fixture-wrong-reason", f"negative fixture {fixture['id']} does not prove its reasonCode")
        fixture_by_id[fixture["id"]] = fixture

    gate_ids = {gate.get("id") for gate in verification.get("gates", []) if isinstance(gate, dict)}
    artifact_ids = {item.get("id") for item in artifacts.get("artifacts", []) if isinstance(item, dict)}
    referenced_fixture_ids = {
        fixture_id
        for finding in manifest["findings"]
        for fixture_id in finding["requiredOutcome"]["negativeFixtureRefs"]
    }
    if referenced_fixture_ids != set(fixture_by_id):
        fail("negative-fixture-ref-missing", "negative fixture inventory and finding references differ")
    previous = {item["id"]: item for item in packet.get("previousBlockingFindings", [])}
    if len(previous) != len(packet.get("previousBlockingFindings", [])):
        fail("finding-duplicate", "ReviewPacket contains duplicate previous finding ids")
    seen_ids: set[str] = set()
    seen_identity: set[bytes] = set()
    manifest_previous: set[str] = set()
    blocking: list[dict] = []
    nonblocking: list[dict] = []
    blocking_domains: set[str] = set()
    for finding in manifest["findings"]:
        finding_id = finding["id"]
        if finding_id in seen_ids:
            fail("finding-duplicate", f"finding id {finding_id} is duplicated")
        seen_ids.add(finding_id)
        identity = canonical_bytes(stable_identity(finding))
        if identity in seen_identity:
            fail("finding-split", f"closure identity for {finding_id} was duplicated or split")
        seen_identity.add(identity)
        validate_location(finding["location"], finding_id)
        if not FINDING_ID_RE.fullmatch(finding_id) or stable_finding_id(finding) != finding_id:
            fail("finding-id-unstable", f"finding {finding_id} does not match its stable identity")
        validate_outcome(finding)
        validate_refs(finding, gate_ids, artifact_ids, fixture_by_id)
        is_previous = finding_id in previous
        classification = finding["classification"]
        disposition = finding["disposition"]
        if is_previous:
            manifest_previous.add(finding_id)
            if classification not in {"continuation", "closed-previous"}:
                fail("previous-finding-lineage-mismatch", f"previous finding {finding_id} has invalid lineage")
            if classification == "continuation" and disposition != "blocking-rework":
                fail("previous-finding-lineage-mismatch", f"continued finding {finding_id} must remain blocking")
            if classification == "closed-previous":
                if disposition != "closed-previous":
                    fail("previous-finding-lineage-mismatch", f"closed finding {finding_id} has invalid disposition")
                old = previous[finding_id]
                changed = (
                    old.get("candidateDigest") != packet.get("candidateDigest")
                    if old.get("candidateDigest") and packet.get("candidateDigest")
                    else old.get("snapshotDigest") != packet.get("snapshotDigest")
                    or old.get("verificationDigest") != packet.get("verificationDigest")
                )
                if not changed:
                    fail("previous-finding-lineage-mismatch", f"finding {finding_id} closed without fresh evidence")
        else:
            if classification not in {"newly-discovered", "reviewer-omission"}:
                fail("previous-finding-lineage-mismatch", f"new finding {finding_id} lacks explicit discovery class")
            if disposition == "closed-previous":
                fail("previous-finding-lineage-mismatch", f"new finding {finding_id} cannot be closed-previous")
        if finding["severity"] in {"P0", "P1"} and disposition != "blocking-rework":
            fail("blocking-finding-omitted", f"{finding['severity']} finding {finding_id} is not blocking")
        if disposition == "blocking-rework":
            blocking.append(projected_finding(finding))
            blocking_domains.add(finding["domainKey"])
        elif disposition == "non-blocking":
            nonblocking.append(projected_finding(finding))
    missing_previous = set(previous) - manifest_previous
    if missing_previous:
        fail("previous-finding-lineage-mismatch", "a previous finding is neither continued nor explicitly closed")
    for finding in manifest["findings"]:
        if finding["severity"] == "P2" and finding["domainKey"] in blocking_domains and finding["disposition"] != "blocking-rework":
            fail("blocking-finding-omitted", f"same-domain P2 finding {finding['id']} must be closed in this rework")
    if decision.get("blockingFindings") != blocking or decision.get("nonBlockingFindings") != nonblocking:
        manifest_ids = {item["id"] for item in blocking + nonblocking}
        decision_ids = {
            item.get("id") for item in decision.get("blockingFindings", []) + decision.get("nonBlockingFindings", [])
            if isinstance(item, dict)
        }
        if decision_ids - manifest_ids:
            fail("unexpected-decision-finding", "ReviewDecision contains a finding outside the closure inventory")
        fail("blocking-finding-omitted", "ReviewDecision is not the exact deterministic projection of the closure inventory")
    return {
        "status": "pass",
        "taskId": manifest["freshness"]["taskId"],
        "runId": manifest["freshness"]["runId"],
        "reviewRound": manifest["freshness"]["reviewRound"],
        "fingerprintDigest": manifest["freshness"]["fingerprintDigest"],
        "blockingFindings": len(blocking),
        "nonBlockingFindings": len(nonblocking),
        "closedPreviousFindings": sum(1 for item in manifest["findings"] if item["disposition"] == "closed-previous"),
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--manifest", required=True, type=Path)
    parser.add_argument("--review-packet", required=True, type=Path)
    parser.add_argument("--review-decision", required=True, type=Path)
    parser.add_argument("--run-state", required=True, type=Path)
    parser.add_argument("--root", default=Path.cwd(), type=Path)
    parser.add_argument("--run-root", required=True, type=Path)
    parser.add_argument("--source-root", required=True, type=Path)
    parser.add_argument(
        "--schema", type=Path, default=Path(__file__).with_name("closure-matrix-preflight.schema.json")
    )
    arguments = parser.parse_args()
    try:
        result = validate(
            arguments.manifest, arguments.review_packet, arguments.review_decision,
            arguments.run_state, arguments.root, arguments.run_root,
            arguments.source_root, arguments.schema,
        )
    except PreflightError as error:
        print(json.dumps({"status": "fail", "reasonCode": error.reason_code, "message": str(error)}, ensure_ascii=False, sort_keys=True), file=sys.stderr)
        return 1
    print(json.dumps(result, ensure_ascii=False, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
