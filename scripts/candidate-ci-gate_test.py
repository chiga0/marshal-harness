#!/usr/bin/env python3
"""候选验证与正式发布 gate 的隔离回归。"""

import copy
import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("candidate_gate", Path(__file__).with_name("candidate-ci-gate.py"))
gate = importlib.util.module_from_spec(spec)
spec.loader.exec_module(gate)
HEAD = "a" * 40
BRANCH = "feat/b1-stop-lifecycle"


class CandidateGateTests(unittest.TestCase):
    def setUp(self):
        self.run = {"id": 10, "head_sha": HEAD, "head_branch": BRANCH,
                    "event": "workflow_dispatch", "status": "completed", "conclusion": "success"}
        self.jobs = {"total_count": 5, "jobs": [
            {"name": name, "head_sha": HEAD, "status": "completed", "conclusion": "success"}
            for name in sorted(gate.JOBS)]}

    def test_exact_green(self):
        paths = []

        def get(path):
            paths.append(path)
            return {"workflow_runs": [self.run]} if len(paths) == 1 else self.jobs

        self.assertEqual(gate.check(HEAD, BRANCH, get), 10)
        self.assertEqual(paths, [
            f"repos/{gate.REPOSITORY}/actions/workflows/ci.yml/runs?head_sha={HEAD}&event=workflow_dispatch&per_page=100",
            f"repos/{gate.REPOSITORY}/actions/runs/10/jobs?per_page=100"])

    def test_invalid_identity_before_network(self):
        def forbidden(_):
            self.fail("invalid identity reached API")
        for head, branch in [(HEAD, "main"), (HEAD, "feat/x?event=push"), ("b", BRANCH)]:
            with self.subTest(head=head, branch=branch), self.assertRaises(ValueError):
                gate.check(head, branch, forbidden)

    def test_latest_failure_cannot_hide_behind_old_success(self):
        for status, conclusion in [("in_progress", None), ("completed", "failure"), ("completed", "cancelled")]:
            newer = dict(self.run, id=11, status=status, conclusion=conclusion)
            with self.subTest(status=status, conclusion=conclusion), self.assertRaises(ValueError):
                gate.latest_candidate_run({"workflow_runs": [newer, self.run]}, HEAD, BRANCH)

    def test_run_provenance(self):
        for field, value in [("head_sha", "b" * 40), ("head_branch", "main"),
                             ("event", "pull_request"), ("event", "push"),
                             ("id", True), ("id", 0), ("id", "10")]:
            with self.subTest(field=field, value=value), self.assertRaises(ValueError):
                gate.latest_candidate_run({"workflow_runs": [dict(self.run, **{field: value})]}, HEAD, BRANCH)

    def test_each_job_is_mandatory_and_green(self):
        for index in range(5):
            for field, value in [("head_sha", "b" * 40), ("conclusion", "failure"),
                                 ("conclusion", "skipped"), ("status", "in_progress"), ("name", "unknown")]:
                jobs = copy.deepcopy(self.jobs)
                jobs["jobs"][index][field] = value
                with self.subTest(index=index, field=field), self.assertRaises(ValueError):
                    gate.validate_jobs(jobs, HEAD)

    def test_missing_duplicate_and_malformed_jobs(self):
        invalid = [dict(self.jobs, total_count=6), {"total_count": 5, "jobs": []}]
        duplicate = copy.deepcopy(self.jobs)
        duplicate["jobs"][0] = duplicate["jobs"][1]
        invalid.append(duplicate)
        malformed = copy.deepcopy(self.jobs)
        malformed["jobs"][0] = None
        invalid.append(malformed)
        for jobs in invalid:
            with self.subTest(jobs=jobs), self.assertRaises(ValueError):
                gate.validate_jobs(jobs, HEAD)


if __name__ == "__main__":
    unittest.main()
