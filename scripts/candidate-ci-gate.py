#!/usr/bin/env python3
"""验证未发布候选的精确分支 CI；不授予 main/stable 发布资格。"""

import json
import re
import subprocess
import sys
import tempfile
from urllib.parse import urlencode

REPOSITORY = "chiga0/marshal-harness"
JOBS = {
    "Quality (ubuntu-latest)", "Quality (macos-latest)",
    "Linux candidate conformance (amd64)",
    "Linux candidate conformance (arm64)", "Secret scan",
}


def positive_id(value):
    return type(value) is int and value > 0


def latest_candidate_run(document, head, branch):
    runs = document.get("workflow_runs")
    if not isinstance(runs, list):
        raise ValueError("runs-shape")
    matches = [run for run in runs if isinstance(run, dict)
               and run.get("head_sha") == head
               and run.get("head_branch") == branch
               and run.get("event") == "workflow_dispatch"]
    if not matches or any(not positive_id(run.get("id")) for run in matches):
        raise ValueError("missing-exact-run")
    latest = max(matches, key=lambda run: run["id"])
    if latest.get("status") != "completed" or latest.get("conclusion") != "success":
        raise ValueError("latest-ci-not-green")
    return latest["id"]


def validate_jobs(document, head):
    jobs = document.get("jobs")
    if document.get("total_count") != 5 or not isinstance(jobs, list) or len(jobs) != 5:
        raise ValueError("job-count")
    if any(not isinstance(job, dict) for job in jobs):
        raise ValueError("job-shape")
    if {job.get("name") for job in jobs} != JOBS:
        raise ValueError("job-set")
    if any(job.get("head_sha") != head or job.get("status") != "completed"
           or job.get("conclusion") != "success" for job in jobs):
        raise ValueError("job-not-green")


def fetch(path):
    # Keep API/error bodies out of logs, memory and credential diagnostics.
    with tempfile.TemporaryFile() as output:
        subprocess.run(["gh", "api", path], stdout=output, stderr=subprocess.DEVNULL,
                       check=True, timeout=30)
        output.seek(0)
        raw = output.read((1 << 20) + 1)
    if len(raw) > 1 << 20:
        raise ValueError("api-size")
    result = json.loads(raw)
    if not isinstance(result, dict):
        raise ValueError("api-shape")
    return result


def check(head, branch, get=fetch):
    if not re.fullmatch(r"[0-9a-f]{40}", head):
        raise ValueError("head")
    if not re.fullmatch(r"feat/[A-Za-z0-9][A-Za-z0-9._/-]{0,180}", branch):
        raise ValueError("candidate-branch")
    query = urlencode({"head_sha": head, "event": "workflow_dispatch", "per_page": 100})
    runs = get(f"repos/{REPOSITORY}/actions/workflows/ci.yml/runs?{query}")
    run_id = latest_candidate_run(runs, head, branch)
    validate_jobs(get(f"repos/{REPOSITORY}/actions/runs/{run_id}/jobs?per_page=100"), head)
    return run_id


if __name__ == "__main__":
    try:
        if len(sys.argv) != 3:
            raise ValueError("arguments")
        run_id = check(sys.argv[1], sys.argv[2])
    except (ValueError, TypeError, KeyError, OSError, subprocess.SubprocessError):
        sys.exit("candidate-ci-gate: FAIL（精确候选 CI 未完成或证据无效；未授权执行）")
    print(f"candidate-ci-gate: PASS ciRunId={run_id} authority=candidate-only")
