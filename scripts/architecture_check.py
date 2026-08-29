#!/usr/bin/env python3
"""Fail closed on domain and staged production-composition inversions."""

from __future__ import annotations

import argparse
import json
from pathlib import Path
import subprocess
import sys


MODULE = "github.com/chiga0/marshal-harness"
DOMAIN_PACKAGE = f"{MODULE}/internal/domain"
INTERNAL_PREFIX = f"{MODULE}/internal/"
DOMAIN_INTERNAL_ALLOWLIST = {
    f"{MODULE}/internal/canonical",
    f"{MODULE}/internal/evidencebinding",
}

APPLICATION_PACKAGE = f"{MODULE}/internal/application"
PRODUCTION_RUNTIME_PACKAGE = f"{MODULE}/internal/productionruntime"
RUNSTORE_PACKAGE = f"{MODULE}/internal/runstore"
RESULT_INGRESS_PACKAGE = f"{MODULE}/internal/resultingress"
INPUT_ADAPTER_PACKAGES = {
    f"{MODULE}/cmd/marshal",
    f"{MODULE}/cmd/marshal-server",
    f"{MODULE}/internal/cli",
    f"{MODULE}/internal/server",
}
FORBIDDEN_PRODUCTION_DEPENDENCIES = {
    f"{MODULE}/internal/execution",
    f"{MODULE}/internal/planning",
    f"{MODULE}/internal/processcontrol",
    f"{MODULE}/internal/processsupervisor",
    f"{MODULE}/internal/resultingress",
    f"{MODULE}/internal/sandboxbridge",
}

# Staged cutover debt is frozen by exact root and first forbidden graph edge.
# Traversal stops at that edge, so wrappers cannot hide a new composition path.
# Entries may disappear without changing this checker; no new entry is allowed.
FROZEN_REACHABLE_DEPENDENCY_DEBT = {
    (root, source, target)
    for root, edges in {
        f"{MODULE}/cmd/marshal": {
            ("adapter/pi", "sandboxbridge"),
            ("app", "execution"),
            ("app", "sandboxbridge"),
            ("cli", "execution"),
            ("cli", "planning"),
            ("cli", "processsupervisor"),
            ("cli", "sandboxbridge"),
            ("control", "planning"),
            ("planpremortem", "planning"),
            ("resultbinding", "resultingress"),
            ("runstore", "resultingress"),
        },
        f"{MODULE}/cmd/marshal-server": {
            ("adapter/pi", "sandboxbridge"),
            ("app", "execution"),
            ("app", "sandboxbridge"),
            ("control", "planning"),
            ("server", "planning"),
            ("runstore", "resultingress"),
        },
        f"{MODULE}/internal/cli": {
            ("adapter/pi", "sandboxbridge"),
            ("app", "execution"),
            ("app", "sandboxbridge"),
            ("cli", "execution"),
            ("cli", "planning"),
            ("cli", "processsupervisor"),
            ("cli", "sandboxbridge"),
            ("control", "planning"),
            ("planpremortem", "planning"),
            ("resultbinding", "resultingress"),
            ("runstore", "resultingress"),
        },
        f"{MODULE}/internal/server": {
            ("adapter/pi", "sandboxbridge"),
            ("app", "execution"),
            ("app", "sandboxbridge"),
            ("control", "planning"),
            ("server", "planning"),
            ("runstore", "resultingress"),
        },
    }.items()
    for source, target in edges
    for source, target in {
        (f"{MODULE}/internal/{source}", f"{MODULE}/internal/{target}")
    }
}

# These maxima only preserve already-shipped selector debt while the next
# vertical slice removes it. Occurrences cannot move files or increase.
FROZEN_SELECTOR_DEBT = {
    ("internal/app/sandbox.go", "MARSHAL_EMBEDDED_SANDBOX"): 1,
    ("internal/cli/cli.go", "MARSHAL_WORKER_EXECUTOR"): 3,
    ("cmd/marshal-server/main.go", "MARSHAL_EMBEDDED_SANDBOX"): 1,
    ("cmd/marshal-server/main.go", "MARSHAL_PRODUCTION_GATE"): 1,
}

LEGACY_SELECTORS = {
    "MARSHAL_EMBEDDED_SANDBOX",
    "MARSHAL_WORKER_EXECUTOR",
    "MARSHAL_PRODUCTION_GATE",
}

FROZEN_SERVER_PROCESS_SPAWNS = {
    (
        "cmd/marshal-server/main.go",
        'i:exec|p:.|i:CommandContext|p:(|i:ctx|p:,|i:executable|p:.|i:Path|p:,|s:"task"|p:,|s:"run"|p:,|s:"--run"|p:,|i:runID|p:,|s:"--json"|p:)',
    ): 1,
    (
        "cmd/marshal-server/main.go",
        'i:exec|p:.|i:CommandContext|p:(|i:ctx|p:,|i:executable|p:,|s:"version"|p:,|s:"--json"|p:)',
    ): 1,
    (
        "cmd/marshal-server/main.go",
        'i:exec|p:.|i:Command|p:(|s:"/usr/bin/git"|p:,|s:"-C"|p:,|i:repositoryRoot|p:,|s:"rev-parse"|p:,|s:"--verify"|p:,|s:"HEAD"|p:)',
    ): 1,
}

PROCESS_SPAWN_CALLEES = {
    ("exec", ".", "Command"),
    ("exec", ".", "CommandContext"),
    ("os", ".", "StartProcess"),
    ("syscall", ".", "ForkExec"),
    ("unix", ".", "ForkExec"),
}
PROCESS_SPAWN_IMPORTS = {
    "os/exec": {"Command", "CommandContext"},
    "os": {"StartProcess"},
    "syscall": {"ForkExec"},
    "golang.org/x/sys/unix": {"ForkExec"},
}

PRIVATE_MUTATION_CALLS = {
    "claimRuntime",
    "newController",
    "prepareRunStart",
    "startPreparedRun",
}
EXPORTED_BYPASS_SYMBOLS = {
    "Controller",
    "NewController",
    "OpenRepositoryOwnerLock",
    "RepositoryOwnerLock",
    "ClaimRuntime",
}


def architecture_layer_inversions(package: dict[str, object]) -> list[str]:
    module = package.get("Module")
    if (
        package.get("ImportPath") != DOMAIN_PACKAGE
        or not isinstance(module, dict)
        or module.get("Path") != MODULE
    ):
        raise ValueError("domain package identity is invalid")
    imports = package.get("Imports")
    dependencies = package.get("Deps")
    if (
        not isinstance(imports, list)
        or not all(isinstance(item, str) for item in imports)
        or not isinstance(dependencies, list)
        or not all(isinstance(item, str) for item in dependencies)
    ):
        raise ValueError("domain dependency graph is invalid")
    return sorted(
        dependency
        for dependency in set(imports + dependencies)
        if dependency.startswith(INTERNAL_PREFIX)
        and dependency not in DOMAIN_INTERNAL_ALLOWLIST
    )


def production_dependency_inversions(packages: list[dict[str, object]]) -> list[str]:
    graph: dict[str, set[str]] = {}
    for package in packages:
        import_path = package.get("ImportPath")
        module = package.get("Module")
        imports = package.get("Imports", [])
        if (
            not isinstance(import_path, str)
            or not isinstance(module, dict)
            or module.get("Path") != MODULE
            or not isinstance(imports, list)
            or not all(isinstance(item, str) for item in imports)
        ):
            raise ValueError("production package identity is invalid")
        graph[import_path] = set(imports)
    required = INPUT_ADAPTER_PACKAGES | {APPLICATION_PACKAGE, PRODUCTION_RUNTIME_PACKAGE}
    if not required.issubset(graph):
        raise ValueError("production package set is incomplete")

    violations: list[str] = []
    for import_path in sorted(graph):
        if import_path.startswith(f"{MODULE}/cmd/") and import_path not in INPUT_ADAPTER_PACKAGES:
            violations.append(f"unexpected-production-root:{import_path}")
    for root in sorted(required):
        queue = [root]
        visited = {root}
        while queue:
            source = queue.pop(0)
            for dependency in sorted(graph.get(source, set())):
                if dependency in FORBIDDEN_PRODUCTION_DEPENDENCIES:
                    edge = (root, source, dependency)
                    if source == PRODUCTION_RUNTIME_PACKAGE and dependency == RESULT_INGRESS_PACKAGE:
                        pass
                    elif root == PRODUCTION_RUNTIME_PACKAGE and source == RUNSTORE_PACKAGE and dependency == RESULT_INGRESS_PACKAGE:
                        pass
                    elif root in INPUT_ADAPTER_PACKAGES:
                        if edge not in FROZEN_REACHABLE_DEPENDENCY_DEBT:
                            violations.append(f"{root}:{source}->{dependency}")
                    elif root == APPLICATION_PACKAGE:
                        violations.append(f"{root}:{source}->{dependency}")
                    else:
                        violations.append(f"{root}:{source}->{dependency}")
                    continue
                if dependency.startswith(f"{MODULE}/") and dependency not in visited:
                    visited.add(dependency)
                    queue.append(dependency)
    return sorted(violations)


def go_tokens(text: str) -> list[tuple[str, str]]:
    """Return identifiers, decoded strings and punctuation, excluding comments."""
    tokens: list[tuple[str, str]] = []
    index = 0
    while index < len(text):
        char = text[index]
        if char.isspace():
            index += 1
            continue
        if text.startswith("//", index):
            newline = text.find("\n", index + 2)
            index = len(text) if newline < 0 else newline + 1
            continue
        if text.startswith("/*", index):
            end = text.find("*/", index + 2)
            index = len(text) if end < 0 else end + 2
            continue
        if char in {'"', "'", "`"}:
            quote = char
            start = index
            index += 1
            escaped = False
            while index < len(text):
                current = text[index]
                if quote != "`" and escaped:
                    escaped = False
                elif quote != "`" and current == "\\":
                    escaped = True
                elif current == quote:
                    index += 1
                    break
                index += 1
            raw = text[start:index]
            if quote == '"':
                try:
                    value = json.loads(raw)
                except json.JSONDecodeError:
                    value = raw[1:-1]
                tokens.append(("string", value))
            elif quote == "`":
                tokens.append(("string", raw[1:-1]))
            continue
        if char.isalpha() or char == "_":
            end = index + 1
            while end < len(text) and (text[end].isalnum() or text[end] == "_"):
                end += 1
            tokens.append(("ident", text[index:end]))
            index = end
            continue
        tokens.append(("punct", char))
        index += 1
    return tokens


def has_token_sequence(tokens: list[tuple[str, str]], values: tuple[str, ...]) -> bool:
    token_values = [value for _, value in tokens]
    width = len(values)
    return any(tuple(token_values[index:index + width]) == values for index in range(len(token_values) - width + 1))


def has_function_call(tokens: list[tuple[str, str]], symbol: str) -> bool:
    values = [value for _, value in tokens]
    for index in range(len(values) - 1):
        if values[index:index + 2] != [symbol, "("]:
            continue
        if index > 0 and values[index - 1] == "func":
            continue
        return True
    return False


def exported_receiver_methods(tokens: list[tuple[str, str]]) -> list[tuple[str, str]]:
    values = [value for _, value in tokens]
    methods: list[tuple[str, str]] = []
    for index in range(len(values) - 4):
        if values[index:index + 2] != ["func", "("]:
            continue
        close = index + 2
        while close < len(values) and values[close] != ")":
            close += 1
        if close + 1 >= len(values) or values[close] != ")":
            continue
        receiver_identifiers = [
            value for kind, value in tokens[index + 2:close] if kind == "ident"
        ]
        method = values[close + 1]
        if receiver_identifiers and method[:1].isupper():
            methods.append((receiver_identifiers[-1], method))
    return methods


def encoded_token(token: tuple[str, str]) -> str:
    kind, value = token
    if kind == "string":
        return "s:" + json.dumps(value, separators=(",", ":"), ensure_ascii=True)
    return ("i:" if kind == "ident" else "p:") + value


def process_spawn_callees(tokens: list[tuple[str, str]]) -> set[tuple[str, ...]]:
    callees = set(PROCESS_SPAWN_CALLEES)
    for index, (kind, path) in enumerate(tokens):
        functions = PROCESS_SPAWN_IMPORTS.get(path) if kind == "string" else None
        if not functions:
            continue
        alias = path.rsplit("/", 1)[-1]
        if index > 0 and tokens[index - 1][0] == "ident" and tokens[index - 1][1] != "import":
            alias = tokens[index - 1][1]
        elif index > 0 and tokens[index - 1] == ("punct", "."):
            alias = "."
        if alias == "_":
            continue
        for function in functions:
            callees.add((function,) if alias == "." else (alias, ".", function))
    return callees


def process_spawn_fingerprints(tokens: list[tuple[str, str]]) -> list[str]:
    values = [value for _, value in tokens]
    fingerprints: list[str] = []
    for callee in process_spawn_callees(tokens):
        width = len(callee)
        for index in range(len(tokens) - width):
            if tuple(values[index:index + width]) != callee or values[index + width] != "(":
                continue
            depth = 0
            end = index + width
            while end < len(tokens):
                if values[end] == "(":
                    depth += 1
                elif values[end] == ")":
                    depth -= 1
                    if depth == 0:
                        end += 1
                        break
                end += 1
            if depth != 0:
                fingerprints.append("unclosed-spawn-call")
                continue
            fingerprints.append("|".join(encoded_token(token) for token in tokens[index:end]))
    return fingerprints


def matching_delimiter(
    tokens: list[tuple[str, str]], start: int, opening: str, closing: str
) -> int | None:
    values = [value for _, value in tokens]
    if start >= len(values) or values[start] != opening:
        return None
    depth = 0
    for index in range(start, len(values)):
        if values[index] == opening:
            depth += 1
        elif values[index] == closing:
            depth -= 1
            if depth == 0:
                return index
    return None


def go_function_nodes(tokens: list[tuple[str, str]]) -> list[dict[str, object]]:
    """Build the narrow function AST needed by the production shape gates."""
    values = [value for _, value in tokens]
    functions: list[dict[str, object]] = []
    for index, value in enumerate(values):
        if value != "func" or index + 1 >= len(values):
            continue
        cursor = index + 1
        receiver = ""
        if values[cursor] == "(":
            close = matching_delimiter(tokens, cursor, "(", ")")
            if close is None:
                continue
            identifiers = [
                token_value
                for kind, token_value in tokens[cursor + 1:close]
                if kind == "ident"
            ]
            if not identifiers:
                # Anonymous functions have no receiver or declaration name.
                continue
            receiver = identifiers[-1]
            cursor = close + 1
        if cursor >= len(tokens) or tokens[cursor][0] != "ident":
            continue
        name = values[cursor]
        body_start = cursor + 1
        while body_start < len(values) and values[body_start] != "{":
            body_start += 1
        if body_start == len(values):
            continue
        body_end = matching_delimiter(tokens, body_start, "{", "}")
        if body_end is None:
            continue
        functions.append(
            {
                "receiver": receiver,
                "name": name,
                "tokens": tokens[index:body_end + 1],
            }
        )
    return functions


def method_calls(
    tokens: list[tuple[str, str]], method: str
) -> list[tuple[str, list[list[str]]]]:
    values = [value for _, value in tokens]
    calls: list[tuple[str, list[list[str]]]] = []
    for index in range(2, len(values) - 1):
        if values[index - 1:index + 2] != [".", method, "("]:
            continue
        receiver_index = index - 2
        receiver_parts = [values[receiver_index]]
        while receiver_index >= 2 and values[receiver_index - 1] == ".":
            receiver_index -= 2
            receiver_parts[0:0] = [values[receiver_index], "."]
            if tokens[receiver_index][0] != "ident":
                receiver_parts[0] = "<complex:" + receiver_parts[0] + ">"
                break
        receiver = "".join(receiver_parts)
        end = matching_delimiter(tokens, index + 1, "(", ")")
        if end is None:
            calls.append((receiver, [["<unclosed>"]]))
            continue
        arguments: list[list[str]] = []
        current: list[str] = []
        parens = brackets = braces = 0
        for value in values[index + 2:end]:
            if value == "(":
                parens += 1
            elif value == ")":
                parens -= 1
            elif value == "[":
                brackets += 1
            elif value == "]":
                brackets -= 1
            elif value == "{":
                braces += 1
            elif value == "}":
                braces -= 1
            if value == "," and parens == brackets == braces == 0:
                arguments.append(current)
                current = []
            else:
                current.append(value)
        if current or arguments:
            arguments.append(current)
        calls.append((receiver, arguments))
    return calls


def sequence_count(tokens: list[tuple[str, str]], sequence: tuple[str, ...]) -> int:
    values = [value for _, value in tokens]
    width = len(sequence)
    return sum(
        tuple(values[index:index + width]) == sequence
        for index in range(len(values) - width + 1)
    )


def provisional_owner_ast_inversions(root: Path) -> list[str]:
    """Freeze ADR 0066's one-shot provisional verifier as a closed Go AST shape."""
    source = root / "internal/productionruntime/owner_lock_darwin.go"
    if not source.is_file():
        return ["owner-provisional-ast:missing-source"]
    tokens = go_tokens(source.read_text(encoding="utf-8"))
    functions = go_function_nodes(tokens)
    acquire = [
        node for node in functions
        if node["receiver"] == "darwinRepositoryOwnerScopeLock"
        and node["name"] == "acquireOwner"
    ]
    verifier_method = [
        node for node in functions
        if node["receiver"] == "darwinProvisionalOwnerVerifier"
        and node["name"] == "WithCurrentOwnerLock"
    ]
    violations: list[str] = []
    type_occurrences = sum(
        1
        for kind, value in tokens
        if kind == "ident" and value == "darwinProvisionalOwnerVerifier"
    )
    if type_occurrences != 3 or len(verifier_method) != 1:
        violations.append("owner-provisional-ast:type-shape")
    constructor = (
        "verifier", ":", "=", "&", "darwinProvisionalOwnerVerifier", "{",
        "candidate", ":", "candidate", "}",
    )
    if sequence_count(tokens, constructor) != 1 or len(acquire) != 1 or sequence_count(acquire[0]["tokens"], constructor) != 1:
        violations.append("owner-provisional-ast:constructor-shape")
    all_calls: list[tuple[str, list[list[str]]]] = []
    production_root = root / "internal/productionruntime"
    for candidate_source in sorted(production_root.glob("*.go")):
        if candidate_source.name.endswith("_test.go"):
            continue
        all_calls.extend(method_calls(go_tokens(candidate_source.read_text(encoding="utf-8")), "AcquireOwner"))
    expected_call = (
        "store",
        [["ctx"], ["verifier"], ["expectedEpoch"], ["expectedFactDigest"], ["candidate"]],
    )
    acquire_calls = method_calls(acquire[0]["tokens"], "AcquireOwner") if len(acquire) == 1 else []
    if all_calls != [expected_call] or acquire_calls != [expected_call]:
        violations.append("owner-provisional-ast:acquire-call-shape")
    if len(acquire) != 1 or sum(
        1
        for kind, value in acquire[0]["tokens"]
        if kind == "ident" and value == "verifier"
    ) != 2:
        violations.append("owner-provisional-ast:verifier-escape")
    return sorted(set(violations))


def production_source_inversions(root: Path) -> list[str]:
    occurrences: dict[tuple[str, str], int] = {}
    process_spawns: dict[tuple[str, str], int] = {}
    source_roots = [root / "cmd", root / "internal"]
    for source in sorted(
        source for source_root in source_roots if source_root.is_dir()
        for source in source_root.rglob("*.go")
    ):
        if source.name.endswith("_test.go"):
            continue
        relative = source.relative_to(root).as_posix()
        tokens = go_tokens(source.read_text(encoding="utf-8"))
        for selector in LEGACY_SELECTORS:
            count = sum(1 for kind, value in tokens if kind == "string" and value == selector)
            if count:
                occurrences[(relative, selector)] = count
        if relative.startswith(("cmd/marshal-server/", "internal/server/")):
            for fingerprint in process_spawn_fingerprints(tokens):
                key = (relative, fingerprint)
                process_spawns[key] = process_spawns.get(key, 0) + 1

        if relative.startswith("internal/productionruntime/"):
            for symbol in EXPORTED_BYPASS_SYMBOLS:
                if has_token_sequence(tokens, ("type", symbol)) or has_token_sequence(tokens, ("func", symbol)):
                    occurrences[(relative, f"exported-bypass:{symbol}")] = 1
            if relative != "internal/productionruntime/runtime.go":
                for symbol in PRIVATE_MUTATION_CALLS:
                    if has_token_sequence(tokens, (".", symbol)) or symbol == "newController" and has_function_call(tokens, symbol):
                        occurrences[(relative, f"private-mutation:{symbol}")] = 1
            for receiver, method in exported_receiver_methods(tokens):
                if receiver == "controller" or receiver.endswith("RepositoryOwnerLock") and method not in {"Close", "WithCurrentOwnerLock"}:
                    occurrences[(relative, f"exported-receiver:{receiver}.{method}")] = 1
        else:
            for symbol in EXPORTED_BYPASS_SYMBOLS:
                if has_token_sequence(tokens, (".", symbol)) or has_function_call(tokens, symbol):
                    occurrences[(relative, f"external-bypass:{symbol}")] = 1

    violations: list[str] = []
    for key, count in occurrences.items():
        if key[1] in LEGACY_SELECTORS:
            if count > FROZEN_SELECTOR_DEBT.get(key, 0):
                violations.append(f"legacy-selector:{key[0]}:{key[1]}")
        else:
            violations.append(f"production-runtime-bypass:{key[0]}:{key[1]}")
    for key, count in process_spawns.items():
        if count > FROZEN_SERVER_PROCESS_SPAWNS.get(key, 0):
            violations.append(f"server-process-spawn:{key[0]}:{key[1]}")
    return sorted(violations)


def domain_package(root: Path, go: str) -> dict[str, object]:
    completed = subprocess.run(
        [go, "list", "-json", "./internal/domain"],
        cwd=root,
        check=False,
        capture_output=True,
        text=True,
        timeout=30,
    )
    if completed.returncode != 0:
        raise RuntimeError("go-list-failed")
    value = json.loads(completed.stdout)
    if not isinstance(value, dict):
        raise RuntimeError("go-list-output-invalid")
    return value


def production_packages(root: Path, go: str) -> list[dict[str, object]]:
    completed = subprocess.run(
        [
            go,
            "list",
            "-deps",
            "-json",
            "./cmd/...",
            "./internal/...",
        ],
        cwd=root,
        check=False,
        capture_output=True,
        text=True,
        timeout=30,
    )
    if completed.returncode != 0:
        raise RuntimeError("go-list-failed")
    decoder = json.JSONDecoder()
    packages: list[dict[str, object]] = []
    index = 0
    while index < len(completed.stdout):
        while index < len(completed.stdout) and completed.stdout[index].isspace():
            index += 1
        if index == len(completed.stdout):
            break
        value, index = decoder.raw_decode(completed.stdout, index)
        if not isinstance(value, dict):
            raise RuntimeError("go-list-output-invalid")
        module = value.get("Module")
        if isinstance(module, dict) and module.get("Path") == MODULE:
            packages.append(value)
    return packages


def emit(payload: dict[str, object], stream) -> None:
    print(json.dumps(payload, separators=(",", ":"), sort_keys=True), file=stream)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parents[1])
    parser.add_argument("--go", default="go")
    arguments = parser.parse_args()
    try:
        root = arguments.root.resolve(strict=True)
        package = domain_package(root, arguments.go)
        inversions = architecture_layer_inversions(package)
        production_inversions = production_dependency_inversions(
            production_packages(root, arguments.go)
        ) + production_source_inversions(root) + provisional_owner_ast_inversions(root)
    except (OSError, RuntimeError, ValueError, json.JSONDecodeError, subprocess.TimeoutExpired):
        emit({"status": "fail", "reasonCode": "architecture-check-unavailable"}, sys.stderr)
        return 1
    if inversions:
        emit(
            {
                "status": "fail",
                "reasonCode": "architecture-layer-inversion",
                "package": DOMAIN_PACKAGE,
                "imports": inversions,
            },
            sys.stderr,
        )
        return 1
    if production_inversions:
        emit(
            {
                "status": "fail",
                "reasonCode": "production-composition-inversion",
                "violations": sorted(production_inversions),
            },
            sys.stderr,
        )
        return 1
    emit({"status": "pass", "reasonCode": "architecture-layer-check-pass"}, sys.stdout)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
