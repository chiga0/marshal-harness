#!/usr/bin/env python3
"""Aggregate offline compatibility checks for the Codex 0.145 provider schema.

This is an operator-local Mac ordinary-user admission aid.  It is neither a
marshal.dev contract nor a Codex/OpenAI official JSON Schema implementation.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import stat
import sys
from typing import NamedTuple


RECEIPT_VERSION = "marshal-operator-codex-provider-schema-preflight/v1"
PROFILE_VERSION = "marshal-operator-codex-provider-schema-profile/v1"
PROFILE_RELATIVE = ".agents/skills/marshal/references/codex-0.145-provider-schema-profile.json"
MAX_PROFILE_BYTES = 64 * 1024
READ_CHUNK_BYTES = 64 * 1024
EXPECTED_PROFILE = {
    "profileVersion": PROFILE_VERSION,
    "adapterId": "codex",
    "cliCompatibilityLine": "0.145.x",
    "authorityScope": "mac-ordinary-user-operator-local",
    "authorityClaim": "none",
    "maxSchemaBytes": 4 * 1024 * 1024,
    "allowedTypes": [
        "array",
        "boolean",
        "integer",
        "null",
        "number",
        "object",
        "string",
    ],
    "allowedKeywords": [
        "additionalProperties",
        "anyOf",
        "default",
        "enum",
        "items",
        "minimum",
        "properties",
        "required",
        "type",
    ],
    "unsupportedKeywords": [
        "$defs",
        "$id",
        "$ref",
        "$schema",
        "allOf",
        "const",
        "format",
        "maxLength",
        "minLength",
        "not",
        "oneOf",
        "pattern",
        "title",
        "uniqueItems",
    ],
    "objectPolicy": {
        "additionalProperties": False,
        "requiredMustEqualSortedPropertyNames": True,
    },
    "arrayPolicy": {"itemsSchemaRequired": True},
}
EXPECTED_EVIDENCE_SOURCES = [
    "internal/adapter/codex/execution.go:providerSchemaDocument",
    "mac-codex-ordinary-smoke-r1-r10-20260819",
    "mac-codex-ordinary-smoke-r16-20260819",
]


class ReadResult(NamedTuple):
    raw: bytes
    digest: str
    size: int


class PreflightError(Exception):
    def __init__(self, reason_code: str, message: str):
        super().__init__(message)
        self.reason_code = reason_code


def fail(reason_code: str, message: str) -> None:
    raise PreflightError(reason_code, message)


def sha256_digest(raw: bytes) -> str:
    return "sha256:" + hashlib.sha256(raw).hexdigest()


def canonical_digest(value: object) -> str:
    raw = json.dumps(
        value, ensure_ascii=False, sort_keys=True, separators=(",", ":")
    ).encode("utf-8")
    return sha256_digest(raw)


def clean_relative_path(raw: str, reason_code: str) -> str:
    if not isinstance(raw, str) or not raw or "\\" in raw or "\x00" in raw:
        fail(reason_code, "path must be a clean non-empty POSIX relative path")
    candidate = PurePosixPath(raw)
    if candidate.is_absolute() or raw != candidate.as_posix() or any(
        part in {"", ".", ".."} for part in candidate.parts
    ):
        fail(reason_code, "path must be a clean non-empty POSIX relative path")
    return raw


def open_root(root: Path) -> int:
    try:
        metadata = root.lstat()
    except OSError:
        fail("codex-provider-preflight-root-invalid", "root is unavailable")
    if stat.S_ISLNK(metadata.st_mode) or not stat.S_ISDIR(metadata.st_mode):
        fail("codex-provider-preflight-root-invalid", "root must be a real directory")
    flags = os.O_RDONLY | getattr(os, "O_DIRECTORY", 0) | getattr(os, "O_NOFOLLOW", 0)
    try:
        return os.open(root, flags)
    except OSError:
        fail("codex-provider-preflight-root-invalid", "root cannot be opened safely")


def read_regular_file_at(
    root_fd: int,
    relative: str,
    limit: int,
    *,
    path_reason: str,
    unreadable_reason: str,
    too_large_reason: str,
    changed_reason: str,
) -> ReadResult:
    clean_relative_path(relative, path_reason)
    directory_fds: list[int] = []
    directory_identities: list[tuple[int, int, int]] = []
    current_fd = root_fd
    components = PurePosixPath(relative).parts
    try:
        directory_flags = (
            os.O_RDONLY | getattr(os, "O_DIRECTORY", 0) | getattr(os, "O_NOFOLLOW", 0)
        )
        for component in components[:-1]:
            try:
                next_fd = os.open(component, directory_flags, dir_fd=current_fd)
            except OSError:
                fail(unreadable_reason, "path cannot be traversed with nofollow")
            directory_fds.append(next_fd)
            metadata = os.fstat(next_fd)
            directory_identities.append((metadata.st_dev, metadata.st_ino, metadata.st_mode))
            current_fd = next_fd
        try:
            file_fd = os.open(
                components[-1],
                os.O_RDONLY
                | getattr(os, "O_NOFOLLOW", 0)
                | getattr(os, "O_NONBLOCK", 0),
                dir_fd=current_fd,
            )
        except OSError:
            fail(unreadable_reason, "file cannot be opened with nofollow")
        try:
            before = os.fstat(file_fd)
            if not stat.S_ISREG(before.st_mode):
                fail(unreadable_reason, "input must be a regular file")
            if before.st_size <= 0:
                fail(unreadable_reason, "input must not be empty")
            if before.st_size > limit:
                fail(too_large_reason, "input exceeds its bounded-read limit")
            chunks: list[bytes] = []
            remaining = limit + 1
            while remaining > 0:
                chunk = os.read(file_fd, min(READ_CHUNK_BYTES, remaining))
                if not chunk:
                    break
                chunks.append(chunk)
                remaining -= len(chunk)
            raw = b"".join(chunks)
            if len(raw) > limit:
                fail(too_large_reason, "input grew beyond its bounded-read limit")
            after = os.fstat(file_fd)
            before_identity = (
                before.st_dev,
                before.st_ino,
                before.st_mode,
                before.st_size,
                before.st_mtime_ns,
                before.st_ctime_ns,
            )
            after_identity = (
                after.st_dev,
                after.st_ino,
                after.st_mode,
                after.st_size,
                after.st_mtime_ns,
                after.st_ctime_ns,
            )
            if before_identity != after_identity or len(raw) != before.st_size:
                fail(changed_reason, "input identity changed during bounded read")
            # Re-open the lexical chain from the still-held root dirfd.  Held
            # directory descriptors prevent redirection while reading; this
            # second walk additionally rejects rename/replacement of any
            # parent or the final leaf instead of silently accepting old bytes.
            recheck_fds: list[int] = []
            recheck_current = root_fd
            try:
                for index, component in enumerate(components[:-1]):
                    try:
                        descriptor = os.open(component, directory_flags, dir_fd=recheck_current)
                    except OSError:
                        fail(changed_reason, "input parent changed during bounded read")
                    recheck_fds.append(descriptor)
                    metadata = os.fstat(descriptor)
                    if (metadata.st_dev, metadata.st_ino, metadata.st_mode) != directory_identities[index]:
                        fail(changed_reason, "input parent identity changed during bounded read")
                    recheck_current = descriptor
                try:
                    rebound_fd = os.open(
                        components[-1],
                        os.O_RDONLY
                        | getattr(os, "O_NOFOLLOW", 0)
                        | getattr(os, "O_NONBLOCK", 0),
                        dir_fd=recheck_current,
                    )
                except OSError:
                    fail(changed_reason, "input leaf changed during bounded read")
                try:
                    rebound = os.fstat(rebound_fd)
                    if (rebound.st_dev, rebound.st_ino, rebound.st_mode) != (
                        before.st_dev,
                        before.st_ino,
                        before.st_mode,
                    ):
                        fail(changed_reason, "input leaf identity changed during bounded read")
                finally:
                    os.close(rebound_fd)
            finally:
                for descriptor in reversed(recheck_fds):
                    os.close(descriptor)
            return ReadResult(raw=raw, digest=sha256_digest(raw), size=len(raw))
        finally:
            os.close(file_fd)
    finally:
        for descriptor in reversed(directory_fds):
            os.close(descriptor)


def decode_json_object(raw: bytes, label: str) -> dict:
    def reject_duplicates(pairs: list[tuple[str, object]]) -> dict:
        result: dict = {}
        for key, value in pairs:
            if key in result:
                fail(
                    f"codex-provider-{label}-duplicate-key",
                    f"{label} contains a duplicate JSON key",
                )
            result[key] = value
        return result

    try:
        value = json.loads(raw.decode("utf-8"), object_pairs_hook=reject_duplicates)
    except PreflightError:
        raise
    except (UnicodeError, json.JSONDecodeError):
        fail(f"codex-provider-{label}-json-invalid", f"{label} is not valid UTF-8 JSON")
    if not isinstance(value, dict):
        fail(f"codex-provider-{label}-json-invalid", f"{label} must be a JSON object")
    return value


def validate_profile(profile: dict) -> dict:
    expected_keys = set(EXPECTED_PROFILE) | {"evidence"}
    if set(profile) != expected_keys:
        fail("codex-provider-profile-invalid", "profile keys differ from the frozen set")
    for key, expected in EXPECTED_PROFILE.items():
        if profile.get(key) != expected:
            fail("codex-provider-profile-invalid", "profile rules differ from the frozen set")
    evidence = profile.get("evidence")
    if not isinstance(evidence, list) or len(evidence) != len(EXPECTED_EVIDENCE_SOURCES):
        fail("codex-provider-profile-invalid", "profile evidence is incomplete")
    sources: list[str] = []
    for item in evidence:
        if (
            not isinstance(item, dict)
            or set(item) != {"source", "observation"}
            or not isinstance(item.get("source"), str)
            or not isinstance(item.get("observation"), str)
            or not item["observation"].strip()
        ):
            fail("codex-provider-profile-invalid", "profile evidence shape is invalid")
        sources.append(item["source"])
    if sources != EXPECTED_EVIDENCE_SOURCES:
        fail("codex-provider-profile-invalid", "profile evidence sources differ")
    return {key: profile[key] for key in EXPECTED_PROFILE}


def pointer_child(pointer: str, token: object) -> str:
    escaped = str(token).replace("~", "~0").replace("/", "~1")
    return pointer + "/" + escaped


def json_value_key(value: object) -> str:
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":"))


def value_matches_type(value: object, schema_type: str) -> bool:
    if schema_type == "null":
        return value is None
    if schema_type == "boolean":
        return isinstance(value, bool)
    if schema_type == "integer":
        return isinstance(value, int) and not isinstance(value, bool)
    if schema_type == "number":
        return isinstance(value, (int, float)) and not isinstance(value, bool)
    if schema_type == "string":
        return isinstance(value, str)
    if schema_type == "array":
        return isinstance(value, list)
    if schema_type == "object":
        return isinstance(value, dict)
    return False


def collect_issues(schema: dict, profile: dict) -> list[dict[str, str]]:
    allowed_types = set(profile["allowedTypes"])
    allowed_keywords = set(profile["allowedKeywords"])
    unsupported_keywords = set(profile["unsupportedKeywords"])
    issues: list[dict[str, str]] = []
    seen: set[tuple[str, str, str]] = set()

    def add(code: str, pointer: str, keyword: str) -> None:
        identity = (pointer, code, keyword)
        if identity not in seen:
            seen.add(identity)
            issues.append({"code": code, "jsonPointer": pointer, "keyword": keyword})

    def walk(node: object, pointer: str) -> None:
        if not isinstance(node, dict):
            add("keyword-value-invalid", pointer, "schema")
            return
        schema_type = node.get("type")
        if "type" not in node:
            add("missing-type", pointer, "type")
        elif not isinstance(schema_type, str) or schema_type not in allowed_types:
            add("type-invalid", pointer_child(pointer, "type"), "type")

        for keyword in sorted(node):
            if keyword in unsupported_keywords:
                add("unsupported-keyword", pointer_child(pointer, keyword), keyword)
            elif keyword not in allowed_keywords:
                add("unknown-keyword", pointer_child(pointer, keyword), keyword)

        properties = node.get("properties")
        if "properties" in node and not isinstance(properties, dict):
            add("object-properties-invalid", pointer_child(pointer, "properties"), "properties")
        if schema_type == "object":
            if not isinstance(properties, dict):
                add("object-properties-invalid", pointer_child(pointer, "properties"), "properties")
            if node.get("additionalProperties") is not False:
                add(
                    "additional-properties-not-false",
                    pointer_child(pointer, "additionalProperties"),
                    "additionalProperties",
                )
            expected_required = sorted(properties) if isinstance(properties, dict) else []
            required = node.get("required")
            if required != expected_required:
                add(
                    "required-properties-mismatch",
                    pointer_child(pointer, "required"),
                    "required",
                )
        if isinstance(properties, dict):
            for name in sorted(properties):
                walk(properties[name], pointer_child(pointer_child(pointer, "properties"), name))

        items = node.get("items")
        if schema_type == "array" and not isinstance(items, dict):
            add("array-items-missing", pointer_child(pointer, "items"), "items")
        if "items" in node:
            if isinstance(items, dict):
                walk(items, pointer_child(pointer, "items"))
            elif schema_type != "array":
                add("keyword-value-invalid", pointer_child(pointer, "items"), "items")

        alternatives = node.get("anyOf")
        if "anyOf" in node:
            if not isinstance(alternatives, list) or not alternatives:
                add("anyof-shape-invalid", pointer_child(pointer, "anyOf"), "anyOf")
            else:
                for index, alternative in enumerate(alternatives):
                    walk(alternative, pointer_child(pointer_child(pointer, "anyOf"), index))

        enum = node.get("enum")
        if "enum" in node:
            if not isinstance(enum, list) or not enum:
                add("enum-shape-invalid", pointer_child(pointer, "enum"), "enum")
            else:
                encoded = [json_value_key(value) for value in enum]
                if len(encoded) != len(set(encoded)) or (
                    isinstance(schema_type, str)
                    and schema_type in allowed_types
                    and any(not value_matches_type(value, schema_type) for value in enum)
                ):
                    add("enum-shape-invalid", pointer_child(pointer, "enum"), "enum")

        minimum = node.get("minimum")
        if "minimum" in node and (
            not isinstance(minimum, (int, float))
            or isinstance(minimum, bool)
            or schema_type not in {"integer", "number"}
        ):
            add("keyword-value-invalid", pointer_child(pointer, "minimum"), "minimum")
        if "default" in node and isinstance(schema_type, str) and schema_type in allowed_types:
            if not value_matches_type(node["default"], schema_type):
                add("keyword-value-invalid", pointer_child(pointer, "default"), "default")

        # Recurse through rejected schema containers as well.  This is how one
        # invocation reports both the rejected outer keyword and all nested
        # missing-type defects that Codex previously revealed one Run at a time.
        nested_object = node.get("not")
        if isinstance(nested_object, dict):
            walk(nested_object, pointer_child(pointer, "not"))
        definitions = node.get("$defs")
        if isinstance(definitions, dict):
            for name in sorted(definitions):
                walk(definitions[name], pointer_child(pointer_child(pointer, "$defs"), name))
        for keyword in ("allOf", "oneOf"):
            nested = node.get(keyword)
            if isinstance(nested, list):
                for index, child in enumerate(nested):
                    walk(child, pointer_child(pointer_child(pointer, keyword), index))

    walk(schema, "")
    return sorted(issues, key=lambda issue: (issue["jsonPointer"], issue["code"], issue["keyword"]))


def build_receipt(
    schema_path: str,
    schema_read: ReadResult,
    profile: dict,
    profile_read: ReadResult,
    rules: dict,
    issues: list[dict[str, str]],
) -> dict:
    passed = not issues
    return {
        "receiptVersion": RECEIPT_VERSION,
        "status": "pass" if passed else "fail",
        "reasonCode": (
            "codex-provider-schema-compatible"
            if passed
            else "codex-provider-schema-incompatible"
        ),
        "adapterId": profile["adapterId"],
        "cliCompatibilityLine": profile["cliCompatibilityLine"],
        "authorityScope": profile["authorityScope"],
        "authorityClaim": profile["authorityClaim"],
        "profileVersion": profile["profileVersion"],
        "profileDigest": profile_read.digest,
        "rulesDigest": canonical_digest(rules),
        "schema": {
            "path": schema_path,
            "rawDigest": schema_read.digest,
            "rawBytes": schema_read.size,
            "nofollow": True,
            "boundedRead": True,
        },
        "issueCount": len(issues),
        "issues": issues,
    }


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", required=True, help="trusted real directory root")
    parser.add_argument("--schema", required=True, help="clean relative provider schema path")
    parser.add_argument(
        "--profile", default=PROFILE_RELATIVE, help="clean relative frozen profile path"
    )
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    root = Path(args.root)
    root_fd: int | None = None
    try:
        root_fd = open_root(root)
        profile_read = read_regular_file_at(
            root_fd,
            args.profile,
            MAX_PROFILE_BYTES,
            path_reason="codex-provider-profile-path-invalid",
            unreadable_reason="codex-provider-profile-unreadable",
            too_large_reason="codex-provider-profile-too-large",
            changed_reason="codex-provider-profile-identity-changed",
        )
        profile = decode_json_object(profile_read.raw, "profile")
        rules = validate_profile(profile)
        schema_read = read_regular_file_at(
            root_fd,
            args.schema,
            profile["maxSchemaBytes"],
            path_reason="codex-provider-schema-path-invalid",
            unreadable_reason="codex-provider-schema-unreadable",
            too_large_reason="codex-provider-schema-too-large",
            changed_reason="codex-provider-schema-identity-changed",
        )
        schema = decode_json_object(schema_read.raw, "schema")
        issues = collect_issues(schema, profile)
        receipt = build_receipt(
            args.schema, schema_read, profile, profile_read, rules, issues
        )
        stream = sys.stdout if not issues else sys.stderr
        print(json.dumps(receipt, ensure_ascii=False, sort_keys=True), file=stream)
        return 0 if not issues else 1
    except PreflightError as error:
        print(
            json.dumps(
                {
                    "status": "fail",
                    "reasonCode": error.reason_code,
                    "message": str(error),
                },
                ensure_ascii=False,
                sort_keys=True,
            ),
            file=sys.stderr,
        )
        return 2
    finally:
        if root_fd is not None:
            os.close(root_fd)


if __name__ == "__main__":
    raise SystemExit(main())
