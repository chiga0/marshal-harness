#!/usr/bin/env python3
"""Bounded offline metadata/ZIP tests plus the unchanged installed Node team.

Metadata below is a deterministic transport fixture, not GitHub or model proof.
The CLI has no switch accepting caller-produced metadata as GitHub authority.
"""
import copy
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import shutil
import stat
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch
import warnings
import zipfile

ROOT = Path(__file__).resolve().parent.parent
spec = importlib.util.spec_from_file_location("node_candidate_admit", ROOT / "scripts/node-candidate-admit.py")
candidate = importlib.util.module_from_spec(spec)
spec.loader.exec_module(candidate)
NODE = os.environ.get("MARSHAL_NODE", shutil.which("node"))
HEAD = "a" * 40


def expected(head=HEAD, archive="sha256:" + "b" * 64, manifest="sha256:" + "c" * 64):
    return candidate.options(head, "101", "1", "202", archive, manifest)


class FixtureGitHub:
    """Exact API shape, injectable only into Python tests; never a CLI option."""
    def __init__(self, binding, raw=b""):
        self.binding, self.archive = binding, raw
        self.queries = []
        self.transform = lambda endpoint, value: value

    def get(self, endpoint):
        self.queries.append(endpoint)
        b = self.binding
        run = {"id": b["runId"], "run_attempt": b["attempt"], "workflow_id": 99, "path": candidate.WORKFLOW,
               "head_sha": b["sourceHead"], "head_branch": "main", "event": "push", "status": "completed", "conclusion": "success",
               "repository": {"id": 88, "full_name": candidate.REPOSITORY},
               "head_repository": {"id": 88, "full_name": candidate.REPOSITORY}}
        if endpoint.endswith("/actions/workflows/node-team.yml"):
            value = {"id": 99, "path": candidate.WORKFLOW}
        elif "/jobs?" in endpoint:
            value = {"total_count": len(candidate.JOBS), "jobs": [] if endpoint.endswith("page=2") else [
                {"id": 501 + i, "run_id": b["runId"], "run_attempt": b["attempt"], "head_sha": b["sourceHead"],
                 "name": name, "status": "completed", "conclusion": "success"} for i, name in enumerate(sorted(candidate.JOBS))]}
        elif "/actions/artifacts/" in endpoint:
            value = {"id": b["artifactId"], "name": f'node-candidate-{b["sourceHead"]}-{b["attempt"]}', "expired": False,
                     "digest": b["archiveDigest"], "size_in_bytes": len(self.archive) or 100,
                     "workflow_run": {"id": b["runId"], "head_sha": b["sourceHead"], "head_branch": "main", "repository_id": 88, "head_repository_id": 88}}
        else:
            value = run
        return self.transform(endpoint, copy.deepcopy(value))

    def raw(self, endpoint, maximum):
        self.queries.append(endpoint)
        return self.archive


def zip_bytes(contents, *, additions=(), mode=stat.S_IFREG | 0o644, extra=b"", compression=zipfile.ZIP_STORED):
    target = io.BytesIO()
    with warnings.catch_warnings():
        warnings.simplefilter("ignore", UserWarning)
        with zipfile.ZipFile(target, "w") as archive:
            for name, value in list(contents.items()) + list(additions):
                entry = zipfile.ZipInfo(name)
                entry.create_system = 3
                entry.external_attr = mode << 16
                entry.compress_type = compression
                entry.extra = extra
                archive.writestr(entry, value)
    return target.getvalue()


def package_fixture():
    files = ["packages/example/index.mjs"]
    contents = {files[0]: b"export const value = 1;\n"}
    manifest = {"format": "marshal-node-script-package/v1", "sourceHead": HEAD, "node": "24.15.0",
                "platforms": ["darwin-arm64", "linux-x64"], "entrypoint": "packages/task-service/main.mjs",
                "files": [{"path": name, "digest": candidate.sha(value), "bytes": len(value)} for name, value in contents.items()]}
    contents["manifest.json"] = (json.dumps(manifest, indent=2) + "\n").encode()
    return files, contents


class MetadataTest(unittest.TestCase):
    def test_complete_exact_attempt_and_tail_page(self):
        api = FixtureGitHub(expected())
        result = candidate.github_snapshot(api, expected())
        self.assertEqual(result["jobs"], [501, 502, 503, 504, 505])
        self.assertTrue(any(value.endswith("page=2") for value in api.queries))
        self.assertEqual(sum(value.endswith("/runs/101") for value in api.queries), 2)

    def test_wrong_run_source_repository_workflow_and_attempt_rejected(self):
        for field, wrong in {"id": 102, "run_attempt": 2, "workflow_id": 98, "head_sha": "d" * 40,
                             "event": "pull_request", "head_branch": "feature", "conclusion": "failure",
                             "path": ".github/workflows/other.yml", "head_repository": {"id": 77, "full_name": "other/repo"}}.items():
            with self.subTest(field=field):
                api = FixtureGitHub(expected())
                api.transform = lambda endpoint, value: dict(value, **{field: wrong}) if endpoint.endswith("/runs/101") else value
                with self.assertRaises(candidate.CandidateError):
                    candidate.github_snapshot(api, expected())

    def test_jobs_truncation_tail_duplicate_and_late_failure_rejected(self):
        for mode in ("short", "count", "tail", "duplicate", "failure", "wrong_attempt"):
            with self.subTest(mode=mode):
                api = FixtureGitHub(expected())
                def mutate(endpoint, value):
                    if "/jobs?" not in endpoint:
                        return value
                    if mode == "tail" and endpoint.endswith("page=2"):
                        value["jobs"] = [{}]
                    if endpoint.endswith("page=1"):
                        if mode == "short": value["jobs"].pop()
                        if mode == "count": value["total_count"] = 6
                        if mode == "duplicate": value["jobs"][1] = value["jobs"][0]
                        if mode == "failure": value["jobs"][-1]["conclusion"] = "failure"
                        if mode == "wrong_attempt": value["jobs"][-1]["run_attempt"] = 2
                    return value
                api.transform = mutate
                with self.assertRaises(candidate.CandidateError):
                    candidate.github_snapshot(api, expected())

    def test_artifact_digest_run_attempt_expiry_and_size_rejected(self):
        for field, wrong in {"id": 203, "expired": True, "digest": "sha256:" + "d" * 64,
                             "name": "node-candidate-" + HEAD + "-2", "size_in_bytes": candidate.MAX_ARCHIVE + 1,
                             "workflow_run": {"id": 404, "head_sha": HEAD}}.items():
            with self.subTest(field=field):
                api = FixtureGitHub(expected())
                api.transform = lambda endpoint, value: dict(value, **{field: wrong}) if "/actions/artifacts/" in endpoint else value
                with self.assertRaises(candidate.CandidateError):
                    candidate.github_snapshot(api, expected())

    def test_rerun_after_jobs_rejects_snapshot(self):
        api = FixtureGitHub(expected())
        count = 0
        def mutate(endpoint, value):
            nonlocal count
            if endpoint.endswith("/runs/101"):
                count += 1
                if count == 2: value["run_attempt"] = 2
            return value
        api.transform = mutate
        with self.assertRaisesRegex(candidate.CandidateError, "run_binding_mismatch"):
            candidate.github_snapshot(api, expected())

    def test_input_and_json_bounds(self):
        for value in (b'{"id":1,"id":2}', b'{"value":NaN}', b'\xff', b'\xef\xbb\xbf{}', b'{}' * 1000000):
            with self.assertRaises(candidate.CandidateError): candidate.parse(value)
        for value in ("0", "01", "-1", "1e2", "9007199254740992"):
            with self.assertRaises(candidate.CandidateError): candidate.options(HEAD, value, "1", "2", "sha256:" + "b" * 64, "sha256:" + "c" * 64)

    def test_unbound_source_never_imports_inventory(self):
        api = FixtureGitHub(expected())
        api.transform = lambda endpoint, value: dict(value, head_sha="d" * 40) if endpoint.endswith("/runs/101") else value
        with patch.object(candidate, "trusted_source") as inventory, patch.object(candidate, "PrivateOutput") as output:
            with self.assertRaisesRegex(candidate.CandidateError, "run_binding_mismatch"):
                candidate.admit(expected(), api=api, source="unused", node=NODE, target="unused")
            inventory.assert_not_called()
            output.assert_not_called()


class ArchiveTest(unittest.TestCase):
    def setUp(self):
        self.files, self.contents = package_fixture()

    def validate(self, raw, manifest_digest=None):
        return candidate.validate_archive(raw, expected(archive=candidate.sha(raw), manifest=manifest_digest or candidate.sha(self.contents["manifest.json"])), self.files)

    def test_original_bytes_with_transport_permissions_and_deflate(self):
        for mode in (0o100600, 0o100644, 0o100755):
            self.assertEqual(self.validate(zip_bytes(self.contents, mode=mode, compression=zipfile.ZIP_DEFLATED)), self.contents)

    def test_archive_and_manifest_pins_separate(self):
        raw = zip_bytes(self.contents)
        with self.assertRaisesRegex(candidate.CandidateError, "archive_digest_mismatch"):
            candidate.validate_archive(raw, expected(), self.files)
        with self.assertRaisesRegex(candidate.CandidateError, "manifest_digest_mismatch"):
            self.validate(raw, "sha256:" + "d" * 64)

    def test_missing_extra_duplicate_and_traversal(self):
        cases = [zip_bytes({"manifest.json": self.contents["manifest.json"]}),
                 zip_bytes(self.contents, additions=[("../escape", b"x")]),
                 zip_bytes(self.contents, additions=[(self.files[0], b"x")]),
                 zip_bytes({"manifest.json": self.contents["manifest.json"], "packages/../escape": b"x"})]
        for raw in cases:
            with self.assertRaises(candidate.CandidateError): self.validate(raw)

    def test_links_fifo_directory_unknown_extra_and_zip64_rejected(self):
        for kind in (stat.S_IFLNK, stat.S_IFIFO, stat.S_IFDIR, stat.S_IFSOCK):
            with self.subTest(kind=kind), self.assertRaises(candidate.CandidateError):
                self.validate(zip_bytes(self.contents, mode=kind | 0o644))
        with self.assertRaises(candidate.CandidateError): self.validate(zip_bytes(self.contents, extra=b'\x01\x00\x00\x00'))
        for suffix in (b"trailer", b"\x00"):
            with self.assertRaises(candidate.CandidateError): self.validate(zip_bytes(self.contents) + suffix)

    def test_exact_names_nul_case_alias_and_broken_crc_rejected(self):
        for wrong in ("PACKAGES/example/index.mjs", "/packages/example/index.mjs", "packages\\example\\index.mjs"):
            contents = {wrong: self.contents[self.files[0]], "manifest.json": self.contents["manifest.json"]}
            with self.assertRaises(candidate.CandidateError): self.validate(zip_bytes(contents))
        raw = bytearray(zip_bytes(self.contents))
        pos = raw.index(self.contents[self.files[0]])
        raw[pos] ^= 1
        with self.assertRaises(candidate.CandidateError): self.validate(bytes(raw))
        raw = zip_bytes(self.contents).replace(b"packages/example/index.mjs", b"packages\x00example/index.mjs")
        with self.assertRaises(candidate.CandidateError): self.validate(raw)

    def test_size_limits_and_manifest_file_drift(self):
        contents = dict(self.contents, **{self.files[0]: b"x" * (candidate.MAX_FILE + 1)})
        with self.assertRaises(candidate.CandidateError): self.validate(zip_bytes(contents, compression=zipfile.ZIP_DEFLATED))
        contents[self.files[0]] = b"different bytes"
        with self.assertRaisesRegex(candidate.CandidateError, "manifest_file_mismatch"): self.validate(zip_bytes(contents))

    def test_no_output_before_complete_zip_validation(self):
        with tempfile.TemporaryDirectory() as temp:
            target = str(Path(temp).resolve() / "new")
            api = FixtureGitHub(expected(), b"invalid")
            with patch.object(candidate, "trusted_source", return_value=self.files):
                with self.assertRaises(candidate.CandidateError):
                    candidate.admit(expected(), api=api, source="unused", node=NODE, target=target)
            self.assertFalse(os.path.exists(target))


class FilesAndConsumerTest(unittest.TestCase):
    def test_exclusive_private_target_and_child_parent_sync_failure_preserved(self):
        with tempfile.TemporaryDirectory() as temp:
            parent = Path(temp).resolve()
            target = parent / "target"
            output = candidate.PrivateOutput(str(target))
            output.write(str(target / "evidence"), b"x")
            self.assertEqual(stat.S_IMODE((target / "evidence").stat().st_mode), 0o600)
            try:
                with patch.object(candidate.os, "fsync", side_effect=OSError("fixture")):
                    with self.assertRaises(OSError): output.sync()
            finally: output.close()
            self.assertEqual((target / "evidence").read_bytes(), b"x")
            with self.assertRaises(FileExistsError): candidate.PrivateOutput(str(target))
            os.chmod(parent, 0o755)
            with self.assertRaises(candidate.CandidateError): candidate.PrivateOutput(str(parent / "other"))

    def test_fifo_hardlink_and_replaced_parent_rejected(self):
        with tempfile.TemporaryDirectory() as temp:
            parent = Path(temp).resolve()
            os.mkfifo(parent / "fifo")
            with self.assertRaises(candidate.CandidateError): candidate.read_regular(str(parent / "fifo"), 100)
            (parent / "regular").write_bytes(b"x")
            os.link(parent / "regular", parent / "link")
            with self.assertRaises(candidate.CandidateError): candidate.read_regular(str(parent / "regular"), 100)
            output = candidate.PrivateOutput(str(parent / "new"))
            try:
                (parent / "new").rename(parent / "old")
                (parent / "new").mkdir(mode=0o700)
                with self.assertRaises(candidate.CandidateError): output.sync()
            finally: output.close()

    def test_consumer_summary_cannot_replace_missing_tests_or_fake_binding(self):
        e = expected()
        observation = {"sourceHead": HEAD, "manifestDigest": e["manifestDigest"], "node": "24.15.0", "platform": "darwin", "arch": "arm64", "uid": 501,
                       "layout": 1, "originalExecutions": 4, "attempts": 4, "deliveryDigest": "sha256:" + "d" * 64, "coldReplayDuplicateStarts": 0}
        lines = ["# tests 2", "# pass 2", "# fail 0", "# cancelled 0", "# skipped 0"]
        for layout in (1, 2):
            lines += ["# " + json.dumps(dict(observation, layout=layout)), "# " + json.dumps({"artifactId": "202", "source": "current-workflow-artifact", "modelCalls": 0})]
        raw = "\n".join(lines).encode()
        self.assertEqual(len(candidate.consumer_result(raw, e)), 2)
        for bad in (raw.replace(b"# pass 2", b"# pass 1"), raw.replace(b'"attempts": 4', b'"attempts": 3'),
                    raw.replace(b'"modelCalls": 0', b'"modelCalls": 1'), raw.replace(b'"artifactId": "202"', b'"artifactId": "203"')):
            with self.assertRaises(candidate.CandidateError): candidate.consumer_result(bad, e)

    def test_bounded_process_error_keeps_original_output_not_stderr(self):
        with self.assertRaises(candidate.CandidateError) as failure:
            candidate.command([NODE, "-e", "process.stdout.write('public failure');process.stderr.write('never echo');process.exitCode=1"])
        self.assertEqual(failure.exception.output, b"public failure")
        with self.assertRaisesRegex(candidate.CandidateError, "command_output_limit"):
            candidate.command([NODE, "-e", "process.stdout.write('x'.repeat(1024))"], maximum=100)
        with self.assertRaisesRegex(candidate.CandidateError, "command_timeout"):
            candidate.command([NODE, "-e", "setInterval(()=>{},100)"], timeout=0.1, owned_group=True)

    def test_original_node_pack_admission_restore_cli_team_and_cold_open(self):
        self.assertGreaterEqual(int(subprocess.check_output([NODE, "--version"]).strip().split(b".")[0][1:]), 22)
        parent = Path(tempfile.mkdtemp(prefix="node-candidate-full.")).resolve()
        passed = False
        try:
            head = subprocess.check_output(["git", "-C", str(ROOT), "rev-parse", "HEAD"], text=True).strip()
            result = json.loads(subprocess.check_output([NODE, str(ROOT / "packages/task-distribution/main.mjs"), "pack", "--source", str(ROOT),
                "--source-head", head, "--target", str(parent / "original")]))
            names = [item["path"] for item in json.loads((parent / "original/manifest.json").read_text())["files"]]
            contents = {name: (parent / "original" / name).read_bytes() for name in names}
            contents["manifest.json"] = (parent / "original/manifest.json").read_bytes()
            raw = zip_bytes(contents)
            binding = expected(head, candidate.sha(raw), result["manifestDigest"])
            api = FixtureGitHub(binding, raw)
            result = candidate.admit(binding, api=api, source=str(ROOT), node=NODE, target=str(parent / "admitted"))
            self.assertTrue(result["candidateVerified"])
            self.assertNotIn("stableApproved", result)
            self.assertEqual(result["consumer"]["passed"], 2)
            self.assertEqual(result["consumer"]["modelCalls"], 0)
            self.assertEqual([item["layout"] for item in result["consumer"]["observations"]], [1, 2])
            self.assertEqual((parent / "admitted/candidate.zip").read_bytes(), raw)
            self.assertEqual((parent / "admitted/installed/manifest.json").read_bytes(), contents["manifest.json"])
            self.assertGreaterEqual(sum(endpoint.endswith("page=2") for endpoint in api.queries), 3)
            passed = True
        finally:
            if passed:
                shutil.rmtree(parent)
            else:
                print("failed fixture evidence retained: " + str(parent), file=sys.stderr)


if __name__ == "__main__":
    unittest.main()
