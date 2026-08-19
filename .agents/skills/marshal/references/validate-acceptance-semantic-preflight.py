#!/usr/bin/env python3
"""Fail-closed operator-local preflight for content acceptance commands."""

from __future__ import annotations

import argparse
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
RULE_REF_RE = re.compile(r"^required_all\[([0-9]+)\]$")
REQUIRED_NEGATIVE_REASONS = {
    "missing-required-all",
    "missing-required-any",
    "forbidden-present",
}
NORMALIZERS = {"nfkc-casefold", "markdown-backtick-strip+nfkc-casefold"}


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


def relative_path(root: Path, value: object, label: str) -> Path:
    raw = require_nonempty_string(value, label)
    candidate = Path(raw)
    if candidate.is_absolute() or ".." in candidate.parts or candidate == Path("."):
        fail("path-boundary-invalid", f"{label} must be a clean relative path")
    resolved = (root / candidate).resolve()
    try:
        resolved.relative_to(root)
    except ValueError:
        fail("path-boundary-invalid", f"{label} escapes the declared root")
    return resolved


def normalize(text: str, normalizer: str) -> str:
    if normalizer == "markdown-backtick-strip+nfkc-casefold":
        text = text.replace("`", "")
    return unicodedata.normalize("NFKC", text).casefold()


def semantic_reason(document: str, manifest: dict) -> str | None:
    normalized = normalize(document, manifest["normalizer"])
    for token in manifest["required_all"]:
        if normalize(token, manifest["normalizer"]) not in normalized:
            return "missing-required-all"
    for alternatives in manifest["required_any"]:
        if not any(
            normalize(token, manifest["normalizer"]) in normalized
            for token in alternatives
        ):
            return "missing-required-any"
    for token in manifest["forbidden"]:
        if normalize(token, manifest["normalizer"]) in normalized:
            return "forbidden-present"
    return None


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
                fail("fixture-type-invalid", f"unsupported fixture entry {path}")
    except (OSError, UnicodeError) as error:
        fail("fixture-tree-unreadable", f"cannot hash isolated fixture tree: {error}")
    return "sha256:" + hasher.hexdigest()


def run_command(argv: list[str], deliverable_path: str, fixture: Path) -> int:
    with tempfile.TemporaryDirectory(prefix="marshal-acceptance-preflight-") as directory:
        sandbox = Path(directory)
        target = sandbox / deliverable_path
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(fixture, target)
        environment = {
            "PATH": os.environ.get("PATH", ""),
            "HOME": str(sandbox / ".home"),
            "TMPDIR": str(sandbox / ".tmp"),
            "PYTHONDONTWRITEBYTECODE": "1",
        }
        (sandbox / ".home").mkdir()
        (sandbox / ".tmp").mkdir()
        before = tree_digest(sandbox)
        try:
            completed = subprocess.run(
                argv,
                cwd=sandbox,
                env=environment,
                stdin=subprocess.DEVNULL,
                stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL,
                timeout=15,
                check=False,
            )
        except (OSError, subprocess.TimeoutExpired) as error:
            fail("command-execution-failed", f"acceptance command could not finish: {error}")
        after = tree_digest(sandbox)
        if before != after:
            fail("command-mutated-fixture", "acceptance command changed its isolated fixture tree")
        return completed.returncode


def validate_manifest_shape(manifest: dict) -> None:
    required = {
        "manifestVersion",
        "taskSpecDigest",
        "command",
        "deliverablePath",
        "normalizer",
        "required_all",
        "required_any",
        "forbidden",
        "prompt_literals",
        "fixtures",
    }
    require_keys(manifest, required, required, "manifest")
    if manifest["manifestVersion"] != "marshal-operator-acceptance-preflight/v1":
        fail("manifest-shape-invalid", "unsupported manifestVersion")
    require_digest(manifest["taskSpecDigest"], "taskSpecDigest")
    require_keys(
        manifest["command"], {"id", "argvDigest"}, {"id", "argvDigest"}, "command"
    )
    require_nonempty_string(manifest["command"]["id"], "command.id")
    require_digest(manifest["command"]["argvDigest"], "command.argvDigest")
    require_nonempty_string(manifest["deliverablePath"], "deliverablePath")
    if manifest["normalizer"] not in NORMALIZERS:
        fail("manifest-shape-invalid", "normalizer is not a closed supported value")

    for field in ("required_all", "required_any", "forbidden", "prompt_literals"):
        if not isinstance(manifest[field], list):
            fail("manifest-shape-invalid", f"{field} must be an array")
    if not manifest["required_all"] or not manifest["required_any"] or not manifest["forbidden"]:
        fail("manifest-shape-invalid", "semantic rule arrays must be non-empty")
    for index, token in enumerate(manifest["required_all"]):
        require_nonempty_string(token, f"required_all[{index}]")
    for index, alternatives in enumerate(manifest["required_any"]):
        if not isinstance(alternatives, list) or len(alternatives) < 2:
            fail(
                "required-any-not-equivalent",
                f"required_any[{index}] must enumerate at least two equivalent forms",
            )
        normalized = []
        for alternative_index, token in enumerate(alternatives):
            require_nonempty_string(token, f"required_any[{index}][{alternative_index}]")
            normalized.append(normalize(token, manifest["normalizer"]))
        if len(set(normalized)) != len(normalized):
            fail("required-any-not-equivalent", f"required_any[{index}] contains duplicates")
    for index, token in enumerate(manifest["forbidden"]):
        require_nonempty_string(token, f"forbidden[{index}]")

    fixtures = manifest["fixtures"]
    if not isinstance(fixtures, dict):
        fail("manifest-shape-invalid", "fixtures must be an object")
    require_keys(fixtures, {"positive", "negative"}, {"positive", "negative"}, "fixtures")
    if not isinstance(fixtures["positive"], list) or not fixtures["positive"]:
        fail("manifest-shape-invalid", "fixtures.positive must be a non-empty array")
    if not isinstance(fixtures["negative"], list) or not fixtures["negative"]:
        fail("manifest-shape-invalid", "fixtures.negative must be a non-empty array")


def validate_prompt_literals(manifest: dict, task_spec: dict) -> None:
    mappings: dict[int, str] = {}
    for index, mapping in enumerate(manifest["prompt_literals"]):
        if not isinstance(mapping, dict):
            fail("prompt-literal-unmapped", f"prompt_literals[{index}] must be an object")
        require_keys(mapping, {"rule", "literal"}, {"rule", "literal"}, f"prompt_literals[{index}]")
        match = RULE_REF_RE.fullmatch(require_nonempty_string(mapping["rule"], "prompt literal rule"))
        if match is None:
            fail("prompt-literal-unmapped", f"invalid prompt literal rule {mapping['rule']!r}")
        rule_index = int(match.group(1))
        if rule_index >= len(manifest["required_all"]):
            fail("prompt-literal-unmapped", f"prompt literal rule index {rule_index} is out of range")
        literal = require_nonempty_string(mapping["literal"], "prompt literal")
        if literal != manifest["required_all"][rule_index] or rule_index in mappings:
            fail("prompt-literal-unmapped", f"prompt literal does not uniquely match required_all[{rule_index}]")
        mappings[rule_index] = literal
    if set(mappings) != set(range(len(manifest["required_all"]))):
        fail("prompt-literal-unmapped", "every required_all token needs one exact prompt mapping")

    work = task_spec.get("work")
    contexts = work.get("context") if isinstance(work, dict) else None
    if not isinstance(contexts, list) or not all(isinstance(item, str) for item in contexts):
        fail("task-spec-shape-invalid", "TaskSpec work.context must be an array of strings")
    prompt = "\n".join(contexts)
    for literal in mappings.values():
        marker = f"逐字包含 `{literal}`"
        if marker not in prompt:
            fail("prompt-literal-unmapped", f"TaskSpec prompt lacks exact marker {marker!r}")


def find_command(task_spec: dict, command_id: str) -> list[str]:
    acceptance = task_spec.get("acceptance")
    commands = acceptance.get("commands") if isinstance(acceptance, dict) else None
    if not isinstance(commands, list):
        fail("task-spec-shape-invalid", "TaskSpec acceptance.commands must be an array")
    matches = [command for command in commands if isinstance(command, dict) and command.get("id") == command_id]
    if len(matches) != 1:
        fail("command-binding-invalid", "command.id must select exactly one acceptance command")
    argv = matches[0].get("argv")
    if not isinstance(argv, list) or not argv or not all(isinstance(item, str) and item for item in argv):
        fail("command-binding-invalid", "selected command argv must be a non-empty string array")
    return argv


def validate_deliverable(task_spec: dict, deliverable_path: str) -> None:
    if Path(deliverable_path).is_absolute() or ".." in Path(deliverable_path).parts:
        fail("path-boundary-invalid", "deliverablePath must be a clean relative path")
    deliverables = task_spec.get("deliverables")
    if not isinstance(deliverables, list):
        fail("task-spec-shape-invalid", "TaskSpec deliverables must be an array")
    globs = [item.get("pathGlob") for item in deliverables if isinstance(item, dict)]
    if not any(isinstance(pattern, str) and fnmatch.fnmatchcase(deliverable_path, pattern) for pattern in globs):
        fail("deliverable-binding-invalid", "deliverablePath does not match a TaskSpec deliverable")


def validate_fixture_entry(entry: object, label: str, root: Path) -> tuple[Path, str | None]:
    if not isinstance(entry, dict):
        fail("manifest-shape-invalid", f"{label} must be an object")
    required = {"id", "path", "digest"}
    allowed = required | {"expectedReason"}
    require_keys(entry, required, allowed, label)
    require_nonempty_string(entry["id"], f"{label}.id")
    path = relative_path(root, entry["path"], f"{label}.path")
    if not path.is_file() or path.is_symlink():
        fail("fixture-unreadable", f"{label}.path must be a regular non-symlink file")
    expected_digest = require_digest(entry["digest"], f"{label}.digest")
    if file_digest(path) != expected_digest:
        fail("fixture-digest-mismatch", f"{label} digest does not match bytes")
    expected_reason = entry.get("expectedReason")
    if expected_reason is not None:
        require_nonempty_string(expected_reason, f"{label}.expectedReason")
    return path, expected_reason


def validate(manifest_path: Path, task_spec_path: Path, root: Path) -> dict:
    root = root.resolve()
    manifest = load_json(manifest_path, "manifest")
    task_spec = load_json(task_spec_path, "TaskSpec")
    validate_manifest_shape(manifest)

    if file_digest(task_spec_path) != manifest["taskSpecDigest"]:
        fail("task-spec-digest-mismatch", "taskSpecDigest does not match TaskSpec bytes")
    argv = find_command(task_spec, manifest["command"]["id"])
    if canonical_digest(argv) != manifest["command"]["argvDigest"]:
        fail("argv-digest-mismatch", "command.argvDigest does not match canonical argv")
    validate_deliverable(task_spec, manifest["deliverablePath"])
    validate_prompt_literals(manifest, task_spec)

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
        document = read_fixture_text(path)
        reason = semantic_reason(document, manifest)
        if reason is not None:
            fail("positive-fixture-failed", f"{fixture_id} failed semantic rule {reason}")
        if run_command(argv, manifest["deliverablePath"], path) != 0:
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
        if expected_reason not in REQUIRED_NEGATIVE_REASONS:
            fail("negative-reason-invalid", f"{fixture_id} has unsupported expectedReason")
        document = read_fixture_text(path)
        reason = semantic_reason(document, manifest)
        if reason != expected_reason:
            fail(
                "negative-fixture-wrong-reason",
                f"{fixture_id} produced {reason!r}, expected {expected_reason!r}",
            )
        if run_command(argv, manifest["deliverablePath"], path) == 0:
            fail("negative-command-passed", f"{fixture_id} was accepted by the bound command")
        observed_negative_reasons.add(expected_reason)
        negative_count += 1
    if observed_negative_reasons != REQUIRED_NEGATIVE_REASONS:
        missing = sorted(REQUIRED_NEGATIVE_REASONS - observed_negative_reasons)
        fail("negative-coverage-incomplete", f"missing negative fixture reasons: {', '.join(missing)}")

    return {
        "status": "pass",
        "commandId": manifest["command"]["id"],
        "taskSpecDigest": manifest["taskSpecDigest"],
        "argvDigest": manifest["command"]["argvDigest"],
        "normalizer": manifest["normalizer"],
        "positiveFixtures": positive_count,
        "negativeFixtures": negative_count,
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--manifest", required=True, type=Path)
    parser.add_argument("--task-spec", required=True, type=Path)
    parser.add_argument("--root", default=Path.cwd(), type=Path)
    arguments = parser.parse_args()
    try:
        result = validate(arguments.manifest, arguments.task_spec, arguments.root)
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
