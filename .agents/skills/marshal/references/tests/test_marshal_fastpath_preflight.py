from __future__ import annotations

import copy
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


REFERENCES = Path(__file__).resolve().parents[1]
REPOSITORY = REFERENCES.parents[3]
VALIDATOR = REFERENCES / "marshal-fastpath-preflight.py"


def load_validator_module():
    spec = importlib.util.spec_from_file_location("marshal_fastpath_preflight", VALIDATOR)
    if spec is None or spec.loader is None:
        raise RuntimeError("cannot load fastpath validator")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


VALIDATOR_MODULE = load_validator_module()


def compact(value: object) -> bytes:
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode()


def digest_bytes(value: bytes) -> str:
    return "sha256:" + hashlib.sha256(value).hexdigest()


def digest_object(value: object) -> str:
    return digest_bytes(compact(value))


def run(command: list[str], cwd: Path) -> str:
    completed = subprocess.run(
        command,
        cwd=cwd,
        check=True,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    return completed.stdout.strip()


class MarshalFastpathPreflightTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        commit = run(["git", "rev-parse", "HEAD"], REPOSITORY)
        subprocess.run(["make", "build", f"COMMIT={commit}"], cwd=REPOSITORY, check=True)
        cls.marshal = (REPOSITORY / "bin/marshal").resolve()

    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory(prefix="marshal-fastpath-plan-test.")
        self.root = Path(self.temporary.name).resolve()
        self.source = self.root / "source"
        self.protected = self.root / "protected"
        self.operator = self.root / "operator"
        self.source.mkdir()
        self.operator.mkdir()
        run(["git", "init", "-q"], self.source)
        run(["git", "config", "user.name", "Marshal Test"], self.source)
        run(["git", "config", "user.email", "marshal@example.invalid"], self.source)
        (self.source / "reports").mkdir()
        (self.source / "reports" / ".keep").write_text("tracked\n", encoding="utf-8")
        run(["git", "add", "reports/.keep"], self.source)
        run(["git", "commit", "-qm", "base"], self.source)
        self.source_head = run(["git", "rev-parse", "HEAD"], self.source)
        run(["git", "worktree", "add", "--detach", str(self.protected), self.source_head], self.source)
        self.qoder = self.root / "qoder"
        self.qoder.write_text(
            "#!/bin/sh\n"
            "for arg in \"$@\"; do\n"
            "  if [ \"$arg\" = \"--version\" ]; then\n"
            "    printf '1.1.23\\n'\n"
            "    exit 0\n"
            "  fi\n"
            "done\n"
            "exit 97\n",
            encoding="utf-8",
        )
        self.qoder.chmod(0o700)
        self.task = self.content_task()
        self.policy = {
            "apiVersion": "marshal.dev/v1alpha1",
            "kind": "PolicySnapshot",
            "taskId": "FASTPATH-CONTENT",
            "runId": "fastpath-plan-r1",
            "sources": [{"scope": "builtin", "digest": "sha256:" + "b" * 64, "required": True}],
            "effective": {
                "minimumExecutionProfile": "workspace-write",
                "requireEnforcedNetworkPolicy": False,
                "networkPolicy": "unenforced",
                "allowFallbackWorkers": False,
                "allowWorkerSubagents": False,
                "allowPublication": False,
                "allowMerge": False,
                "allowGateWaivers": False,
                "allowedAdapters": ["qoder"],
                "environmentAllowlist": ["PATH"],
                "retentionDays": 30,
            },
            "control": {
                "autonomyProfile": "balanced",
                "requiredApprovals": ["plan", "publish"],
                "allowMediatedSteering": False,
                "directPtyPolicy": "deny",
                "maxSteeringRounds": 0,
            },
            "policyDigest": "",
            "generatedAt": "2026-08-20T00:00:00Z",
        }
        self.policy["policyDigest"] = digest_object(self.policy)
        self.write_inputs()

    def tearDown(self) -> None:
        self.temporary.cleanup()

    def content_task(self) -> dict:
        script = (
            "from pathlib import Path; import unicodedata; p=Path('reports/result.md'); "
            "s=p.read_text(encoding='utf-8'); n=unicodedata.normalize('NFKC',s).casefold(); "
            "required_all=['heading: value']; required_any=[['authority: boundary','trust: boundary']]; "
            "forbidden=['unsafe: override']; assert len(s.splitlines()) >= 4; "
            "assert len(s.encode('utf-8')) <= 256; assert not [x for x in required_all if x not in n]; "
            "assert not [g for g in required_any if not any(x in n for x in g)]; "
            "assert not [x for x in forbidden if x in n]"
        )
        return {
            "apiVersion": "marshal.dev/v1alpha1",
            "kind": "Task",
            "metadata": {"id": "FASTPATH-CONTENT", "title": "combined plan preflight"},
            "repository": {"path": str(self.protected), "baseRef": self.source_head, "remote": "origin"},
            "work": {
                "objective": "create a report",
                "context": [
                    "commandPath=`reports/result.md`; deliverablePath=`reports/result.md`; "
                    "minimumDeliverableCount=`1`; minimumLineCount=`4`; maximumBytes=`256`; "
                    "required_all[0]=`heading: value`; required_any[0][0]=`authority: boundary`; "
                    "required_any[0][1]=`trust: boundary`; forbidden[0]=`unsafe: override`."
                ],
                "constraints": [],
                "nonGoals": [],
            },
            "scope": {
                "allowPaths": ["reports/result.md"],
                "denyPaths": [".marshal/**"],
                "allowSubmodules": False,
                "maxChangedFiles": 1,
                "maxDiffBytes": 4096,
            },
            "acceptance": {
                "commands": [{
                    "id": "content-check",
                    "argv": ["python3", "-I", "-B", "-c", script],
                    "cwd": ".",
                    "timeoutSeconds": 10,
                    "required": True,
                    "baselinePolicy": "none",
                    "maxLogBytes": 10000,
                }],
                "allowNoChange": False,
            },
            "deliverables": [{
                "id": "report",
                "kind": "report",
                "required": True,
                "pathGlob": "reports/result.md",
                "minimumCount": 1,
            }],
            "worker": {
                "preferredAdapter": "qoder",
                "fallbackAdapters": [],
                "executionProfile": "workspace-write",
                "sessionPolicy": "ephemeral",
            },
            "budgets": {
                "runTimeoutSeconds": 60,
                "attemptTimeoutSeconds": 30,
                "maxAttempts": 1,
                "maxOperationalRetries": 0,
                "maxReworkRounds": 0,
                "maxOutputBytes": 100000,
            },
            "publication": {
                "required": False,
                "provider": "none",
                "mode": "none",
                "remote": "origin",
                "baseBranch": "main",
                "mergePolicy": "never",
                "requiredChecks": [],
            },
        }

    def acceptance_manifest(self, task_raw: bytes) -> dict:
        command = copy.deepcopy(self.task["acceptance"]["commands"][0])
        command["argvDigest"] = digest_object(command["argv"])
        command["tupleDigest"] = digest_object(self.task["acceptance"]["commands"][0])
        fixtures = {
            "positive.md": "# Result\n\nheading： value\nauthority： boundary\n",
            "missing-all.md": "# Result\n\nother value\nauthority： boundary\n",
            "missing-any.md": "# Result\n\nheading： value\nother boundary\n",
            "forbidden.md": "# Result\n\nheading： value\nauthority： boundary\nunsafe： override\n",
            "too-short.md": "heading： value authority： boundary\n",
            "too-large.md": "heading： value\nauthority： boundary\nextra\n" + "x" * 300,
        }
        fixture_directory = self.operator / "fixtures"
        fixture_directory.mkdir(exist_ok=True)
        for name, content in fixtures.items():
            (fixture_directory / name).write_text(content, encoding="utf-8")
        return {
            "manifestVersion": "marshal-operator-acceptance-preflight/v1",
            "taskSpecDigest": digest_bytes(task_raw),
            "command": command,
            "contentGate": {
                "commandPath": "reports/result.md",
                "deliverablePath": "reports/result.md",
                "minimumDeliverableCount": 1,
                "normalizer": "nfkc-casefold",
                "minimumLineCount": 4,
                "maximumBytes": 256,
                "required_all": ["heading: value"],
                "required_any": [["authority: boundary", "trust: boundary"]],
                "forbidden": ["unsafe: override"],
            },
            "prompt_literals": [
                {"rule": "commandPath", "literal": "reports/result.md"},
                {"rule": "deliverablePath", "literal": "reports/result.md"},
                {"rule": "minimumDeliverableCount", "literal": "1"},
                {"rule": "minimumLineCount", "literal": "4"},
                {"rule": "maximumBytes", "literal": "256"},
                {"rule": "required_all[0]", "literal": "heading: value"},
                {"rule": "required_any[0][0]", "literal": "authority: boundary"},
                {"rule": "required_any[0][1]", "literal": "trust: boundary"},
                {"rule": "forbidden[0]", "literal": "unsafe: override"},
            ],
            "fixtures": {
                "positive": [{
                    "id": "positive-nfkc-fullwidth-colon",
                    "path": "fixtures/positive.md",
                    "digest": digest_bytes(fixtures["positive.md"].encode()),
                }],
                "negative": [
                    {"id": "negative-missing-all", "path": "fixtures/missing-all.md", "digest": digest_bytes(fixtures["missing-all.md"].encode()), "expectedReason": "missing-required-all"},
                    {"id": "negative-missing-any", "path": "fixtures/missing-any.md", "digest": digest_bytes(fixtures["missing-any.md"].encode()), "expectedReason": "missing-required-any"},
                    {"id": "negative-forbidden-nfkc-fullwidth-colon", "path": "fixtures/forbidden.md", "digest": digest_bytes(fixtures["forbidden.md"].encode()), "expectedReason": "forbidden-present"},
                    {"id": "negative-too-short", "path": "fixtures/too-short.md", "digest": digest_bytes(fixtures["too-short.md"].encode()), "expectedReason": "below-minimum-line-count"},
                    {"id": "negative-too-large", "path": "fixtures/too-large.md", "digest": digest_bytes(fixtures["too-large.md"].encode()), "expectedReason": "maximum-bytes-exceeded"},
                ],
            },
        }

    def write_inputs(self) -> None:
        task_raw = compact(self.task)
        policy_raw = compact(self.policy)
        (self.operator / "task-spec.json").write_bytes(task_raw)
        (self.operator / "policy.json").write_bytes(policy_raw)
        (self.operator / "acceptance.json").write_bytes(compact(self.acceptance_manifest(task_raw)))
        plan = {
            "apiVersion": "marshal.operator/v1alpha1",
            "kind": "PlanPremortemPreflight",
            "runId": "fastpath-plan-r1",
            "selectedAdapter": "qoder",
            "sourceHead": self.source_head,
            "taskSpec": {"path": "task-spec.json", "digest": digest_bytes(task_raw)},
            "policySnapshot": {"path": "policy.json", "digest": digest_bytes(policy_raw)},
        }
        (self.operator / "plan.json").write_bytes(compact(plan))

    def invoke(
        self,
        task_kind: str,
        include_acceptance: bool = True,
        semantic_timeout: str | int | None = None,
        premortem_timeout: str | int | None = None,
        bytecode_flag: bool = True,
    ) -> tuple[int, dict]:
        argv = [
            sys.executable,
            "-I",
        ]
        if bytecode_flag:
            argv.append("-B")
        argv.extend([
            str(VALIDATOR),
            "--phase",
            "plan",
            "--task-kind",
            task_kind,
            "--root",
            str(self.operator),
            "--plan-manifest",
            "plan.json",
            "--marshal",
            str(self.marshal),
            "--protected-root",
            str(self.protected),
        ])
        if include_acceptance:
            argv.extend(["--acceptance-manifest", "acceptance.json"])
        if semantic_timeout is not None:
            argv.extend(["--semantic-timeout-seconds", str(semantic_timeout)])
        if premortem_timeout is not None:
            argv.extend(["--premortem-timeout-seconds", str(premortem_timeout)])
        environment = os.environ.copy()
        environment.pop("PYTHONDONTWRITEBYTECODE", None)
        environment.update({
            "MARSHAL_QODER_PATH": str(self.qoder),
            "MARSHAL_QODER_MODE": "ordinary-user",
            "MARSHAL_QODER_CONFORMANCE_CONFIG": "",
            "MARSHAL_CODEX_PATH": "",
            "MARSHAL_CODEX_MODE": "ordinary-user",
            "MARSHAL_CODEX_AUTHORITY_CONFIG": "",
            "MARSHAL_QWEN_PATH": "",
            "MARSHAL_QWEN_MODE": "ordinary-user",
            "MARSHAL_PI_PATH": "",
            "MARSHAL_PI_MODE": "ordinary-user",
        })
        completed = subprocess.run(
            argv,
            cwd=REPOSITORY,
            env=environment,
            check=False,
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
        )
        self.assertEqual(completed.stderr, "")
        return completed.returncode, json.loads(completed.stdout)

    def test_content_plan_combines_semantic_and_premortem_receipts(self) -> None:
        code, receipt = self.invoke("content")
        self.assertEqual(code, 0)
        self.assertEqual(receipt["reasonCode"], "combined-plan-preflight-pass")
        self.assertEqual(receipt["acceptanceSemantic"]["status"], "pass")
        self.assertEqual(receipt["acceptanceSemantic"]["positiveFixtures"], 1)
        self.assertEqual(receipt["acceptanceSemantic"]["negativeFixtures"], 5)
        self.assertEqual(receipt["acceptanceSemantic"]["fixtureCount"], 6)
        self.assertEqual(receipt["acceptanceSemantic"]["timeoutSeconds"], 180)
        self.assertEqual(receipt["planPremortem"]["timeoutSeconds"], 30)
        self.assertRegex(
            receipt["acceptanceSemantic"]["semanticManifestDigest"],
            r"^sha256:[0-9a-f]{64}$",
        )
        self.assertRegex(
            receipt["acceptanceSemantic"]["fixtureAggregateDigest"],
            r"^sha256:[0-9a-f]{64}$",
        )
        combined = dict(receipt)
        combined_digest = combined.pop("combinedDigest")
        self.assertEqual(combined_digest, digest_object(combined))

    def test_phase_timeout_values_are_exact_digest_bound_and_phase_stable(self) -> None:
        default_code, default = self.invoke("content")
        semantic_code, semantic = self.invoke("content", semantic_timeout=181)
        premortem_code, premortem = self.invoke("content", premortem_timeout=31)
        self.assertEqual((default_code, semantic_code, premortem_code), (0, 0, 0))
        self.assertEqual(semantic["acceptanceSemantic"]["timeoutSeconds"], 181)
        for field in ("taskSpecDigest", "policySnapshotDigest", "sourceHead", "status"):
            self.assertEqual(semantic["planPremortem"][field], default["planPremortem"][field])
        # A fresh plan probe intentionally refreshes the capability snapshot;
        # its receipt/capability digest is therefore allowed to change while
        # the semantic timeout remains isolated from the plan phase.
        self.assertEqual(premortem["planPremortem"]["timeoutSeconds"], 31)
        self.assertEqual(premortem["acceptanceSemantic"], default["acceptanceSemantic"])
        self.assertNotEqual(default["combinedDigest"], semantic["combinedDigest"])
        self.assertNotEqual(default["combinedDigest"], premortem["combinedDigest"])

    def test_timeout_cli_boundaries_fail_closed(self) -> None:
        cases = (
            ("semantic", VALIDATOR_MODULE.MAX_SEMANTIC_TIMEOUT_SECONDS),
            ("premortem", VALIDATOR_MODULE.MAX_PREMORTEM_TIMEOUT_SECONDS),
        )
        for label, maximum in cases:
            parse = VALIDATOR_MODULE.bounded_timeout(maximum)
            with self.subTest(label=label, value="maximum"):
                self.assertEqual(parse(str(maximum)), maximum)
            for value in ("0", str(maximum + 1), "1.5"):
                with self.subTest(label=label, value=value):
                    with self.assertRaises(VALIDATOR_MODULE.argparse.ArgumentTypeError):
                        parse(value)

    def test_wrapper_forces_no_bytecode_for_nested_plan(self) -> None:
        code, receipt = self.invoke("content", bytecode_flag=False)
        self.assertEqual(code, 0, receipt)
        self.assertFalse((self.root / "__pycache__").exists())

    def test_combined_digest_changes_with_fixture_raw_bytes(self) -> None:
        first_code, first = self.invoke("content")
        self.assertEqual(first_code, 0)
        fixture = self.operator / "fixtures/positive.md"
        fixture.write_text(fixture.read_text(encoding="utf-8") + "\n", encoding="utf-8")
        manifest_path = self.operator / "acceptance.json"
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
        manifest["fixtures"]["positive"][0]["digest"] = digest_bytes(fixture.read_bytes())
        manifest_path.write_bytes(compact(manifest))
        second_code, second = self.invoke("content")
        self.assertEqual(second_code, 0)
        self.assertNotEqual(first["combinedDigest"], second["combinedDigest"])
        self.assertNotEqual(
            first["acceptanceSemantic"]["fixtureAggregateDigest"],
            second["acceptanceSemantic"]["fixtureAggregateDigest"],
        )
        self.assertNotEqual(
            first["acceptanceSemantic"]["semanticManifestDigest"],
            second["acceptanceSemantic"]["semanticManifestDigest"],
        )

    def test_content_plan_without_semantic_manifest_fails_before_premortem(self) -> None:
        code, receipt = self.invoke("content", include_acceptance=False)
        self.assertEqual(code, 1)
        self.assertEqual(receipt["reasonCode"], "acceptance-semantic-manifest-required")
        self.assertEqual(receipt["stage"], "acceptance-semantic")

    def test_content_signals_cannot_be_declared_non_content(self) -> None:
        code, receipt = self.invoke("non-content", include_acceptance=False)
        self.assertEqual(code, 1)
        self.assertEqual(receipt["reasonCode"], "content-task-semantic-manifest-required")

    def test_explicit_text_carriers_cannot_be_declared_non_content(self) -> None:
        original = copy.deepcopy(self.task)
        for kind in ("other", "diagnostic"):
            with self.subTest(kind=kind):
                self.task = copy.deepcopy(original)
                self.task["deliverables"][0]["kind"] = kind
                self.task["deliverables"][0]["mediaType"] = "text/markdown; charset=utf-8"
                self.task["acceptance"]["commands"][0]["argv"] = [
                    "test",
                    "-f",
                    "reports/result.md",
                ]
                self.write_inputs()
                code, receipt = self.invoke("non-content", include_acceptance=False)
                self.assertEqual(code, 1)
                self.assertEqual(
                    receipt["reasonCode"], "content-task-semantic-manifest-required"
                )

    def test_non_content_branch_is_explicit_in_receipt(self) -> None:
        self.task["deliverables"][0]["kind"] = "code"
        self.task["acceptance"]["commands"][0]["argv"] = ["test", "-f", "reports/result.md"]
        self.write_inputs()
        code, receipt = self.invoke("non-content", include_acceptance=False)
        self.assertEqual(code, 0)
        self.assertEqual(
            receipt["acceptanceSemantic"],
            {
                "contentSignals": [],
                "reasonCode": "non-content-task-declared",
                "sourceHead": self.source_head,
                "status": "not-applicable",
                "taskSpecDigest": receipt["taskSpecDigest"],
                "timeoutSeconds": 180,
            },
        )

    def test_child_timeouts_are_phase_specific_and_stable(self) -> None:
        sleeper = [sys.executable, "-I", "-B", "-c", "import time; time.sleep(5)"]
        for stage in ("acceptance-semantic", "plan-premortem"):
            with self.subTest(stage=stage):
                with self.assertRaises(VALIDATOR_MODULE.FastpathError) as raised:
                    VALIDATOR_MODULE.run_child(sleeper, stage, 0.05)
                self.assertEqual(raised.exception.stage, stage)
                self.assertEqual(raised.exception.reason_code, f"{stage}-timeout")

    def test_child_b_then_external_a_aba_is_rejected(self) -> None:
        evidence_a = {
            "semanticManifestDigest": "sha256:" + "a" * 64,
            "fixtureAggregateDigest": "sha256:" + "1" * 64,
            "fixtureCount": 6,
        }
        child_b = {
            "semanticManifestDigest": "sha256:" + "b" * 64,
            "fixtureAggregateDigest": "sha256:" + "2" * 64,
            "fixtureCount": 6,
        }
        with self.assertRaises(VALIDATOR_MODULE.FastpathError) as raised:
            VALIDATOR_MODULE.cross_check_semantic_evidence(
                evidence_a,
                child_b,
                evidence_a,
            )
        self.assertEqual(raised.exception.stage, "acceptance-semantic")
        self.assertEqual(raised.exception.reason_code, "semantic-input-drift")

    def test_timeout_kills_ignore_term_grandchild_after_leader_exits(self) -> None:
        grandchild = self.root / "ignore-term-grandchild.py"
        pid_file = self.root / "ignore-term-grandchild.pid"
        grandchild.write_text(
            "import os, signal, sys, time\n"
            "from pathlib import Path\n"
            "Path(sys.argv[1]).write_text(str(os.getpid()))\n"
            "signal.signal(signal.SIGTERM, signal.SIG_IGN)\n"
            "os.close(0); os.close(1); os.close(2)\n"
            "while True: time.sleep(1)\n",
            encoding="utf-8",
        )
        leader = (
            "import subprocess, sys, time; "
            f"subprocess.Popen([sys.executable,'-I','-B',{str(grandchild)!r},{str(pid_file)!r}]); "
            "time.sleep(5)"
        )
        with self.assertRaises(VALIDATOR_MODULE.FastpathError) as raised:
            VALIDATOR_MODULE.run_child(
                [sys.executable, "-I", "-B", "-c", leader],
                "plan-premortem",
                0.2,
            )
        self.assertEqual(raised.exception.reason_code, "plan-premortem-timeout")
        grandchild_pid = int(pid_file.read_text(encoding="utf-8"))
        with self.assertRaises(ProcessLookupError):
            os.kill(grandchild_pid, 0)


if __name__ == "__main__":
    unittest.main()
