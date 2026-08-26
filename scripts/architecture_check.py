#!/usr/bin/env python3
"""Fail closed when the domain package imports an implementation layer."""

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
    emit({"status": "pass", "reasonCode": "architecture-layer-check-pass"}, sys.stdout)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
