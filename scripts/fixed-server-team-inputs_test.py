#!/usr/bin/env python3
"""团队输入适配回归；最终合法性另由 Go Core parser 测试确认。"""

import copy
import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import types
import unittest


ROOT = Path(__file__).resolve().parent.parent
spec = importlib.util.spec_from_file_location("team_inputs", ROOT/"scripts/fixed-server-team-inputs.py")
renderer = importlib.util.module_from_spec(spec)
spec.loader.exec_module(renderer)


class InputTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.doctor = Path(self.temp.name)/"doctor.json"
        self.binding = {"schemaVersion": "marshal.local-dogfood-environment.v1", "selfProfile": "darwin-local-dogfood",
                        "activationDigest": "sha256:"+"a"*64, "identitySubjectDigest": "sha256:"+"b"*64,
                        "assurance": "ordinary-user", "execution": "workspace-write", "production": False, "publication": "none"}
        self.doctor.write_text(json.dumps({"policyEnvironmentBinding": self.binding}))
        self.args = types.SimpleNamespace(repository=str(ROOT), base_ref="a"*40, doctor=str(self.doctor), model="openai/qwen3.8-max",
                                          goal_id="goal-quote", proposal_id="proposal-quote", request_id="approve-quote", deadline="2030-01-01T00:00:00Z")

    def test_complete_frozen_three_node_plan(self):
        request = renderer.build(self.args)
        inputs = request["inputs"]
        self.assertEqual(request["inputsDigest"], renderer.digest(inputs))
        self.assertEqual(inputs["proposal"]["goalSpecDigest"], renderer.digest(inputs["spec"]))
        self.assertEqual(inputs["limits"]["maxConcurrentNodes"], 2)
        self.assertEqual(inputs["limits"]["maxTotalRuns"], 3)
        self.assertEqual(len(inputs["proposal"]["edges"]), 2)
        for node in inputs["nodes"]:
            task, policy = node["task"], node["policy"]
            task_id, run_id = renderer.node_ids(inputs["spec"]["authorityNamespaceId"], self.args.goal_id, self.args.proposal_id, node["nodeId"])
            self.assertEqual(task["metadata"]["id"], task_id)
            self.assertEqual((policy["taskId"], policy["runId"]), (task_id, run_id))
            detached = copy.deepcopy(policy)
            detached["policyDigest"] = ""
            self.assertEqual(renderer.digest(detached), policy["policyDigest"])
            self.assertEqual(policy["environmentBinding"], self.binding)
            self.assertEqual(task["scope"]["allowPaths"], renderer.PATHS[node["nodeId"]])
            self.assertEqual(task["publication"]["provider"], "none")
            self.assertEqual(task["budgets"]["maxAttempts"], 1)
            self.assertEqual(task["budgets"]["maxReworkRounds"], 0)
            self.assertFalse(policy["effective"]["allowWorkerSubagents"])
            self.assertFalse(task["acceptance"]["allowNoChange"])

    def test_integration_delivers_bound_handoff_without_forcing_code_edits(self):
        inputs = renderer.build(self.args)["inputs"]
        for node in inputs["nodes"]:
            task = node["task"]
            argv = task["acceptance"]["commands"][0]["argv"]
            if node["role"] == "integrate":
                self.assertEqual(argv[-2:], ["--delivery", "quote_delivery.json"])
                self.assertIn("不要制造代码修改", task["work"]["objective"])
                self.assertEqual(task["scope"]["maxChangedFiles"], 3)
                self.assertEqual(task["deliverables"][-1], {"id": "quote-2", "kind": "diagnostic", "required": True,
                                                          "pathGlob": "quote_delivery.json", "minimumCount": 1})
            else:
                self.assertNotIn("--delivery", argv)
                self.assertNotIn("quote_delivery.json", task["scope"]["allowPaths"])

    def test_identity_change_and_input_digest_change(self):
        first = renderer.build(self.args)
        self.args.model = "openai/other-model"
        second = renderer.build(self.args)
        self.assertNotEqual(first["inputsDigest"], second["inputsDigest"])
        self.assertEqual(first["inputs"]["nodes"][0]["policy"]["runId"], second["inputs"]["nodes"][0]["policy"]["runId"])
        self.args.proposal_id = "proposal-new"
        third = renderer.build(self.args)
        self.assertNotEqual(second["inputs"]["nodes"][0]["policy"]["runId"], third["inputs"]["nodes"][0]["policy"]["runId"])

    def test_work_contract_bounds_exploration_not_independent_acceptance(self):
        for node in renderer.build(self.args)["inputs"]["nodes"]:
            task = node["task"]
            constraints = "\n".join(task["work"]["constraints"])
            self.assertIn("当前工具面没有 shell", constraints)
            self.assertIn("不由 Worker 证明通过", constraints)
            self.assertIn("not-run", constraints)
            self.assertIn("不探索全仓架构", constraints)
            self.assertIn(renderer.CONTRACT, task["work"]["context"])
            self.assertEqual(task["acceptance"]["commands"][0]["id"], "quote-team-"+node["nodeId"])
            self.assertTrue(task["acceptance"]["commands"][0]["required"])
            self.assertFalse(task["acceptance"]["allowNoChange"])

    def test_invalid_ids_deadlines_and_environment(self):
        for field, value in (("goal_id", "../bad"), ("proposal_id", "x"), ("deadline", "2030-01-01T00:00:00+08:00"), ("deadline", "2030-01-01T00:00:00")):
            args = copy.copy(self.args)
            setattr(args, field, value)
            with self.assertRaises(ValueError):
                renderer.build(args)
        self.doctor.write_text("{}")
        with self.assertRaises(SystemExit):
            renderer.build(self.args)

    def test_cli_only_creates_request_and_refuses_overwrite(self):
        output = Path(self.temp.name)/"request.json"
        argv = [sys.executable, "-I", "-B", str(ROOT/"scripts/fixed-server-team-inputs.py")]
        for name, value in vars(self.args).items():
            argv += ["--"+name.replace("_", "-"), value]
        argv += ["--out", str(output)]
        result = subprocess.run(argv, capture_output=True, timeout=10)
        self.assertEqual(result.returncode, 0, result.stderr)
        original = output.read_bytes()
        result = subprocess.run(argv, capture_output=True, timeout=10)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(output.read_bytes(), original)
        self.assertEqual({p.name for p in Path(self.temp.name).iterdir()}, {"doctor.json", "request.json"})


if __name__ == "__main__":
    unittest.main()
