#!/usr/bin/env python3
"""只读业务对照统计；不签发验收、修改 Run 或决定发布。"""

import argparse
import json
import math
import statistics
from pathlib import Path


ARMS = ("lead-subagents", "marshal")


def number(value):
    return type(value) in (int, float) and math.isfinite(value) and value >= 0


def summarize(plan, records):
    """plan 预先列全 pair；records 每行覆盖一次业务交付的全部尝试。"""
    pairs = plan["pairs"]
    if not pairs or len({p["id"] for p in pairs}) != len(pairs):
        raise ValueError("pairs must be nonempty and unique")
    expected = {}
    for pair in pairs:
        for field in ("id", "family", "conditionsRef", "acceptanceRef"):
            if not isinstance(pair[field], str) or not pair[field].strip():
                raise ValueError("empty pair identity or frozen reference")
        expected[pair["id"]] = pair
    indexed = {}
    for row in records:
        key = (row["pairId"], row["arm"])
        if key[0] not in expected or key[1] not in ARMS or key in indexed:
            raise ValueError("unknown or duplicate pair/arm")
        for field in ("conditionsRef", "acceptanceRef"):
            if row[field] != expected[key[0]][field]:
                raise ValueError("unmatched frozen conditions or acceptance")
        if type(row["accepted"]) is not bool:
            raise ValueError("accepted must be an independently checked boolean")
        if not isinstance(row.get("evidenceRefs"), list) or not row["evidenceRefs"] or not all(
            isinstance(ref, str) and ref.strip() for ref in row["evidenceRefs"]
        ):
            raise ValueError("evidence references required, including failures")
        for field in ("elapsedSeconds", "humanSeconds", "attempts", "reworkCycles", "tokens"):
            value = row[field]
            if field == "elapsedSeconds" and not number(value):
                raise ValueError("elapsedSeconds required for every completed observation")
            if value is not None and not number(value):
                raise ValueError("metrics must be nonnegative finite numbers or null")
            if field in ("attempts", "reworkCycles", "tokens") and value is not None and type(value) is not int:
                raise ValueError("counts must be integers")
        indexed[key] = row
    result = {"kind": "descriptive-only-not-release-evidence", "plannedPairs": len(pairs),
              "missing": [], "arms": {}, "paired": {}}
    for arm in ARMS:
        rows = [r for (pair_id, a), r in indexed.items() if a == arm]
        result["missing"].extend({"pairId": p["id"], "arm": arm}
                                 for p in pairs if (p["id"], arm) not in indexed)
        metrics = {}
        for field in ("elapsedSeconds", "humanSeconds", "attempts", "reworkCycles", "tokens"):
            values = [r[field] for r in rows if r[field] is not None]
            metrics[field] = {"known": len(values), "unknown": len(rows) - len(values),
                              "sumKnown": sum(values) if values else None}
        accepted = sum(r["accepted"] for r in rows)
        result["arms"][arm] = {"observed": len(rows), "accepted": accepted,
                                "notAccepted": len(rows) - accepted,
                                "observedAcceptanceRate": accepted / len(rows) if rows else None,
                                "metrics": metrics}
    for family in sorted({p["family"] for p in pairs}):
        matched = [(indexed[(p["id"], ARMS[0])], indexed[(p["id"], ARMS[1])])
                   for p in pairs if p["family"] == family
                   and all((p["id"], arm) in indexed for arm in ARMS)]
        both = [(a, b) for a, b in matched if a["accepted"] and b["accepted"]]
        ratios = [b["elapsedSeconds"] / a["elapsedSeconds"] for a, b in both
                  if a["elapsedSeconds"] > 0]
        result["paired"][family] = {
            "matchedPairs": len(matched), "bothAccepted": len(both),
            "baselineOnlyAccepted": sum(a["accepted"] and not b["accepted"] for a, b in matched),
            "marshalOnlyAccepted": sum(b["accepted"] and not a["accepted"] for a, b in matched),
            "neitherAccepted": sum(not a["accepted"] and not b["accepted"] for a, b in matched),
            "timeRatioSampleCount": len(ratios),
            "bothAcceptedMarshalOverBaselineTimeMedian": statistics.median(ratios) if ratios else None,
        }
    result["warning"] = "成功配对耗时存在幸存者偏差；同时查看失败/缺失分母，不推断普遍加速或生产可用。引用尚须独立核验。"
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--plan", required=True, type=Path)
    parser.add_argument("--records", required=True, type=Path)
    args = parser.parse_args()
    try:
        plan = json.loads(args.plan.read_text())
        records = [json.loads(line) for line in args.records.read_text().splitlines() if line.strip()]
        report = summarize(plan, records)
    except (OSError, ValueError, KeyError, TypeError) as error:
        parser.exit(2, f"invalid scorecard input ({type(error).__name__})\n")
    print(json.dumps(report, ensure_ascii=False, indent=2, allow_nan=False))


if __name__ == "__main__":
    main()
