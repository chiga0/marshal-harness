#!/usr/bin/env python3
"""Fail-closed operator-local preflight for one strict content-gate grammar."""

from __future__ import annotations

import argparse
import ast
import fnmatch
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import stat
import subprocess
import sys
import tempfile
import unicodedata


DIGEST_RE = re.compile(r"^sha256:[0-9a-f]{64}$")
NORMALIZERS = {"nfkc-casefold", "markdown-backtick-strip+nfkc-casefold"}
COMMAND_FIELDS = {
    "id",
    "argv",
    "cwd",
    "timeoutSeconds",
    "required",
    "baselinePolicy",
    "maxLogBytes",
}
SIDE_EFFECT_CALLS = {
    "chmod",
    "mkdir",
    "open",
    "remove",
    "rename",
    "replace",
    "rmdir",
    "touch",
    "unlink",
    "write",
    "write_bytes",
    "write_text",
}
BASE_NEGATIVE_REASONS = {
    "below-minimum-line-count",
    "maximum-bytes-exceeded",
    "missing-required-all",
    "missing-required-any",
    "forbidden-present",
}


class PreflightError(Exception):
    def __init__(self, reason_code: str, message: str):
        super().__init__(message)
        self.reason_code = reason_code


def fail(reason_code: str, message: str) -> None:
    raise PreflightError(reason_code, message)


def load_json(path: Path, label: str) -> dict:
    def reject_duplicates(pairs: list[tuple[str, object]]) -> dict:
        result: dict = {}
        for key, value in pairs:
            if key in result:
                fail("duplicate-json-key", f"{label} contains duplicate key {key!r}")
            result[key] = value
        return result

    try:
        value = json.loads(path.read_text(encoding="utf-8"), object_pairs_hook=reject_duplicates)
    except PreflightError:
        raise
    except (OSError, UnicodeError, json.JSONDecodeError) as error:
        fail("invalid-json", f"cannot read {label}: {error}")
    if not isinstance(value, dict):
        fail("invalid-json", f"{label} must be a JSON object")
    return value


def canonical_digest(value: object) -> str:
    payload = json.dumps(
        value, ensure_ascii=False, sort_keys=True, separators=(",", ":")
    ).encode("utf-8")
    return "sha256:" + hashlib.sha256(payload).hexdigest()


def file_digest(path: Path) -> str:
    try:
        return "sha256:" + hashlib.sha256(path.read_bytes()).hexdigest()
    except OSError as error:
        fail("fixture-unreadable", f"cannot read {path}: {error}")


def read_fixture_text(path: Path) -> str:
    try:
        return path.read_text(encoding="utf-8")
    except (OSError, UnicodeError) as error:
        fail("fixture-unreadable", f"cannot read UTF-8 fixture {path}: {error}")


def require_keys(value: object, required: set[str], allowed: set[str], label: str) -> None:
    if not isinstance(value, dict):
        fail("manifest-shape-invalid", f"{label} must be an object")
    missing = sorted(required - value.keys())
    unknown = sorted(value.keys() - allowed)
    if missing:
        fail("manifest-shape-invalid", f"{label} missing keys: {', '.join(missing)}")
    if unknown:
        fail("manifest-shape-invalid", f"{label} has unknown keys: {', '.join(unknown)}")


def require_nonempty_string(value: object, label: str) -> str:
    if not isinstance(value, str) or not value.strip():
        fail("manifest-shape-invalid", f"{label} must be a non-empty string")
    return value


def require_digest(value: object, label: str) -> str:
    digest = require_nonempty_string(value, label)
    if not DIGEST_RE.fullmatch(digest):
        fail("manifest-shape-invalid", f"{label} must be a lowercase sha256 digest")
    return digest


def clean_relative_path(value: object, label: str) -> str:
    raw = require_nonempty_string(value, label)
    candidate = Path(raw)
    if (
        candidate.is_absolute()
        or ".." in candidate.parts
        or candidate == Path(".") and raw != "."
        or "\\" in raw
        or "\x00" in raw
    ):
        fail("path-boundary-invalid", f"{label} must be a clean relative path")
    return candidate.as_posix()


def relative_file(root: Path, value: object, label: str) -> Path:
    raw = clean_relative_path(value, label)
    if raw == ".":
        fail("path-boundary-invalid", f"{label} must name a file")
    resolved = (root / raw).resolve()
    try:
        resolved.relative_to(root)
    except ValueError:
        fail("path-boundary-invalid", f"{label} escapes the declared root")
    return resolved


def normalize(text: str, normalizer: str) -> str:
    if normalizer == "markdown-backtick-strip+nfkc-casefold":
        text = text.replace("`", "")
    return unicodedata.normalize("NFKC", text).casefold()


# The validator intentionally implements only the Draft 2020-12 keywords used
# by its adjacent operator-local schema. Unknown keywords fail closed instead
# of silently weakening validation.
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
    "uniqueItems",
    "minLength",
    "pattern",
    "minimum",
    "maximum",
}


def validate_schema_document(schema: dict) -> None:
    if schema.get("$schema") != "https://json-schema.org/draft/2020-12/schema":
        fail("schema-document-invalid", "manifest schema must declare Draft 2020-12")
    if not isinstance(schema.get("$id"), str) or "operator-local" not in schema["$id"]:
        fail("schema-document-invalid", "manifest schema must carry an operator-local id")

    def walk(node: object, location: str) -> None:
        if isinstance(node, dict):
            unknown = set(node) - SUPPORTED_SCHEMA_KEYS
            if unknown:
                fail(
                    "schema-document-invalid",
                    f"unsupported schema keywords at {location}: {', '.join(sorted(unknown))}",
                )
            for key, value in node.items():
                if key in {"properties", "$defs"}:
                    if not isinstance(value, dict):
                        fail("schema-document-invalid", f"{location}/{key} must be an object")
                    for child_key, child in value.items():
                        walk(child, f"{location}/{key}/{child_key}")
                elif key == "items":
                    walk(value, f"{location}/items")
        elif not isinstance(node, bool):
            fail("schema-document-invalid", f"schema node {location} must be object or boolean")

    walk(schema, "#")


def schema_type_matches(value: object, declared: str) -> bool:
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


def resolve_local_ref(root_schema: dict, reference: str) -> object:
    if not reference.startswith("#/"):
        fail("schema-document-invalid", f"only local schema refs are supported: {reference}")
    current: object = root_schema
    for part in reference[2:].split("/"):
        key = part.replace("~1", "/").replace("~0", "~")
        if not isinstance(current, dict) or key not in current:
            fail("schema-document-invalid", f"unresolved schema ref: {reference}")
        current = current[key]
    return current


def validate_schema_instance(
    value: object, schema: object, root_schema: dict, location: str = "$"
) -> None:
    if schema is True:
        return
    if schema is False or not isinstance(schema, dict):
        fail("manifest-schema-invalid", f"schema rejected {location}")
    if "$ref" in schema:
        validate_schema_instance(value, resolve_local_ref(root_schema, schema["$ref"]), root_schema, location)
        return
    declared_type = schema.get("type")
    if declared_type is not None and (
        not isinstance(declared_type, str) or not schema_type_matches(value, declared_type)
    ):
        fail("manifest-schema-invalid", f"{location} must have type {declared_type}")
    if "const" in schema and value != schema["const"]:
        fail("manifest-schema-invalid", f"{location} does not equal its const")
    if "enum" in schema and value not in schema["enum"]:
        fail("manifest-schema-invalid", f"{location} is outside its enum")
    if isinstance(value, dict):
        required = schema.get("required", [])
        if not isinstance(required, list) or not all(isinstance(item, str) for item in required):
            fail("schema-document-invalid", f"required at {location} must be a string array")
        missing = [key for key in required if key not in value]
        if missing:
            fail("manifest-schema-invalid", f"{location} missing keys: {', '.join(missing)}")
        properties = schema.get("properties", {})
        if not isinstance(properties, dict):
            fail("schema-document-invalid", f"properties at {location} must be an object")
        if schema.get("additionalProperties") is False:
            unknown = sorted(set(value) - set(properties))
            if unknown:
                fail("manifest-schema-invalid", f"{location} has unknown keys: {', '.join(unknown)}")
        for key, child in value.items():
            if key in properties:
                validate_schema_instance(child, properties[key], root_schema, f"{location}.{key}")
    if isinstance(value, list):
        minimum = schema.get("minItems")
        if minimum is not None and len(value) < minimum:
            fail("manifest-schema-invalid", f"{location} has fewer than {minimum} items")
        if schema.get("uniqueItems") is True:
            canonical = [json.dumps(item, ensure_ascii=False, sort_keys=True) for item in value]
            if len(set(canonical)) != len(canonical):
                fail("manifest-schema-invalid", f"{location} items must be unique")
        if "items" in schema:
            for index, item in enumerate(value):
                validate_schema_instance(item, schema["items"], root_schema, f"{location}[{index}]")
    if isinstance(value, str):
        minimum = schema.get("minLength")
        if minimum is not None and len(value) < minimum:
            fail("manifest-schema-invalid", f"{location} is shorter than {minimum}")
        pattern = schema.get("pattern")
        if pattern is not None:
            try:
                matches = re.search(pattern, value) is not None
            except re.error as error:
                fail("schema-document-invalid", f"invalid pattern at {location}: {error}")
            if not matches:
                fail("manifest-schema-invalid", f"{location} does not match its pattern")
    if isinstance(value, int) and not isinstance(value, bool):
        if "minimum" in schema and value < schema["minimum"]:
            fail("manifest-schema-invalid", f"{location} is below its minimum")
        if "maximum" in schema and value > schema["maximum"]:
            fail("manifest-schema-invalid", f"{location} exceeds its maximum")


def literal_list(node: ast.AST, label: str) -> list:
    try:
        value = ast.literal_eval(node)
    except (ValueError, TypeError, SyntaxError):
        fail("unsupported-content-gate-grammar", f"{label} must be a literal array")
    if not isinstance(value, list):
        fail("unsupported-content-gate-grammar", f"{label} must be a literal array")
    return value


def assignment_value(statement: ast.stmt, target: str) -> ast.AST:
    if (
        not isinstance(statement, ast.Assign)
        or len(statement.targets) != 1
        or not isinstance(statement.targets[0], ast.Name)
        or statement.targets[0].id != target
    ):
        fail("unsupported-content-gate-grammar", f"expected canonical assignment to {target}")
    return statement.value


def expression_matches(node: ast.AST, expression: str) -> bool:
    expected = ast.parse(expression, mode="eval").body
    return ast.dump(node, include_attributes=False) == ast.dump(expected, include_attributes=False)


def canonical_gate_script(gate: dict) -> str:
    source = "s.replace('`', '')" if gate["normalizer"] == "markdown-backtick-strip+nfkc-casefold" else "s"
    return "\n".join(
        [
            "from pathlib import Path",
            "import unicodedata",
            f"p = Path({gate['commandPath']!r})",
            "s = p.read_text(encoding='utf-8')",
            f"n = unicodedata.normalize('NFKC', {source}).casefold()",
            f"required_all = {gate['required_all']!r}",
            f"required_any = {gate['required_any']!r}",
            f"forbidden = {gate['forbidden']!r}",
            f"assert len(s.splitlines()) >= {gate['minimumLineCount']}",
            f"assert len(s.encode('utf-8')) <= {gate['maximumBytes']}",
            "assert not [x for x in required_all if x not in n]",
            "assert not [g for g in required_any if not any(x in n for x in g)]",
            "assert not [x for x in forbidden if x in n]",
        ]
    )


def reject_side_effect_ast(tree: ast.AST) -> None:
    for node in ast.walk(tree):
        if isinstance(node, (ast.ImportFrom, ast.Import)):
            modules = []
            if isinstance(node, ast.ImportFrom):
                modules = [node.module or ""]
            else:
                modules = [alias.name for alias in node.names]
            if any(module not in {"pathlib", "unicodedata"} for module in modules):
                fail("purity-side-effect-rejected", "content gate imports a non-allowlisted module")
        if isinstance(node, ast.Call):
            if isinstance(node.func, ast.Name) and node.func.id in SIDE_EFFECT_CALLS:
                fail("purity-side-effect-rejected", f"content gate calls {node.func.id}")
            if isinstance(node.func, ast.Attribute) and node.func.attr in SIDE_EFFECT_CALLS:
                if node.func.attr == "replace" and isinstance(node.func.value, ast.Name) and node.func.value.id == "s":
                    continue
                fail("purity-side-effect-rejected", f"content gate calls {node.func.attr}")


def extract_content_gate(argv: list[str]) -> dict:
    if len(argv) != 4 or argv[:3] != ["python3", "-B", "-c"]:
        fail(
            "unsupported-content-gate-grammar",
            "content gate must use canonical python3 -B -c argv without a shell wrapper",
        )
    try:
        tree = ast.parse(argv[3], mode="exec")
    except SyntaxError as error:
        fail("unsupported-content-gate-grammar", f"content gate is not valid Python: {error}")
    reject_side_effect_ast(tree)
    if len(tree.body) != 13:
        fail("unsupported-content-gate-grammar", "content gate must contain exactly 13 canonical statements")
    path_node = assignment_value(tree.body[2], "p")
    if (
        not isinstance(path_node, ast.Call)
        or not isinstance(path_node.func, ast.Name)
        or path_node.func.id != "Path"
        or len(path_node.args) != 1
        or path_node.keywords
        or not isinstance(path_node.args[0], ast.Constant)
        or not isinstance(path_node.args[0].value, str)
    ):
        fail("unsupported-content-gate-grammar", "content gate Path must have one literal argument")
    command_path = clean_relative_path(path_node.args[0].value, "content gate path")
    if command_path == ".":
        fail("path-boundary-invalid", "content gate path must name a file")

    normalizer_node = assignment_value(tree.body[4], "n")
    if expression_matches(normalizer_node, "unicodedata.normalize('NFKC', s).casefold()"):
        normalizer = "nfkc-casefold"
    elif expression_matches(
        normalizer_node,
        "unicodedata.normalize('NFKC', s.replace('`', '')).casefold()",
    ):
        normalizer = "markdown-backtick-strip+nfkc-casefold"
    else:
        fail("normalizer-unsupported", "content gate normalizer is outside the closed grammar")

    required_all = literal_list(assignment_value(tree.body[5], "required_all"), "required_all")
    required_any = literal_list(assignment_value(tree.body[6], "required_any"), "required_any")
    forbidden = literal_list(assignment_value(tree.body[7], "forbidden"), "forbidden")

    minimum_node = tree.body[8]
    maximum_node = tree.body[9]
    if not isinstance(minimum_node, ast.Assert) or not isinstance(minimum_node.test, ast.Compare):
        fail("unsupported-content-gate-grammar", "minimum line assertion is not canonical")
    if not isinstance(maximum_node, ast.Assert) or not isinstance(maximum_node.test, ast.Compare):
        fail("unsupported-content-gate-grammar", "maximum byte assertion is not canonical")
    try:
        minimum_line_count = ast.literal_eval(minimum_node.test.comparators[0])
        maximum_bytes = ast.literal_eval(maximum_node.test.comparators[0])
    except (IndexError, ValueError, TypeError):
        fail("unsupported-content-gate-grammar", "content bounds must be integer literals")
    if (
        not isinstance(minimum_line_count, int)
        or isinstance(minimum_line_count, bool)
        or minimum_line_count < 1
        or not isinstance(maximum_bytes, int)
        or isinstance(maximum_bytes, bool)
        or maximum_bytes < 1
    ):
        fail("unsupported-content-gate-grammar", "content bounds must be positive integers")

    gate = {
        "commandPath": command_path,
        "normalizer": normalizer,
        "minimumLineCount": minimum_line_count,
        "maximumBytes": maximum_bytes,
        "required_all": required_all,
        "required_any": required_any,
        "forbidden": forbidden,
    }
    expected = ast.parse(canonical_gate_script(gate), mode="exec")
    if ast.dump(tree, include_attributes=False) != ast.dump(expected, include_attributes=False):
        fail("unsupported-content-gate-grammar", "content gate does not match the closed AST grammar")
    return gate


def validate_rule_arrays(gate: dict) -> None:
    for field in ("required_all", "required_any", "forbidden"):
        if not isinstance(gate[field], list) or not gate[field]:
            fail("semantic-manifest-invalid", f"{field} must be a non-empty array")
    for index, token in enumerate(gate["required_all"]):
        require_nonempty_string(token, f"required_all[{index}]")
        if normalize(token, gate["normalizer"]) != token:
            fail("token-not-normalized", f"required_all[{index}] must already be normalized")
    for index, alternatives in enumerate(gate["required_any"]):
        if not isinstance(alternatives, list) or len(alternatives) < 2:
            fail("required-any-not-equivalent", f"required_any[{index}] needs at least two alternatives")
        normalized = []
        for alternative_index, token in enumerate(alternatives):
            require_nonempty_string(token, f"required_any[{index}][{alternative_index}]")
            canonical = normalize(token, gate["normalizer"])
            if canonical != token:
                fail(
                    "token-not-normalized",
                    f"required_any[{index}][{alternative_index}] must already be normalized",
                )
            normalized.append(canonical)
        if len(set(normalized)) != len(normalized):
            fail("required-any-not-equivalent", f"required_any[{index}] contains normalized duplicates")
    for index, token in enumerate(gate["forbidden"]):
        require_nonempty_string(token, f"forbidden[{index}]")
        if normalize(token, gate["normalizer"]) != token:
            fail("token-not-normalized", f"forbidden[{index}] must already be normalized")


def task_command(task_spec: dict, command_id: str) -> dict:
    acceptance = task_spec.get("acceptance")
    commands = acceptance.get("commands") if isinstance(acceptance, dict) else None
    if not isinstance(commands, list):
        fail("task-spec-shape-invalid", "TaskSpec acceptance.commands must be an array")
    matches = [item for item in commands if isinstance(item, dict) and item.get("id") == command_id]
    if len(matches) != 1:
        fail("command-binding-invalid", "command.id must select exactly one acceptance command")
    command = matches[0]
    if command.get("required") is not True:
        fail("command-not-required", "content preflight only accepts required:true commands")
    if set(command) != COMMAND_FIELDS:
        fail("command-tuple-incomplete", "selected command must declare the full canonical tuple")
    return command


def command_tuple(command: dict) -> dict:
    return {key: command[key] for key in sorted(COMMAND_FIELDS)}


def validate_command_binding(manifest_command: dict, selected: dict) -> None:
    bound = command_tuple(selected)
    manifest_tuple = {key: manifest_command[key] for key in sorted(COMMAND_FIELDS)}
    if manifest_tuple != bound:
        fail("command-tuple-mismatch", "manifest command tuple does not equal TaskSpec command")
    if manifest_command["argvDigest"] != canonical_digest(bound["argv"]):
        fail("argv-digest-mismatch", "command argvDigest does not match canonical argv")
    if manifest_command["tupleDigest"] != canonical_digest(bound):
        fail("command-tuple-digest-mismatch", "command tupleDigest does not match canonical tuple")


def expected_prompt_literals(gate: dict) -> dict[str, str]:
    expected = {
        "commandPath": gate["commandPath"],
        "deliverablePath": gate["deliverablePath"],
        "minimumDeliverableCount": str(gate["minimumDeliverableCount"]),
        "minimumLineCount": str(gate["minimumLineCount"]),
        "maximumBytes": str(gate["maximumBytes"]),
    }
    for index, token in enumerate(gate["required_all"]):
        expected[f"required_all[{index}]"] = token
    for group_index, alternatives in enumerate(gate["required_any"]):
        for token_index, token in enumerate(alternatives):
            expected[f"required_any[{group_index}][{token_index}]"] = token
    for index, token in enumerate(gate["forbidden"]):
        expected[f"forbidden[{index}]"] = token
    return expected


def validate_prompt_literals(manifest: dict, task_spec: dict) -> None:
    mappings: dict[str, str] = {}
    for index, mapping in enumerate(manifest["prompt_literals"]):
        require_keys(mapping, {"rule", "literal"}, {"rule", "literal"}, f"prompt_literals[{index}]")
        rule = require_nonempty_string(mapping["rule"], f"prompt_literals[{index}].rule")
        literal = require_nonempty_string(mapping["literal"], f"prompt_literals[{index}].literal")
        if rule in mappings:
            fail("prompt-literal-unmapped", f"duplicate prompt mapping {rule!r}")
        mappings[rule] = literal
    expected = expected_prompt_literals(manifest["contentGate"])
    if mappings != expected:
        fail("prompt-literal-unmapped", "prompt literal mapping is not closed over every semantic field")
    work = task_spec.get("work")
    contexts = work.get("context") if isinstance(work, dict) else None
    if not isinstance(contexts, list) or not all(isinstance(item, str) for item in contexts):
        fail("task-spec-shape-invalid", "TaskSpec work.context must be an array of strings")
    prompt = "\n".join(contexts)
    for rule, literal in expected.items():
        marker = f"{rule}=`{literal}`"
        if marker not in prompt:
            fail("prompt-literal-unmapped", f"TaskSpec prompt lacks exact marker {marker!r}")


def validate_deliverable(task_spec: dict, gate: dict, cwd: str) -> None:
    clean_cwd = clean_relative_path(cwd, "command.cwd")
    command_path = clean_relative_path(gate["commandPath"], "contentGate.commandPath")
    combined = Path(command_path) if clean_cwd == "." else Path(clean_cwd) / command_path
    if ".." in combined.parts:
        fail("path-boundary-invalid", "command cwd plus path escapes the repository root")
    if combined.as_posix() != gate["deliverablePath"]:
        fail("deliverable-binding-invalid", "command cwd/path does not resolve to deliverablePath")
    deliverables = task_spec.get("deliverables")
    if not isinstance(deliverables, list):
        fail("task-spec-shape-invalid", "TaskSpec deliverables must be an array")
    matches = [
        item
        for item in deliverables
        if isinstance(item, dict)
        and item.get("required") is True
        and isinstance(item.get("pathGlob"), str)
        and fnmatch.fnmatchcase(gate["deliverablePath"], item["pathGlob"])
    ]
    if len(matches) != 1:
        fail("deliverable-binding-invalid", "deliverablePath must match exactly one required TaskSpec deliverable")
    minimum_count = matches[0].get("minimumCount", 1)
    if minimum_count != gate["minimumDeliverableCount"] or minimum_count != 1:
        fail("deliverable-binding-invalid", "single-file content gate requires minimumDeliverableCount=1")


def reject_protected_references(argv: list[str], protected_roots: list[Path]) -> None:
    for root in protected_roots:
        needle = str(root).casefold()
        if any(needle and needle in argument.casefold() for argument in argv):
            fail("protected-root-reference", "content command embeds a protected-root path")


def tree_digest(root: Path) -> str:
    hasher = hashlib.sha256()
    try:
        for path in sorted(root.rglob("*"), key=lambda item: item.as_posix()):
            relative = path.relative_to(root).as_posix().encode("utf-8")
            mode = path.lstat().st_mode
            hasher.update(relative + b"\0" + str(stat.S_IMODE(mode)).encode("ascii") + b"\0")
            if path.is_symlink():
                hasher.update(b"link\0" + os.readlink(path).encode("utf-8"))
            elif path.is_file():
                hasher.update(b"file\0" + path.read_bytes())
            elif path.is_dir():
                hasher.update(b"dir\0")
            else:
                fail("fixture-type-invalid", f"unsupported entry below protected root: {path}")
    except (OSError, UnicodeError) as error:
        fail("protected-root-unreadable", f"cannot hash protected tree {root}: {error}")
    return "sha256:" + hasher.hexdigest()


def snapshot_protected_roots(protected_roots: list[Path]) -> dict[Path, str]:
    return {root: tree_digest(root) for root in protected_roots}


def assert_protected_roots_unchanged(before: dict[Path, str]) -> None:
    for root, digest in before.items():
        if tree_digest(root) != digest:
            fail("protected-root-side-effect", f"acceptance command changed protected root {root}")


def run_command(
    command: dict,
    deliverable_path: str,
    fixture: Path,
    protected_before: dict[Path, str],
) -> int:
    with tempfile.TemporaryDirectory(prefix="marshal-acceptance-preflight-") as directory:
        fixture_root = Path(directory)
        target = fixture_root / deliverable_path
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(fixture, target)
        execution_cwd = fixture_root / command["cwd"]
        execution_cwd.mkdir(parents=True, exist_ok=True)
        environment = {
            "PATH": os.environ.get("PATH", ""),
            "HOME": str(fixture_root / ".home"),
            "TMPDIR": str(fixture_root / ".tmp"),
            "PYTHONDONTWRITEBYTECODE": "1",
        }
        (fixture_root / ".home").mkdir()
        (fixture_root / ".tmp").mkdir()
        before_fixture = tree_digest(fixture_root)
        argv = list(command["argv"])
        executable = shutil.which(argv[0], path=environment["PATH"])
        if executable is None:
            fail("command-execution-failed", f"cannot resolve executable {argv[0]!r}")
        argv[0] = executable
        try:
            completed = subprocess.run(
                argv,
                cwd=execution_cwd,
                env=environment,
                stdin=subprocess.DEVNULL,
                stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL,
                timeout=command["timeoutSeconds"],
                check=False,
            )
        except (OSError, subprocess.TimeoutExpired) as error:
            fail("command-execution-failed", f"acceptance command could not finish: {error}")
        assert_protected_roots_unchanged(protected_before)
        if tree_digest(fixture_root) != before_fixture:
            fail("fixture-tree-side-effect", "acceptance command changed its temporary fixture tree")
        return completed.returncode


def semantic_reason(document: str, gate: dict) -> str | None:
    if len(document.splitlines()) < gate["minimumLineCount"]:
        return "below-minimum-line-count"
    if len(document.encode("utf-8")) > gate["maximumBytes"]:
        return "maximum-bytes-exceeded"
    normalized = normalize(document, gate["normalizer"])
    if any(normalize(token, gate["normalizer"]) not in normalized for token in gate["required_all"]):
        return "missing-required-all"
    if any(
        not any(normalize(token, gate["normalizer"]) in normalized for token in alternatives)
        for alternatives in gate["required_any"]
    ):
        return "missing-required-any"
    if any(normalize(token, gate["normalizer"]) in normalized for token in gate["forbidden"]):
        return "forbidden-present"
    return None


def validate_fixture_entry(entry: object, label: str, root: Path) -> tuple[Path, str | None]:
    if not isinstance(entry, dict):
        fail("manifest-shape-invalid", f"{label} must be an object")
    required = {"id", "path", "digest"}
    allowed = required | {"expectedReason"}
    require_keys(entry, required, allowed, label)
    require_nonempty_string(entry["id"], f"{label}.id")
    path = relative_file(root, entry["path"], f"{label}.path")
    if not path.is_file() or path.is_symlink():
        fail("fixture-unreadable", f"{label}.path must be a regular non-symlink file")
    if file_digest(path) != require_digest(entry["digest"], f"{label}.digest"):
        fail("fixture-digest-mismatch", f"{label} digest does not match bytes")
    expected_reason = entry.get("expectedReason")
    if expected_reason is not None:
        require_nonempty_string(expected_reason, f"{label}.expectedReason")
    return path, expected_reason


def validate(
    manifest_path: Path,
    task_spec_path: Path,
    root: Path,
    schema_path: Path,
    extra_protected_roots: list[Path],
) -> dict:
    root = root.resolve()
    schema = load_json(schema_path, "manifest schema")
    validate_schema_document(schema)
    manifest = load_json(manifest_path, "manifest")
    validate_schema_instance(manifest, schema, schema)
    task_spec = load_json(task_spec_path, "TaskSpec")

    if file_digest(task_spec_path) != manifest["taskSpecDigest"]:
        fail("task-spec-digest-mismatch", "taskSpecDigest does not match TaskSpec bytes")
    selected = task_command(task_spec, manifest["command"]["id"])
    validate_command_binding(manifest["command"], selected)
    extracted = extract_content_gate(selected["argv"])
    manifest_gate = manifest["contentGate"]
    validate_rule_arrays(manifest_gate)
    if extracted["normalizer"] != manifest_gate["normalizer"]:
        fail("normalizer-drift", "content command and manifest normalizers differ")
    for field in (
        "commandPath",
        "minimumLineCount",
        "maximumBytes",
        "required_all",
        "required_any",
        "forbidden",
    ):
        if extracted[field] != manifest_gate[field]:
            fail("semantic-manifest-mismatch", f"content command and manifest differ at {field}")
    validate_deliverable(task_spec, manifest_gate, selected["cwd"])
    validate_prompt_literals(manifest, task_spec)

    protected_roots = [root]
    for candidate in extra_protected_roots:
        resolved = candidate.resolve()
        if not resolved.is_dir():
            fail("protected-root-unreadable", f"protected root is not a directory: {candidate}")
        if resolved not in protected_roots:
            protected_roots.append(resolved)
    reject_protected_references(selected["argv"], protected_roots)
    protected_before = snapshot_protected_roots(protected_roots)

    fixture_ids: set[str] = set()
    positive_count = 0
    for index, entry in enumerate(manifest["fixtures"]["positive"]):
        path, expected_reason = validate_fixture_entry(entry, f"fixtures.positive[{index}]", root)
        fixture_id = entry["id"]
        if fixture_id in fixture_ids:
            fail("manifest-shape-invalid", f"duplicate fixture id {fixture_id!r}")
        fixture_ids.add(fixture_id)
        if expected_reason is not None:
            fail("manifest-shape-invalid", "positive fixture cannot declare expectedReason")
        reason = semantic_reason(read_fixture_text(path), manifest_gate)
        if reason is not None:
            fail("positive-fixture-failed", f"{fixture_id} failed semantic rule {reason}")
        if run_command(selected, manifest_gate["deliverablePath"], path, protected_before) != 0:
            fail("positive-command-failed", f"{fixture_id} was rejected by the bound command")
        positive_count += 1

    observed_negative_reasons: set[str] = set()
    negative_count = 0
    for index, entry in enumerate(manifest["fixtures"]["negative"]):
        path, expected_reason = validate_fixture_entry(entry, f"fixtures.negative[{index}]", root)
        fixture_id = entry["id"]
        if fixture_id in fixture_ids:
            fail("manifest-shape-invalid", f"duplicate fixture id {fixture_id!r}")
        fixture_ids.add(fixture_id)
        if expected_reason not in BASE_NEGATIVE_REASONS:
            fail("negative-reason-invalid", f"{fixture_id} has unsupported expectedReason")
        reason = semantic_reason(read_fixture_text(path), manifest_gate)
        if reason != expected_reason:
            fail("negative-fixture-wrong-reason", f"{fixture_id} produced {reason!r}, expected {expected_reason!r}")
        if run_command(selected, manifest_gate["deliverablePath"], path, protected_before) == 0:
            fail("negative-command-passed", f"{fixture_id} was accepted by the bound command")
        observed_negative_reasons.add(expected_reason)
        negative_count += 1
    if observed_negative_reasons != BASE_NEGATIVE_REASONS:
        missing = sorted(BASE_NEGATIVE_REASONS - observed_negative_reasons)
        fail("negative-coverage-incomplete", f"missing negative fixture reasons: {', '.join(missing)}")

    return {
        "status": "pass",
        "commandId": selected["id"],
        "taskSpecDigest": manifest["taskSpecDigest"],
        "argvDigest": manifest["command"]["argvDigest"],
        "tupleDigest": manifest["command"]["tupleDigest"],
        "normalizer": manifest_gate["normalizer"],
        "positiveFixtures": positive_count,
        "negativeFixtures": negative_count,
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--manifest", required=True, type=Path)
    parser.add_argument("--task-spec", required=True, type=Path)
    parser.add_argument("--root", default=Path.cwd(), type=Path)
    parser.add_argument(
        "--schema",
        type=Path,
        default=Path(__file__).with_name("acceptance-semantic-manifest.schema.json"),
    )
    parser.add_argument("--protected-root", action="append", default=[], type=Path)
    arguments = parser.parse_args()
    try:
        result = validate(
            arguments.manifest,
            arguments.task_spec,
            arguments.root,
            arguments.schema,
            arguments.protected_root,
        )
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
    print(json.dumps(result, ensure_ascii=False, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
