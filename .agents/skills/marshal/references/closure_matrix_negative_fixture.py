#!/usr/bin/env python3
"""Read one closure negative fixture and emit its deterministic failure receipt."""

from __future__ import annotations

import argparse
import json
from pathlib import Path


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input", required=True, type=Path)
    arguments = parser.parse_args()
    try:
        fixture = json.loads(arguments.input.read_text(encoding="utf-8"))
    except (OSError, UnicodeError, json.JSONDecodeError):
        print(json.dumps({"reasonCode": "fixture-invalid"}, sort_keys=True))
        return 2
    allowed = {"check", "observed", "expected", "reasonCode"}
    if not isinstance(fixture, dict) or set(fixture) != allowed:
        print(json.dumps({"reasonCode": "fixture-invalid"}, sort_keys=True))
        return 2
    check = fixture["check"]
    failed = False
    if check == "contains-all" and isinstance(fixture["observed"], list) and isinstance(fixture["expected"], list):
        failed = not set(fixture["expected"]).issubset(set(fixture["observed"]))
    elif check == "equals":
        failed = fixture["observed"] != fixture["expected"]
    else:
        print(json.dumps({"reasonCode": "fixture-invalid"}, sort_keys=True))
        return 2
    if not failed or not isinstance(fixture["reasonCode"], str) or not fixture["reasonCode"]:
        print(json.dumps({"reasonCode": "negative-fixture-did-not-fail"}, sort_keys=True))
        return 2
    print(json.dumps({"reasonCode": fixture["reasonCode"]}, sort_keys=True))
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
