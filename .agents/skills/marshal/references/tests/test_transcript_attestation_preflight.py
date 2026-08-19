import hashlib
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import threading
import unittest


REPOSITORY_ROOT = Path(__file__).resolve().parents[5]
SKILL_ROOT = REPOSITORY_ROOT / ".agents/skills/marshal"
VALIDATOR = SKILL_ROOT / "references/validate-transcript-attestation-preflight.py"
SCHEMA = SKILL_ROOT / "references/transcript-attestation-preflight.schema.json"
TEMPLATE = SKILL_ROOT / "templates/transcript-attestation-preflight.json"
SCHEMA_PROBE = SKILL_ROOT / "references/tests/transcript_attestation_schema_probe.go"
CHECKER_SOURCE = SKILL_ROOT / "references/tests/transcript_attestation_core_probe.go"
FIXTURES = SKILL_ROOT / "references/fixtures/transcript-attestation"


def raw_digest(path: Path) -> str:
    return "sha256:" + hashlib.sha256(path.read_bytes()).hexdigest()


class TranscriptAttestationPreflightTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls._binary_directory = tempfile.TemporaryDirectory()
        cls.checker = (Path(cls._binary_directory.name) / "transcript-attestation-checker").resolve()
        cls.marshal = (Path(cls._binary_directory.name) / "marshal").resolve()
        for output, source in ((cls.checker, str(CHECKER_SOURCE)), (cls.marshal, "./cmd/marshal")):
            completed = subprocess.run(["go", "build", "-o", str(output), source], cwd=REPOSITORY_ROOT, capture_output=True, text=True)
            if completed.returncode != 0:
                raise RuntimeError(completed.stderr)
        spec = importlib.util.spec_from_file_location("transcript_attestation_validator", VALIDATOR)
        if spec is None or spec.loader is None:
            raise RuntimeError("cannot load transcript attestation validator")
        cls.validator_module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(cls.validator_module)

    @classmethod
    def tearDownClass(cls) -> None:
        cls._binary_directory.cleanup()

    def write_json(self, path: Path, value: object) -> None:
        path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n")

    def jcs_digest(self, path: Path) -> str:
        value = json.loads(path.read_text(), parse_constant=lambda token: (_ for _ in ()).throw(ValueError(token)))
        canonical = json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode()
        return "sha256:" + hashlib.sha256(canonical).hexdigest()

    def prepare(self, directory: str) -> tuple[Path, Path, dict]:
        root = Path(directory) / "inputs"
        shutil.copytree(FIXTURES, root)
        worktree = root / "worktree"
        worktree.mkdir()
        worktree = worktree.resolve()
        manifest_path = root / "manifest-positive.json"
        manifest = json.loads(manifest_path.read_text())

        task_path = root / "task-spec.json"
        task = json.loads(task_path.read_text())
        task["repository"]["path"] = str(worktree)
        self.write_json(task_path, task)

        capability_path = root / "capability-snapshot.json"
        request_path = root / "worker-request.json"
        request = json.loads(request_path.read_text())
        request["worktreePath"] = str(worktree)
        request["specDigest"] = self.jcs_digest(task_path)
        request["capabilityDigest"] = self.jcs_digest(capability_path)
        self.write_json(request_path, request)

        for transcript_name, meta_name in (("transcript-positive.jsonl", "transcript-positive-meta.json"), ("transcript-negative-undeclared-wc.jsonl", "transcript-negative-undeclared-wc-meta.json")):
            transcript = root / transcript_name
            transcript.write_text(transcript.read_text().replace("/fixture/repository", str(worktree)))
            meta = json.loads((root / meta_name).read_text())
            meta["capturedBytes"] = transcript.stat().st_size
            self.write_json(root / meta_name, meta)

        self.refresh_manifest(root, manifest)
        self.write_json(manifest_path, manifest)
        negative_path = root / "manifest-negative-undeclared-wc.json"
        negative = json.loads(negative_path.read_text())
        self.refresh_manifest(root, negative)
        self.write_json(negative_path, negative)
        return root, manifest_path, manifest

    def refresh_manifest(self, root: Path, manifest: dict) -> None:
        for descriptor in manifest["inputs"].values():
            descriptor["sha256"] = raw_digest(root / descriptor["path"])

    def refresh_transcript(self, root: Path, manifest: dict, raw: bytes) -> None:
        transcript = root / manifest["inputs"]["transcript"]["path"]
        transcript.write_bytes(raw)
        meta_path = root / manifest["inputs"]["transcriptMeta"]["path"]
        meta = json.loads(meta_path.read_text())
        meta["capturedBytes"] = len(raw)
        meta["eventCount"] = len(raw.splitlines())
        self.write_json(meta_path, meta)
        self.refresh_manifest(root, manifest)

    def invoke(self, root: Path, manifest: str = "manifest-positive.json", env=None, checker=None):
        checker = checker or self.checker
        return subprocess.run([sys.executable, "-I", "-B", str(VALIDATOR), "--root", str(root), "--manifest", manifest, "--checker", str(checker)], capture_output=True, text=True, env=env)

    def assert_failure(self, completed, *reasons: str) -> None:
        self.assertNotEqual(completed.returncode, 0, completed.stdout)
        payload = json.loads(completed.stderr)
        self.assertIn(payload["reasonCode"], reasons, completed.stderr)

    def mutate_events(self, root: Path, manifest: dict, mutation) -> None:
        transcript = root / manifest["inputs"]["transcript"]["path"]
        events = [json.loads(line) for line in transcript.read_text().splitlines()]
        mutation(events)
        raw = ("".join(json.dumps(event, separators=(",", ":")) + "\n" for event in events)).encode()
        self.refresh_transcript(root, manifest, raw)

    def direct_checker_inputs(self, root: Path, manifest: dict) -> dict[str, bytes]:
        return {
            label: (root / descriptor["path"]).read_bytes()
            for label, descriptor in manifest["inputs"].items()
        }

    def test_positive_uses_production_checker_and_bound_identity(self):
        with tempfile.TemporaryDirectory() as directory:
            root, manifest_path, manifest = self.prepare(directory)
            self.write_json(manifest_path, manifest)
            completed = self.invoke(root)
            self.assertEqual(completed.returncode, 0, completed.stderr)
            output = json.loads(completed.stdout)
            self.assertEqual(output["coreIdentity"]["profileVersion"], "qoder-v5-transcript-attestation-v2")
            self.assertEqual(output["observation"]["capabilityDigest"], self.jcs_digest(root / "capability-snapshot.json"))
            self.assertTrue(output["observation"]["workerResultTeeLast"])
            self.assertRegex(
                output["implementationDigests"]["checkerExecutionIdentity"],
                r"^sha256:[0-9a-f]{64}$",
            )
            expected_method = (
                "darwin-codesign-cdhash-full-sha256"
                if sys.platform == "darwin"
                else "linux-proc-exe-sha256"
            )
            self.assertEqual(
                output["implementationDigests"]["checkerExecutionIdentityMethod"],
                expected_method,
            )

    def test_forbidden_unbound_command_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            root, _, _ = self.prepare(directory)
            completed = self.invoke(root, "manifest-negative-undeclared-wc.json")
            self.assert_failure(completed, "forbidden-command-executed")

    def test_manifest_nan_and_infinity_rejected(self):
        for token in ("NaN", "Infinity", "-Infinity"):
            with tempfile.TemporaryDirectory() as directory:
                root, manifest_path, _ = self.prepare(directory)
                raw = manifest_path.read_text().replace('"maxBytes": 8388608', f'"maxBytes": {token}', 1)
                manifest_path.write_text(raw)
                self.assert_failure(self.invoke(root), "invalid-json")

    def test_core_records_reject_nonfinite_numbers(self):
        for label in ("taskSpec", "workerRequest", "workerResult", "capabilitySnapshot", "profile"):
            with tempfile.TemporaryDirectory() as directory:
                root, manifest_path, manifest = self.prepare(directory)
                path = root / manifest["inputs"][label]["path"]
                path.write_text(path.read_text().rstrip()[:-1] + ',"probe":NaN}\n')
                self.refresh_manifest(root, manifest)
                self.write_json(manifest_path, manifest)
                self.assert_failure(self.invoke(root), "core-contract-invalid", "closed-json-invalid")

    def test_duplicate_transcript_member_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            root, manifest_path, manifest = self.prepare(directory)
            transcript = root / manifest["inputs"]["transcript"]["path"]
            raw = transcript.read_bytes().replace(b'{"type":"system"', b'{"type":"system","type":"system"', 1)
            self.refresh_transcript(root, manifest, raw)
            self.write_json(manifest_path, manifest)
            self.assert_failure(self.invoke(root), "closed-transcript-json-invalid")

    def test_state_machine_negative_probes(self):
        mutations = {
            "unknown-event": lambda e: e.insert(-1, {"type":"future"}),
            "unknown-part": lambda e: e[1]["message"]["content"].append({"type":"future"}),
            "duplicate-init": lambda e: e.insert(1, dict(e[0])),
            "session-drift": lambda e: e[2].update({"session_id":"different"}),
            "post-terminal": lambda e: e.append({"type":"assistant","message":{"content":[{"type":"text","text":"late"}]}}),
        }
        for name, mutation in mutations.items():
            with self.subTest(name=name), tempfile.TemporaryDirectory() as directory:
                root, manifest_path, manifest = self.prepare(directory)
                self.mutate_events(root, manifest, mutation)
                self.write_json(manifest_path, manifest)
                self.assert_failure(self.invoke(root), "qoder-v5-transcript-invalid", "transcript-meta-mismatch")

    def test_tee_requires_explicit_completed_exit_zero(self):
        with tempfile.TemporaryDirectory() as directory:
            root, manifest_path, manifest = self.prepare(directory)
            self.mutate_events(root, manifest, lambda e: e[4].pop("tool_use_result"))
            self.write_json(manifest_path, manifest)
            self.assert_failure(self.invoke(root), "tee-result-not-explicit-success")

    def test_path_escape_absolute_dotdot_deny_and_undeclared(self):
        cases = (("/tmp/outside", "tool-path-escape"), ("../outside", "tool-path-noncanonical"), (".marshal/secret", "tool-path-out-of-scope"), ("other.md", "tool-path-out-of-scope"))
        for target, reason in cases:
            with self.subTest(target=target), tempfile.TemporaryDirectory() as directory:
                root, manifest_path, manifest = self.prepare(directory)
                self.mutate_events(root, manifest, lambda e, target=target: e[1]["message"]["content"][0]["input"].update({"file_path":target}))
                self.write_json(manifest_path, manifest)
                self.assert_failure(self.invoke(root), reason)

    def test_symlink_escape_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            root, manifest_path, manifest = self.prepare(directory)
            (root / "worktree" / "link").symlink_to("/tmp")
            self.mutate_events(root, manifest, lambda e: e[1]["message"]["content"][0]["input"].update({"file_path":"link/out"}))
            self.write_json(manifest_path, manifest)
            self.assert_failure(self.invoke(root), "tool-path-symlink-escape")

    def test_write_inside_scope_must_be_declared(self):
        with tempfile.TemporaryDirectory() as directory:
            root, manifest_path, manifest = self.prepare(directory)
            result_path = root / "worker-result.json"
            result = json.loads(result_path.read_text()); result["declaredChangedFiles"] = []
            self.write_json(result_path, result); self.refresh_manifest(root, manifest); self.write_json(manifest_path, manifest)
            self.assert_failure(self.invoke(root), "write-path-not-declared")

    def test_noncanonical_and_hardlinked_inputs_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            root, manifest_path, manifest = self.prepare(directory)
            manifest["inputs"]["profile"]["path"] = "./transcript-attestation-profile.json"
            self.write_json(manifest_path, manifest)
            self.assert_failure(self.invoke(root), "path-boundary-invalid")
        with tempfile.TemporaryDirectory() as directory:
            root, manifest_path, manifest = self.prepare(directory)
            task = root / "task-spec.json"
            duplicate = root / "profile-hardlink.json"; os.link(task, duplicate)
            manifest["inputs"]["profile"]["path"] = duplicate.name
            manifest["inputs"]["profile"]["sha256"] = raw_digest(duplicate)
            self.write_json(manifest_path, manifest)
            self.assert_failure(self.invoke(root), "input-path-invalid")

    def test_isolated_python_ignores_pythonpath_shadow(self):
        with tempfile.TemporaryDirectory() as directory:
            root, manifest_path, manifest = self.prepare(directory)
            self.write_json(manifest_path, manifest)
            shadow = Path(directory) / "shadow"; shadow.mkdir(); marker = Path(directory) / "marker"
            (shadow / "hashlib.py").write_text(f"open({str(marker)!r}, 'w').write('loaded')\n")
            env = dict(os.environ); env["PYTHONPATH"] = str(shadow); env["PYTHONUSERBASE"] = str(shadow)
            completed = self.invoke(root, env=env)
            self.assertEqual(completed.returncode, 0, completed.stderr)
            self.assertFalse(marker.exists())

    def test_checker_symlink_and_oversize_fail_closed(self):
        with tempfile.TemporaryDirectory() as directory:
            root, manifest_path, manifest = self.prepare(directory); self.write_json(manifest_path, manifest)
            symlink = (Path(directory) / "checker-link").resolve(strict=False)
            symlink.symlink_to(self.checker)
            self.assert_failure(self.invoke(root, checker=symlink), "checker-invalid")
            oversized = (Path(directory) / "oversized-checker").resolve()
            with oversized.open("wb") as stream:
                stream.seek(64 * 1024 * 1024)
                stream.write(b"x")
            oversized.chmod(0o700)
            self.assert_failure(self.invoke(root, checker=oversized), "checker-invalid")

    def test_checker_leaf_replace_or_growth_never_runs_unbound_bytes(self):
        for mutation in ("replace", "grow"):
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as directory:
                root, manifest_path, manifest = self.prepare(directory); self.write_json(manifest_path, manifest)
                candidate = (Path(directory) / "checker").resolve(); shutil.copy2(self.checker, candidate); candidate.chmod(0o700)
                started = threading.Event()
                def mutate():
                    started.wait()
                    try:
                        if mutation == "replace":
                            replacement = candidate.with_suffix(".replacement")
                            shutil.copy2("/usr/bin/false", replacement); replacement.chmod(0o700); os.replace(replacement, candidate)
                        else:
                            with candidate.open("ab", buffering=0) as stream: stream.write(b"growth")
                    except OSError:
                        pass
                thread = threading.Thread(target=mutate); thread.start(); started.set()
                completed = self.invoke(root, checker=candidate); thread.join()
                if completed.returncode == 0:
                    self.assertEqual(json.loads(completed.stdout)["reasonCode"], "transcript-attestation-pass")
                else:
                    reason = json.loads(completed.stderr)["reasonCode"]
                    self.assertIn(reason, {"checker-invalid", "checker-changed-during-read", "checker-changed-during-execution", "checker-execution-failed", "invalid-json"})

    def test_private_checker_replacement_before_spawn_gets_no_evidence(self):
        with tempfile.TemporaryDirectory() as directory:
            root, _, manifest = self.prepare(directory)
            marker = Path(directory) / "evidence-reached-replacement"

            def replace_before_spawn(copied_path: Path) -> None:
                replacement = copied_path.with_name("replacement")
                replacement.write_text(
                    "#!/bin/sh\nIFS= read -r payload\nprintf received > "
                    + repr(str(marker))
                    + "\n"
                )
                replacement.chmod(0o700)
                os.replace(replacement, copied_path)

            with self.assertRaises(self.validator_module.PreflightError) as raised:
                self.validator_module.invoke_core_checker(
                    self.checker,
                    manifest["subject"],
                    self.direct_checker_inputs(root, manifest),
                    before_spawn=replace_before_spawn,
                )
            self.assertEqual(raised.exception.reason_code, "checker-process-identity-mismatch")
            self.assertFalse(marker.exists(), "evidence must not be sent before process-image attestation")

    def test_private_checker_mutation_after_spawn_is_rejected_before_evidence(self):
        for mutation in ("replace", "grow", "symlink"):
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as directory:
                root, _, manifest = self.prepare(directory)

                def mutate_after_spawn(copied_path: Path, _process) -> None:
                    if mutation == "grow":
                        with copied_path.open("ab", buffering=0) as stream:
                            stream.write(b"growth")
                        return
                    replacement = copied_path.with_name("replacement")
                    if mutation == "symlink":
                        replacement.symlink_to("/bin/cat")
                    else:
                        shutil.copyfile("/bin/cat", replacement)
                        replacement.chmod(0o700)
                    os.replace(replacement, copied_path)

                with self.assertRaises(self.validator_module.PreflightError) as raised:
                    self.validator_module.invoke_core_checker(
                        self.checker,
                        manifest["subject"],
                        self.direct_checker_inputs(root, manifest),
                        after_spawn=mutate_after_spawn,
                    )
                self.assertEqual(
                    raised.exception.reason_code,
                    "checker-private-path-changed",
                    str(raised.exception),
                )

    def test_python_bridge_contains_no_tool_or_event_semantic_parser(self):
        source = VALIDATOR.read_text()
        for forbidden in ("def parse_jsonl", "def validate_transcript", "tool_use", "tool_result", "TEE_FIRST_LINE", "TASK_TOOL_NAMES"):
            self.assertNotIn(forbidden, source)
        for required in ("O_NOFOLLOW", "O_EXCL", "CHECKER_MAX_BYTES", "TemporaryDirectory", 'python3'):
            if required != "python3":
                self.assertIn(required, source)
        for required in ("codesign_identity", "actual_checker_execution_identity", "checkerExecutionIdentity"):
            self.assertIn(required, source)

    def test_real_mac_r3_receipt_is_current_and_sanitized(self):
        receipt_path = FIXTURES / "mac-qoder-v5-conformance-r3-receipt.json"
        receipt = json.loads(receipt_path.read_text())
        self.assertEqual(receipt["status"], "pass")
        self.assertEqual(receipt["reasonCode"], "transcript-attestation-pass")
        self.assertEqual(receipt["attestationDigest"], "sha256:06a857f95f47c4bcf3b09c7bfc4b4f098bdbb09d998cc6bcf1f83592c924273e")
        self.assertEqual(
            receipt["implementationDigests"]["checkerExecutionIdentityMethod"],
            "darwin-codesign-cdhash-full-sha256",
        )
        serialized = json.dumps(receipt, ensure_ascii=False)
        for secret_or_free_text in ("/Users/", '"prompt"', '"message"', '"description"'):
            self.assertNotIn(secret_or_free_text, serialized)

    def test_core_contracts_and_draft_schema(self):
        for schema_name, filename in (("task-spec","task-spec.json"),("worker-request","worker-request.json"),("worker-result","worker-result.json"),("capability-snapshot","capability-snapshot.json")):
            completed = subprocess.run([str(self.marshal), "contract", "validate", "--schema", schema_name, str(FIXTURES / filename)], cwd=REPOSITORY_ROOT, capture_output=True, text=True)
            self.assertEqual(completed.returncode, 0, completed.stderr)
        completed = subprocess.run(["go","run",str(SCHEMA_PROBE),str(SCHEMA),str(TEMPLATE),str(FIXTURES / "manifest-positive.json"),str(FIXTURES / "manifest-negative-undeclared-wc.json")],cwd=REPOSITORY_ROOT,capture_output=True,text=True)
        self.assertEqual(completed.returncode, 0, completed.stderr)


if __name__ == "__main__":
    unittest.main()
