#!/usr/bin/env python3
"""Validate the immutable, out-of-band receipt binding for v1.0.0-rc1.

The named GitHub artifact is transport only.  This checker neither executes the
candidate nor grants publication authority.  Every external authority value is
supplied by the caller; the receipt is never allowed to authenticate itself.
"""

from __future__ import annotations

import copy
import datetime as dt
import hashlib
import json
import os
import re
import stat
import sys
from typing import NoReturn


VERSION = "1.0.0-rc1"
TAG = "v1.0.0-rc1"
BINARY_NAME = f"marshal_{VERSION}_darwin_arm64"
MANIFEST_NAME = "RELEASE-MANIFEST"
SUMS_NAME = "SHA256SUMS"
RECEIPT_NAME = "RC1-CANARY-RECEIPT.json"
EXPECTED_MEMBERS = frozenset((BINARY_NAME, MANIFEST_NAME, SUMS_NAME, RECEIPT_NAME))
PAYLOAD_MEMBERS = (BINARY_NAME, MANIFEST_NAME, SUMS_NAME)
MAX_BINARY_BYTES = 256 << 20
MAX_TEXT_BYTES = 1 << 20

SHA256 = re.compile(r"^sha256:[0-9a-f]{64}$")
RAW_SHA256 = re.compile(r"^[0-9a-f]{64}$")
SOURCE_HEAD = re.compile(r"^[0-9a-f]{40}$")
POSITIVE_DECIMAL = re.compile(r"^[1-9][0-9]*$")
IDENTIFIER = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._:-]{0,159}$")
GO_VERSION = re.compile(r"^go[0-9]+\.[0-9]+\.[0-9]+$")
BUILD_DATE = re.compile(r"^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$")


def fail(message: str) -> NoReturn:
    raise SystemExit(f"[rc1-carrier-check] ERROR: {message}")


def exact_object(value: object, label: str, fields: tuple[str, ...]) -> dict[str, object]:
    if not isinstance(value, dict):
        fail(f"{label} must be an object")
    expected = set(fields)
    actual = set(value)
    if actual != expected:
        missing = sorted(expected - actual)
        unknown = sorted(actual - expected)
        fail(f"{label} fields are not closed (missing={missing}, unknown={unknown})")
    return value


def string(value: object, label: str, pattern: re.Pattern[str] | None = None) -> str:
    if not isinstance(value, str) or not value:
        fail(f"{label} must be a non-empty string")
    if pattern is not None and pattern.fullmatch(value) is None:
        fail(f"{label} has a non-canonical value")
    return value


def exact_string(value: object, label: str, expected: str) -> str:
    actual = string(value, label)
    if actual != expected:
        fail(f"{label} mismatch")
    return actual


def positive_integer(value: object, label: str, maximum: int = (1 << 63) - 1) -> int:
    if type(value) is not int or value < 1 or value > maximum:
        fail(f"{label} must be a bounded positive integer")
    return value


def true_value(value: object, label: str) -> None:
    if value is not True:
        fail(f"{label} must be true")


def digest(value: object, label: str) -> str:
    return string(value, label, SHA256)


def identifier(value: object, label: str) -> str:
    return string(value, label, IDENTIFIER)


def parse_positive_decimal(value: str, label: str) -> int:
    if POSITIVE_DECIMAL.fullmatch(value) is None:
        fail(f"{label} must be a canonical positive decimal")
    number = int(value)
    if number > (1 << 63) - 1:
        fail(f"{label} exceeds the supported range")
    return number


def file_identity(metadata: os.stat_result) -> tuple[int, int, int, int, int, int, int]:
    return (
        metadata.st_dev,
        metadata.st_ino,
        metadata.st_mode,
        metadata.st_nlink,
        metadata.st_size,
        metadata.st_mtime_ns,
        metadata.st_ctime_ns,
    )


def open_carrier(
    path: str,
) -> tuple[
    list[tuple[int, os.stat_result]],
    list[tuple[int, str, os.stat_result]],
]:
    if not path or not os.path.isabs(path) or os.path.normpath(path) != path:
        fail("carrier directory must be one normalized absolute path")
    if os.path.realpath(path) != path:
        fail("carrier directory or a parent resolves through a symlink")
    flags = os.O_RDONLY | getattr(os, "O_CLOEXEC", 0) | getattr(os, "O_DIRECTORY", 0)
    flags |= getattr(os, "O_NOFOLLOW", 0)
    held_directories: list[tuple[int, os.stat_result]] = []
    path_bindings: list[tuple[int, str, os.stat_result]] = []
    try:
        root_descriptor = os.open("/", flags)
    except OSError as error:
        fail(f"cannot open filesystem root: {error}")
    held_directories.append((root_descriptor, os.fstat(root_descriptor)))
    parent_descriptor = root_descriptor
    try:
        components = path.removeprefix("/").split("/")
        if not components or any(not component for component in components):
            fail("carrier directory cannot be the filesystem root")
        for component in components:
            try:
                before = os.stat(component, dir_fd=parent_descriptor, follow_symlinks=False)
            except OSError as error:
                fail(f"cannot stat carrier path component {component}: {error}")
            if stat.S_ISLNK(before.st_mode) or not stat.S_ISDIR(before.st_mode):
                fail(f"carrier path component must be a real directory: {component}")
            try:
                descriptor = os.open(component, flags, dir_fd=parent_descriptor)
            except OSError as error:
                fail(f"cannot hold carrier path component {component}: {error}")
            held = os.fstat(descriptor)
            if file_identity(before) != file_identity(held):
                os.close(descriptor)
                fail(f"carrier path component changed while opening: {component}")
            path_bindings.append((parent_descriptor, component, held))
            held_directories.append((descriptor, held))
            parent_descriptor = descriptor
    except BaseException:
        for descriptor, _ in reversed(held_directories):
            os.close(descriptor)
        raise
    return held_directories, path_bindings


def open_member(
    directory_fd: int,
    name: str,
    maximum: int,
    expected_mode: int,
) -> tuple[int, os.stat_result]:
    try:
        before = os.stat(name, dir_fd=directory_fd, follow_symlinks=False)
    except OSError as error:
        fail(f"cannot stat carrier member {name}: {error}")
    if not stat.S_ISREG(before.st_mode) or stat.S_ISLNK(before.st_mode):
        fail(f"carrier member must be a regular non-symlink file: {name}")
    if before.st_nlink != 1:
        fail(f"carrier member must have exactly one hard link: {name}")
    if stat.S_IMODE(before.st_mode) != expected_mode:
        fail(f"carrier member mode mismatch: {name}")
    if before.st_size <= 0 or before.st_size > maximum:
        fail(f"carrier member size is empty or excessive: {name}")
    flags = os.O_RDONLY | getattr(os, "O_CLOEXEC", 0) | getattr(os, "O_NOFOLLOW", 0)
    try:
        descriptor = os.open(name, flags, dir_fd=directory_fd)
    except OSError as error:
        fail(f"cannot open carrier member {name}: {error}")
    held = os.fstat(descriptor)
    if file_identity(before) != file_identity(held):
        os.close(descriptor)
        fail(f"carrier member changed while opening: {name}")
    return descriptor, held


def read_held_member(
    descriptor: int,
    held: os.stat_result,
    name: str,
    maximum: int,
) -> bytes:
    try:
        os.lseek(descriptor, 0, os.SEEK_SET)
        chunks: list[bytes] = []
        remaining = maximum + 1
        while remaining:
            chunk = os.read(descriptor, min(1 << 20, remaining))
            if not chunk:
                break
            chunks.append(chunk)
            remaining -= len(chunk)
        raw = b"".join(chunks)
        if file_identity(held) != file_identity(os.fstat(descriptor)):
            fail(f"carrier member changed while reading: {name}")
        if len(raw) != held.st_size or len(raw) > maximum:
            fail(f"carrier member length is not stable and bounded: {name}")
        return raw
    except OSError as error:
        fail(f"cannot read held carrier member {name}: {error}")


def recheck_carrier(
    held_directories: list[tuple[int, os.stat_result]],
    path_bindings: list[tuple[int, str, os.stat_result]],
    members: dict[str, tuple[int, os.stat_result, int]],
    contents: dict[str, bytes],
) -> None:
    for descriptor, held in held_directories:
        if file_identity(held) != file_identity(os.fstat(descriptor)):
            fail("held carrier ancestry changed while validating")
    for parent_descriptor, component, held in path_bindings:
        try:
            current = os.stat(component, dir_fd=parent_descriptor, follow_symlinks=False)
        except OSError as error:
            fail(f"cannot recheck carrier path component {component}: {error}")
        if file_identity(current) != file_identity(held):
            fail(f"carrier path component identity drift: {component}")

    carrier_fd = held_directories[-1][0]
    if set(os.listdir(carrier_fd)) != EXPECTED_MEMBERS:
        fail("carrier directory members changed while validating")
    for name, (descriptor, held, maximum) in members.items():
        try:
            current = os.stat(name, dir_fd=carrier_fd, follow_symlinks=False)
        except OSError as error:
            fail(f"cannot recheck carrier member {name}: {error}")
        if file_identity(current) != file_identity(held):
            fail(f"carrier member path identity drift: {name}")
        if file_identity(os.fstat(descriptor)) != file_identity(held):
            fail(f"held carrier member identity drift: {name}")
        if read_held_member(descriptor, held, name, maximum) != contents[name]:
            fail(f"held carrier member bytes drift: {name}")


def decode_text(raw: bytes, label: str) -> str:
    if raw.startswith(b"\xef\xbb\xbf") or b"\x00" in raw or b"\r" in raw:
        fail(f"{label} contains forbidden BOM, NUL, or CR bytes")
    try:
        return raw.decode("utf-8")
    except UnicodeDecodeError as error:
        fail(f"{label} is not UTF-8: {error}")


def reject_duplicate_members(pairs: list[tuple[str, object]]) -> dict[str, object]:
    result: dict[str, object] = {}
    for key, value in pairs:
        if key in result:
            fail(f"duplicate JSON member: {key}")
        result[key] = value
    return result


def parse_receipt(raw: bytes) -> dict[str, object]:
    text = decode_text(raw, RECEIPT_NAME)
    try:
        value = json.loads(text, object_pairs_hook=reject_duplicate_members)
    except json.JSONDecodeError as error:
        fail(f"receipt is not one exact JSON object: {error}")
    if not isinstance(value, dict):
        fail("receipt root must be an object")
    return value


def raw_sha(raw: bytes) -> str:
    return hashlib.sha256(raw).hexdigest()


def sha(raw: bytes) -> str:
    return "sha256:" + raw_sha(raw)


def validate_build_date(value: object, label: str) -> str:
    text = string(value, label, BUILD_DATE)
    try:
        parsed = dt.datetime.strptime(text, "%Y-%m-%dT%H:%M:%SZ")
    except ValueError:
        fail(f"{label} is not a real UTC second")
    if parsed.strftime("%Y-%m-%dT%H:%M:%SZ") != text:
        fail(f"{label} is not canonical")
    return text


def parse_manifest(raw: bytes, source_head: str) -> dict[str, object]:
    lines = decode_text(raw, MANIFEST_NAME).splitlines()
    if len(lines) != 8 or not decode_text(raw, MANIFEST_NAME).endswith("\n"):
        fail("release manifest must be the exact eight-line RC1 form")
    expected_prefixes = (
        "schemaVersion ", "repository ", "tag ", "sourceHead ",
        "buildDate ", "goVersion ", "buildFlags ", "asset ",
    )
    for line, prefix in zip(lines, expected_prefixes):
        if not line.startswith(prefix) or line.count(" ") < 1:
            fail("release manifest field order or encoding mismatch")
    exact_string(lines[0][len(expected_prefixes[0]):], "manifest schemaVersion", "marshal.rc1-release-manifest.v1")
    exact_string(lines[1][len(expected_prefixes[1]):], "manifest repository", "https://github.com/chiga0/marshal-harness.git")
    exact_string(lines[2][len(expected_prefixes[2]):], "manifest tag", TAG)
    exact_string(lines[3][len(expected_prefixes[3]):], "manifest sourceHead", source_head)
    build_date = validate_build_date(lines[4][len(expected_prefixes[4]):], "manifest buildDate")
    go_version = string(lines[5][len(expected_prefixes[5]):], "manifest goVersion", GO_VERSION)
    exact_string(
        lines[6][len(expected_prefixes[6]):],
        "manifest buildFlags",
        "-trimpath,-buildvcs=false,-mod=readonly,-buildid=",
    )
    asset = lines[7].split(" ")
    if len(asset) != 7 or asset[0] != "asset":
        fail("manifest asset must have seven canonical fields")
    if RAW_SHA256.fullmatch(asset[1]) is None or POSITIVE_DECIMAL.fullmatch(asset[2]) is None:
        fail("manifest asset digest or size is non-canonical")
    if asset[3:] != [BINARY_NAME, "darwin", "arm64", "darwin-local-dogfood"]:
        fail("manifest contains an unsupported asset or profile")
    return {
        "buildDate": build_date,
        "goVersion": go_version,
        "binarySha256": "sha256:" + asset[1],
        "binarySize": int(asset[2]),
    }


def payload_identity(contents: dict[str, bytes]) -> tuple[str, int]:
    hasher = hashlib.sha256()
    hasher.update(b"marshal.rc1-carrier-payload.v1\n")
    total = 0
    for name in PAYLOAD_MEMBERS:
        raw = contents[name]
        total += len(raw)
        hasher.update(f"{name} {len(raw)} {raw_sha(raw)}\n".encode("ascii"))
    return "sha256:" + hasher.hexdigest(), total


def canonical_receipt_digest(receipt: dict[str, object]) -> str:
    detached = copy.deepcopy(receipt)
    detached["receiptDigest"] = ""
    canonical = json.dumps(
        detached,
        ensure_ascii=False,
        sort_keys=True,
        separators=(",", ":"),
        allow_nan=False,
    ).encode("utf-8")
    return sha(canonical)


def validate_receipt(
    receipt: dict[str, object],
    contents: dict[str, bytes],
    expected_source_head: str,
    expected_workflow_run_id: int,
    expected_artifact_id: int,
    expected_artifact_digest: str,
    expected_authority_head: str,
    expected_receipt_digest: str,
) -> None:
    receipt = exact_object(receipt, "receipt", (
        "schemaVersion", "tag", "sourceHead", "candidateWorkflow", "payload",
        "manifest", "checksums", "binary", "activation", "canary", "authority",
        "receiptDigest",
    ))
    exact_string(receipt["schemaVersion"], "schemaVersion", "marshal.rc1-canary-receipt.v1")
    exact_string(receipt["tag"], "tag", TAG)
    exact_string(receipt["sourceHead"], "sourceHead", expected_source_head)

    workflow = exact_object(receipt["candidateWorkflow"], "candidateWorkflow", (
        "runId", "artifactId", "artifactDigest",
    ))
    if positive_integer(workflow["runId"], "candidateWorkflow.runId") != expected_workflow_run_id:
        fail("candidate workflow run id drift")
    if positive_integer(workflow["artifactId"], "candidateWorkflow.artifactId") != expected_artifact_id:
        fail("candidate artifact id drift")
    exact_string(
        workflow["artifactDigest"],
        "candidateWorkflow.artifactDigest",
        "sha256:" + expected_artifact_digest,
    )

    payload = exact_object(receipt["payload"], "payload", ("schemaVersion", "sha256", "size"))
    exact_string(payload["schemaVersion"], "payload.schemaVersion", "marshal.rc1-carrier-payload.v1")
    payload_digest, payload_size = payload_identity(contents)
    exact_string(payload["sha256"], "payload.sha256", payload_digest)
    if positive_integer(payload["size"], "payload.size", MAX_BINARY_BYTES + 2 * MAX_TEXT_BYTES) != payload_size:
        fail("payload size mismatch")

    def validate_file_binding(field: str, name: str) -> None:
        binding = exact_object(receipt[field], field, ("path", "sha256", "size"))
        exact_string(binding["path"], f"{field}.path", name)
        exact_string(binding["sha256"], f"{field}.sha256", sha(contents[name]))
        if positive_integer(binding["size"], f"{field}.size") != len(contents[name]):
            fail(f"{field} size mismatch")

    validate_file_binding("manifest", MANIFEST_NAME)
    validate_file_binding("checksums", SUMS_NAME)

    binary = exact_object(receipt["binary"], "binary", (
        "path", "sha256", "size", "version", "buildDate", "goVersion", "os", "arch", "profile",
    ))
    exact_string(binary["path"], "binary.path", BINARY_NAME)
    binary_digest = sha(contents[BINARY_NAME])
    exact_string(binary["sha256"], "binary.sha256", binary_digest)
    binary_size = positive_integer(binary["size"], "binary.size", MAX_BINARY_BYTES)
    if binary_size != len(contents[BINARY_NAME]):
        fail("binary size mismatch")
    exact_string(binary["version"], "binary.version", VERSION)
    build_date = validate_build_date(binary["buildDate"], "binary.buildDate")
    go_version = string(binary["goVersion"], "binary.goVersion", GO_VERSION)
    exact_string(binary["os"], "binary.os", "darwin")
    exact_string(binary["arch"], "binary.arch", "arm64")
    exact_string(binary["profile"], "binary.profile", "darwin-local-dogfood")

    manifest = parse_manifest(contents[MANIFEST_NAME], expected_source_head)
    if (
        manifest["binarySha256"] != binary_digest
        or manifest["binarySize"] != binary_size
        or manifest["buildDate"] != build_date
        or manifest["goVersion"] != go_version
    ):
        fail("manifest and binary receipt identity disagree")

    expected_sums = (
        f"{raw_sha(contents[MANIFEST_NAME])}  {MANIFEST_NAME}\n"
        f"{raw_sha(contents[BINARY_NAME])}  {BINARY_NAME}\n"
    ).encode("ascii")
    if contents[SUMS_NAME] != expected_sums:
        fail("SHA256SUMS must bind exactly the RC1 manifest and Darwin arm64 binary")

    activation = exact_object(receipt["activation"], "activation", (
        "activationDigest", "identitySubjectDigest", "currentObjectObservationDigest",
        "currentCanonicalPath", "currentObjectRawSHA256", "currentObjectSize",
        "sourceHead", "profile", "localSelfIdentityBindingDigest",
    ))
    digest(activation["activationDigest"], "activation.activationDigest")
    digest(activation["identitySubjectDigest"], "activation.identitySubjectDigest")
    digest(activation["currentObjectObservationDigest"], "activation.currentObjectObservationDigest")
    local_identity_binding_digest = digest(
        activation["localSelfIdentityBindingDigest"],
        "activation.localSelfIdentityBindingDigest",
    )
    current_path = string(activation["currentCanonicalPath"], "activation.currentCanonicalPath")
    if (
        len(current_path) > 4096
        or not os.path.isabs(current_path)
        or os.path.normpath(current_path) != current_path
        or not current_path.endswith("/bin/marshal")
    ):
        fail("activation.currentCanonicalPath must be one normalized absolute bin/marshal path")
    exact_string(activation["currentObjectRawSHA256"], "activation.currentObjectRawSHA256", binary_digest)
    if positive_integer(activation["currentObjectSize"], "activation.currentObjectSize") != binary_size:
        fail("activation current object size mismatch")
    exact_string(activation["sourceHead"], "activation.sourceHead", expected_source_head)
    exact_string(activation["profile"], "activation.profile", "darwin-local-dogfood")

    canary = exact_object(receipt["canary"], "canary", (
        "taskId", "runId", "attemptId", "specDigest", "baseSha",
        "artifactManifestDigest", "workerResultDigests", "localSelfIdentityBindingDigest",
        "agentProvider", "agentVersion", "invocation", "workerActorId", "reviewPacket",
        "verification", "evidence", "reviewDecision", "outcome", "publication",
    ))
    identifier(canary["taskId"], "canary.taskId")
    run_id = identifier(canary["runId"], "canary.runId")
    attempt_id = identifier(canary["attemptId"], "canary.attemptId")
    spec_digest = digest(canary["specDigest"], "canary.specDigest")
    base_sha = string(canary["baseSha"], "canary.baseSha", SOURCE_HEAD)
    artifact_manifest_digest = digest(
        canary["artifactManifestDigest"],
        "canary.artifactManifestDigest",
    )
    worker_result_digests = canary["workerResultDigests"]
    if not isinstance(worker_result_digests, list) or len(worker_result_digests) != 1:
        fail("canary.workerResultDigests must contain exactly the current Attempt result")
    worker_result_digest = digest(worker_result_digests[0], "canary.workerResultDigests[0]")
    exact_string(
        canary["localSelfIdentityBindingDigest"],
        "canary.localSelfIdentityBindingDigest",
        local_identity_binding_digest,
    )
    exact_string(canary["agentProvider"], "canary.agentProvider", "pi")
    agent_version = string(canary["agentVersion"], "canary.agentVersion")
    if len(agent_version) > 128:
        fail("canary.agentVersion exceeds 128 characters")
    exact_string(canary["invocation"], "canary.invocation", "real")
    worker_actor = identifier(canary["workerActorId"], "canary.workerActorId")
    exact_string(canary["publication"], "canary.publication", "none")

    evidence = exact_object(canary["evidence"], "canary.evidence", (
        "digest", "runId", "attemptId", "specDigest", "baseSha",
        "artifactManifestDigest", "workerResultDigests", "localSelfIdentityBindingDigest",
    ))
    evidence_digest = digest(evidence["digest"], "canary.evidence.digest")
    exact_string(evidence["runId"], "canary.evidence.runId", run_id)
    exact_string(evidence["attemptId"], "canary.evidence.attemptId", attempt_id)
    exact_string(evidence["specDigest"], "canary.evidence.specDigest", spec_digest)
    exact_string(evidence["baseSha"], "canary.evidence.baseSha", base_sha)
    exact_string(
        evidence["artifactManifestDigest"],
        "canary.evidence.artifactManifestDigest",
        artifact_manifest_digest,
    )
    if evidence["workerResultDigests"] != [worker_result_digest]:
        fail("Evidence worker result set drift")
    exact_string(
        evidence["localSelfIdentityBindingDigest"],
        "canary.evidence.localSelfIdentityBindingDigest",
        local_identity_binding_digest,
    )

    verification = exact_object(canary["verification"], "canary.verification", (
        "digest", "runId", "attemptId", "specDigest", "artifactManifestDigest",
        "workerResultDigests", "evidenceDigest", "localSelfIdentityBindingDigest",
        "verifier", "status", "independent",
    ))
    verification_digest = digest(verification["digest"], "canary.verification.digest")
    exact_string(verification["runId"], "canary.verification.runId", run_id)
    exact_string(verification["attemptId"], "canary.verification.attemptId", attempt_id)
    exact_string(verification["specDigest"], "canary.verification.specDigest", spec_digest)
    exact_string(
        verification["artifactManifestDigest"],
        "canary.verification.artifactManifestDigest",
        artifact_manifest_digest,
    )
    if verification["workerResultDigests"] != [worker_result_digest]:
        fail("Verification worker result set drift")
    exact_string(verification["evidenceDigest"], "canary.verification.evidenceDigest", evidence_digest)
    exact_string(
        verification["localSelfIdentityBindingDigest"],
        "canary.verification.localSelfIdentityBindingDigest",
        local_identity_binding_digest,
    )
    verifier = exact_object(verification["verifier"], "canary.verification.verifier", ("type", "id"))
    exact_string(verifier["type"], "canary.verification.verifier.type", "deterministic-verifier")
    verifier_actor = identifier(verifier["id"], "canary.verification.verifier.id")
    exact_string(verification["status"], "canary.verification.status", "pass")
    true_value(verification["independent"], "canary.verification.independent")

    packet = exact_object(canary["reviewPacket"], "canary.reviewPacket", (
        "digest", "runId", "attemptId", "reviewRound", "specDigest", "baseSha",
        "verificationDigest", "artifactManifestDigest", "workerResultDigests",
        "evidenceDigest", "localSelfIdentityBindingDigest",
    ))
    packet_digest = digest(packet["digest"], "canary.reviewPacket.digest")
    exact_string(packet["runId"], "canary.reviewPacket.runId", run_id)
    exact_string(packet["attemptId"], "canary.reviewPacket.attemptId", attempt_id)
    review_round = positive_integer(packet["reviewRound"], "canary.reviewPacket.reviewRound")
    exact_string(packet["specDigest"], "canary.reviewPacket.specDigest", spec_digest)
    exact_string(packet["baseSha"], "canary.reviewPacket.baseSha", base_sha)
    exact_string(packet["verificationDigest"], "canary.reviewPacket.verificationDigest", verification_digest)
    exact_string(
        packet["artifactManifestDigest"],
        "canary.reviewPacket.artifactManifestDigest",
        artifact_manifest_digest,
    )
    if packet["workerResultDigests"] != [worker_result_digest]:
        fail("ReviewPacket worker result set drift")
    exact_string(packet["evidenceDigest"], "canary.reviewPacket.evidenceDigest", evidence_digest)
    exact_string(
        packet["localSelfIdentityBindingDigest"],
        "canary.reviewPacket.localSelfIdentityBindingDigest",
        local_identity_binding_digest,
    )

    decision = exact_object(canary["reviewDecision"], "canary.reviewDecision", (
        "digest", "runId", "reviewRound", "reviewer", "independent", "specDigest",
        "reviewPacketDigest", "verificationDigest", "artifactManifestDigest",
        "evidenceDigest", "localSelfIdentityBindingDigest", "verdict",
        "blockingFindingCount", "publicationRecommendation",
    ))
    decision_digest = digest(decision["digest"], "canary.reviewDecision.digest")
    exact_string(decision["runId"], "canary.reviewDecision.runId", run_id)
    if positive_integer(decision["reviewRound"], "canary.reviewDecision.reviewRound") != review_round:
        fail("review round mismatch")
    reviewer = exact_object(decision["reviewer"], "canary.reviewDecision.reviewer", ("type", "id"))
    reviewer_type = string(reviewer["type"], "canary.reviewDecision.reviewer.type")
    if reviewer_type not in ("lead-agent", "human"):
        fail("canary.reviewDecision.reviewer.type is not an authority reviewer type")
    reviewer_actor = identifier(reviewer["id"], "canary.reviewDecision.reviewer.id")
    true_value(decision["independent"], "canary.reviewDecision.independent")
    exact_string(decision["specDigest"], "canary.reviewDecision.specDigest", spec_digest)
    exact_string(decision["reviewPacketDigest"], "canary.reviewDecision.reviewPacketDigest", packet_digest)
    exact_string(decision["verificationDigest"], "canary.reviewDecision.verificationDigest", verification_digest)
    exact_string(
        decision["artifactManifestDigest"],
        "canary.reviewDecision.artifactManifestDigest",
        artifact_manifest_digest,
    )
    exact_string(decision["evidenceDigest"], "canary.reviewDecision.evidenceDigest", evidence_digest)
    exact_string(
        decision["localSelfIdentityBindingDigest"],
        "canary.reviewDecision.localSelfIdentityBindingDigest",
        local_identity_binding_digest,
    )
    exact_string(decision["verdict"], "canary.reviewDecision.verdict", "accept")
    if type(decision["blockingFindingCount"]) is not int or decision["blockingFindingCount"] != 0:
        fail("accepted review decision must have zero blocking findings")
    exact_string(decision["publicationRecommendation"], "canary.reviewDecision.publicationRecommendation", "not-applicable")

    outcome = exact_object(canary["outcome"], "canary.outcome", (
        "digest", "runId", "terminalState", "verdict", "finalReviewDigest",
        "finalEvidenceDigest", "publication",
    ))
    digest(outcome["digest"], "canary.outcome.digest")
    exact_string(outcome["runId"], "canary.outcome.runId", run_id)
    exact_string(outcome["terminalState"], "canary.outcome.terminalState", "ACCEPTED")
    exact_string(outcome["verdict"], "canary.outcome.verdict", "accept")
    exact_string(outcome["finalReviewDigest"], "canary.outcome.finalReviewDigest", decision_digest)
    exact_string(outcome["finalEvidenceDigest"], "canary.outcome.finalEvidenceDigest", evidence_digest)
    exact_string(outcome["publication"], "canary.outcome.publication", "none")

    if len({worker_actor, verifier_actor, reviewer_actor}) != 3:
        fail("worker, verifier, and reviewer actors must be pairwise independent")

    authority = exact_object(receipt["authority"], "authority", (
        "currentHeadDigest", "revision", "outcomeDigest",
    ))
    exact_string(authority["currentHeadDigest"], "authority.currentHeadDigest", expected_authority_head)
    positive_integer(authority["revision"], "authority.revision")
    exact_string(authority["outcomeDigest"], "authority.outcomeDigest", outcome["digest"])

    receipt_digest = digest(receipt["receiptDigest"], "receiptDigest")
    exact_string(receipt_digest, "receiptDigest external admission", "sha256:" + expected_receipt_digest)
    if receipt_digest != canonical_receipt_digest(receipt):
        fail("receiptDigest mismatch")


def main(arguments: list[str]) -> None:
    if len(arguments) != 7:
        fail(
            "usage: rc1-carrier-check.py ABS_CARRIER_DIR EXPECTED_SOURCE_HEAD "
            "EXPECTED_WORKFLOW_RUN_ID EXPECTED_ARTIFACT_ID "
            "EXPECTED_ARTIFACT_DIGEST_RAW_SHA256 EXPECTED_AUTHORITY_HEAD_DIGEST "
            "EXPECTED_RECEIPT_DIGEST_RAW_SHA256"
        )
    (
        carrier_path,
        source_head,
        workflow_run_text,
        artifact_id_text,
        artifact_digest,
        authority_head,
        expected_receipt_digest,
    ) = arguments
    if SOURCE_HEAD.fullmatch(source_head) is None:
        fail("expected sourceHead must be a lowercase 40-character Git object id")
    workflow_run_id = parse_positive_decimal(workflow_run_text, "expected workflow run id")
    artifact_id = parse_positive_decimal(artifact_id_text, "expected artifact id")
    if RAW_SHA256.fullmatch(artifact_digest) is None:
        fail("expected artifact digest must be a raw lowercase SHA-256")
    digest(authority_head, "expected authority current head")
    if RAW_SHA256.fullmatch(expected_receipt_digest) is None:
        fail("expected receipt digest must be a raw lowercase SHA-256")

    held_directories, path_bindings = open_carrier(carrier_path)
    directory_fd = held_directories[-1][0]
    members: dict[str, tuple[int, os.stat_result, int]] = {}
    try:
        names = os.listdir(directory_fd)
        if len(names) != len(set(names)) or set(names) != EXPECTED_MEMBERS:
            fail("carrier directory members are not the exact closed RC1 set")
        member_contracts = {
            BINARY_NAME: (MAX_BINARY_BYTES, 0o755),
            MANIFEST_NAME: (MAX_TEXT_BYTES, 0o644),
            SUMS_NAME: (MAX_TEXT_BYTES, 0o644),
            RECEIPT_NAME: (MAX_TEXT_BYTES, 0o644),
        }
        for name, (maximum, mode) in member_contracts.items():
            descriptor, held = open_member(directory_fd, name, maximum, mode)
            members[name] = (descriptor, held, maximum)
        contents = {
            name: read_held_member(descriptor, held, name, maximum)
            for name, (descriptor, held, maximum) in members.items()
        }
        receipt = parse_receipt(contents[RECEIPT_NAME])
        validate_receipt(
            receipt,
            contents,
            source_head,
            workflow_run_id,
            artifact_id,
            artifact_digest,
            authority_head,
            expected_receipt_digest,
        )
        recheck_carrier(held_directories, path_bindings, members, contents)
    finally:
        for descriptor, _, _ in members.values():
            os.close(descriptor)
        for descriptor, _ in reversed(held_directories):
            os.close(descriptor)
    print("[rc1-carrier-check] PASS")


if __name__ == "__main__":
    main(sys.argv[1:])
