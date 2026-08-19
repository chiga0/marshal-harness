#!/usr/bin/env python3
"""Fail-closed operator-local Qoder v5 transcript attestation preflight."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import stat
import sys
import unicodedata


DIGEST_RE = re.compile(r"^sha256:[0-9a-f]{64}$")
TEE_FIRST_LINE = "cat <<'MARSHAL_RESULT' | tee /dev/null > /dev/null"
TEE_DELIMITER = "MARSHAL_RESULT"
SCHEMA_NAME = "transcript-attestation-preflight.schema.json"
READ_CHUNK_BYTES = 1024 * 1024
MANIFEST_MAX_BYTES = 1024 * 1024
SCHEMA_MAX_BYTES = 1024 * 1024
INPUT_HARD_LIMITS = {
    "transcript": 8 * 1024 * 1024,
    "transcriptMeta": 128 * 1024,
    "workerResult": 256 * 1024,
    "workerRequest": 512 * 1024,
    "taskSpec": 2 * 1024 * 1024,
}
TASK_TOOL_NAMES = {
    "Read": "read",
    "Edit": "edit",
    "Write": "write",
    "Grep": "grep",
    "Glob": "find",
    "Bash": "bash",
}
UNSAFE_TEXT_CATEGORIES = {"Cc", "Cf", "Zl", "Zp"}


class PreflightError(Exception):
    def __init__(self, reason_code: str, message: str):
        super().__init__(message)
        self.reason_code = reason_code


def fail(reason_code: str, message: str) -> None:
    raise PreflightError(reason_code, message)


def sha256_bytes(raw: bytes) -> str:
    return "sha256:" + hashlib.sha256(raw).hexdigest()


def clean_relative_path(value: object, label: str) -> str:
    if not isinstance(value, str) or not value:
        fail("path-boundary-invalid", f"{label} must be a non-empty relative path")
    if "\\" in value or "\x00" in value:
        fail("path-boundary-invalid", f"{label} contains a forbidden path character")
    path = PurePosixPath(value)
    if path.is_absolute() or any(part in {"", ".", ".."} for part in path.parts):
        fail("path-boundary-invalid", f"{label} must be a clean relative path")
    return path.as_posix()


def open_root_nofollow(root: Path) -> int:
    try:
        metadata = root.lstat()
    except OSError as error:
        fail("input-root-invalid", f"cannot lstat input root: {error}")
    if stat.S_ISLNK(metadata.st_mode) or not stat.S_ISDIR(metadata.st_mode):
        fail("input-root-invalid", "input root must be a non-symlink directory")
    flags = os.O_RDONLY | os.O_DIRECTORY | os.O_CLOEXEC | os.O_NOFOLLOW
    try:
        descriptor = os.open(root, flags)
    except OSError as error:
        fail("input-root-invalid", f"cannot open input root without following links: {error}")
    opened = os.fstat(descriptor)
    if (opened.st_dev, opened.st_ino) != (metadata.st_dev, metadata.st_ino):
        os.close(descriptor)
        fail("input-root-invalid", "input root changed during nofollow open")
    return descriptor


def read_relative_nofollow(root: Path, relative: object, maximum: int, label: str) -> bytes:
    path = clean_relative_path(relative, label)
    root_fd = open_root_nofollow(root)
    directory_fd = root_fd
    opened_directories: list[int] = []
    directory_identities: list[tuple[str, tuple[int, int, int]]] = []
    try:
        parts = PurePosixPath(path).parts
        for component in parts[:-1]:
            try:
                next_fd = os.open(
                    component,
                    os.O_RDONLY | os.O_DIRECTORY | os.O_CLOEXEC | os.O_NOFOLLOW,
                    dir_fd=directory_fd,
                )
            except OSError as error:
                fail("input-path-invalid", f"cannot open directory for {label}: {error}")
            opened_directories.append(next_fd)
            opened = os.fstat(next_fd)
            directory_identities.append(
                (component, (opened.st_dev, opened.st_ino, opened.st_mode))
            )
            directory_fd = next_fd
        try:
            file_fd = os.open(
                parts[-1],
                os.O_RDONLY | os.O_CLOEXEC | os.O_NOFOLLOW | os.O_NONBLOCK,
                dir_fd=directory_fd,
            )
        except OSError as error:
            fail("input-path-invalid", f"cannot open {label} without following links: {error}")
        try:
            before = os.fstat(file_fd)
            if not stat.S_ISREG(before.st_mode):
                fail("input-file-invalid", f"{label} must be a regular file")
            if before.st_size > maximum:
                fail("input-too-large", f"{label} exceeds its {maximum}-byte bound")
            chunks: list[bytes] = []
            total = 0
            while True:
                chunk = os.read(file_fd, min(READ_CHUNK_BYTES, maximum + 1 - total))
                if not chunk:
                    break
                chunks.append(chunk)
                total += len(chunk)
                if total > maximum:
                    fail("input-too-large", f"{label} grew beyond its {maximum}-byte bound")
            after = os.fstat(file_fd)
            identity_before = (
                before.st_dev,
                before.st_ino,
                before.st_mode,
                before.st_size,
                before.st_mtime_ns,
                before.st_ctime_ns,
            )
            identity_after = (
                after.st_dev,
                after.st_ino,
                after.st_mode,
                after.st_size,
                after.st_mtime_ns,
                after.st_ctime_ns,
            )
            raw = b"".join(chunks)
            if identity_before != identity_after or len(raw) != after.st_size:
                fail("input-changed-during-read", f"{label} changed during bounded read")
            # Re-walk the lexical path from the held root dirfd. The file fd
            # already prevents redirecting bytes during the read; this second
            # walk additionally fails closed when a parent or leaf was swapped
            # while those bytes were being captured.
            recheck_parent = root_fd
            recheck_directories: list[int] = []
            recheck_file = -1
            try:
                for component, expected in directory_identities:
                    try:
                        recheck_fd = os.open(
                            component,
                            os.O_RDONLY | os.O_DIRECTORY | os.O_CLOEXEC | os.O_NOFOLLOW,
                            dir_fd=recheck_parent,
                        )
                    except OSError as error:
                        fail("input-changed-during-read", f"{label} parent changed: {error}")
                    recheck_directories.append(recheck_fd)
                    current = os.fstat(recheck_fd)
                    if (current.st_dev, current.st_ino, current.st_mode) != expected:
                        fail("input-changed-during-read", f"{label} parent identity changed")
                    recheck_parent = recheck_fd
                try:
                    recheck_file = os.open(
                        parts[-1],
                        os.O_RDONLY | os.O_CLOEXEC | os.O_NOFOLLOW | os.O_NONBLOCK,
                        dir_fd=recheck_parent,
                    )
                except OSError as error:
                    fail("input-changed-during-read", f"{label} leaf changed: {error}")
                current_file = os.fstat(recheck_file)
                if (
                    current_file.st_dev,
                    current_file.st_ino,
                    current_file.st_mode,
                ) != (before.st_dev, before.st_ino, before.st_mode):
                    fail("input-changed-during-read", f"{label} leaf identity changed")
            finally:
                if recheck_file >= 0:
                    os.close(recheck_file)
                for descriptor in reversed(recheck_directories):
                    os.close(descriptor)
            return raw
        finally:
            os.close(file_fd)
    finally:
        for descriptor in reversed(opened_directories):
            os.close(descriptor)
        os.close(root_fd)


def parse_json(raw: bytes, label: str) -> dict:
    def reject_duplicates(pairs: list[tuple[str, object]]) -> dict:
        result: dict = {}
        for key, value in pairs:
            if key in result:
                fail("duplicate-json-key", f"{label} contains duplicate key {key!r}")
            result[key] = value
        return result

    try:
        value = json.loads(raw.decode("utf-8"), object_pairs_hook=reject_duplicates)
    except PreflightError:
        raise
    except (UnicodeError, json.JSONDecodeError) as error:
        fail("invalid-json", f"cannot decode {label}: {error}")
    if not isinstance(value, dict):
        fail("invalid-json", f"{label} must be a JSON object")
    return value


SUPPORTED_SCHEMA_KEYS = {
    "$schema",
    "$id",
    "$ref",
    "$defs",
    "title",
    "description",
    "type",
    "const",
    "enum",
    "additionalProperties",
    "required",
    "properties",
    "items",
    "minItems",
    "maxItems",
    "uniqueItems",
    "minLength",
    "maxLength",
    "pattern",
    "minimum",
    "maximum",
}


def validate_schema_document(schema: dict) -> None:
    if schema.get("$schema") != "https://json-schema.org/draft/2020-12/schema":
        fail("schema-document-invalid", "manifest schema must declare Draft 2020-12")
    if not isinstance(schema.get("$id"), str) or "operator-local" not in schema["$id"]:
        fail("schema-document-invalid", "manifest schema must use an operator-local id")

    def walk(node: object, location: str) -> None:
        if isinstance(node, bool):
            return
        if not isinstance(node, dict):
            fail("schema-document-invalid", f"schema node {location} must be an object or boolean")
        unknown = set(node) - SUPPORTED_SCHEMA_KEYS
        if unknown:
            fail(
                "schema-document-invalid",
                f"unsupported schema keywords at {location}: {', '.join(sorted(unknown))}",
            )
        for key in ("properties", "$defs"):
            if key in node:
                if not isinstance(node[key], dict):
                    fail("schema-document-invalid", f"{location}/{key} must be an object")
                for child_key, child in node[key].items():
                    walk(child, f"{location}/{key}/{child_key}")
        if "items" in node:
            walk(node["items"], f"{location}/items")

    walk(schema, "#")


def resolve_ref(schema: dict, reference: str) -> object:
    if not reference.startswith("#/"):
        fail("schema-document-invalid", f"only local schema refs are supported: {reference}")
    current: object = schema
    for raw_part in reference[2:].split("/"):
        part = raw_part.replace("~1", "/").replace("~0", "~")
        if not isinstance(current, dict) or part not in current:
            fail("schema-document-invalid", f"unresolved schema ref {reference}")
        current = current[part]
    return current


def type_matches(value: object, declared: str) -> bool:
    if declared == "object":
        return isinstance(value, dict)
    if declared == "array":
        return isinstance(value, list)
    if declared == "string":
        return isinstance(value, str)
    if declared == "integer":
        return isinstance(value, int) and not isinstance(value, bool)
    if declared == "boolean":
        return isinstance(value, bool)
    return False


def validate_schema_instance(value: object, node: object, root: dict, location: str = "$") -> None:
    if node is True:
        return
    if node is False or not isinstance(node, dict):
        fail("manifest-schema-invalid", f"schema rejected {location}")
    if "$ref" in node:
        validate_schema_instance(value, resolve_ref(root, node["$ref"]), root, location)
        return
    declared_type = node.get("type")
    if declared_type is not None and (
        not isinstance(declared_type, str) or not type_matches(value, declared_type)
    ):
        fail("manifest-schema-invalid", f"{location} must have type {declared_type}")
    if "const" in node and value != node["const"]:
        fail("manifest-schema-invalid", f"{location} does not equal its const")
    if "enum" in node and value not in node["enum"]:
        fail("manifest-schema-invalid", f"{location} is outside its enum")
    if isinstance(value, dict):
        required = node.get("required", [])
        if not isinstance(required, list) or not all(isinstance(item, str) for item in required):
            fail("schema-document-invalid", f"required at {location} must be a string array")
        missing = sorted(set(required) - set(value))
        if missing:
            fail("manifest-schema-invalid", f"{location} missing keys: {', '.join(missing)}")
        properties = node.get("properties", {})
        if not isinstance(properties, dict):
            fail("schema-document-invalid", f"properties at {location} must be an object")
        if node.get("additionalProperties") is False:
            unknown = sorted(set(value) - set(properties))
            if unknown:
                fail("manifest-schema-invalid", f"{location} has unknown keys: {', '.join(unknown)}")
        for key, child in value.items():
            if key in properties:
                validate_schema_instance(child, properties[key], root, f"{location}.{key}")
    if isinstance(value, list):
        if "minItems" in node and len(value) < node["minItems"]:
            fail("manifest-schema-invalid", f"{location} has too few items")
        if "maxItems" in node and len(value) > node["maxItems"]:
            fail("manifest-schema-invalid", f"{location} has too many items")
        if node.get("uniqueItems") is True:
            encoded = [json.dumps(item, ensure_ascii=False, sort_keys=True) for item in value]
            if len(encoded) != len(set(encoded)):
                fail("manifest-schema-invalid", f"{location} items must be unique")
        if "items" in node:
            for index, child in enumerate(value):
                validate_schema_instance(child, node["items"], root, f"{location}[{index}]")
    if isinstance(value, str):
        if "minLength" in node and len(value) < node["minLength"]:
            fail("manifest-schema-invalid", f"{location} is too short")
        if "maxLength" in node and len(value) > node["maxLength"]:
            fail("manifest-schema-invalid", f"{location} is too long")
        if "pattern" in node and re.search(node["pattern"], value) is None:
            fail("manifest-schema-invalid", f"{location} does not match its pattern")
    if isinstance(value, int) and not isinstance(value, bool):
        if "minimum" in node and value < node["minimum"]:
            fail("manifest-schema-invalid", f"{location} is below its minimum")
        if "maximum" in node and value > node["maximum"]:
            fail("manifest-schema-invalid", f"{location} exceeds its maximum")


def parse_jsonl(raw: bytes) -> list[dict]:
    try:
        text = raw.decode("utf-8")
    except UnicodeError as error:
        fail("transcript-invalid", f"transcript is not UTF-8: {error}")
    if not text:
        fail("transcript-invalid", "transcript is empty")
    events: list[dict] = []
    for index, line in enumerate(text.splitlines(), start=1):
        if not line:
            fail("transcript-invalid", f"transcript line {index} is empty")
        events.append(parse_json(line.encode("utf-8"), f"transcript line {index}"))
    return events


def extract_content(event: dict) -> list[dict]:
    message = event.get("message")
    if not isinstance(message, dict):
        return []
    content = message.get("content")
    if not isinstance(content, list):
        return []
    return [item for item in content if isinstance(item, dict)]


def tool_result_status(event: dict, item: dict) -> str:
    if item.get("is_error") is True:
        return "failed"
    transport = event.get("tool_use_result")
    if isinstance(transport, dict):
        if transport.get("isHardFailure") is True or transport.get("interrupted") is True:
            return "failed"
        exit_code = transport.get("exitCode")
        if isinstance(exit_code, int) and not isinstance(exit_code, bool):
            return "passed" if exit_code == 0 else "failed"
        if transport.get("kind") == "completed":
            return "passed"
    metadata = event.get("tool_result_meta")
    if isinstance(metadata, list):
        tool_id = item.get("tool_use_id")
        if any(
            isinstance(entry, dict)
            and entry.get("id") == tool_id
            and entry.get("non_execution_kind") == "permission-rule"
            for entry in metadata
        ):
            return "failed"
    return "passed"


def validate_safe_description(value: object) -> None:
    if not isinstance(value, str) or not value or len(value.encode("utf-8")) > 512:
        fail("transport-command-invalid", "tee description must be non-empty and at most 512 UTF-8 bytes")
    for character in value:
        if unicodedata.category(character) in UNSAFE_TEXT_CATEGORIES:
            fail("transport-command-invalid", "tee description contains an unsafe code point")


def parse_tee_payload(tool: dict) -> dict | None:
    if tool["name"] != "Bash":
        return None
    payload = tool["input"]
    if not isinstance(payload, dict):
        fail("tool-input-invalid", f"Bash tool {tool['id']} input must be an object")
    command = payload.get("command")
    if not isinstance(command, str):
        fail("tool-input-invalid", f"Bash tool {tool['id']} command must be a string")
    resembles_tee = TEE_DELIMITER in command or "tee /dev/null" in command
    lines = command.split("\n")
    exact = len(lines) >= 3 and lines[0] == TEE_FIRST_LINE and lines[-1] == TEE_DELIMITER
    if not exact:
        if resembles_tee:
            fail("transport-command-invalid", "WorkerResult tee command is not the canonical closed envelope")
        return None
    if set(payload) != {"command", "description"}:
        fail("transport-command-invalid", "WorkerResult tee input must contain only command and description")
    validate_safe_description(payload.get("description"))
    raw_payload = "\n".join(lines[1:-1]).encode("utf-8")
    return parse_json(raw_payload, "WorkerResult tee payload")


def command_contains_word(command: str, word: str) -> bool:
    boundary = r"[A-Za-z0-9._+-]"
    return re.search(rf"(?<!{boundary}){re.escape(word)}(?!{boundary})", command) is not None


def validate_subject(
    subject: dict, task_spec: dict, worker_request: dict, worker_result: dict, meta: dict, task_spec_digest: str
) -> None:
    expected = {
        "taskId": subject["taskId"],
        "runId": subject["runId"],
        "attemptId": subject["attemptId"],
    }
    for field, value in expected.items():
        if worker_result.get(field) != value:
            fail("subject-mismatch", f"WorkerResult {field} does not match manifest subject")
        if worker_request.get(field) != value:
            fail("subject-mismatch", f"WorkerRequest {field} does not match manifest subject")
    adapter = worker_result.get("adapter")
    if not isinstance(adapter, dict) or adapter.get("id") != subject["adapterId"]:
        fail("subject-mismatch", "WorkerResult adapter id does not match manifest subject")
    if adapter.get("version") != subject["binaryVersion"]:
        fail("subject-mismatch", "WorkerResult adapter version does not match manifest subject")
    if worker_request.get("adapterId") != subject["adapterId"]:
        fail("subject-mismatch", "WorkerRequest adapterId does not match manifest subject")
    if worker_request.get("baseSha") != subject["sourceHead"]:
        fail("source-head-mismatch", "WorkerRequest baseSha does not match manifest sourceHead")
    if worker_request.get("specDigest") != task_spec_digest:
        fail("subject-mismatch", "WorkerRequest specDigest does not match TaskSpec raw bytes")
    metadata = task_spec.get("metadata")
    worker = task_spec.get("worker")
    if not isinstance(metadata, dict) or metadata.get("id") != subject["taskId"]:
        fail("subject-mismatch", "TaskSpec metadata.id does not match manifest taskId")
    if not isinstance(worker, dict) or worker.get("preferredAdapter") != subject["adapterId"]:
        fail("subject-mismatch", "TaskSpec preferredAdapter does not match manifest adapter")
    meta_fields = {
        "qodercliVersion": subject["binaryVersion"],
        "protocolVersion": subject["protocolVersion"],
        "permissionMode": subject["permissionMode"],
    }
    for field, value in meta_fields.items():
        if meta.get(field) != value:
            fail("subject-mismatch", f"transcript metadata {field} does not match manifest subject")


def validate_constraints(task_spec: dict, literals: list[str]) -> None:
    work = task_spec.get("work")
    constraints = work.get("constraints") if isinstance(work, dict) else None
    if not isinstance(constraints, list) or not all(isinstance(item, str) for item in constraints):
        fail("task-constraint-mismatch", "TaskSpec work.constraints must be a string array")
    missing = [literal for literal in literals if literal not in constraints]
    if missing:
        fail("task-constraint-mismatch", "TaskSpec is missing an exact required transport constraint")


def validate_transcript(
    events: list[dict], transcript_raw: bytes, meta: dict, task_spec: dict, worker_result: dict, policy: dict
) -> dict:
    tools: list[dict] = []
    results: dict[str, dict] = {}
    for event_index, event in enumerate(events):
        for item in extract_content(event):
            if item.get("type") == "tool_use":
                tool_id = item.get("id")
                name = item.get("name")
                if not isinstance(tool_id, str) or not tool_id or not isinstance(name, str) or not name:
                    fail("transcript-invalid", "tool_use must carry non-empty id and name")
                if any(existing["id"] == tool_id for existing in tools):
                    fail("transcript-invalid", f"duplicate tool_use id {tool_id}")
                tools.append(
                    {
                        "id": tool_id,
                        "name": name,
                        "input": item.get("input"),
                        "eventIndex": event_index,
                    }
                )
            elif item.get("type") == "tool_result":
                tool_id = item.get("tool_use_id")
                if not isinstance(tool_id, str) or not tool_id or tool_id in results:
                    fail("transcript-invalid", "tool_result must bind one unique tool_use id")
                results[tool_id] = {
                    "status": tool_result_status(event, item),
                    "eventIndex": event_index,
                }
    tool_ids = {tool["id"] for tool in tools}
    if set(results) != tool_ids:
        fail("transcript-invalid", "every tool_use must have exactly one matching tool_result")

    allowed = set(policy["allowedToolNames"])
    forbidden = set(policy["forbiddenToolNames"])
    if allowed & forbidden:
        fail("policy-invalid", "allowedToolNames and forbiddenToolNames overlap")
    for tool in tools:
        if tool["name"] in forbidden:
            fail("forbidden-tool-executed", f"forbidden tool {tool['name']} was executed")
        if tool["name"] not in allowed:
            fail("tool-not-allowed", f"tool {tool['name']} is outside the attested allowlist")

    worker_tools = task_spec.get("worker", {}).get("tools")
    if worker_tools is not None:
        if not isinstance(worker_tools, list):
            fail("task-tool-policy-mismatch", "TaskSpec worker.tools must be an array")
        for tool in tools:
            mapped = TASK_TOOL_NAMES.get(tool["name"])
            if mapped is None or mapped not in worker_tools:
                fail("task-tool-policy-mismatch", f"tool {tool['name']} is outside TaskSpec worker.tools")

    tee_calls: list[tuple[dict, dict]] = []
    command_calls: list[dict] = []
    for tool in tools:
        tee_payload = parse_tee_payload(tool)
        if tee_payload is not None:
            tee_calls.append((tool, tee_payload))
            continue
        if tool["name"] == "Bash":
            if not isinstance(tool["input"], dict) or not isinstance(tool["input"].get("command"), str):
                fail("tool-input-invalid", f"Bash tool {tool['id']} lacks a string command")
            command = tool["input"]["command"]
            for word in policy["forbiddenCommandWords"]:
                if command_contains_word(command, word):
                    fail("forbidden-command-executed", f"forbidden command word {word!r} was executed")
            command_calls.append(
                {
                    "toolUseId": tool["id"],
                    "commandDigest": sha256_bytes(command.encode("utf-8")),
                    "status": results[tool["id"]]["status"],
                }
            )

    if len(tee_calls) != 1:
        fail("result-tee-count-invalid", "transcript must contain exactly one canonical WorkerResult tee")
    tee_tool, tee_payload = tee_calls[0]
    if results[tee_tool["id"]]["status"] != "passed":
        fail("result-tee-failed", "the unique WorkerResult tee did not succeed")
    tee_result_index = results[tee_tool["id"]]["eventIndex"]
    if any(tool["eventIndex"] > tee_result_index for tool in tools):
        fail("post-result-tool-use", "a tool_use occurred after the successful WorkerResult tee result")
    if tee_tool is not tools[-1]:
        fail("result-tee-not-last", "the WorkerResult tee was not the final tool_use")

    if not events or events[-1].get("type") != "result":
        fail("terminal-event-invalid", "transcript must end with a terminal result event")
    terminal = events[-1]
    if (
        terminal.get("subtype") != "success"
        or terminal.get("is_error") is not False
        or terminal.get("stop_reason") != "end_turn"
    ):
        fail("terminal-event-invalid", "terminal result must be success/end_turn")

    bindings = policy["commandBindings"]
    binding_by_tool: dict[str, dict] = {}
    command_ids: set[str] = set()
    for binding in bindings:
        if binding["toolUseId"] in binding_by_tool or binding["commandId"] in command_ids:
            fail("policy-invalid", "commandBindings must have unique toolUseId and commandId")
        binding_by_tool[binding["toolUseId"]] = binding
        command_ids.add(binding["commandId"])
    if command_calls and not bindings:
        fail("undeclared-command-executed", "non-transport Bash commands ran without command bindings")
    if {call["toolUseId"] for call in command_calls} != set(binding_by_tool):
        fail("undeclared-command-executed", "actual Bash command set differs from commandBindings")
    expected_declarations: list[dict] = []
    for call in command_calls:
        binding = binding_by_tool[call["toolUseId"]]
        if binding["commandDigest"] != call["commandDigest"]:
            fail("command-binding-mismatch", "command binding raw digest does not match transcript command")
        expected_declarations.append({"commandId": binding["commandId"], "status": call["status"]})

    declared = worker_result.get("declaredCommands")
    if not isinstance(declared, list):
        fail("worker-result-invalid", "WorkerResult declaredCommands must be an array")
    normalized_declared: list[dict] = []
    for item in declared:
        if not isinstance(item, dict) or not isinstance(item.get("commandId"), str):
            fail("worker-result-invalid", "declaredCommands entries must contain commandId")
        if item.get("status") not in {"passed", "failed"}:
            fail("declared-command-mismatch", "executed commands must be declared passed or failed")
        normalized_declared.append({"commandId": item["commandId"], "status": item["status"]})
    if normalized_declared != expected_declarations:
        fail("declared-command-mismatch", "WorkerResult declaredCommands differ from actual transcript commands")

    tee_fields = (
        "apiVersion",
        "kind",
        "taskId",
        "runId",
        "attemptId",
        "status",
        "summary",
        "declaredChangedFiles",
        "declaredArtifacts",
        "declaredCommands",
        "declaredRisks",
    )
    for field in tee_fields:
        if tee_payload.get(field) != worker_result.get(field):
            fail("tee-payload-mismatch", f"tee payload {field} differs from accepted WorkerResult")

    expected_meta = {
        "capturedBytes": len(transcript_raw),
        "eventCount": len(events),
        "toolCalls": len(tools),
        "workerResultTeeAttempts": 1,
        "workerResultTeeSuccesses": 1,
        "workerResultTeeLast": True,
    }
    for field, expected in expected_meta.items():
        if meta.get(field) != expected:
            fail("transcript-meta-mismatch", f"transcript metadata {field} does not match raw transcript")
    actual_names = sorted({tool["name"].lower() for tool in tools})
    if meta.get("toolNames") != actual_names:
        fail("transcript-meta-mismatch", "transcript metadata toolNames differs from actual tools")
    if meta.get("outputTruncated") is not False or meta.get("exitCode") != 0:
        fail("transcript-meta-mismatch", "transcript metadata must show untruncated exit 0")

    return {
        "eventCount": len(events),
        "toolCalls": len(tools),
        "toolNames": actual_names,
        "commandCalls": len(command_calls),
        "workerResultTeeSuccesses": 1,
        "workerResultTeeLast": True,
    }


def load_inputs(root: Path, manifest: dict) -> tuple[dict[str, bytes], dict[str, str]]:
    raw_inputs: dict[str, bytes] = {}
    digests: dict[str, str] = {}
    for label, hard_limit in INPUT_HARD_LIMITS.items():
        descriptor = manifest["inputs"][label]
        maximum = descriptor["maxBytes"]
        if maximum > hard_limit:
            fail("input-bound-invalid", f"{label} maxBytes exceeds the validator hard limit")
        raw = read_relative_nofollow(root, descriptor["path"], maximum, label)
        digest = sha256_bytes(raw)
        if digest != descriptor["sha256"]:
            fail("input-digest-mismatch", f"{label} raw byte digest does not match manifest")
        raw_inputs[label] = raw
        digests[label] = digest
    if len({manifest["inputs"][label]["path"] for label in INPUT_HARD_LIMITS}) != len(INPUT_HARD_LIMITS):
        fail("input-path-invalid", "each input must use a distinct path")
    return raw_inputs, digests


def attestation_digest(manifest_digest: str, input_digests: dict[str, str]) -> str:
    frames = [b"marshal-transcript-attestation-v1\n", manifest_digest.encode("ascii"), b"\n"]
    for label in sorted(input_digests):
        frames.extend([label.encode("ascii"), b"\0", input_digests[label].encode("ascii"), b"\n"])
    return sha256_bytes(b"".join(frames))


def run(arguments: argparse.Namespace) -> dict:
    root = Path(arguments.root)
    if not root.is_absolute():
        fail("input-root-invalid", "--root must be absolute")
    manifest_raw = read_relative_nofollow(root, arguments.manifest, MANIFEST_MAX_BYTES, "manifest")
    manifest = parse_json(manifest_raw, "manifest")

    schema_path = Path(__file__).resolve().with_name(SCHEMA_NAME)
    schema_raw = read_relative_nofollow(schema_path.parent, schema_path.name, SCHEMA_MAX_BYTES, "schema")
    schema = parse_json(schema_raw, "schema")
    validate_schema_document(schema)
    validate_schema_instance(manifest, schema, schema)

    raw_inputs, digests = load_inputs(root, manifest)
    task_spec = parse_json(raw_inputs["taskSpec"], "TaskSpec")
    worker_result = parse_json(raw_inputs["workerResult"], "WorkerResult")
    worker_request = parse_json(raw_inputs["workerRequest"], "WorkerRequest")
    meta = parse_json(raw_inputs["transcriptMeta"], "transcript metadata")
    events = parse_jsonl(raw_inputs["transcript"])
    validate_subject(
        manifest["subject"],
        task_spec,
        worker_request,
        worker_result,
        meta,
        digests["taskSpec"],
    )
    validate_constraints(task_spec, manifest["policy"]["requiredConstraintLiterals"])
    observation = validate_transcript(
        events,
        raw_inputs["transcript"],
        meta,
        task_spec,
        worker_result,
        manifest["policy"],
    )
    manifest_digest = sha256_bytes(manifest_raw)
    return {
        "status": "pass",
        "reasonCode": "transcript-attestation-pass",
        "subject": manifest["subject"],
        "manifestDigest": manifest_digest,
        "inputDigests": digests,
        "observation": observation,
        "attestationDigest": attestation_digest(manifest_digest, digests),
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", required=True, help="absolute compact input root")
    parser.add_argument("--manifest", required=True, help="manifest path relative to --root")
    arguments = parser.parse_args()
    try:
        payload = run(arguments)
    except PreflightError as error:
        print(
            json.dumps(
                {"status": "fail", "reasonCode": error.reason_code, "message": str(error)},
                ensure_ascii=False,
                sort_keys=True,
            ),
            file=sys.stderr,
        )
        return 1
    print(json.dumps(payload, ensure_ascii=False, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
