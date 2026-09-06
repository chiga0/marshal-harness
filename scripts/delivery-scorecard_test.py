"""统计口径回归；所有记录均为合成测试，不是产品效率证据。"""

import copy
import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("scorecard", Path(__file__).with_name("delivery-scorecard.py"))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class ScorecardTest(unittest.TestCase):
    def setUp(self):
        self.plan = {"pairs": [{"id": "case-1", "family": "coupled", "conditionsRef": "frozen-config",
                                "acceptanceRef": "frozen-oracle"}]}
        self.row = {"pairId": "case-1", "arm": "lead-subagents", "conditionsRef": "frozen-config",
                    "acceptanceRef": "frozen-oracle", "accepted": True, "evidenceRefs": ["test-only"],
                    "elapsedSeconds": 100, "humanSeconds": None, "attempts": 3,
                    "reworkCycles": 2, "tokens": None}

    def test_empty_and_missing_are_not_success(self):
        report = module.summarize(self.plan, [])
        self.assertEqual(len(report["missing"]), 2)
        self.assertIsNone(report["arms"]["marshal"]["observedAcceptanceRate"])

    def test_failed_delivery_stays_in_denominator(self):
        failed = dict(self.row, arm="marshal", accepted=False, elapsedSeconds=20)
        report = module.summarize(self.plan, [self.row, failed])
        self.assertEqual(report["arms"]["marshal"]["observedAcceptanceRate"], 0)
        self.assertEqual(report["paired"]["coupled"]["baselineOnlyAccepted"], 1)
        self.assertIsNone(report["paired"]["coupled"]["bothAcceptedMarshalOverBaselineTimeMedian"])

    def test_unknown_not_zero_and_rework_not_discarded(self):
        report = module.summarize(self.plan, [self.row])
        metrics = report["arms"]["lead-subagents"]["metrics"]
        self.assertEqual(metrics["tokens"], {"known": 0, "unknown": 1, "sumKnown": None})
        self.assertEqual(metrics["reworkCycles"]["sumKnown"], 2)

    def test_paired_ratio(self):
        other = dict(self.row, arm="marshal", elapsedSeconds=80)
        report = module.summarize(self.plan, [self.row, other])
        self.assertEqual(report["paired"]["coupled"]["bothAcceptedMarshalOverBaselineTimeMedian"], .8)

    def test_reject_mismatched_or_duplicate_observation(self):
        for rows in ([self.row, self.row], [dict(self.row, conditionsRef="changed")],
                     [dict(self.row, acceptanceRef="changed")], [dict(self.row, arm="solo")]):
            with self.subTest(rows=rows), self.assertRaises(ValueError):
                module.summarize(self.plan, rows)

    def test_reject_invalid_metrics(self):
        for field, value in (("elapsedSeconds", None), ("humanSeconds", -1), ("tokens", True),
                             ("attempts", 1.5), ("elapsedSeconds", float("nan")),
                             ("accepted", "true"), ("evidenceRefs", []), ("evidenceRefs", "not-a-list")):
            with self.subTest(field=field, value=value), self.assertRaises(ValueError):
                module.summarize(self.plan, [dict(self.row, **{field: value})])

    def test_zero_baseline_does_not_divide_or_impute(self):
        report = module.summarize(self.plan, [dict(self.row, elapsedSeconds=0), dict(self.row, arm="marshal")])
        self.assertEqual(report["paired"]["coupled"]["bothAccepted"], 1)
        self.assertEqual(report["paired"]["coupled"]["timeRatioSampleCount"], 0)

    def test_duplicate_plan_rejected(self):
        plan = copy.deepcopy(self.plan)
        plan["pairs"].append(plan["pairs"][0])
        with self.assertRaises(ValueError):
            module.summarize(plan, [])


if __name__ == "__main__":
    unittest.main()
